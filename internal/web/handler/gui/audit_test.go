package gui_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

var jpegBytes = []byte{0xff, 0xd8, 0xff, 0xe0, 'J', 'F', 'I', 'F', 0xff, 0xd9}

func i64(v int64) *int64 { return &v }

// seedAudit は新しい順（id の降順）の 6 件を入れる。id 6 にだけサムネイルがある。
func seedAudit(f *guitest.Fakes) {
	at := func(ago time.Duration) time.Time { return f.Clock.T.Add(-ago) }
	f.Admin.Cs = []domain.Client{{ID: "client-001", DisplayName: "alice-laptop"}}
	f.Audit.Items = []service.AuditEntry{
		{ID: 6, TS: at(1 * time.Minute), Kind: "tool", Event: "tool.left_click", ActorType: domain.ActorMCP, ClientID: "client-001", ClientLabel: "alice-laptop", Result: "ok", DurationMS: i64(12), Thumb: &service.AuditThumb{RelPath: "2026/10/30/a.jpg", Width: 320, Height: 180}},
		{ID: 5, TS: at(2 * time.Minute), Kind: "lease", Event: "lease.acquire", ActorType: domain.ActorMCP, ClientID: "client-001", ClientLabel: "alice-laptop", Result: "denied", ErrorCode: "lease_held"},
		{ID: 4, TS: at(3 * time.Minute), Kind: "tool", Event: "tool.key", ActorType: domain.ActorMCP, ClientID: "client-001", ClientLabel: "alice-laptop", Result: "error", ErrorCode: "input_blocked"},
		{ID: 3, TS: at(4 * time.Minute), Kind: "auth", Event: "auth.bearer_failed", ActorType: domain.ActorMCP, ClientLabel: "unknown-phone", Result: "denied"},
		{ID: 2, TS: at(5 * time.Minute), Kind: "input", Event: "tool.wheel", ActorType: domain.ActorWeb, ClientLabel: "deleted-client", Result: "ok"},
		{ID: 1, TS: at(6 * time.Minute), Kind: "settings", Event: "settings.update", ActorType: domain.ActorHost, ClientLabel: "(host)", Result: "ok"},
	}
	f.Audit.Thumbs = map[int64][]byte{6: jpegBytes}
}

func TestAuditPageListsNewestFirstWithBadgesAndLocalTime(t *testing.T) {
	r := newRig(t, seedAudit)
	res := r.get("/audit")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "<title>監査ログ - puppet-remo</title>", "日時", "クライアント", "種別", "イベント", "結果", "所要時間",
		"2026-10-30 11:59:00", // 時計 12:00 UTC の 1 分前。Location は UTC
		"tool.left_click", "lease.acquire", "auth.bearer_failed", "settings.update", "12 ms",
		`data-result="ok"`, `data-result="denied"`, `data-result="error"`,
		`href="/audit/6"`, `aria-current="page"`, `href="/audit"`,
		"deleted-client", // 削除済みのクライアントはスナップショットの名前
		"alice-laptop")
	last := -1
	for _, ev := range []string{"tool.left_click", "lease.acquire", "tool.key", "auth.bearer_failed", "tool.wheel", "settings.update"} {
		i := strings.Index(res.Body, ">"+ev+"<")
		if i < last || i < 0 {
			t.Fatalf("not newest first at %q", ev)
		}
		last = i
	}
	// サムネイルの有無（id 6 のみ）。
	if n := strings.Count(res.Body, ">あり<"); n != 1 {
		t.Errorf("thumbnail badges = %d, want 1", n)
	}
	// 読み取り専用・ページ送りなし（6 件 < 50）。
	lacks(t, res, "data-pager", `method="post"`, "hx-post", "hx-delete", "hx-put")
	// 通常の遷移は limit なし・cursor なしで先頭から。
	if q := r.f.Audit.LastQuery(); q.Cursor != 0 || q.Limit != 0 || q.Kind != "" {
		t.Errorf("query = %+v", q)
	}
}

func TestAuditSidebarHasAnAuditLink(t *testing.T) {
	r := newRig(t)
	res := r.get("/")
	contains(t, res, `href="/audit"`, "監査ログ")
}

