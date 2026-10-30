//go:build windows

package windows

import (
	"context"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.WindowManager = (*WindowManager)(nil)

// WindowManager は platform.WindowManager の Windows 実装。事実を返すだけで、許可・拒否は決めない（判定は service.InputGuard）。
// 状態は作成時に決めるプロセスの昇格状態だけなので、どの goroutine からも呼べる。
type WindowManager struct {
	selfPID      uint32
	selfElevated bool
}

// NewWindowManager はウィンドウの列挙とフォーカスの実装を作る。
func NewWindowManager() (*WindowManager, error) {
	EnableDPIAwareness()
	return &WindowManager{
		selfPID:      uint32(os.Getpid()),
		selfElevated: windows.GetCurrentProcessToken().IsElevated(),
	}, nil
}

// List は可視のトップレベルウィンドウを、Z オーダー（前面が先）で返す。タイトルのないウィンドウと、
// 非表示（cloaked。別の仮想デスクトップ、UWP の待機中など）のウィンドウは含めない。
func (wm *WindowManager) List(ctx context.Context) ([]domain.WindowInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	all, err := wm.snapshot()
	if err != nil {
		return nil, err
	}
	windowsOut := all[:0]
	for _, w := range all {
		if w.Title != "" {
			windowsOut = append(windowsOut, w)
		}
	}
	return windowsOut, nil
}

// At は p 上の最前面のウィンドウを返す。タイトルのないウィンドウ（オーバーレイなど）も対象にする。最小化されたウィンドウは含めない。
func (wm *WindowManager) At(p domain.Point) (domain.WindowInfo, bool, error) {
	all, err := wm.snapshot()
	if err != nil {
		return domain.WindowInfo{}, false, err
	}
	for _, w := range all {
		if !w.Minimized && w.Rect.Contains(p) {
			return w, true, nil
		}
	}
	return domain.WindowInfo{}, false, nil
}

// Foreground は前面のウィンドウを返す。前面のウィンドウがなければ ErrWindowNotFound（デスクトップを使えなければ ErrDesktopUnavailable）。
func (wm *WindowManager) Foreground() (domain.WindowInfo, error) {
	hwnd, _, _ := w32GetForegroundWindow.Call()
	if hwnd == 0 {
		if !desktopAvailable() {
			return domain.WindowInfo{}, domain.ErrDesktopUnavailable.WithMsg("the input desktop is not accessible")
		}
		return domain.WindowInfo{}, domain.ErrWindowNotFound.WithMsg("there is no foreground window")
	}
	monitors, _ := listMonitors()
	return wm.info(hwnd, hwnd, monitors, newProcessCache()), nil
}

// Focus は id のウィンドウを前面にする（最小化されていれば元に戻す）。
// 存在しない ID・形式不正は ErrWindowNotFound。昇格したウィンドウ（こちらが昇格していないとき）と、
// OS の制限で前面にできなかったときは ErrInputBlocked。
func (wm *WindowManager) Focus(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hwnd, ok := parseWindowID(id)
	if !ok {
		return domain.ErrWindowNotFound.WithMsg("malformed window id: " + id)
	}
	if r, _, _ := w32IsWindow.Call(hwnd); r == 0 {
		return domain.ErrWindowNotFound.WithMsg("no such window: " + id)
	}
	fg, _, _ := w32GetForegroundWindow.Call()
	if wm.blockedByUIPI(wm.info(hwnd, fg, nil, newProcessCache())) {
		return domain.ErrInputBlocked.WithMsg("the window belongs to an elevated process (UIPI)")
	}
	if minimized, _, _ := w32IsIconic.Call(hwnd); minimized != 0 {
		_, _, _ = w32ShowWindow.Call(hwnd, swRestore)
	}
	if !bringToForeground(hwnd) {
		return domain.ErrInputBlocked.WithMsg("the system did not allow the window to take the foreground")
	}
	return nil
}

// blockedByUIPI は w の入力が UIPI で拒否されるか（w が昇格していて、こちらは昇格していない）。
func (wm *WindowManager) blockedByUIPI(w domain.WindowInfo) bool {
	return w.Elevated && !wm.selfElevated
}

// bringToForeground は hwnd を前面にする。前面ウィンドウのスレッドに入力を結びつけて、SetForegroundWindow の制限を避ける。
// 成功したか（呼び出し直後に hwnd が前面になったか）を返す。
func bringToForeground(hwnd uintptr) bool {
	// AttachThreadInput は呼び出しスレッドに対する操作なので、解除までの間 goroutine を同じ OS スレッドに固定する。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var fgTID, selfTID uintptr
	if fg, _, _ := w32GetForegroundWindow.Call(); fg != 0 && fg != hwnd {
		fgTID, _, _ = w32GetWindowThreadProcess.Call(fg, 0)
		selfTID = uintptr(windows.GetCurrentThreadId())
		if fgTID != 0 && fgTID != selfTID {
			if r, _, _ := w32AttachThreadInput.Call(selfTID, fgTID, 1); r != 0 {
				defer func() { _, _, _ = w32AttachThreadInput.Call(selfTID, fgTID, 0) }()
			}
		}
	}
	_, _, _ = w32BringWindowToTop.Call(hwnd)
	_, _, _ = w32SetForegroundWindow.Call(hwnd)
	fg, _, _ := w32GetForegroundWindow.Call()
	return fg == hwnd
}

var (
	enumWindowsMu       sync.Mutex
	enumWindowsResult   []uintptr
	enumWindowsCallback = sync.OnceValue(func() uintptr { return windows.NewCallback(windowEnumProc) })
)

func windowEnumProc(hwnd, _ uintptr) uintptr {
	enumWindowsResult = append(enumWindowsResult, hwnd)
	return 1
}

// snapshot は可視で cloaked でないトップレベルウィンドウの情報を、Z オーダー（前面が先）で集める。
func (wm *WindowManager) snapshot() ([]domain.WindowInfo, error) {
	enumWindowsMu.Lock()
	enumWindowsResult = nil
	r, _, err := w32EnumWindows.Call(enumWindowsCallback(), 0)
	handles := enumWindowsResult
	enumWindowsResult = nil
	enumWindowsMu.Unlock()
	if r == 0 {
		return nil, desktopOr(err, "EnumWindows")
	}

	fg, _, _ := w32GetForegroundWindow.Call()
	monitors, _ := listMonitors()
	cache := newProcessCache()
	out := make([]domain.WindowInfo, 0, len(handles))
	for _, hwnd := range handles {
		if visible, _, _ := w32IsWindowVisible.Call(hwnd); visible == 0 || isCloaked(hwnd) {
			continue
		}
		out = append(out, wm.info(hwnd, fg, monitors, cache))
	}
	return out, nil
}

// info は hwnd 1 つの事実を集める。fg は前面のウィンドウ。取得できない項目は零値（プロセス名は空）にする。
func (wm *WindowManager) info(hwnd, fg uintptr, monitors []domain.Monitor, cache *processCache) domain.WindowInfo {
	var pid uint32
	_, _, _ = w32GetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	proc := cache.lookup(pid)
	minimized, _, _ := w32IsIconic.Call(hwnd)
	rect := windowRect(hwnd)
	return domain.WindowInfo{
		ID:         formatWindowID(hwnd),
		Title:      windowTitle(hwnd),
		Process:    proc.name,
		PID:        pid,
		Rect:       rect,
		Monitor:    monitorIndexFor(monitors, rect),
		Foreground: hwnd == fg,
		Minimized:  minimized != 0,
		Elevated:   proc.elevated,
		Own:        pid == wm.selfPID,
	}
}

func windowTitle(hwnd uintptr) string {
	n, _, _ := w32GetWindowTextLength.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	got, _, _ := w32GetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:got])
}

