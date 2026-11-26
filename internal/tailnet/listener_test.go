package tailnet

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNet は ListenFunc の偽実装。実際にはソケットを開かない。
type fakeNet struct {
	mu        sync.Mutex
	failures  map[string]error // address → Listen の失敗
	listeners map[string]*fakeListener
	calls     []string // "network address"
	events    chan string
}

func newFakeNet() *fakeNet {
	return &fakeNet{
		failures:  make(map[string]error),
		listeners: make(map[string]*fakeListener),
		events:    make(chan string, 64),
	}
}

func (n *fakeNet) setFailure(addr string, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err == nil {
		delete(n.failures, addr)
		return
	}
	n.failures[addr] = err
}

func (n *fakeNet) listen(_ context.Context, network, address string) (net.Listener, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, network+" "+address)
	if err := n.failures[address]; err != nil {
		return nil, err
	}
	l := &fakeListener{
		net: n, addr: address,
		conns:  make(chan net.Conn),
		closed: make(chan struct{}),
		fail:   make(chan error, 1),
	}
	n.listeners[address] = l
	return l, nil
}

func (n *fakeNet) get(addr string) *fakeListener {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.listeners[addr]
}

func (n *fakeNet) allCalls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.calls)
}

type fakeListener struct {
	net       *fakeNet
	addr      string
	conns     chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	fail      chan error
}

func (l *fakeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.fail:
		return nil, err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *fakeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *fakeListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

func (l *fakeListener) Addr() net.Addr { return fakeAddr(l.addr) }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// dial は l の Accept へ接続を渡し、クライアント側の端を返す。
func (l *fakeListener) dial(ctx context.Context) (net.Conn, error) {
	c, s := net.Pipe()
	select {
	case l.conns <- s:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// listenerHarness は Serve を動かし、State の変化を集める。
type listenerHarness struct {
	t      *testing.T
	w      *fakeWatcher
	net    *fakeNet
	clock  *fakeClock
	l      Listener
	states chan ListenState
	cancel context.CancelFunc
	done   chan error
	port   int
	portMu sync.Mutex
}

func newHarness(t *testing.T, h http.Handler) *listenerHarness {
	t.Helper()
	hn := &listenerHarness{
		t: t, w: newFakeWatcher(), net: newFakeNet(), clock: newFakeClock(),
		states: make(chan ListenState, 64), done: make(chan error, 1), port: 8765,
	}
	hn.l = NewListener(ListenerConfig{
		Watcher:    hn.w,
		Clock:      hn.clock,
		ListenFunc: hn.net.listen,
		OnChange:   func(s ListenState) { hn.states <- s },
	})
	if h == nil {
		h = http.NotFoundHandler()
	}
	ctx, cancel := context.WithCancel(context.Background())
	hn.cancel = cancel
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-hn.done:
			if err != nil {
				t.Errorf("Serve = %v, want nil on cancel", err)
			}
		case <-time.After(waitTimeout):
			t.Error("Serve did not return after cancel")
		}
	})
	go func() {
		hn.done <- hn.l.Serve(ctx, h, func() int {
			hn.portMu.Lock()
			defer hn.portMu.Unlock()
			return hn.port
		})
	}()
	return hn
}

func (hn *listenerHarness) setPort(p int) {
	hn.portMu.Lock()
	hn.port = p
	hn.portMu.Unlock()
}

// waitState は pred を満たす State が通知されるまで待つ。
func (hn *listenerHarness) waitState(pred func(ListenState) bool) ListenState {
	hn.t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case s := <-hn.states:
			if pred(s) {
				return s
			}
		case <-deadline:
			hn.t.Fatal("timed out waiting for the expected ListenState")
			return ListenState{}
		}
	}
}

func addrsAre(want ...string) func(ListenState) bool {
	return func(s ListenState) bool { return slices.Equal(s.Addrs, want) }
}

const (
	v4a = "100.64.0.1:8765"
	v6a = "[fd7a:115c:a1e0::1]:8765"
)

