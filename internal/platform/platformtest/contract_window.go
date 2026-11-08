package platformtest

import (
	"context"
	"math"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// RunWindowManagerContract は platform.WindowManager の契約を検査する。
// フォーカスを変えない範囲だけを検査するので、実機でも副作用はない。
func RunWindowManagerContract(t *testing.T, newWindows func(t *testing.T) platform.WindowManager) {
	t.Helper()
	ctx := context.Background()

	t.Run("List returns unique non-empty IDs and at most one foreground window", func(t *testing.T) {
		windows, err := newWindows(t).List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		seen, foreground := map[string]bool{}, 0
		for _, w := range windows {
			if w.ID == "" {
				t.Errorf("window %q has an empty ID", w.Title)
			}
			if seen[w.ID] {
				t.Errorf("window ID %q appears twice", w.ID)
			}
			seen[w.ID] = true
			if w.Foreground {
				foreground++
			}
		}
		if foreground > 1 {
			t.Errorf("%d foreground windows, want at most 1", foreground)
		}
	})

	t.Run("Foreground returns a foreground window with an ID", func(t *testing.T) {
		w, err := newWindows(t).Foreground()
		if err != nil {
			t.Fatalf("Foreground: %v", err)
		}
		if w.Foreground.ID == "" || !w.Foreground.Foreground {
			t.Errorf("Foreground = %+v, want a window with an ID and Foreground set", w.Foreground)
		}
		if w.FocusLeaf.ID == "" && w.FocusLeaf != (domain.LeafRef{}) {
			t.Errorf("FocusLeaf = %+v, want either unknown (zero) or a leaf with an ID", w.FocusLeaf)
		}
	})

	t.Run("ListAll returns unique IDs and bounded owner chains", func(t *testing.T) {
		wm := newWindows(t)
		all, err := wm.ListAll(ctx)
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		seen, foreground := map[string]bool{}, 0
		for _, w := range all {
			if w.ID == "" || seen[w.ID] {
				t.Errorf("ListAll window ID %q is empty or appears twice", w.ID)
			}
			seen[w.ID] = true
			if w.Foreground {
				foreground++
			}
			requireChain(t, w.ID, "OwnerChain", w.OwnerChain)
			requireChain(t, w.ID, "MenuOwnerChain", w.MenuOwnerChain)
			if w.MenuOwnerID == "" && len(w.MenuOwnerChain) != 0 {
				t.Errorf("window %s has a MenuOwnerChain without a MenuOwnerID", w.ID)
			}
		}
		if foreground > 1 {
			t.Errorf("ListAll: %d foreground windows, want at most 1", foreground)
		}
	})

	t.Run("At finds a window that contains the point and reports none far outside the desktop", func(t *testing.T) {
		wm := newWindows(t)
		windows, err := wm.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, w := range windows {
			if w.Minimized || w.Rect.Empty() {
				continue
			}
			p := domain.Point{X: w.Rect.X + w.Rect.W/2, Y: w.Rect.Y + w.Rect.H/2}
			got, ok, err := wm.At(p)
			if err != nil {
				t.Fatalf("At(%v): %v", p, err)
			}
			if ok && !got.Top.Rect.Contains(p) {
				t.Errorf("At(%v) = window %q with rect %+v, which does not contain the point", p, got.Top.ID, got.Top.Rect)
			}
			if ok && got.Top.Overlay {
				t.Errorf("At(%v) = click-through overlay %q; want the real window under it", p, got.Top.ID)
			}
			if ok && got.Leaf.ID == "" && got.Leaf != (domain.LeafRef{}) {
				t.Errorf("At(%v).Leaf = %+v, want either unknown (zero) or a leaf with an ID", p, got.Leaf)
			}
			break
		}
		far := domain.Point{X: math.MinInt32 / 2, Y: math.MinInt32 / 2}
		if _, ok, err := wm.At(far); err != nil || ok {
			t.Errorf("At(%v) = ok %v, error %v; want no window and no error", far, ok, err)
		}
	})

	t.Run("Focus rejects an unknown or malformed ID", func(t *testing.T) {
		wm := newWindows(t)
		for _, id := range []string{"", "no-such-window", "-1", "0"} {
			requireCode(t, wm.Focus(ctx, id), domain.ErrWindowNotFound, "Focus("+id+")")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		wm := newWindows(t)
		_, err := wm.List(canceledCtx())
		requireCanceled(t, err, "List")
		requireCanceled(t, wm.Focus(canceledCtx(), "1"), "Focus")
	})
}

// requireChain は所有関係の鎖が MaxChainHops 段以内で、検証不能の印が最後にだけあり、リンクの ID が空でないことを検査する。
func requireChain(t *testing.T, id, name string, chain []domain.WindowLink) {
	t.Helper()
	if len(chain) > domain.MaxChainHops+1 {
		t.Errorf("window %s: %s has %d links, want at most %d plus a marker", id, name, len(chain), domain.MaxChainHops)
	}
	for i, l := range chain {
		if l.Unverifiable && i != len(chain)-1 {
			t.Errorf("window %s: %s has an unverifiable marker at %d, want it last", id, name, i)
		}
		if !l.Unverifiable && l.ID == "" {
			t.Errorf("window %s: %s link %d has no ID", id, name, i)
		}
		if l.ID == id {
			t.Errorf("window %s: %s contains the window itself", id, name)
		}
	}
}
