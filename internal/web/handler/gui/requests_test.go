package gui_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

func TestRequestsPage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedRequest(f, "req-1", "bob-laptop")
		seedRequest(f, "req-2", "carol-phone")
	})
	res := r.get("/requests")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "bob-laptop", "carol-phone", "bob@example.com", "100.64.0.9", "3 回の要求",
		`hx-post="/requests/req-1/approve"`, `hx-post="/requests/req-1/deny"`, `hx-post="/requests/req-2/approve"`,
		`name="rev_requests"`,
		// サイドバーの件数バッジ。
		`data-pending-count="2"`)
	// 承認モードの既定は「常時承認」だけ（初回のみ承認・自動許可は選択肢に出さない。M3）。
	contains(t, res, `name="approval_mode"`, `<option value="always" selected>常時承認</option>`)
	lacks(t, res, `value="first_time"`, `value="auto"`, "初回のみ承認", "自動許可")
	// 機能の既定: Web・MCP は on、エージェントは off。
	contains(t, res, `name="web_enabled"`, `name="mcp_enabled"`, `name="agent_enabled"`)
}

func TestRequestsEmptyState(t *testing.T) {
	r := newRig(t)
	res := r.get("/requests")
	contains(t, res, `data-empty="requests"`, "未処理の接続要求はありません")
	lacks(t, res, "/approve", "/deny", "data-pending-count")
}

func TestRequestsFragmentVariants(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedRequest(f, "req-1", "bob-laptop") })
	frag := r.get("/requests", hx...)
	contains(t, frag, `id="request-list"`, "bob-laptop")
	lacks(t, frag, "<html", "<body")
	boosted := r.get("/requests", "HX-Request", "true", "HX-Boosted", "true")
	contains(t, boosted, "<title>接続要求 - puppet-remo</title>", `id="request-list"`)
	lacks(t, boosted, "<html")
}

func TestApproveRequest(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedRequest(f, "req-1", "bob-laptop")
		seedRequest(f, "req-2", "carol-phone")
	})
	res := r.post("/requests/req-1/approve", url.Values{
		"display_name": {"Bob の PC"}, "approval_mode": {"always"}, "web_enabled": {"on"}, "mcp_enabled": {"on"},
	}, hx...)
	status(t, res, http.StatusOK)
	// 許可した要求は一覧から消え、件数バッジは OOB で更新、完了のトーストが添えられる。
	lacks(t, res, "bob-laptop", "/requests/req-1/")
	contains(t, res, "carol-phone", `id="request-list"`, `id="nav-requests-badge"`, `data-pending-count="1"`, "接続要求を許可し")
	lacks(t, res, "<html")

	if len(r.f.Admin.Approved) != 1 {
		t.Fatalf("approved = %+v", r.f.Admin.Approved)
	}
	got := r.f.Admin.Approved[0]
	want := service.ApproveInput{DisplayName: "Bob の PC", WebEnabled: true, MCPEnabled: true, AgentEnabled: false}
	if got != want {
		t.Errorf("ApproveInput = %+v, want %+v", got, want)
	}
	if len(r.f.Admin.Cs) != 1 || r.f.Admin.Cs[0].DisplayName != "Bob の PC" {
		t.Errorf("clients = %+v", r.f.Admin.Cs)
	}
}

func TestApproveRequestDefaultsApprovalModeToAlways(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedRequest(f, "req-1", "bob-laptop") })
	// approval_mode を送らなくても許可できる（M1 では常に always で保存し、参照しない）。
	res := r.post("/requests/req-1/approve", url.Values{"mcp_enabled": {"on"}}, hx...)
	status(t, res, http.StatusOK)
	if c := r.f.Admin.Cs[0]; c.ApprovalMode != "always" || c.WebEnabled || !c.MCPEnabled || c.AgentEnabled {
		t.Errorf("client = %+v", c)
	}
}

func TestApproveRequestRejectsOtherApprovalModes(t *testing.T) {
	for _, mode := range []string{"first_time", "auto", "bogus"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) { seedRequest(f, "req-1", "bob-laptop") })
			res := r.post("/requests/req-1/approve", url.Values{"approval_mode": {mode}}, hx...)
			status(t, res, http.StatusUnprocessableEntity)
			contains(t, res, `data-error-code="invalid_argument"`)
			must(t, len(r.f.Admin.Cs) == 0, "no client must be registered")
		})
	}
}

func TestApproveRequestNotPendingIsConflict(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedRequest(f, "req-1", "bob-laptop") })
	status(t, r.post("/requests/req-1/approve", nil, hx...), http.StatusOK)
	res := r.post("/requests/req-1/approve", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, `role="alert"`)
	lacks(t, res, "<html", `id="request-list"`)
	if res.Header.Get("HX-Retarget") != "#toast-root" {
		t.Errorf("HX-Retarget = %q", res.Header.Get("HX-Retarget"))
	}
	res = r.post("/requests/unknown/deny", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`)
}

func TestDenyRequest(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedRequest(f, "req-1", "bob-laptop") })
	res := r.post("/requests/req-1/deny", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="requests"`, "接続要求を拒否しました。")
	lacks(t, res, "bob-laptop", "data-pending-count=\"1\"")
	must(t, len(r.f.Admin.Cs) == 0, "deny must not register a client")
}

func TestRequestsErrorsRenderErrorPages(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Admin.PendingErr = errCleanup })
	res := r.get("/requests")
	status(t, res, http.StatusInternalServerError)
	contains(t, res, "<html", `data-error-code="internal"`, "内部エラーが発生しました。")
	lacks(t, res, "cleanup boom")
}
