package windows

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// fakeUIANode は uiaNode の偽の実装。Release が 1 回ずつ呼ばれたかを数える。
type fakeUIANode struct {
	props    uiaProps
	kids     []*fakeUIANode
	obtained bool // 根として渡された、または Children で返された
	released int
	// childrenCalls は Children が呼ばれた回数。
	childrenCalls int
	childrenErr   error
}

func (n *fakeUIANode) Props() uiaProps { return n.props }
func (n *fakeUIANode) Release()        { n.released++ }
func (n *fakeUIANode) Children(ctx context.Context) ([]uiaNode, error) {
	n.childrenCalls++
	if n.childrenErr != nil {
		return nil, n.childrenErr
	}
	out := make([]uiaNode, len(n.kids))
	for i, k := range n.kids {
		k.obtained = true
		out[i] = k
	}
	return out, nil
}

// uiaTestNode は name の節点を作る。ControlType は Button。
func uiaTestNode(name string, kids ...*fakeUIANode) *fakeUIANode {
	return &fakeUIANode{props: uiaProps{ControlType: 50000, Name: name, Enabled: true}, kids: kids}
}

func (n *fakeUIANode) all() []*fakeUIANode {
	out := []*fakeUIANode{n}
	for _, k := range n.kids {
		out = append(out, k.all()...)
	}
	return out
}

// uiaRequireReleasedOnce は、取得した節点がちょうど 1 回ずつ解放されたことを検査する。
func uiaRequireReleasedOnce(t *testing.T, root *fakeUIANode) {
	t.Helper()
	root.obtained = true
	for _, n := range root.all() {
		switch {
		case n.obtained && n.released != 1:
			t.Errorf("node %q obtained but released %d times", n.props.Name, n.released)
		case !n.obtained && n.released != 0:
			t.Errorf("node %q never obtained but released %d times", n.props.Name, n.released)
		}
	}
}

func uiaCountTruncated(nodes []uiaWalked) int {
	c := 0
	for _, n := range nodes {
		if n.truncated {
			c++
		}
	}
	return c
}

func uiaWalkedNames(nodes []uiaWalked) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.props.Name
	}
	return out
}

func uiaSampleTree() *fakeUIANode {
	//        root
	//       /  |  \
	//      a   b   c
	//     / \      |
	//    a1 a2     c1
	//    |
	//    a11
	return uiaTestNode("root",
		uiaTestNode("a", uiaTestNode("a1", uiaTestNode("a11")), uiaTestNode("a2")),
		uiaTestNode("b"),
		uiaTestNode("c", uiaTestNode("c1")),
	)
}

