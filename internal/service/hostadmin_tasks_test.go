package service

import (
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// cancelledTasks は ClientTaskCanceller の偽物（呼ばれた clientID と理由を記録する）。
type cancelledTasks struct {
	mu    sync.Mutex
	calls []string
}

func (c *cancelledTasks) CancelClient(clientID, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, clientID+"/"+reason)
}

func (c *cancelledTasks) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.calls
	c.calls = nil
	return out
}

// クライアントの削除・無効化（Web・MCP・エージェント）・置き換えで、そのクライアントのタスクを中断する（FR-A09・G10）。
func TestHostAdminCancelsTheClientsTasks(t *testing.T) {
	e := newAdminEnv(t)
	tasks := &cancelledTasks{}
	cfg := HostAdminConfig{
		Clients: e.db.Clients(), Tokens: e.db.Tokens(), ConnRequests: e.db.ConnRequests(), Authz: e.authz,
		Leases: e.leases, Sessions: e.sessions, Tasks: tasks, Audit: e.audit, Bus: e.bus, Clock: e.clock, IDs: &seqIDs{},
	}
	var err error
	if e.admin, err = NewHostAdmin(cfg); err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	c := e.approve(t, "nodeA", "pc", ApproveInput{MCPEnabled: true, WebEnabled: true, AgentEnabled: true})

	// 有効のまま変えるだけ・無効にしないものは中断しない。
	if _, err := e.admin.UpdateClient(e.ctx, c.ID, ClientUpdate{AgentEnabled: &on, MCPEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	if got := tasks.take(); len(got) != 0 {
		t.Errorf("cancelled without a disable: %v", got)
	}
	for name, u := range map[string]ClientUpdate{
		"agent_enabled off": {AgentEnabled: &off},
		"mcp_enabled off":   {MCPEnabled: &off},
		"web_enabled off":   {WebEnabled: &off},
	} {
		if _, err := e.admin.UpdateClient(e.ctx, c.ID, u); err != nil {
			t.Fatal(err)
		}
		if got, want := tasks.take(), []string{c.ID + "/client_disabled"}; !equalStrings(got, want) {
			t.Errorf("%s: cancelled %v, want %v", name, got, want)
		}
		if _, err := e.admin.UpdateClient(e.ctx, c.ID, ClientUpdate{AgentEnabled: &on, MCPEnabled: &on, WebEnabled: &on}); err != nil {
			t.Fatal(err)
		}
		tasks.take()
	}

	if err := e.admin.DeleteClient(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if got, want := tasks.take(), []string{c.ID + "/client_deleted"}; !equalStrings(got, want) {
		t.Errorf("delete: cancelled %v, want %v", got, want)
	}

	// 同じデバイスの再許可（置き換え）で古いクライアントのタスクを中断する。
	old := e.approve(t, "nodeB", "pc2", ApproveInput{MCPEnabled: true})
	r2, _, err := e.db.ConnRequests().UpsertPending(e.ctx, repository.ConnRequest{
		ID: "req-again", StableNodeID: "nodeB", LoginName: "carol@example.com", DisplayName: "again", ExpiresAt: e.clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.ApproveRequest(e.ctx, r2.ID, ApproveInput{MCPEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := tasks.take(), []string{old.ID + "/client_replaced"}; !equalStrings(got, want) {
		t.Errorf("replace: cancelled %v, want %v", got, want)
	}
}
