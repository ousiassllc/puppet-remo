package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func newWebEnv(t *testing.T) *ctlEnv {
	t.Helper()
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlViewer)
	return e
}

func TestControlDoWebInputReachesInjector(t *testing.T) {
	pt := func(x, y int) domain.Point { return domain.Point{X: x, Y: y} }
	tests := []struct {
		name   string
		op     domain.Op
		method string
		args   []any
	}{
		// ストリームの座標は縮小されず、そのまま物理座標になる（モニターの原点 0,0）。
		{name: "pointer move", op: domain.PointerMove{At: pt(500, 100), Monitor: domain.MonitorPrimary}, method: "MoveTo", args: []any{pt(500, 100)}},
		{name: "wheel", op: domain.Wheel{At: pt(500, 100), DX: 0.5, DY: -1.5, Monitor: 0}, method: "Wheel", args: []any{pt(500, 100), 0.5, -1.5}},
		{name: "key down", op: domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a", Mods: []domain.Modifier{domain.ModCtrl}},
			method: "KeyEvent", args: []any{domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a", Mods: []domain.Modifier{domain.ModCtrl}}}},
		{name: "shortcut menu", op: domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl}, Key: "w"}},
			method: "Key", args: []any{domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl}, Key: "w"}}},
		{name: "ime text", op: domain.TypeText{Text: "こんにちは"}, method: "Text", args: []any{"こんにちは"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWebEnv(t)
			if _, err := e.control.Do(context.Background(), ctlViewer, tt.op); err != nil {
				t.Fatal(err)
			}
			calls := e.input.CallsTo(tt.method)
			if len(calls) != 1 || !argsEqual(calls[0].Args, tt.args) {
				t.Fatalf("calls to %s = %+v (all: %+v)", tt.method, calls, inputCalls(e))
			}
		})
	}
}

func TestControlDoWebMouseButtonMovesThenPresses(t *testing.T) {
	e := newWebEnv(t)
	at := domain.Point{X: 500, Y: 100}
	for _, a := range []domain.ButtonAction{domain.ButtonActionDown, domain.ButtonActionUp} {
		op := domain.MouseButton{At: at, Button: domain.ButtonRight, Action: a, Monitor: domain.MonitorPrimary}
		if _, err := e.control.Do(context.Background(), ctlViewer, op); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, c := range inputCalls(e) {
		got = append(got, c.Method)
	}
	want := []string{"MoveTo", "Button", "MoveTo", "Button"}
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	}
	buttons := e.input.CallsTo("Button")
	if !argsEqual(buttons[0].Args, []any{domain.ButtonRight, domain.ButtonActionDown}) || !argsEqual(buttons[1].Args, []any{domain.ButtonRight, domain.ButtonActionUp}) {
		t.Fatalf("button calls = %+v", buttons)
	}
	if len(e.input.HeldButtons()) != 0 {
		t.Fatalf("held = %v", e.input.HeldButtons())
	}
}

// 2 台目のモニターのストリームの座標は、そのモニターの原点からの物理ピクセル（縮小しない）。
func TestControlDoWebStreamCoordinatesUseMonitorOrigin(t *testing.T) {
	e := newWebEnv(t)
	second := domain.Monitor{Index: 1, Name: "m1", Origin: domain.Point{X: 3200, Y: 0}, Size: domain.Size{W: 5000, H: 500}, DPI: 96}
	e.capturer = platformtest.NewFakeCapturer(ctlMonitor, second)
	var err error
	e.control, err = NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 幅 5000 は最大辺 1568 を超えるが、ストリームは縮小しない。
	if _, err := e.control.Do(context.Background(), ctlViewer, domain.PointerMove{At: domain.Point{X: 4999, Y: 20}, Monitor: 1}); err != nil {
		t.Fatal(err)
	}
	got := e.input.CallsTo("MoveTo")
	if len(got) != 1 || got[0].Args[0] != (domain.Point{X: 8199, Y: 20}) {
		t.Fatalf("calls = %+v", got)
	}
}

