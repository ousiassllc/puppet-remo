package mcp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// uiaWindow は縮小座標への変換を確かめやすい、モニター 0 の左上のウィンドウ（物理 2560x1440 → 縮小 1568 幅。縮小率 0.6125）。
var uiaWindow = domain.WindowInfo{ID: "132456", Title: "メモ帳", Process: "notepad.exe", PID: 4120, Rect: domain.Rect{X: 40, Y: 30, W: 800, H: 600}, Monitor: 0, Foreground: true}

func sampleElement(id, name string, rect domain.Rect) domain.UIElementInfo {
	return domain.UIElementInfo{
		ID: id, ControlType: "Button", Name: name, AutomationID: "SaveButton", ClassName: "Button",
		Rect: rect, Enabled: true, Patterns: []string{domain.PatternInvoke, domain.PatternToggle},
	}
}

func TestFindUIElement(t *testing.T) {
	e := newEnv(t)
	e.ctl.windows = []domain.WindowInfo{uiaWindow}
	e.ctl.found = &domain.UIFindResult{
		Window:    uiaWindow,
		Truncated: true,
		Elements: []domain.UIElementInfo{
			sampleElement("uie:132456:4120:2a.1", "保存", domain.Rect{X: 100, Y: 100, W: 200, H: 100}),
			{ID: "uie:132456:4120:2a.2", ControlType: "Edit", Name: "本文", Rect: domain.Rect{X: 100, Y: 300, W: 200, H: 100}, Offscreen: true, Enabled: true},
			sampleElement("uie:132456:4120:2a.3", "空", domain.Rect{}),
		},
	}
	cs := e.connect("c1")

	res := callTool(t, cs, "find_ui_element", map[string]any{
		"window_id": "132456", "title": "メモ", "process_name": "notepad.exe",
		"name": "保存", "automation_id": "SaveButton", "control_type": "Button", "match": "contains", "max_results": 5,
	})
	if res.IsError {
		t.Fatal(res.text())
	}
	wantOp := domain.FindUIElement{
		Window:     domain.WindowLocator{WindowID: "132456", Title: "メモ", ProcessName: "notepad.exe", Match: domain.MatchContains},
		Element:    domain.ElementLocator{Name: "保存", AutomationID: "SaveButton", ControlType: "Button", Match: domain.MatchContains},
		MaxResults: 5,
	}
	if got := e.ctl.lastCall(t).Op; !reflect.DeepEqual(got, wantOp) {
		t.Errorf("op = %#v, want %#v", got, wantOp)
	}
	var got findUIElementBody
	res.decode(t, &got)
	if got.Window.ID != "132456" || got.Window.Process != "notepad.exe" || !got.Truncated || len(got.Elements) != 3 {
		t.Fatalf("body = %+v", got)
	}
	save := got.Elements[0]
	if save.ID != "uie:132456:4120:2a.1" || save.ControlType != "Button" || save.Name != "保存" || save.AutomationID != "SaveButton" || save.ClassName != "Button" ||
		!save.Enabled || save.Offscreen || !save.Visible || !reflect.DeepEqual(save.Patterns, []string{"Invoke", "Toggle"}) {
		t.Errorf("save = %+v", save)
	}
	if want := (rectBody{X: 61, Y: 61, Width: 123, Height: 62}); save.Rect != want {
		t.Errorf("rect = %+v, want %+v (scaled like get_ui_tree)", save.Rect, want)
	}
	if edit := got.Elements[1]; !edit.Offscreen || edit.Visible || edit.Patterns == nil {
		t.Errorf("offscreen edit = %+v (want not visible, patterns [] not null)", edit)
	}
	if empty := got.Elements[2]; empty.Visible {
		t.Errorf("an element with an empty rect is visible: %+v", empty)
	}
	if strings.Contains(res.text(), `"patterns":null`) {
		t.Errorf("patterns must be [] not null: %s", res.text())
	}
}

func TestFindUIElementErrors(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	e.ctl.err = domain.ErrStaleElement
	if got := callTool(t, cs, "find_ui_element", map[string]any{"name": "x"}).errCode(t); got != domain.CodeStaleElement {
		t.Errorf("code = %s, want stale_element", got)
	}
}

