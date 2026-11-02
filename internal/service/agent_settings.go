package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルはホスト GUI の「エージェント設定」（FR-H11・G14・G22、FR-S10〜S19）と、Agent・FastPath・RuleJudge が
// 設定を読む窓口（AgentConfigReader）の実装。設定の書き込みはすべて操作主体 host で監査する。
// API キーの値は SecretStore だけに置き、ログ・監査・エラー・戻り値のどこにも出さない（NFR-S14）。

// 既定値と範囲（docs/architecture/er.md の settings 表、API 仕様 §7.3）。
const (
	defaultAgentModel          = "gpt-6.1-sol"
	defaultAgentMaxSteps       = 50
	defaultAgentMaxDurationSec = 600
	defaultAgentKeepShots      = 3
	defaultFastpathModel       = "jev-1.13"
	defaultFastpathMinConf     = 0.7
	defaultFastpathMaxSteps    = 15

	agentMinSteps       = 1
	agentMaxSteps       = 500
	agentMinDurationSec = 30
	agentMaxDurationSec = 7200
	agentMinKeepShots   = 1
	agentMaxKeepShots   = 10
	maxModelNameRunes   = 128
	// maxAPIKeyBytes は資格情報マネージャー（wincred）の値の上限（2560 バイト）。
	maxAPIKeyBytes = 2560
)

// PUT /agent・/agent/fastpath・/agent/*/api-key のフォームの項目名（ValidationError.Fields のキー。API 仕様 §7.3）。
// エージェントと高速パスは別のフォームなので、同じ名前（enabled・consent・model・max_steps）を共有する。
const (
	FieldAgentEnabled          = "enabled"
	FieldAgentConsent          = "consent"
	FieldAgentModel            = "model"
	FieldAgentMaxSteps         = "max_steps"
	FieldAgentMaxDurationSec   = "max_duration_sec"
	FieldAgentKeepScreenshots  = "keep_screenshots"
	FieldAgentDenyProcesses    = "deny_processes"
	FieldAgentApproveProcesses = "approve_processes"

	FieldFastpathEnabled        = "enabled"
	FieldFastpathConsent        = "consent"
	FieldFastpathModel          = "model"
	FieldFastpathMinConfidence  = "min_confidence"
	FieldFastpathMaxSteps       = "max_steps"
	FieldFastpathExcludedLabels = "excluded_labels"

	FieldAPIKey = "api_key"
)

// 監査のイベント名（kind = agent。docs/architecture/er.md「イベントのカタログ」）。
const (
	agentConsentAuditEvent    = "agent.consent"          // エージェントの有効化の同意・取り消し（detail.state: granted / revoked）
	fastpathEnableAuditEvent  = "agent.fastpath.enable"  // 高速パスの有効化（agent.consent とは別）
	fastpathDisableAuditEvent = "agent.fastpath.disable" // 高速パスの無効化（401 によるシステム側の無効化を含む）
)

// APIKey の監査の項目名（settings.update の detail のキー。値は registered / deleted で、キー自体は入れない）。
const (
	auditKeyOpenAIAPIKey   = "agent.api_key"
	auditKeyTypeSafeAPIKey = "agent.fastpath.api_key"

	// FastpathDisableUnauthorized は DisableFastpath の errorCode（TypeSafe が HTTP 401 を返した。FR-G21）。
	FastpathDisableUnauthorized = "typesafe_unauthorized"
)

var (
	defaultAgentDenyProcesses    = []string{"KeePass.exe", "KeePassXC.exe", "1Password.exe", "Bitwarden.exe"}
	defaultAgentApproveProcesses = []string{}
	defaultFastpathExcluded      = []string{
		"delete", "remove", "send", "submit", "purchase", "buy", "pay", "close", "quit", "exit", "uninstall", "format", "erase",
		"削除", "送信", "購入", "支払い", "閉じる", "終了", "アンインストール", "初期化",
	}
)

// AgentSettingsConfig は NewAgentSettings の設定。Settings・Secrets・Clock は必須。
type AgentSettingsConfig struct {
	Settings repository.Settings
	Secrets  platform.SecretStore
	Audit    AuditRecorder // nil なら記録しない
	Clock    domain.Clock
	Logger   *slog.Logger // nil なら slog.Default()
	// Cancel はエージェントの無効化・OpenAI キーの削除で、実行中のタスクを中断する（FR-G10。app が Agent.CancelAll を渡す）。
	// reason は監査用。nil なら何もしない。高速パスの無効化では呼ばない（実行中のタスクは OpenAI で続行する）。
	Cancel func(reason string)
}

