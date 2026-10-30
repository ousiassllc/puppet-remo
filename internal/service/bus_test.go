package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

const busWait = 5 * time.Second

func recvEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(busWait):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

func expectNoEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected event %+v", e)
	default:
	}
}

func TestBusPublishesToMatchingSubscribers(t *testing.T) {
	bus := NewBus()
	all, cancelAll := bus.Subscribe()
	defer cancelAll()
	leases, cancelLeases := bus.Subscribe(TopicLease)
	defer cancelLeases()

	bus.Publish(Event{Topic: TopicKillSwitch})
	if e := recvEvent(t, all); e.Topic != TopicKillSwitch {
		t.Errorf("all subscriber got %+v", e)
	}
	expectNoEvent(t, leases)

	bus.Publish(Event{Topic: TopicLease, Data: 7})
	if e := recvEvent(t, leases); e.Topic != TopicLease || e.Data != 7 {
		t.Errorf("lease subscriber got %+v", e)
	}
	if e := recvEvent(t, all); e.Topic != TopicLease {
		t.Errorf("all subscriber got %+v", e)
	}
}

func TestBusCancelStopsDelivery(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	cancel()
	cancel() // 複数回呼んでよい
	bus.Publish(Event{Topic: TopicLease})
	expectNoEvent(t, ch)
}

// 遅い購読者が詰まっても、Publish は待たされず、最新のイベントは必ず届く（最も古いものを捨てる）。
func TestBusSlowSubscriberKeepsNewestEvents(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range busBuffer * 3 {
			bus.Publish(Event{Topic: TopicSessions, Data: i})
		}
	}()
	select {
	case <-done:
	case <-time.After(busWait):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	var last any
	for len(ch) > 0 {
		last = (<-ch).Data
	}
	if last != busBuffer*3-1 {
		t.Errorf("newest event = %v, want %d", last, busBuffer*3-1)
	}
}

func TestBridgeLeaseEvents(t *testing.T) {
	e := newCtlEnv(t)
	bus := NewBus()
	ch, cancel := bus.Subscribe(TopicLease)
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	done := BridgeLeaseEvents(ctx, e.leases, bus)

	mustAcquire(t, e, ctlAlice)
	if got := recvEvent(t, ch); got.Topic != TopicLease {
		t.Errorf("event = %+v", got)
	}
	e.leases.Release(ctlAlice)
	if got := recvEvent(t, ch); got.Topic != TopicLease {
		t.Errorf("event = %+v", got)
	}
	stop()
	select {
	case <-done:
	case <-time.After(busWait):
		t.Fatal("bridge did not stop")
	}
}

type fakeHostKill struct {
	active     bool
	trigErr    error
	releaseErr error
}

func (k *fakeHostKill) Active() bool { return k.active }
func (k *fakeHostKill) State() KillSwitchSnapshot {
	return KillSwitchSnapshot{Active: k.active}
}
func (k *fakeHostKill) Trigger(string) error { k.active = true; return k.trigErr }
func (k *fakeHostKill) Release() error {
	if k.releaseErr != nil {
		return k.releaseErr
	}
	k.active = false
	return nil
}

func TestNotifyingKillSwitch(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	inner := &fakeHostKill{trigErr: errors.New("closer failed")}
	k := NewNotifyingKillSwitch(inner, bus)

	// 発動は killswitch・lease・sessions を通知し、inner のエラーをそのまま返す。
	if err := k.Trigger("gui"); !errors.Is(err, inner.trigErr) {
		t.Errorf("Trigger error = %v", err)
	}
	var topics []string
	for range 3 {
		topics = append(topics, recvEvent(t, ch).Topic)
	}
	if topics[0] != TopicKillSwitch || topics[1] != TopicLease || topics[2] != TopicSessions {
		t.Errorf("topics = %v", topics)
	}
	if !k.Active() || !k.State().Active {
		t.Error("state must pass through")
	}

	inner.releaseErr = errors.New("persist failed")
	if err := k.Release(); err == nil {
		t.Error("Release error was swallowed")
	}
	if e := recvEvent(t, ch); e.Topic != TopicKillSwitch {
		t.Errorf("event = %+v", e)
	}
	inner.releaseErr = nil
	if err := k.Release(); err != nil || k.Active() {
		t.Errorf("Release: err=%v active=%v", err, k.Active())
	}
}

func TestNotifyingConnRequests(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	db, err := repository.Open(ctx, t.TempDir()+"/puppet-remo.db", repository.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus := NewBus()
	ch, cancel := bus.Subscribe(TopicConnectionRequests)
	defer cancel()
	store := NewNotifyingConnRequests(db.ConnRequests(), bus)

	req := repository.ConnRequest{ID: "r1", StableNodeID: "nodeA", LoginName: "a@example.com", DisplayName: "A", RemoteIP: "100.64.0.9", ExpiresAt: clock.Now().Add(time.Hour)}
	if _, created, err := store.UpsertPending(ctx, req); err != nil || !created {
		t.Fatalf("UpsertPending: created=%v err=%v", created, err)
	}
	recvEvent(t, ch)

	// 既存の pending への集約（再試行）は通知しない。
	req.ID = "r2"
	if _, created, err := store.UpsertPending(ctx, req); err != nil || created {
		t.Fatalf("aggregate: created=%v err=%v", created, err)
	}
	expectNoEvent(t, ch)

	if err := store.Decide(ctx, "r1", repository.ConnDenied, nil); err != nil {
		t.Fatal(err)
	}
	recvEvent(t, ch)
	// 失敗した Decide は通知しない。
	if err := store.Decide(ctx, "r1", repository.ConnDenied, nil); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("second Decide = %v", err)
	}
	expectNoEvent(t, ch)

	if err := store.Expire(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	recvEvent(t, ch)
}
