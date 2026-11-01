package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// 監査のサムネイル（FR-AU02、#71）の Control への結線。
//
// 操作が成功（result=ok）し、画面を変える種類なら、操作の直後に非同期で 1 枚撮り、その操作の監査エントリに付ける。
// 撮影・符号化・保存の失敗は運用ログに残すだけで、操作の結果も遅延も変えない。
// 監査エントリは撮影のあとに（撮影用の goroutine から）記録する。撮れなかったときは画像なしで同じエントリを記録する。
// 撮影を待つのは goroutine 側だけで、Do・Launch の呼び出し元は待たない。
const (
	// thumbWorkers は同時に撮影できる数の上限。満杯のときはサムネイルだけ捨てる（エントリは同期で記録する）。
	thumbWorkers = 2
	// thumbCaptureTimeout は 1 枚の撮影（モニター一覧・Grab・符号化）の上限。超えたら画像なしでエントリを記録する。
	thumbCaptureTimeout = 10 * time.Second
	// webThumbInterval は Web 入力のサムネイルの最小の間隔。全 viewer を通して 1 秒に 1 枚まで。
	webThumbInterval = time.Second
)

// thumbAuditor は、サムネイル付きで記録できる監査の窓口（*Audit が満たす）。
// ControlConfig.Audit がこれを満たすときだけ、サムネイルを撮る。
type thumbAuditor interface {
	AuditRecorder
	ThumbnailsEnabled(ctx context.Context) bool
	RecordWithThumb(ctx context.Context, e repository.AuditEntry, t Thumbnail)
}

// thumbTarget は撮るモニターの決め方。
type thumbTarget struct {
	// monitor は操作が明示したモニターの Index。domain.MonitorPrimary なら、at を含むモニター、at も無ければプライマリ。
	monitor int
	// at は仮想デスクトップの物理座標（nil なら座標なし）。
	at *domain.Point
	// web は Web 入力か（1 秒 1 枚の制限を受ける）。
	web bool
}

// opChangesScreen は op が画面を変える操作か（サムネイルを撮る対象か）を返す。サムネイルの対象はここで決める。
//
//   - 撮る: クリック・ドラッグ・スクロール・キー・文字入力・ウィンドウの前面化（MCP）、Web のボタン・ホイール・キー・文字入力。
//   - 撮らない: カーソルの移動だけ（MouseMove・PointerMove。クリックを伴わない移動は画面を変えず、Web では連続して来る）、
//     クリップボード（読み書きとも画面は変わらない）、UI ツリー・待ち。
//
// launch_app は Do を通らない（Launch）ので、ここではなく Launch が常に撮る。
func opChangesScreen(op domain.Op) bool {
	switch op.(type) {
	case domain.Click, domain.Drag, domain.Scroll, domain.KeyPress, domain.TypeText, domain.FocusWindow,
		domain.MouseButton, domain.Wheel, domain.KeyEvent:
		return true
	}
	return false
}

// thumbTargetOf は撮る対象の op（opChangesScreen が true）の撮影先を決める。op は Control が物理座標へ変換した後のもの。
// Web 入力はすべてプライマリモニター。MCP は、モニターを明示した操作ならそのモニター、座標のある操作ならその座標を含むモニター、
// どちらも無ければプライマリ。
func thumbTargetOf(who domain.Actor, op domain.Op) thumbTarget {
	t := thumbTarget{monitor: domain.MonitorPrimary, web: who.Kind == domain.ActorWeb}
	if t.web {
		return t
	}
	switch o := op.(type) {
	case domain.Click:
		t.monitor, t.at = o.Monitor, o.At
	case domain.Drag:
		t.monitor, t.at = o.Monitor, &o.To
	case domain.Scroll:
		t.monitor, t.at = o.Monitor, &o.At
	}
	return t
}

// pickThumbMonitor は撮るモニターの Index を返す。明示した Index が一覧に無い・プライマリが無いときは false。
func pickThumbMonitor(mons []domain.Monitor, t thumbTarget) (int, bool) {
	if t.monitor != domain.MonitorPrimary {
		for _, m := range mons {
			if m.Index == t.monitor {
				return m.Index, true
			}
		}
		return 0, false
	}
	if t.at != nil {
		if m, ok := domain.MonitorAt(mons, *t.at); ok {
			return m.Index, true
		}
	}
	for _, m := range mons {
		if m.Primary {
			return m.Index, true
		}
	}
	return 0, false
}

// thumbShooter は撮影の同時数の上限と、Web 入力の間隔の制限を持つ。
type thumbShooter struct {
	audit   thumbAuditor
	capture platform.Capturer
	clock   domain.Clock
	log     *slog.Logger
	sem     chan struct{}

	inflight sync.WaitGroup // 撮影中の goroutine（テストが完了を待つ。wait）

	mu      sync.Mutex
	lastWeb time.Time // 最後に Web 入力のために撮った時刻
}

// wait は、始まっている撮影（と、そのあとの記録の呼び出し）がすべて終わるまで待つ。テスト用。
func (s *thumbShooter) wait() { s.inflight.Wait() }

// allowWeb は Web 入力のサムネイルを今撮ってよいか（前回から webThumbInterval 以上空いたか）を返し、撮るなら時刻を記録する。
func (s *thumbShooter) allowWeb() bool {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lastWeb.IsZero() && now.Sub(s.lastWeb) < webThumbInterval {
		return false
	}
	s.lastWeb = now
	return true
}

// record は e を記録する。t が非 nil で、撮ってよい状況（サムネイルが有効・同時数・Web の間隔）なら、撮影を goroutine に任せて
// 呼び出しから戻り、エントリは撮影のあとに記録する。そうでなければ今すぐ記録する。
func (s *thumbShooter) record(ctx context.Context, e repository.AuditEntry, t *thumbTarget) {
	if t == nil || e.Result != auditOK || !s.audit.ThumbnailsEnabled(ctx) {
		s.audit.Record(ctx, e)
		return
	}
	select {
	case s.sem <- struct{}{}:
	default:
		s.log.Debug("thumbnail skipped: capture workers are busy", "event", e.Event)
		s.audit.Record(ctx, e)
		return
	}
	if t.web && !s.allowWeb() {
		<-s.sem
		s.audit.Record(ctx, e)
		return
	}
	if e.TS.IsZero() {
		e.TS = s.clock.Now() // 記録が撮影のあとになっても、エントリの時刻は操作の時刻
	}
	s.inflight.Add(1)
	go func(ctx context.Context, target thumbTarget) {
		defer s.inflight.Done()
		defer func() { <-s.sem }()
		ctx, cancel := context.WithTimeout(ctx, thumbCaptureTimeout)
		defer cancel()
		thumb, err := s.shoot(ctx, target)
		if err != nil {
			s.log.Warn("audit thumbnail capture failed", "event", e.Event, "err", err)
			s.audit.Record(ctx, e)
			return
		}
		s.audit.RecordWithThumb(ctx, e, thumb)
	}(context.WithoutCancel(ctx), *t)
}

func (s *thumbShooter) shoot(ctx context.Context, t thumbTarget) (Thumbnail, error) {
	mons, err := s.capture.Monitors(ctx)
	if err != nil {
		return Thumbnail{}, err
	}
	idx, ok := pickThumbMonitor(mons, t)
	if !ok {
		return Thumbnail{}, domain.ErrDesktopUnavailable.WithMsg("no monitor for thumbnail")
	}
	return CaptureThumbnail(ctx, s.capture, idx)
}
