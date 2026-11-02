package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// leaseClientsFake は LeaseClients のメモリ実装（クライアントの承認モードと approved_at を持つ）。
type leaseClientsFake struct {
	mu      sync.Mutex
	clients map[string]domain.Client
	getErr  error
	marked  []string
	markErr error
}

func (f *leaseClientsFake) Get(_ context.Context, id string) (domain.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return domain.Client{}, f.getErr
	}
	c, ok := f.clients[id]
	if !ok {
		return domain.Client{}, repository.ErrNotFound
	}
	return c, nil
}

func (f *leaseClientsFake) MarkApproved(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return f.markErr
	}
	c := f.clients[id]
	c.ApprovedAt = &at
	f.clients[id] = c
	f.marked = append(f.marked, id)
	return nil
}

func (f *leaseClientsFake) set(c domain.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients[c.ID] = c
}

func (f *leaseClientsFake) markedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.marked...)
}

// flagKill は発動状態を手で切り替えられる KillSwitchState（ReleaseAll を伴わない「発動した直後」を再現する）。
type flagKill struct {
	mu     sync.Mutex
	active bool
}

func (k *flagKill) Active() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.active
}

func (k *flagKill) set(v bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.active = v
}

type leaseApprovalEnv struct {
	*ctlEnv
	approval  Approval
	aTimers   *ctlTimers // Approval のタイマー（リースのアイドルタイマー e.timers とは別）
	bus       *Bus
	approvals <-chan Event
	clients   *leaseClientsFake
}

// newLeaseApprovalEnv は承認モードを適用する LeaseManager（e.leases）と、それを使う Control（e.control）を組み立てる。
// alice（client-a）は承認モード mode、bob（client-b）は always。
func newLeaseApprovalEnv(t *testing.T, mode domain.ApprovalMode) *leaseApprovalEnv {
	t.Helper()
	e := newCtlEnv(t)
	return rebuildLeaseApproval(t, e, mode, nil)
}

