package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

func TestGetScreenInfo(t *testing.T) {
	e := newEnv(t)
	e.ctl.monitors = []domain.Monitor{
		{Index: 0, Name: "primary", Primary: true, Size: domain.Size{W: 2560, H: 1440}, DPI: 120},
		{Index: 1, Name: "side", Origin: domain.Point{X: -1280, Y: 0}, Size: domain.Size{W: 1280, H: 720}},
	}
	cs := e.connect("c1")

	res := callTool(t, cs, "get_screen_info", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	var got screenInfoBody
	res.decode(t, &got)
	want := screenInfoBody{Monitors: []monitorBody{
		{Index: 0, Name: "primary", Primary: true, Physical: sizeBody{2560, 1440}, DPIScale: 1.25, Scale: 0.6125, Scaled: sizeBody{1568, 882}},
		{Index: 1, Name: "side", Origin: pointBody{-1280, 0}, Physical: sizeBody{1280, 720}, DPIScale: 1, Scale: 1, Scaled: sizeBody{1280, 720}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("get_screen_info =\n %+v\nwant\n %+v", got, want)
	}
	if e.ctl.callCount() != 0 {
		t.Error("get_screen_info must not go through Control.Do (no lease)")
	}
	if _, held := e.leases.Holder(); held {
		t.Error("get_screen_info acquired the lease")
	}
}

func TestScreenshot(t *testing.T) {
	tests := []struct {
		name        string
		args        map[string]any
		wantMonitor int
		wantFormat  string
	}{
		{"defaults to the primary monitor and png", nil, domain.MonitorPrimary, ""},
		{"monitor and jpeg", map[string]any{"monitor": 1, "format": "jpeg"}, 1, "jpeg"},
		{"monitor 0 is not the primary default", map[string]any{"monitor": 0}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.shot = service.Shot{
				Data: []byte("image-bytes"), MIME: "image/jpeg", Monitor: 1,
				Physical: domain.Size{W: 2560, H: 1440}, Scaled: domain.Size{W: 1568, H: 882}, Scale: 0.6125,
			}
			cs := e.connect("c1")

			res := callTool(t, cs, "screenshot", tt.args)
			if res.IsError {
				t.Fatal(res.text())
			}
			if e.ctl.screenshotMonitor != tt.wantMonitor || e.ctl.screenshotFormat != tt.wantFormat {
				t.Errorf("Screenshot(%d, %q), want (%d, %q)", e.ctl.screenshotMonitor, e.ctl.screenshotFormat, tt.wantMonitor, tt.wantFormat)
			}
			if len(res.Content) != 2 {
				t.Fatalf("content = %d items, want an image and a text", len(res.Content))
			}
			img, ok := res.Content[0].(*mcpsdk.ImageContent)
			if !ok || string(img.Data) != "image-bytes" || img.MIMEType != "image/jpeg" {
				t.Errorf("image content = %#v", res.Content[0])
			}
			var meta screenshotBody
			res.decode(t, &meta)
			want := screenshotBody{Monitor: 1, Physical: sizeBody{2560, 1440}, Scaled: sizeBody{1568, 882}, Scale: 0.6125, OwnWindows: []ownWindowBody{}}
			if !reflect.DeepEqual(meta, want) {
				t.Errorf("meta = %+v, want %+v", meta, want)
			}
			if text := res.text(); text == "" {
				t.Error("no text content with the screenshot metadata")
			}
			if _, held := e.leases.Holder(); held {
				t.Error("screenshot acquired the lease")
			}
		})
	}
}

func TestScreenshotReportsOwnWindows(t *testing.T) {
	e := newEnv(t)
	e.ctl.shot = service.Shot{
		Data: []byte("image-bytes"), MIME: "image/png", Monitor: 1,
		Physical: domain.Size{W: 2560, H: 1440}, Scaled: domain.Size{W: 1568, H: 882}, Scale: 0.6125,
		OwnWindows: []domain.OwnRegion{{Monitor: 1, Rect: domain.Rect{X: 784, Y: 0, W: 393, H: 442}}},
	}
	cs := e.connect("c1")

	res := callTool(t, cs, "screenshot", map[string]any{"monitor": 1})
	if res.IsError {
		t.Fatal(res.text())
	}
	var meta screenshotBody
	res.decode(t, &meta)
	want := []ownWindowBody{{Monitor: 1, Rect: rectBody{X: 784, Y: 0, Width: 393, Height: 442}}}
	if !reflect.DeepEqual(meta.OwnWindows, want) {
		t.Errorf("own_windows = %+v, want %+v", meta.OwnWindows, want)
	}
	// JSON のキーは own_windows / monitor / rect{x,y,width,height}（縮小座標）。
	raw, err := json.Marshal(meta.OwnWindows)
	if err != nil {
		t.Fatal(err)
	}
	if got, wantJSON := string(raw), `[{"monitor":1,"rect":{"x":784,"y":0,"width":393,"height":442}}]`; got != wantJSON {
		t.Errorf("json = %s, want %s", got, wantJSON)
	}
}

func TestScreenshotRejectsNegativeMonitor(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	res := callTool(t, cs, "screenshot", map[string]any{"monitor": -3})
	if got := res.errCode(t); got != domain.CodeInvalidArgument {
		t.Errorf("code = %s, want invalid_argument", got)
	}
}

// window_id なしの screenshot は、ウィンドウ単位の撮影を呼ばず、出力にウィンドウ単位の項目（window_id・rect・windows・omitted）を出さない（従来どおり）。
func TestScreenshotWithoutWindowIDKeepsTheMonitorOutput(t *testing.T) {
	e := newEnv(t)
	e.ctl.shot = service.Shot{
		Data: []byte("image-bytes"), MIME: "image/png", Monitor: 0,
		Physical: domain.Size{W: 2560, H: 1440}, Scaled: domain.Size{W: 1568, H: 882}, Scale: 0.6125,
	}
	cs := e.connect("c1")
	res := callTool(t, cs, "screenshot", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	if e.ctl.windowShotCalls != 0 {
		t.Error("WindowScreenshot was called without window_id")
	}
	var raw map[string]any
	res.decode(t, &raw)
	for _, k := range []string{"window_id", "rect", "windows", "omitted"} {
		if _, ok := raw[k]; ok {
			t.Errorf("output has %q without window_id: %v", k, raw)
		}
	}
	for _, k := range []string{"monitor", "physical", "scaled", "scale", "own_windows"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("output lacks %q: %v", k, raw)
		}
	}
}

func TestScreenshotWithWindowID(t *testing.T) {
	e := newEnv(t)
	e.ctl.windowShot = service.WindowShot{
		Data: []byte("window-bytes"), MIME: "image/png", WindowID: "1001", Monitor: 0,
		Rect: domain.Rect{X: 49, Y: 39, W: 270, H: 157}, Physical: domain.Size{W: 550, H: 320}, Scaled: domain.Size{W: 270, H: 157}, Scale: 0.49,
		Members: []service.WindowShotMember{
			{WindowID: "1003", Relation: domain.RelationOwned, Rect: domain.Rect{X: 220, Y: 39, W: 98, H: 59}},
			{WindowID: "1001", Relation: domain.RelationTarget, Rect: domain.Rect{X: 49, Y: 49, W: 196, H: 147}},
		},
		Omitted:    []service.WindowShotOmitted{{WindowID: "1004", Reason: domain.ReasonRelatedCrossProcess}},
		OwnWindows: []domain.OwnRegion{{Monitor: 0, Rect: domain.Rect{X: 10, Y: 20, W: 30, H: 40}}},
	}
	cs := e.connect("c1")

	res := callTool(t, cs, "screenshot", map[string]any{"window_id": "1001", "include_owned": true, "format": "jpeg"})
	if res.IsError {
		t.Fatal(res.text())
	}
	if want := (service.WindowShotRequest{WindowID: "1001", Format: "jpeg", IncludeOwned: true}); e.ctl.windowShotReq != want {
		t.Errorf("WindowScreenshot(%+v), want %+v", e.ctl.windowShotReq, want)
	}
	if e.ctl.screenshotFormat != "" || e.ctl.screenshotMonitor != 0 {
		t.Error("Screenshot (whole monitor) was called with window_id")
	}
	img, ok := res.Content[0].(*mcpsdk.ImageContent)
	if !ok || string(img.Data) != "window-bytes" {
		t.Errorf("image content = %#v", res.Content[0])
	}
	var meta screenshotBody
	res.decode(t, &meta)
	rect := rectBody{X: 49, Y: 39, Width: 270, Height: 157}
	want := screenshotBody{
		Monitor: 0, Physical: sizeBody{550, 320}, Scaled: sizeBody{270, 157}, Scale: 0.49,
		OwnWindows: []ownWindowBody{{Monitor: 0, Rect: rectBody{X: 10, Y: 20, Width: 30, Height: 40}}},
		WindowID:   "1001", Rect: &rect,
		Windows: []shotWindowBody{
			{ID: "1003", Relation: "owned", Rect: rectBody{X: 220, Y: 39, Width: 98, Height: 59}},
			{ID: "1001", Relation: "target", Rect: rectBody{X: 49, Y: 49, Width: 196, Height: 147}},
		},
		Omitted: []shotOmittedBody{{ID: "1004", Reason: "related_cross_process"}},
	}
	if !reflect.DeepEqual(meta, want) {
		t.Errorf("meta =\n %+v\nwant\n %+v", meta, want)
	}
	if _, held := e.leases.Holder(); held {
		t.Error("screenshot acquired the lease")
	}
}

func TestScreenshotWithWindowIDRejectsBadCombinations(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"monitor と window_id", map[string]any{"window_id": "1001", "monitor": 0}},
		{"window_id なしの include_owned", map[string]any{"include_owned": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			cs := e.connect("c1")
			res := callTool(t, cs, "screenshot", tt.args)
			if got := res.errCode(t); got != domain.CodeInvalidArgument {
				t.Errorf("code = %s, want invalid_argument", got)
			}
			if e.ctl.windowShotCalls != 0 {
				t.Error("WindowScreenshot was called")
			}
		})
	}
}

