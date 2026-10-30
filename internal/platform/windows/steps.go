package windows

import (
	"unicode/utf16"
	"unsafe"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// SendInput のフラグと種別（winuser.h）。
const (
	inputMouse    uint32 = 0
	inputKeyboard uint32 = 1

	keyeventfExtendedKey uint32 = 0x0001
	keyeventfKeyUp       uint32 = 0x0002
	keyeventfUnicode     uint32 = 0x0004

	mouseeventfMove        uint32 = 0x0001
	mouseeventfLeftDown    uint32 = 0x0002
	mouseeventfLeftUp      uint32 = 0x0004
	mouseeventfRightDown   uint32 = 0x0008
	mouseeventfRightUp     uint32 = 0x0010
	mouseeventfMiddleDown  uint32 = 0x0020
	mouseeventfMiddleUp    uint32 = 0x0040
	mouseeventfWheel       uint32 = 0x0800
	mouseeventfHWheel      uint32 = 0x1000
	mouseeventfVirtualDesk uint32 = 0x4000
	mouseeventfAbsolute    uint32 = 0x8000
)

// stepKind は inputStep の種別。
type stepKind uint8

const (
	stepKey   stepKind = iota // 仮想キー（vk / extended）の押下・解放
	stepRune                  // KEYEVENTF_UNICODE の 1 UTF-16 コード単位
	stepMouse                 // マウスの移動・ボタン・ホイール
)

// inputStep は SendInput の INPUT 1 件に対応する、Win32 に依存しない組み立ての単位。
type inputStep struct {
	kind stepKind
	up   bool // key / rune: 解放

	vk       uint16 // key
	extended bool   // key
	unit     uint16 // rune: UTF-16 のコード単位

	dx, dy int32  // mouse: 絶対移動のとき 0〜65535 の正規化座標
	data   uint32 // mouse: ホイールの移動量（符号つき int32 を uint32 で持つ）
	flags  uint32 // mouse: MOUSEEVENTF_*
}

func keyDownStep(k keySpec) inputStep {
	return inputStep{kind: stepKey, vk: k.VK, extended: k.Extended}
}
func keyUpStep(k keySpec) inputStep {
	return inputStep{kind: stepKey, vk: k.VK, extended: k.Extended, up: true}
}

func runeStep(unit uint16, up bool) inputStep {
	return inputStep{kind: stepRune, unit: unit, up: up}
}

// absMoveStep は仮想デスクトップ全体を 0〜65535 に正規化した座標へのマウス移動。
func absMoveStep(nx, ny int) inputStep {
	return inputStep{kind: stepMouse, dx: int32(nx), dy: int32(ny),
		flags: mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk}
}

// buttonStep はマウスボタンの押下・解放。未知のボタンは ok=false。
func buttonStep(b domain.Button, up bool) (inputStep, bool) {
	var down, release uint32
	switch b {
	case domain.ButtonLeft:
		down, release = mouseeventfLeftDown, mouseeventfLeftUp
	case domain.ButtonRight:
		down, release = mouseeventfRightDown, mouseeventfRightUp
	case domain.ButtonMiddle:
		down, release = mouseeventfMiddleDown, mouseeventfMiddleUp
	default:
		return inputStep{}, false
	}
	flags := down
	if up {
		flags = release
	}
	return inputStep{kind: stepMouse, flags: flags}, true
}

// wheelStep はホイール 1 イベント。delta は Win32 の向き（縦は正が上、横は正が右）。
func wheelStep(horizontal bool, delta int32) inputStep {
	flags := mouseeventfWheel
	if horizontal {
		flags = mouseeventfHWheel
	}
	return inputStep{kind: stepMouse, data: uint32(delta), flags: flags}
}

// keyDownSteps は keys を順に押す。
func keyDownSteps(keys []keySpec) []inputStep {
	steps := make([]inputStep, 0, len(keys))
	for _, k := range keys {
		steps = append(steps, keyDownStep(k))
	}
	return steps
}

// keyUpStepsReversed は keys を逆順に離す（押した順の逆）。
func keyUpStepsReversed(keys []keySpec) []inputStep {
	steps := make([]inputStep, 0, len(keys))
	for i := len(keys) - 1; i >= 0; i-- {
		steps = append(steps, keyUpStep(keys[i]))
	}
	return steps
}

// releaseSteps は steps の途中で失敗したときの後始末。steps が押下したキーとボタンを、押した順の逆に解放する。
// 実際には押されていないキーの解放が混じってもよい（余分な解放は無害）。同じキーは 1 回だけ。
func releaseSteps(steps []inputStep) []inputStep {
	var out []inputStep
	seen := make(map[keySpec]bool)
	seenButton := make(map[uint32]bool)
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		switch s.kind {
		case stepKey:
			k := keySpec{VK: s.vk, Extended: s.extended}
			if !s.up && !seen[k] {
				seen[k] = true
				out = append(out, keyUpStep(k))
			}
		case stepMouse:
			if up, ok := mouseUpFlag(s.flags); ok && !seenButton[up] {
				seenButton[up] = true
				out = append(out, inputStep{kind: stepMouse, flags: up})
			}
		}
	}
	return out
}

