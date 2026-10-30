//go:build windows

package windows

import (
	"syscall"
	"unsafe"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// winDriver は driver の Windows 実装（SendInput）。
type winDriver struct{}

var _ driver = winDriver{}

// NewInput は SendInput による platform.InputInjector を作る。専用の OS スレッドを 1 本起動するので、使い終わったら Close する。
// 座標を物理ピクセルで扱うため、プロセスを Per-Monitor DPI Aware にする（EnableDPIAwareness）。
func NewInput() *Input {
	EnableDPIAwareness()
	return newInput(winDriver{})
}

func (winDriver) send(steps []inputStep) error {
	if len(steps) == 0 {
		return nil
	}
	inputs := encodeInputs(steps, mapVirtualKey)
	sent, _, errno := w32SendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	if int(sent) == len(inputs) {
		return nil
	}
	code, _ := errno.(syscall.Errno)
	return sendError(int(sent), len(inputs), uintptr(code), desktopAvailable())
}

func mapVirtualKey(vk uint16) uint16 {
	sc, _, _ := w32MapVirtualKeyW.Call(uintptr(vk), 0) // MAPVK_VK_TO_VSC
	return uint16(sc)
}

func (winDriver) cursorPos() (domain.Point, error) {
	var p w32Point
	r, _, err := w32GetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	if r == 0 {
		return domain.Point{}, desktopOr(err, "GetCursorPos")
	}
	return domain.Point{X: int(p.X), Y: int(p.Y)}, nil
}

func (winDriver) setCursorPos(p domain.Point) error {
	r, _, err := w32SetCursorPos.Call(uintptr(int32(p.X)), uintptr(int32(p.Y)))
	if r == 0 {
		return desktopOr(err, "SetCursorPos")
	}
	return nil
}

func (winDriver) virtualScreen() (domain.Rect, error) {
	metric := func(index uintptr) int {
		v, _, _ := w32GetSystemMetrics.Call(index)
		return int(int32(v))
	}
	r := domain.Rect{X: metric(smXVirtualScreen), Y: metric(smYVirtualScreen), W: metric(smCXVirtualScreen), H: metric(smCYVirtualScreen)}
	if r.Empty() {
		return domain.Rect{}, domain.ErrDesktopUnavailable.WithMsg("the virtual screen is empty")
	}
	return r, nil
}

// scanKey は前面ウィンドウのスレッドのキーボード配列で、1 文字を入力できる仮想キーを探す。
func (winDriver) scanKey(r rune) (uint16, bool, bool) {
	if r > 0xFFFF {
		return 0, false, false
	}
	hwnd, _, _ := w32GetForegroundWindow.Call()
	var tid uintptr
	if hwnd != 0 {
		tid, _, _ = w32GetWindowThreadProcess.Call(hwnd, 0)
	}
	hkl, _, _ := w32GetKeyboardLayoutProc.Call(tid)
	v, _, _ := w32VkKeyScanExW.Call(uintptr(r), hkl)
	scan := int16(v)
	return decodeVkScan(scan)
}

// desktopOr は失敗した Win32 呼び出しのエラーを、デスクトップを使えないなら ErrDesktopUnavailable、そうでなければ原因つきのエラーにする。
func desktopOr(err error, call string) error {
	if !desktopAvailable() {
		return domain.ErrDesktopUnavailable.WithMsg(call + " failed: the input desktop is not accessible")
	}
	return domain.NewError(domain.CodeInternal, call+" failed: "+w32LastError(err).Error(), nil)
}
