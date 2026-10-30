package windows

import (
	"fmt"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// RegisterHotKey の fsModifiers。
const (
	hotkeyModAlt     uint32 = 0x0001
	hotkeyModControl uint32 = 0x0002
	hotkeyModShift   uint32 = 0x0004
	hotkeyModWin     uint32 = 0x0008
)

// hotkeyChord は RegisterHotKey に渡す修飾キーの集合と仮想キーコード。同じ組み合わせは spec の書き方によらず等しい。
type hotkeyChord struct {
	Mods uint32
	VK   uint32
}

// hotkeyNamedVK は名前つきキー（domain.ValidKeyName が受理するもの）の仮想キーコード。
var hotkeyNamedVK = map[string]uint32{
	"backspace": 0x08, "tab": 0x09, "enter": 0x0D, "capslock": 0x14, "escape": 0x1B, "space": 0x20,
	"pageup": 0x21, "pagedown": 0x22, "end": 0x23, "home": 0x24,
	"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28,
	"insert": 0x2D, "delete": 0x2E,
}

// hotkeyPunctVK は US 配列の記号キーの仮想キーコード（VK_OEM_*）。
var hotkeyPunctVK = map[rune]uint32{
	';': 0xBA, '=': 0xBB, ',': 0xBC, '-': 0xBD, '.': 0xBE, '/': 0xBF,
	'`': 0xC0, '[': 0xDB, '\\': 0xDC, ']': 0xDD, '\'': 0xDE,
}

// hotkeyVK は小文字に正規化したキー名の仮想キーコードを返す。対応しないキーは ok=false。
func hotkeyVK(key string) (vk uint32, ok bool) {
	if vk, ok := hotkeyNamedVK[key]; ok {
		return vk, true
	}
	if len(key) >= 2 && key[0] == 'f' { // f1〜f24（形式は domain.ValidKeyName が検査済み）
		n := uint32(0)
		for _, c := range key[1:] {
			n = n*10 + uint32(c-'0')
		}
		return 0x70 + n - 1, true // VK_F1 = 0x70
	}
	runes := []rune(key)
	if len(runes) != 1 {
		return 0, false
	}
	switch r := runes[0]; {
	case r >= 'a' && r <= 'z':
		return uint32(r-'a') + 'A', true // VK_A〜VK_Z は大文字の ASCII と同じ
	case r >= '0' && r <= '9':
		return uint32(r), true
	default:
		vk, ok := hotkeyPunctVK[r]
		return vk, ok
	}
}

// hotkeyParse は `Ctrl+Alt+Shift+K` の形式（修飾キーとキーを "+" で連結。大文字小文字は区別しない）を解釈する。
// 修飾キーが 1 つもない spec は、そのキーを全アプリから奪ってしまうので受理しない。
// 空・不正・未対応の spec は domain.ErrInvalidArgument。
func hotkeyParse(spec string) (hotkeyChord, error) {
	combo, err := domain.ParseKeyCombo(spec)
	if err != nil {
		return hotkeyChord{}, err
	}
	if len(combo.Mods) == 0 {
		return hotkeyChord{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("hotkey %q needs at least one modifier", strings.TrimSpace(spec)))
	}
	vk, ok := hotkeyVK(combo.Key)
	if !ok {
		return hotkeyChord{}, domain.ErrInvalidArgument.WithMsg("unsupported hotkey key: " + combo.Key)
	}
	chord := hotkeyChord{VK: vk}
	for _, m := range combo.Mods {
		switch m {
		case domain.ModAlt:
			chord.Mods |= hotkeyModAlt
		case domain.ModCtrl:
			chord.Mods |= hotkeyModControl
		case domain.ModShift:
			chord.Mods |= hotkeyModShift
		case domain.ModWin:
			chord.Mods |= hotkeyModWin
		}
	}
	return chord, nil
}