// AgentSettingsService は AgentSettings と AgentConfigReader の実装。DisableFastpath（FR-G21）も持つ。
type AgentSettingsService struct {
	cfg AgentSettingsConfig
	log *slog.Logger
	// mu は読んで直して書く操作（保存・キーの削除・DisableFastpath）を直列化する。
	mu chanMutex
}

var (
	_ AgentSettings     = (*AgentSettingsService)(nil)
	_ AgentConfigReader = (*AgentSettingsService)(nil)
)

// NewAgentSettings は AgentSettingsService を作る。
func NewAgentSettings(cfg AgentSettingsConfig) (*AgentSettingsService, error) {
	if cfg.Settings == nil || cfg.Secrets == nil || cfg.Clock == nil {
		return nil, errors.New("service: agent settings requires Settings, Secrets and Clock")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &AgentSettingsService{cfg: cfg, log: log, mu: make(chanMutex, 1)}, nil
}

// chanMutex は ctx を待てる排他（容量 1 のチャネル）。設定の書き込みが I/O を挟むので、待ちを ctx で打ち切れるようにする。
type chanMutex chan struct{}

func (m chanMutex) lock(ctx context.Context) (unlock func(), err error) {
	select {
	case m <- struct{}{}:
		return func() { <-m }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ---- 読み取り ----

// Config は設定を既定値を適用して返す。保存済みの値が不正（型・範囲）なら、その項目は既定値にして運用ログに警告する。
func (s *AgentSettingsService) Config(ctx context.Context) (AgentConfig, error) {
	r := &settingReader{ctx: ctx, set: s.cfg.Settings, log: s.log}
	inRange := func(lo, hi int) func(int) bool { return func(n int) bool { return n >= lo && n <= hi } }
	list := func(v []string) bool { return v != nil }
	c := AgentConfig{
		Enabled:          readSetting(r, SettingAgentEnabled, false, nil),
		Model:            readSetting(r, SettingAgentModel, defaultAgentModel, validModelName),
		MaxSteps:         readSetting(r, SettingAgentMaxSteps, defaultAgentMaxSteps, inRange(agentMinSteps, agentMaxSteps)),
		MaxDurationSec:   readSetting(r, SettingAgentMaxDurationSec, defaultAgentMaxDurationSec, inRange(agentMinDurationSec, agentMaxDurationSec)),
		DenyProcesses:    slices.Clone(readSetting(r, SettingAgentDenyProcesses, defaultAgentDenyProcesses, list)),
		ApproveProcesses: slices.Clone(readSetting(r, SettingAgentApproveProcesses, defaultAgentApproveProcesses, list)),
		KeepScreenshots:  readSetting(r, SettingAgentKeepScreenshots, defaultAgentKeepShots, inRange(agentMinKeepShots, agentMaxKeepShots)),
		Fastpath: FastpathConfig{
			Enabled:        readSetting(r, SettingFastpathEnabled, false, nil),
			Model:          readSetting(r, SettingFastpathModel, defaultFastpathModel, validModelName),
			MinConfidence:  readSetting(r, SettingFastpathMinConfidence, defaultFastpathMinConf, func(f float64) bool { return f >= 0 && f <= 1 }),
			MaxSteps:       readSetting(r, SettingFastpathMaxSteps, defaultFastpathMaxSteps, inRange(agentMinSteps, agentMaxSteps)),
			ExcludedLabels: slices.Clone(readSetting(r, SettingFastpathExcludedLabels, defaultFastpathExcluded, list)),
		},
	}
	c.ConsentAt = readConsentAt(r, SettingAgentConsentAt)
	c.Fastpath.ConsentAt = readConsentAt(r, SettingFastpathConsentAt)
	if r.err != nil {
		return AgentConfig{}, r.err
	}
	return c, nil
}

// settingReader は設定の読み出しの文脈。最初の読み出しの失敗を err に残し、以後の読み出しは何もしない。
type settingReader struct {
	ctx context.Context
	set repository.Settings
	log *slog.Logger
	err error
}

// readSetting は key を T として読む。行が無ければ def。null・型が違う・valid（nil なら常に真）が偽なら、警告して def
// （HostSettings.Load と同じ規則）。読み出し自体の失敗は r.err に残して def。
func readSetting[T any](r *settingReader, key string, def T, valid func(T) bool) T {
	if r.err != nil {
		return def
	}
	raw, found, err := r.set.Get(r.ctx, key)
	if err != nil {
		r.err = fmt.Errorf("read setting %s: %w", key, err)
		return def
	}
	if !found {
		return def
	}
	var v T
	if string(raw) == "null" || json.Unmarshal(raw, &v) != nil || (valid != nil && !valid(v)) {
		r.log.Warn("invalid agent setting; using the default", "key", key)
		return def
	}
	return v
}

// readConsentAt は consent_at（UTC ミリ秒の数値）を読む。行が無い・null は nil。不正な値は警告して nil（同意が無い扱い）。
func readConsentAt(r *settingReader, key string) *time.Time {
	raw, found, err := r.set.Get(r.ctx, key)
	if r.err != nil {
		return nil
	}
	if err != nil {
		r.err = fmt.Errorf("read setting %s: %w", key, err)
		return nil
	}
	if !found || string(raw) == "null" {
		return nil
	}
	var ms float64
	if err := json.Unmarshal(raw, &ms); err != nil || ms <= 0 || ms != math.Trunc(ms) || ms > float64(maxJSONInteger) {
		r.log.Warn("invalid agent setting; treating consent as absent", "key", key)
		return nil
	}
	t := time.UnixMilli(int64(ms)).UTC()
	return &t
}

func (s *AgentSettingsService) OpenAIKeyRegistered(context.Context) (bool, error) {
	return s.keyRegistered(platform.SecretOpenAI)
}

func (s *AgentSettingsService) TypeSafeKeyRegistered(context.Context) (bool, error) {
	return s.keyRegistered(platform.SecretTypeSafe)
}

// keyRegistered は name の API キーが登録されているかを返す。値は読んですぐに捨てる（呼び出し側にも返さない）。
func (s *AgentSettingsService) keyRegistered(name string) (bool, error) {
	_, err := s.cfg.Secrets.Get(name)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, platform.ErrNotFound):
		return false, nil
	}
	return false, fmt.Errorf("check the credential %s: %w", name, err)
}

func (s *AgentSettingsService) Load(ctx context.Context) (AgentSettingsView, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return AgentSettingsView{}, err
	}
	openai, err := s.OpenAIKeyRegistered(ctx)
	if err != nil {
		return AgentSettingsView{}, err
	}
	typesafe, err := s.TypeSafeKeyRegistered(ctx)
	if err != nil {
		return AgentSettingsView{}, err
	}
	return AgentSettingsView{AgentConfig: cfg, OpenAIKeyRegistered: openai, TypeSafeKeyRegistered: typesafe}, nil
}

