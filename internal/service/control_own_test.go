package service

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// ctlSecondMonitor は縮小される 2 台目のモニター（右隣。3136x1764 → 縮小率 0.5）。
var ctlSecondMonitor = domain.Monitor{Index: 1, Name: "m1", Origin: domain.Point{X: 3200, Y: 0}, Size: domain.Size{W: 3136, H: 1764}, DPI: 96}

// withTwoMonitors は e の Control を、ctlMonitor（縮小率 0.49）と ctlSecondMonitor（縮小率 0.5）の 2 台構成で作り直す。
func withTwoMonitors(t *testing.T, e *ctlEnv) {
	t.Helper()
	e.capturer = platformtest.NewFakeCapturer(ctlMonitor, ctlSecondMonitor)
	var err error
	e.control, err = NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ownWindowsOf は err が own_window の input_blocked であることを確かめ、Detail の own_windows を返す。
func ownWindowsOf(t *testing.T, err error) []domain.OwnRegion {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked || de.Detail["reason"] != domain.ReasonOwnWindow {
		t.Fatalf("want input_blocked/own_window, got %v", err)
	}
	raw, ok := de.Detail[DetailOwnWindows]
	if !ok {
		return nil
	}
	regions, ok := raw.([]domain.OwnRegion)
	if !ok {
		t.Fatalf("own_windows = %#v", raw)
	}
	return regions
}

func TestControlDoOwnWindowBlockedReportsScaledRect(t *testing.T) {
	// 既定の自ウィンドウは物理 (1600,0) 800x900。縮小率 0.49 → 左上 (784,0)、右下の最後の px (2399,899) → (1176,441)。
	e := newCtlEnv(t)
	_, err := e.control.Do(context.Background(), ctlAlice, domain.MouseMove{At: domain.Point{X: 980, Y: 100}, Monitor: 0})
	want := []domain.OwnRegion{{Monitor: 0, Rect: domain.Rect{X: 784, Y: 0, W: 393, H: 442}}}
	if got := ownWindowsOf(t, err); !equalRegions(got, want) {
		t.Fatalf("own_windows = %+v, want %+v", got, want)
	}
}

func TestControlDoOwnWindowOnNonPrimaryScaledMonitor(t *testing.T) {
	e := newCtlEnv(t)
	withTwoMonitors(t, e)
	// 2 台目の物理 (3600,200) 600x400 は、モニターの原点 (3200,0) を引いて縮小率 0.5 → 左上 (200,100)。
	// 右下は最後の px (999,599) を縮小して +1（list_windows の rect と同じ丸め。converter の規則）するので幅・高さは 301x201。
	e.windows.SetWindows(
		domain.WindowInfo{ID: "2", Own: true, Monitor: 1, Rect: domain.Rect{X: 3600, Y: 200, W: 600, H: 400}},
		domain.WindowInfo{ID: "1", Monitor: 0, Rect: domain.Rect{X: 0, Y: 0, W: 3200, H: 1800}},
	)
	// 縮小座標 (250,150) → 物理 (3700,300)。自ウィンドウの中。
	_, err := e.control.Do(context.Background(), ctlAlice, domain.Click{
		At: &domain.Point{X: 250, Y: 150}, Button: domain.ButtonLeft, Count: 1, Monitor: 1,
	})
	want := []domain.OwnRegion{{Monitor: 1, Rect: domain.Rect{X: 200, Y: 100, W: 301, H: 201}}}
	if got := ownWindowsOf(t, err); !equalRegions(got, want) {
		t.Fatalf("own_windows = %+v, want %+v", got, want)
	}
	if hasInputCall(e, "Click") {
		t.Fatal("blocked click must not reach the injector")
	}
}

func TestControlDoOwnWindowSpanningMonitorsIsClippedPerMonitor(t *testing.T) {
	e := newCtlEnv(t)
	withTwoMonitors(t, e)
	// 物理 (3000,0) 600x400 は ctlMonitor に (3000..3200)、ctlSecondMonitor に (3200..3600) かかる。
	e.windows.SetWindows(domain.WindowInfo{ID: "2", Own: true, Foreground: true, Monitor: 0, Rect: domain.Rect{X: 3000, Y: 0, W: 600, H: 400}})
	_, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "x"})
	want := []domain.OwnRegion{
		{Monitor: 0, Rect: domain.Rect{X: 1470, Y: 0, W: 98, H: 197}}, // x: 3000*0.49=1470 〜 縮小画像の右端(1568)、y: 399*0.49=195.5→196 で +1
		{Monitor: 1, Rect: domain.Rect{X: 0, Y: 0, W: 201, H: 201}},
	}
	if got := ownWindowsOf(t, err); !equalRegions(got, want) {
		t.Fatalf("own_windows = %+v, want %+v", got, want)
	}
}

