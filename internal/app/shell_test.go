package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

type fakeWindow struct {
	mu    sync.Mutex
	calls []string
}

func (w *fakeWindow) Show(path string) { w.add("show " + path) }
func (w *fakeWindow) Hide()            { w.add("hide") }

func (w *fakeWindow) add(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, s)
}

func (w *fakeWindow) Calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

type fakeTray struct {
	states chan TrayState
}

func newFakeTray() *fakeTray { return &fakeTray{states: make(chan TrayState, 32)} }

func (t *fakeTray) Apply(s TrayState) { t.states <- s }

type fakeShellApp struct {
	bus      *service.Bus
	active   bool
	triggers []string
	trigErr  error
	pending  int
	countErr error
	mu       sync.Mutex
}

func (a *fakeShellApp) KillSwitch() KillSwitchControl { return a }
func (a *fakeShellApp) Bus() *service.Bus             { return a.bus }

func (a *fakeShellApp) Active() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

func (a *fakeShellApp) Trigger(source string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.triggers = append(a.triggers, source)
	if a.trigErr == nil {
		a.active = true
	}
	return a.trigErr
}

func (a *fakeShellApp) PendingRequests(context.Context) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending, a.countErr
}

func (a *fakeShellApp) set(f func(*fakeShellApp)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a)
}

type shellKit struct {
	app   *fakeShellApp
	win   *fakeWindow
	tray  *fakeTray
	shell *Shell
	quit  chan struct{}
}

func newShellKit() *shellKit {
	k := &shellKit{app: &fakeShellApp{bus: service.NewBus()}, win: &fakeWindow{}, tray: newFakeTray(), quit: make(chan struct{}, 1)}
	k.shell = NewShell(k.app, k.win, k.tray, func() { k.quit <- struct{}{} }, discardLogger())
	return k
}

func recvState(t *testing.T, tray *fakeTray) TrayState {
	t.Helper()
	select {
	case s := <-tray.states:
		return s
	case <-time.After(waitLimit):
		t.Fatal("no tray update")
		return TrayState{}
	}
}

// FR-H01: ウィンドウを閉じても終了せず、トレイへ隠れる。
func TestClosingTheWindowHidesItAndCancelsTheClose(t *testing.T) {
	k := newShellKit()
	cancelled := 0
	k.shell.WindowClosing(func() { cancelled++ })

	if got := k.win.Calls(); len(got) != 1 || got[0] != "hide" {
		t.Errorf("window calls = %v, want [hide]", got)
	}
	if cancelled != 1 {
		t.Error("the close was not cancelled; the app would quit")
	}
	select {
	case <-k.quit:
		t.Error("closing the window quit the app")
	default:
	}
}

func TestQuitFromTheTrayQuitsAndNoLongerCancelsClose(t *testing.T) {
	k := newShellKit()
	k.shell.Quit()
	select {
	case <-k.quit:
	default:
		t.Fatal("Quit did not quit the app")
	}
	if !k.shell.Quitting() {
		t.Error("Quitting() = false after Quit")
	}

	// 終了処理が各ウィンドウを閉じるときに、取り消して終了を妨げない。
	cancelled := false
	k.shell.WindowClosing(func() { cancelled = true })
	if cancelled || len(k.win.Calls()) != 0 {
		t.Error("a close during shutdown must pass through")
	}
}

