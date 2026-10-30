package windows

import (
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// errnoAccessDenied は ERROR_ACCESS_DENIED。
const errnoAccessDenied = 5

// sendError は SendInput が total 件のうち sent 件しか入らなかったときのエラーを決める。
//   - 入力デスクトップを使えない（ロック画面・セキュアデスクトップ・セッション切断）→ ErrDesktopUnavailable
//   - ERROR_ACCESS_DENIED（別の整合性レベルのスレッドにブロックされた）→ ErrInputBlocked
//   - それ以外 → ErrInternal の原因つきエラー
//
// UIPI が入力を捨てても SendInput は成功を返すことがある（Win32 の仕様）。昇格ウィンドウへの入力は
// service.InputGuard が WindowInfo.Elevated を見て先に拒否する。
func sendError(sent, total int, errno uintptr, desktopAvailable bool) error {
	switch {
	case !desktopAvailable:
		return domain.ErrDesktopUnavailable.WithMsg("the input desktop is not accessible (locked or disconnected)")
	case errno == errnoAccessDenied:
		return domain.ErrInputBlocked.WithMsg("the system blocked the input (UIPI)")
	}
	return domain.NewError(domain.CodeInternal, fmt.Sprintf("SendInput injected %d of %d events (error %d)", sent, total, errno), nil)
}
