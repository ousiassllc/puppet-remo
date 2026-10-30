package tailnet

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func recvStatus(t *testing.T, ch <-chan Status) Status {
	t.Helper()
	select {
	case s, ok := <-ch:
		if !ok {
			t.Fatal("status channel closed")
		}
		return s
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for a status")
		return Status{}
	}
}

func startWatcher(t *testing.T, api *fakeAPI, clock *fakeClock) (<-chan Status, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := NewWatcher(WatcherConfig{API: api, Clock: clock}).Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ch, cancel
}

func running(addrs ...string) Status {
	return Status{LoggedIn: true, IPs: ips(addrs...), DNSName: "host.tail.ts.net"}
}

func TestWatcherEmitsInitialStatusAndIPChanges(t *testing.T) {
	api := newFakeAPI()
	api.setStatus(running("100.64.0.1", "fd7a:115c:a1e0::1"), nil)
	ch, _ := startWatcher(t, api, newFakeClock())

	s := recvStatus(t, ch)
	if !s.LoggedIn || !slices.Equal(s.IPs, ips("100.64.0.1", "fd7a:115c:a1e0::1")) || s.DNSName != "host.tail.ts.net" || s.Err != nil {
		t.Fatalf("initial = %+v", s)
	}
	bus := api.nextBus(t)

	// IPv6 が外れる。
	api.setStatus(running("100.64.0.1"), nil)
	bus.notify()
	if s := recvStatus(t, ch); !slices.Equal(s.IPs, ips("100.64.0.1")) {
		t.Fatalf("after IPv6 removal = %+v", s)
	}

	// IPv4 が付け替わる。
	api.setStatus(running("100.64.0.9"), nil)
	bus.notify()
	if s := recvStatus(t, ch); !slices.Equal(s.IPs, ips("100.64.0.9")) {
		t.Fatalf("after IP change = %+v", s)
	}

	// ログアウト。
	api.setStatus(Status{}, nil)
	bus.notify()
	if s := recvStatus(t, ch); s.LoggedIn || len(s.IPs) != 0 {
		t.Fatalf("after logout = %+v", s)
	}
}

func TestWatcherSuppressesUnchangedStatus(t *testing.T) {
	api := newFakeAPI()
	api.setStatus(running("100.64.0.1"), nil)
	ch, _ := startWatcher(t, api, newFakeClock())
	recvStatus(t, ch)
	bus := api.nextBus(t)
	api.waitStatusCall(t) // 初回の取得

	// 通知が 2 回届いても状態が同じなら、何も流れない。
	bus.notify()
	api.waitStatusCall(t)
	bus.notify()
	api.waitStatusCall(t) // 1 回目の通知の処理（emit）はこの時点で終わっている
	select {
	case s := <-ch:
		t.Fatalf("unchanged status was emitted: %+v", s)
	default:
	}
}

func TestWatcherRecoversAfterTailscaledRestart(t *testing.T) {
	clock := newFakeClock()
	api := newFakeAPI()
	api.setStatus(running("100.64.0.1"), nil)
	ch, _ := startWatcher(t, api, clock)
	recvStatus(t, ch)
	bus1 := api.nextBus(t)

	// tailscaled が止まる: バスが切れ、以後の接続も失敗する。
	api.setWatchErr(errors.New("dial: no such file"))
	bus1.disconnect()
	s := recvStatus(t, ch)
	if s.Err == nil || s.LoggedIn || len(s.IPs) != 0 {
		t.Fatalf("outage status = %+v, want Err and no IPs", s)
	}
	clock.waitTimer(t)

	// 切断の理由から接続失敗の理由に変わるので 1 度流れる。その後も失敗が続く間は、同じエラーを重ねて流さない。
	clock.Advance(defaultRetryInterval)
	if s := recvStatus(t, ch); s.Err == nil {
		t.Fatalf("status = %+v, want the connection failure", s)
	}
	clock.waitTimer(t)
	clock.Advance(defaultRetryInterval)
	clock.waitTimer(t)
	select {
	case s := <-ch:
		t.Fatalf("repeated outage was emitted: %+v", s)
	default:
	}

	// tailscaled が戻る（IP は変わっていてもよい）。再試行間隔内に追従する。
	api.setWatchErr(nil)
	api.setStatus(running("100.64.0.7"), nil)
	clock.Advance(defaultRetryInterval)
	s = recvStatus(t, ch)
	if s.Err != nil || !s.LoggedIn || !slices.Equal(s.IPs, ips("100.64.0.7")) {
		t.Fatalf("recovered status = %+v", s)
	}
	if defaultRetryInterval > 2*time.Second {
		t.Fatalf("retry interval %v is too long for the 10s recovery (NFR-A03)", defaultRetryInterval)
	}
	if api.nextBus(t).isClosed() { // 新しい購読が張られている
		t.Error("the new subscription is already closed")
	}
}

func TestWatcherReportsStatusFailure(t *testing.T) {
	clock := newFakeClock()
	api := newFakeAPI()
	api.setStatus(Status{}, errors.New("status unavailable"))
	ch, _ := startWatcher(t, api, clock)
	if s := recvStatus(t, ch); s.Err == nil {
		t.Fatalf("status = %+v, want Err", s)
	}
	bus := api.nextBus(t)
	clock.waitTimer(t)
	// 状態を取得できなかった購読は閉じられ、時間を置いて張り直される。
	if !bus.isClosed() {
		t.Error("the failed subscription was not closed")
	}
	api.setStatus(running("100.64.0.1"), nil)
	clock.Advance(defaultRetryInterval)
	if s := recvStatus(t, ch); s.Err != nil || len(s.IPs) != 1 {
		t.Fatalf("status = %+v", s)
	}
}

func TestWatcherStopsOnContextCancel(t *testing.T) {
	api := newFakeAPI()
	api.setStatus(running("100.64.0.1"), nil)
	ch, cancel := startWatcher(t, api, newFakeClock())
	recvStatus(t, ch)
	bus := api.nextBus(t)
	cancel()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if !bus.isClosed() {
					t.Error("the subscription was not closed")
				}
				return
			}
		case <-time.After(waitTimeout):
			t.Fatal("status channel was not closed after cancel")
		}
	}
}

func TestWatcherKeepsOnlyLatestForSlowReader(t *testing.T) {
	api := newFakeAPI()
	api.setStatus(running("100.64.0.1"), nil)
	ch, _ := startWatcher(t, api, newFakeClock())
	bus := api.nextBus(t)
	api.waitStatusCall(t)

	// 読み手が止まっていても送り手は詰まらず、最新の状態が残る。
	for _, ip := range []string{"100.64.0.2", "100.64.0.3", "100.64.0.4"} {
		api.setStatus(running(ip), nil)
		bus.notify()
		api.waitStatusCall(t)
	}
	var last Status
	deadline := time.After(waitTimeout)
	for !slices.Equal(last.IPs, ips("100.64.0.4")) {
		select {
		case last = <-ch:
		case <-deadline:
			t.Fatalf("latest status never arrived; last = %+v", last)
		}
	}
}

func TestWatcherRejectsNilAPI(t *testing.T) {
	if _, err := NewWatcher(WatcherConfig{}).Watch(context.Background()); err == nil {
		t.Fatal("want an error for a nil LocalAPI")
	}
}
