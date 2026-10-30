package windows

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// inspectApp は AppLauncher.Inspect の本体: exePath が存在する通常ファイルか、workingDir（空でなければ）が
// 存在するディレクトリかを調べる。無ければ platform.ErrNotFound。調べられない（権限など）ときは OS のエラーコードだけを
// 返す（os のエラーはパスを含むので、そのまま返さない）。
func inspectApp(ctx context.Context, exePath, workingDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := statKind(exePath, func(m fs.FileMode) bool { return m.IsRegular() }); err != nil {
		return err
	}
	if workingDir == "" {
		return nil
	}
	return statKind(workingDir, fs.FileMode.IsDir)
}

func statKind(path string, want func(fs.FileMode) bool) error {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) { // 途中が通常ファイルのパスも「無い」
			return platform.ErrNotFound
		}
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return errors.New("windows: inspect failed: " + err.Error())
	}
	if !want(fi.Mode()) {
		return platform.ErrNotFound
	}
	return nil
}
