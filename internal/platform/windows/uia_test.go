package windows

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// fakeUIABackend は hwnd ごとの木を返す。
type fakeUIABackend struct {
	trees map[uintptr]func() *fakeUIANode
	opens atomic.Int32
}

func (b *fakeUIABackend) Root(hwnd uintptr) (uiaNode, error) {
	b.opens.Add(1)
	mk, ok := b.trees[hwnd]
	if !ok {
		return nil, domain.ErrWindowNotFound.WithMsg("gone")
	}
	return mk(), nil
}

func uiaFixtureWindows() *platformtest.FakeWindows {
	return platformtest.NewFakeWindows(
		domain.WindowInfo{ID: "100", Title: "Notepad", Process: "notepad.exe", PID: 7, Foreground: true},
		domain.WindowInfo{ID: "200", Title: "Other", Process: "other.exe", PID: 8},
	)
}

func uiaFixtureBackend() *fakeUIABackend {
	return &fakeUIABackend{trees: map[uintptr]func() *fakeUIANode{
		100: func() *fakeUIANode {
			w := uiaTestNode("Notepad", uiaTestNode("File", uiaTestNode("Open")), uiaTestNode("Edit"), uiaTestNode("Help"))
			w.props.ControlType = 50032
			uiaAssignRuntimeIDs(w)
			return w
		},
		200: func() *fakeUIANode { return uiaAssignRuntimeIDs(uiaTestNode("Other")) },
	}}
}

// uiaAssignRuntimeIDs は root の木の各節点に、一意な RuntimeId（42, 1 始まりの通し番号）を付けて root を返す。
func uiaAssignRuntimeIDs(root *fakeUIANode) *fakeUIANode {
	for i, n := range root.all() {
		n.props.RuntimeID = []int32{42, int32(i + 1)}
	}
	return root
}

