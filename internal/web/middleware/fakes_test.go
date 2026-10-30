package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

const (
	testPort = 8765
	testIP   = "100.64.0.1"
	testDNS  = "host.tail1234.ts.net"
)

func testHosts() ([]string, int) { return []string{testIP, "fd7a:115c:a1e0::1", testDNS}, testPort }

// callLog は各段が呼ばれた順序の記録。
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *callLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.calls, ",")
}

type fakeKill struct {
	log    *callLog
	active bool
}

func (k *fakeKill) Active() bool { k.log.add("killswitch"); return k.active }

type fakeWhoIs struct {
	log *callLog
	id  service.Identity
	err error
}

func (w *fakeWhoIs) Lookup(context.Context, string) (service.Identity, error) {
	w.log.add("whois")
	return w.id, w.err
}

// fakeAuth は service.Authenticator の偽実装。呼び出しを記録し、設定したエラーを返す。
type fakeAuth struct {
	log         *callLog
	allowErr    error
	bearerErr   error
	client      domain.Client
	token       service.TokenRecord
	gotAuthz    string
	gotFeature  service.Feature
	gotIdentity service.Identity
	gotPrincipl service.Principal
}

func (a *fakeAuth) Identify(context.Context, string) (service.Identity, error) {
	panic("middleware must not call Identify; it resolves via tailnet.WhoIs")
}

func (a *fakeAuth) CheckAllowlist(_ context.Context, _ string, id service.Identity, f service.Feature) (domain.Client, error) {
	a.log.add("allowlist")
	a.gotIdentity, a.gotFeature = id, f
	return a.client, a.allowErr
}

func (a *fakeAuth) VerifyBearer(_ context.Context, _ string, p service.Principal, authorization string) (*service.TokenRecord, error) {
	a.log.add("bearer")
	a.gotPrincipl, a.gotAuthz = p, authorization
	if a.bearerErr != nil {
		return nil, a.bearerErr
	}
	t := a.token
	return &t, nil
}

// kit は 1 テスト分の連鎖と偽の依存先。
type kit struct {
	log     *callLog
	kill    *fakeKill
	whois   *fakeWhoIs
	auth    *fakeAuth
	handler http.Handler
	reached *bool
}

func newKit() *kit {
	log := &callLog{}
	reached := new(bool)
	k := &kit{
		log:     log,
		kill:    &fakeKill{log: log},
		whois:   &fakeWhoIs{log: log, id: service.Identity{LoginName: "alice@example.com", StableNodeID: "nodeA", NodeName: "laptop"}},
		auth:    &fakeAuth{log: log, client: domain.Client{ID: "c1", StableNodeID: "nodeA", LoginName: "alice@example.com", WebEnabled: true, MCPEnabled: true}},
		reached: reached,
	}
	k.auth.token = service.TokenRecord{ID: "t1", ClientID: "c1"}
	k.handler = Remote(RemoteConfig{Hosts: testHosts, KillSwitch: k.kill, WhoIs: k.whois, Authz: k.auth},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.add("handler")
			*reached = true
			w.WriteHeader(http.StatusNoContent)
		}))
	return k
}

func (k *kit) do(method, target string, mut func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+testIP+":8765"+target, nil)
	r.RemoteAddr = "100.64.0.9:51234"
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	k.handler.ServeHTTP(w, r)
	return w
}