func rebuildLeaseApproval(t *testing.T, e *ctlEnv, mode domain.ApprovalMode, kill KillSwitchState) *leaseApprovalEnv {
	t.Helper()
	le := &leaseApprovalEnv{ctlEnv: e, aTimers: &ctlTimers{}, bus: NewBus()}
	var cancel func()
	le.approvals, cancel = le.bus.Subscribe(TopicApproval)
	t.Cleanup(cancel)
	le.clients = &leaseClientsFake{clients: map[string]domain.Client{
		"client-a": {ID: "client-a", DisplayName: "alice", ApprovalMode: mode},
		"client-b": {ID: "client-b", DisplayName: "bob", ApprovalMode: domain.ApprovalAlways},
	}}
	var err error
	le.approval, err = NewApproval(ApprovalConfig{Clock: e.clock, IDs: &ctlIDs{}, Bus: le.bus, AfterFunc: le.aTimers.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	if kill == nil {
		kill = e.kill
	}
	e.leases, err = NewLeaseManager(LeaseConfig{
		Clock: e.clock, IDs: &ctlIDs{}, IdleTimeout: domain.DefaultLeaseIdleTimeout,
		Audit: e.audit, Kill: kill, AfterFunc: e.timers.AfterFunc, Power: e.power,
		Approval: le.approval, Clients: le.clients,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.kill.BindLeases(e.leases)
	e.control, err = NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps,
		Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return le
}

type acquireResult struct {
	l   Lease
	err error
}

// acquire は別 goroutine で Acquire を呼び、承認が要る場合は approval イベント（承認待ちが始まった合図）を受けてから返す。
func (le *leaseApprovalEnv) acquire(t *testing.T, ctx context.Context, who domain.Actor) (<-chan acquireResult, ApprovalRequest) {
	t.Helper()
	out := make(chan acquireResult, 1)
	go func() {
		l, err := le.leases.Acquire(ctx, who)
		out <- acquireResult{l, err}
	}()
	select {
	case ev := <-le.approvals:
		data := ev.Data.(ApprovalEvent)
		for _, r := range le.approval.Pending() {
			if r.ID == data.ID {
				return out, r
			}
		}
		t.Fatalf("event %q is not pending", data.ID)
	case r := <-out:
		t.Fatalf("Acquire returned without waiting for approval: %+v", r)
	case <-time.After(approvalWait):
		t.Fatal("approval event was not published")
	}
	return nil, ApprovalRequest{}
}

func (le *leaseApprovalEnv) wait(t *testing.T, out <-chan acquireResult) acquireResult {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(approvalWait):
		t.Fatal("Acquire did not return")
	}
	return acquireResult{}
}

func (le *leaseApprovalEnv) expectNoApproval(t *testing.T) {
	t.Helper()
	select {
	case ev := <-le.approvals:
		t.Fatalf("unexpected approval request: %+v", ev)
	default:
	}
}

func TestLeaseAcquireAlwaysWaitsForApproval(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	sub := le.leases.Subscribe()
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if req.Who != ctlAlice || req.Kind != ApprovalKindLease {
		t.Fatalf("request = %+v", req)
	}

	// 承認待ちの間: held ではなく、Pending に要求者が出る。アイドルタイマーは張らない。
	if _, ok := le.leases.Holder(); ok {
		t.Error("Holder は承認待ちの間 held ではない")
	}
	p, ok := le.leases.Pending()
	if !ok || !p.Actor.SameSession(ctlAlice) || p.LeaseID == "" || !p.Since.Equal(le.clock.Now()) {
		t.Errorf("Pending = %+v, %v", p, ok)
	}
	if n := le.timers.Active(); n != 0 {
		t.Errorf("承認待ちの間のアイドルタイマー = %d, want 0", n)
	}
	if le.power.Awake() {
		t.Error("承認待ちの間はスリープを抑止しない（held になってから）")
	}

	le.clock.Advance(10 * time.Second)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	r := le.wait(t, out)
	if r.err != nil || r.l.ID != p.LeaseID || !r.l.Holder.SameSession(ctlAlice) {
		t.Fatalf("Acquire = %+v, %v; want the pending lease %q", r.l, r.err, p.LeaseID)
	}
	if !r.l.Since.Equal(le.clock.Now()) {
		t.Errorf("Since = %v, want the approval time %v", r.l.Since, le.clock.Now())
	}
	h, ok := le.leases.Holder()
	if !ok || h.LeaseID != r.l.ID || !h.Actor.SameSession(ctlAlice) {
		t.Errorf("Holder = %+v, %v", h, ok)
	}
	if _, ok := le.leases.Pending(); ok {
		t.Error("承認後は Pending が無い")
	}
	if n := le.timers.Active(); n != 1 {
		t.Errorf("承認後のアイドルタイマー = %d, want 1", n)
	}
	if !le.power.Awake() {
		t.Error("held になったらスリープを抑止する")
	}
	if ids := le.clients.markedIDs(); len(ids) != 0 {
		t.Errorf("always は approved_at を記録しない: %v", ids)
	}

	// 遷移は idle → pending_approval → held の順に購読者へ届く。
	var got []domain.LeaseState
	for range 2 {
		select {
		case ev := <-sub:
			got = append(got, ev.To)
		case <-time.After(approvalWait):
			t.Fatal("lease event missing")
		}
	}
	if got[0] != domain.LeasePendingApproval || got[1] != domain.LeaseHeld {
		t.Errorf("lease states = %v, want [pending_approval held]", got)
	}
	// 監査: 承認の要求 → 許可 → リースの取得（この順）。
	wantEvents := []string{"approval.requested", "approval.granted", "lease.acquire"}
	if ev := le.audit.Events(); !reflect.DeepEqual(ev, wantEvents) {
		t.Fatalf("audit = %v, want %v", ev, wantEvents)
	}
	for _, e := range le.audit.Entries() {
		if e.Result != auditOK || e.LeaseID != r.l.ID || e.ClientID != "client-a" || e.Kind != "lease" {
			t.Errorf("audit entry = %+v", e)
		}
	}
}

func TestLeaseAcquireDeniedReturnsToIdle(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, false); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); !errors.Is(r.err, domain.ErrApprovalDenied) {
		t.Fatalf("err = %v, want ErrApprovalDenied", r.err)
	}
	if _, ok := le.leases.Holder(); ok {
		t.Error("拒否されたら held にならない")
	}
	if _, ok := le.leases.Pending(); ok {
		t.Error("拒否されたら承認待ちでなくなる")
	}
	if err := le.leases.Check(ctlAlice); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Errorf("Check = %v, want ErrLeaseRequired（拒否は剥奪ではない）", err)
	}
	if ids := le.clients.markedIDs(); len(ids) != 0 {
		t.Errorf("拒否では approved_at を記録しない: %v", ids)
	}
	wantEvents := []string{"approval.requested", "approval.denied"}
	if ev := le.audit.Events(); !reflect.DeepEqual(ev, wantEvents) {
		t.Errorf("audit = %v, want %v", ev, wantEvents)
	}
	// 他者がすぐ取得できる（bob も always なので承認を待つ）。
	out2, req2 := le.acquire(t, context.Background(), ctlBob)
	if err := le.approval.Resolve(req2.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out2); r.err != nil {
		t.Fatalf("bob: %v", r.err)
	}
}

