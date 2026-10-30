package windows

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"slices"
	"strconv"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは UI Automation の純粋なロジック（ツリーの走査・上限処理・整形・ハッシュ）。
// COM には触れないので、Windows 以外でもテストできる。COM 側は uia_windows.go が uiaNode を実装する。

// uiaProps は UIA 要素 1 件のプロパティ。取得に失敗した項目は零値にする（要素は並行して消えうるため）。
type uiaProps struct {
	RuntimeID    []int32
	ControlType  int
	Name         string
	AutomationID string
	Value        string // ValuePattern の値。パスワード欄では取得しない
	IsPassword   bool
	Enabled      bool
	Offscreen    bool
	Rect         domain.Rect
	ToggleState  int  // ToggleState（0 オフ / 1 オン / 2 不定）。非対応は 0
	Selected     bool // SelectionItem.IsSelected。非対応は false
}

// uiaNode は走査できる UIA 要素。走査は専用スレッド 1 本の上で行う。
type uiaNode interface {
	// Props は要素のプロパティを返す。
	Props() uiaProps
	// Children は直接の子（コントロールビュー）を返す。呼び出し側が各子の Release を呼ぶ。
	Children(ctx context.Context) ([]uiaNode, error)
	// Release は要素の参照を手放す。1 つの要素につき 1 回だけ呼ぶ。
	Release()
}

// uiaWalked は走査で採用した節点。nodes[0] が根で、親は子より前に並ぶ（幅優先）。
type uiaWalked struct {
	props     uiaProps
	depth     int
	parent    int
	children  []int
	truncated bool // 子孫を maxDepth か maxNodes で打ち切った
}

// walkUIA は root から幅優先で走査する。根の深さを 0 とし、深さが maxDepth を超える節点と、
// 総数が maxNodes を超える節点は採用しない。打ち切った節点の親は truncated になる。
// 浅い節点を先に採るので、上限に達しても画面の骨格が残る。
// root の所有権を引き取る（走査に使った要素はすべて Release する）。maxNodes が 1 未満なら ErrInvalidArgument。
func walkUIA(ctx context.Context, root uiaNode, maxDepth, maxNodes int) (nodes []uiaWalked, err error) {
	type pending struct {
		idx  int
		node uiaNode
	}
	if maxNodes < 1 {
		root.Release()
		return nil, domain.ErrInvalidArgument.WithMsg("maxNodes must be at least 1")
	}
	nodes = []uiaWalked{{props: root.Props(), parent: -1}}
	queue := []pending{{0, root}}
	head := 0
	defer func() {
		for _, p := range queue[head:] {
			p.node.Release()
		}
	}()

	for head < len(queue) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := queue[head]
		head++
		kids, err := p.node.Children(ctx)
		p.node.Release()
		if err != nil {
			return nil, err
		}
		if len(kids) == 0 {
			continue
		}
		depth := nodes[p.idx].depth + 1
		if depth > maxDepth {
			nodes[p.idx].truncated = true
			uiaReleaseAll(kids)
			continue
		}
		for i, kid := range kids {
			if len(nodes) >= maxNodes {
				nodes[p.idx].truncated = true
				uiaReleaseAll(kids[i:])
				break
			}
			idx := len(nodes)
			nodes = append(nodes, uiaWalked{props: kid.Props(), depth: depth, parent: p.idx})
			nodes[p.idx].children = append(nodes[p.idx].children, idx)
			queue = append(queue, pending{idx, kid})
		}
	}
	return nodes, nil
}

func uiaReleaseAll(nodes []uiaNode) {
	for _, n := range nodes {
		n.Release()
	}
}

// uiaTreeFromWalked は走査結果を domain.UINode の木に整形する。パスワード欄は値を持たず ValueRedacted を立てる。
func uiaTreeFromWalked(nodes []uiaWalked) domain.UINode { return uiaTreeAt(nodes, 0) }

func uiaTreeAt(nodes []uiaWalked, i int) domain.UINode {
	w := nodes[i]
	p := w.props
	n := domain.UINode{
		ID:        uiaNodeID(p.RuntimeID, i),
		Role:      uiaRoleName(p.ControlType),
		Name:      p.Name,
		Rect:      p.Rect,
		Enabled:   p.Enabled,
		Truncated: w.truncated,
	}
	if p.IsPassword {
		n.ValueRedacted = true
	} else {
		n.Value = p.Value
	}
	for _, c := range w.children {
		n.Children = append(n.Children, uiaTreeAt(nodes, c))
	}
	return n
}

// uiaNodeID は RuntimeId から参照用の ID を作る（"uia:2a.1f4.3"）。RuntimeId が無いときは走査の添字で代える。
func uiaNodeID(runtimeID []int32, fallback int) string {
	if len(runtimeID) == 0 {
		return "uia:n" + strconv.Itoa(fallback)
	}
	parts := make([]string, len(runtimeID))
	for i, v := range runtimeID {
		parts[i] = strconv.FormatUint(uint64(uint32(v)), 16)
	}
	return "uia:" + strings.Join(parts, ".")
}

