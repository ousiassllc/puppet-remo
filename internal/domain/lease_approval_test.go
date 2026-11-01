package domain_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func mustRequest(t *testing.T, m *domain.LeaseMachine, who domain.Actor) domain.Lease {
	t.Helper()
	l, events, err := m.Request(who)
	if err != nil {
		t.Fatalf("Request(%s): %v", who.SessionID, err)
	}
	assertEvents(t, events, wantEvent{from: domain.LeaseIdle, to: domain.LeasePendingApproval, holder: who.SessionID})
	return l
}

func TestLeaseRequest(t *testing.T) {
	alice, bob := mcpActor("alice"), mcpActor("bob")

	t.Run("idle → pending_approval。リースの ID は承認後も同じ", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		req := mustRequest(t, m, alice)
		if m.State() != domain.LeasePendingApproval {
			t.Fatalf("state = %s, want pending_approval", m.State())
		}
		if _, held := m.Holder(); held {
			t.Error("Holder は pending_approval の間 held ではない")
		}
		p, ok := m.Pending()
		if !ok || p.ID != req.ID || !p.Holder.SameSession(alice) {
			t.Errorf("Pending = %+v, %v; want the request of alice (%s)", p, ok, req.ID)
		}
		if _, ok := m.IdleDeadline(); ok {
			t.Error("アイドルタイマーは held の間だけ（pending_approval では期限を持たない）")
		}
		l, events, err := m.Approve(req.ID)
		if err != nil || l.ID != req.ID || !l.Holder.SameSession(alice) {
			t.Fatalf("Approve = %+v, %v", l, err)
		}
		assertEvents(t, events, wantEvent{from: domain.LeasePendingApproval, to: domain.LeaseHeld, holder: "alice"})
		if m.State() != domain.LeaseHeld {
			t.Errorf("state = %s, want held", m.State())
		}
		if _, ok := m.Pending(); ok {
			t.Error("承認後は Pending が無い")
		}
		if _, ok := m.IdleDeadline(); !ok {
			t.Error("held になったらアイドルの期限がある")
		}
	})

	t.Run("承認の時刻から Since とアイドルの起点を数える", func(t *testing.T) {
		m, clk := newMachine(t, time.Minute)
		req := mustRequest(t, m, alice)
		clk.Advance(30 * time.Second)
		l, _, err := m.Approve(req.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !l.Since.Equal(clk.Now()) {
			t.Errorf("Since = %v, want the approval time %v", l.Since, clk.Now())
		}
		clk.Advance(59 * time.Second)
		if m.State() != domain.LeaseHeld {
			t.Errorf("承認から 59 秒ではまだ held のはず: %s", m.State())
		}
		clk.Advance(time.Second)
		if m.State() != domain.LeaseIdle {
			t.Errorf("承認から 60 秒でアイドル解放のはず: %s", m.State())
		}
	})

	t.Run("pending_approval の間は他者の Request・Acquire・Check を ErrLeaseHeldByOther で断る", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustRequest(t, m, alice)
		if _, _, err := m.Request(bob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Request(bob) err = %v, want ErrLeaseHeldByOther", err)
		}
		_, events, err := m.Acquire(bob)
		if !errors.Is(err, domain.ErrLeaseHeldByOther) || len(events) != 0 {
			t.Errorf("Acquire(bob) = %v, %v; want ErrLeaseHeldByOther without events", events, err)
		}
		var de *domain.Error
		if !errors.As(err, &de) || de.Detail["holder_kind"] != "mcp" || de.Detail["holder_label"] != "label-alice" {
			t.Errorf("Detail = %+v, want the requester", err)
		}
		if _, err := m.Check(bob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Check(bob) err = %v, want ErrLeaseHeldByOther", err)
		}
	})

	t.Run("pending_approval の間は要求者本人の Acquire・Check も断る（承認を待たずに held にしない）", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustRequest(t, m, alice)
		if _, _, err := m.Acquire(alice); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Acquire(alice) err = %v, want ErrLeaseHeldByOther", err)
		}
		if _, err := m.Check(alice); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Check(alice) err = %v, want ErrLeaseHeldByOther", err)
		}
		if _, _, err := m.Request(alice); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Request(alice) again err = %v, want ErrLeaseHeldByOther", err)
		}
		if m.State() != domain.LeasePendingApproval {
			t.Errorf("state = %s, want pending_approval", m.State())
		}
	})

	t.Run("明示解放（Release）は承認待ちに触れない", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustRequest(t, m, alice)
		assertEvents(t, m.Release(alice, domain.ReleaseExplicit))
		if m.State() != domain.LeasePendingApproval {
			t.Errorf("state = %s, want pending_approval", m.State())
		}
	})

	t.Run("他者が保持中は ErrLeaseHeldByOther、自分が保持中は ErrInvalidArgument", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustAcquire(t, m, alice)
		if _, _, err := m.Request(bob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("Request(bob) err = %v, want ErrLeaseHeldByOther", err)
		}
		if _, _, err := m.Request(alice); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Request(alice) err = %v, want ErrInvalidArgument", err)
		}
		if m.State() != domain.LeaseHeld {
			t.Errorf("state = %s, want held", m.State())
		}
	})

	t.Run("期限切れの保持者を先に解放して承認待ちに入る", func(t *testing.T) {
		m, clk := newMachine(t, time.Minute)
		mustAcquire(t, m, alice)
		clk.Advance(time.Minute)
		_, events, err := m.Request(bob)
		if err != nil {
			t.Fatal(err)
		}
		assertEvents(t, events,
			wantEvent{from: domain.LeaseHeld, to: domain.LeaseIdle, reason: domain.ReleaseIdleTimeout, holder: "alice"},
			wantEvent{from: domain.LeaseIdle, to: domain.LeasePendingApproval, holder: "bob"})
	})

	t.Run("不正な要求者", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		for name, who := range map[string]domain.Actor{
			"agent は保持者になれない": {Kind: domain.ActorAgent, SessionID: "t"},
			"セッション ID が空":     {Kind: domain.ActorMCP, ClientID: "c"},
		} {
			if _, _, err := m.Request(who); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("%s: err = %v, want ErrInvalidArgument", name, err)
			}
		}
		if m.State() != domain.LeaseIdle {
			t.Errorf("state = %s, want idle", m.State())
		}
	})
}

