package guitest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// AgentSettings は gui.AgentSettings の偽実装。本物（service.AgentSettings）と同じ規則を持つ:
// 範囲の検証（*service.ValidationError）、有効化には同意が必要（無ければ consent の項目エラー）、API キー未登録での有効化は
// domain.ErrAgentDisabled（高速パスは conflict）、OpenAI キーの削除でエージェントを無効にし、TypeSafe キーの削除で高速パスを無効にして同意を消す。
// API キーは登録の有無の真偽値でしか持たない（値は保存しない。NFR-S14）。
type AgentSettings struct {
	mu   sync.Mutex
	view service.AgentSettingsView
	// Clock は同意の日時（nil なら time.Now）。
	Clock domain.Clock

	// SetKeyErr は SetOpenAIKey / SetTypeSafeKey が返すエラー（保存の失敗を模す）。nil なら成功。
	SetKeyErr error
	// SetKeyCalls は鍵の登録の呼び出し回数（値は残さない）。
	SetKeyCalls int
	// Calls は呼び出された操作名の履歴（引数は残さない）。
	Calls []string
	// LastAgent・LastFastpath は直近に渡された入力（リストの分割などの検査用）。
	LastAgent    service.AgentSettingsInput
	LastFastpath service.FastpathSettingsInput
	// LoadErr は Load の失敗。
	LoadErr error
	// SaveErr は SaveAgent / SaveFastpath の検証の後に返す失敗。
	SaveErr error
}

// NewAgentSettings は既定値（無効・キー未登録）の AgentSettings を返す。
func NewAgentSettings(clock domain.Clock) *AgentSettings {
	return &AgentSettings{Clock: clock, view: service.AgentSettingsView{AgentConfig: service.AgentConfig{
		Model: "gpt-6.1-sol", MaxSteps: 50, MaxDurationSec: 600, KeepScreenshots: 3,
		DenyProcesses: []string{"KeePass.exe", "KeePassXC.exe", "1Password.exe", "Bitwarden.exe"},
		Fastpath: service.FastpathConfig{
			Model: "jev-1.13", MinConfidence: 0.7, MaxSteps: 15,
			ExcludedLabels: []string{"delete", "remove", "send", "submit", "purchase", "buy", "pay", "close", "quit", "exit", "uninstall", "format", "erase", "削除", "送信", "購入", "支払い", "閉じる", "終了", "アンインストール", "初期化"},
		},
	}}}
}

// Registered は API キーの登録の有無を直接設定する（テスト・デモの初期状態用）。
func (a *AgentSettings) Registered(openAI, typeSafe bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.view.OpenAIKeyRegistered, a.view.TypeSafeKeyRegistered = openAI, typeSafe
}

// State は現在の読み取り結果を返す（検査用）。
func (a *AgentSettings) State() service.AgentSettingsView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.view
}

func (a *AgentSettings) now() *time.Time {
	t := time.Now()
	if a.Clock != nil {
		t = a.Clock.Now()
	}
	return &t
}

func (a *AgentSettings) Load(context.Context) (service.AgentSettingsView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.LoadErr != nil {
		return service.AgentSettingsView{}, a.LoadErr
	}
	v := a.view
	v.DenyProcesses = append([]string(nil), v.DenyProcesses...)
	v.ApproveProcesses = append([]string(nil), v.ApproveProcesses...)
	v.Fastpath.ExcludedLabels = append([]string(nil), v.Fastpath.ExcludedLabels...)
	return v, nil
}

func inRange(bad map[string]string, key string, n, lo, hi int, label string) {
	if n < lo || n > hi {
		bad[key] = fmt.Sprintf("%sは %d〜%d で指定してください。", label, lo, hi)
	}
}

