package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

func pausedOps() map[string]domain.Op {
	pt := domain.Point{X: 100, Y: 100}
	return map[string]domain.Op{
		"mouse_move": domain.MouseMove{At: pt, Monitor: domain.MonitorPrimary},
		"click":      domain.Click{At: &pt, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary},
		"drag":       domain.Drag{From: pt, To: domain.Point{X: 200, Y: 200}, Monitor: domain.MonitorPrimary},
		"scroll":     domain.Scroll{At: pt, Dir: domain.DirDown, Amount: 1, Monitor: domain.MonitorPrimary},
		"key":        domain.KeyPress{Keys: domain.KeyCombo{Key: "enter"}},
		"type":       domain.TypeText{Text: "x"},
		"focus":      domain.FocusWindow{ID: "1"},
		"pointer":    domain.PointerMove{At: pt, Monitor: domain.MonitorPrimary},
		"button":     domain.MouseButton{At: pt, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: domain.MonitorPrimary},
		"wheel":      domain.Wheel{At: pt, DY: 1, Monitor: domain.MonitorPrimary},
		"key_event":  domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a"},
	}
}

func hostPauseActors() map[string]domain.Actor {
	return map[string]domain.Actor{"mcp": ctlAlice, "web": ctlViewer, "agent": ctlAgent}
}

// allowJudge は agent のアクションをすべて許可する Judge（このテストの対象は Judge ではなく一時停止）。
type allowJudge struct{}

func (allowJudge) Assess(context.Context, domain.Action, domain.WindowInfo) (domain.Verdict, Reason, error) {
	return domain.VerdictAllow, Reason{}, nil
}

// injectedCalls は実機のカーソル・キー・前面に触れた呼び出しの数（FocusWindow は WindowManager.Focus）。
func injectedCalls(e *ctlEnv) int {
	n := len(e.windows.CallsTo("Focus"))
	for _, m := range []string{"MoveTo", "Click", "Button", "Drag", "Scroll", "Wheel", "Key", "KeyEvent", "Text"} {
		n += len(e.input.CallsTo(m))
	}
	return n
}

// newHostPauseEnv は who がリースを保持した Control を返す。agent は Judge が必要なので、許可する Judge で組み直す。
func newHostPauseEnv(t *testing.T, who domain.Actor) *ctlEnv {
	t.Helper()
	e := newCtlEnv(t)
	mustAcquire(t, e, who)
	if who.Kind == domain.ActorAgent {
		var err error
		e.control, err = NewControl(ControlConfig{
			Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
			Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, HostPause: e.hostPause,
			Settings: e.settings, Audit: e.audit, Clock: e.clock, Judge: allowJudge{},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// 実入力で停止すると、どの経路（mcp・web・agent）の SendInput 系の操作も ErrInputPaused で拒否され、何も注入されない。
// 自分の注入（Control が行う入力）では停止しないので、停止の前は通る。
func TestControlRejectsInputWhileHostIsActive(t *testing.T) {
	for actorName, who := range hostPauseActors() {
		for opName, op := range pausedOps() {
			t.Run(actorName+"/"+opName, func(t *testing.T) {
				e := newHostPauseEnv(t, who)
				// 停止の前は通る（自分の注入では停止しない。NoteHostInput は実入力でしか呼ばれない）。
				if _, err := e.control.Do(context.Background(), who, op); err != nil {
					t.Fatalf("before the host input: %v", err)
				}
				if injectedCalls(e) == 0 {
					t.Fatal("the op injected nothing")
				}
				e.input.Reset()
				e.windows.Reset()

				e.hostPause.NoteHostInput(platform.HostInputMouse)
				_, err := e.control.Do(context.Background(), who, op)
				if !errors.Is(err, domain.ErrInputPaused) {
					t.Fatalf("Do while paused = %v, want input_paused", err)
				}
				if n := injectedCalls(e); n != 0 {
					t.Errorf("%d injected calls while paused: %+v", n, e.input.Calls())
				}

				e.clock.Advance(5 * time.Second)
				if _, err := e.control.Do(context.Background(), who, op); err != nil {
					t.Errorf("Do after the idle time = %v, want it to work again", err)
				}
			})
		}
	}
}

// 停止中も、読み取り・待ち・クリップボードの書き込み（カーソル・フォーカスを動かさない）は通る。
func TestControlDoesNotPauseReadsWaitAndClipboard(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	e.hostPause.NoteHostInput(platform.HostInputKeyboard)
	for name, op := range map[string]domain.Op{
		"wait":            domain.Wait{Duration: time.Millisecond},
		"clipboard read":  domain.ClipboardRead{},
		"clipboard write": domain.ClipboardWrite{Text: "x"},
		"ui tree":         domain.GetUITree{},
	} {
		if _, err := e.control.Do(context.Background(), ctlAlice, op); err != nil {
			t.Errorf("%s while paused: %v", name, err)
		}
	}
	if _, err := e.control.Screenshot(context.Background(), domain.MonitorPrimary, "png"); err != nil {
		t.Errorf("Screenshot while paused: %v", err)
	}
	if _, err := e.control.CursorPosition(context.Background()); err != nil {
		t.Errorf("CursorPosition while paused: %v", err)
	}
	if _, err := e.control.Windows(context.Background()); err != nil {
		t.Errorf("Windows while paused: %v", err)
	}
}

// 停止の拒否はリースの取得・承認より前。リースを持たない mcp が暗黙に取得して保持してしまわない。
func TestControlPausedInputDoesNotAcquireTheLease(t *testing.T) {
	e := newCtlEnv(t)
	e.hostPause.NoteHostInput(platform.HostInputMouse)
	_, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "x"})
	if !errors.Is(err, domain.ErrInputPaused) {
		t.Fatalf("Do = %v, want input_paused", err)
	}
	if _, held := e.leases.Holder(); held {
		t.Error("a paused request acquired the lease")
	}
}

// キルスイッチは停止と独立に効く（キルスイッチが先に断る）。
func TestControlKillSwitchStillWinsWhilePaused(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	e.hostPause.NoteHostInput(platform.HostInputMouse)
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	_, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "x"})
	if !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("Do = %v, want kill_switch_active", err)
	}
}

