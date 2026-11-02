package windows

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.InputInjector = (*Input)(nil)

// textChunk は Text が 1 回の SendInput で送るコード単位の目安（INPUT は 1 文字あたり 2 件）。
const textChunk = 256

// driver は Input が Win32 に頼る部分。Windows では SendInput などの実装を、テストでは偽の実装を使う。
// すべての呼び出しは Input の専用スレッドから行われる。
type driver interface {
	// send は steps を 1 回の SendInput で注入する。一部しか入らなかった場合はエラー。
	// 昇格ウィンドウなどに UIPI で拒否されたら ErrInputBlocked、デスクトップを使えなければ ErrDesktopUnavailable。
	send(steps []inputStep) error
	cursorPos() (domain.Point, error)
	setCursorPos(p domain.Point) error
	// virtualScreen は仮想デスクトップ全体の矩形（物理座標）。
	virtualScreen() (domain.Rect, error)
	// scanKey は 1 文字を入力できる仮想キーを現在の配列から探す。
	scanKey(r rune) (vk uint16, shift bool, ok bool)
}

// Input は platform.InputInjector の Windows 実装（SendInput）。
// 呼び出しは専用の OS スレッドに直列化され、押下中のキーとホイールの端数はこのスレッドだけが触る。
type Input struct {
	drv    driver
	runner *threadRunner

	// 以下は専用スレッドだけが読み書きする。
	held    []keySpec       // KeyEvent で押下中のキー（押した順）
	buttons []domain.Button // Button で押下中のボタン（押した順）
	carry   wheelCarry
}

func newInput(drv driver) *Input {
	return &Input{drv: drv, runner: newThreadRunner()}
}

// Close は専用スレッドを止める。押下中のキーは解放する。
func (in *Input) Close() error {
	err := in.ReleaseAll()
	in.runner.close()
	return err
}

func (in *Input) MoveTo(ctx context.Context, p domain.Point) error {
	return in.runner.do(ctx, func(context.Context) error { return in.moveTo(p) })
}

func (in *Input) Click(ctx context.Context, b domain.Button, count int, mods []domain.Modifier) error {
	return in.runner.do(ctx, func(context.Context) error {
		if !b.Valid() {
			return domain.ErrInvalidArgument.WithMsg("unknown button: " + string(b))
		}
		if count < 1 || count > 3 {
			return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("click count must be 1 to 3: %d", count))
		}
		modKeys, err := in.modifierKeys(mods)
		if err != nil {
			return err
		}
		down, _ := buttonStep(b, false)
		up, _ := buttonStep(b, true)
		steps := keyDownSteps(modKeys)
		for range count {
			steps = append(steps, down, up)
		}
		return in.sendBalanced(append(steps, keyUpStepsReversed(modKeys)...))
	})
}

// Button は b の down / up を 1 つ送る。押下中のボタンは覚え、ReleaseAll が離す。
func (in *Input) Button(ctx context.Context, b domain.Button, a domain.ButtonAction) error {
	return in.runner.do(ctx, func(context.Context) error {
		if !b.Valid() {
			return domain.ErrInvalidArgument.WithMsg("unknown button: " + string(b))
		}
		if !a.Valid() {
			return domain.ErrInvalidArgument.WithMsg("unknown button action: " + string(a))
		}
		step, _ := buttonStep(b, a == domain.ButtonActionUp)
		if err := in.drv.send([]inputStep{step}); err != nil {
			return err
		}
		if a == domain.ButtonActionUp {
			in.buttons = slices.DeleteFunc(in.buttons, func(x domain.Button) bool { return x == b })
		} else if !slices.Contains(in.buttons, b) {
			in.buttons = append(in.buttons, b)
		}
		return nil
	})
}

func (in *Input) Drag(ctx context.Context, from, to domain.Point, mods []domain.Modifier) error {
	return in.runner.do(ctx, func(ctx context.Context) error {
		modKeys, err := in.modifierKeys(mods)
		if err != nil {
			return err
		}
		screen, err := in.drv.virtualScreen()
		if err != nil {
			return err
		}
		if err := checkPoints(screen, from, to); err != nil {
			return err
		}
		if err := in.moveTo(from); err != nil {
			return err
		}
		down, _ := buttonStep(domain.ButtonLeft, false)
		up, _ := buttonStep(domain.ButtonLeft, true)
		press := append(keyDownSteps(modKeys), down)
		if err := in.sendBalanced(press); err != nil {
			return err
		}
		// 押下した後は、どう終わっても解放する。
		err = in.dragMoves(ctx, screen, from, to)
		release := append([]inputStep{up}, keyUpStepsReversed(modKeys)...)
		if rerr := in.drv.send(release); err == nil {
			err = rerr
		}
		return err
	})
}

