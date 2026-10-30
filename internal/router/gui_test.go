package router_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ousiassllc/puppet-remo/internal/router"
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
		{http.MethodGet, "/killswitch", []string{"HX-Request", "true"}},
		{http.MethodGet, "/poll", []string{"HX-Request", "true"}},
		{http.MethodPost, "/killswitch/trigger", []string{"HX-Request", "true"}},
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
	for _, p := range []string{"/", "/requests", "/clients", "/settings", "/nope"} {
		if got := do(h, http.MethodGet, p).Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", p, got)
		}
	}
}

// ホスト専用の経路だけがあり、/wails/* は予約され、M1 に無いものと、リモート向けの経路は存在しない。
func TestRoutesAreExactlyTheM1GUISet(t *testing.T) {
	h, _ := newGUI(t)
	for _, p := range []string{"/wails/runtime.js", "/wails/", "/viewer", "/ws/screen", "/mcp", "/api/v1/status", "/audit", "/tasks", "/agent", "/approvals/a", "/update"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, rec.Code)
		}
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
