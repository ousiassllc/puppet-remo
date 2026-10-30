package windows

import (
	"slices"
	"sync"
	"testing"
)

// recordingSetter は SetThreadExecutionState の代わりに、渡されたフラグを記録する。
type recordingSetter struct {
	mu    sync.Mutex
	flags []uint32
}

func (r *recordingSetter) set(flags uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flags = append(r.flags, flags)
}

func (r *recordingSetter) Flags() []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.flags)
}

func TestPowerFlags(t *testing.T) {
	if got, want := powerFlags(true), uint32(0x80000003); got != want {
		t.Errorf("powerFlags(true) = %#x, want %#x (ES_CONTINUOUS|ES_SYSTEM_REQUIRED|ES_DISPLAY_REQUIRED)", got, want)
	}
	if got, want := powerFlags(false), uint32(0x80000000); got != want {
		t.Errorf("powerFlags(false) = %#x, want %#x (ES_CONTINUOUS)", got, want)
	}
}

func TestPowerKeepAwakePassesFlagsInOrderAndCloseClears(t *testing.T) {
	rec := &recordingSetter{}
	p := newPower(rec.set)
	p.KeepAwake(true)
	p.KeepAwake(false)
	p.KeepAwake(true)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	want := []uint32{powerFlags(true), powerFlags(false), powerFlags(true), powerFlags(false)}
	if got := rec.Flags(); !slices.Equal(got, want) {
		t.Errorf("flags = %#x, want %#x (Close must clear the state)", got, want)
	}
}

func TestPowerCloseIsIdempotentAndLaterCallsAreIgnored(t *testing.T) {
	rec := &recordingSetter{}
	p := newPower(rec.set)
	p.KeepAwake(true)
	_ = p.Close()
	_ = p.Close()
	p.KeepAwake(true) // Close 後は専用スレッドが止まっているので何もしない
	want := []uint32{powerFlags(true), powerFlags(false)}
	if got := rec.Flags(); !slices.Equal(got, want) {
		t.Errorf("flags = %#x, want %#x", got, want)
	}
}
