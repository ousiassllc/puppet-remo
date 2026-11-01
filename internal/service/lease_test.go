package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestLeaseAcquireTable(t *testing.T) {
	web := domain.Actor{Kind: domain.ActorWeb, ClientID: "c", SessionID: "viewer"}
	agent := domain.Actor{Kind: domain.ActorAgent, SessionID: "task"}
	tests := []struct {
		name    string
		setup   func(e *ctlEnv)
		who     domain.Actor
		wantErr error
	}{
		{name: "idle なら承認なしで取得できる", who: ctlAlice},
		{name: "同じセッションの再取得は成功する", who: ctlAlice, setup: func(e *ctlEnv) { mustAcquire(t, e, ctlAlice) }},
		{name: "他者が保持中は ErrLeaseHeldByOther", who: ctlBob, setup: func(e *ctlEnv) { mustAcquire(t, e, ctlAlice) }, wantErr: domain.ErrLeaseHeldByOther},
		{name: "同じクライアントでも別セッションは別の保持者", who: domain.Actor{Kind: domain.ActorMCP, ClientID: "client-a", SessionID: "sess-a2"},
			setup: func(e *ctlEnv) { mustAcquire(t, e, ctlAlice) }, wantErr: domain.ErrLeaseHeldByOther},
		{name: "web は保持者になれる（M2）", who: web},
		{name: "web が保持中は mcp が ErrLeaseHeldByOther", who: ctlAlice, setup: func(e *ctlEnv) { mustAcquire(t, e, web) }, wantErr: domain.ErrLeaseHeldByOther},
		{name: "agent は M4 まで保持者になれない", who: agent, wantErr: domain.ErrInvalidArgument},
		{name: "セッション ID が空は不可", who: domain.Actor{Kind: domain.ActorMCP, ClientID: "c"}, wantErr: domain.ErrInvalidArgument},
		{name: "キルスイッチ中は取得できない", who: ctlAlice, setup: func(e *ctlEnv) { _ = e.kill.Trigger("test") }, wantErr: domain.ErrKillSwitchActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			l, err := e.leases.Acquire(context.Background(), tt.who)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if l.ID == "" || !l.Holder.SameSession(tt.who) {
				t.Fatalf("unexpected lease %+v", l)
			}
		})
	}
}

func mustAcquire(t *testing.T, e *ctlEnv, who domain.Actor) Lease {
	t.Helper()
	l, err := e.leases.Acquire(context.Background(), who)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return l
}

func TestLeaseConcurrentAcquireHasExactlyOneWinner(t *testing.T) {
	e := newCtlEnv(t)
	const n = 32
	var (
		wg      sync.WaitGroup
		wins    atomic.Int32
		heldErr atomic.Int32
		start   = make(chan struct{})
	)
	for i := range n {
		who := domain.Actor{Kind: domain.ActorMCP, ClientID: "c", SessionID: string(rune('A' + i))}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.leases.Acquire(context.Background(), who)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, domain.ErrLeaseHeldByOther):
				heldErr.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || heldErr.Load() != n-1 {
		t.Fatalf("wins=%d heldByOther=%d, want 1 and %d", wins.Load(), heldErr.Load(), n-1)
	}
	if got := countEvent(e.audit.Events(), "lease.acquire"); got != n {
		// 勝者 1 件 + 敗者 n-1 件（denied）が記録される
		t.Fatalf("lease.acquire audit entries = %d, want %d", got, n)
	}
}

func countEvent(events []string, name string) int {
	n := 0
	for _, ev := range events {
		if ev == name {
			n++
		}
	}
	return n
}

