package platformtest

import (
	"context"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// watchTimeout は、実機の Watch が最初のコマを流す・停止するまでの待ちの上限。
const watchTimeout = 10 * time.Second

// RunCapturerContract は platform.Capturer の契約を検査する。Grab と Watch は画面を読むだけで、実機でも副作用はない。
// Watch の「画面が変化したときだけ流す」は、静止した画面では最初の 1 コマしか確かめられないので、最初の 1 コマだけを検査する。
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
		_, _, err = c.Watch(canceledCtx(), 0, 10)
		requireCanceled(t, err, "Watch")
	})

	t.Run("Watch rejects an unknown monitor and a non-positive fps", func(t *testing.T) {
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
			_, _, err := c.Watch(ctx, index, 10)
			requireCode(t, err, domain.ErrInvalidArgument, "Watch unknown monitor")
		}
		for _, fps := range []int{0, -1} {
			_, _, err := c.Watch(ctx, monitors[0].Index, fps)
			requireCode(t, err, domain.ErrInvalidArgument, "Watch non-positive fps")
		}
	})

	t.Run("Watch sends a first frame of the monitor size, then closes both channels on cancel", func(t *testing.T) {
		c := newCapturer(t)
		monitors, err := c.Monitors(ctx)
		if err != nil {
			t.Fatalf("Monitors: %v", err)
		}
		for _, m := range monitors {
			watchOneMonitor(t, c, m)
		}
	})
}

// watchOneMonitor は m の Watch が最初のコマを流し、取り消しで 2 つのチャネルを閉じることを検査する。
func watchOneMonitor(t *testing.T, c platform.Capturer, m domain.Monitor) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, errs, err := c.Watch(ctx, m.Index, 10)
	if err != nil {
		t.Fatalf("Watch(%d): %v", m.Index, err)
	}

	timeout := time.NewTimer(watchTimeout)
	defer timeout.Stop()
	select {
	case fr, ok := <-frames:
		if !ok {
			t.Fatalf("Watch(%d): frames was closed before the first frame", m.Index)
		}
		checkFrame(t, m, fr)
	case err := <-errs:
		t.Fatalf("Watch(%d): error before the first frame: %v", m.Index, err)
	case <-timeout.C:
		t.Fatalf("Watch(%d): no frame within %v", m.Index, watchTimeout)
	}

	cancel()
	for frames != nil || errs != nil {
		select {
		case _, ok := <-frames:
			if !ok {
				frames = nil
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
		case <-timeout.C:
			t.Fatalf("Watch(%d): the channels were not closed within %v of the cancel", m.Index, watchTimeout)
		}
	}
}

// checkFrame は fr がモニター m の 1 コマとして正しい形（大きさ・原点・変化した矩形）であることを検査する。
func checkFrame(t *testing.T, m domain.Monitor, fr platform.Frame) {
	t.Helper()
	if fr.Image == nil {
		t.Fatalf("Watch(%d): frame has no image", m.Index)
	}
	b := fr.Image.Bounds()
	if b.Min.X != 0 || b.Min.Y != 0 || b.Dx() != m.Size.W || b.Dy() != m.Size.H {
		t.Errorf("Watch(%d): frame bounds = %v, want 0,0 %dx%d", m.Index, b, m.Size.W, m.Size.H)
	}
	if len(fr.Dirty) == 0 {
		t.Errorf("Watch(%d): the first frame has no dirty rectangle", m.Index)
	}
	for _, d := range fr.Dirty {
		if d.Empty() || !d.In(b) {
			t.Errorf("Watch(%d): dirty rectangle %v is empty or outside %v", m.Index, d, b)
		}
	}
	if fr.Seq != 1 {
		t.Errorf("Watch(%d): the first frame Seq = %d, want 1", m.Index, fr.Seq)
	}
}