func TestLeaseAcquireTimeout(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, _ := le.acquire(t, context.Background(), ctlAlice)
	if got := le.aTimers.Last(); got != DefaultApprovalTimeout {
		t.Errorf("承認の待ち時間 = %v, want %v", got, DefaultApprovalTimeout)
	}
	le.aTimers.FireAll()
	if r := le.wait(t, out); !errors.Is(r.err, domain.ErrApprovalTimeout) {
		t.Fatalf("err = %v, want ErrApprovalTimeout", r.err)
	}
	if _, ok := le.leases.Holder(); ok {
		t.Error("タイムアウトで held にならない")
	}
	if _, ok := le.leases.Pending(); ok {
		t.Error("タイムアウトで承認待ちでなくなる")
	}
	if le.power.Awake() {
		t.Error("held にならなかったのでスリープを抑止しない")
	}
}

func TestLeaseAcquireContextCancelWithdraws(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	ctx, cancel := context.WithCancel(context.Background())
	out, req := le.acquire(t, ctx, ctlAlice)
	cancel()
	if r := le.wait(t, out); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", r.err)
	}
	if _, ok := le.leases.Pending(); ok {
		t.Error("取り下げたら承認待ちでなくなる")
	}
	if len(le.approval.Pending()) != 0 {
		t.Error("取り下げたら Approval の待ちも消える")
	}
	if err := le.approval.Resolve(req.ID, true); !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("取り下げ後の Resolve: %v, want ErrApprovalTimeout", err)
	}
	if _, ok := le.leases.Holder(); ok {
		t.Error("held にならない")
	}
}

func TestLeaseAcquireKillSwitchWithdrawsPending(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); !errors.Is(r.err, domain.ErrKillSwitchActive) {
		t.Fatalf("err = %v, want ErrKillSwitchActive", r.err)
	}
	if _, ok := le.leases.Pending(); ok {
		t.Error("キルスイッチで承認待ちが取り下げられる")
	}
	// 後から許可しても held にならない（要求は取り下げ済みで、Resolve は失効扱い）。
	if err := le.approval.Resolve(req.ID, true); !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("取り下げ後の Resolve: %v, want ErrApprovalTimeout", err)
	}
	if _, ok := le.leases.Holder(); ok {
		t.Error("held にならない")
	}
}

