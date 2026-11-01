package platformtest

import (
	"context"
	"math"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// RunInputContract は platform.InputInjector の契約を検査する。
// safe は画面内の、動かしてもクリックしても害のない座標。実機のスモークでは実際にカーソルを動かし、
// クリック・ホイール・キー・文字入力を前面のウィンドウへ送るので、使い捨ての環境で実行する。
func RunInputContract(t *testing.T, newInput func(t *testing.T) platform.InputInjector, safe domain.Point) {
	t.Helper()
	ctx := context.Background()
	near := domain.Point{X: safe.X + 5, Y: safe.Y + 5}

	requireCursor := func(t *testing.T, in platform.InputInjector, want domain.Point, after string) {
		t.Helper()
		got, err := in.CursorPos()
		if err != nil {
			t.Fatalf("CursorPos: %v", err)
		}
		if got != want {
			t.Errorf("cursor after %s = %v, want %v", after, got, want)
		}
	}

	t.Run("MoveTo moves the cursor", func(t *testing.T) {
		in := newInput(t)
		if err := in.MoveTo(ctx, safe); err != nil {
			t.Fatalf("MoveTo: %v", err)
		}
		requireCursor(t, in, safe, "MoveTo")
	})

	t.Run("Drag leaves the cursor at the destination", func(t *testing.T) {
		in := newInput(t)
		t.Cleanup(func() { _ = in.ReleaseAll() })
		if err := in.Drag(ctx, safe, near, []domain.Modifier{domain.ModShift}); err != nil {
			t.Fatalf("Drag: %v", err)
		}
		requireCursor(t, in, near, "Drag")
	})

	t.Run("Scroll and Wheel move the cursor to the target", func(t *testing.T) {
		in := newInput(t)
		if err := in.Scroll(ctx, safe, domain.DirDown, 1); err != nil {
			t.Fatalf("Scroll: %v", err)
		}
		requireCursor(t, in, safe, "Scroll")
		if err := in.Wheel(ctx, near, 0.5, -1.5); err != nil {
			t.Fatalf("Wheel: %v", err)
		}
		requireCursor(t, in, near, "Wheel")
		if err := in.Wheel(ctx, near, 0, 0); err != nil {
			t.Errorf("Wheel(0, 0): %v", err)
		}
	})

	t.Run("Click accepts valid arguments", func(t *testing.T) {
		in := newInput(t)
		if err := in.MoveTo(ctx, safe); err != nil {
			t.Fatalf("MoveTo: %v", err)
		}
		for _, count := range []int{1, 2, 3} {
			if err := in.Click(ctx, domain.ButtonLeft, count, []domain.Modifier{domain.ModCtrl}); err != nil {
				t.Errorf("Click(left, %d, ctrl): %v", count, err)
			}
		}
		requireCursor(t, in, safe, "Click")
	})

	t.Run("Button presses and releases at the cursor", func(t *testing.T) {
		in := newInput(t)
		t.Cleanup(func() { _ = in.ReleaseAll() })
		if err := in.MoveTo(ctx, safe); err != nil {
			t.Fatalf("MoveTo: %v", err)
		}
		for _, b := range []domain.Button{domain.ButtonLeft, domain.ButtonRight, domain.ButtonMiddle} {
			if err := in.Button(ctx, b, domain.ButtonActionDown); err != nil {
				t.Errorf("Button(%s, down): %v", b, err)
			}
			if err := in.Button(ctx, b, domain.ButtonActionUp); err != nil {
				t.Errorf("Button(%s, up): %v", b, err)
			}
		}
		requireCursor(t, in, safe, "Button")
	})

	t.Run("invalid arguments are rejected", func(t *testing.T) {
		in := newInput(t)
		requireCode(t, in.Click(ctx, "x", 1, nil), domain.ErrInvalidArgument, "Click(unknown button)")
		requireCode(t, in.Click(ctx, domain.ButtonLeft, 0, nil), domain.ErrInvalidArgument, "Click(count 0)")
		requireCode(t, in.Click(ctx, domain.ButtonLeft, 4, nil), domain.ErrInvalidArgument, "Click(count 4)")
		requireCode(t, in.Click(ctx, domain.ButtonLeft, 1, []domain.Modifier{"hyper"}), domain.ErrInvalidArgument, "Click(unknown modifier)")
		requireCode(t, in.Button(ctx, "x", domain.ButtonActionDown), domain.ErrInvalidArgument, "Button(unknown button)")
		requireCode(t, in.Button(ctx, domain.ButtonLeft, "hold"), domain.ErrInvalidArgument, "Button(unknown action)")
		requireCode(t, in.Drag(ctx, safe, near, []domain.Modifier{"hyper"}), domain.ErrInvalidArgument, "Drag(unknown modifier)")
		requireCode(t, in.Scroll(ctx, safe, "sideways", 1), domain.ErrInvalidArgument, "Scroll(unknown direction)")
		requireCode(t, in.Scroll(ctx, safe, domain.DirUp, 0), domain.ErrInvalidArgument, "Scroll(amount 0)")
		requireCode(t, in.Wheel(ctx, safe, math.NaN(), 0), domain.ErrInvalidArgument, "Wheel(NaN)")
		requireCode(t, in.Wheel(ctx, safe, 0, math.Inf(1)), domain.ErrInvalidArgument, "Wheel(Inf)")
		requireCode(t, in.Key(ctx, domain.KeyCombo{}), domain.ErrInvalidArgument, "Key(empty)")
		requireCode(t, in.Key(ctx, domain.KeyCombo{Key: "nosuchkey"}), domain.ErrInvalidArgument, "Key(unknown key)")
		requireCode(t, in.Key(ctx, domain.KeyCombo{Mods: []domain.Modifier{"hyper"}, Key: "a"}), domain.ErrInvalidArgument, "Key(unknown modifier)")
		requireCode(t, in.KeyEvent(ctx, domain.KeyEvent{Action: "hold", Code: "ShiftLeft"}), domain.ErrInvalidArgument, "KeyEvent(unknown action)")
		requireCode(t, in.KeyEvent(ctx, domain.KeyEvent{Action: domain.KeyDown}), domain.ErrInvalidArgument, "KeyEvent(empty code)")
	})

	t.Run("Key, KeyEvent and Text accept valid input", func(t *testing.T) {
		in := newInput(t)
		t.Cleanup(func() { _ = in.ReleaseAll() })
		if err := in.Key(ctx, domain.KeyCombo{Key: "f24"}); err != nil {
			t.Errorf("Key(f24): %v", err)
		}
		if err := in.KeyEvent(ctx, domain.KeyEvent{Action: domain.KeyDown, Code: "ShiftLeft", Key: "Shift"}); err != nil {
			t.Errorf("KeyEvent(down): %v", err)
		}
		if err := in.KeyEvent(ctx, domain.KeyEvent{Action: domain.KeyUp, Code: "ShiftLeft", Key: "Shift"}); err != nil {
			t.Errorf("KeyEvent(up): %v", err)
		}
		for _, s := range []string{"", "abc", "日本語", "😀 emoji"} {
			if err := in.Text(ctx, s); err != nil {
				t.Errorf("Text(%q): %v", s, err)
			}
		}
	})

	t.Run("ReleaseAll succeeds with and without pressed keys", func(t *testing.T) {
		in := newInput(t)
		if err := in.ReleaseAll(); err != nil {
			t.Errorf("ReleaseAll (nothing pressed): %v", err)
		}
		if err := in.KeyEvent(ctx, domain.KeyEvent{Action: domain.KeyDown, Code: "ShiftLeft", Key: "Shift"}); err != nil {
			t.Fatalf("KeyEvent(down): %v", err)
		}
		if err := in.ReleaseAll(); err != nil {
			t.Errorf("ReleaseAll (key pressed): %v", err)
		}
		if err := in.Button(ctx, domain.ButtonLeft, domain.ButtonActionDown); err != nil {
			t.Fatalf("Button(down): %v", err)
		}
		if err := in.ReleaseAll(); err != nil {
			t.Errorf("ReleaseAll (button pressed): %v", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		in := newInput(t)
		c := canceledCtx()
		requireCanceled(t, in.MoveTo(c, safe), "MoveTo")
		requireCanceled(t, in.Click(c, domain.ButtonLeft, 1, nil), "Click")
		requireCanceled(t, in.Button(c, domain.ButtonLeft, domain.ButtonActionDown), "Button")
		requireCanceled(t, in.Drag(c, safe, near, nil), "Drag")
		requireCanceled(t, in.Scroll(c, safe, domain.DirUp, 1), "Scroll")
		requireCanceled(t, in.Wheel(c, safe, 0, 1), "Wheel")
		requireCanceled(t, in.Key(c, domain.KeyCombo{Key: "f24"}), "Key")
		requireCanceled(t, in.KeyEvent(c, domain.KeyEvent{Action: domain.KeyDown, Code: "ShiftLeft"}), "KeyEvent")
		requireCanceled(t, in.Text(c, "x"), "Text")
	})
}
