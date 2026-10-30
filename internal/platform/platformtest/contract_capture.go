package platformtest

import (
	"context"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// RunCapturerContract は platform.Capturer の契約を検査する。継続キャプチャ（Watch）は M2 の対象なので含めない。
// Grab は画面を読むだけで、実機でも副作用はない。
func RunCapturerContract(t *testing.T, newCapturer func(t *testing.T) platform.Capturer) {
	t.Helper()
	ctx := context.Background()

	t.Run("Monitors lists one primary with unique indexes and positive sizes", func(t *testing.T) {
		monitors, err := newCapturer(t).Monitors(ctx)
		if err != nil {
			t.Fatalf("Monitors: %v", err)
		}
		if len(monitors) == 0 {
			t.Fatal("Monitors returned no monitor")
		}
		primaries, seen := 0, map[int]bool{}
		for _, m := range monitors {
			if m.Primary {
				primaries++
			}
			if seen[m.Index] {
				t.Errorf("monitor index %d appears twice", m.Index)
			}
			seen[m.Index] = true
			if m.Size.W <= 0 || m.Size.H <= 0 {
				t.Errorf("monitor %d has size %+v", m.Index, m.Size)
			}
		}
		if primaries != 1 {
			t.Errorf("%d primary monitors, want 1", primaries)
		}
	})

	t.Run("Grab returns an image of the monitor size at the origin", func(t *testing.T) {
		c := newCapturer(t)
		monitors, err := c.Monitors(ctx)
		if err != nil {
			t.Fatalf("Monitors: %v", err)
		}
		for _, m := range monitors {
			img, err := c.Grab(ctx, m.Index)
			if err != nil {
				t.Fatalf("Grab(%d): %v", m.Index, err)
			}
			b := img.Bounds()
			if b.Min.X != 0 || b.Min.Y != 0 || b.Dx() != m.Size.W || b.Dy() != m.Size.H {
				t.Errorf("Grab(%d) bounds = %v, want 0,0 %dx%d", m.Index, b, m.Size.W, m.Size.H)
			}
		}
	})

	t.Run("Grab rejects an unknown monitor", func(t *testing.T) {
		c := newCapturer(t)
		monitors, err := c.Monitors(ctx)
		if err != nil {
			t.Fatalf("Monitors: %v", err)
		}
		highest := 0
		for _, m := range monitors {
			highest = max(highest, m.Index)
		}
		for _, index := range []int{-1, highest + 1} {
			_, err := c.Grab(ctx, index)
			requireCode(t, err, domain.ErrInvalidArgument, "Grab unknown monitor")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		c := newCapturer(t)
		_, err := c.Monitors(canceledCtx())
		requireCanceled(t, err, "Monitors")
		_, err = c.Grab(canceledCtx(), 0)
		requireCanceled(t, err, "Grab")
	})
}
