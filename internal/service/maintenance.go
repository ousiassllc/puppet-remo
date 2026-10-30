package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// DefaultMaintenanceInterval は保守の間隔（docs/components/overview.md §5.10。起動時と 1 時間ごと）。
const DefaultMaintenanceInterval = time.Hour

// MaintenanceConfig は NewMaintenance の設定。ConnRequests は必須。
type MaintenanceConfig struct {
	// ConnRequests は期限切れの接続要求を失効させる先。通知つきのラッパー（NewNotifyingConnRequests）を渡すと、
	// 失効が GUI の件数バッジに反映される。
	ConnRequests repository.ConnRequests
	// Clock は nil なら tailnet.SystemClock。
	Clock tailnet.Clock
	// Interval は 0 以下なら 1 時間。
	Interval time.Duration
	Logger   *slog.Logger // nil なら slog.Default()
}

// Maintenance は定期的な保守（docs/components/overview.md §5.10）。M1 は接続要求の失効だけを行う
// （pending は 24 時間で expired になり、最後の要求から 7 日を過ぎた行は削除される。FR-A06）。
// 監査の保持期間・サムネイル・孤児ファイル・タスクの保持期間と AbortActive は、それぞれ M3・M4 で足す。
type Maintenance struct {
	cfg MaintenanceConfig
}

// NewMaintenance は Maintenance を作る。動かすには Run を goroutine で呼ぶ。
func NewMaintenance(cfg MaintenanceConfig) (*Maintenance, error) {
	if cfg.ConnRequests == nil {
		return nil, errors.New("service: maintenance requires ConnRequests")
	}
	if cfg.Clock == nil {
		cfg.Clock = tailnet.SystemClock
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultMaintenanceInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Maintenance{cfg: cfg}, nil
}

// RunOnce は保守を 1 回行う。
func (m *Maintenance) RunOnce(ctx context.Context) error {
	return m.cfg.ConnRequests.Expire(ctx, m.cfg.Clock.Now())
}

// Run は起動時に 1 回、以後 Interval ごとに保守を行い、ctx の終了で戻る。失敗はログに残して続ける。
func (m *Maintenance) Run(ctx context.Context) {
	for {
		if err := m.RunOnce(ctx); err != nil && ctx.Err() == nil {
			m.cfg.Logger.Error("maintenance failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.cfg.Clock.After(m.cfg.Interval):
		}
	}
}
