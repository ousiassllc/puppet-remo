package windows

import (
	"errors"
	"reflect"
	"testing"
	"unsafe"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func errorsIsInvalidArgument(err error) bool { return errors.Is(err, domain.ErrInvalidArgument) }

func TestTextSteps(t *testing.T) {
	enter := keySpec{VK: vkReturn}
	tab := keySpec{VK: vkTab}
	tests := []struct {
		name string
		in   string
		want []inputStep
	}{
		{"empty", "", nil},
		{"ascii", "a", []inputStep{runeStep('a', false), runeStep('a', true)}},
		{"japanese", "あ", []inputStep{runeStep(0x3042, false), runeStep(0x3042, true)}},
		{"surrogate pair", "😀", []inputStep{
			runeStep(0xD83D, false), runeStep(0xD83D, true), runeStep(0xDE00, false), runeStep(0xDE00, true)}},
		{"LF is Enter", "\n", []inputStep{keyDownStep(enter), keyUpStep(enter)}},
		{"CRLF is one Enter", "\r\n", []inputStep{keyDownStep(enter), keyUpStep(enter)}},
		{"CR is Enter", "\r", []inputStep{keyDownStep(enter), keyUpStep(enter)}},
		{"CR CR is two Enters", "\r\r", []inputStep{keyDownStep(enter), keyUpStep(enter), keyDownStep(enter), keyUpStep(enter)}},
		{"tab is Tab", "\t", []inputStep{keyDownStep(tab), keyUpStep(tab)}},
		{"NUL dropped", "\x00", nil},
		{"invalid UTF-8 becomes U+FFFD", "\xff", []inputStep{runeStep(0xFFFD, false), runeStep(0xFFFD, true)}},
	}
	for _, tt := range tests {
		if got := textSteps(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: textSteps(%q) = %+v, want %+v", tt.name, tt.in, got, tt.want)
		}
	}
}

// 注入する入力は down と up が対になっている（押しっぱなしにならない）。
func TestTextStepsAreBalanced(t *testing.T) {
	steps := textSteps("abc日本語😀\r\n\tz")
	downs, ups := 0, 0
	for _, s := range steps {
		if s.up {
			ups++
		} else {
			downs++
		}
	}
	if downs != ups || downs == 0 {
		t.Errorf("downs = %d, ups = %d", downs, ups)
	}
	if !steps[len(steps)-1].up {
		t.Error("the last step must be a key up")
	}
}

func TestEncodeInputsLayout(t *testing.T) {
	wantSize := uintptr(40)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wantSize = 28
	}
	if got := unsafe.Sizeof(winInput{}); got != wantSize {
		t.Fatalf("sizeof(INPUT) = %d, want %d", got, wantSize)
	}
	if got := unsafe.Sizeof(keybdInput{}); got > unsafe.Sizeof(mouseInput{}) {
		t.Fatalf("KEYBDINPUT (%d) must fit in the MOUSEINPUT-sized union (%d)", got, unsafe.Sizeof(mouseInput{}))
	}

	scan := func(vk uint16) uint16 { return vk + 0x100 }
	got := encodeInputs([]inputStep{
		keyDownStep(keySpec{VK: vkLeft, Extended: true}),
		keyUpStep(keySpec{VK: vkLeft, Extended: true}),
		runeStep(0x3042, false),
		runeStep(0x3042, true),
		absMoveStep(100, 200),
		wheelStep(false, -120),
	}, scan)

	key := func(i int) keybdInput { return *(*keybdInput)(unsafe.Pointer(&got[i].mi)) }
	if got[0].typ != inputKeyboard || key(0).vk != vkLeft || key(0).scan != vkLeft+0x100 || key(0).flags != keyeventfExtendedKey {
		t.Errorf("extended key down = %+v", key(0))
	}
	if key(1).flags != keyeventfExtendedKey|keyeventfKeyUp {
		t.Errorf("extended key up flags = %#x", key(1).flags)
	}
	if got[2].typ != inputKeyboard || key(2).vk != 0 || key(2).scan != 0x3042 || key(2).flags != keyeventfUnicode {
		t.Errorf("unicode down = %+v", key(2))
	}
	if key(3).flags != keyeventfUnicode|keyeventfKeyUp {
		t.Errorf("unicode up flags = %#x", key(3).flags)
	}
	if got[4].typ != inputMouse || got[4].mi.dx != 100 || got[4].mi.dy != 200 ||
		got[4].mi.flags != mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk {
		t.Errorf("absolute move = %+v", got[4])
	}
	if got[5].typ != inputMouse || int32(got[5].mi.mouseData) != -120 || got[5].mi.flags != mouseeventfWheel {
		t.Errorf("wheel = %+v", got[5])
	}
}

func TestButtonStep(t *testing.T) {
	tests := []struct {
		b        domain.Button
		up       bool
		wantFlag uint32
		ok       bool
	}{
		{domain.ButtonLeft, false, mouseeventfLeftDown, true},
		{domain.ButtonLeft, true, mouseeventfLeftUp, true},
		{domain.ButtonRight, false, mouseeventfRightDown, true},
		{domain.ButtonRight, true, mouseeventfRightUp, true},
		{domain.ButtonMiddle, false, mouseeventfMiddleDown, true},
		{domain.ButtonMiddle, true, mouseeventfMiddleUp, true},
		{"x", false, 0, false},
	}
	for _, tt := range tests {
		s, ok := buttonStep(tt.b, tt.up)
		if ok != tt.ok || s.flags != tt.wantFlag {
			t.Errorf("buttonStep(%q, %v) = flags %#x, %v; want %#x, %v", tt.b, tt.up, s.flags, ok, tt.wantFlag, tt.ok)
		}
	}
}

func TestReleaseSteps(t *testing.T) {
	ctrl, shift := keySpec{VK: vkControl}, keySpec{VK: vkShift}
	left, _ := buttonStep(domain.ButtonLeft, false)
	steps := []inputStep{keyDownStep(ctrl), keyDownStep(shift), left, keyDownStep(shift)}
	want := []inputStep{
		keyUpStep(shift),
		{kind: stepMouse, flags: mouseeventfLeftUp},
		keyUpStep(ctrl),
	}
	if got := releaseSteps(steps); !reflect.DeepEqual(got, want) {
		t.Errorf("releaseSteps = %+v, want %+v", got, want)
	}
	if got := releaseSteps([]inputStep{keyDownStep(ctrl), keyUpStep(ctrl)}); len(got) != 1 || !got[0].up {
		// ctrl の down が先にあるので、解放は 1 つ（余分でも無害）。
		t.Errorf("releaseSteps(down+up) = %+v", got)
	}
	if got := releaseSteps(nil); len(got) != 0 {
		t.Errorf("releaseSteps(nil) = %+v", got)
	}
}
