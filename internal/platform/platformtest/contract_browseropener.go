package platformtest

import (
	"context"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// RunBrowserOpenerContract は platform.BrowserOpener の契約のうち、実機でブラウザを開かないものを検査する:
// https 以外の URL・空・NUL・空白を含む URL は拒み、エラーに URL を含めない。取り消し済みの ctx では何もしない。
// 実際に開く確認（https の URL を渡す）は呼び出し側が別に行う。
func RunBrowserOpenerContract(t *testing.T, newOpener func(t *testing.T) platform.BrowserOpener) {
	t.Helper()
	ctx := context.Background()

	for name, raw := range map[string]string{
		"empty":    "",
		"http":     "http://example.com/?q=URL-CANARY",
		"file":     "file:///C:/URL-CANARY.exe",
		"path":     `C:\URL-CANARY.exe`,
		"no host":  "https:///URL-CANARY",
		"NUL":      "https://example.com/\x00URL-CANARY",
		"space":    "https://example.com/ URL-CANARY",
		"newline":  "https://example.com/\nURL-CANARY",
		"relative": "URL-CANARY",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			err := newOpener(t).OpenURL(ctx, raw)
			requireCode(t, err, domain.ErrInvalidArgument, "OpenURL("+name+")")
			if err != nil && strings.Contains(err.Error(), "URL-CANARY") {
				t.Errorf("error leaks the url: %v", err)
			}
		})
	}
	t.Run("does nothing with a canceled context", func(t *testing.T) {
		requireCanceled(t, newOpener(t).OpenURL(canceledCtx(), "https://example.com/"), "OpenURL")
	})
}
