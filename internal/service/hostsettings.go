package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは GUI の設定画面が扱う設定（FR-S01〜S04、FR-S06〜S09。docs/architecture/er.md のキー一覧）。
// 監査ログの設定（audit.*）のキー・既定値・範囲は、読み手（audit_thumb.go・control_ops.go・maintenance.go）と同じものを使う。
const (
	settingServerPort           = "server.port"
	settingKillHotkey           = "killswitch.hotkey"
	settingViewerDefaultQuality = "viewer.default_quality"
	settingViewerDefaultFPS     = "viewer.default_fps"
	settingUpdateApplyMode      = "update.apply_mode"
	settingUpdateChannel        = "update.channel"
	settingUpdateLastCheckedAt  = "update.last_checked_at"
	defaultServerPort           = 8765
	defaultKillHotkey           = "Ctrl+Alt+Shift+K"
	defaultViewerQuality        = 70
	defaultViewerFPS            = 10
	settingMinPort              = 1024
	settingMaxPort              = 65535
	settingMinMaxDim            = 256
	settingMaxMaxDim            = 4096
	settingMinIdleSec           = 30
	settingMaxIdleSec           = 3600
	viewerMinQuality            = 1
	viewerMaxQuality            = 100
	viewerMinFPS                = 1
	viewerMaxFPS                = 30
	maxHotkeyLength             = 64
	hostSettingsAuditKey        = "settings.update"
)

// 更新の適用方法（update.apply_mode。FR-S09）。既定は UpdateApplyConfirm。
const (
	UpdateApplyConfirm  = "confirm"   // ホストが GUI で確認してから適用する
	UpdateApplyAutoIdle = "auto_idle" // リースが空いている（アイドルの）ときに自動で適用する
)

// HostSettings は設定画面の設定（M1 の 4 項目と自動起動、M2 の FR-S06、M3 の FR-S07・FR-S09）。
type HostSettings struct {
	Port                   int    // server.port（1024〜65535）
	KillSwitchHotkey       string // killswitch.hotkey（`Ctrl+Alt+Shift+K` の書式）
	ScreenshotMaxDimension int    // screenshot.max_dimension（256〜4096）
	LeaseIdleTimeoutSec    int    // lease.idle_timeout_sec（30〜3600）
	ViewerDefaultQuality   int    // viewer.default_quality（1〜100）。Web UI の既定の JPEG 品質（FR-S06）
	ViewerDefaultFPS       int    // viewer.default_fps（1〜30）。Web UI の既定の FPS（FR-S06）
	// 監査ログ（FR-S07）。
	AuditThumbnailsEnabled bool  // audit.thumbnails_enabled（既定 true。FR-AU02）
	AuditRecordText        bool  // audit.record_text（既定 false。FR-AU03）
	AuditRetentionDays     int   // audit.retention_days（1〜36500。既定 30。FR-AU04）
	AuditThumbsMaxBytes    int64 // audit.thumbs_max_bytes（1〜2^53。既定 1 GiB。単位はバイト。FR-AU04）
	// ファイル転送（#264。FR-S24）。MCP の put_file などが使う置き場所と上限。MCP からは変えられない。
	FileTransferRoot          string // file_transfer.root（絶対パス。空なら既定の %LOCALAPPDATA%\PuppetRemo\inbox）
	FileTransferMaxFileBytes  int64  // file_transfer.max_file_bytes（1〜2^53。既定 100 MiB）
	FileTransferMaxTotalBytes int64  // file_transfer.max_total_bytes（1〜2^53。既定 1 GiB）
	// UpdateApplyMode は update.apply_mode（UpdateApplyConfirm / UpdateApplyAutoIdle。既定 confirm。FR-S09）。
	UpdateApplyMode string
	// UpdateChannel は update.channel（domain.ChannelStable / domain.ChannelNightly の文字列。既定 stable。FR-S09）。
	UpdateChannel string
	Autostart     bool // ログオン時の自動起動（OS が真実の源。DB には持たない）
}

