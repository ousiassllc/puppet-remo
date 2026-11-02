package platformtest

import (
	"context"
	"slices"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.WindowManager = (*FakeWindows)(nil)

// FakeWindows は platform.WindowManager の偽の実装。ウィンドウは前面（最上位）から順に並べる。
type FakeWindows struct {
	Recorder

	mu      sync.Mutex
	windows []domain.WindowInfo
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

func (f *FakeWindows) List(ctx context.Context) ([]domain.WindowInfo, error) {
	if err := f.enter(ctx, "List"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.windows), nil
}

// At は最小化されていない最上位のウィンドウのうち、p を含むものを返す。
// クリックスルーのオーバーレイ（Overlay）は入力が下へ通るので飛ばし、その下の実ウィンドウを返す（InputGuard が下の自ウィンドウを見逃さないため）。
func (f *FakeWindows) At(p domain.Point) (domain.WindowInfo, bool, error) {
	if err := f.enterSync("At", p); err != nil {
		return domain.WindowInfo{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.windows {
		if !w.Minimized && !w.Overlay && w.Rect.Contains(p) {
			return w, true, nil
		}
	}
	return domain.WindowInfo{}, false, nil
}

func (f *FakeWindows) Foreground() (domain.WindowInfo, error) {
	if err := f.enterSync("Foreground"); err != nil {
		return domain.WindowInfo{}, err
	}
	return f.foreground()
}

// Focus は id のウィンドウを前面（z 順の先頭）にし、最小化を解く。
func (f *FakeWindows) Focus(ctx context.Context, id string) error {
	if err := f.enter(ctx, "Focus", id); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.windows, func(w domain.WindowInfo) bool { return w.ID == id })
	if i < 0 {
		return domain.ErrWindowNotFound.WithMsg("no window " + id)
	}
	for j := range f.windows {
		f.windows[j].Foreground = false
	}
	w := f.windows[i]
	w.Foreground, w.Minimized = true, false
	f.windows = slices.Delete(f.windows, i, i+1)
	f.windows = slices.Insert(f.windows, 0, w)
	return nil
}
