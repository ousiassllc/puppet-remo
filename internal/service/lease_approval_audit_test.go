package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは、承認の監査（approval.*）・拒否後のクールダウン（30 秒）・RequestAcquire（Web の待たない取得）のテスト。

func (le *leaseApprovalEnv) auditEvents() []string { return le.audit.Events() }

// 承認の終わり方ごとの監査: 取得要求（approval.requested）に続き、理由に応じた 1 件が付く。
func TestApprovalAuditByOutcome(t *testing.T) {
	tests := []struct {
		name       string
		finish     func(le *leaseApprovalEnv, req ApprovalRequest)
		wantEvents []string
		wantResult string
		wantCode   string
		wantReason string
	}{
		{"denied", func(le *leaseApprovalEnv, req ApprovalRequest) { _ = le.approval.Resolve(req.ID, false) },
			[]string{"approval.requested", "approval.denied"}, auditDenied, string(domain.CodeApprovalDenied), "approval_denied"},
		{"timeout", func(le *leaseApprovalEnv, _ ApprovalRequest) { le.aTimers.FireAll() },
			[]string{"approval.requested", "approval.timeout"}, auditDenied, string(domain.CodeApprovalTimeout), "approval_timeout"},
		{"kill switch", func(le *leaseApprovalEnv, _ ApprovalRequest) { _ = le.kill.Trigger("test") },
			[]string{"approval.requested", "approval.withdrawn"}, auditOK, "", "kill_switch"},
		{"session closed", func(le *leaseApprovalEnv, _ ApprovalRequest) { le.leases.ReleaseSession(ctlAlice) },
			[]string{"approval.requested", "approval.withdrawn"}, auditOK, "", "disconnect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
			out, req := le.acquire(t, context.Background(), ctlAlice)
			tt.finish(le, req)
			le.wait(t, out)
			var got []string
			for _, ev := range le.auditEvents() {
				if ev != "killswitch.trigger" { // キルスイッチ自体の監査は対象外
					got = append(got, ev)
				}
			}
			if !reflect.DeepEqual(got, tt.wantEvents) {
				t.Fatalf("audit = %v, want %v", got, tt.wantEvents)
			}
			last := le.audit.Entries()[1]
			if last.Kind != "lease" || last.Result != tt.wantResult || last.ErrorCode != tt.wantCode || last.ClientID != "client-a" || last.SessionID != "sess-a" {
				t.Errorf("entry = %+v", last)
			}
			var detail map[string]any
			if err := json.Unmarshal(last.DetailJSON, &detail); err != nil {
				t.Fatal(err)
			}
			if detail["reason"] != tt.wantReason || detail["client_id"] != "client-a" || detail["lease_id"] == "" || detail["holder_kind"] != "mcp" {
				t.Errorf("detail = %v", detail)
			}
			if len(detail) != 5 { // client_id・lease_id・holder_kind・holder_label・reason だけ
				t.Errorf("detail has unexpected keys: %v", detail)
			}
		})
	}
}

// ctx のキャンセルによる取り下げも approval.withdrawn。
func TestApprovalAuditContextCancelIsWithdrawn(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	ctx, cancel := context.WithCancel(context.Background())
	out, _ := le.acquire(t, ctx, ctlAlice)
	cancel()
	le.wait(t, out)
	if got, want := le.auditEvents(), []string{"approval.requested", "approval.withdrawn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit = %v, want %v", got, want)
	}
}

