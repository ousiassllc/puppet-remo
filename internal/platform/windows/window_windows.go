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
	all, err := wm.snapshot(false)
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

// ListAll は可視で cloaked でないすべてのトップレベルウィンドウを、Z オーダー（前面が先）で返す。タイトルのないウィンドウ・
// ツールウィンドウ・ポップアップ・メニューを含み、所有関係の鎖・クラス・プロセスの開始時刻・スタイルを埋める（対象グループの判定用）。
// 取れなかった事実は不明（零値、鎖は検証不能の印）のままにする。
func (wm *WindowManager) ListAll(ctx context.Context) ([]domain.WindowInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return wm.snapshot(true)
}

// At は p 上の最前面のウィンドウを返す。タイトルのないウィンドウも対象にする。最小化されたウィンドウは含めない。
// 操作中オーバーレイ（クリックスルーの自ウィンドウ。モニターの縁の枠と札）は入力が下へ通るので飛ばし、その下の実ウィンドウを返す
// （下にある承認ダイアログなどが own_window の判定から漏れないようにする。#69）。
// 葉（GA_ROOT に引き上げる前の子ウィンドウ）は WindowFromPoint で求め、その最上位が返すウィンドウと一致したときだけ採る。
// 一致しない・取れないときは葉を不明（零値）にし、判定側が止める（推測しない）。
func (wm *WindowManager) At(p domain.Point) (domain.HitInfo, bool, error) {
	all, err := wm.snapshot(true)
	if err != nil {
		return domain.HitInfo{}, false, err
	}
	for _, w := range all {
		if !w.Minimized && !w.Overlay && w.Rect.Contains(p) {
			return domain.HitInfo{Top: w, Leaf: wm.leafAt(p, w)}, true, nil
		}
	}
	return domain.HitInfo{}, false, nil
}

// leafAt は p にある最も深いウィンドウ（WindowFromPoint）を、top の配下にあるときだけ葉として返す。
func (wm *WindowManager) leafAt(p domain.Point, top domain.WindowInfo) domain.LeafRef {
	topHWND, ok := parseWindowID(top.ID)
	if !ok {
		return domain.LeafRef{}
	}
	leaf := windowFromPoint(p)
	if leaf == 0 || rootOf(leaf) != topHWND {
		return domain.LeafRef{}
	}
	if leaf == topHWND {
		return domain.LeafOf(top)
	}
	f := wm.facts(leaf, newProcessCache())
	if f.link.Unverifiable {
		return domain.LeafRef{}
	}
	return domain.LeafRef{WindowRef: domain.WindowRef{ID: f.link.ID, PID: f.link.PID, ProcessStart: f.link.ProcessStart, Class: f.link.Class}, Process: f.process}
}

// Foreground は前面のウィンドウと、そのスレッドのフォーカス先の葉（GetGUIThreadInfo の hwndFocus。GA_ROOT に引き上げる前）を返す。
// 前面のウィンドウがなければ ErrWindowNotFound（デスクトップを使えなければ ErrDesktopUnavailable）。
// hwndFocus が無い（フォーカスのある子がない）ときは前面のウィンドウ自身を葉にし、GetGUIThreadInfo が失敗したときは葉を不明（零値）にする。
func (wm *WindowManager) Foreground() (domain.FocusInfo, error) {
	hwnd, _, _ := w32GetForegroundWindow.Call()
	if hwnd == 0 {
		if !desktopAvailable() {
			return domain.FocusInfo{}, domain.ErrDesktopUnavailable.WithMsg("the input desktop is not accessible")
		}
		return domain.FocusInfo{}, domain.ErrWindowNotFound.WithMsg("there is no foreground window")
	}
	monitors, _ := listMonitors()
	cache := newProcessCache()
	fg := wm.fullInfo(hwnd, hwnd, monitors, cache)
	focus := domain.FocusInfo{Foreground: fg}
	gti, ok := guiThreadInfo(hwnd)
	switch {
	case !ok:
	case gti.hwndFocus == 0 || gti.hwndFocus == hwnd:
		focus.FocusLeaf = domain.LeafOf(fg)
	default:
		f := wm.facts(gti.hwndFocus, cache)
		if !f.link.Unverifiable {
			focus.FocusLeaf = domain.LeafRef{WindowRef: domain.WindowRef{ID: f.link.ID, PID: f.link.PID, ProcessStart: f.link.ProcessStart, Class: f.link.Class}, Process: f.process}
		}
	}
	return focus, nil
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
	w32GetWindowLongPtr = w32User32.NewProc("GetWindowLongPtrW")
	w32GetClassNameW    = w32User32.NewProc("GetClassNameW")
	w32GetWindow        = w32User32.NewProc("GetWindow")
	w32GetAncestor      = w32User32.NewProc("GetAncestor")
	w32GetDesktopWindow = w32User32.NewProc("GetDesktopWindow")
	w32GetGUIThreadInfo = w32User32.NewProc("GetGUIThreadInfo")
	w32WindowFromPoint  = w32User32.NewProc("WindowFromPoint")

	enumWindowsMu       sync.Mutex
	enumWindowsResult   []uintptr
	enumWindowsCallback = sync.OnceValue(func() uintptr { return windows.NewCallback(windowEnumProc) })
)

