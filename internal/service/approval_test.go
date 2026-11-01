package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// approvalWait は上限つきの待ち（固定時間の sleep ではなく、チャネルの受信に期限をつける）。
const approvalWait = 5 * time.Second

type approvalEnv struct {
	clock  *ctlClock
	timers *ctlTimers
	bus    *Bus
	events <-chan Event
	a      Approval
}

func newApprovalEnv(t *testing.T) *approvalEnv {
	t.Helper()
	e := &approvalEnv{clock: newCtlClock(), timers: &ctlTimers{}, bus: NewBus()}
	var cancel func()
	e.events, cancel = e.bus.Subscribe(TopicApproval)
	t.Cleanup(cancel)
	a, err := NewApproval(ApprovalConfig{Clock: e.clock, IDs: &ctlIDs{}, Bus: e.bus, AfterFunc: e.timers.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	e.a = a
	return e
}

type approvalResultMsg struct {
	d   Decision
	err error
}

// request は別 goroutine で Request を呼び、approval イベント（＝要求が登録された合図）を受けてから要求を返す。
func (e *approvalEnv) request(t *testing.T, ctx context.Context, who domain.Actor) (ApprovalRequest, <-chan approvalResultMsg) {
	t.Helper()
	out := make(chan approvalResultMsg, 1)
	go func() {
		d, err := e.a.Request(ctx, ApprovalRequest{Kind: ApprovalKindLease, Who: who})
		out <- approvalResultMsg{d, err}
	}()
	select {
	case ev := <-e.events:
		data, ok := ev.Data.(ApprovalEvent)
		if !ok || data.Kind != ApprovalKindLease || data.ID == "" {
			t.Fatalf("approval event data = %#v", ev.Data)
		}
		for _, r := range e.a.Pending() {
			if r.ID == data.ID {
				return r, out
			}
		}
		t.Fatalf("event %q is not in Pending() = %+v", data.ID, e.a.Pending())
	case <-time.After(approvalWait):
		t.Fatal("approval event was not published")
	}
	return ApprovalRequest{}, nil
}

func recv(t *testing.T, ch <-chan approvalResultMsg) approvalResultMsg {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(approvalWait):
		t.Fatal("Request did not return")
	}
	return approvalResultMsg{}
}

func TestApprovalResolve(t *testing.T) {
	for _, allow := range []bool{true, false} {
		e := newApprovalEnv(t)
		req, out := e.request(t, context.Background(), ctlAlice)
		if req.Who != ctlAlice || req.Kind != ApprovalKindLease {
			t.Errorf("request = %+v", req)
		}
		if want := e.clock.Now().Add(DefaultApprovalTimeout); !req.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", req.ExpiresAt, want)
		}
		if err := e.a.Resolve(req.ID, allow); err != nil {
			t.Fatalf("Resolve(allow=%v): %v", allow, err)
		}
		if r := recv(t, out); r.err != nil || r.d.Allow != allow {
			t.Errorf("allow=%v: Request = %+v, %v", allow, r.d, r.err)
		}
		if p := e.a.Pending(); len(p) != 0 {
			t.Errorf("Pending after resolve = %+v", p)
		}
		if e.timers.Active() != 0 {
			t.Error("解決したらタイマーを止める")
		}
	}
}

func TestApprovalResolveTwiceAndUnknown(t *testing.T) {
	e := newApprovalEnv(t)
	req, out := e.request(t, context.Background(), ctlAlice)
	if err := e.a.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	recv(t, out)
	if err := e.a.Resolve(req.ID, false); !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("二重の Resolve: err = %v, want ErrApprovalTimeout", err)
	}
	if err := e.a.Resolve("nope", true); !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("未知の id の Resolve: err = %v, want ErrApprovalTimeout", err)
	}
}

