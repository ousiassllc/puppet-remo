package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func TestOpChangesScreen(t *testing.T) {
	pt := domain.Point{X: 1, Y: 1}
	tests := []struct {
		name string
		op   domain.Op
		want bool
	}{
		{"click", domain.Click{At: &pt, Button: domain.ButtonLeft, Count: 1}, true},
		{"drag", domain.Drag{From: pt, To: pt}, true},
		{"scroll", domain.Scroll{At: pt, Dir: domain.DirDown, Amount: 1}, true},
		{"key press", domain.KeyPress{}, true},
		{"type", domain.TypeText{Text: "a"}, true},
		{"focus_window", domain.FocusWindow{ID: "1"}, true},
		{"web mouse button", domain.MouseButton{At: pt, Button: domain.ButtonLeft, Action: domain.ButtonActionUp}, true},
		{"web wheel", domain.Wheel{At: pt, DY: 1}, true},
		{"web key event", domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA"}, true},
		// カーソルの移動だけは撮らない（クリックを伴わない）。
		{"mouse_move", domain.MouseMove{At: pt}, false},
		{"web pointer move", domain.PointerMove{At: pt}, false},
		{"clipboard_read", domain.ClipboardRead{}, false},
		{"clipboard_write", domain.ClipboardWrite{Text: "x"}, false},
		{"get_ui_tree", domain.GetUITree{}, false},
		{"wait", domain.Wait{Duration: time.Second}, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opChangesScreen(tt.op); got != tt.want {
				t.Fatalf("opChangesScreen(%T) = %v, want %v", tt.op, got, tt.want)
			}
		})
	}
}

// thumbMonitors は左（Index 0・非プライマリ）と右（Index 1・プライマリ）の 2 台。プライマリが Index 0 ではないことで、
// 「プライマリ」と「Index 0」の取り違えを検出する。
var thumbMonitors = []domain.Monitor{
	{Index: 0, Name: "left", Origin: domain.Point{X: -1920, Y: 0}, Size: domain.Size{W: 1920, H: 1080}, DPI: 96},
	{Index: 1, Name: "right", Primary: true, Origin: domain.Point{X: 0, Y: 0}, Size: domain.Size{W: 1920, H: 1080}, DPI: 96},
}