func TestListenerListensOnEachTailnetAddress(t *testing.T) {
	hn := newHarness(t, nil)
	hn.w.send(running("100.64.0.1", "fd7a:115c:a1e0::1"))
	s := hn.waitState(addrsAre(v4a, v6a))
	if !s.Listening || s.Reason != "" {
		t.Fatalf("state = %+v", s)
	}
	if got := hn.l.State(); !got.Listening || !slices.Equal(got.Addrs, []string{v4a, v6a}) {
		t.Errorf("State() = %+v", got)
	}
	want := []string{"tcp4 " + v4a, "tcp6 " + v6a}
	if got := hn.net.allCalls(); !slices.Equal(got, want) {
		t.Errorf("listen calls = %v, want %v", got, want)
	}
}

func TestListenerWorksWithOnlyOneFamily(t *testing.T) {
	t.Run("IPv4 only", func(t *testing.T) {
		hn := newHarness(t, nil)
		hn.w.send(running("100.64.0.1"))
		hn.waitState(addrsAre(v4a))
		if got := hn.net.allCalls(); !slices.Equal(got, []string{"tcp4 " + v4a}) {
			t.Errorf("listen calls = %v", got)
		}
	})
	t.Run("IPv6 only", func(t *testing.T) {
		hn := newHarness(t, nil)
		hn.w.send(running("fd7a:115c:a1e0::1"))
		hn.waitState(addrsAre(v6a))
		if got := hn.net.allCalls(); !slices.Equal(got, []string{"tcp6 " + v6a}) {
			t.Errorf("listen calls = %v", got)
		}
	})
}

func TestListenerNeverListensOutsideTailnet(t *testing.T) {
	hn := newHarness(t, nil)
	hn.w.send(running("127.0.0.1", "0.0.0.0", "192.168.1.5", "10.0.0.1", "::1", "::", "fe80::1", "2001:db8::1", "100.63.255.255", "100.128.0.0"))
	s := hn.waitState(func(s ListenState) bool { return s.Reason != "" })
	if s.Listening || len(s.Addrs) != 0 {
		t.Fatalf("state = %+v, want not listening", s)
	}
	if got := hn.net.allCalls(); len(got) != 0 {
		t.Errorf("listen calls = %v, want none", got)
	}

	// tailnet のアドレスが混じっていても、それだけで Listen する（境界の 100.64.0.0 と 100.127.255.255 を含む）。
	hn.w.send(running("127.0.0.1", "100.64.0.0", "0.0.0.0", "100.127.255.255", "::"))
	hn.waitState(addrsAre("100.64.0.0:8765", "100.127.255.255:8765"))
	if got := hn.net.allCalls(); len(got) != 2 {
		t.Errorf("listen calls = %v, want exactly the two tailnet addresses", got)
	}
}

func TestListenerFollowsAddressSetChanges(t *testing.T) {
	hn := newHarness(t, nil)
	hn.w.send(running("100.64.0.1", "fd7a:115c:a1e0::1"))
	hn.waitState(addrsAre(v4a, v6a))
	oldV4, oldV6 := hn.net.get(v4a), hn.net.get(v6a)

	// IPv6 が外れる: 外れた側だけ閉じる。
	hn.w.send(running("100.64.0.1"))
	hn.waitState(addrsAre(v4a))
	if !oldV6.isClosed() {
		t.Error("the IPv6 listener was not closed")
	}
	if oldV4.isClosed() {
		t.Error("the unchanged IPv4 listener was closed")
	}

	// IPv4 が付け替わる: 新しいアドレスで張り、古いものは閉じる。
	hn.w.send(running("100.64.0.9"))
	hn.waitState(addrsAre("100.64.0.9:8765"))
	if !oldV4.isClosed() {
		t.Error("the old IPv4 listener was not closed")
	}

	// IPv6 が戻る: 追加される。
	hn.w.send(running("100.64.0.9", "fd7a:115c:a1e0::2"))
	hn.waitState(addrsAre("100.64.0.9:8765", "[fd7a:115c:a1e0::2]:8765"))

	// 同じ状態の再通知では張り直さない。
	before := len(hn.net.allCalls())
	hn.w.send(running("100.64.0.9", "fd7a:115c:a1e0::2"))
	hn.w.send(running("100.64.0.9", "fd7a:115c:a1e0::2"))
	hn.w.send(Status{}) // 同期用: これが反映されたら、直前の 2 つも処理済み
	hn.waitState(func(s ListenState) bool { return !s.Listening })
	if got := len(hn.net.allCalls()); got != before {
		t.Errorf("listen calls went from %d to %d on unchanged status", before, got)
	}
}

func TestListenerStopsWhileTailscaledIsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		reason string
	}{
		{"tailscaled unreachable", Status{Err: errors.New("connection refused")}, "tailscaled に接続できません"},
		{"not logged in", Status{}, "ログインしていません"},
		{"no IPs", Status{LoggedIn: true}, "IP アドレスがありません"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hn := newHarness(t, nil)
			hn.w.send(running("100.64.0.1", "fd7a:115c:a1e0::1"))
			hn.waitState(addrsAre(v4a, v6a))
			v4, v6 := hn.net.get(v4a), hn.net.get(v6a)

			hn.w.send(tt.status)
			s := hn.waitState(func(s ListenState) bool { return !s.Listening })
			if !strings.Contains(s.Reason, tt.reason) {
				t.Errorf("reason = %q, want it to mention %q", s.Reason, tt.reason)
			}
			if !v4.isClosed() || !v6.isClosed() {
				t.Error("listeners were left open while tailscaled is unavailable")
			}

			// 復帰したら張り直す（Reason は消える）。
			hn.w.send(running("100.64.0.1"))
			s = hn.waitState(addrsAre(v4a))
			if s.Reason != "" {
				t.Errorf("reason after recovery = %q", s.Reason)
			}
		})
	}
}

func TestListenerRetriesFailedBindWithinInterval(t *testing.T) {
	hn := newHarness(t, nil)
	// tailscaled の再起動直後など、IP が付く前の Listen は失敗する。
	hn.net.setFailure(v4a, errors.New("cannot assign requested address"))
	hn.w.send(running("100.64.0.1", "fd7a:115c:a1e0::1"))

	// IPv6 だけで動き、IPv4 の失敗が理由に出る。
	s := hn.waitState(addrsAre(v6a))
	if !strings.Contains(s.Reason, v4a) || !strings.Contains(s.Reason, "cannot assign requested address") {
		t.Errorf("reason = %q", s.Reason)
	}
	hn.clock.waitTimer(t)

	// まだ失敗する間は、再試行が続く。
	hn.clock.Advance(defaultRetryInterval)
	hn.clock.waitTimer(t)

	hn.net.setFailure(v4a, nil)
	hn.clock.Advance(defaultRetryInterval)
	s = hn.waitState(addrsAre(v4a, v6a))
	if s.Reason != "" {
		t.Errorf("reason after retry = %q", s.Reason)
	}
}

func TestListenerRejectsInvalidPort(t *testing.T) {
	hn := newHarness(t, nil)
	hn.setPort(0)
	hn.w.send(running("100.64.0.1"))
	s := hn.waitState(func(s ListenState) bool { return s.Reason != "" })
	if s.Listening || !strings.Contains(s.Reason, "ポート") {
		t.Fatalf("state = %+v", s)
	}
	hn.setPort(9000)
	hn.w.send(running("100.64.0.1", "100.64.0.2"))
	hn.waitState(addrsAre("100.64.0.1:9000", "100.64.0.2:9000"))
}

func TestListenerServesRequestsAndRecoversFromAcceptFailure(t *testing.T) {
	hn := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}))
	hn.w.send(running("100.64.0.1"))
	hn.waitState(addrsAre(v4a))

	get := func(l *fakeListener) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		client := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return l.dial(ctx) },
		}}
		defer client.CloseIdleConnections()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://tailnet.invalid/x", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	first := hn.net.get(v4a)
	if got := get(first); got != "hello /x" {
		t.Fatalf("body = %q", got)
	}

	// 受け付けが異常終了したら、間隔を置いて張り直す。
	first.fail <- errors.New("accept: boom")
	s := hn.waitState(func(s ListenState) bool { return !s.Listening })
	if !strings.Contains(s.Reason, v4a) {
		t.Errorf("reason = %q", s.Reason)
	}
	hn.clock.waitTimer(t)
	hn.clock.Advance(defaultRetryInterval)
	hn.waitState(addrsAre(v4a))
	second := hn.net.get(v4a)
	if second == first {
		t.Fatal("the listener was not rebound")
	}
	if got := get(second); got != "hello /x" {
		t.Fatalf("body after rebind = %q", got)
	}
}

