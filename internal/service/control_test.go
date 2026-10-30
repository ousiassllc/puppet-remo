package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func inputCalls(e *ctlEnv) []platformtest.Call { return e.input.Calls() }

func hasInputCall(e *ctlEnv, method string) bool { return len(e.input.CallsTo(method)) > 0 }

func TestControlDoConvertsShrunkToPhysicalCoordinates(t *testing.T) {
	// モニター 3200x1800、最大辺 1568 → 縮小率 0.49。物理 = round(x / 0.49)。
	pt := func(x, y int) domain.Point { return domain.Point{X: x, Y: y} }
	tests := []struct {
		name   string
		op     domain.Op
		method string
		args   []any
	}{
		{name: "mouse_move", op: domain.MouseMove{At: pt(500, 100), Monitor: domain.MonitorPrimary}, method: "MoveTo", args: []any{pt(1020, 204)}},
		{name: "left_click は移動してからクリック", op: domain.Click{At: &domain.Point{X: 500, Y: 100}, Button: domain.ButtonLeft, Count: 1, Mods: []domain.Modifier{domain.ModCtrl}, Monitor: 0},
			method: "Click", args: []any{domain.ButtonLeft, 1, []domain.Modifier{domain.ModCtrl}}},
		{name: "left_click_drag", op: domain.Drag{From: pt(500, 100), To: pt(600, 200), Monitor: 0}, method: "Drag", args: []any{pt(1020, 204), pt(1224, 408), []domain.Modifier(nil)}},
		{name: "scroll", op: domain.Scroll{At: pt(500, 100), Dir: domain.DirDown, Amount: 3, Monitor: 0}, method: "Scroll", args: []any{pt(1020, 204), domain.DirDown, 3}},
		{name: "key", op: domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl}, Key: "c"}}, method: "Key", args: []any{domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl}, Key: "c"}}},
		{name: "type", op: domain.TypeText{Text: "こんにちは"}, method: "Text", args: []any{"こんにちは"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			if _, err := e.control.Do(context.Background(), ctlAlice, tt.op); err != nil {
				t.Fatal(err)
			}
			calls := e.input.CallsTo(tt.method)
			if len(calls) != 1 {
				t.Fatalf("calls to %s = %+v (all: %+v)", tt.method, calls, inputCalls(e))
			}
			if !argsEqual(calls[0].Args, tt.args) {
				t.Fatalf("args = %#v, want %#v", calls[0].Args, tt.args)
			}
			if _, isClick := tt.op.(domain.Click); isClick {
				move := e.input.CallsTo("MoveTo")
				if len(move) != 1 || move[0].Args[0] != pt(1020, 204) {
					t.Fatalf("click must move to the physical point first, got %+v", move)
				}
			}
		})
	}
}

func argsEqual(a, b []any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func TestControlDoClickWithoutPositionClicksInPlace(t *testing.T) {
	e := newCtlEnv(t)
	e.input.SetCursor(domain.Point{X: 300, Y: 300})
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.Click{Button: domain.ButtonRight, Count: 2}); err != nil {
		t.Fatal(err)
	}
	if hasInputCall(e, "MoveTo") {
		t.Fatal("click without a position must not move the cursor")
	}
	if len(e.input.CallsTo("Click")) != 1 {
		t.Fatalf("calls = %+v", inputCalls(e))
	}
}

func TestControlDoClickInPlaceOverOwnWindowIsBlocked(t *testing.T) {
	e := newCtlEnv(t)
	e.input.SetCursor(domain.Point{X: 1700, Y: 100}) // 自ウィンドウ上
	_, err := e.control.Do(context.Background(), ctlAlice, domain.Click{Button: domain.ButtonLeft, Count: 1})
	if !errors.Is(err, domain.ErrInputBlocked) {
		t.Fatalf("want ErrInputBlocked, got %v", err)
	}
	if hasInputCall(e, "Click") {
		t.Fatal("blocked click must not reach the injector")
	}
}

func TestControlDoSecondMonitorUsesItsOwnOriginAndScale(t *testing.T) {
	e := newCtlEnv(t)
	second := domain.Monitor{Index: 1, Name: "m1", Origin: domain.Point{X: 3200, Y: 0}, Size: domain.Size{W: 1000, H: 500}, DPI: 96}
	e.capturer = platformtest.NewFakeCapturer(ctlMonitor, second)
	var err error
	e.control, err = NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Leases: e.leases, Kill: e.kill, Settings: e.settings, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 1000x500 は縮小されない（スケール 1）。
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.MouseMove{At: domain.Point{X: 10, Y: 20}, Monitor: 1}); err != nil {
		t.Fatal(err)
	}
	got := e.input.CallsTo("MoveTo")
	if len(got) != 1 || got[0].Args[0] != (domain.Point{X: 3210, Y: 20}) {
		t.Fatalf("calls = %+v", got)
	}
}

