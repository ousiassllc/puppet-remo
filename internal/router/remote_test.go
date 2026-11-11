package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/router"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote/remotetest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

const (
	remoteIP   = "100.64.0.1"
	remotePort = 8765
	remotePeer = "100.64.0.9:51234"
)

type remoteKill struct{ active bool }

func (k *remoteKill) Active() bool { return k.active }

type remoteWhoIs struct{}

func (remoteWhoIs) Lookup(context.Context, string) (service.Identity, error) {
	return service.Identity{LoginName: "alice@example.com", StableNodeID: "nodeA", NodeName: "laptop"}, nil
}

// remoteAuth は許可リストと Bearer を通す偽の Authenticator。登録していないデバイスは device_not_registered。
type remoteAuth struct {
	registered bool
	allowCalls int
}

func (*remoteAuth) Identify(context.Context, string) (service.Identity, error) {
	panic("the chain resolves identities via WhoIs")
}

func (a *remoteAuth) CheckAllowlist(context.Context, string, service.Identity, service.Feature) (domain.Client, error) {
	a.allowCalls++
	if !a.registered {
		return domain.Client{}, service.ErrDeviceNotRegistered
	}
	return domain.Client{ID: "c1", StableNodeID: "nodeA", LoginName: "alice@example.com", WebEnabled: true, MCPEnabled: true}, nil
}

func (*remoteAuth) VerifyBearer(context.Context, string, service.Principal, string) (*service.TokenRecord, error) {
	return &service.TokenRecord{ID: "t1", ClientID: "c1"}, nil
}

type remoteKit struct {
	kill    *remoteKill
	auth    *remoteAuth
	mcpHits int
	wsHits  int
	// staticPaths は /static ハンドラに届いたパス（/static を外したあと）。
	staticPaths []string
	fakes       *remotetest.Fakes
	handler     http.Handler
}

