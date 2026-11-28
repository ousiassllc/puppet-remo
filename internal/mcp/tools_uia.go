package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// UI 要素の探し方（find_ui_element・wait_for・今後の操作ツールで共通。domain.WindowLocator・ElementLocator）。
const locatorDoc = "Window locator: window_id (from list_windows; preferred), title, process_name (e.g. notepad.exe, case-insensitive). " +
	"Without any window field the foreground window is used. When several windows match, the topmost one is used; puppet-remo's own windows are never matched by title or process_name. " +
	"Element locator (all given fields must match; at least one is required): name, automation_id (exact), control_type (UI Automation ControlType name such as Button, Edit, CheckBox). " +
	`match applies to title and name: "exact" (default; case-sensitive) or "contains" (case-insensitive).`

type findUIElementIn struct {
	WindowID     string `json:"window_id,omitempty" jsonschema:"window id from list_windows (preferred over title/process_name)"`
	Title        string `json:"title,omitempty" jsonschema:"window title (see match)"`
	ProcessName  string `json:"process_name,omitempty" jsonschema:"window process name such as notepad.exe (case-insensitive)"`
	Name         string `json:"name,omitempty" jsonschema:"element name (see match)"`
	AutomationID string `json:"automation_id,omitempty" jsonschema:"element AutomationId (exact match)"`
	ControlType  string `json:"control_type,omitempty" jsonschema:"element ControlType name such as Button, Edit, CheckBox"`
	Match        string `json:"match,omitempty" jsonschema:"how title and name are compared: exact (default) or contains"`
	MaxResults   int    `json:"max_results,omitempty" jsonschema:"maximum number of elements to return (default 20, at most 100)"`
}

type waitForIn struct {
	WindowID     string `json:"window_id,omitempty" jsonschema:"window id from list_windows (preferred over title/process_name)"`
	Title        string `json:"title,omitempty" jsonschema:"window title (see match)"`
	ProcessName  string `json:"process_name,omitempty" jsonschema:"window process name such as notepad.exe (case-insensitive)"`
	Name         string `json:"name,omitempty" jsonschema:"element name (see match). Any element field makes this wait for an element instead of a window"`
	AutomationID string `json:"automation_id,omitempty" jsonschema:"element AutomationId (exact match)"`
	ControlType  string `json:"control_type,omitempty" jsonschema:"element ControlType name such as Button, Edit, CheckBox"`
	Match        string `json:"match,omitempty" jsonschema:"how title and name are compared: exact (default) or contains"`
	State        string `json:"state" jsonschema:"appear or disappear"`
	TimeoutMs    int    `json:"timeout_ms" jsonschema:"how long to wait in milliseconds (1 to 60000)"`
	IntervalMs   int    `json:"interval_ms,omitempty" jsonschema:"polling interval in milliseconds (default 500, 100 to 5000)"`
}

// uiElementBody は find_ui_element・wait_for の要素 1 件。id は get_ui_tree の id と同じ形式の要素 ID。
type uiElementBody struct {
	ID           string   `json:"id"`
	ControlType  string   `json:"control_type"`
	Name         string   `json:"name"`
	AutomationID string   `json:"automation_id"`
	ClassName    string   `json:"class_name"`
	Rect         rectBody `json:"rect"`
	Enabled      bool     `json:"enabled"`
	Offscreen    bool     `json:"offscreen"`
	// Visible は画面外でなく、矩形がモニター上にあるか。
	Visible  bool     `json:"visible"`
	Patterns []string `json:"patterns"`
}

type findUIElementBody struct {
	Window    windowBody      `json:"window"`
	Truncated bool            `json:"truncated"`
	Elements  []uiElementBody `json:"elements"`
}

type waitCandidatesBody struct {
	Windows  []windowBody    `json:"windows"`
	Elements []uiElementBody `json:"elements"`
}

