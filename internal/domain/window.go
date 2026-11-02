package domain

// WindowInfo はトップレベルウィンドウ 1 枚の事実（FR-M11）。platform.WindowManager が返す。
// 許可・拒否は決めない（判定は service.InputGuard）。
type WindowInfo struct {
	ID      string // HWND の 10 進文字列
	Title   string
	Process string // プロセス名（例: notepad.exe）
	PID     uint32
	Rect    Rect // 仮想デスクトップ上の物理座標
	Monitor int  // 最も重なるモニターの Index

	Foreground bool
	Minimized  bool
	Elevated   bool // 昇格（管理者権限）ウィンドウ。UIPI のため入力できない
	Own        bool // 自プロセスのウィンドウ
	// Overlay は自プロセスのクリックスルーのウィンドウ（操作中オーバーレイ。Own かつ WS_EX_TRANSPARENT かつ WS_EX_LAYERED。#69）。
	// 入力を受けず、下のウィンドウへそのまま通すので、InputGuard は拒否しない（InputBlockReason）。Own のときだけ true になる。
	Overlay bool
}
