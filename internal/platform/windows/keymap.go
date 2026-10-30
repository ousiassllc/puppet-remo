package windows

import (
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 仮想キーコード（winuser.h の VK_*）。
const (
	vkBack     uint16 = 0x08
	vkTab      uint16 = 0x09
	vkReturn   uint16 = 0x0D
	vkShift    uint16 = 0x10
	vkControl  uint16 = 0x11
	vkMenu     uint16 = 0x12 // Alt
	vkPause    uint16 = 0x13
	vkCapital  uint16 = 0x14
	vkKana     uint16 = 0x15
	vkEscape   uint16 = 0x1B
	vkConvert  uint16 = 0x1C
	vkNonconv  uint16 = 0x1D
	vkSpace    uint16 = 0x20
	vkPrior    uint16 = 0x21
	vkNext     uint16 = 0x22
	vkEnd      uint16 = 0x23
	vkHome     uint16 = 0x24
	vkLeft     uint16 = 0x25
	vkUp       uint16 = 0x26
	vkRight    uint16 = 0x27
	vkDown     uint16 = 0x28
	vkSnapshot uint16 = 0x2C
	vkInsert   uint16 = 0x2D
	vkDelete   uint16 = 0x2E
	vkLWin     uint16 = 0x5B
	vkRWin     uint16 = 0x5C
	vkApps     uint16 = 0x5D
	vkNumpad0  uint16 = 0x60
	vkMultiply uint16 = 0x6A
	vkAdd      uint16 = 0x6B
	vkSubtract uint16 = 0x6D
	vkDecimal  uint16 = 0x6E
	vkDivide   uint16 = 0x6F
	vkF1       uint16 = 0x70
	vkNumLock  uint16 = 0x90
	vkScroll   uint16 = 0x91
	vkLShift   uint16 = 0xA0
	vkRShift   uint16 = 0xA1
	vkLControl uint16 = 0xA2
	vkRControl uint16 = 0xA3
	vkLMenu    uint16 = 0xA4
	vkRMenu    uint16 = 0xA5
	vkOem1     uint16 = 0xBA // ;:
	vkOemPlus  uint16 = 0xBB // =+
	vkOemComma uint16 = 0xBC
	vkOemMinus uint16 = 0xBD
	vkOemPer   uint16 = 0xBE
	vkOem2     uint16 = 0xBF // /?
	vkOem3     uint16 = 0xC0 // `~
	vkOem4     uint16 = 0xDB // [{
	vkOem5     uint16 = 0xDC // \|
	vkOem6     uint16 = 0xDD // ]}
	vkOem7     uint16 = 0xDE // '"
	vkOem102   uint16 = 0xE2
)

// keySpec は仮想キー 1 つ。Extended は SendInput の KEYEVENTF_EXTENDEDKEY を付けるキー（矢印・Home / End・右 Ctrl / Alt・Win など）。
type keySpec struct {
	VK       uint16
	Extended bool
}

// modifierKey は修飾キーを仮想キーへ変換する。左右を区別しない汎用の VK を使う。
func modifierKey(m domain.Modifier) (keySpec, bool) {
	switch m {
	case domain.ModCtrl:
		return keySpec{VK: vkControl}, true
	case domain.ModShift:
		return keySpec{VK: vkShift}, true
	case domain.ModAlt:
		return keySpec{VK: vkMenu}, true
	case domain.ModWin:
		return keySpec{VK: vkLWin, Extended: true}, true
	}
	return keySpec{}, false
}

var namedKeySpecs = map[string]keySpec{
	"enter":     {VK: vkReturn},
	"escape":    {VK: vkEscape},
	"tab":       {VK: vkTab},
	"backspace": {VK: vkBack},
	"delete":    {VK: vkDelete, Extended: true},
	"insert":    {VK: vkInsert, Extended: true},
	"home":      {VK: vkHome, Extended: true},
	"end":       {VK: vkEnd, Extended: true},
	"pageup":    {VK: vkPrior, Extended: true},
	"pagedown":  {VK: vkNext, Extended: true},
	"up":        {VK: vkUp, Extended: true},
	"down":      {VK: vkDown, Extended: true},
	"left":      {VK: vkLeft, Extended: true},
	"right":     {VK: vkRight, Extended: true},
	"space":     {VK: vkSpace},
	"capslock":  {VK: vkCapital},
}

// lookupKeyName は domain.ParseKeyCombo が受理するキー名のうち、キーボード配列に依存しないもの
// （名前つきキー、f1〜f24、英字、数字）を仮想キーへ変換する。小文字に正規化済みの名前を渡す。
// それ以外の記号は配列に依存するので、呼び出し側が resolveKeyName で配列に問い合わせる。
func lookupKeyName(name string) (keySpec, bool) {
	if spec, ok := namedKeySpecs[name]; ok {
		return spec, true
	}
	if spec, ok := functionKey(name, "f"); ok {
		return spec, true
	}
	if len(name) == 1 {
		return letterOrDigit(name[0])
	}
	return keySpec{}, false
}

// functionKey は prefix + 1〜24 の名前（f5、F12）を VK_F1〜VK_F24 へ変換する。
func functionKey(name, prefix string) (keySpec, bool) {
	if len(name) < len(prefix)+1 || !strings.EqualFold(name[:len(prefix)], prefix) {
		return keySpec{}, false
	}
	digits := name[len(prefix):]
	n := 0
	for _, c := range digits {
		if c < '0' || c > '9' || len(digits) > 2 {
			return keySpec{}, false
		}
		n = n*10 + int(c-'0')
	}
	if digits[0] == '0' || n < 1 || n > 24 { // 先頭の 0 は不可（f05）
		return keySpec{}, false
	}
	return keySpec{VK: vkF1 + uint16(n-1)}, true
}

// letterOrDigit は英小文字・大文字と数字の仮想キー（VK_A〜VK_Z は ASCII の大文字、VK_0〜VK_9 は ASCII の数字と同じ値）。
func letterOrDigit(c byte) (keySpec, bool) {
	switch {
	case c >= 'a' && c <= 'z':
		return keySpec{VK: uint16(c - 'a' + 'A')}, true
	case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return keySpec{VK: uint16(c)}, true
	}
	return keySpec{}, false
}

// layoutScan は 1 文字を入力できる仮想キーを、現在のキーボード配列から探す（Windows では VkKeyScanEx）。
// shift はその文字に Shift が要るか。Ctrl / Alt（AltGr）が要る文字は ok=false にする。
type layoutScan func(r rune) (vk uint16, shift bool, ok bool)

// resolveKeyName は domain.ParseKeyCombo が返す Key を仮想キーへ変換する。
// 配列に依存しない名前はそのまま、記号などは scan で探す。needShift はその記号に Shift が要ることを表す。
func resolveKeyName(name string, scan layoutScan) (spec keySpec, needShift bool, err error) {
	if spec, ok := lookupKeyName(name); ok {
		return spec, false, nil
	}
	runes := []rune(name)
	if len(runes) == 1 && scan != nil {
		if vk, shift, ok := scan(runes[0]); ok {
			return keySpec{VK: vk}, shift, nil
		}
	}
	return keySpec{}, false, domain.ErrInvalidArgument.WithMsg("key is not available on the current keyboard layout: " + name)
}

var codeKeySpecs = map[string]keySpec{
	"ShiftLeft":      {VK: vkLShift},
	"ShiftRight":     {VK: vkRShift},
	"ControlLeft":    {VK: vkLControl},
	"ControlRight":   {VK: vkRControl, Extended: true},
	"AltLeft":        {VK: vkLMenu},
	"AltRight":       {VK: vkRMenu, Extended: true},
	"MetaLeft":       {VK: vkLWin, Extended: true},
	"MetaRight":      {VK: vkRWin, Extended: true},
	"OSLeft":         {VK: vkLWin, Extended: true},
	"OSRight":        {VK: vkRWin, Extended: true},
	"ContextMenu":    {VK: vkApps, Extended: true},
	"Enter":          {VK: vkReturn},
	"NumpadEnter":    {VK: vkReturn, Extended: true},
	"Escape":         {VK: vkEscape},
	"Tab":            {VK: vkTab},
	"Backspace":      {VK: vkBack},
	"Delete":         {VK: vkDelete, Extended: true},
	"Insert":         {VK: vkInsert, Extended: true},
	"Home":           {VK: vkHome, Extended: true},
	"End":            {VK: vkEnd, Extended: true},
	"PageUp":         {VK: vkPrior, Extended: true},
	"PageDown":       {VK: vkNext, Extended: true},
	"ArrowUp":        {VK: vkUp, Extended: true},
	"ArrowDown":      {VK: vkDown, Extended: true},
	"ArrowLeft":      {VK: vkLeft, Extended: true},
	"ArrowRight":     {VK: vkRight, Extended: true},
	"Space":          {VK: vkSpace},
	"CapsLock":       {VK: vkCapital},
	"NumLock":        {VK: vkNumLock, Extended: true},
	"ScrollLock":     {VK: vkScroll},
	"Pause":          {VK: vkPause},
	"PrintScreen":    {VK: vkSnapshot, Extended: true},
	"NumpadAdd":      {VK: vkAdd},
	"NumpadSubtract": {VK: vkSubtract},
	"NumpadMultiply": {VK: vkMultiply},
	"NumpadDivide":   {VK: vkDivide, Extended: true},
	"NumpadDecimal":  {VK: vkDecimal},
	"Backquote":      {VK: vkOem3},
	"Minus":          {VK: vkOemMinus},
	"Equal":          {VK: vkOemPlus},
	"BracketLeft":    {VK: vkOem4},
	"BracketRight":   {VK: vkOem6},
	"Backslash":      {VK: vkOem5},
	"Semicolon":      {VK: vkOem1},
	"Quote":          {VK: vkOem7},
	"Comma":          {VK: vkOemComma},
	"Period":         {VK: vkOemPer},
	"Slash":          {VK: vkOem2},
	"IntlBackslash":  {VK: vkOem102},
	"IntlYen":        {VK: vkOem5},
	"IntlRo":         {VK: vkOem102},
	"Convert":        {VK: vkConvert},
	"NonConvert":     {VK: vkNonconv},
	"KanaMode":       {VK: vkKana},
}

// codeKey はブラウザの KeyboardEvent.code（"KeyA"、"ShiftLeft"、"ArrowUp" など）を仮想キーへ変換する。
// code は物理キーの位置を表すので、記号キーは配列に依らず同じ位置の OEM キーへ対応づける。未知の code は ok=false。
func codeKey(code string) (keySpec, bool) {
	if spec, ok := codeKeySpecs[code]; ok {
		return spec, true
	}
	switch {
	case len(code) == 4 && strings.HasPrefix(code, "Key"):
		if c := code[3]; c >= 'A' && c <= 'Z' {
			return letterOrDigit(c)
		}
	case len(code) == 6 && strings.HasPrefix(code, "Digit"):
		if c := code[5]; c >= '0' && c <= '9' {
			return letterOrDigit(c)
		}
	case len(code) == 7 && strings.HasPrefix(code, "Numpad"):
		if c := code[6]; c >= '0' && c <= '9' {
			return keySpec{VK: vkNumpad0 + uint16(c-'0')}, true
		}
	case strings.HasPrefix(code, "F"):
		return functionKey(code, "F")
	}
	return keySpec{}, false
}

// decodeVkScan は VkKeyScanEx の戻り値（下位バイトが仮想キー、上位バイトが修飾キーのビット: 1 = Shift、2 = Ctrl、4 = Alt）を解釈する。
// その文字が配列にない（-1）、または Ctrl / Alt（AltGr）が要る場合は ok=false。
func decodeVkScan(v int16) (vk uint16, shift bool, ok bool) {
	if v == -1 {
		return 0, false, false
	}
	state := uint8(uint16(v) >> 8)
	if state&^1 != 0 {
		return 0, false, false
	}
	return uint16(uint8(v)), state&1 != 0, true
}