func TestLeaseCheckTable(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(e *ctlEnv)
		who     domain.Actor
		wantErr error
	}{
		{name: "未保持は ErrLeaseRequired", who: ctlAlice, wantErr: domain.ErrLeaseRequired},
		{name: "保持者は成功", who: ctlAlice, setup: func(e *ctlEnv) { mustAcquire(t, e, ctlAlice) }},
		{name: "他者は ErrLeaseHeldByOther", who: ctlBob, setup: func(e *ctlEnv) { mustAcquire(t, e, ctlAlice) }, wantErr: domain.ErrLeaseHeldByOther},
		{name: "解放後は ErrLeaseRequired", who: ctlAlice, setup: func(e *ctlEnv) {
			mustAcquire(t, e, ctlAlice)
			e.leases.Release(ctlAlice)
		}, wantErr: domain.ErrLeaseRequired},
		{name: "剥奪された保持者は ErrLeaseRevoked", who: ctlAlice, setup: func(e *ctlEnv) {
			mustAcquire(t, e, ctlAlice)
			if err := e.leases.Revoke(HostActor); err != nil {
				t.Fatal(err)
			}
		}, wantErr: domain.ErrLeaseRevoked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			if err := e.leases.Check(tt.who); !errors.Is(err, tt.wantErr) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLeaseReleaseOnlyByHolder(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	e.leases.Release(ctlBob) // 保持者ではないので何もしない
	if h, ok := e.leases.Holder(); !ok || !h.Actor.SameSession(ctlAlice) {
		t.Fatalf("holder changed: %+v %v", h, ok)
	}
	e.leases.Release(ctlAlice)
	if _, ok := e.leases.Holder(); ok {
		t.Fatal("lease should be released")
	}
	e.leases.Release(ctlAlice) // 保持していなければ何もしない（監査も増えない）
	if got := countEvent(e.audit.Events(), "lease.release"); got != 1 {
		t.Fatalf("lease.release entries = %d, want 1", got)
	}
}

func TestLeaseIdleExpiryWithFakeClock(t *testing.T) {
	e := newCtlEnv(t)
	sub := e.leases.Subscribe()
	mustAcquire(t, e, ctlAlice)

	if e.timers.Active() != 1 || e.timers.Last() != 5*time.Minute {
		t.Fatalf("timer: active=%d last=%s, want 1 and 5m", e.timers.Active(), e.timers.Last())
	}

	// 4 分後の活動で期限が延びる。タイマーは作り直さず、満了時に残りを見て延長する。
	e.clock.Advance(4 * time.Minute)
	if err := e.leases.Check(ctlAlice); err != nil {
		t.Fatal(err)
	}
	if e.timers.Active() != 1 {
		t.Fatalf("timers should stay at 1, got %d", e.timers.Active())
	}
	h, _ := e.leases.Holder()
	if h.IdleRemaining != 5*time.Minute {
		t.Fatalf("IdleRemaining = %s, want 5m", h.IdleRemaining)
	}

	// 最初のタイマー満了（5 分時点）では、まだ期限前なので解放しない。
	e.clock.Advance(time.Minute)
	e.timers.FireAll()
	if _, ok := e.leases.Holder(); !ok {
		t.Fatal("lease must survive: last activity was 1m ago")
	}
	if e.timers.Active() != 1 || e.timers.Last() != 4*time.Minute {
		t.Fatalf("timer should be re-armed for the remaining 4m, got active=%d last=%s", e.timers.Active(), e.timers.Last())
	}

	// 期限を過ぎて満了すると idle_timeout で解放される。
	e.clock.Advance(4 * time.Minute)
	e.timers.FireAll()
	if _, ok := e.leases.Holder(); ok {
		t.Fatal("lease should expire after 5m without activity")
	}
	if err := e.leases.Check(ctlAlice); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("Check after expiry = %v, want ErrLeaseRequired", err)
	}
	if e.timers.Active() != 0 {
		t.Fatalf("no timer should remain, got %d", e.timers.Active())
	}

	// 解放後は他者が取得できる。
	mustAcquire(t, e, ctlBob)

	got := drain(sub)
	wantTo := []domain.LeaseState{domain.LeaseHeld, domain.LeaseIdle, domain.LeaseHeld}
	if len(got) != len(wantTo) {
		t.Fatalf("events = %+v, want %d", got, len(wantTo))
	}
	for i, ev := range got {
		if ev.To != wantTo[i] {
			t.Fatalf("event %d: To = %s, want %s", i, ev.To, wantTo[i])
		}
	}
	if got[1].Reason != domain.ReleaseIdleTimeout {
		t.Fatalf("release reason = %q, want idle_timeout", got[1].Reason)
	}
}

func TestLeaseExpiryIsEnforcedWithoutTimer(t *testing.T) {
	// タイマーが遅れても、Check・Acquire は時計で期限切れを判定する。
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	e.clock.Advance(5 * time.Minute)
	if err := e.leases.Check(ctlAlice); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("Check = %v, want ErrLeaseRequired", err)
	}
	mustAcquire(t, e, ctlBob)
}