func (a *AgentSettings) SaveAgent(_ context.Context, in service.AgentSettingsInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, "SaveAgent")
	a.LastAgent = in
	bad := map[string]string{}
	if strings.TrimSpace(in.Model) == "" {
		bad[service.SettingAgentModel] = "モデルを指定してください。"
	}
	inRange(bad, service.SettingAgentMaxSteps, in.MaxSteps, 1, 500, "ステップ数の上限")
	inRange(bad, service.SettingAgentMaxDurationSec, in.MaxDurationSec, 30, 7200, "実行時間の上限")
	inRange(bad, service.SettingAgentKeepScreenshots, in.KeepScreenshots, 1, 10, "スクリーンショットの保持枚数")
	if in.Enabled && !in.Consent {
		bad[service.SettingAgentConsentAt] = "有効にするには、OpenAI へのスクリーンショット送信への同意が必要です。"
	}
	if len(bad) > 0 {
		return &service.ValidationError{Fields: bad}
	}
	if in.Enabled && !a.view.OpenAIKeyRegistered {
		return domain.ErrAgentDisabled
	}
	if a.SaveErr != nil {
		return a.SaveErr
	}
	a.view.Model, a.view.MaxSteps, a.view.MaxDurationSec, a.view.KeepScreenshots = in.Model, in.MaxSteps, in.MaxDurationSec, in.KeepScreenshots
	a.view.DenyProcesses, a.view.ApproveProcesses = in.DenyProcesses, in.ApproveProcesses
	switch {
	case in.Enabled && a.view.ConsentAt == nil:
		a.view.ConsentAt = a.now()
	case !in.Enabled:
		a.view.ConsentAt = nil
	}
	a.view.Enabled = in.Enabled
	return nil
}

func (a *AgentSettings) SetOpenAIKey(_ context.Context, key string) error {
	return a.setKey("SetOpenAIKey", key, func() { a.view.OpenAIKeyRegistered = true })
}

func (a *AgentSettings) SetTypeSafeKey(_ context.Context, key string) error {
	return a.setKey("SetTypeSafeKey", key, func() { a.view.TypeSafeKeyRegistered = true })
}

// setKey はキーが空でないことだけを検査し、登録の有無だけを記録する（値は捨てる）。
func (a *AgentSettings) setKey(name, key string, register func()) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, name)
	a.SetKeyCalls++
	if strings.TrimSpace(key) == "" {
		return &service.ValidationError{Fields: map[string]string{"api_key": "API キーを入力してください。"}}
	}
	if a.SetKeyErr != nil {
		return a.SetKeyErr
	}
	register()
	return nil
}

// DeleteOpenAIKey はキーを消し、エージェントを無効にする（実行中のタスクの中断は実体が行う）。
func (a *AgentSettings) DeleteOpenAIKey(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, "DeleteOpenAIKey")
	a.view.OpenAIKeyRegistered, a.view.Enabled, a.view.ConsentAt = false, false, nil
	return nil
}

func (a *AgentSettings) SaveFastpath(_ context.Context, in service.FastpathSettingsInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, "SaveFastpath")
	a.LastFastpath = in
	bad := map[string]string{}
	if strings.TrimSpace(in.Model) == "" {
		bad[service.SettingFastpathModel] = "モデルを指定してください。"
	}
	if in.MinConfidence < 0 || in.MinConfidence > 1 {
		bad[service.SettingFastpathMinConfidence] = "確信度のしきい値は 0〜1 で指定してください。"
	}
	inRange(bad, service.SettingFastpathMaxSteps, in.MaxSteps, 1, 500, "高速ステップ数の上限")
	if in.Enabled && !in.Consent {
		bad[service.SettingFastpathConsentAt] = "有効にするには、TypeSafe へのデータ送信への同意が必要です。"
	}
	if len(bad) > 0 {
		return &service.ValidationError{Fields: bad}
	}
	if in.Enabled && !a.view.TypeSafeKeyRegistered {
		return domain.NewError(domain.CodeConflict, "typesafe api key is not registered", nil)
	}
	if a.SaveErr != nil {
		return a.SaveErr
	}
	f := &a.view.Fastpath
	f.Model, f.MinConfidence, f.MaxSteps, f.ExcludedLabels = in.Model, in.MinConfidence, in.MaxSteps, in.ExcludedLabels
	switch {
	case in.Enabled && f.ConsentAt == nil:
		f.ConsentAt = a.now()
	case !in.Enabled:
		f.ConsentAt = nil
	}
	f.Enabled = in.Enabled
	return nil
}

// DeleteTypeSafeKey はキーを消し、高速パスを無効にして同意を消す。
func (a *AgentSettings) DeleteTypeSafeKey(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, "DeleteTypeSafeKey")
	a.view.TypeSafeKeyRegistered = false
	a.view.Fastpath.Enabled, a.view.Fastpath.ConsentAt = false, nil
	return nil
}