// waitForBody は wait_for の結果。タイムアウトはエラーではなく matched が false・timed_out が true の結果で、candidates を添える。
type waitForBody struct {
	State    string `json:"state"`
	Target   string `json:"target"` // window | element
	Matched  bool   `json:"matched"`
	TimedOut bool   `json:"timed_out"`
	WaitedMs int64  `json:"waited_ms"`
	// Window は appear の成功では見つかったウィンドウ（要素を待つときはその要素を含むウィンドウ）。タイムアウトでは要素を探したウィンドウ（無ければ省略）。
	Window   *windowBody     `json:"window,omitempty"`
	Elements []uiElementBody `json:"elements,omitempty"` // 要素を待つ appear の成功: 一致した要素
	// Candidates はタイムアウトのとき、appear なら近い候補、disappear なら消えずに残っている対象（最大 20 件ずつ）。
	Candidates *waitCandidatesBody `json:"candidates,omitempty"`
	Truncated  bool                `json:"truncated,omitempty"`
}

// uiActionIn は ui_action の入力。value は set_value のときだけ必須（空文字列も有効で、内容を空にする）、number は set_range_value のときだけ必須（0 も有効）なので、
// どちらも省略と空・0 を区別できるポインタ。
type uiActionIn struct {
	ElementID string   `json:"element_id" jsonschema:"element id (id) returned by find_ui_element, get_ui_tree or wait_for"`
	Action    string   `json:"action" jsonschema:"invoke, set_value, toggle, select, expand, collapse, scroll_into_view or set_range_value"`
	Value     *string  `json:"value,omitempty" jsonschema:"text for set_value (required for it; an empty string clears the field). Not allowed for other actions"`
	Number    *float64 `json:"number,omitempty" jsonschema:"set_range_value only (required for it): the finite number to set on a slider or spinner; 0 is valid. Outside the element's minimum and maximum it is rejected, not clamped"`
	State     string   `json:"state,omitempty" jsonschema:"toggle only: on or off to set that state (default: flip the current state)"`
}

// uiActionBody は ui_action の結果。element は操作した要素（find_ui_element と同じ形。縮小座標。値は含めない）。
type uiActionBody struct {
	OK      bool          `json:"ok"`
	Action  string        `json:"action"`
	Element uiElementBody `json:"element"`
	// Changed は UI Automation の操作を実際に呼んだか。false はすでにその状態だった（toggle の state 指定・expand・collapse）。
	Changed bool `json:"changed"`
	// State は操作後の状態。toggle: on / off / indeterminate、expand・collapse: expanded / collapsed / partially_expanded、select: selected / not_selected。
	State string `json:"state,omitempty"`
}

func (in findUIElementIn) window() domain.WindowLocator {
	return domain.WindowLocator{WindowID: in.WindowID, Title: in.Title, ProcessName: in.ProcessName, Match: domain.MatchMode(in.Match)}
}

func (in findUIElementIn) element() domain.ElementLocator {
	return domain.ElementLocator{Name: in.Name, AutomationID: in.AutomationID, ControlType: in.ControlType, Match: domain.MatchMode(in.Match)}
}

// windowBodyOf はウィンドウを縮小座標の出力にする（list_windows と同じ形）。
func windowBodyOf(conv converter, w domain.WindowInfo) windowBody {
	blockReason := domain.InputBlockReason(w)
	return windowBody{
		ID:         w.ID,
		Title:      w.Title,
		Process:    w.Process,
		PID:        w.PID,
		Rect:       conv.rect(w.Monitor, w.Rect),
		Monitor:    w.Monitor,
		Foreground: w.Foreground,
		Minimized:  w.Minimized,
		Elevated:   w.Elevated,

		Blocked:       blockReason != "",
		BlockedReason: blockReason,
	}
}

func windowBodiesOf(conv converter, ws []domain.WindowInfo) []windowBody {
	out := make([]windowBody, 0, len(ws))
	for _, w := range ws {
		out = append(out, windowBodyOf(conv, w))
	}
	return out
}

