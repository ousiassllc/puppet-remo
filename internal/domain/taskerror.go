package domain

import "errors"

// タスクの error_code（agent_tasks.error_code・get_task の error_code。ER・API 仕様 §3）。
// API のエラーコード表（Code）とは別の語彙で、judge_denied・unsupported_action・safety_check_denied・openai_error は
// HTTP のエラーにならず、タスクの失敗理由としてだけ現れる（API 仕様 §2）。
const (
	TaskErrorInputBlocked       = "input_blocked"
	TaskErrorJudgeDenied        = "judge_denied"
	TaskErrorApprovalDenied     = "approval_denied"
	TaskErrorApprovalTimeout    = "approval_timeout"
	TaskErrorKillSwitchActive   = "kill_switch_active"
	TaskErrorLeaseRequired      = "lease_required"
	TaskErrorLeaseHeldByOther   = "lease_held_by_other"
	TaskErrorLeaseRevoked       = "lease_revoked"
	TaskErrorInvalidCoordinates = "invalid_coordinates"
	TaskErrorSafetyCheckDenied  = "safety_check_denied"
	TaskErrorUnsupportedAction  = "unsupported_action"
	TaskErrorOpenAI             = "openai_error"
	TaskErrorInternal           = "internal"

	// ChatGPT のプランの経路（接続方式 chatgpt。FR-G29・G30）。
	TaskErrorChatGPTUsageLimit   = "chatgpt_usage_limit"
	TaskErrorChatGPTUnauthorized = "chatgpt_unauthorized"
	TaskErrorChatGPT             = "chatgpt_error"
	// Codex の app-server の経路（接続方式 chatgpt-codex。#247）。
	TaskErrorCodexUsageLimit   = "codex_usage_limit"
	TaskErrorCodexUnauthorized = "codex_unauthorized"
	TaskErrorCodexEngine       = "codex_engine_unavailable"
	TaskErrorCodex             = "codex_error"
)

// ErrOpenAI は OpenAI の呼び出しの失敗（FR-G04）。openai.Model の失敗は単純なエラーで返るので、
// エージェントが fmt.Errorf("%w: %w", ErrOpenAI, err) のように包んで TaskErrorCode が openai_error になるようにする。
var ErrOpenAI = errors.New("openai request failed")

// TaskErrorCode は、タスクを失敗させた err を agent_tasks.error_code の文字列へ写す。err が nil なら空文字。
// 対応を決めていない失敗（想定外のエラー、上の表に無いドメインエラー）は "internal"。
func TaskErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrJudgeDenied):
		return TaskErrorJudgeDenied
	case errors.Is(err, ErrUnsupportedAction):
		return TaskErrorUnsupportedAction
	case errors.Is(err, ErrSafetyCheckDenied):
		return TaskErrorSafetyCheckDenied
	case errors.Is(err, ErrChatGPTUsageLimit):
		return TaskErrorChatGPTUsageLimit
	case errors.Is(err, ErrChatGPTUnauthorized):
		return TaskErrorChatGPTUnauthorized
	case errors.Is(err, ErrChatGPT):
		return TaskErrorChatGPT
	case errors.Is(err, ErrCodexUsageLimit):
		return TaskErrorCodexUsageLimit
	case errors.Is(err, ErrCodexUnauthorized):
		return TaskErrorCodexUnauthorized
	case errors.Is(err, ErrCodexEngine):
		return TaskErrorCodexEngine
	case errors.Is(err, ErrCodex):
		return TaskErrorCodex
	case errors.Is(err, ErrOpenAI):
		return TaskErrorOpenAI
	}
	switch code := CodeOf(err); code {
	case CodeInputBlocked, CodeApprovalDenied, CodeApprovalTimeout, CodeKillSwitchActive,
		CodeLeaseRequired, CodeLeaseHeldByOther, CodeLeaseRevoked, CodeInvalidCoordinates:
		return string(code)
	}
	return TaskErrorInternal
}