const (
	gwlStyle   = ^uintptr(15) // GWL_STYLE（-16）
	gwlExStyle = ^uintptr(19) // GWL_EXSTYLE（-20）

	gwOwner   = 4 // GW_OWNER
	gaParent  = 1 // GA_PARENT
	gaRoot    = 2 // GA_ROOT
	maxClass  = 256
	menuClass = domain.ClassMenu
)

func windowEnumProc(hwnd, _ uintptr) uintptr {
	enumWindowsResult = append(enumWindowsResult, hwnd)
	return 1
}

// snapshot は可視で cloaked でないトップレベルウィンドウの情報を、Z オーダー（前面が先）で集める。
// full が true のときは所有関係の鎖（OwnerChain・MenuOwnerChain）も集める（List は使わないので集めない）。
func (wm *WindowManager) snapshot(full bool) ([]domain.WindowInfo, error) {
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
		if full {
			out = append(out, wm.fullInfo(hwnd, fg, monitors, cache))
		} else {
			out = append(out, wm.info(hwnd, fg, monitors, cache))
		}
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
	own := pid == wm.selfPID
	ex := windowExStyle(hwnd)
	return domain.WindowInfo{
		ID:           formatWindowID(hwnd),
		Title:        windowTitle(hwnd),
		Process:      proc.name,
		PID:          pid,
		ProcessStart: proc.start,
		Class:        windowClass(hwnd),
		Style:        decodeStyle(windowStyle(hwnd), ex),
		Rect:         rect,
		Monitor:      monitorIndexFor(monitors, rect),
		Foreground:   hwnd == fg,
		Minimized:    minimized != 0,
		Elevated:     proc.elevated,
		Own:          own,
		Overlay:      own && isOverlayStyle(ex),
	}
}

// fullInfo は info に所有関係の鎖を足す。OwnerChain は GW_OWNER（所有者がない子ウィンドウは親）を辿る。
// メニュー（クラス #32768）は、そのスレッドの hwndMenuOwner（GetGUIThreadInfo）を起点に MenuOwnerChain を作る。
// hwndMenuOwner が取れない・メニューモードでない（0）ときは MenuOwnerID を空にする（判定側が related_unverifiable で止める）。
func (wm *WindowManager) fullInfo(hwnd, fg uintptr, monitors []domain.Monitor, cache *processCache) domain.WindowInfo {
	w := wm.info(hwnd, fg, monitors, cache)
	fact := func(h uintptr) (domain.WindowLink, bool) {
		f := wm.facts(h, cache)
		return f.link, !f.link.Unverifiable
	}
	w.OwnerChain = walkLinks(ownerOf(hwnd), hwnd, ownerOf, fact)
	if w.Class == menuClass {
		if gti, ok := guiThreadInfo(hwnd); ok && gti.hwndMenuOwner != 0 {
			w.MenuOwnerID = formatWindowID(gti.hwndMenuOwner)
			w.MenuOwnerChain = walkLinks(gti.hwndMenuOwner, hwnd, ownerOf, fact)
		}
	}
	return w
}