// Control の拒否（window_not_found・input_blocked own_window・capture_unavailable）は、そのままのコードでツールの失敗になる。
func TestScreenshotWithWindowIDPropagatesControlErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want domain.Code
	}{
		{"存在しない id", domain.ErrWindowNotFound, domain.CodeWindowNotFound},
		{"自ウィンドウ", domain.ErrInputBlocked.WithDetail("reason", domain.ReasonOwnWindow), domain.CodeInputBlocked},
		{"撮れない", domain.ErrInputBlocked.WithDetail("reason", string(domain.ReasonCaptureUnavailable)), domain.CodeInputBlocked},
		{"キルスイッチ", domain.ErrKillSwitchActive, domain.CodeKillSwitchActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.err = tt.err
			cs := e.connect("c1")
			res := callTool(t, cs, "screenshot", map[string]any{"window_id": "9"})
			if got := res.errCode(t); got != tt.want {
				t.Errorf("code = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCursorPosition(t *testing.T) {
	e := newEnv(t)
	e.ctl.cursor = service.Cursor{Point: domain.Point{X: 640, Y: 360}, Monitor: 1}
	cs := e.connect("c1")

	res := callTool(t, cs, "cursor_position", nil)
	var got cursorBody
	res.decode(t, &got)
	if want := (cursorBody{X: 640, Y: 360, Monitor: 1}); got != want {
		t.Errorf("cursor_position = %+v, want %+v", got, want)
	}
}

// 入力系ツールは引数を domain.Op に変換して Control.Do に渡す。座標は縮小座標のまま渡す（FR-M16）。
func TestInputToolsBuildOps(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
		want domain.Op
	}{
		{"mouse_move", map[string]any{"x": 10, "y": 20},
			domain.MouseMove{At: domain.Point{X: 10, Y: 20}, Monitor: domain.MonitorPrimary}},
		{"mouse_move", map[string]any{"x": 0, "y": 0, "monitor": 1},
			domain.MouseMove{Monitor: 1}},
		{"left_click", map[string]any{"x": 100, "y": 200},
			domain.Click{At: &domain.Point{X: 100, Y: 200}, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}},
		{"left_click", map[string]any{},
			domain.Click{Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}},
		{"left_click", map[string]any{"x": 5, "y": 6, "modifiers": []string{"Ctrl", "shift"}, "monitor": 2},
			domain.Click{At: &domain.Point{X: 5, Y: 6}, Button: domain.ButtonLeft, Count: 1, Mods: []domain.Modifier{domain.ModCtrl, domain.ModShift}, Monitor: 2}},
		{"right_click", map[string]any{"x": 1, "y": 2},
			domain.Click{At: &domain.Point{X: 1, Y: 2}, Button: domain.ButtonRight, Count: 1, Monitor: domain.MonitorPrimary}},
		{"middle_click", map[string]any{"x": 1, "y": 2},
			domain.Click{At: &domain.Point{X: 1, Y: 2}, Button: domain.ButtonMiddle, Count: 1, Monitor: domain.MonitorPrimary}},
		{"double_click", map[string]any{"x": 1, "y": 2},
			domain.Click{At: &domain.Point{X: 1, Y: 2}, Button: domain.ButtonLeft, Count: 2, Monitor: domain.MonitorPrimary}},
		{"triple_click", map[string]any{"x": 1, "y": 2, "modifiers": []string{"alt"}},
			domain.Click{At: &domain.Point{X: 1, Y: 2}, Button: domain.ButtonLeft, Count: 3, Mods: []domain.Modifier{domain.ModAlt}, Monitor: domain.MonitorPrimary}},
		{"left_click_drag", map[string]any{"from": map[string]any{"x": 1, "y": 2}, "to": map[string]any{"x": 30, "y": 40}, "modifiers": []string{"win"}},
			domain.Drag{From: domain.Point{X: 1, Y: 2}, To: domain.Point{X: 30, Y: 40}, Mods: []domain.Modifier{domain.ModWin}, Monitor: domain.MonitorPrimary}},
		{"scroll", map[string]any{"x": 7, "y": 8, "direction": "down", "amount": 3},
			domain.Scroll{At: domain.Point{X: 7, Y: 8}, Dir: domain.DirDown, Amount: 3, Monitor: domain.MonitorPrimary}},
		{"scroll", map[string]any{"x": 7, "y": 8, "direction": "LEFT", "amount": 50, "monitor": 1},
			domain.Scroll{At: domain.Point{X: 7, Y: 8}, Dir: domain.DirLeft, Amount: 50, Monitor: 1}},
		{"key", map[string]any{"keys": "Ctrl+Shift+T"},
			domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl, domain.ModShift}, Key: "t"}}},
		{"key", map[string]any{"keys": "enter"},
			domain.KeyPress{Keys: domain.KeyCombo{Key: "enter"}}},
		{"type", map[string]any{"text": "こんにちは, world"},
			domain.TypeText{Text: "こんにちは, world"}},
		{"type", map[string]any{"text": ""},
			domain.TypeText{}},
		{"wait", map[string]any{"ms": 0}, domain.Wait{}},
		{"wait", map[string]any{"ms": 10000}, domain.Wait{Duration: 10 * time.Second}},
		{"focus_window", map[string]any{"id": "132456"}, domain.FocusWindow{ID: "132456"}},
		{"clipboard_write", map[string]any{"text": "copied"}, domain.ClipboardWrite{Text: "copied"}},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			e := newEnv(t)
			cs := e.connect("c1")

			res := callTool(t, cs, tt.tool, tt.args)
			if res.IsError {
				t.Fatalf("%s failed: %s", tt.tool, res.text())
			}
			var ok okBody
			res.decode(t, &ok)
			if !ok.OK {
				t.Errorf("structuredContent = %v, want {ok: true}", res.StructuredContent)
			}
			got := e.ctl.lastCall(t)
			if !reflect.DeepEqual(got.Op, tt.want) {
				t.Errorf("op = %#v, want %#v", got.Op, tt.want)
			}
			wantWho := domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "client-c1", SessionID: cs.ID()}
			if got.Who != wantWho {
				t.Errorf("actor = %+v, want %+v", got.Who, wantWho)
			}
		})
	}
}

