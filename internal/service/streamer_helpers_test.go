package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// このファイルは Streamer のテストの道具（テスト用の Capturer、フレームの復号、条件待ち）。
// 固定時間の待ちは使わず、チャネルの受信と上限付きの条件待ち（waitUntil）で同期する。

const streamWait = 5 * time.Second

// streamWatch は streamCapturer.Watch の 1 回分。テストがコマを流し、取り消しを観測する。
type streamWatch struct {
	monitor, fps int
	frames       chan platform.Frame
	errs         chan error
	stopped      chan struct{} // ctx が取り消され、チャネルを閉じたら閉じる
	endOnce      sync.Once
}

// push はコマを流す（Watch の goroutine が受け取るまで待つ）。
func (w *streamWatch) push(t *testing.T, img *image.RGBA) {
	t.Helper()
	select {
	case w.frames <- platform.Frame{Image: img, Dirty: []image.Rectangle{img.Bounds()}}:
	case <-time.After(streamWait):
		t.Fatal("capture frame was not consumed")
	}
}

// end はキャプチャが自分で終わったこと（チャネルが閉じる）を模す。
func (w *streamWatch) end() { w.endOnce.Do(func() { close(w.frames) }) }

func (w *streamWatch) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case <-w.stopped:
	case <-time.After(streamWait):
		t.Fatalf("capture of monitor %d was not stopped", w.monitor)
	}
}

// streamCapturer は platform.Capturer のテスト用の実装。Watch のたびに started へ流す。
type streamCapturer struct {
	valid   map[int]bool
	mu      sync.Mutex
	watches []*streamWatch
	started chan *streamWatch
	failErr error
}

func newStreamCapturer(monitors ...int) *streamCapturer {
	c := &streamCapturer{valid: map[int]bool{}, started: make(chan *streamWatch, 32)}
	for _, m := range monitors {
		c.valid[m] = true
	}
	return c
}

func (c *streamCapturer) Monitors(context.Context) ([]domain.Monitor, error) { return nil, nil }

func (c *streamCapturer) Grab(context.Context, int) (*image.RGBA, error) {
	return nil, domain.ErrInvalidArgument
}

func (c *streamCapturer) Watch(ctx context.Context, monitor, fps int) (<-chan platform.Frame, <-chan error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failErr != nil {
		return nil, nil, c.failErr
	}
	if !c.valid[monitor] || fps <= 0 {
		return nil, nil, domain.ErrInvalidArgument.WithMsg("bad watch")
	}
	w := &streamWatch{
		monitor: monitor, fps: fps,
		frames: make(chan platform.Frame), errs: make(chan error, 1), stopped: make(chan struct{}),
	}
	context.AfterFunc(ctx, func() {
		w.end()
		close(w.errs)
		close(w.stopped)
	})
	c.watches = append(c.watches, w)
	c.started <- w
	return w.frames, w.errs, nil
}

func (c *streamCapturer) watchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.watches)
}

// nextWatch は次に始まった Watch を待つ。
func (c *streamCapturer) nextWatch(t *testing.T) *streamWatch {
	t.Helper()
	select {
	case w := <-c.started:
		return w
	case <-time.After(streamWait):
		t.Fatal("capture did not start")
		return nil
	}
}

type streamEnv struct {
	s     *streamer
	cap   *streamCapturer
	clock *fakeClock
	store *ctlSettings
}

func newStreamEnv(t *testing.T, monitors ...int) *streamEnv {
	t.Helper()
	if len(monitors) == 0 {
		monitors = []int{0}
	}
	e := &streamEnv{cap: newStreamCapturer(monitors...), clock: newFakeClock(), store: newCtlSettings()}
	st, err := NewStreamer(StreamerConfig{Capturer: e.cap, Clock: e.clock, IDs: &seqIDs{}, Settings: e.store})
	if err != nil {
		t.Fatal(err)
	}
	e.s = st.(*streamer)
	return e
}

// subscribe は閲覧者を登録し、ctx の取り消しをテストの終了に結びつける。
func (e *streamEnv) subscribe(t *testing.T, c ViewerConfig) (*Viewer, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := e.s.Subscribe(ctx, c)
	if err != nil {
		t.Fatalf("Subscribe(%+v) = %v", c, err)
	}
	return v, cancel
}

func (e *streamEnv) viewerCount() int {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	return len(e.s.viewers)
}

func (e *streamEnv) needKey(id string) bool {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	return e.s.viewers[id].needKey
}

