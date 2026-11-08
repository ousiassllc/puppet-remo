package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Task 一覧の件数。API 仕様の `GET /tasks`（Audit と同じ既定 50、最大 200）。
const (
	DefaultTaskLimit = 50
	MaxTaskLimit     = 200
)

// TaskQuery は List の絞り込みとページ送り。空・ゼロの項目は絞り込まない。
type TaskQuery struct {
	ClientID string // 依頼したクライアント（agent_tasks.client_id。削除済みのクライアントのタスクは client_id が NULL なので含まれない）
	Cursor   string // 直前のページの最後の id。空なら先頭から
	Limit    int    // 0 以下は DefaultTaskLimit、MaxTaskLimit を超える値は MaxTaskLimit
}

// TaskPage は List の 1 ページ。Next は次のページがあるときの最後の id（無ければ空）で、そのまま次の Cursor に渡す。
type TaskPage struct {
	Tasks []domain.Task
	Next  string
}

// Tasks は内蔵エージェントのタスク（agent_tasks 表。FR-G03）のストア。書き込みは service.Agent だけ。
// プロンプト・最終回答の長さの上限は呼び出し側（service）が守り、ストアは表の CHECK を Go 側でも検査するだけ
// （総数と内訳の不一致・負の値・定義外の状態や引き継ぎ理由は ErrInvalidArgument）。
// ステップごとの記録は audit_logs（task_id・step_no）にあり、Audit.ListByTask で読む。
type Tasks interface {
	// Create は新しいタスクを挿入する。状態は pending_approval か running だけ（終了状態で作らない）。
	// ID の重複は ErrConflict。CreatedAt がゼロなら現在時刻。ClientID が空、またはクライアントの行がすでに無いときは
	// client_id を NULL で保存する（ClientLabel は残る。Audit.Append と同じ）。
	Create(ctx context.Context, t domain.Task) error
	// Get は 1 件。無ければ ErrNotFound。
	Get(ctx context.Context, id string) (domain.Task, error)
	// Update は可変の列（status・started_at・finished_at・step_count・fast_steps・openai_steps・handoff_reason・
	// input_tokens・output_tokens・result_text・error_code・capture_partial）だけを置き換える。それ以外の列は変えない。
	// 終了状態の行は二度と変えない（ErrConflict。cancel と完了の競合で、先に終わった結果が残る）。無ければ ErrNotFound。
	// 終了状態にするときは FinishedAt が必須で、実行中・承認待ちにするときは FinishedAt を持たせない。
	Update(ctx context.Context, t domain.Task) error
	// List は created_at 降順（同時刻は id 降順）で 1 ページ返す。カーソルの行が保守で消えていたら空のページ。
	List(ctx context.Context, q TaskQuery) (TaskPage, error)
	// ListActive は pending_approval・running のタスクを created_at 昇順（同時刻は id 昇順）で返す（ix_task_active）。
	ListActive(ctx context.Context) ([]domain.Task, error)
	// AbortActive は pending_approval・running のすべての行を 1 文で aborted（finished_at = at、error_code は NULL）にし、
	// その件数を返す（起動時の回収。NFR-A08）。at がゼロなら ErrInvalidArgument。
	AbortActive(ctx context.Context, at time.Time) (int, error)
	// PurgeFinishedBefore は finished_at < before の行を削除し、その件数を返す（タスクの保持期間。ER「データの保守」）。
	// 実行中・承認待ちの行は消さない。audit_logs.task_id は外部キー（ON DELETE SET NULL）で NULL になり、ログは残る。
	PurgeFinishedBefore(ctx context.Context, before time.Time) (int, error)
}

type taskStore struct{ db *DB }

const taskColumns = `id, client_id, client_label, prompt, status, model, max_steps, max_duration_sec,
	step_count, fast_steps, openai_steps, handoff_reason, input_tokens, output_tokens, result_text, error_code,
	created_at, started_at, finished_at, target_window_id, target_process, target_pid, capture_partial`

// taskActive は ix_task_active（部分索引）の条件。索引の WHERE と同じ式にして索引を使わせる。
const taskActive = `status IN ('pending_approval', 'running')`

