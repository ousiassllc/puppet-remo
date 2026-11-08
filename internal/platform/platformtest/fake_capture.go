package platformtest

import (
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var (
	_ platform.Capturer      = (*FakeCapturer)(nil)
	_ platform.GroupCapturer = (*FakeCapturer)(nil)
)

// FakeCapturer は platform.Capturer と platform.GroupCapturer の偽の実装。
// GrabGroup は AttachWindows で結んだ FakeWindows のウィンドウの位置を使い、メンバー自身の画像（SetWindowImage、なければ ID ごとの単色）だけを
// 透明のキャンバスに置く（デスクトップの画像・モニターの画像は一切使わない）。
type FakeCapturer struct {
	Recorder

	mu           sync.Mutex
	monitors     []domain.Monitor
	images       map[int]*image.RGBA
	watchFrames  []platform.Frame
	watchErrs    []error
	windows      *FakeWindows
	winImages    map[string]*image.RGBA
	notPrintable map[string]bool
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
// 設定しない（または両方とも空）と、Watch は全面が変化した最初の 1 コマ（Grab と同じ画像、Seq 1）だけを流す。
func (f *FakeCapturer) SetWatch(frames []platform.Frame, errs []error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchFrames = slices.Clone(frames)
	f.watchErrs = slices.Clone(errs)
}

// AttachWindows は GrabGroup がウィンドウの位置と同一性を調べる FakeWindows を結ぶ。結ばないと GrabGroup は ErrDesktopUnavailable。
func (f *FakeCapturer) AttachWindows(w *FakeWindows) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = w
}

// SetWindowImage は GrabGroup が id のウィンドウに対して置く画像を差し替える（大きさはウィンドウの Rect と違ってもよい。置く矩形は画像の大きさ）。
func (f *FakeCapturer) SetWindowImage(id string, img *image.RGBA) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.winImages == nil {
		f.winImages = make(map[string]*image.RGBA)
	}
	f.winImages[id] = img
}

// SetNotPrintable は id のウィンドウを撮れない（PrintWindow の失敗）ものにする（GrabGroup の Status は not_printable）。false で解除。
func (f *FakeCapturer) SetNotPrintable(id string, notPrintable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notPrintable == nil {
		f.notPrintable = make(map[string]bool)
	}
	f.notPrintable[id] = notPrintable
}

// GroupGrabCalls は GrabGroup が呼ばれた回数。
func (f *FakeCapturer) GroupGrabCalls() int { return len(f.CallsTo("GrabGroup")) }

// MonitorCaptureCalls はモニター撮影（Grab・Watch）が呼ばれた回数。スコープ付きの表示がモニター撮影に切り替わらないことの確認に使う。
func (f *FakeCapturer) MonitorCaptureCalls() int {
	return len(f.CallsTo("Grab")) + len(f.CallsTo("Watch"))
}

// AssertNoMonitorCapture はモニター撮影（Grab・Watch）が 1 回も呼ばれていないことを確かめる。
func (f *FakeCapturer) AssertNoMonitorCapture(t testing.TB) {
	t.Helper()
	if n := f.MonitorCaptureCalls(); n != 0 {
		t.Errorf("monitor capture (Grab/Watch) was called %d times, want 0", n)
	}
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
	return f.image(monitor)
}

// image は monitor の画像（SetImage の差し替え、なければ単色）を返す。f.mu を持って呼ぶ。
func (f *FakeCapturer) image(monitor int) (*image.RGBA, error) {
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
	img, err := f.image(monitor)
	if err != nil {
		return nil, nil, err
	}
	if fps <= 0 {
		return nil, nil, domain.ErrInvalidArgument.WithMsg("fps must be positive")
	}
	watchFrames := f.watchFrames
	if len(watchFrames) == 0 && len(f.watchErrs) == 0 {
		watchFrames = []platform.Frame{{Image: img, Dirty: []image.Rectangle{img.Bounds()}, Seq: 1}}
	}
	frames := make(chan platform.Frame, len(watchFrames))
	errs := make(chan error, len(f.watchErrs))
	for _, fr := range watchFrames {
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

// windowImage は w の画像（SetWindowImage の差し替え、なければ ID ごとの単色で Rect と同じ大きさ）を返す。f.mu を持って呼ぶ。
func (f *FakeCapturer) windowImage(w domain.WindowInfo) *image.RGBA {
	if img, ok := f.winImages[w.ID]; ok {
		return img
	}
	img := image.NewRGBA(image.Rect(0, 0, max(w.Rect.W, 0), max(w.Rect.H, 0)))
	draw.Draw(img, img.Bounds(), image.NewUniform(WindowColor(w.ID)), image.Point{}, draw.Src)
	return img
}

// WindowColor は SetWindowImage をしていないウィンドウ id の画像の色（ID から決まる不透明な色。テストが画素の持ち主を調べるのに使う）。
func WindowColor(id string) color.RGBA {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	v := h.Sum32()
	return color.RGBA{R: uint8(v), G: uint8(v >> 8), B: uint8(v >> 16), A: 255}
}

// GrabGroup は members（前面が先）の画像を、撮れたメンバーの矩形の外接矩形の透明のキャンバスに、後ろから順に置いて返す。
// 一覧にない・同一性（WindowRef）が違うメンバーは CaptureGone、SetNotPrintable・最小化・空の画像は CaptureNotPrintable。
func (f *FakeCapturer) GrabGroup(ctx context.Context, members []domain.WindowRef) (platform.GroupImage, error) {
	if err := f.enter(ctx, "GrabGroup", slices.Clone(members)); err != nil {
		return platform.GroupImage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.windows == nil {
		return platform.GroupImage{}, domain.ErrDesktopUnavailable.WithMsg("no windows attached to the fake capturer")
	}
	out := platform.GroupImage{Members: make([]platform.MemberCapture, len(members))}
	imgs := make([]*image.RGBA, len(members))
	var bounds image.Rectangle
	for i, ref := range members {
		mc := platform.MemberCapture{Ref: ref, Status: platform.CaptureGone}
		if w, ok := f.windows.find(ref.ID); ok && domain.RefOf(w) == ref {
			img := f.windowImage(w)
			if f.notPrintable[ref.ID] || w.Minimized || img.Bounds().Empty() {
				mc.Status = platform.CaptureNotPrintable
			} else {
				mc.Status = platform.CaptureOK
				mc.Rect = domain.Rect{X: w.Rect.X, Y: w.Rect.Y, W: img.Bounds().Dx(), H: img.Bounds().Dy()}
				imgs[i] = img
				bounds = bounds.Union(image.Rect(mc.Rect.X, mc.Rect.Y, mc.Rect.X+mc.Rect.W, mc.Rect.Y+mc.Rect.H))
			}
		}
		out.Members[i] = mc
	}
	if bounds.Empty() {
		return out, nil
	}
	out.Origin = domain.Point{X: bounds.Min.X, Y: bounds.Min.Y}
	out.Image = image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for i := len(members) - 1; i >= 0; i-- {
		if imgs[i] == nil {
			continue
		}
		r := out.Members[i].Rect
		dst := image.Rect(r.X-bounds.Min.X, r.Y-bounds.Min.Y, r.X-bounds.Min.X+r.W, r.Y-bounds.Min.Y+r.H)
		draw.Draw(out.Image, dst, imgs[i], imgs[i].Bounds().Min, draw.Over)
	}
	return out, nil
}
