package domain

import "time"

// TaskStatus はエージェントのタスクの状態（FR-G03。ER の agent_tasks.status と 1 対 1）。
type TaskStatus string

const (
	TaskPendingApproval TaskStatus = "pending_approval" // リース承認待ち
	TaskRunning         TaskStatus = "running"
	TaskCompleted       TaskStatus = "completed"
	TaskFailed          TaskStatus = "failed"
	TaskCancelled       TaskStatus = "cancelled"
	TaskLimitReached    TaskStatus = "limit_reached"
	TaskAborted         TaskStatus = "aborted" // アプリの再起動・異常終了で中断
	TaskDenied          TaskStatus = "denied"  // リース承認の拒否
)

// Valid は定義済みの状態か。
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskPendingApproval, TaskRunning, TaskCompleted, TaskFailed, TaskCancelled, TaskLimitReached, TaskAborted, TaskDenied:
		return true
	}
	return false
}

// Terminal は終了状態か。終了状態からは遷移しない（FR-G03）。
func (s TaskStatus) Terminal() bool { return s.Valid() && s != TaskPendingApproval && s != TaskRunning }

// HandoffReason は高速パスから OpenAI へ引き継いだ理由（FR-G20。ER の agent_tasks.handoff_reason と 1 対 1）。
type HandoffReason string

const (
	HandoffAbstain            HandoffReason = "abstain"
	HandoffLowConfidence      HandoffReason = "low_confidence"
	HandoffNoCandidates       HandoffReason = "no_candidates"
	HandoffInvalidChoice      HandoffReason = "invalid_choice"
	HandoffNoProgress         HandoffReason = "no_progress"
	HandoffGoalMaybeDone      HandoffReason = "goal_maybe_done"
	HandoffFastStepLimit      HandoffReason = "fast_step_limit"
	HandoffChooserUnavailable HandoffReason = "chooser_unavailable"
)

// Valid は 8 種のどれかか。空（引き継ぎなし = NULL）は有効としない。
func (r HandoffReason) Valid() bool {
	switch r {
	case HandoffAbstain, HandoffLowConfidence, HandoffNoCandidates, HandoffInvalidChoice,
		HandoffNoProgress, HandoffGoalMaybeDone, HandoffFastStepLimit, HandoffChooserUnavailable:
		return true
	}
	return false
}

// Task は agent_tasks の 1 行（FR-G03・G15・G23）。純粋な値で、永続化は repository.Tasks、実行は service.Agent が担う。
type Task struct {
	ID          string // ULID。run_task が返す task_id
	ClientID    string // 依頼したクライアント。クライアントが削除されていれば空（NULL）
	ClientLabel string // 依頼元の表示名のスナップショット
	Prompt      string // 指示本文（10,000 文字まで）
	Status      TaskStatus
	Model       string // 開始時の agent.model
	MaxSteps    int    // 適用した上限
	// MaxDurationSec は適用した上限（秒）。
	MaxDurationSec int
	StepCount      int // 総数。常に FastSteps + OpenAISteps（ER の CHECK）
	FastSteps      int
	OpenAISteps    int
	HandoffReason  HandoffReason // 引き継ぎなしは空（NULL）
	InputTokens    int64
	OutputTokens   int64
	ResultText     string // 最終回答（10,000 文字で切り詰め）。無ければ空
	ErrorCode      string // 失敗の理由（input_blocked / judge_denied / approval_denied / safety_check_denied / unsupported_action / openai_error など）。無ければ空
	CreatedAt      time.Time
	StartedAt      *time.Time // running になった時刻。denied は nil
	FinishedAt     *time.Time // 終了状態になった時刻
}