// 設定の項目名（ValidationError.Fields のキー。API 仕様 §7.3 のフォームのキー）。
const (
	FieldPort                   = "server.port"
	FieldKillSwitchHotkey       = "killswitch.hotkey"
	FieldScreenshotMaxDimension = "screenshot.max_dimension"
	FieldLeaseIdleTimeoutSec    = "lease.idle_timeout_sec"
	FieldViewerDefaultQuality   = "viewer.default_quality"
	FieldViewerDefaultFPS       = "viewer.default_fps"
	FieldAuditThumbnails        = "audit.thumbnails_enabled"
	FieldAuditRecordText        = "audit.record_text"
	FieldAuditRetentionDays     = "audit.retention_days"
	FieldAuditThumbsMaxBytes    = "audit.thumbs_max_bytes"
	FieldFileTransferRoot       = settingFileTransferRoot
	FieldFileTransferMaxFile    = settingFileTransferMaxFile
	FieldFileTransferMaxTotal   = settingFileTransferMaxTotal
	FieldUpdateApplyMode        = "update.apply_mode"
	FieldUpdateChannel          = "update.channel"
	fieldAutostart              = "autostart"
)

// ValidationError は入力の項目別の不備（GUI は 422 でフォームを再描画する）。domain.CodeInvalidArgument として扱える。
type ValidationError struct {
	Fields map[string]string // 項目名 → 利用者向けのメッセージ
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for k, v := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", k, v))
	}
	return "invalid settings: " + strings.Join(parts, "; ")
}

// Unwrap は errors.Is / domain.CodeOf が invalid_argument と判定できるようにする。
func (e *ValidationError) Unwrap() error { return domain.ErrInvalidArgument }

// Validate は s の範囲を検査し、不備があれば *ValidationError を返す。
func (s HostSettings) Validate() error {
	bad := map[string]string{}
	if s.Port < settingMinPort || s.Port > settingMaxPort {
		bad[FieldPort] = fmt.Sprintf("%d〜%d の整数を指定してください。", settingMinPort, settingMaxPort)
	}
	if s.ScreenshotMaxDimension < settingMinMaxDim || s.ScreenshotMaxDimension > settingMaxMaxDim {
		bad[FieldScreenshotMaxDimension] = fmt.Sprintf("%d〜%d の整数を指定してください。", settingMinMaxDim, settingMaxMaxDim)
	}
	if s.LeaseIdleTimeoutSec < settingMinIdleSec || s.LeaseIdleTimeoutSec > settingMaxIdleSec {
		bad[FieldLeaseIdleTimeoutSec] = fmt.Sprintf("%d〜%d 秒の整数を指定してください。", settingMinIdleSec, settingMaxIdleSec)
	}
	if s.ViewerDefaultQuality < viewerMinQuality || s.ViewerDefaultQuality > viewerMaxQuality {
		bad[FieldViewerDefaultQuality] = fmt.Sprintf("%d〜%d の整数を指定してください。", viewerMinQuality, viewerMaxQuality)
	}
	if s.ViewerDefaultFPS < viewerMinFPS || s.ViewerDefaultFPS > viewerMaxFPS {
		bad[FieldViewerDefaultFPS] = fmt.Sprintf("%d〜%d の整数を指定してください。", viewerMinFPS, viewerMaxFPS)
	}
	if s.AuditRetentionDays < 1 || s.AuditRetentionDays > maxAuditRetentionDays {
		bad[FieldAuditRetentionDays] = fmt.Sprintf("1〜%d 日の整数を指定してください。", maxAuditRetentionDays)
	}
	if s.AuditThumbsMaxBytes < 1 || s.AuditThumbsMaxBytes > maxJSONInteger {
		bad[FieldAuditThumbsMaxBytes] = fmt.Sprintf("1〜%d バイトの整数を指定してください。", maxJSONInteger)
	}
	if s.FileTransferRoot != "" {
		if err := domain.ValidateTransferRoot(s.FileTransferRoot); err != nil {
			bad[FieldFileTransferRoot] = "ドライブ文字で始まる絶対パスを、末尾の「\\」・「..」なしで指定してください（ネットワーク共有・UNC・ドライブの直下は不可）。空なら既定の場所を使います。"
		}
	}
	if s.FileTransferMaxFileBytes < 1 || s.FileTransferMaxFileBytes > maxJSONInteger {
		bad[FieldFileTransferMaxFile] = fmt.Sprintf("1〜%d バイトの整数を指定してください。", maxJSONInteger)
	}
	if s.FileTransferMaxTotalBytes < 1 || s.FileTransferMaxTotalBytes > maxJSONInteger {
		bad[FieldFileTransferMaxTotal] = fmt.Sprintf("1〜%d バイトの整数を指定してください。", maxJSONInteger)
	}
	if s.UpdateApplyMode != UpdateApplyConfirm && s.UpdateApplyMode != UpdateApplyAutoIdle {
		bad[FieldUpdateApplyMode] = "「ホストが確認して適用」か「アイドル時に自動で適用」を選んでください。"
	}
	if !domain.Channel(s.UpdateChannel).Valid() {
		bad[FieldUpdateChannel] = "「正式版」か「ナイトリー」を選んでください。"
	}
	if msg := validateHotkey(s.KillSwitchHotkey); msg != "" {
		bad[FieldKillSwitchHotkey] = msg
	}
	if len(bad) > 0 {
		return &ValidationError{Fields: bad}
	}
	return nil
}

