package gui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

func TestClientsPage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedClient(f, "c1", "alice-desktop", true, true, false)
		seedClient(f, "c2", "alice-laptop", false, false, true)
	})
	res := r.get("/clients")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", `id="client-c1"`, "alice-desktop", `id="client-c2"`, "alice-laptop", "alice@example.com", "承認モード: 常時承認",
		`href="/clients/c1"`, `hx-get="/clients/c1/delete/confirm"`)
	// 有効・無効の切り替えは、現在の状態の逆だけを描画する。
	c1 := between(t, res.Body, `id="client-c1"`, `id="client-c2"`)
	for _, want := range []string{`data-flag="web_enabled"`, `data-flag="mcp_enabled"`, `data-flag="agent_enabled"`} {
		if !strings.Contains(c1, want) {
			t.Errorf("client c1 card lacks %s", want)
		}
	}
	if n := strings.Count(c1, "無効にする"); n != 2 { // web・mcp は有効
		t.Errorf("c1: 無効にする count = %d, want 2", n)
	}
	if n := strings.Count(c1, "有効にする"); n != 1 { // agent は無効
		t.Errorf("c1: 有効にする count = %d, want 1", n)
	}
	// 切り替えの送信値は現在の状態の逆。
	if !strings.Contains(c1, `hx-vals="{&#34;web_enabled&#34;:&#34;false&#34;}"`) || !strings.Contains(c1, `hx-vals="{&#34;agent_enabled&#34;:&#34;true&#34;}"`) {
		t.Errorf("toggle values are wrong:\n%s", c1)
	}
}

func between(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	j := strings.Index(s, to)
	if i < 0 || j < i {
		t.Fatalf("cannot slice %q..%q", from, to)
	}
	return s[i:j]
}

func TestClientsEmptyState(t *testing.T) {
	res := newRig(t).get("/clients")
	contains(t, res, `data-empty="clients"`, "登録済みのクライアントはありません")
	lacks(t, res, `data-client=`)
}

func TestClientsFragmentVariants(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })
	frag := r.get("/clients", hx...)
	contains(t, frag, `id="client-list"`, "alice-desktop")
	lacks(t, frag, "<html")
	boosted := r.get("/clients", "HX-Request", "true", "HX-Boosted", "true")
	contains(t, boosted, "<title>クライアント管理 - puppet-remo</title>", `id="client-list"`)
	lacks(t, boosted, "<html")
}

func TestUpdateClient(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })

	res := r.do(http.MethodPatch, "/clients/c1", url.Values{"mcp_enabled": {"false"}}, hx...)
	status(t, res, http.StatusOK)
	// カードのフラグメント: 一覧のカードなので削除と詳細の導線を含み、MCP は「有効にする」に変わる。
	contains(t, res, `id="client-c1"`, `hx-get="/clients/c1/delete/confirm"`)
	lacks(t, res, "<html", `id="client-list"`)
	mcp := between(t, res.Body, `data-flag="mcp_enabled"`, `data-flag="agent_enabled"`)
	if !strings.Contains(mcp, "有効にする") || strings.Contains(mcp, "無効にする") {
		t.Errorf("mcp flag after disabling:\n%s", mcp)
	}
	if got := r.f.Admin.Updates; len(got) != 1 || got[0].MCPEnabled == nil || *got[0].MCPEnabled || got[0].WebEnabled != nil || got[0].AgentEnabled != nil {
		t.Errorf("updates = %+v", got)
	}
	if r.f.Admin.Cs[0].MCPEnabled || !r.f.Admin.Cs[0].WebEnabled {
		t.Errorf("client = %+v", r.f.Admin.Cs[0])
	}

	// 詳細ページからの更新は、削除・詳細の導線を出さないカードを返し、切り替えの PATCH 先に view=detail を持つ。
	res = r.do(http.MethodPatch, "/clients/c1?view=detail", url.Values{"agent_enabled": {"true"}}, hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, "/delete/confirm", `href="/clients/c1"`)
	contains(t, res, `hx-patch="/clients/c1?view=detail"`)
}

