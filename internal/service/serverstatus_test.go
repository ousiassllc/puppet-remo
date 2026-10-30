package service

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

type statusWatcher struct {
	ch  chan tailnet.Status
	err error
}

func (w *statusWatcher) Watch(context.Context) (<-chan tailnet.Status, error) { return w.ch, w.err }

type statusListener struct{ st tailnet.ListenState }

func (l *statusListener) State() tailnet.ListenState { return l.st }

func TestStatusTrackerStates(t *testing.T) {
	ip4, ip6 := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("fd7a:115c:a1e0::1")
	tests := []struct {
		name     string
		status   *tailnet.Status // nil は状態を受け取る前
		listener tailnet.ListenState
		want     ServerStatus
	}{
		{"before any status", nil, tailnet.ListenState{}, ServerStatus{Tailscaled: TailscaledUnknown, Port: 8765}},
		{"running and listening",
			&tailnet.Status{LoggedIn: true, IPs: []netip.Addr{ip4, ip6}, DNSName: "host.tail.ts.net"},
			tailnet.ListenState{Listening: true, Addrs: []string{"100.64.0.1:8765"}},
			ServerStatus{Tailscaled: TailscaledRunning, TailnetIPs: []string{"100.64.0.1", "fd7a:115c:a1e0::1"}, DNSName: "host.tail.ts.net", Port: 8765,
				Listening: true, ListenAddrs: []string{"100.64.0.1:8765"}}},
		{"needs login", &tailnet.Status{LoggedIn: false}, tailnet.ListenState{Reason: "no tailnet address"},
			ServerStatus{Tailscaled: TailscaledNeedsLogin, Port: 8765, Reason: "tailscaled にログインしていません。"}},
		{"unavailable", &tailnet.Status{Err: errors.New("socket: permission denied")}, tailnet.ListenState{},
			ServerStatus{Tailscaled: TailscaledUnavailable, Port: 8765, Reason: "tailscaled に接続できません。"}},
		{"running but listen failed", &tailnet.Status{LoggedIn: true, IPs: []netip.Addr{ip4}}, tailnet.ListenState{Reason: "address in use"},
			ServerStatus{Tailscaled: TailscaledRunning, TailnetIPs: []string{"100.64.0.1"}, Port: 8765, Reason: "address in use"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &statusWatcher{ch: make(chan tailnet.Status, 1)}
			bus := NewBus()
			events, unsubscribe := bus.Subscribe(TopicServerStatus)
			defer unsubscribe()
			tr := NewStatusTracker(StatusTrackerConfig{Watcher: w, Listener: &statusListener{st: tt.listener}, Port: func() int { return 8765 }, Bus: bus})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- tr.Run(ctx) }()
			if tt.status != nil {
				w.ch <- *tt.status
				recvEvent(t, events) // Run が状態を取り込んだ通知
			}
			got := tr.ServerStatus(ctx)
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if got.Tailscaled != tt.want.Tailscaled || got.Port != tt.want.Port || got.DNSName != tt.want.DNSName ||
				got.Listening != tt.want.Listening || got.Reason != tt.want.Reason ||
				!slices.Equal(got.TailnetIPs, tt.want.TailnetIPs) || !slices.Equal(got.ListenAddrs, tt.want.ListenAddrs) {
				t.Errorf("status = %+v\nwant     %+v", got, tt.want)
			}
		})
	}
}

func TestStatusTrackerPublishesOnChange(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe(TopicServerStatus)
	defer cancel()
	w := &statusWatcher{ch: make(chan tailnet.Status, 1)}
	tr := NewStatusTracker(StatusTrackerConfig{Watcher: w, Bus: bus})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()

	w.ch <- tailnet.Status{LoggedIn: true}
	recvEvent(t, ch)
	tr.Notify() // Listener の OnChange から呼ばれる
	recvEvent(t, ch)
	// 状態は通知の時点で読める（通知が先に届いても、読み直せば最新）。
	if got := tr.ServerStatus(ctx).Tailscaled; got != TailscaledRunning {
		t.Errorf("Tailscaled = %q", got)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStatusTrackerRunReturnsWatchError(t *testing.T) {
	boom := errors.New("cannot watch")
	tr := NewStatusTracker(StatusTrackerConfig{Watcher: &statusWatcher{err: boom}})
	if err := tr.Run(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Run = %v", err)
	}
	// Watcher のチャネルが閉じたら戻る。
	w := &statusWatcher{ch: make(chan tailnet.Status)}
	close(w.ch)
	if err := NewStatusTracker(StatusTrackerConfig{Watcher: w}).Run(context.Background()); err != nil {
		t.Errorf("Run on closed channel = %v", err)
	}
}
