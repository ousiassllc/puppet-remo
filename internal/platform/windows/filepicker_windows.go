//go:build windows

package windows

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.FilePicker = (*FilePicker)(nil)

var (
	w32Comdlg32             = windows.NewLazySystemDLL("comdlg32.dll")
	w32GetOpenFileNameW     = w32Comdlg32.NewProc("GetOpenFileNameW")
	w32CommDlgExtendedError = w32Comdlg32.NewProc("CommDlgExtendedError")
	w32GetParent            = w32User32.NewProc("GetParent")
	w32PostMessageW         = w32User32.NewProc("PostMessageW")
)

const (
	ofnHideReadOnly   = 0x00000004
	ofnNoChangeDir    = 0x00000008
	ofnEnableHook     = 0x00000020
	ofnPathMustExist  = 0x00000800
	ofnFileMustExist  = 0x00001000
	ofnExplorer       = 0x00080000
	wmInitDialog      = 0x0110
	wmClose           = 0x0010
	pickerBufferChars = 32768 // 長いパス（32767 文字）まで受け取る
)

// openFileName は OPENFILENAMEW。
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

// FilePicker は platform.FilePicker の Windows 実装（GetOpenFileNameW。`.exe` のフィルター、メインウィンドウを親にしたモーダル）。
//
// ダイアログは呼び出した goroutine（OS スレッドに固定）で開き、閉じるまで戻らない。ctx の取り消しは、ダイアログの
// フック（WM_INITDIALOG）で得たウィンドウへ WM_CLOSE を送って閉じる（呼び出しの間だけ監視用の goroutine を 1 本持つ）。
// ダイアログは同時に 1 つ（呼び出しは直列化され、待っている間も ctx で抜けられる）。
type FilePicker struct {
	owner func() uintptr
}

// NewFilePicker はファイル選択の実装を作る。owner は親にするメインウィンドウの HWND を返す関数で（Wails のメインウィンドウの
// NativeWindow()。結線は app が行う）、nil、または 0・無効なハンドルを返すときは親なしで開く。
// 親ウィンドウは別のスレッド（Wails の UI スレッド）のものでよい。
func NewFilePicker(owner func() uintptr) *FilePicker { return &FilePicker{owner: owner} }

// pickSem はダイアログを同時に 1 つにする（フックが現在の呼び出しを 1 つだけ知ればよいようにする）。
var pickSem = make(chan struct{}, 1)

// pickSession はいま開いているダイアログ。フックがダイアログのウィンドウを書き、ctx の監視が読む。
type pickSession struct {
	ctx    context.Context
	dialog atomic.Uintptr
}

// close はダイアログが開いていれば WM_CLOSE を送る（キャンセルになる）。
func (s *pickSession) close() {
	if h := s.dialog.Load(); h != 0 {
		_, _, _ = w32PostMessageW.Call(h, wmClose, 0, 0)
	}
}

var (
	activePick     atomic.Pointer[pickSession]
	openFileHookCb = syscall.NewCallback(openFileHook)
)

// openFileHook は OFN のフック（ダイアログのスレッドで呼ばれる）。WM_INITDIALOG の hdlg は子のダイアログなので、親が本体。
// 本体のウィンドウを覚え、すでに ctx が終わっていたら（開く途中で取り消された）すぐ閉じる。常に 0 を返し、既定の処理に任せる。
func openFileHook(hdlg, msg, _, _ uintptr) uintptr {
	if msg != wmInitDialog {
		return 0
	}
	s := activePick.Load()
	if s == nil {
		return 0
	}
	if parent, _, _ := w32GetParent.Call(hdlg); parent != 0 {
		s.dialog.Store(parent)
	}
	if s.ctx.Err() != nil {
		s.close()
	}
	return 0
}

// PickExecutable はダイアログを開き、選ばれた実行ファイルのパスを返す。キャンセルは ok=false。
func (p *FilePicker) PickExecutable(ctx context.Context) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	select {
	case pickSem <- struct{}{}:
		defer func() { <-pickSem }()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	buf := make([]uint16, pickerBufferChars)
	title, err := windows.UTF16PtrFromString("実行ファイルを選択")
	if err != nil {
		return "", false, err
	}
	ofn := &openFileName{
		Owner:   p.ownerWindow(),
		Filter:  &openFileFilter[0],
		File:    &buf[0],
		MaxFile: uint32(len(buf)),
		Title:   title,
		Flags:   ofnExplorer | ofnEnableHook | ofnFileMustExist | ofnPathMustExist | ofnHideReadOnly | ofnNoChangeDir,
		FnHook:  openFileHookCb,
	}
	ofn.StructSize = uint32(unsafe.Sizeof(*ofn))

	s := &pickSession{ctx: ctx}
	activePick.Store(s)
	defer activePick.Store(nil)

	stop := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			s.close()
		case <-stop:
		}
	}()
	r, _, _ := w32GetOpenFileNameW.Call(uintptr(unsafe.Pointer(ofn)))
	close(stop)
	<-exited
	runtime.KeepAlive(ofn)
	runtime.KeepAlive(buf)

	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if r == 0 {
		if code, _, _ := w32CommDlgExtendedError.Call(); code != 0 {
			return "", false, fmt.Errorf("windows: file dialog failed (CommDlgExtendedError 0x%X)", code)
		}
		return "", false, nil // キャンセル
	}
	return windows.UTF16ToString(buf), true, nil
}

// ownerWindow は親にするウィンドウ。無い・無効なら 0（親なし）。
func (p *FilePicker) ownerWindow() uintptr {
	if p.owner == nil {
		return 0
	}
	h := p.owner()
	if h == 0 {
		return 0
	}
	if ok, _, _ := w32IsWindow.Call(h); ok == 0 {
		return 0
	}
	return h
}

// openFileFilter は OPENFILENAME のフィルター（表示名 NUL パターン NUL … NUL NUL）。
var openFileFilter = func() []uint16 {
	var f []uint16
	for _, s := range []string{"実行ファイル (*.exe)", "*.exe"} {
		f = append(f, windows.StringToUTF16(s)...)
	}
	return append(f, 0)
}()
