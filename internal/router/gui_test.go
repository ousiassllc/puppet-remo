package router_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/router"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
	"github.com/ousiassllc/puppet-remo/static"
)

func newGUI(t *testing.T) (http.Handler, *guitest.Fakes) {
	t.Helper()
	f := guitest.New()
	d := f.Deps()
	d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := gui.New(d)
	if err != nil {
		t.Fatal(err)
	}
	dist := fstest.MapFS{
		"js/app.js":         {Data: []byte("export {}")},
		"css/app.css":       {Data: []byte("body{}")},
		"js/htmx.min.js":    {Data: []byte("htmx")},
		"js/htmx-config.js": {Data: []byte("htmx.config.allowEval=false")},
	}
	r, err := router.NewGUI(router.GUIConfig{Handler: h, Static: static.NewHandler(dist, true)})
	if err != nil {
		t.Fatal(err)
	}
	return r, f
}

func do(h http.Handler, method, target string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewGUIRequiresDependencies(t *testing.T) {
	if _, err := router.NewGUI(router.GUIConfig{}); err == nil {
		t.Error("NewGUI with no dependencies must fail")
	}
}

// すべての応答（ページ・フラグメント・エラー・静的ファイル・404）に CSP が付く（NFR-S19）。
func TestEveryResponseCarriesCSP(t *testing.T) {
	h, _ := newGUI(t)
	targets := []struct {
		method, path string
		headers      []string
	}{
		{http.MethodGet, "/", nil},
		{http.MethodGet, "/requests", nil},
		{http.MethodGet, "/clients", nil},
		{http.MethodGet, "/clients/unknown", nil},
		{http.MethodGet, "/settings", nil},
		{http.MethodGet, "/update", nil},
		{http.MethodPost, "/update/check", []string{"HX-Request", "true"}},
		{http.MethodGet, "/apps", nil},
		{http.MethodGet, "/apps/new", nil},
		{http.MethodGet, "/apps/unknown/edit", nil},
		{http.MethodGet, "/apps/unknown/delete/confirm", []string{"HX-Request", "true"}},
		{http.MethodPost, "/apps/pick-file", []string{"HX-Request", "true"}},
		{http.MethodGet, "/killswitch", []string{"HX-Request", "true"}},
		{http.MethodGet, "/poll", []string{"HX-Request", "true"}},
		{http.MethodPost, "/killswitch/trigger", []string{"HX-Request", "true"}},
		{http.MethodPost, "/lease/revoke", []string{"HX-Request", "true"}},
		{http.MethodGet, "/lease/revoke/confirm", []string{"HX-Request", "true"}},
		{http.MethodGet, "/tasks", nil},
		{http.MethodGet, "/tasks/unknown", nil},
		{http.MethodPost, "/tasks/unknown/cancel", []string{"HX-Request", "true"}},
		{http.MethodGet, "/tasks/unknown/cancel/confirm", []string{"HX-Request", "true"}},
		{http.MethodPost, "/requests/x/approve", []string{"HX-Request", "true"}},
		{http.MethodGet, "/static/js/app.js", nil},
		{http.MethodGet, "/static/missing.js", nil},
		{http.MethodGet, "/nope", nil},
		{http.MethodGet, "/wails/runtime.js", nil},
	}
	for _, tt := range targets {
		rec := do(h, tt.method, tt.path, tt.headers...)
		if got := rec.Header().Get("Content-Security-Policy"); got != middleware.CSPValue {
			t.Errorf("%s %s (%d): CSP = %q", tt.method, tt.path, rec.Code, got)
		}
	}
}

func TestStaticAssetsUseNoCacheAndETag(t *testing.T) {
	h, _ := newGUI(t)
	rec := do(h, http.MethodGet, "/static/js/app.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "export {}" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if rec := do(h, http.MethodGet, "/static/js/app.js", "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status %d, want 304", rec.Code)
	}
	if rec := do(h, http.MethodHead, "/static/css/app.css"); rec.Code != http.StatusOK {
		t.Errorf("HEAD: status %d", rec.Code)
	}
	for _, p := range []string{"/static/", "/static", "/static/../go.mod", "/static/js"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, rec.Code)
		}
	}
}

