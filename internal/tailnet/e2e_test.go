//go:build e2e

package tailnet

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

// freeLoopbackPort は今空いている loopback のポートを返す（取ってすぐ閉じる。テストの 1 回の Listen にだけ使う）。
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestLoopbackListenFuncBindsLoopbackOnly(t *testing.T) {
	port := freeLoopbackPort(t)
	ln, err := LoopbackListenFunc()(context.Background(), "tcp4", net.JoinHostPort(E2ETailnetIP, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() || addr.IP.IsUnspecified() {
		t.Fatalf("listening on %v, want a loopback address", addr)
	}
	if addr.Port != port {
		t.Fatalf("port = %d, want %d", addr.Port, port)
	}
}

func TestLoopbackListenFuncRejectsIPv6(t *testing.T) {
	if ln, err := LoopbackListenFunc()(context.Background(), "tcp6", "[fd7a:115c:a1e0::1]:8765"); err == nil {
		_ = ln.Close()
		t.Fatal("tcp6 must be rejected")
	}
}

func TestE2EStatusListenTargetIsTheTailnetIPOnly(t *testing.T) {
	got := tailnetAddrs(E2EStatus().IPs)
	if len(got) != 1 || got[0] != netip.MustParseAddr(E2ETailnetIP) {
		t.Fatalf("listener targets = %v, want only %s (127.0.0.1 is reported for the Host check but never listened on by tailnet.Listener)", got, E2ETailnetIP)
	}
}

// 本物の Listener（NewListener）に偽の Watcher と LoopbackListenFunc を渡すと、tailnet の IPv4 に Listen する代わりに
// loopback の同じポートで配信し、State は tailnet のアドレスのまま報告する。
func TestE2EListenerServesOnLoopback(t *testing.T) {
	port := freeLoopbackPort(t)
	l := NewListener(ListenerConfig{Watcher: NewE2EWatcher(E2EStatus()), ListenFunc: LoopbackListenFunc()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- l.Serve(ctx, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }), func() int { return port })
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(waitTimeout):
			t.Error("Serve did not stop")
		}
	}()

	deadline := time.Now().Add(waitTimeout)
	for !l.State().Listening {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the listener: %+v", l.State())
		}
		time.Sleep(time.Millisecond)
	}
	if want := net.JoinHostPort(E2ETailnetIP, strconv.Itoa(port)); len(l.State().Addrs) != 1 || l.State().Addrs[0] != want {
		t.Fatalf("State.Addrs = %v, want [%s]", l.State().Addrs, want)
	}
	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body, _ := io.ReadAll(resp.Body); string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestE2EWhoIsSwitches(t *testing.T) {
	a, b := Identity{StableNodeID: "node-a"}, Identity{StableNodeID: "node-b"}
	w := NewE2EWhoIs(a)
	if got, _ := w.Lookup(context.Background(), "x"); got != a {
		t.Fatalf("got %v", got)
	}
	w.Set(b)
	if got, _ := w.Lookup(context.Background(), "x"); got != b {
		t.Fatalf("got %v", got)
	}
}
