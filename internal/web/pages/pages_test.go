package pages_test

import (
	"strings"
	"testing"

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
		`src="/static/js/sse.min.js"`, `<script type="module" src="/static/js/app.js">`,
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
	for _, want := range []string{`<link rel="stylesheet" href="/static/css/app.css"`, `hx-ext="sse,response-targets"`, `sse-connect="/events"`, `X-Puppet-Remo`,
		`id="toast-root"`, `id="modal-root"`, `id="main"`, `<html lang="ja">`} {
		if !strings.Contains(out, want) {
			t.Errorf("layout lacks %q", want)
		}
	}
	// 外部 URL を読み込まない。Lit・Web Component は M2。
	for _, bad := range []string{"http://", "https://", "//cdn", "remo-screen-viewer", "lit"} {
		if strings.Contains(out, bad) {
			t.Errorf("layout must not contain %q", bad)
		}
	}
}

func TestEveryPageRendersCSPCompliantHTMLInBothForms(t *testing.T) {
	chrome := view.Chrome{Active: view.NavClients}
	forms := map[string]string{
		"dashboard": webtest.Render(t, pages.Dashboard(view.Dashboard{Chrome: chrome})),
		"requests":  webtest.Render(t, pages.Requests(view.Requests{Chrome: chrome})),
		"clients":   webtest.Render(t, pages.Clients(view.Clients{Chrome: chrome})),
		"detail":    webtest.Render(t, pages.ClientDetail(view.ClientDetail{Chrome: chrome, BackURL: "/clients", Form: view.TokenForm{ActionURL: "/clients/c1/tokens"}})),
		"settings":  webtest.Render(t, pages.Settings(view.Settings{Chrome: chrome})),
		"error":     webtest.Render(t, pages.Error(view.ErrorPage{Chrome: chrome, Code: "not_found", Title: "x"})),

		"dashboard boosted": webtest.Render(t, pages.DashboardBoosted(view.Dashboard{Chrome: chrome})),
		"requests boosted":  webtest.Render(t, pages.RequestsBoosted(view.Requests{Chrome: chrome})),
		"clients boosted":   webtest.Render(t, pages.ClientsBoosted(view.Clients{Chrome: chrome})),
		"detail boosted":    webtest.Render(t, pages.ClientDetailBoosted(view.ClientDetail{Chrome: chrome, BackURL: "/clients"})),
		"settings boosted":  webtest.Render(t, pages.SettingsBoosted(view.Settings{Chrome: chrome})),
		"error boosted":     webtest.Render(t, pages.ErrorBoosted(view.ErrorPage{Chrome: chrome, Code: "not_found"})),
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