func TestInputToolsRejectBadArguments(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args map[string]any
		want domain.Code
	}{
		{"x without y", "left_click", map[string]any{"x": 1}, domain.CodeInvalidArgument},
		{"y without x", "double_click", map[string]any{"y": 1}, domain.CodeInvalidArgument},
		{"negative monitor", "mouse_move", map[string]any{"x": 1, "y": 1, "monitor": -1}, domain.CodeInvalidArgument},
		{"unknown key token", "key", map[string]any{"keys": "ctrl+nosuchkey"}, domain.CodeInvalidArgument},
		{"empty keys", "key", map[string]any{"keys": ""}, domain.CodeInvalidArgument},
		{"wait too long", "wait", map[string]any{"ms": 10001}, domain.CodeInvalidArgument},
		{"wait negative", "wait", map[string]any{"ms": -1}, domain.CodeInvalidArgument},
		{"wait overflows a Duration", "wait", map[string]any{"ms": 9_223_372_036_854}, domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			cs := e.connect("c1")
			res := callTool(t, cs, tt.tool, tt.args)
			if got := res.errCode(t); got != tt.want {
				t.Errorf("code = %s, want %s", got, tt.want)
			}
			if e.ctl.callCount() != 0 {
				t.Error("Control.Do was called for an invalid argument")
			}
		})
	}
}