// validateHotkey はホットキーの書式（修飾キー 1 つ以上 + キー 1 つ）を検査し、不備なら利用者向けのメッセージを返す。
func validateHotkey(spec string) string {
	const format = "Ctrl+Alt+Shift+K の書式で、修飾キーを 1 つ以上含めて指定してください。"
	if spec != strings.TrimSpace(spec) || spec == "" || len(spec) > maxHotkeyLength {
		return format
	}
	combo, err := domain.ParseKeyCombo(spec)
	if err != nil || len(combo.Mods) == 0 {
		return format
	}
	return ""
}

// Autostart はログオン時の自動起動の登録（FR-S08）。有効・無効は OS（スタートアップ登録）が真実の源。
// 実装は platform の層（Windows のスタートアップ登録）が与える。
type Autostart interface {
	Enabled(ctx context.Context) (bool, error)
	Set(ctx context.Context, enabled bool) error
}

// HostSettingsService は設定画面の読み書き。
type HostSettingsService interface {
	// Load は現在の設定を返す。未設定の項目は既定値。
	Load(ctx context.Context) (HostSettings, error)
	// Save は s を検証して保存する。不備は *ValidationError（何も保存しない）。保存した設定を返す。
	// 変更があれば settings.update を同期で監査し、書けなければ保存した項目（自動起動を含む）を元に戻して domain.ErrAuditUnavailable を返す
	// （fail-closed。#182。audit_commit.go）。OnChange は監査に書けたあとにだけ呼ぶ。
	Save(ctx context.Context, s HostSettings) (HostSettings, error)
	// AutostartAvailable は自動起動の切り替えが使えるか（OS の登録手段があるか）。
	AutostartAvailable() bool
}

// HostSettingsConfig は NewHostSettings の設定。Settings は必須。
type HostSettingsConfig struct {
	Settings  repository.Settings
	Autostart Autostart         // nil なら自動起動は常に無効として扱い、変更は保存しない
	Audit     SyncAuditRecorder // nil なら記録しない。Save は RecordSync で記録し、書けなければ保存を元に戻す
	Clock     domain.Clock      // Audit があるとき必須
	Logger    *slog.Logger      // nil なら slog.Default()。保存済みの audit.* の数値が不正で既定値を使うときの警告と、監査に書けなかったときの出力先
	// OnChange は保存に成功し、値が変わって監査にも書けたときに old・new を渡して呼ぶ（ポートの再 Listen、ホットキーの再登録など、
	// 影響のあるコンポーネントへの通知）。nil なら何もしない。保存の呼び出しの中で同期に呼ぶ。
	OnChange func(old, updated HostSettings)
}

type hostSettings struct {
	cfg HostSettingsConfig
	log *slog.Logger
	// mu は Save の「読む → 書く → 監査 → 失敗なら戻す」を 1 つの臨界区間にする（並行した保存のロールバックが、別の保存を取り消さないため）。
	mu sync.Mutex
}

