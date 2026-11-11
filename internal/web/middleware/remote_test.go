package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func decodeProblem(t *testing.T, w *httptest.ResponseRecorder) problem {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body %q)", ct, w.Body.String())
	}
	var p problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not JSON: %v: %q", err, w.Body.String())
	}
	if p.Type != "urn:puppet-remo:error:"+p.Code || p.Status != w.Code || p.Title == "" || p.Detail == "" {
		t.Fatalf("problem = %+v (status %d)", p, w.Code)
	}
	return p
}

// TestRemoteChainOrder は、同時に複数の検査に失敗するリクエストが、連鎖の順に最初の失敗で止まり、
// 以降の段（WhoIs・Authz・ハンドラ）が呼ばれないことを確かめる。
func TestRemoteChainOrder(t *testing.T) {
	badHost := func(r *http.Request) { r.Host = "evil.example.com:8765" }
	badOrigin := func(r *http.Request) { r.Header.Set("Origin", "http://evil.example.com") }

	cases := []struct {
		name     string
		method   string
		target   string
		setup    func(k *kit, r *http.Request)
		wantCode domain.Code
		wantLog  string
	}{
		{
			name: "全部不正なら Host", method: "POST", target: "/api/v1/x",
			setup: func(k *kit, r *http.Request) {
				badHost(r)
				badOrigin(r)
				k.kill.active = true
				k.whois.err = service.ErrWhoIsFailed
				k.auth.allowErr = service.ErrDeviceNotRegistered
			},
			wantCode: domain.CodeInvalidHost, wantLog: "",
		},
		{
			name: "Host が正しければ Origin", method: "POST", target: "/api/v1/x",
			setup: func(k *kit, r *http.Request) {
				badOrigin(r)
				k.kill.active = true
				k.whois.err = service.ErrWhoIsFailed
			},
			wantCode: domain.CodeInvalidOrigin, wantLog: "",
		},
		{
			name: "Origin が正しければ X-Puppet-Remo", method: "POST", target: "/api/v1/x",
			setup: func(k *kit, r *http.Request) {
				k.kill.active = true
				k.whois.err = service.ErrWhoIsFailed
			},
			wantCode: domain.CodeMissingHeader, wantLog: "",
		},
		{
			name: "ヘッダーがあればキルスイッチ（WhoIs・許可リストを呼ばない）", method: "POST", target: "/api/v1/x",
			setup: func(k *kit, r *http.Request) {
				r.Header.Set(CSRFHeader, "1")
				k.kill.active = true
				k.whois.err = service.ErrWhoIsFailed
				k.auth.allowErr = service.ErrDeviceNotRegistered
			},
			wantCode: domain.CodeKillSwitchActive, wantLog: "killswitch",
		},
		{
			name: "キルスイッチ中の未登録デバイスにも接続要求を作らず 503", method: "GET", target: "/viewer",
			setup: func(k *kit, r *http.Request) {
				k.kill.active = true
				k.auth.allowErr = service.ErrDeviceNotRegistered
			},
			wantCode: domain.CodeKillSwitchActive, wantLog: "killswitch",
		},
		{
			name: "発動していなければ WhoIs（許可リストを呼ばない）", method: "GET", target: "/api/v1/x",
			setup: func(k *kit, r *http.Request) {
				k.whois.err = service.ErrWhoIsFailed
				k.auth.allowErr = service.ErrDeviceNotRegistered
			},
			wantCode: domain.CodeWhoisFailed, wantLog: "killswitch,whois",
		},
		{
			name: "WhoIs が通れば許可リスト（Bearer を呼ばない）", method: "GET", target: "/mcp",
			setup: func(k *kit, r *http.Request) {
				k.auth.allowErr = service.ErrDeviceNotRegistered
				k.auth.bearerErr = service.ErrUnauthorized
			},
			wantCode: domain.CodeDeviceNotRegistered, wantLog: "killswitch,whois,allowlist",
		},
		{
			name: "機能無効", method: "GET", target: "/api/v1/x",
			setup:    func(k *kit, r *http.Request) { k.auth.allowErr = service.ErrClientDisabled },
			wantCode: domain.CodeClientDisabled, wantLog: "killswitch,whois,allowlist",
		},
		{
			name: "許可リストを通れば /mcp は Bearer", method: "POST", target: "/mcp",
			setup:    func(k *kit, r *http.Request) { k.auth.bearerErr = service.ErrUnauthorized },
			wantCode: domain.CodeUnauthorized, wantLog: "killswitch,whois,allowlist,bearer",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			k := newKit()
			w := k.do(tt.method, tt.target, func(r *http.Request) { tt.setup(k, r) })
			if got := domain.Code(decodeProblemOrHTMLCode(t, w)); got != tt.wantCode {
				t.Fatalf("code = %q, want %q (status %d)", got, tt.wantCode, w.Code)
			}
			if w.Code != tt.wantCode.HTTPStatus() {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantCode.HTTPStatus())
			}
			if got := k.log.String(); got != tt.wantLog {
				t.Fatalf("calls = %q, want %q", got, tt.wantLog)
			}
			if *k.reached {
				t.Fatal("the handler was reached")
			}
		})
	}
}