func TestWalkUIAFullTree(t *testing.T) {
	root := uiaSampleTree()
	nodes, err := walkUIA(context.Background(), root, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	// 幅優先: 浅い節点から並ぶ
	want := []string{"root", "a", "b", "c", "a1", "a2", "c1", "a11"}
	if got := uiaWalkedNames(nodes); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if c := uiaCountTruncated(nodes); c != 0 {
		t.Errorf("%d nodes truncated in a full walk", c)
	}
	uiaRequireReleasedOnce(t, root)
}

func TestWalkUIAMaxDepth(t *testing.T) {
	tests := []struct {
		maxDepth int
		want     []string
		truncAt  []string
	}{
		{1, []string{"root", "a", "b", "c"}, []string{"a", "c"}}, // b は子なし
		{2, []string{"root", "a", "b", "c", "a1", "a2", "c1"}, []string{"a1"}},
		{3, []string{"root", "a", "b", "c", "a1", "a2", "c1", "a11"}, nil},
	}
	for _, tt := range tests {
		root := uiaSampleTree()
		nodes, err := walkUIA(context.Background(), root, tt.maxDepth, 100)
		if err != nil {
			t.Fatal(err)
		}
		if got := uiaWalkedNames(nodes); !slices.Equal(got, tt.want) {
			t.Errorf("maxDepth %d: nodes = %v, want %v", tt.maxDepth, got, tt.want)
		}
		var trunc []string
		for _, n := range nodes {
			if n.truncated {
				trunc = append(trunc, n.props.Name)
			}
		}
		if !slices.Equal(trunc, tt.truncAt) {
			t.Errorf("maxDepth %d: truncated = %v, want %v", tt.maxDepth, trunc, tt.truncAt)
		}
		uiaRequireReleasedOnce(t, root)
	}
}

func TestWalkUIAMaxNodes(t *testing.T) {
	root := uiaSampleTree()
	nodes, err := walkUIA(context.Background(), root, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := uiaWalkedNames(nodes), []string{"root", "a", "b", "c", "a1"}; !slices.Equal(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}
	// a は a1 を採り a2 を切った。b は子なし。c は c1 を切った。a1 は a11 を切った。root は全部採った。
	trunc := map[string]bool{}
	for _, n := range nodes {
		if n.truncated {
			trunc[n.props.Name] = true
		}
	}
	if want := map[string]bool{"a": true, "c": true, "a1": true}; len(trunc) != len(want) || !trunc["a"] || !trunc["c"] || !trunc["a1"] {
		t.Errorf("truncated = %v, want %v", trunc, want)
	}
	uiaRequireReleasedOnce(t, root)
}

func TestWalkUIAMaxNodesOne(t *testing.T) {
	root := uiaSampleTree()
	nodes, err := walkUIA(context.Background(), root, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !nodes[0].truncated {
		t.Errorf("nodes = %v truncated=%v, want only the root, truncated", uiaWalkedNames(nodes), nodes[0].truncated)
	}
	uiaRequireReleasedOnce(t, root)
}

func TestWalkUIARejectsNoBudget(t *testing.T) {
	root := uiaSampleTree()
	_, err := walkUIA(context.Background(), root, 5, 0)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("error = %v, want ErrInvalidArgument", err)
	}
	if root.released != 1 {
		t.Errorf("root released %d times, want 1", root.released)
	}
}

func TestWalkUIAChildrenErrorReleasesEverything(t *testing.T) {
	boom := errors.New("boom")
	root := uiaSampleTree()
	root.kids[1].childrenErr = boom // b
	_, err := walkUIA(context.Background(), root, 100, 100)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
	uiaRequireReleasedOnce(t, root)
}

func TestWalkUIACanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	root := uiaSampleTree()
	root.kids[0].childrenErr = nil
	cancel()
	_, err := walkUIA(ctx, root, 100, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	uiaRequireReleasedOnce(t, root)
}

func TestUIATreeRedactsPasswordAndKeepsValue(t *testing.T) {
	root := uiaTestNode("form", uiaTestNode("user"), uiaTestNode("pass"))
	root.kids[0].props.Value = "alice"
	root.kids[1].props.Value = "should-not-be-read"
	root.kids[1].props.IsPassword = true
	nodes, err := walkUIA(context.Background(), root, 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	tree := uiaTreeFromWalked(nodes)
	user, pass := tree.Children[0], tree.Children[1]
	if user.Value != "alice" || user.ValueRedacted {
		t.Errorf("user = value %q redacted %v", user.Value, user.ValueRedacted)
	}
	if pass.Value != "" || !pass.ValueRedacted {
		t.Errorf("password = value %q redacted %v; want empty and redacted", pass.Value, pass.ValueRedacted)
	}
}

func TestUIATreeShape(t *testing.T) {
	root := uiaTestNode("w")
	root.props.ControlType = 50032
	root.props.RuntimeID = []int32{42, 0x1f4, -3}
	root.props.Rect = domain.Rect{X: 1, Y: 2, W: 3, H: 4}
	root.props.Enabled = false
	root.kids = []*fakeUIANode{uiaTestNode("b")}
	nodes, err := walkUIA(context.Background(), root, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	tree := uiaTreeFromWalked(nodes)
	if tree.ID != "uia:2a.1f4.fffffffd" || tree.Role != "Window" || tree.Name != "w" || tree.Rect != root.props.Rect || tree.Enabled {
		t.Errorf("root = %+v", tree)
	}
	if len(tree.Children) != 1 || tree.Children[0].Role != "Button" || tree.Children[0].ID != "uia:n1" {
		t.Errorf("children = %+v", tree.Children)
	}
}

func TestUIARoleName(t *testing.T) {
	tests := map[int]string{50000: "Button", 50032: "Window", 50004: "Edit", 50040: "AppBar", 1: "Unknown"}
	for ct, want := range tests {
		if got := uiaRoleName(ct); got != want {
			t.Errorf("uiaRoleName(%d) = %q, want %q", ct, got, want)
		}
	}
}

// uiaSnapshotOf は tree の走査結果から要素とハッシュを作る。
func uiaSnapshotOf(t *testing.T, root *fakeUIANode) ([]domain.UIElement, string) {
	t.Helper()
	nodes, err := walkUIA(context.Background(), root, uiaUnlimitedDepth, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return uiaSnapshotFromWalked(nodes)
}

func TestUIASnapshotElements(t *testing.T) {
	window := uiaTestNode("  My   App ")
	window.props.ControlType = 50032
	title := &fakeUIANode{props: uiaProps{ControlType: 50037, Name: "My App", Enabled: true}}
	closeBtn := &fakeUIANode{props: uiaProps{ControlType: 50000, Name: "Close", AutomationID: "Close", Enabled: true}}
	title.kids = []*fakeUIANode{closeBtn}
	edit := &fakeUIANode{props: uiaProps{ControlType: 50004, Name: "x", Value: "x", Enabled: true}}
	pane := &fakeUIANode{props: uiaProps{ControlType: 50033, Name: "Content", Enabled: true}} // 役割クラスなし
	ok := &fakeUIANode{props: uiaProps{ControlType: 50000, Name: "OK", Enabled: true, Offscreen: true, Rect: domain.Rect{X: 5, Y: 6, W: 7, H: 8}}}
	pane.kids = []*fakeUIANode{ok}
	window.kids = []*fakeUIANode{title, edit, pane}

	elements, _ := uiaSnapshotOf(t, window)
	byName := map[string]domain.UIElement{}
	for _, e := range elements {
		byName[e.Name] = e
	}
	if len(elements) != 6 {
		t.Fatalf("%d elements, want 6", len(elements))
	}
	if root := elements[0]; root.ControlType != 50032 || len(root.Path) != 0 || root.Chrome {
		t.Errorf("root = %+v", root)
	}
	// 文書順（先行順）
	if got := []string{elements[1].Name, elements[2].Name, elements[3].Name, elements[4].Name, elements[5].Name}; !slices.Equal(got, []string{"My App", "Close", "x", "Content", "OK"}) {
		t.Errorf("order = %v", got)
	}
	if !byName["My App"].Chrome || !byName["Close"].Chrome {
		t.Errorf("title bar and its descendants must be Chrome: %+v %+v", byName["My App"], byName["Close"])
	}
	if byName["OK"].Chrome || byName["x"].Chrome {
		t.Error("content elements must not be Chrome")
	}
	if !byName["x"].SameAsValue || byName["OK"].SameAsValue {
		t.Error("SameAsValue should be true only for the edit whose name equals its value")
	}
	if got, want := byName["OK"].Path, []string{"window:My App"}; !slices.Equal(got, want) {
		t.Errorf("OK path = %v, want %v (Pane has no role class; whitespace is collapsed)", got, want)
	}
	if ok := byName["OK"]; !ok.Offscreen || ok.Rect != (domain.Rect{X: 5, Y: 6, W: 7, H: 8}) || !ok.Enabled || ok.ControlType != 50000 {
		t.Errorf("OK = %+v", ok)
	}
}

func TestUIASnapshotChromeButtonUnderRootWithoutTitleBar(t *testing.T) {
	window := uiaTestNode("w")
	window.props.ControlType = 50032
	window.kids = []*fakeUIANode{
		{props: uiaProps{ControlType: 50000, Name: "Minimize", AutomationID: "Minimize"}},
		uiaTestNode("Inner", &fakeUIANode{props: uiaProps{ControlType: 50000, Name: "Close", AutomationID: "Close"}}),
	}
	elements, _ := uiaSnapshotOf(t, window)
	chrome := map[string]bool{}
	for _, e := range elements {
		chrome[e.Name] = e.Chrome
	}
	if !chrome["Minimize"] {
		t.Error("a Minimize button directly under the window must be Chrome")
	}
	if chrome["Close"] {
		t.Error("a Close button deep in the content (a dialog's own button) must not be Chrome")
	}
}

func TestUIASnapshotPathIsCopied(t *testing.T) {
	window := uiaTestNode("w")
	window.props.ControlType = 50032
	window.kids = []*fakeUIANode{uiaTestNode("a"), uiaTestNode("b")}
	elements, _ := uiaSnapshotOf(t, window)
	elements[1].Path[0] = "mutated"
	if elements[2].Path[0] == "mutated" {
		t.Error("elements share one Path backing array")
	}
}

func TestUIASnapshotHash(t *testing.T) {
	build := func(mutate func(*fakeUIANode)) string {
		w := uiaTestNode("w", uiaTestNode("a"), uiaTestNode("b"))
		w.props.ControlType = 50032
		if mutate != nil {
			mutate(w)
		}
		_, h := uiaSnapshotOf(t, w)
		return h
	}
	base := build(nil)
	if base == "" || base != build(nil) {
		t.Fatalf("hash is empty or not deterministic: %q", base)
	}
	changed := map[string]func(*fakeUIANode){
		"name":      func(w *fakeUIANode) { w.kids[0].props.Name = "z" },
		"enabled":   func(w *fakeUIANode) { w.kids[0].props.Enabled = false },
		"offscreen": func(w *fakeUIANode) { w.kids[0].props.Offscreen = true },
		"rect":      func(w *fakeUIANode) { w.kids[0].props.Rect.W = 9 },
		"toggle":    func(w *fakeUIANode) { w.kids[0].props.ToggleState = 1 },
		"selected":  func(w *fakeUIANode) { w.kids[0].props.Selected = true },
		"type":      func(w *fakeUIANode) { w.kids[0].props.ControlType = 50002 },
		"shape":     func(w *fakeUIANode) { w.kids[1].kids = []*fakeUIANode{uiaTestNode("c")} },
		"reparent": func(w *fakeUIANode) {
			// 同じ並びでも木の形が違えば別のハッシュ（a の子として b を置く）
			w.kids[0].kids = []*fakeUIANode{w.kids[1]}
			w.kids = w.kids[:1]
		},
	}
	for what, mutate := range changed {
		if build(mutate) == base {
			t.Errorf("hash did not change when %s changed", what)
		}
	}
	for what, mutate := range map[string]func(*fakeUIANode){
		"value":      func(w *fakeUIANode) { w.kids[0].props.Value = "secret text" },
		"runtime id": func(w *fakeUIANode) { w.kids[0].props.RuntimeID = []int32{1, 2} },
		"automation": func(w *fakeUIANode) { w.kids[0].props.AutomationID = "id" },
	} {
		if build(mutate) != base {
			t.Errorf("hash changed when %s changed; it must not depend on it", what)
		}
	}
}

func TestUIASnapshotHashNameBoundary(t *testing.T) {
	// 名前の連結で境界が曖昧にならない（"ab","c" と "a","bc"）
	hash := func(a, b string) string {
		w := uiaTestNode("w", uiaTestNode(a), uiaTestNode(b))
		_, h := uiaSnapshotOf(t, w)
		return h
	}
	if hash("ab", "c") == hash("a", "bc") {
		t.Error("hash collides across name boundaries")
	}
}
