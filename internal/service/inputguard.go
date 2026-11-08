package service

import (
	"context"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// InputGuard は、入力が自ウィンドウ・昇格ウィンドウ・SAS を対象にしないことを事実（platform）から判定する
// 純粋なロジック（NFR-S06、UIPI。docs/components/overview.md §5.4）。状態を持たない。
// 判定の規則そのものは domain（CheckInputTarget など）にあり、ここは対象のウィンドウを調べて規則へ渡すだけ。
//
// 座標を持つ op は、その座標（物理座標）の最上位ウィンドウを WindowManager.At で、座標を持たない key / type は
// Foreground で調べる。調べられない（platform のエラー）ときは拒否側に倒す（エラーをそのまま返す）。
type InputGuard struct{ Windows platform.WindowManager }

// Check は op（座標は物理座標。Control が変換済み）の宛先を検査する。mon はモニター一覧で、
// 空でなければ座標がいずれかのモニター上にあることも確かめる（なければ ErrInvalidCoordinates）。
// 拒否は domain.ErrInputBlocked。座標・キー・文字列を持たない op（Wait・クリップボード）は常に許可する。
func (g InputGuard) Check(ctx context.Context, op domain.Op, mon []domain.Monitor) error {
	switch o := op.(type) {
	case domain.MouseMove:
		return g.checkPoints(mon, o.At)
	case domain.Click:
		if o.At == nil {
			return g.checkForeground(domain.CheckInputTarget)
		}
		return g.checkPoints(mon, *o.At)
	case domain.Drag:
		return g.checkPoints(mon, o.From, o.To)
	case domain.Scroll:
		return g.checkPoints(mon, o.At)
	case domain.PointerMove:
		return g.checkPoints(mon, o.At)
	case domain.MouseButton:
		return g.checkPoints(mon, o.At)
	case domain.Wheel:
		return g.checkPoints(mon, o.At)
	case domain.KeyEvent:
		if err := domain.CheckKeyEvent(o); err != nil {
			return err
		}
		return g.checkForeground(domain.CheckInputTarget)
	case domain.KeyPress:
		if err := domain.CheckKeyCombo(o.Keys); err != nil {
			return err
		}
		return g.checkForeground(domain.CheckInputTarget)
	case domain.TypeText:
		return g.checkForeground(domain.CheckInputTarget)
	case domain.FocusWindow:
		return g.checkWindowByID(ctx, o.ID, domain.CheckObserveTarget)
	case domain.GetUITree:
		if o.WindowID == "" {
			return g.checkForeground(domain.CheckObserveTarget)
		}
		return g.checkWindowByID(ctx, o.WindowID, domain.CheckObserveTarget)
	case domain.ClipboardRead, domain.ClipboardWrite, domain.Wait:
		return nil
	}
	return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("unsupported operation %T", op))
}

func (g InputGuard) checkPoints(mon []domain.Monitor, points ...domain.Point) error {
	for _, p := range points {
		if err := checkOnMonitor(mon, p); err != nil {
			return err
		}
		hit, ok, err := g.Windows.At(p)
		if err != nil {
			return err
		}
		if !ok {
			continue // どのウィンドウにもない（デスクトップ）
		}
		if err := domain.CheckInputTarget(hit.Top); err != nil {
			return err
		}
	}
	return nil
}

// checkOnMonitor は p がいずれかのモニター上にあることを確かめる（mon が空なら確かめない）。なければ ErrInvalidCoordinates。
// スコープ付きリースの検査（control_target.go）も同じ規則を使う。
func checkOnMonitor(mon []domain.Monitor, p domain.Point) error {
	if len(mon) > 0 {
		if _, ok := domain.MonitorAt(mon, p); !ok {
			return domain.ErrInvalidCoordinates.WithMsg(fmt.Sprintf("(%d, %d) is outside every monitor", p.X, p.Y))
		}
	}
	return nil
}

func (g InputGuard) checkForeground(rule func(domain.WindowInfo) error) error {
	f, err := g.Windows.Foreground()
	if err != nil {
		return err
	}
	return rule(f.Foreground)
}

// checkWindowByID は id のウィンドウに rule を適用する。存在しない id は、実行側（platform）が
// ErrWindowNotFound を返すのでここでは許可する。
func (g InputGuard) checkWindowByID(ctx context.Context, id string, rule func(domain.WindowInfo) error) error {
	windows, err := g.Windows.List(ctx)
	if err != nil {
		return err
	}
	for _, w := range windows {
		if w.ID == id {
			return rule(w)
		}
	}
	return nil
}
