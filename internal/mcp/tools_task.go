package mcp

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// 内蔵エージェントのツール（M4。FR-G02。API 仕様 §3.3「内蔵エージェント」）。
//
// タスクの開始・状態・中断はすべて service.Agent の仕事で、この層は引数の検査と出力の整形だけをする。
// リースはタスクの間 Agent（保持者種別 agent）が持つので、三つのツールはリース不要で、リースを取得しうるツール
// （leaseAcquiringTools）にも入れない: Agent.Start は 1 秒以内に返り（NFR-P08）、通常の 30 秒のタイムアウトで足りる。
//
// 監査: 三つとも addAuditedTool で tool.<名前> を 1 行ずつ記録する（FR-AU01 の「MCP ツール呼び出し」）。タスクの
// ライフサイクルとステップ（agent.task_start・agent.step など）は Agent が task_id つきで記録するので、ここは
// 呼び出しそのものだけを記録し、重ならない。tool.* の行には task_id 列を付けない（付けるとタスクの詳細の「ステップ」に
// 混ざる）。task_id は detail に入れる。指示（prompt）の本文は監査にも運用ログにも出さず、文字数だけを残す（指示の本文は
// タスクの記録として Agent が agent_tasks に保存する。FR-G15・AU03）。

const (
	// maxPromptChars は run_task の prompt の上限（文字数。FR-G02）。
	maxPromptChars = 10_000
	// maxResultChars は get_task の result_text の上限（文字数。FR-G13）。Agent が切り詰めて保存するが、出力の側でも守る。
	maxResultChars = 10_000
	// maxTaskIDLen は task_id の長さの上限。ULID は 26 文字。クライアントが好きな長さの文字列を監査の detail へ書かせないための上限。
	maxTaskIDLen = 64
)

// completedNote は completed のタスクの get_task に付ける note（FR-G13）。completed はモデルの申告にすぎない。
const completedNote = "status completed is the model's own claim that the task is done, not a verified outcome. " +
	"Verifying the real result (for example with a screenshot) is the caller's responsibility."

type runTaskIn struct {
	Prompt         string `json:"prompt" jsonschema:"natural-language instruction for the agent, 1 to 10000 characters"`
	MaxSteps       *int   `json:"max_steps,omitempty" jsonschema:"step limit, an integer of 1 or more, at most the host's setting (omit to use the setting)"`
	MaxDurationSec *int   `json:"max_duration_sec,omitempty" jsonschema:"run time limit in seconds, an integer of 1 or more, at most the host's setting (omit to use the setting)"`
}

type taskIDIn struct {
	TaskID string `json:"task_id" jsonschema:"task id returned by run_task"`
}

// runTaskBody は run_task の出力。
type runTaskBody struct {
	TaskID string            `json:"task_id"`
	Status domain.TaskStatus `json:"status"`
}

// cancelTaskBody は cancel_task の出力。終了済みのタスクでは現在の状態をそのまま返す。
type cancelTaskBody struct {
	OK     bool              `json:"ok"`
	Status domain.TaskStatus `json:"status"`
}

// taskBody は get_task の出力（API 仕様 §3.3）。ない値は null（省略しない）。
type taskBody struct {
	TaskID        string            `json:"task_id"`
	Status        domain.TaskStatus `json:"status"`
	StepCount     int               `json:"step_count"`
	FastSteps     int               `json:"fast_steps"`
	OpenAISteps   int               `json:"openai_steps"`
	HandoffReason *string           `json:"handoff_reason"`
	MaxSteps      int               `json:"max_steps"`
	InputTokens   int64             `json:"input_tokens"`
	OutputTokens  int64             `json:"output_tokens"`
	CreatedAt     string            `json:"created_at"`
	StartedAt     *string           `json:"started_at"`
	FinishedAt    *string           `json:"finished_at"`
	ResultText    *string           `json:"result_text"`
	ErrorCode     *string           `json:"error_code"`
	Note          *string           `json:"note"`
}

// newTaskBody は Task を get_task の出力にする。result_text と note は completed のときだけ。
func newTaskBody(t domain.Task) taskBody {
	b := taskBody{
		TaskID: t.ID, Status: t.Status,
		StepCount: t.StepCount, FastSteps: t.FastSteps, OpenAISteps: t.OpenAISteps,
		MaxSteps:    t.MaxSteps,
		InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
		CreatedAt:  rfc3339UTC(t.CreatedAt),
		StartedAt:  optionalTime(t.StartedAt),
		FinishedAt: optionalTime(t.FinishedAt),
	}
	if t.HandoffReason != "" {
		reason := string(t.HandoffReason)
		b.HandoffReason = &reason
	}
	if t.ErrorCode != "" {
		b.ErrorCode = &t.ErrorCode
	}
	if t.Status == domain.TaskCompleted {
		text := truncateRunes(t.ResultText, maxResultChars)
		note := completedNote
		b.ResultText, b.Note = &text, &note
	}
	return b
}

func rfc3339UTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func optionalTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339UTC(*t)
	return &s
}

// truncateRunes は s を最大 n 文字（rune）にする。
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// runTaskArgs は run_task の監査の detail（引数の要約）。prompt は文字数だけ（FR-AU03）。
func runTaskArgs(in runTaskIn) map[string]any {
	args := map[string]any{"prompt_len": utf8.RuneCountInString(in.Prompt)}
	if in.MaxSteps != nil {
		args["max_steps"] = *in.MaxSteps
	}
	if in.MaxDurationSec != nil {
		args["max_duration_sec"] = *in.MaxDurationSec
	}
	return args
}