func TestControlDoRejections(t *testing.T) {
	pt := func(x, y int) domain.Point { return domain.Point{X: x, Y: y} }
	tests := []struct {
		name    string
		op      domain.Op
		setup   func(e *ctlEnv)
		wantErr error
	}{
		{name: "縮小座標が範囲外", op: domain.MouseMove{At: pt(1568, 0), Monitor: 0}, wantErr: domain.ErrInvalidCoordinates},
		{name: "負の座標", op: domain.MouseMove{At: pt(-1, 0), Monitor: 0}, wantErr: domain.ErrInvalidCoordinates},
		{name: "未知のモニター", op: domain.MouseMove{At: pt(1, 1), Monitor: 7}, wantErr: domain.ErrInvalidArgument},
		{name: "自ウィンドウへのクリック", op: domain.Click{At: &domain.Point{X: 980, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 0}, wantErr: domain.ErrInputBlocked},
		{name: "昇格ウィンドウへのクリック", op: domain.Click{At: &domain.Point{X: 1300, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 0}, wantErr: domain.ErrInputBlocked},
		{name: "自ウィンドウへのドラッグ終点", op: domain.Drag{From: pt(10, 10), To: pt(980, 100), Monitor: 0}, wantErr: domain.ErrInputBlocked},
		{name: "未知のボタン", op: domain.Click{Button: "x", Count: 1}, wantErr: domain.ErrInvalidArgument},
		{name: "クリック回数が範囲外", op: domain.Click{Button: domain.ButtonLeft, Count: 4}, wantErr: domain.ErrInvalidArgument},
		{name: "未知の修飾キー", op: domain.Click{Button: domain.ButtonLeft, Count: 1, Mods: []domain.Modifier{"hyper"}}, wantErr: domain.ErrInvalidArgument},
		{name: "scroll の amount が範囲外", op: domain.Scroll{At: pt(1, 1), Dir: domain.DirUp, Amount: 51, Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "scroll の方向が不正", op: domain.Scroll{At: pt(1, 1), Dir: "x", Amount: 1, Monitor: 0}, wantErr: domain.ErrInvalidArgument},
		{name: "未知のキー", op: domain.KeyPress{Keys: domain.KeyCombo{Key: "nope"}}, wantErr: domain.ErrInvalidArgument},
		{name: "SAS", op: domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl, domain.ModAlt}, Key: "delete"}}, wantErr: domain.ErrInputBlocked},
		{name: "type は 10000 文字まで", op: domain.TypeText{Text: strings.Repeat("あ", 10_001)}, wantErr: domain.ErrInvalidArgument},
		{name: "前面が自ウィンドウのとき type は拒否", op: domain.TypeText{Text: "x"}, wantErr: domain.ErrInputBlocked,
			setup: func(e *ctlEnv) { e.windows.SetWindows(domain.WindowInfo{ID: "2", Own: true, Foreground: true}) }},
		{name: "focus_window の ID が空", op: domain.FocusWindow{}, wantErr: domain.ErrInvalidArgument},
		{name: "自ウィンドウを前面にできない", op: domain.FocusWindow{ID: "2"}, wantErr: domain.ErrInputBlocked},
		{name: "クリップボードは 1 MiB まで", op: domain.ClipboardWrite{Text: strings.Repeat("a", 1<<20+1)}, wantErr: domain.ErrInvalidArgument},
		{name: "UI ツリーの上限が負", op: domain.GetUITree{MaxDepth: -1}, wantErr: domain.ErrInvalidArgument},
		{name: "自ウィンドウの UI ツリー", op: domain.GetUITree{WindowID: "2"}, wantErr: domain.ErrInputBlocked},
		{name: "未対応の op", op: nil, wantErr: domain.ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			_, err := e.control.Do(context.Background(), ctlAlice, tt.op)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
			for _, m := range []string{"MoveTo", "Click", "Drag", "Scroll", "Key", "Text"} {
				if hasInputCall(e, m) {
					t.Fatalf("rejected op reached the injector: %s", m)
				}
			}
			if len(e.windows.CallsTo("Focus")) > 0 || len(e.clipboard.CallsTo("WriteText")) > 0 {
				t.Fatal("rejected op reached a platform effect")
			}
			// 拒否も監査に残る（result は denied か error）。
			entries := e.audit.Entries()
			last := entries[len(entries)-1]
			if last.Kind != "tool" || last.Result == "ok" || last.ErrorCode != string(domain.CodeOf(tt.wantErr)) {
				t.Fatalf("audit entry = %+v", last)
			}
		})
	}
}

