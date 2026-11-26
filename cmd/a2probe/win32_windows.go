//go:build windows

package main

import (
	"errors"
	"image"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Win32 への薄い束縛（cgo なし）。このプローブ専用で、製品の internal/platform/windows には足さない。
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pGetModuleHandleW           = kernel32.NewProc("GetModuleHandleW")
	pRegisterClassExW           = user32.NewProc("RegisterClassExW")
	pCreateWindowExW            = user32.NewProc("CreateWindowExW")
	pDefWindowProcW             = user32.NewProc("DefWindowProcW")
	pGetMessageW                = user32.NewProc("GetMessageW")
	pTranslateMessage           = user32.NewProc("TranslateMessage")
	pDispatchMessageW           = user32.NewProc("DispatchMessageW")
	pPostMessageW               = user32.NewProc("PostMessageW")
	pSendMessageTimeoutW        = user32.NewProc("SendMessageTimeoutW")
	pPostQuitMessage            = user32.NewProc("PostQuitMessage")
	pDestroyWindow              = user32.NewProc("DestroyWindow")
	pSetWindowPos               = user32.NewProc("SetWindowPos")
	pGetWindowRect              = user32.NewProc("GetWindowRect")
	pGetClientRect              = user32.NewProc("GetClientRect")
	pBeginPaint                 = user32.NewProc("BeginPaint")
	pEndPaint                   = user32.NewProc("EndPaint")
	pFillRect                   = user32.NewProc("FillRect")
	pCreateMenu                 = user32.NewProc("CreateMenu")
	pCreatePopupMenu            = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                = user32.NewProc("AppendMenuW")
	pEndMenu                    = user32.NewProc("EndMenu")
	pEnumThreadWindows          = user32.NewProc("EnumThreadWindows")
	pClientToScreen             = user32.NewProc("ClientToScreen")
	pGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	pSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	pAllowSetForegroundWindow   = user32.NewProc("AllowSetForegroundWindow")
	pPrintWindow                = user32.NewProc("PrintWindow")
	pGetClassNameW              = user32.NewProc("GetClassNameW")
	pGetDpiForWindow            = user32.NewProc("GetDpiForWindow")
	pLoadCursorW                = user32.NewProc("LoadCursorW")
	pMapVirtualKeyW             = user32.NewProc("MapVirtualKeyW")
	pGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	pIsWindow                   = user32.NewProc("IsWindow")
	pGetWindow                  = user32.NewProc("GetWindow")
	pCreateSolidBrush           = gdi32.NewProc("CreateSolidBrush")
	pDeleteObject               = gdi32.NewProc("DeleteObject")
	pCreateCompatibleDC         = gdi32.NewProc("CreateCompatibleDC")
	pDeleteDC                   = gdi32.NewProc("DeleteDC")
	pCreateDIBSection           = gdi32.NewProc("CreateDIBSection")
	pSelectObject               = gdi32.NewProc("SelectObject")
	pSetBkMode                  = gdi32.NewProc("SetBkMode")
	pSetTextColor               = gdi32.NewProc("SetTextColor")
	pTextOutW                   = gdi32.NewProc("TextOutW")
	pCreatePen                  = gdi32.NewProc("CreatePen")
	pMoveToEx                   = gdi32.NewProc("MoveToEx")
	pLineTo                     = gdi32.NewProc("LineTo")
	pGetSaveFileNameW           = comdlg32.NewProc("GetSaveFileNameW")
	pCommDlgExtendedError       = comdlg32.NewProc("CommDlgExtendedError")
	errNoWindow                 = errors.New("window does not exist")
	errRectEmpty                = errors.New("window rect is empty")
	errPrintWindowNotDrawn      = errors.New("PrintWindow returned FALSE")
	errCreateDIBSectionFailed   = errors.New("CreateDIBSection failed")
	errCreateCompatibleDCFailed = errors.New("CreateCompatibleDC failed")
)

