//go:build windows

package app

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 操作中オーバーレイ（#69・#86）の Windows 実装: Win32 のネイティブウィンドウで、モニターごとに
// 「縁の細い枠 4 本」と「上端中央の札」を出す。WebView2 の全面ウィンドウは使わない（#86: 透明が効かず、全モニターが真っ白になった）。
// 真ん中のウィンドウは最初から作らないので、画面の中身は覆わない。実機が要るため CI では動かず、実機スモーク
// （docs/operations/smoke-windows.md §4.12）で確認する。配置の計算は overlay_layout.go（偽物なしでテストしている）、
// 出し入れの判断は overlayController（overlay.go）にある。
//
// 全ウィンドウは同じ拡張スタイルと属性を持つ:
//   - WS_EX_TRANSPARENT | WS_EX_LAYERED: クリックスルー（マウスを下のウィンドウへ通す。WM_NCHITTEST も HTTRANSPARENT を返す）。
//     platform/windows の WindowManager はこの組で自プロセスのウィンドウを Overlay と判定し、InputGuard の own_window から外す
//     （domain.WindowInfo.Overlay）。ウィンドウにタイトルは付けない（list_windows に出さない）
//   - WS_EX_TOOLWINDOW: タスクバーと Alt+Tab に出さない
//   - WS_EX_NOACTIVATE と SW_SHOWNOACTIVATE・SWP_NOACTIVATE・WM_MOUSEACTIVATE=MA_NOACTIVATE: 操作中のウィンドウからフォーカスを奪わない
//   - WS_EX_TOPMOST: 最前面
//   - SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE): スクリーンショットにもライブ画面にも写さない（NFR-S06）。
//     設定できなければそのウィンドウを出さず、オーバーレイ全体を出さない（写ってしまうより出さないほうを選ぶ）
//
// Win32 のウィンドウは作ったスレッドでメッセージを回す必要があるので、専用の OS スレッド（overlayThread）1 本ですべて作る・直す・壊す。
const overlayExStyle = w32.WS_EX_TOPMOST | w32.WS_EX_TOOLWINDOW | w32.WS_EX_TRANSPARENT | w32.WS_EX_LAYERED | w32.WS_EX_NOACTIVATE

const (
	overlayClassName = "PuppetRemoOverlay"

	overlayBarAlpha   = 255 // 枠は不透明（細いので中身は隠れない）
	overlayLabelAlpha = 235

	// COLORREF は 0x00BBGGRR。枠は rose-500（#f43f5e）、札は rose-600（#e11d48）、文字は白。
	overlayBarColor   = 0x5e3ff4
	overlayLabelColor = 0x481de1
	overlayTextColor  = 0xffffff

	overlayFontFace      = "Segoe UI"
	overlayFontPtFirst   = 11 // 1 行目（太字）
	overlayFontPtDetail  = 9  // 2 行目
	overlayMaMouseNoActv = 3  // MA_NOACTIVATE

	drawTextFormat = w32.DT_CENTER | w32.DT_SINGLELINE | w32.DT_NOPREFIX | w32.DT_END_ELLIPSIS
)

var (
	overlayUser32                = windows.NewLazySystemDLL("user32.dll")
	overlayGdi32                 = windows.NewLazySystemDLL("gdi32.dll")
	overlayProcPostThreadMessage = overlayUser32.NewProc("PostThreadMessageW")
	overlayProcDrawText          = overlayUser32.NewProc("DrawTextW")
	overlayProcSetTextColor      = overlayGdi32.NewProc("SetTextColor")
	overlayProcSetBkMode         = overlayGdi32.NewProc("SetBkMode")
	overlayProcSetThreadDPI      = overlayUser32.NewProc("SetThreadDpiAwarenessContext")
)

// overlayWin は出している 1 枚のウィンドウ。overlayThread のスレッドだけが触る。
type overlayWin struct {
	hwnd uintptr
	// 札だけ: 文言・フォント・行の高さ・余白（描画で使う。枠は何も持たない）
	label                     bool
	first, detail             string
	fontFirst, fontDetail     uintptr
	heightFirst, heightDetail int
	textW                     int // 文字の幅の最大（物理 px）
	padX, padY                int
}