func TestLeaseIdleTimeoutFromSettings(t *testing.T) {
	tests := []struct {
		name    string
		raw     string // 空なら未設定
		want    time.Duration
		wantErr bool
	}{
		{name: "未設定は 300 秒", want: 300 * time.Second},
		{name: "設定値", raw: "60", want: time.Minute},
		{name: "0 は不正", raw: "0", wantErr: true},
		{name: "負は不正", raw: "-5", wantErr: true},
		{name: "小数は不正", raw: "1.5", wantErr: true},
		{name: "文字列は不正", raw: `"300"`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newCtlSettings()
			if tt.raw != "" {
				s.put("lease.idle_timeout_sec", tt.raw)
			}
			got, err := LeaseIdleTimeout(context.Background(), s)
			if tt.wantErr {
				if !errors.Is(err, domain.ErrInvalidArgument) {
					t.Fatalf("want ErrInvalidArgument, got %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %s, %v; want %s", got, err, tt.want)
			}
		})
	}
}

func TestLeaseReleaseAllAndSessionDisconnectReasons(t *testing.T) {
	tests := []struct {
		name       string
		act        func(e *ctlEnv)
		wantReason string
	}{
		{name: "Release は explicit", act: func(e *ctlEnv) { e.leases.Release(ctlAlice) }, wantReason: "explicit"},
		{name: "ReleaseSession は disconnect", act: func(e *ctlEnv) { e.leases.ReleaseSession(ctlAlice) }, wantReason: "disconnect"},
		{name: "ReleaseAll は渡した理由", act: func(e *ctlEnv) { e.leases.ReleaseAll("kill_switch") }, wantReason: "kill_switch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			mustAcquire(t, e, ctlAlice)
			tt.act(e)
			if _, ok := e.leases.Holder(); ok {
				t.Fatal("lease should be released")
			}
			var reason string
			for _, en := range e.audit.Entries() {
				if en.Event == "lease.release" {
					reason = string(en.DetailJSON)
				}
			}
			if want := `{"reason":"` + tt.wantReason + `"}`; reason != want {
				t.Fatalf("detail = %s, want %s", reason, want)
			}
		})
	}
}

func TestLeaseAuditEntries(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	if _, err := e.leases.Acquire(context.Background(), ctlBob); err == nil {
		t.Fatal("bob should be denied")
	}
	e.leases.Release(ctlAlice)

	entries := e.audit.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	acquire, denied, release := entries[0], entries[1], entries[2]
	if acquire.Kind != "lease" || acquire.Event != "lease.acquire" || acquire.Result != "ok" || acquire.LeaseID != "lease-1" ||
		acquire.ClientID != "client-a" || acquire.SessionID != "sess-a" || acquire.ActorType != domain.ActorMCP {
		t.Fatalf("acquire entry = %+v", acquire)
	}
	if denied.Event != "lease.acquire" || denied.Result != "denied" || denied.ErrorCode != "lease_held_by_other" || denied.ClientID != "client-b" {
		t.Fatalf("denied entry = %+v", denied)
	}
	if release.Event != "lease.release" || release.LeaseID != "lease-1" {
		t.Fatalf("release entry = %+v", release)
	}
}

