package windows

import (
	"context"
	"errors"
	"image"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

const testInterval = 100 * time.Millisecond

// step は scriptSource の 1 回の Next の結果。
type step struct {
	img *image.RGBA
	err error
}

// scriptSource は決められた順に結果を返す frameSource。尽きたら、変化なし（wait だけ待って (nil, nil)）を返す。
type scriptSource struct {
	mu     sync.Mutex
	steps  []step
	closed int
}

func (s *scriptSource) Next(ctx context.Context, wait time.Duration) (*image.RGBA, error) {
	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		sleep(ctx, wait)
		return nil, nil
	}
	st := s.steps[0]
	s.steps = s.steps[1:]
	s.mu.Unlock()
	return st.img, st.err
}

func (s *scriptSource) Close() {
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
}

func (s *scriptSource) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func solid(w, h int, v byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return img
}

// sources は openDXGI / openGDI に順に渡す源。呼ばれた回数も数える。
type sources struct {
	mu        sync.Mutex
	dxgi      []*scriptSource // i 回目の openDXGI が返す源。尽きたら失敗する
	dxgiCalls int
	gdi       *scriptSource
}

func (s *sources) openDXGI() (frameSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.dxgiCalls
	s.dxgiCalls++
	if i >= len(s.dxgi) {
		return nil, errors.New("no more DXGI sources")
	}
	return s.dxgi[i], nil
}

func (s *sources) openGDI() frameSource { return s.gdi }

func (s *sources) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dxgiCalls
}

func newSources(gdi *scriptSource, dxgi ...*scriptSource) *sources {
	return &sources{dxgi: dxgi, gdi: gdi}
}

func (s *sources) config() watchConfig {
	return watchConfig{
		interval:     testInterval,
		openDXGI:     s.openDXGI,
		openGDI:      s.openGDI,
		recoverTries: 3,
		recoverDelay: testInterval,
	}
}

// requireClosed は frames と errs が閉じられるまで進め、閉じられていることを検査する。
func requireClosed(t *testing.T, frames <-chan platform.Frame, errs <-chan error) {
	t.Helper()
	synctest.Wait()
	for range frames {
		t.Error("frames carried a frame after cancel")
	}
	for e := range errs {
		t.Errorf("errs carried %v after cancel", e)
	}
}

func TestWatchDXGIFramesFullDirtyAndSeq(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := solid(4, 2, 1), solid(4, 2, 2)
		dxgi := &scriptSource{steps: []step{{img: a}, {img: b}}}
		s := newSources(&scriptSource{}, dxgi)
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, s.config())

		first := <-frames
		if first.Seq != 1 || first.Image != a || len(first.Dirty) != 1 || first.Dirty[0] != a.Bounds() {
			t.Errorf("first frame = %+v, want Seq 1, the image, and its whole bounds as Dirty", first)
		}
		second := <-frames
		if second.Seq != 2 || second.Image != b {
			t.Errorf("second frame = %+v, want Seq 2 and the second image", second)
		}

		cancel()
		requireClosed(t, frames, errs)
		if got := dxgi.closeCount(); got != 1 {
			t.Errorf("DXGI source closed %d times, want 1", got)
		}
	})
}

func TestWatchHonorsFPS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dxgi := &scriptSource{steps: []step{{img: solid(1, 1, 1)}, {img: solid(1, 1, 2)}, {img: solid(1, 1, 3)}}}
		s := newSources(&scriptSource{}, dxgi)
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, s.config())

		<-frames
		start := time.Now()
		<-frames
		<-frames
		if got := time.Since(start); got < 2*testInterval {
			t.Errorf("two more frames arrived within %v, want at least %v", got, 2*testInterval)
		}
		cancel()
		requireClosed(t, frames, errs)
	})
}