// overlayScreen は 1 モニター分のウィンドウ。
type overlayScreen struct {
	monitor domain.Monitor
	bars    []*overlayWin
	label   *overlayWin
}

// overlayThread はオーバーレイのウィンドウを持つメッセージループ用の OS スレッド（最初の ShowOverlay で起動する。プロセスの終了まで残る）。
type overlayThread struct {
	id uint32

	mu   sync.Mutex
	jobs []overlayJob

	// 以下はスレッドだけが触る。
	screens  []overlayScreen
	byHwnd   map[uintptr]*overlayWin
	barBrush uintptr
	lblBrush uintptr
}

type overlayJob struct {
	fn  func() error
	res chan error
}

// wailsOverlay は WailsHost が持つオーバーレイの状態。ShowOverlay・HideOverlay は overlayController の goroutine だけが呼ぶが、念のため mu で守る。
type wailsOverlay struct {
	mu       sync.Mutex
	th       *overlayThread
	monitors []domain.Monitor // 出しているウィンドウを作ったときのモニター構成（出していなければ nil）
}

// ShowOverlay は Windows の実装。すでに出していてモニター構成が同じなら、札の文言だけを差し替える。
// 構成が違えば（モニターの抜き差し・解像度・配置・DPI の変更）作り直す。出している最中の構成の変化は追わず、次の ShowOverlay で反映する。
// 失敗してもリースの取得は妨げない: ログに残し、出さない。
func (w wailsWindows) ShowOverlay(monitors []domain.Monitor, label string) {
	if len(monitors) == 0 {
		return
	}
	o := &w.h.overlay
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.th == nil {
		th, err := startOverlayThread()
		if err != nil {
			w.h.log.Warn("operation overlay is not shown", "error", err)
			return
		}
		o.th = th
	}
	first, detail := splitOverlayLabel(label)
	var err error
	if o.monitors != nil && slices.Equal(o.monitors, monitors) {
		err = o.th.do(func() error { return o.th.relabel(first, detail) })
	} else {
		o.monitors = nil
		err = o.th.do(func() error { return o.th.build(monitors, first, detail, w.h.warnOverlayBounds) })
		if err == nil {
			o.monitors = slices.Clone(monitors)
		}
	}
	if err != nil {
		o.monitors = nil
		w.h.log.Warn("operation overlay is not shown", "error", err)
	}
}

// HideOverlay は Windows の実装。出しているオーバーレイをすべて閉じる。
func (w wailsWindows) HideOverlay() {
	o := &w.h.overlay
	o.mu.Lock()
	defer o.mu.Unlock()
	o.monitors = nil
	if o.th == nil {
		return
	}
	if err := o.th.do(func() error { o.th.destroyAll(); return nil }); err != nil {
		w.h.log.Warn("operation overlay is not closed", "error", err)
	}
}

func (h *WailsHost) warnOverlayBounds(monitor int, want, got domain.Rect) {
	h.log.Warn("operation overlay is not exactly on the monitor", "monitor", monitor, "want", want, "got", got)
}

// --- スレッド ---

// overlayWndProcPtr はウィンドウプロシージャ（プロセスで 1 つ。コールバックの数に上限があるので使い回す）。
var overlayWndProcPtr = syscall.NewCallback(overlayWndProc)

// overlayThreadRef は overlayWndProc が自分のスレッドの状態を引くための参照。オーバーレイのスレッドは 1 本だけで、そのスレッドだけが読む。
var overlayThreadRef *overlayThread

func startOverlayThread() (*overlayThread, error) {
	t := &overlayThread{byHwnd: make(map[uintptr]*overlayWin)}
	ready := make(chan error, 1)
	go t.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return t, nil
}