// 動的な応答（トークンの平文を含みうる）はキャッシュさせない。
func TestDynamicResponsesAreNoStore(t *testing.T) {
	h, _ := newGUI(t)
	for _, p := range []string{"/", "/requests", "/clients", "/settings", "/update", "/apps", "/agent", "/nope"} {
		if got := do(h, http.MethodGet, p).Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", p, got)
		}
	}
}

// 監査ログの閲覧（M3。FR-AU05）の経路は GUI にある。
func TestGUIServesTheAuditRoutes(t *testing.T) {
	h, _ := newGUI(t)
	for _, p := range []string{"/audit", "/audit?kind=tool&limit=10"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", p, rec.Code)
		}
	}
	for _, p := range []string{"/audit/1", "/audit/1/thumbnail"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusNotFound { // 偽の AuditReader は空
			t.Errorf("GET %s: status %d, want 404", p, rec.Code)
		}
	}
}

// 更新（M3。FR-H09）の経路は GUI にある。確認・適用は POST で、GET では動かない。
func TestGUIServesTheUpdateRoutes(t *testing.T) {
	h, f := newGUI(t)
	f.Updates.ApplyErr = service.ErrUpdateUnavailable
	if rec := do(h, http.MethodGet, "/update"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="update-panel"`) {
		t.Errorf("GET /update: status %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/update/check", "HX-Request", "true"); rec.Code != http.StatusOK {
		t.Errorf("POST /update/check: status %d, want 200", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/update/apply", "HX-Request", "true"); rec.Code != http.StatusConflict { // 適用できる更新が無い
		t.Errorf("POST /update/apply: status %d, want 409", rec.Code)
	}
	for _, p := range []string{"/update/check", "/update/apply"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404 (actions are POST only)", p, rec.Code)
		}
	}
	if len(f.Updates.Applied) != 0 {
		t.Error("an update was applied without a verified release")
	}
}

// ログフォルダーを開く（#54）の経路は GUI にあり、POST だけ（GET では動かない）。開く場所は経路にもリクエストにも無い。
func TestGUIServesTheOpenLogFolderRoute(t *testing.T) {
	h, f := newGUI(t)
	if rec := do(h, http.MethodPost, "/logs/open-folder", "HX-Request", "true"); rec.Code != http.StatusOK {
		t.Errorf("POST /logs/open-folder: status %d, want 200", rec.Code)
	}
	if f.Logs.OpenFolderCalls != 1 {
		t.Errorf("OpenFolder calls = %d, want 1", f.Logs.OpenFolderCalls)
	}
	if rec := do(h, http.MethodGet, "/logs/open-folder"); rec.Code != http.StatusNotFound || f.Logs.OpenFolderCalls != 1 {
		t.Errorf("GET /logs/open-folder: status %d, calls %d, want 404 and no call (POST only)", rec.Code, f.Logs.OpenFolderCalls)
	}
}

// ホスト専用の経路だけがあり、/wails/* は予約され、M1 に無いものと、リモート向けの経路は存在しない。
func TestRoutesAreExactlyTheM1GUISet(t *testing.T) {
	h, _ := newGUI(t)
	for _, p := range []string{"/wails/runtime.js", "/wails/", "/viewer", "/ws/screen", "/mcp", "/api/v1/status", "/tasks/", "/tasks/t1/cancel"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, rec.Code)
		}
	}
	// エージェントのタスク（M4。FR-H10）は GUI ルーターにある。一覧は 200、無い id の詳細は 404（ハンドラの応答）、
	// 中断は POST だけ（GET は経路なしの 404）、無い id の中断は 404（エラーのアラート）。
	if rec := do(h, http.MethodGet, "/tasks"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="task-list"`) {
		t.Errorf("GET /tasks: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/tasks/nope"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `data-error-code="not_found"`) {
		t.Errorf("GET /tasks/nope: status %d, want 404 with data-error-code=not_found", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/tasks/nope/cancel", "HX-Request", "true"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `data-error-code="not_found"`) {
		t.Errorf("POST /tasks/nope/cancel: status %d, want 404 with data-error-code=not_found", rec.Code)
	}
	// 中断・強制剥奪の確認モーダル（GET）も GUI ルーターにある。無い id は 404、保持者がいなければ 409（どちらもハンドラの応答。経路なしの 404 と区別する）。
	if rec := do(h, http.MethodGet, "/tasks/nope/cancel/confirm", "HX-Request", "true"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `data-error-code="not_found"`) {
		t.Errorf("GET /tasks/nope/cancel/confirm: status %d, want 404 with data-error-code=not_found", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/lease/revoke/confirm", "HX-Request", "true"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `data-error-code="conflict"`) {
		t.Errorf("GET /lease/revoke/confirm: status %d, want 409 with data-error-code=conflict", rec.Code)
	}
	// 承認ダイアログ（M3。#68）は GUI ルーターにある。承認要求が無い id は 410 approval_timeout。
	if rec := do(h, http.MethodGet, "/approvals/a"); rec.Code != http.StatusGone {
		t.Errorf("GET /approvals/a: status %d, want 410", rec.Code)
	}
	// 末尾のスラッシュを別の URL に飛ばさない（リダイレクトしない）。
	if rec := do(h, http.MethodGet, "/requests/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /requests/: status %d, want 404", rec.Code)
	}
	// 登録していないメソッドは 405 ではなく 404（経路が存在しないように見せる）。
	if rec := do(h, http.MethodPost, "/requests"); rec.Code != http.StatusNotFound {
		t.Errorf("POST /requests: status %d, want 404", rec.Code)
	}
}

func TestPagesRenderThroughRouter(t *testing.T) {
	h, _ := newGUI(t)
	rec := do(h, http.MethodGet, "/")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "<html") || !strings.Contains(body, `src="/static/js/htmx.min.js"`) {
		t.Fatalf("status %d body %.200s", rec.Code, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// 起動許可アプリの管理（M1.5。FR-H12）は GUI ルーターだけにある。各ルートが登録されている（404 の「経路なし」ではなく、
// ハンドラが応答する）。リモートルーターに同名の経路が無いことは remote_test.go が固定する。
func TestGUIRegistersTheAppRoutes(t *testing.T) {
	h, f := newGUI(t)
	f.Apps.Rows = append(f.Apps.Rows, domain.LaunchableApp{ID: "app1", Label: "メモ帳", ExePath: `C:\Windows\notepad.exe`})
	for _, tt := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/apps", http.StatusOK},
		{http.MethodGet, "/apps/new", http.StatusOK},
		{http.MethodGet, "/apps/app1/edit", http.StatusOK},
		{http.MethodGet, "/apps/app1/delete/confirm", http.StatusOK},
		{http.MethodPost, "/apps/pick-file", http.StatusNoContent}, // 既定の偽のダイアログはキャンセルする
		{http.MethodPut, "/apps/missing", http.StatusNotFound},     // ハンドラが返す not_found（経路なしのページではない）
		{http.MethodDelete, "/apps/app1", http.StatusOK},
	} {
		rec := do(h, tt.method, tt.path, "HX-Request", "true")
		if rec.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.want)
		}
	}
	if len(f.Apps.Deleted) != 1 || f.Apps.Picks != 1 {
		t.Errorf("deleted=%v picks=%d", f.Apps.Deleted, f.Apps.Picks)
	}
	if rec := do(h, http.MethodPut, "/apps/missing", "HX-Request", "true"); !strings.Contains(rec.Body.String(), `data-error-code="not_found"`) {
		t.Errorf("PUT unknown: %s", rec.Body)
	}
}

// GUI の状態変更リクエストの本文は GUIMaxBodyBytes までで、超えたら 413 でハンドラに届かない（何も保存しない）。
// 上限ちょうどまでは通る。GET は本文を見ない。
func TestGUILimitsStateChangingRequestBodies(t *testing.T) {
	h, f := newGUI(t)
	post := func(method, target string, size int) *httptest.ResponseRecorder {
		body := "label=" + strings.Repeat("a", size-len("label="))
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/apps"}, {http.MethodPut, "/apps/app1"}, {http.MethodPost, "/apps/pick-file"}, {http.MethodPut, "/settings"},
	} {
		if rec := post(tt.method, tt.path, router.GUIMaxBodyBytes+1); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s over the limit = %d, want 413", tt.method, tt.path, rec.Code)
		}
	}
	if len(f.Apps.Created) != 0 || f.Apps.Picks != 0 {
		t.Errorf("an oversized request reached a handler: created=%v picks=%d", f.Apps.Created, f.Apps.Picks)
	}

	// 上限ちょうどはハンドラまで届く（検証エラーなどで 413 以外が返る）。
	if rec := post(http.MethodPost, "/apps", router.GUIMaxBodyBytes); rec.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("POST /apps at the limit = %d, want it to reach the handler", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/apps"); rec.Code != http.StatusOK {
		t.Errorf("GET /apps = %d", rec.Code)
	}
}

