package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// fakeStates は service.ConnRequestStates の偽実装。引いた node ID を記録する。
type fakeStates struct {
	state service.ConnRequestState
	err   error
	nodes []string
}

func (f *fakeStates) State(_ context.Context, node string) (service.ConnRequestState, error) {
	f.nodes = append(f.nodes, node)
	return f.state, f.err
}

// 未登録デバイスのブラウザには、接続要求の状態に応じた待機・拒否・失効のページを返す（FR-W02）。
// ステータスとエラーコードは変えず、状態は接続元デバイスの ID で引く。
func TestNotRegisteredPageFollowsTheConnectionRequestState(t *testing.T) {
	tests := []struct {
		name      string
		states    func() *fakeStates
		wantState string
		wantText  string
	}{
		{"待機", func() *fakeStates { return &fakeStates{state: service.ConnRequestWaiting} }, "pending", "ホストの許可を待っています"},
		{"拒否", func() *fakeStates { return &fakeStates{state: service.ConnRequestDenied} }, "denied", "拒否されました"},
		{"失効", func() *fakeStates { return &fakeStates{state: service.ConnRequestExpired} }, "expired", "有効期限が切れました"},
		{"状態を読めなければ待機", func() *fakeStates {
			return &fakeStates{state: service.ConnRequestWaiting, err: errors.New("db: secret details")}
		}, "pending", "ホストの許可を待っています"},
		{"参照が無ければ待機", nil, "pending", "ホストの許可を待っています"},
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, tt := range tests {
			t.Run(method+" "+tt.name, func(t *testing.T) {
				k := newKit()
				k.auth.allowErr = service.ErrDeviceNotRegistered
				cfg := RemoteConfig{Hosts: testHosts, KillSwitch: k.kill, WhoIs: k.whois, Authz: k.auth}
				var states *fakeStates
				if tt.states != nil {
					states = tt.states()
					cfg.ConnStates = states
				}
				h := Remote(cfg, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("reached") }))
				r := httptest.NewRequest(method, "http://"+testIP+":8765/viewer", nil)
				r.RemoteAddr = "100.64.0.9:51234"
				r.Header.Set(CSRFHeader, "1")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)

				body := w.Body.String()
				if w.Code != http.StatusForbidden || !strings.Contains(body, `data-error-code="device_not_registered"`) {
					t.Fatalf("status %d, body %q; want 403 device_not_registered", w.Code, body)
				}
				if !strings.Contains(body, `data-connection-state="`+tt.wantState+`"`) || !strings.Contains(body, tt.wantText) {
					t.Errorf("page does not show %q (%q): %q", tt.wantState, tt.wantText, body)
				}
				if strings.Contains(body, "secret details") {
					t.Errorf("internal error detail leaked: %q", body)
				}
				webtest.AssertNoticeCSPCompliantHTML(t, body, w.Header().Get("Content-Security-Policy"))
				if got := w.Header().Get("Content-Security-Policy"); got != NoticeCSPValue {
					t.Errorf("CSP = %q, want the notice page CSP", got)
				}
				if states == nil {
					return
				}
				if len(states.nodes) != 1 || states.nodes[0] != "nodeA" {
					t.Errorf("state lookups = %v, want one lookup by the device ID nodeA", states.nodes)
				}
			})
		}
	}
}

// 状態は未登録のときだけ引く（登録済み・他の拒否・API では引かない）。
func TestConnectionRequestStateIsReadOnlyForNotRegistered(t *testing.T) {
	states := &fakeStates{state: service.ConnRequestDenied}
	k := newKit()
	k.auth.allowErr = service.ErrClientDisabled
	h := Remote(RemoteConfig{Hosts: testHosts, KillSwitch: k.kill, WhoIs: k.whois, Authz: k.auth, ConnStates: states},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, target := range []string{"/viewer", "/api/v1/status"} {
		r := httptest.NewRequest("GET", "http://"+testIP+":8765"+target, nil)
		r.RemoteAddr = "100.64.0.9:51234"
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if len(states.nodes) != 0 {
		t.Errorf("states were read for %v; only a not-registered HTML rejection reads them", states.nodes)
	}

	k.auth.allowErr = service.ErrDeviceNotRegistered
	r := httptest.NewRequest("GET", "http://"+testIP+":8765/api/v1/status", nil)
	r.RemoteAddr = "100.64.0.9:51234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if len(states.nodes) != 0 || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("API not-registered: states read %v, Content-Type %q; want problem+json without a lookup", states.nodes, w.Header().Get("Content-Type"))
	}
}