// ウィンドウメッセージ・スタイル・定数（このプローブが使うものだけ）。
const (
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmPaint         = 0x000F
	wmEraseBkgnd    = 0x0014
	wmKeyDown       = 0x0100
	wmKeyUp         = 0x0101
	wmChar          = 0x0102
	wmCommand       = 0x0111
	wmSysCommand    = 0x0112
	wmContextMenu   = 0x007B
	wmMeasureItem   = 0x002C
	wmDrawItem      = 0x002B
	wmApp           = 0x8000
	wmCancelMode    = 0x001F
	scKeyMenu       = 0xF100
	cdmGetSpec      = 0x0400 + 100 // WM_USER + 100
	cdmSetControlTx = 0x0400 + 104 // WM_USER + 104
	idFilenameCombo = 0x47C        // Explorer 形式の共通ダイアログのファイル名欄

	wsOverlapped       = 0x00000000
	wsCaption          = 0x00C00000
	wsSysMenu          = 0x00080000
	wsThickFrame       = 0x00040000
	wsMinimizeBox      = 0x00020000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsPopup            = 0x80000000
	wsBorder           = 0x00800000
	wsExToolWindow     = 0x00000080
	wsExTopmost        = 0x00000008
	wsExNoActivate     = 0x08000000
	esAutoHScroll      = 0x0080
	swShowNoActivate   = 4
	swShow             = 5
	swpNoActivate      = 0x0010
	swpShowWindow      = 0x0040
	swpNoZOrder        = 0x0004
	mfString           = 0x0000
	mfPopup            = 0x0010
	mfSeparator        = 0x0800
	mfOwnerDraw        = 0x0100
	smtoAbortIfHung    = 0x0002
	pwRenderFullConten = 0x00000002
	biRGB              = 0
	dibRGBColors       = 0
	transparentBk      = 1
	psSolid            = 0
	idcArrow           = 32512
	gwOwnerCmd         = 4
	cwUseDefault       = 0x80000000
)

var hwndTopmost = ^uintptr(0) // HWND_TOPMOST（-1）

// wndClassEx は WNDCLASSEXW。
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type rect32 struct{ Left, Top, Right, Bottom int32 }

func (r rect32) rectangle() image.Rectangle {
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom))
}

type msg32 struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type paintStruct struct {
	Hdc         uintptr
	Erase       int32
	Paint       rect32
	Restore     int32
	IncUpdate   int32
	RGBReserved [32]byte
}

type measureItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemWidth  uint32
	ItemHeight uint32
	ItemData   uintptr
}

type drawItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemAction uint32
	ItemState  uint32
	HwndItem   uintptr
	Hdc        uintptr
	Item       rect32
	ItemData   uintptr
}