func TestLeaseAcquireApprovedAfterKillSwitchActiveDoesNotHold(t *testing.T) {
	// キルスイッチの状態は立ったが ReleaseAll がまだ届いていない間に許可が返っても、held にしない。
	e := newCtlEnv(t)
	kill := &flagKill{}
	le := rebuildLeaseApproval(t, e, domain.ApprovalAlways, kill)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	kill.set(true)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); !errors.Is(r.err, domain.ErrKillSwitchActive) {
		t.Fatalf("err = %v, want ErrKillSwitchActive", r.err)
	}
	if _, ok := le.leases.Holder(); ok {
		t.Error("held にならない")
	}
	if _, ok := le.leases.Pending(); ok {
		t.Error("承認待ちも残らない")
	}
	if ids := le.clients.markedIDs(); len(ids) != 0 {
		t.Errorf("approved_at を記録しない: %v", ids)
	}
}

func TestLeaseAcquireWhilePendingIsRefused(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)

	// 他者の取得は待たずに ErrLeaseHeldByOther（FR-L03）。承認要求も増やさない。
	if _, err := le.leases.Acquire(context.Background(), ctlBob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("bob Acquire = %v, want ErrLeaseHeldByOther", err)
	}
	le.expectNoApproval(t)
	if n := len(le.approval.Pending()); n != 1 {
		t.Errorf("承認待ちの要求 = %d, want 1", n)
	}
	// 本人も、承認前は Acquire・Check できない。Release は何もしない。
	if _, err := le.leases.Acquire(context.Background(), ctlAlice); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("alice 2 回目の Acquire = %v, want ErrLeaseHeldByOther", err)
	}
	if err := le.leases.Check(ctlAlice); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("alice Check = %v, want ErrLeaseHeldByOther", err)
	}
	if err := le.leases.Check(ctlBob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("bob Check = %v, want ErrLeaseHeldByOther", err)
	}
	le.leases.Release(ctlAlice)
	if _, ok := le.leases.Pending(); !ok {
		t.Error("明示解放は承認待ちを取り下げない")
	}

	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); r.err != nil {
		t.Fatal(r.err)
	}
	// 他者の拒否の audit（lease.acquire denied）は既存のとおり記録される。
	var denied int
	for _, en := range le.audit.Entries() {
		if en.Event == "lease.acquire" && en.Result == auditDenied && en.ErrorCode == string(domain.CodeLeaseHeldByOther) {
			denied++
		}
	}
	if denied != 2 { // bob と alice の 2 回目の Acquire（Check は記録しない）
		t.Errorf("lease_held_by_other の拒否の記録 = %d, want 2", denied)
	}
}

func TestLeaseHolderReturnsWhilePendingWithoutBlocking(t *testing.T) {
	// 承認待ちの間ロックを持たない: Holder・Pending・Release・Revoke が即座に返る。
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	done := make(chan struct{})
	go func() {
		defer close(done)
		le.leases.Holder()
		le.leases.Pending()
		le.leases.Release(ctlBob)
		_ = le.leases.Revoke(HostActor)
		_ = le.leases.SetIdleTimeout(time.Minute)
	}()
	select {
	case <-done:
	case <-time.After(approvalWait):
		t.Fatal("承認待ちの間にロックを持っている")
	}
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	le.wait(t, out)
}

func TestLeaseReleaseSessionWithdrawsPending(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	le.leases.ReleaseSession(ctlBob) // 別のセッションの切断は影響しない
	if _, ok := le.leases.Pending(); !ok {
		t.Fatal("他者の切断で取り下げられた")
	}
	le.leases.ReleaseSession(ctlAlice) // クライアントの削除・無効化・セッション切断
	if r := le.wait(t, out); !errors.Is(r.err, domain.ErrLeaseRequired) {
		t.Fatalf("err = %v, want ErrLeaseRequired（取り下げ）", r.err)
	}
	if _, ok := le.leases.Pending(); ok {
		t.Error("承認待ちが残っている")
	}
	if err := le.approval.Resolve(req.ID, true); !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("取り下げ後の Resolve: %v, want ErrApprovalTimeout", err)
	}
	if _, ok := le.leases.Holder(); ok {
		t.Error("held にならない")
	}
}