// mouseUpFlag はボタン押下のフラグに対応する解放のフラグを返す。押下以外は ok=false。
func mouseUpFlag(flags uint32) (uint32, bool) {
	switch flags {
	case mouseeventfLeftDown:
		return mouseeventfLeftUp, true
	case mouseeventfRightDown:
		return mouseeventfRightUp, true
	case mouseeventfMiddleDown:
		return mouseeventfMiddleUp, true
	}
	return 0, false
}

// textSteps は文字列を KEYEVENTF_UNICODE の入力列にする。サロゲートペアは 2 コード単位で送る。
// 改行（\r\n・\n・\r）は Enter キー、タブは Tab キーにする（Unicode のまま送ると受け付けないアプリがあるため）。
// NUL は捨てる。不正な UTF-8 は U+FFFD になる。
func textSteps(s string) []inputStep {
	var steps []inputStep
	stroke := func(k keySpec) {
		steps = append(steps, keyDownStep(k), keyUpStep(k))
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; r {
		case 0:
		case '\r':
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			stroke(keySpec{VK: vkReturn})
		case '\n':
			stroke(keySpec{VK: vkReturn})
		case '\t':
			stroke(keySpec{VK: vkTab})
		default:
			if r > 0xFFFF { // BMP 外のルーンはサロゲートペアの 2 コード単位
				hi, lo := utf16.EncodeRune(r)
				steps = append(steps, runeStep(uint16(hi), false), runeStep(uint16(hi), true),
					runeStep(uint16(lo), false), runeStep(uint16(lo), true))
				continue
			}
			unit := uint16(r)
			steps = append(steps, runeStep(unit, false), runeStep(unit, true))
		}
	}
	return steps
}

// --- SendInput の INPUT 構造体（Win32 の ABI） ---

// mouseInput は MOUSEINPUT。
type mouseInput struct {
	dx, dy      int32
	mouseData   uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

// keybdInput は KEYBDINPUT。
type keybdInput struct {
	vk          uint16
	scan        uint16
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

// winInput は INPUT（種別 + MOUSEINPUT / KEYBDINPUT の共用体）。共用体の大きさと配置は MOUSEINPUT に合わせる
// （最大のメンバーで、アラインメントも同じ）。大きさは 64 ビットで 40 バイト、32 ビットで 28 バイト。
type winInput struct {
	typ uint32
	mi  mouseInput
}

// encodeInputs は steps を INPUT の配列にする。scanCode は仮想キーのスキャンコード（Windows では MapVirtualKey。nil なら 0）。
func encodeInputs(steps []inputStep, scanCode func(vk uint16) uint16) []winInput {
	out := make([]winInput, len(steps))
	for i, s := range steps {
		switch s.kind {
		case stepMouse:
			out[i] = winInput{typ: inputMouse, mi: mouseInput{dx: s.dx, dy: s.dy, mouseData: s.data, flags: s.flags}}
		case stepKey:
			ki := keybdInput{vk: s.vk}
			if scanCode != nil {
				ki.scan = scanCode(s.vk)
			}
			if s.extended {
				ki.flags |= keyeventfExtendedKey
			}
			if s.up {
				ki.flags |= keyeventfKeyUp
			}
			out[i].typ = inputKeyboard
			*(*keybdInput)(unsafe.Pointer(&out[i].mi)) = ki
		case stepRune:
			ki := keybdInput{scan: s.unit, flags: keyeventfUnicode}
			if s.up {
				ki.flags |= keyeventfKeyUp
			}
			out[i].typ = inputKeyboard
			*(*keybdInput)(unsafe.Pointer(&out[i].mi)) = ki
		}
	}
	return out
}