func (t *overlayThread) run(ready chan<- error) {
	runtime.LockOSThread()
	t.id = windows.GetCurrentThreadId()
	overlayThreadRef = t
	// 座標を物理ピクセルで扱う（プロセスが Per-Monitor V2 でなくても、このスレッドのウィンドウは仮想化されない）。古い Windows では無い関数なので結果は見ない。
	if overlayProcSetThreadDPI.Find() == nil {
		_, _, _ = overlayProcSetThreadDPI.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (HANDLE)-4
	}
	// PostThreadMessage はメッセージキューのあるスレッドにしか届かない。空の PeekMessage でキューを作る。
	var msg w32.MSG
	w32.PeekMessage(&msg, 0, w32.WM_APP, w32.WM_APP, 0)

	if err := t.registerClass(); err != nil {
		ready <- err
		return
	}
	t.barBrush = w32.CreateSolidBrush(overlayBarColor)
	t.lblBrush = w32.CreateSolidBrush(overlayLabelColor)
	ready <- nil

	for {
		if r := w32.GetMessage(&msg, 0, 0, 0); r <= 0 { // 0: WM_QUIT、-1: エラー
			return
		}
		if msg.Message == w32.WM_APP {
			t.drain()
			continue
		}
		w32.TranslateMessage(&msg)
		w32.DispatchMessage(&msg)
	}
}

func (t *overlayThread) registerClass() error {
	name, err := windows.UTF16PtrFromString(overlayClassName)
	if err != nil {
		return err
	}
	cls := w32.WNDCLASSEX{
		Size:      uint32(unsafe.Sizeof(w32.WNDCLASSEX{})),
		WndProc:   overlayWndProcPtr,
		Instance:  w32.GetModuleHandle(""),
		ClassName: name,
	}
	if w32.RegisterClassEx(&cls) == 0 {
		return errors.New("overlay: RegisterClassEx failed")
	}
	return nil
}

// do は fn をオーバーレイのスレッドで実行し、終わるまで待つ。
func (t *overlayThread) do(fn func() error) error {
	res := make(chan error, 1)
	t.mu.Lock()
	t.jobs = append(t.jobs, overlayJob{fn: fn, res: res})
	t.mu.Unlock()
	if r, _, err := overlayProcPostThreadMessage.Call(uintptr(t.id), w32.WM_APP, 0, 0); r == 0 {
		return fmt.Errorf("overlay: PostThreadMessage: %w", err)
	}
	return <-res
}

func (t *overlayThread) drain() {
	t.mu.Lock()
	jobs := t.jobs
	t.jobs = nil
	t.mu.Unlock()
	for _, j := range jobs {
		j.res <- j.fn()
	}
}

// --- ウィンドウ（以下はオーバーレイのスレッドだけが呼ぶ）---

// build は既存のウィンドウを閉じて、monitors の全モニターに枠と札を作り直す。1 枚でも作れなければ、作ったものを全部閉じて失敗にする。
// ウィンドウは作る前に screens へ登録する（途中で失敗しても destroyAll がすべて閉じられる）。
func (t *overlayThread) build(monitors []domain.Monitor, first, detail string, warnBounds func(monitor int, want, got domain.Rect)) error {
	t.destroyAll()
	for _, m := range monitors {
		t.screens = append(t.screens, overlayScreen{monitor: m})
		s := &t.screens[len(t.screens)-1]
		for _, r := range overlayBars(m) {
			if r.Empty() {
				continue
			}
			bar := &overlayWin{}
			s.bars = append(s.bars, bar)
			if err := t.show(bar, r, overlayBarAlpha, m.Index, warnBounds); err != nil {
				t.destroyAll()
				return err
			}
		}
		lbl := &overlayWin{label: true, first: first, detail: detail}
		s.label = lbl
		if err := t.layoutLabel(s.monitor, lbl); err != nil {
			t.destroyAll()
			return err
		}
		if err := t.show(lbl, lbl.rect(s.monitor), overlayLabelAlpha, m.Index, warnBounds); err != nil {
			t.destroyAll()
			return err
		}
	}
	return nil
}

// relabel は出している札の文言を差し替える（大きさと位置を測り直して描き直す）。
func (t *overlayThread) relabel(first, detail string) error {
	for _, s := range t.screens {
		lbl := s.label
		if lbl == nil || lbl.hwnd == 0 {
			continue
		}
		lbl.first, lbl.detail = first, detail
		if err := t.layoutLabel(s.monitor, lbl); err != nil {
			return err
		}
		r := lbl.rect(s.monitor)
		w32.SetWindowPos(lbl.hwnd, w32.HWND_TOPMOST, r.X, r.Y, r.W, r.H, w32.SWP_NOACTIVATE)
		w32.InvalidateRect(lbl.hwnd, nil, true)
	}
	return nil
}

