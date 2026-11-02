package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// FR-L08: 保持者種別 agent は Actor{Kind: agent, ClientID: 依頼したクライアント, SessionID: task_id}。

func TestLeaseAgentHolderLifecycle(t *testing.T) {
	e := newCtlEnv(t)
	sub := e.leases.Subscribe()
	l := mustAcquire(t, e, ctlAgent)
	if l.Holder.Kind != domain.ActorAgent || !l.Holder.SameSession(ctlAgent) {
		t.Fatalf("lease = %+v", l)
	}
	h, ok := e.leases.Holder()
	if !ok || h.Actor.Kind != domain.ActorAgent || h.LeaseID != l.ID {
		t.Fatalf("Holder = %+v, %v", h, ok)
	}
	if err := e.leases.Check(ctlAgent); err != nil {
		t.Fatalf("Check(holder) = %v", err)
	}
	// 同じクライアントの別タスク（別セッション）・mcp・web は保持者ではない。
	for _, other := range []domain.Actor{
		{Kind: domain.ActorAgent, ClientID: "client-a", SessionID: "task-2"},
		ctlAlice,
		ctlViewer,
	} {
		if err := e.leases.Check(other); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Check(%v) = %v, want ErrLeaseHeldByOther", other.Kind, err)
		}
		if _, err := e.leases.Acquire(context.Background(), other); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Acquire(%v) = %v, want ErrLeaseHeldByOther (FR-L08)", other.Kind, err)
		}
	}
	if !e.power.Awake() {
		t.Error("KeepAwake should be on while the agent holds the lease")
	}
	e.leases.Release(ctlAgent)
	if _, ok := e.leases.Holder(); ok {
		t.Fatal("Release by the holder must free the lease")
	}
	if err := e.leases.Check(ctlAgent); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Errorf("Check after release = %v, want ErrLeaseRequired", err)
	}
	if e.power.Awake() {
		t.Error("KeepAwake should be off after the release")
	}
	events := drain(sub)
	if len(events) != 2 || events[0].To != domain.LeaseHeld || events[1].Reason != domain.ReleaseExplicit || !events[1].Lease.Holder.SameSession(ctlAgent) {
		t.Fatalf("events = %+v", events)
	}

	// 監査: 操作主体 agent・client_id・session_id = task_id。
	var acquire, release bool
	for _, en := range e.audit.Entries() {
		switch en.Event {
		case "lease.acquire":
			acquire = acquire || en.Kind == "lease" && en.ActorType == domain.ActorAgent && en.ClientID == "client-a" && en.SessionID == "task-1" && en.Result == "ok"
		case "lease.release":
			release = release || en.ActorType == domain.ActorAgent && en.SessionID == "task-1" && string(en.DetailJSON) == `{"reason":"explicit"}`
		}
	}
	if !acquire || !release {
		t.Errorf("audit = %+v", e.audit.Entries())
	}
}

func TestLeaseAgentAcquireIsRejectedWhenAnotherHolds(t *testing.T) {
	for name, holder := range map[string]domain.Actor{"mcp": ctlAlice, "web": ctlViewer} {
		t.Run(name, func(t *testing.T) {
			e := newCtlEnv(t)
			mustAcquire(t, e, holder)
			_, err := e.leases.Acquire(context.Background(), ctlAgent)
			if !errors.Is(err, domain.ErrLeaseHeldByOther) {
				t.Fatalf("err = %v, want ErrLeaseHeldByOther", err)
			}
			var denied bool
			for _, en := range e.audit.Entries() {
				if en.Event == "lease.acquire" && en.ActorType == domain.ActorAgent && en.Result == "denied" && en.ErrorCode == "lease_held_by_other" {
					denied = true
				}
			}
			if !denied {
				t.Errorf("audit = %+v", e.audit.Entries())
			}
		})
	}
}

