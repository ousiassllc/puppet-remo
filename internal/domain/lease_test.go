package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func mcpActor(session string) domain.Actor {
	return domain.Actor{Kind: domain.ActorMCP, ClientID: "client-" + session, Label: "label-" + session, SessionID: session}
}

func newMachine(t *testing.T, timeout time.Duration) (*domain.LeaseMachine, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	m, err := domain.NewLeaseMachine(clk, &seqIDGen{}, timeout)
	if err != nil {
		t.Fatalf("NewLeaseMachine: %v", err)
	}
	return m, clk
}

func mustAcquire(t *testing.T, m *domain.LeaseMachine, who domain.Actor) domain.Lease {
	t.Helper()
	l, _, err := m.Acquire(who)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", who.SessionID, err)
	}
	return l
}

type wantEvent struct {
	from, to domain.LeaseState
	reason   domain.ReleaseReason
	holder   string // Lease.Holder.SessionID
}

func assertEvents(t *testing.T, got []domain.LeaseEvent, want ...wantEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %d events", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.From != w.from || g.To != w.to || g.Reason != w.reason || g.Lease.Holder.SessionID != w.holder {
			t.Errorf("events[%d] = {%s→%s reason=%q holder=%q}, want {%s→%s reason=%q holder=%q}",
				i, g.From, g.To, g.Reason, g.Lease.Holder.SessionID, w.from, w.to, w.reason, w.holder)
		}
	}
}

func TestNewLeaseMachineValidation(t *testing.T) {
	clk, ids := newFakeClock(), &seqIDGen{}
	tests := []struct {
		name    string
		clock   domain.Clock
		ids     domain.IDGen
		timeout time.Duration
	}{
		{"タイムアウト 0", clk, ids, 0},
		{"タイムアウト負", clk, ids, -time.Second},
		{"Clock なし", nil, ids, time.Minute},
		{"IDGen なし", clk, nil, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := domain.NewLeaseMachine(tt.clock, tt.ids, tt.timeout); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestLeaseAcquire(t *testing.T) {
	alice, bob := mcpActor("alice"), mcpActor("bob")

	t.Run("web も保持者になれる", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		l := mustAcquire(t, m, webActor("viewer"))
		if l.Holder.Kind != domain.ActorWeb {
			t.Errorf("holder kind = %q, want web", l.Holder.Kind)
		}
	})

	t.Run("idle から取得すると held になる", func(t *testing.T) {
		m, clk := newMachine(t, domain.DefaultLeaseIdleTimeout)
		if m.State() != domain.LeaseIdle {
			t.Fatalf("initial state = %s", m.State())
		}
		l, events, err := m.Acquire(alice)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		assertEvents(t, events, wantEvent{from: domain.LeaseIdle, to: domain.LeaseHeld, holder: "alice"})
		if l.ID != "lease-1" || l.Holder != alice || !l.Since.Equal(clk.Now()) || !l.LastActivity.Equal(clk.Now()) {
			t.Errorf("lease = %+v", l)
		}
		if m.State() != domain.LeaseHeld {
			t.Errorf("state = %s, want held", m.State())
		}
		if h, ok := m.Holder(); !ok || h != l {
			t.Errorf("Holder() = %+v, %v", h, ok)
		}
	})

	t.Run("他者の保持中は lease_held_by_other（待機しない）", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		first, _, _ := m.Acquire(alice)
		_, events, err := m.Acquire(bob)
		if !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Fatalf("err = %v, want ErrLeaseHeldByOther", err)
		}
		var de *domain.Error
		if !errors.As(err, &de) || de.Detail["holder_kind"] != "mcp" || de.Detail["holder_label"] != "label-alice" {
			t.Errorf("detail = %+v", de)
		}
		assertEvents(t, events)
		if h, _ := m.Holder(); h != first {
			t.Errorf("holder changed: %+v", h)
		}
	})

	t.Run("自分が保持中なら同じリースを返し活動時刻だけ更新する", func(t *testing.T) {
		m, clk := newMachine(t, domain.DefaultLeaseIdleTimeout)
		first, _, _ := m.Acquire(alice)
		clk.Advance(time.Minute)
		again, events, err := m.Acquire(alice)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		assertEvents(t, events)
		if again.ID != first.ID || !again.Since.Equal(first.Since) || !again.LastActivity.Equal(clk.Now()) {
			t.Errorf("again = %+v, first = %+v", again, first)
		}
	})

	t.Run("Label が違っても同じセッションなら保持者とみなす", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustAcquire(t, m, alice)
		renamed := alice
		renamed.Label = "renamed"
		if _, _, err := m.Acquire(renamed); err != nil {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("同じ ClientID でもセッションが違えば他者", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustAcquire(t, m, alice)
		other := alice
		other.SessionID = "alice-2"
		if _, _, err := m.Acquire(other); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("err = %v, want ErrLeaseHeldByOther", err)
		}
	})

	t.Run("同じ SessionID でもクライアントが違えば他者", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustAcquire(t, m, alice)
		other := alice
		other.ClientID = "client-other"
		if _, _, err := m.Acquire(other); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("err = %v, want ErrLeaseHeldByOther", err)
		}
	})

	t.Run("保持者種別は mcp と web のみ（agent は M4）", func(t *testing.T) {
		for _, kind := range []domain.ActorKind{domain.ActorAgent, domain.ActorHost, domain.ActorSystem, ""} {
			m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
			who := domain.Actor{Kind: kind, SessionID: "s"}
			if _, events, err := m.Acquire(who); !errors.Is(err, domain.ErrInvalidArgument) || len(events) != 0 {
				t.Errorf("kind %q: err = %v, events = %v", kind, err, events)
			}
			if m.State() != domain.LeaseIdle {
				t.Errorf("kind %q: state = %s, want idle", kind, m.State())
			}
		}
	})

	t.Run("セッション ID が空は不正", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		if _, _, err := m.Acquire(domain.Actor{Kind: domain.ActorMCP}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("err = %v, want ErrInvalidArgument", err)
		}
	})
}

