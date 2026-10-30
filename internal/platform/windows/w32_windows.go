//go:build windows

package windows

import (
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// x/sys/windows が持たない Win32 関数への薄い束縛。cgo は使わず、LazyProc で呼ぶ。
// 識別子は w32 で始める（同じパッケージの他のファイルと衝突させない）。
var (
	w32User32   = windows.NewLazySystemDLL("user32.dll")
	w32Gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	w32Kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	w32Shcore   = windows.NewLazySystemDLL("shcore.dll")
	w32Dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	w32SendInput             = w32User32.NewProc("SendInput")
	w32GetCursorPos          = w32User32.NewProc("GetCursorPos")
	w32SetCursorPos          = w32User32.NewProc("SetCursorPos")
	w32GetSystemMetrics      = w32User32.NewProc("GetSystemMetrics")
	w32MapVirtualKeyW        = w32User32.NewProc("MapVirtualKeyW")
	w32VkKeyScanExW          = w32User32.NewProc("VkKeyScanExW")
	w32GetKeyboardLayoutProc = w32User32.NewProc("GetKeyboardLayout")
	w32OpenInputDesktop      = w32User32.NewProc("OpenInputDesktop")
	w32CloseDesktop          = w32User32.NewProc("CloseDesktop")
	w32EnumDisplayMonitors   = w32User32.NewProc("EnumDisplayMonitors")
	w32GetMonitorInfoW       = w32User32.NewProc("GetMonitorInfoW")
	w32GetDC                 = w32User32.NewProc("GetDC")
	w32ReleaseDC             = w32User32.NewProc("ReleaseDC")
	w32EnumWindows           = w32User32.NewProc("EnumWindows")
	w32GetWindowTextW        = w32User32.NewProc("GetWindowTextW")
	w32GetWindowTextLength   = w32User32.NewProc("GetWindowTextLengthW")
	w32GetWindowRect         = w32User32.NewProc("GetWindowRect")
	w32IsIconic              = w32User32.NewProc("IsIconic")
	w32ShowWindow            = w32User32.NewProc("ShowWindow")
	w32SetForegroundWindow   = w32User32.NewProc("SetForegroundWindow")
	w32BringWindowToTop      = w32User32.NewProc("BringWindowToTop")
	w32AttachThreadInput     = w32User32.NewProc("AttachThreadInput")
	w32OpenClipboard         = w32User32.NewProc("OpenClipboard")
	w32CloseClipboard        = w32User32.NewProc("CloseClipboard")
	w32EmptyClipboard        = w32User32.NewProc("EmptyClipboard")
	w32GetClipboardData      = w32User32.NewProc("GetClipboardData")
	w32SetClipboardData      = w32User32.NewProc("SetClipboardData")
	w32IsClipboardFormat     = w32User32.NewProc("IsClipboardFormatAvailable")
	w32SetDpiContext         = w32User32.NewProc("SetProcessDpiAwarenessContext")

	w32CreateCompatibleDC     = w32Gdi32.NewProc("CreateCompatibleDC")
	w32DeleteDC               = w32Gdi32.NewProc("DeleteDC")
	w32CreateDIBSection       = w32Gdi32.NewProc("CreateDIBSection")
	w32SelectObject           = w32Gdi32.NewProc("SelectObject")
	w32DeleteObject           = w32Gdi32.NewProc("DeleteObject")
	w32BitBlt                 = w32Gdi32.NewProc("BitBlt")
	w32GlobalAlloc            = w32Kernel32.NewProc("GlobalAlloc")
	w32GlobalFree             = w32Kernel32.NewProc("GlobalFree")
	w32GlobalLock             = w32Kernel32.NewProc("GlobalLock")
	w32GlobalUnlock           = w32Kernel32.NewProc("GlobalUnlock")
	w32GlobalSize             = w32Kernel32.NewProc("GlobalSize")
	w32GetDpiForMonitor       = w32Shcore.NewProc("GetDpiForMonitor")
	w32DwmGetWindowAttribute  = w32Dwmapi.NewProc("DwmGetWindowAttribute")
	w32GetWindowThreadProcess = w32User32.NewProc("GetWindowThreadProcessId")
	w32GetForegroundWindow    = w32User32.NewProc("GetForegroundWindow")
	w32IsWindow               = w32User32.NewProc("IsWindow")
	w32IsWindowVisible        = w32User32.NewProc("IsWindowVisible")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	srccopy      = 0x00CC0020
	captureblt   = 0x40000000
	dibRGBColors = 0

	swRestore = 9

	cfUnicodeText = 13
	gmemMoveable  = 0x0002

	dwmwaCloaked             = 14
	dwmwaExtendedFrameBounds = 9

	mdtEffectiveDPI = 0

	desktopSwitchDesktop = 0x0100

	dpiAwarenessPerMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (HANDLE)-4

	monitorInfoPrimary = 0x1
)

// w32Point は POINT。
type w32Point struct{ X, Y int32 }

// w32Rect は RECT。
type w32Rect struct{ Left, Top, Right, Bottom int32 }

func (r w32Rect) width() int  { return int(r.Right - r.Left) }
func (r w32Rect) height() int { return int(r.Bottom - r.Top) }

// w32MonitorInfoEx は MONITORINFOEXW。
type w32MonitorInfoEx struct {
	Size    uint32
	Monitor w32Rect
	Work    w32Rect
	Flags   uint32
	Device  [32]uint16
}

// w32BitmapInfoHeader は BITMAPINFOHEADER。
type w32BitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// w32LastError は失敗した呼び出しの GetLastError（LazyProc.Call が返す err）を返す。エラー番号が 0 のときは EINVAL。
func w32LastError(err error) error {
	if err == nil || err == syscall.Errno(0) {
		return syscall.EINVAL
	}
	return err
}

var dpiOnce sync.Once

// EnableDPIAwareness はプロセスを Per-Monitor DPI Aware V2 にする（座標を物理ピクセルで扱うため）。
// 通常はマニフェストで宣言済みで（docs/architecture/overview.md）、その場合は何もしない。
// ウィンドウを作る前に呼ぶこと。呼び出しは 1 回だけ有効で、すでに設定済みのプロセスでは失敗を無視する。
func EnableDPIAwareness() {
	dpiOnce.Do(func() {
		if w32SetDpiContext.Find() == nil {
			_, _, _ = w32SetDpiContext.Call(dpiAwarenessPerMonitorV2)
		}
	})
}

// desktopAvailable は入力デスクトップへアクセスできるか（ロック画面・UAC のセキュアデスクトップ・セッション切断ではできない）。
func desktopAvailable() bool {
	h, _, _ := w32OpenInputDesktop.Call(0, 0, desktopSwitchDesktop)
	if h == 0 {
		return false
	}
	_, _, _ = w32CloseDesktop.Call(h)
	return true
}