func (t *overlayThread) destroyAll() {
	for _, s := range t.screens {
		for _, b := range s.bars {
			t.destroyWindow(b)
		}
		if s.label != nil {
			t.destroyWindow(s.label)
		}
	}
	t.screens = nil
}

func (t *overlayThread) destroyWindow(w *overlayWin) {
	if w.hwnd != 0 {
		delete(t.byHwnd, w.hwnd)
		w32.DestroyWindow(w.hwnd)
		w.hwnd = 0
	}
	w.deleteFonts()
}

func (w *overlayWin) deleteFonts() {
	for _, f := range []*uintptr{&w.fontFirst, &w.fontDetail} {
		if *f != 0 {
			w32.DeleteObject(*f)
			*f = 0
		}
	}
}

// rect は札の矩形（モニターの上端の中央。物理座標）。layoutLabel で測ったあとに呼ぶ。
func (w *overlayWin) rect(m domain.Monitor) domain.Rect {
	return overlayLabelRect(m, w.textW, w.heightFirst+w.heightDetail)
}

// show は w を r（物理座標）に、クリックスルー・フォーカスを奪わない・キャプチャ除外のウィンドウとして作って表示する。
// タイトルは付けない。位置と大きさは物理ピクセルで渡す（スレッドは Per-Monitor DPI Aware V2 のプロセスの一部で、座標は仮想化されない）。
func (t *overlayThread) show(w *overlayWin, r domain.Rect, alpha byte, monitor int, warnBounds func(int, domain.Rect, domain.Rect)) error {
	class, err := windows.UTF16PtrFromString(overlayClassName)
	if err != nil {
		return err
	}
	hwnd := w32.CreateWindowEx(overlayExStyle, class, nil, w32.WS_POPUP, r.X, r.Y, r.W, r.H, 0, 0, w32.GetModuleHandle(""), nil)
	if hwnd == 0 {
		return errors.New("overlay: CreateWindowEx failed")
	}
	w.hwnd = hwnd
	t.byHwnd[hwnd] = w
	if !w32.SetLayeredWindowAttributes(hwnd, 0, alpha, w32.LWA_ALPHA) {
		return errors.New("overlay: SetLayeredWindowAttributes failed")
	}
	if !w32.SetWindowDisplayAffinity(hwnd, w32.WDA_EXCLUDEFROMCAPTURE) {
		return errors.New("overlay: SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE) failed")
	}
	place := func() {
		w32.SetWindowPos(hwnd, w32.HWND_TOPMOST, r.X, r.Y, r.W, r.H, w32.SWP_NOACTIVATE|w32.SWP_SHOWWINDOW)
	}
	place()
	// DPI の違うモニターへ置くと、最初の指定がその DPI の再計算でずれることがあるので、確かめて 1 度だけ置き直す。
	if got := windowBounds(hwnd); got != r {
		place()
		if got = windowBounds(hwnd); got != r {
			warnBounds(monitor, r, got)
		}
	}
	return nil
}

func windowBounds(hwnd uintptr) domain.Rect {
	rc := w32.GetWindowRect(hwnd)
	if rc == nil {
		return domain.Rect{}
	}
	return domain.Rect{X: int(rc.Left), Y: int(rc.Top), W: int(rc.Right - rc.Left), H: int(rc.Bottom - rc.Top)}
}

// layoutLabel は札 lbl のフォントを（モニターの DPI で）作り直し、文字の行の高さと余白を測る。
func (t *overlayThread) layoutLabel(m domain.Monitor, lbl *overlayWin) error {
	dpi := int(m.DPI)
	if dpi <= 0 {
		dpi = 96
	}
	for _, f := range []*uintptr{&lbl.fontFirst, &lbl.fontDetail} {
		if *f != 0 {
			w32.DeleteObject(*f)
			*f = 0
		}
	}
	lbl.fontFirst = createOverlayFont(overlayFontPtFirst, dpi, w32.FW_SEMIBOLD)
	lbl.fontDetail = createOverlayFont(overlayFontPtDetail, dpi, w32.FW_NORMAL)
	if lbl.fontFirst == 0 || lbl.fontDetail == 0 {
		return errors.New("overlay: CreateFontIndirect failed")
	}
	lbl.padX, lbl.padY = overlayPx(m, overlayLabelPadXDIP), overlayPx(m, overlayLabelPadYDIP)

	hdc := w32.GetDC(0)
	if hdc == 0 {
		return errors.New("overlay: GetDC failed")
	}
	defer w32.ReleaseDC(0, hdc)
	width, h1 := measureOverlayText(hdc, lbl.fontFirst, lbl.first)
	lbl.heightFirst = h1
	lbl.heightDetail = 0
	if lbl.detail != "" {
		w2, h2 := measureOverlayText(hdc, lbl.fontDetail, lbl.detail)
		width, lbl.heightDetail = max(width, w2), h2
	}
	lbl.textW = width
	return nil
}

