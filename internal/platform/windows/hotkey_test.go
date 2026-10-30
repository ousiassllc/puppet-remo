package windows

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func TestHotkeyParse(t *testing.T) {
	const ctrlAltShift = hotkeyModControl | hotkeyModAlt | hotkeyModShift
	tests := []struct {
		spec string
		want hotkeyChord
	}{
		{"Ctrl+Alt+Shift+K", hotkeyChord{ctrlAltShift, 'K'}},
		{"ctrl+alt+shift+k", hotkeyChord{ctrlAltShift, 'K'}},
		{" SHIFT+Alt+CTRL+K ", hotkeyChord{ctrlAltShift, 'K'}},
		{"Ctrl+Alt+Shift+K+", hotkeyChord{}}, // 下で不正として扱う（want は使わない）
		{"Ctrl+1", hotkeyChord{hotkeyModControl, '1'}},
		{"Win+F23", hotkeyChord{hotkeyModWin, 0x70 + 22}},
		{"ctrl+f1", hotkeyChord{hotkeyModControl, 0x70}},
		{"alt+space", hotkeyChord{hotkeyModAlt, 0x20}},
		{"alt+enter", hotkeyChord{hotkeyModAlt, 0x0D}},
		{"ctrl+pagedown", hotkeyChord{hotkeyModControl, 0x22}},
		{"ctrl+;", hotkeyChord{hotkeyModControl, 0xBA}},
		{"ctrl+ctrl+a", hotkeyChord{hotkeyModControl, 'A'}},
	}
	for _, tt := range tests {
		got, err := hotkeyParse(tt.spec)
		if tt.spec == "Ctrl+Alt+Shift+K+" {
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("hotkeyParse(%q) error = %v, want ErrInvalidArgument", tt.spec, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("hotkeyParse(%q): %v", tt.spec, err)
			continue
		}
		if got != tt.want {
			t.Errorf("hotkeyParse(%q) = %+v, want %+v", tt.spec, got, tt.want)
		}
	}
}

func TestHotkeyParseRejects(t *testing.T) {
	for _, spec := range []string{
		"", "   ", "ctrl+", "+k", "ctrl++k", "k", "f5", // 修飾キーなし
		"ctrl+nokey", "hyper+k", "ctrl+f25", "ctrl+é", "ctrl+<", // 未知・未対応
	} {
		if _, err := hotkeyParse(spec); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("hotkeyParse(%q) error = %v, want ErrInvalidArgument", spec, err)
		}
	}
}

// fakeHotkeyLoop は hotkeyLoop の偽の実装。Do / Post は呼び出し元の goroutine で同期的に実行し、
// 押下は press で模擬する。OS のホットキーは (mods, vk) の重複を拒否する。
type fakeHotkeyLoop struct {
	mu       sync.Mutex
	os       map[int32]hotkeyChord // OS に登録中のホットキー
	taken    map[hotkeyChord]bool  // 他のアプリが握っている組み合わせ
	closed   bool
	registry []int32 // Register された ID の履歴
	fire     func(id int32)
}

func newFakeHotkeyLoop() *fakeHotkeyLoop {
	return &fakeHotkeyLoop{os: map[int32]hotkeyChord{}, taken: map[hotkeyChord]bool{}}
}

func (l *fakeHotkeyLoop) Do(fn func() error) error {
	if l.closed {
		return errors.New("closed")
	}
	return fn()
}
func (l *fakeHotkeyLoop) Post(fn func()) {
	if !l.closed {
		fn()
	}
}
func (l *fakeHotkeyLoop) Register(id int32, chord hotkeyChord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.taken[chord] {
		return errors.New("already taken by another app")
	}
	l.os[id] = chord
	l.registry = append(l.registry, id)
	return nil
}
func (l *fakeHotkeyLoop) Unregister(id int32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.os, id)
}
func (l *fakeHotkeyLoop) Close() {
	l.mu.Lock()
	l.closed = true
	l.os = map[int32]hotkeyChord{}
	l.mu.Unlock()
}

// press は OS が WM_HOTKEY を届けるのを模擬する。OS に登録中のホットキーだけが届く。
func (l *fakeHotkeyLoop) press(h *Hotkey, chord hotkeyChord) {
	l.mu.Lock()
	var ids []int32
	for id, c := range l.os {
		if c == chord {
			ids = append(ids, id)
		}
	}
	l.mu.Unlock()
	for _, id := range ids {
		h.fire(id)
	}
}

