package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/layout"
)

func TestCSPValueMatchesSpec(t *testing.T) {
	// docs/architecture/frontend.md §7.1・API 仕様 §1 の値と一字一句同じ。
	const want = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	if CSPValue != want {
		t.Errorf("CSPValue = %q", CSPValue)
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(CSPValue, bad) {
			t.Errorf("CSPValue must not contain %q", bad)
		}
	}
}

// 待機ページの CSP は、style-src だけが違う。'unsafe-inline' は許さず、インライン <style> のハッシュだけを通す。
func TestNoticeCSPValueDiffersOnlyInStyleSrc(t *testing.T) {
	want := strings.Replace(CSPValue, "style-src 'self'", "style-src "+layout.NoticeStyleHash(), 1)
	if NoticeCSPValue != want || NoticeCSPValue == CSPValue {
		t.Errorf("NoticeCSPValue = %q, want %q", NoticeCSPValue, want)
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(NoticeCSPValue, bad) {
			t.Errorf("NoticeCSPValue must not contain %q", bad)
		}
	}
	if !strings.Contains(NoticeCSPValue, "style-src 'sha256-") {
		t.Errorf("NoticeCSPValue lacks the style hash: %q", NoticeCSPValue)
	}
}

func TestCSPSetsHeaderOnEveryResponse(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"html": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<p>x</p>"))
		},
		"json": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		},
		"not found":  http.NotFound,
		"no body":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"redirected": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/x", http.StatusFound) },
		"error":      func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			CSP()(h).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if got := w.Header().Get("Content-Security-Policy"); got != CSPValue {
				t.Errorf("Content-Security-Policy = %q", got)
			}
		})
	}
}

// csp を最外に置くと、連鎖の拒否（hostcheck のエラーページ・キルスイッチの 503）にもヘッダーが付く（frontend.md §7.1）。
// HTML のエラーページは待機ページと同じ外枠（インライン <style>）なので、style-src にそのハッシュを足した CSP に置き換わる。
func TestCSPCoversRemoteChainRejections(t *testing.T) {
	k := newKit()
	k.handler = CSP()(k.handler)

	// hostcheck: 許可していない Host（HTML のエラーページ）。
	w := k.do(http.MethodGet, "/viewer", func(r *http.Request) { r.Host = "evil.example:8765" })
	if w.Code != http.StatusBadRequest || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("host check: status %d, content-type %q", w.Code, w.Header().Get("Content-Type"))
	}
	if got := w.Header().Get("Content-Security-Policy"); got != NoticeCSPValue {
		t.Errorf("host check rejection: CSP = %q, want the notice page CSP", got)
	}

	// killswitch: 503 のページ。
	k.kill.active = true
	w = k.do(http.MethodGet, "/viewer", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("kill switch: status %d", w.Code)
	}
	if got := w.Header().Get("Content-Security-Policy"); got != NoticeCSPValue {
		t.Errorf("kill switch rejection: CSP = %q, want the notice page CSP", got)
	}

	// problem+json にも付く（害はない）。
	w = k.do(http.MethodGet, "/api/v1/status", nil)
	if got := w.Header().Get("Content-Security-Policy"); got != CSPValue {
		t.Errorf("problem response: CSP = %q", got)
	}
}
