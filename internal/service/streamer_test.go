package service

import (
	"context"
	"errors"
	"image"
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

var screen = image.Rect(0, 0, 200, 150) // 4 列 × 3 行のタイル（右端・下端は 64 に満たない）

func TestChangedRects(t *testing.T) {
	base := solidImage(200, 150, colBlue)
	tests := []struct {
		name string
		cur  *image.RGBA
		want []image.Rectangle
	}{
		{"identical", withPixel(base, 0, 0, colBlue), nil},
		{"one pixel", withPixel(base, 70, 10, colRed), []image.Rectangle{image.Rect(64, 0, 128, 64)}},
		{"tile boundary pixel", withPixel(base, 63, 63, colRed), []image.Rectangle{image.Rect(0, 0, 64, 64)}},
		{"right and bottom edge are clipped", withPixel(base, 199, 149, colRed), []image.Rectangle{image.Rect(192, 128, 200, 150)}},
		{"adjacent tiles in a row merge", withPixel(withPixel(base, 10, 10, colRed), 70, 10, colRed), []image.Rectangle{image.Rect(0, 0, 128, 64)}},
		{"same span in the next row extends down", withPixel(withPixel(base, 10, 10, colRed), 10, 70, colRed), []image.Rectangle{image.Rect(0, 0, 64, 128)}},
		{"different spans stay separate", withPixel(withPixel(base, 10, 10, colRed), 100, 70, colRed),
			[]image.Rectangle{image.Rect(0, 0, 64, 64), image.Rect(64, 64, 128, 128)}},
		{"distant tiles in a row stay separate", withPixel(withPixel(base, 10, 10, colRed), 150, 10, colRed),
			[]image.Rectangle{image.Rect(0, 0, 64, 64), image.Rect(128, 0, 192, 64)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := changedRects(base, tt.cur)
			if !slices.Equal(got, tt.want) {
				t.Errorf("changedRects = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEncodeFrameLayout(t *testing.T) {
	img := solidImage(200, 150, colGreen)
	rects := []image.Rectangle{image.Rect(64, 0, 128, 64), image.Rect(192, 128, 200, 150)}
	data, err := encodeFrame(img, rects, 3, 0x01020304, false, 60)
	if err != nil {
		t.Fatal(err)
	}
	// リトルエンディアンの見出しをバイト列で確かめる（API 仕様 §5.4）。
	wantHead := []byte{1, 0, 3, 0, 0x04, 0x03, 0x02, 0x01, 200, 0, 150, 0, 2, 0}
	if !slices.Equal(data[:frameHeaderSize], wantHead) {
		t.Errorf("header = %v, want %v", data[:frameHeaderSize], wantHead)
	}
	f := parseFrame(t, data)
	if f.key || f.monitor != 3 || f.seq != 0x01020304 || f.w != 200 || f.h != 150 || !slices.Equal(f.rects(), rects) {
		t.Errorf("frame = %v", f)
	}
	if got := f.tiles[0].img.At(10, 10); !nearColor(got, colGreen) {
		t.Errorf("tile color = %v, want about %v", got, colGreen)
	}

	key, err := encodeFrame(img, []image.Rectangle{img.Bounds()}, 0, 1, true, 60)
	if err != nil {
		t.Fatal(err)
	}
	if kf := parseFrame(t, key); !kf.key || kf.flags != FrameFlagKeyframe || len(kf.tiles) != 1 || kf.tiles[0].rect != img.Bounds() {
		t.Errorf("keyframe = %v", kf)
	}
}

func TestEncodeFrameRejectsUnrepresentable(t *testing.T) {
	img := solidImage(64, 64, colBlue)
	full := []image.Rectangle{img.Bounds()}
	if _, err := encodeFrame(img, nil, 0, 1, false, 60); err == nil {
		t.Error("a frame without tiles must fail")
	}
	if _, err := encodeFrame(img, full, -1, 1, false, 60); err == nil {
		t.Error("a negative monitor must fail")
	}
	if _, err := encodeFrame(img, full, 70000, 1, false, 60); err == nil {
		t.Error("a monitor over u16 must fail")
	}
	huge := image.NewRGBA(image.Rect(0, 0, 70000, 1))
	if _, err := encodeFrame(huge, []image.Rectangle{huge.Bounds()}, 0, 1, true, 60); err == nil {
		t.Error("a width over u16 must fail")
	}
}

func TestNewStreamerValidatesConfig(t *testing.T) {
	c := newStreamCapturer(0)
	for name, cfg := range map[string]StreamerConfig{
		"no capturer": {Clock: newFakeClock(), IDs: &seqIDs{}},
		"no clock":    {Capturer: c, IDs: &seqIDs{}},
		"no ids":      {Capturer: c, Clock: newFakeClock()},
	} {
		if _, err := NewStreamer(cfg); err == nil {
			t.Errorf("%s: NewStreamer must fail", name)
		}
	}
}

func TestStreamerFirstFrameIsKeyframe(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w := e.cap.nextWatch(t)
	if w.monitor != 0 || w.fps != 10 {
		t.Errorf("Watch(monitor %d, fps %d), want (0, 10)", w.monitor, w.fps)
	}
	w.push(t, solidImage(200, 150, colBlue))

	_, f := recvFrame(t, v)
	if f.version != FrameVersion || !f.key || f.monitor != 0 || f.seq != 1 || f.w != 200 || f.h != 150 ||
		len(f.tiles) != 1 || f.tiles[0].rect != screen {
		t.Errorf("first frame = %v, want a full-screen keyframe", f)
	}
	if got := f.tiles[0].img.At(100, 100); !nearColor(got, colBlue) {
		t.Errorf("decoded pixel = %v, want about %v", got, colBlue)
	}
	if v.ID == "" {
		t.Error("Viewer.ID must be set for Configure")
	}
}

func TestStreamerSendsOnlyChangedTiles(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w := e.cap.nextWatch(t)
	base := solidImage(200, 150, colBlue)
	w.push(t, base)
	recvFrame(t, v)

	// 見た目が同じコマは送らない（連番も進めない）。変わったコマは、変わった 64×64 だけを送る。
	e.clock.Advance(time.Second)
	w.push(t, withPixel(base, 0, 0, colBlue))
	e.clock.Advance(time.Second)
	w.push(t, withRect(base, image.Rect(64, 0, 80, 16), colRed))
	_, f := recvFrame(t, v)
	if f.key || f.seq != 2 || !slices.Equal(f.rects(), []image.Rectangle{image.Rect(64, 0, 128, 64)}) {
		t.Errorf("diff frame = %v, want one non-key tile (64,0)-(128,64) with seq 2", f)
	}
	if got := f.tiles[0].img.At(8, 8); !nearColor(got, colRed) {
		t.Errorf("changed pixel area = %v, want about %v", got, colRed)
	}
}

func TestStreamerThrottlesToProfileFPS(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w := e.cap.nextWatch(t)
	base := solidImage(200, 150, colBlue)
	w.push(t, base)
	recvFrame(t, v)

	// 1 秒に 10 回までなので、時計が進むまでの 2 コマは 1 つにまとまって届く。
	w.push(t, withPixel(base, 10, 10, colRed))
	w.push(t, withPixel(withPixel(base, 10, 10, colRed), 150, 100, colRed))
	e.clock.Advance(time.Second)
	_, f := recvFrame(t, v)
	want := []image.Rectangle{image.Rect(0, 0, 64, 64), image.Rect(128, 64, 192, 128)}
	if f.seq != 2 || f.key || !slices.Equal(f.rects(), want) {
		t.Errorf("frame = %v, want one merged diff with tiles %v", f, want)
	}
}

func TestStreamerPeriodicKeyframe(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w := e.cap.nextWatch(t)
	base := solidImage(200, 150, colBlue)
	w.push(t, base)
	recvFrame(t, v)

	e.clock.Advance(4 * time.Second)
	w.push(t, withPixel(base, 5, 5, colRed))
	if _, f := recvFrame(t, v); f.key {
		t.Errorf("a frame 4 s after the keyframe must be a diff: %v", f)
	}
	// 既定の間隔（5 秒）を過ぎると、変化がなくても全面を送り直す。
	e.clock.Advance(2 * time.Second)
	w.push(t, withPixel(base, 5, 5, colRed))
	_, f := recvFrame(t, v)
	if !f.key || len(f.tiles) != 1 || f.tiles[0].rect != screen {
		t.Errorf("frame after the interval = %v, want a full keyframe", f)
	}
}

func TestStreamerStopsCaptureAtZeroViewers(t *testing.T) {
	e := newStreamEnv(t)
	v1, cancel1 := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	v2, cancel2 := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w := e.cap.nextWatch(t)
	if n := e.cap.watchCount(); n != 1 {
		t.Fatalf("Watch called %d times for one monitor, want 1", n)
	}

	cancel1()
	waitDone(t, v1)
	select {
	case <-w.stopped:
		t.Fatal("capture stopped while a viewer remains")
	default:
	}
	cancel2()
	waitDone(t, v2)
	w.waitStopped(t)
	waitUntil(t, "viewers to be released", func() bool { return e.viewerCount() == 0 })

	// また誰かが見始めたら、新しくキャプチャを始める。
	v3, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w2 := e.cap.nextWatch(t)
	w2.push(t, solidImage(200, 150, colBlue))
	if _, f := recvFrame(t, v3); !f.key {
		t.Errorf("first frame after restart = %v, want a keyframe", f)
	}
}

func TestStreamerSubscribeWithCancelledContext(t *testing.T) {
	e := newStreamEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.s.Subscribe(ctx, ViewerConfig{Quality: 70, FPS: 10}); !errors.Is(err, context.Canceled) {
		t.Errorf("Subscribe = %v, want context.Canceled", err)
	}
	if n := e.cap.watchCount(); n != 0 {
		t.Errorf("capture started %d times for a cancelled subscribe", n)
	}
}

func TestStreamerSharesEncodingPerProfile(t *testing.T) {
	e := newStreamEnv(t)
	a, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	b, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	c, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 20, FPS: 10})
	w := e.cap.nextWatch(t)
	if n := e.cap.watchCount(); n != 1 {
		t.Errorf("Watch called %d times, want 1 shared capture", n)
	}
	base := solidImage(200, 150, colBlue)
	// ノイズの入った画像なら品質の違いがバイト列に出る。
	for y := range 150 {
		for x := range 200 {
			base.SetRGBA(x, y, colBlue)
			if (x*7+y*13)%5 == 0 {
				base.SetRGBA(x, y, colRed)
			}
		}
	}
	w.push(t, base)

	da, _ := recvFrame(t, a)
	db, _ := recvFrame(t, b)
	dc, _ := recvFrame(t, c)
	// 同じプロファイルの閲覧者には、エンコードした 1 つのバイト列をそのまま渡す（NFR-P07）。
	if len(da) == 0 || &da[0] != &db[0] {
		t.Error("viewers with the same profile must share one encoded frame")
	}
	if len(dc) == 0 || &dc[0] == &da[0] || len(dc) >= len(da) {
		t.Errorf("a lower quality must be encoded separately and smaller: %d vs %d bytes", len(dc), len(da))
	}

	// 差分も同じ。
	e.clock.Advance(time.Second)
	w.push(t, withPixel(base, 3, 3, colGreen))
	da, _ = recvFrame(t, a)
	db, _ = recvFrame(t, b)
	if &da[0] != &db[0] {
		t.Error("diff frames must be shared as well")
	}
}

func TestStreamerSeparateMonitorsCaptureSeparately(t *testing.T) {
	e := newStreamEnv(t, 0, 1)
	v0, cancel0 := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w0 := e.cap.nextWatch(t)
	v1, _ := e.subscribe(t, ViewerConfig{Monitor: 1, Quality: 70, FPS: 10})
	w1 := e.cap.nextWatch(t)
	if w0.monitor != 0 || w1.monitor != 1 {
		t.Fatalf("watches = monitor %d, %d", w0.monitor, w1.monitor)
	}
	w0.push(t, solidImage(200, 150, colBlue))
	w1.push(t, solidImage(320, 240, colRed))
	if _, f := recvFrame(t, v0); f.monitor != 0 || f.w != 200 {
		t.Errorf("monitor 0 frame = %v", f)
	}
	if _, f := recvFrame(t, v1); f.monitor != 1 || f.w != 320 || f.h != 240 {
		t.Errorf("monitor 1 frame = %v", f)
	}

	// 片方のモニターの閲覧者がゼロになっても、もう片方は止めない。
	cancel0()
	w0.waitStopped(t)
	select {
	case <-w1.stopped:
		t.Error("monitor 1 capture stopped with its viewer still connected")
	default:
	}
}

func TestStreamerLimitsViewers(t *testing.T) {
	e := newStreamEnv(t)
	var cancels []context.CancelFunc
	var viewers []*Viewer
	for range MaxViewers {
		v, cancel := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
		viewers = append(viewers, v)
		cancels = append(cancels, cancel)
	}
	if _, err := e.s.Subscribe(context.Background(), ViewerConfig{Monitor: 0, Quality: 70, FPS: 10}); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("Subscribe over the limit = %v, want ErrTooManyViewers", err)
	}
	cancels[0]()
	waitDone(t, viewers[0])
	waitUntil(t, "a slot to be freed", func() bool { return e.viewerCount() == MaxViewers-1 })
	if _, cancel := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10}); cancel == nil {
		t.Fatal("no cancel")
	}
}

func TestStreamerRejectsBadConfig(t *testing.T) {
	e := newStreamEnv(t)
	for name, c := range map[string]ViewerConfig{
		"quality too high": {Monitor: 0, Quality: 101, FPS: 10},
		"quality negative": {Monitor: 0, Quality: -1, FPS: 10},
		"fps too high":     {Monitor: 0, Quality: 70, FPS: 31},
		"fps negative":     {Monitor: 0, Quality: 70, FPS: -3},
		"negative monitor": {Monitor: -1, Quality: 70, FPS: 10},
		"unknown monitor":  {Monitor: 9, Quality: 70, FPS: 10},
	} {
		if _, err := e.s.Subscribe(context.Background(), c); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: Subscribe = %v, want invalid_argument", name, err)
		}
	}
	if n := e.viewerCount(); n != 0 {
		t.Errorf("%d viewers registered after failures", n)
	}
	// 失敗しても上限の枠を消費しない。
	for range MaxViewers {
		e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	}
}

