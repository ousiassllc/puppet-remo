//go:build windows

package windows

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.HostInputWatcher = (*HostInput)(nil)

const (
	whKeyboardLL = 13
	whMouseLL    = 14
	hcAction     = 0
)

var (
	w32SetWindowsHookEx   = w32User32.NewProc("SetWindowsHookExW")
	w32UnhookWindowsHook  = w32User32.NewProc("UnhookWindowsHookEx")
	w32CallNextHookEx     = w32User32.NewProc("CallNextHookEx")
	w32GetModuleHandleW   = w32Kernel32.NewProc("GetModuleHandleW")
	hostInputMouseProc    = windows.NewCallback(hostInputMouseHook)
	hostInputKeyboardProc = windows.NewCallback(hostInputKeyboardHook)
)

// msllHookStruct は MSLLHOOKSTRUCT。
type msllHookStruct struct {
	_           w32Point // pt（座標は読まない）
	_           uint32   // mouseData
	flags       uint32
	_           uint32 // time
	dwExtraInfo uintptr
}

// kbdllHookStruct は KBDLLHOOKSTRUCT。押されたキー（vkCode・scanCode）は読まない（検知するのは「入力があった」ことだけ）。
type kbdllHookStruct struct {
	_           uint32 // vkCode
	_           uint32 // scanCode
	flags       uint32
	_           uint32 // time
	dwExtraInfo uintptr
}

// hostInputSink は監視中のコールバック。フックはプロセスで 1 組だけ動かすので、Watch の間だけ非 nil。
var hostInputSink atomic.Pointer[func(kind platform.HostInputKind)]

// HostInput は platform.HostInputWatcher の Windows 実装（WH_MOUSE_LL / WH_KEYBOARD_LL）。
// フックは Watch ごとに専用のメッセージループの OS スレッドへ置き、stop で外してスレッドを止める。
// 低レベルフックのコールバックは OS が短い時間で打ち切るので、中ではフラグを見て fn を呼ぶだけにする。
type HostInput struct {
	mu     sync.Mutex
	active bool
}

// NewHostInput は HostInput を返す。スレッドとフックは Watch が作る。
func NewHostInput() *HostInput { return &HostInput{} }

func (h *HostInput) Watch(fn func(kind platform.HostInputKind)) (func(), error) {
	if fn == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("host input callback is nil")
	}
	h.mu.Lock()
	if h.active {
		h.mu.Unlock()
		return nil, errors.New("host input is already being watched")
	}
	h.active = true
	h.mu.Unlock()

	loop := &hostInputLoop{done: make(chan struct{})}
	ready := make(chan error, 1)
	hostInputSink.Store(&fn) // フックを張る前に入れる（張った直後の入力を取りこぼさない）
	go loop.run(ready)
	if err := <-ready; err != nil {
		hostInputSink.Store(nil)
		h.release()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			hostInputSink.Store(nil)
			loop.stop()
			h.release()
		})
	}, nil
}

func (h *HostInput) release() {
	h.mu.Lock()
	h.active = false
	h.mu.Unlock()
}

// hostInputLoop はフックを置いてメッセージを回す OS スレッド 1 本。
type hostInputLoop struct {
	threadID uint32
	done     chan struct{}
}

func (l *hostInputLoop) run(ready chan<- error) {
	runtime.LockOSThread()
	defer close(l.done)

	tid, _, _ := hotkeyProcGetCurrentThread.Call()
	l.threadID = uint32(tid)
	// PostThreadMessage はメッセージキューのあるスレッドにしか届かない。空の PeekMessage でキューを作る。
	var msg hotkeyMsg
	_, _, _ = hotkeyProcPeekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, hotkeyWMUser, hotkeyWMUser, hotkeyPMNoRemove)

	module, _, _ := w32GetModuleHandleW.Call(0)
	mouse, err := setHook(whMouseLL, hostInputMouseProc, module)
	if err != nil {
		ready <- err
		return
	}
	defer unhook(mouse)
	keyboard, err := setHook(whKeyboardLL, hostInputKeyboardProc, module)
	if err != nil {
		ready <- err
		return
	}
	defer unhook(keyboard)
	ready <- nil

	for {
		r, _, _ := hotkeyProcGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0: WM_QUIT、-1: エラー
			return
		}
	}
}

func (l *hostInputLoop) stop() {
	_, _, _ = hotkeyProcPostThreadMessage.Call(uintptr(l.threadID), hotkeyWMQuit, 0, 0)
	<-l.done
}

func setHook(id int, proc, module uintptr) (uintptr, error) {
	h, _, err := w32SetWindowsHookEx.Call(uintptr(id), proc, module, 0)
	if h == 0 {
		return 0, fmt.Errorf("SetWindowsHookEx(%d): %w", id, w32LastError(err))
	}
	return h, nil
}

func unhook(h uintptr) { _, _, _ = w32UnhookWindowsHook.Call(h) }

// hookStruct は低レベルフックの lParam（OS が渡す構造体へのポインタ）を T として読む。
func hookStruct[T any](lParam uintptr) *T {
	return (*T)(*(*unsafe.Pointer)(unsafe.Pointer(&lParam)))
}

func hostInputMouseHook(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) == hcAction {
		ev := hookStruct[msllHookStruct](lParam)
		if isHostMouseEvent(ev.flags, ev.dwExtraInfo) {
			if fn := hostInputSink.Load(); fn != nil {
				(*fn)(platform.HostInputMouse)
			}
		}
	}
	r, _, _ := w32CallNextHookEx.Call(0, nCode, wParam, lParam)
	return r
}

func hostInputKeyboardHook(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) == hcAction {
		ev := hookStruct[kbdllHookStruct](lParam)
		if isHostKeyEvent(ev.flags, ev.dwExtraInfo) {
			if fn := hostInputSink.Load(); fn != nil {
				(*fn)(platform.HostInputKeyboard)
			}
		}
	}
	r, _, _ := w32CallNextHookEx.Call(0, nCode, wParam, lParam)
	return r
}