// 拒否・タイムアウトの直後 30 秒は、同じクライアントの取得を承認なし（ダイアログなし・approval.requested なし）で断る。
func TestApprovalCooldownAfterDenyOrTimeout(t *testing.T) {
	tests := []struct {
		name   string
		finish func(le *leaseApprovalEnv, req ApprovalRequest)
	}{
		{"denied", func(le *leaseApprovalEnv, req ApprovalRequest) { _ = le.approval.Resolve(req.ID, false) }},
		{"timeout", func(le *leaseApprovalEnv, _ ApprovalRequest) { le.aTimers.FireAll() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
			out, req := le.acquire(t, context.Background(), ctlAlice)
			tt.finish(le, req)
			le.wait(t, out)
			before := len(le.audit.Entries())

			// 期間内: 即座に approval_denied。承認の要求（ダイアログ）は作らない。
			le.clock.Advance(ApprovalCooldown - time.Second)
			_, err := le.leases.Acquire(context.Background(), ctlAlice)
			if !errors.Is(err, domain.ErrApprovalDenied) {
				t.Fatalf("Acquire in the cooldown = %v, want ErrApprovalDenied", err)
			}
			le.expectNoApproval(t)
			if len(le.approval.Pending()) != 0 {
				t.Error("the cooldown must not create an approval request")
			}
			if _, ok := le.leases.Pending(); ok {
				t.Error("the cooldown must not make the lease pending")
			}
			added := le.audit.Entries()[before:]
			if len(added) != 1 || added[0].Event != "lease.acquire" || added[0].Result != auditDenied || added[0].ErrorCode != string(domain.CodeApprovalDenied) {
				t.Fatalf("audit added = %+v, want one denied lease.acquire", added)
			}
			var detail map[string]any
			if err := json.Unmarshal(added[0].DetailJSON, &detail); err != nil || detail["cooldown"] != true {
				t.Errorf("detail = %s, want cooldown: true (%v)", added[0].DetailJSON, err)
			}

			// 別のクライアントは影響しない。
			outBob, reqBob := le.acquire(t, context.Background(), ctlBob)
			_ = le.approval.Resolve(reqBob.ID, true)
			if r := le.wait(t, outBob); r.err != nil {
				t.Fatalf("bob: %v", r.err)
			}
			le.leases.Release(ctlBob)

			// 期間が過ぎたら、また承認を求める。
			le.clock.Advance(time.Second)
			out2, req2 := le.acquire(t, context.Background(), ctlAlice)
			_ = le.approval.Resolve(req2.ID, true)
			if r := le.wait(t, out2); r.err != nil {
				t.Fatalf("after the cooldown: %v", r.err)
			}
		})
	}
}

// 取り下げ（ctx のキャンセル）はクールダウンを始めない。
func TestApprovalWithdrawnDoesNotStartCooldown(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	ctx, cancel := context.WithCancel(context.Background())
	out, _ := le.acquire(t, ctx, ctlAlice)
	cancel()
	le.wait(t, out)
	out2, req2 := le.acquire(t, context.Background(), ctlAlice)
	_ = le.approval.Resolve(req2.ID, true)
	if r := le.wait(t, out2); r.err != nil {
		t.Fatalf("Acquire after a withdrawal = %v", r.err)
	}
}

// 保持中のクライアントの再取得は承認もクールダウンも関係しない。ClearApprovalCooldown でクールダウンが解ける。
func TestApprovalCooldownClear(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	_ = le.approval.Resolve(req.ID, false)
	le.wait(t, out)
	if _, err := le.leases.Acquire(context.Background(), ctlAlice); !errors.Is(err, domain.ErrApprovalDenied) {
		t.Fatalf("in the cooldown: %v", err)
	}
	le.leases.ClearApprovalCooldown("client-a")
	out2, req2 := le.acquire(t, context.Background(), ctlAlice)
	_ = le.approval.Resolve(req2.ID, true)
	if r := le.wait(t, out2); r.err != nil {
		t.Fatalf("after clearing: %v", r.err)
	}
}

// countingApproval は Request の回数を数える Approval。
type countingApproval struct {
	Approval
	mu sync.Mutex
	n  int
}

func (a *countingApproval) Request(ctx context.Context, req ApprovalRequest) (Decision, error) {
	a.mu.Lock()
	a.n++
	a.mu.Unlock()
	return a.Approval.Request(ctx, req)
}

func (a *countingApproval) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// requestAcquireEnv は RequestAcquire 用: always の alice・bob、Request の回数、lease-result の購読つき。
type requestAcquireEnv struct {
	*leaseApprovalEnv
	counter *countingApproval
	results <-chan Event
}