func TestUpdateClientErrors(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })
	tests := []struct {
		name   string
		target string
		form   url.Values
		status int
		code   string
	}{
		{"no field", "/clients/c1", url.Values{}, http.StatusUnprocessableEntity, "invalid_argument"},
		{"not a bool", "/clients/c1", url.Values{"web_enabled": {"maybe"}}, http.StatusUnprocessableEntity, "invalid_argument"},
		{"unknown client", "/clients/zzz", url.Values{"web_enabled": {"true"}}, http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := r.do(http.MethodPatch, tt.target, tt.form, hx...)
			status(t, res, tt.status)
			contains(t, res, `data-error-code="`+tt.code+`"`)
			lacks(t, res, "<html")
		})
	}
	must(t, len(r.f.Admin.Updates) == 0, "failed updates must not reach the service")
}

func TestClientDetailPage(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	past, future, revokedAt := now.Add(-time.Hour), now.Add(30*24*time.Hour), now.Add(-2*time.Hour)
	r := newRig(t, func(f *guitest.Fakes) {
		seedClient(f, "c1", "alice-desktop", true, true, false)
		seedToken(f, "c1", "t-active", "claude-desktop", &future, nil)
		seedToken(f, "c1", "t-forever", "ci-bot", nil, nil)
		seedToken(f, "c1", "t-revoked", "old-laptop", &future, &revokedAt)
		seedToken(f, "c1", "t-expired", "expired-one", &past, nil)
	})
	res := r.get("/clients/c1")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "alice-desktop", `hx-post="/clients/c1/tokens"`, `name="label"`, `name="expires_in_days"`,
		`<option value="90" selected>`, "無期限", `href="/clients"`,
		"prm_AbCd…", "claude-desktop", "ci-bot", "old-laptop", "expired-one", "有効", "失効済み", "期限切れ", "2026-11-29 12:00")
	// 一覧のカードではないので、削除と詳細への導線は出さない。
	lacks(t, res, "/delete/confirm", `href="/clients/c1"`)

	// 失効のボタンは有効なトークンにだけ出す（失効済み・期限切れには出さない）。
	contains(t, res, `hx-get="/clients/c1/tokens/t-active/revoke/confirm"`, `hx-get="/clients/c1/tokens/t-forever/revoke/confirm"`)
	lacks(t, res, "t-revoked/revoke", "t-expired/revoke")

	// 平文はどこにも出ない（接頭辞のみ）。
	lacks(t, res, "data-token-plaintext", "issued-token")
}

func TestClientDetailUnknownClient(t *testing.T) {
	res := newRig(t).get("/clients/zzz")
	status(t, res, http.StatusNotFound)
	contains(t, res, "<html", `data-error-code="not_found"`)
	boosted := newRig(t).get("/clients/zzz", "HX-Request", "true", "HX-Boosted", "true")
	status(t, boosted, http.StatusNotFound)
	lacks(t, boosted, "<html")
}

