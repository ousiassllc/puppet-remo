//go:build !production

package static

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// distFS はソースツリーの static/dist をディスクから返す。ブラウザの再読み込みで最新のビルドが反映される。
func distFS() (fs.FS, bool, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, false, os.ErrNotExist
	}
	return os.DirFS(filepath.Join(filepath.Dir(file), "dist")), false, nil
}