// decodeProblemOrHTMLCode は応答から code を取り出す（problem+json、または HTML の data-error-code）。
func decodeProblemOrHTMLCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		return decodeProblem(t, w).Code
	}
	_, rest, ok := strings.Cut(w.Body.String(), `data-error-code="`)
	if !ok {
		t.Fatalf("no error code in response: %q", w.Body.String())
	}
	code, _, _ := strings.Cut(rest, `"`)
	return code
}

func TestRemoteChainPasses(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		target      string
		mut         func(r *http.Request)
		wantLog     string
		wantFeature service.Feature
		wantToken   bool
	}{
		{"Web の GET", "GET", "/viewer", nil, "killswitch,whois,allowlist", service.FeatureWeb, false},
		{"Web の POST（ヘッダー付き）", "POST", "/viewer/lease", func(r *http.Request) { r.Header.Set(CSRFHeader, "1") }, "killswitch,whois,allowlist", service.FeatureWeb, false},
		{"API の GET", "GET", "/api/v1/monitors", nil, "killswitch,whois,allowlist", service.FeatureWeb, false},
		{"WebSocket のハンドシェイク", "GET", "/ws/screen", func(r *http.Request) { r.Header.Set("Origin", "http://"+testIP+":8765") }, "killswitch,whois,allowlist", service.FeatureWeb, false},
		{"MCP の POST（X-Puppet-Remo 不要）", "POST", "/mcp", func(r *http.Request) { r.Header.Set("Authorization", "Bearer dummy") }, "killswitch,whois,allowlist,bearer", service.FeatureMCP, true},
		{"MCP のサブパス", "GET", "/mcp/", func(r *http.Request) { r.Header.Set("Authorization", "Bearer dummy") }, "killswitch,whois,allowlist,bearer", service.FeatureMCP, true},
		{"正規化すると /mcp になるパスも MCP", "GET", "/x/../mcp", func(r *http.Request) { r.Header.Set("Authorization", "Bearer dummy") }, "killswitch,whois,allowlist,bearer", service.FeatureMCP, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newKit()
			w := k.do(tt.method, tt.target, tt.mut)
			if w.Code != http.StatusNoContent || !*k.reached {
				t.Fatalf("status = %d, reached = %v, body = %q", w.Code, *k.reached, w.Body.String())
			}
			if got := k.log.String(); got != tt.wantLog+",handler" {
				t.Fatalf("calls = %q", got)
			}
			if k.auth.gotFeature != tt.wantFeature || k.auth.gotIdentity.StableNodeID != "nodeA" {
				t.Fatalf("allowlist got feature %v, identity %+v", k.auth.gotFeature, k.auth.gotIdentity)
			}
			if tt.wantToken && (k.auth.gotAuthz != "Bearer dummy" || k.auth.gotPrincipl.Client.ID != "c1" || k.auth.gotPrincipl.Identity.StableNodeID != "nodeA") {
				t.Fatalf("bearer got %q, principal %+v", k.auth.gotAuthz, k.auth.gotPrincipl)
			}
		})
	}
}

