package remote

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

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
// 空きなら取得、自分が保持なら解放、他者（mcp・web・agent）が保持なら強制剥奪（agent の剥奪はそのタスクを中断する。FR-L08）。
// 承認待ち（pending_approval）は、要求者の表示だけで操作は無い（他者の取得は 409 になるので取得も出さない。承認待ちの
// 強制剥奪・取り下げは docs に記述が無く、実装していない）。保持者（承認待ちなら要求者）が agent のときは、実行中のタスクの
// 要約（taskView）も付ける。
func (h *Handler) leaseView(ctx context.Context, who domain.Actor) view.ViewerLease {
	holder, ok := h.d.Leases.Holder()
	if !ok {
		if p, pending := h.d.Leases.Pending(); pending {
			return view.ViewerLease{
				Pending: true, PendingMine: p.Actor.SameSession(who), HolderLabel: actorLabel(p.Actor), KindLabel: string(p.Actor.Kind),
				Task: h.taskView(ctx, p.Actor),
			}
		}
		return view.ViewerLease{CanAcquire: true}
	}
	label := actorLabel(holder.Actor)
	mine := holder.Actor.SameSession(who)
	v := view.ViewerLease{
		Held: true, Mine: mine, HolderLabel: label, KindLabel: string(holder.Actor.Kind),
		CanRelease: mine,
		CanRevoke:  !mine && holder.Actor.Kind.CanHoldLease() && who.Kind.CanRevokeLease(),
		Task:       h.taskView(ctx, holder.Actor),
	}
	if mine && holder.Scope != nil {
		v.ScopedProcess = holder.Scope.Process
	}
	return v
}

// taskView は保持者が agent のとき、そのタスク（Actor.SessionID = task_id）の要約を返す。agent 以外・タスクが見つからない・
// 読めない（ログに残す）ときは nil（パネルはタスクの欄なしで描画する）。要約にはビューアに見せてよい項目だけを写す:
// 状態・ステップ数と上限・経過と上限・開始時刻・依頼元の表示名。指示と最終回答は写さない（ビューアはタスクの依頼元ではない）。
func (h *Handler) taskView(ctx context.Context, holder domain.Actor) *view.ViewerTask {
	if holder.Kind != domain.ActorAgent {
		return nil
	}
	tasks, err := h.d.Tasks.Running(ctx)
	if err != nil {
		h.log.Warn("read the running task for the lease panel failed", "error", err)
		return nil
	}
	for _, t := range tasks {
		if t.ID != holder.SessionID {
			continue
		}
		v := &view.ViewerTask{
			Status:    taskStatusLabel(t.Status),
			Steps:     strconv.Itoa(t.StepCount) + " / " + strconv.Itoa(t.MaxSteps),
			Elapsed:   "-",
			MaxTime:   formatTaskDuration(time.Duration(t.MaxDurationSec) * time.Second),
			Requester: taskRequester(t),
		}
		if t.StartedAt != nil {
			v.StartedAt = t.StartedAt.Local().Format("15:04:05")
			v.Elapsed = formatTaskDuration(max(0, h.d.Clock.Now().Sub(*t.StartedAt)))
		}
		return v
	}
	return nil
}

func taskStatusLabel(s domain.TaskStatus) string {
	if s == domain.TaskPendingApproval {
		return "承認待ち"
	}
	return "実行中"
}

// taskRequester は依頼元のクライアントの表示名（スナップショット。無ければ ID。どちらも無ければ「（削除済み）」）。
func taskRequester(t domain.Task) string {
	switch {
	case t.ClientLabel != "":
		return t.ClientLabel
	case t.ClientID != "":
		return t.ClientID
	}
	return "（削除済み）"
}

// formatTaskDuration は経過時間・上限の表示（「45秒」「2分30秒」）。1 秒未満は「0秒」。
func formatTaskDuration(d time.Duration) string {
	d = d.Round(time.Second)
	m, s := int(d/time.Minute), int(d%time.Minute/time.Second)
	switch {
	case m == 0:
		return fmt.Sprintf("%d秒", s)
	case s == 0:
		return fmt.Sprintf("%d分", m)
	}
	return fmt.Sprintf("%d分%d秒", m, s)
}

// actorLabel は操作主体の表示名。ラベルが空なら ClientID。
func actorLabel(a domain.Actor) string {
	if a.Label == "" {
		return a.ClientID
	}
	return a.Label
}

// opsView は操作権を持つときだけ、クリップボードとショートカット送信を出す。
// スコープ付きリース（#157）では、Alt+Tab・Win など対象の外へ出るキーは無効（灰色）にし、クリップボードの読み取りの UI を出さない。
func opsView(l view.ViewerLease) view.ViewerOps {
	scoped := l.ScopedProcess != ""
	keys := make([]view.KeyMenuItem, len(shortcutMenu))
	for i, k := range shortcutMenu {
		keys[i] = k
		keys[i].Disabled = scoped && scopedKeyForbidden(k.Keys)
	}
	return view.ViewerOps{Enabled: l.Mine, Scoped: scoped, Keys: keys}
}

// scopedKeyForbidden は送信メニューの keys がスコープ付きリースで拒否されるか（domain.ScopedKeyForbidden と同じ規則）。
func scopedKeyForbidden(keys string) bool {
	combo, err := domain.ParseKeyCombo(keys)
	return err == nil && domain.ScopedKeyForbidden(combo.Mods, combo.Key)
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
	lease := h.leaseView(ctx, actor)
	return view.Viewer{
		ViewerID: id,
		WSURL:    "ws://" + host + "/ws/screen?viewer_id=" + url.QueryEscape(id),
		Monitor:  monitor, Quality: settings.ViewerDefaultQuality, FPS: settings.ViewerDefaultFPS,
		Toolbar: toolbarView(mons, monitor, strconv.Itoa(settings.ViewerDefaultQuality), strconv.Itoa(settings.ViewerDefaultFPS)),
		Lease:   lease,
		Ops:     opsView(lease),
		Picker:  h.windowPickerView(ctx, actor),
	}, nil
}