func TestApprovalTimeout(t *testing.T) {
	e := newApprovalEnv(t)
	req, out := e.request(t, context.Background(), ctlAlice)
	if got := e.timers.Last(); got != DefaultApprovalTimeout {
		t.Errorf("タイマーの時間 = %v, want %v", got, DefaultApprovalTimeout)
	}
	e.timers.FireAll()
	if r := recv(t, out); !errors.Is(r.err, domain.ErrApprovalTimeout) {
		t.Errorf("Request err = %v, want ErrApprovalTimeout", r.err)
	}
	if p := e.a.Pending(); len(p) != 0 {
		t.Errorf("Pending after timeout = %+v", p)
	}
	// 失効後の Resolve は 410（ErrApprovalTimeout）。
	if err := e.a.Resolve(req.ID, true); !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("失効後の Resolve: err = %v, want ErrApprovalTimeout", err)
	}
}

func TestApprovalCustomTimeout(t *testing.T) {
	timers := &ctlTimers{}
	a, err := NewApproval(ApprovalConfig{Clock: newCtlClock(), IDs: &ctlIDs{}, AfterFunc: timers.AfterFunc, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.Request(ctx, ApprovalRequest{Kind: ApprovalKindLease, Who: ctlAlice})
	}()
	waitFor(t, func() bool { return len(a.Pending()) == 1 })
	if got := timers.Last(); got != 5*time.Second {
		t.Errorf("タイマーの時間 = %v, want 5s", got)
	}
	cancel()
	<-done
}

// waitFor は cond が真になるまで、上限つきで譲りながら待つ。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(approvalWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestApprovalContextCancel(t *testing.T) {
	e := newApprovalEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, out := e.request(t, ctx, ctlAlice)
	cancel()
	if r := recv(t, out); !errors.Is(r.err, context.Canceled) {
		t.Errorf("Request err = %v, want context.Canceled", r.err)
	}
	if p := e.a.Pending(); len(p) != 0 {
		t.Errorf("取り下げたら Pending から消える: %+v", p)
	}
	if e.timers.Active() != 0 {
		t.Error("取り下げたらタイマーを止める")
	}
	if err := e.a.Resolve(req.ID, true); !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("取り下げ後の Resolve: err = %v, want ErrApprovalTimeout", err)
	}
}

