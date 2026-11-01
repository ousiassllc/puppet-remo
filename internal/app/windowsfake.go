package app

import (
	"sync"
)

// このファイルは Windows の偽の実装（テストと、実 Wails の無い開発用デモが使う。platform/platformtest と同じ位置づけ）。
// 呼び出しを記録するだけで、ウィンドウは作らない。

// FakeWindows は Windows の偽物。OpenApproval の呼び出しと、各ダイアログが閉じられたかを記録する。
type FakeWindows struct {
	main *FakeMainWindow

	mu       sync.Mutex
	approved []*FakeApprovalWindow
	opened   chan *FakeApprovalWindow
}

// NewFakeWindows は FakeWindows を返す。
func NewFakeWindows() *FakeWindows {
	return &FakeWindows{main: &FakeMainWindow{}, opened: make(chan *FakeApprovalWindow, 64)}
}

func (f *FakeWindows) Main() MainWindow { return f.main }

func (f *FakeWindows) OpenApproval(id string) Window {
	w := &FakeApprovalWindow{id: id, closed: make(chan struct{})}
	f.mu.Lock()
	f.approved = append(f.approved, w)
	f.mu.Unlock()
	f.opened <- w
	return w
}

// Opened は OpenApproval で開いたダイアログを、開いた順に届ける（テストの待ち合わせ用。容量 64 を超えて開くと OpenApproval が止まる）。
func (f *FakeWindows) Opened() <-chan *FakeApprovalWindow { return f.opened }

// ApprovalWindows は OpenApproval で開いたダイアログの全記録（開いた順）。
func (f *FakeWindows) ApprovalWindows() []*FakeApprovalWindow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*FakeApprovalWindow(nil), f.approved...)
}

// FakeApprovalWindow は偽の承認ダイアログ。
type FakeApprovalWindow struct {
	id     string
	once   sync.Once
	closed chan struct{}

	mu     sync.Mutex
	closes int
}

// ID は承認要求の ID。
func (w *FakeApprovalWindow) ID() string { return w.id }

// Close は Window。何度呼んでもよい（回数は Closes）。
func (w *FakeApprovalWindow) Close() {
	w.mu.Lock()
	w.closes++
	w.mu.Unlock()
	w.once.Do(func() { close(w.closed) })
}

// Closed は最初の Close で閉じるチャネル。
func (w *FakeApprovalWindow) Closed() <-chan struct{} { return w.closed }

// Closes は Close を呼ばれた回数。
func (w *FakeApprovalWindow) Closes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closes
}

// FakeMainWindow は偽のメインウィンドウ（MainWindow）。
type FakeMainWindow struct {
	mu      sync.Mutex
	visible bool
	paths   []string
}

func (w *FakeMainWindow) Show(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.visible = true
	w.paths = append(w.paths, path)
}

func (w *FakeMainWindow) Hide() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.visible = false
}

func (w *FakeMainWindow) Visible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.visible
}
