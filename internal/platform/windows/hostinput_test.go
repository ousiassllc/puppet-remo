package windows

import "testing"

func TestIsHostMouseEvent(t *testing.T) {
	tests := []struct {
		name  string
		flags uint32
		extra uintptr
		want  bool
	}{
		{"real mouse", 0, 0, true},
		{"real mouse with a foreign extra info (touch / pen)", 0, 0xFF515700, true},
		{"injected by SendInput", llmhfInjected, 0, false},
		{"injected from a lower integrity level", llmhfLowerILInjected, 0, false},
		{"injected by our own Input", llmhfInjected, ownInputTag, false},
		{"our tag without the injected flag", 0, ownInputTag, false},
	}
	for _, tt := range tests {
		if got := isHostMouseEvent(tt.flags, tt.extra); got != tt.want {
			t.Errorf("%s: isHostMouseEvent(%#x, %#x) = %v, want %v", tt.name, tt.flags, tt.extra, got, tt.want)
		}
	}
}

func TestIsHostKeyEvent(t *testing.T) {
	tests := []struct {
		name  string
		flags uint32
		extra uintptr
		want  bool
	}{
		{"real key", 0, 0, true},
		{"real key, extended (flag 0x01 is not an injected flag)", 0x01, 0, true},
		{"real key, alt down (flag 0x20)", 0x20, 0, true},
		{"injected by SendInput", llkhfInjected, 0, false},
		{"injected from a lower integrity level", llkhfLowerILInjected, 0, false},
		{"injected by our own Input", llkhfInjected, ownInputTag, false},
		{"our tag without the injected flag", 0, ownInputTag, false},
	}
	for _, tt := range tests {
		if got := isHostKeyEvent(tt.flags, tt.extra); got != tt.want {
			t.Errorf("%s: isHostKeyEvent(%#x, %#x) = %v, want %v", tt.name, tt.flags, tt.extra, got, tt.want)
		}
	}
}