func TestControlDoOwnWindowUnmappableWindowsAreOmitted(t *testing.T) {
	e := newCtlEnv(t)
	e.windows.SetWindows(
		domain.WindowInfo{ID: "2", Own: true, Foreground: true, Rect: domain.Rect{X: -32000, Y: -32000, W: 160, H: 28}}, // どのモニターにも重ならない
		domain.WindowInfo{ID: "5", Own: true, Minimized: true, Rect: domain.Rect{X: 0, Y: 0, W: 400, H: 300}},
		domain.WindowInfo{ID: "1", Rect: domain.Rect{X: 0, Y: 0, W: 800, H: 600}}, // 他アプリは含めない
	)
	_, err := e.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "x"})
	got := ownWindowsOf(t, err)
	if got == nil || len(got) != 0 {
		t.Fatalf("own_windows = %#v, want an empty non-nil list", got)
	}
}

func TestControlDoOwnWindowGetUITreeAndFocusAlsoReportRects(t *testing.T) {
	for name, op := range map[string]domain.Op{
		"get_ui_tree":  domain.GetUITree{WindowID: "2"},
		"focus_window": domain.FocusWindow{ID: "2"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCtlEnv(t)
			_, err := e.control.Do(context.Background(), ctlAlice, op)
			if got := ownWindowsOf(t, err); len(got) != 1 || got[0].Monitor != 0 {
				t.Fatalf("own_windows = %+v", got)
			}
		})
	}
}

func TestControlDoOwnWindowRectsAreBestEffort(t *testing.T) {
	e := newCtlEnv(t)
	e.windows.SetError("List", errors.New("boom"))
	_, err := e.control.Do(context.Background(), ctlAlice, domain.MouseMove{At: domain.Point{X: 980, Y: 100}, Monitor: 0})
	if got := ownWindowsOf(t, err); got != nil {
		t.Fatalf("own_windows = %+v, want none when the window list cannot be read", got)
	}
}

func TestControlDoElevatedBlockHasNoOwnWindows(t *testing.T) {
	e := newCtlEnv(t)
	// 昇格ウィンドウ（物理 2400..3200）。縮小 (1300,100) → 物理 2653。
	_, err := e.control.Do(context.Background(), ctlAlice, domain.MouseMove{At: domain.Point{X: 1300, Y: 100}, Monitor: 0})
	var de *domain.Error
	if !errors.As(err, &de) || de.Detail["reason"] != domain.ReasonElevatedWindow {
		t.Fatalf("err = %v", err)
	}
	if _, ok := de.Detail[DetailOwnWindows]; ok {
		t.Fatalf("detail = %+v", de.Detail)
	}
}

