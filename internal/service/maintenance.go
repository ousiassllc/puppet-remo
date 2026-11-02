package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// DefaultMaintenanceInterval は保守の間隔（docs/components/overview.md §5.10。起動時と 1 時間ごと）。
const DefaultMaintenanceInterval = time.Hour

// 監査の保持の設定（docs/architecture/er.md の settings 表）。行が無い・値が不正なら既定値を使う。
const (
	settingAuditRetentionDays  = "audit.retention_days"
	settingAuditThumbsMaxBytes = "audit.thumbs_max_bytes"

	// DefaultAuditRetentionDays と DefaultThumbsMaxBytes は FR-AU04 の既定（30 日・1 GiB）。
	DefaultAuditRetentionDays = 30
	DefaultThumbsMaxBytes     = int64(1) << 30

	// maxAuditRetentionDays は保持期間の上限（100 年。時間の計算が溢れない範囲）。これを超える値は不正として既定値にする。
	maxAuditRetentionDays = 36500
	// maxJSONInteger は JSON の数値（float64）が整数として正確に表せる上限（2^53）。
	maxJSONInteger = int64(1) << 53
)

// MaintenanceConfig は NewMaintenance の設定。ConnRequests は必須。
type MaintenanceConfig struct {
	// ConnRequests は期限切れの接続要求を失効させる先。通知つきのラッパー（NewNotifyingConnRequests）を渡すと、
	// 失効が GUI の件数バッジに反映される。
	ConnRequests repository.ConnRequests
	// Audit が非 nil なら、監査の保持期間・サムネイルの容量・孤児ファイルの掃除（FR-AU04）も行う。
	// その場合は Thumbs と Settings が必須（nil のままなら NewMaintenance が失敗する）。nil なら接続要求の失効だけ。
	Audit    repository.Audit
	Thumbs   repository.ThumbStore
	Settings repository.Settings // audit.retention_days・audit.thumbs_max_bytes を保守の都度読む
	// Tasks が非 nil なら、Run の開始時に実行中・承認待ちのタスクを aborted にし（AbortActive。NFR-A08）、保守のたびに
	// audit.retention_days を過ぎた終了済みのタスクを削除する（PurgeFinishedBefore）。タスクの保持期間は監査と同じ設定を使うので、
	// Settings が必須（nil のままなら NewMaintenance が失敗する）。
	Tasks repository.Tasks
	// Clock は nil なら tailnet.SystemClock。
	Clock tailnet.Clock
	// Interval は 0 以下なら 1 時間。
	Interval time.Duration
	Logger   *slog.Logger // nil なら slog.Default()
}

// Maintenance は定期的な保守（docs/components/overview.md §5.10）。
// 接続要求の失効（pending は 24 時間で expired になり、最後の要求から 7 日を過ぎた行は削除される。FR-A06）と、
// Audit を渡されたときの監査の保持（保持期間を過ぎた行の削除、サムネイルの容量の上限、起動時の孤児ファイルの掃除。FR-AU04）を行う。
// Tasks を渡されたときは、タスクの回収（起動時の AbortActive。NFR-A08）と保持（保持期間を過ぎた終了済みタスクの削除）も行う。
type Maintenance struct {
	cfg MaintenanceConfig
	// recovered は AbortActiveTasks を 1 度でも試みたか。Run は、アプリが先に回収した（Start が Agent の起動前に呼ぶ）なら、
	// 動き出したタスクを巻き込まないよう、もう回収しない。
	recovered atomic.Bool
}