func TestControlDoOrderKillSwitchThenLeaseThenGuard(t *testing.T) {
	own := domain.Click{At: &domain.Point{X: 980, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 0}

	t.Run("キルスイッチはリースより優先（リースも取らない）", func(t *testing.T) {
		e := newCtlEnv(t)
		if err := e.kill.Trigger("test"); err != nil {
			t.Fatal(err)
		}
		_, err := e.control.Do(context.Background(), ctlAlice, own)
		if !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("want ErrKillSwitchActive, got %v", err)
		}
		if _, ok := e.leases.Holder(); ok {
			t.Fatal("kill switch must not leave an implicit lease behind")
		}
		if len(e.windows.CallsTo("At")) > 0 {
			t.Fatal("InputGuard must not run after the kill switch refused")
		}
	})

	t.Run("リースは InputGuard より優先", func(t *testing.T) {
		e := newCtlEnv(t)
		mustAcquire(t, e, ctlBob)
		_, err := e.control.Do(context.Background(), ctlAlice, own)
		if !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Fatalf("want ErrLeaseHeldByOther, got %v", err)
		}
		if len(e.windows.CallsTo("At")) > 0 {
			t.Fatal("InputGuard must not run after the lease check failed")
		}
	})

	t.Run("InputGuard は platform 実行より優先", func(t *testing.T) {
		e := newCtlEnv(t)
		_, err := e.control.Do(context.Background(), ctlAlice, own)
		if !errors.Is(err, domain.ErrInputBlocked) {
			t.Fatalf("want ErrInputBlocked, got %v", err)
		}
		if len(inputCalls(e)) != 0 {
			t.Fatalf("injector called: %+v", inputCalls(e))
		}
	})

	t.Run("監査は全拒否の後にも残る", func(t *testing.T) {
		e := newCtlEnv(t)
		_ = e.kill.Trigger("test")
		_, _ = e.control.Do(context.Background(), ctlAlice, own)
		entries := e.audit.Entries()
		last := entries[len(entries)-1]
		if last.Event != "tool.left_click" || last.Result != "denied" || last.ErrorCode != "kill_switch_active" {
			t.Fatalf("audit entry = %+v", last)
		}
	})
}

func TestControlDoImplicitLease(t *testing.T) {
	tests := []struct {
		name    string
		who     domain.Actor
		wantErr error
	}{
		{name: "mcp は暗黙に取得する（FR-L04）", who: ctlAlice},
		{name: "web は暗黙取得できない", who: domain.Actor{Kind: domain.ActorWeb, SessionID: "v"}, wantErr: domain.ErrLeaseRequired},
		{name: "agent は暗黙取得できない", who: domain.Actor{Kind: domain.ActorAgent, SessionID: "t"}, wantErr: domain.ErrLeaseRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			_, err := e.control.Do(context.Background(), tt.who, domain.TypeText{Text: "a"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
			_, held := e.leases.Holder()
			if held != (tt.wantErr == nil) {
				t.Fatalf("held = %v", held)
			}
		})
	}
}

func TestControlDoOtherHoldersCannotInput(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	_, err := e.control.Do(context.Background(), ctlBob, domain.TypeText{Text: "a"})
	if !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Fatalf("want ErrLeaseHeldByOther, got %v", err)
	}
	if hasInputCall(e, "Text") {
		t.Fatal("non-holder input reached the injector")
	}
}

func TestControlDoKeepsLeaseAliveUntilIdle(t *testing.T) {
	e := newCtlEnv(t)
	do := func() {
		t.Helper()
		if _, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	do()
	e.clock.Advance(4 * time.Minute)
	do() // 活動で延長
	e.clock.Advance(4 * time.Minute)
	do() // 最初の取得から 8 分でも生きている
	e.clock.Advance(5 * time.Minute)
	if err := e.leases.Check(ctlAlice); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("lease should have expired, got %v", err)
	}
}

