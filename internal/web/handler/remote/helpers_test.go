package remote_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote/remotetest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	testIP   = "100.64.0.1"
	testPort = 8765
	peer     = "100.64.0.9:51234"
)

// testClient は連鎖が認可するクライアント。
var testClient = domain.Client{ID: "c1", StableNodeID: "nodeA", LoginName: "alice@example.com", DisplayName: "alice-laptop", WebEnabled: true}

type noKill struct{}

func (noKill) Active() bool { return false }

type whoIs struct{}

func (whoIs) Lookup(context.Context, string) (service.Identity, error) {
	return service.Identity{LoginName: testClient.LoginName, StableNodeID: testClient.StableNodeID, NodeName: "laptop"}, nil
}

// auth は testClient を許可する Authenticator（許可リストの照合だけを偽物にし、連鎖そのものは本物）。
type auth struct{}

func (auth) Identify(context.Context, string) (service.Identity, error) { panic("unused") }
func (auth) CheckAllowlist(context.Context, string, service.Identity, service.Feature) (domain.Client, error) {
	return testClient, nil
}
func (auth) VerifyBearer(context.Context, string, service.Principal, string) (*service.TokenRecord, error) {
	panic("unused")
}

// rig は router.NewRemote と同じく、ミドルウェア連鎖（本物）の内側に Web UI のハンドラを置く（web は router を import できない）。
type rig struct {
	t *testing.T
	f *remotetest.Fakes
	h http.Handler
}

func newRig(t *testing.T, mutate ...func(*remotetest.Fakes)) *rig {
	t.Helper()
	f := remotetest.New()
	for _, m := range mutate {
		m(f)
	}
	d := f.Deps()
	d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := remote.New(d)
	if err != nil {
		t.Fatal(err)
	}
	e := gin.New()
	e.RedirectTrailingSlash, e.RedirectFixedPath, e.HandleMethodNotAllowed = false, false, false
	h.Register(e)
	e.NoRoute(h.NotFound)
	chain := middleware.Remote(middleware.RemoteConfig{
		Hosts:      func() ([]string, int) { return []string{testIP}, testPort },
		KillSwitch: noKill{}, WhoIs: whoIs{}, Authz: auth{},
	}, e)
	return &rig{t: t, f: f, h: middleware.CSP()(chain)}
}

// resp は 1 回の応答（本文は読み切り済み）。
type resp struct {
	*http.Response
	Body string
}

// do は連鎖を通して 1 回要求する。状態変更には X-Puppet-Remo: 1 を付ける（<body hx-headers> が付ける値）。
func (r *rig) do(method, target string, form url.Values, headers ...string) resp {
	r.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://"+testIP+":8765"+target, body)
	req.RemoteAddr = peer
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		req.Header.Set(middleware.CSRFHeader, "1")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	res := rec.Result()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	_ = res.Body.Close()
	out := resp{Response: res, Body: string(b)}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		if got := res.Header.Get("Content-Security-Policy"); got != middleware.CSPValue {
			r.t.Errorf("Content-Security-Policy = %q, want %q", got, middleware.CSPValue)
		}
		webtest.AssertCSPCompliantHTML(r.t, out.Body)
	}
	return out
}

func (r *rig) get(target string, headers ...string) resp {
	return r.do(http.MethodGet, target, nil, headers...)
}

func (r *rig) post(target string, form url.Values, headers ...string) resp {
	return r.do(http.MethodPost, target, form, headers...)
}

// viewer は GET /viewer で viewer_id を発行し、その X-Viewer-Id のヘッダーを返す。
func (r *rig) viewer() (id string, header []string) {
	r.t.Helper()
	res := r.get("/viewer")
	if res.StatusCode != http.StatusOK {
		r.t.Fatalf("GET /viewer = %d: %s", res.StatusCode, res.Body)
	}
	const key = `ws/screen?viewer_id=`
	i := strings.Index(res.Body, key)
	if i < 0 {
		r.t.Fatalf("no ws-url in %s", res.Body)
	}
	rest := res.Body[i+len(key):]
	id = rest[:strings.IndexAny(rest, `"&`)]
	return id, []string{"X-Viewer-Id", id}
}

// actor は viewer_id のリース保持者。
func actor(viewerID string) domain.Actor {
	return domain.Actor{Kind: domain.ActorWeb, ClientID: testClient.ID, Label: testClient.DisplayName, SessionID: viewerID}
}

var hx = []string{"HX-Request", "true"}

func contains(t *testing.T, r resp, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(r.Body, s) {
			t.Errorf("response (%d) does not contain %q\n%s", r.StatusCode, s, r.Body)
		}
	}
}

func lacks(t *testing.T, r resp, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(r.Body, s) {
			t.Errorf("response (%d) unexpectedly contains %q\n%s", r.StatusCode, s, r.Body)
		}
	}
}

func status(t *testing.T, r resp, want int) {
	t.Helper()
	if r.StatusCode != want {
		t.Errorf("status = %d, want %d\n%s", r.StatusCode, want, r.Body)
	}
}

// failed はアクションの失敗: 同じステータス・data-error-code 付きのインラインアラート・#alert-root への HX-Retarget。
func failed(t *testing.T, r resp, wantStatus int, code string) {
	t.Helper()
	status(t, r, wantStatus)
	contains(t, r, `data-error-code="`+code+`"`)
	if got := r.Header.Get("HX-Retarget"); got != "#alert-root" {
		t.Errorf("HX-Retarget = %q, want #alert-root", got)
	}
	// 確認モーダルの確定の失敗は、アラートが暗幕の背面に隠れないようモーダルを閉じる（確認を経ない要求では #modal-root は空で無害）。
	contains(t, r, `id="modal-root" hx-swap-oob="true"`)
	lacks(t, r, "<html")
}
