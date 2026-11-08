package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 入力スコープ（#156）の独立レビューの指摘: SAS の拒否・現在位置クリックの検証・検証失敗時の解放・ストリーム入力の検証。

// Ctrl+Alt+Delete（SAS）はスコープ付きのリースでも、スコープなしと同じ拒否（secure_attention_sequence）。注入は 0 件で、停止にもしない。
func TestScopedSecureAttentionSequenceIsRefusedLikeUnscoped(t *testing.T) {
	tests := []struct {
		name string
		op   domain.Op
	}{
		{"KeyPress", keyOf("ctrl+alt+delete")},
		{"KeyEvent", domain.KeyEvent{Action: domain.KeyDown, Code: "Delete", Key: "Delete", Mods: []domain.Modifier{domain.ModCtrl, domain.ModAlt}}},
		{"KeyEvent（key だけ）", domain.KeyEvent{Action: domain.KeyDown, Code: "NumpadDecimal", Key: "Delete", Mods: []domain.Modifier{domain.ModCtrl, domain.ModAlt}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTgtEnv(t)
			te.acquire(ctlAlice)
			err := te.do(ctlAlice, tt.op)
			if !errors.Is(err, domain.ErrInputBlocked) {
				t.Fatalf("err = %v, want input_blocked", err)
			}
			if d := blockedDetail(t, err); d["reason"] != "secure_attention_sequence" {
				t.Errorf("reason = %v, want secure_attention_sequence", d["reason"])
			}
			if n := len(injections(te.ctlEnv)); n != 0 {
				t.Errorf("injections = %+v, want none", injections(te.ctlEnv))
			}
			if s := te.scope(); s.State != domain.ScopeActive {
				t.Errorf("scope = %+v, want it to stay active (the operation alone is refused)", s)
			}
		})
	}
}

// At を省略したクリックは、plan の時点ではなく注入の直前（lockInput の中）に読んだカーソル位置を検証する。
func TestScopedClickInPlaceVerifiesTheCursorAtInjectionTime(t *testing.T) {
	inPlace := domain.Click{Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}

	t.Run("カーソルがメンバーの上ならクリックは注入され、移動はしない", func(t *testing.T) {
		te := newTgtEnv(t)
		te.acquire(ctlAlice)
		te.input.SetCursor(ptInT)
		if err := te.do(ctlAlice, inPlace); err != nil {
			t.Fatalf("click in place over T: %v", err)
		}
		if len(te.input.CallsTo("Click")) != 1 || len(te.input.CallsTo("MoveTo")) != 0 {
			t.Errorf("injections = %+v, want one Click and no MoveTo", injections(te.ctlEnv))
		}
	})

	t.Run("カーソルが非メンバーの上なら注入せず停止する", func(t *testing.T) {
		te := newTgtEnv(t)
		te.acquire(ctlAlice)
		te.windows.Add(otherWindow("2001", rectOutside, false))
		te.input.SetCursor(ptOutside)
		te.requireHalted(te.do(ctlAlice, inPlace), domain.ReasonOutsideTargetGroup)
		if n := len(injections(te.ctlEnv)); n != 0 {
			t.Errorf("injections = %+v, want none", injections(te.ctlEnv))
		}
	})

	t.Run("plan のあとでカーソルが非メンバーの上へ動いたら、動いたあとの位置で検証して注入しない", func(t *testing.T) {
		te := newTgtEnv(t)
		hook := &hookInput{InputInjector: te.app}
		te.useControl(hook, te.capturer)
		te.acquire(ctlAlice)
		te.input.SetCursor(ptInT) // plan が読む位置はメンバーの上
		hook.afterCursorPos = func() {
			te.windows.Add(otherWindow("2001", rectOutside, false))
			te.input.SetCursor(ptOutside)
		}
		te.requireHalted(te.do(ctlAlice, inPlace), domain.ReasonOutsideTargetGroup)
		if n := len(injections(te.ctlEnv)); n != 0 {
			t.Errorf("injections = %+v, want none (the stale plan-time position must not be used)", injections(te.ctlEnv))
		}
	})
}