// ---- エージェントの保存 ----

func (s *AgentSettingsService) SaveAgent(ctx context.Context, in AgentSettingsInput) error {
	in.Model = strings.TrimSpace(in.Model)
	in.DenyProcesses = cleanProcessList(in.DenyProcesses)
	in.ApproveProcesses = cleanProcessList(in.ApproveProcesses)
	unlock, err := s.mu.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if err := validateAgentInput(in, cur); err != nil {
		return err
	}
	if in.Enabled {
		registered, err := s.OpenAIKeyRegistered(ctx)
		if err != nil {
			return err
		}
		if !registered {
			return domain.ErrAgentDisabled.WithMsg("register the OpenAI API key before enabling the agent")
		}
	}
	changed := map[string]any{}
	note := func(key string, differs bool, val any) {
		if differs {
			changed[key] = val
		}
	}
	note(SettingAgentModel, cur.Model != in.Model, in.Model)
	note(SettingAgentMaxSteps, cur.MaxSteps != in.MaxSteps, in.MaxSteps)
	note(SettingAgentMaxDurationSec, cur.MaxDurationSec != in.MaxDurationSec, in.MaxDurationSec)
	note(SettingAgentKeepScreenshots, cur.KeepScreenshots != in.KeepScreenshots, in.KeepScreenshots)
	note(SettingAgentDenyProcesses, !slices.Equal(cur.DenyProcesses, in.DenyProcesses), in.DenyProcesses)
	note(SettingAgentApproveProcesses, !slices.Equal(cur.ApproveProcesses, in.ApproveProcesses), in.ApproveProcesses)
	note(SettingAgentEnabled, cur.Enabled != in.Enabled, in.Enabled)

	// 無効化は enabled を最初に、有効化は最後に書く（途中で失敗しても、同意なしで有効な状態を残さない）。
	if !in.Enabled {
		if err := s.setValue(ctx, SettingAgentEnabled, false); err != nil {
			return err
		}
	}
	for _, kv := range []struct {
		key string
		val any
	}{
		{SettingAgentModel, in.Model}, {SettingAgentMaxSteps, in.MaxSteps}, {SettingAgentMaxDurationSec, in.MaxDurationSec},
		{SettingAgentKeepScreenshots, in.KeepScreenshots}, {SettingAgentDenyProcesses, in.DenyProcesses},
		{SettingAgentApproveProcesses, in.ApproveProcesses},
	} {
		if err := s.setValue(ctx, kv.key, kv.val); err != nil {
			return err
		}
	}
	now := s.cfg.Clock.Now()
	switch {
	case in.Enabled && (!cur.Enabled || cur.ConsentAt == nil):
		if err := s.setValue(ctx, SettingAgentConsentAt, now.UnixMilli()); err != nil {
			return err
		}
		if err := s.setValue(ctx, SettingAgentEnabled, true); err != nil {
			return err
		}
		s.recordAgent(ctx, agentConsentAuditEvent, auditOK, "", map[string]any{"state": "granted", "consent_at": now.UnixMilli()})
	case in.Enabled:
		if err := s.setValue(ctx, SettingAgentEnabled, true); err != nil {
			return err
		}
	case cur.Enabled || cur.ConsentAt != nil:
		if err := s.setValue(ctx, SettingAgentConsentAt, nil); err != nil {
			return err
		}
		s.recordAgent(ctx, agentConsentAuditEvent, auditOK, "", map[string]any{"state": "revoked"})
	}
	s.recordSettingsUpdate(ctx, changed)
	if cur.Enabled && !in.Enabled {
		s.cancelTasks("agent_disabled")
	}
	return nil
}

