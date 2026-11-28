package domain

import "errors"

// AgentConnection は内蔵エージェントの接続方式（FR-G25・S20。設定 agent.connection と agent_tasks.connection）。
// タスクの開始時に決まり、終了まで変わらない。アプリが一方から他方へ自動で切り替えることはない（NFR-S22）。
type AgentConnection string

const (
	// ConnectionChatGPT は公式の Sign in with ChatGPT（OAuth のアクセストークンで api.openai.com の Responses API）。既定。
	ConnectionChatGPT AgentConnection = "chatgpt"
	// ConnectionChatGPTCodex は Codex の app-server 経由の ChatGPT のプラン（公式の経路が拒否するワークスペースのアカウント向け。#247）。
	ConnectionChatGPTCodex AgentConnection = "chatgpt-codex"
	// ConnectionOpenAIAPIKey は OpenAI API キー（ネイティブの computer ツール）。
	ConnectionOpenAIAPIKey AgentConnection = "openai_api_key"
)

// DefaultAgentConnection は agent.connection が未設定のときの値（ただし起動時の移行は FR-G25）。
const DefaultAgentConnection = ConnectionChatGPT

// Valid は定義済みの接続方式か。
func (c AgentConnection) Valid() bool {
	switch c {
	case ConnectionChatGPT, ConnectionChatGPTCodex, ConnectionOpenAIAPIKey:
		return true
	}
	return false
}

// Plan は ChatGPT のプランを使う接続方式（公式・Codex）か。この 2 つは関数ツール（FR-G27）でループし、高速パスを使わない。
func (c AgentConnection) Plan() bool {
	return c == ConnectionChatGPT || c == ConnectionChatGPTCodex
}

// agent_disabled の detail.reason（FR-G01。API 仕様 §2）。接続方式が使えない状態の理由。
const (
	AgentDisabledOpenAIKeyMissing      = "openai_key_missing"
	AgentDisabledChatGPTNotSignedIn    = "chatgpt_not_signed_in"
	AgentDisabledChatGPTSigningIn      = "chatgpt_signing_in"
	AgentDisabledChatGPTReauthRequired = "chatgpt_reauth_required"
	AgentDisabledChatGPTUsageLimited   = "chatgpt_usage_limited"
	AgentDisabledCodexUnsupported      = "codex_unsupported"
	AgentDisabledCodexEngineMissing    = "codex_engine_missing"
	AgentDisabledCodexNotSignedIn      = "codex_not_signed_in"
	AgentDisabledCodexSigningIn        = "codex_signing_in"
	AgentDisabledCodexReauthRequired   = "codex_reauth_required"
	AgentDisabledCodexUsageLimited     = "codex_usage_limited"
)

// ChatGPT のプランの経路のタスクの失敗（agent_tasks.error_code。FR-G29・G30。Codex は #247）。
// エージェントが接続方式ごとに包む（モデルの失敗の分類は openai の ErrUsageLimit などで、ここはタスクの理由）。
var (
	ErrChatGPTUsageLimit   = errors.New("chatgpt plan usage limit reached")
	ErrChatGPTUnauthorized = errors.New("chatgpt authorization is no longer valid")
	ErrChatGPT             = errors.New("chatgpt request failed")
	ErrCodexUsageLimit     = errors.New("codex usage limit reached")
	ErrCodexUnauthorized   = errors.New("codex sign-in is no longer valid")
	ErrCodexEngine         = errors.New("codex engine is unavailable")
	ErrCodex               = errors.New("codex request failed")
)