func TestLeaseWithdraw(t *testing.T) {
	alice, bob := mcpActor("alice"), mcpActor("bob")

	for _, reason := range []domain.ReleaseReason{
		domain.ReleaseApprovalDenied, domain.ReleaseApprovalTimeout, domain.ReleaseWithdrawn,
		domain.ReleaseKillSwitch, domain.ReleaseDisconnect,
	} {
		t.Run("pending_approval → idle（"+string(reason)+"）", func(t *testing.T) {
			m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
			req := mustRequest(t, m, alice)
			assertEvents(t, m.Withdraw(req.ID, reason),
				wantEvent{from: domain.LeasePendingApproval, to: domain.LeaseIdle, reason: reason, holder: "alice"})
			if m.State() != domain.LeaseIdle {
				t.Errorf("state = %s, want idle", m.State())
			}
			if _, ok := m.Pending(); ok {
				t.Error("取り下げ後は Pending が無い")
			}
			// 拒否・取り下げは剥奪ではない。次の Check は ErrLeaseRequired。
			if _, err := m.Check(alice); !errors.Is(err, domain.ErrLeaseRequired) {
				t.Errorf("Check err = %v, want ErrLeaseRequired", err)
			}
			// 他者がすぐ取得できる。
			if _, _, err := m.Acquire(bob); err != nil {
				t.Errorf("Acquire(bob) after withdraw: %v", err)
			}
		})
	}

	t.Run("承認待ちでない・別の要求の id は何もしない", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		assertEvents(t, m.Withdraw("lease-1", domain.ReleaseWithdrawn))
		mustAcquire(t, m, alice)
		assertEvents(t, m.Withdraw("lease-1", domain.ReleaseWithdrawn))
		if m.State() != domain.LeaseHeld {
			t.Errorf("state = %s, want held（保持中は取り下げの対象ではない）", m.State())
		}

		m2, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustRequest(t, m2, alice)
		assertEvents(t, m2.Withdraw("other", domain.ReleaseWithdrawn))
		if m2.State() != domain.LeasePendingApproval {
			t.Errorf("state = %s, want pending_approval", m2.State())
		}
	})

	t.Run("WithdrawSession は要求者のセッションだけ取り下げる", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustRequest(t, m, alice)
		assertEvents(t, m.WithdrawSession(bob, domain.ReleaseDisconnect))
		if m.State() != domain.LeasePendingApproval {
			t.Fatalf("state = %s, want pending_approval", m.State())
		}
		assertEvents(t, m.WithdrawSession(alice, domain.ReleaseDisconnect),
			wantEvent{from: domain.LeasePendingApproval, to: domain.LeaseIdle, reason: domain.ReleaseDisconnect, holder: "alice"})
	})

	t.Run("ReleaseAll（キルスイッチ）は承認待ちも取り下げる", func(t *testing.T) {
		m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
		mustRequest(t, m, alice)
		assertEvents(t, m.ReleaseAll(domain.ReleaseKillSwitch),
			wantEvent{from: domain.LeasePendingApproval, to: domain.LeaseIdle, reason: domain.ReleaseKillSwitch, holder: "alice"})
		if m.State() != domain.LeaseIdle {
			t.Errorf("state = %s, want idle", m.State())
		}
	})
}

