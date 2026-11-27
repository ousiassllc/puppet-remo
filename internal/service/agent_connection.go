package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは AgentSettingsService の接続方式まわり（FR-G25・G30・S20・S21・#247）: 起動時の移行、切り替え、プランのモデルの保存、
// 接続方式ごとの「有効にできるか」の判定。接続方式を変えるのはこのファイルの SetConnection だけ（自動で呼ぶコードを作らない。NFR-S22）。

// 監査のイベント名（kind = agent）。detail は from・to・reason だけ（トークン・メールは入れない）。
const agentConnectionAuditEvent = "agent.connection"

// SetConnection の reason（監査用）。
const (
	connectionReasonManual     = "manual"
	connectionReasonUsageLimit = "usage_limit"
	connectionReasonMigration  = "migration" // MigrateConnection（起動時の移行。actor system）
)

var _ AgentConnectionSettings = (*AgentSettingsService)(nil)

// MigrateConnection は agent.connection が未設定で OpenAI キーが登録済みなら openai_api_key を保存する（FR-G25）。
// 保存したあとは設定済みなので、二度目以降は何もしない。キーが無ければ保存せず、既定（chatgpt）のまま。
func (s *AgentSettingsService) MigrateConnection(ctx context.Context) error {
	unlock, err := s.mu.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	_, found, err := s.cfg.Settings.Get(ctx, SettingAgentConnection)
	if err != nil {
		return fmt.Errorf("read setting %s: %w", SettingAgentConnection, err)
	}
	if found {
		return nil
	}
	registered, err := s.OpenAIKeyRegistered(ctx)
	if err != nil {
		return err
	}
	if !registered {
		return nil
	}
	if err := s.setValue(ctx, SettingAgentConnection, string(domain.ConnectionOpenAIAPIKey)); err != nil {
		return err
	}
	e := s.agentEntry(agentConnectionAuditEvent, auditOK, "", map[string]any{
		"from": string(domain.DefaultAgentConnection), "to": string(domain.ConnectionOpenAIAPIKey), "reason": connectionReasonMigration,
	})
	setAuditActor(&e, domain.Actor{Kind: domain.ActorSystem, Label: systemLabel})
	s.record(ctx, e)
	s.log.Info("agent connection migrated", "to", string(domain.ConnectionOpenAIAPIKey))
	return nil
}

