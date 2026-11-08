package platformtest

import (
	"context"
	"slices"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.WindowManager = (*FakeWindows)(nil)

// FakeChild は FakeWindows が At の葉として返す子ウィンドウ（別プロセスの子を SetParent で置いたものなど）。
type FakeChild struct {
	Rect domain.Rect // 仮想デスクトップ上の物理座標
	Leaf domain.LeafRef
}

// FakeWindows は platform.WindowManager の偽の実装。ウィンドウは前面（最上位）から順に並べる（Z オーダー）。
// OwnerChain・MenuOwnerID・MenuOwnerChain・Class・ProcessStart・Style・無題のウィンドウは WindowInfo にそのまま置く。
// 葉は AddChild・SetFocusLeaf、HWND の再利用は Replace、Z オーダーの変更は Add・Raise・Remove で作る。
type FakeWindows struct {
	Recorder

	mu        sync.Mutex
	windows   []domain.WindowInfo
	children  map[string][]FakeChild // 最上位の ID → 子（前面が先）
	focusLeaf map[string]domain.LeafRef
}

// NewFakeWindows は windows（z 順の先頭が最上位）を持つ偽のウィンドウ管理を返す。
func NewFakeWindows(windows ...domain.WindowInfo) *FakeWindows {
	return &FakeWindows{windows: slices.Clone(windows)}
}

// SetWindows はウィンドウ一覧を差し替える。
func (f *FakeWindows) SetWindows(windows ...domain.WindowInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = slices.Clone(windows)
}

// Add は w を最前面（Z オーダーの先頭）に置く。前面にはしない（メニューのように、アクティブにならずに現れるウィンドウ）。
// w.Foreground が true なら、ほかのウィンドウの Foreground を外す。同じ ID があれば置き換える（Z オーダーも先頭になる）。
func (f *FakeWindows) Add(w domain.WindowInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeLocked(w.ID)
	if w.Foreground {
		for i := range f.windows {
			f.windows[i].Foreground = false
		}
	}
	f.windows = slices.Insert(f.windows, 0, w)
}

// Remove は id のウィンドウ（と、その子・フォーカスの葉の設定）を消す。なければ何もしない。
func (f *FakeWindows) Remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeLocked(id)
}

func (f *FakeWindows) removeLocked(id string) {
	f.windows = slices.DeleteFunc(f.windows, func(w domain.WindowInfo) bool { return w.ID == id })
	delete(f.children, id)
	delete(f.focusLeaf, id)
}

// Update は id のウィンドウを fn で書き換える（Z オーダーはそのまま）。なければ false。
func (f *FakeWindows) Update(id string, fn func(*domain.WindowInfo)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.windows, func(w domain.WindowInfo) bool { return w.ID == id })
	if i < 0 {
		return false
	}
	fn(&f.windows[i])
	f.windows[i].ID = id
	return true
}

// Replace は HWND の再利用を作る: id のウィンドウを、同じ ID・同じ Z オーダーのまま w（別の PID・ProcessStart・Class など）に差し替える。
// w.ID は無視して id にする。w.Foreground は元のウィンドウの値を引き継ぐ。なければ false。
func (f *FakeWindows) Replace(id string, w domain.WindowInfo) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.windows, func(x domain.WindowInfo) bool { return x.ID == id })
	if i < 0 {
		return false
	}
	w.ID, w.Foreground = id, f.windows[i].Foreground
	f.windows[i] = w
	delete(f.children, id)
	delete(f.focusLeaf, id)
	return true
}

// Raise は id のウィンドウを最前面（Z オーダーの先頭）に移す。前面（Foreground）は変えない。なければ false。
func (f *FakeWindows) Raise(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.windows, func(w domain.WindowInfo) bool { return w.ID == id })
	if i < 0 {
		return false
	}
	w := f.windows[i]
	f.windows = slices.Insert(slices.Delete(f.windows, i, i+1), 0, w)
	return true
}

// AddChild は最上位 topID の中の座標 child.Rect に子ウィンドウを置く。At は、その座標では child.Leaf を葉として返す
// （別プロセスの子を置くには child.Leaf の PID・ProcessStart を変える）。後から足したものが前面。
func (f *FakeWindows) AddChild(topID string, child FakeChild) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.children == nil {
		f.children = make(map[string][]FakeChild)
	}
	f.children[topID] = slices.Insert(f.children[topID], 0, child)
}

