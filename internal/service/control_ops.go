package service

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// plan は検査済みで、座標を物理座標へ変換した実行計画。
type plan struct {
	op           domain.Op
	monitors     []domain.Monitor // 座標を持たない op では nil
	clickInPlace bool             // Click で At が省略された（現在位置でクリックし、移動しない）
}

// plan は op の引数を検査し、縮小座標を物理座標へ変換する（FR-M16）。
func (c *control) plan(ctx context.Context, op domain.Op) (plan, error) {
	switch o := op.(type) {
	case domain.MouseMove:
		return c.planPoints(ctx, o.Monitor, func(conv pointConv) (domain.Op, error) {
			at, err := conv(o.At)
			return domain.MouseMove{At: at, Monitor: o.Monitor}, err
		})
	case domain.Click:
		if !o.Button.Valid() {
			return plan{}, domain.ErrInvalidArgument.WithMsg("unknown button: " + string(o.Button))
		}
		if o.Count < minClickCount || o.Count > maxClickCount {
			return plan{}, domain.ErrInvalidArgument.WithMsg("click count must be 1 to 3")
		}
		if err := validateMods(o.Mods); err != nil {
			return plan{}, err
		}
		if o.At == nil {
			return c.planCurrentCursor(ctx, o)
		}
		return c.planPoints(ctx, o.Monitor, func(conv pointConv) (domain.Op, error) {
			at, err := conv(*o.At)
			o.At = &at
			return o, err
		})
	case domain.Drag:
		if err := validateMods(o.Mods); err != nil {
			return plan{}, err
		}
		return c.planPoints(ctx, o.Monitor, func(conv pointConv) (domain.Op, error) {
			from, err := conv(o.From)
			if err != nil {
				return nil, err
			}
			to, err := conv(o.To)
			o.From, o.To = from, to
			return o, err
		})
	case domain.Scroll:
		if !o.Dir.Valid() {
			return plan{}, domain.ErrInvalidArgument.WithMsg("unknown direction: " + string(o.Dir))
		}
		if o.Amount < minScrollAmount || o.Amount > maxScrollAmount {
			return plan{}, domain.ErrInvalidArgument.WithMsg("scroll amount must be 1 to 50")
		}
		return c.planPoints(ctx, o.Monitor, func(conv pointConv) (domain.Op, error) {
			at, err := conv(o.At)
			o.At = at
			return o, err
		})
	case domain.KeyPress:
		if err := o.Keys.Validate(); err != nil {
			return plan{}, err
		}
	case domain.TypeText:
		if utf8.RuneCountInString(o.Text) > maxTypeTextRunes {
			return plan{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("text is limited to %d characters", maxTypeTextRunes))
		}
	case domain.FocusWindow:
		if o.ID == "" {
			return plan{}, domain.ErrInvalidArgument.WithMsg("window id is required")
		}
	case domain.ClipboardRead:
	case domain.ClipboardWrite:
		if len(o.Text) > maxClipboardBytes {
			return plan{}, domain.ErrInvalidArgument.WithMsg("clipboard text is limited to 1 MiB")
		}
	case domain.GetUITree:
		if o.MaxDepth < 0 || o.MaxNodes < 0 {
			return plan{}, domain.ErrInvalidArgument.WithMsg("max_depth and max_nodes must not be negative")
		}
		if o.MaxDepth > maxUITreeDepth || o.MaxNodes > maxUITreeNodes {
			return plan{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("max_depth is limited to %d and max_nodes to %d", maxUITreeDepth, maxUITreeNodes))
		}
		if o.MaxDepth == 0 {
			o.MaxDepth = defaultUITreeDepth
		}
		if o.MaxNodes == 0 {
			o.MaxNodes = defaultUITreeNodes
		}
		return plan{op: o}, nil
	default:
		return plan{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("unsupported operation %T", op))
	}
	return plan{op: op}, nil
}

