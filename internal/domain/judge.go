package domain

import "errors"

// Verdict は Judge の判定。厳しさの順に Allow < RequireApproval < Deny（FR-G12）。
// ゼロ値は VerdictAllow。判定の失敗を許可にしてはならないので、呼び出し側は失敗時に VerdictRequireApproval を使う（fail-closed）。
type Verdict int

const (
	VerdictAllow Verdict = iota
	VerdictRequireApproval
	VerdictDeny
)

func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictRequireApproval:
		return "require_approval"
	case VerdictDeny:
		return "deny"
	}
	return "unknown"
}

// Stricter は a と b の厳しい方を返す。複数の判定は厳しい方を採る（FR-G12）。
func Stricter(a, b Verdict) Verdict {
	return max(a, b)
}

// Action はエージェントが 1 件ずつ検査を受ける対象（FR-G07）。OpenAI のアクションや高速パスの選択を Op へ写像したもの。
type Action struct {
	Op     Op
	Source string // "mcp" | "web" | "agent"
}

// 内部エラー。API 仕様 §2 のエラーコード表には無い（judge_denied・unsupported_action はタスクの error_code の文字列だけ）ので、
// *Error ではなく単純なエラーで、errors.Is で判定する。TaskErrorCode がタスクの error_code へ写像する。
var (
	// ErrJudgeDenied は Judge が deny を返した（FR-G07・G12）。
	ErrJudgeDenied = errors.New("the judge denied the action")
	// ErrUnsupportedAction は対応しないアクション（FR-G05）。
	ErrUnsupportedAction = errors.New("unsupported action")
	// ErrSafetyCheckDenied は OpenAI の pending_safety_checks を所有者が拒否した、または 60 秒で応答が無かった（FR-G08）。
	ErrSafetyCheckDenied = errors.New("the safety check was not acknowledged")
	// ErrInvalidChoice は高速パスの選択が候補表に無い（CandidateTable.Validate）。引き継ぎ理由 invalid_choice に写像する。
	ErrInvalidChoice = errors.New("invalid choice")
)
