package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/api"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote/remotetest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// apiFakes は JSON API（/api/v1）の依存先の偽物。
type apiFakes struct {
	status       service.ServerStatus
	agentEnabled bool
	agentErr     error
	mons         []domain.Monitor
	maxDim       int
	monsErr      error
	holder       *service.Holder
	pending      *domain.Actor
	viewers      *remotetest.Sessions
}

func (f *apiFakes) ServerStatus(context.Context) service.ServerStatus { return f.status }

func (f *apiFakes) Config(context.Context) (service.AgentConfig, error) {
	return service.AgentConfig{Enabled: f.agentEnabled}, f.agentErr
}

func (f *apiFakes) Monitors(context.Context) ([]domain.Monitor, error) { return f.mons, f.monsErr }

func (f *apiFakes) MaxDimension(context.Context) (int, error) { return f.maxDim, nil }

func (f *apiFakes) Holder() (service.Holder, bool) {
	if f.holder == nil {
		return service.Holder{}, false
	}
	return *f.holder, true
}

func (f *apiFakes) Pending() (service.PendingLease, bool) {
	if f.pending == nil {
		return service.PendingLease{}, false
	}
	return service.PendingLease{LeaseID: "l1", Actor: *f.pending}, true
}

func (f *apiFakes) Actor(viewerID string, c domain.Client) (domain.Actor, error) {
	return f.viewers.Actor(viewerID, c)
}

