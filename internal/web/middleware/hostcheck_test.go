package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestHostAllowed(t *testing.T) {
	tests := []struct {
		name  string
		host  string
		hosts []string
		port  int
		allow bool
	}{
		{"tailnet IPv4", "100.64.0.1:8765", []string{"100.64.0.1"}, 8765, true},
		{"tailnet IPv6", "[fd7a:115c:a1e0::1]:8765", []string{"fd7a:115c:a1e0::1"}, 8765, true},
		{"IPv6 は表記ゆれを同一視", "[FD7A:115C:A1E0:0:0:0:0:1]:8765", []string{"fd7a:115c:a1e0::1"}, 8765, true},
		{"IPv4 射影アドレス", "[::ffff:100.64.0.1]:8765", []string{"100.64.0.1"}, 8765, true},
		{"MagicDNS 名", "host.tail1234.ts.net:8765", []string{"host.tail1234.ts.net"}, 8765, true},
		{"大文字小文字を区別しない", "HOST.Tail1234.ts.net:8765", []string{"host.tail1234.ts.net"}, 8765, true},
		{"末尾のドット", "host.tail1234.ts.net.:8765", []string{"host.tail1234.ts.net."}, 8765, true},
		{"別のポート", "100.64.0.1:9999", []string{"100.64.0.1"}, 8765, false},
		{"ポートなし（80 以外）", "100.64.0.1", []string{"100.64.0.1"}, 8765, false},
		{"ポートなし（設定が 80）", "100.64.0.1", []string{"100.64.0.1"}, 80, true},
		{"ポートなしの IPv6（設定が 80）", "[fd7a:115c:a1e0::1]", []string{"fd7a:115c:a1e0::1"}, 80, true},
		{"ブラケットなしの IPv6 は不正", "fd7a:115c:a1e0::1", []string{"fd7a:115c:a1e0::1"}, 80, false},
		{"ポートが空", "100.64.0.1:", []string{"100.64.0.1"}, 8765, false},
		{"ポートの表記違い", "100.64.0.1:08765", []string{"100.64.0.1"}, 8765, false},
		{"DNS リバインディング（任意のドメイン）", "evil.example.com:8765", []string{"100.64.0.1"}, 8765, false},
		{"サブドメインは別", "x.host.tail1234.ts.net:8765", []string{"host.tail1234.ts.net"}, 8765, false},
		{"前方一致は別", "host.tail1234.ts.net.evil.com:8765", []string{"host.tail1234.ts.net"}, 8765, false},
		{"ループバック", "127.0.0.1:8765", []string{"100.64.0.1"}, 8765, false},
		{"localhost", "localhost:8765", []string{"100.64.0.1"}, 8765, false},
		{"ゾーン付き IPv6", "[fe80::1%25eth0]:8765", []string{"fe80::1"}, 8765, false},
		{"空の Host", "", []string{"100.64.0.1"}, 8765, false},
		{"許可する host がない", "100.64.0.1:8765", nil, 8765, false},
		{"IP の許可リストに名前は入らない", "100.64.0.1:8765", []string{"host.tail1234.ts.net"}, 8765, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostAllowed(tt.host, tt.hosts, tt.port); got != tt.allow {
				t.Fatalf("hostAllowed(%q, %v, %d) = %v, want %v", tt.host, tt.hosts, tt.port, got, tt.allow)
			}
		})
	}
}