func TestAuditEmptyIsOK(t *testing.T) {
	r := newRig(t)
	res := r.get("/audit")
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="audit-list"`, "条件に合う監査ログはありません")
	lacks(t, res, "data-pager", "<table")
}

func TestAuditFiltersPassThroughAndCombine(t *testing.T) {
	r := newRig(t, seedAudit)
	tests := []struct {
		name    string
		query   string
		want    []string
		notWant []string
	}{
		{"kind", "kind=lease", []string{">lease.acquire<"}, []string{">tool.left_click<", ">settings.update<"}},
		{"result", "result=error", []string{">tool.key<"}, []string{">tool.left_click<", ">lease.acquire<"}},
		{"client", "client_id=client-001", []string{">tool.left_click<", ">lease.acquire<", ">tool.key<"}, []string{">auth.bearer_failed<", ">tool.wheel<"}},
		{"q", "q=tool.", []string{">tool.left_click<", ">tool.key<", ">tool.wheel<"}, []string{">lease.acquire<", ">settings.update<"}},
		{"combined", "kind=tool&result=ok&client_id=client-001", []string{">tool.left_click<"}, []string{">tool.key<", ">tool.wheel<"}},
		{"from", "from=" + url.QueryEscape("2026-10-30T11:56:30Z"), []string{">tool.left_click<", ">tool.key<"}, []string{">auth.bearer_failed<", ">settings.update<"}},
		{"to", "to=" + url.QueryEscape("2026-10-30T11:56:30Z"), []string{">auth.bearer_failed<", ">settings.update<"}, []string{">tool.left_click<", ">tool.key<"}},
		{"range with offset", "from=" + url.QueryEscape("2026-10-30T20:56:30+09:00") + "&to=" + url.QueryEscape("2026-10-30T20:58:30+09:00"), []string{">lease.acquire<", ">tool.key<"}, []string{">tool.left_click<", ">settings.update<"}},
		{"no match", "q=nothing-like-this", []string{`data-empty="audit-list"`}, []string{">tool.left_click<"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := r.get("/audit?" + tt.query)
			status(t, res, http.StatusOK)
			contains(t, res, tt.want...)
			lacks(t, res, tt.notWant...)
		})
	}
	// 絞り込みの値がフォームに残る。
	res := r.get("/audit?kind=lease&q=lease&from=" + url.QueryEscape("2026-10-30T00:00:00Z"))
	contains(t, res, `value="lease" selected`, `id="audit-q" name="q" type="text" value="lease"`, `value="2026-10-30T00:00:00Z"`)
}

func TestAuditPaginationWithCursorAndLimit(t *testing.T) {
	r := newRig(t, seedAudit)
	res := r.get("/audit?limit=2")
	status(t, res, http.StatusOK)
	contains(t, res, ">tool.left_click<", ">lease.acquire<", "data-pager-next", `href="/audit?cursor=5&amp;limit=2"`, `hx-push-url="true"`)
	lacks(t, res, ">tool.key<")
	if q := r.f.Audit.LastQuery(); q.Limit != 2 || q.Cursor != 0 {
		t.Errorf("query = %+v", q)
	}

	// 「次へ」の URL に絞り込みが引き継がれる。
	res = r.get("/audit?limit=1&kind=tool")
	contains(t, res, "kind=tool", "cursor=6", "limit=1")

	res = r.get("/audit?limit=2&cursor=5")
	contains(t, res, ">tool.key<", ">auth.bearer_failed<", "cursor=3")
	lacks(t, res, ">tool.left_click<", ">lease.acquire<")
	if q := r.f.Audit.LastQuery(); q.Cursor != 5 {
		t.Errorf("cursor = %d", q.Cursor)
	}

	// 最後のページには「次へ」が出ない。
	res = r.get("/audit?limit=2&cursor=3")
	contains(t, res, ">tool.wheel<", ">settings.update<")
	lacks(t, res, "data-pager")

	// 範囲外の cursor は空の表示（200）。
	res = r.get("/audit?cursor=1")
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="audit-list"`)
}

func TestAuditLimitIsCappedAt200(t *testing.T) {
	r := newRig(t, seedAudit)
	status(t, r.get("/audit?limit=100000"), http.StatusOK)
	if q := r.f.Audit.LastQuery(); q.Limit != service.MaxAuditLimit || service.MaxAuditLimit != 200 {
		t.Errorf("limit = %d (max %d), want 200", q.Limit, service.MaxAuditLimit)
	}
}

