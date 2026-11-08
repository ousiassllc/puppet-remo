package service

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// グループ源（#157）の Streamer のテスト。撮影元は偽の GroupGrabber で、モニター撮影（streamCapturer.Watch）が 1 回も呼ばれないことを確かめる。
// 偽 platform の試験で、Windows 実機の成立性ゲート（S1〜S3）の代わりにならない。

var groupT = domain.WindowRef{ID: "1001", PID: 4120, ProcessStart: 9, Class: "FakeNotepad"}

// fakeGroups は GroupGrabber の偽物。frame・err を差し替えて、呼び出しの回数を数える。
type fakeGroups struct {
	mu    sync.Mutex
	frame GroupFrame
	err   error
	calls int
	refs  []domain.WindowRef
}

func (g *fakeGroups) GrabGroupFrame(_ context.Context, ref domain.WindowRef) (GroupFrame, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.refs = append(g.refs, ref)
	return g.frame, g.err
}

func (g *fakeGroups) set(img *image.RGBA, origin domain.Point, members int, excluded ...domain.BlockedBy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = nil
	g.frame = GroupFrame{Image: img, Origin: origin, Excluded: excluded}
	g.frame.Members = make([]GroupMemberFrame, members)
}

func (g *fakeGroups) fail(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}

func (g *fakeGroups) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func newGroupStreamEnv(t *testing.T, g *fakeGroups) *streamEnv {
	t.Helper()
	e := &streamEnv{cap: newStreamCapturer(0), clock: newFakeClock(), store: newCtlSettings()}
	st, err := NewStreamer(StreamerConfig{Capturer: e.cap, Clock: e.clock, IDs: &seqIDs{}, Settings: e.store, Groups: g})
	if err != nil {
		t.Fatal(err)
	}
	e.s = st.(*streamer)
	return e
}

func groupViewerConfig() ViewerConfig {
	return ViewerConfig{Monitor: 0, Quality: 70, FPS: 30, Target: groupT}
}

func recvGroupStatus(t *testing.T, v *Viewer) GroupStatus {
	t.Helper()
	select {
	case st, ok := <-v.Group:
		if !ok {
			t.Fatal("Group is closed")
		}
		return st
	case <-time.After(streamWait):
		t.Fatal("no group status arrived")
		return GroupStatus{}
	}
}

// グループ源のフレームは合成画像の大きさで、見出しにグループのフラグ・原点が入る。モニター撮影は使わない。
func TestStreamerGroupSourceSendsOnlyTheCompositeWithItsOrigin(t *testing.T) {
	g := &fakeGroups{}
	g.set(solidImage(200, 150, colBlue), domain.Point{X: -30, Y: 80}, 1)
	e := newGroupStreamEnv(t, g)
	v, _ := e.subscribe(t, groupViewerConfig())

	data, f := recvFrame(t, v)
	if !f.key || f.w != 200 || f.h != 150 || f.monitor != FrameMonitorGroup || f.flags&FrameFlagGroup == 0 || len(f.tiles) != 1 {
		t.Fatalf("frame = %+v, want a 200x150 group keyframe", f)
	}
	info, ok := ParseGroupFrameInfo(data)
	if !ok || info.Width != 200 || info.Height != 150 || info.Origin != (domain.Point{X: -30, Y: 80}) {
		t.Errorf("ParseGroupFrameInfo = %+v, %v; want 200x150 at (-30, 80)", info, ok)
	}
	if got := binary.LittleEndian.Uint16(data[12:]); got != 1 {
		t.Errorf("tile count = %d, want 1", got)
	}
	if n := e.cap.watchCount(); n != 0 {
		t.Errorf("monitor Watch was called %d times for a group source, want 0", n)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.refs) == 0 || g.refs[0] != groupT {
		t.Errorf("GrabGroupFrame refs = %v, want the target %v", g.refs, groupT)
	}
}