func TestThumbTargetAndMonitorSelection(t *testing.T) {
	at := func(x, y int) *domain.Point { return &domain.Point{X: x, Y: y} }
	web := domain.Actor{Kind: domain.ActorWeb}
	mcp := domain.Actor{Kind: domain.ActorMCP}
	tests := []struct {
		name   string
		who    domain.Actor
		op     domain.Op
		want   int
		wantOK bool
	}{
		{"explicit monitor wins", mcp, domain.Click{At: at(-100, 10), Monitor: 0}, 0, true},
		{"explicit monitor 1", mcp, domain.Click{At: at(-100, 10), Monitor: 1}, 1, true},
		{"unknown explicit monitor", mcp, domain.Click{At: at(10, 10), Monitor: 7}, 0, false},
		{"coordinates pick the containing monitor", mcp, domain.Click{At: at(-100, 10), Monitor: domain.MonitorPrimary}, 0, true},
		{"drag uses the end point", mcp, domain.Drag{From: domain.Point{X: 10, Y: 10}, To: domain.Point{X: -10, Y: 10}, Monitor: domain.MonitorPrimary}, 0, true},
		{"scroll", mcp, domain.Scroll{At: domain.Point{X: -5, Y: 5}, Monitor: domain.MonitorPrimary}, 0, true},
		{"click in place at the cursor", mcp, domain.Click{At: at(300, 300), Monitor: domain.MonitorPrimary}, 1, true},
		{"coordinates outside every monitor fall back to primary", mcp, domain.Click{At: at(99999, 0), Monitor: domain.MonitorPrimary}, 1, true},
		{"no coordinates: primary", mcp, domain.KeyPress{}, 1, true},
		{"type: primary", mcp, domain.TypeText{Text: "a"}, 1, true},
		{"focus_window: primary", mcp, domain.FocusWindow{ID: "1"}, 1, true},
		{"web is always primary", web, domain.MouseButton{At: domain.Point{X: -5, Y: 5}, Monitor: 0}, 1, true},
		{"web wheel is always primary", web, domain.Wheel{At: domain.Point{X: -5, Y: 5}, Monitor: 0}, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pickThumbMonitor(thumbMonitors, thumbTargetOf(tt.who, tt.op))
			if ok != tt.wantOK || (ok && got != tt.want) {
				t.Fatalf("monitor = %d (ok=%v), want %d (ok=%v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
	if _, ok := pickThumbMonitor([]domain.Monitor{{Index: 0}}, thumbTarget{monitor: domain.MonitorPrimary}); ok {
		t.Fatal("no primary monitor must not pick one")
	}
}

// thumbCapturer は Grab を gate で止められる Capturer。gate が nil なら止めない。
type thumbCapturer struct {
	*platformtest.FakeCapturer
	gate    chan struct{}
	started chan struct{} // Grab が gate の前に来るたびに送る（容量は十分に大きい）
}

func (c *thumbCapturer) Grab(ctx context.Context, monitor int) (*image.RGBA, error) {
	if c.gate != nil {
		c.started <- struct{}{}
		select {
		case <-c.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.FakeCapturer.Grab(ctx, monitor)
}

// thumbRecord は thumbFakeAudit が受けた 1 件。Thumb が非 nil なら RecordWithThumb で来た。
type thumbRecord struct {
	Entry repository.AuditEntry
	Thumb *Thumbnail
}

// thumbFakeAudit は thumbAuditor の偽物。
type thumbFakeAudit struct {
	enabled atomic.Bool
	mu      sync.Mutex
	got     []thumbRecord
}

func newThumbFakeAudit() *thumbFakeAudit {
	a := &thumbFakeAudit{}
	a.enabled.Store(true)
	return a
}

func (a *thumbFakeAudit) Record(_ context.Context, e repository.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, thumbRecord{Entry: e})
}

// RecordSync は launch_app の試行の行。サムネイルの検査の対象ではないので、何も溜めずに成功させる。
func (a *thumbFakeAudit) RecordSync(context.Context, repository.AuditEntry) error { return nil }

func (a *thumbFakeAudit) RecordWithThumb(_ context.Context, e repository.AuditEntry, t Thumbnail) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, thumbRecord{Entry: e, Thumb: &t})
}

func (a *thumbFakeAudit) ThumbnailsEnabled(context.Context) bool { return a.enabled.Load() }

func (a *thumbFakeAudit) records() []thumbRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]thumbRecord(nil), a.got...)
}

// thumbCtl は、2 台のモニター・サムネイル対応の監査・撮影を止められる Capturer で組んだ Control。
type thumbCtl struct {
	*ctlEnv
	ctl     Control
	shooter *thumbShooter
	cap     *thumbCapturer
	audit   *thumbFakeAudit
	logs    *bytes.Buffer
}