func drain(ch <-chan domain.LeaseEvent) []domain.LeaseEvent {
	var out []domain.LeaseEvent
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestLeaseSetIdleTimeout(t *testing.T) {
	t.Run("不正な値はエラーで、タイマーも期限も変えない", func(t *testing.T) {
		e := newCtlEnv(t)
		mustAcquire(t, e, ctlAlice)
		if err := e.leases.SetIdleTimeout(0); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("SetIdleTimeout(0) = %v, want ErrInvalidArgument", err)
		}
		if e.timers.Active() != 1 || e.timers.Last() != 5*time.Minute {
			t.Fatalf("timer changed: active=%d last=%s", e.timers.Active(), e.timers.Last())
		}
	})

	t.Run("保持中に変えると、タイマーを新しい期限で張り直す", func(t *testing.T) {
		e := newCtlEnv(t)
		mustAcquire(t, e, ctlAlice)
		e.clock.Advance(time.Minute)
		if err := e.leases.SetIdleTimeout(10 * time.Minute); err != nil {
			t.Fatal(err)
		}
		if e.timers.Active() != 1 || e.timers.Last() != 9*time.Minute {
			t.Fatalf("timer: active=%d last=%s, want 1 and 9m (10m - 1m idle)", e.timers.Active(), e.timers.Last())
		}
		h, _ := e.leases.Holder()
		if h.IdleRemaining != 9*time.Minute {
			t.Fatalf("IdleRemaining = %s, want 9m", h.IdleRemaining)
		}
	})

	t.Run("短くして既に期限を過ぎていれば、すぐ idle_timeout で解放する", func(t *testing.T) {
		e := newCtlEnv(t)
		sub := e.leases.Subscribe()
		mustAcquire(t, e, ctlAlice)
		e.clock.Advance(2 * time.Minute)
		if err := e.leases.SetIdleTimeout(time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.leases.Holder(); ok {
			t.Fatal("the lease must be released immediately")
		}
		got := drain(sub)
		if len(got) != 2 || got[1].To != domain.LeaseIdle || got[1].Reason != domain.ReleaseIdleTimeout {
			t.Fatalf("events = %+v, want acquire then an idle_timeout release", got)
		}
		if e.timers.Active() != 0 {
			t.Fatalf("no timer should remain, got %d", e.timers.Active())
		}
	})

	t.Run("保持していなければタイマーを張らない", func(t *testing.T) {
		e := newCtlEnv(t)
		if err := e.leases.SetIdleTimeout(time.Minute); err != nil {
			t.Fatal(err)
		}
		if e.timers.Active() != 0 {
			t.Fatalf("active timers = %d, want 0", e.timers.Active())
		}
		mustAcquire(t, e, ctlAlice)
		if e.timers.Last() != time.Minute {
			t.Fatalf("the next lease must use the new timeout, timer = %s", e.timers.Last())
		}
	})
}

// キルスイッチ中の取得の拒否も、他者保持中の拒否と同じく監査に残る（kind = lease、result = denied）。
func TestLeaseAcquireRefusedByKillSwitchIsAudited(t *testing.T) {
	e := newCtlEnv(t)
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	before := len(e.audit.Entries())

	if _, err := e.leases.Acquire(context.Background(), ctlAlice); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("Acquire = %v, want ErrKillSwitchActive", err)
	}
	got := e.audit.Entries()[before:]
	if len(got) != 1 {
		t.Fatalf("new audit entries = %+v, want exactly one", got)
	}
	en := got[0]
	if en.Kind != "lease" || en.Event != "lease.acquire" || en.Result != "denied" || en.ErrorCode != "kill_switch_active" ||
		en.ClientID != ctlAlice.ClientID || en.SessionID != ctlAlice.SessionID {
		t.Errorf("entry = %+v", en)
	}
}

var ctlViewer = domain.Actor{Kind: domain.ActorWeb, ClientID: "client-v", Label: "viewer", SessionID: "viewer-1"}

func TestLeaseRevokeReleasesTheHolderAndAuditsTheRevoker(t *testing.T) {
	for _, by := range []domain.Actor{HostActor, ctlViewer} {
		t.Run(string(by.Kind), func(t *testing.T) {
			e := newCtlEnv(t)
			sub := e.leases.Subscribe()
			l := mustAcquire(t, e, ctlAlice)
			drain(sub)

			if err := e.leases.Revoke(by); err != nil {
				t.Fatal(err)
			}
			if _, ok := e.leases.Holder(); ok {
				t.Fatal("the lease should be revoked")
			}
			events := drain(sub)
			if len(events) != 1 || events[0].Reason != domain.ReleaseRevoked || events[0].To != domain.LeaseIdle || events[0].By != by {
				t.Fatalf("events = %+v, want one revoked release by %v", events, by.Kind)
			}
			if e.power.Awake() {
				t.Error("KeepAwake should be off after the revoke")
			}

			entries := e.audit.Entries()
			revoke := entries[len(entries)-1]
			if revoke.Kind != "lease" || revoke.Event != "lease.revoke" || revoke.Result != "ok" || revoke.LeaseID != l.ID ||
				revoke.ActorType != by.Kind || revoke.ClientLabel != by.Label {
				t.Fatalf("revoke entry = %+v", revoke)
			}
			var detail map[string]string
			if err := json.Unmarshal(revoke.DetailJSON, &detail); err != nil {
				t.Fatal(err)
			}
			if detail["holder_kind"] != "mcp" || detail["holder_client_id"] != "client-a" || detail["holder_label"] != "alice" {
				t.Fatalf("detail = %v, want the revoked holder", detail)
			}
			if countEvent(e.audit.Events(), "lease.release") != 0 {
				t.Fatalf("a revoke must not also be audited as lease.release: %v", e.audit.Events())
			}
		})
	}
}

