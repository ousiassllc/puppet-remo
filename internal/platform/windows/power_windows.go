//go:build windows

package windows

var w32SetThreadExecutionState = w32Kernel32.NewProc("SetThreadExecutionState")

// NewPower は SetThreadExecutionState による platform.Power を作る。専用の OS スレッドを 1 本起動するので、
// 使い終わったら Close する（Close は抑止を解除する）。
func NewPower() *Power { return newPower(setThreadExecutionState) }

func setThreadExecutionState(flags uint32) {
	_, _, _ = w32SetThreadExecutionState.Call(uintptr(flags))
}