// 検証そのものが platform の失敗で終わったとき、すでに注入していれば ReleaseAll を呼んでから返し、続きは注入しない。
func TestScopedVerificationFailureAfterAnInjectionReleasesInput(t *testing.T) {
	boom := errors.New("platform failure")
	buttonDown := domain.MouseButton{At: ptInT, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: domain.MonitorPrimary}

	for _, method := range []string{"ListAll", "At"} {
		t.Run("MouseButton: MoveTo のあとの検証が "+method+" の失敗", func(t *testing.T) {
			te := newTgtEnv(t)
			hook := &hookInput{InputInjector: te.app}
			te.useControl(hook, te.capturer)
			te.acquire(ctlAlice)
			hook.afterMoveTo = func() { te.windows.SetError(method, boom) }

			err := te.do(ctlAlice, buttonDown)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the platform failure", err)
			}
			if n := releaseAllCalls(te.ctlEnv); n != 1 {
				t.Errorf("ReleaseAll calls = %d, want 1", n)
			}
			if len(te.input.CallsTo("MoveTo")) != 1 || len(te.input.CallsTo("Button")) != 0 {
				t.Errorf("injections = %+v, want only the MoveTo", injections(te.ctlEnv))
			}
			if held := te.input.HeldButtons(); len(held) != 0 {
				t.Errorf("held buttons = %v, want none", held)
			}
			if s := te.scope(); s.State != domain.ScopeActive {
				t.Errorf("scope = %+v, a platform failure is not a halt", s)
			}
		})
	}

	t.Run("MouseButton: MoveTo のあとの検証が ctx のキャンセルで終わる", func(t *testing.T) {
		te := newTgtEnv(t)
		hook := &hookInput{InputInjector: te.app}
		te.useControl(hook, te.capturer)
		te.acquire(ctlAlice)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hook.afterMoveTo = cancel

		_, err := te.ctl.Do(ctx, ctlAlice, buttonDown)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if n := releaseAllCalls(te.ctlEnv); n != 1 {
			t.Errorf("ReleaseAll calls = %d, want 1", n)
		}
		if len(te.input.CallsTo("Button")) != 0 {
			t.Errorf("injections = %+v, want only the MoveTo", injections(te.ctlEnv))
		}
	})

	t.Run("Click: MoveTo のあとの検証が失敗する", func(t *testing.T) {
		te := newTgtEnv(t)
		hook := &hookInput{InputInjector: te.app}
		te.useControl(hook, te.capturer)
		te.acquire(ctlAlice)
		hook.afterMoveTo = func() { te.windows.SetError("ListAll", boom) }

		if err := te.click(ctlAlice, ptInT); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the platform failure", err)
		}
		if n := releaseAllCalls(te.ctlEnv); n != 1 {
			t.Errorf("ReleaseAll calls = %d, want 1", n)
		}
		if len(te.input.CallsTo("Click")) != 0 {
			t.Errorf("injections = %+v, want only the MoveTo", injections(te.ctlEnv))
		}
	})

	t.Run("TypeText: 1 つ目の塊のあとの検証（前面）が失敗する", func(t *testing.T) {
		te := newTgtEnv(t)
		hook := &hookInput{InputInjector: te.app}
		te.useControl(hook, te.capturer)
		te.acquire(ctlAlice)
		hook.afterText = func() { te.windows.SetError("Foreground", boom) }

		err := te.do(ctlAlice, domain.TypeText{Text: "0123456789abcdefghij"})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the platform failure", err)
		}
		if n := releaseAllCalls(te.ctlEnv); n != 1 {
			t.Errorf("ReleaseAll calls = %d, want 1", n)
		}
		if n := len(te.input.CallsTo("Text")); n != 1 {
			t.Errorf("Text calls = %d, want 1", n)
		}
	})

	t.Run("最初の注入の前の検証の失敗では ReleaseAll は呼ばない（押下中のものがない）", func(t *testing.T) {
		te := newTgtEnv(t)
		te.acquire(ctlAlice)
		te.windows.SetError("ListAll", boom)

		if err := te.do(ctlAlice, buttonDown); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the platform failure", err)
		}
		if n := releaseAllCalls(te.ctlEnv); n != 0 {
			t.Errorf("ReleaseAll calls = %d, want 0", n)
		}
		if n := len(injections(te.ctlEnv)); n != 0 {
			t.Errorf("injections = %+v, want none", injections(te.ctlEnv))
		}
	})
}