func newRequestAcquireEnv(t *testing.T, mode domain.ApprovalMode) *requestAcquireEnv {
	t.Helper()
	e := newCtlEnv(t)
	bus := NewBus()
	results, cancel := bus.Subscribe(TopicLeaseResult)
	t.Cleanup(cancel)
	le := &leaseApprovalEnv{ctlEnv: e, aTimers: &ctlTimers{}, bus: bus}
	var cancelApprovals func()
	le.approvals, cancelApprovals = bus.Subscribe(TopicApproval)
	t.Cleanup(cancelApprovals)
	le.clients = &leaseClientsFake{clients: map[string]domain.Client{
		"client-a": {ID: "client-a", DisplayName: "alice", ApprovalMode: mode},
		"client-b": {ID: "client-b", DisplayName: "bob", ApprovalMode: domain.ApprovalAlways},
	}}
	approval, err := NewApproval(ApprovalConfig{Clock: e.clock, IDs: &ctlIDs{}, Bus: bus, AfterFunc: le.aTimers.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingApproval{Approval: approval}
	le.approval = approval
	e.leases, err = NewLeaseManager(LeaseConfig{
		Clock: e.clock, IDs: &ctlIDs{}, IdleTimeout: domain.DefaultLeaseIdleTimeout,
		Audit: e.audit, Kill: e.kill, AfterFunc: e.timers.AfterFunc, Power: e.power,
		Approval: counter, Clients: le.clients, Bus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.kill.BindLeases(e.leases)
	return &requestAcquireEnv{leaseApprovalEnv: le, counter: counter, results: results}
}

func (e *requestAcquireEnv) awaitApprovalEvent(t *testing.T) ApprovalRequest {
	t.Helper()
	select {
	case ev := <-e.approvals:
		for _, r := range e.approval.Pending() {
			if r.ID == ev.Data.(ApprovalEvent).ID {
				return r
			}
		}
		t.Fatal("approval event is not pending")
	case <-time.After(approvalWait):
		t.Fatal("approval event was not published")
	}
	return ApprovalRequest{}
}

func (e *requestAcquireEnv) awaitResult(t *testing.T) LeaseResult {
	t.Helper()
	select {
	case ev := <-e.results:
		return ev.Data.(LeaseResult)
	case <-time.After(approvalWait):
		t.Fatal("lease-result was not published")
	}
	return LeaseResult{}
}

// RequestAcquire は承認が要るとき、結果を待たずに (承認待ちのリース, true) を返す。許可は lease の変化で伝わり、lease-result は出ない。
func TestRequestAcquireReturnsImmediatelyAndGrantIsAHeldLease(t *testing.T) {
	e := newRequestAcquireEnv(t, domain.ApprovalAlways)
	sub := e.leases.Subscribe()
	l, pending, err := e.leases.RequestAcquire(context.Background(), ctlAlice)
	if err != nil || !pending || l.ID == "" || !l.Holder.SameSession(ctlAlice) {
		t.Fatalf("RequestAcquire = %+v, pending %v, %v", l, pending, err)
	}
	req := e.awaitApprovalEvent(t)
	if p, ok := e.leases.Pending(); !ok || p.LeaseID != l.ID {
		t.Fatalf("Pending = %+v, %v; want the returned lease %q", p, ok, l.ID)
	}
	if err := e.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []domain.LeaseState{domain.LeasePendingApproval, domain.LeaseHeld} {
		select {
		case ev := <-sub:
			if ev.To != want {
				t.Fatalf("lease event to = %s, want %s", ev.To, want)
			}
		case <-time.After(approvalWait):
			t.Fatalf("lease event %s missing", want)
		}
	}
	if h, ok := e.leases.Holder(); !ok || h.LeaseID != l.ID {
		t.Errorf("Holder = %+v, %v", h, ok)
	}
	select {
	case ev := <-e.results:
		t.Errorf("a grant must not publish lease-result: %+v", ev)
	default:
	}
}

// 承認待ちのあいだの RequestAcquire（Web の再試行）は、Approval.Request を呼び直さず同じ要求を返す。
func TestRequestAcquireWhilePendingDoesNotCreateASecondApproval(t *testing.T) {
	e := newRequestAcquireEnv(t, domain.ApprovalAlways)
	first, pending, err := e.leases.RequestAcquire(context.Background(), ctlAlice)
	if err != nil || !pending {
		t.Fatalf("first = pending %v, %v", pending, err)
	}
	req := e.awaitApprovalEvent(t)
	for range 3 { // 408 のあとの再送などを模す
		again, pending, err := e.leases.RequestAcquire(context.Background(), ctlAlice)
		if err != nil || !pending || again.ID != first.ID {
			t.Fatalf("retry = %+v, pending %v, %v; want the same pending lease %q", again, pending, err, first.ID)
		}
	}
	if n := e.counter.count(); n != 1 {
		t.Fatalf("Approval.Request calls = %d, want 1", n)
	}
	if n := len(e.approval.Pending()); n != 1 {
		t.Fatalf("pending approvals = %d, want 1", n)
	}
	// 結果は 1 度だけ。
	if err := e.approval.Resolve(req.ID, false); err != nil {
		t.Fatal(err)
	}
	if res := e.awaitResult(t); !errors.Is(res.Err, domain.ErrApprovalDenied) || !res.Who.SameSession(ctlAlice) {
		t.Errorf("lease-result = %+v", res)
	}
	select {
	case ev := <-e.results:
		t.Errorf("a second lease-result: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
	// 拒否のあとの再送はクールダウンで即座に approval_denied（承認は増えない）。
	if _, _, err := e.leases.RequestAcquire(context.Background(), ctlAlice); !errors.Is(err, domain.ErrApprovalDenied) {
		t.Fatalf("retry after the denial = %v, want ErrApprovalDenied", err)
	}
	if n := e.counter.count(); n != 1 {
		t.Errorf("Approval.Request calls = %d, want 1", n)
	}
}

// 拒否・タイムアウトは lease-result（Who は要求者のセッション）で届く。
func TestRequestAcquireFailuresArePublished(t *testing.T) {
	tests := []struct {
		name   string
		finish func(e *requestAcquireEnv, req ApprovalRequest)
		want   error
	}{
		{"denied", func(e *requestAcquireEnv, req ApprovalRequest) { _ = e.approval.Resolve(req.ID, false) }, domain.ErrApprovalDenied},
		{"timeout", func(e *requestAcquireEnv, _ ApprovalRequest) { e.aTimers.FireAll() }, domain.ErrApprovalTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRequestAcquireEnv(t, domain.ApprovalAlways)
			if _, pending, err := e.leases.RequestAcquire(context.Background(), ctlAlice); err != nil || !pending {
				t.Fatalf("RequestAcquire = pending %v, %v", pending, err)
			}
			tt.finish(e, e.awaitApprovalEvent(t))
			res := e.awaitResult(t)
			if !errors.Is(res.Err, tt.want) || !res.Who.SameSession(ctlAlice) {
				t.Errorf("lease-result = %+v, want %v for alice", res, tt.want)
			}
		})
	}
}

// 取り下げ（キルスイッチ・切断）は lease-result を出さず、リースは idle に戻る。待ちの goroutine も終わる。
func TestRequestAcquireWithdrawalIsNotPublished(t *testing.T) {
	for name, withdraw := range map[string]func(e *requestAcquireEnv){
		"kill switch": func(e *requestAcquireEnv) { _ = e.kill.Trigger("test") },
		"disconnect":  func(e *requestAcquireEnv) { e.leases.ReleaseSession(ctlAlice) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newRequestAcquireEnv(t, domain.ApprovalAlways)
			if _, pending, err := e.leases.RequestAcquire(context.Background(), ctlAlice); err != nil || !pending {
				t.Fatalf("RequestAcquire = pending %v, %v", pending, err)
			}
			e.awaitApprovalEvent(t)
			withdraw(e)
			waitFor(t, func() bool { return len(e.approval.Pending()) == 0 })
			if _, ok := e.leases.Pending(); ok {
				t.Error("the lease is still pending")
			}
			select {
			case ev := <-e.results:
				t.Errorf("a withdrawal must not publish lease-result: %+v", ev)
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}

// HTTP リクエストの ctx が終わっても、承認待ちは続く（ctx から独立した待ち）。
func TestRequestAcquireOutlivesTheRequestContext(t *testing.T) {
	e := newRequestAcquireEnv(t, domain.ApprovalAlways)
	ctx, cancel := context.WithCancel(context.Background())
	if _, pending, err := e.leases.RequestAcquire(ctx, ctlAlice); err != nil || !pending {
		t.Fatalf("RequestAcquire = pending %v, %v", pending, err)
	}
	req := e.awaitApprovalEvent(t)
	cancel()
	if _, ok := e.leases.Pending(); !ok || len(e.approval.Pending()) != 1 {
		t.Fatal("the request context ending must not withdraw the approval")
	}
	if err := e.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { h, ok := e.leases.Holder(); return ok && h.Actor.SameSession(ctlAlice) })
}

// 承認が要らない（auto）または保持中の再取得は、承認を作らず pending = false で取得する。他者の保持中は ErrLeaseHeldByOther。
func TestRequestAcquireWithoutApproval(t *testing.T) {
	e := newRequestAcquireEnv(t, domain.ApprovalAuto)
	l, pending, err := e.leases.RequestAcquire(context.Background(), ctlAlice)
	if err != nil || pending || l.ID == "" {
		t.Fatalf("RequestAcquire = %+v, pending %v, %v", l, pending, err)
	}
	if _, pending, err := e.leases.RequestAcquire(context.Background(), ctlAlice); err != nil || pending {
		t.Errorf("reacquire = pending %v, %v", pending, err)
	}
	if _, _, err := e.leases.RequestAcquire(context.Background(), ctlBob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("bob = %v, want ErrLeaseHeldByOther", err)
	}
	if n := e.counter.count(); n != 0 {
		t.Errorf("Approval.Request calls = %d, want 0", n)
	}
}

// approval.reset は host の操作として監査し、拒否後のクールダウンも解く。
func TestHostAdminResetApprovalIsAuditedAndClearsTheCooldown(t *testing.T) {
	e := newAdminEnv(t)
	spy := &cooldownSpy{LeaseManager: e.leases}
	admin, err := NewHostAdmin(HostAdminConfig{
		Clients: e.db.Clients(), Tokens: e.db.Tokens(), ConnRequests: e.db.ConnRequests(), Authz: e.authz,
		Leases: spy, Sessions: e.sessions, Audit: e.audit, Bus: e.bus, Clock: e.clock, IDs: &seqIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.admin = admin
	c := e.approve(t, "nodeA", "bob", ApproveInput{MCPEnabled: true, ApprovalMode: domain.ApprovalFirstTime})
	if err := e.db.Clients().MarkApproved(e.ctx, c.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	before := len(e.audit.entries)
	if _, err := admin.ResetApproval(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	added := e.audit.entries[before:]
	if len(added) != 1 || added[0].Event != "approval.reset" || added[0].Kind != "lease" || added[0].ActorType != domain.ActorHost ||
		added[0].ClientID != c.ID || added[0].Result != auditOK {
		t.Fatalf("audit added = %+v, want one approval.reset by host", added)
	}
	var detail map[string]any
	if err := json.Unmarshal(added[0].DetailJSON, &detail); err != nil || detail["client_id"] != c.ID {
		t.Errorf("detail = %s (%v)", added[0].DetailJSON, err)
	}
	if !reflect.DeepEqual(spy.cleared, []string{c.ID}) {
		t.Errorf("cooldown cleared for %v, want [%s]", spy.cleared, c.ID)
	}
	// 失敗した取り消し（conflict）は監査もクールダウンの解除もしない。
	n := len(e.audit.entries)
	if _, err := admin.ResetApproval(e.ctx, c.ID); domain.CodeOf(err) != domain.CodeConflict {
		t.Fatalf("second reset: %v", err)
	}
	if len(e.audit.entries) != n || len(spy.cleared) != 1 {
		t.Error("a conflicting reset must not be audited or clear the cooldown")
	}
	// 承認モードの変更・削除でも解く。
	auto := domain.ApprovalAuto
	if _, err := admin.UpdateClient(e.ctx, c.ID, ClientUpdate{ApprovalMode: &auto}); err != nil {
		t.Fatal(err)
	}
	if err := admin.DeleteClient(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spy.cleared, []string{c.ID, c.ID, c.ID}) {
		t.Errorf("cooldown cleared for %v, want 3 times (reset, mode change, delete)", spy.cleared)
	}
}

// cooldownSpy は ClearApprovalCooldown の呼び出しを記録する LeaseManager。
type cooldownSpy struct {
	LeaseManager
	cleared []string
}

func (s *cooldownSpy) ClearApprovalCooldown(clientID string) {
	s.cleared = append(s.cleared, clientID)
	s.LeaseManager.ClearApprovalCooldown(clientID)
}