func newThumbCtl(t *testing.T) *thumbCtl {
	t.Helper()
	e := newAppEnv(t)
	tc := &thumbCtl{
		ctlEnv: e,
		cap:    &thumbCapturer{FakeCapturer: platformtest.NewFakeCapturer(thumbMonitors...), started: make(chan struct{}, 64)},
		audit:  newThumbFakeAudit(),
		logs:   &bytes.Buffer{},
	}
	ctl, err := NewControl(ControlConfig{
		Capturer: tc.cap, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings,
		Audit: tc.audit, Clock: e.clock,
		Sleep:  func(context.Context, time.Duration) error { return nil },
		Logger: slog.New(slog.NewTextHandler(tc.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	tc.ctl = ctl
	tc.shooter = ctl.(*control).thumbs
	if tc.shooter == nil {
		t.Fatal("control must enable thumbnails when the audit supports them")
	}
	return tc
}

func (tc *thumbCtl) do(who domain.Actor, op domain.Op) error {
	tc.t.Helper()
	_, err := tc.ctl.Do(context.Background(), who, op)
	return err
}

// finish は始まった撮影がすべて終わるまで待ち、記録を返す。
func (tc *thumbCtl) finish() []thumbRecord {
	tc.shooter.wait()
	return tc.audit.records()
}

func (tc *thumbCtl) grabs() []int {
	var out []int
	for _, c := range tc.cap.CallsTo("Grab") {
		out = append(out, c.Args[0].(int))
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestControlThumbnailCapturesScreenChangingOps(t *testing.T) {
	click := func(m int) domain.Op {
		return domain.Click{At: &domain.Point{X: 10, Y: 10}, Button: domain.ButtonLeft, Count: 1, Monitor: m}
	}
	tests := []struct {
		name  string
		who   domain.Actor
		op    domain.Op
		event string
		grab  int
	}{
		{"mcp click on monitor 0", ctlAlice, click(0), "tool.left_click", 0},
		{"mcp click on monitor 1", ctlAlice, click(1), "tool.left_click", 1},
		{"mcp click on primary", ctlAlice, click(domain.MonitorPrimary), "tool.left_click", 1},
		{"mcp key", ctlAlice, domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}}, "tool.key", 1},
		{"mcp type", ctlAlice, domain.TypeText{Text: "hi"}, "tool.type", 1},
		{"mcp scroll", ctlAlice, domain.Scroll{At: domain.Point{X: 10, Y: 10}, Dir: domain.DirDown, Amount: 1, Monitor: 0}, "tool.scroll", 0},
		{"mcp drag", ctlAlice, domain.Drag{From: domain.Point{X: 10, Y: 10}, To: domain.Point{X: 20, Y: 20}, Monitor: 0}, "tool.left_click_drag", 0},
		{"mcp focus_window", ctlAlice, domain.FocusWindow{ID: "1"}, "tool.focus_window", 1},
		{"web key", ctlViewer, domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a"}, "tool.key_down", 1},
		{"web text", ctlViewer, domain.TypeText{Text: "あ"}, "tool.type", 1},
		// Web 入力は座標・モニターの指定にかかわらずプライマリ。
		{"web mouse on monitor 0", ctlViewer, domain.MouseButton{At: domain.Point{X: 5, Y: 5}, Button: domain.ButtonLeft, Action: domain.ButtonActionUp, Monitor: 0}, "tool.mouse_up", 1},
		{"web wheel", ctlViewer, domain.Wheel{At: domain.Point{X: 5, Y: 5}, DY: 1, Monitor: 0}, "tool.wheel", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := newThumbCtl(t)
			mustAcquire(t, tc.ctlEnv, tt.who)
			if err := tc.do(tt.who, tt.op); err != nil {
				t.Fatal(err)
			}
			got := tc.finish()
			if len(got) != 1 || got[0].Entry.Event != tt.event || got[0].Entry.Result != auditOK {
				t.Fatalf("records = %+v, want one ok %s", got, tt.event)
			}
			if got[0].Thumb == nil || len(got[0].Thumb.JPEG) == 0 {
				t.Fatalf("no thumbnail on %+v", got[0])
			}
			if g := tc.grabs(); !equalInts(g, []int{tt.grab}) {
				t.Fatalf("grabbed monitors = %v, want [%d]", g, tt.grab)
			}
			if got[0].Entry.TS.IsZero() {
				t.Fatal("the entry must carry the operation time even though it is recorded after the capture")
			}
		})
	}
}

func TestControlThumbnailLaunchApp(t *testing.T) {
	tc := newThumbCtl(t)
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	app := registerNotepad(t, tc.ctlEnv)
	if _, err := tc.ctl.Launch(context.Background(), ctlAlice, app.ID); err != nil {
		t.Fatal(err)
	}
	got := tc.finish()
	if len(got) != 1 || got[0].Entry.Event != "tool.launch_app" || got[0].Thumb == nil {
		t.Fatalf("records = %+v", got)
	}
	if g := tc.grabs(); !equalInts(g, []int{1}) {
		t.Fatalf("grabbed monitors = %v, want the primary [1]", g)
	}
}

func TestControlThumbnailSkipsOpsThatDoNotChangeTheScreen(t *testing.T) {
	ops := []struct {
		name string
		who  domain.Actor
		op   domain.Op
	}{
		{"mouse_move", ctlAlice, domain.MouseMove{At: domain.Point{X: 10, Y: 10}, Monitor: 1}},
		{"clipboard_write", ctlAlice, domain.ClipboardWrite{Text: "x"}},
		{"clipboard_read", ctlAlice, domain.ClipboardRead{}},
		{"get_ui_tree", ctlAlice, domain.GetUITree{}},
		{"wait", ctlAlice, domain.Wait{Duration: 0}},
	}
	for _, tt := range ops {
		t.Run(tt.name, func(t *testing.T) {
			tc := newThumbCtl(t)
			mustAcquire(t, tc.ctlEnv, tt.who)
			if err := tc.do(tt.who, tt.op); err != nil {
				t.Fatal(err)
			}
			// 撮らない操作は、Do が戻った時点でエントリが記録済み（待たずに見える）。
			got := tc.audit.records()
			tc.shooter.wait()
			if tt.name == "wait" {
				if len(got) != 1 {
					t.Fatalf("records = %+v", got)
				}
			} else if len(got) != 1 || got[0].Thumb != nil {
				t.Fatalf("records = %+v, want one entry without a thumbnail", got)
			}
			if g := tc.grabs(); len(g) != 0 {
				t.Fatalf("grabbed %v", g)
			}
		})
	}
	t.Run("web pointer move is not audited and not captured", func(t *testing.T) {
		tc := newThumbCtl(t)
		mustAcquire(t, tc.ctlEnv, ctlViewer)
		if err := tc.do(ctlViewer, domain.PointerMove{At: domain.Point{X: 5, Y: 5}, Monitor: domain.MonitorPrimary}); err != nil {
			t.Fatal(err)
		}
		if got := tc.finish(); len(got) != 0 || len(tc.grabs()) != 0 {
			t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
		}
	})
}

func TestControlThumbnailSkipsFailedOps(t *testing.T) {
	t.Run("invalid argument", func(t *testing.T) {
		tc := newThumbCtl(t)
		mustAcquire(t, tc.ctlEnv, ctlAlice)
		err := tc.do(ctlAlice, domain.Click{At: &domain.Point{X: 1, Y: 1}, Button: "bogus", Count: 1})
		if err == nil {
			t.Fatal("want an error")
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Entry.Result == auditOK || got[0].Thumb != nil || len(tc.grabs()) != 0 {
			t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
		}
	})
	t.Run("rejected by input guard (elevated window)", func(t *testing.T) {
		tc := newThumbCtl(t)
		mustAcquire(t, tc.ctlEnv, ctlAlice)
		// 昇格ウィンドウの上（物理 2400..3200）を、右（プライマリ）のモニターの座標で狙う。
		tc.windows.SetWindows(domain.WindowInfo{ID: "9", Title: "admin", Elevated: true, Rect: domain.Rect{X: 0, Y: 0, W: 1920, H: 1080}})
		err := tc.do(ctlAlice, domain.Click{At: &domain.Point{X: 100, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 1})
		if !errors.Is(err, domain.ErrInputBlocked) {
			t.Fatalf("err = %v", err)
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Entry.Result != auditDenied || got[0].Thumb != nil || len(tc.grabs()) != 0 {
			t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
		}
	})
	t.Run("lease required (web without a lease)", func(t *testing.T) {
		tc := newThumbCtl(t)
		err := tc.do(ctlViewer, domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}})
		if !errors.Is(err, domain.ErrLeaseRequired) {
			t.Fatalf("err = %v", err)
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Entry.Result != auditDenied || got[0].Thumb != nil || len(tc.grabs()) != 0 {
			t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
		}
	})
	t.Run("platform failure", func(t *testing.T) {
		tc := newThumbCtl(t)
		mustAcquire(t, tc.ctlEnv, ctlAlice)
		tc.input.SetError("Key", errors.New("boom"))
		if err := tc.do(ctlAlice, domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}}); err == nil {
			t.Fatal("want an error")
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Entry.Result != auditError || got[0].Thumb != nil || len(tc.grabs()) != 0 {
			t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
		}
	})
	t.Run("launch_app failure", func(t *testing.T) {
		tc := newThumbCtl(t)
		mustAcquire(t, tc.ctlEnv, ctlAlice)
		if _, err := tc.ctl.Launch(context.Background(), ctlAlice, "no-such-app"); err == nil {
			t.Fatal("want an error")
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Thumb != nil || len(tc.grabs()) != 0 {
			t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
		}
	})
}

func TestControlThumbnailDisabledSettingNeverGrabs(t *testing.T) {
	tc := newThumbCtl(t)
	tc.audit.enabled.Store(false)
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	if err := tc.do(ctlAlice, domain.TypeText{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := tc.audit.records()
	if len(got) != 1 || got[0].Thumb != nil || len(tc.grabs()) != 0 {
		t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
	}
}

func TestControlThumbnailWebInputIsLimitedToOnePerSecondGlobally(t *testing.T) {
	tc := newThumbCtl(t)
	mustAcquire(t, tc.ctlEnv, ctlViewer)
	key := domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}}
	thumbs := func() int {
		n := 0
		for _, r := range tc.finish() {
			if r.Thumb != nil {
				n++
			}
		}
		return n
	}

	for range 3 {
		if err := tc.do(ctlViewer, key); err != nil {
			t.Fatal(err)
		}
		tc.shooter.wait()
	}
	if n := thumbs(); n != 1 {
		t.Fatalf("thumbnails in the first second = %d, want 1", n)
	}
	// 別の viewer でも、全体で 1 秒 1 枚（viewer ごとではない）。
	tc.clock.Advance(webThumbInterval - time.Millisecond)
	other := domain.Actor{Kind: domain.ActorWeb, ClientID: "client-w", SessionID: "viewer-2"}
	_ = tc.do(other, key) // リース保持者ではないので失敗し、撮らない
	if err := tc.do(ctlViewer, domain.TypeText{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if n := thumbs(); n != 1 {
		t.Fatalf("thumbnails just before 1s = %d, want 1", n)
	}
	// 1 秒たつと次の 1 枚。
	tc.clock.Advance(time.Millisecond)
	if err := tc.do(ctlViewer, domain.TypeText{Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if n := thumbs(); n != 2 {
		t.Fatalf("thumbnails after 1s = %d, want 2", n)
	}
	if got := len(tc.grabs()); got != 2 {
		t.Fatalf("Grab calls = %d, want 2 (a throttled op must not grab)", got)
	}
	// 間引かれた操作のエントリも記録される。
	if n := len(tc.audit.records()); n != 6 {
		t.Fatalf("entries = %d, want 6", n)
	}
}

func TestControlThumbnailMCPIsNotLimitedByTheWebInterval(t *testing.T) {
	tc := newThumbCtl(t)
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	for range 3 {
		if err := tc.do(ctlAlice, domain.TypeText{Text: "a"}); err != nil {
			t.Fatal(err)
		}
		tc.shooter.wait()
	}
	if g := tc.grabs(); !equalInts(g, []int{1, 1, 1}) {
		t.Fatalf("grabs = %v, want 3", g)
	}
}

func TestControlThumbnailWebThrottleDoesNotConsumeOnDisabledOrFailed(t *testing.T) {
	tc := newThumbCtl(t)
	mustAcquire(t, tc.ctlEnv, ctlViewer)
	// 設定が無効の間の操作は、間隔の枠を使わない。
	tc.audit.enabled.Store(false)
	if err := tc.do(ctlViewer, domain.TypeText{Text: "a"}); err != nil {
		t.Fatal(err)
	}
	tc.audit.enabled.Store(true)
	if err := tc.do(ctlViewer, domain.TypeText{Text: "b"}); err != nil {
		t.Fatal(err)
	}
	tc.shooter.wait()
	if g := tc.grabs(); !equalInts(g, []int{1}) {
		t.Fatalf("grabs = %v, want one", g)
	}
}

func TestControlThumbnailGrabFailureKeepsTheOpResult(t *testing.T) {
	tc := newThumbCtl(t)
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	tc.cap.SetError("Grab", errors.New("gdi exploded"))
	res, err := tc.ctl.Do(context.Background(), ctlAlice, domain.TypeText{Text: "hi"})
	if err != nil || res != (Result{}) {
		t.Fatalf("Do = %+v, %v; the op must stay ok", res, err)
	}
	got := tc.finish()
	if len(got) != 1 || got[0].Entry.Result != auditOK || got[0].Thumb != nil {
		t.Fatalf("records = %+v, want the ok entry without a thumbnail", got)
	}
	if !strings.Contains(tc.logs.String(), "audit thumbnail capture failed") || !strings.Contains(tc.logs.String(), "gdi exploded") {
		t.Fatalf("logs = %s", tc.logs.String())
	}
}

func TestControlThumbnailMonitorsFailureKeepsTheOpResult(t *testing.T) {
	tc := newThumbCtl(t)
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	// 計画（座標の変換）のあと、撮影の時点でだけ Monitors が失敗する。KeyPress は計画で Monitors を呼ばない。
	tc.cap.SetError("Monitors", errors.New("no desktop"))
	if err := tc.do(ctlAlice, domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}}); err != nil {
		t.Fatal(err)
	}
	got := tc.finish()
	if len(got) != 1 || got[0].Thumb != nil || len(tc.grabs()) != 0 {
		t.Fatalf("records = %+v grabs = %v", got, tc.grabs())
	}
	if !strings.Contains(tc.logs.String(), "audit thumbnail capture failed") {
		t.Fatalf("logs = %s", tc.logs.String())
	}
}

func TestControlThumbnailIsAsynchronous(t *testing.T) {
	tc := newThumbCtl(t)
	tc.cap.gate = make(chan struct{})
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	done := make(chan error, 1)
	go func() { done <- tc.do(ctlAlice, domain.TypeText{Text: "hi"}) }()
	// Grab が止まったままでも Do は戻る。
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-tc.cap.started
	if got := tc.audit.records(); len(got) != 0 {
		t.Fatalf("the entry is recorded after the capture, got %+v", got)
	}
	close(tc.cap.gate)
	got := tc.finish()
	if len(got) != 1 || got[0].Thumb == nil || got[0].Entry.Event != "tool.type" {
		t.Fatalf("records = %+v", got)
	}
}

func TestControlThumbnailDropsWhenWorkersAreBusy(t *testing.T) {
	tc := newThumbCtl(t)
	tc.cap.gate = make(chan struct{})
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	for range thumbWorkers {
		if err := tc.do(ctlAlice, domain.TypeText{Text: "a"}); err != nil {
			t.Fatal(err)
		}
		<-tc.cap.started
	}
	// 満杯のあいだの操作は、撮らずに（同期で）エントリを記録する。
	if err := tc.do(ctlAlice, domain.TypeText{Text: "dropped"}); err != nil {
		t.Fatal(err)
	}
	got := tc.audit.records()
	if len(got) != 1 || got[0].Thumb != nil {
		t.Fatalf("records = %+v, want the dropped op's entry only", got)
	}
	if !strings.Contains(tc.logs.String(), "capture workers are busy") {
		t.Fatalf("logs = %s", tc.logs.String())
	}
	close(tc.cap.gate)
	if got := tc.finish(); len(got) != thumbWorkers+1 {
		t.Fatalf("records = %d, want %d", len(got), thumbWorkers+1)
	}
	if g := len(tc.grabs()); g != thumbWorkers {
		t.Fatalf("Grab calls = %d, want %d", g, thumbWorkers)
	}
	// 終われば枠は戻り、次は撮れる。
	tc.cap.gate = nil
	if err := tc.do(ctlAlice, domain.TypeText{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	last := tc.finish()
	if last[len(last)-1].Thumb == nil {
		t.Fatalf("workers must be freed after the captures finish: %+v", last[len(last)-1])
	}
}

// 本物の Audit では、サムネイルはその操作の行に付く（行 id は追記した行）。
func TestControlThumbnailAttachesToTheOpsAuditRow(t *testing.T) {
	tc := newThumbCtl(t)
	te := newThumbEnv(t, 0, 0)
	ctl, err := NewControl(ControlConfig{
		Capturer: tc.cap, Input: tc.input, Windows: tc.windows, Clipboard: tc.clipboard, UI: tc.ui,
		Launcher: tc.launcher, Apps: tc.apps, Leases: tc.leases, Kill: tc.kill, Settings: te.settings,
		Audit: te.audit, Clock: tc.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustAcquire(t, tc.ctlEnv, ctlAlice)
	if _, err := ctl.Do(context.Background(), ctlAlice, domain.TypeText{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	ctl.(*control).thumbs.wait()
	if err := te.audit.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := te.store.Entries()
	if len(entries) != 1 || entries[0].Event != "tool.type" {
		t.Fatalf("entries = %+v", entries)
	}
	if len(te.store.attached) != 1 || te.store.ids[0] != 1 {
		t.Fatalf("attached = %+v ids = %v, want the thumbnail on row 1", te.store.attached, te.store.ids)
	}
	if got, want := strings.Join(te.events.get(), ","), "append:tool.type,save,attach,purge"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

func TestControlWithoutThumbnailAuditNeverGrabs(t *testing.T) {
	e := newCtlEnv(t) // ctlAudit は ThumbnailsEnabled・RecordWithThumb を持たない
	mustAcquire(t, e, ctlAlice)
	if _, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if ev := e.audit.Events(); len(ev) != 2 || ev[1] != "tool.type" || len(e.capturer.CallsTo("Grab")) != 0 {
		t.Fatalf("entries = %v grabs = %d", ev, len(e.capturer.CallsTo("Grab")))
	}
}