func TestListenerServeStopsOnCancelAndClosesEverything(t *testing.T) {
	hn := newHarness(t, nil)
	hn.w.send(running("100.64.0.1", "fd7a:115c:a1e0::1"))
	hn.waitState(addrsAre(v4a, v6a))
	v4, v6 := hn.net.get(v4a), hn.net.get(v6a)

	hn.cancel()
	select {
	case err := <-hn.done:
		if err != nil {
			t.Fatalf("Serve = %v, want nil on cancel", err)
		}
		hn.done <- nil // Cleanup が読む分
	case <-time.After(waitTimeout):
		t.Fatal("Serve did not return")
	}
	if !v4.isClosed() || !v6.isClosed() {
		t.Error("listeners are still open after Serve returned")
	}
	if s := hn.l.State(); s.Listening || len(s.Addrs) != 0 {
		t.Errorf("State() = %+v after stop", s)
	}
}

func TestListenerServeErrors(t *testing.T) {
	t.Run("second Serve", func(t *testing.T) {
		hn := newHarness(t, nil)
		hn.w.send(running("100.64.0.1"))
		hn.waitState(addrsAre(v4a))
		err := hn.l.Serve(context.Background(), http.NotFoundHandler(), func() int { return 1 })
		if err == nil {
			t.Fatal("want an error for a second Serve")
		}
	})
	t.Run("nil watcher", func(t *testing.T) {
		l := NewListener(ListenerConfig{})
		if err := l.Serve(context.Background(), http.NotFoundHandler(), func() int { return 1 }); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("watcher closed early", func(t *testing.T) {
		w := newFakeWatcher()
		l := NewListener(ListenerConfig{Watcher: w, ListenFunc: newFakeNet().listen})
		close(w.ch)
		if err := l.Serve(context.Background(), http.NotFoundHandler(), func() int { return 1 }); err == nil {
			t.Fatal("want an error when the watcher stops unexpectedly")
		}
	})
}

func TestListenerInitialState(t *testing.T) {
	l := NewListener(ListenerConfig{Watcher: newFakeWatcher()})
	s := l.State()
	if s.Listening || len(s.Addrs) != 0 || s.Reason == "" {
		t.Errorf("initial State() = %+v", s)
	}
}

func TestListenerWithWatcherEndToEnd(t *testing.T) {
	// 本物の Watcher と偽の LocalAPI をつなぎ、tailscaled の再起動後に Listen が戻ることを確かめる。
	clock := newFakeClock()
	api := newFakeAPI()
	api.setStatus(running("100.64.0.1", "fd7a:115c:a1e0::1"), nil)
	fn := newFakeNet()
	states := make(chan ListenState, 64)
	l := NewListener(ListenerConfig{
		Watcher:    NewWatcher(WatcherConfig{API: api, Clock: clock}),
		Clock:      clock,
		ListenFunc: fn.listen,
		OnChange:   func(s ListenState) { states <- s },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Serve(ctx, http.NotFoundHandler(), func() int { return 8765 }) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve = %v, want nil on cancel", err)
		}
	}()
	wait := func(pred func(ListenState) bool) ListenState {
		t.Helper()
		deadline := time.After(waitTimeout)
		for {
			select {
			case s := <-states:
				if pred(s) {
					return s
				}
			case <-deadline:
				t.Fatal("timed out")
				return ListenState{}
			}
		}
	}
	wait(addrsAre(v4a, v6a))
	bus := api.nextBus(t)

	api.setWatchErr(errors.New("no such file"))
	bus.disconnect()
	s := wait(func(s ListenState) bool { return !s.Listening })
	if !strings.Contains(s.Reason, "tailscaled") {
		t.Errorf("reason = %q", s.Reason)
	}

	api.setWatchErr(nil)
	// Watcher の再試行の待ち（After）が登録されてから進める。登録の前に進めると、再試行が発火せず待ちが尽きる（CI の負荷で起きた不安定）。
	clock.waitPending(t)
	clock.Advance(defaultRetryInterval)
	wait(addrsAre(v4a, v6a))
}

func TestNewHTTPServerBoundsIdleConnectionsAndHeaders(t *testing.T) {
	srv := newHTTPServer(context.Background(), http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second || srv.IdleTimeout != 120*time.Second || srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("timeouts = %v / %v / %d", srv.ReadHeaderTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, must stay unset for long-lived responses", srv.WriteTimeout)
	}
}