// 拒否された要求は、通常の監査（tool.<名前>、result = denied、error_code = input_paused）に残る。
func TestControlPausedRejectionIsAudited(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	e.hostPause.NoteHostInput(platform.HostInputMouse)
	e.audit.reset()
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "secret"}); !errors.Is(err, domain.ErrInputPaused) {
		t.Fatal(err)
	}
	entries := e.audit.Entries()
	if len(entries) != 1 || entries[0].Event != "tool.type" || entries[0].Result != auditDenied || entries[0].ErrorCode != string(domain.CodeInputPaused) {
		t.Fatalf("audit = %+v, want one denied tool.type with input_paused", entries)
	}
}

// 停止に入ったときの onPause（本番では押下中のキー・ボタンを離す）が、Control の ReleaseInput に結べる。
func TestHostPauseOnPauseCanReleaseHeldInput(t *testing.T) {
	e := newCtlEnv(t)
	e.hostPause.SetOnPause(func() { _ = e.control.ReleaseInput() })
	e.hostPause.NoteHostInput(platform.HostInputMouse)
	if got := len(e.input.CallsTo("ReleaseAll")); got != 1 {
		t.Errorf("ReleaseAll calls = %d, want 1", got)
	}
}

func TestOpPausedByHostInputHelper(t *testing.T) {
	for name, op := range pausedOps() {
		if !domain.OpPausedByHostInput(op) {
			t.Errorf("%s is not paused", name)
		}
	}
	for name, op := range map[string]domain.Op{
		"wait": domain.Wait{}, "clipboard read": domain.ClipboardRead{}, "clipboard write": domain.ClipboardWrite{}, "ui tree": domain.GetUITree{},
	} {
		if domain.OpPausedByHostInput(op) {
			t.Errorf("%s is paused", name)
		}
	}
	// 新しい Op（UI Automation のパターン操作など）は、HostInputGated で選べる。既定は止めない。
	if domain.OpPausedByHostInput(plainOp{}) || !domain.OpPausedByHostInput(gatedOp{}) {
		t.Error("HostInputGated is not honored")
	}
}

type plainOp struct{ domain.Wait }

type gatedOp struct{ domain.Wait }

func (gatedOp) PausedByHostInput() bool { return true }
