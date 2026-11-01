package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
	// UpdateApplyMode は update.apply_mode（UpdateApplyConfirm / UpdateApplyAutoIdle。既定 confirm。FR-S09）。
	// 読む側（更新の適用）は #74 で入る。それまでは保存するだけ。
	UpdateApplyMode string
	Autostart       bool // ログオン時の自動起動（OS が真実の源。DB には持たない）
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
	FieldUpdateApplyMode        = "update.apply_mode"
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
	if s.UpdateApplyMode != UpdateApplyConfirm && s.UpdateApplyMode != UpdateApplyAutoIdle {
		bad[FieldUpdateApplyMode] = "「ホストが確認して適用」か「アイドル時に自動で適用」を選んでください。"
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
	Save(ctx context.Context, s HostSettings) (HostSettings, error)
	// AutostartAvailable は自動起動の切り替えが使えるか（OS の登録手段があるか）。
	AutostartAvailable() bool
}

// HostSettingsConfig は NewHostSettings の設定。Settings は必須。
type HostSettingsConfig struct {
	Settings  repository.Settings
	Autostart Autostart     // nil なら自動起動は常に無効として扱い、変更は保存しない
	Audit     AuditRecorder // nil なら記録しない
	Clock     domain.Clock  // Audit があるとき必須
	Logger    *slog.Logger  // nil なら slog.Default()。保存済みの audit.* の数値が不正で既定値を使うときの警告に使う
	// OnChange は保存に成功し、値が変わったときに old・new を渡して呼ぶ（ポートの再 Listen、ホットキーの再登録など、
	// 影響のあるコンポーネントへの通知）。nil なら何もしない。保存の呼び出しの中で同期に呼ぶ。
	OnChange func(old, updated HostSettings)
}

type hostSettings struct {
	cfg HostSettingsConfig
	log *slog.Logger
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
	applyMode, err := loadUpdateApplyMode(ctx, h.cfg.Settings)
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
		AuditThumbsMaxBytes: thumbsMax, UpdateApplyMode: applyMode, Autostart: autostart,
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
	if err := s.Validate(); err != nil {
		return HostSettings{}, err
	}
	old, err := h.Load(ctx)
	if err != nil {
		// 保存済みの値が壊れていても、検証済みの入力で上書きして復旧できるようにする。
		old = HostSettings{}
	}
	// 書き込みは項目ごとで、途中で失敗しうる。実際に反映できた分（applied）だけを OnChange・監査へ渡し、
	// 保存済みの項目と動いているコンポーネントがずれないようにする。
	applied := old
	fail := func(err error) (HostSettings, error) {
		h.committed(ctx, old, applied)
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
		{settingUpdateApplyMode, s.UpdateApplyMode, func(a *HostSettings) { a.UpdateApplyMode = s.UpdateApplyMode }},
	} {
		raw, err := json.Marshal(kv.val)
		if err != nil {
			return fail(err)
		}
		if err := h.cfg.Settings.Set(ctx, kv.key, raw); err != nil {
			return fail(fmt.Errorf("save setting %s: %w", kv.key, err))
		}
		kv.apply(&applied)
	}
	if h.cfg.Autostart != nil {
		if err := h.cfg.Autostart.Set(ctx, s.Autostart); err != nil {
			return fail(fmt.Errorf("save autostart: %w", err))
		}
		applied.Autostart = s.Autostart
	} else {
		s.Autostart = false
	}
	h.committed(ctx, old, s)
	return s, nil
}

// committed は old から updated へ実際に変わった内容を、監査と OnChange（ポートの再 Listen、ホットキーの再登録など）へ渡す。
func (h *hostSettings) committed(ctx context.Context, old, updated HostSettings) {
	h.audit(ctx, old, updated)
	if h.cfg.OnChange != nil && old != updated {
		h.cfg.OnChange(old, updated)
	}
}

// audit は変更した項目の名前と新しい値を記録する（値は範囲内の数値・真偽値・列挙・ホットキーで、秘密ではない。
// audit.record_text の変更は、本文の記録の有無の切り替えとして残す価値があるため値ごと残す）。
func (h *hostSettings) audit(ctx context.Context, old, updated HostSettings) {
	if h.cfg.Audit == nil {
		return
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
		{FieldAuditRecordText, old.AuditRecordText != updated.AuditRecordText, updated.AuditRecordText},
		{FieldAuditRetentionDays, old.AuditRetentionDays != updated.AuditRetentionDays, updated.AuditRetentionDays},
		{FieldAuditThumbsMaxBytes, old.AuditThumbsMaxBytes != updated.AuditThumbsMaxBytes, updated.AuditThumbsMaxBytes},
		{FieldUpdateApplyMode, old.UpdateApplyMode != updated.UpdateApplyMode, updated.UpdateApplyMode},
		{fieldAutostart, old.Autostart != updated.Autostart, updated.Autostart},
	} {
		if f.diff {
			changed[f.key] = f.val
		}
	}
	if len(changed) == 0 {
		return
	}
	raw, err := json.Marshal(changed)
	if err != nil {
		raw = nil
	}
	h.cfg.Audit.Record(ctx, repository.AuditEntry{
		TS: h.cfg.Clock.Now(), Kind: "settings", Event: hostSettingsAuditKey, ActorType: domain.ActorHost,
		Result: auditOK, DetailJSON: raw,
	})
}