// dragMoves は押下中の from から to まで途中の点を通って移動し、to で止まる。
func (in *Input) dragMoves(ctx context.Context, screen domain.Rect, from, to domain.Point) error {
	for _, p := range dragPath(from, to) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := in.drv.send([]inputStep{absMoveStep(normalizeAbs(p.X, screen.X, screen.W), normalizeAbs(p.Y, screen.Y, screen.H))}); err != nil {
			return err
		}
	}
	return in.settleCursor(to)
}

func (in *Input) Scroll(ctx context.Context, at domain.Point, dir domain.Direction, amount int) error {
	return in.runner.do(ctx, func(context.Context) error {
		dx, dy, ok := scrollNotches(dir, amount)
		if !ok {
			return domain.ErrInvalidArgument.WithMsg("unknown direction: " + string(dir))
		}
		if amount < 1 || amount > maxWheelNotches {
			return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("scroll amount must be 1 to %d: %d", maxWheelNotches, amount))
		}
		return in.wheel(at, dx, dy)
	})
}

func (in *Input) Wheel(ctx context.Context, at domain.Point, dx, dy float64) error {
	return in.runner.do(ctx, func(context.Context) error {
		for _, v := range []float64{dx, dy} {
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > maxWheelNotches {
				return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("wheel delta must be finite and within ±%d notches", maxWheelNotches))
			}
		}
		return in.wheel(at, dx, dy)
	})
}

// wheel は at へ移動してから、dx / dy ノッチ分のホイールを回す。
func (in *Input) wheel(at domain.Point, dx, dy float64) error {
	if err := in.moveTo(at); err != nil {
		return err
	}
	h, v := in.carry.take(dx, dy)
	steps := append(wheelSteps(false, v), wheelSteps(true, h)...)
	if len(steps) == 0 {
		return nil
	}
	return in.drv.send(steps)
}

func (in *Input) Key(ctx context.Context, k domain.KeyCombo) error {
	return in.runner.do(ctx, func(context.Context) error {
		if err := k.Validate(); err != nil {
			return err
		}
		key, needShift, err := resolveKeyName(k.Key, in.drv.scanKey)
		if err != nil {
			return err
		}
		mods := k.Mods
		if needShift && !slices.Contains(mods, domain.ModShift) {
			mods = append(slices.Clone(mods), domain.ModShift)
		}
		modKeys, err := in.modifierKeys(mods)
		if err != nil {
			return err
		}
		steps := append(keyDownSteps(modKeys), keyDownStep(key), keyUpStep(key))
		return in.sendBalanced(append(steps, keyUpStepsReversed(modKeys)...))
	})
}

// KeyEvent は 1 つのキーの down / up を送る。e.Mods は無視する（修飾キーそのものの down / up が別のイベントで届くため）。
func (in *Input) KeyEvent(ctx context.Context, e domain.KeyEvent) error {
	return in.runner.do(ctx, func(context.Context) error {
		if !e.Action.Valid() {
			return domain.ErrInvalidArgument.WithMsg("unknown key action: " + string(e.Action))
		}
		if e.Code == "" {
			return domain.ErrInvalidArgument.WithMsg("key code is empty")
		}
		key, ok := codeKey(e.Code)
		if !ok {
			return domain.ErrInvalidArgument.WithMsg("unknown key code: " + e.Code)
		}
		if e.Action == domain.KeyUp {
			if err := in.drv.send([]inputStep{keyUpStep(key)}); err != nil {
				return err
			}
			in.held = slices.DeleteFunc(in.held, func(k keySpec) bool { return k == key })
			return nil
		}
		if err := in.drv.send([]inputStep{keyDownStep(key)}); err != nil {
			return err
		}
		if !slices.Contains(in.held, key) {
			in.held = append(in.held, key)
		}
		return nil
	})
}

func (in *Input) Text(ctx context.Context, s string) error {
	return in.runner.do(ctx, func(ctx context.Context) error {
		steps := textSteps(s)
		for len(steps) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			n := textChunkLen(steps, 2*textChunk)
			if err := in.sendBalanced(steps[:n]); err != nil {
				return err
			}
			steps = steps[n:]
		}
		return nil
	})
}

// textChunkLen は steps の先頭から 1 回の SendInput で送る件数を返す。limit 件を目安に、1 文字を分割しない位置まで進める。
// 1 文字は down + up（サロゲートペアは上位 → 下位の 4 件）で、切れるのは文字の最後の解放の直後だけ。
// 上位サロゲートの解放の直後で切ると、ペアが別々の SendInput に分かれて文字化けする。
func textChunkLen(steps []inputStep, limit int) int {
	n := min(len(steps), limit)
	for n < len(steps) && !endsCharacter(steps[n-1]) {
		n++
	}
	return n
}

