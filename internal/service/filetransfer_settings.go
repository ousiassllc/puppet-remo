package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// ファイル転送（#264）の設定のキー。ホスト GUI の設定画面（HostSettings）だけが書く。MCP・Web UI には書く経路が無い。
const (
	settingFileTransferRoot     = "file_transfer.root"
	settingFileTransferMaxFile  = "file_transfer.max_file_bytes"
	settingFileTransferMaxTotal = "file_transfer.max_total_bytes"
)

// fileTransferSettings は読み出した設定。Root は設定が無い（空の）ときは空文字（既定の置き場所を使う）。
type fileTransferSettings struct {
	Root          string
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// loadFileTransferSettings は file_transfer.* を読む。置き場所が文字列でない・形が不正なら domain.ErrInvalidArgument（fail closed。
// 壊れた設定のまま既定の場所へ静かに切り替えない）。上限は 1〜2^53 の整数で、値が不正なら既定値と invalid = true（既定値が
// 厳しい側なので、ファイルの出し入れを止めずに既定へ戻す。audit.* の保持期間と同じ規則）。
func loadFileTransferSettings(ctx context.Context, settings repository.Settings) (s fileTransferSettings, invalid bool, err error) {
	if settings == nil {
		return fileTransferSettings{MaxFileBytes: domain.DefaultFileMaxBytes, MaxTotalBytes: domain.DefaultFileTotalMaxBytes}, false, nil
	}
	if s.Root, err = loadFileTransferRoot(ctx, settings); err != nil {
		return fileTransferSettings{}, false, err
	}
	var bad bool
	if s.MaxFileBytes, bad, err = readBoundedInt(ctx, settings, settingFileTransferMaxFile, domain.DefaultFileMaxBytes, maxJSONInteger); err != nil {
		return fileTransferSettings{}, false, err
	}
	invalid = invalid || bad
	if s.MaxTotalBytes, bad, err = readBoundedInt(ctx, settings, settingFileTransferMaxTotal, domain.DefaultFileTotalMaxBytes, maxJSONInteger); err != nil {
		return fileTransferSettings{}, false, err
	}
	return s, invalid || bad, nil
}

func loadFileTransferRoot(ctx context.Context, settings repository.Settings) (string, error) {
	raw, ok, err := settings.Get(ctx, settingFileTransferRoot)
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", settingFileTransferRoot, err)
	}
	if !ok {
		return "", nil
	}
	var root string
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", domain.ErrInvalidArgument.WithMsg("setting " + settingFileTransferRoot + " must be a string")
	}
	if root == "" {
		return "", nil
	}
	if err := domain.ValidateTransferRoot(root); err != nil {
		return "", domain.ErrInvalidArgument.WithMsg("setting " + settingFileTransferRoot + " is not a valid folder path")
	}
	return root, nil
}

// cleanFileTransferRoot は GUI から受け取った置き場所を整える（前後の空白を除き、Clean する）。空ならそのまま空（既定の置き場所）。
func cleanFileTransferRoot(root string) string {
	if root = strings.TrimSpace(root); root == "" {
		return ""
	}
	return filepath.Clean(root)
}