func TestStreamerCaptureStartFailure(t *testing.T) {
	e := newStreamEnv(t)
	boom := errors.New("no desktop")
	e.cap.failErr = boom
	if _, err := e.s.Subscribe(context.Background(), ViewerConfig{Monitor: 0, Quality: 70, FPS: 10}); !errors.Is(err, boom) {
		t.Errorf("Subscribe = %v, want %v", err, boom)
	}
	if n := e.viewerCount(); n != 0 {
		t.Errorf("%d viewers registered after a failure", n)
	}
}

func TestStreamerUsesSettingsDefaults(t *testing.T) {
	e := newStreamEnv(t)
	e.store.put("viewer.default_quality", "40")
	e.store.put("viewer.default_fps", "5")
	def, _ := e.subscribe(t, ViewerConfig{Monitor: 0})
	w := e.cap.nextWatch(t)
	explicit, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 40, FPS: 5})
	if w.fps != 5 {
		t.Errorf("Watch fps = %d, want the default 5", w.fps)
	}
	w.push(t, solidImage(200, 150, colBlue))
	a, _ := recvFrame(t, def)
	b, _ := recvFrame(t, explicit)
	if &a[0] != &b[0] {
		t.Error("a viewer using the defaults must share the (40, 5) profile")
	}
}

