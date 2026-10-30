package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは GUI の設定画面が扱う M1 の設定（FR-S01〜S04、FR-S08。docs/architecture/er.md のキー一覧）。
const (
	settingServerPort    = "server.port"
	settingKillHotkey    = "killswitch.hotkey"
	defaultServerPort    = 8765
	defaultKillHotkey    = "Ctrl+Alt+Shift+K"
	settingMinPort       = 1024
	settingMaxPort       = 65535
	settingMinMaxDim     = 256
	settingMaxMaxDim     = 4096
	settingMinIdleSec    = 30
	settingMaxIdleSec    = 3600
	maxHotkeyLength      = 64
	hostSettingsAuditKey = "settings.update"
)

// HostSettings は M1 の設定。
type HostSettings struct {
	Port                   int    // server.port（1024〜65535）
	KillSwitchHotkey       string // killswitch.hotkey（`Ctrl+Alt+Shift+K` の書式）
	ScreenshotMaxDimension int    // screenshot.max_dimension（256〜4096）
	LeaseIdleTimeoutSec    int    // lease.idle_timeout_sec（30〜3600）
	Autostart              bool   // ログオン時の自動起動（OS が真実の源。DB には持たない）
}

// 設定の項目名（ValidationError.Fields のキー。API 仕様 §7.3 のフォームのキー）。
const (
	FieldPort                   = "server.port"
	FieldKillSwitchHotkey       = "killswitch.hotkey"
	FieldScreenshotMaxDimension = "screenshot.max_dimension"
	FieldLeaseIdleTimeoutSec    = "lease.idle_timeout_sec"
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
	// OnChange は保存に成功し、値が変わったときに old・new を渡して呼ぶ（ポートの再 Listen、ホットキーの再登録など、
	// 影響のあるコンポーネントへの通知）。nil なら何もしない。保存の呼び出しの中で同期に呼ぶ。
	OnChange func(old, updated HostSettings)
}

type hostSettings struct{ cfg HostSettingsConfig }

// NewHostSettings は HostSettingsService を作る。
func NewHostSettings(cfg HostSettingsConfig) (HostSettingsService, error) {
	if cfg.Settings == nil {
		return nil, errors.New("service: host settings requires Settings")
	}
	if cfg.Audit != nil && cfg.Clock == nil {
		return nil, errors.New("service: host settings requires Clock when Audit is set")
	}
	return &hostSettings{cfg: cfg}, nil
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
		LeaseIdleTimeoutSec: idle, Autostart: autostart,
	}, nil
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

// audit は変更した項目の名前だけを記録する（値は設定の範囲内の数値・ホットキーで秘密ではないが、差分の事実だけで足りる）。
func (h *hostSettings) audit(ctx context.Context, old, updated HostSettings) {
	if h.cfg.Audit == nil {
		return
	}
	changed := map[string]any{}
	if old.Port != updated.Port {
		changed[FieldPort] = updated.Port
	}
	if old.KillSwitchHotkey != updated.KillSwitchHotkey {
		changed[FieldKillSwitchHotkey] = updated.KillSwitchHotkey
	}
	if old.ScreenshotMaxDimension != updated.ScreenshotMaxDimension {
		changed[FieldScreenshotMaxDimension] = updated.ScreenshotMaxDimension
	}
	if old.LeaseIdleTimeoutSec != updated.LeaseIdleTimeoutSec {
		changed[FieldLeaseIdleTimeoutSec] = updated.LeaseIdleTimeoutSec
	}
	if old.Autostart != updated.Autostart {
		changed["autostart"] = updated.Autostart
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
