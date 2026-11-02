package domain_test

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"短ければそのまま", "abc", 3, "abc"},
		{"超えたら文字（rune）で切る", "あいうえお", 3, "あいう"},
		{"空文字", "", 5, ""},
		{"0 文字", "abc", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.TruncateRunes(tt.in, tt.n); got != tt.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}