// validateAgentInput は in（整形済み）を検査し、不備は *ValidationError（項目名 → メッセージ）にする。
// cur は現在の設定: すでに同意して有効なら、保存のたびに同意のチェックを求めない。
func validateAgentInput(in AgentSettingsInput, cur AgentConfig) error {
	bad := map[string]string{}
	if in.Enabled && !in.Consent && (!cur.Enabled || cur.ConsentAt == nil) {
		bad[FieldAgentConsent] = "有効にするには、スクリーンショットを OpenAI へ送信することへの同意が必要です。"
	}
	if !validModelName(in.Model) {
		bad[FieldAgentModel] = fmt.Sprintf("モデル名を 1〜%d 文字で指定してください。", maxModelNameRunes)
	}
	if in.MaxSteps < agentMinSteps || in.MaxSteps > agentMaxSteps {
		bad[FieldAgentMaxSteps] = fmt.Sprintf("%d〜%d の整数を指定してください。", agentMinSteps, agentMaxSteps)
	}
	if in.MaxDurationSec < agentMinDurationSec || in.MaxDurationSec > agentMaxDurationSec {
		bad[FieldAgentMaxDurationSec] = fmt.Sprintf("%d〜%d 秒の整数を指定してください。", agentMinDurationSec, agentMaxDurationSec)
	}
	if in.KeepScreenshots < agentMinKeepShots || in.KeepScreenshots > agentMaxKeepShots {
		bad[FieldAgentKeepScreenshots] = fmt.Sprintf("%d〜%d の整数を指定してください。", agentMinKeepShots, agentMaxKeepShots)
	}
	if len(bad) > 0 {
		return &ValidationError{Fields: bad}
	}
	return nil
}

// validModelName はモデル名が空でなく、128 文字以内で、制御文字を含まないか。
func validModelName(s string) bool {
	n := utf8.RuneCountInString(s)
	return s == strings.TrimSpace(s) && n > 0 && n <= maxModelNameRunes && !strings.ContainsFunc(s, unicode.IsControl)
}

