package static_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/ousiassllc/puppet-remo/static"
)

func newFS() fstest.MapFS {
	return fstest.MapFS{
		"js/app.js":    {Data: []byte("console.log(1)")},
		"css/app.css":  {Data: []byte("body{}")},
		"js/other.min": {Data: []byte("x")},
	}
}

func get(h http.Handler, method, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesWithNoCacheAndETag(t *testing.T) {
	h := static.NewHandler(newFS(), true)
	rec := get(h, http.MethodGet, "/js/app.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	etag := rec.Header().Get("ETag")
	if len(etag) < 4 || etag[0] != '"' {
		t.Fatalf("ETag = %q", etag)
	}
	if got := rec.Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, validation must rely on ETag only", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

// Windows では Go の mime がレジストリから型を読むので、拡張子ごとの型はハンドラが固定する。
func TestContentTypeIsFixedPerExtension(t *testing.T) {
	fsys := fstest.MapFS{}
	want := map[string]string{
		"a.js":     "text/javascript; charset=utf-8",
		"a.css":    "text/css; charset=utf-8",
		"a.js.map": "application/json",
		"a.svg":    "image/svg+xml",
		"a.woff2":  "font/woff2",
		"A.JS":     "text/javascript; charset=utf-8",
	}
	for name := range want {
		fsys[name] = &fstest.MapFile{Data: []byte("x")}
	}
	h := static.NewHandler(fsys, true)
	for name, ct := range want {
		if got := get(h, http.MethodGet, "/"+name).Header().Get("Content-Type"); got != ct {
			t.Errorf("%s: Content-Type = %q, want %q", name, got, ct)
		}
	}
}

func TestIfNoneMatchReturns304(t *testing.T) {
	h := static.NewHandler(newFS(), true)
	etag := get(h, http.MethodGet, "/css/app.css").Header().Get("ETag")
	rec := get(h, http.MethodGet, "/css/app.css", "If-None-Match", etag)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control on 304 = %q", got)
	}
	rec = get(h, http.MethodGet, "/css/app.css", "If-None-Match", `"stale"`)
	if rec.Code != http.StatusOK {
		t.Errorf("stale ETag: status %d", rec.Code)
	}
}

// ビルド ID は dist 全体の内容から決まる: 1 ファイルでも変われば全ファイルの ETag が変わる。
func TestBuildIDChangesWithContent(t *testing.T) {
	a := get(static.NewHandler(newFS(), true), http.MethodGet, "/js/app.js").Header().Get("ETag")
	same := get(static.NewHandler(newFS(), true), http.MethodGet, "/js/app.js").Header().Get("ETag")
	if a != same {
		t.Errorf("same content, different ETag: %s %s", a, same)
	}
	changed := newFS()
	changed["css/app.css"] = &fstest.MapFile{Data: []byte("body{color:red}")}
	b := get(static.NewHandler(changed, true), http.MethodGet, "/js/app.js").Header().Get("ETag")
	if a == b {
		t.Error("ETag must change when any dist file changes")
	}
}

// 開発（ディスク配信）はファイルが変わるたびに ETag も変わる。
func TestDiskModeRecomputesBuildID(t *testing.T) {
	fsys := newFS()
	h := static.NewHandler(fsys, false)
	a := get(h, http.MethodGet, "/js/app.js").Header().Get("ETag")
	fsys["js/app.js"] = &fstest.MapFile{Data: []byte("console.log(2)")}
	b := get(h, http.MethodGet, "/js/app.js").Header().Get("ETag")
	if a == b {
		t.Error("disk mode must recompute the build id")
	}
}

func TestNotFoundAndMethods(t *testing.T) {
	h := static.NewHandler(newFS(), true)
	for _, path := range []string{"/", "/js", "/js/", "/missing.js", "/../go.mod", "/js/../../x", "//etc/passwd"} {
		if rec := get(h, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
		}
	}
	if rec := get(h, http.MethodHead, "/js/app.js"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: status %d body %d", rec.Code, rec.Body.Len())
	}
	rec := get(h, http.MethodPost, "/js/app.js")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: status %d allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestEmptyDistIsAnError(t *testing.T) {
	rec := get(static.NewHandler(fstest.MapFS{}, true), http.MethodGet, "/js/app.js")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
	rec = get(static.NewHandler(fstest.MapFS{"x.js": {Data: nil}}, true), http.MethodGet, "/x.js")
	if rec.Code != http.StatusOK {
		t.Errorf("empty file: status %d", rec.Code)
	}
}
