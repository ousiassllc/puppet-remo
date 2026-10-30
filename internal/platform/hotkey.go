package platform

// Hotkey はグローバルホットキー。
type Hotkey interface {
	// Register は spec（`Ctrl+Alt+Shift+K` の形式。修飾キーとキーを "+" で連結し、大文字小文字を区別しない）を登録し、
	// 押されたとき fn を呼ぶ。fn は実装のメッセージループのスレッドから呼ばれる。
	// 空・不正な spec と nil の fn は ErrInvalidArgument。登録中の spec の重複登録はエラー。
	// unregister は解除する。複数回呼んでも安全で、解除後は fn を呼ばない。
	Register(spec string, fn func()) (unregister func(), err error)
}
