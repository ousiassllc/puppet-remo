//go:build windows

package windows

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.BrowserOpener = (*BrowserOpener)(nil)

// BrowserOpener は platform.BrowserOpener の Windows 実装（ShellExecute の "open"。#247）。状態を持たず、どの goroutine からも呼べる。
//
//   - 開くのは https の URL だけ（checkBrowserURL）。file: や実行ファイルのパス・他のスキームは渡せない。
//   - 動詞は固定の "open"、引数・作業ディレクトリは渡さない。シェル（cmd.exe）・コマンドラインを経由しない。
//   - エラーに URL を含めない（id_token_hint などを含みうる。NFR-S13）。
type BrowserOpener struct{}

// NewBrowserOpener は platform.BrowserOpener の実装を作る。
func NewBrowserOpener() *BrowserOpener { return &BrowserOpener{} }

// OpenURL は rawURL を既定のブラウザで開く。
func (*BrowserOpener) OpenURL(ctx context.Context, rawURL string) error {
	if err := checkBrowserURL(ctx, rawURL); err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(rawURL)
	if err != nil {
		return fmt.Errorf("windows: open url: %w", err)
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		// Errno のメッセージに URL は含まれない。
		return fmt.Errorf("windows: open url: %w", err)
	}
	return nil
}