func scanTask(r rowScanner) (domain.Task, error) {
	var (
		t                                  domain.Task
		client, handoff, resultText, errCd sql.NullString
		status                             string
		created                            int64
		started, finished                  sql.NullInt64
		targetWindow, targetProc           sql.NullString
		targetPID                          sql.NullInt64
		partial                            int
	)
	if err := r.Scan(&t.ID, &client, &t.ClientLabel, &t.Prompt, &status, &t.Model, &t.MaxSteps, &t.MaxDurationSec,
		&t.StepCount, &t.FastSteps, &t.OpenAISteps, &handoff, &t.InputTokens, &t.OutputTokens, &resultText, &errCd,
		&created, &started, &finished, &targetWindow, &targetProc, &targetPID, &partial); err != nil {
		return domain.Task{}, err
	}
	t.ClientID = client.String
	t.Status = domain.TaskStatus(status)
	t.HandoffReason = domain.HandoffReason(handoff.String)
	t.ResultText, t.ErrorCode = resultText.String, errCd.String
	t.CreatedAt = fromMS(created)
	t.StartedAt, t.FinishedAt = fromNullMS(started), fromNullMS(finished)
	t.TargetWindowID, t.TargetProcess, t.TargetPID = targetWindow.String, targetProc.String, uint32(targetPID.Int64)
	t.CapturePartial = partial != 0
	return t, nil
}

// checkTaskValues は agent_tasks の CHECK を Go 側でも検査する（制約違反の SQLite のエラーを呼び出し側へ見せない）。
func checkTaskValues(t domain.Task) error {
	switch {
	case t.ID == "":
		return fmt.Errorf("%w: task id is empty", domain.ErrInvalidArgument)
	case !t.Status.Valid():
		return fmt.Errorf("%w: task status %q", domain.ErrInvalidArgument, t.Status)
	case t.StepCount < 0 || t.FastSteps < 0 || t.OpenAISteps < 0 || t.InputTokens < 0 || t.OutputTokens < 0:
		return fmt.Errorf("%w: task counters must not be negative", domain.ErrInvalidArgument)
	case t.StepCount != t.FastSteps+t.OpenAISteps:
		return fmt.Errorf("%w: step_count %d != fast_steps %d + openai_steps %d",
			domain.ErrInvalidArgument, t.StepCount, t.FastSteps, t.OpenAISteps)
	case t.HandoffReason != "" && !t.HandoffReason.Valid():
		return fmt.Errorf("%w: task handoff_reason %q", domain.ErrInvalidArgument, t.HandoffReason)
	case t.Status.Terminal() && t.FinishedAt == nil:
		return fmt.Errorf("%w: task status %q requires finished_at", domain.ErrInvalidArgument, t.Status)
	case !t.Status.Terminal() && t.FinishedAt != nil:
		return fmt.Errorf("%w: task status %q must not have finished_at", domain.ErrInvalidArgument, t.Status)
	}
	return nil
}