func TestAuditInvalidValuesAre422WithFieldMessages(t *testing.T) {
	r := newRig(t, seedAudit)
	tests := []struct {
		name  string
		query string
		field string
	}{
		{"from", "from=yesterday", "audit-from"},
		{"to", "to=2026-10-30", "audit-to"},
		{"kind", "kind=bogus", "audit-kind"},
		{"result", "result=maybe", "audit-result"},
		{"limit", "limit=abc", "audit-paging"},
		{"negative limit", "limit=-1", "audit-paging"},
		{"cursor", "cursor=x", "audit-paging"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(r.f.Audit.Queries)
			res := r.get("/audit?"+tt.query, hx...)
			status(t, res, http.StatusUnprocessableEntity)
			contains(t, res, `data-field-error="`+tt.field+`"`, `id="audit-list"`)
			lacks(t, res, `data-empty="audit-list"`) // 検索していないので「該当なし」とは言わない
			if len(r.f.Audit.Queries) != before {
				t.Error("an invalid request must not reach the reader")
			}
			// フルページでも 422 で、入力値と一覧の枠が残る。
			full := r.get("/audit?" + tt.query)
			status(t, full, http.StatusUnprocessableEntity)
			contains(t, full, "<html", `data-field-error="`+tt.field+`"`)
		})
	}
	// 入力値は HTML エスケープして再描画する。
	res := r.get("/audit?from=" + url.QueryEscape(`"><script>alert(1)</script>`))
	status(t, res, http.StatusUnprocessableEntity)
	lacks(t, res, "<script>alert(1)</script>")
}

func TestAuditRenderingBranches(t *testing.T) {
	r := newRig(t, seedAudit)
	// HX-Request: 一覧のフラグメント。
	frag := r.get("/audit?kind=tool", hx...)
	status(t, frag, http.StatusOK)
	contains(t, frag, `id="audit-list"`, "tool.left_click")
	lacks(t, frag, "<html", `id="sidebar-nav"`)
	// HX-Boosted: #main の中身（フルページの枠は出さない）。
	boosted := r.get("/audit", "HX-Request", "true", "HX-Boosted", "true")
	status(t, boosted, http.StatusOK)
	contains(t, boosted, `id="audit-list"`, "<title>監査ログ - puppet-remo</title>")
	lacks(t, boosted, "<html")
	// 履歴の復元はフルページ。
	restore := r.get("/audit", "HX-Request", "true", "HX-History-Restore-Request", "true")
	contains(t, restore, "<html", `id="audit-list"`)
	// フィルターと「次へ」は #audit-list を差し替え、URL を更新する。
	full := r.get("/audit?limit=1")
	contains(t, full, `hx-get="/audit"`, `hx-target="#audit-list"`, `hx-push-url="true"`, `hx-target-422="#audit-list"`)
}

func TestAuditReadFailureIs500WithoutLeakingTheReason(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Audit.Err = fmt.Errorf("sqlite: disk I/O error at /secret/path") })
	res := r.get("/audit")
	status(t, res, http.StatusInternalServerError)
	lacks(t, res, "/secret/path", "sqlite")
	res = r.get("/audit/1")
	status(t, res, http.StatusInternalServerError)
	lacks(t, res, "/secret/path")
}

func TestAuditDetailShowsFieldsArgsAndThumbnail(t *testing.T) {
	r := newRig(t, seedAudit, func(f *guitest.Fakes) {
		d := service.AuditDetail{AuditEntry: f.Audit.Items[0], Args: []service.AuditArg{{Key: "button", Value: "left"}, {Key: "x", Value: "100"}}}
		d.SessionID, d.LeaseID, d.Tool = "sess-1", "lease-9", "left_click"
		f.Audit.Details = map[int64]service.AuditDetail{6: d}
	})
	res := r.get("/audit/6")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "<title>監査ログの詳細 - puppet-remo</title>", `data-audit-detail="tool.left_click"`,
		`data-audit-field="time"`, "2026-10-30 11:59:00", `data-audit-field="client"`, "alice-laptop", `data-audit-field="session"`, "sess-1",
		`data-audit-field="lease"`, "lease-9", `data-audit-field="tool"`, "left_click", `data-audit-field="duration"`, "12 ms", `data-audit-field="actor"`, "mcp",
		"data-audit-args", "button", "left", "100",
		`<img`, `src="/audit/6/thumbnail"`, `width="320"`, `height="180"`, "data-audit-thumb", `href="/audit"`)
	lacks(t, res, `data-audit-field="error-code"`) // 無い項目は出さない
	// 日時・ID・所要時間は Mono、文章の項目は Regular のまま（ガイド T5）。
	contains(t, res, `class="break-all text-ink font-mono" data-audit-field="time"`, `class="break-all text-ink font-mono" data-audit-field="session"`,
		`class="break-all text-ink font-mono" data-audit-field="lease"`, `class="break-all text-ink font-mono" data-audit-field="duration"`,
		`class="break-all text-ink" data-audit-field="client"`)

	// HX-Boosted は #main の中身。
	b := r.get("/audit/6", "HX-Request", "true", "HX-Boosted", "true")
	status(t, b, http.StatusOK)
	lacks(t, b, "<html")
}

