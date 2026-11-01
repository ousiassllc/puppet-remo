package remote

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// viewerIDHeader はビューアのセッション（viewer_id）を伝えるヘッダー。<main id="viewer-root" hx-headers> が付ける（API 仕様 §4.1）。
const viewerIDHeader = "X-Viewer-Id"

// shortcutMenu はショートカット送信メニュー（FR-W06）の項目。ブラウザ・OS が先に処理して、キャンバスからは送れないキー操作の
// 最小の組。`keys` の書式（API 仕様 §3.3）の文字列で送る。Ctrl+Alt+Del は送れないので含めない（FR-W08）。
// Win キー単独は `keys` の書式で表せない（win は修飾キー）ため、Win+D（デスクトップを表示）にしている。
var shortcutMenu = []view.KeyMenuItem{
	{Label: "Alt+Tab", Keys: "alt+tab"},
	{Label: "Alt+F4", Keys: "alt+f4"},
	{Label: "Win+D", Keys: "win+d"},
	{Label: "Ctrl+W", Keys: "ctrl+w"},
	{Label: "Esc", Keys: "escape"},
}

// viewerContext は連鎖が認可したクライアントと、X-Viewer-Id が表すビューアの操作主体。
// 連鎖を通っていない（クライアントが無い）リクエストは 403 whois_failed、viewer_id の不正は 400 invalid_argument。
func (h *Handler) viewerContext(c *gin.Context) (domain.Client, domain.Actor, bool) {
	client, ok := middleware.ClientFrom(c.Request.Context())
	if !ok {
		h.failMessage(c, 403, string(domain.CodeWhoisFailed), "接続元を特定できませんでした。")
		return domain.Client{}, domain.Actor{}, false
	}
	actor, err := h.d.Sessions.Actor(c.GetHeader(viewerIDHeader), client)
	if err != nil {
		h.failMessage(c, 400, string(domain.CodeInvalidArgument), "ビューアのセッションが無効です。ページを再読み込みしてください。")
		return domain.Client{}, domain.Actor{}, false
	}
	return client, actor, true
}

// leaseView は who から見たリースパネルの表示。操作は状態に合うものだけを許す（状態駆動）:
// 空きなら取得、自分が保持なら解放、他者（mcp・web）が保持なら強制剥奪（agent の剥奪は M4）。
func (h *Handler) leaseView(who domain.Actor) view.ViewerLease {
	holder, ok := h.d.Leases.Holder()
	if !ok {
		return view.ViewerLease{CanAcquire: true}
	}
	label := holder.Actor.Label
	if label == "" {
		label = holder.Actor.ClientID
	}
	mine := holder.Actor.SameSession(who)
	return view.ViewerLease{
		Held: true, Mine: mine, HolderLabel: label, KindLabel: string(holder.Actor.Kind),
		CanRelease: mine,
		CanRevoke:  !mine && holder.Actor.Kind.CanHoldLease() && who.Kind.CanRevokeLease(),
	}
}

// opsView は操作権を持つときだけ、クリップボードとショートカット送信を出す。
func opsView(l view.ViewerLease) view.ViewerOps {
	return view.ViewerOps{Enabled: l.Mine, Keys: shortcutMenu}
}

// toolbarView は設定フォームの表示。monitors が空なら選択肢も無い。
func toolbarView(monitors []domain.Monitor, selected int, quality, fps string) view.ViewerToolbar {
	opts := make([]view.Option, len(monitors))
	for i, m := range monitors {
		opts[i] = view.Option{Value: strconv.Itoa(m.Index), Label: monitorLabel(m), Selected: m.Index == selected}
	}
	return view.ViewerToolbar{Monitors: opts, Quality: quality, FPS: fps}
}

func monitorLabel(m domain.Monitor) string {
	label := fmt.Sprintf("モニター %d（%d×%d）", m.Index+1, m.Size.W, m.Size.H)
	if m.Primary {
		label += " メイン"
	}
	return label
}

// primaryMonitor は接続時に選ばれるモニター（プライマリ。無ければ先頭。ws の接続時の選び方と同じ）。
func primaryMonitor(mons []domain.Monitor) int {
	for _, m := range mons {
		if m.Primary {
			return m.Index
		}
	}
	if len(mons) > 0 {
		return mons[0].Index
	}
	return 0
}

// viewerPage は GET /viewer の表示。viewer_id を発行して埋め込む（API 仕様 §4.1）。
func (h *Handler) viewerPage(ctx context.Context, host string, client domain.Client) (view.Viewer, error) {
	settings, err := h.d.Settings.Load(ctx)
	if err != nil {
		return view.Viewer{}, err
	}
	mons, err := h.d.Control.Monitors(ctx)
	if err != nil {
		return view.Viewer{}, err
	}
	id := h.d.Sessions.Issue(client)
	actor, err := h.d.Sessions.Actor(id, client)
	if err != nil {
		return view.Viewer{}, err
	}
	monitor := primaryMonitor(mons)
	lease := h.leaseView(actor)
	return view.Viewer{
		ViewerID: id,
		WSURL:    "ws://" + host + "/ws/screen?viewer_id=" + url.QueryEscape(id),
		Monitor:  monitor, Quality: settings.ViewerDefaultQuality, FPS: settings.ViewerDefaultFPS,
		Toolbar: toolbarView(mons, monitor, strconv.Itoa(settings.ViewerDefaultQuality), strconv.Itoa(settings.ViewerDefaultFPS)),
		Lease:   lease,
		Ops:     opsView(lease),
	}, nil
}
