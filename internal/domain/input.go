package domain

import (
	"strings"
	"unicode"
)

// Button はマウスボタン。
type Button string

const (
	ButtonLeft   Button = "left"
	ButtonRight  Button = "right"
	ButtonMiddle Button = "middle"
)

// Valid は b が既知のボタンかを返す。
func (b Button) Valid() bool {
	return b == ButtonLeft || b == ButtonRight || b == ButtonMiddle
}

// Modifier は修飾キー（API 仕様 §3.3）。
type Modifier string

const (
	ModCtrl  Modifier = "ctrl"
	ModShift Modifier = "shift"
	ModAlt   Modifier = "alt"
	ModWin   Modifier = "win"
)

// Valid は m が既知の修飾キーかを返す。
func (m Modifier) Valid() bool {
	return m == ModCtrl || m == ModShift || m == ModAlt || m == ModWin
}

// Direction はスクロールの方向。
type Direction string

const (
	DirUp    Direction = "up"
	DirDown  Direction = "down"
	DirLeft  Direction = "left"
	DirRight Direction = "right"
)

// Valid は d が既知の方向かを返す。
func (d Direction) Valid() bool {
	return d == DirUp || d == DirDown || d == DirLeft || d == DirRight
}

// KeyCombo は修飾キーと 1 つのキーの組（`ctrl+shift+t`）。Key は小文字の名前。
type KeyCombo struct {
	Mods []Modifier
	Key  string
}

// KeyAction はキーの押下か解放か。
type KeyAction string

const (
	KeyDown KeyAction = "down"
	KeyUp   KeyAction = "up"
)

// Valid は a が既知の操作かを返す。
func (a KeyAction) Valid() bool { return a == KeyDown || a == KeyUp }

// KeyEvent はキー 1 つの down / up（Web UI の input.key。API 仕様 §5.3）。
type KeyEvent struct {
	Action KeyAction
	Code   string // ブラウザの KeyboardEvent.code（例: "KeyA"）
	Key    string // KeyboardEvent.key（例: "a"）
	Mods   []Modifier
}

var namedKeys = map[string]bool{
	"enter": true, "escape": true, "tab": true, "backspace": true, "delete": true, "insert": true,
	"home": true, "end": true, "pageup": true, "pagedown": true,
	"up": true, "down": true, "left": true, "right": true, "space": true, "capslock": true,
}

// ValidKeyName は name が `keys` の書式（API 仕様 §3.3）のキー名かを返す。
// 名前つきキー、f1〜f24、1 文字の英数字・記号（"+" は区切りなので除く）を受理する。小文字に正規化済みの名前を渡す。
func ValidKeyName(name string) bool {
	if namedKeys[name] {
		return true
	}
	if len(name) >= 2 && name[0] == 'f' {
		n := 0
		for _, c := range name[1:] {
			if c < '0' || c > '9' {
				return false
			}
			n = n*10 + int(c-'0')
			if n > 24 {
				return false
			}
		}
		return n >= 1 && name[1] != '0'
	}
	runes := []rune(name)
	if len(runes) != 1 {
		return false
	}
	r := runes[0]
	return r != '+' && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsPunct(r) || unicode.IsSymbol(r))
}

// Validate は k が送れる形かを検査する。違反は ErrInvalidArgument。
func (k KeyCombo) Validate() error {
	if !ValidKeyName(k.Key) {
		return ErrInvalidArgument.WithMsg("unknown key: " + k.Key)
	}
	for _, m := range k.Mods {
		if !m.Valid() {
			return ErrInvalidArgument.WithMsg("unknown modifier: " + string(m))
		}
	}
	return nil
}

// String は ParseKeyCombo が受理する書式（`ctrl+shift+t`）で返す。
func (k KeyCombo) String() string {
	parts := make([]string, 0, len(k.Mods)+1)
	for _, m := range k.Mods {
		parts = append(parts, string(m))
	}
	return strings.Join(append(parts, k.Key), "+")
}

// ParseKeyCombo は `keys` の書式（API 仕様 §3.3）を解釈する。
// 大文字小文字は区別しない。キーは最後の 1 つだけで、その前に修飾キーを置く（重複は 1 つにまとめる）。
// "+" 自体はキーにできない（区切りのため）。未知のトークン・空のトークンは ErrInvalidArgument。
// ctrl+alt+delete（SAS）の拒否はここでは行わない（service.InputGuard の責務）。
func ParseKeyCombo(s string) (KeyCombo, error) {
	tokens := strings.Split(strings.ToLower(strings.TrimSpace(s)), "+")
	combo := KeyCombo{Key: tokens[len(tokens)-1]}
	for _, t := range tokens[:len(tokens)-1] {
		m := Modifier(t)
		if !m.Valid() {
			return KeyCombo{}, ErrInvalidArgument.WithMsg("unknown modifier: " + t)
		}
		if !containsModifier(combo.Mods, m) {
			combo.Mods = append(combo.Mods, m)
		}
	}
	if err := combo.Validate(); err != nil {
		return KeyCombo{}, err
	}
	return combo, nil
}

func containsModifier(mods []Modifier, m Modifier) bool {
	for _, x := range mods {
		if x == m {
			return true
		}
	}
	return false
}