// waitUntil は cond が真になるまで、上限つきで待つ。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(streamWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// recvFrame は閲覧者の次のフレームを復号して返す。
func recvFrame(t *testing.T, v *Viewer) ([]byte, parsedFrame) {
	t.Helper()
	select {
	case data, ok := <-v.Frames:
		if !ok {
			t.Fatal("Frames is closed")
		}
		return data, parseFrame(t, data)
	case <-time.After(streamWait):
		t.Fatal("no frame arrived")
		return nil, parsedFrame{}
	}
}

func waitDone(t *testing.T, v *Viewer) {
	t.Helper()
	select {
	case <-v.Done:
	case <-time.After(streamWait):
		t.Fatal("Done was not closed")
	}
	if _, ok := <-v.Frames; ok {
		t.Error("Frames must be closed after Done")
	}
}

type parsedTile struct {
	rect image.Rectangle
	img  image.Image
}

type parsedFrame struct {
	version, flags int
	key            bool
	monitor        int
	seq            uint32
	w, h           int
	tiles          []parsedTile
}

// parseFrame はバイナリフレーム（API 仕様 §5.4）を復号する。形式の不備はテストの失敗にする。
func parseFrame(t *testing.T, data []byte) parsedFrame {
	t.Helper()
	if len(data) < frameHeaderSize {
		t.Fatalf("frame is %d bytes; want at least %d", len(data), frameHeaderSize)
	}
	le := binary.LittleEndian
	f := parsedFrame{
		version: int(data[0]), flags: int(data[1]), key: data[1]&1 != 0,
		monitor: int(le.Uint16(data[2:])), seq: le.Uint32(data[4:]),
		w: int(le.Uint16(data[8:])), h: int(le.Uint16(data[10:])),
	}
	n := int(le.Uint16(data[12:]))
	rest := data[frameHeaderSize:]
	for i := range n {
		if len(rest) < tileHeaderSize {
			t.Fatalf("tile %d header is truncated", i)
		}
		x, y, w, h := int(le.Uint16(rest[0:])), int(le.Uint16(rest[2:])), int(le.Uint16(rest[4:])), int(le.Uint16(rest[6:]))
		size := int(le.Uint32(rest[8:]))
		rest = rest[tileHeaderSize:]
		if len(rest) < size {
			t.Fatalf("tile %d jpeg is truncated: %d of %d", i, len(rest), size)
		}
		img, err := jpeg.Decode(bytes.NewReader(rest[:size]))
		if err != nil {
			t.Fatalf("tile %d is not a jpeg: %v", i, err)
		}
		if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
			t.Fatalf("tile %d jpeg is %v; header says %dx%d", i, img.Bounds().Size(), w, h)
		}
		f.tiles = append(f.tiles, parsedTile{rect: image.Rect(x, y, x+w, y+h), img: img})
		rest = rest[size:]
	}
	if len(rest) != 0 {
		t.Fatalf("%d trailing bytes after the last tile", len(rest))
	}
	return f
}

func (f parsedFrame) rects() []image.Rectangle {
	out := make([]image.Rectangle, len(f.tiles))
	for i, tl := range f.tiles {
		out[i] = tl.rect
	}
	return out
}

func (f parsedFrame) String() string {
	return fmt.Sprintf("frame{key=%v monitor=%d seq=%d %dx%d tiles=%v}", f.key, f.monitor, f.seq, f.w, f.h, f.rects())
}

// solidImage は単色の w×h の画像。
func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// withPixel は img の複製の (x, y) を c に変えたものを返す（Capturer は流した画像に触らないので、別の画像として流す）。
func withPixel(img *image.RGBA, x, y int, c color.RGBA) *image.RGBA {
	out := image.NewRGBA(img.Bounds())
	copy(out.Pix, img.Pix)
	out.SetRGBA(x, y, c)
	return out
}

// withRect は img の複製の r を c で塗ったものを返す。
func withRect(img *image.RGBA, r image.Rectangle, c color.RGBA) *image.RGBA {
	out := image.NewRGBA(img.Bounds())
	copy(out.Pix, img.Pix)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			out.SetRGBA(x, y, c)
		}
	}
	return out
}

var (
	colBlue  = color.RGBA{R: 20, G: 60, B: 200, A: 255}
	colRed   = color.RGBA{R: 230, G: 20, B: 20, A: 255}
	colGreen = color.RGBA{R: 20, G: 220, B: 20, A: 255}
)