// SetFocusLeaf は最上位 topID が前面のときの Foreground().FocusLeaf（hwndFocus）を leaf にする。設定がなければ前面のウィンドウ自身。
func (f *FakeWindows) SetFocusLeaf(topID string, leaf domain.LeafRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.focusLeaf == nil {
		f.focusLeaf = make(map[string]domain.LeafRef)
	}
	f.focusLeaf[topID] = leaf
}

// find は記録せずに id のウィンドウを探す（FakeUIAutomation も使う）。
func (f *FakeWindows) find(id string) (domain.WindowInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.windows {
		if w.ID == id {
			return w, true
		}
	}
	return domain.WindowInfo{}, false
}

// foreground は記録せずに前面ウィンドウを返す。
func (f *FakeWindows) foreground() (domain.WindowInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.windows {
		if w.Foreground {
			return w, nil
		}
	}
	return domain.WindowInfo{}, domain.ErrDesktopUnavailable.WithMsg("no foreground window")
}

// List はすべてのウィンドウを返す（従来どおり。実機の「タイトルのあるものだけ」は模さない。無題の自ウィンドウを使う既存のテストのため。無題・メニューを含む一覧の契約は ListAll）。
func (f *FakeWindows) List(ctx context.Context) ([]domain.WindowInfo, error) {
	if err := f.enter(ctx, "List"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.windows), nil
}

// ListAll は無題のウィンドウ・メニュー・ポップアップを含むすべてのウィンドウを Z オーダー（前面が先）で返す。
func (f *FakeWindows) ListAll(ctx context.Context) ([]domain.WindowInfo, error) {
	if err := f.enter(ctx, "ListAll"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.windows), nil
}

// At は最小化されていない最上位のウィンドウのうち、p を含むものを返す。
// クリックスルーのオーバーレイ（Overlay）は入力が下へ通るので飛ばし、その下の実ウィンドウを返す（InputGuard が下の自ウィンドウを見逃さないため）。
// 葉は、最上位に AddChild した子のうち p を含む最前面のもの。なければ最上位自身。
func (f *FakeWindows) At(p domain.Point) (domain.HitInfo, bool, error) {
	if err := f.enterSync("At", p); err != nil {
		return domain.HitInfo{}, false, err
	}
	hit, ok := f.hitTest(p)
	return hit, ok, nil
}

// hitTest は記録せずに At の結果を返す（FakeApp も使う）。
func (f *FakeWindows) hitTest(p domain.Point) (domain.HitInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.windows {
		if w.Minimized || w.Overlay || !w.Rect.Contains(p) {
			continue
		}
		hit := domain.HitInfo{Top: w, Leaf: domain.LeafOf(w)}
		for _, c := range f.children[w.ID] {
			if c.Rect.Contains(p) {
				hit.Leaf = c.Leaf
				break
			}
		}
		return hit, true
	}
	return domain.HitInfo{}, false
}

// Foreground は前面のウィンドウと、そのフォーカスの葉（SetFocusLeaf の設定、なければ前面のウィンドウ自身）を返す。
func (f *FakeWindows) Foreground() (domain.FocusInfo, error) {
	if err := f.enterSync("Foreground"); err != nil {
		return domain.FocusInfo{}, err
	}
	w, err := f.foreground()
	if err != nil {
		return domain.FocusInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	leaf, ok := f.focusLeaf[w.ID]
	if !ok {
		leaf = domain.LeafOf(w)
	}
	return domain.FocusInfo{Foreground: w, FocusLeaf: leaf}, nil
}

// Focus は id のウィンドウを前面（z 順の先頭）にし、最小化を解く。
func (f *FakeWindows) Focus(ctx context.Context, id string) error {
	if err := f.enter(ctx, "Focus", id); err != nil {
		return err
	}
	if !f.activate(id) {
		return domain.ErrWindowNotFound.WithMsg("no window " + id)
	}
	return nil
}

// activate は記録せずに id のウィンドウを前面（z 順の先頭）にし、最小化を解く。なければ false（FakeApp も使う）。
func (f *FakeWindows) activate(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.windows, func(w domain.WindowInfo) bool { return w.ID == id })
	if i < 0 {
		return false
	}
	for j := range f.windows {
		f.windows[j].Foreground = false
	}
	w := f.windows[i]
	w.Foreground, w.Minimized = true, false
	f.windows = slices.Delete(f.windows, i, i+1)
	f.windows = slices.Insert(f.windows, 0, w)
	return true
}
