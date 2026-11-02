//go:build windows

package windows

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.FolderOpener = (*FolderOpener)(nil)

// FolderOpener は platform.FolderOpener の Windows 実装（ShellExecute の "open"。#54）。状態を持たず、どの goroutine からも呼べる。
//
//   - 開くのは、実在するディレクトリの絶対パスだけ（checkFolderPath）。ファイルは開かない（ファイルへの "open" は実行になるため）。
//   - 動詞は固定の "open"、引数・作業ディレクトリは渡さない。シェル（cmd.exe）・コマンドラインを経由せず、呼び出し元の文字列で組み立てるものも無い。
//   - ハンドルは受け取らない（エクスプローラーは本アプリと独立に動く）。
type FolderOpener struct{}

// NewFolderOpener は platform.FolderOpener の実装を作る。
func NewFolderOpener() *FolderOpener { return &FolderOpener{} }

// OpenFolder は dir をエクスプローラーで開く。
func (*FolderOpener) OpenFolder(ctx context.Context, dir string) error {
	if err := checkFolderPath(ctx, dir); err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		// Errno のメッセージにパスは含まれない。
		return fmt.Errorf("windows: open folder: %w", err)
	}
	return nil
}
