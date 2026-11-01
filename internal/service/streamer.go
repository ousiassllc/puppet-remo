package service

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは画面配信の Streamer（docs/components/overview.md §5.7、docs/api/endpoints.md §5.4）。
// フレームの符号化と差分の検出は streamer_frame.go。

const (
	// MaxViewers は同時閲覧者の上限（NFR-C03）。
	MaxViewers = 5
	// DefaultKeyframeInterval はキーフレームの既定の間隔。
	DefaultKeyframeInterval = 5 * time.Second

	// viewerQueueSize は閲覧者ごとの送信チャネルの容量。
	viewerQueueSize = 2
)

// ErrTooManyViewers は同時閲覧者が MaxViewers に達しているときの Subscribe のエラー（WebSocket は閉じコード 4429）。
var ErrTooManyViewers = errors.New("service: too many viewers")

// ViewerConfig は閲覧者のモニター・品質・FPS（API 仕様 §4.1 の `/viewer/settings`、§5.3 の `config`）。
//
// Subscribe では Quality / FPS が 0 なら設定 viewer.default_quality / viewer.default_fps（FR-S06）を使う。
// Configure では Quality / FPS が 0 なら今の値のままにし、Monitor は常に反映する（変えないなら今のモニターを渡す）。
type ViewerConfig struct {
	Monitor int // モニターの Index
	Quality int // JPEG の品質（1〜100）
	FPS     int // 1 秒あたりの更新の上限（1〜30）
}

// Viewer は登録した閲覧者。登録の解除は Subscribe に渡した ctx の取り消しで、解除されると Frames と Done が閉じる。
type Viewer struct {
	// ID は Configure に渡す識別子（Streamer が発行する。ws の viewer_id とは別）。
	ID string
	// Frames はバイナリフレーム（API 仕様 §5.4）。容量 2 で、詰まったら古いものを捨てて次のキーフレームで復帰する。
	// 受け手は読んだ []byte を書き換えない（同じプロファイルの閲覧者で共有する）。
	Frames <-chan []byte
	// Done は閲覧者が終わったとき（ctx の取り消し、キャプチャの失敗）に閉じる。
	Done <-chan struct{}
}

// Streamer は画面配信。モニターごとに 1 つのキャプチャを共有し、閲覧者がいる間だけ動かす（FR-W09）。
type Streamer interface {
	// Subscribe は閲覧者を登録する。キャプチャは閲覧者がいる間だけ動く（FR-W09）。接続直後にキーフレームが届く。
	// 上限（MaxViewers）を超えると ErrTooManyViewers、範囲外の品質・FPS と未知のモニターは domain.ErrInvalidArgument。
	Subscribe(ctx context.Context, c ViewerConfig) (*Viewer, error)
	// Configure は閲覧者のモニター・品質・FPS を変える。変えたあとはキーフレームから始まる。
	// 未知の id と範囲外の値は domain.ErrInvalidArgument（何も変えない）。
	Configure(id string, c ViewerConfig) error
}

// StreamerConfig は NewStreamer の設定。Capturer・Clock・IDs は必須。
type StreamerConfig struct {
	Capturer platform.Capturer
	Clock    domain.Clock
	IDs      domain.IDGen
	// Settings は既定の品質・FPS（viewer.default_*）の読み出し元。nil なら組み込みの既定値（70 / 10）。
	Settings repository.Settings
	// KeyframeInterval はキーフレームの間隔。0 以下なら DefaultKeyframeInterval。
	KeyframeInterval time.Duration
	Logger           *slog.Logger // nil なら slog.Default()
}

