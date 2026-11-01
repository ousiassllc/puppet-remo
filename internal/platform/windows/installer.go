package windows

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// checkInstallerPath は Installer.Run の引数の検査（OS を呼ばない部分）: 絶対パスの .exe で、実在する通常ファイルであること。
// 絶対パスでない・拡張子違いは domain.ErrInvalidArgument、実在しなければ platform.ErrNotFound。エラーにパスは含めない。
func checkInstallerPath(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path == "" || !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return domain.ErrInvalidArgument.WithMsg("installer path must be an absolute path to an .exe file")
	}
	if strings.ContainsRune(path, 0) {
		return domain.ErrInvalidArgument.WithMsg("installer path contains a NUL character")
	}
	return inspectApp(ctx, path, "")
}
