package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func TestKillSwitchTriggerRunsStepsInOrder(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)

	var (
		mu    sync.Mutex
		steps []string
	)
	e.kill.RegisterCloser("sessions", func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		// 発動状態は永続化済みで、リースは剥奪済み（NFR-S07: 永続化が先頭）。
		raw, ok, _ := e.settings.Get(context.Background(), "killswitch.state")
		if !ok || !strings.Contains(string(raw), `"active":true`) {
			t.Errorf("state must be persisted before closers run, got %s", raw)
		}
		if _, held := e.leases.Holder(); held {
			t.Error("leases must be released before closers run")
		}
		steps = append(steps, "sessions")
		return nil
	})

	if err := e.kill.Trigger("hotkey"); err != nil {
		t.Fatal(err)
	}
	if !e.kill.Active() {
		t.Fatal("kill switch should be active")
	}
	if st := e.kill.State(); !st.Active || st.Source != "hotkey" || !st.TriggeredAt.Equal(e.clock.Now()) {
		t.Fatalf("state = %+v", st)
	}
	if len(steps) != 1 {
		t.Fatalf("closer calls = %v", steps)
	}
	// Control が登録した「押下中の入力の解放」も走る。
	if len(e.input.CallsTo("ReleaseAll")) != 1 {
		t.Fatalf("input ReleaseAll calls = %d", len(e.input.CallsTo("ReleaseAll")))
	}
	// リースは kill_switch で解放され、監査に残る。
	var released, trigger bool
	for _, en := range e.audit.Entries() {
		switch en.Event {
		case "lease.release":
			released = strings.Contains(string(en.DetailJSON), `"kill_switch"`)
		case "killswitch.trigger":
			trigger = en.Kind == "killswitch" && en.ActorType == domain.ActorHost && en.Result == "ok" &&
				strings.Contains(string(en.DetailJSON), `"hotkey"`)
		}
	}
	if !released || !trigger {
		t.Fatalf("audit events = %v", e.audit.Events())
	}
}

func TestKillSwitchBlocksEverythingUntilReleased(t *testing.T) {
	e := newCtlEnv(t)
	ctx := context.Background()
	mustAcquire(t, e, ctlAlice)
	if err := e.kill.Trigger("tray"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.leases.Acquire(ctx, ctlAlice); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := e.control.Do(ctx, ctlAlice, domain.TypeText{Text: "x"}); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("Do: %v", err)
	}
	// 2 回目の発動は冪等。発動時刻と発動元は最初のまま。
	first := e.kill.State()
	e.clock.Advance(time.Minute)
	if err := e.kill.Trigger("dashboard"); err != nil {
		t.Fatal(err)
	}
	if st := e.kill.State(); st != first {
		t.Fatalf("state changed on re-trigger: %+v -> %+v", first, st)
	}

	if err := e.kill.Release(); err != nil {
		t.Fatal(err)
	}
	if e.kill.Active() {
		t.Fatal("still active after Release")
	}
	if _, err := e.control.Do(ctx, ctlAlice, domain.TypeText{Text: "x"}); err != nil {
		t.Fatalf("Do after release: %v", err)
	}
	if len(e.input.CallsTo("Text")) != 1 {
		t.Fatal("input should work again after release")
	}
	// 解除済みの Release は何もしない。
	if err := e.kill.Release(); err != nil {
		t.Fatal(err)
	}
	if got := countEvent(e.audit.Events(), "killswitch.release"); got != 1 {
		t.Fatalf("killswitch.release entries = %d, want 1", got)
	}
}

func TestKillSwitchPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	clock := newCtlClock()

	open := func() (*repository.DB, *KillSwitchManager) {
		t.Helper()
		db, err := repository.Open(ctx, path, repository.WithClock(clock))
		if err != nil {
			t.Fatal(err)
		}
		k, err := NewKillSwitch(ctx, KillSwitchConfig{Settings: db.Settings(), Clock: clock})
		if err != nil {
			t.Fatal(err)
		}
		return db, k
	}

	db, k := open()
	if k.Active() {
		t.Fatal("fresh database must not start with the kill switch active")
	}
	leases := mustLeaseManager(t, clock, k)
	k.BindLeases(leases)
	if err := k.Trigger("hotkey"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 再起動: 停止状態を維持し、リースは取れない。
	db, k = open()
	if !k.Active() {
		t.Fatal("kill switch must stay active after restart (NFR-S07)")
	}
	if st := k.State(); st.Source != "hotkey" || !st.TriggeredAt.Equal(clock.Now()) {
		t.Fatalf("restored state = %+v", st)
	}
	leases = mustLeaseManager(t, clock, k)
	if _, err := leases.Acquire(ctx, ctlAlice); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("Acquire after restart: %v", err)
	}
	if err := k.Release(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 解除も永続化される。
	db, k = open()
	defer func() { _ = db.Close() }()
	if k.Active() {
		t.Fatal("kill switch must stay released after restart")
	}
}