func TestLeaseAgentIdleTimeoutAndActivity(t *testing.T) {
	e := newCtlEnv(t)
	sub := e.leases.Subscribe()
	mustAcquire(t, e, ctlAgent)
	e.clock.Advance(4 * time.Minute)
	if err := e.leases.Check(ctlAgent); err != nil { // エージェントのアクション実行は活動
		t.Fatal(err)
	}
	e.clock.Advance(4 * time.Minute)
	e.timers.FireAll()
	if _, ok := e.leases.Holder(); !ok {
		t.Fatal("activity must extend the agent's lease")
	}
	e.clock.Advance(2 * time.Minute)
	e.timers.FireAll()
	if _, ok := e.leases.Holder(); ok {
		t.Fatal("the idle agent lease must be released")
	}
	got := drain(sub)
	if len(got) != 2 || got[1].Reason != domain.ReleaseIdleTimeout || !got[1].Lease.Holder.SameSession(ctlAgent) {
		t.Fatalf("events = %+v", got)
	}
}

func TestLeaseAgentReleaseSessionAndReleaseAll(t *testing.T) {
	tests := []struct {
		name string
		act  func(e *ctlEnv)
		want string
	}{
		{"ReleaseSession", func(e *ctlEnv) { e.leases.ReleaseSession(ctlAgent) }, "disconnect"},
		{"ReleaseAll", func(e *ctlEnv) { e.leases.ReleaseAll("kill_switch") }, "kill_switch"},
		{"kill switch", func(e *ctlEnv) { _ = e.kill.Trigger("test") }, "kill_switch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			mustAcquire(t, e, ctlAgent)
			tt.act(e)
			if _, ok := e.leases.Holder(); ok {
				t.Fatal("the lease should be released")
			}
			var reason string
			for _, en := range e.audit.Entries() {
				if en.Event == "lease.release" && en.ActorType == domain.ActorAgent {
					reason = string(en.DetailJSON)
				}
			}
			if want := `{"reason":"` + tt.want + `"}`; reason != want {
				t.Fatalf("detail = %s, want %s", reason, want)
			}
		})
	}
}

// 強制剥奪（FR-L06）は agent の保持者にも効く: reason = revoked で解放され、lease イベントで Agent がタスクを中断できる。
func TestLeaseRevokeOfAnAgentHolder(t *testing.T) {
	for _, by := range []domain.Actor{HostActor, ctlViewer} {
		t.Run(string(by.Kind), func(t *testing.T) {
			e := newCtlEnv(t)
			sub := e.leases.Subscribe()
			l := mustAcquire(t, e, ctlAgent)
			drain(sub)
			if err := e.leases.Revoke(by); err != nil {
				t.Fatal(err)
			}
			if _, ok := e.leases.Holder(); ok {
				t.Fatal("the agent's lease should be revoked")
			}
			events := drain(sub)
			if len(events) != 1 || events[0].Reason != domain.ReleaseRevoked || events[0].By != by || !events[0].Lease.Holder.SameSession(ctlAgent) {
				t.Fatalf("events = %+v", events)
			}
			if err := e.leases.Check(ctlAgent); !errors.Is(err, domain.ErrLeaseRevoked) {
				t.Fatalf("Check = %v, want ErrLeaseRevoked", err)
			}
			last := e.audit.Entries()[len(e.audit.Entries())-1]
			if last.Event != "lease.revoke" || last.ActorType != by.Kind || last.LeaseID != l.ID ||
				string(last.DetailJSON) != `{"holder_client_id":"client-a","holder_kind":"agent","holder_label":"agent"}` {
				t.Fatalf("revoke entry = %+v (%s)", last, last.DetailJSON)
			}
			// 剥奪後は Control も agent のアクションを通さない（Judge の前に ErrLeaseRevoked）。
			if _, err := e.control.Do(context.Background(), ctlAgent, domain.TypeText{Text: "a"}); !errors.Is(err, domain.ErrLeaseRevoked) {
				t.Fatalf("Do = %v, want ErrLeaseRevoked", err)
			}
		})
	}
}

