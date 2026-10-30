package windows

import (
	"errors"
	"syscall"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// CreateProcess が返す Win32 のエラーコード（x/sys/windows の定数と同じ値。Linux でもテストできるように数値で持つ）。
const (
	errFileNotFound      syscall.Errno = 2
	errPathNotFound      syscall.Errno = 3
	errAccessDenied      syscall.Errno = 5
	errDirectory         syscall.Errno = 267  // 作業ディレクトリが無い・ディレクトリではない
	errElevationRequired syscall.Errno = 740  // requireAdministrator など、昇格が要る
	errPrivilegeNotHeld  syscall.Errno = 1314 // 必要な特権が無い
	errDisabledByPolicy  syscall.Errno = 1260 // グループポリシー・AppLocker などで実行が禁止されている
)

// classifyLaunchError は CreateProcess のエラーを launch_failed の理由に分類する。
// 実行ファイル・作業ディレクトリが無い → file_missing、拒否 → access_denied、昇格が必要 → elevation_required、それ以外 → other。
func classifyLaunchError(err error) domain.LaunchFailure {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return domain.LaunchOther
	}
	switch errno {
	case errFileNotFound, errPathNotFound, errDirectory:
		return domain.LaunchFileMissing
	case errAccessDenied, errPrivilegeNotHeld, errDisabledByPolicy:
		return domain.LaunchAccessDenied
	case errElevationRequired:
		return domain.LaunchElevationRequired
	}
	return domain.LaunchOther
}

// newLaunchError は CreateProcess のエラー err から *platform.LaunchError を作る。
// 原因には Win32 のエラーコード（パス・引数を含まない）だけを入れ、それ以外のエラーは入れない。
func newLaunchError(err error) *platform.LaunchError {
	le := &platform.LaunchError{Reason: classifyLaunchError(err)}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		le.Err = errno
	}
	return le
}