func TestLeaseApproveStale(t *testing.T) {
	alice := mcpActor("alice")
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)

	if _, _, err := m.Approve("lease-1"); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Errorf("idle での Approve: err = %v, want ErrLeaseRequired", err)
	}
	req := mustRequest(t, m, alice)
	m.Withdraw(req.ID, domain.ReleaseKillSwitch)
	// 取り下げ後に許可が届いても held にならない。
	if _, events, err := m.Approve(req.ID); !errors.Is(err, domain.ErrLeaseRequired) || len(events) != 0 {
		t.Errorf("取り下げ後の Approve = %v, %v; want ErrLeaseRequired", events, err)
	}
	if m.State() != domain.LeaseIdle {
		t.Errorf("state = %s, want idle", m.State())
	}
	// 別の要求が承認待ちのとき、古い要求の id では承認できない。
	req2 := mustRequest(t, m, alice)
	if _, _, err := m.Approve(req.ID); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Errorf("古い id の Approve: err = %v, want ErrLeaseRequired", err)
	}
	if p, ok := m.Pending(); !ok || p.ID != req2.ID {
		t.Errorf("Pending = %+v, %v; want %s", p, ok, req2.ID)
	}
}

func TestLeaseApproveClearsRevokedMark(t *testing.T) {
	alice, host := mcpActor("alice"), domain.Actor{Kind: domain.ActorHost, Label: "(host)"}
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	mustAcquire(t, m, alice)
	if _, err := m.Revoke(host); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Check(alice); !errors.Is(err, domain.ErrLeaseRevoked) {
		t.Fatalf("Check after revoke: %v", err)
	}
	// 剥奪された本人が承認を経て取り直すと、剥奪の印は消える（FR-L06）。承認待ちの間は取得を断る状態。
	req := mustRequest(t, m, alice)
	if _, err := m.Check(alice); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("承認待ちの間の Check: %v, want ErrLeaseHeldByOther", err)
	}
	if _, _, err := m.Approve(req.ID); err != nil {
		t.Fatal(err)
	}
	m.Release(alice, domain.ReleaseExplicit)
	if _, err := m.Check(alice); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Errorf("取り直した後の解放で Check: %v, want ErrLeaseRequired（剥奪の印は消えている）", err)
	}
}

func TestLeaseRevokeDuringPending(t *testing.T) {
	alice, host := mcpActor("alice"), domain.Actor{Kind: domain.ActorHost, Label: "(host)"}
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	mustRequest(t, m, alice)
	// 承認待ちは保持ではないので、剥奪の対象ではない（何もせず成功。承認待ちは続く）。
	events, err := m.Revoke(host)
	if err != nil || len(events) != 0 {
		t.Errorf("Revoke = %v, %v; want no-op", events, err)
	}
	if m.State() != domain.LeasePendingApproval {
		t.Errorf("state = %s, want pending_approval", m.State())
	}
}

