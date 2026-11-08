package domain

// 所有関係の鎖（WindowInfo.OwnerChain・MenuOwnerChain）の上限と、判定で使うウィンドウクラス名。
const (
	// MaxChainHops は鎖を辿る段数の上限。超えたら検証不能の印（WindowLink.Unverifiable）で打ち切る。
	MaxChainHops = 8

	// ClassMenu はメニュー（メニューバーのドロップダウン・右クリックメニュー）のウィンドウクラス。
	ClassMenu = "#32768"
	// ClassApplicationFrameWindow は UWP のフレーム。内容（CoreWindow）は別 PID なので、検証前は対象にできない（ReasonTargetUnsupported）。
	ClassApplicationFrameWindow = "ApplicationFrameWindow"
)

// WindowInfo はトップレベルウィンドウ 1 枚の事実（FR-M11）。platform.WindowManager が返す。
// 許可・拒否は決めない（判定は service.InputGuard と ResolveGroup）。
type WindowInfo struct {
	ID      string // HWND の 10 進文字列
	Title   string
	Process string // プロセス名（例: notepad.exe）
	PID     uint32
	Rect    Rect // 仮想デスクトップ上の物理座標
	Monitor int  // 最も重なるモニターの Index

	// Class はウィンドウクラス名（GetClassNameW）。空は不明。
	Class string
	// ProcessStart はプロセスの開始時刻（GetProcessTimes の作成時刻。FILETIME の値）。PID の再利用を見分けるための不透明な値で、等しいかどうかだけに使う。
	// 0 は不明（プロセスを開けないなど）。不明は一致として扱わない。
	ProcessStart uint64
	// OwnerChain はこのウィンドウの所有者を近い順に並べた鎖（GW_OWNER を繰り返す。所有者のない子ウィンドウは GetAncestor(GA_PARENT)）。
	// このウィンドウ自身は含めない。所有者がなければ空。最大 MaxChainHops 段で、循環・超過・辿れなかったときは最後に Unverifiable のリンクを置く。
	OwnerChain []WindowLink
	// MenuOwnerID はメニュー（Class == ClassMenu）のとき、そのスレッドの hwndMenuOwner（GetGUIThreadInfo）。空はメニューモードでない・取得失敗。
	MenuOwnerID string
	// MenuOwnerChain は MenuOwnerID から GW_OWNER を辿った鎖（先頭が hwndMenuOwner 自身）。MenuOwnerID が空なら空。メニューのときだけ意味を持つ。
	MenuOwnerChain []WindowLink
	Style          WindowStyle

	Foreground bool
	Minimized  bool
	Elevated   bool // 昇格（管理者権限）ウィンドウ。UIPI のため入力できない
	Own        bool // 自プロセスのウィンドウ
	// Overlay は自プロセスのクリックスルーのウィンドウ（操作中オーバーレイ。Own かつ WS_EX_TRANSPARENT かつ WS_EX_LAYERED。#69）。
	// 入力を受けず、下のウィンドウへそのまま通すので、InputGuard は拒否しない（InputBlockReason）。Own のときだけ true になる。
	Overlay bool
}

// WindowStyle はウィンドウスタイルのうち所有関係の判定・診断に使う印。platform がスタイルのビットから埋める。
type WindowStyle struct {
	Popup       bool // WS_POPUP
	Child       bool // WS_CHILD
	Tool        bool // WS_EX_TOOLWINDOW
	Transparent bool // WS_EX_TRANSPARENT
	NoActivate  bool // WS_EX_NOACTIVATE
}

// WindowLink は所有関係の鎖の 1 段（そのウィンドウ自身の事実）。
type WindowLink struct {
	ID           string
	PID          uint32
	ProcessStart uint64
	Class        string
	// Unverifiable は、鎖をこの先へ辿れなかった（循環・MaxChainHops 段の超過・ウィンドウの消滅）ことを示す印。ほかの項目は空にする。
	// 判定側は、この印より前に T に至っていなければ検証不能として止める。
	Unverifiable bool
}

// UnverifiableLink は鎖を辿れなかったことを示す印のリンクを返す。
func UnverifiableLink() WindowLink { return WindowLink{Unverifiable: true} }

// WindowRef は選んだトップレベルウィンドウの同一性（タイトルは含めない）。HWND の再利用を ID だけで同一視しないために、PID・開始時刻・クラスを束ねる。
type WindowRef struct {
	ID           string
	PID          uint32
	ProcessStart uint64
	Class        string
}

// RefOf は w の同一性を返す。
func RefOf(w WindowInfo) WindowRef {
	return WindowRef{ID: w.ID, PID: w.PID, ProcessStart: w.ProcessStart, Class: w.Class}
}

// Known は同一性を確かめられる値が揃っているかを返す。ID・PID・開始時刻・クラスのどれかが不明なら false（不明は一致にしない）。
func (r WindowRef) Known() bool {
	return r.ID != "" && r.PID != 0 && r.ProcessStart != 0 && r.Class != ""
}

// Matches は w が r と同じウィンドウかを返す（ID・PID・開始時刻・クラスがすべて一致）。タイトルは見ない（タイトルは変わる）。
// r が Known でなければ常に false。
func (r WindowRef) Matches(w WindowInfo) bool {
	return r.Known() && r == RefOf(w)
}

// LeafRef は座標・フォーカスの葉（GA_ROOT に引き上げる前の子ウィンドウ）の事実。最上位ウィンドウへ引き上げると別プロセスの子（SetParent）を見逃すため、引き上げ前の値を残す。
// 取得できなかった項目は零値（ID が空なら葉そのものが不明）。
type LeafRef struct {
	WindowRef
	Process string // プロセス名。blocked_by に出す。空は不明
}

// LeafOf は w 自身を葉として返す（子ウィンドウがなく、w が葉のとき）。
func LeafOf(w WindowInfo) LeafRef { return LeafRef{WindowRef: RefOf(w), Process: w.Process} }

// HitInfo は座標上のウィンドウ。Top は最上位のトップレベルウィンドウ、Leaf はその座標にある最も深い子ウィンドウ（子がなければ Top 自身）。
type HitInfo struct {
	Top  WindowInfo
	Leaf LeafRef
}

// FocusInfo は入力の宛先。Foreground は前面のトップレベルウィンドウ、FocusLeaf は前面のスレッドの hwndFocus（GetGUIThreadInfo。
// hwndFocus が無いときは前面のウィンドウ自身）。FocusLeaf が零値なら取得に失敗した（不明）。
type FocusInfo struct {
	Foreground WindowInfo
	FocusLeaf  LeafRef
}