func (f *apiFakes) handler(t *testing.T, kill service.KillSwitchState) http.Handler {
	t.Helper()
	h, err := api.New(api.Deps{
		Version: "1.2.3", Status: f, KillSwitch: kill, Agent: f, Monitors: f, Leases: f, Viewers: f,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func newAPIFakes(viewers *remotetest.Sessions) *apiFakes {
	return &apiFakes{
		status:  service.ServerStatus{TailnetIPs: []string{remoteIP, "fd7a:115c:a1e0::1"}, Port: remotePort},
		viewers: viewers,
		maxDim:  1568,
	}
}

// decode は応答の本文を v に読む。Content-Type は application/json でなければならない。
func decode(t *testing.T, k *remoteKit, path string, v any, mut ...func(*http.Request)) {
	t.Helper()
	rec := k.do(http.MethodGet, path, mut...)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET %s = %d %q, want 200 application/json (body %s)", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	// 前の応答の項目が残らないよう、v を空にしてから読む。
	reflect.ValueOf(v).Elem().SetZero()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("GET %s: %v (body %s)", path, err, rec.Body)
	}
}

// requireProblem は応答が problem+json で、status と code が期待どおりであることを確かめる。
func requireProblem(t *testing.T, k *remoteKit, path string, status int, code domain.Code, mut ...func(*http.Request)) map[string]any {
	t.Helper()
	rec := k.do(http.MethodGet, path, mut...)
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("GET %s: %v (body %s)", path, err, rec.Body)
	}
	if rec.Code != status || rec.Header().Get("Content-Type") != "application/problem+json" || p["code"] != string(code) ||
		p["type"] != "urn:puppet-remo:error:"+string(code) || p["status"] != float64(status) {
		t.Fatalf("GET %s = %d %q %v, want %d problem+json code %s", path, rec.Code, rec.Header().Get("Content-Type"), p, status, code)
	}
	return p
}

func TestAPIStatus(t *testing.T) {
	k := newRemote(t)
	k.api.agentEnabled = true
	var got map[string]any
	decode(t, k, "/api/v1/status", &got)
	want := map[string]any{
		"version": "1.2.3", "tailnet_ip": remoteIP, "port": float64(remotePort),
		"killswitch": map[string]any{"active": false}, "agent_enabled": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status = %v, want %v", got, want)
	}

	k.api.status.TailnetIPs = nil
	decode(t, k, "/api/v1/status", &got)
	if got["tailnet_ip"] != "" {
		t.Errorf("tailnet_ip without a tailnet IP = %v, want an empty string", got["tailnet_ip"])
	}
}

func TestAPIStatusFailureIsInternalAndHidesTheCause(t *testing.T) {
	k := newRemote(t)
	k.api.agentErr = errors.New("db: secret path /var/lib/x")
	p := requireProblem(t, k, "/api/v1/status", http.StatusInternalServerError, domain.CodeInternal)
	if strings.Contains(strings.ToLower(p["detail"].(string)), "secret") {
		t.Errorf("the internal cause leaked into the problem: %v", p)
	}
}

func TestAPIMonitorsMatchGetScreenInfo(t *testing.T) {
	k := newRemote(t)
	k.api.mons = []domain.Monitor{
		{Index: 0, Name: "primary", Primary: true, Size: domain.Size{W: 2560, H: 1440}, DPI: 120},
		{Index: 1, Name: "side", Origin: domain.Point{X: -1280}, Size: domain.Size{W: 1280, H: 720}},
	}
	var got []map[string]any
	decode(t, k, "/api/v1/monitors", &got)
	// MCP の get_screen_info の monitors[] と同じ値（internal/mcp/tools_test.go）。
	want := []map[string]any{
		{"index": 0.0, "name": "primary", "primary": true, "origin": map[string]any{"x": 0.0, "y": 0.0},
			"physical": map[string]any{"width": 2560.0, "height": 1440.0}, "dpi_scale": 1.25, "scale": 0.6125,
			"scaled": map[string]any{"width": 1568.0, "height": 882.0}},
		{"index": 1.0, "name": "side", "primary": false, "origin": map[string]any{"x": -1280.0, "y": 0.0},
			"physical": map[string]any{"width": 1280.0, "height": 720.0}, "dpi_scale": 1.0, "scale": 1.0,
			"scaled": map[string]any{"width": 1280.0, "height": 720.0}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("monitors = %v, want %v", got, want)
	}

	k.api.mons = nil
	rec := k.do(http.MethodGet, "/api/v1/monitors")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("no monitors = %s, want []", rec.Body)
	}
}

func TestAPIMonitorsErrorsFollowTheErrorTable(t *testing.T) {
	k := newRemote(t)
	k.api.monsErr = domain.ErrDesktopUnavailable
	requireProblem(t, k, "/api/v1/monitors", http.StatusServiceUnavailable, domain.CodeDesktopUnavailable)

	k.api.monsErr = errors.New("platform exploded")
	requireProblem(t, k, "/api/v1/monitors", http.StatusInternalServerError, domain.CodeInternal)
}

func TestAPILease(t *testing.T) {
	type lease = map[string]any
	k := newRemote(t)
	viewer := k.fakes.Sessions.Issue(domain.Client{ID: "c1"})
	viewerHeader := func(id string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("X-Viewer-Id", id) }
	}

	var got lease
	decode(t, k, "/api/v1/lease", &got)
	if !reflect.DeepEqual(got, lease{"state": "idle", "mine": false}) {
		t.Errorf("idle = %v", got)
	}

	// 保持者が web のビューア。X-Viewer-Id が保持者のものなら mine。idle_remaining_sec は切り上げ。
	web := domain.Actor{Kind: domain.ActorWeb, ClientID: "c1", Label: "laptop", SessionID: viewer}
	k.api.holder = &service.Holder{LeaseID: "l1", Actor: web, IdleRemaining: 90*time.Second + time.Millisecond}
	wantHeld := func(mine bool) lease {
		return lease{"state": "held", "holder": lease{"kind": "web", "label": "laptop"}, "mine": mine, "idle_remaining_sec": 91.0}
	}
	decode(t, k, "/api/v1/lease", &got)
	if !reflect.DeepEqual(got, wantHeld(false)) {
		t.Errorf("held, no X-Viewer-Id = %v, want %v (mine is false)", got, wantHeld(false))
	}
	decode(t, k, "/api/v1/lease", &got, viewerHeader(viewer))
	if !reflect.DeepEqual(got, wantHeld(true)) {
		t.Errorf("held, own X-Viewer-Id = %v, want %v", got, wantHeld(true))
	}
	other := k.fakes.Sessions.Issue(domain.Client{ID: "c1"})
	decode(t, k, "/api/v1/lease", &got, viewerHeader(other))
	if !reflect.DeepEqual(got, wantHeld(false)) {
		t.Errorf("held, another viewer of the same client = %v, want %v", got, wantHeld(false))
	}

	// agent が保持者のときだけ task_id。
	k.api.holder = &service.Holder{LeaseID: "l1", Actor: domain.Actor{Kind: domain.ActorAgent, Label: "agent", SessionID: "task-1"}, IdleRemaining: -time.Second}
	decode(t, k, "/api/v1/lease", &got, viewerHeader(viewer))
	want := lease{"state": "held", "holder": lease{"kind": "agent", "label": "agent", "task_id": "task-1"}, "mine": false, "idle_remaining_sec": 0.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("held by agent = %v, want %v", got, want)
	}

	// 承認待ちは holder を出さず、要求者が呼び出し元なら mine。
	k.api.holder, k.api.pending = nil, &web
	decode(t, k, "/api/v1/lease", &got, viewerHeader(viewer))
	if !reflect.DeepEqual(got, lease{"state": "pending_approval", "mine": true}) {
		t.Errorf("pending, own viewer = %v", got)
	}
	decode(t, k, "/api/v1/lease", &got)
	if !reflect.DeepEqual(got, lease{"state": "pending_approval", "mine": false}) {
		t.Errorf("pending, no viewer = %v", got)
	}
}