func TestLeaseRevokeOnIdleIsANoOp(t *testing.T) {
	e := newCtlEnv(t)
	sub := e.leases.Subscribe()
	if err := e.leases.Revoke(HostActor); err != nil {
		t.Fatalf("Revoke on idle = %v, want nil", err)
	}
	if got := drain(sub); len(got) != 0 {
		t.Errorf("events = %+v, want none", got)
	}
	if got := e.audit.Events(); len(got) != 0 {
		t.Errorf("audit = %v, want none", got)
	}
}

func TestLeaseRevokeRejectsActorsThatCannotRevoke(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	if err := e.leases.Revoke(ctlBob); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Revoke by mcp = %v, want ErrInvalidArgument", err)
	}
	if _, ok := e.leases.Holder(); !ok {
		t.Fatal("the lease must stay held")
	}
}

func TestLeaseRevokedHolderGetsLeaseRevokedUntilItAcquiresAgain(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	if err := e.leases.Revoke(HostActor); err != nil {
		t.Fatal(err)
	}

	// 操作系は lease_revoked（mcp でも暗黙に取得し直さない。FR-M17）。
	for range 2 {
		if _, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "a"}); !errors.Is(err, domain.ErrLeaseRevoked) {
			t.Fatalf("Do after the revoke = %v, want ErrLeaseRevoked", err)
		}
	}
	if _, ok := e.leases.Holder(); ok {
		t.Fatal("a revoked session must not re-acquire implicitly")
	}

	mustAcquire(t, e, ctlAlice) // 明示の取得で戻る
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "a"}); err != nil {
		t.Fatalf("Do after acquiring again = %v, want nil", err)
	}
}

func TestLeaseWebHolderCanBeRevokedByHost(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlViewer)
	if _, err := e.control.Do(context.Background(), ctlViewer, domain.TypeText{Text: "a"}); err != nil {
		t.Fatalf("web holder Do = %v, want nil", err)
	}
	if err := e.leases.Revoke(HostActor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Do(context.Background(), ctlViewer, domain.TypeText{Text: "a"}); !errors.Is(err, domain.ErrLeaseRevoked) {
		t.Fatalf("revoked web Do = %v, want ErrLeaseRevoked", err)
	}
	// 他者（mcp）は暗黙に取得できる（承認は M3 から）。
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "a"}); err != nil {
		t.Fatalf("mcp Do after the web lease was revoked = %v, want nil", err)
	}
}

func TestLeaseWebHolderReleasedOnSessionDisconnect(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlViewer)
	e.leases.ReleaseSession(ctlViewer)
	if _, ok := e.leases.Holder(); ok {
		t.Fatal("the web lease should be released")
	}
}

func TestToolAuditEntryForLeaseRevokedIsDenied(t *testing.T) {
	e := ToolAuditEntry(ctlAlice, "type", nil, "", domain.ErrLeaseRevoked, 0)
	if e.Result != "denied" || e.ErrorCode != "lease_revoked" {
		t.Errorf("entry = result %q, error_code %q; want denied / lease_revoked", e.Result, e.ErrorCode)
	}
	web := ToolAuditEntry(ctlViewer, "type", nil, "", nil, 0)
	if web.Kind != "input" {
		t.Errorf("a web holder's tool entry kind = %q, want input", web.Kind)
	}
}
