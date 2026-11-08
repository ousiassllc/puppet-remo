package windows

import "github.com/ousiassllc/puppet-remo/internal/domain"

// ウィンドウスタイル（GWL_STYLE・GWL_EXSTYLE）のビット。wsExTransparent は overlaystyle.go。
const (
	wsPopup        = 0x80000000 // WS_POPUP
	wsChild        = 0x40000000 // WS_CHILD
	wsExToolWindow = 0x00000080 // WS_EX_TOOLWINDOW
	wsExNoActivate = 0x08000000 // WS_EX_NOACTIVATE
)

// decodeStyle はスタイル（GWL_STYLE）と拡張スタイル（GWL_EXSTYLE）を domain.WindowStyle にする。
func decodeStyle(style, ex uintptr) domain.WindowStyle {
	return domain.WindowStyle{
		Popup:       style&wsPopup != 0,
		Child:       style&wsChild != 0,
		Tool:        ex&wsExToolWindow != 0,
		Transparent: ex&wsExTransparent != 0,
		NoActivate:  ex&wsExNoActivate != 0,
	}
}

// filetimeValue は FILETIME（上位・下位 32 ビット）を 1 つの値にする。プロセスの開始時刻（ProcessStart）に使う。0 は不明。
func filetimeValue(high, low uint32) uint64 { return uint64(high)<<32 | uint64(low) }

// walkLinks は first から next で所有者を辿った鎖を、近い順に返す（OwnerChain・MenuOwnerChain の組み立て）。
// self は鎖に入れないウィンドウ（鎖の起点のウィンドウ自身。循環の検出に使う）。first が 0 なら空。
// fact は 1 枚の事実（ID・PID・開始時刻・クラス）を返し、ウィンドウが消えたなど取れなければ ok=false。
// 次の場合は鎖を打ち切り、最後に検証不能の印（domain.UnverifiableLink）を置く: 循環（self・既出のウィンドウに戻る）、
// domain.MaxChainHops 段を超えて続く、fact が取れない。印の前のリンクは正しい（所有者は取れたが、その先を確かめられない）。
func walkLinks(first, self uintptr, next func(hwnd uintptr) uintptr, fact func(hwnd uintptr) (domain.WindowLink, bool)) []domain.WindowLink {
	var out []domain.WindowLink
	seen := map[uintptr]bool{self: true}
	for h := first; h != 0; h = next(h) {
		if len(out) >= domain.MaxChainHops || seen[h] {
			return append(out, domain.UnverifiableLink())
		}
		seen[h] = true
		l, ok := fact(h)
		if !ok {
			return append(out, domain.UnverifiableLink())
		}
		out = append(out, l)
	}
	return out
}