// windowFacts は 1 枚のウィンドウの同一性とプロセス名。link.Unverifiable は事実を取れなかった（消えた・PID が取れない）こと。
type windowFacts struct {
	link    domain.WindowLink
	process string
}

// facts は hwnd の ID・PID・開始時刻・クラスを集める。ウィンドウが消えている・PID が取れないときは link を検証不能にする。
func (wm *WindowManager) facts(hwnd uintptr, cache *processCache) windowFacts {
	if r, _, _ := w32IsWindow.Call(hwnd); r == 0 {
		return windowFacts{link: domain.UnverifiableLink()}
	}
	var pid uint32
	_, _, _ = w32GetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return windowFacts{link: domain.UnverifiableLink()}
	}
	proc := cache.lookup(pid)
	return windowFacts{
		link:    domain.WindowLink{ID: formatWindowID(hwnd), PID: pid, ProcessStart: proc.start, Class: windowClass(hwnd)},
		process: proc.name,
	}
}

// ownerOf は hwnd の所有者（GW_OWNER）を返す。所有者がなく子ウィンドウなら親（デスクトップは除く）。なければ 0。
func ownerOf(hwnd uintptr) uintptr {
	if owner, _, _ := w32GetWindow.Call(hwnd, gwOwner); owner != 0 {
		return owner
	}
	parent, _, _ := w32GetAncestor.Call(hwnd, gaParent)
	if desktop, _, _ := w32GetDesktopWindow.Call(); parent == desktop {
		return 0
	}
	return parent
}

// rootOf は hwnd の最上位の祖先（GA_ROOT。所有者は辿らない）。
func rootOf(hwnd uintptr) uintptr {
	root, _, _ := w32GetAncestor.Call(hwnd, gaRoot)
	return root
}

// windowFromPoint は p にある最も深いウィンドウ（なければ 0）。POINT は 64 ビットでは 1 つのレジスタに詰めて渡す（このパッケージは amd64 だけ）。
func windowFromPoint(p domain.Point) uintptr {
	packed := uint64(uint32(int32(p.X))) | uint64(uint32(int32(p.Y)))<<32
	hwnd, _, _ := w32WindowFromPoint.Call(uintptr(packed))
	return hwnd
}

// w32GUIThreadInfo は GUITHREADINFO。
type w32GUIThreadInfo struct {
	cbSize        uint32
	flags         uint32
	hwndActive    uintptr
	hwndFocus     uintptr
	hwndCapture   uintptr
	hwndMenuOwner uintptr
	hwndMoveSize  uintptr
	hwndCaret     uintptr
	rcCaret       w32Rect
}

// guiThreadInfo は hwnd を作ったスレッドの GUITHREADINFO。取れなければ ok=false。
func guiThreadInfo(hwnd uintptr) (info w32GUIThreadInfo, ok bool) {
	tid, _, _ := w32GetWindowThreadProcess.Call(hwnd, 0)
	if tid == 0 {
		return info, false
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, _ := w32GetGUIThreadInfo.Call(tid, uintptr(unsafe.Pointer(&info)))
	return info, r != 0
}

// windowClass は hwnd のクラス名。取れなければ空（不明）。
func windowClass(hwnd uintptr) string {
	var buf [maxClass]uint16
	n, _, _ := w32GetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// windowStyle は hwnd のスタイル（GWL_STYLE）。取れなければ 0。
func windowStyle(hwnd uintptr) uintptr {
	r, _, _ := w32GetWindowLongPtr.Call(hwnd, gwlStyle)
	return r
}

// windowExStyle は hwnd の拡張ウィンドウスタイル（GWL_EXSTYLE）。取れなければ 0。
func windowExStyle(hwnd uintptr) uintptr {
	r, _, _ := w32GetWindowLongPtr.Call(hwnd, gwlExStyle)
	return r
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

// processInfo はプロセス 1 つの名前・昇格状態・開始時刻。
type processInfo struct {
	name     string
	elevated bool
	start    uint64 // 作成時刻（GetProcessTimes の FILETIME）。0 は不明（開けない・取れない）
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
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
		info.start = filetimeValue(created.HighDateTime, created.LowDateTime)
	}
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
