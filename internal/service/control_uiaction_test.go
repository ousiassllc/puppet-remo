package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// ui_action（#268）の Control のテスト。カーソルもキーボードフォーカスも動かさず、入力を注入しないことを、偽の platform の記録で確かめる。

// ui_action 用の木の要素（ウィンドウ "1"。uiaID の n）。
const (
	uaSave     = 2 // Invoke を持つボタン
	uaName     = 3 // Value を持つ編集欄
	uaCheck    = 4 // Toggle を持つチェックボックス
	uaItem     = 5 // SelectionItem を持つリスト項目
	uaNode     = 6 // ExpandCollapse を持つ木の項目
	uaPlain    = 7 // パターンを持たないボタン
	uaDisabled = 8 // 無効なボタン
	uaPassword = 9 // パスワード欄
)

func setActionTree(e *ctlEnv) {
	e.ui.SetTree("1", domain.UINode{
		ID: uiaID(1, 1), Role: "Window", Name: "normal", Enabled: true,
		Children: []domain.UINode{
			{ID: uiaID(1, uaSave), Role: "Button", Name: "Save", AutomationID: "SaveButton", Enabled: true, Patterns: []string{domain.PatternInvoke}},
			{ID: uiaID(1, uaName), Role: "Edit", Name: "Name", AutomationID: "NameBox", Enabled: true, Patterns: []string{domain.PatternValue, domain.PatternText}},
			{ID: uiaID(1, uaCheck), Role: "CheckBox", Name: "Agree", AutomationID: "AgreeBox", Enabled: true, Patterns: []string{domain.PatternToggle}},
			{ID: uiaID(1, uaItem), Role: "ListItem", Name: "Item", Enabled: true, Patterns: []string{domain.PatternSelectionItem}},
			{ID: uiaID(1, uaNode), Role: "TreeItem", Name: "Folder", Enabled: true, Patterns: []string{domain.PatternExpandCollapse}},
			{ID: uiaID(1, uaPlain), Role: "Button", Name: "Plain", Enabled: true},
			{ID: uiaID(1, uaDisabled), Role: "Button", Name: "Off", Enabled: false, Patterns: []string{domain.PatternInvoke}},
			{ID: uiaID(1, uaPassword), Role: "Edit", Name: "Secret", AutomationID: "PwBox", Enabled: true, Patterns: []string{domain.PatternValue}, ValueRedacted: true},
		},
	})
}

func newActionEnv(t *testing.T) *ctlEnv {
	t.Helper()
	e := newCtlEnv(t)
	setActionTree(e)
	return e
}

func act(n int32, kind domain.UIActionKind) domain.UIAction {
	return domain.UIAction{ElementID: uiaID(1, n), Kind: kind}
}

func actSetValue(n int32, v string) domain.UIAction {
	a := act(n, domain.UIActionSetValue)
	a.Value = &v
	return a
}

// requireNoInputSideEffects は、入力の注入・ウィンドウの前面化・クリップボードの操作が 1 つも無かったことを確かめる。
func requireNoInputSideEffects(t *testing.T, e *ctlEnv) {
	t.Helper()
	if calls := inputCalls(e); len(calls) != 0 {
		t.Errorf("input was touched: %v", calls)
	}
	if calls := e.windows.CallsTo("Focus"); len(calls) != 0 {
		t.Errorf("a window was focused: %v", calls)
	}
	if calls := e.clipboard.Calls(); len(calls) != 0 {
		t.Errorf("the clipboard was touched: %v", calls)
	}
}