// uiElementBodiesOf は要素を、window のモニターの縮小座標にする。
func uiElementBodiesOf(conv converter, window domain.WindowInfo, elems []domain.UIElementInfo) []uiElementBody {
	out := make([]uiElementBody, 0, len(elems))
	for _, e := range elems {
		rect := conv.rect(window.Monitor, e.Rect)
		patterns := e.Patterns
		if patterns == nil {
			patterns = []string{}
		}
		out = append(out, uiElementBody{
			ID:           e.ID,
			ControlType:  e.ControlType,
			Name:         e.Name,
			AutomationID: e.AutomationID,
			ClassName:    e.ClassName,
			Rect:         rect,
			Enabled:      e.Enabled,
			Offscreen:    e.Offscreen,
			Visible:      !e.Offscreen && rect.Width > 0 && rect.Height > 0,
			Patterns:     patterns,
		})
	}
	return out
}

// uiActionNote は ui_action の説明。
const uiActionNote = "Operate a UI element through its UI Automation pattern, without moving the mouse cursor or the keyboard focus and without sending any keystroke or click. " +
	"element_id is the id from find_ui_element, get_ui_tree or wait_for. Actions: invoke (Invoke pattern: press a button, menu item or link), " +
	"set_value (Value pattern: replace the text of an edit field with value; not typed, so IME and focus are not involved), " +
	"toggle (Toggle pattern: flip a check box or toggle button, or set state to on or off), select (SelectionItem pattern: select a list item, tab or radio button), " +
	"expand and collapse (ExpandCollapse pattern), scroll_into_view (ScrollItem pattern: bring an off-screen item into view inside its scrollable parent; no other argument), " +
	"set_range_value (RangeValue pattern: set a slider or spinner to number; a number outside the element's minimum and maximum is rejected with invalid_argument (detail.minimum and detail.maximum), never clamped). " +
	"The result has the element, changed (false when it was already in that state) and the resulting state. " +
	"Errors: pattern_unsupported (the element lacks the pattern; detail.supported lists its patterns; use the pointer or keyboard tools instead), " +
	"stale_element (the element or its window is gone; find it again), element_disabled, element_read_only (set_value, set_range_value), " +
	"input_blocked (own_window: a puppet-remo window; elevated_window or access_denied: the app is elevated so UI Automation cannot reach it), " +
	"ui_action_failed (detail.reason), invalid_argument. " +
	"Web content (WebView2, browsers): Invoke on buttons and links usually works, but set_value may not fire the page's input events (a framework such as Vue may not see the change); " +
	"check the result on screen, and fall back to click and type when it does not take effect. " +
	"With a window-scoped lease the element's window must belong to the target window group (outside_target_group otherwise). " +
	"It counts as input: the control lease is needed (acquired implicitly), the kill switch stops it, and it is audited (the action and the element's control type and automation id; for set_value only the length of the text, for set_range_value no number unless the audit.record_text setting is on). " +
	"Unlike mouse and keyboard tools it is not paused while the host person is using the mouse or keyboard."