func TestLeaseRelease(t *testing.T) {
	alice, bob := mcpActor("alice"), mcpActor("bob")

	tests := []struct {
		name       string
		releaser   domain.Actor
		reason     domain.ReleaseReason
		wantEvents []wantEvent
		wantState  domain.LeaseState
	}{
		{
			name: "保持者の明示解放で idle に戻る", releaser: alice, reason: domain.ReleaseExplicit,
			wantEvents: []wantEvent{{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseExplicit, holder: "alice"}},
			wantState:  domain.LeaseIdle,
		},
		{
			name: "切断による解放は理由が disconnect", releaser: alice, reason: domain.ReleaseDisconnect,
			wantEvents: []wantEvent{{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseDisconnect, holder: "alice"}},
			wantState:  domain.LeaseIdle,
		},
		{
			name: "保持者でない者の解放は何もしない（保持者不一致）", releaser: bob, reason: domain.ReleaseExplicit,
			wantState: domain.LeaseHeld,
		},
		{
			name: "同じ ClientID で別セッションの解放は何もしない", releaser: domain.Actor{Kind: domain.ActorMCP, ClientID: "client-alice", SessionID: "alice-2"}, reason: domain.ReleaseExplicit,
			wantState: domain.LeaseHeld,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
			mustAcquire(t, m, alice)
			assertEvents(t, m.Release(tt.releaser, tt.reason), tt.wantEvents...)
			if m.State() != tt.wantState {
				t.Errorf("state = %s, want %s", m.State(), tt.wantState)
			}
		})
	}

	t.Run("idle での解放は何もしない", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		assertEvents(t, m.Release(alice, domain.ReleaseExplicit))
	})

	t.Run("解放後は他者が取得できる", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustAcquire(t, m, alice)
		m.Release(alice, domain.ReleaseExplicit)
		l, _, err := m.Acquire(bob)
		if err != nil || l.Holder.SessionID != "bob" || l.ID != "lease-2" {
			t.Errorf("lease = %+v, err = %v", l, err)
		}
	})
}

func TestLeaseReleaseAll(t *testing.T) {
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	assertEvents(t, m.ReleaseAll(domain.ReleaseKillSwitch))

	mustAcquire(t, m, mcpActor("alice"))
	assertEvents(t, m.ReleaseAll(domain.ReleaseKillSwitch),
		wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseKillSwitch, holder: "alice"})
	if m.State() != domain.LeaseIdle {
		t.Errorf("state = %s, want idle", m.State())
	}
	assertEvents(t, m.ReleaseAll(domain.ReleaseKillSwitch))
}