func TestDeleteClient(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedClient(f, "c1", "alice-desktop", true, true, false)
		seedClient(f, "c2", "alice-laptop", true, true, false)
	})
	// 確認のモーダルはサーバーが描画し、確定のボタンだけが DELETE を送る。
	conf := r.get("/clients/c1/delete/confirm", hx...)
	status(t, conf, http.StatusOK)
	contains(t, conf, `role="dialog"`, "alice-desktop", `hx-delete="/clients/c1"`, `hx-target="#client-list"`, "削除する")
	lacks(t, conf, "<html")
	must(t, len(r.f.Admin.Cs) == 2, "confirmation must not delete")

	res := r.do(http.MethodDelete, "/clients/c1", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="client-list"`, "alice-laptop", `id="modal-root" hx-swap-oob="true"`, "クライアントを削除しました。")
	lacks(t, res, "alice-desktop", `id="client-c1"`)
	if len(r.f.Admin.Cs) != 1 {
		t.Errorf("clients = %+v", r.f.Admin.Cs)
	}

	for _, res := range []resp{r.get("/clients/c1/delete/confirm", hx...), r.do(http.MethodDelete, "/clients/c1", nil, hx...)} {
		status(t, res, http.StatusNotFound)
		contains(t, res, `data-error-code="not_found"`)
	}
}

func TestSidebarHighlightsCurrentPage(t *testing.T) {
	for path, label := range map[string]string{"/": "ダッシュボード", "/requests": "接続要求", "/clients": "クライアント", "/settings": "設定"} {
		res := newRig(t).get(path)
		nav := between(t, res.Body, `id="sidebar-nav"`, `</nav>`)
		if n := strings.Count(nav, `aria-current="page"`); n != 1 {
			t.Errorf("%s: aria-current count = %d", path, n)
		}
		cur := nav[strings.Index(nav, `aria-current="page"`):]
		cur = cur[:strings.Index(cur, "</a>")]
		if !strings.Contains(cur, label) {
			t.Errorf("%s: highlighted item does not contain %q: %s", path, label, cur)
		}
	}
}

// hx-boost の遷移が 4xx/5xx を返すと、htmx は既定では差し替えない。response-targets の hx-target-error で
// 境界のリンクごとに #main を指定し、エラーページを表示する。
func TestBoostedLinksTargetMainForErrorPages(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })
	for _, path := range []string{"/", "/clients"} {
		res := r.get(path)
		contains(t, res, `hx-ext="response-targets"`)
		nav := between(t, res.Body, `id="sidebar-nav"`, `</nav>`)
		if got, want := strings.Count(nav, `hx-target-error="#main"`), strings.Count(nav, `hx-boost="true"`); got != want || got == 0 {
			t.Errorf("%s: sidebar links with hx-target-error = %d, boosted links = %d", path, got, want)
		}
	}
	// ページ内の boost のリンク（ダッシュボードの導線・クライアント一覧・詳細の戻り）にも付く。
	for _, path := range []string{"/", "/clients", "/clients/c1"} {
		body := r.get(path).Body
		main := body[strings.Index(body, `<main`):]
		if got, want := strings.Count(main, `hx-target-error="#main"`), strings.Count(main, `hx-boost="true"`); got != want {
			t.Errorf("%s: main links with hx-target-error = %d, boosted links = %d", path, got, want)
		}
	}
}

// 承認モード（FR-H03・H04）: カードに 3 つの選択肢（現在の値が選択済み）を出し、PATCH で変える。変更で approved_at が NULL に戻る。
func TestClientCardApprovalMode(t *testing.T) {
	approvedAt := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	r := newRig(t, func(f *guitest.Fakes) {
		seedClient(f, "c1", "alice-desktop", true, true, false)
		c := seedClient(f, "c2", "bob-laptop", true, true, false)
		_ = c
		f.Admin.Cs[1].ApprovalMode = domain.ApprovalFirstTime
		f.Admin.Cs[1].ApprovedAt = &approvedAt
	})

	list := r.get("/clients")
	status(t, list, http.StatusOK)
	c1 := between(t, list.Body, `id="client-c1"`, `id="client-c2"`)
	for _, want := range []string{
		`hx-patch="/clients/c1"`, `name="approval_mode"`, `<option value="always" selected>常時承認</option>`,
		`<option value="first_time">初回のみ承認</option>`, `<option value="auto">自動許可</option>`, "承認モード: 常時承認",
	} {
		if !strings.Contains(c1, want) {
			t.Errorf("card c1 does not contain %q\n%s", want, c1)
		}
	}
	// always のクライアントには「初回のみ」の状態も取り消しも出ない。
	for _, bad := range []string{"data-first-time-status", "approval/reset", "承認を取り消す"} {
		if strings.Contains(c1, bad) {
			t.Errorf("card c1 unexpectedly contains %q", bad)
		}
	}
	// first_time で承認済みなら、承認済み（日時）と取り消しのボタンを出す。
	contains(t, list, `<option value="first_time" selected>初回のみ承認</option>`, "承認済み（2026-10-30 11:00", `hx-post="/clients/c2/approval/reset"`, "承認を取り消す")

	res := r.do(http.MethodPatch, "/clients/c1", url.Values{"approval_mode": {"first_time"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="client-c1"`, `<option value="first_time" selected>初回のみ承認</option>`, "承認モード: 初回のみ承認", "未承認（次のリース取得で承認が必要です）")
	lacks(t, res, "<html", "承認を取り消す")
	if got := r.f.Admin.Updates; len(got) != 1 || got[0].ApprovalMode == nil || *got[0].ApprovalMode != domain.ApprovalFirstTime {
		t.Fatalf("updates = %+v", got)
	}

	// モードを変えると、承認済みだった first_time の approved_at が NULL に戻る。
	res = r.do(http.MethodPatch, "/clients/c2", url.Values{"approval_mode": {"auto"}}, hx...)
	status(t, res, http.StatusOK)
	if r.f.Admin.Cs[1].ApprovedAt != nil || r.f.Admin.Cs[1].ApprovalMode != domain.ApprovalAuto {
		t.Errorf("client = %+v, want auto with approved_at NULL", r.f.Admin.Cs[1])
	}

	for _, bad := range []string{"bogus", "", "Always"} {
		res := r.do(http.MethodPatch, "/clients/c1", url.Values{"approval_mode": {bad}}, hx...)
		status(t, res, http.StatusUnprocessableEntity)
		contains(t, res, `data-error-code="invalid_argument"`)
		lacks(t, res, "<html")
	}
	if len(r.f.Admin.Updates) != 2 {
		t.Errorf("an invalid approval_mode reached the service: %+v", r.f.Admin.Updates)
	}
}