// endsCharacter は s が 1 文字の最後の入力か（解放で、かつ上位サロゲートの解放ではない）。
func endsCharacter(s inputStep) bool {
	isHighSurrogate := s.kind == stepRune && s.unit >= 0xD800 && s.unit < 0xDC00
	return s.up && !isHighSurrogate
}

// ReleaseAll は Button で押下中のボタンと KeyEvent で押下中のキーを解放し、ホイールの端数を捨てる。
// 押した順の逆に（ボタンを先、キーを後）、1 回の SendInput で離す。
// ctx を取らず、取り消されずに実行する（中断・キルスイッチ時に呼ばれる）。解放に失敗したキーは押下中のまま残す。
func (in *Input) ReleaseAll() error {
	return in.runner.do(context.Background(), func(context.Context) error {
		in.carry.reset()
		var steps []inputStep
		for i := len(in.buttons) - 1; i >= 0; i-- {
			step, _ := buttonStep(in.buttons[i], true)
			steps = append(steps, step)
		}
		steps = append(steps, keyUpStepsReversed(in.held)...)
		if len(steps) == 0 {
			return nil
		}
		if err := in.drv.send(steps); err != nil {
			return err
		}
		in.buttons, in.held = nil, nil
		return nil
	})
}

func (in *Input) CursorPos() (domain.Point, error) {
	var p domain.Point
	err := in.runner.do(context.Background(), func(context.Context) error {
		var err error
		p, err = in.drv.cursorPos()
		return err
	})
	return p, err
}

// moveTo はカーソルを p へ動かす。p は仮想デスクトップの内側でなければならない（外は ErrInvalidCoordinates）。
func (in *Input) moveTo(p domain.Point) error {
	screen, err := in.drv.virtualScreen()
	if err != nil {
		return err
	}
	if err := checkPoints(screen, p); err != nil {
		return err
	}
	step := absMoveStep(normalizeAbs(p.X, screen.X, screen.W), normalizeAbs(p.Y, screen.Y, screen.H))
	if err := in.drv.send([]inputStep{step}); err != nil {
		return err
	}
	return in.settleCursor(p)
}

// settleCursor は絶対座標の丸めで 1 画素ずれたときだけ、SetCursorPos で p に合わせる。
func (in *Input) settleCursor(p domain.Point) error {
	got, err := in.drv.cursorPos()
	if err != nil {
		return err
	}
	if got == p {
		return nil
	}
	return in.drv.setCursorPos(p)
}

func checkPoints(screen domain.Rect, points ...domain.Point) error {
	for _, p := range points {
		if !screen.Contains(p) {
			return domain.ErrInvalidCoordinates.WithMsg(fmt.Sprintf("(%d, %d) is outside the virtual desktop", p.X, p.Y))
		}
	}
	return nil
}

// modifierKeys は mods の仮想キーを返す（重複は 1 つに、KeyEvent で押下中のものは除く）。未知の修飾キーは ErrInvalidArgument。
// 押下中のキーを除くのは、呼び出しの終わりに解放して、利用者が押し続けているキーを離さないため。
func (in *Input) modifierKeys(mods []domain.Modifier) ([]keySpec, error) {
	var keys []keySpec
	for _, m := range mods {
		k, ok := modifierKey(m)
		if !ok {
			return nil, domain.ErrInvalidArgument.WithMsg("unknown modifier: " + string(m))
		}
		if !slices.Contains(keys, k) && !in.holds(k) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// holds は k または同じ種類の修飾キー（左右どちらか）が KeyEvent で押下中かを返す。
func (in *Input) holds(k keySpec) bool {
	for _, h := range in.held {
		if h == k || (modifierFamily(k.VK) != 0 && modifierFamily(h.VK) == modifierFamily(k.VK)) {
			return true
		}
	}
	return false
}

// modifierFamily は左右の修飾キーを同じ値にまとめる（修飾キーでなければ 0）。
func modifierFamily(vk uint16) uint16 {
	switch vk {
	case vkShift, vkLShift, vkRShift:
		return vkShift
	case vkControl, vkLControl, vkRControl:
		return vkControl
	case vkMenu, vkLMenu, vkRMenu:
		return vkMenu
	case vkLWin, vkRWin:
		return vkLWin
	}
	return 0
}

// sendBalanced は steps を 1 回で送る。失敗したら、steps が押したキーとボタンを解放してからエラーを返す。
func (in *Input) sendBalanced(steps []inputStep) error {
	err := in.drv.send(steps)
	if err != nil {
		if release := releaseSteps(steps); len(release) > 0 {
			_ = in.drv.send(release)
		}
	}
	return err
}