// pointConv は縮小座標を物理座標へ変換する。
type pointConv func(domain.Point) (domain.Point, error)

// planPoints は monitor（Index か MonitorPrimary）の縮小率で座標を変換する関数を build に渡し、その結果を計画にする。
func (c *control) planPoints(ctx context.Context, monitor int, build func(pointConv) (domain.Op, error)) (plan, error) {
	mons, err := c.capture.Monitors(ctx)
	if err != nil {
		return plan{}, err
	}
	mon, err := pickMonitor(mons, monitor)
	if err != nil {
		return plan{}, err
	}
	maxDim, err := c.MaxDimension(ctx)
	if err != nil {
		return plan{}, err
	}
	op, err := build(func(p domain.Point) (domain.Point, error) { return mon.ScaledToPhysical(p, maxDim) })
	if err != nil {
		return plan{}, err
	}
	return plan{op: op, monitors: mons}, nil
}

// planCurrentCursor は At を省略した Click の計画。現在のカーソル位置（物理座標）を宛先として InputGuard へ渡す。
func (c *control) planCurrentCursor(ctx context.Context, o domain.Click) (plan, error) {
	mons, err := c.capture.Monitors(ctx)
	if err != nil {
		return plan{}, err
	}
	pos, err := c.input.CursorPos()
	if err != nil {
		return plan{}, err
	}
	o.At = &pos
	return plan{op: o, monitors: mons, clickInPlace: true}, nil
}

// pickMonitor は monitor（Index、または MonitorPrimary）のモニターを返す。
func pickMonitor(mons []domain.Monitor, monitor int) (domain.Monitor, error) {
	for _, m := range mons {
		if monitor == domain.MonitorPrimary && m.Primary || monitor >= 0 && m.Index == monitor {
			return m, nil
		}
	}
	if monitor == domain.MonitorPrimary {
		return domain.Monitor{}, domain.ErrDesktopUnavailable.WithMsg("no primary monitor")
	}
	return domain.Monitor{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("unknown monitor %d", monitor))
}

func validateMods(mods []domain.Modifier) error {
	for _, m := range mods {
		if !m.Valid() {
			return domain.ErrInvalidArgument.WithMsg("unknown modifier: " + string(m))
		}
	}
	return nil
}