func TestLeaseFirstTimeApprovesOnce(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalFirstTime)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); r.err != nil {
		t.Fatal(r.err)
	}
	if ids := le.clients.markedIDs(); len(ids) != 1 || ids[0] != "client-a" {
		t.Fatalf("approved_at の記録 = %v, want [client-a]", ids)
	}
	le.leases.Release(ctlAlice)

	// 2 回目は承認なしで idle → held。
	l, err := le.leases.Acquire(context.Background(), ctlAlice)
	if err != nil || l.ID == "" {
		t.Fatalf("2 回目の Acquire = %+v, %v", l, err)
	}
	le.expectNoApproval(t)
	if ids := le.clients.markedIDs(); len(ids) != 1 {
		t.Errorf("2 回目は記録し直さない: %v", ids)
	}
	le.leases.Release(ctlAlice)

	// 承認の取り消し（approved_at を NULL に戻す。承認モードの変更でも同じ）で、また承認が要る。
	c, _ := le.clients.Get(context.Background(), "client-a")
	c.ApprovedAt = nil
	le.clients.set(c)
	out, req = le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, false); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); !errors.Is(r.err, domain.ErrApprovalDenied) {
		t.Fatalf("取り消し後の拒否: %v", r.err)
	}
	if ids := le.clients.markedIDs(); len(ids) != 1 {
		t.Errorf("拒否では記録しない: %v", ids)
	}
}

func TestLeaseFirstTimeDeniedDoesNotMarkApproved(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalFirstTime)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, false); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); !errors.Is(r.err, domain.ErrApprovalDenied) {
		t.Fatalf("err = %v", r.err)
	}
	if ids := le.clients.markedIDs(); len(ids) != 0 {
		t.Errorf("拒否で approved_at を記録した: %v", ids)
	}
}

func TestLeaseFirstTimeMarkFailureKeepsTheLease(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalFirstTime)
	le.clients.markErr = errors.New("disk full")
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); r.err != nil {
		t.Fatalf("承認の記録に失敗しても承認済みのリースは取り消さない: %v", r.err)
	}
	if _, ok := le.leases.Holder(); !ok {
		t.Error("held のまま")
	}
}

func TestLeaseAutoNeedsNoApproval(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAuto)
	l, err := le.leases.Acquire(context.Background(), ctlAlice)
	if err != nil || l.ID == "" {
		t.Fatalf("Acquire = %+v, %v", l, err)
	}
	le.expectNoApproval(t)
	if len(le.approval.Pending()) != 0 {
		t.Error("auto は承認を要求しない")
	}
	if ev := le.audit.Events(); len(ev) != 1 || ev[0] != "lease.acquire" {
		t.Errorf("audit = %v", ev)
	}
}

func TestLeaseReacquireByHolderNeedsNoApproval(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	first := le.wait(t, out)
	if first.err != nil {
		t.Fatal(first.err)
	}
	again, err := le.leases.Acquire(context.Background(), ctlAlice)
	if err != nil || again.ID != first.l.ID {
		t.Fatalf("保持者の再取得 = %+v, %v; want the same lease", again, err)
	}
	le.expectNoApproval(t)
}

func TestLeaseExpiredHolderIsReplacedThroughApproval(t *testing.T) {
	// 保持者のアイドルが過ぎていれば、次の取得者は（承認が要るなら）承認を待つ。
	le := newLeaseApprovalEnv(t, domain.ApprovalAuto)
	if _, err := le.leases.Acquire(context.Background(), ctlAlice); err != nil {
		t.Fatal(err)
	}
	le.clock.Advance(domain.DefaultLeaseIdleTimeout)
	out, req := le.acquire(t, context.Background(), ctlBob)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); r.err != nil || !r.l.Holder.SameSession(ctlBob) {
		t.Fatalf("bob = %+v, %v", r.l, r.err)
	}
}