func TestControlUIActionEachActionSucceedsWithoutInput(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		op    domain.UIAction
		check func(t *testing.T, e *ctlEnv, res Result)
	}{
		{"invoke", act(uaSave, domain.UIActionInvoke), func(t *testing.T, e *ctlEnv, res Result) {
			if !res.Acted.Changed || res.Acted.Element.Name != "Save" {
				t.Errorf("acted = %+v", res.Acted)
			}
		}},
		{"set_value", actSetValue(uaName, "日本語 value"), func(t *testing.T, e *ctlEnv, res Result) {
			if v, ok := e.ui.Value(uiaID(1, uaName)); !ok || v != "日本語 value" {
				t.Errorf("value = %q, %v", v, ok)
			}
		}},
		{"set_value empty clears", actSetValue(uaName, ""), func(t *testing.T, e *ctlEnv, res Result) {
			if v, ok := e.ui.Value(uiaID(1, uaName)); !ok || v != "" {
				t.Errorf("value = %q, %v", v, ok)
			}
		}},
		{"toggle flips", act(uaCheck, domain.UIActionToggle), func(t *testing.T, e *ctlEnv, res Result) {
			if e.ui.ToggleState(uiaID(1, uaCheck)) != domain.ToggleStateOn || res.Acted.State != domain.ToggleStateOn {
				t.Errorf("toggle = %q, result %+v", e.ui.ToggleState(uiaID(1, uaCheck)), res.Acted)
			}
		}},
		{"toggle to off when already off does nothing", domain.UIAction{ElementID: uiaID(1, uaCheck), Kind: domain.UIActionToggle, State: domain.ToggleStateOff}, func(t *testing.T, e *ctlEnv, res Result) {
			if res.Acted.Changed || res.Acted.State != domain.ToggleStateOff {
				t.Errorf("acted = %+v", res.Acted)
			}
		}},
		{"toggle to on", domain.UIAction{ElementID: uiaID(1, uaCheck), Kind: domain.UIActionToggle, State: domain.ToggleStateOn}, func(t *testing.T, e *ctlEnv, res Result) {
			if !res.Acted.Changed || e.ui.ToggleState(uiaID(1, uaCheck)) != domain.ToggleStateOn {
				t.Errorf("acted = %+v", res.Acted)
			}
		}},
		{"select", act(uaItem, domain.UIActionSelect), func(t *testing.T, e *ctlEnv, res Result) {
			if !e.ui.Selected(uiaID(1, uaItem)) || res.Acted.State != domain.SelectStateSelected {
				t.Errorf("acted = %+v", res.Acted)
			}
		}},
		{"expand", act(uaNode, domain.UIActionExpand), func(t *testing.T, e *ctlEnv, res Result) {
			if e.ui.ExpandState(uiaID(1, uaNode)) != domain.ExpandStateExpanded || res.Acted.State != domain.ExpandStateExpanded {
				t.Errorf("acted = %+v", res.Acted)
			}
		}},
		{"collapse", act(uaNode, domain.UIActionCollapse), func(t *testing.T, e *ctlEnv, res Result) {
			if res.Acted.Changed || res.Acted.State != domain.ExpandStateCollapsed {
				t.Errorf("collapse of a collapsed node: %+v", res.Acted)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newActionEnv(t)
			e.input.SetCursor(domain.Point{X: 123, Y: 456})
			fgBefore, err := e.windows.Foreground()
			if err != nil {
				t.Fatal(err)
			}
			e.input.Reset()
			e.windows.Reset()

			res, err := e.control.Do(ctx, ctlAlice, tt.op)
			if err != nil || res.Acted == nil {
				t.Fatalf("Do = %+v, %v", res, err)
			}
			tt.check(t, e, res)
			requireNoInputSideEffects(t, e)

			if got, _ := e.input.CursorPos(); got != (domain.Point{X: 123, Y: 456}) {
				t.Errorf("the cursor moved to %v", got)
			}
			fgAfter, _ := e.windows.Foreground()
			if fgAfter.Foreground.ID != fgBefore.Foreground.ID {
				t.Errorf("the foreground window changed from %s to %s", fgBefore.Foreground.ID, fgAfter.Foreground.ID)
			}
		})
	}
}

