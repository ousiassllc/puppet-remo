package platformtest

import (
	"context"
	"slices"
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
		root = domain.UINode{ID: w.ID, Role: "Window", Name: w.Title, Rect: w.Rect, Enabled: true}
	}
	budget := maxNodes - 1
	return pruneTree(root, 0, maxDepth, &budget), nil
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