func TestAuditDetailWithoutThumbnailOrArgs(t *testing.T) {
	r := newRig(t, seedAudit)
	res := r.get("/audit/5")
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="audit-args"`, `data-empty="audit-thumb"`, "サムネイルはありません", `data-audit-field="error-code"`, "lease_held")
	lacks(t, res, "<img", "data-audit-thumb")
}

func TestAuditDetailNotFound(t *testing.T) {
	r := newRig(t, seedAudit)
	for _, p := range []string{"/audit/999", "/audit/abc", "/audit/0", "/audit/-1", "/audit/1.5"} {
		res := r.get(p)
		status(t, res, http.StatusNotFound)
		contains(t, res, `data-error-code="not_found"`)
	}
}

// 引数・クライアント名は信頼しない値として HTML エスケープする。表示に使うのは AuditReader が伏せた値だけで、秘密は出ない。
func TestAuditEscapesUserSuppliedValuesAndShowsOnlyRedactedArgs(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		e := service.AuditEntry{ID: 1, TS: f.Clock.T, Kind: "tool", Event: "tool.type", ActorType: domain.ActorMCP, ClientLabel: `<img src=x onerror=alert(1)>`, Result: "ok"}
		f.Audit.Items = []service.AuditEntry{e}
		f.Audit.Details = map[int64]service.AuditDetail{1: {AuditEntry: e, Args: []service.AuditArg{
			{Key: "<script>k</script>", Value: `<script>alert("x")</script>`},
			{Key: "token", Value: "[REDACTED]"},
			{Key: "note", Value: "Bearer [REDACTED]"},
		}}}
	})
	for _, p := range []string{"/audit", "/audit/1"} {
		res := r.get(p)
		status(t, res, http.StatusOK)
		lacks(t, res, "<script>alert", "<script>k", "<img src=x", "onerror=alert(1)>")
	}
	res := r.get("/audit/1")
	contains(t, res, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;", "[REDACTED]")
	lacks(t, res, "prm_")
}

func TestAuditThumbnailServesJPEGWithHeaders(t *testing.T) {
	r := newRig(t, seedAudit)
	res := r.get("/audit/6/thumbnail")
	status(t, res, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if res.Body != string(jpegBytes) {
		t.Errorf("body = %v", []byte(res.Body))
	}
}

func TestAuditThumbnailErrors(t *testing.T) {
	r := newRig(t, seedAudit, func(f *guitest.Fakes) {
		f.Audit.ThumbErr = map[int64]error{
			4: fmt.Errorf("thumbnail path: %w", domain.ErrInvalidArgument), // パスの走査（ThumbStore が拒否）
			3: fmt.Errorf("disk I/O error at /secret/path"),
		}
	})
	tests := []struct {
		path string
		want int
	}{
		{"/audit/5/thumbnail", http.StatusNotFound},   // サムネイルなし
		{"/audit/999/thumbnail", http.StatusNotFound}, // ログなし
		{"/audit/abc/thumbnail", http.StatusNotFound},
		{"/audit/4/thumbnail", http.StatusNotFound}, // 走査を拒否 → 画像は返さず 404
		{"/audit/3/thumbnail", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		res := r.get(tt.path)
		status(t, res, tt.want)
		lacks(t, res, "/secret/path", "JFIF")
	}
}

// 監査ログの経路は GUI だけにある（リモートのルーターには作らない）。ここでは POST などの変更系がないことを確かめる。
func TestAuditRoutesAreReadOnly(t *testing.T) {
	r := newRig(t, seedAudit)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		for _, p := range []string{"/audit", "/audit/6", "/audit/6/thumbnail"} {
			if res := r.do(m, p, nil); res.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", m, p, res.StatusCode)
			}
		}
	}
}