// ストリーム入力（PointerMove・MouseButton・Wheel）は、座標が G の外なら注入 0 で停止し、メンバーの上なら注入される。
func TestScopedStreamInputIsVerifiedAgainstTheGroup(t *testing.T) {
	mon := domain.MonitorPrimary
	tests := []struct {
		name   string
		at     func(domain.Point) domain.Op
		inject map[string]int // メンバーの上で届く注入
	}{
		{"PointerMove", func(p domain.Point) domain.Op { return domain.PointerMove{At: p, Monitor: mon} }, map[string]int{"MoveTo": 1}},
		{"MouseButton down", func(p domain.Point) domain.Op {
			return domain.MouseButton{At: p, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: mon}
		}, map[string]int{"MoveTo": 1, "Button": 1}},
		{"Wheel", func(p domain.Point) domain.Op { return domain.Wheel{At: p, DY: 2, Monitor: mon} }, map[string]int{"Wheel": 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name+": メンバーの上は注入される", func(t *testing.T) {
			te := newTgtEnv(t)
			te.acquire(ctlAlice)
			if err := te.do(ctlAlice, tt.at(ptInT)); err != nil {
				t.Fatalf("%s over T: %v", tt.name, err)
			}
			total := 0
			for method, want := range tt.inject {
				total += want
				if got := len(te.input.CallsTo(method)); got != want {
					t.Errorf("%s calls = %d, want %d (injections %+v)", method, got, want, injections(te.ctlEnv))
				}
			}
			if n := len(injections(te.ctlEnv)); n != total {
				t.Errorf("injections = %+v, want only %v", injections(te.ctlEnv), tt.inject)
			}
			if s := te.scope(); s.State != domain.ScopeActive {
				t.Errorf("scope = %+v, want active", s)
			}
		})
		t.Run(tt.name+": G の外は注入 0 で停止する", func(t *testing.T) {
			te := newTgtEnv(t)
			te.acquire(ctlAlice)
			te.windows.Add(otherWindow("2001", rectOutside, false))
			te.requireHalted(te.do(ctlAlice, tt.at(ptOutside)), domain.ReasonOutsideTargetGroup)
			if n := len(injections(te.ctlEnv)); n != 0 {
				t.Errorf("injections = %+v, want none", injections(te.ctlEnv))
			}
		})
	}
}

// MouseButton は MoveTo のあと・ボタンの前にも検証し直す。その間に非メンバーが座標を覆ったら、ボタンは注入しない。
func TestScopedMouseButtonIsVerifiedAgainBetweenMoveToAndTheButton(t *testing.T) {
	te := newTgtEnv(t)
	hook := &hookInput{InputInjector: te.app}
	te.useControl(hook, te.capturer)
	te.acquire(ctlAlice)
	hook.afterMoveTo = func() { te.windows.Add(otherWindow("2001", rectCoverT, true)) }

	err := te.do(ctlAlice, domain.MouseButton{At: domain.Point{X: 400, Y: 300}, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: domain.MonitorPrimary})
	te.requireHalted(err, domain.ReasonTargetObscured)
	if len(te.input.CallsTo("MoveTo")) != 1 || len(te.input.CallsTo("Button")) != 0 {
		t.Errorf("injections = %+v, want only the MoveTo", injections(te.ctlEnv))
	}
}