func TestLeaseCheck(t *testing.T) {
	alice, bob := mcpActor("alice"), mcpActor("bob")

	t.Run("idle は lease_required", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		if _, err := m.Check(alice); !errors.Is(err, domain.ErrLeaseRequired) {
			t.Errorf("err = %v, want ErrLeaseRequired", err)
		}
	})

	t.Run("保持者は通り、活動時刻が更新される", func(t *testing.T) {
		m, clk := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustAcquire(t, m, alice)
		clk.Advance(2 * time.Minute)
		if _, err := m.Check(alice); err != nil {
			t.Fatalf("Check: %v", err)
		}
		h, _ := m.Holder()
		if !h.LastActivity.Equal(clk.Now()) {
			t.Errorf("LastActivity = %v, want %v", h.LastActivity, clk.Now())
		}
		deadline, ok := m.IdleDeadline()
		if !ok || !deadline.Equal(clk.Now().Add(domain.DefaultLeaseIdleTimeout)) {
			t.Errorf("IdleDeadline = %v, %v", deadline, ok)
		}
	})

	t.Run("他者の保持中は lease_held_by_other", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustAcquire(t, m, alice)
		if _, err := m.Check(bob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("err = %v, want ErrLeaseHeldByOther", err)
		}
	})

	t.Run("他者の Check は保持者の活動時刻を更新しない", func(t *testing.T) {
		m, clk := newMachine(t, domain.DefaultLeaseIdleTimeout)
		first, _, _ := m.Acquire(alice)
		clk.Advance(time.Minute)
		_, _ = m.Check(bob)
		if h, _ := m.Holder(); !h.LastActivity.Equal(first.LastActivity) {
			t.Errorf("LastActivity = %v, want %v", h.LastActivity, first.LastActivity)
		}
	})
}