// NewHostSettings は HostSettingsService を作る。
func NewHostSettings(cfg HostSettingsConfig) (HostSettingsService, error) {
	if cfg.Settings == nil {
		return nil, errors.New("service: host settings requires Settings")
	}
	if cfg.Audit != nil && cfg.Clock == nil {
		return nil, errors.New("service: host settings requires Clock when Audit is set")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &hostSettings{cfg: cfg, log: log}, nil
}

func (h *hostSettings) AutostartAvailable() bool { return h.cfg.Autostart != nil }

func (h *hostSettings) Load(ctx context.Context) (HostSettings, error) {
	port, err := loadPositiveInt(ctx, h.cfg.Settings, settingServerPort, defaultServerPort)
	if err != nil {
		return HostSettings{}, err
	}
	dim, err := loadPositiveInt(ctx, h.cfg.Settings, settingScreenshotMaxDim, defaultMaxDimension)
	if err != nil {
		return HostSettings{}, err
	}
	idle, err := loadPositiveInt(ctx, h.cfg.Settings, settingLeaseIdleTimeoutSec, int(domain.DefaultLeaseIdleTimeout.Seconds()))
	if err != nil {
		return HostSettings{}, err
	}
	quality, err := loadPositiveInt(ctx, h.cfg.Settings, settingViewerDefaultQuality, defaultViewerQuality)
	if err != nil {
		return HostSettings{}, err
	}
	fps, err := loadPositiveInt(ctx, h.cfg.Settings, settingViewerDefaultFPS, defaultViewerFPS)
	if err != nil {
		return HostSettings{}, err
	}
	thumbsOn, err := loadBool(ctx, h.cfg.Settings, settingAuditThumbnailsEnabled, true)
	if err != nil {
		return HostSettings{}, err
	}
	recordText, err := loadBool(ctx, h.cfg.Settings, settingAuditRecordText, false)
	if err != nil {
		return HostSettings{}, err
	}
	retention, err := h.loadAuditInt(ctx, settingAuditRetentionDays, DefaultAuditRetentionDays, maxAuditRetentionDays)
	if err != nil {
		return HostSettings{}, err
	}
	thumbsMax, err := h.loadAuditInt(ctx, settingAuditThumbsMaxBytes, DefaultThumbsMaxBytes, maxJSONInteger)
	if err != nil {
		return HostSettings{}, err
	}
	fileRoot, err := h.loadFileTransferRoot(ctx)
	if err != nil {
		return HostSettings{}, err
	}
	fileMax, err := h.loadFileTransferInt(ctx, settingFileTransferMaxFile, domain.DefaultFileMaxBytes)
	if err != nil {
		return HostSettings{}, err
	}
	fileTotal, err := h.loadFileTransferInt(ctx, settingFileTransferMaxTotal, domain.DefaultFileTotalMaxBytes)
	if err != nil {
		return HostSettings{}, err
	}
	applyMode, err := loadUpdateApplyMode(ctx, h.cfg.Settings)
	if err != nil {
		return HostSettings{}, err
	}
	channel, err := loadUpdateChannel(ctx, h.cfg.Settings)
	if err != nil {
		return HostSettings{}, err
	}
	hotkey := defaultKillHotkey
	raw, ok, err := h.cfg.Settings.Get(ctx, settingKillHotkey)
	if err != nil {
		return HostSettings{}, fmt.Errorf("read setting %s: %w", settingKillHotkey, err)
	}
	if ok {
		if err := json.Unmarshal(raw, &hotkey); err != nil || hotkey == "" {
			return HostSettings{}, domain.ErrInvalidArgument.WithMsg("setting " + settingKillHotkey + " must be a non-empty string")
		}
	}
	autostart := false
	if h.cfg.Autostart != nil {
		if autostart, err = h.cfg.Autostart.Enabled(ctx); err != nil {
			return HostSettings{}, fmt.Errorf("read autostart: %w", err)
		}
	}
	return HostSettings{
		Port: port, KillSwitchHotkey: hotkey, ScreenshotMaxDimension: dim,
		LeaseIdleTimeoutSec: idle, ViewerDefaultQuality: quality, ViewerDefaultFPS: fps,
		AuditThumbnailsEnabled: thumbsOn, AuditRecordText: recordText, AuditRetentionDays: int(retention),
		AuditThumbsMaxBytes: thumbsMax, FileTransferRoot: fileRoot, FileTransferMaxFileBytes: fileMax, FileTransferMaxTotalBytes: fileTotal,
		UpdateApplyMode: applyMode, UpdateChannel: string(channel), Autostart: autostart,
	}, nil
}

// loadAuditInt は audit.retention_days・audit.thumbs_max_bytes を、保守（Maintenance）と同じ規則で読む。
// 値が不正（数値でない・小数・範囲外）なら警告を出して def を返す（実際に効いている値を表示し、保存し直して直せるようにする）。
func (h *hostSettings) loadAuditInt(ctx context.Context, key string, def, upper int64) (int64, error) {
	v, invalid, err := readBoundedInt(ctx, h.cfg.Settings, key, def, upper)
	if invalid {
		h.log.Warn("invalid audit retention setting; using the default", "key", key, "default", def)
	}
	return v, err
}

// loadFileTransferRoot は file_transfer.root を設定画面に出す値として読む。未設定は空（既定の場所）。文字列なら、形が不正でも
// そのまま返す（画面に出して、保存し直して直せるようにする。使う側の FileTransfer は不正な値のとき fail closed で使えない）。文字列でなければ空にして警告する。
func (h *hostSettings) loadFileTransferRoot(ctx context.Context) (string, error) {
	raw, ok, err := h.cfg.Settings.Get(ctx, settingFileTransferRoot)
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", settingFileTransferRoot, err)
	}
	if !ok {
		return "", nil
	}
	var root string
	if err := json.Unmarshal(raw, &root); err != nil {
		h.log.Warn("invalid file transfer folder setting; file transfer is disabled until it is saved again", "key", settingFileTransferRoot)
		return "", nil
	}
	return root, nil
}

