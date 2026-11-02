package app

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// overlayController は Bus の lease を購読して、操作中オーバーレイ（#69）を出し入れする。
//
// 振る舞い（docs/components/overview.md §7・§13.6）:
//   - リースが held になったら、全モニターにオーバーレイを出す。ラベルは「操作中: <クライアント名>（<種別>）」。保持者が変われば更新する。
//   - held でなくなったら（解放・アイドル解放・強制剥奪・キルスイッチ・切断）、または承認待ち（pending_approval）の間は隠す。
//   - モニター一覧が取れない・0 台のときはログに残すだけで、リースの取得は妨げない（オーバーレイは出さない）。
//
// Bus のイベントは捨てられうるので、イベントの中身ではなく LeaseManager.Holder の今の状態を真実の源にする。
type overlayController struct {
	windows Windows
	leases  service.LeaseManager
	capture platform.Capturer
	bus     *service.Bus
	log     *slog.Logger

	mu    sync.Mutex
	shown string // 今出しているオーバーレイのラベル（出していなければ空）
}

func newOverlayController(w Windows, leases service.LeaseManager, capture platform.Capturer, bus *service.Bus, log *slog.Logger) *overlayController {
	return &overlayController{windows: w, leases: leases, capture: capture, bus: bus, log: log}
}

// Watch は購読を（戻る前に）済ませて、リースの変化に従ってオーバーレイを出し入れする goroutine を始める。ctx の終了で、
// 出しているオーバーレイを隠してから、返したチャネルを閉じる（シャットダウンで隠す）。購読を取りこぼさないよう、起動時にも 1 度確かめる。
func (o *overlayController) Watch(ctx context.Context) <-chan struct{} {
	events, cancel := o.bus.Subscribe(service.TopicLease)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		defer o.hide()
		o.sync(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-events:
				o.sync(ctx)
			}
		}
	}()
	return done
}

// sync は今のリースの状態にオーバーレイを合わせる。呼ぶのは Watch の goroutine だけ（テストは直接呼んで順序を決定的にする）。
func (o *overlayController) sync(ctx context.Context) {
	h, held := o.leases.Holder()
	if !held {
		o.hide()
		return
	}
	label := overlayLabel(h.Actor)
	o.mu.Lock()
	same := o.shown == label
	o.mu.Unlock()
	if same {
		return // 保持者が同じなら、モニター構成の確認も作り直しもしない（構成の変化は次に出すときに反映する）
	}
	monitors, err := o.capture.Monitors(ctx)
	if err != nil || len(monitors) == 0 {
		o.log.Warn("operation overlay is not shown: cannot list monitors", "err", err, "monitors", len(monitors))
		o.hide() // 前の保持者のラベルを出したままにしない
		return
	}
	o.windows.ShowOverlay(monitors, label)
	o.mu.Lock()
	o.shown = label
	o.mu.Unlock()
}

func (o *overlayController) hide() {
	o.mu.Lock()
	was := o.shown != ""
	o.shown = ""
	o.mu.Unlock()
	if was {
		o.windows.HideOverlay()
	}
}

// overlayLabel はオーバーレイに添える文言「操作中: <クライアント名>（<種別>）」。名前が空ならクライアント ID を使う。
// 長さの制限と HTML のエスケープは、描画する側（GET /overlay）が行う。
func overlayLabel(a domain.Actor) string {
	name := a.Label
	if name == "" {
		name = a.ClientID
	}
	return "操作中: " + name + "（" + overlayKindLabel(a.Kind) + "）"
}

func overlayKindLabel(k domain.ActorKind) string {
	switch k {
	case domain.ActorMCP:
		return "MCP"
	case domain.ActorWeb:
		return "Web"
	case domain.ActorAgent:
		return "エージェント"
	case domain.ActorHost:
		return "ホスト"
	}
	return string(k)
}
