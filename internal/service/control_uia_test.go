package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// uiaID はウィンドウ hwnd（ctlDefaultWindows は PID 0）の RuntimeId (42, n) の要素 ID。
func uiaID(hwnd uint64, n int32) string {
	return domain.ElementRef{HWND: hwnd, PID: 0, RuntimeID: []int32{42, n}}.String()
}

// setNormalTree はウィンドウ "1" に、保存ボタン・名前を付ける編集欄・別のボタンを持つ木を置く。
func setNormalTree(e *ctlEnv) {
	e.ui.SetTree("1", domain.UINode{
		ID: uiaID(1, 1), Role: "Window", Name: "normal", Enabled: true,
		Children: []domain.UINode{
			{ID: uiaID(1, 2), Role: "Button", Name: "Save", AutomationID: "SaveButton", Enabled: true, Patterns: []string{domain.PatternInvoke}},
			{ID: uiaID(1, 3), Role: "Button", Name: "Save As", AutomationID: "SaveAsButton", Enabled: true, Patterns: []string{domain.PatternInvoke}},
			{ID: uiaID(1, 4), Role: "Edit", Name: "Save name", AutomationID: "NameBox", Enabled: true, Patterns: []string{domain.PatternValue}},
		},
	})
}

func findNames(res Result) []string {
	var out []string
	for _, el := range res.Found.Elements {
		out = append(out, el.Name)
	}
	return out
}