func TestControlUIActionRejections(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		op     domain.UIAction
		want   *domain.Error
		detail map[string]any
	}{
		{"pattern unsupported (set_value on a button)", actSetValue(uaSave, "x"), domain.ErrPatternUnsupported, map[string]any{"pattern": domain.PatternValue, "action": "set_value"}},
		{"pattern unsupported (invoke on a button without patterns)", act(uaPlain, domain.UIActionInvoke), domain.ErrPatternUnsupported, nil},
		{"disabled", act(uaDisabled, domain.UIActionInvoke), domain.ErrElementDisabled, nil},
		{"stale element id", act(99, domain.UIActionInvoke), domain.ErrStaleElement, nil},
		{"stale element id in a closed window", domain.UIAction{ElementID: uiaID(77, 1), Kind: domain.UIActionInvoke}, domain.ErrStaleElement, nil},
		{"not an element id", domain.UIAction{ElementID: "nonsense", Kind: domain.UIActionInvoke}, domain.ErrInvalidArgument, nil},
		{"unknown action", domain.UIAction{ElementID: uiaID(1, uaSave), Kind: "click"}, domain.ErrInvalidArgument, nil},
		{"set_value without value", act(uaName, domain.UIActionSetValue), domain.ErrInvalidArgument, nil},
		{"value on invoke", func() domain.UIAction { a := act(uaSave, domain.UIActionInvoke); v := "x"; a.Value = &v; return a }(), domain.ErrInvalidArgument, nil},
		{"state on invoke", domain.UIAction{ElementID: uiaID(1, uaSave), Kind: domain.UIActionInvoke, State: "on"}, domain.ErrInvalidArgument, nil},
		{"bad toggle state", domain.UIAction{ElementID: uiaID(1, uaCheck), Kind: domain.UIActionToggle, State: "maybe"}, domain.ErrInvalidArgument, nil},
		{"value too long", actSetValue(uaName, strings.Repeat("あ", maxTypeTextRunes+1)), domain.ErrInvalidArgument, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newActionEnv(t)
			e.input.Reset()
			_, err := e.control.Do(ctx, ctlAlice, tt.op)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != tt.want.Code {
				t.Fatalf("error = %v, want %s", err, tt.want.Code)
			}
			for k, v := range tt.detail {
				if de.Detail[k] != v {
					t.Errorf("detail[%s] = %v, want %v", k, de.Detail[k], v)
				}
			}
			requireNoInputSideEffects(t, e)
		})
	}
}

func TestControlUIActionPatternUnsupportedListsTheSupportedPatterns(t *testing.T) {
	e := newActionEnv(t)
	_, err := e.control.Do(context.Background(), ctlAlice, act(uaName, domain.UIActionInvoke))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePatternUnsupported {
		t.Fatalf("error = %v", err)
	}
	got, _ := de.Detail["supported"].([]string)
	if len(got) != 2 || got[0] != domain.PatternValue || got[1] != domain.PatternText {
		t.Errorf("detail.supported = %v", de.Detail["supported"])
	}
}

func TestControlUIActionReadOnlyValue(t *testing.T) {
	e := newActionEnv(t)
	e.ui.SetReadOnly(uiaID(1, uaName), true)
	_, err := e.control.Do(context.Background(), ctlAlice, actSetValue(uaName, "x"))
	if !errors.Is(err, domain.ErrElementReadOnly) {
		t.Fatalf("error = %v, want element_read_only", err)
	}
	if _, ok := e.ui.Value(uiaID(1, uaName)); ok {
		t.Error("a value was set on a read-only element")
	}
}