// loadFileTransferInt は file_transfer の上限（バイト）を、FileTransfer と同じ規則で読む（不正な値は既定値と警告）。
func (h *hostSettings) loadFileTransferInt(ctx context.Context, key string, def int64) (int64, error) {
	v, invalid, err := readBoundedInt(ctx, h.cfg.Settings, key, def, maxJSONInteger)
	if invalid {
		h.log.Warn("invalid file transfer size limit setting; using the default", "key", key, "default", def)
	}
	return v, err
}

// loadUpdateApplyMode は update.apply_mode（既定 confirm）を読む。未知の値は domain.ErrInvalidArgument。
func loadUpdateApplyMode(ctx context.Context, settings repository.Settings) (string, error) {
	raw, ok, err := settings.Get(ctx, settingUpdateApplyMode)
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", settingUpdateApplyMode, err)
	}
	if !ok {
		return UpdateApplyConfirm, nil
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err != nil || (mode != UpdateApplyConfirm && mode != UpdateApplyAutoIdle) {
		return "", domain.ErrInvalidArgument.WithMsg("setting " + settingUpdateApplyMode + " must be \"confirm\" or \"auto_idle\"")
	}
	return mode, nil
}

// loadUpdateChannel は update.channel（既定 stable）を読む。未知の値は domain.ErrInvalidArgument。
func loadUpdateChannel(ctx context.Context, settings repository.Settings) (domain.Channel, error) {
	raw, ok, err := settings.Get(ctx, settingUpdateChannel)
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", settingUpdateChannel, err)
	}
	if !ok {
		return domain.ChannelStable, nil
	}
	var ch domain.Channel
	if err := json.Unmarshal(raw, &ch); err != nil || !ch.Valid() {
		return "", domain.ErrInvalidArgument.WithMsg("setting " + settingUpdateChannel + " must be \"stable\" or \"nightly\"")
	}
	return ch, nil
}

// ViewerDefaults は設定 viewer.default_quality・viewer.default_fps（既定 70・10。FR-S06）を読む。
// 値が範囲（品質 1〜100、FPS 1〜30）の整数でなければ domain.ErrInvalidArgument。settings が nil なら既定値。
func ViewerDefaults(ctx context.Context, settings repository.Settings) (quality, fps int, err error) {
	if quality, err = loadPositiveInt(ctx, settings, settingViewerDefaultQuality, defaultViewerQuality); err != nil {
		return 0, 0, err
	}
	if fps, err = loadPositiveInt(ctx, settings, settingViewerDefaultFPS, defaultViewerFPS); err != nil {
		return 0, 0, err
	}
	if quality > viewerMaxQuality || fps > viewerMaxFPS {
		return 0, 0, domain.ErrInvalidArgument.WithMsg("viewer default settings are out of range")
	}
	return quality, fps, nil
}

