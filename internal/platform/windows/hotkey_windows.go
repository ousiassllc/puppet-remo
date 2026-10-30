//go:build windows

package windows

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	hotkeyWMHotkey = 0x0312
	hotkeyWMApp    = 0x8000 // 予約した仕事があることをループへ知らせる
	hotkeyWMUser   = 0x0400
	hotkeyWMQuit   = 0x0012

	hotkeyModNoRepeat = 0x4000 // 押しっぱなしで fn を繰り返し呼ばない
	hotkeyPMNoRemove  = 0x0000
)

var (
	hotkeyUser32                = syscall.NewLazyDLL("user32.dll")
	hotkeyKernel32              = syscall.NewLazyDLL("kernel32.dll")
	hotkeyProcRegisterHotKey    = hotkeyUser32.NewProc("RegisterHotKey")
	hotkeyProcUnregisterHotKey  = hotkeyUser32.NewProc("UnregisterHotKey")
	hotkeyProcGetMessage        = hotkeyUser32.NewProc("GetMessageW")
	hotkeyProcPeekMessage       = hotkeyUser32.NewProc("PeekMessageW")
	hotkeyProcPostThreadMessage = hotkeyUser32.NewProc("PostThreadMessageW")
	hotkeyProcGetCurrentThread  = hotkeyKernel32.NewProc("GetCurrentThreadId")
)

var errHotkeyClosed = errors.New("hotkey: message loop is closed")

// hotkeyMsg は Win32 の MSG。
type hotkeyMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
	private uint32
}

type hotkeyJob struct {
	fn  func() error
	res chan error // Post のときは nil
}

// hotkeyWinLoop はメッセージループを回す OS スレッド 1 本（hotkeyLoop の Windows 実装）。
type hotkeyWinLoop struct {
	fire func(id int32)

	threadID uint32
	done     chan struct{}

	mu     sync.Mutex
	queue  []hotkeyJob
	closed bool

	registered map[int32]struct{} // ループのスレッドだけが触る
}

// NewHotkey はメッセージループのスレッドを起動する。使い終えたら Close を呼ぶ。
func NewHotkey() (*Hotkey, error) {
	h := newHotkey(nil)
	loop, err := startHotkeyWinLoop(func(id int32) { h.fire(id) })
	if err != nil {
		return nil, err
	}
	h.loop = loop // fire は loop を読まないので、スレッドの起動後に設定してよい
	return h, nil
}

func startHotkeyWinLoop(fire func(id int32)) (*hotkeyWinLoop, error) {
	l := &hotkeyWinLoop{
		fire:       fire,
		done:       make(chan struct{}),
		registered: make(map[int32]struct{}),
	}
	ready := make(chan error, 1)
	go l.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return l, nil
}

func (l *hotkeyWinLoop) run(ready chan<- error) {
	runtime.LockOSThread()
	defer close(l.done)

	tid, _, _ := hotkeyProcGetCurrentThread.Call()
	l.threadID = uint32(tid)
	// PostThreadMessage はメッセージキューのあるスレッドにしか届かない。空の PeekMessage でキューを作る。
	var msg hotkeyMsg
	_, _, _ = hotkeyProcPeekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, hotkeyWMUser, hotkeyWMUser, hotkeyPMNoRemove)
	ready <- nil

	defer func() {
		for id := range l.registered {
			l.Unregister(id)
		}
	}()
	for {
		r, _, _ := hotkeyProcGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0: WM_QUIT、-1: エラー
			return
		}
		switch msg.message {
		case hotkeyWMHotkey:
			l.fire(int32(msg.wParam))
		case hotkeyWMApp:
			l.drain()
		}
	}
}

// drain は予約された仕事を予約順に実行する。
func (l *hotkeyWinLoop) drain() {
	l.mu.Lock()
	jobs := l.queue
	l.queue = nil
	l.mu.Unlock()
	for _, j := range jobs {
		err := j.fn()
		if j.res != nil {
			j.res <- err
		}
	}
}

func (l *hotkeyWinLoop) enqueue(j hotkeyJob) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errHotkeyClosed
	}
	l.queue = append(l.queue, j)
	l.mu.Unlock()
	r, _, err := hotkeyProcPostThreadMessage.Call(uintptr(l.threadID), hotkeyWMApp, 0, 0)
	if r == 0 {
		return fmt.Errorf("hotkey: PostThreadMessage: %w", err)
	}
	return nil
}

func (l *hotkeyWinLoop) Do(fn func() error) error {
	res := make(chan error, 1)
	if err := l.enqueue(hotkeyJob{fn: fn, res: res}); err != nil {
		return err
	}
	select {
	case err := <-res:
		return err
	case <-l.done:
		select {
		case err := <-res:
			return err
		default:
			return errHotkeyClosed
		}
	}
}

func (l *hotkeyWinLoop) Post(fn func()) {
	_ = l.enqueue(hotkeyJob{fn: func() error { fn(); return nil }}) // 閉じた後は解除済みなので捨ててよい
}

func (l *hotkeyWinLoop) Register(id int32, chord hotkeyChord) error {
	r, _, err := hotkeyProcRegisterHotKey.Call(0, uintptr(id), uintptr(chord.Mods|hotkeyModNoRepeat), uintptr(chord.VK))
	if r == 0 {
		return fmt.Errorf("RegisterHotKey: %w", err)
	}
	l.registered[id] = struct{}{}
	return nil
}

func (l *hotkeyWinLoop) Unregister(id int32) {
	if _, ok := l.registered[id]; !ok {
		return
	}
	_, _, _ = hotkeyProcUnregisterHotKey.Call(0, uintptr(id))
	delete(l.registered, id)
}

func (l *hotkeyWinLoop) Close() {
	l.mu.Lock()
	first := !l.closed
	l.closed = true
	l.mu.Unlock()
	if first {
		_, _, _ = hotkeyProcPostThreadMessage.Call(uintptr(l.threadID), hotkeyWMQuit, 0, 0)
	}
	<-l.done
}
