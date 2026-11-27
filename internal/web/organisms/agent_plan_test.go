package organisms_test

import (
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// ChatGPT のカードは状態駆動: 状態に合わない操作は HTML に存在しない（hidden / disabled でも隠さない）。
func TestChatGPTCardRendersOnlyActionsOfTheState(t *testing.T) {
	tests := []struct {
		name string
		v    view.ChatGPTCard
		has  []string
		not  []string
	}{
		{"signed out", view.ChatGPTCard{State: "signed_out"},
			[]string{`data-chatgpt-state="signed_out"`, "Continue with ChatGPT"}, []string{"中止", "使用量を管理", "再開", "解除", "API キーに切り替える"}},
		{"signing in", view.ChatGPTCard{State: "signing_in"},
			[]string{`data-chatgpt-state="signing_in"`, "中止"}, []string{"Continue with ChatGPT", "使用量を管理", "解除"}},
		{"connected", view.ChatGPTCard{State: "connected", Email: "demo-user@example.com", Model: view.PlanModelSection{ID: "chatgpt-model", Current: "m1", ModelsURL: "/agent/chatgpt/models"}},
			[]string{"demo-user@example.com", "ChatGPT のプランを使用中", "使用量を管理", "解除", "m1"}, []string{"Continue with ChatGPT", "再開", "中止"}},
		{"usage limited with key", view.ChatGPTCard{State: "usage_limited", OpenAIKeyRegistered: true},
			[]string{"使用量の上限に達しました", "使用量を管理", "再開", "API キーに切り替える…"}, []string{"Continue with ChatGPT", "ChatGPT のプランを使用中"}},
		{"usage limited without key", view.ChatGPTCard{State: "usage_limited"},
			[]string{"再開"}, []string{"API キーに切り替える"}},
		{"policy denied", view.ChatGPTCard{State: "signed_out", Problem: "policy_denied", ProblemMessage: "拒否されました", OfferCodex: true},
			[]string{"拒否されました", "Codex 経由で使う", "chatgpt-codex"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := webtest.Render(t, organisms.ChatGPTCard(tt.v))
			has(t, out, `id="chatgpt-card"`, `name="rev_chatgpt"`, `data-poll-rev`)
			has(t, out, tt.has...)
			hasNot(t, out, tt.not...)
			hasNot(t, out, `hx-swap-oob`)
		})
	}
	// OOB 版は同じ要素を hx-swap-oob で差し替える。
	has(t, webtest.Render(t, organisms.ChatGPTCardOOB(view.ChatGPTCard{State: "signed_out"})), `id="chatgpt-card"`, `hx-swap-oob="true"`)
}

func TestCodexCardRendersOnlyActionsOfTheEngineAndState(t *testing.T) {
	tests := []struct {
		name string
		v    view.CodexCard
		has  []string
		not  []string
	}{
		{"unsupported", view.CodexCard{Engine: "unsupported", State: "signed_out"},
			[]string{`data-codex="unsupported"`}, []string{"エンジンを取得する", "Codex でログイン", "ログアウト"}},
		{"missing", view.CodexCard{Engine: "missing", State: "signed_out", EngineVersion: "rust-v0.164.0"},
			[]string{"エンジンを取得する", "rust-v0.164.0"}, []string{"Codex でログイン", `data-codex-state-badge`}},
		{"installing", view.CodexCard{Engine: "installing", State: "signed_out", Progress: 30},
			[]string{`<progress`, `value="30"`, "30%", "中止"}, []string{"エンジンを取得する", "Codex でログイン"}},
		{"failed", view.CodexCard{Engine: "failed", State: "signed_out", EngineError: "検証に失敗"},
			[]string{"検証に失敗", "もう一度取得する"}, []string{"Codex でログイン"}},
		{"installed signed out", view.CodexCard{Engine: "installed", State: "signed_out", EngineVersion: "v"},
			[]string{"Codex でログイン", "エンジンを削除"}, []string{"ログアウト", "再開"}},
		{"connected", view.CodexCard{Engine: "installed", State: "connected", Email: "demo-user@example.com", PlanType: "plus", Model: view.PlanModelSection{ID: "codex-model", Current: "m1", ModelsURL: "/agent/codex/models"}},
			[]string{"demo-user@example.com", "plus", "ログアウト", "m1"}, []string{"Codex でログイン", "再開"}},
		{"usage limited", view.CodexCard{Engine: "installed", State: "usage_limited", OpenAIKeyRegistered: true},
			[]string{"再開", "使用量を管理", "API キーに切り替える…"}, []string{"Codex でログイン"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := webtest.Render(t, organisms.CodexCard(tt.v))
			has(t, out, `id="codex-card"`, `name="rev_codex"`, `data-poll-rev`, `data-codex="terms"`)
			has(t, out, tt.has...)
			hasNot(t, out, tt.not...)
		})
	}
}

func TestConnectionPanelMarksTheCurrentOption(t *testing.T) {
	v := view.ConnectionPanel{Current: "chatgpt", Options: []view.ConnectionOption{
		{Value: "chatgpt", Label: "A", Current: true},
		{Value: "chatgpt-codex", Label: "B"},
		{Value: "openai_api_key", Label: "C", ConfirmURL: "/agent/connection/api-key/confirm?reason=manual"},
	}}
	out := webtest.Render(t, organisms.ConnectionPanel(v))
	has(t, out, `data-connection="chatgpt"`, "使用中の接続方式", `hx-get="/agent/connection/api-key/confirm?reason=manual"`, `hx-put="/agent/connection"`)
	// 現在の方式を選ぶボタンは無い（1 つだけ。残りの 2 つに「この方式を使う」）。
	if got := strings.Count(out, "この方式を使う"); got != 2 {
		t.Errorf("choose buttons = %d, want 2", got)
	}
}
