package windows

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func actProps(patterns ...string) uiaProps {
	return uiaProps{ControlType: 50000, Name: "n", AutomationID: "aid", Enabled: true, Patterns: patterns, RuntimeID: []int32{42, 7}}
}

var actRef = domain.ElementRef{HWND: 100, PID: 7, RuntimeID: []int32{42, 7}}

func TestUIAPerformEachAction(t *testing.T) {
	tests := []struct {
		name     string
		act      domain.UIAction
		pattern  string
		setup    func(n *fakeUIANode)
		wantActs []string
		changed  bool
		state    string
	}{
		{"invoke", domain.UIAction{Kind: domain.UIActionInvoke}, domain.PatternInvoke, nil, []string{"Invoke"}, true, ""},
		{"set_value", domain.UIAction{Kind: domain.UIActionSetValue, Value: new("hello")}, domain.PatternValue, nil, []string{"SetValue"}, true, ""},
		{"toggle flips", domain.UIAction{Kind: domain.UIActionToggle}, domain.PatternToggle, nil, []string{"Toggle"}, true, domain.ToggleStateOn},
		{"toggle on from off", domain.UIAction{Kind: domain.UIActionToggle, State: domain.ToggleStateOn}, domain.PatternToggle, nil, []string{"Toggle"}, true, domain.ToggleStateOn},
		{"toggle on when already on", domain.UIAction{Kind: domain.UIActionToggle, State: domain.ToggleStateOn}, domain.PatternToggle,
			func(n *fakeUIANode) { n.toggle = uiaToggleOn }, nil, false, domain.ToggleStateOn},
		{"toggle on from indeterminate goes through off", domain.UIAction{Kind: domain.UIActionToggle, State: domain.ToggleStateOn}, domain.PatternToggle,
			func(n *fakeUIANode) { n.toggle = uiaToggleIndeterminate }, []string{"Toggle", "Toggle"}, true, domain.ToggleStateOn},
		{"toggle off from on", domain.UIAction{Kind: domain.UIActionToggle, State: domain.ToggleStateOff}, domain.PatternToggle,
			func(n *fakeUIANode) { n.toggle = uiaToggleOn }, []string{"Toggle", "Toggle"}, true, domain.ToggleStateOff},
		{"select", domain.UIAction{Kind: domain.UIActionSelect}, domain.PatternSelectionItem, nil, []string{"Select"}, true, domain.SelectStateSelected},
		{"expand", domain.UIAction{Kind: domain.UIActionExpand}, domain.PatternExpandCollapse, nil, []string{"Expand"}, true, domain.ExpandStateExpanded},
		{"expand when expanded", domain.UIAction{Kind: domain.UIActionExpand}, domain.PatternExpandCollapse,
			func(n *fakeUIANode) { n.expand = uiaExpandExpanded }, nil, false, domain.ExpandStateExpanded},
		{"collapse", domain.UIAction{Kind: domain.UIActionCollapse}, domain.PatternExpandCollapse,
			func(n *fakeUIANode) { n.expand = uiaExpandExpanded }, []string{"Collapse"}, true, domain.ExpandStateCollapsed},
		{"collapse when collapsed", domain.UIAction{Kind: domain.UIActionCollapse}, domain.PatternExpandCollapse, nil, nil, false, domain.ExpandStateCollapsed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &fakeUIANode{props: actProps(tt.pattern)}
			if tt.setup != nil {
				tt.setup(n)
			}
			res, err := uiaPerform(n, n.props, actRef, tt.act)
			if err != nil {
				t.Fatalf("uiaPerform: %v", err)
			}
			if !slices.Equal(n.acts, tt.wantActs) {
				t.Errorf("pattern calls = %v, want %v", n.acts, tt.wantActs)
			}
			if res.Changed != tt.changed || res.State != tt.state {
				t.Errorf("changed = %v, state = %q; want %v, %q", res.Changed, res.State, tt.changed, tt.state)
			}
			if res.Element.ID != actRef.String() || res.Element.ControlType != "Button" || res.Element.AutomationID != "aid" {
				t.Errorf("element = %+v", res.Element)
			}
		})
	}
}

func TestUIAPerformSetValuePassesTheText(t *testing.T) {
	n := &fakeUIANode{props: actProps(domain.PatternValue)}
	if _, err := uiaPerform(n, n.props, actRef, domain.UIAction{Kind: domain.UIActionSetValue, Value: new("日本語 text")}); err != nil {
		t.Fatal(err)
	}
	if n.value != "日本語 text" {
		t.Errorf("value = %q", n.value)
	}
}