func TestControlUIActionIsGatedLikeInput(t *testing.T) {
	ctx := context.Background()
	op := act(uaSave, domain.UIActionInvoke)

	t.Run("kill switch", func(t *testing.T) {
		e := newActionEnv(t)
		if err := e.kill.Trigger("test"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.control.Do(ctx, ctlAlice, op); !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("error = %v", err)
		}
		if _, ok := e.leases.Holder(); ok {
			t.Error("an implicit lease was left behind")
		}
		if len(e.ui.CallsTo("Act")) != 0 {
			t.Error("Act ran after the kill switch")
		}
	})
	t.Run("lease held by another session", func(t *testing.T) {
		e := newActionEnv(t)
		mustAcquire(t, e, ctlBob)
		if _, err := e.control.Do(ctx, ctlAlice, op); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Fatalf("error = %v", err)
		}
		if len(e.ui.CallsTo("Act")) != 0 {
			t.Error("Act ran without the lease")
		}
	})
	t.Run("implicit lease for mcp", func(t *testing.T) {
		e := newActionEnv(t)
		if _, err := e.control.Do(ctx, ctlAlice, op); err != nil {
			t.Fatal(err)
		}
		if h, ok := e.leases.Holder(); !ok || !h.Actor.SameSession(ctlAlice) {
			t.Errorf("holder = %+v, %v", h, ok)
		}
	})
	t.Run("web without a lease", func(t *testing.T) {
		e := newActionEnv(t)
		web := domain.Actor{Kind: domain.ActorWeb, SessionID: "v"}
		if _, err := e.control.Do(ctx, web, op); !errors.Is(err, domain.ErrLeaseRequired) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("revoked lease", func(t *testing.T) {
		e := newActionEnv(t)
		mustAcquire(t, e, ctlAlice)
		if err := e.leases.Revoke(HostActor); err != nil {
			t.Fatal(err)
		}
		if _, err := e.control.Do(ctx, ctlAlice, op); !errors.Is(err, domain.ErrLeaseRevoked) {
			t.Fatalf("error = %v", err)
		}
		if len(e.ui.CallsTo("Act")) != 0 {
			t.Error("Act ran with a revoked lease")
		}
	})
}

func TestControlUIActionBlockedWindows(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		window uint64
		reason string
	}{
		{"own window", 2, domain.ReasonOwnWindow},
		{"elevated window", 3, domain.ReasonElevatedWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newActionEnv(t)
			e.ui.SetTree(strconv.FormatUint(tt.window, 10), domain.UINode{
				ID: uiaID(tt.window, 1), Role: "Window", Enabled: true,
				Children: []domain.UINode{{ID: uiaID(tt.window, 2), Role: "Button", Name: "B", Enabled: true, Patterns: []string{domain.PatternInvoke}}},
			})
			_, err := e.control.Do(ctx, ctlAlice, domain.UIAction{ElementID: uiaID(tt.window, 2), Kind: domain.UIActionInvoke})
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked || de.Detail["reason"] != tt.reason {
				t.Fatalf("error = %v, want input_blocked %s", err, tt.reason)
			}
			if len(e.ui.CallsTo("Act")) != 0 {
				t.Error("Act ran against a blocked window")
			}
		})
	}
	t.Run("own click-through overlay is blocked too", func(t *testing.T) {
		e := newActionEnv(t)
		e.windows.SetWindows(domain.WindowInfo{ID: "5", Title: "overlay", Own: true, Overlay: true, Foreground: true})
		e.ui.SetTree("5", domain.UINode{ID: uiaID(5, 1), Role: "Window", Enabled: true,
			Children: []domain.UINode{{ID: uiaID(5, 2), Role: "Button", Enabled: true, Patterns: []string{domain.PatternInvoke}}}})
		_, err := e.control.Do(ctx, ctlAlice, domain.UIAction{ElementID: uiaID(5, 2), Kind: domain.UIActionInvoke})
		if !errors.Is(err, domain.ErrInputBlocked) {
			t.Fatalf("error = %v, want input_blocked", err)
		}
	})
}