// メニュー・ダイアログの開閉で画像の大きさが変わると、新しい大きさのキーフレームを送る。原点だけが変わっても同じ（差分にしない）。
func TestStreamerGroupSourceKeyframesWhenSizeOrOriginChanges(t *testing.T) {
	g := &fakeGroups{}
	g.set(solidImage(200, 150, colBlue), domain.Point{X: 10, Y: 10}, 1)
	e := newGroupStreamEnv(t, g)
	v, _ := e.subscribe(t, groupViewerConfig())
	_, first := recvFrame(t, v)
	if first.w != 200 || first.h != 150 {
		t.Fatalf("first frame = %+v", first)
	}

	// 大きさが変わる（ダイアログが T の外へはみ出して開いた）。
	e.clock.Advance(time.Second)
	g.set(solidImage(300, 160, colBlue), domain.Point{X: 10, Y: 0}, 2)
	var grown parsedFrame
	for {
		_, f := recvFrame(t, v)
		if f.w == 300 {
			grown = f
			break
		}
		e.clock.Advance(time.Second)
	}
	if !grown.key || grown.h != 160 || len(grown.tiles) != 1 || grown.tiles[0].rect != image.Rect(0, 0, 300, 160) {
		t.Errorf("grown frame = %+v, want a full 300x160 keyframe", grown)
	}

	// 大きさは同じで原点だけが動く（左・上へメニューがはみ出して閉じた直後など）。内容が同じでもキーフレームで原点を伝える。
	e.clock.Advance(time.Second)
	g.set(solidImage(300, 160, colBlue), domain.Point{X: 0, Y: 0}, 2)
	for {
		data, f := recvFrame(t, v)
		if info, _ := ParseGroupFrameInfo(data); info.Origin == (domain.Point{}) {
			if !f.key {
				t.Errorf("origin change frame = %+v, want a keyframe", f)
			}
			break
		}
		e.clock.Advance(time.Second)
	}
}

// 閲覧者がゼロになると撮影を止める（FR-W09）。
func TestStreamerGroupSourceStopsAtZeroViewers(t *testing.T) {
	g := &fakeGroups{}
	g.set(solidImage(100, 100, colBlue), domain.Point{}, 1)
	e := newGroupStreamEnv(t, g)
	a, cancelA := e.subscribe(t, groupViewerConfig())
	b, cancelB := e.subscribe(t, groupViewerConfig())
	recvFrame(t, a)
	recvFrame(t, b)
	sources := func() int {
		e.s.mu.Lock()
		defer e.s.mu.Unlock()
		return len(e.s.sources) + len(e.s.stopping)
	}
	if sources() != 1 {
		t.Fatalf("sources = %d, want one shared group source", sources())
	}
	cancelA()
	waitDone(t, a)
	if sources() != 1 {
		t.Error("the source must keep running while a viewer remains")
	}
	cancelB()
	waitDone(t, b)
	waitUntil(t, "the group capture to stop", func() bool { return sources() == 0 })
	n := g.callCount()
	if n == 0 {
		t.Fatal("the group was never captured")
	}
	if e.cap.watchCount() != 0 {
		t.Error("monitor capture must not be used")
	}
}

// 撮影の失敗は閲覧者を終えず、GroupStatus.Err で知らせる。モニター撮影には切り替えず、フレームも流さない。直れば続ける。
func TestStreamerGroupSourceReportsFailureWithoutMonitorFallback(t *testing.T) {
	g := &fakeGroups{}
	boom := domain.ErrInputBlocked.WithDetail("reason", string(domain.ReasonCaptureUnavailable))
	g.fail(boom)
	e := newGroupStreamEnv(t, g)
	v, _ := e.subscribe(t, groupViewerConfig())
	st := recvGroupStatus(t, v)
	if !errors.Is(st.Err, domain.ErrInputBlocked) {
		t.Fatalf("status = %+v, want the capture failure", st)
	}
	select {
	case data := <-v.Frames:
		t.Fatalf("a frame (%d bytes) was streamed although the group could not be captured", len(data))
	default:
	}
	select {
	case <-v.Done:
		t.Fatal("a capture failure must not end the viewer")
	default:
	}
	if e.cap.watchCount() != 0 {
		t.Error("the stream fell back to monitor capture")
	}

	g.set(solidImage(100, 100, colBlue), domain.Point{}, 1)
	if _, f := recvFrame(t, v); f.w != 100 {
		t.Errorf("frame after recovery = %+v", f)
	}
}