func TestGetUITreeCarriesLocatorFieldsAndElementIDs(t *testing.T) {
	e := newEnv(t)
	e.ctl.windows = []domain.WindowInfo{uiaWindow}
	e.ctl.tree = &domain.UINode{
		ID: "uie:132456:4120:2a.1", Role: "Window", Name: "メモ帳", AutomationID: "MainWindow", ClassName: "Notepad",
		Patterns: []string{domain.PatternWindow}, Rect: domain.Rect{X: 40, Y: 30, W: 800, H: 600}, Enabled: true,
		Children: []domain.UINode{
			{ID: "uie:132456:4120:2a.2", Role: "Edit", Name: "本文", ValueRedacted: true, Value: "must-not-appear", Offscreen: true, Enabled: true},
		},
	}
	cs := e.connect("c1")
	res := callTool(t, cs, "get_ui_tree", map[string]any{"window_id": "132456"})
	if res.IsError {
		t.Fatal(res.text())
	}
	var got uiTreeBody
	res.decode(t, &got)
	root := got.Root
	if root.ID != "uie:132456:4120:2a.1" || root.Role != "Window" || root.ControlType != "Window" || root.AutomationID != "MainWindow" ||
		root.ClassName != "Notepad" || !reflect.DeepEqual(root.Patterns, []string{"Window"}) {
		t.Errorf("root = %+v", root)
	}
	child := root.Children[0]
	if child.ID != "uie:132456:4120:2a.2" || child.ControlType != "Edit" || child.Patterns == nil || len(child.Patterns) != 0 || !child.Offscreen || !child.ValueRedacted || child.Value != "" {
		t.Errorf("child = %+v", child)
	}
	if strings.Contains(res.text(), "must-not-appear") {
		t.Errorf("redacted value leaked: %s", res.text())
	}
	// FindUIElement と同じ要素 ID の形式で、そのまま ParseElementRef に通る。
	if _, err := domain.ParseElementRef(root.ID); err != nil {
		t.Errorf("tree id %q does not parse: %v", root.ID, err)
	}
}

func TestWaitForMatchedWindowAndElement(t *testing.T) {
	e := newEnv(t)
	e.ctl.windows = []domain.WindowInfo{uiaWindow}
	cs := e.connect("c1")

	// window を待つ（element の項目なし）。
	win := uiaWindow
	e.ctl.waited = &service.WaitOutcome{Satisfied: true, Elapsed: 1500 * time.Millisecond, Window: &win}
	res := callTool(t, cs, "wait_for", map[string]any{"title": "メモ帳", "process_name": "notepad.exe", "state": "appear", "timeout_ms": 5000, "interval_ms": 250})
	if res.IsError {
		t.Fatal(res.text())
	}
	wantOp := domain.WaitFor{
		Window: domain.WindowLocator{Title: "メモ帳", ProcessName: "notepad.exe"}, State: domain.WaitAppear,
		Timeout: 5 * time.Second, Interval: 250 * time.Millisecond,
	}
	if got := e.ctl.lastCall(t).Op; !reflect.DeepEqual(got, wantOp) {
		t.Errorf("op = %#v, want %#v", got, wantOp)
	}
	var got waitForBody
	res.decode(t, &got)
	if !got.Matched || got.TimedOut || got.Target != "window" || got.State != "appear" || got.WaitedMs != 1500 || got.Candidates != nil || got.Elements != nil {
		t.Fatalf("body = %+v", got)
	}
	if got.Window == nil || got.Window.ID != "132456" || got.Window.Title != "メモ帳" || got.Window.Process != "notepad.exe" ||
		got.Window.Rect != (rectBody{X: 25, Y: 18, Width: 490, Height: 368}) {
		t.Errorf("window = %+v", got.Window)
	}

	// element を待つ。
	e.ctl.waited = &service.WaitOutcome{
		Satisfied: true, Window: &win,
		Elements: []domain.UIElementInfo{sampleElement("uie:132456:4120:2a.1", "保存", domain.Rect{X: 100, Y: 100, W: 200, H: 100})},
	}
	res = callTool(t, cs, "wait_for", map[string]any{"window_id": "132456", "name": "保存", "control_type": "Button", "state": "appear", "timeout_ms": 5000})
	if res.IsError {
		t.Fatal(res.text())
	}
	wantOp = domain.WaitFor{
		Window:  domain.WindowLocator{WindowID: "132456"},
		Element: &domain.ElementLocator{Name: "保存", ControlType: "Button"},
		State:   domain.WaitAppear, Timeout: 5 * time.Second,
	}
	if got := e.ctl.lastCall(t).Op; !reflect.DeepEqual(got, wantOp) {
		t.Errorf("op = %#v, want %#v", got, wantOp)
	}
	got = waitForBody{}
	res.decode(t, &got)
	if !got.Matched || got.Target != "element" || len(got.Elements) != 1 || got.Elements[0].ID != "uie:132456:4120:2a.1" ||
		got.Elements[0].Rect != (rectBody{X: 61, Y: 61, Width: 123, Height: 62}) || !reflect.DeepEqual(got.Elements[0].Patterns, []string{"Invoke", "Toggle"}) {
		t.Errorf("body = %+v", got)
	}
}