func createOverlayFont(pt, dpi int, weight int32) uintptr {
	lf := w32.LOGFONT{
		Height:  -int32(w32.MulDiv(pt, dpi, 72)),
		Weight:  weight,
		CharSet: w32.DEFAULT_CHARSET,
		Quality: w32.CLEARTYPE_QUALITY,
	}
	copy(lf.FaceName[:], windows.StringToUTF16(overlayFontFace))
	return w32.CreateFontIndirect(&lf)
}

// measureOverlayText は font で s を 1 行で描いたときの幅と高さ（物理 px）。s が空なら 0・フォントの行の高さ。
func measureOverlayText(hdc, font uintptr, s string) (width, height int) {
	old := w32.SelectObject(hdc, font)
	defer w32.SelectObject(hdc, old)
	u := windows.StringToUTF16(s) // 末尾の NUL 付き
	var sz w32.SIZE
	w32.GetTextExtentPoint32(hdc, &u[0], len(u)-1, &sz)
	return int(sz.CX), int(sz.CY)
}

// --- ウィンドウプロシージャ ---

func overlayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case w32.WM_NCHITTEST:
		return ^uintptr(0) // HTTRANSPARENT: マウスを下のウィンドウへ通す
	case w32.WM_MOUSEACTIVATE:
		return overlayMaMouseNoActv
	case w32.WM_ERASEBKGND:
		return 1 // WM_PAINT がすべて塗る（ちらつかせない）
	case w32.WM_PAINT:
		if t := overlayThreadRef; t != nil {
			t.paint(hwnd)
			return 0
		}
	}
	return w32.DefWindowProc(hwnd, uint32(msg), wParam, lParam)
}

// paint は hwnd のウィンドウを描く: 枠は 1 色で塗るだけ、札は塗って文字を重ねる。
func (t *overlayThread) paint(hwnd uintptr) {
	var ps w32.PAINTSTRUCT
	hdc := w32.BeginPaint(hwnd, &ps)
	defer w32.EndPaint(hwnd, &ps)
	if hdc == 0 {
		return
	}
	rc := w32.GetClientRect(hwnd)
	if rc == nil {
		return
	}
	w, ok := t.byHwnd[hwnd]
	if !ok || !w.label {
		w32.FillRect(hdc, rc, t.barBrush)
		return
	}
	w32.FillRect(hdc, rc, t.lblBrush)
	_, _, _ = overlayProcSetBkMode.Call(hdc, w32.TRANSPARENT)
	_, _, _ = overlayProcSetTextColor.Call(hdc, overlayTextColor)
	left, right := int32(w.padX), rc.Right-int32(w.padX)
	top := int32(w.padY)
	drawOverlayLine(hdc, w.fontFirst, w.first, &w32.RECT{Left: left, Top: top, Right: right, Bottom: top + int32(w.heightFirst)})
	if w.detail != "" {
		top += int32(w.heightFirst)
		drawOverlayLine(hdc, w.fontDetail, w.detail, &w32.RECT{Left: left, Top: top, Right: right, Bottom: top + int32(w.heightDetail)})
	}
}

// drawOverlayLine は rc の中に s を 1 行で描く（入りきらなければ末尾を「…」にする）。
func drawOverlayLine(hdc, font uintptr, s string, rc *w32.RECT) {
	if s == "" {
		return
	}
	old := w32.SelectObject(hdc, font)
	defer w32.SelectObject(hdc, old)
	u := windows.StringToUTF16(s)
	_, _, _ = overlayProcDrawText.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(rc)), drawTextFormat)
}