type bitmapInfoHeader struct {
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

// openFileName は OPENFILENAMEW（amd64）。
type openFileName struct {
	StructSize    uint32
	Owner         uintptr
	Instance      uintptr
	Filter        *uint16
	CustomFilter  *uint16
	MaxCustFilter uint32
	FilterIndex   uint32
	File          *uint16
	MaxFile       uint32
	FileTitle     *uint16
	MaxFileTitle  uint32
	InitialDir    *uint16
	Title         *uint16
	Flags         uint32
	FileOffset    uint16
	FileExtension uint16
	DefExt        *uint16
	CustData      uintptr
	FnHook        uintptr
	TemplateName  *uint16
	PvReserved    uintptr
	DwReserved    uint32
	FlagsEx       uint32
}

// ptrAt は lParam などの整数を T へのポインタとして読む。uintptr → unsafe.Pointer の直接変換（go vet の unsafeptr）を避ける標準の書き方。
func ptrAt[T any](p uintptr) *T { return *(**T)(unsafe.Pointer(&p)) }

func utf16Ptr(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func makeLParam(x, y int) uintptr {
	return uintptr(uint32(uint16(int16(x))) | uint32(uint16(int16(y)))<<16)
}

func windowPID(hwnd uintptr) uint32 {
	var pid uint32
	_, _, _ = pGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func className(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := pGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func isWindow(hwnd uintptr) bool {
	r, _, _ := pIsWindow.Call(hwnd)
	return r != 0
}

func getWindowRect(hwnd uintptr) (image.Rectangle, bool) {
	var r rect32
	ok, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r.rectangle(), ok != 0
}

// sendTimeout は別スレッドのウィンドウへ同期でメッセージを送る（モーダルループの中でも処理される）。タイムアウトまたは失敗なら false。
func sendTimeout(hwnd uintptr, msg uint32, wparam, lparam uintptr, timeout time.Duration) (uintptr, bool) {
	var result uintptr
	r, _, _ := pSendMessageTimeoutW.Call(hwnd, uintptr(msg), wparam, lparam, smtoAbortIfHung, uintptr(timeout.Milliseconds()), uintptr(unsafe.Pointer(&result)))
	return result, r != 0
}

func postMessage(hwnd uintptr, msg uint32, wparam, lparam uintptr) bool {
	r, _, _ := pPostMessageW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r != 0
}

// printResult は PrintWindow 1 回の結果。img は GetWindowRect の大きさ（DIB の生の画素。アルファは PrintWindow が書いたまま）。
type printResult struct {
	Img        *image.RGBA
	Returned   bool
	WindowRect image.Rectangle
	Elapsed    time.Duration
	Err        error
	Win32Err   string
}

// printWindow は hwnd 1 枚を PrintWindow(flags) で撮る。メモリ DC・DIB だけを使い、画面の DC（GetDC(NULL)・BitBlt）には触れない。
// 撮る前に DIB を sentinel の色で塗り、PrintWindow が TRUE を返しても何も描かなかった場合を、黒いウィンドウと見分けられるようにする。
// 呼び出し側が、hwnd が照合済みの fixture のものであることを確かめてから呼ぶ（ここは確かめない）。
func printWindow(hwnd uintptr, flags uint32) printResult {
	var res printResult
	if !isWindow(hwnd) {
		res.Err = errNoWindow
		return res
	}
	wr, ok := getWindowRect(hwnd)
	if !ok || wr.Empty() {
		res.Err = errRectEmpty
		return res
	}
	res.WindowRect = wr
	w, h := wr.Dx(), wr.Dy()

	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		res.Err = errCreateCompatibleDCFailed
		return res
	}
	defer func() { _, _, _ = pDeleteDC.Call(dc) }()
	bmi := bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: biRGB}
	var bits unsafe.Pointer
	bmp, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bmi)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		res.Err = errCreateDIBSectionFailed
		return res
	}
	defer func() { _, _, _ = pDeleteObject.Call(bmp) }()
	old, _, _ := pSelectObject.Call(dc, bmp)
	defer func() { _, _, _ = pSelectObject.Call(dc, old) }()

	pix := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = sentinel.B, sentinel.G, sentinel.R, sentinel.A
	}

	start := time.Now()
	r, _, callErr := pPrintWindow.Call(hwnd, dc, uintptr(flags))
	res.Elapsed = time.Since(start)
	res.Returned = r != 0
	if !res.Returned {
		res.Err = errPrintWindowNotDrawn
		if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
			res.Win32Err = callErr.Error()
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(pix); i += 4 { // BGRA → RGBA。アルファは「PrintWindow が描いた画素=255、触られていない sentinel=0」にそろえる（製品の GrabGroup と同じ約束）
		a := byte(255)
		if pix[i] == sentinel.B && pix[i+1] == sentinel.G && pix[i+2] == sentinel.R {
			a = 0
		}
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pix[i+2], pix[i+1], pix[i], a
	}
	res.Img = img
	return res
}

func allowSetForeground(pid uint32) bool {
	r, _, _ := pAllowSetForegroundWindow.Call(uintptr(pid))
	return r != 0
}

func windowDPI(hwnd uintptr) uint32 {
	if pGetDpiForWindow.Find() != nil {
		return 0
	}
	d, _, _ := pGetDpiForWindow.Call(hwnd)
	return uint32(d)
}

// placeTopmost は hwnd を最前面の位置 r に動かして表示する（アクティブにしない）。別プロセスの interferer の窓を動かすのに使う。
func placeTopmost(hwnd uintptr, r image.Rectangle) bool {
	ok, _, _ := pSetWindowPos.Call(hwnd, hwndTopmost, uintptr(int32(r.Min.X)), uintptr(int32(r.Min.Y)), uintptr(int32(r.Dx())), uintptr(int32(r.Dy())), swpNoActivate|swpShowWindow)
	return ok != 0
}

func foregroundExists() bool {
	h, _, _ := pGetForegroundWindow.Call()
	return h != 0
}

// clientArea は hwnd のクライアント領域を、PrintWindow の画像（GetWindowRect の左上が原点）上の矩形で返す。取れなければ画像全体。
func clientArea(hwnd uintptr, wr image.Rectangle) image.Rectangle {
	var rc rect32
	if ok, _, _ := pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); ok == 0 {
		return image.Rect(0, 0, wr.Dx(), wr.Dy())
	}
	pt := struct{ X, Y int32 }{0, 0}
	if ok, _, _ := pClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return image.Rect(0, 0, wr.Dx(), wr.Dy())
	}
	r := image.Rect(int(pt.X)-wr.Min.X, int(pt.Y)-wr.Min.Y, int(pt.X)-wr.Min.X+int(rc.Right), int(pt.Y)-wr.Min.Y+int(rc.Bottom))
	return r.Intersect(image.Rect(0, 0, wr.Dx(), wr.Dy()))
}