// cleanProcessList は各項目の前後の空白を除き、空の項目と重複（RuleJudge と同じ比較: パスの区切りより後ろの大文字小文字を
// 区別しない一致）を除く。順序は保つ。常に nil でない（保存すると [] になる）。
func cleanProcessList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if k := normalizeProcessName(p); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// cleanLabelList は各項目の前後の空白を除き、空の項目と大文字小文字を区別しない重複を除く。
func cleanLabelList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		if k := strings.ToLower(l); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	return out
}

// ---- API キー ----

func (s *AgentSettingsService) SetOpenAIKey(ctx context.Context, key string) error {
	return s.setKey(ctx, platform.SecretOpenAI, auditKeyOpenAIAPIKey, "OpenAI", key)
}

func (s *AgentSettingsService) SetTypeSafeKey(ctx context.Context, key string) error {
	return s.setKey(ctx, platform.SecretTypeSafe, auditKeyTypeSafeAPIKey, "TypeSafe", key)
}

// setKey は API キーの形を検査して SecretStore に保存し、キー名だけを settings.update で監査する。値はどこにも出さない。
func (s *AgentSettingsService) setKey(ctx context.Context, secret, auditKey, label, key string) error {
	key, err := validateAPIKey(key)
	if err != nil {
		return err
	}
	if err := s.cfg.Secrets.Set(secret, key); err != nil {
		return fmt.Errorf("save the %s API key: %w", label, err)
	}
	s.log.Info("api key registered", "provider", label)
	s.recordSettingsUpdate(ctx, map[string]any{auditKey: "registered"})
	return nil
}

// validateAPIKey は API キーの形（前後の空白を除いて空でない・UTF-8・制御文字なし・2560 バイト以下）を検査する。
// エラーの文面に入力を含めない。
func validateAPIKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	msg := ""
	switch {
	case key == "":
		msg = "API キーを入力してください。"
	case len(key) > maxAPIKeyBytes:
		msg = fmt.Sprintf("API キーが長すぎます（%d バイトまで）。", maxAPIKeyBytes)
	case !utf8.ValidString(key) || strings.ContainsFunc(key, unicode.IsControl):
		msg = "API キーに使えない文字が含まれています。"
	}
	if msg != "" {
		return "", &ValidationError{Fields: map[string]string{FieldAPIKey: msg}}
	}
	return key, nil
}

// DeleteOpenAIKey は先にエージェントを無効にして同意を取り消し、実行中のタスクを中断してから、キーを削除する
// （途中で失敗しても、キーの無い有効なエージェントを残さない）。登録が無くても成功する。
func (s *AgentSettingsService) DeleteOpenAIKey(ctx context.Context) error {
	unlock, err := s.mu.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if err := s.setValue(ctx, SettingAgentEnabled, false); err != nil {
		return err
	}
	if cur.Enabled || cur.ConsentAt != nil {
		if err := s.setValue(ctx, SettingAgentConsentAt, nil); err != nil {
			return err
		}
		s.recordAgent(ctx, agentConsentAuditEvent, auditOK, "", map[string]any{"state": "revoked", "reason": "api_key_deleted"})
	}
	s.cancelTasks("api_key_deleted")
	if err := s.cfg.Secrets.Delete(platform.SecretOpenAI); err != nil && !errors.Is(err, platform.ErrNotFound) {
		return fmt.Errorf("delete the OpenAI API key: %w", err)
	}
	s.log.Info("api key deleted", "provider", "OpenAI")
	changed := map[string]any{auditKeyOpenAIAPIKey: "deleted"}
	if cur.Enabled {
		changed[SettingAgentEnabled] = false
	}
	s.recordSettingsUpdate(ctx, changed)
	return nil
}

// ---- 高速パス ----

