package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは AgentSettingsService の接続方式まわり（agent_connection.go。FR-G25・G30・S20・S21・#247）の試験。

// connectionEnv は接続方式の試験用の環境。newAgentSettingsEnv は既存の試験の前提で agent.connection = openai_api_key を入れるので、外す。
func connectionEnv(t *testing.T) *agentSettingsEnv {
	t.Helper()
	e := newAgentSettingsEnv(t)
	delete(e.set.values, SettingAgentConnection)
	return e
}

func (e *agentSettingsEnv) connection(t *testing.T) AgentConfig {
	t.Helper()
	c, err := e.s.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAgentConfigConnectionAndPlanModels(t *testing.T) {
	e := connectionEnv(t)
	c := e.connection(t)
	if c.Connection != domain.ConnectionChatGPT || c.ChatGPTModel != DefaultChatGPTModel || c.CodexModel != DefaultCodexModel {
		t.Fatalf("defaults = %q %q %q", c.Connection, c.ChatGPTModel, c.CodexModel)
	}
	e.set.put(SettingAgentConnection, `"chatgpt-codex"`)
	e.set.put(SettingChatGPTModel, `"m-chatgpt"`)
	e.set.put(SettingCodexModel, `"m-codex"`)
	if c := e.connection(t); c.Connection != domain.ConnectionChatGPTCodex || c.ChatGPTModel != "m-chatgpt" || c.CodexModel != "m-codex" {
		t.Errorf("config = %q %q %q", c.Connection, c.ChatGPTModel, c.CodexModel)
	}
	// 不正な値は既定値にして警告する。
	e.set.put(SettingAgentConnection, `"carrier-pigeon"`)
	e.set.put(SettingChatGPTModel, `""`)
	if c := e.connection(t); c.Connection != domain.ConnectionChatGPT || c.ChatGPTModel != DefaultChatGPTModel {
		t.Errorf("invalid values = %q %q, want the defaults", c.Connection, c.ChatGPTModel)
	}
	if !strings.Contains(e.logs.String(), "invalid agent setting") {
		t.Error("an invalid value must warn")
	}
}

func TestMigrateConnection(t *testing.T) {
	ctx := context.Background()
	t.Run("key registered and unset saves openai_api_key once", func(t *testing.T) {
		e := connectionEnv(t)
		if err := e.s.SetOpenAIKey(ctx, canaryKey); err != nil {
			t.Fatal(err)
		}
		if err := e.s.MigrateConnection(ctx); err != nil {
			t.Fatal(err)
		}
		if c := e.connection(t); c.Connection != domain.ConnectionOpenAIAPIKey {
			t.Fatalf("connection = %q", c.Connection)
		}
		if raw, found, _ := e.set.Get(ctx, SettingAgentConnection); !found || string(raw) != `"openai_api_key"` {
			t.Errorf("stored = %s (%v)", raw, found)
		}
		ev := e.entries(agentConnectionAuditEvent)
		if len(ev) != 1 || ev[0].ActorType != domain.ActorSystem {
			t.Fatalf("audit = %+v", ev)
		}
		var d map[string]any
		_ = json.Unmarshal(ev[0].DetailJSON, &d)
		if d["from"] != "chatgpt" || d["to"] != "openai_api_key" || d["reason"] != "migration" {
			t.Errorf("detail = %v", d)
		}
		// 二度目は何もしない（ユーザーが chatgpt に戻したあとも上書きしない）。
		if err := e.s.SetConnection(ctx, domain.ConnectionChatGPT, false, ""); err != nil {
			t.Fatal(err)
		}
		if err := e.s.MigrateConnection(ctx); err != nil {
			t.Fatal(err)
		}
		if c := e.connection(t); c.Connection != domain.ConnectionChatGPT || len(e.entries(agentConnectionAuditEvent)) != 2 {
			t.Errorf("connection = %q, audit = %d", c.Connection, len(e.entries(agentConnectionAuditEvent)))
		}
	})
	t.Run("no key leaves it unset", func(t *testing.T) {
		e := connectionEnv(t)
		if err := e.s.MigrateConnection(ctx); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := e.set.Get(ctx, SettingAgentConnection); found || len(e.audit.All()) != 0 {
			t.Errorf("saved = %v, audit = %v", found, e.audit.All())
		}
		if c := e.connection(t); c.Connection != domain.ConnectionChatGPT {
			t.Errorf("connection = %q, want the default chatgpt", c.Connection)
		}
	})
	t.Run("already set is kept", func(t *testing.T) {
		e := connectionEnv(t)
		e.set.put(SettingAgentConnection, `"chatgpt-codex"`)
		if err := e.s.SetOpenAIKey(ctx, canaryKey); err != nil {
			t.Fatal(err)
		}
		if err := e.s.MigrateConnection(ctx); err != nil {
			t.Fatal(err)
		}
		if c := e.connection(t); c.Connection != domain.ConnectionChatGPTCodex || len(e.entries(agentConnectionAuditEvent)) != 0 {
			t.Errorf("connection = %q", c.Connection)
		}
	})
}

func TestSetConnection(t *testing.T) {
	ctx := context.Background()
	e := connectionEnv(t)

	// chatgpt ↔ chatgpt-codex は確認なし。監査 agent.connection（from・to・reason。actor host）。
	if err := e.s.SetConnection(ctx, domain.ConnectionChatGPTCodex, false, ""); err != nil {
		t.Fatal(err)
	}
	ev := e.entries(agentConnectionAuditEvent)
	if len(ev) != 1 || ev[0].ActorType != domain.ActorHost || ev[0].Kind != "agent" {
		t.Fatalf("audit = %+v", ev)
	}
	var d map[string]any
	_ = json.Unmarshal(ev[0].DetailJSON, &d)
	if d["from"] != "chatgpt" || d["to"] != "chatgpt-codex" || d["reason"] != "manual" || len(d) != 3 {
		t.Errorf("detail = %v", d)
	}
	// 同じ値なら何も変えず、監査も残さない。
	if err := e.s.SetConnection(ctx, domain.ConnectionChatGPTCodex, false, ""); err != nil || len(e.entries(agentConnectionAuditEvent)) != 1 {
		t.Errorf("same value: err %v, audit %d", err, len(e.entries(agentConnectionAuditEvent)))
	}

	// openai_api_key は確認（confirmed）が必須。無ければ変わらない。
	if err := e.s.SetConnection(ctx, domain.ConnectionOpenAIAPIKey, false, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unconfirmed = %v, want invalid_argument", err)
	}
	// 確認ありでも OpenAI キー未登録は conflict。
	if err := e.s.SetConnection(ctx, domain.ConnectionOpenAIAPIKey, true, ""); domain.CodeOf(err) != domain.CodeConflict {
		t.Fatalf("no key = %v, want conflict", err)
	}
	if c := e.connection(t); c.Connection != domain.ConnectionChatGPTCodex {
		t.Fatalf("connection changed to %q", c.Connection)
	}
	if err := e.s.SetOpenAIKey(ctx, canaryKey); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetConnection(ctx, domain.ConnectionOpenAIAPIKey, true, "usage_limit"); err != nil {
		t.Fatal(err)
	}
	ev = e.entries(agentConnectionAuditEvent)
	if len(ev) != 2 {
		t.Fatalf("audit = %+v", ev)
	}
	d = nil
	_ = json.Unmarshal(ev[1].DetailJSON, &d)
	if d["from"] != "chatgpt-codex" || d["to"] != "openai_api_key" || d["reason"] != "usage_limit" {
		t.Errorf("detail = %v", d)
	}
	// 戻すのは確認なし。
	if err := e.s.SetConnection(ctx, domain.ConnectionChatGPT, false, "manual"); err != nil {
		t.Fatal(err)
	}
	// 定義外。
	if err := e.s.SetConnection(ctx, "carrier-pigeon", true, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("unknown connection = %v", err)
	}
	if err := e.s.SetConnection(ctx, domain.ConnectionChatGPT, true, "because"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("unknown reason = %v", err)
	}
	// エージェントの有効・同意・実行中のタスクは変えない。
	if len(e.cancels()) != 0 {
		t.Errorf("cancels = %v", e.cancels())
	}
	for _, en := range e.audit.All() {
		if strings.Contains(string(en.DetailJSON), "CANARY") {
			t.Errorf("the key leaked into the audit: %s", en.DetailJSON)
		}
	}
}

func TestSetConnectionKeepsEnabledAndConsent(t *testing.T) {
	ctx := context.Background()
	e := connectionEnv(t)
	e.s.cfg.ChatGPT = &fakePlanGate{}
	in := validAgentInput()
	in.Enabled, in.Consent = true, true
	if err := e.s.SaveAgent(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetConnection(ctx, domain.ConnectionChatGPTCodex, false, ""); err != nil {
		t.Fatal(err)
	}
	if c := e.connection(t); !c.Enabled || c.ConsentAt == nil || len(e.cancels()) != 0 {
		t.Errorf("config = %+v, cancels = %v", c, e.cancels())
	}
}

func TestSavePlanModel(t *testing.T) {
	ctx := context.Background()
	e := connectionEnv(t)
	if err := e.s.SavePlanModel(ctx, domain.ConnectionChatGPT, " gpt-x "); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SavePlanModel(ctx, domain.ConnectionChatGPTCodex, "codex-y"); err != nil {
		t.Fatal(err)
	}
	if c := e.connection(t); c.ChatGPTModel != "gpt-x" || c.CodexModel != "codex-y" {
		t.Errorf("config = %q %q", c.ChatGPTModel, c.CodexModel)
	}
	for _, bad := range []string{"", "   ", "a\nb", strings.Repeat("x", maxModelNameRunes+1)} {
		if err := e.s.SavePlanModel(ctx, domain.ConnectionChatGPT, bad); fieldsOf(t, err)[FieldAgentModel] == "" {
			t.Errorf("model %q accepted", bad)
		}
	}
	if err := e.s.SavePlanModel(ctx, domain.ConnectionOpenAIAPIKey, "gpt-x"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("api key connection = %v", err)
	}
	if c := e.connection(t); c.ChatGPTModel != "gpt-x" {
		t.Errorf("a rejected save changed the model: %q", c.ChatGPTModel)
	}
}

// SaveAgent の有効化の条件は接続方式ごと: openai_api_key は OpenAI キー、プランは PlanGate.Ready。満たさなければ agent_disabled（detail.reason）。
func TestSaveAgentEnableDependsOnTheConnection(t *testing.T) {
	ctx := context.Background()
	enable := func() AgentSettingsInput {
		in := validAgentInput()
		in.Enabled, in.Consent = true, true
		return in
	}
	reasonOf := func(t *testing.T, err error) string {
		t.Helper()
		var de *domain.Error
		if !errors.Is(err, domain.ErrAgentDisabled) || !errors.As(err, &de) {
			t.Fatalf("err = %v, want agent_disabled", err)
		}
		r, _ := de.Detail["reason"].(string)
		return r
	}

	t.Run("openai_api_key needs the key", func(t *testing.T) {
		e := connectionEnv(t)
		e.set.put(SettingAgentConnection, `"openai_api_key"`)
		if r := reasonOf(t, e.s.SaveAgent(ctx, enable())); r != domain.AgentDisabledOpenAIKeyMissing {
			t.Errorf("reason = %q", r)
		}
		if err := e.s.SetOpenAIKey(ctx, canaryKey); err != nil {
			t.Fatal(err)
		}
		if err := e.s.SaveAgent(ctx, enable()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("chatgpt needs a ready gate, not the key", func(t *testing.T) {
		e := connectionEnv(t)
		if r := reasonOf(t, e.s.SaveAgent(ctx, enable())); r != "" { // 窓口が組み込まれていない
			t.Errorf("reason = %q, want none (not available)", r)
		}
		gate := &fakePlanGate{reason: domain.AgentDisabledChatGPTNotSignedIn}
		e.s.cfg.ChatGPT = gate
		if r := reasonOf(t, e.s.SaveAgent(ctx, enable())); r != domain.AgentDisabledChatGPTNotSignedIn {
			t.Errorf("reason = %q", r)
		}
		if v, _ := e.s.Load(ctx); v.ConnectionNotReady != domain.AgentDisabledChatGPTNotSignedIn {
			t.Errorf("view.ConnectionNotReady = %q", v.ConnectionNotReady)
		}
		if c := e.connection(t); c.Enabled || c.ConsentAt != nil || len(e.audit.All()) != 0 {
			t.Errorf("a rejected enable saved something: %+v", c)
		}
		gate.reason = ""
		if err := e.s.SaveAgent(ctx, enable()); err != nil { // OpenAI キーは未登録のまま
			t.Fatal(err)
		}
		if c := e.connection(t); !c.Enabled || c.ConsentAt == nil {
			t.Errorf("config = %+v", c)
		}
		if v, _ := e.s.Load(ctx); v.ConnectionNotReady != "" {
			t.Errorf("view.ConnectionNotReady = %q", v.ConnectionNotReady)
		}
	})
	t.Run("codex uses its own gate", func(t *testing.T) {
		e := connectionEnv(t)
		e.set.put(SettingAgentConnection, `"chatgpt-codex"`)
		e.s.cfg.ChatGPT = &fakePlanGate{} // 公式が Ready でも Codex の窓口を見る
		e.s.cfg.Codex = &fakePlanGate{reason: domain.AgentDisabledCodexEngineMissing}
		if r := reasonOf(t, e.s.SaveAgent(ctx, enable())); r != domain.AgentDisabledCodexEngineMissing {
			t.Errorf("reason = %q", r)
		}
	})
}

// OpenAI キーの削除は、接続方式が openai_api_key のときだけエージェントを無効にして中断する（プランの経路は有効のまま・中断しない）。
func TestDeleteOpenAIKeyDependsOnTheConnection(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, conn domain.AgentConnection) *agentSettingsEnv {
		e := connectionEnv(t)
		e.s.cfg.ChatGPT, e.s.cfg.Codex = &fakePlanGate{}, &fakePlanGate{}
		if err := e.s.SetOpenAIKey(ctx, canaryKey); err != nil {
			t.Fatal(err)
		}
		e.set.put(SettingAgentConnection, `"`+string(conn)+`"`)
		in := validAgentInput()
		in.Enabled, in.Consent = true, true
		if err := e.s.SaveAgent(ctx, in); err != nil {
			t.Fatal(err)
		}
		e.audit.reset()
		return e
	}

	e := setup(t, domain.ConnectionChatGPT)
	if err := e.s.DeleteOpenAIKey(ctx); err != nil {
		t.Fatal(err)
	}
	if c := e.connection(t); !c.Enabled || c.ConsentAt == nil || len(e.cancels()) != 0 || len(e.entries(agentConsentAuditEvent)) != 0 {
		t.Errorf("plan route: config %+v, cancels %v", c, e.cancels())
	}
	if reg, _ := e.s.OpenAIKeyRegistered(ctx); reg {
		t.Error("the key was not deleted")
	}

	e = setup(t, domain.ConnectionOpenAIAPIKey)
	if err := e.s.DeleteOpenAIKey(ctx); err != nil {
		t.Fatal(err)
	}
	if c := e.connection(t); c.Enabled || c.ConsentAt != nil || len(e.cancels()) != 1 || len(e.entries(agentConsentAuditEvent)) != 1 {
		t.Errorf("api key route: config %+v, cancels %v", c, e.cancels())
	}
}

// 接続方式とプランのモデルの変更は fail-closed（#182）: 監査に書けなければ元の値に戻し、audit_unavailable（agent_settings）を返す。
func TestConnectionChangesAreFailClosed(t *testing.T) {
	ctx := context.Background()
	e := connectionEnv(t)
	e.audit.SetSyncErr(errors.New("disk full"))

	err := e.s.SetConnection(ctx, domain.ConnectionChatGPTCodex, false, "")
	if subject, restored, ok := AuditChangeFailure(err); !ok || subject != AuditSubjectAgentSettings || !restored {
		t.Fatalf("SetConnection = %v", err)
	}
	if c := e.connection(t); c.Connection != domain.ConnectionChatGPT {
		t.Errorf("connection = %q, want the change undone", c.Connection)
	}

	before := e.connection(t).CodexModel
	err = e.s.SavePlanModel(ctx, domain.ConnectionChatGPTCodex, before+"-x")
	if subject, restored, ok := AuditChangeFailure(err); !ok || subject != AuditSubjectAgentSettings || !restored {
		t.Fatalf("SavePlanModel = %v", err)
	}
	if c := e.connection(t); c.CodexModel != before {
		t.Errorf("codex model = %q, want %q", c.CodexModel, before)
	}
}
