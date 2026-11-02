//go:build windows

package app

import (
	"os"

	"golang.org/x/sys/windows"
)

// openLogFile はログファイルを読み取り専用で開く（ログ画面の読み出し。FR-H13）。
//
// Go の os.Open は共有モードに FILE_SHARE_DELETE を付けない。そのため、画面が読んでいる間に lumberjack がローテーション
// （今のファイルのリネーム）をすると、Windows ではリネームが共有違反で失敗し、そのときの 1 行が書けない（#53）。
// ここでは FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE で開き、読んでいる間も書き込み・リネーム・削除を妨げない。
func openLogFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
