package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは domain / service の値を view（表示用モデル）に写す。状態に応じてどの操作を出すかは、ここで決める。

const timeLayout = "2006-01-02 15:04"

func (h *Handler) formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(h.loc).Format(timeLayout)
}

func (h *Handler) formatTimePtr(t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return h.formatTime(*t)
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Second {
		return "まもなく"
	}
	m, s := int(d/time.Minute), int(d%time.Minute/time.Second)
	switch {
	case m == 0:
		return fmt.Sprintf("%d秒", s)
	case s == 0:
		return fmt.Sprintf("%d分", m)
	}
	return fmt.Sprintf("%d分%d秒", m, s)
}

func kindLabel(k domain.ActorKind) string {
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

func approvalLabel(m domain.ApprovalMode) string {
	switch m {
	case domain.ApprovalAlways:
		return "常時承認"
	case domain.ApprovalFirstTime:
		return "初回のみ承認"
	case domain.ApprovalAuto:
		return "自動許可"
	}
	return string(m)
}

// approvalModes は接続要求の許可フォームの承認モードの選択肢。M1 は「常時承認」だけを出し、既定で選択する
// （初回のみ承認・自動許可は適用が M3 のため、M3 で選択肢を足す）。
func approvalModes() []view.ApprovalModeOption {
	return []view.ApprovalModeOption{{Value: string(domain.ApprovalAlways), Label: approvalLabel(domain.ApprovalAlways), Selected: true}}
}

// chrome は全画面共通のヘッダー・サイドバーの表示を組み立てる。件数が取れなくても画面は出す（0 件として扱いログに残す）。
func (h *Handler) chrome(ctx context.Context, active view.Nav) view.Chrome {
	return view.Chrome{Active: active, Header: h.header(ctx)}
}

func (h *Handler) header(ctx context.Context) view.Header {
	pending, err := h.d.Admin.PendingRequests(ctx)
	if err != nil {
		h.log.Warn("count pending requests failed", "error", err)
	}
	return view.Header{KillSwitchActive: h.d.KillSwitch.Active(), PendingRequests: len(pending)}
}

func (h *Handler) serverStatusView(ctx context.Context) view.ServerStatus {
	st := h.d.Status.ServerStatus(ctx)
	v := view.ServerStatus{
		TailnetIPs: st.TailnetIPs, DNSName: st.DNSName, Port: st.Port, ListenAddrs: st.ListenAddrs, Reason: st.Reason,
	}
	switch st.Tailscaled {
	case service.TailscaledRunning:
		v.TailscaledLabel, v.TailscaledTone = "稼働中", view.ToneOK
	case service.TailscaledNeedsLogin:
		v.TailscaledLabel, v.TailscaledTone = "要ログイン", view.ToneWarn
	case service.TailscaledUnavailable:
		v.TailscaledLabel, v.TailscaledTone = "接続できません", view.ToneDanger
	default:
		v.TailscaledLabel, v.TailscaledTone = "確認中", view.ToneNeutral
	}
	if st.Listening {
		v.ListenLabel, v.ListenTone = "待ち受け中", view.ToneOK
	} else {
		v.ListenLabel, v.ListenTone = "停止中", view.ToneWarn
	}
	return v
}

func (h *Handler) sessionsView() view.Sessions {
	var v view.Sessions
	for _, s := range h.d.Sessions.Sessions() {
		label := s.Label
		if label == "" {
			label = s.ClientID
		}
		v.Items = append(v.Items, view.Session{ClientLabel: label, KindLabel: kindLabel(s.Kind), Since: h.formatTime(s.Since)})
	}
	return v
}

func (h *Handler) leaseView() view.Lease {
	holder, ok := h.d.Leases.Holder()
	if !ok {
		return view.Lease{}
	}
	label := holder.Actor.Label
	if label == "" {
		label = holder.Actor.ClientID
	}
	return view.Lease{
		Held: true, CanRevoke: holder.Actor.Kind.CanHoldLease(), HolderLabel: label, KindLabel: kindLabel(holder.Actor.Kind),
		Since: h.formatTime(holder.Since), IdleRemaining: formatDuration(holder.IdleRemaining),
	}
}

// killSwitchView は発動中なら解除だけ、そうでなければ発動だけを出す。
func (h *Handler) killSwitchView() view.KillSwitch {
	st := h.d.KillSwitch.State()
	v := view.KillSwitch{Active: st.Active, CanTrigger: !st.Active, CanRelease: st.Active, ConfirmRoute: "/killswitch/release/confirm"}
	if st.Active {
		v.Since, v.Source = h.formatTime(st.TriggeredAt), sourceLabel(st.Source)
	}
	v.Rev = revision(v)
	return v
}

func sourceLabel(s string) string {
	switch s {
	case killSwitchSource:
		return "ホスト GUI"
	case "hotkey":
		return "ホットキー"
	case "":
		return "不明"
	}
	return s
}

func (h *Handler) dashboardView(ctx context.Context) view.Dashboard {
	return view.Dashboard{
		Chrome:     h.chrome(ctx, view.NavDashboard),
		Status:     h.serverStatusView(ctx),
		Sessions:   h.sessionsView(),
		Lease:      h.leaseView(),
		KillSwitch: h.killSwitchView(),
	}
}

func (h *Handler) requestListView(reqs []service.ConnRequest) view.RequestList {
	var v view.RequestList
	for _, r := range reqs {
		id := url.PathEscape(r.ID)
		v.Rows = append(v.Rows, view.RequestRow{
			ID: r.ID, DisplayName: r.DisplayName, LoginName: r.LoginName, RemoteIP: r.RemoteIP, Count: r.RequestCount,
			FirstSeen: h.formatTime(r.FirstSeenAt), LastSeen: h.formatTime(r.LastSeenAt), ExpiresAt: h.formatTime(r.ExpiresAt),
			ApproveURL: "/requests/" + id + "/approve", DenyURL: "/requests/" + id + "/deny", Modes: approvalModes(),
		})
	}
	v.Rev = revision(v)
	return v
}

// pageParam はクエリ page（1 始まり）。無い・数値でない・1 未満は 1。
func pageParam(c *gin.Context) int {
	n, err := strconv.Atoi(c.Query("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// historyResult は履歴の行の結果の表示。期限を過ぎた pending は、保守（Expire）で expired にされる前でも失効として出す。
func (h *Handler) historyResult(r service.ConnRequest) (code, label string, tone view.BadgeTone) {
	status := r.Status
	if status == service.ConnPending && !r.ExpiresAt.After(h.d.Clock.Now()) {
		status = service.ConnExpired
	}
	switch status {
	case service.ConnApproved:
		return status, "許可", view.ToneOK
	case service.ConnDenied:
		return status, "拒否", view.ToneDanger
	case service.ConnExpired:
		return status, "失効", view.ToneNeutral
	}
	return status, "未処理", view.ToneWarn
}

func (h *Handler) requestHistoryView(p service.RequestHistoryPage) view.RequestHistory {
	v := view.RequestHistory{Page: p.Page}
	for _, r := range p.Items {
		code, label, tone := h.historyResult(r)
		v.Rows = append(v.Rows, view.HistoryRow{
			ID: r.ID, DisplayName: r.DisplayName, LoginName: r.LoginName, RemoteIP: r.RemoteIP,
			ReceivedAt: h.formatTime(r.FirstSeenAt), Result: label, ResultTone: tone, ResultCode: code,
			DecidedAt: h.formatTimePtr(r.DecidedAt, "-"),
		})
	}
	if p.HasPrev {
		v.PrevURL = "/requests/history?page=" + strconv.Itoa(p.Page-1)
	}
	if p.HasNext {
		v.NextURL = "/requests/history?page=" + strconv.Itoa(p.Page+1)
	}
	return v
}

// revision は表示モデル v の版（内容が同じなら同じ文字列）。v は文字列・数値・それらの slice / struct だけでできている。
func revision(v any) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%+v", v))
	return hex.EncodeToString(sum[:8])
}

func (h *Handler) clientView(c domain.Client, inList bool) view.Client {
	id := url.PathEscape(c.ID)
	toggle := "/clients/" + id
	if !inList {
		toggle += "?view=detail"
	}
	flag := func(field, label string, enabled bool) view.ClientFlag {
		return view.ClientFlag{Field: field, Label: label, Enabled: enabled, ToggleURL: toggle, NextValue: strconv.FormatBool(!enabled)}
	}
	return view.Client{
		ID: c.ID, DisplayName: c.DisplayName, LoginName: c.LoginName, NodeID: c.StableNodeID, Approved: approvalLabel(c.ApprovalMode),
		Flags: []view.ClientFlag{
			flag("web_enabled", "Web", c.WebEnabled),
			flag("mcp_enabled", "MCP", c.MCPEnabled),
			flag("agent_enabled", "エージェント", c.AgentEnabled),
		},
		DetailURL: "/clients/" + id, DeleteConfirm: "/clients/" + id + "/delete/confirm",
		CanDelete: inList, CanOpenDetail: inList,
	}
}

func (h *Handler) clientListView(cs []domain.Client) view.ClientList {
	var v view.ClientList
	for _, c := range cs {
		v.Items = append(v.Items, h.clientView(c, true))
	}
	return v
}

func (h *Handler) tokenListView(clientID string, ts []service.TokenRecord) view.TokenList {
	now := h.d.Clock.Now()
	v := view.TokenList{ClientID: clientID}
	for _, t := range ts {
		status, tone, active := "有効", view.ToneOK, true
		switch {
		case t.RevokedAt != nil:
			status, tone, active = "失効済み", view.ToneDanger, false
		case t.ExpiresAt != nil && !t.ExpiresAt.After(now):
			status, tone, active = "期限切れ", view.ToneWarn, false
		}
		v.Items = append(v.Items, view.Token{
			ID: t.ID, Label: t.Label, Prefix: t.Prefix, Status: status, StatusTone: tone,
			CreatedAt: h.formatTime(t.CreatedAt), ExpiresAt: h.formatTimePtr(t.ExpiresAt, "無期限"),
			LastUsedAt:    h.formatTimePtr(t.LastUsedAt, "未使用"),
			CanRevoke:     active,
			RevokeConfirm: "/clients/" + url.PathEscape(clientID) + "/tokens/" + url.PathEscape(t.ID) + "/revoke/confirm",
		})
	}
	return v
}

// expiryDays はトークンの有効期限の選択肢（日数。never は無期限）。既定は 90 日（FR-A08）。
var expiryDays = []struct {
	Value, Label string
}{
	{"30", "30 日"}, {"90", "90 日（既定）"}, {"180", "180 日"}, {"365", "365 日"}, {"never", "無期限"},
}

const defaultExpiryValue = "90"

func tokenFormView(clientID, label, errMsg string) view.TokenForm {
	f := view.TokenForm{ActionURL: "/clients/" + url.PathEscape(clientID) + "/tokens", Label: label, Error: errMsg}
	for _, o := range expiryDays {
		f.Expiry = append(f.Expiry, view.ExpiryOption{Value: o.Value, Label: o.Label, Selected: o.Value == defaultExpiryValue})
	}
	return f
}

func (h *Handler) settingsFormView(values view.SettingsValues, errs view.SettingsErrors) view.SettingsForm {
	return view.SettingsForm{Values: values, Errors: errs, CanSetAutostart: h.d.Settings.AutostartAvailable()}
}

func settingsValues(s service.HostSettings) view.SettingsValues {
	return view.SettingsValues{
		Port: strconv.Itoa(s.Port), Hotkey: s.KillSwitchHotkey, ScreenshotMaxDim: strconv.Itoa(s.ScreenshotMaxDimension),
		LeaseIdleSec: strconv.Itoa(s.LeaseIdleTimeoutSec), ViewerQuality: strconv.Itoa(s.ViewerDefaultQuality),
		ViewerFPS: strconv.Itoa(s.ViewerDefaultFPS), Autostart: s.Autostart,
	}
}

// logLevelTone はログのレベルの色（ERROR 以上は赤、WARN は黄、INFO は緑＝成功・通常、DEBUG などは灰）。
func logLevelTone(l slog.Level) view.BadgeTone {
	switch {
	case l >= slog.LevelError:
		return view.ToneDanger
	case l >= slog.LevelWarn:
		return view.ToneWarn
	case l >= slog.LevelInfo:
		return view.ToneOK
	}
	return view.ToneNeutral
}

// logURL は /logs の URL。all と 1 ページ目は省略する。
func logURL(filter service.AppLogFilter, page int) string {
	q := url.Values{}
	if filter != service.AppLogAll {
		q.Set("level", string(filter))
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/logs"
	}
	return "/logs?" + q.Encode()
}

func (h *Handler) appLogView(filter service.AppLogFilter, p service.AppLogPage) view.AppLog {
	v := view.AppLog{Page: p.Page, Filter: string(filter)}
	for _, f := range []struct {
		filter service.AppLogFilter
		label  string
	}{{service.AppLogAll, "すべて"}, {service.AppLogWarn, "WARN 以上"}, {service.AppLogError, "ERROR のみ"}} {
		v.Filters = append(v.Filters, view.LogFilterLink{Code: string(f.filter), Label: f.label, URL: logURL(f.filter, 1), Active: f.filter == filter})
	}
	for _, e := range p.Items {
		row := view.LogRow{Time: h.formatLogTime(e.Time), Level: e.Level, LevelTone: logLevelTone(e.Severity), Message: e.Message}
		for _, a := range e.Attrs {
			row.Attrs = append(row.Attrs, view.LogAttr{Key: a.Key, Value: a.Value})
		}
		v.Rows = append(v.Rows, row)
	}
	if p.HasPrev {
		v.PrevURL = logURL(filter, p.Page-1)
	}
	if p.HasNext {
		v.NextURL = logURL(filter, p.Page+1)
	}
	return v
}

// formatLogTime はログの日時（秒まで）。
func (h *Handler) formatLogTime(t time.Time) string {
	return t.In(h.loc).Format("2006-01-02 15:04:05")
}
