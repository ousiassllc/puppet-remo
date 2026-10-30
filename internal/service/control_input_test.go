package service

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// gatedInput は最初の MoveTo を gate が閉じるまで止める InputInjector。MoveTo と Click の呼び出し順を記録する。
type gatedInput struct {
	*platformtest.FakeInput
	entered chan struct{} // 最初の MoveTo に入ったら閉じる
	gate    chan struct{}

	once sync.Once
	mu   sync.Mutex
	log  []string
}

func (g *gatedInput) record(s string) {
	g.mu.Lock()
	g.log = append(g.log, s)
	g.mu.Unlock()
}

func (g *gatedInput) MoveTo(ctx context.Context, p domain.Point) error {
	g.record("MoveTo")
	g.once.Do(func() { close(g.entered); <-g.gate })
	return g.FakeInput.MoveTo(ctx, p)
}

func (g *gatedInput) Click(ctx context.Context, b domain.Button, n int, mods []domain.Modifier) error {
	g.record("Click")
	return g.FakeInput.Click(ctx, b, n, mods)
}

func (g *gatedInput) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.log...)
}

// 同じセッションからの並行した Click は、それぞれ「移動 → クリック」を 1 組として順に実行される。
// 1 つ目が移動で止まっている間、2 つ目の移動は始まらない。
func TestControlDoSerializesClickMoveAndClick(t *testing.T) {
	e := newCtlEnv(t)
	in := &gatedInput{FakeInput: e.input, entered: make(chan struct{}), gate: make(chan struct{})}
	ctl, err := NewControl(ControlConfig{
		Capturer: e.capturer, Input: in, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	click := func(x int) {
		_, err := ctl.Do(context.Background(), ctlAlice, domain.Click{
			At: &domain.Point{X: x, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 0,
		})
		if err != nil {
			t.Errorf("Click: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); click(100) }()
	<-in.entered // 1 つ目は移動で止まっている
	listed := len(e.windows.CallsTo("At"))
	go func() { defer wg.Done(); click(200) }()
	// 2 つ目が InputGuard を通り（At を呼び）、実行へ進む機会を十分に与える。
	for len(e.windows.CallsTo("At")) == listed {
		runtime.Gosched()
	}
	for range 10_000 {
		runtime.Gosched()
	}
	if got := in.calls(); len(got) != 1 {
		t.Fatalf("while the first click is moving, calls = %v; the second must wait", got)
	}

	close(in.gate)
	wg.Wait()
	want := []string{"MoveTo", "Click", "MoveTo", "Click"}
	got := in.calls()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	}
}

// 実行中の入力があっても、キルスイッチは入力の解放を待たされない（ReleaseAll は入力の直列化を取らない）。
func TestKillSwitchReleasesInputWhileAnInputIsRunning(t *testing.T) {
	e := newCtlEnv(t)
	in := &gatedInput{FakeInput: e.input, entered: make(chan struct{}), gate: make(chan struct{})}
	ctl, err := NewControl(ControlConfig{
		Capturer: e.capturer, Input: in, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ctl.Do(context.Background(), ctlAlice, domain.Click{
			At: &domain.Point{X: 100, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 0,
		})
	}()
	<-in.entered
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if len(e.input.CallsTo("ReleaseAll")) == 0 {
		t.Error("the kill switch did not release the input while a click was running")
	}
	close(in.gate)
	<-done
}