func TestControlDoWebRejections(t *testing.T) {
	pt := func(x, y int) domain.Point { return domain.Point{X: x, Y: y} }
	tests := []struct {
		name    string
		op      domain.Op
		wantErr error
	}{
		{name: "ストリームの外（右端）", op: domain.PointerMove{At: pt(3200, 0), Monitor: 0}, wantErr: domain.ErrInvalidCoordinates},
		{name: "ストリームの外（負）", op: domain.MouseButton{At: pt(-1, 0), Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: 0}, wantErr: domain.ErrInvalidCoordinates},
		{name: "ストリームの外（ホイール）", op: domain.Wheel{At: pt(0, 1800), DY: 1, Monitor: 0}, wantErr: domain.ErrInvalidCoordinates},
		{name: "未知のモニター", op: domain.PointerMove{At: pt(1, 1), Monitor: 7}, wantErr: domain.ErrInvalidArgument},
		{name: "未知のボタン", op: domain.MouseButton{At: pt(1, 1), Button: "x", Action: domain.ButtonActionDown, Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "未知のボタン操作", op: domain.MouseButton{At: pt(1, 1), Button: domain.ButtonLeft, Action: "hold", Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "ホイールは 50 ノッチまで（縦）", op: domain.Wheel{At: pt(1, 1), DY: 50.5, Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "ホイールは 50 ノッチまで（横・負）", op: domain.Wheel{At: pt(1, 1), DX: -51, Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "ホイールの NaN", op: domain.Wheel{At: pt(1, 1), DY: math.NaN(), Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "ホイールの無限大", op: domain.Wheel{At: pt(1, 1), DX: math.Inf(1), Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "未知のキー操作", op: domain.KeyEvent{Action: "hold", Code: "KeyA"}, wantErr: domain.ErrInvalidArgument},
		{name: "キーの code が空", op: domain.KeyEvent{Action: domain.KeyDown}, wantErr: domain.ErrInvalidArgument},
		{name: "未知の修飾キー", op: domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Mods: []domain.Modifier{"hyper"}}, wantErr: domain.ErrInvalidArgument},
		{name: "自ウィンドウ上の移動", op: domain.PointerMove{At: pt(1700, 100), Monitor: 0}, wantErr: domain.ErrInputBlocked},
		{name: "昇格ウィンドウ上のボタン", op: domain.MouseButton{At: pt(2500, 100), Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: 0}, wantErr: domain.ErrInputBlocked},
		{name: "自ウィンドウ上のホイール", op: domain.Wheel{At: pt(1700, 100), DY: 1, Monitor: 0}, wantErr: domain.ErrInputBlocked},
		{name: "SAS の Delete の down", op: domain.KeyEvent{Action: domain.KeyDown, Code: "Delete", Key: "Delete", Mods: []domain.Modifier{domain.ModCtrl, domain.ModAlt}}, wantErr: domain.ErrInputBlocked},
		{name: "SAS の Delete の up", op: domain.KeyEvent{Action: domain.KeyUp, Code: "Delete", Key: "Delete", Mods: []domain.Modifier{domain.ModCtrl, domain.ModAlt}}, wantErr: domain.ErrInputBlocked},
		{name: "SAS（ショートカット送信）", op: domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl, domain.ModAlt}, Key: "delete"}}, wantErr: domain.ErrInputBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWebEnv(t)
			_, err := e.control.Do(context.Background(), ctlViewer, tt.op)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
			if len(inputCalls(e)) != 0 {
				t.Fatalf("rejected op reached the injector: %+v", inputCalls(e))
			}
		})
	}
}

func TestControlDoWebKeyEventBlockedWhenForegroundIsOwnWindow(t *testing.T) {
	e := newWebEnv(t)
	e.windows.SetWindows(domain.WindowInfo{ID: "2", Own: true, Foreground: true})
	_, err := e.control.Do(context.Background(), ctlViewer, domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA"})
	if !errors.Is(err, domain.ErrInputBlocked) {
		t.Fatalf("want ErrInputBlocked, got %v", err)
	}
	if hasInputCall(e, "KeyEvent") {
		t.Fatal("blocked key event reached the injector")
	}
}

// 操作権（リース）の検査は、キルスイッチの次、InputGuard の前。web は暗黙に取得できない。
func TestControlDoWebRequiresLease(t *testing.T) {
	ops := []domain.Op{
		domain.PointerMove{At: domain.Point{X: 1, Y: 1}, Monitor: 0},
		domain.MouseButton{At: domain.Point{X: 1, Y: 1}, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: 0},
		domain.Wheel{At: domain.Point{X: 1, Y: 1}, DY: 1, Monitor: 0},
		domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA"},
		domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}},
		domain.TypeText{Text: "a"},
		domain.ClipboardRead{},
		domain.ClipboardWrite{Text: "a"},
	}
	for _, op := range ops {
		t.Run("未保持", func(t *testing.T) {
			e := newCtlEnv(t)
			if _, err := e.control.Do(context.Background(), ctlViewer, op); !errors.Is(err, domain.ErrLeaseRequired) {
				t.Fatalf("%T: want ErrLeaseRequired, got %v", op, err)
			}
			if _, held := e.leases.Holder(); held {
				t.Fatal("web must not acquire the lease implicitly")
			}
			if len(inputCalls(e)) != 0 || len(e.clipboard.Calls()) != 0 {
				t.Fatal("op reached a platform effect without the lease")
			}
		})
		t.Run("他者が保持", func(t *testing.T) {
			e := newCtlEnv(t)
			mustAcquire(t, e, ctlAlice)
			if _, err := e.control.Do(context.Background(), ctlViewer, op); !errors.Is(err, domain.ErrLeaseHeldByOther) {
				t.Fatalf("%T: want ErrLeaseHeldByOther, got %v", op, err)
			}
		})
	}
}

