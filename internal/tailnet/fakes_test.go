package tailnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// waitTimeout は「届くはずのものが届かない」ときにテストを失敗させる上限。待ち時間の仕様ではない。
const waitTimeout = 5 * time.Second

// fakeClock は進行をテストが制御する Clock。
type fakeClock struct {
	mu         sync.Mutex
	now        time.Time
	waiters    []fakeWaiter
	registered chan struct{} // After が呼ばれるたびに 1 つ送る
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:        time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		registered: make(chan struct{}, 64),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	select {
	case c.registered <- struct{}{}:
	default:
	}
	return ch
}

// Advance は時刻を進め、期限に達した After を発火する。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	rest := c.waiters[:0]
	for _, w := range c.waiters {
		if !c.now.Before(w.at) {
			w.ch <- c.now
		} else {
			rest = append(rest, w)
		}
	}
	c.waiters = rest
}

// waitTimer は After が呼ばれる（待機が始まる）まで待つ。
func (c *fakeClock) waitTimer(t *testing.T) {
	t.Helper()
	select {
	case <-c.registered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for a timer to be registered")
	}
}

// fakeAPI は偽の LocalAPI。
type fakeAPI struct {
	mu          sync.Mutex
	status      Status
	statusErr   error
	watchErr    error
	whoIs       func(addr string) (Identity, error)
	whoIsCalls  []string
	statusCalls chan struct{} // StatusWithoutPeers が呼ばれるたびに送る
	buses       chan *fakeBus // WatchIPNBus が成功するたびに送る
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		statusCalls: make(chan struct{}, 64),
		buses:       make(chan *fakeBus, 16),
	}
}

func (a *fakeAPI) setStatus(s Status, err error) {
	a.mu.Lock()
	a.status, a.statusErr = s, err
	a.mu.Unlock()
}

func (a *fakeAPI) setWatchErr(err error) {
	a.mu.Lock()
	a.watchErr = err
	a.mu.Unlock()
}

func (a *fakeAPI) StatusWithoutPeers(context.Context) (Status, error) {
	a.mu.Lock()
	s, err := a.status, a.statusErr
	a.mu.Unlock()
	select {
	case a.statusCalls <- struct{}{}:
	default:
	}
	return s, err
}

func (a *fakeAPI) WhoIs(_ context.Context, addr string) (Identity, error) {
	a.mu.Lock()
	a.whoIsCalls = append(a.whoIsCalls, addr)
	f := a.whoIs
	a.mu.Unlock()
	if f == nil {
		return Identity{}, errors.New("no whois func")
	}
	return f(addr)
}

func (a *fakeAPI) whoIsCallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.whoIsCalls)
}

func (a *fakeAPI) WatchIPNBus(ctx context.Context) (BusWatcher, error) {
	a.mu.Lock()
	err := a.watchErr
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b := &fakeBus{events: make(chan struct{}, 8), closed: make(chan struct{}), broken: make(chan struct{})}
	a.buses <- b
	return b, nil
}

func (a *fakeAPI) nextBus(t *testing.T) *fakeBus {
	t.Helper()
	select {
	case b := <-a.buses:
		return b
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for WatchIPNBus")
		return nil
	}
}

func (a *fakeAPI) waitStatusCall(t *testing.T) {
	t.Helper()
	select {
	case <-a.statusCalls:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for StatusWithoutPeers")
	}
}

// fakeBus は偽の IPN バス購読。
type fakeBus struct {
	events    chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	broken    chan struct{}
	breakOnce sync.Once
}

func (b *fakeBus) notify() { b.events <- struct{}{} }

// disconnect は tailscaled の停止による切断を模す。
func (b *fakeBus) disconnect() { b.breakOnce.Do(func() { close(b.broken) }) }

func (b *fakeBus) Next() error {
	select {
	case <-b.events:
		return nil
	case <-b.broken:
		return errors.New("connection reset")
	case <-b.closed:
		return net.ErrClosed
	}
}

func (b *fakeBus) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func (b *fakeBus) isClosed() bool {
	select {
	case <-b.closed:
		return true
	default:
		return false
	}
}

// fakeWatcher はテストが Status を直接流す Watcher。
type fakeWatcher struct{ ch chan Status }

func newFakeWatcher() *fakeWatcher { return &fakeWatcher{ch: make(chan Status, 16)} }

func (w *fakeWatcher) Watch(ctx context.Context) (<-chan Status, error) { return w.ch, nil }

func (w *fakeWatcher) send(s Status) { w.ch <- s }

func ips(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}