func TestLeaseAcquireFailsClosedWhenClientUnreadable(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(le *leaseApprovalEnv)
		who     domain.Actor
		wantErr error
		wantAny bool
	}{
		{name: "クライアントが無い", who: domain.Actor{Kind: domain.ActorMCP, ClientID: "gone", SessionID: "s"}, wantErr: ErrDeviceNotRegistered},
		{name: "ClientID が空", who: domain.Actor{Kind: domain.ActorMCP, SessionID: "s"}, wantErr: ErrDeviceNotRegistered},
		{name: "読み取りの失敗", who: ctlAlice, setup: func(le *leaseApprovalEnv) { le.clients.getErr = errors.New("db closed") }, wantAny: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			le := newLeaseApprovalEnv(t, domain.ApprovalAuto)
			if tt.setup != nil {
				tt.setup(le)
			}
			_, err := le.leases.Acquire(context.Background(), tt.who)
			if tt.wantAny && err == nil || !tt.wantAny && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if _, ok := le.leases.Holder(); ok {
				t.Error("held にならない")
			}
			le.expectNoApproval(t)
		})
	}
}

func TestLeaseAcquireInvalidActorIsStillInvalidArgumentWithApproval(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	for _, who := range []domain.Actor{
		{Kind: domain.ActorAgent, ClientID: "client-a", SessionID: "task"},
		{Kind: domain.ActorMCP, ClientID: "client-a"},
	} {
		if _, err := le.leases.Acquire(context.Background(), who); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Acquire(%+v) = %v, want ErrInvalidArgument", who, err)
		}
	}
	le.expectNoApproval(t)
}

func TestLeaseKillSwitchBeforeAcquireDoesNotAskForApproval(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	if err := le.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := le.leases.Acquire(context.Background(), ctlAlice); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("err = %v, want ErrKillSwitchActive", err)
	}
	le.expectNoApproval(t)
}

func TestLeaseRevokeDoesNotAffectPending(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.leases.Revoke(HostActor); err != nil {
		t.Fatal(err)
	}
	if _, ok := le.leases.Pending(); !ok {
		t.Fatal("剥奪は保持の取り消しで、承認待ちは対象ではない")
	}
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); r.err != nil {
		t.Fatal(r.err)
	}
}

func TestLeaseRevokedHolderMustBeApprovedAgain(t *testing.T) {
	// FR-L06: 剥奪後に自身が取得する場合は承認モードが適用される。
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	out, req := le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	le.wait(t, out)
	if err := le.leases.Revoke(HostActor); err != nil {
		t.Fatal(err)
	}
	if err := le.leases.Check(ctlAlice); !errors.Is(err, domain.ErrLeaseRevoked) {
		t.Fatalf("Check = %v, want ErrLeaseRevoked", err)
	}
	out, req = le.acquire(t, context.Background(), ctlAlice)
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := le.wait(t, out); r.err != nil {
		t.Fatal(r.err)
	}
	if err := le.leases.Check(ctlAlice); err != nil {
		t.Errorf("Check after reacquire = %v", err)
	}
}