func TestControlScreenshotListsOwnWindowsOfThatMonitor(t *testing.T) {
	e := newCtlEnv(t)
	withTwoMonitors(t, e)
	e.capturer.SetImage(0, image.NewRGBA(image.Rect(0, 0, 64, 36)))
	e.capturer.SetImage(1, image.NewRGBA(image.Rect(0, 0, 64, 36)))
	e.windows.SetWindows(
		domain.WindowInfo{ID: "2", Own: true, Monitor: 1, Rect: domain.Rect{X: 3600, Y: 200, W: 600, H: 400}},
		domain.WindowInfo{ID: "1", Rect: domain.Rect{X: 0, Y: 0, W: 800, H: 600}},
	)
	ctx := context.Background()
	second, err := e.control.Screenshot(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []domain.OwnRegion{{Monitor: 1, Rect: domain.Rect{X: 200, Y: 100, W: 301, H: 201}}}; !equalRegions(second.OwnWindows, want) {
		t.Fatalf("monitor 1 own windows = %+v, want %+v", second.OwnWindows, want)
	}
	primary, err := e.control.Screenshot(ctx, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(primary.OwnWindows) != 0 {
		t.Fatalf("monitor 0 own windows = %+v, want none", primary.OwnWindows)
	}

	// 一覧が読めなくても、スクリーンショットは返る。
	e.windows.SetError("List", errors.New("boom"))
	if shot, err := e.control.Screenshot(ctx, 1, ""); err != nil || len(shot.OwnWindows) != 0 {
		t.Fatalf("shot = %+v, err = %v", shot, err)
	}
}

func equalRegions(a, b []domain.OwnRegion) bool {
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

// 承認ダイアログ（専用の Wails ウィンドウ。FR-H05）は自プロセスのウィンドウで、最前面の中央に出る。その座標への MCP・Web の入力は
// own_window の input_blocked で拒否され、注入器に届かない（NFR-S06。AI は承認ダイアログを押せない）。
func TestControlDoBlocksInputToTheApprovalDialog(t *testing.T) {
	// 3200x1800 の中央に 440x340（物理 (1380,730)〜(1820,1070)）。縮小率 0.49 では左上 (676,357)。ダイアログの中の「許可」ボタンあたり。
	dialog := domain.WindowInfo{ID: "9", Title: "puppet-remo 承認", Own: true, Foreground: true, Rect: domain.Rect{X: 1380, Y: 730, W: 440, H: 340}}
	normal := domain.WindowInfo{ID: "1", Title: "normal", Rect: domain.Rect{X: 0, Y: 0, W: 3200, H: 1800}}
	inside := domain.Point{X: 750, Y: 450} // 縮小座標 → 物理 (1531,918)

	t.Run("mcp left_click", func(t *testing.T) {
		e := newCtlEnv(t)
		e.windows.SetWindows(dialog, normal)
		_, err := e.control.Do(context.Background(), ctlAlice, domain.Click{At: &inside, Button: domain.ButtonLeft, Count: 1, Monitor: 0})
		regions := ownWindowsOf(t, err)
		if len(regions) != 1 || regions[0].Rect.W == 0 {
			t.Errorf("own_windows = %+v, want the dialog's rect", regions)
		}
		if hasInputCall(e, "Click") || hasInputCall(e, "MoveTo") {
			t.Error("a click on the approval dialog reached the injector")
		}
	})
	t.Run("web pointer", func(t *testing.T) {
		e := newWebEnv(t)
		e.windows.SetWindows(dialog, normal)
		stream := domain.Point{X: 1531, Y: 918} // ストリームの座標（物理ピクセル）
		for _, op := range []domain.Op{
			domain.PointerMove{At: stream, Monitor: 0},
			domain.MouseButton{At: stream, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: 0},
		} {
			_, err := e.control.Do(context.Background(), ctlViewer, op)
			ownWindowsOf(t, err)
		}
		if hasInputCall(e, "MoveTo") || hasInputCall(e, "Button") {
			t.Error("a pointer operation on the approval dialog reached the injector")
		}
	})
	t.Run("outside the dialog is not blocked", func(t *testing.T) {
		e := newCtlEnv(t)
		e.windows.SetWindows(dialog, normal)
		if _, err := e.control.Do(context.Background(), ctlAlice, domain.Click{At: &domain.Point{X: 50, Y: 50}, Button: domain.ButtonLeft, Count: 1, Monitor: 0}); err != nil {
			t.Errorf("click outside the dialog: %v", err)
		}
	})
}