func (h *hostSettings) Save(ctx context.Context, s HostSettings) (HostSettings, error) {
	s.KillSwitchHotkey = strings.TrimSpace(s.KillSwitchHotkey)
	s.FileTransferRoot = cleanFileTransferRoot(s.FileTransferRoot)
	if err := s.Validate(); err != nil {
		return HostSettings{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	old, err := h.Load(ctx)
	if err != nil {
		// 保存済みの値が壊れていても、検証済みの入力で上書きして復旧できるようにする。
		old = HostSettings{}
	}
	// 書き込みは項目ごとで、途中で失敗しうる。実際に反映できた分（applied）だけを OnChange・監査へ渡し、
	// 保存済みの項目と動いているコンポーネントがずれないようにする。反映できた分を監査に書けなければ、その分も元に戻す（undo）。
	applied := old
	var undo undoLog
	fail := func(err error) (HostSettings, error) {
		if auditErr := h.committed(ctx, old, applied, undo); auditErr != nil {
			return HostSettings{}, auditErr
		}
		return HostSettings{}, err
	}
	for _, kv := range []struct {
		key   string
		val   any
		apply func(*HostSettings)
	}{
		{settingServerPort, s.Port, func(a *HostSettings) { a.Port = s.Port }},
		{settingKillHotkey, s.KillSwitchHotkey, func(a *HostSettings) { a.KillSwitchHotkey = s.KillSwitchHotkey }},
		{settingScreenshotMaxDim, s.ScreenshotMaxDimension, func(a *HostSettings) { a.ScreenshotMaxDimension = s.ScreenshotMaxDimension }},
		{settingLeaseIdleTimeoutSec, s.LeaseIdleTimeoutSec, func(a *HostSettings) { a.LeaseIdleTimeoutSec = s.LeaseIdleTimeoutSec }},
		{settingViewerDefaultQuality, s.ViewerDefaultQuality, func(a *HostSettings) { a.ViewerDefaultQuality = s.ViewerDefaultQuality }},
		{settingViewerDefaultFPS, s.ViewerDefaultFPS, func(a *HostSettings) { a.ViewerDefaultFPS = s.ViewerDefaultFPS }},
		{settingAuditThumbnailsEnabled, s.AuditThumbnailsEnabled, func(a *HostSettings) { a.AuditThumbnailsEnabled = s.AuditThumbnailsEnabled }},
		{settingAuditRecordText, s.AuditRecordText, func(a *HostSettings) { a.AuditRecordText = s.AuditRecordText }},
		{settingAuditRetentionDays, s.AuditRetentionDays, func(a *HostSettings) { a.AuditRetentionDays = s.AuditRetentionDays }},
		{settingAuditThumbsMaxBytes, s.AuditThumbsMaxBytes, func(a *HostSettings) { a.AuditThumbsMaxBytes = s.AuditThumbsMaxBytes }},
		{settingFileTransferRoot, s.FileTransferRoot, func(a *HostSettings) { a.FileTransferRoot = s.FileTransferRoot }},
		{settingFileTransferMaxFile, s.FileTransferMaxFileBytes, func(a *HostSettings) { a.FileTransferMaxFileBytes = s.FileTransferMaxFileBytes }},
		{settingFileTransferMaxTotal, s.FileTransferMaxTotalBytes, func(a *HostSettings) { a.FileTransferMaxTotalBytes = s.FileTransferMaxTotalBytes }},
		{settingUpdateApplyMode, s.UpdateApplyMode, func(a *HostSettings) { a.UpdateApplyMode = s.UpdateApplyMode }},
		{settingUpdateChannel, s.UpdateChannel, func(a *HostSettings) { a.UpdateChannel = s.UpdateChannel }},
	} {
		raw, err := json.Marshal(kv.val)
		if err != nil {
			return fail(err)
		}
		if err := setSetting(ctx, h.cfg.Settings, &undo, kv.key, raw); err != nil {
			return fail(fmt.Errorf("save setting %s: %w", kv.key, err))
		}
		kv.apply(&applied)
	}
	if h.cfg.Autostart != nil {
		prev, prevErr := h.cfg.Autostart.Enabled(ctx)
		if err := h.cfg.Autostart.Set(ctx, s.Autostart); err != nil {
			return fail(fmt.Errorf("save autostart: %w", err))
		}
		undo.add(func(ctx context.Context) error {
			if prevErr != nil {
				return fmt.Errorf("restore autostart: previous state unknown: %w", prevErr) // 元の状態が分からないので戻せない
			}
			return h.cfg.Autostart.Set(ctx, prev)
		})
		applied.Autostart = s.Autostart
	} else {
		s.Autostart = false
	}
	if err := h.committed(ctx, old, s, undo); err != nil {
		return HostSettings{}, err
	}
	return s, nil
}

// committed は old から updated へ実際に変わった内容を、監査（同期。書けなければ undo で戻して失敗を返す）と、
// 書けたあとの OnChange（ポートの再 Listen、ホットキーの再登録など）へ渡す。
func (h *hostSettings) committed(ctx context.Context, old, updated HostSettings, undo undoLog) error {
	if entry, ok := h.auditEntry(old, updated); ok {
		if err := commitAudited(ctx, h.log, h.cfg.Audit, AuditSubjectHostSettings, undo, entry); err != nil {
			return err
		}
	}
	if h.cfg.OnChange != nil && old != updated {
		h.cfg.OnChange(old, updated)
	}
	return nil
}

// auditEntry は変更した項目の名前と新しい値の監査の行を作る（値は範囲内の数値・真偽値・列挙・ホットキーで、秘密ではない）。
// ただし audit.record_text は、変えたことだけを残して値は入れない（null。本文の記録の設定を監査の経路に流さない）。
// 変わった項目が無い（または Audit が nil）なら ok = false。
func (h *hostSettings) auditEntry(old, updated HostSettings) (entry repository.AuditEntry, ok bool) {
	if h.cfg.Audit == nil {
		return repository.AuditEntry{}, false
	}
	changed := map[string]any{}
	for _, f := range []struct {
		key  string
		diff bool
		val  any
	}{
		{FieldPort, old.Port != updated.Port, updated.Port},
		{FieldKillSwitchHotkey, old.KillSwitchHotkey != updated.KillSwitchHotkey, updated.KillSwitchHotkey},
		{FieldScreenshotMaxDimension, old.ScreenshotMaxDimension != updated.ScreenshotMaxDimension, updated.ScreenshotMaxDimension},
		{FieldLeaseIdleTimeoutSec, old.LeaseIdleTimeoutSec != updated.LeaseIdleTimeoutSec, updated.LeaseIdleTimeoutSec},
		{FieldViewerDefaultQuality, old.ViewerDefaultQuality != updated.ViewerDefaultQuality, updated.ViewerDefaultQuality},
		{FieldViewerDefaultFPS, old.ViewerDefaultFPS != updated.ViewerDefaultFPS, updated.ViewerDefaultFPS},
		{FieldAuditThumbnails, old.AuditThumbnailsEnabled != updated.AuditThumbnailsEnabled, updated.AuditThumbnailsEnabled},
		{FieldAuditRecordText, old.AuditRecordText != updated.AuditRecordText, nil},
		{FieldAuditRetentionDays, old.AuditRetentionDays != updated.AuditRetentionDays, updated.AuditRetentionDays},
		{FieldAuditThumbsMaxBytes, old.AuditThumbsMaxBytes != updated.AuditThumbsMaxBytes, updated.AuditThumbsMaxBytes},
		{FieldFileTransferRoot, old.FileTransferRoot != updated.FileTransferRoot, updated.FileTransferRoot},
		{FieldFileTransferMaxFile, old.FileTransferMaxFileBytes != updated.FileTransferMaxFileBytes, updated.FileTransferMaxFileBytes},
		{FieldFileTransferMaxTotal, old.FileTransferMaxTotalBytes != updated.FileTransferMaxTotalBytes, updated.FileTransferMaxTotalBytes},
		{FieldUpdateApplyMode, old.UpdateApplyMode != updated.UpdateApplyMode, updated.UpdateApplyMode},
		{FieldUpdateChannel, old.UpdateChannel != updated.UpdateChannel, updated.UpdateChannel},
		{fieldAutostart, old.Autostart != updated.Autostart, updated.Autostart},
	} {
		if f.diff {
			changed[f.key] = f.val
		}
	}
	if len(changed) == 0 {
		return repository.AuditEntry{}, false
	}
	raw, err := json.Marshal(changed)
	if err != nil {
		raw = nil
	}
	return repository.AuditEntry{
		TS: h.cfg.Clock.Now(), Kind: "settings", Event: hostSettingsAuditKey, ActorType: domain.ActorHost,
		Result: auditOK, DetailJSON: raw,
	}, true
}