func TestStreamerBuiltInDefaults(t *testing.T) {
	e := newStreamEnv(t)
	e.subscribe(t, ViewerConfig{Monitor: 0})
	if w := e.cap.nextWatch(t); w.fps != 10 {
		t.Errorf("Watch fps = %d, want the built-in default 10", w.fps)
	}
	// 壊れた設定でも、閲覧は組み込みの既定値で続く。
	e2 := newStreamEnv(t)
	e2.store.put("viewer.default_fps", `"fast"`)
	e2.subscribe(t, ViewerConfig{Monitor: 0})
	if w := e2.cap.nextWatch(t); w.fps != 10 {
		t.Errorf("Watch fps = %d with a corrupt setting, want 10", w.fps)
	}
}

func TestStreamerDropsOldestAndRecoversWithKeyframe(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 30})
	w := e.cap.nextWatch(t)
	base := solidImage(200, 150, colBlue)
	w.push(t, base) // → キーフレーム（キュー: 1）
	waitUntil(t, "the keyframe to be queued", func() bool { return len(v.Frames) == 1 })

	e.clock.Advance(time.Second)
	w.push(t, withPixel(base, 1, 1, colRed)) // → 差分 A（キュー: 2、満杯）
	waitUntil(t, "diff A to be queued", func() bool { return len(v.Frames) == 2 })

	e.clock.Advance(time.Second)
	w.push(t, withPixel(base, 70, 1, colRed)) // → 満杯。最古（キーフレーム）を捨てて差分 B を入れ、キーフレームを待つ
	waitUntil(t, "the viewer to need a keyframe", func() bool { return e.needKey(v.ID) })
	if n := len(v.Frames); n != 2 {
		t.Fatalf("queue length = %d, want it capped at 2", n)
	}

	e.clock.Advance(time.Second)
	w.push(t, withPixel(base, 1, 70, colRed)) // → 復帰のキーフレーム。最古（差分 A）を捨てる
	waitUntil(t, "the keyframe to recover the viewer", func() bool { return !e.needKey(v.ID) })

	var got []parsedFrame
	for range 2 {
		_, f := recvFrame(t, v)
		got = append(got, f)
	}
	if got[0].key || !got[1].key {
		t.Errorf("queue = %v, want [diff B, recovery keyframe]", got)
	}
	// 連番はキーフレーム 1、差分 A が 2、差分 B が 3、復帰のキーフレームが 4。
	if got[0].seq != 3 || got[1].seq != 4 {
		t.Errorf("queue seqs = %d, %d, want 3 (diff B), 4 (recovery)", got[0].seq, got[1].seq)
	}
	if len(got[1].tiles) != 1 || got[1].tiles[0].rect != screen {
		t.Errorf("recovery keyframe = %v, want full screen", got[1])
	}
	if len(v.Frames) != 0 {
		t.Errorf("%d extra frames queued", len(v.Frames))
	}
}

