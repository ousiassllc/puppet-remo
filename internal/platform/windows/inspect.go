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

// os.Stat が返す Win32 のエラーコード（x/sys/windows の定数と同じ値。launcherr.go と同じく Linux でもテストできるよう数値で持つ）。
const (
	errInvalidName syscall.Errno = 123 // ERROR_INVALID_NAME: 名前・ボリュームラベルの構文が正しくない
	errBadPathname syscall.Errno = 161 // ERROR_BAD_PATHNAME: パス名が正しくない
)

func statKind(path string, want func(fs.FileMode) bool) error {
	fi, err := os.Stat(path)
	if err != nil {
		return mapStatError(err)
	}
	if !want(fi.Mode()) {
		return platform.ErrNotFound
	}
	return nil
}

// mapStatError は os.Stat の失敗 err を写す: 存在しない・途中が通常ファイル・名前やパスの構文が正しくない
// （ERROR_INVALID_NAME・ERROR_BAD_PATHNAME。登録時の入力の不備であって OS の障害ではない）は platform.ErrNotFound。
// それ以外は OS のエラーコードだけを含むエラー（os のエラーはパスを含むので、そのまま返さない）。
func mapStatError(err error) error {
	var errno syscall.Errno
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) ||
		errors.As(err, &errno) && (errno == errInvalidName || errno == errBadPathname) {
		return platform.ErrNotFound
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return errors.New("windows: inspect failed: " + err.Error())
}