// 承認モード（FR-L07）は agent にも、依頼したクライアントの設定で適用する。
func TestLeaseAgentFollowsTheRequestingClientsApprovalMode(t *testing.T) {
	t.Run("always asks on every task", func(t *testing.T) {
		le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
		out, req := le.acquire(t, context.Background(), ctlAgent)
		if req.Kind != ApprovalKindLease || !req.Who.SameSession(ctlAgent) || req.Who.Kind != domain.ActorAgent {
			t.Fatalf("request = %+v", req)
		}
		if _, ok := le.leases.Holder(); ok {
			t.Error("not held while pending")
		}
		if err := le.approval.Resolve(req.ID, true); err != nil {
			t.Fatal(err)
		}
		if r := le.wait(t, out); r.err != nil || r.l.Holder.Kind != domain.ActorAgent {
			t.Fatalf("acquire = %+v", r)
		}
		le.leases.Release(ctlAgent)
		next := domain.Actor{Kind: domain.ActorAgent, ClientID: "client-a", Label: "agent", SessionID: "task-2"}
		out, req = le.acquire(t, context.Background(), next)
		if !req.Who.SameSession(next) {
			t.Fatalf("request = %+v", req)
		}
		_ = le.approval.Resolve(req.ID, true)
		le.wait(t, out)
	})
	t.Run("auto never asks", func(t *testing.T) {
		le := newLeaseApprovalEnv(t, domain.ApprovalAuto)
		if _, err := le.leases.Acquire(context.Background(), ctlAgent); err != nil {
			t.Fatal(err)
		}
		le.expectNoApproval(t)
	})
	t.Run("first_time asks once per client", func(t *testing.T) {
		le := newLeaseApprovalEnv(t, domain.ApprovalFirstTime)
		out, req := le.acquire(t, context.Background(), ctlAgent)
		_ = le.approval.Resolve(req.ID, true)
		if r := le.wait(t, out); r.err != nil {
			t.Fatal(r.err)
		}
		if ids := le.clients.markedIDs(); len(ids) != 1 || ids[0] != "client-a" {
			t.Fatalf("marked = %v", ids)
		}
		le.leases.Release(ctlAgent)
		next := domain.Actor{Kind: domain.ActorAgent, ClientID: "client-a", Label: "agent", SessionID: "task-2"}
		if _, err := le.leases.Acquire(context.Background(), next); err != nil {
			t.Fatal(err)
		}
		le.expectNoApproval(t)
	})
	t.Run("a denial puts the client on cooldown for every holder kind", func(t *testing.T) {
		le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
		out, req := le.acquire(t, context.Background(), ctlAgent)
		_ = le.approval.Resolve(req.ID, false)
		if r := le.wait(t, out); !errors.Is(r.err, domain.ErrApprovalDenied) {
			t.Fatalf("err = %v", r.err)
		}
		if _, err := le.leases.Acquire(context.Background(), ctlAlice); !errors.Is(err, domain.ErrApprovalDenied) {
			t.Fatalf("mcp of the same client during the cooldown: err = %v", err)
		}
		le.expectNoApproval(t)
	})
	t.Run("an agent without a client id is not registered", func(t *testing.T) {
		le := newLeaseApprovalEnv(t, domain.ApprovalAuto)
		_, err := le.leases.Acquire(context.Background(), domain.Actor{Kind: domain.ActorAgent, SessionID: "task-1"})
		if !errors.Is(err, ErrDeviceNotRegistered) {
			t.Fatalf("err = %v, want ErrDeviceNotRegistered", err)
		}
	})
}

// agent のアクション実行はリースの活動（アイドルタイムアウトの起点）。
func TestControlAgentActionsKeepTheLeaseAlive(t *testing.T) {
	je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictAllow})
	do := func() {
		t.Helper()
		if _, err := je.control.Do(context.Background(), ctlAgent, domain.TypeText{Text: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	do()
	je.clock.Advance(4 * time.Minute)
	do()
	je.clock.Advance(4 * time.Minute)
	do()
	je.clock.Advance(5 * time.Minute)
	if err := je.leases.Check(ctlAgent); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("the idle lease should have expired, got %v", err)
	}
}
