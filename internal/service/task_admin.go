package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルはホスト GUI のタスク画面・ダッシュボードの窓口（TaskAdmin。FR-H10）。タスクの記録（repository.Tasks）と、
// ステップ（監査ログの task_id 紐づけの行。repository.Audit.ListByTask）を読み、中断は Agent に渡す。
// 引数の表示は AuditReader と同じ変換（auditArgs。秘密の伏せ字・500 文字の切り詰め）を通す。

// TaskCanceller はホスト GUI の中断ボタンの先（*AgentService が満たす）。
type TaskCanceller interface {
	CancelByHost(ctx context.Context, id string) (domain.Task, error)
}

// TaskAdminConfig は NewTaskAdmin の設定。Audit・Clock 以外は必須。
type TaskAdminConfig struct {
	Tasks  repository.Tasks
	Steps  repository.Audit // ListByTask だけを使う
	Cancel TaskCanceller
	// Audit は host の中断を記録する先。nil なら記録しない。
	Audit AuditRecorder
}

type taskAdmin struct{ cfg TaskAdminConfig }

var _ TaskAdmin = (*taskAdmin)(nil)

// NewTaskAdmin は TaskAdmin を作る。
func NewTaskAdmin(cfg TaskAdminConfig) (TaskAdmin, error) {
	if cfg.Tasks == nil || cfg.Steps == nil || cfg.Cancel == nil {
		return nil, errors.New("service: task admin requires Tasks, Steps and Cancel")
	}
	return &taskAdmin{cfg: cfg}, nil
}

func (t *taskAdmin) List(ctx context.Context, q TaskListQuery) (TaskPage, error) {
	page, err := t.cfg.Tasks.List(ctx, repository.TaskQuery{Cursor: q.Cursor, Limit: q.Limit})
	if err != nil {
		return TaskPage{}, fmt.Errorf("list tasks: %w", err)
	}
	return TaskPage{Tasks: page.Tasks, Next: page.Next}, nil
}

func (t *taskAdmin) Detail(ctx context.Context, id string) (TaskDetail, error) {
	task, err := t.cfg.Tasks.Get(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return TaskDetail{}, taskNotFound(id)
	}
	if err != nil {
		return TaskDetail{}, fmt.Errorf("read the task: %w", err)
	}
	entries, err := t.cfg.Steps.ListByTask(ctx, id)
	if err != nil {
		return TaskDetail{}, fmt.Errorf("read the task steps: %w", err)
	}
	d := TaskDetail{Task: task}
	for _, e := range entries {
		if s, ok := taskStepOf(e); ok {
			d.Steps = append(d.Steps, s)
		}
	}
	return d, nil
}

func (t *taskAdmin) Running(ctx context.Context) ([]domain.Task, error) {
	tasks, err := t.cfg.Tasks.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list running tasks: %w", err)
	}
	return tasks, nil
}

// CancelByHost は中断ボタン。Agent が中断し、その結果の状態を返す。監査は agent.task_cancel（actor_type = host）。無いタスクは ErrTaskNotFound。
func (t *taskAdmin) CancelByHost(ctx context.Context, id string) (domain.Task, error) {
	task, err := t.cfg.Cancel.CancelByHost(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if t.cfg.Audit != nil {
		t.cfg.Audit.Record(context.WithoutCancel(ctx), repository.AuditEntry{
			Kind: "agent", Event: agentTaskCancelAuditEvent, ActorType: domain.ActorHost, ClientLabel: hostLabel,
			TaskID: id, Result: auditOK, DetailJSON: marshalDetail(map[string]any{"task_id": id, "status": string(task.Status)}),
		})
	}
	return task, nil
}

// taskStepEvents はタスクの詳細に「ステップ」として出す監査のイベント。agent.task_start・agent.task_end は、タスクの行
// （作成・終了の時刻と状態）が同じ情報を持つので出さない。
var taskStepEvents = map[string]bool{agentStepAuditEvent: true, agentJudgeAuditEvent: true, agentSafetyCheckAuditEvent: true}

// taskStepOf は監査の 1 行をタスクの 1 ステップにする（ステップでない行は ok = false）。
func taskStepOf(e repository.AuditEntry) (TaskStep, bool) {
	if e.Kind != "agent" || !taskStepEvents[e.Event] {
		return TaskStep{}, false
	}
	s := TaskStep{Event: e.Event, Result: e.Result, AuditID: e.ID, Thumb: e.Thumb != nil, At: e.TS, Args: auditArgs(e.DetailJSON)}
	if e.StepNo != nil {
		s.No = *e.StepNo
	}
	var detail map[string]json.RawMessage
	_ = json.Unmarshal(e.DetailJSON, &detail)
	s.Route = detailString(detail, "route")
	s.Summary = stepSummary(e, detail)
	return s, true
}

func detailString(detail map[string]json.RawMessage, key string) string {
	var v string
	if raw, ok := detail[key]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return ""
}

// stepSummary はステップの短い説明（本文なし）。アクションはツール名、行動しない高速パスのパスは選択と引き継ぎ理由、
// Judge・安全チェックはその結果。
func stepSummary(e repository.AuditEntry, detail map[string]json.RawMessage) string {
	var parts []string
	switch e.Event {
	case agentJudgeAuditEvent:
		parts = append(parts, "judge: "+detailString(detail, "judge"))
		if r := detailString(detail, "judge_reason"); r != "" {
			parts = append(parts, "("+r+")")
		}
	case agentSafetyCheckAuditEvent:
		parts = append(parts, "safety check: "+detailString(detail, "result"))
	default:
		if e.Tool != "" {
			parts = append(parts, e.Tool)
		} else if c := detailString(detail, "chosen_id"); c != "" {
			parts = append(parts, "chosen: "+c)
		}
		if r := detailString(detail, "handoff_reason"); r != "" {
			parts = append(parts, "→ handoff to OpenAI ("+r+")")
		}
	}
	if e.ErrorCode != "" {
		parts = append(parts, "["+e.ErrorCode+"]")
	}
	return strings.Join(parts, " ")
}
