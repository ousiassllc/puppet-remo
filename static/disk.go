//go:build !production

package static

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DistDirEnv は開発ビルドが dist を読むディレクトリを指す環境変数。未設定なら、カレントから親へ辿って static/dist を探す。
const DistDirEnv = "PUPPET_REMO_STATIC_DIR"

// distFS はディスクの static/dist を返す。ブラウザの再読み込みで最新のビルドが反映される。
// ソースの位置（runtime.Caller）は -trimpath で失われるため使わない。
func distFS() (fs.FS, bool, error) {
	dir := os.Getenv(DistDirEnv)
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, false, err
		}
		for d := wd; ; d = filepath.Dir(d) {
			if info, err := os.Stat(filepath.Join(d, "static", "dist")); err == nil && info.IsDir() {
				dir = filepath.Join(d, "static", "dist")
				break
			}
			if filepath.Dir(d) == d {
				return nil, false, errors.New("static: static/dist not found; run `task frontend:build` or set " + DistDirEnv)
			}
		}
	}
	return os.DirFS(dir), false, nil
}
