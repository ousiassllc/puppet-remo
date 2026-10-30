package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// maintClock は After の待ちをテストが 1 回ずつ解く tailnet.Clock。
type maintClock struct {
	*fakeClock
	waits chan time.Duration // After が呼ばれるたびに待ちの長さを送る
	fire  chan time.Time     // 待ちを解く
}

func newMaintClock() *maintClock {
	return &maintClock{fakeClock: newFakeClock(), waits: make(chan time.Duration, 8), fire: make(chan time.Time)}
}

func (c *maintClock) After(d time.Duration) <-chan time.Time {
	c.waits <- d
	return c.fire
}

// expireRecorder は Expire の呼び出しだけを記録する repository.ConnRequests。
type expireRecorder struct {
	repository.ConnRequests
	mu    sync.Mutex
	times []time.Time
	err   error
	calls chan struct{}
}

func (r *expireRecorder) Expire(_ context.Context, now time.Time) error {
	r.mu.Lock()
	r.times = append(r.times, now)
	err := r.err
	r.mu.Unlock()
	r.calls <- struct{}{}
	return err
}

func recvOrFail[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func TestMaintenanceRequiresConnRequests(t *testing.T) {
	if _, err := NewMaintenance(MaintenanceConfig{}); err == nil {
		t.Error("NewMaintenance without ConnRequests must fail")
	}
}

func TestMaintenanceRunExpiresAtStartAndEveryInterval(t *testing.T) {
	clock := newMaintClock()
	rec := &expireRecorder{calls: make(chan struct{}, 8)}
	m, err := NewMaintenance(MaintenanceConfig{ConnRequests: rec, Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()

	recvOrFail(t, rec.calls, "the startup expiry")
	if d := recvOrFail(t, clock.waits, "the first wait"); d != DefaultMaintenanceInterval {
		t.Errorf("wait = %v, want %v", d, DefaultMaintenanceInterval)
	}

	clock.Advance(time.Hour)
	clock.fire <- clock.Now()
	recvOrFail(t, rec.calls, "the hourly expiry")
	recvOrFail(t, clock.waits, "the second wait")

	cancel()
	recvOrFail(t, done, "Run to return after cancellation")

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.times) != 2 || !rec.times[1].Equal(rec.times[0].Add(time.Hour)) {
		t.Errorf("Expire times = %v, want the clock's time at start and one hour later", rec.times)
	}
}

func TestMaintenanceRunKeepsGoingAfterAFailure(t *testing.T) {
	clock := newMaintClock()
	rec := &expireRecorder{calls: make(chan struct{}, 8), err: errors.New("database is locked")}
	m, err := NewMaintenance(MaintenanceConfig{ConnRequests: rec, Clock: clock, Interval: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()

	recvOrFail(t, rec.calls, "the first expiry")
	if d := recvOrFail(t, clock.waits, "the wait after a failure"); d != time.Minute {
		t.Errorf("wait = %v, want the configured 1m interval", d)
	}
	clock.fire <- clock.Now()
	recvOrFail(t, rec.calls, "the retry")

	cancel()
	recvOrFail(t, done, "Run to return")
}
