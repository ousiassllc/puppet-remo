//go:build !windows

package localfs

import (
	"errors"
	"os"
	"syscall"
)

// openNoFollow は path の末尾がシンボリックリンクなら開かずに失敗する（O_NOFOLLOW）。write が true なら読み書き、false なら読み取りだけ。
// create が true なら無いファイルを作る（権限 0600）。作る・開くときに切り詰めはしない（呼び出し側が開いたあとの検査に通ってから切り詰める）。
func openNoFollow(path string, write, create bool) (*os.File, error) {
	flag := os.O_RDONLY
	if write {
		flag = os.O_RDWR
	}
	if create {
		flag |= os.O_CREATE
	}
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, 0o600)
}

// openedIsReparsePoint は開いたファイル自身がリパースポイントか（Windows のみ。ここでは常に false）。
func openedIsReparsePoint(*os.File) (bool, error) { return false, nil }

// isSymlinkLoop は O_NOFOLLOW がシンボリックリンクを開くのを拒否した失敗（ELOOP。BSD 系は EMLINK）か。
func isSymlinkLoop(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}