func TestWatchRecoversFromADXGIError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lost := errors.New("ACCESS_LOST")
		first := &scriptSource{steps: []step{{img: solid(2, 2, 1)}, {err: lost}}}
		second := &scriptSource{steps: []step{{img: solid(2, 2, 2)}}}
		s := newSources(&scriptSource{}, first, second)
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, s.config())

		if fr := <-frames; fr.Seq != 1 {
			t.Errorf("first Seq = %d, want 1", fr.Seq)
		}
		if fr := <-frames; fr.Seq != 2 {
			t.Errorf("Seq after the re-initialization = %d, want 2 (it continues)", fr.Seq)
		}
		cancel()
		requireClosed(t, frames, errs)
		if got := first.closeCount(); got != 1 {
			t.Errorf("lost source closed %d times, want 1", got)
		}
		if got := second.closeCount(); got != 1 {
			t.Errorf("recovered source closed %d times, want 1", got)
		}
	})
}

func TestWatchFallsBackToGDIWhenDXGICannotBeRestored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lost := errors.New("ACCESS_LOST")
		first := &scriptSource{steps: []step{{err: lost}}}
		gdi := &scriptSource{steps: []step{{img: solid(2, 2, 9)}}}
		s := newSources(gdi, first) // 2 回目以降の openDXGI は失敗する
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, s.config())

		fr := <-frames
		if fr.Seq != 1 || fr.Image.Pix[0] != 9 {
			t.Errorf("frame = Seq %d pix %d, want the GDI frame with Seq 1", fr.Seq, fr.Image.Pix[0])
		}
		select {
		case err := <-errs:
			if !errors.Is(err, domain.ErrDesktopUnavailable) {
				t.Errorf("error = %v, want ErrDesktopUnavailable", err)
			}
		default:
			t.Error("no ErrDesktopUnavailable was sent on the fall back to GDI")
		}
		if got := s.calls(); got != 1+3 {
			t.Errorf("openDXGI called %d times, want 1 initial + 3 recovery attempts", got)
		}
		cancel()
		synctest.Wait()
		if got := gdi.closeCount(); got != 1 {
			t.Errorf("GDI source closed %d times, want 1", got)
		}
	})
}

func TestWatchUsesGDIWhenDXGIIsUnavailableFromTheStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gdi := &scriptSource{steps: []step{{img: solid(2, 2, 5)}}}
		s := newSources(gdi) // openDXGI は失敗する
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, s.config())

		if fr := <-frames; fr.Image.Pix[0] != 5 {
			t.Errorf("frame pix = %d, want the GDI frame", fr.Image.Pix[0])
		}
		if got := s.calls(); got != 1 {
			t.Errorf("openDXGI called %d times, want 1 (no recovery before a DXGI source existed)", got)
		}
		cancel()
		requireClosed(t, frames, errs)
	})
}

func TestWatchWithoutDXGIUsesGDIOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gdi := &scriptSource{steps: []step{{img: solid(2, 2, 5)}}}
		cfg := watchConfig{interval: testInterval, openGDI: func() frameSource { return gdi }}
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, cfg)
		<-frames
		cancel()
		requireClosed(t, frames, errs)
	})
}

func TestWatchReportsGDIErrorsOnceUntilTheyChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locked := domain.ErrDesktopUnavailable.WithMsg("locked")
		gdi := &scriptSource{steps: []step{
			{err: locked}, {err: locked}, // 同じ文面は 1 回だけ
			{img: solid(1, 1, 1)},
		}}
		cfg := watchConfig{interval: testInterval, openGDI: func() frameSource { return gdi }}
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, cfg)

		<-frames
		cancel()
		synctest.Wait()
		var got []error
		for e := range errs {
			got = append(got, e)
		}
		if len(got) != 1 || !errors.Is(got[0], domain.ErrDesktopUnavailable) {
			t.Fatalf("errors before the first frame = %v, want exactly one ErrDesktopUnavailable", got)
		}
		for range frames {
		}
	})
}

func TestWatchReportsARepeatedErrorAfterASuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locked := domain.ErrDesktopUnavailable.WithMsg("locked")
		gdi := &scriptSource{steps: []step{{err: locked}, {img: solid(1, 1, 1)}, {err: locked}}}
		cfg := watchConfig{interval: testInterval, openGDI: func() frameSource { return gdi }}
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, cfg)

		<-frames
		time.Sleep(testInterval) // 次の Next（2 つ目の locked）まで時計を進める
		synctest.Wait()
		cancel()
		synctest.Wait()
		count := 0
		for range errs {
			count++
		}
		if count != 2 {
			t.Errorf("%d errors, want 2 (the same error is reported again after a success)", count)
		}
		for range frames {
		}
	})
}

