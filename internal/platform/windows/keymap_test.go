package windows

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestLookupKeyName(t *testing.T) {
	tests := []struct {
		name string
		want keySpec
		ok   bool
	}{
		{"enter", keySpec{VK: 0x0D}, true},
		{"escape", keySpec{VK: 0x1B}, true},
		{"tab", keySpec{VK: 0x09}, true},
		{"backspace", keySpec{VK: 0x08}, true},
		{"delete", keySpec{VK: 0x2E, Extended: true}, true},
		{"left", keySpec{VK: 0x25, Extended: true}, true},
		{"pageup", keySpec{VK: 0x21, Extended: true}, true},
		{"space", keySpec{VK: 0x20}, true},
		{"capslock", keySpec{VK: 0x14}, true},
		{"f1", keySpec{VK: 0x70}, true},
		{"f12", keySpec{VK: 0x7B}, true},
		{"f24", keySpec{VK: 0x87}, true},
		{"a", keySpec{VK: 'A'}, true},
		{"z", keySpec{VK: 'Z'}, true},
		{"0", keySpec{VK: '0'}, true},
		{"9", keySpec{VK: '9'}, true},
		{"f0", keySpec{}, false},
		{"f25", keySpec{}, false},
		{"f05", keySpec{}, false},
		{"f", keySpec{VK: 'F'}, true}, // 1 文字の f は英字のキー
		{"fx", keySpec{}, false},
		{"f1000", keySpec{}, false},
		{"nosuchkey", keySpec{}, false},
		{"", keySpec{}, false},
		{"-", keySpec{}, false}, // 記号は配列に問い合わせる
		{"あ", keySpec{}, false},
	}
	for _, tt := range tests {
		got, ok := lookupKeyName(tt.name)
		if ok != tt.ok || got != tt.want {
			t.Errorf("lookupKeyName(%q) = %+v, %v; want %+v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// domain.ValidKeyName が受理する名前つきキーと f1〜f24 は、配列に頼らずにすべて仮想キーへ変換できる。
func TestEveryValidNamedKeyResolves(t *testing.T) {
	names := []string{"enter", "escape", "tab", "backspace", "delete", "insert", "home", "end", "pageup", "pagedown",
		"up", "down", "left", "right", "space", "capslock"}
	for i := 1; i <= 24; i++ {
		names = append(names, "f"+itoa(i))
	}
	for _, name := range names {
		if !domain.ValidKeyName(name) {
			t.Fatalf("test list out of date: %q is not a valid key name", name)
		}
		if _, ok := lookupKeyName(name); !ok {
			t.Errorf("lookupKeyName(%q) not found", name)
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestFunctionKeysAreContiguous(t *testing.T) {
	for n := 1; n <= 24; n++ {
		got, ok := lookupKeyName("f" + itoa(n))
		if !ok || got.VK != vkF1+uint16(n-1) {
			t.Errorf("f%d = %+v, %v", n, got, ok)
		}
	}
}

func TestResolveKeyNameUsesLayoutForSymbols(t *testing.T) {
	scan := func(r rune) (uint16, bool, bool) {
		switch r {
		case '-':
			return vkOemMinus, false, true
		case '!':
			return '1', true, true
		case '€': // AltGr が要る文字は拒否される想定
			return 0, false, false
		}
		return 0, false, false
	}
	tests := []struct {
		name      string
		wantVK    uint16
		wantShift bool
		wantErr   bool
	}{
		{"a", 'A', false, false},
		{"enter", vkReturn, false, false},
		{"-", vkOemMinus, false, false},
		{"!", '1', true, false},
		{"€", 0, false, true},
		{"nosuchkey", 0, false, true},
	}
	for _, tt := range tests {
		spec, shift, err := resolveKeyName(tt.name, scan)
		if (err != nil) != tt.wantErr {
			t.Errorf("resolveKeyName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			continue
		}
		if err != nil {
			if !errorsIsInvalidArgument(err) {
				t.Errorf("resolveKeyName(%q) error = %v, want invalid_argument", tt.name, err)
			}
			continue
		}
		if spec.VK != tt.wantVK || shift != tt.wantShift {
			t.Errorf("resolveKeyName(%q) = vk %#x shift %v; want %#x, %v", tt.name, spec.VK, shift, tt.wantVK, tt.wantShift)
		}
	}
	if _, _, err := resolveKeyName("-", nil); err == nil {
		t.Error("resolveKeyName without a layout scanner should fail for a symbol")
	}
}

func TestModifierKey(t *testing.T) {
	tests := []struct {
		m    domain.Modifier
		want keySpec
	}{
		{domain.ModCtrl, keySpec{VK: vkControl}},
		{domain.ModShift, keySpec{VK: vkShift}},
		{domain.ModAlt, keySpec{VK: vkMenu}},
		{domain.ModWin, keySpec{VK: vkLWin, Extended: true}},
	}
	for _, tt := range tests {
		got, ok := modifierKey(tt.m)
		if !ok || got != tt.want {
			t.Errorf("modifierKey(%q) = %+v, %v; want %+v", tt.m, got, ok, tt.want)
		}
	}
	if _, ok := modifierKey("hyper"); ok {
		t.Error("modifierKey(hyper) should not be found")
	}
}

func TestCodeKey(t *testing.T) {
	tests := []struct {
		code string
		want keySpec
		ok   bool
	}{
		{"KeyA", keySpec{VK: 'A'}, true},
		{"KeyZ", keySpec{VK: 'Z'}, true},
		{"Digit0", keySpec{VK: '0'}, true},
		{"Digit9", keySpec{VK: '9'}, true},
		{"Numpad0", keySpec{VK: 0x60}, true},
		{"Numpad9", keySpec{VK: 0x69}, true},
		{"F1", keySpec{VK: 0x70}, true},
		{"F24", keySpec{VK: 0x87}, true},
		{"ShiftLeft", keySpec{VK: vkLShift}, true},
		{"ShiftRight", keySpec{VK: vkRShift}, true},
		{"ControlRight", keySpec{VK: vkRControl, Extended: true}, true},
		{"AltRight", keySpec{VK: vkRMenu, Extended: true}, true},
		{"MetaLeft", keySpec{VK: vkLWin, Extended: true}, true},
		{"ArrowUp", keySpec{VK: vkUp, Extended: true}, true},
		{"Enter", keySpec{VK: vkReturn}, true},
		{"NumpadEnter", keySpec{VK: vkReturn, Extended: true}, true},
		{"Space", keySpec{VK: vkSpace}, true},
		{"Backslash", keySpec{VK: vkOem5}, true},
		{"IntlYen", keySpec{VK: vkOem5}, true},
		{"IntlRo", keySpec{VK: vkOem102}, true},
		{"Convert", keySpec{VK: vkConvert}, true},
		{"Key", keySpec{}, false},
		{"Keya", keySpec{}, false},
		{"Key1", keySpec{}, false},
		{"Digitx", keySpec{}, false},
		{"F0", keySpec{}, false},
		{"F25", keySpec{}, false},
		{"Numpadx", keySpec{}, false},
		{"NoSuchCode", keySpec{}, false},
		{"", keySpec{}, false},
	}
	for _, tt := range tests {
		got, ok := codeKey(tt.code)
		if ok != tt.ok || got != tt.want {
			t.Errorf("codeKey(%q) = %+v, %v; want %+v, %v", tt.code, got, ok, tt.want, tt.ok)
		}
	}
}

func TestDecodeVkScan(t *testing.T) {
	tests := []struct {
		name      string
		v         int16
		vk        uint16
		shift, ok bool
	}{
		{"unmapped", -1, 0, false, false},
		{"plain a", 0x0041, 0x41, false, true},
		{"shifted", 0x0131, 0x31, true, true},
		{"ctrl needed", 0x0241, 0, false, false},
		{"alt needed (AltGr)", 0x0641, 0, false, false},
		{"shift and ctrl", 0x0341, 0, false, false},
	}
	for _, tt := range tests {
		vk, shift, ok := decodeVkScan(tt.v)
		if vk != tt.vk || shift != tt.shift || ok != tt.ok {
			t.Errorf("%s: decodeVkScan(%#x) = %#x, %v, %v; want %#x, %v, %v", tt.name, tt.v, vk, shift, ok, tt.vk, tt.shift, tt.ok)
		}
	}
}
