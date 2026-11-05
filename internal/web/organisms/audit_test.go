package organisms_test

import (
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

func TestAuditListResultBadgesLocalTimeAndThumbnailFlag(t *testing.T) {
	out := webtest.Render(t, organisms.AuditList(view.AuditList{Rows: []view.AuditRow{
		{ID: 3, DetailURL: "/audit/3", Time: "2026-10-30 20:59:00", Client: "alice", Kind: "tool", Event: "tool.left_click", Result: "ok", ResultTone: view.ToneOK, Duration: "12 ms", HasThumb: true},
		{ID: 2, DetailURL: "/audit/2", Time: "2026-10-30 20:58:00", Client: "alice", Kind: "lease", Event: "lease.acquire", Result: "denied", ResultTone: view.ToneWarn, Duration: "-"},
		{ID: 1, DetailURL: "/audit/1", Time: "2026-10-30 20:57:00", Client: "bob", Kind: "tool", Event: "tool.key", Result: "error", ResultTone: view.ToneDanger, Duration: "3 ms"},
	}, NextURL: "/audit?cursor=1"}))
	has(t, out, `id="audit-list"`, "2026-10-30 20:59:00", `data-audit-row="/audit/3"`, `data-result="ok"`, `data-result="denied"`, `data-result="error"`,
		"12 ms", "data-pager-next", `hx-get="/audit?cursor=1"`)
	// 結果バッジの状態色（ok は success・denied は warning・error は danger。atoms.Badge と同じトークン）。
	for result, tone := range map[string]string{"ok": "success", "denied": "warning", "error": "danger"} {
		i := strings.Index(out, `data-result="`+result+`"`)
		j := strings.Index(out[i:], "</tr>")
		if !strings.Contains(out[i:i+j], tone) {
			t.Errorf("%s badge does not use the %s tone:\n%s", result, tone, out[i:i+j])
		}
	}
	if n := strings.Count(out, ">あり<"); n != 1 {
		t.Errorf("thumbnail flags = %d, want 1", n)
	}
	webtest.AssertCSPCompliantHTML(t, out)
}

func TestAuditListEmptyAndNoPager(t *testing.T) {
	out := webtest.Render(t, organisms.AuditList(view.AuditList{}))
	has(t, out, `data-empty="audit-list"`)
	hasNot(t, out, "<table", "data-pager")
}

func TestAuditFiltersFieldErrorsAndSelection(t *testing.T) {
	out := webtest.Render(t, organisms.AuditFilters(view.AuditFilters{
		From: "bad", Limit: "20",
		Kinds:  []view.AuditFilterOption{{Value: "", Label: "すべて"}, {Value: "tool", Label: "tool", Selected: true}},
		Errors: view.AuditFilterErrors{From: "日時が不正です", Paging: "limit が不正です"},
	}))
	has(t, out, `data-field-error="audit-from"`, "日時が不正です", `data-field-error="audit-paging"`, `type="hidden" name="limit" value="20"`, `value="tool" selected`, `hx-target-422="#audit-list"`, "novalidate")
	// ページ送りのエラーも、入力のエラーと同じ 14px（T3）でフェードインする。
	has(t, out, `class="motion-rise-in mt-1 text-sm text-danger" role="alert" data-field-error="audit-paging"`)
	hasNot(t, out, `data-field-error="audit-to"`)
	webtest.AssertCSPCompliantHTML(t, out)
}

func TestAuditDetailArgsThumbnailAndEscaping(t *testing.T) {
	d := view.AuditDetail{
		ID: 7, Time: "2026-10-30 20:59:00", Kind: "tool", Event: "tool.type", Actor: "mcp", Client: `<b>x</b>`, Result: "ok", ResultTone: view.ToneOK,
		Args:     []view.AuditArg{{Key: "k", Value: `<script>alert(1)</script>`}},
		ThumbURL: "/audit/7/thumbnail", ThumbW: "320", ThumbH: "180", BackURL: "/audit",
	}
	out := webtest.Render(t, organisms.AuditDetail(d))
	has(t, out, `data-audit-detail="tool.type"`, `<img`, `src="/audit/7/thumbnail"`, `width="320"`, `height="180"`, "data-audit-args", "&lt;script&gt;")
	hasNot(t, out, "<script>alert", "<b>x</b>", `data-empty="audit-thumb"`)
	webtest.AssertCSPCompliantHTML(t, out)

	d.ThumbURL = ""
	d.ThumbNote = "サムネイルはありません"
	d.Args = nil
	out = webtest.Render(t, organisms.AuditDetail(d))
	has(t, out, `data-empty="audit-thumb"`, "サムネイルはありません", `data-empty="audit-args"`)
	hasNot(t, out, "<img", "data-audit-args")
}

func TestSidebarHasTheAuditEntry(t *testing.T) {
	out := webtest.Render(t, organisms.Sidebar(view.Chrome{Active: view.NavAudit}, false))
	has(t, out, `href="/audit"`, "監査ログ")
	if strings.Count(out, `aria-current="page"`) != 1 || !strings.Contains(out[strings.Index(out, `href="/audit"`):], `aria-current="page"`) {
		t.Errorf("the audit entry must be the current page:\n%s", out)
	}
}
