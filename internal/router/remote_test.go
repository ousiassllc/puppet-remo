package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/router"
	"github.com/ousiassllc/puppet-remo/internal/service"
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
	handler http.Handler
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
	h, err := router.NewRemote(router.RemoteConfig{
		Chain: middleware.RemoteConfig{
			Hosts:      func() ([]string, int) { return []string{remoteIP}, remotePort },
			KillSwitch: k.kill,
			WhoIs:      remoteWhoIs{},
			Authz:      k.auth,
		},
		MCP: mcp,
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

// M1 の tailnet 公開面は /mcp だけ（Web UI・WebSocket・API は M2 から）。連鎖を通っても、ほかの経路は 404 で、
// ホスト GUI の経路は存在しない。
func TestRemoteExposesNothingButMCP(t *testing.T) {
	k := newRemote(t)
	paths := []string{
		"/", "/viewer", "/ws/screen", "/api/v1/clients", "/events", "/poll", "/settings", "/killswitch", "/clients", "/requests",
		"/mcp/", "/mcp/extra", "/static/js/app.js",
		"/apps", "/apps/new", "/apps/pick", "/api/v1/apps", "/settings/apps", // 起動許可リストの管理（FR-H12）はホスト GUI だけ
	}
	for _, p := range paths {
		if rec := k.do(http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	for _, p := range []string{"/killswitch/trigger", "/killswitch/release", "/clients/c1", "/requests/r1/approve", "/apps", "/apps/a1", "/apps/pick"} {
		rec := k.do(http.MethodPost, p, func(r *http.Request) { r.Header.Set(middleware.CSRFHeader, "1") })
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404 (host-only operations must not be reachable remotely)", p, rec.Code)
		}
	}
	if k.mcpHits != 0 {
		t.Errorf("the MCP handler was reached %d times by non-/mcp paths", k.mcpHits)
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
	for _, rec := range []*httptest.ResponseRecorder{k.do(http.MethodGet, "/mcp"), k.do(http.MethodGet, "/nope")} {
		if got := rec.Header().Get("Content-Security-Policy"); got != middleware.CSPValue {
			t.Errorf("CSP = %q, want the shared policy (status %d)", got, rec.Code)
		}
	}
}
