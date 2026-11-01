package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは LeaseManager・Control・KillSwitch が読む settings のキーと既定値（docs/architecture/er.md）。
const (
	settingLeaseIdleTimeoutSec = "lease.idle_timeout_sec"
	settingScreenshotMaxDim    = "screenshot.max_dimension"
	settingKillSwitchState     = "killswitch.state"
	// 監査の記録の設定（FR-AU02・FR-AU03・FR-S07）。
	settingAuditThumbnailsEnabled = "audit.thumbnails_enabled"
	settingAuditRecordText        = "audit.record_text"

	defaultMaxDimension = 1568
)

// LeaseIdleTimeout は設定 lease.idle_timeout_sec（既定 300 秒。FR-S04）を読む。
// 値が正の整数でなければ domain.ErrInvalidArgument。settings が nil なら既定値。
func LeaseIdleTimeout(ctx context.Context, settings repository.Settings) (time.Duration, error) {
	sec, err := loadPositiveInt(ctx, settings, settingLeaseIdleTimeoutSec, int(domain.DefaultLeaseIdleTimeout/time.Second))
	if err != nil {
		return 0, err
	}
	return time.Duration(sec) * time.Second, nil
}

// loadPositiveInt は key の数値設定を読む。未設定なら def。
func loadPositiveInt(ctx context.Context, settings repository.Settings, key string, def int) (int, error) {
	if settings == nil {
		return def, nil
	}
	raw, ok, err := settings.Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("read setting %s: %w", key, err)
	}
	if !ok {
		return def, nil
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || v < 1 || v > math.MaxInt32 || v != math.Trunc(v) {
		return 0, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("setting %s must be a positive integer", key))
	}
	return int(v), nil
}

// loadBool は key の真偽値の設定を読む。未設定なら def。値が真偽値でなければ domain.ErrInvalidArgument。settings が nil なら def。
func loadBool(ctx context.Context, settings repository.Settings, key string, def bool) (bool, error) {
	if settings == nil {
		return def, nil
	}
	raw, ok, err := settings.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("read setting %s: %w", key, err)
	}
	if !ok {
		return def, nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("setting %s must be a boolean", key))
	}
	return v, nil
}
