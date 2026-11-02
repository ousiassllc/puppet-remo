package gui

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
)

// Poll は GET /poll。レイアウトが数秒ごとに呼び、ホスト GUI の「いま表示している領域」を最新にする。
//
// Wails v3.0.0 の Windows の AssetServer は AssetOptions.Handler の応答を、ハンドラが返るまでバッファする（http.Flusher も無い）。
// 長く開いたままの GET /events（SSE）の応答は WebView に届かないため、ウィンドウの更新はこのルートで行う
// （docs/architecture/frontend.md §5.3）。応答は htmx の OOB（hx-swap-oob）のフラグメントだけで、描画は SSE と同じ
// 表示モデル・ボディのコンポーネントを使う。
//
//	ヘッダーのキルスイッチのバッジ・サイドバーの接続要求の件数  → 常に
//	ダッシュボードの #server-status・#sessions・#lease            → クエリ rev_killswitch があるとき（ダッシュボードを表示中）
//	キルスイッチのパネル                                         → rev_killswitch があり、版が違うとき
//	ダッシュボードの実行中タスク（#running-tasks）                 → rev_tasks があり、版が違うとき（M4）
//	接続要求の一覧                                               → rev_requests があり、版が違うとき
//	トースト                                                     → 前回の呼び出し以降に溜まった分
//
// rev_* は、表示中の領域が持つ隠し入力（data-poll-rev）の値で、レイアウトの hx-include が送り返す。表示していない領域の OOB は
// 差し込み先が無いため送らない。版が変わっていない領域は差し替えない（入力中のフォームや押下中のボタンを、ポーリングで壊さないため）。
func (h *Handler) Poll(c *gin.Context) {
	ctx := c.Request.Context()
	pending, pendingErr := h.d.Admin.PendingRequests(ctx)
	if pendingErr != nil {
		h.log.Warn("poll: count pending requests failed", "error", pendingErr)
	}

	comps := []templ.Component{
		organisms.HeaderKillSwitchOOB(h.d.KillSwitch.Active()),
		organisms.RequestsBadgeOOB(len(pending)),
	}
	if rev, shown := c.GetQuery("rev_requests"); shown && pendingErr == nil {
		if list := h.requestListView(pending); list.Rev != rev {
			comps = append(comps, organisms.ConnectionRequestListOOB(list))
		}
	}
	if rev, shown := c.GetQuery("rev_tasks"); shown {
		if tasks := h.runningTasksView(c.Request.Context()); tasks.Rev != rev {
			comps = append(comps, organisms.RunningTasksOOB(tasks))
		}
	}
	if rev, shown := c.GetQuery("rev_killswitch"); shown {
		comps = append(comps,
			organisms.ServerStatusOOB(h.serverStatusView(ctx)),
			organisms.SessionListOOB(h.sessionsView()),
			organisms.LeasePanelOOB(h.leaseView()),
		)
		if ks := h.killSwitchView(); ks.Rev != rev {
			comps = append(comps, organisms.KillSwitchPanelOOB(ks))
		}
	}
	comps = append(comps, h.drainToasts()...)

	c.Header("Cache-Control", "no-store")
	h.render(c, http.StatusOK, comps...)
}

// drainToasts は Bus の toast イベント（New で購読済み。バッファは Bus が持ち、満杯なら古いものから捨てる）を取り出し、
// #toast-root へ追加する OOB にする。
func (h *Handler) drainToasts() []templ.Component {
	var out []templ.Component
	for {
		select {
		case e := <-h.toasts:
			if t, ok := toastView(e.Data); ok {
				out = append(out, organisms.ToastOOB(t))
			}
		default:
			return out
		}
	}
}