func TestAPILeaseRejectsAnUnknownViewerID(t *testing.T) {
	k := newRemote(t)
	requireProblem(t, k, "/api/v1/lease", http.StatusBadRequest, domain.CodeInvalidArgument,
		func(r *http.Request) { r.Header.Set("X-Viewer-Id", "nope") })
}

func TestAPIOpenAPIDocumentsExactlyTheDocumentedRoutes(t *testing.T) {
	k := newRemote(t)
	rec := k.do(http.MethodGet, "/api/v1/openapi.json")
	if rec.Code != http.StatusOK || !strings.HasSuffix(rec.Header().Get("Content-Type"), "+json") {
		t.Fatalf("GET /api/v1/openapi.json = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var spec struct {
		OpenAPI string                    `json:"openapi"`
		Info    struct{ Version string }  `json:"info"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") || spec.Info.Version != "1.2.3" {
		t.Errorf("openapi = %q, version = %q", spec.OpenAPI, spec.Info.Version)
	}
	if len(spec.Paths) != 3 {
		t.Errorf("paths = %v, want status, monitors and lease", spec.Paths)
	}
	for _, p := range []string{"/api/v1/status", "/api/v1/monitors", "/api/v1/lease"} {
		ops, ok := spec.Paths[p]
		if !ok || len(ops) != 1 || ops["get"] == nil {
			t.Errorf("paths[%s] = %v, want a single GET operation", p, ops)
		}
	}
}

// Huma の既定の docs・schemas・YAML の経路は有効にしない。状態を変える経路は無く、仕様に無い経路は 404。
func TestAPIExposesNothingBeyondTheSpec(t *testing.T) {
	k := newRemote(t)
	for _, p := range []string{"/api/v1", "/api/v1/", "/docs", "/api/v1/docs", "/schemas/Problem.json", "/api/v1/schemas/Problem.json",
		"/api/v1/openapi.yaml", "/api/v1/openapi-3.0.json", "/api/v1/status/", "/api/v1/monitors/0"} {
		if rec := k.do(http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	for _, p := range []string{"/api/v1/status", "/api/v1/monitors", "/api/v1/lease", "/api/v1/openapi.json"} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			rec := k.do(m, p, func(r *http.Request) { r.Header.Set(middleware.CSRFHeader, "1") })
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404 (read-only API)", m, p, rec.Code)
			}
		}
	}
	// X-Puppet-Remo が無い状態変更は、経路の手前の連鎖が problem+json で拒否する（§1 の 2）。
	rec := k.do(http.MethodPost, "/api/v1/status")
	if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("POST /api/v1/status without the header = %d %q, want 403 problem+json", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestAPIRunsBehindTheChain(t *testing.T) {
	paths := []string{"/api/v1/status", "/api/v1/monitors", "/api/v1/lease", "/api/v1/openapi.json"}
	t.Run("kill switch", func(t *testing.T) {
		k := newRemote(t)
		k.kill.active = true
		for _, p := range paths {
			requireProblem(t, k, p, http.StatusServiceUnavailable, domain.CodeKillSwitchActive)
		}
	})
	t.Run("unregistered device", func(t *testing.T) {
		k := newRemote(t)
		k.auth.registered = false
		for _, p := range paths {
			requireProblem(t, k, p, http.StatusForbidden, domain.CodeDeviceNotRegistered)
		}
	})
	t.Run("foreign Origin", func(t *testing.T) {
		k := newRemote(t)
		for _, p := range paths {
			requireProblem(t, k, p, http.StatusForbidden, domain.CodeInvalidOrigin,
				func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") })
		}
	})
}

// 連鎖はキルスイッチ中のリクエストを 503 で止めるので、killswitch.active は連鎖の外（ハンドラへの直接の呼び出し）で確かめる。
func TestAPIStatusReportsTheKillSwitch(t *testing.T) {
	f := newAPIFakes(remotetest.New().Sessions)
	h := f.handler(t, &remoteKill{active: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	var got struct {
		KillSwitch struct{ Active bool } `json:"killswitch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.KillSwitch.Active {
		t.Errorf("status with the kill switch active = %s (%v), want killswitch.active true", rec.Body, err)
	}
}

func TestNewAPIRequiresDependencies(t *testing.T) {
	if _, err := api.New(api.Deps{}); err == nil {
		t.Error("api.New with no dependencies must fail")
	}
}
