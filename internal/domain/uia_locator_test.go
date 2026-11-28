package domain_test

import (
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestParseMatchMode(t *testing.T) {
	for in, want := range map[string]domain.MatchMode{"": domain.MatchExact, "exact": domain.MatchExact, "contains": domain.MatchContains} {
		if got, err := domain.ParseMatchMode(in); err != nil || got != want {
			t.Errorf("ParseMatchMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"Exact", "regex", "prefix"} {
		if _, err := domain.ParseMatchMode(in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("ParseMatchMode(%q) error = %v, want invalid_argument", in, err)
		}
	}
}

func TestWindowLocatorMatches(t *testing.T) {
	w := domain.WindowInfo{ID: "100", Title: "Untitled - Notepad", Process: "notepad.exe"}
	tests := []struct {
		name string
		loc  domain.WindowLocator
		want bool
	}{
		{"empty matches everything", domain.WindowLocator{}, true},
		{"id", domain.WindowLocator{WindowID: "100"}, true},
		{"other id", domain.WindowLocator{WindowID: "101"}, false},
		{"exact title", domain.WindowLocator{Title: "Untitled - Notepad"}, true},
		{"exact title is case sensitive", domain.WindowLocator{Title: "untitled - notepad"}, false},
		{"exact title is not a substring match", domain.WindowLocator{Title: "Notepad"}, false},
		{"contains title ignores case", domain.WindowLocator{Title: "NOTEPAD", Match: domain.MatchContains}, true},
		{"process ignores case", domain.WindowLocator{ProcessName: "NotePad.EXE"}, true},
		{"process is exact even with contains", domain.WindowLocator{ProcessName: "notepad", Match: domain.MatchContains}, false},
		{"all fields must match", domain.WindowLocator{WindowID: "100", Title: "Other", Match: domain.MatchContains}, false},
		{"all fields match", domain.WindowLocator{WindowID: "100", Title: "note", ProcessName: "notepad.exe", Match: domain.MatchContains}, true},
	}
	for _, tt := range tests {
		if got := tt.loc.Matches(w); got != tt.want {
			t.Errorf("%s: Matches = %v, want %v", tt.name, got, tt.want)
		}
	}
	if (domain.WindowLocator{Match: domain.MatchContains}).IsZero() != true || (domain.WindowLocator{Title: "x"}).IsZero() {
		t.Error("IsZero must look at the criteria only")
	}
	if !(domain.WindowLocator{Title: "pad"}).MatchesAny(w) || (domain.WindowLocator{}).MatchesAny(w) ||
		!(domain.WindowLocator{Title: "nope", ProcessName: "NOTEPAD.EXE"}).MatchesAny(w) || (domain.WindowLocator{Title: "nope"}).MatchesAny(w) {
		t.Error("MatchesAny: want contains-title OR process OR id, false without criteria")
	}
}

func TestElementLocatorMatches(t *testing.T) {
	e := domain.UIElementInfo{ControlType: "Button", Name: "Save As…", AutomationID: "SaveAsButton"}
	tests := []struct {
		name string
		loc  domain.ElementLocator
		want bool
	}{
		{"name exact", domain.ElementLocator{Name: "Save As…"}, true},
		{"name exact is case sensitive", domain.ElementLocator{Name: "save as…"}, false},
		{"name contains", domain.ElementLocator{Name: "SAVE", Match: domain.MatchContains}, true},
		{"automation id exact", domain.ElementLocator{AutomationID: "SaveAsButton"}, true},
		{"automation id is ordinal even with contains", domain.ElementLocator{AutomationID: "saveasbutton", Match: domain.MatchContains}, false},
		{"control type ignores case", domain.ElementLocator{ControlType: "button"}, true},
		{"control type differs", domain.ElementLocator{ControlType: "Edit"}, false},
		{"combination", domain.ElementLocator{Name: "Save", Match: domain.MatchContains, ControlType: "Button", AutomationID: "SaveAsButton"}, true},
		{"combination with one miss", domain.ElementLocator{Name: "Save", Match: domain.MatchContains, ControlType: "Edit"}, false},
	}
	for _, tt := range tests {
		if got := tt.loc.Matches(e); got != tt.want {
			t.Errorf("%s: Matches = %v, want %v", tt.name, got, tt.want)
		}
	}
	if !(domain.ElementLocator{Name: "zzz", ControlType: "Button"}).MatchesAny(e) || (domain.ElementLocator{Name: "zzz"}).MatchesAny(e) || (domain.ElementLocator{}).MatchesAny(e) {
		t.Error("MatchesAny: want OR over the fields, false without criteria")
	}
}

func TestElementLocatorValidate(t *testing.T) {
	ok := []domain.ElementLocator{{Name: "x"}, {AutomationID: "x"}, {ControlType: "edit"}, {Name: "x", Match: domain.MatchContains}}
	for _, l := range ok {
		if err := l.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", l, err)
		}
	}
	bad := []domain.ElementLocator{{}, {Match: domain.MatchContains}, {ControlType: "Buton"}, {Name: "x", Match: "regex"}}
	for _, l := range bad {
		if err := l.Validate(); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Validate(%+v) = %v, want invalid_argument", l, err)
		}
	}
	if err := (domain.WindowLocator{Match: "nope"}).Validate(); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("WindowLocator.Validate = %v", err)
	}
}

func TestCanonicalUIControlType(t *testing.T) {
	if n, ok := domain.CanonicalUIControlType("checkbox"); !ok || n != "CheckBox" {
		t.Errorf("CanonicalUIControlType(checkbox) = %q, %v", n, ok)
	}
	if _, ok := domain.CanonicalUIControlType("Unknown"); ok {
		t.Error("Unknown is not a ControlType a caller can ask for")
	}
	if domain.UIControlTypeName(50000) != "Button" || domain.UIControlTypeName(1) != "Unknown" {
		t.Error("UIControlTypeName")
	}
}

func TestElementRefRoundTrip(t *testing.T) {
	ref := domain.ElementRef{HWND: 197382, PID: 4812, RuntimeID: []int32{42, 0x1f4, -3}}
	s := ref.String()
	if s != "uie:197382:4812:2a.1f4.fffffffd" {
		t.Fatalf("String = %q", s)
	}
	got, err := domain.ParseElementRef(s)
	if err != nil || got.HWND != ref.HWND || got.PID != ref.PID || len(got.RuntimeID) != 3 || got.RuntimeID[2] != -3 || got.WindowID() != "197382" {
		t.Errorf("ParseElementRef = %+v, %v", got, err)
	}
	if (domain.ElementRef{HWND: 1, PID: 1}).String() != "" {
		t.Error("a ref without a RuntimeId must have no id")
	}
}

func TestParseElementRefRejectsMalformedIDs(t *testing.T) {
	for _, s := range []string{
		"", "uia:2a.1f4.3", "uie:", "uie:1:2", "uie:1:2:", "uie:0:2:3", "uie:x:2:3", "uie:1:y:3", "uie:1:2:zz",
		"uie:1:2:3:4", "uie:1:2:03", "uie:1:2:2A", "uie:01:2:3", "uie:1:2:3.", "uie:1:99999999999:3",
	} {
		if _, err := domain.ParseElementRef(s); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("ParseElementRef(%q) error = %v, want invalid_argument", s, err)
		}
	}
}