// lockInput は入力の実行（マウス・キーボード）を 1 度に 1 つへ直列化する。Click の「移動してからクリック」のように
// platform を複数回呼ぶ操作が、別の呼び出しの入力と混ざらないようにする。キルスイッチのクローザー
// （InputInjector.ReleaseAll）はこれを取らないため、実行中の入力があっても解放できる。
// 待っている間に ctx が終わるか、キルスイッチが発動したら、実行せずに返す。
func (c *control) lockInput(ctx context.Context) (unlock func(), err error) {
	select {
	case c.inputSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	unlock = func() { <-c.inputSem }
	if c.kill.Active() {
		unlock()
		return nil, domain.ErrKillSwitchActive
	}
	return unlock, nil
}

// execute は検査済みの計画を platform で実行する。
func (c *control) execute(ctx context.Context, p plan) (Result, error) {
	switch p.op.(type) {
	case domain.MouseMove, domain.Click, domain.Drag, domain.Scroll, domain.KeyPress, domain.TypeText:
		unlock, err := c.lockInput(ctx)
		if err != nil {
			return Result{}, err
		}
		defer unlock()
	}
	switch o := p.op.(type) {
	case domain.MouseMove:
		return Result{}, c.input.MoveTo(ctx, o.At)
	case domain.Click:
		if !p.clickInPlace {
			if err := c.input.MoveTo(ctx, *o.At); err != nil {
				return Result{}, err
			}
		}
		return Result{}, c.input.Click(ctx, o.Button, o.Count, o.Mods)
	case domain.Drag:
		return Result{}, c.input.Drag(ctx, o.From, o.To, o.Mods)
	case domain.Scroll:
		return Result{}, c.input.Scroll(ctx, o.At, o.Dir, o.Amount)
	case domain.KeyPress:
		return Result{}, c.input.Key(ctx, o.Keys)
	case domain.TypeText:
		return Result{}, c.input.Text(ctx, o.Text)
	case domain.FocusWindow:
		return Result{}, c.windows.Focus(ctx, o.ID)
	case domain.ClipboardRead:
		text, err := c.clipboard.ReadText(ctx)
		return Result{Text: truncateUTF8(text, maxClipboardBytes)}, err
	case domain.ClipboardWrite:
		return Result{}, c.clipboard.WriteText(ctx, o.Text)
	case domain.GetUITree:
		tree, err := c.ui.Tree(ctx, o.WindowID, o.MaxDepth, o.MaxNodes)
		if err != nil {
			return Result{}, err
		}
		return Result{Tree: &tree}, nil
	}
	return Result{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("unsupported operation %T", p.op))
}

// truncateUTF8 は s を n バイト以内に、文字の途中で切らずに切り詰める。
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// describeOp は監査のツール名と引数の要約を返す。縮小座標のまま（呼び出し側の指定どおり）を残す。
// type の本文・クリップボードの内容は入れず、文字数だけを入れる（FR-AU03、NFR-S13）。
func describeOp(op domain.Op, res Result) (tool string, args map[string]any) {
	switch o := op.(type) {
	case domain.MouseMove:
		return "mouse_move", map[string]any{"x": o.At.X, "y": o.At.Y, "monitor": o.Monitor}
	case domain.Click:
		args = map[string]any{"monitor": o.Monitor, "button": string(o.Button), "count": o.Count}
		if o.At != nil {
			args["x"], args["y"] = o.At.X, o.At.Y
		}
		if len(o.Mods) > 0 {
			args["modifiers"] = modNames(o.Mods)
		}
		return clickToolName(o), args
	case domain.Drag:
		args = map[string]any{
			"from":    map[string]int{"x": o.From.X, "y": o.From.Y},
			"to":      map[string]int{"x": o.To.X, "y": o.To.Y},
			"monitor": o.Monitor,
		}
		if len(o.Mods) > 0 {
			args["modifiers"] = modNames(o.Mods)
		}
		return "left_click_drag", args
	case domain.Scroll:
		return "scroll", map[string]any{"x": o.At.X, "y": o.At.Y, "direction": string(o.Dir), "amount": o.Amount, "monitor": o.Monitor}
	case domain.KeyPress:
		return "key", map[string]any{"keys": o.Keys.String()}
	case domain.TypeText:
		return "type", map[string]any{"text_len": utf8.RuneCountInString(o.Text)}
	case domain.FocusWindow:
		return "focus_window", map[string]any{"id": o.ID}
	case domain.ClipboardRead:
		return "clipboard_read", map[string]any{"text_len": utf8.RuneCountInString(res.Text)}
	case domain.ClipboardWrite:
		return "clipboard_write", map[string]any{"text_len": utf8.RuneCountInString(o.Text)}
	case domain.GetUITree:
		return "get_ui_tree", map[string]any{"window_id": o.WindowID, "max_depth": o.MaxDepth, "max_nodes": o.MaxNodes}
	case domain.Wait:
		return "wait", map[string]any{"ms": o.Duration.Milliseconds()}
	}
	return "unknown", map[string]any{"type": fmt.Sprintf("%T", op)}
}

// clickToolName は FR-M05 のツール名（left_click / right_click / middle_click / double_click / triple_click）。
func clickToolName(o domain.Click) string {
	if o.Button == domain.ButtonLeft {
		switch o.Count {
		case 2:
			return "double_click"
		case 3:
			return "triple_click"
		}
	}
	if !o.Button.Valid() {
		return "click"
	}
	return string(o.Button) + "_click"
}

func modNames(mods []domain.Modifier) []string {
	names := make([]string, len(mods))
	for i, m := range mods {
		names[i] = string(m)
	}
	return names
}