func TestPrincipalInContext(t *testing.T) {
	k := newKit()
	var got service.Principal
	var ok bool
	k.handler = Remote(RemoteConfig{Hosts: testHosts, KillSwitch: k.kill, WhoIs: k.whois, Authz: k.auth},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok = PrincipalFrom(r.Context())
		}))
	k.do("POST", "/mcp", func(r *http.Request) { r.Header.Set("Authorization", "Bearer dummy") })
	if !ok || got.Client.ID != "c1" || got.Identity.StableNodeID != "nodeA" || got.Token == nil || got.Token.ID != "t1" {
		t.Fatalf("principal = %+v, ok = %v", got, ok)
	}
	k.do("GET", "/viewer", nil)
	if !ok || got.Token != nil {
		t.Fatalf("web principal = %+v", got)
	}
	if _, ok := PrincipalFrom(httptest.NewRequest("GET", "/", nil).Context()); ok {
		t.Fatal("PrincipalFrom on a bare context returned ok")
	}
}

func TestRejectionFormats(t *testing.T) {
	headers := func(h map[string]string) func(*http.Request) {
		return func(r *http.Request) {
			for k, v := range h {
				r.Header.Set(k, v)
			}
		}
	}
	tests := []struct {
		name       string
		method     string
		target     string
		mut        func(*http.Request)
		setup      func(k *kit)
		wantStatus int
		wantCode   string
		wantKind   string // problem / page / fragment
		wantHeader map[string]string
	}{
		{"API の Host は problem+json", "GET", "/api/v1/x", func(r *http.Request) { r.Host = "evil.test" }, nil, 400, "invalid_host", "problem", nil},
		{"MCP の Origin は problem+json", "POST", "/mcp", headers(map[string]string{"Origin": "http://evil.test"}), nil, 403, "invalid_origin", "problem", nil},
		{"API のヘッダー欠落は problem+json", "POST", "/api/v1/x", nil, nil, 403, "missing_header", "problem", nil},
		{"WS ハンドシェイクのキルスイッチは problem+json", "GET", "/ws/screen", nil, func(k *kit) { k.kill.active = true }, 503, "kill_switch_active", "problem", nil},
		{"API の WhoIs 失敗は problem+json", "GET", "/api/v1/x", nil, func(k *kit) { k.whois.err = service.ErrWhoIsFailed }, 403, "whois_failed", "problem", nil},
		{"API の未登録は problem+json", "GET", "/api/v1/x", nil, func(k *kit) { k.auth.allowErr = service.ErrDeviceNotRegistered }, 403, "device_not_registered", "problem", nil},
		{"MCP の Bearer 失敗は 401 + WWW-Authenticate", "POST", "/mcp", nil, func(k *kit) { k.auth.bearerErr = service.ErrUnauthorized }, 401, "unauthorized", "problem", map[string]string{"WWW-Authenticate": "Bearer"}},
		{"MCP の失敗超過は 429 + Retry-After", "POST", "/mcp", nil, func(k *kit) {
			k.auth.bearerErr = domain.NewError(domain.CodeRateLimited, "x", map[string]any{service.DetailRetryAfterSec: 42})
		}, 429, "rate_limited", "problem", map[string]string{"Retry-After": "42"}},
		{"想定外のエラーは 500 internal（詳細を出さない）", "GET", "/api/v1/x", nil, func(k *kit) { k.auth.allowErr = errors.New("db: secret details") }, 500, "internal", "problem", nil},
		{"Web ページの Host は HTML のエラーページ", "GET", "/viewer", func(r *http.Request) { r.Host = "evil.test" }, nil, 400, "invalid_host", "page", nil},
		{"Web ページのキルスイッチ", "GET", "/viewer", nil, func(k *kit) { k.kill.active = true }, 503, "kill_switch_active", "page", nil},
		{"Web ページの機能無効", "GET", "/viewer", nil, func(k *kit) { k.auth.allowErr = service.ErrClientDisabled }, 403, "client_disabled", "page", nil},
		{"Web ページの未登録は待機ページ", "GET", "/viewer", nil, func(k *kit) { k.auth.allowErr = service.ErrDeviceNotRegistered }, 403, "device_not_registered", "page", nil},
		{"Web アクションのヘッダー欠落はフラグメント", "POST", "/viewer/lease", nil, nil, 403, "missing_header", "fragment", nil},
		{"Web アクションのキルスイッチはフラグメント", "POST", "/viewer/lease", headers(map[string]string{CSRFHeader: "1"}), func(k *kit) { k.kill.active = true }, 503, "kill_switch_active", "fragment", map[string]string{"HX-Retarget": "#alert-root", "HX-Reswap": "innerHTML"}},
		{"Web アクションの未登録は待機ページ", "POST", "/viewer/lease", headers(map[string]string{CSRFHeader: "1"}), func(k *kit) { k.auth.allowErr = service.ErrDeviceNotRegistered }, 403, "device_not_registered", "page", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newKit()
			if tt.setup != nil {
				tt.setup(k)
			}
			w := k.do(tt.method, tt.target, tt.mut)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %q", w.Code, tt.wantStatus, w.Body.String())
			}
			body := w.Body.String()
			switch tt.wantKind {
			case "problem":
				if p := decodeProblem(t, w); p.Code != tt.wantCode {
					t.Fatalf("code = %q, want %q", p.Code, tt.wantCode)
				}
			case "page", "fragment":
				if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
					t.Fatalf("Content-Type = %q", ct)
				}
				if !strings.Contains(body, `data-error-code="`+tt.wantCode+`"`) {
					t.Fatalf("no data-error-code %q in %q", tt.wantCode, body)
				}
				if isPage := strings.Contains(body, "<html"); isPage != (tt.wantKind == "page") {
					t.Fatalf("page = %v, want %q: %q", isPage, tt.wantKind, body)
				}
				if closesModal := strings.Contains(body, `id="modal-root" hx-swap-oob="true"`); closesModal != (tt.wantKind == "fragment") {
					t.Fatalf("closes the confirm modal = %v, want it only for %q: %q", closesModal, "fragment", body)
				}
				for _, bad := range []string{"<script", "style=", " onclick=", "problem"} {
					if strings.Contains(body, bad) {
						t.Fatalf("HTML contains %q (CSP): %q", bad, body)
					}
				}
			}
			for k, v := range tt.wantHeader {
				if got := w.Header().Get(k); got != v {
					t.Fatalf("%s = %q, want %q", k, got, v)
				}
			}
			if strings.Contains(body, "secret details") {
				t.Fatalf("internal error detail leaked: %q", body)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", w.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestAllowlistWithoutWhoIsFailsClosed(t *testing.T) {
	k := newKit()
	h := Allowlist(k.auth, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("reached") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/x", nil))
	if p := decodeProblem(t, w); p.Code != "whois_failed" || k.log.String() != "" {
		t.Fatalf("problem = %+v, calls = %q", p, k.log.String())
	}
	h = Bearer(k.auth)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("reached") }))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", nil))
	if p := decodeProblem(t, w); p.Code != "whois_failed" || k.log.String() != "" {
		t.Fatalf("problem = %+v, calls = %q", p, k.log.String())
	}
}

// TestNoSecretsInLogsOrResponses は、Authorization ヘッダーの値が応答にもログにも出ないことを確かめる。
func TestNoSecretsInLogsOrResponses(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	secret := service.TokenPrefix + strings.Repeat("S", 43)
	for _, setup := range []func(k *kit){
		func(k *kit) { k.auth.bearerErr = service.ErrUnauthorized },
		func(k *kit) { k.auth.allowErr = errors.New("boom") },
		func(k *kit) { k.whois.err = errors.New("lookup " + "failed") },
		func(k *kit) {},
	} {
		k := newKit()
		setup(k)
		w := k.do("POST", "/mcp", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) })
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("response contains the token: %q", w.Body.String())
		}
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("logs contain the token: %q", logs.String())
	}
}

func TestRetryAfterRoundsUp(t *testing.T) {
	w := httptest.NewRecorder()
	writeReject(w, httptest.NewRequest("GET", "/mcp", nil), routeMCP, reject{code: domain.CodeRateLimited, retryAfter: 1500 * time.Millisecond})
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	w = httptest.NewRecorder()
	writeReject(w, httptest.NewRequest("GET", "/mcp", nil), routeMCP, reject{code: domain.CodeRateLimited})
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}