// addUIATools は UI Automation のツールを登録する: FR-M28（find_ui_element）・FR-M29（wait_for）・FR-M31（ui_action）。
func (s *Server) addUIATools() {
	addTool(s, "find_ui_element",
		"Find UI elements in a window with UI Automation, by name, automation_id and/or control_type (all given must match). "+
			"Returns up to max_results elements (shallowest first) with id (the same element id get_ui_tree returns), control_type, name, automation_id, class_name, "+
			"rect (scaled coordinates), enabled, offscreen, visible and the supported patterns (Invoke, Value, Toggle, SelectionItem, ExpandCollapse, Scroll, ScrollItem, RangeValue, Window, Text, Selection). "+
			"An element id stops working (stale_element) once the element or its window is gone. Read-only: no input is sent. "+
			locatorDoc+" Needs the control lease (acquired implicitly).",
		true, func(ctx context.Context, who domain.Actor, in findUIElementIn) (toolResult, error) {
			res, err := s.control.Do(ctx, who, domain.FindUIElement{Window: in.window(), Element: in.element(), MaxResults: in.MaxResults})
			if err != nil {
				return toolResult{}, err
			}
			if res.Found == nil {
				return toolResult{}, domain.ErrWindowNotFound
			}
			conv, err := s.converter(ctx)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: findUIElementBody{
				Window:    windowBodyOf(conv, res.Found.Window),
				Truncated: res.Found.Truncated,
				Elements:  uiElementBodiesOf(conv, res.Found.Window, res.Found.Elements),
			}}, nil
		})

	addTool(s, "wait_for",
		"Wait until a window or a UI element appears or disappears, polling until timeout_ms (required, at most 60000). "+
			"Give a window (window_id, title and/or process_name) to wait for a window; give element fields (name, automation_id, control_type) as well to wait for an element inside the window. "+
			"state is appear or disappear. On success matched is true and the window (window id, title, process, rect) or the elements (id, rect, patterns) are returned for the next step. "+
			"On timeout the result is not an error: matched is false, timed_out is true, and candidates lists what was visible (for appear: windows or elements that nearly match; for disappear: what is still there), at most 20 each. "+
			"Read-only: no input is sent; stops at once when the kill switch is triggered. "+
			locatorDoc+" Needs the control lease (acquired implicitly).",
		true, func(ctx context.Context, who domain.Actor, in waitForIn) (toolResult, error) {
			loc := findUIElementIn{WindowID: in.WindowID, Title: in.Title, ProcessName: in.ProcessName,
				Name: in.Name, AutomationID: in.AutomationID, ControlType: in.ControlType, Match: in.Match}
			op := domain.WaitFor{
				Window:   loc.window(),
				State:    domain.WaitState(in.State),
				Timeout:  time.Duration(in.TimeoutMs) * time.Millisecond,
				Interval: time.Duration(in.IntervalMs) * time.Millisecond,
			}
			target := "window"
			if el := loc.element(); !el.IsZero() {
				op.Element, target = &el, "element"
			}
			res, err := s.control.Do(ctx, who, op)
			if err != nil {
				return toolResult{}, err
			}
			if res.Waited == nil {
				return toolResult{}, domain.ErrInvalidArgument.WithMsg("the wait returned no result")
			}
			conv, err := s.converter(ctx)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: waitForBodyOf(conv, in.State, target, *res.Waited)}, nil
		})

	addTool(s, "ui_action", uiActionNote,
		false, func(ctx context.Context, who domain.Actor, in uiActionIn) (toolResult, error) {
			op := domain.UIAction{ElementID: in.ElementID, Kind: domain.UIActionKind(strings.ToLower(in.Action)), Value: in.Value, Number: in.Number, State: strings.ToLower(in.State)}
			res, err := s.control.Do(ctx, who, op)
			if err != nil {
				return toolResult{}, err
			}
			if res.Acted == nil {
				return toolResult{}, domain.ErrInvalidArgument.WithMsg("the action returned no result")
			}
			conv, err := s.converter(ctx)
			if err != nil {
				return toolResult{}, err
			}
			el := uiElementBodiesOf(conv, res.Acted.Window, []domain.UIElementInfo{res.Acted.Element})[0]
			return toolResult{Structured: uiActionBody{OK: true, Action: string(op.Kind), Element: el, Changed: res.Acted.Changed, State: res.Acted.State}}, nil
		})
}

func waitForBodyOf(conv converter, state, target string, w service.WaitOutcome) waitForBody {
	out := waitForBody{
		State:     state,
		Target:    target,
		Matched:   w.Satisfied,
		TimedOut:  !w.Satisfied,
		WaitedMs:  w.Elapsed.Milliseconds(),
		Truncated: w.Truncated,
	}
	var window domain.WindowInfo
	if w.Window != nil {
		window = *w.Window
		b := windowBodyOf(conv, window)
		out.Window = &b
	}
	elements := uiElementBodiesOf(conv, window, w.Elements)
	if w.Satisfied {
		if len(elements) > 0 {
			out.Elements = elements
		}
		return out
	}
	out.Candidates = &waitCandidatesBody{Windows: windowBodiesOf(conv, w.Windows), Elements: elements}
	return out
}