func TestNewLeaseManagerApprovalConfig(t *testing.T) {
	base := LeaseConfig{Clock: newCtlClock(), IDs: &ctlIDs{}, IdleTimeout: time.Minute}
	approval, err := NewApproval(ApprovalConfig{Clock: base.Clock, IDs: base.IDs})
	if err != nil {
		t.Fatal(err)
	}
	onlyApproval, onlyClients := base, base
	onlyApproval.Approval = approval
	onlyClients.Clients = &leaseClientsFake{}
	for name, cfg := range map[string]LeaseConfig{"Approval だけ": onlyApproval, "Clients だけ": onlyClients} {
		if _, err := NewLeaseManager(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	both := base
	both.Approval, both.Clients = approval, &leaseClientsFake{}
	if _, err := NewLeaseManager(both); err != nil {
		t.Errorf("両方あれば作れる: %v", err)
	}
}

func TestControlImplicitAcquireWaitsForApproval(t *testing.T) {
	// FR-L04 と FR-L07: 暗黙取得（mcp）は承認を待つ。承認後に操作が実行される。
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	type doResult struct{ err error }
	out := make(chan doResult, 1)
	go func() {
		_, err := le.control.Do(context.Background(), ctlAlice, domain.Click{Button: domain.ButtonLeft, Count: 1})
		out <- doResult{err}
	}()
	var req ApprovalRequest
	select {
	case ev := <-le.approvals:
		for _, r := range le.approval.Pending() {
			if r.ID == ev.Data.(ApprovalEvent).ID {
				req = r
			}
		}
	case <-time.After(approvalWait):
		t.Fatal("approval event was not published")
	}
	if len(le.input.CallsTo("Click")) != 0 {
		t.Fatal("承認前に入力を実行した")
	}
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-out:
		if r.err != nil {
			t.Fatal(r.err)
		}
	case <-time.After(approvalWait):
		t.Fatal("Do did not return")
	}
	if len(le.input.CallsTo("Click")) != 1 {
		t.Errorf("承認後に 1 回だけクリックする: %+v", inputCalls(le.ctlEnv))
	}
}

func TestControlImplicitAcquireDeniedDoesNotExecute(t *testing.T) {
	for name, tc := range map[string]struct {
		resolve func(le *leaseApprovalEnv, req ApprovalRequest)
		want    error
	}{
		"拒否":     {func(le *leaseApprovalEnv, r ApprovalRequest) { _ = le.approval.Resolve(r.ID, false) }, domain.ErrApprovalDenied},
		"タイムアウト": {func(le *leaseApprovalEnv, _ ApprovalRequest) { le.aTimers.FireAll() }, domain.ErrApprovalTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
			out := make(chan error, 1)
			go func() {
				_, err := le.control.Do(context.Background(), ctlAlice, domain.Click{Button: domain.ButtonLeft, Count: 1})
				out <- err
			}()
			var req ApprovalRequest
			select {
			case ev := <-le.approvals:
				for _, r := range le.approval.Pending() {
					if r.ID == ev.Data.(ApprovalEvent).ID {
						req = r
					}
				}
			case <-time.After(approvalWait):
				t.Fatal("approval event was not published")
			}
			tc.resolve(le, req)
			select {
			case err := <-out:
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
			case <-time.After(approvalWait):
				t.Fatal("Do did not return")
			}
			if len(le.input.CallsTo("Click")) != 0 {
				t.Error("承認されなかった操作を実行した")
			}
			// 監査の result は denied（auditResult は承認の拒否・タイムアウトを denied にする）。
			entries := le.audit.Entries()
			last := entries[len(entries)-1]
			if last.Kind != "tool" || last.Result != auditDenied || last.ErrorCode != string(domain.CodeOf(tc.want)) {
				t.Errorf("tool の監査 = %+v", last)
			}
		})
	}
}

func TestControlLaunchAsksApprovalOnlyAtLeaseAcquire(t *testing.T) {
	// launch_app への承認は、リース取得時の 1 回だけ（FR-L07。オーナー決定）。保持中の呼び出しごとの承認は足さない。
	le := rebuildLeaseApproval(t, newAppEnv(t), domain.ApprovalAlways, nil)
	app := registerNotepad(t, le.ctlEnv)

	out := make(chan error, 1)
	go func() {
		_, err := le.control.Launch(context.Background(), ctlAlice, app.ID)
		out <- err
	}()
	var req ApprovalRequest
	select {
	case ev := <-le.approvals:
		for _, r := range le.approval.Pending() {
			if r.ID == ev.Data.(ApprovalEvent).ID {
				req = r
			}
		}
	case <-time.After(approvalWait):
		t.Fatal("approval event was not published")
	}
	if err := le.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-out:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(approvalWait):
		t.Fatal("Launch did not return")
	}
	// 保持中の 2 回目の起動は承認を要求しない。
	if _, err := le.control.Launch(context.Background(), ctlAlice, app.ID); err != nil {
		t.Fatal(err)
	}
	le.expectNoApproval(t)
}
