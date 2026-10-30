package domain_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestParseKeyCombo(t *testing.T) {
	tests := []struct {
		in      string
		want    domain.KeyCombo
		invalid bool
	}{
		{in: "enter", want: domain.KeyCombo{Key: "enter"}},
		{in: "Enter", want: domain.KeyCombo{Key: "enter"}},
		{in: "ctrl+shift+t", want: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl, domain.ModShift}, Key: "t"}},
		{in: "CTRL+Alt+F24", want: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl, domain.ModAlt}, Key: "f24"}},
		{in: "win+left", want: domain.KeyCombo{Mods: []domain.Modifier{domain.ModWin}, Key: "left"}},
		{in: "ctrl+ctrl+a", want: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl}, Key: "a"}},
		{in: "alt+;", want: domain.KeyCombo{Mods: []domain.Modifier{domain.ModAlt}, Key: ";"}},
		{in: "1", want: domain.KeyCombo{Key: "1"}},
		{in: "あ", want: domain.KeyCombo{Key: "あ"}},
		{in: "", invalid: true},
		{in: "ctrl+", invalid: true},
		{in: "+a", invalid: true},
		{in: "ctrl++", invalid: true},
		{in: "ctrl", invalid: true},
		{in: "ctrl+a+shift", invalid: true},
		{in: "super+a", invalid: true},
		{in: "f0", invalid: true},
		{in: "f25", invalid: true},
		{in: "f01", invalid: true},
		{in: "fx", invalid: true},
		{in: "ab", invalid: true},
		{in: "ctrl+ ", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := domain.ParseKeyCombo(tt.in)
			if tt.invalid {
				if !errors.Is(err, domain.ErrInvalidArgument) {
					t.Fatalf("ParseKeyCombo(%q) error = %v, want ErrInvalidArgument", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseKeyCombo(%q) error = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseKeyCombo(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestKeyComboStringRoundTrip(t *testing.T) {
	for _, in := range []string{"enter", "ctrl+shift+t", "alt+f4", "win+d"} {
		combo, err := domain.ParseKeyCombo(in)
		if err != nil {
			t.Fatalf("ParseKeyCombo(%q): %v", in, err)
		}
		if got := combo.String(); got != in {
			t.Errorf("String() = %q, want %q", got, in)
		}
	}
}

func TestKeyComboValidate(t *testing.T) {
	if err := (domain.KeyCombo{Mods: []domain.Modifier{"hyper"}, Key: "a"}).Validate(); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("unknown modifier: error = %v, want ErrInvalidArgument", err)
	}
	if err := (domain.KeyCombo{}).Validate(); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("empty key: error = %v, want ErrInvalidArgument", err)
	}
}

func TestEnumValid(t *testing.T) {
	if !domain.ButtonLeft.Valid() || domain.Button("").Valid() || domain.Button("x").Valid() {
		t.Error("Button.Valid")
	}
	if !domain.ModWin.Valid() || domain.Modifier("").Valid() {
		t.Error("Modifier.Valid")
	}
	if !domain.DirRight.Valid() || domain.Direction("").Valid() {
		t.Error("Direction.Valid")
	}
	if !domain.KeyUp.Valid() || domain.KeyAction("hold").Valid() {
		t.Error("KeyAction.Valid")
	}
}

func TestRectContains(t *testing.T) {
	r := domain.Rect{X: -10, Y: 5, W: 20, H: 10}
	tests := []struct {
		p    domain.Point
		want bool
	}{
		{domain.Point{X: -10, Y: 5}, true},
		{domain.Point{X: 9, Y: 14}, true},
		{domain.Point{X: 10, Y: 14}, false},
		{domain.Point{X: 9, Y: 15}, false},
		{domain.Point{X: -11, Y: 5}, false},
	}
	for _, tt := range tests {
		if got := r.Contains(tt.p); got != tt.want {
			t.Errorf("Contains(%v) = %v, want %v", tt.p, got, tt.want)
		}
	}
	if (domain.Rect{W: 0, H: 5}).Empty() == false || r.Empty() {
		t.Error("Empty")
	}
}