func TestWaitForTimeoutIsAResultWithCandidates(t *testing.T) {
	e := newEnv(t)
	e.ctl.windows = []domain.WindowInfo{uiaWindow}
	win := uiaWindow
	e.ctl.waited = &service.WaitOutcome{
		Satisfied: false, Elapsed: 5 * time.Second, Window: &win, Truncated: true,
		Windows:  []domain.WindowInfo{{ID: "7", Title: "別", Process: "other.exe", Rect: domain.Rect{W: 100, H: 100}}},
		Elements: []domain.UIElementInfo{sampleElement("uie:132456:4120:2a.9", "保存して閉じる", domain.Rect{X: 100, Y: 100, W: 200, H: 100})},
	}
	cs := e.connect("c1")
	res := callTool(t, cs, "wait_for", map[string]any{"window_id": "132456", "name": "保存", "state": "appear", "timeout_ms": 5000})
	if res.IsError {
		t.Fatalf("a timeout must not be a tool error: %s", res.text())
	}
	var got waitForBody
	res.decode(t, &got)
	if got.Matched || !got.TimedOut || got.WaitedMs != 5000 || !got.Truncated || got.Window == nil || got.Candidates == nil {
		t.Fatalf("body = %+v", got)
	}
	if len(got.Candidates.Windows) != 1 || got.Candidates.Windows[0].ID != "7" || len(got.Candidates.Elements) != 1 || got.Candidates.Elements[0].Name != "保存して閉じる" {
		t.Errorf("candidates = %+v", got.Candidates)
	}
	if !strings.Contains(res.text(), `"timed_out":true`) || !strings.Contains(res.text(), `"windows":[`) {
		t.Errorf("text = %s", res.text())
	}
}

func TestWaitForDisappearSuccessCarriesNoWindow(t *testing.T) {
	e := newEnv(t)
	e.ctl.waited = &service.WaitOutcome{Satisfied: true, Elapsed: time.Second}
	cs := e.connect("c1")
	res := callTool(t, cs, "wait_for", map[string]any{"title": "x", "state": "disappear", "timeout_ms": 1000})
	var got waitForBody
	res.decode(t, &got)
	if !got.Matched || got.State != "disappear" || got.Window != nil || got.Candidates != nil {
		t.Errorf("body = %+v", got)
	}
}

func TestUIAToolsPassInvalidArgumentsThrough(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	e.ctl.err = domain.ErrInvalidArgument
	for name, args := range map[string]map[string]any{
		"find_ui_element": {"name": "x"},
		"wait_for":        {"name": "x", "state": "appear", "timeout_ms": 1},
	} {
		if got := callTool(t, cs, name, args).errCode(t); got != domain.CodeInvalidArgument {
			t.Errorf("%s: code = %s, want invalid_argument", name, got)
		}
	}
}
