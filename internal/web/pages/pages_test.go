package pages_test

import (
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

func TestLayoutLoadsOnlyLocalExternalScriptsInOrder(t *testing.T) {
	out := webtest.Render(t, pages.Dashboard(view.Dashboard{Chrome: view.Chrome{Active: view.NavDashboard}}))
	webtest.AssertCSPCompliantHTML(t, out)

	// htmx → htmx-config（classic）→ 拡張 → app（module）の順（frontend.md §7.1）。
	order := []string{
		`src="/static/js/htmx.min.js"`, `src="/static/js/htmx-config.js"`, `src="/static/js/response-targets.min.js"`,
		`<script type="module" src="/static/js/app.js">`,
	}
	prev := -1
	for _, s := range order {
		i := strings.Index(out, s)
		if i < 0 {
			t.Fatalf("missing %s", s)
		}
		if i < prev {
			t.Errorf("%s is out of order", s)
		}
		prev = i
	}
	for _, want := range []string{`<link rel="stylesheet" href="/static/css/app.css"`, `hx-ext="response-targets"`, `X-Puppet-Remo`,
		`id="live-poll"`, `hx-get="/poll"`, `hx-trigger="every 3s"`, `hx-swap="none"`,
		`id="toast-root"`, `id="modal-root"`, `id="main"`, `<html lang="ja">`} {
		if !strings.Contains(out, want) {
			t.Errorf("layout lacks %q", want)
		}
	}
	// 外部 URL を読み込まない。Lit・Web Component は M2。
	for _, bad := range []string{"http://", "https://", "//cdn", "remo-screen-viewer", "lit", "sse-connect", "sse.min.js", "sse-swap"} {
		if strings.Contains(out, bad) {
			t.Errorf("layout must not contain %q", bad)
		}
	}
}

func TestEveryPageRendersCSPCompliantHTMLInBothForms(t *testing.T) {
	chrome := view.Chrome{Active: view.NavClients}
	forms := map[string]string{
		"dashboard":        webtest.Render(t, pages.Dashboard(view.Dashboard{Chrome: chrome})),
		"requests":         webtest.Render(t, pages.Requests(view.Requests{Chrome: chrome})),
		"requests history": webtest.Render(t, pages.RequestsHistory(view.RequestsHistory{Chrome: chrome})),
		"logs":             webtest.Render(t, pages.Logs(view.AppLogPage{Chrome: chrome, Path: `C:\x\puppet-remo.log`})),
		"clients":          webtest.Render(t, pages.Clients(view.Clients{Chrome: chrome})),
		"detail":           webtest.Render(t, pages.ClientDetail(view.ClientDetail{Chrome: chrome, BackURL: "/clients", Form: view.TokenForm{ActionURL: "/clients/c1/tokens"}})),
		"settings":         webtest.Render(t, pages.Settings(view.Settings{Chrome: chrome})),
		"apps":             webtest.Render(t, pages.Apps(view.Apps{Chrome: chrome, NewForm: view.AppForm{DOMID: "app-form-new", ActionURL: "/apps", PickURL: "/apps/pick-file"}})),
		"agent":            webtest.Render(t, pages.AgentSettings(view.AgentSettings{Chrome: chrome})),
		"error":            webtest.Render(t, pages.Error(view.ErrorPage{Chrome: chrome, Code: "not_found", Title: "x"})),

		"dashboard boosted":        webtest.Render(t, pages.DashboardBoosted(view.Dashboard{Chrome: chrome})),
		"requests boosted":         webtest.Render(t, pages.RequestsBoosted(view.Requests{Chrome: chrome})),
		"requests history boosted": webtest.Render(t, pages.RequestsHistoryBoosted(view.RequestsHistory{Chrome: chrome})),
		"logs boosted":             webtest.Render(t, pages.LogsBoosted(view.AppLogPage{Chrome: chrome})),
		"clients boosted":          webtest.Render(t, pages.ClientsBoosted(view.Clients{Chrome: chrome})),
		"detail boosted":           webtest.Render(t, pages.ClientDetailBoosted(view.ClientDetail{Chrome: chrome, BackURL: "/clients"})),
		"settings boosted":         webtest.Render(t, pages.SettingsBoosted(view.Settings{Chrome: chrome})),
		"apps boosted":             webtest.Render(t, pages.AppsBoosted(view.Apps{Chrome: chrome, NewForm: view.AppForm{DOMID: "app-form-new", ActionURL: "/apps", PickURL: "/apps/pick-file"}})),
		"agent boosted":            webtest.Render(t, pages.AgentSettingsBoosted(view.AgentSettings{Chrome: chrome})),
		"error boosted":            webtest.Render(t, pages.ErrorBoosted(view.ErrorPage{Chrome: chrome, Code: "not_found"})),
	}
	for name, out := range forms {
		t.Run(name, func(t *testing.T) {
			webtest.AssertCSPCompliantHTML(t, out)
			full := !strings.HasSuffix(name, "boosted")
			if got := strings.Contains(out, "<html"); got != full {
				t.Errorf("contains <html> = %v, want %v", got, full)
			}
		})
	}
}

// リモートのビューアのページ（FR-W01）: 外部ファイルだけを読み、screen-viewer.js は app.js の後（モジュール）。
func TestRemoteLayoutLoadsViewerScripts(t *testing.T) {
	out := webtest.Render(t, pages.Viewer(view.Viewer{ViewerID: "V", WSURL: "ws://h/ws/screen?viewer_id=V"}))
	webtest.AssertCSPCompliantHTML(t, out)
	prev := -1
	for _, s := range []string{
		`src="/static/js/htmx.min.js"`, `src="/static/js/htmx-config.js"`, `src="/static/js/response-targets.min.js"`,
		`<script type="module" src="/static/js/app.js">`, `<script type="module" src="/static/js/screen-viewer.js">`,
	} {
		i := strings.Index(out, s)
		if i < 0 {
			t.Fatalf("missing %s", s)
		}
		if i < prev {
			t.Errorf("%s is out of order", s)
		}
		prev = i
	}
	for _, want := range []string{`hx-ext="response-targets"`, `X-Puppet-Remo`, `id="toast-root"`, `id="viewer-root"`, `<remo-screen-viewer`, `<html lang="ja">`} {
		if !strings.Contains(out, want) {
			t.Errorf("remote layout lacks %q", want)
		}
	}
	for _, bad := range []string{"http://", "https://", "//cdn", "live-poll", `hx-get="/poll"`, "sse"} {
		if strings.Contains(out, bad) {
			t.Errorf("remote layout contains %q", bad)
		}
	}
}

// 待機・拒否・失効のページ（FR-W02）は、スタイル・スクリプトを読み込まない（未登録デバイスには /static も 403）。
func TestRemoteNoticePages(t *testing.T) {
	tests := []struct {
		name  string
		comp  templ.Component
		state string
		text  string
	}{
		{"待機", pages.Pending(), "pending", "接続要求を送信しました。ホストの許可を待っています。"},
		{"拒否", pages.Denied(), "denied", "接続要求は拒否されました"},
		{"失効", pages.Expired(), "expired", "接続要求の有効期限が切れました"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := webtest.Render(t, tt.comp)
			webtest.AssertCSPCompliantHTML(t, out)
			for _, want := range []string{`data-error-code="device_not_registered"`, `data-connection-state="` + tt.state + `"`, tt.text, `<html lang="ja">`} {
				if !strings.Contains(out, want) {
					t.Errorf("page lacks %q:\n%s", want, out)
				}
			}
			for _, bad := range []string{"<script", "<link", "http://"} {
				if strings.Contains(out, bad) {
					t.Errorf("notice page contains %q", bad)
				}
			}
		})
	}
	out := webtest.Render(t, pages.RemoteError(view.RemoteError{Code: "invalid_host", Title: "<b>t</b>", Message: "m"}))
	if !strings.Contains(out, `data-error-code="invalid_host"`) || strings.Contains(out, "<b>t</b>") {
		t.Errorf("error page = %s", out)
	}
}