func (s *AgentSettingsService) SaveFastpath(ctx context.Context, in FastpathSettingsInput) error {
	in.Model = strings.TrimSpace(in.Model)
	in.ExcludedLabels = cleanLabelList(in.ExcludedLabels)
	unlock, err := s.mu.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if err := validateFastpathInput(in, cur.Fastpath); err != nil {
		return err
	}
	if in.Enabled {
		registered, err := s.TypeSafeKeyRegistered(ctx)
		if err != nil {
			return err
		}
		if !registered {
			return domain.ErrAgentDisabled.WithMsg("register the TypeSafe API key before enabling the fast path")
		}
	}
	fp := cur.Fastpath
	changed := map[string]any{}
	note := func(key string, differs bool, val any) {
		if differs {
			changed[key] = val
		}
	}
	note(SettingFastpathModel, fp.Model != in.Model, in.Model)
	note(SettingFastpathMinConfidence, fp.MinConfidence != in.MinConfidence, in.MinConfidence)
	note(SettingFastpathMaxSteps, fp.MaxSteps != in.MaxSteps, in.MaxSteps)
	note(SettingFastpathExcludedLabels, !slices.Equal(fp.ExcludedLabels, in.ExcludedLabels), in.ExcludedLabels)
	note(SettingFastpathEnabled, fp.Enabled != in.Enabled, in.Enabled)

	if !in.Enabled {
		if err := s.setValue(ctx, SettingFastpathEnabled, false); err != nil {
			return err
		}
	}
	for _, kv := range []struct {
		key string
		val any
	}{
		{SettingFastpathModel, in.Model}, {SettingFastpathMinConfidence, in.MinConfidence},
		{SettingFastpathMaxSteps, in.MaxSteps}, {SettingFastpathExcludedLabels, in.ExcludedLabels},
	} {
		if err := s.setValue(ctx, kv.key, kv.val); err != nil {
			return err
		}
	}
	now := s.cfg.Clock.Now()
	switch {
	case in.Enabled && (!fp.Enabled || fp.ConsentAt == nil):
		if err := s.setValue(ctx, SettingFastpathConsentAt, now.UnixMilli()); err != nil {
			return err
		}
		if err := s.setValue(ctx, SettingFastpathEnabled, true); err != nil {
			return err
		}
		s.recordAgent(ctx, fastpathEnableAuditEvent, auditOK, "", map[string]any{"consent_at": now.UnixMilli()})
	case in.Enabled:
		if err := s.setValue(ctx, SettingFastpathEnabled, true); err != nil {
			return err
		}
	case fp.Enabled || fp.ConsentAt != nil:
		if err := s.setValue(ctx, SettingFastpathConsentAt, nil); err != nil {
			return err
		}
		s.recordAgent(ctx, fastpathDisableAuditEvent, auditOK, "", map[string]any{"reason": "settings"})
	}
	s.recordSettingsUpdate(ctx, changed)
	return nil
}

func validateFastpathInput(in FastpathSettingsInput, cur FastpathConfig) error {
	bad := map[string]string{}
	if in.Enabled && !in.Consent && (!cur.Enabled || cur.ConsentAt == nil) {
		bad[FieldFastpathConsent] = "有効にするには、TypeSafe へデータを送信することへの同意が必要です。"
	}
	if !validModelName(in.Model) {
		bad[FieldFastpathModel] = fmt.Sprintf("モデル名を 1〜%d 文字で指定してください。", maxModelNameRunes)
	}
	if math.IsNaN(in.MinConfidence) || in.MinConfidence < 0 || in.MinConfidence > 1 {
		bad[FieldFastpathMinConfidence] = "0〜1 の数値を指定してください。"
	}
	if in.MaxSteps < agentMinSteps || in.MaxSteps > agentMaxSteps {
		bad[FieldFastpathMaxSteps] = fmt.Sprintf("%d〜%d の整数を指定してください。", agentMinSteps, agentMaxSteps)
	}
	if len(bad) > 0 {
		return &ValidationError{Fields: bad}
	}
	return nil
}

// DeleteTypeSafeKey は先に高速パスを無効にして consent_at を消してから、キーを削除する。登録が無くても成功する。
// 実行中のタスクは中断しない（以後の高速ステップを行わず、OpenAI で続行する）。
func (s *AgentSettingsService) DeleteTypeSafeKey(ctx context.Context) error {
	unlock, err := s.mu.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if err := s.disableFastpathLocked(ctx, cur.Fastpath, domain.Actor{Kind: domain.ActorHost, Label: hostLabel}, "", "api_key_deleted"); err != nil {
		return err
	}
	if err := s.cfg.Secrets.Delete(platform.SecretTypeSafe); err != nil && !errors.Is(err, platform.ErrNotFound) {
		return fmt.Errorf("delete the TypeSafe API key: %w", err)
	}
	s.log.Info("api key deleted", "provider", "TypeSafe")
	s.recordSettingsUpdate(ctx, map[string]any{auditKeyTypeSafeAPIKey: "deleted"})
	return nil
}

