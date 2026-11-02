package windows

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// checkFolderPath は FolderOpener.OpenFolder の引数の検査（OS を呼ばない部分）: NUL を含まない絶対パスで、実在するディレクトリであること。
// 空・絶対パスでない・NUL を含むは domain.ErrInvalidArgument、存在しない・ディレクトリでないは platform.ErrNotFound。エラーにパスは含めない。
func checkFolderPath(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return domain.ErrInvalidArgument.WithMsg("folder path must be an absolute path")
	}
	if strings.ContainsRune(dir, 0) {
		return domain.ErrInvalidArgument.WithMsg("folder path contains a NUL character")
	}
	return statKind(dir, fs.FileMode.IsDir)
}
