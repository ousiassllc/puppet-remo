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

// コマンド実行（#265）の設定のキー。ホスト GUI の設定画面（HostSettings）だけが書く。MCP・Web UI には書く経路が無い。
const (
	settingCommandEnabled       = "command.enabled"
	settingCommandAllowlistPath = "command.allowlist_path"
)

// commandSettings は読み出した設定。AllowlistPath は設定が無い（空の）ときは空文字（既定の場所を使う）。
type commandSettings struct {
	Enabled       bool // 既定 false
	AllowlistPath string
}

// loadCommandSettings は command.* を読む。有効・無効が真偽値でない、許可リストのパスが文字列でない・形が不正なら
// domain.ErrInvalidArgument（fail closed。壊れた設定のまま既定の場所へ静かに切り替えない）。
func loadCommandSettings(ctx context.Context, settings repository.Settings) (commandSettings, error) {
	enabled, err := loadBool(ctx, settings, settingCommandEnabled, false)
	if err != nil {
		return commandSettings{}, err
	}
	s := commandSettings{Enabled: enabled}
	if s.AllowlistPath, err = loadCommandAllowlistPath(ctx, settings); err != nil {
		return commandSettings{}, err
	}
	return s, nil
}

func loadCommandAllowlistPath(ctx context.Context, settings repository.Settings) (string, error) {
	if settings == nil {
		return "", nil
	}
	raw, ok, err := settings.Get(ctx, settingCommandAllowlistPath)
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", settingCommandAllowlistPath, err)
	}
	if !ok {
		return "", nil
	}
	var p string
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", domain.ErrInvalidArgument.WithMsg("setting " + settingCommandAllowlistPath + " must be a string")
	}
	if p == "" {
		return "", nil
	}
	if err := domain.ValidateHostFilePath(p); err != nil {
		return "", domain.ErrInvalidArgument.WithMsg("setting " + settingCommandAllowlistPath + " is not a valid file path")
	}
	return p, nil
}

// cleanCommandAllowlistPath は GUI から受け取った許可リストのパスを整える（前後の空白を除き、Clean する）。空ならそのまま空（既定の場所）。
func cleanCommandAllowlistPath(p string) string {
	if p = strings.TrimSpace(p); p == "" {
		return ""
	}
	return filepath.Clean(p)
}
