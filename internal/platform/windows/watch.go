package windows

import (
	"context"
	"hash/maphash"
	"image"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// 継続キャプチャ（Capturer.Watch）の制御。DXGI → GDI の切り替え、再初期化、間引き、チャネルの寿命は
// OS に依存しないので、この（タグなしの）ファイルに置いて Linux でもテストする。画面を読む部分は frameSource の背後にある。

const (
	// errsBuffer は errs の余裕。受け手が詰まっていても撮るのを待たせない（あふれたら捨てる）。
	errsBuffer = 4

	// minInterval は fps が大きすぎるときの下限。
	minInterval = time.Millisecond
)

// frameSource は 1 台のモニターの画面の源（DXGI Desktop Duplication または GDI）。
// 1 つの goroutine だけが呼ぶ。
type frameSource interface {
	// Next は最大 wait のあいだ画面の変化を待ち、変化があれば新しい画像（呼び出し側のもの）を返す。
	// 変化が無ければ (nil, nil)。ctx が取り消されたら早めに戻る。
	Next(ctx context.Context, wait time.Duration) (*image.RGBA, error)
	// Close は源の持つ OS の資源を解放する。
	Close()
}

// watchConfig は runWatch の部品。
type watchConfig struct {
	interval time.Duration
	// openDXGI は DXGI の源を作る（再初期化にも使う）。nil なら最初から GDI だけ。失敗は errs に流さず GDI に切り替える。
	openDXGI func() (frameSource, error)
	// openGDI は GDI の源を作る。
	openGDI func() frameSource

	recoverTries int
	recoverDelay time.Duration
}

// watchInterval は fps の 1 コマあたりの間隔（下限 minInterval）。
func watchInterval(fps int) time.Duration {
	return max(time.Second/time.Duration(fps), minInterval)
}

// startWatch は runWatch を 1 本の goroutine で始め、2 つのチャネルを返す。
func startWatch(ctx context.Context, cfg watchConfig) (<-chan platform.Frame, <-chan error) {
	frames := make(chan platform.Frame)
	errs := make(chan error, errsBuffer)
	go runWatch(ctx, cfg, frames, errs)
	return frames, errs
}

// runWatch は ctx が取り消されるまで撮り続け、frames に変化したコマを流す。終わるときは源を閉じてからチャネルを閉じる。
func runWatch(ctx context.Context, cfg watchConfig, frames chan<- platform.Frame, errs chan<- error) {
	defer close(errs)
	defer close(frames)
	w := &watcher{cfg: cfg, frames: frames, errs: errs}
	w.src, w.dxgi = w.openInitial()
	defer func() { w.src.Close() }()
	w.loop(ctx)
}

type watcher struct {
	cfg    watchConfig
	frames chan<- platform.Frame
	errs   chan<- error

	src      frameSource
	dxgi     bool // src が DXGI
	seq      uint32
	lastSent time.Time
	lastErr  string
}

func (w *watcher) openInitial() (frameSource, bool) {
	if w.cfg.openDXGI != nil {
		if src, err := w.cfg.openDXGI(); err == nil {
			return src, true
		}
	}
	return w.cfg.openGDI(), false
}

func (w *watcher) loop(ctx context.Context) {
	for {
		// 流す間隔の下限（fps）を守る。GDI は変化が無ければ Next の中で待つので、ここは直前に流してからの間隔だけ見る。
		if !w.waitSince(ctx, w.lastSent) {
			return
		}
		img, err := w.src.Next(ctx, w.cfg.interval)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			if !w.handleError(ctx, err) {
				return
			}
		case img != nil:
			w.lastErr = ""
			if !w.send(ctx, img) {
				return
			}
		default:
			w.lastErr = ""
		}
	}
}

// handleError は源のエラーを扱う。DXGI なら作り直し、できなければ GDI に切り替える。GDI なら errs に流して 1 間隔だけ待つ。
// ctx が取り消されたら false。
func (w *watcher) handleError(ctx context.Context, err error) bool {
	if !w.dxgi {
		w.sendErr(err)
		return sleep(ctx, w.cfg.interval)
	}
	w.src.Close()
	for range w.cfg.recoverTries {
		if !sleep(ctx, w.cfg.recoverDelay) {
			w.src = closedSource{}
			return false
		}
		if src, openErr := w.cfg.openDXGI(); openErr == nil {
			w.src = src
			return true
		}
	}
	w.sendErr(domain.ErrDesktopUnavailable.WithMsg("DXGI capture was lost and could not be restored; falling back to GDI"))
	w.src, w.dxgi = w.cfg.openGDI(), false
	return true
}

// send は 1 コマを frames に流す。受け手が詰まっていれば待つ。ctx が取り消されたら false。
func (w *watcher) send(ctx context.Context, img *image.RGBA) bool {
	w.seq++
	fr := platform.Frame{Image: img, Dirty: []image.Rectangle{img.Bounds()}, Seq: w.seq}
	select {
	case w.frames <- fr:
		w.lastSent = time.Now()
		return true
	case <-ctx.Done():
		return false
	}
}

// sendErr は err を errs に流す。詰まっていれば捨て、直前と同じ文面なら流さない。
func (w *watcher) sendErr(err error) {
	msg := err.Error()
	if msg == w.lastErr {
		return
	}
	w.lastErr = msg
	select {
	case w.errs <- err:
	default:
	}
}

// waitSince は since から interval が経つまで待つ。since がゼロ（まだ流していない）なら待たない。ctx が取り消されたら false。
func (w *watcher) waitSince(ctx context.Context, since time.Time) bool {
	if since.IsZero() {
		return ctx.Err() == nil
	}
	return sleep(ctx, w.cfg.interval-time.Since(since))
}

// sleep は d のあいだ待つ。ctx が取り消されたら false。d が 0 以下なら待たずに ctx だけ見る。
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// closedSource は Close 済みの源の置き場（二重に Close しない）。
type closedSource struct{}

func (closedSource) Next(context.Context, time.Duration) (*image.RGBA, error) { return nil, nil }
func (closedSource) Close()                                                   {}

// gdiSource は grab（GDI の 1 枚撮り）を繰り返し、前のコマと違うときだけ画像を返す。
// GDI は変化を教えてくれないので、画面の中身のハッシュを前のコマと比べる（画像を取っておかない: 受け手が書き換えても影響しない）。
type gdiSource struct {
	grab func(ctx context.Context) (*image.RGBA, error)
	seed maphash.Seed

	have     bool
	prevRect image.Rectangle
	prevHash uint64
}

func newGDISource(grab func(ctx context.Context) (*image.RGBA, error)) *gdiSource {
	return &gdiSource{grab: grab, seed: maphash.MakeSeed()}
}

// Next は 1 枚撮り、前のコマと違えばその画像を返す。同じなら wait のあいだ待って (nil, nil) を返す。
func (g *gdiSource) Next(ctx context.Context, wait time.Duration) (*image.RGBA, error) {
	img, err := g.grab(ctx)
	if err != nil {
		return nil, err
	}
	hash := maphash.Bytes(g.seed, img.Pix)
	if g.have && img.Rect == g.prevRect && hash == g.prevHash {
		sleep(ctx, wait)
		return nil, nil
	}
	g.have, g.prevRect, g.prevHash = true, img.Rect, hash
	return img, nil
}

func (*gdiSource) Close() {}
