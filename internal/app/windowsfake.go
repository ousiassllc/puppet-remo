package app

import (
	"slices"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは Windows の偽の実装（テストと、実 Wails の無い開発用デモが使う。platform/platformtest と同じ位置づけ）。
// 呼び出しを記録するだけで、ウィンドウは作らない。

// FakeWindows は Windows の偽物。OpenApproval の呼び出しと各ダイアログが閉じられたか、ShowOverlay・HideOverlay の呼び出しを記録する。
type FakeWindows struct {
	main *FakeMainWindow

	mu       sync.Mutex
	approved []*FakeApprovalWindow
	opened   chan *FakeApprovalWindow

	overlayCalls  []OverlayCall
	overlayShown  bool
	overlayLabel  string
	overlayEvents chan OverlayCall
}

// OverlayCall は ShowOverlay・HideOverlay の呼び出し 1 件。
type OverlayCall struct {
	Hide     bool // true なら HideOverlay（Monitors・Label は空）
	Monitors []domain.Monitor
	Label    string
}

// NewFakeWindows は FakeWindows を返す。
func NewFakeWindows() *FakeWindows {
	return &FakeWindows{main: &FakeMainWindow{}, opened: make(chan *FakeApprovalWindow, 64), overlayEvents: make(chan OverlayCall, 64)}
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

// ShowOverlay は呼び出しを記録する。実装（Wails）と同じく monitors が空なら何もしない。
func (f *FakeWindows) ShowOverlay(monitors []domain.Monitor, label string) {
	if len(monitors) == 0 {
		return
	}
	c := OverlayCall{Monitors: slices.Clone(monitors), Label: label}
	f.mu.Lock()
	f.overlayCalls = append(f.overlayCalls, c)
	f.overlayShown, f.overlayLabel = true, label
	f.mu.Unlock()
	f.notifyOverlay(c)
}

// HideOverlay は出していたときだけ呼び出しを記録する（実装と同じく、出していなければ何もしない）。
func (f *FakeWindows) HideOverlay() {
	f.mu.Lock()
	if !f.overlayShown {
		f.mu.Unlock()
		return
	}
	f.overlayShown, f.overlayLabel = false, ""
	c := OverlayCall{Hide: true}
	f.overlayCalls = append(f.overlayCalls, c)
	f.mu.Unlock()
	f.notifyOverlay(c)
}

// notifyOverlay は OverlayEvents へ届ける。読み手がいなくて満杯のときは捨てる（呼び出し側を止めない。全記録は OverlayCalls）。
func (f *FakeWindows) notifyOverlay(c OverlayCall) {
	select {
	case f.overlayEvents <- c:
	default:
	}
}

// OverlayEvents は ShowOverlay・HideOverlay の呼び出しを、呼ばれた順に届ける（テストの待ち合わせ用。容量 64）。
func (f *FakeWindows) OverlayEvents() <-chan OverlayCall { return f.overlayEvents }

// OverlayCalls は ShowOverlay・HideOverlay の全記録（呼ばれた順）。
func (f *FakeWindows) OverlayCalls() []OverlayCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.overlayCalls)
}

// OverlayShown はオーバーレイを今出しているかと、その label。
func (f *FakeWindows) OverlayShown() (shown bool, label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overlayShown, f.overlayLabel
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