// windowRect は目に見える枠の矩形（DWM の拡張フレーム。影の分を含まない）。取れなければ GetWindowRect。
func windowRect(hwnd uintptr) domain.Rect {
	var r w32Rect
	if w32DwmGetWindowAttribute.Find() == nil {
		if hr, _, _ := w32DwmGetWindowAttribute.Call(hwnd, dwmwaExtendedFrameBounds, uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); hr == 0 {
			return domain.Rect{X: int(r.Left), Y: int(r.Top), W: r.width(), H: r.height()}
		}
	}
	if ok, _, _ := w32GetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return domain.Rect{}
	}
	return domain.Rect{X: int(r.Left), Y: int(r.Top), W: r.width(), H: r.height()}
}

// isCloaked は DWM が隠しているウィンドウ（別の仮想デスクトップ、UWP の待機中）かを返す。
func isCloaked(hwnd uintptr) bool {
	if w32DwmGetWindowAttribute.Find() != nil {
		return false
	}
	var cloaked uint32
	hr, _, _ := w32DwmGetWindowAttribute.Call(hwnd, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked))
	return hr == 0 && cloaked != 0
}

// processInfo はプロセス 1 つの名前と昇格状態。
type processInfo struct {
	name     string
	elevated bool
}

// processCache は 1 回の列挙の間、同じ PID の問い合わせを 1 回にする。
type processCache map[uint32]processInfo

func newProcessCache() *processCache { c := processCache{}; return &c }

func (c *processCache) lookup(pid uint32) processInfo {
	if p, ok := (*c)[pid]; ok {
		return p
	}
	p := queryProcess(pid)
	(*c)[pid] = p
	return p
}

// queryProcess は pid の実行ファイル名と昇格状態を調べる。
// プロセスを開けない（保護されたプロセス、より高い権限のプロセス）ときは、名前を空にし、昇格している（入力できない）ものとして扱う。
func queryProcess(pid uint32) processInfo {
	if pid == 0 {
		return processInfo{}
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return processInfo{elevated: err == windows.ERROR_ACCESS_DENIED}
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var info processInfo
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil {
		info.name = processBaseName(windows.UTF16ToString(buf[:size]))
	}
	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		// 昇格したプロセスのトークンは、昇格していない呼び出し元からは開けない。
		info.elevated = err == windows.ERROR_ACCESS_DENIED
		return info
	}
	defer func() { _ = token.Close() }()
	info.elevated = token.IsElevated()
	return info
}