func newRemote(t *testing.T) *remoteKit {
	t.Helper()
	k := &remoteKit{kill: &remoteKill{}, auth: &remoteAuth{registered: true}}
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mcpHits++
		if _, ok := middleware.PrincipalFrom(r.Context()); !ok {
			t.Error("the MCP handler must run behind the chain, with a principal in the context")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ws := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.wsHits++
		if _, ok := middleware.ClientFrom(r.Context()); !ok {
			t.Error("the WebSocket handler must run behind the chain, with the client in the context")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	k.fakes = remotetest.New()
	web, err := remote.New(k.fakes.Deps())
	if err != nil {
		t.Fatal(err)
	}
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.staticPaths = append(k.staticPaths, r.URL.Path)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"build"`)
		_, _ = w.Write([]byte("asset"))
	})
	h, err := router.NewRemote(router.RemoteConfig{
		Chain: middleware.RemoteConfig{
			Hosts:      func() ([]string, int) { return []string{remoteIP}, remotePort },
			KillSwitch: k.kill,
			WhoIs:      remoteWhoIs{},
			Authz:      k.auth,
		},
		MCP:    mcp,
		WS:     ws,
		Web:    web,
		Static: static,
	})
	if err != nil {
		t.Fatal(err)
	}
	k.handler = h
	return k
}

func (k *remoteKit) do(method, path string, mut ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+remoteIP+":8765"+path, nil)
	r.RemoteAddr = remotePeer
	r.Header.Set("Authorization", "Bearer fake-test-token")
	for _, m := range mut {
		m(r)
	}
	w := httptest.NewRecorder()
	k.handler.ServeHTTP(w, r)
	return w
}

func TestNewRemoteRequiresDependencies(t *testing.T) {
	if _, err := router.NewRemote(router.RemoteConfig{}); err == nil {
		t.Error("NewRemote with no dependencies must fail")
	}
}

func TestRemoteServesMCPBehindTheChain(t *testing.T) {
	k := newRemote(t)
	if rec := k.do(http.MethodPost, "/mcp"); rec.Code != http.StatusNoContent || k.mcpHits != 1 {
		t.Errorf("POST /mcp = %d (handler hits %d), want 204 via the MCP handler", rec.Code, k.mcpHits)
	}
}

func TestRemoteServesWebSocketBehindTheChainForGETOnly(t *testing.T) {
	k := newRemote(t)
	if rec := k.do(http.MethodGet, "/ws/screen"); rec.Code != http.StatusNoContent || k.wsHits != 1 {
		t.Errorf("GET /ws/screen = %d (handler hits %d), want 204 via the WebSocket handler", rec.Code, k.wsHits)
	}
	// 状態を変更するメソッドは、連鎖（X-Puppet-Remo）で止まるか、経路に届かない。
	for _, hdr := range []bool{false, true} {
		rec := k.do(http.MethodPost, "/ws/screen", func(r *http.Request) {
			if hdr {
				r.Header.Set(middleware.CSRFHeader, "1")
			}
		})
		if rec.Code == http.StatusNoContent {
			t.Errorf("POST /ws/screen (csrf header %v) reached the WebSocket handler", hdr)
		}
	}
	if k.wsHits != 1 {
		t.Errorf("WebSocket handler hits = %d, want 1 (GET only)", k.wsHits)
	}
}

func TestRemoteServesTheWebUIBehindTheChain(t *testing.T) {
	k := newRemote(t)
	if rec := k.do(http.MethodGet, "/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/viewer" {
		t.Errorf("GET / = %d (Location %q), want 302 /viewer", rec.Code, rec.Header().Get("Location"))
	}
	rec := k.do(http.MethodGet, "/viewer")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<remo-screen-viewer") {
		t.Errorf("GET /viewer = %d %q, want the viewer page", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != middleware.CSPValue {
		t.Errorf("CSP on /viewer = %q", got)
	}
	// 連鎖が認可したクライアントを、ハンドラが viewer_id の束縛に使う。
	if !strings.Contains(rec.Body.String(), "VIEWER01") {
		t.Errorf("no viewer_id in the page: %q", rec.Body.String())
	}
	post := k.do(http.MethodPost, "/viewer/lease/acquire", func(r *http.Request) {
		r.Header.Set(middleware.CSRFHeader, "1")
		r.Header.Set("X-Viewer-Id", "VIEWER01")
	})
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), "/viewer/lease/release") {
		t.Errorf("POST /viewer/lease/acquire = %d %q", post.Code, post.Body.String())
	}
	if noHeader := k.do(http.MethodPost, "/viewer/lease/acquire"); noHeader.Code != http.StatusForbidden {
		t.Errorf("POST without X-Puppet-Remo = %d, want 403", noHeader.Code)
	}
}

// /static/* は、連鎖を通ったあと、/static を外して static の handler へ渡す（GET と HEAD。ほかのメソッドは届かない）。
func TestRemoteServesStaticFilesBehindTheChain(t *testing.T) {
	k := newRemote(t)
	rec := k.do(http.MethodGet, "/static/js/screen-viewer.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "asset" || rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
		t.Errorf("GET /static/js/screen-viewer.js = %d %q (Cache-Control %q, ETag %q)", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
	if rec := k.do(http.MethodHead, "/static/css/app.css"); rec.Code != http.StatusOK {
		t.Errorf("HEAD /static/css/app.css = %d, want 200", rec.Code)
	}
	if want := []string{"/js/screen-viewer.js", "/css/app.css"}; !reflect.DeepEqual(k.staticPaths, want) {
		t.Errorf("static handler paths = %v, want %v", k.staticPaths, want)
	}
	if rec := k.do(http.MethodPost, "/static/js/app.js", func(r *http.Request) { r.Header.Set(middleware.CSRFHeader, "1") }); rec.Code == http.StatusOK {
		t.Errorf("POST /static/js/app.js = %d, want it not served", rec.Code)
	}
	// 未登録デバイスには配信しない。
	k = newRemote(t)
	k.auth.registered = false
	if rec := k.do(http.MethodGet, "/static/js/app.js"); rec.Code != http.StatusForbidden || len(k.staticPaths) != 0 {
		t.Errorf("unregistered GET /static/js/app.js = %d (static hits %v), want 403 before the handler", rec.Code, k.staticPaths)
	}
}

func TestRemoteWebSocketRejectionsAreProblemJSON(t *testing.T) {
	k := newRemote(t)
	k.auth.registered = false
	rec := k.do(http.MethodGet, "/ws/screen")
	if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/problem+json" || k.wsHits != 0 {
		t.Errorf("unregistered GET /ws/screen = %d %q (hits %d), want 403 problem+json before the handler",
			rec.Code, rec.Header().Get("Content-Type"), k.wsHits)
	}
	k = newRemote(t)
	rec = k.do(http.MethodGet, "/ws/screen", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") })
	if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/problem+json" || k.wsHits != 0 {
		t.Errorf("foreign Origin = %d %q (hits %d), want 403 problem+json before the handler",
			rec.Code, rec.Header().Get("Content-Type"), k.wsHits)
	}
}

// tailnet 公開面は /mcp・GET /ws/screen・Web UI（/ と /viewer*）・GET /static/* だけ（/api/v1 は M2 以降の別の段階）。
// 連鎖を通っても、ほかの経路は 404 で、ホスト GUI の経路は存在しない。
func TestRemoteExposesNothingButTheDocumentedRoutes(t *testing.T) {
	k := newRemote(t)
	paths := []string{
		"/ws/screen/", "/ws/other", "/api/v1/clients", "/events", "/poll", "/settings", "/killswitch", "/clients", "/requests",
		"/mcp/", "/mcp/extra", "/viewer/nope", "/viewer/lease/", "/viewer/clipboard", "/viewer/keys",
		"/audit", "/audit/1", "/audit/1/thumbnail", // 監査ログの閲覧（FR-AU05）はホスト GUI だけ
		"/licenses",                                                          // ライセンス表記（#133）はホスト GUI だけ
		"/update", "/update/check", "/update/apply", "/update/apply/confirm", // 更新（FR-H09）はホスト GUI だけ
		"/apps", "/apps/new", "/apps/pick", "/apps/pick-file", "/apps/a1/edit", "/apps/a1/delete/confirm", "/api/v1/apps", "/settings/apps", // 起動許可リストの管理（FR-H12）はホスト GUI だけ
		"/lease/revoke/confirm", "/tasks/t1/cancel/confirm", // 強制剥奪・タスク中断の確認モーダルはホスト GUI だけ（リモートは /viewer/lease/revoke/confirm）
	}
	for _, p := range paths {
		if rec := k.do(http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	for _, p := range []string{"/killswitch/trigger", "/killswitch/release", "/lease/revoke", "/clients/c1", "/requests/r1/approve", "/apps", "/apps/a1", "/apps/pick", "/apps/pick-file"} {
		rec := k.do(http.MethodPost, p, func(r *http.Request) { r.Header.Set(middleware.CSRFHeader, "1") })
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404 (host-only operations must not be reachable remotely)", p, rec.Code)
		}
	}
	for _, tc := range []struct{ method, path string }{{http.MethodPut, "/apps/a1"}, {http.MethodDelete, "/apps/a1"}} {
		rec := k.do(tc.method, tc.path, func(r *http.Request) { r.Header.Set(middleware.CSRFHeader, "1") })
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (host-only operations must not be reachable remotely)", tc.method, tc.path, rec.Code)
		}
	}
	if k.mcpHits != 0 || k.wsHits != 0 {
		t.Errorf("the MCP handler was reached %d times and the WebSocket handler %d times by other paths", k.mcpHits, k.wsHits)
	}
}

// 起動許可リストを書く service.AppRegistry は、リモートルーターの依存に含まれない（型の上で届かない。NFR-S20）。
func TestRemoteConfigHasNoAppRegistry(t *testing.T) {
	registry := reflect.TypeFor[service.AppRegistry]()
	for _, typ := range []reflect.Type{reflect.TypeFor[router.RemoteConfig](), reflect.TypeFor[middleware.RemoteConfig]()} {
		for f := range typ.Fields() {
			if f.Type.Implements(registry) || f.Type == registry {
				t.Errorf("%s.%s (%s) can write the allow list", typ, f.Name, f.Type)
			}
		}
	}
}

func TestRemoteChainRunsBeforeRouting(t *testing.T) {
	t.Run("kill switch returns 503 even for unknown paths and creates no allowlist lookup", func(t *testing.T) {
		k := newRemote(t)
		k.kill.active = true
		for _, p := range []string{"/mcp", "/viewer"} {
			if rec := k.do(http.MethodGet, p); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("GET %s with the kill switch active = %d, want 503", p, rec.Code)
			}
		}
		if k.auth.allowCalls != 0 || k.mcpHits != 0 {
			t.Errorf("the kill switch must stop requests before the allowlist: allow=%d mcp=%d", k.auth.allowCalls, k.mcpHits)
		}
	})
	t.Run("an unregistered device is rejected before routing", func(t *testing.T) {
		k := newRemote(t)
		k.auth.registered = false
		for _, p := range []string{"/mcp", "/anything"} {
			if rec := k.do(http.MethodGet, p); rec.Code != http.StatusForbidden {
				t.Errorf("GET %s from an unregistered device = %d, want 403", p, rec.Code)
			}
		}
		if k.mcpHits != 0 {
			t.Error("an unregistered device reached the MCP handler")
		}
	})
	t.Run("a foreign Host header is rejected", func(t *testing.T) {
		k := newRemote(t)
		rec := k.do(http.MethodPost, "/mcp", func(r *http.Request) { r.Host = "evil.example:8765" })
		if rec.Code != http.StatusBadRequest || k.mcpHits != 0 {
			t.Errorf("foreign Host = %d (hits %d), want 400 before the handler", rec.Code, k.mcpHits)
		}
	})
}

func TestRemoteResponsesCarryCSP(t *testing.T) {
	k := newRemote(t)
	k.auth.registered = false
	// problem+json（API・MCP）は共有の CSP。HTML のページ（未登録デバイスの待機ページ）は、インライン <style> のハッシュを足した CSP。
	for path, want := range map[string]string{"/mcp": middleware.CSPValue, "/nope": middleware.NoticeCSPValue} {
		rec := k.do(http.MethodGet, path)
		if got := rec.Header().Get("Content-Security-Policy"); got != want {
			t.Errorf("GET %s: CSP = %q, want %q (status %d)", path, got, want, rec.Code)
		}
	}
}
