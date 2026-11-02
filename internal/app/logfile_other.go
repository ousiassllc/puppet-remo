//go:build !windows

package app

import "os"

// openLogFile はログファイルを読み取り専用で開く（ログ画面の読み出し。FR-H13）。
// Windows 以外では、開いたままでもリネーム・削除ができるので、os.Open でよい（Windows の実装は logfile_windows.go。#53）。
func openLogFile(path string) (*os.File, error) { return os.Open(path) }