func TestControlDoWebRevokedHolderGetsLeaseRevoked(t *testing.T) {
	e := newWebEnv(t)
	if err := e.leases.Revoke(HostActor); err != nil {
		t.Fatal(err)
	}
	_, err := e.control.Do(context.Background(), ctlViewer, domain.TypeText{Text: "a"})
	if !errors.Is(err, domain.ErrLeaseRevoked) {
		t.Fatalf("want ErrLeaseRevoked, got %v", err)
	}
	if hasInputCall(e, "Text") {
		t.Fatal("revoked holder reached the injector")
	}
}

func TestControlDoWebClipboard(t *testing.T) {
	e := newWebEnv(t)
	if _, err := e.control.Do(context.Background(), ctlViewer, domain.ClipboardWrite{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	res, err := e.control.Do(context.Background(), ctlViewer, domain.ClipboardRead{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hello" {
		t.Fatalf("clipboard = %q", res.Text)
	}
}

func TestControlDoWebAudit(t *testing.T) {
	e := newWebEnv(t)
	ctx := context.Background()
	at := domain.Point{X: 10, Y: 20}
	for _, op := range []domain.Op{
		domain.PointerMove{At: at, Monitor: 0},
		domain.MouseButton{At: at, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: 0},
		domain.Wheel{At: at, DY: 2, Monitor: 0},
		domain.KeyEvent{Action: domain.KeyDown, Code: "KeyS", Key: "s", Mods: []domain.Modifier{domain.ModCtrl}},
	} {
		if _, err := e.control.Do(ctx, ctlViewer, op); err != nil {
			t.Fatal(err)
		}
	}
	var events []string
	for _, ent := range e.audit.Entries() {
		if ent.Kind == "input" {
			events = append(events, ent.Event)
		}
	}
	want := []string{"tool.mouse_down", "tool.wheel", "tool.key_down"} // PointerMove は監査しない
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
	for _, ent := range e.audit.Entries() {
		if ent.Event == "tool.key_down" {
			s := string(ent.DetailJSON)
			for _, secret := range []string{"KeyS", `"s"`} {
				if strings.Contains(s, secret) {
					t.Fatalf("audit detail %s must not contain the key name %s", s, secret)
				}
			}
		}
	}
}

func TestControlDoWebRejectedInputIsAudited(t *testing.T) {
	e := newWebEnv(t)
	_, err := e.control.Do(context.Background(), ctlViewer, domain.Wheel{At: domain.Point{X: 1700, Y: 100}, DY: 1, Monitor: 0})
	if !errors.Is(err, domain.ErrInputBlocked) {
		t.Fatalf("want ErrInputBlocked, got %v", err)
	}
	entries := e.audit.Entries()
	last := entries[len(entries)-1]
	if last.Event != "tool.wheel" || last.Result != "denied" || last.ErrorCode != "input_blocked" || last.ActorType != "web" {
		t.Fatalf("audit entry = %+v", last)
	}
}

// キルスイッチは Button で押したボタンと KeyEvent で押したキーを離す。
func TestControlWebKillSwitchReleasesPressedInput(t *testing.T) {
	e := newWebEnv(t)
	ctx := context.Background()
	if _, err := e.control.Do(ctx, ctlViewer, domain.MouseButton{At: domain.Point{X: 5, Y: 5}, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Do(ctx, ctlViewer, domain.KeyEvent{Action: domain.KeyDown, Code: "ShiftLeft"}); err != nil {
		t.Fatal(err)
	}
	if len(e.input.HeldButtons()) != 1 || len(e.input.HeldKeys()) != 1 {
		t.Fatalf("held = %v %v", e.input.HeldButtons(), e.input.HeldKeys())
	}
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if len(e.input.HeldButtons()) != 0 || len(e.input.HeldKeys()) != 0 {
		t.Fatalf("held after kill = %v %v", e.input.HeldButtons(), e.input.HeldKeys())
	}
}