// 引数の範囲・文字数の検査は Control が行う。その失敗は、そのままツールのエラーコードになる。
func TestControlValidationErrorsPassThrough(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
		err  error
		want domain.Code
	}{
		{"mouse_move", map[string]any{"x": 99999, "y": 1}, domain.ErrInvalidCoordinates, domain.CodeInvalidCoordinates},
		{"scroll", map[string]any{"x": 1, "y": 1, "direction": "sideways", "amount": 1}, domain.ErrInvalidArgument, domain.CodeInvalidArgument},
		{"scroll", map[string]any{"x": 1, "y": 1, "direction": "up", "amount": 51}, domain.ErrInvalidArgument, domain.CodeInvalidArgument},
		{"key", map[string]any{"keys": "ctrl+alt+delete"}, domain.ErrInputBlocked, domain.CodeInputBlocked},
		{"type", map[string]any{"text": "x"}, domain.ErrInputBlocked, domain.CodeInputBlocked},
		{"focus_window", map[string]any{"id": "999"}, domain.ErrWindowNotFound, domain.CodeWindowNotFound},
		{"clipboard_read", nil, domain.ErrDesktopUnavailable, domain.CodeDesktopUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.tool+"/"+string(tt.want), func(t *testing.T) {
			e := newEnv(t)
			e.ctl.err = tt.err
			cs := e.connect("c1")
			res := callTool(t, cs, tt.tool, tt.args)
			if got := res.errCode(t); got != tt.want {
				t.Errorf("code = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMissingRequiredArgumentIsRejectedBySchema(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	res := callTool(t, cs, "mouse_move", map[string]any{"x": 1})
	if !res.IsError {
		t.Fatal("a call without a required argument succeeded")
	}
	if e.ctl.callCount() != 0 {
		t.Error("Control.Do was called despite the schema violation")
	}
}

func TestClipboardRead(t *testing.T) {
	e := newEnv(t)
	e.ctl.clipboard = "クリップボード"
	cs := e.connect("c1")

	res := callTool(t, cs, "clipboard_read", nil)
	var got clipboardBody
	res.decode(t, &got)
	if got.Text != "クリップボード" {
		t.Errorf("text = %q", got.Text)
	}
	if _, ok := e.ctl.lastCall(t).Op.(domain.ClipboardRead); !ok {
		t.Errorf("op = %#v", e.ctl.lastCall(t).Op)
	}
}

func TestListWindows(t *testing.T) {
	e := newEnv(t)
	e.ctl.monitors = []domain.Monitor{
		{Index: 0, Primary: true, Size: domain.Size{W: 2560, H: 1440}},
		{Index: 1, Origin: domain.Point{X: 2560, Y: 0}, Size: domain.Size{W: 1280, H: 720}},
	}
	e.ctl.windows = []domain.WindowInfo{
		{ID: "132456", Title: "メモ帳", Process: "notepad.exe", PID: 4120, Rect: domain.Rect{X: 40, Y: 30, W: 800, H: 600}, Monitor: 0, Foreground: true},
		{ID: "7", Title: "管理者", Process: "admin.exe", PID: 8, Rect: domain.Rect{X: 2560, Y: 0, W: 640, H: 360}, Monitor: 1, Elevated: true},
		{ID: "8", Title: "最小化", Process: "min.exe", PID: 9, Rect: domain.Rect{X: -32000, Y: -32000, W: 160, H: 28}, Monitor: 0, Minimized: true},
		{ID: "9", Title: "puppet-remo", Process: "puppet-remo.exe", PID: 10, Rect: domain.Rect{X: 1600, Y: 0, W: 800, H: 900}, Monitor: 0, Own: true},
	}
	cs := e.connect("c1")

	res := callTool(t, cs, "list_windows", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	var got windowsBody
	res.decode(t, &got)
	want := windowsBody{Windows: []windowBody{
		{ID: "132456", Title: "メモ帳", Process: "notepad.exe", PID: 4120, Rect: rectBody{X: 25, Y: 18, Width: 490, Height: 368}, Monitor: 0, Foreground: true},
		{ID: "7", Title: "管理者", Process: "admin.exe", PID: 8, Rect: rectBody{X: 0, Y: 0, Width: 640, Height: 360}, Monitor: 1, Elevated: true, Blocked: true, BlockedReason: "elevated_window"},
		{ID: "8", Title: "最小化", Process: "min.exe", PID: 9, Monitor: 0, Minimized: true},
		{ID: "9", Title: "puppet-remo", Process: "puppet-remo.exe", PID: 10, Rect: rectBody{X: 980, Y: 0, Width: 490, Height: 552}, Monitor: 0, Blocked: true, BlockedReason: "own_window"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list_windows =\n %+v\nwant\n %+v", got, want)
	}
}

func TestListWindowsEmptyIsAnArray(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	res := callTool(t, cs, "list_windows", nil)
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent = %#v", res.StructuredContent)
	}
	if ws, ok := m["windows"].([]any); !ok || len(ws) != 0 {
		t.Errorf("windows = %#v, want an empty array", m["windows"])
	}
}

func TestRectConversion(t *testing.T) {
	// 2560x1440 を最大辺 1568 に縮小（scale 0.6125）。2 台目は右隣の 1280x720（縮小なし）。
	c := converter{
		maxDim: 1568,
		monitors: []domain.Monitor{
			{Index: 0, Size: domain.Size{W: 2560, H: 1440}},
			{Index: 1, Origin: domain.Point{X: 2560, Y: 0}, Size: domain.Size{W: 1280, H: 720}},
		},
	}
	tests := []struct {
		name    string
		monitor int
		rect    domain.Rect
		want    rectBody
	}{
		{"whole monitor", 0, domain.Rect{W: 2560, H: 1440}, rectBody{0, 0, 1568, 882}},
		{"inside, scaled", 0, domain.Rect{X: 40, Y: 30, W: 800, H: 600}, rectBody{25, 18, 490, 368}},
		{"unscaled monitor with origin", 1, domain.Rect{X: 2660, Y: 10, W: 100, H: 50}, rectBody{100, 10, 100, 50}},
		{"overhangs the left and top edge", 0, domain.Rect{X: -100, Y: -100, W: 500, H: 500}, rectBody{0, 0, 245, 245}},
		{"overhangs the right and bottom edge", 0, domain.Rect{X: 2460, Y: 1340, W: 500, H: 500}, rectBody{1507, 821, 61, 61}},
		{"larger than the monitor", 1, domain.Rect{X: 2000, Y: -50, W: 5000, H: 5000}, rectBody{0, 0, 1280, 720}},
		{"entirely outside", 0, domain.Rect{X: -32000, Y: -32000, W: 160, H: 28}, rectBody{}},
		{"empty", 0, domain.Rect{X: 10, Y: 10}, rectBody{}},
		{"unknown monitor", 9, domain.Rect{W: 10, H: 10}, rectBody{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.rect(tt.monitor, tt.rect); got != tt.want {
				t.Errorf("rect = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func sampleTree() *domain.UINode {
	return &domain.UINode{
		ID: "root", Role: "Window", Name: "メモ帳", Rect: domain.Rect{X: 40, Y: 30, W: 800, H: 600}, Enabled: true,
		Children: []domain.UINode{
			{ID: "edit", Role: "Edit", Name: "本文", Value: "hello", Rect: domain.Rect{X: 50, Y: 100, W: 780, H: 500}, Enabled: true},
			{ID: "pw", Role: "Edit", Name: "パスワード", ValueRedacted: true, Value: "must-not-appear", Rect: domain.Rect{X: 50, Y: 60, W: 200, H: 30}, Enabled: true,
				Children: []domain.UINode{{ID: "deep", Role: "Text", Name: "deep", Truncated: true, Enabled: false}}},
		},
	}
}

func TestGetUITree(t *testing.T) {
	tests := []struct {
		name       string
		args       map[string]any
		wantOp     domain.GetUITree
		wantWindow string
	}{
		{"defaults to the foreground window", nil, domain.GetUITree{}, "132456"},
		{"explicit window and limits", map[string]any{"window_id": "132456", "max_depth": 2, "max_nodes": 10},
			domain.GetUITree{WindowID: "132456", MaxDepth: 2, MaxNodes: 10}, "132456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.tree = sampleTree()
			e.ctl.windows = []domain.WindowInfo{
				{ID: "1", Rect: domain.Rect{W: 10, H: 10}, Monitor: 0},
				{ID: "132456", Rect: domain.Rect{X: 40, Y: 30, W: 800, H: 600}, Monitor: 0, Foreground: true},
			}
			cs := e.connect("c1")

			res := callTool(t, cs, "get_ui_tree", tt.args)
			if res.IsError {
				t.Fatal(res.text())
			}
			if got := e.ctl.lastCall(t).Op; !reflect.DeepEqual(got, tt.wantOp) {
				t.Errorf("op = %#v, want %#v", got, tt.wantOp)
			}
			var got uiTreeBody
			res.decode(t, &got)
			if got.WindowID != tt.wantWindow {
				t.Errorf("window_id = %q, want %q", got.WindowID, tt.wantWindow)
			}
			if !got.Truncated {
				t.Error("truncated = false, want true (a descendant was cut off)")
			}
			if want := (rectBody{X: 25, Y: 18, Width: 490, Height: 368}); got.Root.Rect != want {
				t.Errorf("root rect = %+v, want %+v (scaled)", got.Root.Rect, want)
			}
			if len(got.Root.Children) != 2 {
				t.Fatalf("children = %d", len(got.Root.Children))
			}
			edit, pw := got.Root.Children[0], got.Root.Children[1]
			if edit.Value != "hello" || edit.ValueRedacted {
				t.Errorf("edit = %+v", edit)
			}
			if pw.Value != "" || !pw.ValueRedacted {
				t.Errorf("password field = %+v, want value withheld and value_redacted", pw)
			}
			if len(pw.Children) != 1 || pw.Children[0].ID != "deep" {
				t.Errorf("password children = %+v", pw.Children)
			}
		})
	}
}

func TestGetUITreeNotTruncatedAndRedactedValueNeverSerialised(t *testing.T) {
	e := newEnv(t)
	e.ctl.tree = sampleTree()
	e.ctl.tree.Children[1].Children = nil
	cs := e.connect("c1")

	res := callTool(t, cs, "get_ui_tree", nil)
	var got uiTreeBody
	res.decode(t, &got)
	if got.Truncated {
		t.Error("truncated = true for a complete tree")
	}
	if got.WindowID != "" {
		t.Errorf("window_id = %q, want empty when the foreground window is unknown", got.WindowID)
	}
	if text := res.text(); strings.Contains(text, "must-not-appear") {
		t.Errorf("redacted value leaked: %s", text)
	}
}

func TestGetUITreeWithoutTreeIsWindowNotFound(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	res := callTool(t, cs, "get_ui_tree", map[string]any{"window_id": "5"})
	if got := res.errCode(t); got != domain.CodeWindowNotFound {
		t.Errorf("code = %s, want window_not_found", got)
	}
}
