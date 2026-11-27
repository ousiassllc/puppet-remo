package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestAgentConnectionValidAndPlan(t *testing.T) {
	tests := []struct {
		conn  domain.AgentConnection
		valid bool
		plan  bool
	}{
		{domain.ConnectionChatGPT, true, true},
		{domain.ConnectionChatGPTCodex, true, true},
		{domain.ConnectionOpenAIAPIKey, true, false},
		{"", false, false},
		{"chatgpt-api", false, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.conn), func(t *testing.T) {
			if got := tt.conn.Valid(); got != tt.valid {
				t.Errorf("Valid() = %v, want %v", got, tt.valid)
			}
			if got := tt.conn.Plan(); got != tt.plan {
				t.Errorf("Plan() = %v, want %v", got, tt.plan)
			}
		})
	}
	if domain.DefaultAgentConnection != domain.ConnectionChatGPT {
		t.Errorf("DefaultAgentConnection = %q, want chatgpt", domain.DefaultAgentConnection)
	}
}

// プランの経路の失敗は、接続方式ごとのコードに写る（FR-G29・G30・#247）。包んでも変わらず、Codex と公式は混ざらない。
func TestTaskErrorCodePlanRoutes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"chatgpt_usage_limit", domain.ErrChatGPTUsageLimit, domain.TaskErrorChatGPTUsageLimit},
		{"chatgpt_unauthorized", domain.ErrChatGPTUnauthorized, domain.TaskErrorChatGPTUnauthorized},
		{"chatgpt_error", domain.ErrChatGPT, domain.TaskErrorChatGPT},
		{"codex_usage_limit", domain.ErrCodexUsageLimit, domain.TaskErrorCodexUsageLimit},
		{"codex_unauthorized", domain.ErrCodexUnauthorized, domain.TaskErrorCodexUnauthorized},
		{"codex_engine_unavailable", domain.ErrCodexEngine, domain.TaskErrorCodexEngine},
		{"codex_error", domain.ErrCodex, domain.TaskErrorCodex},
		{"wrapped", fmt.Errorf("%w: %w", domain.ErrCodexUsageLimit, errors.New("429")), "codex_usage_limit"},
		{"unsupported_action が優先", fmt.Errorf("%w: %w", domain.ErrChatGPT, domain.ErrUnsupportedAction), "unsupported_action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.TaskErrorCode(tt.err); got != tt.want {
				t.Errorf("TaskErrorCode(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// agent_disabled の detail.reason は WithDetail で付き、errors.Is の判定は保たれる。
func TestAgentDisabledWithReason(t *testing.T) {
	err := domain.ErrAgentDisabled.WithDetail("reason", domain.AgentDisabledChatGPTNotSignedIn)
	if !errors.Is(err, domain.ErrAgentDisabled) {
		t.Fatal("errors.Is must still match agent_disabled")
	}
	if got := err.Detail["reason"]; got != domain.AgentDisabledChatGPTNotSignedIn {
		t.Errorf("detail.reason = %v", got)
	}
	if _, ok := domain.ErrAgentDisabled.Detail["reason"]; ok {
		t.Error("the shared sentinel must not be modified")
	}
}