// NewMaintenance は Maintenance を作る。動かすには Run を goroutine で呼ぶ。
func NewMaintenance(cfg MaintenanceConfig) (*Maintenance, error) {
	if cfg.ConnRequests == nil {
		return nil, errors.New("service: maintenance requires ConnRequests")
	}
	if cfg.Audit != nil && (cfg.Thumbs == nil || cfg.Settings == nil) {
		return nil, errors.New("service: maintenance with Audit requires Thumbs and Settings")
	}
	if cfg.Tasks != nil && cfg.Settings == nil {
		return nil, errors.New("service: maintenance with Tasks requires Settings")
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

// RunOnce は保守を 1 回行う（孤児ファイルの掃除は含まない。起動時の SweepOrphans）。
// 各処理は互いに独立で、1 つが失敗しても残りを行い、エラーをまとめて返す。
func (m *Maintenance) RunOnce(ctx context.Context) error {
	errs := []error{m.cfg.ConnRequests.Expire(ctx, m.cfg.Clock.Now())}
	if m.cfg.Audit != nil {
		errs = append(errs, m.PurgeAuditBeforeRetention(ctx), m.PurgeThumbsOverCap(ctx))
	}
	if m.cfg.Tasks != nil {
		errs = append(errs, m.PurgeTasksBeforeRetention(ctx))
	}
	return errors.Join(errs...)
}

// PurgeAuditBeforeRetention は audit.retention_days を過ぎた監査ログの行を削除し、そのサムネイルのファイルも消す（FR-AU04）。
// 行の削除が先で、ファイルの削除は後。ファイルの削除に失敗しても残るのは孤児ファイルで、次の起動時の SweepOrphans が消す。
// Audit が無ければ何もしない。
func (m *Maintenance) PurgeAuditBeforeRetention(ctx context.Context) error {
	if m.cfg.Audit == nil {
		return nil
	}
	days, err := m.auditRetentionDays(ctx)
	if err != nil {
		return err
	}
	cutoff := m.cfg.Clock.Now().Add(-time.Duration(days) * 24 * time.Hour)
	removed, err := m.cfg.Audit.PurgeBefore(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("purge audit before retention: %w", err)
	}
	m.cfg.Logger.Debug("audit retention purge", "retention_days", days, "removed_thumbs", len(removed))
	return m.removeThumbFiles(removed)
}

// PurgeTasksBeforeRetention は audit.retention_days を過ぎた終了済みのタスク（finished_at が古い行）を削除する。
// 実行中・承認待ちのタスクは消さない。監査ログの task_id は NULL になり、ログは残る（ER「データの保守」）。Tasks が無ければ何もしない。
func (m *Maintenance) PurgeTasksBeforeRetention(ctx context.Context) error {
	if m.cfg.Tasks == nil {
		return nil
	}
	days, err := m.auditRetentionDays(ctx)
	if err != nil {
		return err
	}
	cutoff := m.cfg.Clock.Now().Add(-time.Duration(days) * 24 * time.Hour)
	n, err := m.cfg.Tasks.PurgeFinishedBefore(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("purge tasks before retention: %w", err)
	}
	m.cfg.Logger.Debug("task retention purge", "retention_days", days, "removed_tasks", n)
	return nil
}

// AbortActiveTasks は実行中・承認待ちのタスクをすべて aborted にする（起動時の回収。NFR-A08）。App.Start が、Listener（MCP の受付）・Agent を
// 動かす前に呼ぶ。呼ばれていなければ Run が最初の保守の前に 1 回呼ぶ（呼んだあとの Run は回収しない）。回収した件数を返す。Tasks が無ければ 0。
func (m *Maintenance) AbortActiveTasks(ctx context.Context) (int, error) {
	if m.cfg.Tasks == nil {
		return 0, nil
	}
	m.recovered.Store(true)
	n, err := m.cfg.Tasks.AbortActive(ctx, m.cfg.Clock.Now())
	if err != nil {
		return 0, fmt.Errorf("abort active tasks: %w", err)
	}
	if n > 0 {
		m.cfg.Logger.Warn("aborted tasks left active by the previous run", "count", n)
	} else {
		m.cfg.Logger.Debug("no active tasks to abort")
	}
	return n, nil
}

// PurgeThumbsOverCap はサムネイルの総容量が audit.thumbs_max_bytes を超えていたら、古い順に行とファイルを削除する（ログ行は残す。FR-AU04）。
// 保守の 1 時間ごと・起動時に加えて、サムネイルを保存するたびに呼べるよう公開している（複数 goroutine から呼んでよい）。
// Audit が無ければ何もしない。
func (m *Maintenance) PurgeThumbsOverCap(ctx context.Context) error {
	if m.cfg.Audit == nil {
		return nil
	}
	maxBytes, err := m.thumbsMaxBytes(ctx)
	if err != nil {
		return err
	}
	removed, err := m.cfg.Audit.PurgeThumbsOver(ctx, maxBytes)
	if err != nil {
		return fmt.Errorf("purge thumbnails over cap: %w", err)
	}
	if len(removed) > 0 {
		m.cfg.Logger.Info("thumbnails purged over the size cap", "max_bytes", maxBytes, "removed_thumbs", len(removed))
	}
	return m.removeThumbFiles(removed)
}

// SweepOrphans は起動時の掃除（FR-AU04）: ファイルが無いのに行がある audit_thumbnails の行を削除し、
// 行が無いのに thumbs/ にあるファイルを削除する。Audit が無ければ何もしない。
func (m *Maintenance) SweepOrphans(ctx context.Context) error {
	if m.cfg.Audit == nil {
		return nil
	}
	gone, err := m.cfg.Audit.PurgeMissingThumbs(ctx, m.thumbExists)
	if err != nil {
		return fmt.Errorf("purge thumbnail rows without files: %w", err)
	}
	paths, err := m.cfg.Audit.ThumbPaths(ctx)
	if err != nil {
		return fmt.Errorf("list thumbnail rows: %w", err)
	}
	known := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		known[p] = struct{}{}
	}
	if err := m.cfg.Thumbs.SweepOrphans(ctx, func(rel string) bool { _, ok := known[rel]; return ok }); err != nil {
		return fmt.Errorf("sweep orphan thumbnail files: %w", err)
	}
	if gone > 0 {
		m.cfg.Logger.Info("thumbnail rows without files removed", "removed_rows", gone)
	}
	return nil
}

// thumbExists はサムネイルのファイルがあるか。ErrNotFound だけを「無い」とする（読めないなどの一時的な失敗で行を消さない）。
func (m *Maintenance) thumbExists(rel string) bool {
	rc, err := m.cfg.Thumbs.Open(rel)
	if err == nil {
		_ = rc.Close()
		return true
	}
	return !errors.Is(err, repository.ErrNotFound)
}

// removeThumbFiles はファイルを消す。1 つ失敗しても残りを消し、失敗をまとめて返す。
func (m *Maintenance) removeThumbFiles(rels []string) error {
	var errs []error
	for _, rel := range rels {
		if err := m.cfg.Thumbs.Remove(rel); err != nil {
			errs = append(errs, fmt.Errorf("remove thumbnail file: %w", err))
		}
	}
	return errors.Join(errs...)
}

// auditRetentionDays は audit.retention_days（1〜36500 の整数。行が無い・不正なら既定値の 30 日）。
func (m *Maintenance) auditRetentionDays(ctx context.Context) (int64, error) {
	return m.positiveIntSetting(ctx, settingAuditRetentionDays, DefaultAuditRetentionDays, maxAuditRetentionDays)
}

// thumbsMaxBytes は audit.thumbs_max_bytes（1 以上の整数。行が無い・不正なら既定値の 1 GiB）。
func (m *Maintenance) thumbsMaxBytes(ctx context.Context) (int64, error) {
	return m.positiveIntSetting(ctx, settingAuditThumbsMaxBytes, DefaultThumbsMaxBytes, maxJSONInteger)
}

// positiveIntSetting は settings の key を 1〜upper の整数として読む。読み出し自体の失敗はエラー、
// 値が不正（数値でない・小数・範囲外）なら警告を出して def を返す（保守を止めない）。
func (m *Maintenance) positiveIntSetting(ctx context.Context, key string, def, upper int64) (int64, error) {
	v, invalid, err := readBoundedInt(ctx, m.cfg.Settings, key, def, upper)
	if invalid {
		m.cfg.Logger.Warn("invalid audit retention setting; using the default", "key", key, "default", def)
	}
	return v, err
}

// readBoundedInt は settings の key を 1〜upper の整数として読む（audit.retention_days・audit.thumbs_max_bytes の共通の規則）。
// 行が無ければ def。値が不正（数値でない・小数・範囲外）なら def と invalid = true。読み出し自体の失敗は err。
func readBoundedInt(ctx context.Context, settings repository.Settings, key string, def, upper int64) (v int64, invalid bool, err error) {
	raw, ok, err := settings.Get(ctx, key)
	if err != nil {
		return 0, false, fmt.Errorf("read setting %s: %w", key, err)
	}
	if !ok {
		return def, false, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f != math.Trunc(f) || f < 1 || f > float64(upper) {
		return def, true, nil
	}
	return int64(f), false, nil
}

// Run は起動時にタスクの回収（Tasks があり、まだ回収されていなければ AbortActive を 1 回）・孤児の掃除・保守を 1 回、以後 Interval ごとに保守を行い、ctx の終了で戻る。
// 失敗はログに残して続ける。回収は Run の先頭の 1 回だけ（動いているタスクを巻き込まないよう、Agent が動く前に呼ぶこと）。
func (m *Maintenance) Run(ctx context.Context) {
	if !m.recovered.Load() {
		if _, err := m.AbortActiveTasks(ctx); err != nil && ctx.Err() == nil {
			m.cfg.Logger.Error("task recovery failed", "err", err)
		}
	}
	if err := m.SweepOrphans(ctx); err != nil && ctx.Err() == nil {
		m.cfg.Logger.Error("orphan sweep failed", "err", err)
	}
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