func TestHotkeyContract(t *testing.T) {
	platformtest.RunHotkeyContract(t, func(t *testing.T) platform.Hotkey {
		return newHotkey(newFakeHotkeyLoop())
	})
}

func TestHotkeyCallsFnOnPress(t *testing.T) {
	loop := newFakeHotkeyLoop()
	h := newHotkey(loop)
	calls := 0
	unregister, err := h.Register("Ctrl+Alt+Shift+K", func() { calls++ })
	if err != nil {
		t.Fatal(err)
	}
	chord, _ := hotkeyParse("ctrl+alt+shift+k")
	loop.press(h, chord)
	loop.press(h, chord)
	loop.press(h, hotkeyChord{hotkeyModControl, 'K'}) // 別の組み合わせ
	if calls != 2 {
		t.Errorf("fn called %d times, want 2", calls)
	}
	unregister()
	loop.press(h, chord)
	if calls != 2 {
		t.Errorf("fn called after unregister: %d calls", calls)
	}
}

func TestHotkeyUnregisterIsIdempotentAndDoesNotAffectAReRegistration(t *testing.T) {
	loop := newFakeHotkeyLoop()
	h := newHotkey(loop)
	first := 0
	unregister, err := h.Register("ctrl+alt+k", func() { first++ })
	if err != nil {
		t.Fatal(err)
	}
	unregister()
	second := 0
	if _, err := h.Register("Ctrl+Alt+K", func() { second++ }); err != nil {
		t.Fatalf("re-register after unregister: %v", err)
	}
	unregister() // 古い unregister を再び呼んでも、新しい登録は残る
	chord, _ := hotkeyParse("ctrl+alt+k")
	loop.press(h, chord)
	if first != 0 || second != 1 {
		t.Errorf("first %d, second %d; want 0 and 1", first, second)
	}
	if len(loop.os) != 1 {
		t.Errorf("OS registrations = %d, want 1", len(loop.os))
	}
}

func TestHotkeyDuplicateWithDifferentSpelling(t *testing.T) {
	h := newHotkey(newFakeHotkeyLoop())
	if _, err := h.Register("Ctrl+Alt+Shift+K", func() {}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Register("shift+ctrl+alt+k", func() {}); err == nil {
		t.Error("the same chord in a different spelling registered twice")
	}
}

func TestHotkeyOSRefusalLeavesNoRegistration(t *testing.T) {
	loop := newFakeHotkeyLoop()
	chord, _ := hotkeyParse("ctrl+alt+k")
	loop.taken[chord] = true
	h := newHotkey(loop)
	if _, err := h.Register("ctrl+alt+k", func() {}); err == nil {
		t.Fatal("registration of a chord taken by another app succeeded")
	}
	loop.taken[chord] = false
	if _, err := h.Register("ctrl+alt+k", func() {}); err != nil {
		t.Errorf("retry after the other app released the chord: %v", err)
	}
}

func TestHotkeyUnregisterFromInsideFn(t *testing.T) {
	loop := newFakeHotkeyLoop()
	h := newHotkey(loop)
	var unregister func()
	calls := 0
	unregister, err := h.Register("ctrl+alt+k", func() { calls++; unregister() })
	if err != nil {
		t.Fatal(err)
	}
	chord, _ := hotkeyParse("ctrl+alt+k")
	loop.press(h, chord)
	loop.press(h, chord)
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestHotkeyDistinctIDsPerRegistration(t *testing.T) {
	loop := newFakeHotkeyLoop()
	h := newHotkey(loop)
	for _, spec := range []string{"ctrl+alt+a", "ctrl+alt+b"} {
		if _, err := h.Register(spec, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	if len(loop.registry) != 2 || loop.registry[0] == loop.registry[1] {
		t.Errorf("registered IDs = %v, want two distinct", loop.registry)
	}
	if !slices.IsSorted(loop.registry) {
		t.Errorf("IDs not increasing: %v", loop.registry)
	}
}

func TestHotkeyRegisterAfterCloseFails(t *testing.T) {
	loop := newFakeHotkeyLoop()
	h := newHotkey(loop)
	h.Close()
	if _, err := h.Register("ctrl+alt+k", func() {}); err == nil {
		t.Error("Register after Close succeeded")
	}
	// 失敗した登録は残らない（spec を再利用できる）
	if len(h.byChord) != 0 || len(h.byID) != 0 {
		t.Errorf("failed registration left state: %v %v", h.byChord, h.byID)
	}
}
