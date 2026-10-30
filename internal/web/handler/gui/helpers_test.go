package gui_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

func init() { gin.SetMode(gin.TestMode) }

// rig はハンドラを CSP ミドルウェアの内側に置いた GUI ルーター（router.NewGUI と同じ構成。web は router を import できない）。
type rig struct {
	t      *testing.T
	f      *guitest.Fakes
	h      http.Handler
	events *gui.Handler
}

func newRig(t *testing.T, mutate ...func(*guitest.Fakes)) *rig {
	t.Helper()
	f := guitest.New()
	for _, m := range mutate {
		m(f)
	}
	return newRigFrom(t, f, 0)
}

func newHandler(d gui.Deps) (*gui.Handler, error) { return gui.New(d) }

func newRigFrom(t *testing.T, f *guitest.Fakes, heartbeat time.Duration) *rig {
	t.Helper()
	d := f.Deps()
	d.Heartbeat = heartbeat
	d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := gui.New(d)
	if err != nil {
		t.Fatal(err)
	}
	e := gin.New()
	h.Register(e)
	e.NoRoute(h.NotFound)
	return &rig{t: t, f: f, h: middleware.CSP()(e), events: h}
}

// resp は 1 回の応答（本文は読み切り済み）。
type resp struct {
	*http.Response
	Body string
}

func (r *rig) do(method, target string, form url.Values, headers ...string) resp {
	r.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
		assertCSPHeader(r.t, res)
		assertCSPCompliantHTML(r.t, out.Body)
	}
	return out
}

func (r *rig) get(target string, headers ...string) resp {
	return r.do(http.MethodGet, target, nil, headers...)
}
func (r *rig) post(target string, form url.Values, headers ...string) resp {
	return r.do(http.MethodPost, target, form, headers...)
}

// hx は HTMX の部分更新のヘッダー。
var hx = []string{"HX-Request", "true"}

func assertCSPHeader(t *testing.T, res *http.Response) {
	t.Helper()
	if got := res.Header.Get("Content-Security-Policy"); got != middleware.CSPValue {
		t.Errorf("Content-Security-Policy = %q, want %q", got, middleware.CSPValue)
	}
}

func assertCSPCompliantHTML(t *testing.T, body string) { webtest.AssertCSPCompliantHTML(t, body) }

func must(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Errorf(format, args...)
	}
}

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
		t.Fatalf("status = %d, want %d\n%s", r.StatusCode, want, r.Body)
	}
}

// seed は代表的なデータを入れる。
func seedClient(f *guitest.Fakes, id, name string, web, mcp, agent bool) domain.Client {
	c := domain.Client{ID: id, StableNodeID: "node-" + id, LoginName: "alice@example.com", DisplayName: name,
		WebEnabled: web, MCPEnabled: mcp, AgentEnabled: agent, ApprovalMode: domain.ApprovalAlways}
	f.Admin.Cs = append(f.Admin.Cs, c)
	return c
}

func seedRequest(f *guitest.Fakes, id, name string) {
	f.Admin.Requests = append(f.Admin.Requests, service.ConnRequest{
		ID: id, StableNodeID: "node-" + id, LoginName: "bob@example.com", DisplayName: name, RemoteIP: "100.64.0.9",
		Status: service.ConnPending, RequestCount: 3,
		FirstSeenAt: f.Clock.T.Add(-time.Hour), LastSeenAt: f.Clock.T.Add(-time.Minute), ExpiresAt: f.Clock.T.Add(24 * time.Hour),
	})
}

func seedToken(f *guitest.Fakes, clientID, id, label string, expires, revoked *time.Time) {
	if f.Admin.Toks == nil {
		f.Admin.Toks = map[string][]service.TokenRecord{}
	}
	f.Admin.Toks[clientID] = append(f.Admin.Toks[clientID], service.TokenRecord{
		ID: id, ClientID: clientID, Label: label, Prefix: "prm_AbCd", ExpiresAt: expires, RevokedAt: revoked, CreatedAt: f.Clock.T.Add(-48 * time.Hour),
	})
}