func TestStreamerSlowViewerDoesNotAffectOthers(t *testing.T) {
	e := newStreamEnv(t)
	slow, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 30})
	fast, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 30})
	w := e.cap.nextWatch(t)
	base := solidImage(200, 150, colBlue)
	w.push(t, base)
	recvFrame(t, fast)

	for i := range 5 {
		e.clock.Advance(100 * time.Millisecond) // 5 回でも既定のキーフレームの間隔（5 秒）には届かない
		w.push(t, withPixel(base, i*30, 1, colRed))
		_, f := recvFrame(t, fast)
		if f.key || f.seq != uint32(i+2) {
			t.Fatalf("fast viewer frame %d = %v", i, f)
		}
	}
	if n := len(slow.Frames); n != viewerQueueSize {
		t.Errorf("slow viewer queue = %d, want %d", n, viewerQueueSize)
	}
}

func TestStreamerResolutionChangeRebuildsProfiles(t *testing.T) {
	e := newStreamEnv(t)
	a, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 30})
	b, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 30, FPS: 30})
	w := e.cap.nextWatch(t)
	w.push(t, solidImage(200, 150, colBlue))
	recvFrame(t, a)
	recvFrame(t, b)

	e.clock.Advance(time.Second)
	w.push(t, solidImage(320, 180, colBlue))
	for name, v := range map[string]*Viewer{"a": a, "b": b} {
		_, f := recvFrame(t, v)
		if !f.key || f.w != 320 || f.h != 180 || len(f.tiles) != 1 || f.tiles[0].rect != image.Rect(0, 0, 320, 180) {
			t.Errorf("viewer %s frame after resize = %v, want a full keyframe at 320x180", name, f)
		}
	}
}