func TestControlUIActionIsAudited(t *testing.T) {
	ctx := context.Background()
	find := func(e *ctlEnv) map[string]any {
		t.Helper()
		var found map[string]any
		n := 0
		for _, en := range e.audit.Entries() {
			if en.Event != "tool.ui_action" {
				continue
			}
			n++
			found = detailOf(t, en)
			found["_result"] = en.Result
			found["_tool"] = en.Tool
		}
		if n != 1 {
			t.Fatalf("tool.ui_action entries = %d, want 1", n)
		}
		return found
	}

	t.Run("invoke records the action and the element, not the name", func(t *testing.T) {
		e := newActionEnv(t)
		if _, err := e.control.Do(ctx, ctlAlice, act(uaSave, domain.UIActionInvoke)); err != nil {
			t.Fatal(err)
		}
		d := find(e)
		if d["action"] != "invoke" || d["control_type"] != "Button" || d["automation_id"] != "SaveButton" || d["element_id"] != uiaID(1, uaSave) ||
			d["_result"] != "ok" || d["_tool"] != "ui_action" {
			t.Errorf("detail = %v", d)
		}
		if _, ok := d["name"]; ok {
			t.Errorf("the element name was recorded: %v", d)
		}
	})
	t.Run("set_value records only the length", func(t *testing.T) {
		e := newActionEnv(t)
		if _, err := e.control.Do(ctx, ctlAlice, actSetValue(uaName, "hunter2-あ")); err != nil {
			t.Fatal(err)
		}
		d := find(e)
		if d["text_len"] != float64(9) {
			t.Errorf("text_len = %v", d["text_len"])
		}
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), "hunter2") {
			t.Errorf("the value leaked into the audit: %s", raw)
		}
	})
	t.Run("record_text on records the text, except for a password field", func(t *testing.T) {
		e := newActionEnv(t)
		e.settings.put(settingAuditRecordText, "true")
		if _, err := e.control.Do(ctx, ctlAlice, actSetValue(uaName, "visible-text")); err != nil {
			t.Fatal(err)
		}
		if d := find(e); d["text"] != "visible-text" {
			t.Errorf("text = %v", d["text"])
		}
		e = newActionEnv(t)
		e.settings.put(settingAuditRecordText, "true")
		if _, err := e.control.Do(ctx, ctlAlice, actSetValue(uaPassword, "s3cret-pw")); err != nil {
			t.Fatal(err)
		}
		d := find(e)
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), "s3cret") || d["text_len"] != float64(9) {
			t.Errorf("a password value reached the audit: %s", raw)
		}
	})
	t.Run("a failed action is audited with the element and the error", func(t *testing.T) {
		e := newActionEnv(t)
		if _, err := e.control.Do(ctx, ctlAlice, actSetValue(uaSave, "x")); !errors.Is(err, domain.ErrPatternUnsupported) {
			t.Fatalf("error = %v", err)
		}
		var entry map[string]any
		for _, en := range e.audit.Entries() {
			if en.Event == "tool.ui_action" {
				entry = detailOf(t, en)
				if en.Result != "error" || en.ErrorCode != string(domain.CodePatternUnsupported) {
					t.Errorf("result = %s, error code = %s", en.Result, en.ErrorCode)
				}
			}
		}
		if entry["control_type"] != "Button" || entry["automation_id"] != "SaveButton" || entry["action"] != "set_value" {
			t.Errorf("detail = %v", entry)
		}
	})
	t.Run("a stale element is audited without element details", func(t *testing.T) {
		e := newActionEnv(t)
		_, _ = e.control.Do(ctx, ctlAlice, act(99, domain.UIActionInvoke))
		d := find(e)
		if _, ok := d["control_type"]; ok || d["_result"] == "ok" {
			t.Errorf("detail = %v", d)
		}
	})
}

func TestUIActionIsAScreenChangingOpButNotPausedByHostInput(t *testing.T) {
	op := domain.UIAction{}
	if !opChangesScreen(op) {
		t.Error("ui_action is not treated as a screen-changing op (no audit thumbnail)")
	}
	if domain.OpPausedByHostInput(op) {
		t.Error("ui_action must not be paused by host input: it moves neither the cursor nor the focus")
	}
}

