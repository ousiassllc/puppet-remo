package service

import (
	"context"
	"fmt"
	"math"
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
	case domain.PointerMove:
		return c.planStream(ctx, o.Monitor, func(conv pointConv) (domain.Op, error) {
			at, err := conv(o.At)
			o.At = at
			return o, err
		})
	case domain.MouseButton:
		if !o.Button.Valid() {
			return plan{}, domain.ErrInvalidArgument.WithMsg("unknown button: " + string(o.Button))
		}
		if !o.Action.Valid() {
			return plan{}, domain.ErrInvalidArgument.WithMsg("unknown button action: " + string(o.Action))
		}
		return c.planStream(ctx, o.Monitor, func(conv pointConv) (domain.Op, error) {
			at, err := conv(o.At)
			o.At = at
			return o, err
		})
	case domain.Wheel:
		if err := checkWheelNotches(o.DX, o.DY); err != nil {
			return plan{}, err
		}
		return c.planStream(ctx, o.Monitor, func(conv pointConv) (domain.Op, error) {
			at, err := conv(o.At)
			o.At = at
			return o, err
		})
	case domain.KeyEvent:
		if !o.Action.Valid() {
			return plan{}, domain.ErrInvalidArgument.WithMsg("unknown key action: " + string(o.Action))
		}
		if o.Code == "" {
			return plan{}, domain.ErrInvalidArgument.WithMsg("key code is required")
		}
		if err := validateMods(o.Mods); err != nil {
			return plan{}, err
		}
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

// planStream は monitor（Index か MonitorPrimary）の座標系（モニターの物理ピクセル。Web UI のストリームの座標）で
// 座標を変換する関数を build に渡し、その結果を計画にする。変換後は仮想デスクトップの物理座標。
func (c *control) planStream(ctx context.Context, monitor int, build func(pointConv) (domain.Op, error)) (plan, error) {
	mons, err := c.capture.Monitors(ctx)
	if err != nil {
		return plan{}, err
	}
	mon, err := pickMonitor(mons, monitor)
	if err != nil {
		return plan{}, err
	}
	op, err := build(mon.StreamToPhysical)
	if err != nil {
		return plan{}, err
	}
	return plan{op: op, monitors: mons}, nil
}

// checkWheelNotches は web のホイールの 1 メッセージの上限（絶対値 50 ノッチ。API 仕様 §5.3）を検査する。
func checkWheelNotches(dx, dy float64) error {
	for _, v := range []float64{dx, dy} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > maxScrollAmount {
			return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("wheel delta must be finite and at most %d notches", maxScrollAmount))
		}
	}
	return nil
}

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
	conv := func(p domain.Point) (domain.Point, error) { return p, nil } // 変換済みの物理座標（WithPhysicalCoords）
	if !physicalCoords(ctx) {
		maxDim, err := c.MaxDimension(ctx)
		if err != nil {
			return plan{}, err
		}
		conv = func(p domain.Point) (domain.Point, error) { return mon.ScaledToPhysical(p, maxDim) }
	}
	op, err := build(conv)
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

