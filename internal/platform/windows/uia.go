package windows

import (
	"context"
	"errors"
	"slices"
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

// uiaWindowHandle は win の HWND。ID が HWND の 10 進文字列でなければ ErrWindowNotFound。
func uiaWindowHandle(win domain.WindowInfo) (uint64, error) {
	hwnd, err := strconv.ParseUint(win.ID, 10, 64)
	if err != nil {
		return 0, domain.ErrWindowNotFound.WithMsg("window id is not a handle: " + win.ID)
	}
	return hwnd, nil
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
	walked, err := u.walkWindow(ctx, win, maxDepth, maxNodes)
	if err != nil {
		return domain.WindowInfo{}, nil, err
	}
	return win, walked, nil
}

// walkWindow は解決済みの win を専用スレッドの上で走査する。
func (u *UIAutomation) walkWindow(ctx context.Context, win domain.WindowInfo, maxDepth, maxNodes int) ([]uiaWalked, error) {
	hwnd, err := uiaWindowHandle(win)
	if err != nil {
		return nil, err
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
	return walked, err
}

// Tree は platform.UIAutomation.Tree。maxDepth・maxNodes が 1 未満なら ErrInvalidArgument。
func (u *UIAutomation) Tree(ctx context.Context, windowID string, maxDepth, maxNodes int) (domain.UINode, error) {
	win, walked, err := u.walk(ctx, windowID, maxDepth, maxNodes)
	if err != nil {
		return domain.UINode{}, err
	}
	hwnd, err := uiaWindowHandle(win)
	if err != nil {
		return domain.UINode{}, err
	}
	return uiaTreeFromWalked(walked, uiaIDMaker{hwnd: hwnd, pid: win.PID}), nil
}

// Find は platform.UIAutomation.Find。走査は浅い階層から（walkUIA の幅優先）で、結果もその順。
func (u *UIAutomation) Find(ctx context.Context, windowID string, match func(domain.UIElementInfo) bool, maxResults, maxNodes int) (domain.UIFindResult, error) {
	if maxResults < 1 {
		return domain.UIFindResult{}, domain.ErrInvalidArgument.WithMsg("limits must be at least 1")
	}
	win, walked, err := u.walk(ctx, windowID, uiaUnlimitedDepth, maxNodes)
	if err != nil {
		return domain.UIFindResult{}, err
	}
	hwnd, err := uiaWindowHandle(win)
	if err != nil {
		return domain.UIFindResult{}, err
	}
	ids := uiaIDMaker{hwnd: hwnd, pid: win.PID}
	res := domain.UIFindResult{Window: win, Truncated: uiaAnyTruncated(walked)}
	for _, w := range walked {
		info := uiaElementInfo(w.props)
		if !match(info) {
			continue
		}
		if len(res.Elements) == maxResults {
			res.Truncated = true
			break
		}
		info.ID = ids.id(w.props.RuntimeID)
		res.Elements = append(res.Elements, info)
	}
	return res, nil
}

// uiaResolveMaxNodes は Resolve が要素を探して走査する節点の上限。
const uiaResolveMaxNodes = 50_000

// Resolve は platform.UIAutomation.Resolve。HWND のウィンドウが一覧にあり、PID が ID のものと同じで、
// その木に RuntimeId の同じ要素があるときだけ返す。それ以外は ErrStaleElement。
func (u *UIAutomation) Resolve(ctx context.Context, ref domain.ElementRef) (domain.UIElementInfo, error) {
	if err := ctx.Err(); err != nil {
		return domain.UIElementInfo{}, err
	}
	if len(ref.RuntimeID) == 0 {
		return domain.UIElementInfo{}, domain.ErrStaleElement
	}
	win, err := uiaResolveWindow(ctx, u.windows, ref.WindowID())
	if errors.Is(err, domain.ErrWindowNotFound) || err == nil && win.PID != ref.PID {
		return domain.UIElementInfo{}, domain.ErrStaleElement
	}
	if err != nil {
		return domain.UIElementInfo{}, err
	}
	walked, err := u.walkWindow(ctx, win, uiaUnlimitedDepth, uiaResolveMaxNodes)
	if errors.Is(err, domain.ErrWindowNotFound) {
		return domain.UIElementInfo{}, domain.ErrStaleElement
	}
	if err != nil {
		return domain.UIElementInfo{}, err
	}
	for _, w := range walked {
		if slices.Equal(w.props.RuntimeID, ref.RuntimeID) {
			info := uiaElementInfo(w.props)
			info.ID = ref.String()
			return info, nil
		}
	}
	return domain.UIElementInfo{}, domain.ErrStaleElement
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
