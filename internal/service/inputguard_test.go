package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func TestInputGuardCheckTable(t *testing.T) {
	mons := []domain.Monitor{ctlMonitor}
	pt := func(x, y int) domain.Point { return domain.Point{X: x, Y: y} }
	keys := func(s string) domain.KeyCombo {
		k, err := domain.ParseKeyCombo(s)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	at := pt(1700, 100) // 自ウィンドウ上（物理座標）

	tests := []struct {
		name    string
		op      domain.Op
		wins    []domain.WindowInfo // nil なら既定
		mons    []domain.Monitor
		wantErr error
	}{
		{name: "mouse_move: 通常ウィンドウ", op: domain.MouseMove{At: pt(1000, 100)}, mons: mons},
		{name: "mouse_move: 自ウィンドウ", op: domain.MouseMove{At: at}, mons: mons, wantErr: domain.ErrInputBlocked},
		{name: "mouse_move: 昇格ウィンドウ", op: domain.MouseMove{At: pt(2500, 100)}, mons: mons, wantErr: domain.ErrInputBlocked},
		{name: "mouse_move: ウィンドウなし（デスクトップ）", op: domain.MouseMove{At: pt(100, 1500)}, mons: mons},
		{name: "mouse_move: どのモニターにもない", op: domain.MouseMove{At: pt(-5, 10)}, mons: mons, wantErr: domain.ErrInvalidCoordinates},
		{name: "mouse_move: モニター一覧が空なら範囲は見ない", op: domain.MouseMove{At: pt(-5, 10)}},
		{name: "click 座標あり: 自ウィンドウ", op: domain.Click{At: &at, Button: domain.ButtonLeft, Count: 1}, mons: mons, wantErr: domain.ErrInputBlocked},
		{name: "click 座標なし: 前面は通常", op: domain.Click{Button: domain.ButtonLeft, Count: 1}, mons: mons},
		{name: "click 座標なし: 前面が自ウィンドウ", op: domain.Click{Button: domain.ButtonLeft, Count: 1}, mons: mons,
			wins: []domain.WindowInfo{{ID: "2", Own: true, Foreground: true}}, wantErr: domain.ErrInputBlocked},
		{name: "drag: 終点が自ウィンドウ", op: domain.Drag{From: pt(10, 10), To: at}, mons: mons, wantErr: domain.ErrInputBlocked},
		{name: "drag: 始点が昇格ウィンドウ", op: domain.Drag{From: pt(2500, 10), To: pt(10, 10)}, mons: mons, wantErr: domain.ErrInputBlocked},
		{name: "drag: 両方通常", op: domain.Drag{From: pt(10, 10), To: pt(900, 700)}, mons: mons},
		{name: "scroll: 自ウィンドウ", op: domain.Scroll{At: at, Dir: domain.DirDown, Amount: 1}, mons: mons, wantErr: domain.ErrInputBlocked},
		{name: "key: 前面は通常", op: domain.KeyPress{Keys: keys("ctrl+c")}, mons: mons},
		{name: "key: 前面が自ウィンドウ", op: domain.KeyPress{Keys: keys("a")},
			wins: []domain.WindowInfo{{ID: "2", Own: true, Foreground: true}}, wantErr: domain.ErrInputBlocked},
		{name: "key: 前面が昇格ウィンドウ", op: domain.KeyPress{Keys: keys("a")},
			wins: []domain.WindowInfo{{ID: "3", Elevated: true, Foreground: true}}, wantErr: domain.ErrInputBlocked},
		{name: "key: SAS", op: domain.KeyPress{Keys: keys("ctrl+alt+delete")}, wantErr: domain.ErrInputBlocked},
		{name: "type: 前面が自ウィンドウ", op: domain.TypeText{Text: "x"},
			wins: []domain.WindowInfo{{ID: "2", Own: true, Foreground: true}}, wantErr: domain.ErrInputBlocked},
		{name: "type: 前面なし", op: domain.TypeText{Text: "x"}, wins: []domain.WindowInfo{{ID: "1"}}, wantErr: domain.ErrDesktopUnavailable},
		{name: "focus_window: 自ウィンドウ", op: domain.FocusWindow{ID: "2"}, wantErr: domain.ErrInputBlocked},
		{name: "focus_window: 昇格ウィンドウは前面にできる", op: domain.FocusWindow{ID: "3"}},
		{name: "focus_window: 存在しない ID は platform に任せる", op: domain.FocusWindow{ID: "999"}},
		{name: "get_ui_tree: 自ウィンドウ", op: domain.GetUITree{WindowID: "2"}, wantErr: domain.ErrInputBlocked},
		{name: "get_ui_tree: 前面が自ウィンドウ", op: domain.GetUITree{},
			wins: []domain.WindowInfo{{ID: "2", Own: true, Foreground: true}}, wantErr: domain.ErrInputBlocked},
		{name: "get_ui_tree: 昇格ウィンドウは読み取りのみで許可", op: domain.GetUITree{WindowID: "3"}},
		{name: "clipboard / wait は宛先を持たない", op: domain.ClipboardWrite{Text: "x"},
			wins: []domain.WindowInfo{{ID: "2", Own: true, Foreground: true}}},
		{name: "未対応の op", op: nil, wantErr: domain.ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wins := tt.wins
			if wins == nil {
				wins = ctlDefaultWindows()
			}
			g := InputGuard{Windows: platformtest.NewFakeWindows(wins...)}
			err := g.Check(context.Background(), tt.op, tt.mons)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestInputGuardPropagatesPlatformErrors(t *testing.T) {
	w := platformtest.NewFakeWindows(ctlDefaultWindows()...)
	boom := errors.New("boom")
	w.SetError("At", boom)
	g := InputGuard{Windows: w}
	if err := g.Check(context.Background(), domain.MouseMove{At: domain.Point{X: 1, Y: 1}}, nil); !errors.Is(err, boom) {
		t.Fatalf("want platform error, got %v", err)
	}
}
