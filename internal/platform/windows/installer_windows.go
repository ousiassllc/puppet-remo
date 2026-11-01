//go:build windows

package windows

import (
	"context"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// installerSilentArg は NSIS のインストーラをサイレントで動かす引数（画面を出さない。大文字の /S）。
const installerSilentArg = "/S"

var _ platform.Installer = (*Installer)(nil)

// Installer は platform.Installer の Windows 実装（ShellExecute の "open"。FR-H09、NFR-S10）。状態を持たず、どの goroutine からも呼べる。
//
//   - 引数は `/S`（NSIS のサイレント）だけで、シェル（cmd.exe）を経由しない。
//   - インストーラは既定で全ユーザー向け（Program Files。RequestExecutionLevel admin）なので、CreateProcess ではなく
//     ShellExecute で起動して、昇格の確認（UAC）をユーザーに出す（CreateProcess は ERROR_ELEVATION_REQUIRED で失敗する）。
//     UAC の確認を取り消すと ERROR_CANCELLED で失敗し、何もインストールされない。
//   - ウィンドウは出さない（SW_HIDE）。終了は待たず、ハンドルは受け取らない（アプリの終了後もインストーラは動き続ける）。
type Installer struct{}

// NewInstaller は platform.Installer の実装を作る。
func NewInstaller() *Installer { return &Installer{} }

// Run は path のインストーラを `/S` で起動する。
func (*Installer) Run(ctx context.Context, path string) error {
	if err := checkInstallerPath(ctx, path); err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	args, err := windows.UTF16PtrFromString(installerSilentArg)
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := windows.ShellExecute(0, nil, file, args, dir, windows.SW_HIDE); err != nil {
		// Errno のメッセージにパスは含まれない。
		return fmt.Errorf("windows: start installer: %w", err)
	}
	return nil
}
