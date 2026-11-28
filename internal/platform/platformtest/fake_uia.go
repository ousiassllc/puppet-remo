package platformtest

import (
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.UIAutomation = (*FakeUIAutomation)(nil)

// FakeUIAutomation は platform.UIAutomation の偽の実装。ウィンドウの有無は FakeWindows に問い合わせる。
type FakeUIAutomation struct {
	Recorder

	windows *FakeWindows

	mu        sync.Mutex
	trees     map[string]domain.UINode
	snapshots map[string]domain.UISnapshot
}

func NewFakeUIAutomation(windows *FakeWindows) *FakeUIAutomation {
	return &FakeUIAutomation{
		windows:   windows,
		trees:     make(map[string]domain.UINode),
		snapshots: make(map[string]domain.UISnapshot),
	}
}

// SetTree は windowID の完全なツリーを設定する。未設定のウィンドウは根だけのツリーを返す。
func (f *FakeUIAutomation) SetTree(windowID string, root domain.UINode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trees[windowID] = root
}

// SetSnapshot は windowID のスナップショットを設定する。Window は返すときにウィンドウ一覧の現在値で上書きする。
// 未設定のウィンドウは、要素なしでハッシュ "empty" のスナップショットを返す。
func (f *FakeUIAutomation) SetSnapshot(windowID string, snap domain.UISnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots[windowID] = snap
}

// resolve は windowID（空なら前面）のウィンドウを返す。存在しなければ ErrWindowNotFound。
func (f *FakeUIAutomation) resolve(windowID string) (domain.WindowInfo, error) {
	if windowID == "" {
		return f.windows.foreground()
	}
	w, ok := f.windows.find(windowID)
	if !ok {
		return domain.WindowInfo{}, domain.ErrWindowNotFound.WithMsg("no window " + windowID)
	}
	return w, nil
}

func (f *FakeUIAutomation) Tree(ctx context.Context, windowID string, maxDepth, maxNodes int) (domain.UINode, error) {
	if err := f.enter(ctx, "Tree", windowID, maxDepth, maxNodes); err != nil {
		return domain.UINode{}, err
	}
	if maxDepth < 1 || maxNodes < 1 {
		return domain.UINode{}, domain.ErrInvalidArgument.WithMsg("maxDepth and maxNodes must be at least 1")
	}
	w, err := f.resolve(windowID)
	if err != nil {
		return domain.UINode{}, err
	}
	f.mu.Lock()
	root, ok := f.trees[w.ID]
	f.mu.Unlock()
	if !ok {
		root = f.defaultRoot(w)
	}
	budget := maxNodes - 1
	return pruneTree(root, 0, maxDepth, &budget), nil
}

// defaultRoot は木を設定していないウィンドウの、根だけのツリー。
func (f *FakeUIAutomation) defaultRoot(w domain.WindowInfo) domain.UINode {
	hwnd, _ := strconv.ParseUint(w.ID, 10, 64)
	ref := domain.ElementRef{HWND: hwnd, PID: w.PID, RuntimeID: []int32{42, int32(hwnd)}}
	return domain.UINode{ID: ref.String(), Role: "Window", Name: w.Title, Rect: w.Rect, Enabled: true}
}

// treeOf は w の木（未設定なら根だけ）を返す。
func (f *FakeUIAutomation) treeOf(w domain.WindowInfo) domain.UINode {
	f.mu.Lock()
	root, ok := f.trees[w.ID]
	f.mu.Unlock()
	if !ok {
		root = f.defaultRoot(w)
	}
	return root
}

// bfs は root の子孫を幅優先（浅い階層から）に並べる。root 自身を先頭に含む。
func bfs(root domain.UINode) []domain.UINode {
	out := []domain.UINode{root}
	for i := 0; i < len(out); i++ {
		out = append(out, out[i].Children...)
	}
	return out
}

func elementInfoOf(n domain.UINode) domain.UIElementInfo {
	return domain.UIElementInfo{
		ID: n.ID, ControlType: n.Role, Name: n.Name, AutomationID: n.AutomationID, ClassName: n.ClassName,
		Rect: n.Rect, Enabled: n.Enabled, Offscreen: n.Offscreen, Patterns: slices.Clone(n.Patterns),
	}
}

// Find は platform.UIAutomation.Find。木は SetTree で設定したもの（未設定なら根だけ）。
func (f *FakeUIAutomation) Find(ctx context.Context, windowID string, match func(domain.UIElementInfo) bool, maxResults, maxNodes int) (domain.UIFindResult, error) {
	if err := f.enter(ctx, "Find", windowID, maxResults, maxNodes); err != nil {
		return domain.UIFindResult{}, err
	}
	if maxResults < 1 || maxNodes < 1 {
		return domain.UIFindResult{}, domain.ErrInvalidArgument.WithMsg("maxResults and maxNodes must be at least 1")
	}
	w, err := f.resolve(windowID)
	if err != nil {
		return domain.UIFindResult{}, err
	}
	all := bfs(f.treeOf(w))
	res := domain.UIFindResult{Window: w}
	if len(all) > maxNodes {
		all, res.Truncated = all[:maxNodes], true
	}
	for _, n := range all {
		info := elementInfoOf(n)
		probe := info
		probe.ID = ""
		if !match(probe) {
			continue
		}
		if len(res.Elements) == maxResults {
			res.Truncated = true
			break
		}
		res.Elements = append(res.Elements, info)
	}
	return res, nil
}

// Resolve は platform.UIAutomation.Resolve。ウィンドウが無い・PID が違う・木に ID の要素が無いときは ErrStaleElement。
func (f *FakeUIAutomation) Resolve(ctx context.Context, ref domain.ElementRef) (domain.UIElementInfo, error) {
	if err := f.enter(ctx, "Resolve", ref.String()); err != nil {
		return domain.UIElementInfo{}, err
	}
	w, ok := f.windows.find(ref.WindowID())
	if !ok || w.PID != ref.PID {
		return domain.UIElementInfo{}, domain.ErrStaleElement
	}
	id := ref.String()
	for _, n := range bfs(f.treeOf(w)) {
		if id != "" && n.ID == id {
			return elementInfoOf(n), nil
		}
	}
	return domain.UIElementInfo{}, domain.ErrStaleElement
}

// pruneTree は深さ maxDepth・総数 maxNodes までに刈り込む。budget は残りの節点数（n 自身は数え済み）。
func pruneTree(n domain.UINode, depth, maxDepth int, budget *int) domain.UINode {
	out := n
	out.Children = nil
	for _, c := range n.Children {
		if depth+1 > maxDepth || *budget <= 0 {
			out.Truncated = true
			break
		}
		*budget--
		out.Children = append(out.Children, pruneTree(c, depth+1, maxDepth, budget))
	}
	return out
}

func (f *FakeUIAutomation) Snapshot(ctx context.Context, windowID string, maxNodes int) (domain.UISnapshot, error) {
	if err := f.enter(ctx, "Snapshot", windowID, maxNodes); err != nil {
		return domain.UISnapshot{}, err
	}
	if maxNodes < 1 {
		return domain.UISnapshot{}, domain.ErrInvalidArgument.WithMsg("maxNodes must be at least 1")
	}
	w, err := f.resolve(windowID)
	if err != nil {
		return domain.UISnapshot{}, err
	}
	f.mu.Lock()
	snap, ok := f.snapshots[w.ID]
	f.mu.Unlock()
	if !ok {
		snap = domain.UISnapshot{Hash: "empty"}
	}
	snap.Window = w
	snap.Elements = slices.Clone(snap.Elements)
	if len(snap.Elements) > maxNodes {
		snap.Elements = snap.Elements[:maxNodes]
		snap.Truncated = true
	}
	return snap, nil
}
