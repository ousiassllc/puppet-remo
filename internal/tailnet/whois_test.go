package tailnet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func okWhoIs(name string) func(string) (Identity, error) {
	return func(addr string) (Identity, error) {
		return Identity{LoginName: name, StableNodeID: "n-" + addr, NodeName: "host"}, nil
	}
}

func TestWhoIsCache(t *testing.T) {
	clock := newFakeClock()
	api := newFakeAPI()
	api.whoIs = okWhoIs("alice@example.com")
	w := NewWhoIs(WhoIsConfig{API: api, Clock: clock})
	ctx := context.Background()

	id, err := w.Lookup(ctx, "100.64.0.5:51234")
	if err != nil {
		t.Fatal(err)
	}
	if id.LoginName != "alice@example.com" || id.StableNodeID != "n-100.64.0.5" {
		t.Fatalf("identity = %+v", id)
	}
	if got := api.whoIsCalls[0]; got != "100.64.0.5" {
		t.Errorf("LocalAPI called with %q, want the IP without the port", got)
	}

	// 同じ IP は、ポートが違っても期限内ならキャッシュを使う。
	clock.Advance(MaxWhoIsTTL - time.Nanosecond)
	if _, err := w.Lookup(ctx, "100.64.0.5:60000"); err != nil {
		t.Fatal(err)
	}
	if n := api.whoIsCallCount(); n != 1 {
		t.Fatalf("LocalAPI calls before expiry = %d, want 1", n)
	}

	// 期限（10 秒）ちょうどで失効し、引き直す。
	clock.Advance(time.Nanosecond)
	if _, err := w.Lookup(ctx, "100.64.0.5:51234"); err != nil {
		t.Fatal(err)
	}
	if n := api.whoIsCallCount(); n != 2 {
		t.Fatalf("LocalAPI calls after expiry = %d, want 2", n)
	}

	// 別の IP は別エントリ。
	if _, err := w.Lookup(ctx, "100.64.0.6:1"); err != nil {
		t.Fatal(err)
	}
	if n := api.whoIsCallCount(); n != 3 {
		t.Fatalf("LocalAPI calls for a new IP = %d, want 3", n)
	}
}

func TestWhoIsReflectsReassignmentAfterTTL(t *testing.T) {
	clock := newFakeClock()
	api := newFakeAPI()
	owner := "alice@example.com"
	api.whoIs = func(addr string) (Identity, error) { return okWhoIs(owner)(addr) }
	w := NewWhoIs(WhoIsConfig{API: api, Clock: clock})

	if id, _ := w.Lookup(context.Background(), "100.64.0.5"); id.LoginName != "alice@example.com" {
		t.Fatalf("first = %+v", id)
	}
	owner = "bob@example.com" // IP の再割り当て
	clock.Advance(MaxWhoIsTTL)
	id, err := w.Lookup(context.Background(), "100.64.0.5")
	if err != nil || id.LoginName != "bob@example.com" {
		t.Fatalf("after TTL = %+v, %v; want bob", id, err)
	}
}

func TestWhoIsFailsClosed(t *testing.T) {
	boom := errors.New("tailscaled is down")
	tests := []struct {
		name string
		addr string
		fn   func(string) (Identity, error)
	}{
		{"LocalAPI error", "100.64.0.5:1", func(string) (Identity, error) { return Identity{}, boom }},
		{"invalid address", "not-an-ip", okWhoIs("a@example.com")},
		{"empty address", "", okWhoIs("a@example.com")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI()
			api.whoIs = tt.fn
			w := NewWhoIs(WhoIsConfig{API: api, Clock: newFakeClock()})
			_, err := w.Lookup(context.Background(), tt.addr)
			if !errors.Is(err, ErrWhoIsFailed) {
				t.Fatalf("err = %v, want ErrWhoIsFailed", err)
			}
			if got := domain.CodeOf(err); got != domain.CodeWhoisFailed {
				t.Errorf("code = %q, want %q", got, domain.CodeWhoisFailed)
			}
			if tt.name == "LocalAPI error" && !errors.Is(err, boom) {
				t.Errorf("the cause is not wrapped: %v", err)
			}
			if tt.name != "LocalAPI error" && api.whoIsCallCount() != 0 {
				t.Error("LocalAPI must not be called for an unparsable address")
			}
		})
	}
}

