package platform

// HostInputKind はホストの実入力の種類。値はログ・監査に出すので固定の文字列にする。
type HostInputKind string

const (
	HostInputMouse    HostInputKind = "mouse"
	HostInputKeyboard HostInputKind = "keyboard"
)

// HostInputWatcher は、ホストの人が実際に動かしたマウス・キーボードの検知（#270）。
// 自分が注入した入力（InputInjector の SendInput）と、他のソフトウェアが注入した入力は区別して検知しない。
// 検知するのは「入力があった」という事実だけで、押されたキー・文字・座標は読まない。
type HostInputWatcher interface {
	// Watch は監視を始め、実入力のたびに fn を呼ぶ。fn は実装のメッセージループのスレッドから、連続する入力（マウスの移動など）で
	// 頻繁に呼ばれるので、速やかに戻ること（待たない・I/O をしない）。同時に監視できるのは 1 つで、監視中の Watch はエラー。
	// nil の fn は ErrInvalidArgument。stop は監視を止める。複数回呼んでも安全で、止めた後は fn を呼ばない。
	Watch(fn func(kind HostInputKind)) (stop func(), err error)
}
