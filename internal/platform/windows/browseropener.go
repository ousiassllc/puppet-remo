package windows

import (
	"context"
	"net/url"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// checkBrowserURL は BrowserOpener.OpenURL の引数の検査（OS を呼ばない部分）: NUL・空白・制御文字を含まない、ホスト付きの https の URL であること。
// 違えば domain.ErrInvalidArgument。エラーに URL は含めない（id_token_hint などの秘密を含みうる。NFR-S13）。
func checkBrowserURL(ctx context.Context, raw string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return domain.ErrInvalidArgument.WithMsg("browser url contains a control or space character")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
		return domain.ErrInvalidArgument.WithMsg("browser url must be an https url")
	}
	return nil
}