func TestStreamerConfigureQualityStartsWithKeyframe(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w := e.cap.nextWatch(t)
	base := solidImage(200, 150, colBlue)
	w.push(t, base)
	recvFrame(t, v)

	if err := e.s.Configure(v.ID, ViewerConfig{Monitor: 0, Quality: 30}); err != nil {
		t.Fatal(err)
	}
	_, f := recvFrame(t, v)
	if !f.key || f.tiles[0].rect != screen {
		t.Errorf("frame after Configure = %v, want a full keyframe", f)
	}
	if e.cap.watchCount() != 1 {
		t.Error("changing quality must not restart the capture")
	}
}

func TestStreamerConfigureFPSRestartsCaptureAtHighestRate(t *testing.T) {
	e := newStreamEnv(t)
	slow, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 5})
	w1 := e.cap.nextWatch(t)
	w1.push(t, solidImage(200, 150, colBlue))
	recvFrame(t, slow)

	fast, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 20})
	w1.waitStopped(t)
	w2 := e.cap.nextWatch(t)
	if w2.fps != 20 {
		t.Errorf("restarted capture fps = %d, want 20", w2.fps)
	}
	w2.push(t, solidImage(200, 150, colBlue))
	if _, f := recvFrame(t, fast); !f.key {
		t.Errorf("fast viewer first frame = %v", f)
	}

	// 速い閲覧者が抜けたら、遅い方の FPS に戻す。
	if err := e.s.Configure(fast.ID, ViewerConfig{Monitor: 0, FPS: 5}); err != nil {
		t.Fatal(err)
	}
	w2.waitStopped(t)
	if w3 := e.cap.nextWatch(t); w3.fps != 5 {
		t.Errorf("capture fps after the fast viewer slowed = %d, want 5", w3.fps)
	}
}