// execute は検査済みの計画を platform で実行する。sr が非 nil（スコープ付きリースの入力）なら、lockInput の中で、注入の直前と、1 つの操作の
// 複数回の注入の間に、対象グループの検証（verifyInput。control_target.go）を行う。通らなければ、そこから先は注入しない（ReleaseAll・halted）。
func (c *control) execute(ctx context.Context, p plan, sr *scopeRun) (Result, error) {
	switch p.op.(type) {
	case domain.MouseMove, domain.Click, domain.Drag, domain.Scroll, domain.KeyPress, domain.TypeText,
		domain.PointerMove, domain.MouseButton, domain.Wheel, domain.KeyEvent:
		unlock, err := c.lockInput(ctx)
		if err != nil {
			return Result{}, err
		}
		defer unlock()
	case domain.FocusWindow:
		if sr != nil {
			unlock, err := c.lockInput(ctx)
			if err != nil {
				return Result{}, err
			}
			defer unlock()
		}
	}
	atPoints := scopeCheck{points: opPoints(p.op)}
	verify := func(chk scopeCheck) error { return c.verifyInput(ctx, sr, chk) }
	switch o := p.op.(type) {
	case domain.MouseMove:
		if err := verify(atPoints); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.MoveTo(ctx, o.At)
	case domain.Click:
		chk := atPoints
		if !p.clickInPlace {
			if err := verify(atPoints); err != nil {
				return Result{}, err
			}
			if err := c.input.MoveTo(ctx, *o.At); err != nil {
				return Result{}, err
			}
			sr.markInjected()
		} else if sr != nil {
			// 現在位置でのクリックは、plan の時点の位置（lockInput の外で読んだもの）ではなく、注入の直前に読み直した位置を検証する。
			pos, err := c.input.CursorPos()
			if err != nil {
				return Result{}, err
			}
			chk = scopeCheck{points: []domain.Point{pos}}
		}
		if err := verify(chk); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.Click(ctx, o.Button, o.Count, o.Mods)
	case domain.Drag:
		if err := verify(atPoints); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.Drag(ctx, o.From, o.To, o.Mods)
	case domain.Scroll:
		if err := verify(atPoints); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.Scroll(ctx, o.At, o.Dir, o.Amount)
	case domain.PointerMove:
		if err := verify(atPoints); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.MoveTo(ctx, o.At)
	case domain.MouseButton:
		if err := verify(atPoints); err != nil {
			return Result{}, err
		}
		if err := c.input.MoveTo(ctx, o.At); err != nil {
			return Result{}, err
		}
		sr.markInjected()
		if err := verify(atPoints); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.Button(ctx, o.Button, o.Action)
	case domain.Wheel:
		if err := verify(atPoints); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.Wheel(ctx, o.At, o.DX, o.DY)
	case domain.KeyEvent:
		if err := verify(scopeCheck{focus: true}); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.KeyEvent(ctx, o)
	case domain.KeyPress:
		if err := verify(scopeCheck{focus: true}); err != nil {
			return Result{}, err
		}
		return Result{}, c.input.Key(ctx, o.Keys)
	case domain.TypeText:
		if sr != nil {
			return Result{}, c.typeScoped(ctx, sr, o.Text)
		}
		return Result{}, c.input.Text(ctx, o.Text)
	case domain.FocusWindow:
		if err := verify(scopeCheck{focusID: o.ID}); err != nil {
			return Result{}, err
		}
		return Result{}, c.windows.Focus(ctx, o.ID)
	case domain.ClipboardRead:
		text, err := c.clipboard.ReadText(ctx)
		return Result{Text: truncateUTF8(text, maxClipboardBytes)}, err
	case domain.ClipboardWrite:
		if err := c.scopeActive(sr); err != nil {
			return Result{}, err
		}
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
	case domain.MouseButton:
		return "mouse_" + string(o.Action), map[string]any{"x": o.At.X, "y": o.At.Y, "button": string(o.Button), "monitor": o.Monitor}
	case domain.Wheel:
		return "wheel", map[string]any{"x": o.At.X, "y": o.At.Y, "dx": o.DX, "dy": o.DY, "monitor": o.Monitor}
	case domain.KeyEvent:
		// キーの名前（code・key）は記録しない（パスワードなどの入力を監査ログに残さない。FR-AU03 の type と同じ扱い）。
		args = map[string]any{"action": string(o.Action)}
		if len(o.Mods) > 0 {
			args["modifiers"] = modNames(o.Mods)
		}
		return "key_" + string(o.Action), args
	case domain.KeyPress:
		return "key", keyPressArgs(o.Keys)
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

// recordedTextMaxRunes は audit.record_text が true のとき detail_json に入れる本文の上限（文字数）。
// type は 10,000 文字、クリップボードは 1 MiB まで受け付けるが、監査の行を肥大させないため切り詰める。
const recordedTextMaxRunes = 4096

// keyPressRedacted は監査の keys に、表示できる 1 文字のキーの代わりに入れる印。
const keyPressRedacted = "[char]"

// isTypedCharacterKey は k のキーが表示できる 1 文字（文字・数字・記号）か。パスワードなどを 1 文字ずつ key で打てるので、
// type の本文と同じく監査には残さない（FR-AU03）。enter・tab・escape・方向キー・f1 などの名前つきのキーと、修飾キーは名前を残す。
func isTypedCharacterKey(k domain.KeyCombo) bool {
	return utf8.RuneCountInString(k.Key) == 1
}

// keyPressArgs は key の監査の引数。表示できる 1 文字のキーは keys にその文字を入れず（`ctrl+[char]`）、文字数 text_len = 1 だけを残す。
// 本文は設定 audit.record_text が true のときだけ addRecordedText が text に足す。
func keyPressArgs(k domain.KeyCombo) map[string]any {
	if !isTypedCharacterKey(k) {
		return map[string]any{"keys": k.String()}
	}
	masked := k
	masked.Key = keyPressRedacted
	return map[string]any{"keys": masked.String(), "text_len": 1}
}

// addRecordedText は、設定 audit.record_text（既定 false）が true のとき、type の本文とクリップボードの内容（clipboard_read の結果を含む）
// と、key の表示できる 1 文字（keyPressArgs が伏せたもの）を args["text"] に足す（FR-AU03、FR-S07）。トークン（prm_…）・Bearer の形の文字列は、本文の中にあっても先に伏せる（トークンはどの設定でも記録しない。NFR-S13）。
// 伏せたあとの文字列が recordedTextMaxRunes を超えるときは切り詰めて args["text_truncated"] = true を付ける（text_len は元の長さ）。
// 設定が読めない・真偽値でないときは記録しない側に倒す。
func (c *control) addRecordedText(ctx context.Context, op domain.Op, res Result, args map[string]any) {
	var text string
	switch o := op.(type) {
	case domain.TypeText:
		text = o.Text
	case domain.ClipboardWrite:
		text = o.Text
	case domain.ClipboardRead:
		text = res.Text
	case domain.KeyPress:
		if !isTypedCharacterKey(o.Keys) {
			return
		}
		text = o.Keys.Key
	default:
		return
	}
	if text == "" {
		return
	}
	if on, err := loadBool(ctx, c.settings, settingAuditRecordText, false); err != nil || !on {
		return
	}
	// 伏せてから切り詰める。先に切ると、境界をまたぐトークンが形の最小長を割って伏せられずに残る（NFR-S13）。
	text = auditRedactString(text)
	if cut, ok := cutAtRunes(text, recordedTextMaxRunes); ok {
		text = cut
		args["text_truncated"] = true
	}
	args["text"] = text
}

// cutAtRunes は s の先頭 limit ルーンを返す。切り詰めたときだけ ok が true。s 全体のルーン列は作らない。
func cutAtRunes(s string, limit int) (string, bool) {
	n := 0
	for i := range s {
		if n == limit {
			return s[:i], true
		}
		n++
	}
	return s, false
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
