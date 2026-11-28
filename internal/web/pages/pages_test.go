package pages_test

import (
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ousiassllc/puppet-remo/internal/web/layout"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
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
		`id="toast-root"`, `id="alert-root"`, `id="modal-root"`, `id="main"`, `<html lang="ja">`,
		`<link rel="icon" href="/static/brand/favicon.ico"`, `src="/static/brand/lockup-light.svg"`, `src="/static/brand/lockup-dark.svg"`, `alt="Nefos Remote"`,
		// forced-colors 用の形だけのマスク（#282。app.css の .brand-forced-*）。ロックアップのほうが名前（role="img"）を持つ。
		`class="brand-forced brand-forced-lockup `, `role="img" aria-label="Nefos Remote"></span>`} {
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

var sampleTaskList = view.TaskList{
	Rows:    []view.TaskRow{{ShortID: "00RUN001", DetailURL: "/tasks/t1", Client: "c", PromptExcerpt: "p", Code: "running", Label: "実行中", Tone: view.ToneOK, Steps: "1 / 50", Created: "2026-11-02 12:00", Elapsed: "5秒"}},
	NextURL: "/tasks?cursor=abc",
}

func sampleTaskDetail(chrome view.Chrome) view.TaskDetail {
	return view.TaskDetail{
		Chrome: chrome, ID: "t1", Prompt: "p", BackURL: "/tasks", ResultText: "r", ResultNote: "n",
		Status: view.TaskStatus{Code: "running", Label: "実行中", Tone: view.ToneOK, Steps: "1 / 50", Elapsed: "5秒", CancelURL: "/tasks/t1/cancel"},
		Steps:  []view.TaskStepRow{{No: 1, Route: "fast", RouteLabel: "高速パス", Summary: "s", Result: "ok", AuditURL: "/audit/1", ThumbURL: "/audit/1/thumbnail", Args: []view.AuditArg{{Key: "k", Value: "v"}}}},
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
		"licenses":         webtest.Render(t, pages.Licenses(view.Licenses{Chrome: chrome, Text: "x"})),
		"error":            webtest.Render(t, pages.Error(view.ErrorPage{Chrome: chrome, Code: "not_found", Title: "x"})),
		"tasks":            webtest.Render(t, pages.Tasks(view.TaskListPage{Chrome: chrome, List: sampleTaskList})),
		"task detail":      webtest.Render(t, pages.TaskDetail(sampleTaskDetail(chrome))),

		"dashboard boosted":        webtest.Render(t, pages.DashboardBoosted(view.Dashboard{Chrome: chrome})),
		"requests boosted":         webtest.Render(t, pages.RequestsBoosted(view.Requests{Chrome: chrome})),
		"requests history boosted": webtest.Render(t, pages.RequestsHistoryBoosted(view.RequestsHistory{Chrome: chrome})),
		"logs boosted":             webtest.Render(t, pages.LogsBoosted(view.AppLogPage{Chrome: chrome})),
		"clients boosted":          webtest.Render(t, pages.ClientsBoosted(view.Clients{Chrome: chrome})),
		"detail boosted":           webtest.Render(t, pages.ClientDetailBoosted(view.ClientDetail{Chrome: chrome, BackURL: "/clients"})),
		"settings boosted":         webtest.Render(t, pages.SettingsBoosted(view.Settings{Chrome: chrome})),
		"apps boosted":             webtest.Render(t, pages.AppsBoosted(view.Apps{Chrome: chrome, NewForm: view.AppForm{DOMID: "app-form-new", ActionURL: "/apps", PickURL: "/apps/pick-file"}})),
		"agent boosted":            webtest.Render(t, pages.AgentSettingsBoosted(view.AgentSettings{Chrome: chrome})),
		"licenses boosted":         webtest.Render(t, pages.LicensesBoosted(view.Licenses{Chrome: chrome, Text: "x"})),
		"error boosted":            webtest.Render(t, pages.ErrorBoosted(view.ErrorPage{Chrome: chrome, Code: "not_found"})),
		"tasks boosted":            webtest.Render(t, pages.TasksBoosted(view.TaskListPage{Chrome: chrome, List: sampleTaskList})),
		"task detail boosted":      webtest.Render(t, pages.TaskDetailBoosted(sampleTaskDetail(chrome))),
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

// 起動許可アプリ: h1・説明文・題付きの Card（起動の方式・アプリの追加・拒否リスト）は、どれも 24（--gap-grid-lg。外枠 space-y-6）で並ぶ。
// 以前は Card の積みだけを 48 の内側の枠にしていた（#204）が、題付き Card は部品なので節の間隔にしない。
func TestAppsPageStacksCardsAtGapGridLg(t *testing.T) {
	out := webtest.Render(t, pages.Apps(view.Apps{Chrome: view.Chrome{Active: view.NavApps}, NewForm: view.AppForm{DOMID: "app-form-new", ActionURL: "/apps", PickURL: "/apps/pick-file"}}))
	if !strings.Contains(out, `<div class="max-w-3xl space-y-6">`) || !strings.Contains(out, `</p><div id="launch-mode"`) {
		t.Errorf("the cards must follow the intro directly inside the space-y-6 frame:\n%s", out)
	}
	if strings.Contains(out, "space-y-12") {
		t.Errorf("titled cards must not be separated by 48:\n%s", out)
	}
}

// ダッシュボード: 題付き Card（実行中のタスク）とカードのグリッドの間、グリッド内の横・縦、h1 の下は、どれも 24（--gap-grid-lg。gap-6・外枠 space-y-6）。
// 実行中のタスクが無いとき、空の枠（hidden）は間隔に入らない。
func TestDashboardSeparatesCardsByGapGridLg(t *testing.T) {
	out := webtest.Render(t, pages.Dashboard(view.Dashboard{Chrome: view.Chrome{Active: view.NavDashboard}}))
	for _, want := range []string{
		`<div class="space-y-6"><h1 class="text-page-title font-semibold text-ink">ダッシュボード</h1><div class="flex flex-col gap-6" data-dashboard-sections><div id="running-tasks" hidden>`,
		`<div class="grid grid-cols-1 gap-6 lg:grid-cols-2">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "gap-12") || strings.Contains(out, "gap-4 lg:") {
		t.Errorf("dashboard cards must not use 48 or 16:\n%s", out)
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
	// ガター（S5）はモバイル 16px・デスクトップ 24px（ホスト GUI と同じ）。
	for _, want := range []string{`hx-ext="response-targets"`, `X-Puppet-Remo`, `id="toast-root"`, `id="alert-root"`, `id="viewer-root"`, `<remo-screen-viewer`, `<html lang="ja">`, "space-y-4 p-4 md:p-6", `px-4 text-sm text-ink md:px-6">`} {
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

// 待機・拒否・失効・エラーのページ（FR-W02）は、/static を読まず、インラインの <style>（NoticeStyle）だけで見た目を付ける
// （未登録デバイスには /static も 403）。<style> のハッシュが、応答に付ける CSP（middleware.NoticeCSPValue）の style-src に入っていること。
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
			webtest.AssertNoticeCSPCompliantHTML(t, out, middleware.NoticeCSPValue)
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
	webtest.AssertNoticeCSPCompliantHTML(t, out, middleware.NoticeCSPValue)
	if !strings.Contains(out, `data-error-code="invalid_host"`) || strings.Contains(out, "<b>t</b>") {
		t.Errorf("error page = %s", out)
	}
}

// T6（#214）: 待機ページの見出しの見た目（margin・サイズ・ウェイト）は役割クラス .pg-t に置き、h1 の要素セレクタには置かない（タグを差し替えても見た目が変わらない）。
// 4 種のページとも h1 に class="pg-t" を付ける。
func TestRemoteNoticeHeadingUsesARoleClass(t *testing.T) {
	if !strings.Contains(layout.NoticeStyle, "\n.pg-t {") || strings.Contains(layout.NoticeStyle, "\nh1 ") {
		t.Errorf("NoticeStyle must style the heading with .pg-t, not an h1 selector:\n%s", layout.NoticeStyle)
	}
	for name, comp := range map[string]templ.Component{
		"待機":     pages.Pending(),
		"拒否":     pages.Denied(),
		"失効":     pages.Expired(),
		"エラーページ": pages.RemoteError(view.RemoteError{Code: "invalid_host", Title: "t", Message: "m"}),
	} {
		out := webtest.Render(t, comp)
		if !strings.Contains(out, `<h1 class="pg-t">`) || strings.Contains(out, "<h1>") {
			t.Errorf("%s: h1 lacks class=\"pg-t\":\n%s", name, out)
		}
	}
}
