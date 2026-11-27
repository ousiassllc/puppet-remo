package openai

import "errors"

// ChatGPT のプランを使う Model（公式の ChatGPT 経路 NewChatGPT と、Codex の app-server 経路）が返す失敗の分類（FR-G29・G30・#247）。
// errors.Is で判定する。どれにも当たらない失敗は一時的・その他の失敗（タスクは chatgpt_error / codex_error）。
// エージェントは接続方式ごとにタスクの error_code へ写し、接続の状態（usage_limited・reauth_required）を更新する。
// どの分類でも、別の接続方式へ自動で切り替えない（NFR-S22）。
var (
	// ErrUsageLimit はプランの使用量の上限（subscription_sharing_usage_limit_exceeded・Codex の usage_limit）。
	ErrUsageLimit = errors.New("openai: plan usage limit exceeded")
	// ErrPlanUnauthorized は認可が使えない（401・subscription_sharing_invalid_user・Codex の未ログイン / 認可切れ）。
	ErrPlanUnauthorized = errors.New("openai: plan authorization is not valid")
	// ErrPolicyDenied はワークスペースのポリシーで公式の経路が拒否された（3p_delegated_access_policy_denied・
	// subscription_sharing_user_not_eligible）。GUI は Codex の経路へ案内する（#247）。
	ErrPolicyDenied = errors.New("openai: the workspace does not allow this sign-in")
	// ErrEngineUnavailable は Codex のエンジン（app-server）が無い・起動できない・版が合わない。
	ErrEngineUnavailable = errors.New("openai: the codex engine is not available")
)
