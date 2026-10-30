package mcp

import (
	"net/http"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// tokenPrincipal は testPrincipal にトークン（ID は "tok-<クライアント ID>"）を足した主体。
func tokenPrincipal(r *http.Request) (service.Principal, bool) {
	p, ok := testPrincipal(r)
	if ok {
		p.Token = &repository.TokenRecord{ID: "tok-" + p.Client.ID}
	}
	return p, ok
}

// eventually は cond が真になるまで、上限つきで繰り返し確認する。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func sessionClients(s *Server) []string {
	var ids []string
	for _, si := range s.Sessions() {
		ids = append(ids, si.ClientID)
	}
	return ids
}

func TestSessionsListsConnectedClients(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Principal = tokenPrincipal })
	if got := e.srv.Sessions(); len(got) != 0 {
		t.Fatalf("Sessions before any connection = %v, want none", got)
	}

	cs1 := e.connect("c1")
	eventually(t, "c1 session", func() bool { return len(e.srv.Sessions()) == 1 })
	e.connect("c2")
	eventually(t, "c2 session", func() bool { return len(e.srv.Sessions()) == 2 })

	got := e.srv.Sessions()
	if got[0].ClientID != "c1" || got[1].ClientID != "c2" {
		t.Errorf("sessions are not in connection order: %+v", got)
	}
	first := got[0]
	if first.ID == "" || first.Label != "client-c1" || first.Kind != domain.ActorMCP || first.Since.IsZero() {
		t.Errorf("session info = %+v", first)
	}

	// クライアントが切断したら表から外れる。
	if err := cs1.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c1 to leave the table", func() bool { return len(e.srv.Sessions()) == 1 })
	if ids := sessionClients(e.srv); len(ids) != 1 || ids[0] != "c2" {
		t.Errorf("remaining sessions = %v, want [c2]", ids)
	}
}

func TestCloseClientClosesOnlyThatClientsSessionsAndReleasesTheLease(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Principal = tokenPrincipal })
	cs1 := e.connect("c1")
	e.connect("c2")
	if res := callTool(t, cs1, "acquire_control", nil); res.IsError {
		t.Fatal(res.text())
	}
	eventually(t, "two sessions", func() bool { return len(e.srv.Sessions()) == 2 })

	e.srv.CloseClient("c1", domain.ActorMCP)

	eventually(t, "c1 to leave the table", func() bool { return len(e.srv.Sessions()) == 1 })
	if ids := sessionClients(e.srv); ids[0] != "c2" {
		t.Errorf("remaining sessions = %v, want [c2]", ids)
	}
	e.waitEvent(isRelease)
	if _, held := e.leases.Holder(); held {
		t.Error("the closed session's lease was not released")
	}
}

func TestCloseClientIgnoresOtherActorKinds(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Principal = tokenPrincipal })
	e.connect("c1")
	eventually(t, "c1 session", func() bool { return len(e.srv.Sessions()) == 1 })

	e.srv.CloseClient("c1", domain.ActorWeb)

	if got := e.srv.Sessions(); len(got) != 1 {
		t.Errorf("a web close must not touch MCP sessions: %+v", got)
	}
}

func TestCloseTokenClosesSessionsOfThatToken(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Principal = tokenPrincipal })
	e.connect("c1")
	e.connect("c2")
	eventually(t, "two sessions", func() bool { return len(e.srv.Sessions()) == 2 })

	e.srv.CloseToken("tok-c2")
	eventually(t, "c2 to leave the table", func() bool { return len(e.srv.Sessions()) == 1 })
	if ids := sessionClients(e.srv); ids[0] != "c1" {
		t.Errorf("remaining sessions = %v, want [c1]", ids)
	}

	e.srv.CloseToken("") // 空は何も閉じない
	if got := e.srv.Sessions(); len(got) != 1 {
		t.Errorf("an empty token ID must not close sessions: %+v", got)
	}
}

func TestCloseSessionsClearsTheTable(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Principal = tokenPrincipal })
	e.connect("c1")
	e.connect("c2")
	eventually(t, "two sessions", func() bool { return len(e.srv.Sessions()) == 2 })

	if err := e.srv.CloseSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the table to empty", func() bool { return len(e.srv.Sessions()) == 0 })
}