// TestLeaseTransitionsExhaustive は、操作の列を深さ 5 まですべて試し、どの列でも状態機械の不変条件を保つことを確かめる:
//   - held と pending_approval は同時に存在しない
//   - 返るイベントは、直前の State から今の State への実際の遷移とつながっている
//   - pending_approval の間は、誰の Acquire・Check も成功しない
//   - idle にいるとき Pending は無く、held でないとき Holder は無い
func TestLeaseTransitionsExhaustive(t *testing.T) {
	alice, bob := mcpActor("alice"), mcpActor("bob")
	host := domain.Actor{Kind: domain.ActorHost, Label: "(host)"}

	type step struct {
		name string
		do   func(m *domain.LeaseMachine, clk *fakeClock) ([]domain.LeaseEvent, error)
	}
	pendingID := func(m *domain.LeaseMachine) string {
		if p, ok := m.Pending(); ok {
			return p.ID
		}
		return "none"
	}
	steps := []step{
		{"Acquire(a)", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			_, ev, err := m.Acquire(alice)
			return ev, err
		}},
		{"Request(b)", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			_, ev, err := m.Request(bob)
			return ev, err
		}},
		{"Approve", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			_, ev, err := m.Approve(pendingID(m))
			return ev, err
		}},
		{"Withdraw(denied)", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			return m.Withdraw(pendingID(m), domain.ReleaseApprovalDenied), nil
		}},
		{"Release(a)", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			return m.Release(alice, domain.ReleaseExplicit), nil
		}},
		{"Release(b)", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			return m.Release(bob, domain.ReleaseExplicit), nil
		}},
		{"ReleaseAll", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			return m.ReleaseAll(domain.ReleaseKillSwitch), nil
		}},
		{"Revoke", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			return m.Revoke(host)
		}},
		{"Check(a)", func(m *domain.LeaseMachine, _ *fakeClock) ([]domain.LeaseEvent, error) {
			return m.Check(alice)
		}},
		{"Advance(idle)", func(m *domain.LeaseMachine, clk *fakeClock) ([]domain.LeaseEvent, error) {
			clk.Advance(time.Minute)
			return m.Expire(), nil
		}},
	}

	const depth = 5
	var walk func(seq []int)
	var run func(seq []int)
	count := 0
	run = func(seq []int) {
		count++
		m, clk := newMachine(t, time.Minute)
		names := make([]string, 0, len(seq))
		for _, i := range seq {
			names = append(names, steps[i].name)
			before := m.State()
			events, _ := steps[i].do(m, clk)
			after := m.State()
			where := fmt.Sprintf("%v", names)

			_, held := m.Holder()
			_, pend := m.Pending()
			if held && pend {
				t.Fatalf("%s: held と pending_approval が同時にある", where)
			}
			if pend && after != domain.LeasePendingApproval || held && after != domain.LeaseHeld || !held && !pend && after != domain.LeaseIdle {
				t.Fatalf("%s: State = %s, held=%v pending=%v", where, after, held, pend)
			}
			// イベントの連鎖: 最初の From は直前の状態（期限切れの解放が先に出ることがある）、最後の To は今の状態。
			if len(events) > 0 {
				last := events[len(events)-1]
				if last.To != after {
					t.Fatalf("%s: 最後のイベントの To = %s, State = %s", where, last.To, after)
				}
				for j := 1; j < len(events); j++ {
					if events[j-1].To != events[j].From {
						t.Fatalf("%s: イベントがつながらない: %+v", where, events)
					}
				}
				if events[0].From != before && (before != domain.LeaseIdle || events[0].From != domain.LeaseHeld) {
					t.Fatalf("%s: 最初のイベントの From = %s, 直前の State = %s", where, events[0].From, before)
				}
				for _, ev := range events {
					if (ev.To == domain.LeaseIdle) != (ev.Reason != "") {
						t.Fatalf("%s: Reason は idle への遷移だけに付く: %+v", where, ev)
					}
				}
			} else if before != after {
				t.Fatalf("%s: イベントなしで %s → %s", where, before, after)
			}
			if after == domain.LeasePendingApproval {
				for _, who := range []domain.Actor{alice, bob} {
					if _, _, err := m.Acquire(who); err == nil {
						t.Fatalf("%s: pending_approval で Acquire(%s) が成功した", where, who.SessionID)
					}
					if _, err := m.Check(who); err == nil {
						t.Fatalf("%s: pending_approval で Check(%s) が成功した", where, who.SessionID)
					}
				}
			}
		}
	}
	walk = func(seq []int) {
		if len(seq) > 0 {
			run(seq)
		}
		if len(seq) == depth {
			return
		}
		for i := range steps {
			walk(append(seq[:len(seq):len(seq)], i))
		}
	}
	walk(nil)
	if count < 10000 {
		t.Fatalf("explored %d sequences, want at least 10000", count)
	}
}
