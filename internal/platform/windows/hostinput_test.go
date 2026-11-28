package windows

import (
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

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

func TestSelfMoveRecorder(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	newRec := func() *selfMoveRecorder { return &selfMoveRecorder{now: func() time.Time { return now }} }
	p := domain.Point{X: 640, Y: 360}

	t.Run("nothing expected", func(t *testing.T) {
		if newRec().consume(wmMouseMove, 640, 360) {
			t.Error("consumed without an expectation")
		}
	})
	t.Run("the expected move is consumed once", func(t *testing.T) {
		r := newRec()
		r.expect(p)
		if !r.consume(wmMouseMove, 640, 360) {
			t.Fatal("the expected move was not consumed")
		}
		if r.consume(wmMouseMove, 640, 360) {
			t.Error("consumed twice")
		}
	})
	t.Run("a move elsewhere is the host's and keeps the expectation", func(t *testing.T) {
		r := newRec()
		r.expect(p)
		if r.consume(wmMouseMove, 641, 360) || r.consume(wmMouseMove, 640, 361) {
			t.Error("a move to another point was consumed")
		}
		if !r.consume(wmMouseMove, 640, 360) {
			t.Error("the expectation was lost by an unrelated move")
		}
	})
	t.Run("only a mouse move matches", func(t *testing.T) {
		r := newRec()
		r.expect(p)
		if r.consume(0x0201 /* WM_LBUTTONDOWN */, 640, 360) {
			t.Error("a button event was consumed")
		}
	})
	t.Run("an expired expectation is dropped", func(t *testing.T) {
		r := newRec()
		r.expect(p)
		now = now.Add(ownCursorMoveWindow + time.Millisecond)
		defer func() { now = now.Add(-ownCursorMoveWindow - time.Millisecond) }()
		if r.consume(wmMouseMove, 640, 360) {
			t.Error("consumed after the window")
		}
	})
	t.Run("a new expectation replaces the old", func(t *testing.T) {
		r := newRec()
		r.expect(p)
		r.expect(domain.Point{X: 1, Y: 2})
		if r.consume(wmMouseMove, 640, 360) || !r.consume(wmMouseMove, 1, 2) {
			t.Error("the older expectation was not replaced")
		}
	})
}
