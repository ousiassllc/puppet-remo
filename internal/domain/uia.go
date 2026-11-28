package domain

// UINode は UI Automation ツリーの節点（FR-M14）。platform.UIAutomation.Tree が返す。
// Rect は物理座標。MCP へ出すときの縮小座標への変換は呼び出し側が行う。
type UINode struct {
	ID            string // 要素 ID（ElementRef の文字列。find_ui_element と同じ形式）。RuntimeId を取れない要素は空（引き直せない）
	Role          string // ControlType の名前。例: "Window"、"Button"
	Name          string
	AutomationID  string   // UIA の AutomationId。無ければ空
	ClassName     string   // UIA の ClassName。無ければ空
	Patterns      []string // 対応しているパターン（UIPatternNames の順）
	Value         string   // パスワード欄では空
	ValueRedacted bool     // パスワード欄など、値を意図して返さなかった
	Rect          Rect
	Enabled       bool
	Offscreen     bool
	Children      []UINode
	// Truncated は、この節点の子孫を maxDepth または maxNodes で打ち切ったとき true。
	Truncated bool
}

// UIElement は UI Automation スナップショットの要素 1 件。要素の値・テキスト本文は持たない。
type UIElement struct {
	ControlType int    // UIA の ControlType ID
	Name        string // UIA の Name（ラベル）
	Enabled     bool
	Offscreen   bool
	Rect        Rect     // 物理座標の外接矩形
	Path        []string // 祖先パス: 役割クラスとラベルを持つ祖先の "role:label" を、根 → 親の順に並べたもの
	Chrome      bool     // ウィンドウ枠（タイトルバー・システムメニュー・最小化 / 最大化 / 閉じる）
	SameAsValue bool     // 名前と値が同一か（値そのものは持たない）
}

// UISnapshot は 1 つの整合した UI Automation スナップショット。platform.UIAutomation.Snapshot が返す。
type UISnapshot struct {
	Window    WindowInfo
	Hash      string // ツリーのハッシュ。テキスト値は含めない
	Elements  []UIElement
	Truncated bool
}

// UI Automation のパターン名（UIElementInfo.Patterns・UINode.Patterns）。この順に並べる。
const (
	PatternInvoke         = "Invoke"
	PatternValue          = "Value"
	PatternToggle         = "Toggle"
	PatternSelectionItem  = "SelectionItem"
	PatternExpandCollapse = "ExpandCollapse"
	PatternScroll         = "Scroll"
	PatternScrollItem     = "ScrollItem"
	PatternRangeValue     = "RangeValue"
	PatternWindow         = "Window"
	PatternText           = "Text"
	PatternSelection      = "Selection"
)

// UIElementInfo は探索で見つけた UI 要素 1 件（platform.UIAutomation.Find・Resolve の結果）。値・テキスト本文は持たない。
type UIElementInfo struct {
	ID           string // 要素 ID（ElementRef の文字列）。Find の match に渡す時点では空
	ControlType  string // ControlType の名前（"Button"）
	Name         string
	AutomationID string
	ClassName    string
	Rect         Rect // 物理座標
	Enabled      bool
	Offscreen    bool
	Patterns     []string
}

// UIFindResult は platform.UIAutomation.Find の結果。
type UIFindResult struct {
	Window   WindowInfo
	Elements []UIElementInfo // 浅い階層から順（同じ深さでは UIA の並び）。最大 maxResults 件
	// Truncated は、一致がまだあるのに maxResults で止めた、または走査を maxNodes で打ち切った（取りこぼしがありうる）とき true。
	Truncated bool
}