// NewStreamer は Streamer を作る。
func NewStreamer(cfg StreamerConfig) (Streamer, error) {
	if cfg.Capturer == nil || cfg.Clock == nil || cfg.IDs == nil {
		return nil, errors.New("service: streamer requires Capturer, Clock and IDs")
	}
	if cfg.KeyframeInterval <= 0 {
		cfg.KeyframeInterval = DefaultKeyframeInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &streamer{
		cfg:      cfg,
		viewers:  make(map[string]*viewerState),
		monitors: make(map[int]*monitorStream),
		stopping: make(map[int]chan struct{}),
	}, nil
}

// streamer は閲覧者の一覧とモニターごとのキャプチャの所有者。mu は viewers・monitors・stopping と viewerState を守る。
// 画像・プロファイル（monitorStream の img・profiles）は、そのモニターの goroutine だけが触る。
type streamer struct {
	cfg StreamerConfig

	mu       sync.Mutex
	viewers  map[string]*viewerState
	monitors map[int]*monitorStream
	// stopping は止めている最中のモニターの終了通知。同じモニターのキャプチャを重ねて開始しないよう、開始側はこれを待つ。
	stopping map[int]chan struct{}
}

// viewerState は閲覧者 1 人の状態（mu が守る）。
type viewerState struct {
	id      string
	cfg     ViewerConfig
	out     chan []byte
	done    chan struct{}
	stop    func() bool // ctx の取り消しの登録の解除
	ended   bool
	needKey bool // 次の送信をキーフレームにする（接続直後、設定の変更、フレームを捨てたあと）
	fresh   bool // まだ 1 度も送っていない（FPS の間引きを待たずに最初のキーフレームを送る）
}

func (v *viewerState) profileKey() profileKey {
	return profileKey{quality: v.cfg.Quality, fps: v.cfg.FPS}
}

// profileKey は 1 回だけエンコードする単位（モニターは monitorStream が持つ）。
type profileKey struct{ quality, fps int }

// target は 1 回の配信の時点の閲覧者の状態の写し。
type target struct {
	v       *viewerState
	needKey bool
	fresh   bool
}

func (s *streamer) Subscribe(ctx context.Context, c ViewerConfig) (*Viewer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := s.withDefaults(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := s.lockStarted(ctx, c.Monitor); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if len(s.viewers) >= MaxViewers {
		return nil, ErrTooManyViewers
	}
	if _, ok := s.monitors[c.Monitor]; !ok {
		if err := s.startLocked(c.Monitor, c.FPS); err != nil {
			return nil, err
		}
	}
	v := &viewerState{
		id: s.cfg.IDs.NewID(), cfg: c, out: make(chan []byte, viewerQueueSize), done: make(chan struct{}),
		needKey: true, fresh: true,
	}
	s.viewers[v.id] = v
	v.stop = context.AfterFunc(ctx, func() { s.remove(v) })
	s.wakeLocked(c.Monitor)
	return &Viewer{ID: v.id, Frames: v.out, Done: v.done}, nil
}

func (s *streamer) Configure(id string, c ViewerConfig) error {
	var v *viewerState
	for {
		s.mu.Lock()
		var ok bool
		if v, ok = s.viewers[id]; !ok {
			s.mu.Unlock()
			return domain.ErrInvalidArgument.WithMsg("unknown viewer")
		}
		// 新しいモニターを止めている最中なら、止まるのを待ってからやり直す。
		ch, stopping := s.stopping[c.Monitor]
		if !stopping || c.Monitor == v.cfg.Monitor {
			break
		}
		s.mu.Unlock()
		<-ch
	}
	defer s.mu.Unlock()

	next := v.cfg
	next.Monitor = c.Monitor
	if c.Quality != 0 {
		next.Quality = c.Quality
	}
	if c.FPS != 0 {
		next.FPS = c.FPS
	}
	if err := validateViewerConfig(next); err != nil {
		return err
	}
	if next == v.cfg {
		return nil
	}
	prev := v.cfg.Monitor
	if _, ok := s.monitors[next.Monitor]; !ok {
		if err := s.startLocked(next.Monitor, next.FPS); err != nil {
			return err
		}
	}
	v.cfg = next
	v.needKey = true
	s.wakeLocked(next.Monitor)
	if prev != next.Monitor {
		s.releaseLocked(prev)
	}
	return nil
}

// withDefaults は 0 の品質・FPS を既定値で埋め、範囲を検査する。
func (s *streamer) withDefaults(ctx context.Context, c ViewerConfig) (ViewerConfig, error) {
	if c.Quality == 0 || c.FPS == 0 {
		quality, fps, err := ViewerDefaults(ctx, s.cfg.Settings)
		if err != nil {
			// 壊れた設定で閲覧を止めない。組み込みの既定値で続ける。
			s.cfg.Logger.Warn("viewer defaults are unreadable; using built-in defaults", "error", err)
			quality, fps = defaultViewerQuality, defaultViewerFPS
		}
		if c.Quality == 0 {
			c.Quality = quality
		}
		if c.FPS == 0 {
			c.FPS = fps
		}
	}
	return c, validateViewerConfig(c)
}

func validateViewerConfig(c ViewerConfig) error {
	switch {
	case c.Monitor < 0:
		return domain.ErrInvalidArgument.WithMsg("monitor must not be negative")
	case c.Quality < viewerMinQuality || c.Quality > viewerMaxQuality:
		return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("quality must be %d-%d", viewerMinQuality, viewerMaxQuality))
	case c.FPS < viewerMinFPS || c.FPS > viewerMaxFPS:
		return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("fps must be %d-%d", viewerMinFPS, viewerMaxFPS))
	}
	return nil
}