func newFakeUIAutomation(t *testing.T, windows platform.WindowManager, backend uiaBackend) *UIAutomation {
	t.Helper()
	u, err := newUIAutomation(windows, func() (uiaBackend, func(), error) { return backend, func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.Close)
	return u
}

func TestUIAutomationContract(t *testing.T) {
	platformtest.RunUIAutomationContract(t, func(t *testing.T) platform.UIAutomation {
		return newFakeUIAutomation(t, uiaFixtureWindows(), uiaFixtureBackend())
	})
}

func TestUIAutomationTargetsTheRequestedWindow(t *testing.T) {
	u := newFakeUIAutomation(t, uiaFixtureWindows(), uiaFixtureBackend())
	ctx := context.Background()

	fg, err := u.Tree(ctx, "", 5, 100)
	if err != nil || fg.Name != "Notepad" {
		t.Fatalf("foreground tree = %q, %v; want Notepad", fg.Name, err)
	}
	other, err := u.Tree(ctx, "200", 5, 100)
	if err != nil || other.Name != "Other" {
		t.Fatalf("tree(200) = %q, %v; want Other", other.Name, err)
	}
	snap, err := u.Snapshot(ctx, "200", 100)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Window.ID != "200" || snap.Window.Process != "other.exe" || len(snap.Elements) != 1 || snap.Hash == "" {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestUIAutomationWindowGoneBetweenListAndOpen(t *testing.T) {
	windows := platformtest.NewFakeWindows(domain.WindowInfo{ID: "300", Foreground: true}) // backend に木が無い
	u := newFakeUIAutomation(t, windows, uiaFixtureBackend())
	_, err := u.Tree(context.Background(), "300", 5, 10)
	if !errors.Is(err, domain.ErrWindowNotFound) {
		t.Errorf("error = %v, want ErrWindowNotFound", err)
	}
}

func TestUIAutomationNonHandleWindowID(t *testing.T) {
	windows := platformtest.NewFakeWindows(domain.WindowInfo{ID: "not-a-handle", Foreground: true})
	backend := uiaFixtureBackend()
	u := newFakeUIAutomation(t, windows, backend)
	_, err := u.Snapshot(context.Background(), "", 10)
	if !errors.Is(err, domain.ErrWindowNotFound) {
		t.Errorf("error = %v, want ErrWindowNotFound", err)
	}
	if backend.opens.Load() != 0 {
		t.Error("the backend was opened for a window id that is not a handle")
	}
}

func TestUIAutomationNoForegroundWindow(t *testing.T) {
	u := newFakeUIAutomation(t, platformtest.NewFakeWindows(), uiaFixtureBackend())
	_, err := u.Tree(context.Background(), "", 5, 10)
	if !errors.Is(err, domain.ErrDesktopUnavailable) {
		t.Errorf("error = %v, want the window manager's ErrDesktopUnavailable", err)
	}
}

func TestUIAutomationInvalidLimitsDoNotTouchTheBackend(t *testing.T) {
	backend := uiaFixtureBackend()
	u := newFakeUIAutomation(t, uiaFixtureWindows(), backend)
	if _, err := u.Tree(context.Background(), "", 0, 10); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Tree error = %v", err)
	}
	if _, err := u.Snapshot(context.Background(), "", 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Snapshot error = %v", err)
	}
	if backend.opens.Load() != 0 {
		t.Error("the backend was opened for invalid limits")
	}
}

func TestUIAutomationCloseIsIdempotentAndStopsCalls(t *testing.T) {
	u, err := newUIAutomation(uiaFixtureWindows(), func() (uiaBackend, func(), error) {
		return uiaFixtureBackend(), func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	u.Close()
	if _, err := u.Tree(context.Background(), "", 5, 10); !errors.Is(err, errUIAClosed) {
		t.Errorf("error after Close = %v, want errUIAClosed", err)
	}
}

func TestUIAutomationOpenFailure(t *testing.T) {
	boom := errors.New("no com")
	if _, err := newUIAutomation(uiaFixtureWindows(), func() (uiaBackend, func(), error) { return nil, nil, boom }); !errors.Is(err, boom) {
		t.Errorf("error = %v, want boom", err)
	}
}

// uiaFindFixture は Find・Resolve のテスト用: ウィンドウ 100（PID 7）に RuntimeId つきの木を持つ。
func uiaFindFixture() (windows *platformtest.FakeWindows, backend *fakeUIABackend) {
	windows = uiaFixtureWindows()
	backend = &fakeUIABackend{trees: map[uintptr]func() *fakeUIANode{
		100: func() *fakeUIANode {
			root := uiaTestNode("Notepad", uiaTestNode("Save", uiaTestNode("Inner")), uiaTestNode("Save As"), uiaTestNode("Body"))
			root.props.ControlType = 50032
			root.kids[0].props.AutomationID = "SaveButton"
			root.kids[0].props.ClassName = "Button"
			root.kids[0].props.Patterns = []string{domain.PatternInvoke}
			root.kids[1].props.ControlType = 50011
			root.kids[2].props.ControlType = 50004
			return uiaAssignRuntimeIDs(root)
		},
		200: func() *fakeUIANode { return uiaAssignRuntimeIDs(uiaTestNode("Other")) },
	}}
	return windows, backend
}

func TestUIAutomationFindMatchesAndIDs(t *testing.T) {
	windows, backend := uiaFindFixture()
	u := newFakeUIAutomation(t, windows, backend)
	ctx := context.Background()

	res, err := u.Find(ctx, "100", func(e domain.UIElementInfo) bool { return e.AutomationID == "SaveButton" }, 10, 100)
	if err != nil || len(res.Elements) != 1 || res.Truncated || res.Window.ID != "100" {
		t.Fatalf("Find = %+v, %v", res, err)
	}
	e := res.Elements[0]
	wantID := domain.ElementRef{HWND: 100, PID: 7, RuntimeID: []int32{42, 2}}.String()
	if e.ID != wantID || e.Name != "Save" || e.ControlType != "Button" || e.ClassName != "Button" || !slices.Equal(e.Patterns, []string{"Invoke"}) {
		t.Errorf("element = %+v, want id %s", e, wantID)
	}

	// match が受け取る要素には ID がまだ無い。
	_, err = u.Find(ctx, "100", func(e domain.UIElementInfo) bool {
		if e.ID != "" {
			t.Errorf("match saw id %q", e.ID)
		}
		return false
	}, 10, 100)
	if err != nil {
		t.Fatal(err)
	}

	// 浅い階層から返し、maxResults で止めたら Truncated。
	all, err := u.Find(ctx, "100", func(domain.UIElementInfo) bool { return true }, 3, 100)
	if err != nil || len(all.Elements) != 3 || !all.Truncated {
		t.Fatalf("Find(3) = %d elements, Truncated %v, %v", len(all.Elements), all.Truncated, err)
	}
	if names := []string{all.Elements[0].Name, all.Elements[1].Name, all.Elements[2].Name}; !slices.Equal(names, []string{"Notepad", "Save", "Save As"}) {
		t.Errorf("order = %v, want breadth first", names)
	}
}

func TestUIAutomationResolve(t *testing.T) {
	windows, backend := uiaFindFixture()
	u := newFakeUIAutomation(t, windows, backend)
	ctx := context.Background()
	ref := func(id int32) domain.ElementRef {
		return domain.ElementRef{HWND: 100, PID: 7, RuntimeID: []int32{42, id}}
	}

	got, err := u.Resolve(ctx, ref(3)) // Inner（深さ 2）
	if err != nil || got.Name != "Inner" || got.ID != ref(3).String() {
		t.Fatalf("Resolve = %+v, %v", got, err)
	}

	stale := map[string]domain.ElementRef{
		"element is gone":      ref(99),
		"window is gone":       {HWND: 300, PID: 7, RuntimeID: []int32{42, 3}},
		"process changed":      {HWND: 100, PID: 8, RuntimeID: []int32{42, 3}},
		"no runtime id":        {HWND: 100, PID: 7},
		"other window's index": {HWND: 200, PID: 8, RuntimeID: []int32{42, 3}},
	}
	for name, r := range stale {
		if _, err := u.Resolve(ctx, r); !errors.Is(err, domain.ErrStaleElement) {
			t.Errorf("%s: error = %v, want ErrStaleElement", name, err)
		}
	}

	// ウィンドウが一覧にあっても、UIA が開けない（閉じ始め）なら古い。
	delete(backend.trees, 100)
	if _, err := u.Resolve(ctx, ref(3)); !errors.Is(err, domain.ErrStaleElement) {
		t.Errorf("window closed under UIA: error = %v, want ErrStaleElement", err)
	}
}

func TestUIAutomationFindLimitsDoNotTouchTheBackend(t *testing.T) {
	windows, backend := uiaFindFixture()
	u := newFakeUIAutomation(t, windows, backend)
	all := func(domain.UIElementInfo) bool { return true }
	if _, err := u.Find(context.Background(), "", all, 0, 10); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Find(0 results) error = %v", err)
	}
	if backend.opens.Load() != 0 {
		t.Error("the backend was opened for invalid limits")
	}
}