// DisableFastpath は高速パスをシステム側で無効にする（FR-G21。Agent が TypeSafe の HTTP 401 を受けたときに呼ぶ）。
// agent.fastpath.enabled を false にして consent_at を消し、agent.fastpath.disable を by（actor_type system）で、
// error_code = errorCode（FastpathDisableUnauthorized）、result = error で記録する。すでに無効で同意も無ければ何もしない（冪等）。
// 実行中のタスクは中断しない。TypeSafe のキーは残す（GUI が再登録を促す）。
func (s *AgentSettingsService) DisableFastpath(ctx context.Context, by domain.Actor, errorCode string) error {
	if by.Kind == "" {
		by = domain.Actor{Kind: domain.ActorSystem, Label: systemLabel}
	}
	unlock, err := s.mu.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.Config(ctx)
	if err != nil {
		return err
	}
	return s.disableFastpathLocked(ctx, cur.Fastpath, by, truncateRunes(errorCode, 64), errorCode)
}

// disableFastpathLocked は fp（現在の設定）が有効か同意を持つなら、無効にして consent_at を消し、agent.fastpath.disable を記録する。
// errorCode が空なら result = ok、あれば result = error。reason は detail.reason。mu を持って呼ぶ。
func (s *AgentSettingsService) disableFastpathLocked(ctx context.Context, fp FastpathConfig, by domain.Actor, errorCode, reason string) error {
	if !fp.Enabled && fp.ConsentAt == nil {
		return nil
	}
	if err := s.setValue(ctx, SettingFastpathEnabled, false); err != nil {
		return err
	}
	if err := s.setValue(ctx, SettingFastpathConsentAt, nil); err != nil {
		return err
	}
	result := auditOK
	if errorCode != "" {
		result = auditError
	}
	e := s.agentEntry(fastpathDisableAuditEvent, result, errorCode, map[string]any{"reason": reason})
	e.ActorType, e.ClientID, e.ClientLabel, e.SessionID = by.Kind, by.ClientID, by.Label, by.SessionID
	if by.Kind == domain.ActorHost && by.Label == "" {
		e.ClientLabel = hostLabel
	}
	s.record(ctx, e)
	s.log.Info("fast path disabled", "reason", reason, "error_code", errorCode)
	return nil
}

// ---- 書き込み・監査の部品 ----

// setValue は key に val（nil なら JSON の null = 値なし）を保存する。
func (s *AgentSettingsService) setValue(ctx context.Context, key string, val any) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	if err := s.cfg.Settings.Set(ctx, key, raw); err != nil {
		return fmt.Errorf("save setting %s: %w", key, err)
	}
	return nil
}

func (s *AgentSettingsService) cancelTasks(reason string) {
	if s.cfg.Cancel != nil {
		s.cfg.Cancel(reason)
	}
}

func (s *AgentSettingsService) agentEntry(event, result, errorCode string, detail map[string]any) repository.AuditEntry {
	return repository.AuditEntry{
		TS: s.cfg.Clock.Now(), Kind: "agent", Event: event, ActorType: domain.ActorHost, ClientLabel: hostLabel,
		Result: result, ErrorCode: errorCode, DetailJSON: marshalDetail(detail),
	}
}

// recordAgent は kind = agent の host の操作を記録する。
func (s *AgentSettingsService) recordAgent(ctx context.Context, event, result, errorCode string, detail map[string]any) {
	s.record(ctx, s.agentEntry(event, result, errorCode, detail))
}

// recordSettingsUpdate は変えた項目（キー名 → 新しい値。API キーは registered / deleted だけ）を settings.update で記録する。
func (s *AgentSettingsService) recordSettingsUpdate(ctx context.Context, changed map[string]any) {
	if len(changed) == 0 {
		return
	}
	e := s.agentEntry(hostSettingsAuditKey, auditOK, "", changed)
	e.Kind = "settings"
	s.record(ctx, e)
}

func (s *AgentSettingsService) record(ctx context.Context, e repository.AuditEntry) {
	if s.cfg.Audit != nil {
		s.cfg.Audit.Record(ctx, e)
	}
}

// systemLabel はシステム側の操作（DisableFastpath の既定）の client_label。hostLabel と対になる。
const systemLabel = "(system)"
