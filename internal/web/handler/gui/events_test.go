package gui_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// sseEvent は受信した 1 イベント（data は改行で連結し直したもの）。
type sseEvent struct{ Name, Data string }

type sseStream struct {
	t      *testing.T
	resp   *http.Response
	events chan sseEvent
	pings  chan struct{}
	cancel context.CancelFunc
}

const sseWait = 5 * time.Second // 上限付きの待ち（固定の sleep ではない）

func openSSE(t *testing.T, r *rig) *sseStream {
	t.Helper()
	srv := httptest.NewServer(r.h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	s := &sseStream{t: t, resp: res, events: make(chan sseEvent, 64), pings: make(chan struct{}, 64), cancel: cancel}
	t.Cleanup(func() { cancel(); _ = res.Body.Close() })
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var cur sseEvent
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.Name != "" {
					cur.Data = strings.Join(data, "\n")
					s.events <- cur
				}
				cur, data = sseEvent{}, nil
			case strings.HasPrefix(line, ": "):
				s.pings <- struct{}{}
			case strings.HasPrefix(line, "event: "):
				cur.Name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()
	return s
}

func (s *sseStream) next() sseEvent {
	s.t.Helper()
	select {
	case e, ok := <-s.events:
		if !ok {
			s.t.Fatal("event stream closed")
		}
		return e
	case <-time.After(sseWait):
		s.t.Fatal("timed out waiting for an SSE event")
	}
	return sseEvent{}
}

func (s *sseStream) nextNamed(name string) sseEvent {
	s.t.Helper()
	for {
		if e := s.next(); e.Name == name {
			return e
		}
	}
}

func TestEventsSendsCurrentStateOnConnect(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Status.V = service.ServerStatus{Tailscaled: service.TailscaledRunning, TailnetIPs: []string{"100.64.0.1"}, Port: 8765, Listening: true}
		f.Sessions.Items = []service.SessionInfo{{ID: "s1", Label: "claude-desktop", Kind: domain.ActorMCP, Since: f.Clock.T}}
		f.Leases.Held = true
		f.Leases.H = service.Holder{Actor: domain.Actor{Kind: domain.ActorMCP, Label: "claude-desktop"}, Since: f.Clock.T, IdleRemaining: time.Minute}
		seedRequest(f, "req-1", "bob-laptop")
		f.KillSwitch.Snapshot = service.KillSwitchSnapshot{Active: true, TriggeredAt: f.Clock.T, Source: "gui"}
	})
	s := openSSE(t, r)
	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := s.resp.Header.Get("Content-Security-Policy"); got != middleware.CSPValue {
		t.Errorf("CSP = %q", got)
	}
	want := map[string][]string{
		"server-status":       {"100.64.0.1", "稼働中"},
		"sessions":            {"claude-desktop"},
		"lease":               {"保持中", "claude-desktop", "MCP"},
		"connection-requests": {`data-pending-count="1"`},
		"killswitch":          {"キルスイッチ発動中"},
	}
	got := map[string]string{}
	for range want {
		e := s.next()
		got[e.Name] = e.Data
	}
	for name, subs := range want {
		data, ok := got[name]
		if !ok {
			t.Errorf("no initial %q event", name)
			continue
		}
		for _, sub := range subs {
			if !strings.Contains(data, sub) {
				t.Errorf("%s event lacks %q: %s", name, sub, data)
			}
		}
		assertCSPCompliantHTML(t, data)
	}
}

func TestEventsPushesFragmentsOnChange(t *testing.T) {
	r := newRig(t)
	s := openSSE(t, r)
	for range 5 { // 接続直後の現在値
		s.next()
	}

	// 接続要求が増える → 件数バッジのフラグメント。
	seedRequest(r.f, "req-1", "bob-laptop")
	r.f.Bus.Publish(service.Event{Topic: service.TopicConnectionRequests})
	if e := s.nextNamed("connection-requests"); !strings.Contains(e.Data, `data-pending-count="1"`) {
		t.Errorf("badge event = %q", e.Data)
	}

	// キルスイッチが発動する → ヘッダーのバッジのフラグメント。
	if err := r.f.KillSwitch.Trigger("hotkey"); err != nil {
		t.Fatal(err)
	}
	if e := s.nextNamed("killswitch"); !strings.Contains(e.Data, "キルスイッチ発動中") {
		t.Errorf("killswitch event = %q", e.Data)
	}

	// リースの変化 → 保持者のフラグメント。表示だけで、強制剥奪の操作は含まない（/events は M2 でも変えない）。
	r.f.Leases.Held = true
	r.f.Leases.H = service.Holder{Actor: domain.Actor{Kind: domain.ActorMCP, Label: "claude-desktop"}, Since: r.f.Clock.T, IdleRemaining: 5 * time.Minute}
	r.f.Bus.Publish(service.Event{Topic: service.TopicLease})
	e := s.nextNamed("lease")
	if !strings.Contains(e.Data, "claude-desktop") || strings.Contains(e.Data, "revoke") {
		t.Errorf("lease event = %q", e.Data)
	}

	// toast（エラー）は Data の内容をそのまま（エスケープして）インラインアラートとして流す。
	r.f.Bus.Publish(service.Event{Topic: service.TopicToast, Data: service.Toast{Level: "error", Message: "<b>壊れた</b>"}})
	e = s.nextNamed("toast")
	if !strings.Contains(e.Data, `data-alert-tone="danger"`) || !strings.Contains(e.Data, "&lt;b&gt;壊れた&lt;/b&gt;") || strings.Contains(e.Data, "<b>") {
		t.Errorf("toast event = %q", e.Data)
	}
}

func TestEventsMultilineDataStaysOneEvent(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Sessions.Items = []service.SessionInfo{{ID: "s1", Label: "a", Kind: domain.ActorMCP, Since: f.Clock.T}, {ID: "s2", Label: "b", Kind: domain.ActorMCP, Since: f.Clock.T}}
	})
	s := openSSE(t, r)
	e := s.nextNamed("sessions")
	if strings.Count(e.Data, "data-session") != 2 {
		t.Errorf("sessions data = %q", e.Data)
	}
}

func TestEventsKeepaliveAndShutdown(t *testing.T) {
	f := guitest.New()
	r := newRigFrom(t, f, 5*time.Millisecond)
	s := openSSE(t, r)
	select {
	case <-s.pings:
	case <-time.After(sseWait):
		t.Fatal("no keepalive comment")
	}
	// クライアントの切断でハンドラが終わる（購読が解除され、ストリームが閉じる）。
	s.cancel()
	deadline := time.After(sseWait)
	for {
		select {
		case _, ok := <-s.events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream did not close after the client went away")
		}
	}
}
