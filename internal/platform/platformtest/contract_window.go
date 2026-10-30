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
		if w.ID == "" || !w.Foreground {
			t.Errorf("Foreground = %+v, want a window with an ID and Foreground set", w)
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
			if ok && !got.Rect.Contains(p) {
				t.Errorf("At(%v) = window %q with rect %+v, which does not contain the point", p, got.ID, got.Rect)
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