// SetConnection は agent.connection を保存し、監査 agent.connection（from・to・reason。actor host）を残す。
// 定義外の接続方式・reason は domain.ErrInvalidArgument。openai_api_key にするときは confirmed が真（確認モーダルの確定）でなければ
// ErrInvalidArgument、OpenAI キー未登録なら CodeConflict（NFR-S22）。chatgpt と chatgpt-codex の間の切り替えは確認なし。
// すでにその値なら何も変えず、監査も残さない。エージェントの有効・同意・トークン・実行中のタスクは変えない。
func (s *AgentSettingsService) SetConnection(ctx context.Context, conn domain.AgentConnection, confirmed bool, reason string) error {
	if reason == "" {
		reason = connectionReasonManual
	}
	switch {
	case !conn.Valid():
		return domain.ErrInvalidArgument.WithMsg("unknown connection")
	case reason != connectionReasonManual && reason != connectionReasonUsageLimit:
		return domain.ErrInvalidArgument.WithMsg("unknown reason")
	case conn == domain.ConnectionOpenAIAPIKey && !confirmed:
		return domain.ErrInvalidArgument.WithMsg("switching to the OpenAI API key needs confirmation")
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
	if conn == domain.ConnectionOpenAIAPIKey {
		registered, err := s.OpenAIKeyRegistered(ctx)
		if err != nil {
			return err
		}
		if !registered {
			return domain.NewError(domain.CodeConflict, "register the OpenAI API key before switching to it", nil)
		}
	}
	if cur.Connection == conn {
		return nil
	}
	if err := s.setValue(ctx, SettingAgentConnection, string(conn)); err != nil {
		return err
	}
	s.recordAgent(ctx, agentConnectionAuditEvent, auditOK, "", map[string]any{
		"from": string(cur.Connection), "to": string(conn), "reason": reason,
	})
	s.log.Info("agent connection changed", "from", string(cur.Connection), "to", string(conn), "reason", reason)
	return nil
}

// SavePlanModel は agent.chatgpt.model（chatgpt）か agent.codex.model（chatgpt-codex）を保存する。名前の形の不備は *ValidationError
// （項目 model）、プランでない接続方式は ErrInvalidArgument。
func (s *AgentSettingsService) SavePlanModel(ctx context.Context, conn domain.AgentConnection, model string) error {
	var key string
	switch conn {
	case domain.ConnectionChatGPT:
		key = SettingChatGPTModel
	case domain.ConnectionChatGPTCodex:
		key = SettingCodexModel
	default:
		return domain.ErrInvalidArgument.WithMsg("the connection has no plan model")
	}
	model = strings.TrimSpace(model)
	if !validModelName(model) {
		return &ValidationError{Fields: map[string]string{FieldAgentModel: fmt.Sprintf("モデル名を 1〜%d 文字で指定してください。", maxModelNameRunes)}}
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
	old := cur.ChatGPTModel
	if conn == domain.ConnectionChatGPTCodex {
		old = cur.CodexModel
	}
	if err := s.setValue(ctx, key, model); err != nil {
		return err
	}
	if old != model {
		s.recordSettingsUpdate(ctx, map[string]any{key: model})
	}
	return nil
}

// connectionNotReady は接続方式 conn でエージェントを有効にできない理由（agent_disabled の detail.reason）。空なら有効にできる。
// openaiKeyRegistered は OpenAI キーの登録の有無（openai_api_key のときだけ見る）。窓口が無いプランの接続方式は、理由を持たない
// 「使えない」状態として not_available を返す。
func (s *AgentSettingsService) connectionNotReady(conn domain.AgentConnection, openaiKeyRegistered bool) string {
	if !conn.Plan() {
		if openaiKeyRegistered {
			return ""
		}
		return domain.AgentDisabledOpenAIKeyMissing
	}
	gate := s.planGate(conn)
	if gate == nil {
		return connectionNotAvailable
	}
	if reason, ok := gate.Ready(); !ok {
		return reason
	}
	return ""
}

// connectionNotAvailable は接続方式の窓口が組み込まれていないときの理由（GUI の表示用。API の detail.reason には出さない）。
const connectionNotAvailable = "not_available"

func (s *AgentSettingsService) planGate(conn domain.AgentConnection) PlanGate {
	switch conn {
	case domain.ConnectionChatGPT:
		return s.cfg.ChatGPT
	case domain.ConnectionChatGPTCodex:
		return s.cfg.Codex
	}
	return nil
}

// checkConnectionReady は SaveAgent の有効化の条件: 選んだ接続方式が使える状態でなければ ErrAgentDisabled（detail.reason に理由）。
func (s *AgentSettingsService) checkConnectionReady(ctx context.Context, conn domain.AgentConnection) error {
	registered := false
	if !conn.Plan() {
		var err error
		if registered, err = s.OpenAIKeyRegistered(ctx); err != nil {
			return err
		}
	}
	switch reason := s.connectionNotReady(conn, registered); reason {
	case "":
		return nil
	case domain.AgentDisabledOpenAIKeyMissing:
		return domain.ErrAgentDisabled.WithMsg("register the OpenAI API key before enabling the agent").WithDetail("reason", reason)
	case connectionNotAvailable:
		return domain.ErrAgentDisabled.WithMsg("the connection is not available in this build")
	default:
		return domain.ErrAgentDisabled.WithMsg("the selected connection is not ready").WithDetail("reason", reason)
	}
}