func TestStreamerConfigureMonitor(t *testing.T) {
	e := newStreamEnv(t, 0, 1)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w0 := e.cap.nextWatch(t)
	w0.push(t, solidImage(200, 150, colBlue))
	recvFrame(t, v)

	if err := e.s.Configure(v.ID, ViewerConfig{Monitor: 1}); err != nil {
		t.Fatal(err)
	}
	// 閲覧者のいなくなった元のモニターは止まり、新しいモニターのキーフレームが届く。
	w0.waitStopped(t)
	w1 := e.cap.nextWatch(t)
	if w1.monitor != 1 {
		t.Fatalf("capture monitor = %d, want 1", w1.monitor)
	}
	w1.push(t, solidImage(320, 240, colRed))
	_, f := recvFrame(t, v)
	if !f.key || f.monitor != 1 || f.w != 320 || f.h != 240 {
		t.Errorf("frame after the monitor change = %v", f)
	}
}

func TestStreamerConfigureErrors(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	e.cap.nextWatch(t)

	for name, c := range map[string]ViewerConfig{
		"quality too high": {Monitor: 0, Quality: 101},
		"fps too high":     {Monitor: 0, FPS: 31},
		"unknown monitor":  {Monitor: 7},
		"negative monitor": {Monitor: -1},
	} {
		if err := e.s.Configure(v.ID, c); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: Configure = %v, want invalid_argument", name, err)
		}
	}
	if err := e.s.Configure("nobody", ViewerConfig{Monitor: 0, Quality: 50}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("unknown viewer: Configure = %v, want invalid_argument", err)
	}
	// 何も変えない指定は成功し、キーフレームも要らない。
	if err := e.s.Configure(v.ID, ViewerConfig{Monitor: 0}); err != nil {
		t.Errorf("no-op Configure = %v", err)
	}
	e.s.mu.Lock()
	cfg := e.s.viewers[v.ID].cfg
	e.s.mu.Unlock()
	if cfg != (ViewerConfig{Monitor: 0, Quality: 70, FPS: 10}) {
		t.Errorf("config changed by failed Configure calls: %+v", cfg)
	}
}

func TestStreamerEndsViewersWhenCaptureEnds(t *testing.T) {
	e := newStreamEnv(t)
	a, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	b, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 40, FPS: 10})
	w := e.cap.nextWatch(t)
	w.end() // キャプチャが自分で終わった（復旧できない失敗）
	waitDone(t, a)
	waitDone(t, b)
	waitUntil(t, "viewers to be released", func() bool { return e.viewerCount() == 0 })

	// 失敗したあとでも、新しい閲覧者はキャプチャをやり直せる。
	e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	if e.cap.nextWatch(t) == nil {
		t.Fatal("no new capture")
	}
}

func TestStreamerLogsCaptureErrorsAndContinues(t *testing.T) {
	e := newStreamEnv(t)
	v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 10})
	w := e.cap.nextWatch(t)
	w.errs <- domain.ErrDesktopUnavailable
	w.push(t, solidImage(200, 150, colBlue))
	if _, f := recvFrame(t, v); !f.key {
		t.Errorf("frame = %v", f)
	}
}

// nearColor は JPEG の損失を許して、c にほぼ等しいかを返す。
func nearColor(got color.Color, c color.RGBA) bool {
	r, g, b, _ := got.RGBA()
	near := func(a uint32, b uint8) bool {
		d := int(a>>8) - int(b)
		return d > -24 && d < 24
	}
	return near(r, c.R) && near(g, c.G) && near(b, c.B)
}