// uiaSnapshotFromWalked は走査結果を文書順（先行順）の要素列とハッシュにする。
// ハッシュは ControlType・名前・有効・画面外・矩形・トグル / 選択状態と木の形から作り、値（テキスト本文）は含めない。
func uiaSnapshotFromWalked(nodes []uiaWalked) (elements []domain.UIElement, digest string) {
	h := sha256.New()
	elements = make([]domain.UIElement, 0, len(nodes))
	var visit func(i int, path []string, parentChrome bool)
	visit = func(i int, path []string, parentChrome bool) {
		w := nodes[i]
		p := w.props
		chrome := parentChrome || uiaIsChrome(p, w.parent == 0)
		elements = append(elements, domain.UIElement{
			ControlType: p.ControlType,
			Name:        p.Name,
			Enabled:     p.Enabled,
			Offscreen:   p.Offscreen,
			Rect:        p.Rect,
			Path:        slices.Clone(path),
			Chrome:      chrome,
			SameAsValue: !p.IsPassword && p.Value != "" && p.Value == p.Name,
		})
		uiaHashNode(h, w)

		childPath := path
		if class, ok := uiaRoleClass(p.ControlType); ok {
			if label := strings.Join(strings.Fields(p.Name), " "); label != "" {
				childPath = append(slices.Clip(path), class+":"+label)
			}
		}
		for _, c := range w.children {
			visit(c, childPath, chrome)
		}
	}
	visit(0, nil, false)
	return elements, hex.EncodeToString(h.Sum(nil)[:16])
}

func uiaHashNode(h hash.Hash, w uiaWalked) {
	p := w.props
	var buf [8]byte
	putInt := func(v int) {
		binary.LittleEndian.PutUint64(buf[:], uint64(int64(v)))
		h.Write(buf[:])
	}
	putBool := func(b bool) {
		if b {
			putInt(1)
		} else {
			putInt(0)
		}
	}
	putInt(w.depth)
	putInt(p.ControlType)
	putInt(len(p.Name)) // 長さを先頭に置き、フィールドの境界を曖昧にしない
	h.Write([]byte(p.Name))
	putBool(p.Enabled)
	putBool(p.Offscreen)
	putInt(p.Rect.X)
	putInt(p.Rect.Y)
	putInt(p.Rect.W)
	putInt(p.Rect.H)
	putInt(p.ToggleState)
	putBool(p.Selected)
}

const uiaControlTypeTitleBar = 50037

// uiaChromeAutomationIDs はウィンドウ枠の部品の AutomationId（タイトルバー・システムメニュー・最小化 / 最大化 / 閉じる）。
var uiaChromeAutomationIDs = map[string]bool{
	"TitleBar": true, "SystemMenuBar": true,
	"Minimize": true, "Maximize": true, "Restore": true, "Close": true,
}

// uiaIsChrome は要素自身がウィンドウ枠かを返す。タイトルバー、または根ウィンドウ直下の枠の部品。
// タイトルバーの子孫の判定は呼び出し側が親から引き継ぐ。
func uiaIsChrome(p uiaProps, parentIsRoot bool) bool {
	return p.ControlType == uiaControlTypeTitleBar || (parentIsRoot && uiaChromeAutomationIDs[p.AutomationID])
}

// uiaRoleClasses は ControlType → 役割クラス（docs/components/overview.md §2 の RoleClass と同じ名前）。
// 表にない型（Edit・Document・Pane など）は祖先パスに載せない。
var uiaRoleClasses = map[int]string{
	50000: "button",
	50002: "check_box",
	50003: "combo_box",
	50005: "hyperlink",
	50007: "list_item",
	50011: "menu_item",
	50013: "radio_button",
	50019: "tab_item",
	50024: "tree_item",
	50032: "window",
}

func uiaRoleClass(controlType int) (string, bool) {
	c, ok := uiaRoleClasses[controlType]
	return c, ok
}

var uiaRoleNames = map[int]string{
	50000: "Button", 50001: "Calendar", 50002: "CheckBox", 50003: "ComboBox", 50004: "Edit",
	50005: "Hyperlink", 50006: "Image", 50007: "ListItem", 50008: "List", 50009: "Menu",
	50010: "MenuBar", 50011: "MenuItem", 50012: "ProgressBar", 50013: "RadioButton", 50014: "ScrollBar",
	50015: "Slider", 50016: "Spinner", 50017: "StatusBar", 50018: "Tab", 50019: "TabItem",
	50020: "Text", 50021: "ToolBar", 50022: "ToolTip", 50023: "Tree", 50024: "TreeItem",
	50025: "Custom", 50026: "Group", 50027: "Thumb", 50028: "DataGrid", 50029: "DataItem",
	50030: "Document", 50031: "SplitButton", 50032: "Window", 50033: "Pane", 50034: "Header",
	50035: "HeaderItem", 50036: "Table", 50037: "TitleBar", 50038: "Separator", 50039: "SemanticZoom",
	50040: "AppBar",
}

// uiaRoleName は ControlType の名前（"Button"、"Window"）。未知の型は "Unknown"。
func uiaRoleName(controlType int) string {
	if n, ok := uiaRoleNames[controlType]; ok {
		return n
	}
	return "Unknown"
}
