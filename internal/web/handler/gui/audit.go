package gui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは監査ログの閲覧（GET /audit・/audit/:id・/audit/:id/thumbnail。FR-AU05、FR-H08）。
// すべて読み取りで、GUI ルーターだけにある。ポーリング（/poll）の対象にしない。

// auditKinds は絞り込みの種別（docs/architecture/er.md の audit_logs.kind）。
var auditKinds = []struct{ Value, Label string }{
	{"auth", "auth（認証）"}, {"client", "client（クライアント）"}, {"token", "token（トークン）"}, {"lease", "lease（リース）"},
	{"tool", "tool（MCP ツール）"}, {"input", "input（Web の入力）"}, {"agent", "agent（エージェント）"},
	{"killswitch", "killswitch（キルスイッチ）"}, {"settings", "settings（設定）"}, {"update", "update（更新）"},
}

// auditResults は絞り込みの結果（audit_logs.result の CHECK 制約）。
var auditResults = []struct{ Value, Label string }{{"ok", "ok（成功）"}, {"denied", "denied（拒否）"}, {"error", "error（失敗）"}}

const auditTimeLayout = "2006-01-02 15:04:05"

// 絞り込みの入力の不備に出す固定の文。
const (
	msgAuditTime   = "日時は RFC 3339 で入力してください（例: 2026-11-02T09:00:00+09:00）。"
	msgAuditKind   = "未知の種別です。一覧から選んでください。"
	msgAuditResult = "未知の結果です。一覧から選んでください。"
	msgAuditPaging = "limit と cursor は 0 以上の整数で指定してください。"
)

// auditFilter は GET /audit のクエリを読んだ結果。Values は検証前の入力（エラーのときもそのまま再描画する）。
type auditFilter struct {
	Query  service.AuditQuery
	Values view.AuditFilters
	Errors view.AuditFilterErrors
	Limit  int // 検証済みの limit（0 は既定）
}

func (f auditFilter) invalid() bool { return f.Errors.Any() }

func parseAuditFilter(c *gin.Context) auditFilter {
	var f auditFilter
	q := func(key string) string { return strings.TrimSpace(c.Query(key)) }
	f.Values.From, f.Values.To, f.Values.Q = q("from"), q("to"), q("q")
	clientID, kind, result := q("client_id"), q("kind"), q("result")
	f.Query.ClientID, f.Query.Q = clientID, f.Values.Q

	parseTime := func(raw string, msg *string) time.Time {
		if raw == "" {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			*msg = msgAuditTime
		}
		return t
	}
	f.Query.From = parseTime(f.Values.From, &f.Errors.From)
	f.Query.To = parseTime(f.Values.To, &f.Errors.To)

	if kind != "" {
		f.Query.Kind = kind
		f.Errors.Kind = msgAuditKind
		for _, k := range auditKinds {
			if k.Value == kind {
				f.Errors.Kind = ""
			}
		}
	}
	if result != "" {
		f.Query.Result = result
		f.Errors.Result = msgAuditResult
		for _, r := range auditResults {
			if r.Value == result {
				f.Errors.Result = ""
			}
		}
	}

	if raw := q("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			f.Errors.Paging = msgAuditPaging
		}
		f.Limit = min(n, service.MaxAuditLimit)
		if f.Limit > 0 && f.Limit != service.DefaultAuditLimit {
			f.Values.Limit = strconv.Itoa(f.Limit)
		}
	}
	if raw := q("cursor"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			f.Errors.Paging = msgAuditPaging
		}
		f.Query.Cursor = n
	}
	f.Query.Limit = f.Limit
	f.Values.Errors = f.Errors
	f.Values.Clients = []view.AuditFilterOption{{Value: "", Label: "すべて", Selected: clientID == ""}}
	f.Values.Kinds = []view.AuditFilterOption{{Value: "", Label: "すべて", Selected: kind == ""}}
	for _, k := range auditKinds {
		f.Values.Kinds = append(f.Values.Kinds, view.AuditFilterOption{Value: k.Value, Label: k.Label, Selected: k.Value == kind})
	}
	f.Values.Results = []view.AuditFilterOption{{Value: "", Label: "すべて", Selected: result == ""}}
	for _, r := range auditResults {
		f.Values.Results = append(f.Values.Results, view.AuditFilterOption{Value: r.Value, Label: r.Label, Selected: r.Value == result})
	}
	return f
}

// Audit は GET /audit?from=&to=&client_id=&kind=&result=&q=&cursor=&limit=（ts の降順。API 仕様 §7.3）。
// HX-Request は一覧のフラグメント（#audit-list）。HX-Boosted とそれ以外（履歴の復元を含む）はフルページ。
// 不正な値は 422（項目別のメッセージ付きで同じ画面を描き直す）。読み出しの失敗は 500（理由は画面に出さない）。
func (h *Handler) Audit(c *gin.Context) {
	ctx := c.Request.Context()
	f := parseAuditFilter(c)
	clients, err := h.d.Admin.Clients(ctx)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	for _, cl := range clients {
		f.Values.Clients = append(f.Values.Clients, view.AuditFilterOption{
			Value: cl.ID, Label: cl.DisplayName, Selected: cl.ID == f.Query.ClientID,
		})
	}
	list := view.AuditList{Filters: f.Values}
	code := http.StatusOK
	if f.invalid() {
		code = http.StatusUnprocessableEntity
	} else {
		page, err := h.d.Audit.Query(ctx, f.Query)
		if err != nil {
			h.pageFail(c, err)
			return
		}
		list.Rows = h.auditRows(page.Entries)
		list.NextURL = auditNextURL(f, page.Next)
	}
	switch {
	case boostedRequest(c):
		h.render(c, code, pages.AuditBoosted(view.AuditListPage{Chrome: h.chrome(ctx, view.NavAudit), List: list}))
	case fragmentRequest(c):
		h.render(c, code, organisms.AuditList(list))
	default:
		h.render(c, code, pages.Audit(view.AuditListPage{Chrome: h.chrome(ctx, view.NavAudit), List: list}))
	}
}

