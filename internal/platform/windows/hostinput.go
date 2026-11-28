package windows

import (
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルはホストの実入力の検知（#270。platform.HostInputWatcher）の、Win32 に依存しない判定。
// フックの本体（WH_MOUSE_LL / WH_KEYBOARD_LL）は hostinput_windows.go。

// ownInputTag は、Input が SendInput で注入するすべての INPUT の dwExtraInfo に付ける印（"NEFO"）。
// 低レベルフックの injected フラグだけでも自分の SendInput は見分けられるが、印も併せて見る（印が付けられるのは SendInput だけ）。
// SetCursorPos には dwExtraInfo を渡せないので、その動き（Input.settleCursor の位置補正）は印では見分けられない。
// 自分の補正は ownCursorMoves に期待する位置を記録し、フックが injected フラグの無い WM_MOUSEMOVE を同じ位置で受けたとき 1 回だけ無視する。
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

// wmMouseMove は低レベルのマウスフックの wParam に来る WM_MOUSEMOVE。
const wmMouseMove = 0x0200

// ownCursorMoveWindow は、自分の SetCursorPos による移動をフックが受けるまで待つ時間（これを過ぎた記録は使わない）。
const ownCursorMoveWindow = 250 * time.Millisecond

// ownCursorMoves はプロセスで共有する、自分の SetCursorPos による移動の記録（フックもプロセスで 1 組）。
var ownCursorMoves = &selfMoveRecorder{now: time.Now}

// selfMoveRecorder は「自分が SetCursorPos で動かすカーソルの位置」を短い間だけ覚え、フックがそれを実入力と取り違えないようにする。
// 期待は 1 つだけ（Input の呼び出しは専用スレッドに直列化される）。新しい期待は古いものを置き換える。
type selfMoveRecorder struct {
	now func() time.Time

	mu    sync.Mutex
	valid bool
	at    domain.Point
	until time.Time
}

// expect は p への SetCursorPos を行う直前に呼ぶ。
func (r *selfMoveRecorder) expect(p domain.Point) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.valid, r.at, r.until = true, p, r.now().Add(ownCursorMoveWindow)
}

// consume はフックが injected フラグ無しで受けたマウスのイベント（msg は wParam、(x, y) は pt）が、記録した自分の移動なら
// true を返し、その記録を消す（1 回だけ）。期限が過ぎた記録は捨てる。位置が違う動き（人の実操作）は記録を残したまま false。
func (r *selfMoveRecorder) consume(msg uintptr, x, y int32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.valid {
		return false
	}
	if r.now().After(r.until) {
		r.valid = false
		return false
	}
	if msg != wmMouseMove || r.at.X != int(x) || r.at.Y != int(y) {
		return false
	}
	r.valid = false
	return true
}