func TestWatchDoesNotBlockOnAFullErrs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var steps []step
		for i := range errsBuffer + 3 {
			steps = append(steps, step{err: domain.ErrDesktopUnavailable.WithMsg(string(rune('a' + i)))})
		}
		steps = append(steps, step{img: solid(1, 1, 1)})
		gdi := &scriptSource{steps: steps}
		cfg := watchConfig{interval: testInterval, openGDI: func() frameSource { return gdi }}
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, cfg)

		<-frames // errs を読まなくても、エラーを捨てて撮り続ける
		cancel()
		synctest.Wait()
		count := 0
		for range errs {
			count++
		}
		if count != errsBuffer {
			t.Errorf("%d errors buffered, want %d (the rest are dropped)", count, errsBuffer)
		}
		for range frames {
		}
	})
}

func TestWatchStopsWhileBlockedOnASlowReceiver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dxgi := &scriptSource{steps: []step{{img: solid(1, 1, 1)}, {img: solid(1, 1, 2)}}}
		s := newSources(&scriptSource{}, dxgi)
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, s.config())

		synctest.Wait() // 受け手が読まないので、最初の send で止まる
		cancel()
		synctest.Wait()
		for range frames {
			t.Error("a frame was delivered after cancel")
		}
		for range errs {
		}
		if got := dxgi.closeCount(); got != 1 {
			t.Errorf("DXGI source closed %d times, want 1", got)
		}
	})
}

func TestWatchStopsDuringRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := &scriptSource{steps: []step{{err: errors.New("ACCESS_LOST")}}}
		s := newSources(&scriptSource{}, first)
		ctx, cancel := context.WithCancel(context.Background())
		frames, errs := startWatch(ctx, s.config())

		synctest.Wait() // 作り直しの待ちで止まる
		cancel()
		requireClosed(t, frames, errs)
		if got := first.closeCount(); got != 1 {
			t.Errorf("lost source closed %d times, want exactly 1", got)
		}
	})
}

func TestGDISourceReturnsOnlyChangedFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		imgs := []*image.RGBA{solid(2, 2, 1), solid(2, 2, 1), solid(2, 2, 2), solid(3, 2, 2)}
		next := 0
		src := newGDISource(func(context.Context) (*image.RGBA, error) {
			img := imgs[next]
			next++
			return img, nil
		})
		ctx := context.Background()

		want := []bool{true, false, true, true} // 同じ内容は流さない。内容が違う・大きさが違うなら流す
		for i, w := range want {
			img, err := src.Next(ctx, testInterval)
			if err != nil {
				t.Fatalf("Next %d: %v", i, err)
			}
			if got := img != nil; got != w {
				t.Errorf("Next %d returned a frame = %v, want %v", i, got, w)
			}
			if w && img != imgs[i] {
				t.Errorf("Next %d returned a different image than the one grabbed", i)
			}
		}
	})
}

func TestGDISourcePassesTheGrabError(t *testing.T) {
	boom := errors.New("boom")
	src := newGDISource(func(context.Context) (*image.RGBA, error) { return nil, boom })
	if img, err := src.Next(context.Background(), testInterval); img != nil || !errors.Is(err, boom) {
		t.Errorf("Next = %v, %v; want nil and the grab error", img, err)
	}
}

func TestWatchInterval(t *testing.T) {
	for _, tc := range []struct {
		fps  int
		want time.Duration
	}{
		{1, time.Second},
		{10, 100 * time.Millisecond},
		{30, time.Second / 30},
		{100000, time.Millisecond},
	} {
		if got := watchInterval(tc.fps); got != tc.want {
			t.Errorf("watchInterval(%d) = %v, want %v", tc.fps, got, tc.want)
		}
	}
}