// スコープ付きのリース（#156）: 要素のウィンドウが G の外なら、FocusWindow と同じくその操作だけ拒否する。
func TestControlUIActionUnderAScopedLease(t *testing.T) {
	setup := func(t *testing.T) (*tgtEnv, string, string) {
		te := newTgtEnv(t)
		hwnd, err := strconv.ParseUint(platformtest.FakeAppMainID, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		tID := domain.ElementRef{HWND: hwnd, PID: platformtest.FakeAppPID, RuntimeID: []int32{42, 2}}.String()
		te.ui.SetTree(platformtest.FakeAppMainID, domain.UINode{
			ID: domain.ElementRef{HWND: hwnd, PID: platformtest.FakeAppPID, RuntimeID: []int32{42, 1}}.String(), Role: "Window", Enabled: true,
			Children: []domain.UINode{{ID: tID, Role: "Button", Name: "OK", Enabled: true, Patterns: []string{domain.PatternInvoke}}},
		})
		te.windows.Add(otherWindow("2001", rectOutside, false))
		otherID := domain.ElementRef{HWND: 2001, PID: 7000, RuntimeID: []int32{42, 2}}.String()
		te.ui.SetTree("2001", domain.UINode{
			ID: domain.ElementRef{HWND: 2001, PID: 7000, RuntimeID: []int32{42, 1}}.String(), Role: "Window", Enabled: true,
			Children: []domain.UINode{{ID: otherID, Role: "Button", Name: "Other", Enabled: true, Patterns: []string{domain.PatternInvoke}}},
		})
		te.acquire(ctlAlice)
		te.input.Reset()
		return te, tID, otherID
	}

	t.Run("an element in T works, with no input and no thumbnail source change", func(t *testing.T) {
		te, tID, _ := setup(t)
		if err := te.do(ctlAlice, domain.UIAction{ElementID: tID, Kind: domain.UIActionInvoke}); err != nil {
			t.Fatalf("Do: %v", err)
		}
		if calls := te.input.Calls(); len(injections(te.ctlEnv)) != 0 {
			t.Errorf("input was injected: %v", calls)
		}
		if s := te.scope(); s.State != domain.ScopeActive {
			t.Errorf("scope = %+v", s)
		}
	})
	t.Run("an element in a window outside G is refused for that operation only", func(t *testing.T) {
		te, _, otherID := setup(t)
		err := te.do(ctlAlice, domain.UIAction{ElementID: otherID, Kind: domain.UIActionInvoke})
		te.requireRefused(err, domain.ReasonOutsideTargetGroup)
		if len(te.ui.CallsTo("Act")) != 0 {
			t.Error("Act ran for an element outside the target group")
		}
	})
	t.Run("a halted lease refuses the action", func(t *testing.T) {
		te, tID, _ := setup(t)
		te.leases.Halt(te.scopeLeaseID(), domain.ReasonTargetNotForeground)
		err := te.do(ctlAlice, domain.UIAction{ElementID: tID, Kind: domain.UIActionInvoke})
		requireBlockedReason(t, err, domain.ReasonTargetHalted)
		if len(te.ui.CallsTo("Act")) != 0 {
			t.Error("Act ran under a halted lease")
		}
	})
	t.Run("T closed halts like input", func(t *testing.T) {
		te, tID, _ := setup(t)
		te.windows.Remove(platformtest.FakeAppMainID)
		err := te.do(ctlAlice, domain.UIAction{ElementID: tID, Kind: domain.UIActionInvoke})
		te.requireHalted(err, domain.ReasonTargetClosed)
		if len(te.ui.CallsTo("Act")) != 0 {
			t.Error("Act ran after T closed")
		}
	})
}

func (te *tgtEnv) scopeLeaseID() string {
	te.t.Helper()
	h, ok := te.leases.Holder()
	if !ok {
		te.t.Fatal("no lease holder")
	}
	return h.LeaseID
}