func TestTrayAndSecondInstanceOpenTheWindow(t *testing.T) {
	k := newShellKit()
	k.shell.OpenDashboard()
	k.shell.OpenRequests()
	k.shell.SecondInstance()
	want := []string{"show /", "show /requests", "show "}
	got := k.win.Calls()
	if len(got) != len(want) {
		t.Fatalf("window calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// FR-H06: トレイメニューからのキルスイッチ。
func TestTrayKillSwitchTriggersWithTheTraySource(t *testing.T) {
	k := newShellKit()
	k.shell.TriggerKillSwitch()
	k.app.mu.Lock()
	got := append([]string(nil), k.app.triggers...)
	k.app.mu.Unlock()
	if len(got) != 1 || got[0] != "tray" {
		t.Errorf("triggers = %v, want [tray]", got)
	}
}

func TestTrayKillSwitchFailureIsLoggedNotFatal(t *testing.T) {
	k := newShellKit()
	k.app.set(func(a *fakeShellApp) { a.trigErr = errors.New("closers timed out") })
	k.shell.TriggerKillSwitch() // パニックしない
}

func TestTrayStateReflectsKillSwitchAndPendingRequests(t *testing.T) {
	k := newShellKit()
	st := k.shell.State(t.Context())
	if !st.KillSwitchEnabled || st.RequestsVisible || st.Tooltip != "puppet-remo" {
		t.Errorf("idle state = %+v", st)
	}

	k.app.set(func(a *fakeShellApp) { a.pending = 3 })
	st = k.shell.State(t.Context())
	if !st.RequestsVisible || st.RequestsLabel != "接続要求 3 件を確認" || st.Tooltip != "puppet-remo（接続要求 3 件）" {
		t.Errorf("state with requests = %+v", st)
	}

	k.app.set(func(a *fakeShellApp) { a.active = true })
	st = k.shell.State(t.Context())
	if st.KillSwitchEnabled {
		t.Error("the kill switch item must be disabled while active (release is on the dashboard)")
	}
	if st.Tooltip != "puppet-remo（キルスイッチ発動中）（接続要求 3 件）" {
		t.Errorf("tooltip = %q", st.Tooltip)
	}

	k.app.set(func(a *fakeShellApp) { a.countErr = errors.New("db locked") })
	if st := k.shell.State(t.Context()); st.RequestsVisible {
		t.Errorf("a failed count must not show requests: %+v", st)
	}
}

func TestWatchRefreshesTheTrayOnBusEvents(t *testing.T) {
	k := newShellKit()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); k.shell.Watch(ctx) }()

	if st := recvState(t, k.tray); st.RequestsVisible || !st.KillSwitchEnabled {
		t.Fatalf("initial state = %+v", st)
	}

	k.app.set(func(a *fakeShellApp) { a.pending = 2 })
	k.app.bus.Publish(service.Event{Topic: service.TopicConnectionRequests})
	if st := recvState(t, k.tray); !st.RequestsVisible || st.RequestsLabel != "接続要求 2 件を確認" {
		t.Fatalf("after a connection request = %+v", st)
	}

	k.app.set(func(a *fakeShellApp) { a.active = true })
	k.app.bus.Publish(service.Event{Topic: service.TopicKillSwitch})
	if st := recvState(t, k.tray); st.KillSwitchEnabled {
		t.Fatalf("after the kill switch = %+v", st)
	}

	// 関係のない通知では更新しない。
	k.app.bus.Publish(service.Event{Topic: service.TopicSessions})
	select {
	case st := <-k.tray.states:
		t.Errorf("unrelated event refreshed the tray: %+v", st)
	default:
	}

	cancel()
	select {
	case <-done:
	case <-time.After(waitLimit):
		t.Fatal("Watch did not return after cancellation")
	}
}

// App と Shell をつなぐと、ホットキー・トレイのどちらから発動しても同じ状態になる（実 App・偽の窓）。
func TestShellWithARealAppSharesTheKillSwitchPath(t *testing.T) {
	e := newEnv(t)
	e.start()
	win, tray := &fakeWindow{}, newFakeTray()
	shell := NewShell(e.app, win, tray, func() {}, discardLogger())

	shell.TriggerKillSwitch()
	if !e.app.KillSwitch().Active() {
		t.Fatal("the tray kill switch did not activate the real kill switch")
	}
	if st := shell.State(t.Context()); st.KillSwitchEnabled {
		t.Errorf("tray state after the kill switch = %+v", st)
	}
	select {
	case st := <-tray.states:
		if st.KillSwitchEnabled {
			t.Errorf("refresh after trigger = %+v", st)
		}
	default:
		t.Error("TriggerKillSwitch did not refresh the tray")
	}
}