// lockStarted は monitor のキャプチャを止めている最中でなくなるまで待ち、s.mu を持って返す（エラーのときは持たない）。
func (s *streamer) lockStarted(ctx context.Context, monitor int) error {
	for {
		s.mu.Lock()
		ch, stopping := s.stopping[monitor]
		if !stopping {
			return nil
		}
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// remove は閲覧者を外す。ctx の取り消しから呼ばれる。
func (s *streamer) remove(v *viewerState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLocked(v)
}

// endLocked は閲覧者を終わらせる（Frames と Done を閉じる）。最後の閲覧者ならそのモニターのキャプチャを止める。
func (s *streamer) endLocked(v *viewerState) {
	if v.ended {
		return
	}
	v.ended = true
	delete(s.viewers, v.id)
	if v.stop != nil {
		v.stop()
	}
	close(v.out)
	close(v.done)
	s.releaseLocked(v.cfg.Monitor)
}

// releaseLocked は monitor の閲覧者が減ったあとの後始末。ゼロならキャプチャを止め（FR-W09）、いればその goroutine に知らせる。
func (s *streamer) releaseLocked(monitor int) {
	if s.fpsLocked(monitor) > 0 {
		s.wakeLocked(monitor)
		return
	}
	if m, ok := s.monitors[monitor]; ok {
		delete(s.monitors, monitor)
		s.stopping[monitor] = m.finished
		m.cancel()
	}
}

// fpsLocked は monitor の閲覧者の FPS の最大値（閲覧者がいなければ 0）。キャプチャはこの FPS で動かす。
func (s *streamer) fpsLocked(monitor int) int {
	fps := 0
	for _, v := range s.viewers {
		if v.cfg.Monitor == monitor {
			fps = max(fps, v.cfg.FPS)
		}
	}
	return fps
}

func (s *streamer) captureFPS(monitor int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fpsLocked(monitor)
}

func (s *streamer) wakeLocked(monitor int) {
	if m, ok := s.monitors[monitor]; ok {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

// startLocked は monitor のキャプチャを fps で始める。開始できなければ（未知のモニターなど）そのエラー。
func (s *streamer) startLocked(monitor, fps int) error {
	ctx, cancel := context.WithCancel(context.Background())
	watchCtx, watchCancel := context.WithCancel(ctx)
	frames, errs, err := s.cfg.Capturer.Watch(watchCtx, monitor, fps)
	if err != nil {
		watchCancel()
		cancel()
		return err
	}
	m := &monitorStream{
		s: s, index: monitor, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), finished: make(chan struct{}),
		profiles: make(map[profileKey]*profile),
	}
	s.monitors[monitor] = m
	go m.run(frames, errs, watchCancel, fps)
	return nil
}

// collect は monitor の閲覧者をプロファイルごとにまとめ、今の状態の写しを返す。
func (s *streamer) collect(monitor int) map[profileKey][]target {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := make(map[profileKey][]target)
	for _, v := range s.viewers {
		if v.cfg.Monitor == monitor {
			groups[v.profileKey()] = append(groups[v.profileKey()], target{v: v, needKey: v.needKey, fresh: v.fresh})
		}
	}
	return groups
}

// deliver は data を targets に送る。集めたあとに設定を変えた・終わった閲覧者には送らない。
// キーフレームを送った閲覧者は復帰する。チャネルが満杯なら最古を捨て、キーフレームでなければ次のキーフレームを待つ。
func (s *streamer) deliver(monitor int, key profileKey, targets []target, data []byte, isKey bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range targets {
		v := t.v
		if v.ended || v.cfg.Monitor != monitor || v.profileKey() != key {
			continue
		}
		select {
		case v.out <- data:
		default:
			select {
			case <-v.out:
			default:
			}
			select {
			case v.out <- data:
			default:
			}
			v.needKey = !isKey
			v.fresh = false
			continue
		}
		v.needKey = v.needKey && !isKey
		v.fresh = false
	}
}

// monitorStream はモニター 1 つのキャプチャの受信とエンコードを行う goroutine（run）。
type monitorStream struct {
	s        *streamer
	index    int
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{} // 閲覧者・設定の変化の通知（容量 1）
	finished chan struct{} // goroutine が終わったら閉じる

	// 以下は run の goroutine だけが触る。
	img      *image.RGBA // 最新のコマ（Capturer は流したあとに触らない）
	profiles map[profileKey]*profile
}

// profile は (モニター, 品質, FPS) ごとの配信の状態。エンコードはプロファイルごとに 1 回だけで、閲覧者へは同じ []byte を送る。
type profile struct {
	ref        *image.RGBA // 閲覧者が見ている画面。nil なら差分を取れない（全員キーフレーム）
	seq        uint32
	lastEncode time.Time // 最後に送った時刻（FPS の間引き）
	lastKey    time.Time // 最後にキーフレームを送った時刻
}

func (m *monitorStream) run(frames <-chan platform.Frame, errs <-chan error, watchCancel context.CancelFunc, fps int) {
	defer func() {
		watchCancel()
		for range frames { // Capturer は取り消し後にチャネルを閉じる。閉じるまで待ってから終わりにする
		}
		m.finish()
	}()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var timerC <-chan time.Time

	for {
		select {
		case <-m.ctx.Done():
			return
		case fr, ok := <-frames:
			if !ok {
				return
			}
			m.accept(fr)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			m.s.cfg.Logger.Warn("screen capture reported an error", "monitor", m.index, "error", err)
			continue
		case <-m.wake:
			if want := m.s.captureFPS(m.index); want > 0 && want != fps {
				watchCancel()
				for range frames {
				}
				watchCtx, cancel := context.WithCancel(m.ctx)
				nf, ne, err := m.s.cfg.Capturer.Watch(watchCtx, m.index, want)
				if err != nil {
					cancel()
					m.s.cfg.Logger.Error("restart screen capture failed", "monitor", m.index, "error", err)
					return
				}
				frames, errs, watchCancel, fps = nf, ne, cancel, want
			}
		case <-timerC:
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
		if deadline := m.step(); !deadline.IsZero() {
			timer.Reset(max(deadline.Sub(m.s.cfg.Clock.Now()), time.Millisecond))
			timerC = timer.C
		}
	}
}

// finish は goroutine の最後の後始末。キャプチャが自分で終わったとき（失敗）は、残った閲覧者を終わらせる。
func (m *monitorStream) finish() {
	s := m.s
	s.mu.Lock()
	if s.monitors[m.index] == m {
		delete(s.monitors, m.index)
		for _, v := range s.viewers {
			if v.cfg.Monitor == m.index {
				s.endLocked(v)
			}
		}
	}
	if s.stopping[m.index] == m.finished {
		delete(s.stopping, m.index)
	}
	s.mu.Unlock()
	m.cancel()
	close(m.finished)
}

// accept は新しいコマを受け取る。解像度が変わったら全プロファイルの基準を捨てる（次の配信は全員キーフレーム）。
func (m *monitorStream) accept(fr platform.Frame) {
	if fr.Image == nil {
		return
	}
	if m.img != nil && m.img.Bounds() != fr.Image.Bounds() {
		for _, p := range m.profiles {
			p.ref = nil
		}
	}
	m.img = fr.Image
}

// step は最新のコマを各プロファイルの閲覧者へ配る。次に step を呼ぶべき時刻（FPS の間引きの解除、キーフレームの期限）を返す。無ければゼロ値。
func (m *monitorStream) step() time.Time {
	if m.img == nil {
		return time.Time{}
	}
	groups := m.s.collect(m.index)
	for key := range m.profiles {
		if _, ok := groups[key]; !ok {
			delete(m.profiles, key)
		}
	}
	now := m.s.cfg.Clock.Now()
	var deadline time.Time
	for key, targets := range groups {
		p := m.profiles[key]
		if p == nil {
			p = &profile{}
			m.profiles[key] = p
		}
		if d := m.encode(now, key, p, targets); !d.IsZero() && (deadline.IsZero() || d.Before(deadline)) {
			deadline = d
		}
	}
	return deadline
}

// encode は 1 つのプロファイルの閲覧者へ配る。キーフレームが要る閲覧者（接続直後・設定の変更・フレームを捨てた・期限）には
// 全面を 1 タイルにしたキーフレームを、それ以外には 64×64 の格子で変わった矩形だけの差分を、それぞれ 1 回だけエンコードして送る。
// 次に呼ぶべき時刻を返す。
func (m *monitorStream) encode(now time.Time, key profileKey, p *profile, targets []target) time.Time {
	interval := m.s.cfg.KeyframeInterval
	keyDue := p.ref == nil || p.lastKey.IsZero() || !now.Before(p.lastKey.Add(interval))
	var keyTargets, diffTargets []target
	fresh := false
	for _, t := range targets {
		if keyDue || t.needKey {
			keyTargets = append(keyTargets, t)
			fresh = fresh || t.fresh
		} else {
			diffTargets = append(diffTargets, t)
		}
	}
	if len(keyTargets) == 0 && p.ref == m.img {
		return p.lastKey.Add(interval)
	}
	// FPS の間引き。最初のキーフレームだけは待たせない。
	if frameInterval := time.Second / time.Duration(key.fps); !fresh && !p.lastEncode.IsZero() && now.Sub(p.lastEncode) < frameInterval {
		return p.lastEncode.Add(frameInterval)
	}

	var rects []image.Rectangle
	if len(diffTargets) > 0 {
		rects = changedRects(p.ref, m.img)
	}
	if len(keyTargets) == 0 && len(rects) == 0 {
		p.ref = m.img // 見た目が変わらないコマ
		return p.lastKey.Add(interval)
	}
	seq := p.seq + 1
	if len(keyTargets) > 0 {
		data, err := encodeFrame(m.img, []image.Rectangle{m.img.Bounds()}, m.index, seq, true, key.quality)
		if err != nil {
			m.s.cfg.Logger.Error("encode keyframe failed", "monitor", m.index, "error", err)
			return time.Time{}
		}
		m.s.deliver(m.index, key, keyTargets, data, true)
		if keyDue {
			p.lastKey = now
		}
	}
	if len(rects) > 0 {
		data, err := encodeFrame(m.img, rects, m.index, seq, false, key.quality)
		if err != nil {
			m.s.cfg.Logger.Error("encode frame failed", "monitor", m.index, "error", err)
			return time.Time{}
		}
		m.s.deliver(m.index, key, diffTargets, data, false)
	}
	p.seq, p.ref, p.lastEncode = seq, m.img, now
	return p.lastKey.Add(interval)
}