func TestControlFindUIElementLocators(t *testing.T) {
	tests := []struct {
		name string
		op   domain.FindUIElement
		want []string
	}{
		{"name exact", domain.FindUIElement{Element: domain.ElementLocator{Name: "Save"}}, []string{"Save"}},
		{"name contains ignores case", domain.FindUIElement{Element: domain.ElementLocator{Name: "SAVE", Match: domain.MatchContains}}, []string{"Save", "Save As", "Save name"}},
		{"automation id", domain.FindUIElement{Element: domain.ElementLocator{AutomationID: "SaveAsButton"}}, []string{"Save As"}},
		{"control type", domain.FindUIElement{Element: domain.ElementLocator{ControlType: "edit"}}, []string{"Save name"}},
		{"name and control type", domain.FindUIElement{Element: domain.ElementLocator{Name: "Save", Match: domain.MatchContains, ControlType: "Button"}}, []string{"Save", "Save As"}},
		{"all three", domain.FindUIElement{Element: domain.ElementLocator{Name: "Save", ControlType: "Button", AutomationID: "SaveButton"}}, []string{"Save"}},
		{"no match", domain.FindUIElement{Element: domain.ElementLocator{Name: "Save", ControlType: "Edit"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			setNormalTree(e)
			tt.op.Window = domain.WindowLocator{WindowID: "1"}
			res, err := e.control.Do(context.Background(), ctlAlice, tt.op)
			if err != nil || res.Found == nil {
				t.Fatalf("Do = %+v, %v", res, err)
			}
			got := findNames(res)
			if len(got) != len(tt.want) {
				t.Fatalf("names = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("names = %v, want %v", got, tt.want)
				}
			}
			if res.Found.Window.ID != "1" {
				t.Errorf("window = %q, want 1", res.Found.Window.ID)
			}
		})
	}
}

func TestControlFindUIElementReturnsIDsAndDetails(t *testing.T) {
	e := newCtlEnv(t)
	setNormalTree(e)
	res, err := e.control.Do(context.Background(), ctlAlice, domain.FindUIElement{
		Window: domain.WindowLocator{WindowID: "1"}, Element: domain.ElementLocator{AutomationID: "NameBox"},
	})
	if err != nil || len(res.Found.Elements) != 1 {
		t.Fatalf("Do = %+v, %v", res, err)
	}
	el := res.Found.Elements[0]
	if el.ID != uiaID(1, 4) || el.ControlType != "Edit" || len(el.Patterns) != 1 || el.Patterns[0] != "Value" || !el.Enabled {
		t.Errorf("element = %+v", el)
	}
	// 返した ID は platform で引き直せる（#268 が使う）。
	ref, err := domain.ParseElementRef(el.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ui.Resolve(context.Background(), ref); err != nil {
		t.Errorf("Resolve: %v", err)
	}
}

func TestControlFindUIElementWindowSelection(t *testing.T) {
	e := newCtlEnv(t)
	e.windows.SetWindows(
		domain.WindowInfo{ID: "10", Title: "Doc - Notepad", Process: "notepad.exe", Foreground: true},
		domain.WindowInfo{ID: "20", Title: "Other - Notepad", Process: "notepad.exe"},
		domain.WindowInfo{ID: "30", Title: "puppet-remo", Process: "puppet-remo.exe", Own: true},
	)
	find := func(loc domain.WindowLocator) (string, error) {
		res, err := e.control.Do(context.Background(), ctlAlice, domain.FindUIElement{Window: loc, Element: domain.ElementLocator{ControlType: "Window"}})
		if err != nil {
			return "", err
		}
		return res.Found.Window.ID, nil
	}
	cases := []struct {
		name string
		loc  domain.WindowLocator
		want string
	}{
		{"foreground by default", domain.WindowLocator{}, "10"},
		{"title exact", domain.WindowLocator{Title: "Other - Notepad"}, "20"},
		{"title contains picks the topmost", domain.WindowLocator{Title: "notepad", Match: domain.MatchContains}, "10"},
		{"process", domain.WindowLocator{ProcessName: "NOTEPAD.EXE"}, "10"},
		{"id wins", domain.WindowLocator{WindowID: "20"}, "20"},
		{"id and title must both match", domain.WindowLocator{WindowID: "20", Title: "Other", Match: domain.MatchContains}, "20"},
	}
	for _, c := range cases {
		if got, err := find(c.loc); err != nil || got != c.want {
			t.Errorf("%s: window = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for name, loc := range map[string]domain.WindowLocator{
		"no such title":                          {Title: "nothing"},
		"id and title disagree":                  {WindowID: "20", Title: "Doc"},
		"own window by title is never matched":   {Title: "puppet-remo"},
		"own window by process is never matched": {ProcessName: "puppet-remo.exe"},
	} {
		if _, err := find(loc); !errors.Is(err, domain.ErrWindowNotFound) {
			t.Errorf("%s: error = %v, want window_not_found", name, err)
		}
	}
	if _, err := find(domain.WindowLocator{WindowID: "30"}); !errors.Is(err, domain.ErrInputBlocked) {
		t.Errorf("own window by id: error = %v, want input_blocked", err)
	}
}

func TestControlFindUIElementBoundsAndValidation(t *testing.T) {
	e := newCtlEnv(t)
	setNormalTree(e)
	ctx := context.Background()
	loc := domain.ElementLocator{Name: "Save", Match: domain.MatchContains}

	res, err := e.control.Do(ctx, ctlAlice, domain.FindUIElement{Window: domain.WindowLocator{WindowID: "1"}, Element: loc, MaxResults: 2})
	if err != nil || len(res.Found.Elements) != 2 || !res.Found.Truncated {
		t.Errorf("max_results 2: %d elements, truncated %v, %v", len(res.Found.Elements), res.Found.Truncated, err)
	}
	if _, err := e.control.Do(ctx, ctlAlice, domain.FindUIElement{Element: loc}); err != nil {
		t.Fatal(err)
	}
	calls := e.ui.CallsTo("Find")
	if got := calls[len(calls)-1].Args[1]; got != defaultFindResults {
		t.Errorf("default max_results = %v, want %d", got, defaultFindResults)
	}

	bad := map[string]domain.FindUIElement{
		"no element field":     {},
		"match only":           {Element: domain.ElementLocator{Match: domain.MatchContains}},
		"unknown control type": {Element: domain.ElementLocator{ControlType: "Buton"}},
		"unknown match":        {Element: domain.ElementLocator{Name: "x", Match: "regex"}},
		"unknown window match": {Window: domain.WindowLocator{Match: "regex"}, Element: loc},
		"negative max_results": {Element: loc, MaxResults: -1},
		"too many max_results": {Element: loc, MaxResults: maxFindResults + 1},
	}
	for name, op := range bad {
		if _, err := e.control.Do(ctx, ctlAlice, op); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: error = %v, want invalid_argument", name, err)
		}
	}
}

// 読み取りの 2 ツールは、get_ui_tree と同じ検査（キルスイッチ → リース → 観測の規則）を通る。
func TestControlUIAReadOpsAreGatedLikeGetUITree(t *testing.T) {
	ops := map[string]domain.Op{
		"find_ui_element": domain.FindUIElement{Window: domain.WindowLocator{WindowID: "1"}, Element: domain.ElementLocator{Name: "x"}},
		"wait_for":        domain.WaitFor{Window: domain.WindowLocator{WindowID: "1"}, Element: &domain.ElementLocator{Name: "x"}, State: domain.WaitAppear, Timeout: time.Second},
		"wait_for window": domain.WaitFor{Window: domain.WindowLocator{WindowID: "1"}, State: domain.WaitAppear, Timeout: time.Second},
	}
	for name, op := range ops {
		t.Run(name+" kill switch", func(t *testing.T) {
			e := newCtlEnv(t)
			if err := e.kill.Trigger("test"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.control.Do(context.Background(), ctlAlice, op); !errors.Is(err, domain.ErrKillSwitchActive) {
				t.Fatalf("error = %v, want kill_switch_active", err)
			}
			if _, ok := e.leases.Holder(); ok {
				t.Error("the kill switch must not leave an implicit lease")
			}
			if len(e.ui.CallsTo("Find")) != 0 {
				t.Error("UIA was read after the kill switch refused")
			}
		})
		t.Run(name+" lease held by other", func(t *testing.T) {
			e := newCtlEnv(t)
			mustAcquire(t, e, ctlBob)
			if _, err := e.control.Do(context.Background(), ctlAlice, op); !errors.Is(err, domain.ErrLeaseHeldByOther) {
				t.Fatalf("error = %v, want lease_held_by_other", err)
			}
			if len(e.ui.CallsTo("Find")) != 0 {
				t.Error("UIA was read without the lease")
			}
		})
		t.Run(name+" implicit lease", func(t *testing.T) {
			e := newCtlEnv(t)
			advanceOnSleep(e, nil)
			_, _ = e.control.Do(context.Background(), ctlAlice, op)
			if h, ok := e.leases.Holder(); !ok || !h.Actor.SameSession(ctlAlice) {
				t.Errorf("holder = %+v, %v; want an implicit lease for alice like get_ui_tree", h, ok)
			}
		})
	}
	// 自ウィンドウ（window_id）は get_ui_tree と同じ input_blocked / own_window。
	for name, op := range map[string]domain.Op{
		"find_ui_element": domain.FindUIElement{Window: domain.WindowLocator{WindowID: "2"}, Element: domain.ElementLocator{Name: "x"}},
		"wait_for":        domain.WaitFor{Window: domain.WindowLocator{WindowID: "2"}, Element: &domain.ElementLocator{Name: "x"}, State: domain.WaitAppear, Timeout: time.Second},
		"wait_for window": domain.WaitFor{Window: domain.WindowLocator{WindowID: "2"}, State: domain.WaitAppear, Timeout: time.Second},
	} {
		e := newCtlEnv(t)
		_, err := e.control.Do(context.Background(), ctlAlice, op)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked || de.Detail["reason"] != domain.ReasonOwnWindow {
			t.Errorf("%s: error = %v, want input_blocked own_window", name, err)
		}
		if len(e.ui.CallsTo("Find")) != 0 {
			t.Errorf("%s: UIA was read for an own window", name)
		}
	}
	// 前面が自ウィンドウのときの window なし指定も同じ。
	e := newCtlEnv(t)
	e.windows.SetWindows(domain.WindowInfo{ID: "2", Title: "puppet-remo", Own: true, Foreground: true})
	_, err := e.control.Do(context.Background(), ctlAlice, domain.FindUIElement{Element: domain.ElementLocator{Name: "x"}})
	if !errors.Is(err, domain.ErrInputBlocked) {
		t.Errorf("own foreground window: error = %v, want input_blocked", err)
	}
}

func TestControlUIAReadOpsAreAudited(t *testing.T) {
	e := newCtlEnv(t)
	setNormalTree(e)
	ctx := context.Background()
	if _, err := e.control.Do(ctx, ctlAlice, domain.FindUIElement{Window: domain.WindowLocator{WindowID: "1"}, Element: domain.ElementLocator{Name: "Save"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Do(ctx, ctlAlice, domain.WaitFor{Window: domain.WindowLocator{Title: "normal"}, State: domain.WaitAppear, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	entries := e.audit.Entries()
	var find, wait map[string]any
	for _, en := range entries {
		var detail map[string]any
		if len(en.DetailJSON) > 0 {
			_ = json.Unmarshal(en.DetailJSON, &detail)
		}
		switch en.Event {
		case "tool.find_ui_element":
			find = detail
		case "tool.wait_for":
			wait = detail
		}
	}
	if find == nil || find["name"] != "Save" || find["window_id"] != "1" || find["count"] != float64(1) {
		t.Errorf("find_ui_element audit detail = %v", find)
	}
	if wait == nil || wait["title"] != "normal" || wait["state"] != "appear" || wait["matched"] != true || wait["timeout_ms"] != float64(1000) {
		t.Errorf("wait_for audit detail = %v", wait)
	}
}

// advanceOnSleep は Sleep のたびに、その長さだけ Clock を進め、hook（nil 可）に何回目かを渡す。
func advanceOnSleep(e *ctlEnv, hook func(n int)) {
	e.onSleep = func() {
		e.clock.Advance(e.slept[len(e.slept)-1])
		if hook != nil {
			hook(len(e.slept))
		}
	}
}

func waitWindow(loc domain.WindowLocator, state domain.WaitState) domain.WaitFor {
	return domain.WaitFor{Window: loc, State: state, Timeout: 5 * time.Second, Interval: time.Second}
}

func TestControlWaitForWindowAppears(t *testing.T) {
	e := newCtlEnv(t)
	advanceOnSleep(e, func(n int) {
		if n == 2 {
			e.windows.Add(domain.WindowInfo{ID: "50", Title: "Save As", Process: "notepad.exe", PID: 9})
		}
	})
	res, err := e.control.Do(context.Background(), ctlAlice, waitWindow(domain.WindowLocator{Title: "Save As"}, domain.WaitAppear))
	if err != nil || res.Waited == nil || !res.Waited.Satisfied {
		t.Fatalf("Do = %+v, %v", res.Waited, err)
	}
	w := res.Waited
	if w.Window == nil || w.Window.ID != "50" || w.Window.PID != 9 || w.Elapsed != 2*time.Second || len(e.slept) != 2 {
		t.Errorf("outcome = %+v (window %+v), slept %v", w, w.Window, e.slept)
	}
}

func TestControlWaitForWindowAlreadyThereReturnsAtOnce(t *testing.T) {
	e := newCtlEnv(t)
	res, err := e.control.Do(context.Background(), ctlAlice, waitWindow(domain.WindowLocator{Title: "normal"}, domain.WaitAppear))
	if err != nil || !res.Waited.Satisfied || res.Waited.Window.ID != "1" || len(e.slept) != 0 {
		t.Fatalf("outcome = %+v, %v, slept %v", res.Waited, err, e.slept)
	}
}

func TestControlWaitForWindowDisappears(t *testing.T) {
	e := newCtlEnv(t)
	advanceOnSleep(e, func(n int) {
		if n == 3 {
			e.windows.Remove("1")
		}
	})
	res, err := e.control.Do(context.Background(), ctlAlice, waitWindow(domain.WindowLocator{Title: "normal"}, domain.WaitDisappear))
	if err != nil || !res.Waited.Satisfied || res.Waited.Window != nil || res.Waited.Elapsed != 3*time.Second {
		t.Fatalf("outcome = %+v, %v", res.Waited, err)
	}
}

func TestControlWaitForWindowTimeoutKeepsNearCandidates(t *testing.T) {
	e := newCtlEnv(t)
	e.windows.SetWindows(
		domain.WindowInfo{ID: "10", Title: "Report - Word", Process: "winword.exe", Foreground: true},
		domain.WindowInfo{ID: "20", Title: "Notes", Process: "notepad.exe"},
		domain.WindowInfo{ID: "30", Title: "Save dialog", Process: "other.exe"},
		domain.WindowInfo{ID: "40", Title: "puppet-remo", Process: "puppet-remo.exe", Own: true},
	)
	advanceOnSleep(e, nil)
	op := waitWindow(domain.WindowLocator{Title: "save", Match: domain.MatchContains, ProcessName: "notepad.exe"}, domain.WaitAppear)
	res, err := e.control.Do(context.Background(), ctlAlice, op)
	if err != nil {
		t.Fatalf("a timeout is a result, not an error: %v", err)
	}
	w := res.Waited
	if w.Satisfied || w.Elapsed != 5*time.Second || len(e.slept) != 5 {
		t.Fatalf("outcome = %+v, slept %v; want a timeout after exactly timeout_ms", w, e.slept)
	}
	var ids []string
	for _, c := range w.Windows {
		ids = append(ids, c.ID)
	}
	// 近い候補（title か process に当たるもの）が先、自ウィンドウは出さない。
	if len(ids) != 3 || ids[0] != "20" && ids[0] != "30" || ids[2] != "10" {
		t.Errorf("candidates = %v, want 20 and 30 first, then 10, never 40", ids)
	}
}

func TestControlWaitForWindowTimeoutCandidatesAreBounded(t *testing.T) {
	e := newCtlEnv(t)
	var many []domain.WindowInfo
	for i := range 30 {
		many = append(many, domain.WindowInfo{ID: string(rune('A' + i)), Title: "w"})
	}
	e.windows.SetWindows(many...)
	advanceOnSleep(e, nil)
	res, err := e.control.Do(context.Background(), ctlAlice, waitWindow(domain.WindowLocator{Title: "nope"}, domain.WaitAppear))
	if err != nil || len(res.Waited.Windows) != waitCandidateLimit || !res.Waited.Truncated {
		t.Fatalf("candidates = %d, truncated %v, %v; want %d and truncated", len(res.Waited.Windows), res.Waited.Truncated, err, waitCandidateLimit)
	}
}

func TestControlWaitForWindowDisappearTimeoutReturnsWhatRemains(t *testing.T) {
	e := newCtlEnv(t)
	advanceOnSleep(e, nil)
	res, err := e.control.Do(context.Background(), ctlAlice, waitWindow(domain.WindowLocator{Title: "normal"}, domain.WaitDisappear))
	if err != nil || res.Waited.Satisfied || len(res.Waited.Windows) != 1 || res.Waited.Windows[0].ID != "1" || res.Waited.Window == nil {
		t.Fatalf("outcome = %+v, %v", res.Waited, err)
	}
}

func waitElement(win domain.WindowLocator, el domain.ElementLocator, state domain.WaitState) domain.WaitFor {
	return domain.WaitFor{Window: win, Element: &el, State: state, Timeout: 5 * time.Second, Interval: time.Second}
}

func TestControlWaitForElementAppears(t *testing.T) {
	e := newCtlEnv(t)
	e.ui.SetTree("1", domain.UINode{ID: uiaID(1, 1), Role: "Window", Name: "normal"})
	advanceOnSleep(e, func(n int) {
		if n == 2 {
			setNormalTree(e)
		}
	})
	res, err := e.control.Do(context.Background(), ctlAlice,
		waitElement(domain.WindowLocator{WindowID: "1"}, domain.ElementLocator{AutomationID: "SaveButton"}, domain.WaitAppear))
	if err != nil || !res.Waited.Satisfied {
		t.Fatalf("outcome = %+v, %v", res.Waited, err)
	}
	w := res.Waited
	if len(w.Elements) != 1 || w.Elements[0].ID != uiaID(1, 2) || w.Window == nil || w.Window.ID != "1" || w.Elapsed != 2*time.Second {
		t.Errorf("outcome = %+v", w)
	}
}

func TestControlWaitForElementWaitsForTheWindowToo(t *testing.T) {
	e := newCtlEnv(t)
	e.windows.SetWindows(domain.WindowInfo{ID: "10", Title: "front", Foreground: true})
	advanceOnSleep(e, func(n int) {
		if n == 1 {
			e.windows.Add(domain.WindowInfo{ID: "1", Title: "Dialog"})
			setNormalTree(e)
		}
	})
	res, err := e.control.Do(context.Background(), ctlAlice,
		waitElement(domain.WindowLocator{Title: "Dialog"}, domain.ElementLocator{Name: "Save"}, domain.WaitAppear))
	if err != nil || !res.Waited.Satisfied || res.Waited.Window.ID != "1" || len(res.Waited.Elements) != 1 {
		t.Fatalf("outcome = %+v, %v", res.Waited, err)
	}
}

func TestControlWaitForElementDisappears(t *testing.T) {
	e := newCtlEnv(t)
	setNormalTree(e)
	advanceOnSleep(e, func(n int) {
		if n == 2 {
			e.ui.SetTree("1", domain.UINode{ID: uiaID(1, 1), Role: "Window", Name: "normal"})
		}
	})
	res, err := e.control.Do(context.Background(), ctlAlice,
		waitElement(domain.WindowLocator{WindowID: "1"}, domain.ElementLocator{AutomationID: "SaveButton"}, domain.WaitDisappear))
	if err != nil || !res.Waited.Satisfied || len(res.Waited.Elements) != 0 || res.Waited.Elapsed != 2*time.Second {
		t.Fatalf("outcome = %+v, %v", res.Waited, err)
	}
}

func TestControlWaitForElementDisappearsWhenTheWindowCloses(t *testing.T) {
	for name, loc := range map[string]domain.WindowLocator{"by id": {WindowID: "1"}, "by title": {Title: "normal"}} {
		t.Run(name, func(t *testing.T) {
			e := newCtlEnv(t)
			setNormalTree(e)
			advanceOnSleep(e, func(int) { e.windows.Remove("1") })
			res, err := e.control.Do(context.Background(), ctlAlice, waitElement(loc, domain.ElementLocator{Name: "Save"}, domain.WaitDisappear))
			if err != nil || !res.Waited.Satisfied {
				t.Fatalf("outcome = %+v, %v", res.Waited, err)
			}
		})
	}
}

func TestControlWaitForElementTimeoutReturnsNearElements(t *testing.T) {
	e := newCtlEnv(t)
	setNormalTree(e)
	advanceOnSleep(e, nil)
	res, err := e.control.Do(context.Background(), ctlAlice,
		waitElement(domain.WindowLocator{WindowID: "1"}, domain.ElementLocator{Name: "Save as copy", ControlType: "Button"}, domain.WaitAppear))
	if err != nil {
		t.Fatal(err)
	}
	w := res.Waited
	if w.Satisfied || w.Window == nil || w.Window.ID != "1" {
		t.Fatalf("outcome = %+v", w)
	}
	// name は部分一致に当たらないが、control_type が当たる 2 つのボタンが近い候補。
	if len(w.Elements) != 2 || w.Elements[0].Name != "Save" || w.Elements[1].Name != "Save As" {
		t.Errorf("candidate elements = %+v", w.Elements)
	}
}

func TestControlWaitForElementTimeoutWithoutWindowListsVisibleWindows(t *testing.T) {
	e := newCtlEnv(t)
	advanceOnSleep(e, nil)
	res, err := e.control.Do(context.Background(), ctlAlice,
		waitElement(domain.WindowLocator{Title: "Missing"}, domain.ElementLocator{Name: "OK"}, domain.WaitAppear))
	if err != nil {
		t.Fatal(err)
	}
	w := res.Waited
	if w.Satisfied || w.Window != nil || len(w.Elements) != 0 || len(w.Windows) == 0 {
		t.Fatalf("outcome = %+v", w)
	}
	for _, c := range w.Windows {
		if c.Own {
			t.Errorf("an own window %q was offered as a candidate", c.ID)
		}
	}
}

func TestControlWaitForElementByIDFailsFastWhenTheWindowIsGone(t *testing.T) {
	e := newCtlEnv(t)
	_, err := e.control.Do(context.Background(), ctlAlice,
		waitElement(domain.WindowLocator{WindowID: "999"}, domain.ElementLocator{Name: "OK"}, domain.WaitAppear))
	if !errors.Is(err, domain.ErrWindowNotFound) || len(e.slept) != 0 {
		t.Fatalf("error = %v, slept %v; want window_not_found without waiting", err, e.slept)
	}
}

func TestControlWaitForStopsOnKillSwitchAndCancel(t *testing.T) {
	t.Run("kill switch during the wait", func(t *testing.T) {
		e := newCtlEnv(t)
		advanceOnSleep(e, func(n int) {
			if n == 2 {
				_ = e.kill.Trigger("test")
			}
		})
		_, err := e.control.Do(context.Background(), ctlAlice, waitWindow(domain.WindowLocator{Title: "never"}, domain.WaitAppear))
		if !errors.Is(err, domain.ErrKillSwitchActive) || len(e.slept) != 2 {
			t.Fatalf("error = %v, slept %v; want kill_switch_active right after the trigger", err, e.slept)
		}
	})
	t.Run("canceled while sleeping", func(t *testing.T) {
		e := newCtlEnv(t)
		e.sleepErr = context.Canceled
		_, err := e.control.Do(context.Background(), ctlAlice, waitWindow(domain.WindowLocator{Title: "never"}, domain.WaitAppear))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}

func TestControlWaitForSleepsNoLongerThanTheTimeLeft(t *testing.T) {
	e := newCtlEnv(t)
	advanceOnSleep(e, nil)
	op := domain.WaitFor{Window: domain.WindowLocator{Title: "never"}, State: domain.WaitAppear, Timeout: 700 * time.Millisecond, Interval: time.Second}
	if _, err := e.control.Do(context.Background(), ctlAlice, op); err != nil {
		t.Fatal(err)
	}
	if len(e.slept) != 1 || e.slept[0] != 700*time.Millisecond {
		t.Errorf("slept = %v, want one sleep of the remaining 700ms", e.slept)
	}
}

func TestControlWaitForValidation(t *testing.T) {
	el := &domain.ElementLocator{Name: "x"}
	win := domain.WindowLocator{Title: "t"}
	bad := map[string]domain.WaitFor{
		"unknown state":            {Window: win, State: "gone", Timeout: time.Second},
		"empty state":              {Window: win, Timeout: time.Second},
		"no timeout":               {Window: win, State: domain.WaitAppear},
		"negative timeout":         {Window: win, State: domain.WaitAppear, Timeout: -time.Second},
		"timeout above the cap":    {Window: win, State: domain.WaitAppear, Timeout: MaxWaitForTimeout + time.Millisecond},
		"interval too short":       {Window: win, State: domain.WaitAppear, Timeout: time.Second, Interval: time.Millisecond},
		"interval too long":        {Window: win, State: domain.WaitAppear, Timeout: time.Second, Interval: time.Minute},
		"no window and no element": {State: domain.WaitAppear, Timeout: time.Second},
		"element without fields":   {Window: win, Element: &domain.ElementLocator{Match: domain.MatchContains}, State: domain.WaitAppear, Timeout: time.Second},
		"unknown control type":     {Window: win, Element: &domain.ElementLocator{ControlType: "Buton"}, State: domain.WaitAppear, Timeout: time.Second},
		"unknown match":            {Window: domain.WindowLocator{Title: "t", Match: "re"}, Element: el, State: domain.WaitAppear, Timeout: time.Second},
	}
	for name, op := range bad {
		e := newCtlEnv(t)
		if _, err := e.control.Do(context.Background(), ctlAlice, op); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: error = %v, want invalid_argument", name, err)
		}
		if len(e.slept) != 0 {
			t.Errorf("%s: waited despite the invalid argument", name)
		}
	}
	// 上限ちょうどは通る（待たずに済む対象）。
	e := newCtlEnv(t)
	op := domain.WaitFor{Window: domain.WindowLocator{Title: "normal"}, State: domain.WaitAppear, Timeout: MaxWaitForTimeout}
	if _, err := e.control.Do(context.Background(), ctlAlice, op); err != nil {
		t.Errorf("timeout at the cap: %v", err)
	}
}

// スコープ付きのリースの保持者も、get_ui_tree と同じく UI を読める（入力ではない。InputGuard の観測の規則は通る）。
func TestControlUIAReadOpsUnderAScopedLease(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	ctx := context.Background()
	front := te.app.Ref().ID
	if _, err := te.ctl.Do(ctx, ctlAlice, domain.GetUITree{WindowID: front}); err != nil {
		t.Fatalf("GetUITree under a scoped lease: %v", err)
	}
	res, err := te.ctl.Do(ctx, ctlAlice, domain.FindUIElement{Window: domain.WindowLocator{WindowID: front}, Element: domain.ElementLocator{ControlType: "Window"}})
	if err != nil || res.Found == nil {
		t.Fatalf("FindUIElement under a scoped lease = %+v, %v", res, err)
	}
	wf := domain.WaitFor{Window: domain.WindowLocator{WindowID: front}, State: domain.WaitAppear, Timeout: time.Second}
	if res, err := te.ctl.Do(ctx, ctlAlice, wf); err != nil || !res.Waited.Satisfied {
		t.Fatalf("WaitFor under a scoped lease = %+v, %v", res.Waited, err)
	}
}
