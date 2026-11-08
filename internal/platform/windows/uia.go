package windows

import (
	"context"
	"strconv"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.UIAutomation = (*UIAutomation)(nil)

// uiaBackend は UI Automation の根要素を開く。専用スレッドの上でだけ呼ぶ（Windows では COM の IUIAutomation）。
type uiaBackend interface {
	// Root は HWND のウィンドウの UIA 要素を返す。呼び出し側が Release する。
	// ウィンドウが既に無ければ domain.ErrWindowNotFound。
	Root(hwnd uintptr) (uiaNode, error)
}

// UIAutomation は platform.UIAutomation の実装。COM の呼び出しは専用の OS スレッド 1 本へ直列化する。
// ウィンドウの指定（空なら前面）の解決は platform.WindowManager に任せる。使い終えたら Close を呼ぶ。
type UIAutomation struct {
	windows platform.WindowManager
	worker  *uiaWorker
	backend uiaBackend // 専用スレッドの上でだけ使う
}

// newUIAutomation は専用スレッドを起動し、そのスレッドの上で open を呼んで backend を作る。
func newUIAutomation(windows platform.WindowManager, open func() (uiaBackend, func(), error)) (*UIAutomation, error) {
	u := &UIAutomation{windows: windows}
	worker, err := startUIAWorker(func() (func(), error) {
		backend, cleanup, err := open()
		if err != nil {
			return nil, err
		}
		u.backend = backend
		return cleanup, nil
	})
	if err != nil {
		return nil, err
	}
	u.worker = worker
	return u, nil
}

// Close は専用スレッドを止める。複数回呼んでも安全。Close 後の呼び出しはエラーを返す。
func (u *UIAutomation) Close() { u.worker.Close() }

// uiaResolveWindow は windowID（空なら前面）のウィンドウを返す。存在しなければ ErrWindowNotFound。
func uiaResolveWindow(ctx context.Context, windows platform.WindowManager, windowID string) (domain.WindowInfo, error) {
	if windowID == "" {
		f, err := windows.Foreground()
		return f.Foreground, err
	}
	list, err := windows.List(ctx)
	if err != nil {
		return domain.WindowInfo{}, err
	}
	for _, w := range list {
		if w.ID == windowID {
			return w, nil
		}
	}
	return domain.WindowInfo{}, domain.ErrWindowNotFound.WithMsg("no window " + windowID)
}

// walk はウィンドウを解決し、専用スレッドの上で走査する。
func (u *UIAutomation) walk(ctx context.Context, windowID string, maxDepth, maxNodes int) (domain.WindowInfo, []uiaWalked, error) {
	if err := ctx.Err(); err != nil {
		return domain.WindowInfo{}, nil, err
	}
	if maxDepth < 1 || maxNodes < 1 {
		return domain.WindowInfo{}, nil, domain.ErrInvalidArgument.WithMsg("limits must be at least 1")
	}
	win, err := uiaResolveWindow(ctx, u.windows, windowID)
	if err != nil {
		return domain.WindowInfo{}, nil, err
	}
	hwnd, err := strconv.ParseUint(win.ID, 10, 64)
	if err != nil {
		return domain.WindowInfo{}, nil, domain.ErrWindowNotFound.WithMsg("window id is not a handle: " + win.ID)
	}
	var walked []uiaWalked
	err = u.worker.Do(ctx, func() error {
		root, err := u.backend.Root(uintptr(hwnd))
		if err != nil {
			return err
		}
		walked, err = walkUIA(ctx, root, maxDepth, maxNodes)
		return err
	})
	if err != nil {
		return domain.WindowInfo{}, nil, err
	}
	return win, walked, nil
}

// Tree は platform.UIAutomation.Tree。maxDepth・maxNodes が 1 未満なら ErrInvalidArgument。
func (u *UIAutomation) Tree(ctx context.Context, windowID string, maxDepth, maxNodes int) (domain.UINode, error) {
	_, walked, err := u.walk(ctx, windowID, maxDepth, maxNodes)
	if err != nil {
		return domain.UINode{}, err
	}
	return uiaTreeFromWalked(walked), nil
}

// uiaUnlimitedDepth は Snapshot の深さの上限（深さは制限せず、maxNodes だけで打ち切る）。
const uiaUnlimitedDepth = 1 << 30

// Snapshot は platform.UIAutomation.Snapshot。値（テキスト本文）は返さない。maxNodes が 1 未満なら ErrInvalidArgument。
func (u *UIAutomation) Snapshot(ctx context.Context, windowID string, maxNodes int) (domain.UISnapshot, error) {
	win, walked, err := u.walk(ctx, windowID, uiaUnlimitedDepth, maxNodes)
	if err != nil {
		return domain.UISnapshot{}, err
	}
	elements, digest := uiaSnapshotFromWalked(walked)
	return domain.UISnapshot{Window: win, Hash: digest, Elements: elements, Truncated: uiaAnyTruncated(walked)}, nil
}

func uiaAnyTruncated(nodes []uiaWalked) bool {
	for _, n := range nodes {
		if n.truncated {
			return true
		}
	}
	return false
}