func TestApprovalRequestWithCanceledContext(t *testing.T) {
	e := newApprovalEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.a.Request(ctx, ApprovalRequest{Kind: ApprovalKindLease, Who: ctlAlice}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(e.a.Pending()) != 0 || len(e.events) != 0 {
		t.Error("すでにキャンセルされた ctx では要求を作らない")
	}
}

func TestApprovalPendingOrderAndIsolation(t *testing.T) {
	e := newApprovalEnv(t)
	r1, out1 := e.request(t, context.Background(), ctlAlice)
	r2, out2 := e.request(t, context.Background(), ctlBob)
	if r1.ID == r2.ID {
		t.Fatal("要求の ID は一意")
	}
	p := e.a.Pending()
	if len(p) != 2 || p[0].ID != r1.ID || p[1].ID != r2.ID {
		t.Fatalf("Pending = %+v, want [%s %s]（要求の順）", p, r1.ID, r2.ID)
	}
	if err := e.a.Resolve(r2.ID, false); err != nil {
		t.Fatal(err)
	}
	if r := recv(t, out2); r.err != nil || r.d.Allow {
		t.Errorf("r2 = %+v", r)
	}
	select {
	case r := <-out1:
		t.Fatalf("r1 は別の要求の解決で返らない: %+v", r)
	default:
	}
	if p := e.a.Pending(); len(p) != 1 || p[0].ID != r1.ID {
		t.Errorf("Pending = %+v", p)
	}
	if err := e.a.Resolve(r1.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := recv(t, out1); r.err != nil || !r.d.Allow {
		t.Errorf("r1 = %+v", r)
	}
}

func TestApprovalPublishDoesNotBlock(t *testing.T) {
	// 購読者が読まなくても（バッファが満杯でも）Request は進む（NFR-P06）。Bus は古いイベントを捨てる。
	e := newApprovalEnv(t)
	var wg sync.WaitGroup
	const n = busBuffer * 3
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.a.Request(context.Background(), ApprovalRequest{Kind: ApprovalKindLease, Who: ctlAlice})
		}()
	}
	waitFor(t, func() bool { return len(e.a.Pending()) == n })
	for _, r := range e.a.Pending() {
		if err := e.a.Resolve(r.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestApprovalResolveRaceWithTimeoutDeliversOneResult(t *testing.T) {
	// 満了と Resolve が競っても、Request に届く結果は 1 つで、Resolve は成功か ErrApprovalTimeout のどちらか。
	for range 50 {
		e := newApprovalEnv(t)
		req, out := e.request(t, context.Background(), ctlAlice)
		resolved := make(chan error, 1)
		go func() { resolved <- e.a.Resolve(req.ID, true) }()
		e.timers.FireAll()
		err := <-resolved
		r := recv(t, out)
		switch {
		case err == nil && r.err == nil && r.d.Allow:
		case errors.Is(err, domain.ErrApprovalTimeout) && errors.Is(r.err, domain.ErrApprovalTimeout):
		default:
			t.Fatalf("Resolve err = %v, Request = %+v, %v", err, r.d, r.err)
		}
	}
}

func TestNewApprovalRequiresClockAndIDs(t *testing.T) {
	if _, err := NewApproval(ApprovalConfig{IDs: &ctlIDs{}}); err == nil {
		t.Error("Clock なしはエラー")
	}
	if _, err := NewApproval(ApprovalConfig{Clock: newCtlClock()}); err == nil {
		t.Error("IDs なしはエラー")
	}
}

func TestApprovalWithoutBus(t *testing.T) {
	timers := &ctlTimers{}
	a, err := NewApproval(ApprovalConfig{Clock: newCtlClock(), IDs: &ctlIDs{}, AfterFunc: timers.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Request(context.Background(), ApprovalRequest{Kind: ApprovalKindLease, Who: ctlAlice})
		done <- err
	}()
	waitFor(t, func() bool { return len(a.Pending()) == 1 })
	timers.FireAll()
	if err := <-done; !errors.Is(err, domain.ErrApprovalTimeout) {
		t.Errorf("err = %v, want ErrApprovalTimeout", err)
	}
}

// 承認待ちが終わるたびに approval-closed を 1 回出す（許可・拒否・失効・取り下げのどれでも）。Pending から消えたあとに届く。
func TestApprovalPublishesClosedWhenTheRequestEnds(t *testing.T) {
	ends := map[string]func(e *approvalEnv, req ApprovalRequest, cancel context.CancelFunc){
		"allow":   func(e *approvalEnv, req ApprovalRequest, _ context.CancelFunc) { _ = e.a.Resolve(req.ID, true) },
		"deny":    func(e *approvalEnv, req ApprovalRequest, _ context.CancelFunc) { _ = e.a.Resolve(req.ID, false) },
		"timeout": func(e *approvalEnv, _ ApprovalRequest, _ context.CancelFunc) { e.timers.FireAll() },
		"cancel":  func(_ *approvalEnv, _ ApprovalRequest, cancel context.CancelFunc) { cancel() },
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			e := newApprovalEnv(t)
			closed, cancelSub := e.bus.Subscribe(TopicApprovalClosed)
			defer cancelSub()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, out := e.request(t, ctx, ctlAlice)
			select {
			case ev := <-closed:
				t.Fatalf("approval-closed before the request ended: %+v", ev)
			default:
			}

			end(e, req, cancel)
			recv(t, out)
			select {
			case ev := <-closed:
				if got, ok := ev.Data.(ApprovalClosedEvent); !ok || got.ID != req.ID {
					t.Errorf("event data = %#v, want ApprovalClosedEvent{%s}", ev.Data, req.ID)
				}
				if p := e.a.Pending(); len(p) != 0 {
					t.Errorf("the event must come after the request left Pending: %+v", p)
				}
			case <-time.After(approvalWait):
				t.Fatal("no approval-closed event")
			}
			// 二重の Resolve（負けた側）は何も出さない。
			_ = e.a.Resolve(req.ID, true)
			select {
			case ev := <-closed:
				t.Errorf("a second approval-closed event: %+v", ev)
			default:
			}
		})
	}
}
