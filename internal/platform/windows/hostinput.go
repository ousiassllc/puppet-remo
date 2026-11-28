package windows

// このファイルはホストの実入力の検知（#270。platform.HostInputWatcher）の、Win32 に依存しない判定。
// フックの本体（WH_MOUSE_LL / WH_KEYBOARD_LL）は hostinput_windows.go。

// ownInputTag は、Input が SendInput で注入するすべての INPUT の dwExtraInfo に付ける印（"NEFO"）。
// 低レベルフックの injected フラグだけでも自分の注入は見分けられるが、フラグの付かない経路（SetCursorPos の補正など）が
// 実入力に見えないよう、印も併せて見る。
const ownInputTag uintptr = 0x4E45464F

// 低レベルフックの flags のビット（MSLLHOOKSTRUCT.flags / KBDLLHOOKSTRUCT.flags）。
const (
	llmhfInjected        = 0x01 // LLMHF_INJECTED
	llmhfLowerILInjected = 0x02 // LLMHF_LOWER_IL_INJECTED
	llkhfLowerILInjected = 0x02 // LLKHF_LOWER_IL_INJECTED
	llkhfInjected        = 0x10 // LLKHF_INJECTED
)

// isHostMouseEvent は低レベルのマウスフックのイベントが、ホストの人の実際の操作かを返す。
// 注入されたもの（自分の SendInput も、他のソフトウェアのものも）と、自分の印の付いたものは実操作ではない。
func isHostMouseEvent(flags uint32, extraInfo uintptr) bool {
	return flags&(llmhfInjected|llmhfLowerILInjected) == 0 && extraInfo != ownInputTag
}

// isHostKeyEvent は低レベルのキーボードフックのイベントが、ホストの人の実際の操作かを返す（isHostMouseEvent と同じ規則）。
func isHostKeyEvent(flags uint32, extraInfo uintptr) bool {
	return flags&(llkhfInjected|llkhfLowerILInjected) == 0 && extraInfo != ownInputTag
}