func TestLeaseIdleTimeout(t *testing.T) {
	const timeout = 5 * time.Minute
	alice, bob := mcpActor("alice"), mcpActor("bob")

	t.Run("期限の直前は保持され、ちょうど期限で解放される", func(t *testing.T) {
		m, clk := newMachine(t, timeout)
		mustAcquire(t, m, alice)

		clk.Advance(timeout - time.Nanosecond)
		assertEvents(t, m.Expire())
		if m.State() != domain.LeaseHeld {
			t.Fatalf("state just before the deadline = %s, want held", m.State())
		}

		clk.Advance(time.Nanosecond)
		if m.State() != domain.LeaseIdle {
			t.Errorf("State() at the deadline = %s, want idle (read-only view)", m.State())
		}
		assertEvents(t, m.Expire(),
			wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"})
		assertEvents(t, m.Expire())
		if _, ok := m.IdleDeadline(); ok {
			t.Error("IdleDeadline reported for idle state")
		}
	})

	t.Run("Check が期限を延ばす", func(t *testing.T) {
		m, clk := newMachine(t, timeout)
		mustAcquire(t, m, alice)
		for range 3 {
			clk.Advance(timeout - time.Second)
			if _, err := m.Check(alice); err != nil {
				t.Fatalf("Check: %v", err)
			}
		}
		clk.Advance(timeout)
		assertEvents(t, m.Expire(),
			wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"})
	})

	t.Run("期限切れの後の Check は解放を記録して lease_required", func(t *testing.T) {
		m, clk := newMachine(t, timeout)
		mustAcquire(t, m, alice)
		clk.Advance(timeout)
		events, err := m.Check(alice)
		if !errors.Is(err, domain.ErrLeaseRequired) {
			t.Errorf("err = %v, want ErrLeaseRequired", err)
		}
		assertEvents(t, events,
			wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"})
	})

	t.Run("期限切れの後の他者の Acquire は、解放と取得を順に返す（タイマー遅延に依存しない）", func(t *testing.T) {
		m, clk := newMachine(t, timeout)
		mustAcquire(t, m, alice)
		clk.Advance(timeout + time.Second)
		l, events, err := m.Acquire(bob)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		assertEvents(t, events,
			wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"},
			wantEvent{from: domain.LeaseIdle, to: domain.LeaseHeld, holder: "bob"})
		if l.Holder.SessionID != "bob" || l.ID != "lease-2" {
			t.Errorf("lease = %+v", l)
		}
	})

	t.Run("期限切れの後の元の保持者の Acquire は新しいリース", func(t *testing.T) {
		m, clk := newMachine(t, timeout)
		first, _, _ := m.Acquire(alice)
		clk.Advance(timeout)
		l, events, err := m.Acquire(alice)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if l.ID == first.ID || len(events) != 2 {
			t.Errorf("lease = %+v, events = %+v", l, events)
		}
	})

	t.Run("期限切れの後の Release は解放（idle_timeout）だけを返す", func(t *testing.T) {
		m, clk := newMachine(t, timeout)
		mustAcquire(t, m, alice)
		clk.Advance(timeout)
		assertEvents(t, m.Release(alice, domain.ReleaseExplicit),
			wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"})
	})

	t.Run("タイムアウトは設定できる", func(t *testing.T) {
		m, clk := newMachine(t, 10*time.Second)
		mustAcquire(t, m, alice)
		clk.Advance(9 * time.Second)
		assertEvents(t, m.Expire())
		clk.Advance(time.Second)
		assertEvents(t, m.Expire(),
			wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"})
	})
}

func TestActorKindCanHoldLease(t *testing.T) {
	tests := []struct {
		kind domain.ActorKind
		want bool
	}{
		{domain.ActorMCP, true},
		{domain.ActorWeb, true},
		{domain.ActorAgent, false},
		{domain.ActorHost, false},
		{domain.ActorSystem, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.kind.CanHoldLease(); got != tt.want {
			t.Errorf("%q.CanHoldLease() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestActorKindCanRevokeLease(t *testing.T) {
	tests := []struct {
		kind domain.ActorKind
		want bool
	}{
		{domain.ActorHost, true},
		{domain.ActorWeb, true},
		{domain.ActorMCP, false},
		{domain.ActorAgent, false},
		{domain.ActorSystem, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.kind.CanRevokeLease(); got != tt.want {
			t.Errorf("%q.CanRevokeLease() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func webActor(session string) domain.Actor {
	return domain.Actor{Kind: domain.ActorWeb, ClientID: "client-" + session, Label: "label-" + session, SessionID: session}
}

var hostActor = domain.Actor{Kind: domain.ActorHost, Label: "(host)"}

func TestLeaseRevoke(t *testing.T) {
	alice, bob, viewer := mcpActor("alice"), mcpActor("bob"), webActor("viewer")

	t.Run("ホストが保持者を剥奪すると idle になり、revoked の事象に剥奪した側が入る", func(t *testing.T) {
		m, _ := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		events, err := m.Revoke(hostActor)
		if err != nil {
			t.Fatal(err)
		}
		assertEvents(t, events, wantEvent{domain.LeaseHeld, domain.LeaseIdle, domain.ReleaseRevoked, "alice"})
		if events[0].By != hostActor {
			t.Errorf("By = %+v, want the host", events[0].By)
		}
		if got := m.State(); got != domain.LeaseIdle {
			t.Errorf("State = %q, want idle", got)
		}
	})

	t.Run("web も他者のリースを剥奪できる", func(t *testing.T) {
		m, _ := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		events, err := m.Revoke(viewer)
		if err != nil {
			t.Fatal(err)
		}
		assertEvents(t, events, wantEvent{domain.LeaseHeld, domain.LeaseIdle, domain.ReleaseRevoked, "alice"})
		if events[0].By != viewer {
			t.Errorf("By = %+v, want the web actor", events[0].By)
		}
	})

	t.Run("web の保持者を剥奪できる", func(t *testing.T) {
		m, _ := newMachine(t, time.Minute)
		mustAcquire(t, m, viewer)
		events, err := m.Revoke(hostActor)
		if err != nil {
			t.Fatal(err)
		}
		assertEvents(t, events, wantEvent{domain.LeaseHeld, domain.LeaseIdle, domain.ReleaseRevoked, "viewer"})
	})

	t.Run("保持者がいなければ何もしない（冪等）", func(t *testing.T) {
		m, _ := newMachine(t, time.Minute)
		events, err := m.Revoke(hostActor)
		if err != nil || len(events) != 0 {
			t.Errorf("Revoke on idle = %+v, %v; want no events and no error", events, err)
		}
		if err := checkOnly(m, alice); !errors.Is(err, domain.ErrLeaseRequired) {
			t.Errorf("Check after a no-op revoke = %v, want ErrLeaseRequired (nobody was revoked)", err)
		}
	})

	t.Run("mcp・agent・system・空の種別は剥奪できず、リースは変わらない", func(t *testing.T) {
		for _, kind := range []domain.ActorKind{domain.ActorMCP, domain.ActorAgent, domain.ActorSystem, ""} {
			m, _ := newMachine(t, time.Minute)
			mustAcquire(t, m, alice)
			events, err := m.Revoke(domain.Actor{Kind: kind, SessionID: "x"})
			if !errors.Is(err, domain.ErrInvalidArgument) || len(events) != 0 {
				t.Errorf("Revoke by %q = %+v, %v; want ErrInvalidArgument and no events", kind, events, err)
			}
			if _, ok := m.Holder(); !ok {
				t.Errorf("Revoke by %q released the lease", kind)
			}
		}
	})

	t.Run("剥奪された保持者の Check は、次の Acquire まで ErrLeaseRevoked を返し続ける", func(t *testing.T) {
		m, clk := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		if _, err := m.Revoke(hostActor); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			clk.Advance(10 * time.Second)
			if err := checkOnly(m, alice); !errors.Is(err, domain.ErrLeaseRevoked) {
				t.Fatalf("Check by the revoked holder = %v, want ErrLeaseRevoked", err)
			}
		}
		if err := checkOnly(m, bob); !errors.Is(err, domain.ErrLeaseRequired) {
			t.Errorf("Check by another session = %v, want ErrLeaseRequired", err)
		}
	})

	t.Run("剥奪された保持者が Acquire し直すと印が消え、解放後は ErrLeaseRequired に戻る", func(t *testing.T) {
		m, _ := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		if _, err := m.Revoke(hostActor); err != nil {
			t.Fatal(err)
		}
		mustAcquire(t, m, alice)
		if err := checkOnly(m, alice); err != nil {
			t.Errorf("Check after re-acquire = %v, want nil", err)
		}
		m.Release(alice, domain.ReleaseExplicit)
		if err := checkOnly(m, alice); !errors.Is(err, domain.ErrLeaseRequired) {
			t.Errorf("Check after the re-acquired lease was released = %v, want ErrLeaseRequired", err)
		}
	})

	t.Run("別の保持者が現れたら、剥奪された側は ErrLeaseHeldByOther になり、その保持者が解放されても ErrLeaseRequired", func(t *testing.T) {
		m, _ := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		if _, err := m.Revoke(hostActor); err != nil {
			t.Fatal(err)
		}
		mustAcquire(t, m, bob)
		if err := checkOnly(m, alice); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Check by the revoked holder while bob holds = %v, want ErrLeaseHeldByOther", err)
		}
		m.Release(bob, domain.ReleaseExplicit)
		if err := checkOnly(m, alice); !errors.Is(err, domain.ErrLeaseRequired) {
			t.Errorf("Check after bob released = %v, want ErrLeaseRequired (the revoked mark ended with bob's Acquire)", err)
		}
	})

	t.Run("期限切れのリースは idle_timeout で解放され、剥奪の対象にならない", func(t *testing.T) {
		m, clk := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		clk.Advance(time.Minute)
		events, err := m.Revoke(hostActor)
		if err != nil {
			t.Fatal(err)
		}
		assertEvents(t, events, wantEvent{domain.LeaseHeld, domain.LeaseIdle, domain.ReleaseIdleTimeout, "alice"})
		if err := checkOnly(m, alice); !errors.Is(err, domain.ErrLeaseRequired) {
			t.Errorf("Check = %v, want ErrLeaseRequired (the lease expired, it was not revoked)", err)
		}
	})
}

func checkOnly(m *domain.LeaseMachine, who domain.Actor) error {
	_, err := m.Check(who)
	return err
}

func TestSetIdleTimeout(t *testing.T) {
	alice := mcpActor("alice")

	t.Run("0 以下は ErrInvalidArgument で値を変えない", func(t *testing.T) {
		m, clk := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		for _, d := range []time.Duration{0, -time.Second} {
			if err := m.SetIdleTimeout(d); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("SetIdleTimeout(%s) = %v, want ErrInvalidArgument", d, err)
			}
		}
		if dl, _ := m.IdleDeadline(); !dl.Equal(clk.Now().Add(time.Minute)) {
			t.Errorf("deadline = %v, want unchanged (+1m)", dl)
		}
	})

	t.Run("保持中のリースの期限は最後の活動から新しい値で数え直す", func(t *testing.T) {
		m, clk := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		clk.Advance(30 * time.Second)
		if err := m.SetIdleTimeout(10 * time.Minute); err != nil {
			t.Fatal(err)
		}
		if dl, _ := m.IdleDeadline(); !dl.Equal(clk.Now().Add(10*time.Minute - 30*time.Second)) {
			t.Errorf("deadline = %v, want acquire time + 10m", dl)
		}
		clk.Advance(2 * time.Minute) // 旧タイムアウトなら期限切れだった
		if m.State() != domain.LeaseHeld {
			t.Errorf("state = %s, want held under the longer timeout", m.State())
		}
	})

	t.Run("短くして期限を過ぎたら次の Expire で解放される", func(t *testing.T) {
		m, clk := newMachine(t, 10*time.Minute)
		mustAcquire(t, m, alice)
		clk.Advance(2 * time.Minute)
		if err := m.SetIdleTimeout(time.Minute); err != nil {
			t.Fatal(err)
		}
		assertEvents(t, m.Expire(), wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"})
	})
}