func TestControlDoWaitNeedsNoLease(t *testing.T) {
	e := newCtlEnv(t)
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.Wait{Duration: 1500 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if _, held := e.leases.Holder(); held {
		t.Fatal("wait must not acquire the lease")
	}
	if len(e.slept) != 1 || e.slept[0] != 1500*time.Millisecond {
		t.Fatalf("slept = %v", e.slept)
	}
	for _, d := range []time.Duration{-time.Millisecond, 10*time.Second + time.Millisecond} {
		if _, err := e.control.Do(context.Background(), ctlAlice, domain.Wait{Duration: d}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("wait %s: want ErrInvalidArgument, got %v", d, err)
		}
	}
	e.kill.Trigger("test")
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.Wait{Duration: time.Second}); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("wait during kill switch: %v", err)
	}
}

func TestControlDoClipboardAndUITree(t *testing.T) {
	e := newCtlEnv(t)
	ctx := context.Background()
	if _, err := e.control.Do(ctx, ctlAlice, domain.ClipboardWrite{Text: "秘密のメモ"}); err != nil {
		t.Fatal(err)
	}
	res, err := e.control.Do(ctx, ctlAlice, domain.ClipboardRead{})
	if err != nil || res.Text != "秘密のメモ" {
		t.Fatalf("clipboard = %q, %v", res.Text, err)
	}
	e.ui.SetTree("1", domain.UINode{ID: "root", Role: "Window", Name: "normal"})
	res, err = e.control.Do(ctx, ctlAlice, domain.GetUITree{WindowID: "1"})
	if err != nil || res.Tree == nil || res.Tree.Name != "normal" {
		t.Fatalf("tree = %+v, %v", res.Tree, err)
	}
	calls := e.ui.CallsTo("Tree")
	if len(calls) != 1 || calls[0].Args[1] != 5 || calls[0].Args[2] != 500 {
		t.Fatalf("defaults depth=5 nodes=500 expected, got %+v", calls)
	}
}