func TestUIAPerformRejections(t *testing.T) {
	t.Run("disabled is checked before the pattern", func(t *testing.T) {
		p := actProps()
		p.Enabled = false
		n := &fakeUIANode{props: p}
		_, err := uiaPerform(n, p, actRef, domain.UIAction{Kind: domain.UIActionInvoke})
		if !errors.Is(err, domain.ErrElementDisabled) || len(n.acts) != 0 {
			t.Errorf("err = %v, acts = %v", err, n.acts)
		}
	})
	t.Run("missing pattern lists what is supported", func(t *testing.T) {
		n := &fakeUIANode{props: actProps(domain.PatternValue, domain.PatternText)}
		res, err := uiaPerform(n, n.props, actRef, domain.UIAction{Kind: domain.UIActionInvoke})
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodePatternUnsupported {
			t.Fatalf("err = %v, want pattern_unsupported", err)
		}
		supported, _ := de.Detail["supported"].([]string)
		if de.Detail["pattern"] != domain.PatternInvoke || de.Detail["action"] != "invoke" || !slices.Equal(supported, []string{domain.PatternValue, domain.PatternText}) {
			t.Errorf("detail = %v", de.Detail)
		}
		if len(n.acts) != 0 {
			t.Errorf("a pattern call was made: %v", n.acts)
		}
		if res.Element.ControlType != "Button" {
			t.Errorf("the failed result must still name the element: %+v", res.Element)
		}
	})
	t.Run("read-only value", func(t *testing.T) {
		n := &fakeUIANode{props: actProps(domain.PatternValue), readOnly: true}
		_, err := uiaPerform(n, n.props, actRef, domain.UIAction{Kind: domain.UIActionSetValue, Value: new("x")})
		if !errors.Is(err, domain.ErrElementReadOnly) || len(n.acts) != 0 {
			t.Errorf("err = %v, acts = %v", err, n.acts)
		}
	})
	t.Run("password is flagged and still settable", func(t *testing.T) {
		p := actProps(domain.PatternValue)
		p.IsPassword = true
		n := &fakeUIANode{props: p}
		res, err := uiaPerform(n, p, actRef, domain.UIAction{Kind: domain.UIActionSetValue, Value: new("s3cret")})
		if err != nil || !res.Password {
			t.Errorf("err = %v, password = %v", err, res.Password)
		}
	})
	t.Run("leaf node cannot expand", func(t *testing.T) {
		n := &fakeUIANode{props: actProps(domain.PatternExpandCollapse), expand: uiaExpandLeafNode}
		_, err := uiaPerform(n, n.props, actRef, domain.UIAction{Kind: domain.UIActionExpand})
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeUIActionFailed || de.Detail["reason"] != "leaf_node" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the actor's error is returned", func(t *testing.T) {
		n := &fakeUIANode{props: actProps(domain.PatternInvoke), actErr: domain.ErrInputBlocked}
		_, err := uiaPerform(n, n.props, actRef, domain.UIAction{Kind: domain.UIActionInvoke})
		if !errors.Is(err, domain.ErrInputBlocked) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestUIAActionError(t *testing.T) {
	tests := []struct {
		code   uint32
		want   domain.Code
		reason string
	}{
		{uiaHRElementNotAvailable, domain.CodeStaleElement, ""},
		{uiaHRInvalidWindow, domain.CodeStaleElement, ""},
		{uiaHRRPCDisconnected, domain.CodeStaleElement, ""},
		{uiaHRAccessDenied, domain.CodeInputBlocked, "access_denied"},
		{uiaHRElementNotEnabled, domain.CodeElementDisabled, ""},
		{uiaHRNoInterface, domain.CodePatternUnsupported, ""},
		{uiaHRNotSupported, domain.CodePatternUnsupported, ""},
		{uiaHRInvalidOperation, domain.CodeUIActionFailed, "invalid_operation"},
		{uiaHRTimeout, domain.CodeUIActionFailed, "timeout"},
		{0x80004005, domain.CodeUIActionFailed, "failed"},
	}
	for _, tt := range tests {
		err := uiaActionError(tt.code, "Invoke")
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != tt.want {
			t.Errorf("code %#x: err = %v, want %s", tt.code, err, tt.want)
			continue
		}
		if tt.reason != "" && de.Detail["reason"] != tt.reason {
			t.Errorf("code %#x: reason = %v, want %s", tt.code, de.Detail["reason"], tt.reason)
		}
	}
}

func TestUIAFindLiveReleasesEverythingButTheMatch(t *testing.T) {
	root := uiaAssignRuntimeIDs(uiaSampleTree())
	root.obtained = true
	want := root.all()[5] // a2 の次あたり。幅優先でなくても、RuntimeId が一意なら引ける
	got, err := uiaFindLive(context.Background(), root, want.props.RuntimeID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got.(*fakeUIANode) != want {
		t.Fatalf("found %q, want %q", got.(*fakeUIANode).props.Name, want.props.Name)
	}
	for _, n := range root.all() {
		switch {
		case n == want && n.released != 0:
			t.Errorf("the returned node %q was released %d times", n.props.Name, n.released)
		case n != want && n.obtained && n.released != 1:
			t.Errorf("node %q released %d times", n.props.Name, n.released)
		case !n.obtained && n.released != 0:
			t.Errorf("node %q never obtained but released %d times", n.props.Name, n.released)
		}
	}
}

func TestUIAFindLiveStaleAndBounded(t *testing.T) {
	root := uiaAssignRuntimeIDs(uiaSampleTree())
	root.obtained = true
	if _, err := uiaFindLive(context.Background(), root, []int32{9, 9}, 100); !errors.Is(err, domain.ErrStaleElement) {
		t.Fatalf("err = %v, want stale_element", err)
	}
	for _, n := range root.all() {
		if n.obtained && n.released != 1 {
			t.Errorf("node %q released %d times", n.props.Name, n.released)
		}
	}
	// 走査の上限を超えた先の要素は見つからない（stale）。
	root = uiaAssignRuntimeIDs(uiaSampleTree())
	root.obtained = true
	deep := root.all()[len(root.all())-1]
	if _, err := uiaFindLive(context.Background(), root, deep.props.RuntimeID, 2); !errors.Is(err, domain.ErrStaleElement) {
		t.Fatalf("err = %v, want stale_element beyond the node cap", err)
	}
	for _, n := range root.all() {
		if n.obtained && n.released != 1 {
			t.Errorf("capped: node %q released %d times", n.props.Name, n.released)
		}
	}
}

func actBackend() *fakeUIABackend {
	return &fakeUIABackend{trees: map[uintptr]func() *fakeUIANode{
		100: func() *fakeUIANode {
			w := uiaTestNode("Notepad", uiaTestNode("Save"), uiaTestNode("Name"))
			w.props.ControlType = 50032
			uiaAssignRuntimeIDs(w)
			w.kids[0].props.Patterns = []string{domain.PatternInvoke}
			w.kids[1].props.Patterns = []string{domain.PatternValue}
			return w
		},
	}}
}

func TestUIAutomationActOnTheLiveElement(t *testing.T) {
	u := newFakeUIAutomation(t, uiaFixtureWindows(), actBackend())
	ctx := context.Background()
	save := domain.ElementRef{HWND: 100, PID: 7, RuntimeID: []int32{42, 2}}

	res, err := u.Act(ctx, save, domain.UIAction{ElementID: save.String(), Kind: domain.UIActionInvoke})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if res.Window.ID != "100" || res.Element.Name != "Save" || res.Element.ID != save.String() || !res.Changed {
		t.Errorf("result = %+v", res)
	}

	// パターンを持たない要素
	_, err = u.Act(ctx, save, domain.UIAction{Kind: domain.UIActionSetValue, Value: new("x")})
	if !errors.Is(err, domain.ErrPatternUnsupported) {
		t.Errorf("set_value on a button: %v", err)
	}
	// 失敗でも要素と窓を返す
	if res, _ := u.Act(ctx, save, domain.UIAction{Kind: domain.UIActionSetValue, Value: new("x")}); res.Element.Name != "Save" || res.Window.ID != "100" {
		t.Errorf("a failed Act must still identify the element: %+v", res)
	}
}

func TestUIAutomationActStale(t *testing.T) {
	u := newFakeUIAutomation(t, uiaFixtureWindows(), actBackend())
	ctx := context.Background()
	inv := domain.UIAction{Kind: domain.UIActionInvoke}
	for name, ref := range map[string]domain.ElementRef{
		"no runtime id":        {HWND: 100, PID: 7},
		"element is gone":      {HWND: 100, PID: 7, RuntimeID: []int32{42, 99}},
		"window is gone":       {HWND: 999, PID: 7, RuntimeID: []int32{42, 2}},
		"hwnd got another pid": {HWND: 100, PID: 8, RuntimeID: []int32{42, 2}},
		"closed after listing": {HWND: 200, PID: 8, RuntimeID: []int32{42, 1}}, // 一覧にはあるが backend に木が無い
	} {
		res, err := u.Act(ctx, ref, inv)
		if !errors.Is(err, domain.ErrStaleElement) {
			t.Errorf("%s: err = %v, want stale_element", name, err)
		}
		if res.Element.ID != "" {
			t.Errorf("%s: a stale element must not be reported: %+v", name, res)
		}
	}
}

func TestUIAutomationActCanceled(t *testing.T) {
	u := newFakeUIAutomation(t, uiaFixtureWindows(), actBackend())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref := domain.ElementRef{HWND: 100, PID: 7, RuntimeID: []int32{42, 2}}
	if _, err := u.Act(ctx, ref, domain.UIAction{Kind: domain.UIActionInvoke}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