// エージェント設定（M4。FR-H11）の経路は GUI にある。API キーの値は応答のどこにも出ず、キーの経路はキャッシュさせない。
func TestGUIServesTheAgentRoutes(t *testing.T) {
	h, f := newGUI(t)
	const canary = "sk-canary-router-0123456789"
	if rec := do(h, http.MethodGet, "/agent"); rec.Code != http.StatusOK {
		t.Errorf("GET /agent: status %d, want 200", rec.Code)
	}
	// キー未登録の削除の確認は 409。
	if rec := do(h, http.MethodGet, "/agent/api-key/delete/confirm", "HX-Request", "true"); rec.Code != http.StatusConflict {
		t.Errorf("GET /agent/api-key/delete/confirm: status %d, want 409", rec.Code)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPut, "/agent"}, {http.MethodPut, "/agent/api-key"}, {http.MethodDelete, "/agent/api-key"},
		{http.MethodPut, "/agent/fastpath"}, {http.MethodPut, "/agent/fastpath/api-key"}, {http.MethodDelete, "/agent/fastpath/api-key"},
	} {
		if rec := do(h, route.method, route.path, "HX-Request", "true"); rec.Code == http.StatusNotFound {
			t.Errorf("%s %s is not registered", route.method, route.path)
		}
	}
	// キーは本文だけで受ける。URL に付けても登録されない（本文が空なので 422）。
	rec := do(h, http.MethodPut, "/agent/api-key?api_key="+canary, "HX-Request", "true")
	if rec.Code != http.StatusUnprocessableEntity || f.AgentSettings.State().OpenAIKeyRegistered {
		t.Errorf("query api_key: status %d registered=%v", rec.Code, f.AgentSettings.State().OpenAIKeyRegistered)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	// 本文で登録すると 200。応答にキーは出ない。
	rec = doBody(h, http.MethodPut, "/agent/api-key", "api_key="+canary)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), canary) {
		t.Errorf("PUT /agent/api-key: status %d body %.300s", rec.Code, rec.Body.String())
	}
	if !f.AgentSettings.State().OpenAIKeyRegistered {
		t.Error("the key was not registered")
	}
	// 本文の上限（1 MiB）を超えるキーは 413 で、登録されない。
	rec = doBody(h, http.MethodPut, "/agent/fastpath/api-key", "api_key="+strings.Repeat("a", router.GUIMaxBodyBytes+1))
	if rec.Code != http.StatusRequestEntityTooLarge || f.AgentSettings.State().TypeSafeKeyRegistered {
		t.Errorf("oversized key body: status %d registered=%v", rec.Code, f.AgentSettings.State().TypeSafeKeyRegistered)
	}
}

// doBody はフォームの本文つきのリクエストを送る。
func doBody(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