func TestHostCheckOrigin(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		origins  []string
		wantCode domain.Code // 空は通過
	}{
		{"Origin なし（非ブラウザ）", testIP + ":8765", nil, ""},
		{"同一オリジン", testIP + ":8765", []string{"http://" + testIP + ":8765"}, ""},
		{"同一オリジン（MagicDNS）", testDNS + ":8765", []string{"http://" + testDNS + ":8765"}, ""},
		{"大文字小文字の違い", testDNS + ":8765", []string{"http://" + "HOST.tail1234.ts.net" + ":8765"}, ""},
		{"別のホスト", testIP + ":8765", []string{"http://" + testDNS + ":8765"}, domain.CodeInvalidOrigin},
		{"別のポート", testIP + ":8765", []string{"http://" + testIP + ":9999"}, domain.CodeInvalidOrigin},
		{"https は別", testIP + ":8765", []string{"https://" + testIP + ":8765"}, domain.CodeInvalidOrigin},
		{"null", testIP + ":8765", []string{"null"}, domain.CodeInvalidOrigin},
		{"空", testIP + ":8765", []string{""}, domain.CodeInvalidOrigin},
		{"外部サイト", testIP + ":8765", []string{"http://evil.example.com"}, domain.CodeInvalidOrigin},
		{"パス付き", testIP + ":8765", []string{"http://" + testIP + ":8765/"}, domain.CodeInvalidOrigin},
		{"ユーザー情報付き", testIP + ":8765", []string{"http://evil@" + testIP + ":8765"}, domain.CodeInvalidOrigin},
		{"Origin が複数", testIP + ":8765", []string{"http://" + testIP + ":8765", "http://evil.example.com"}, domain.CodeInvalidOrigin},
		{"Host が不正なら Origin より先に 400", "evil.example.com:8765", []string{"http://evil.example.com:8765"}, domain.CodeInvalidHost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			h := HostCheck(testHosts)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
			r := httptest.NewRequest("GET", "/api/v1/x", nil)
			r.Host = tt.host
			r.Header["Origin"] = tt.origins
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if tt.wantCode == "" {
				if !reached {
					t.Fatalf("rejected: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if reached {
				t.Fatal("not rejected")
			}
			if p := decodeProblem(t, w); domain.Code(p.Code) != tt.wantCode {
				t.Fatalf("code = %q, want %q", p.Code, tt.wantCode)
			}
		})
	}
}

func TestHostSourceIsReadPerRequest(t *testing.T) {
	hosts := []string{"100.64.0.1"}
	h := HostCheck(func() ([]string, int) { return hosts, 8765 })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	do := func(host string) int {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if do("100.64.0.2:8765") != 400 {
		t.Fatal("unexpected host accepted")
	}
	hosts = []string{"100.64.0.2"} // tailnet IP の変化に追従する
	if do("100.64.0.2:8765") != 200 {
		t.Fatal("updated host rejected")
	}
}

func TestRequireCSRFHeader(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		header map[string][]string
		want   int
	}{
		{"GET は不要", "GET", "/viewer", nil, 200},
		{"HEAD は不要", "HEAD", "/viewer", nil, 200},
		{"OPTIONS は不要", "OPTIONS", "/viewer", nil, 200},
		{"POST にヘッダー", "POST", "/viewer/lease", map[string][]string{CSRFHeader: {"1"}}, 200},
		{"POST にヘッダーなし", "POST", "/viewer/lease", nil, 403},
		{"値が 1 でない", "POST", "/viewer/lease", map[string][]string{CSRFHeader: {"true"}}, 403},
		{"値が空", "POST", "/viewer/lease", map[string][]string{CSRFHeader: {""}}, 403},
		{"PUT", "PUT", "/api/v1/x", nil, 403},
		{"PATCH", "PATCH", "/api/v1/x", nil, 403},
		{"DELETE", "DELETE", "/api/v1/x", nil, 403},
		{"MCP の POST は対象外", "POST", "/mcp", nil, 200},
		{"MCP の DELETE は対象外", "DELETE", "/mcp", nil, 200},
		{"正規化後が MCP でも対象外", "POST", "/a/../mcp", nil, 200},
		{"MCP に見えるだけの別経路は対象", "POST", "/mcpx", nil, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireCSRFHeader()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			r := httptest.NewRequest(tt.method, tt.target, nil)
			for k, v := range tt.header {
				r.Header[k] = v
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := map[string]routeKind{
		"/mcp": routeMCP, "/mcp/": routeMCP, "/mcp/x": routeMCP, "//mcp": routeMCP, "/a/../mcp": routeMCP,
		"/api/v1": routeAPI, "/api/v1/clients": routeAPI, "/api/v1/../v1/x": routeAPI,
		"/ws/screen": routeWS, "/ws": routeWS,
		"/": routeWeb, "/viewer": routeWeb, "/static/app.js": routeWeb, "/mcpx": routeWeb, "/api/v2/x": routeWeb, "/api/v1x": routeWeb,
	}
	for p, want := range tests {
		r := httptest.NewRequest("GET", "http://h/", nil)
		r.URL.Path = p
		if got := classify(r); got != want {
			t.Errorf("classify(%q) = %v, want %v", p, got, want)
		}
	}
}