func (s taskStore) Create(ctx context.Context, t domain.Task) error {
	if err := checkTaskValues(t); err != nil {
		return err
	}
	if t.Status.Terminal() {
		return fmt.Errorf("%w: task must be created as pending_approval or running, not %q", domain.ErrInvalidArgument, t.Status)
	}
	created := t.CreatedAt
	if created.IsZero() {
		created = s.db.clock.Now()
	}
	_, err := s.db.sql.ExecContext(ctx,
		`INSERT INTO agent_tasks (id, client_id, client_label, prompt, status, model, max_steps, max_duration_sec,
			step_count, fast_steps, openai_steps, handoff_reason, input_tokens, output_tokens, result_text, error_code,
			created_at, started_at, finished_at, target_window_id, target_process, target_pid, capture_partial)
		VALUES (?, (SELECT id FROM clients WHERE id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, nullStr(t.ClientID), t.ClientLabel, t.Prompt, string(t.Status), t.Model, t.MaxSteps, t.MaxDurationSec,
		t.StepCount, t.FastSteps, t.OpenAISteps, nullStr(string(t.HandoffReason)), t.InputTokens, t.OutputTokens,
		nullStr(t.ResultText), nullStr(t.ErrorCode), toMS(created), toNullMS(t.StartedAt), toNullMS(t.FinishedAt),
		nullStr(t.TargetWindowID), nullStr(t.TargetProcess), nullPID(t.TargetPID), boolInt(t.CapturePartial))
	return mapErr(err)
}

func (s taskStore) Get(ctx context.Context, id string) (domain.Task, error) {
	t, err := scanTask(s.db.sql.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM agent_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrNotFound
	}
	return t, err
}

func (s taskStore) Update(ctx context.Context, t domain.Task) error {
	if err := checkTaskValues(t); err != nil {
		return err
	}
	res, err := s.db.sql.ExecContext(ctx,
		`UPDATE agent_tasks SET status = ?, started_at = ?, finished_at = ?, step_count = ?, fast_steps = ?,
			openai_steps = ?, handoff_reason = ?, input_tokens = ?, output_tokens = ?, result_text = ?, error_code = ?,
			capture_partial = ?
		WHERE id = ? AND `+taskActive,
		string(t.Status), toNullMS(t.StartedAt), toNullMS(t.FinishedAt), t.StepCount, t.FastSteps, t.OpenAISteps,
		nullStr(string(t.HandoffReason)), t.InputTokens, t.OutputTokens, nullStr(t.ResultText), nullStr(t.ErrorCode), boolInt(t.CapturePartial), t.ID)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	// 効かなかった: 行が無いか、すでに終了している。
	var one int
	err = s.db.sql.QueryRowContext(ctx, `SELECT 1 FROM agent_tasks WHERE id = ?`, t.ID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: task %s is already finished", ErrConflict, t.ID)
}

func (s taskStore) List(ctx context.Context, q TaskQuery) (TaskPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultTaskLimit
	}
	limit = min(limit, MaxTaskLimit)

	var (
		where []string
		args  []any
	)
	if q.ClientID != "" {
		where = append(where, "client_id = ?")
		args = append(args, q.ClientID)
	}
	if q.Cursor != "" {
		var cursorCreated int64
		err := s.db.sql.QueryRowContext(ctx, `SELECT created_at FROM agent_tasks WHERE id = ?`, q.Cursor).Scan(&cursorCreated)
		if errors.Is(err, sql.ErrNoRows) {
			// 閲覧の途中で保持期間の削除が走り、カーソルの行が消えた。続きは無い。
			return TaskPage{}, nil
		}
		if err != nil {
			return TaskPage{}, err
		}
		where = append(where, "(created_at, id) < (?, ?)")
		args = append(args, cursorCreated, q.Cursor)
	}

	query := `SELECT ` + taskColumns + ` FROM agent_tasks`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// 次のページの有無を知るために 1 件多く読む。
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	tasks, err := s.queryTasks(ctx, query, args...)
	if err != nil {
		return TaskPage{}, err
	}
	var page TaskPage
	if len(tasks) > limit {
		tasks = tasks[:limit]
		page.Next = tasks[limit-1].ID
	}
	page.Tasks = tasks
	return page, nil
}

func (s taskStore) ListActive(ctx context.Context) ([]domain.Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskColumns+` FROM agent_tasks WHERE `+taskActive+` ORDER BY created_at, id`)
}

// queryTasks は行を読み切って rows を閉じる。
func (s taskStore) queryTasks(ctx context.Context, query string, args ...any) ([]domain.Task, error) {
	rows, err := s.db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	tasks := []domain.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s taskStore) AbortActive(ctx context.Context, at time.Time) (int, error) {
	if at.IsZero() {
		return 0, fmt.Errorf("%w: abort time is zero", domain.ErrInvalidArgument)
	}
	// 1 文なので、途中で落ちても一部だけ aborted になることはない。
	res, err := s.db.sql.ExecContext(ctx,
		`UPDATE agent_tasks SET status = 'aborted', finished_at = ?, error_code = NULL WHERE `+taskActive, toMS(at))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s taskStore) PurgeFinishedBefore(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.sql.ExecContext(ctx,
		`DELETE FROM agent_tasks WHERE finished_at IS NOT NULL AND finished_at < ? AND NOT (`+taskActive+`)`, toMS(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// nullPID は PID を NULL 可の列の値にする（0 は NULL）。
func nullPID(pid uint32) any {
	if pid == 0 {
		return nil
	}
	return int64(pid)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
