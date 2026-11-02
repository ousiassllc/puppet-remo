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

// オーバーレイ（クリックスルーの自ウィンドウ）は全モニターを覆うが、入力は下のウィンドウへ通る。
// 判定は下の実ウィンドウで行い、オーバーレイ自身は拒否しない（#69）。
func TestInputGuardOverlay(t *testing.T) {
	overlay := domain.WindowInfo{ID: "ov", Title: "overlay", Own: true, Overlay: true, Rect: domain.Rect{X: 0, Y: 0, W: 3200, H: 1800}}
	dialog := domain.WindowInfo{ID: "dlg", Title: "approval", Own: true, Rect: domain.Rect{X: 100, Y: 100, W: 400, H: 300}}
	normal := domain.WindowInfo{ID: "n", Title: "normal", Rect: domain.Rect{X: 600, Y: 100, W: 400, H: 300}}
	mons := []domain.Monitor{ctlMonitor}
	pt := func(x, y int) domain.Point { return domain.Point{X: x, Y: y} }

	tests := []struct {
		name    string
		wins    []domain.WindowInfo // 先頭が最上位
		op      domain.Op
		wantErr error
	}{
		{name: "オーバーレイが自ダイアログの上にあっても own_window", wins: []domain.WindowInfo{overlay, dialog},
			op: domain.MouseMove{At: pt(150, 150)}, wantErr: domain.ErrInputBlocked},
		{name: "オーバーレイが通常ウィンドウの上にあれば許可", wins: []domain.WindowInfo{overlay, normal},
			op: domain.MouseMove{At: pt(650, 150)}},
		{name: "オーバーレイだけの場所は許可", wins: []domain.WindowInfo{overlay},
			op: domain.MouseMove{At: pt(2000, 1500)}},
		{name: "前面がオーバーレイならキー入力を拒否しない", wins: []domain.WindowInfo{{ID: "ov", Own: true, Overlay: true, Foreground: true}},
			op: domain.TypeText{Text: "x"}},
		{name: "オーバーレイの下でも前面の自ダイアログへのキー入力は拒否", wins: []domain.WindowInfo{overlay, {ID: "dlg", Own: true, Foreground: true}},
			op: domain.TypeText{Text: "x"}, wantErr: domain.ErrInputBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := InputGuard{Windows: platformtest.NewFakeWindows(tt.wins...)}
			err := g.Check(context.Background(), tt.op, mons)
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