// auditNextURL は「次へ」の URL。今の絞り込み（空でないものだけ）と、次のページの cursor を付ける。次のページが無ければ空。
func auditNextURL(f auditFilter, next int64) string {
	if next <= 0 {
		return ""
	}
	q := url.Values{}
	for k, v := range map[string]string{
		"from": f.Values.From, "to": f.Values.To, "q": f.Values.Q, "client_id": f.Query.ClientID,
		"kind": f.Query.Kind, "result": f.Query.Result, "limit": f.Values.Limit,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	q.Set("cursor", strconv.FormatInt(next, 10))
	return "/audit?" + q.Encode()
}

func resultTone(result string) view.BadgeTone {
	switch result {
	case "ok":
		return view.ToneOK
	case "denied":
		return view.ToneWarn
	case "error":
		return view.ToneDanger
	}
	return view.ToneNeutral
}

func formatAuditDuration(ms *int64) string {
	if ms == nil {
		return "-"
	}
	return strconv.FormatInt(*ms, 10) + " ms"
}

func (h *Handler) auditRows(entries []service.AuditEntry) []view.AuditRow {
	rows := make([]view.AuditRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, view.AuditRow{
			ID: e.ID, DetailURL: "/audit/" + strconv.FormatInt(e.ID, 10), Time: e.TS.In(h.loc).Format(auditTimeLayout),
			Client: e.ClientLabel, Kind: e.Kind, Event: e.Event, Result: e.Result, ResultTone: resultTone(e.Result),
			Duration: formatAuditDuration(e.DurationMS), HasThumb: e.Thumb != nil,
		})
	}
	return rows
}

// auditID は :id を監査ログの id にする。正の整数でなければ ok=false（呼び出し側が 404）。
func auditID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return id, err == nil && id > 0
}

const (
	noThumbNote = "サムネイルはありません（保存の設定が無効だった・保存できなかった・保持期間か容量の上限で削除された、のいずれかです）。"
)

// AuditDetail は GET /audit/:id（1 件の詳細）。対象が無い・id が数値でないときは 404。
func (h *Handler) AuditDetail(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := auditID(c)
	if !ok {
		h.pageFail(c, errNoRoute)
		return
	}
	d, err := h.d.Audit.Get(ctx, id)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	v := view.AuditDetail{
		Chrome: h.chrome(ctx, view.NavAudit), ID: d.ID, Time: d.TS.In(h.loc).Format(auditTimeLayout), Kind: d.Kind, Event: d.Event,
		Actor: string(d.ActorType), Client: d.ClientLabel, SessionID: d.SessionID, LeaseID: d.LeaseID, Tool: d.Tool,
		Result: d.Result, ResultTone: resultTone(d.Result), ErrorCode: d.ErrorCode, Duration: "", ThumbNote: noThumbNote, BackURL: "/audit",
	}
	if d.DurationMS != nil {
		v.Duration = formatAuditDuration(d.DurationMS)
	}
	for _, a := range d.Args {
		v.Args = append(v.Args, view.AuditArg{Key: a.Key, Value: a.Value})
	}
	if d.Thumb != nil {
		v.ThumbURL = "/audit/" + strconv.FormatInt(d.ID, 10) + "/thumbnail"
		v.ThumbW, v.ThumbH = strconv.Itoa(d.Thumb.Width), strconv.Itoa(d.Thumb.Height)
	}
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.AuditDetailBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.AuditDetail(v))
}

// AuditThumbnail は GET /audit/:id/thumbnail（JPEG）。パスは DB の rel_path で、ユーザー入力ではない。thumbs/ の外へ出るパスは
// ThumbStore が拒否し（domain.ErrInvalidArgument）、ここでは存在しないものと同じ 404 にする。
func (h *Handler) AuditThumbnail(c *gin.Context) {
	id, ok := auditID(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	rc, err := h.d.Audit.Thumbnail(c.Request.Context(), id)
	switch {
	case err == nil:
	case errors.Is(err, service.ErrNotFound):
		c.Status(http.StatusNotFound)
		return
	case errors.Is(err, domain.ErrInvalidArgument):
		h.log.Warn("audit thumbnail path was rejected", "audit_id", id, "error", err)
		c.Status(http.StatusNotFound)
		return
	default:
		h.log.Error("audit thumbnail failed", "audit_id", id, "error", err)
		c.Status(http.StatusInternalServerError)
		return
	}
	defer func() { _ = rc.Close() }()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, -1, "image/jpeg", rc, nil)
}
