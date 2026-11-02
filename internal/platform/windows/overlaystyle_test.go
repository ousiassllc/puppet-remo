package windows

import "testing"

func TestIsOverlayStyle(t *testing.T) {
	tests := []struct {
		name string
		ex   uintptr
		want bool
	}{
		{"スタイルなし", 0, false},
		{"TRANSPARENT のみ", wsExTransparent, false},
		{"LAYERED のみ", wsExLayered, false},
		{"両方", wsExTransparent | wsExLayered, true},
		{"両方と他のスタイル（TOPMOST・TOOLWINDOW・NOACTIVATE）", wsExTransparent | wsExLayered | 0x8 | 0x80 | 0x08000000, true},
	}
	for _, tt := range tests {
		if got := isOverlayStyle(tt.ex); got != tt.want {
			t.Errorf("%s: isOverlayStyle(%#x) = %v, want %v", tt.name, tt.ex, got, tt.want)
		}
	}
}
