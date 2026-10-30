package service

import (
	"testing"
	"time"
)

func TestWindowLimiter(t *testing.T) {
	clk := newFakeClock()
	l := newWindowLimiter(time.Minute, 2)
	step := func(key string, consume, wantAllowed, wantFirst bool, wantRetry time.Duration) {
		t.Helper()
		ok, first, retry := l.check(key, clk.Now(), consume)
		if ok != wantAllowed || first != wantFirst || retry != wantRetry {
			t.Fatalf("check(%q) = %v, %v, %v; want %v, %v, %v", key, ok, first, retry, wantAllowed, wantFirst, wantRetry)
		}
	}
	step("a", false, true, false, 0) // 調べるだけでは数えない
	step("a", false, true, false, 0)
	step("a", true, true, false, 0)
	clk.Advance(10 * time.Second)
	step("a", true, true, false, 0)
	step("a", true, false, true, 50*time.Second)
	step("a", false, false, false, 50*time.Second) // 超過の通知は窓ごとに 1 回
	step("b", true, true, false, 0)                // キーは独立
	clk.Advance(50 * time.Second)                  // 窓の終わり
	step("a", true, true, false, 0)
	step("a", true, true, false, 0)
	step("a", true, false, true, time.Minute)
}

func TestWindowLimiterSweepsExpiredKeys(t *testing.T) {
	clk := newFakeClock()
	l := newWindowLimiter(time.Minute, 1)
	for _, k := range []string{"a", "b", "c"} {
		l.check(k, clk.Now(), true)
	}
	clk.Advance(2 * time.Minute)
	l.check("d", clk.Now(), true)
	if len(l.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(l.entries))
	}
}