// メンバー数・除外したウィンドウの変化は GroupStatus で届く（変わらない間は重ねて送らない）。
func TestStreamerGroupSourceReportsMembersAndExcluded(t *testing.T) {
	g := &fakeGroups{}
	g.set(solidImage(100, 100, colBlue), domain.Point{}, 1)
	e := newGroupStreamEnv(t, g)
	v, _ := e.subscribe(t, groupViewerConfig())
	if st := recvGroupStatus(t, v); st.Err != nil || st.Members != 1 || len(st.Excluded) != 0 {
		t.Fatalf("first status = %+v", st)
	}
	menu := domain.BlockedBy{WindowID: "1002", Process: "fakenotepad.exe", PID: 4120, Reason: domain.ReasonRelatedUnverifiable}
	g.set(solidImage(100, 100, colBlue), domain.Point{}, 2, menu)
	st := recvGroupStatus(t, v)
	if st.Members != 2 || len(st.Excluded) != 1 || st.Excluded[0] != menu {
		t.Errorf("status = %+v, want 2 members and the excluded menu", st)
	}
}

func TestStreamerGroupSourceConfigRules(t *testing.T) {
	g := &fakeGroups{}
	g.set(solidImage(100, 100, colBlue), domain.Point{}, 1)

	t.Run("Groups が無ければ拒否", func(t *testing.T) {
		e := newStreamEnv(t)
		if _, err := e.s.Subscribe(context.Background(), groupViewerConfig()); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("err = %v, want invalid_argument", err)
		}
		if e.cap.watchCount() != 0 {
			t.Error("a rejected group source must not start monitor capture")
		}
	})
	t.Run("同一性を確かめられない Target は拒否", func(t *testing.T) {
		e := newGroupStreamEnv(t, g)
		c := groupViewerConfig()
		c.Target = domain.WindowRef{ID: "1001"}
		if _, err := e.s.Subscribe(context.Background(), c); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("err = %v, want invalid_argument", err)
		}
	})
	t.Run("モニター源からグループ源へ切り替えるとモニターの撮影を止める", func(t *testing.T) {
		e := newGroupStreamEnv(t, g)
		v, _ := e.subscribe(t, ViewerConfig{Monitor: 0, Quality: 70, FPS: 30})
		w := e.cap.nextWatch(t)
		if err := e.s.Configure(v.ID, groupViewerConfig()); err != nil {
			t.Fatal(err)
		}
		w.waitStopped(t)
		if _, f := recvFrameWhere(t, v, func(f parsedFrame) bool { return f.monitor == FrameMonitorGroup }); !f.key {
			t.Errorf("the first group frame = %+v, want a keyframe", f)
		}
		// グループ源からモニター源へ戻る。
		if err := e.s.Configure(v.ID, ViewerConfig{Monitor: 0}); err != nil {
			t.Fatal(err)
		}
		if w2 := e.cap.nextWatch(t); w2.monitor != 0 {
			t.Errorf("Watch(monitor %d), want 0", w2.monitor)
		}
	})
}

// recvFrameWhere は pred を満たすフレームまで読み捨てる（切り替え前の飛行中のフレームを飛ばす）。
func recvFrameWhere(t *testing.T, v *Viewer, pred func(parsedFrame) bool) ([]byte, parsedFrame) {
	t.Helper()
	for {
		data, f := recvFrame(t, v)
		if pred(f) {
			return data, f
		}
	}
}
