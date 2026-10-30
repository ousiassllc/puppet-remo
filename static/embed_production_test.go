//go:build production

package static_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/static"
)

// 本番ビルドの go:embed には、レイアウトが読み込むファイルがそろっている（task frontend:build の成果物）。
func TestEmbeddedDistHasEveryAssetTheLayoutLoads(t *testing.T) {
	h, err := static.Handler()
	if err != nil {
		t.Fatal(err)
	}
	get := func(name string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+name, nil))
		return rec
	}
	for _, name := range []string{
		"css/app.css", "js/htmx.min.js", "js/htmx-config.js", "js/response-targets.min.js", "js/sse.min.js", "js/app.js",
	} {
		rec := get(name)
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s: status %d, %d bytes", name, rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
			t.Errorf("%s: Cache-Control %q ETag %q", name, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
		}
	}
	// htmx の設定（CSP と整合させるため eval とインラインのスタイルを無効にする。frontend.md §7.1）。
	cfg := get("js/htmx-config.js").Body.String()
	for _, want := range []string{"allowEval", "includeIndicatorStyles"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("htmx-config.js does not set %s", want)
		}
	}
	// ソースマップは本番に含めない。
	if rec := get("js/app.js.map"); rec.Code != http.StatusNotFound {
		t.Errorf("source map is embedded: status %d", rec.Code)
	}
}
