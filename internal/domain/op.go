package domain

import "time"

// Op はツールの操作（MCP が組み立てる。Web UI（M2）・エージェント（M4）も同じ型を使う）。
// 座標を持つ Op の座標は、service.Control.Do に渡す時点では縮小スクリーンショット座標（FR-M16）。
// Control が Monitor の縮小率で物理座標へ変換してから InputGuard と platform へ渡す。
type Op interface{ isOp() }

// MonitorPrimary は Op の Monitor に指定する「プライマリモニター」（monitor 省略時）。
// 0 以上は get_screen_info が返すモニターの Index。
const MonitorPrimary = -1

// MouseMove は At（縮小座標）へカーソルを動かす（FR-M04）。
type MouseMove struct {
	At      Point
	Monitor int
}

// Click は At でクリックする（FR-M05）。At が nil なら現在のカーソル位置。Count は 1〜3。
type Click struct {
	At      *Point
	Button  Button
	Count   int
	Mods    []Modifier
	Monitor int
}

// Drag は From で左ボタンを押し、To まで動かして離す（FR-M06）。
type Drag struct {
	From, To Point
	Mods     []Modifier
	Monitor  int
}

// Scroll は At でホイールをノッチ単位で回す（FR-M07）。Amount は 1〜50。
type Scroll struct {
	At      Point
	Dir     Direction
	Amount  int
	Monitor int
}

// KeyPress はキーの組み合わせを押す（FR-M08）。
type KeyPress struct{ Keys KeyCombo }

// TypeText は Unicode 文字列を入力する（FR-M09）。
type TypeText struct{ Text string }

// FocusWindow は ID（HWND の 10 進文字列）のウィンドウを前面にする（FR-M12）。
type FocusWindow struct{ ID string }

// ClipboardRead はクリップボードのテキストを読む（FR-M13）。
type ClipboardRead struct{}

// ClipboardWrite はクリップボードへテキストを書く（FR-M13）。
type ClipboardWrite struct{ Text string }

// GetUITree はウィンドウの UI Automation ツリーを読む（FR-M14）。
// WindowID が空なら前面ウィンドウ。MaxDepth・MaxNodes が 0 なら既定値（5・500）。
type GetUITree struct {
	WindowID           string
	MaxDepth, MaxNodes int
}

// FindUIElement はウィンドウの中の UI 要素を探す（#267。読み取りだけで入力しない）。Window が空なら前面ウィンドウ。
// MaxResults が 0 なら既定値（20。上限 100）。
type FindUIElement struct {
	Window     WindowLocator
	Element    ElementLocator
	MaxResults int
}

// WaitState は WaitFor の待ち方。
type WaitState string

const (
	WaitAppear    WaitState = "appear"    // 対象が現れるまで
	WaitDisappear WaitState = "disappear" // 対象が消えるまで
)

// Valid は s が既知の待ち方かを返す。
func (s WaitState) Valid() bool { return s == WaitAppear || s == WaitDisappear }

// WaitFor はウィンドウ（Element が nil のとき。Window で探す）または UI 要素（Window のウィンドウの中の Element）が
// 現れる・消えるまで、Timeout を上限に待つ（#266。読み取りだけで入力しない）。Interval が 0 なら既定の間隔。
type WaitFor struct {
	Window   WindowLocator
	Element  *ElementLocator
	State    WaitState
	Timeout  time.Duration
	Interval time.Duration
}

// Wait は Duration だけ待つ（FR-M10。上限 10 秒）。
type Wait struct{ Duration time.Duration }

// 以下は Web UI の WebSocket（input.*。API 仕様 §5.3）が組み立てる Op。MCP の Op と違い、座標は縮小座標ではなく
// ストリームの座標（Monitor の物理ピクセル。Monitor の左上が原点）で、Control が仮想デスクトップの物理座標へ変換する。
// Monitor に MonitorPrimary か 0 以上の Index を指定する（MCP の Op と同じ）。

// PointerMove は At（ストリームの座標）へカーソルを動かす（input.mouse の move）。
type PointerMove struct {
	At      Point
	Monitor int
}

// MouseButton は At（ストリームの座標）へカーソルを動かし、Button を押す・離す（input.mouse の down / up）。
// 押したボタンは ReleaseAll（キルスイッチ）で離される。
type MouseButton struct {
	At      Point
	Button  Button
	Action  ButtonAction
	Monitor int
}

// Wheel は At（ストリームの座標）へ動かしてホイールを回す（input.mouse の wheel）。
// 単位はノッチ（小数可。MCP の Scroll.Amount と同じ）。DY > 0 が下、DX > 0 が右。1 回の絶対値は 50 まで。
type Wheel struct {
	At      Point
	DX, DY  float64
	Monitor int
}

func (MouseMove) isOp()      {}
func (Click) isOp()          {}
func (Drag) isOp()           {}
func (Scroll) isOp()         {}
func (KeyPress) isOp()       {}
func (TypeText) isOp()       {}
func (FocusWindow) isOp()    {}
func (ClipboardRead) isOp()  {}
func (ClipboardWrite) isOp() {}
func (GetUITree) isOp()      {}
func (FindUIElement) isOp()  {}
func (WaitFor) isOp()        {}
func (Wait) isOp()           {}
func (PointerMove) isOp()    {}
func (MouseButton) isOp()    {}
func (Wheel) isOp()          {}
func (KeyEvent) isOp()       {}

// HostInputGated は、ホストの人が実際のマウス・キーボードを使っている間の一時停止（#270）の対象になるかを、Op 自身が答えるための任意の窓口。
// 組み込みの Op の判定は OpPausedByHostInput が持つ。新しい Op（UI Automation のパターン操作など）は、カーソル・フォーカス・キー入力を
// 動かすなら実装して true を返す。実装しない Op は一時停止の対象にならない。
type HostInputGated interface {
	Op
	PausedByHostInput() bool
}

// OpPausedByHostInput は op が、ホストの実入力による一時停止（service.HostInputPause）で拒否される操作かを返す。
// 対象は、実際のカーソルを動かす・キーを押す・前面を奪う操作: MouseMove・Click・Drag・Scroll・KeyPress・TypeText・FocusWindow と、
// Web UI の PointerMove・MouseButton・Wheel・KeyEvent（いずれも SendInput か前面の切り替え）。
// 対象外は、読み取り（ClipboardRead・GetUITree・スクリーンショットなど）、待ち（Wait）、クリップボードへの書き込み（カーソル・フォーカスを動かさない）。
// HostInputGated を実装した Op は、その答えに従う。
func OpPausedByHostInput(op Op) bool {
	switch op.(type) {
	case MouseMove, Click, Drag, Scroll, KeyPress, TypeText, FocusWindow, PointerMove, MouseButton, Wheel, KeyEvent:
		return true
	}
	g, ok := op.(HostInputGated)
	return ok && g.PausedByHostInput()
}