func mustLeaseManager(t *testing.T, clock domain.Clock, kill KillSwitchState) LeaseManager {
	t.Helper()
	l, err := NewLeaseManager(LeaseConfig{Clock: clock, IDs: &ctlIDs{}, IdleTimeout: time.Minute, Kill: kill, AfterFunc: (&ctlTimers{}).AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestKillSwitchTriggerFailuresDoNotStopLaterSteps(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	boom := errors.New("persist failed")
	e.settings.setErr = boom
	closed := false
	e.kill.RegisterCloser("sessions", func(context.Context) error { closed = true; return errors.New("close failed") })

	err := e.kill.Trigger("hotkey")
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("want joined errors, got %v", err)
	}
	// 永続化に失敗しても、メモリ上は発動し、リース剥奪とセッション切断は実行される。
	if !e.kill.Active() || !closed {
		t.Fatalf("active=%v closed=%v", e.kill.Active(), closed)
	}
	if _, held := e.leases.Holder(); held {
		t.Fatal("lease must be released even if persisting failed")
	}
	entries := e.audit.Entries()
	if last := entries[len(entries)-1]; last.Event != "killswitch.trigger" || last.Result != "error" {
		t.Fatalf("audit = %+v", last)
	}
}

func TestKillSwitchTriggerWithoutBoundLeasesStillActivates(t *testing.T) {
	settings := newCtlSettings()
	k, err := NewKillSwitch(context.Background(), KillSwitchConfig{Settings: settings, Clock: newCtlClock()})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Trigger("test"); err == nil {
		t.Fatal("want an error reporting the missing lease manager")
	}
	if !k.Active() {
		t.Fatal("kill switch must be active regardless")
	}
}

func TestKillSwitchReleaseFailureKeepsItActive(t *testing.T) {
	e := newCtlEnv(t)
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	e.settings.setErr = errors.New("disk full")
	if err := e.kill.Release(); err == nil {
		t.Fatal("want error")
	}
	if !e.kill.Active() {
		t.Fatal("a failed Release must leave the kill switch active")
	}
}

func TestKillSwitchSlowCloserDoesNotExceedDeadline(t *testing.T) {
	settings := newCtlSettings()
	k, err := NewKillSwitch(context.Background(), KillSwitchConfig{Settings: settings, Clock: newCtlClock(), Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	k.BindLeases(mustLeaseManager(t, newCtlClock(), k))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	k.RegisterCloser("stuck", func(context.Context) error { <-release; return nil })

	start := time.Now()
	err = k.Trigger("test")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Trigger took %s, want within 1s (NFR-P05)", elapsed)
	}
	if !k.Active() {
		t.Fatal("must be active")
	}
}

func TestKillSwitchRejectsCorruptState(t *testing.T) {
	s := newCtlSettings()
	s.put("killswitch.state", `"yes"`)
	if _, err := NewKillSwitch(context.Background(), KillSwitchConfig{Settings: s, Clock: newCtlClock()}); err == nil {
		t.Fatal("corrupt state must not silently read as released")
	}
}

func TestKillSwitchStateJSONShape(t *testing.T) {
	e := newCtlEnv(t)
	if err := e.kill.Trigger("hotkey"); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := e.settings.Get(context.Background(), "killswitch.state")
	var got struct {
		Active      bool   `json:"active"`
		TriggeredAt string `json:"triggered_at"`
		Source      string `json:"source"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.Source != "hotkey" || got.TriggeredAt != "2026-10-30T09:00:00Z" {
		t.Fatalf("persisted = %s", raw)
	}
	if err := e.kill.Release(); err != nil {
		t.Fatal(err)
	}
	raw, _, _ = e.settings.Get(context.Background(), "killswitch.state")
	if string(raw) != `{"active":false}` {
		t.Fatalf("released = %s", raw)
	}
}

// 発動と取得が競合しても、Trigger が返った後にリースが残らず、以後の取得は断られる。
func TestKillSwitchRacesWithAcquire(t *testing.T) {
	e := newCtlEnv(t)
	const workers = 8
	var wg sync.WaitGroup
	started := make(chan struct{}, workers)
	for i := range workers {
		who := domain.Actor{Kind: domain.ActorMCP, ClientID: "c", SessionID: string(rune('a' + i))}
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			for {
				_, err := e.leases.Acquire(context.Background(), who)
				if errors.Is(err, domain.ErrKillSwitchActive) {
					return
				}
				e.leases.Release(who)
			}
		}()
	}
	for range workers {
		<-started
	}
	if err := e.kill.Trigger("race"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, held := e.leases.Holder(); held {
		t.Fatal("no lease may remain after the kill switch")
	}
}

// slowSettings は Set が ctx の期限まで返らない設定ストア（遅いディスク）。
type slowSettings struct{ *ctlSettings }

func (s slowSettings) Set(ctx context.Context, _ string, _ json.RawMessage) error {
	<-ctx.Done()
	return ctx.Err()
}

// 状態の永続化が遅くても、クローザーは自分の期限を持って走る（期限切れの ctx で呼ばれない）。
func TestKillSwitchSlowPersistDoesNotStarveClosers(t *testing.T) {
	k, err := NewKillSwitch(context.Background(), KillSwitchConfig{
		Settings: slowSettings{newCtlSettings()}, Clock: newCtlClock(), Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	k.BindLeases(mustLeaseManager(t, newCtlClock(), k))
	var ctxErr error
	ran := false
	k.RegisterCloser("sessions", func(ctx context.Context) error { ran, ctxErr = true, ctx.Err(); return nil })

	start := time.Now()
	err = k.Trigger("test")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Trigger = %v, want the persist deadline error", err)
	}
	if !ran || ctxErr != nil {
		t.Fatalf("closer ran=%v, ctx error at start=%v; want it to run with a live context", ran, ctxErr)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Trigger took %s, want within 1s (NFR-P05)", took)
	}
	if !k.Active() {
		t.Fatal("must be active")
	}
}