func TestWhoIsFailureIsNotCachedAndExpiredEntryIsNotServedStale(t *testing.T) {
	clock := newFakeClock()
	api := newFakeAPI()
	healthy := true
	api.whoIs = func(addr string) (Identity, error) {
		if !healthy {
			return Identity{}, errors.New("down")
		}
		return okWhoIs("alice@example.com")(addr)
	}
	w := NewWhoIs(WhoIsConfig{API: api, Clock: clock})
	ctx := context.Background()

	healthy = false
	if _, err := w.Lookup(ctx, "100.64.0.5"); !errors.Is(err, ErrWhoIsFailed) {
		t.Fatalf("err = %v", err)
	}
	healthy = true
	if _, err := w.Lookup(ctx, "100.64.0.5"); err != nil { // 失敗は覚えていない
		t.Fatalf("err = %v, want success after recovery", err)
	}

	// 期限切れのあとで tailscaled が落ちたら、古い結果を返さず拒否する。
	clock.Advance(MaxWhoIsTTL)
	healthy = false
	if _, err := w.Lookup(ctx, "100.64.0.5"); !errors.Is(err, ErrWhoIsFailed) {
		t.Fatalf("err = %v, want ErrWhoIsFailed (no stale result)", err)
	}
}

func TestWhoIsNormalizesAddress(t *testing.T) {
	api := newFakeAPI()
	api.whoIs = okWhoIs("a@example.com")
	w := NewWhoIs(WhoIsConfig{API: api, Clock: newFakeClock()})
	ctx := context.Background()
	for _, addr := range []string{"100.64.0.5", "100.64.0.5:80", "[::ffff:100.64.0.5]:80", "::ffff:100.64.0.5"} {
		if _, err := w.Lookup(ctx, addr); err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
	}
	if n := api.whoIsCallCount(); n != 1 {
		t.Errorf("LocalAPI calls = %d, want 1 (all forms are the same IP)", n)
	}

	if _, err := w.Lookup(ctx, "[fd7a:115c:a1e0::1%eth0]:80"); err != nil {
		t.Fatal(err)
	}
	if got := api.whoIsCalls[len(api.whoIsCalls)-1]; got != "fd7a:115c:a1e0::1" {
		t.Errorf("IPv6 lookup used %q, want the zone stripped", got)
	}
}

func TestWhoIsTTLIsCapped(t *testing.T) {
	for _, ttl := range []time.Duration{0, -1, time.Hour} {
		clock := newFakeClock()
		api := newFakeAPI()
		api.whoIs = okWhoIs("a@example.com")
		w := NewWhoIs(WhoIsConfig{API: api, Clock: clock, TTL: ttl})
		_, _ = w.Lookup(context.Background(), "100.64.0.5")
		clock.Advance(MaxWhoIsTTL)
		_, _ = w.Lookup(context.Background(), "100.64.0.5")
		if n := api.whoIsCallCount(); n != 2 {
			t.Errorf("ttl=%v: calls = %d, want 2 (capped at %v)", ttl, n, MaxWhoIsTTL)
		}
	}

	clock := newFakeClock()
	api := newFakeAPI()
	api.whoIs = okWhoIs("a@example.com")
	w := NewWhoIs(WhoIsConfig{API: api, Clock: clock, TTL: 2 * time.Second})
	_, _ = w.Lookup(context.Background(), "100.64.0.5")
	clock.Advance(2 * time.Second)
	_, _ = w.Lookup(context.Background(), "100.64.0.5")
	if n := api.whoIsCallCount(); n != 2 {
		t.Errorf("short ttl: calls = %d, want 2", n)
	}
}

func TestWhoIsSweepsExpiredEntries(t *testing.T) {
	clock := newFakeClock()
	api := newFakeAPI()
	api.whoIs = okWhoIs("a@example.com")
	c := NewWhoIs(WhoIsConfig{API: api, Clock: clock}).(*whoIsCache)
	ctx := context.Background()
	for _, ip := range []string{"100.64.0.1", "100.64.0.2", "100.64.0.3"} {
		_, _ = c.Lookup(ctx, ip)
	}
	clock.Advance(MaxWhoIsTTL)
	_, _ = c.Lookup(ctx, "100.64.0.9")
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("cache entries = %d, want 1 (expired ones swept)", n)
	}
}