func TestControlDoAuditRecordsLengthsNotText(t *testing.T) {
	e := newCtlEnv(t)
	ctx := context.Background()
	const secret = "p@ssw0rd-こんにちは"
	if _, err := e.control.Do(ctx, ctlAlice, domain.TypeText{Text: secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Do(ctx, ctlAlice, domain.ClipboardWrite{Text: secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Do(ctx, ctlAlice, domain.ClipboardRead{}); err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, en := range e.audit.Entries() {
		if en.Kind != "tool" {
			continue
		}
		tools = append(tools, en.Tool)
		if strings.Contains(string(en.DetailJSON), "p@ssw0rd") || strings.Contains(string(en.DetailJSON), "こんにちは") {
			t.Fatalf("audit detail leaks text: %s", en.DetailJSON)
		}
		if want := `{"text_len":14}`; string(en.DetailJSON) != want {
			t.Fatalf("%s detail = %s, want %s", en.Tool, en.DetailJSON, want)
		}
		if en.Result != "ok" || en.LeaseID != "lease-1" || en.ClientID != "client-a" || en.DurationMS == nil {
			t.Fatalf("entry = %+v", en)
		}
	}
	if want := []string{"type", "clipboard_write", "clipboard_read"}; strings.Join(tools, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", tools, want)
	}
}

func TestControlDoAuditToolNamesAndDuration(t *testing.T) {
	e := newCtlEnv(t)
	ctx := context.Background()
	p := &domain.Point{X: 10, Y: 10}
	ops := []struct {
		op   domain.Op
		tool string
	}{
		{domain.Click{At: p, Button: domain.ButtonLeft, Count: 1}, "left_click"},
		{domain.Click{At: p, Button: domain.ButtonLeft, Count: 2}, "double_click"},
		{domain.Click{At: p, Button: domain.ButtonLeft, Count: 3}, "triple_click"},
		{domain.Click{At: p, Button: domain.ButtonRight, Count: 1}, "right_click"},
		{domain.Click{At: p, Button: domain.ButtonMiddle, Count: 1}, "middle_click"},
	}
	for _, o := range ops {
		if _, err := e.control.Do(ctx, ctlAlice, o.op); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, en := range e.audit.Entries() {
		if en.Kind == "tool" {
			got = append(got, en.Tool)
		}
	}
	for i, o := range ops {
		if got[i] != o.tool {
			t.Fatalf("tool[%d] = %s, want %s", i, got[i], o.tool)
		}
	}
}

func TestControlDoPlatformErrorIsAuditedAsError(t *testing.T) {
	e := newCtlEnv(t)
	boom := errors.New("injector down")
	e.input.SetError("Text", boom)
	_, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "a"})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	entries := e.audit.Entries()
	last := entries[len(entries)-1]
	if last.Result != "error" || last.ErrorCode != "internal" {
		t.Fatalf("entry = %+v", last)
	}
}

func TestControlViewOperations(t *testing.T) {
	e := newCtlEnv(t)
	ctx := context.Background()
	e.capturer.SetImage(0, image.NewRGBA(image.Rect(0, 0, 3200, 1800)))

	t.Run("screenshot は縮小され、リース不要", func(t *testing.T) {
		shot, err := e.control.Screenshot(ctx, domain.MonitorPrimary, "")
		if err != nil {
			t.Fatal(err)
		}
		if shot.MIME != "image/png" || shot.Monitor != 0 || shot.Physical != (domain.Size{W: 3200, H: 1800}) ||
			shot.Scaled != (domain.Size{W: 1568, H: 882}) || shot.Scale != 0.49 {
			t.Fatalf("shot = %+v", shot)
		}
		img, err := png.Decode(bytes.NewReader(shot.Data))
		if err != nil || img.Bounds().Dx() != 1568 || img.Bounds().Dy() != 882 {
			t.Fatalf("decoded = %v, %v", img.Bounds(), err)
		}
		if _, held := e.leases.Holder(); held {
			t.Fatal("screenshot must not take the lease")
		}
	})

	// 以降は縮小の中身を見ないので、小さな画像で速くする。
	e.capturer.SetImage(0, image.NewRGBA(image.Rect(0, 0, 64, 36)))

	t.Run("jpeg と不正な形式", func(t *testing.T) {
		shot, err := e.control.Screenshot(ctx, 0, "jpeg")
		if err != nil || shot.MIME != "image/jpeg" {
			t.Fatalf("shot = %+v, %v", shot, err)
		}
		if _, err := e.control.Screenshot(ctx, 0, "gif"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("got %v", err)
		}
		if _, err := e.control.Screenshot(ctx, 9, "png"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("max_dimension の設定を使う", func(t *testing.T) {
		e.settings.put("screenshot.max_dimension", "800")
		shot, err := e.control.Screenshot(ctx, 0, "png")
		if err != nil || shot.Scaled != (domain.Size{W: 800, H: 450}) {
			t.Fatalf("shot = %+v, %v", shot, err)
		}
		e.settings.put("screenshot.max_dimension", "1568")
	})

	t.Run("カーソル位置は縮小座標", func(t *testing.T) {
		e.input.SetCursor(domain.Point{X: 1020, Y: 204})
		cur, err := e.control.CursorPosition(ctx)
		if err != nil || cur.Point != (domain.Point{X: 500, Y: 100}) || cur.Monitor != 0 {
			t.Fatalf("cursor = %+v, %v", cur, err)
		}
		e.input.SetCursor(domain.Point{X: 99999, Y: 0})
		if _, err := e.control.CursorPosition(ctx); !errors.Is(err, domain.ErrDesktopUnavailable) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("monitors と windows", func(t *testing.T) {
		mons, err := e.control.Monitors(ctx)
		if err != nil || len(mons) != 1 {
			t.Fatalf("monitors = %+v, %v", mons, err)
		}
		wins, err := e.control.Windows(ctx)
		if err != nil || len(wins) != 4 {
			t.Fatalf("windows = %+v, %v", wins, err)
		}
	})

	t.Run("キルスイッチ中は閲覧系も断る", func(t *testing.T) {
		_ = e.kill.Trigger("test")
		if _, err := e.control.Screenshot(ctx, 0, "png"); !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("screenshot: %v", err)
		}
		if _, err := e.control.Monitors(ctx); !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("monitors: %v", err)
		}
		if _, err := e.control.Windows(ctx); !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("windows: %v", err)
		}
		if _, err := e.control.CursorPosition(ctx); !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("cursor: %v", err)
		}
	})
}

func TestNewControlRequiresDependencies(t *testing.T) {
	if _, err := NewControl(ControlConfig{}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("got %v", err)
	}
}

func TestControlDoFocusWindow(t *testing.T) {
	e := newCtlEnv(t)
	ctx := context.Background()
	if _, err := e.control.Do(ctx, ctlAlice, domain.FocusWindow{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	if got := e.windows.CallsTo("Focus"); len(got) != 1 || got[0].Args[0] != "1" {
		t.Fatalf("focus calls = %+v", got)
	}
	// 存在しない ID は InputGuard を通り、platform が ErrWindowNotFound を返す。
	if _, err := e.control.Do(ctx, ctlAlice, domain.FocusWindow{ID: "999"}); !errors.Is(err, domain.ErrWindowNotFound) {
		t.Fatalf("want ErrWindowNotFound, got %v", err)
	}
}
