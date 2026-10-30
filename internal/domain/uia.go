package domain

// UINode は UI Automation ツリーの節点（FR-M14）。platform.UIAutomation.Tree が返す。
// Rect は物理座標。MCP へ出すときの縮小座標への変換は呼び出し側が行う。
type UINode struct {
	ID            string // RuntimeId から作る参照用の文字列。他の操作の引数には使わない
	Role          string // 例: "Window"、"Button"
	Name          string
	Value         string // パスワード欄では空
	ValueRedacted bool   // パスワード欄など、値を意図して返さなかった
	Rect          Rect
	Enabled       bool
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