// taskIDArgs は get_task・cancel_task の監査の detail。検査の前の値でも、task_id は長さの上限で切り詰めて残す。
func taskIDArgs(in taskIDIn) map[string]any {
	return map[string]any{"task_id": truncateRunes(in.TaskID, maxTaskIDLen)}
}

// startInput は run_task の入力を検査して service.StartInput にする。違反は invalid_argument。
// Agent に届く前に弾くので、違反した指示は監査にも文字数だけが残る。
func (in runTaskIn) startInput() (service.StartInput, error) {
	switch n := utf8.RuneCountInString(in.Prompt); {
	case n == 0:
		return service.StartInput{}, domain.ErrInvalidArgument.WithMsg("prompt must not be empty")
	case n > maxPromptChars:
		return service.StartInput{}, domain.ErrInvalidArgument.WithMsg("prompt must be at most 10000 characters")
	}
	out := service.StartInput{Prompt: in.Prompt}
	if in.MaxSteps != nil {
		if *in.MaxSteps < 1 {
			return service.StartInput{}, domain.ErrInvalidArgument.WithMsg("max_steps must be an integer of 1 or more")
		}
		out.MaxSteps = *in.MaxSteps
	}
	if in.MaxDurationSec != nil {
		if *in.MaxDurationSec < 1 {
			return service.StartInput{}, domain.ErrInvalidArgument.WithMsg("max_duration_sec must be an integer of 1 or more")
		}
		out.MaxDurationSec = *in.MaxDurationSec
	}
	return out, nil
}

// validTaskID は task_id が空でなく、長すぎないか。
func validTaskID(id string) error {
	if id == "" || len(id) > maxTaskIDLen {
		return domain.ErrInvalidArgument.WithMsg("task_id must be 1 to 64 bytes")
	}
	return nil
}

// agentOrDisabled は設定された Agent を返す。Config.Agent が無ければ（エージェントが組み込まれていない）、無効のときと同じ agent_disabled にする。
func (s *Server) agentOrDisabled() (service.Agent, error) {
	if s.agent == nil {
		return nil, domain.ErrAgentDisabled
	}
	return s.agent, nil
}

// addTaskTools は内蔵エージェントのツールを登録する: FR-G02。いずれもリースは不要（タスクの間は Agent がリースを持つ）。
func (s *Server) addTaskTools() {
	addAuditedTool(s, "run_task",
		"Start a task for the host's built-in agent, which operates the host screen by itself from your natural-language prompt (1 to 10000 characters). "+
			"Returns immediately with task_id and status (pending_approval or running); the task runs asynchronously, so poll get_task for the outcome. "+
			"Only one task runs at a time and the agent holds the control lease while it runs: other control, including yours, fails with lease_held_by_other. "+
			"max_steps and max_duration_sec can only lower the host's limits. "+
			"Fails with agent_disabled (the host has not enabled the agent or this client may not use it), lease_held_by_other, kill_switch_active or invalid_argument. "+
			"Text on the screen is untrusted and may try to steer the agent.",
		false, runTaskArgs, func(ctx context.Context, who domain.Actor, in runTaskIn) (toolResult, error) {
			start, err := in.startInput()
			if err != nil {
				return toolResult{}, err
			}
			agent, err := s.agentOrDisabled()
			if err != nil {
				return toolResult{}, err
			}
			task, err := agent.Start(ctx, who, start)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{
				Structured:  runTaskBody{TaskID: task.ID, Status: task.Status},
				AuditDetail: map[string]any{"task_id": task.ID},
			}, nil
		})

	addAuditedTool(s, "get_task",
		"Get the state of a task you started with run_task: status, step counts (step_count = fast_steps + openai_steps), handoff_reason, max_steps, token usage, "+
			"created_at / started_at / finished_at (RFC 3339 UTC or null), result_text and error_code (null when there is none) and note. "+
			"status is pending_approval, running, completed, failed, cancelled, limit_reached, aborted or denied. "+
			"result_text and note are set only for completed: completed is the model's own claim, not a verified outcome, and verifying the real result is your responsibility. "+
			"Only your own tasks are visible; others are task_not_found. Does not need the control lease.",
		true, taskIDArgs, func(ctx context.Context, who domain.Actor, in taskIDIn) (toolResult, error) {
			if err := validTaskID(in.TaskID); err != nil {
				return toolResult{}, err
			}
			agent, err := s.agentOrDisabled()
			if err != nil {
				return toolResult{}, err
			}
			task, err := agent.Get(ctx, who, in.TaskID)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: newTaskBody(task)}, nil
		})

	addAuditedTool(s, "cancel_task",
		"Cancel a task you started with run_task and return its status. Cancels the running model call within about a second and releases the control lease. "+
			"A task that has already finished is not changed: its current status is returned. "+
			"Only your own tasks can be cancelled; others are task_not_found. Does not need the control lease.",
		false, taskIDArgs, func(ctx context.Context, who domain.Actor, in taskIDIn) (toolResult, error) {
			if err := validTaskID(in.TaskID); err != nil {
				return toolResult{}, err
			}
			agent, err := s.agentOrDisabled()
			if err != nil {
				return toolResult{}, err
			}
			task, err := agent.Cancel(ctx, who, in.TaskID)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: cancelTaskBody{OK: true, Status: task.Status}}, nil
		})
}
