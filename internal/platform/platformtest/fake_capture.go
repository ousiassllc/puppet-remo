package platformtest

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"slices"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Capturer = (*FakeCapturer)(nil)

// FakeCapturer は platform.Capturer の偽の実装。
type FakeCapturer struct {
	Recorder

	mu          sync.Mutex
	monitors    []domain.Monitor
	images      map[int]*image.RGBA
	watchFrames []platform.Frame
	watchErrs   []error
}

// NewFakeCapturer は monitors を持つ偽のキャプチャを返す。省略すると 640x480 のプライマリ 1 台。
func NewFakeCapturer(monitors ...domain.Monitor) *FakeCapturer {
	if len(monitors) == 0 {
		monitors = []domain.Monitor{{Index: 0, Name: "fake-0", Primary: true, Size: domain.Size{W: 640, H: 480}, DPI: 96}}
	}
	return &FakeCapturer{monitors: slices.Clone(monitors), images: make(map[int]*image.RGBA)}
}

// SetImage は Grab が monitor に対して返す画像を差し替える（複製せずそのまま返す）。
func (f *FakeCapturer) SetImage(monitor int, img *image.RGBA) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[monitor] = img
}

// SetWatch は Watch が流すフレームとエラーを設定する。Watch の開始時に流れ、ctx の取り消しでチャネルが閉じる。
func (f *FakeCapturer) SetWatch(frames []platform.Frame, errs []error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchFrames = slices.Clone(frames)
	f.watchErrs = slices.Clone(errs)
}

func (f *FakeCapturer) monitor(index int) (domain.Monitor, error) {
	for _, m := range f.monitors {
		if m.Index == index {
			return m, nil
		}
	}
	return domain.Monitor{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("unknown monitor %d", index))
}

func (f *FakeCapturer) Monitors(ctx context.Context) ([]domain.Monitor, error) {
	if err := f.enter(ctx, "Monitors"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.monitors), nil
}

func (f *FakeCapturer) Grab(ctx context.Context, monitor int) (*image.RGBA, error) {
	if err := f.enter(ctx, "Grab", monitor); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.monitor(monitor)
	if err != nil {
		return nil, err
	}
	if img, ok := f.images[monitor]; ok {
		return img, nil
	}
	img := image.NewRGBA(image.Rect(0, 0, m.Size.W, m.Size.H))
	fill := color.RGBA{R: uint8(40 * (m.Index + 1)), G: 80, B: 120, A: 255}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = fill.R, fill.G, fill.B, fill.A
	}
	return img, nil
}

func (f *FakeCapturer) Watch(ctx context.Context, monitor int, fps int) (<-chan platform.Frame, <-chan error, error) {
	if err := f.enter(ctx, "Watch", monitor, fps); err != nil {
		return nil, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.monitor(monitor); err != nil {
		return nil, nil, err
	}
	if fps <= 0 {
		return nil, nil, domain.ErrInvalidArgument.WithMsg("fps must be positive")
	}
	frames := make(chan platform.Frame, len(f.watchFrames))
	errs := make(chan error, len(f.watchErrs))
	for _, fr := range f.watchFrames {
		frames <- fr
	}
	for _, e := range f.watchErrs {
		errs <- e
	}
	context.AfterFunc(ctx, func() {
		close(frames)
		close(errs)
	})
	return frames, errs, nil
}