// POST /clients/{id}/approval/reset: first_time で承認済みのときだけ approved_at を NULL に戻す。それ以外は 409、無ければ 404。
func TestResetClientApproval(t *testing.T) {
	approvedAt := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	r := newRig(t, func(f *guitest.Fakes) {
		seedClient(f, "c1", "alice-desktop", true, true, false)
		seedClient(f, "c2", "bob-laptop", true, true, false)
		f.Admin.Cs[1].ApprovalMode = domain.ApprovalFirstTime
		f.Admin.Cs[1].ApprovedAt = &approvedAt
	})

	res := r.post("/clients/c2/approval/reset", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="client-c2"`, "未承認（次のリース取得で承認が必要です）", "承認を取り消しました")
	lacks(t, res, "承認を取り消しました。次のリース取得から")
	lacks(t, res, "<html", "承認を取り消す</button>")
	if r.f.Admin.Cs[1].ApprovedAt != nil || r.f.Admin.Cs[1].ApprovalMode != domain.ApprovalFirstTime {
		t.Errorf("client = %+v, want first_time with approved_at NULL", r.f.Admin.Cs[1])
	}
	// 詳細ページからはカードの PATCH 先に view=detail を保つ。
	r.f.Admin.Cs[1].ApprovedAt = &approvedAt
	res = r.post("/clients/c2/approval/reset?view=detail", nil, hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, "/delete/confirm")
	contains(t, res, `hx-patch="/clients/c2?view=detail"`)

	// 取り消すものが無い（未承認の first_time・always）は 409、無いクライアントは 404。
	for _, id := range []string{"c2", "c1"} {
		res := r.post("/clients/"+id+"/approval/reset", nil, hx...)
		status(t, res, http.StatusConflict)
		contains(t, res, `data-error-code="conflict"`)
	}
	res = r.post("/clients/zzz/approval/reset", nil, hx...)
	status(t, res, http.StatusNotFound)
	contains(t, res, `data-error-code="not_found"`)
}
