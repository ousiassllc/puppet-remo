package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func taskAt(id string, created time.Time) domain.Task {
	return domain.Task{
		ID: id, ClientLabel: "dev1", Prompt: "メモ帳を開いて", Status: domain.TaskRunning, Model: "m",
		MaxSteps: 50, MaxDurationSec: 600, CreatedAt: created,
	}
}

func insertTestClient(t *testing.T, db *DB, id string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES (?, 'n-'||?, 'l', 'dev1', 1, 1)`, id, id)
}

func mustCreateTask(t *testing.T, db *DB, task domain.Task) {
	t.Helper()
	if err := db.Tasks().Create(context.Background(), task); err != nil {
		t.Fatalf("Create %s: %v", task.ID, err)
	}
}

func TestTasksCreateGetRoundTrip(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	insertTestClient(t, db, "c1")
	started := clock.Now().Add(time.Second)

	want := domain.Task{
		ID: "01T1", ClientID: "c1", ClientLabel: "dev1", Prompt: "p", Status: domain.TaskRunning, Model: "gpt",
		MaxSteps: 30, MaxDurationSec: 300, StepCount: 3, FastSteps: 2, OpenAISteps: 1,
		HandoffReason: domain.HandoffLowConfidence, InputTokens: 10, OutputTokens: 20,
		CreatedAt: clock.Now(), StartedAt: &started,
	}
	mustCreateTask(t, db, want)
	got, err := db.Tasks().Get(ctx, "01T1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v, want %+v", got, want)
	}

	// 時刻が空なら現在時刻、任意項目は空のまま戻る。
	mustCreateTask(t, db, domain.Task{ID: "01T2", ClientLabel: "x", Prompt: "p", Status: domain.TaskPendingApproval,
		Model: "m", MaxSteps: 1, MaxDurationSec: 1})
	got, err = db.Tasks().Get(ctx, "01T2")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(clock.Now()) || got.ClientID != "" || got.HandoffReason != "" ||
		got.ResultText != "" || got.ErrorCode != "" || got.StartedAt != nil || got.FinishedAt != nil {
		t.Errorf("defaults = %+v", got)
	}

	if _, err := db.Tasks().Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestTasksCreateRejects(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	mustCreateTask(t, db, taskAt("dup", clock.Now()))
	if err := db.Tasks().Create(ctx, taskAt("dup", clock.Now())); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate id = %v, want ErrConflict", err)
	}

	fin := clock.Now()
	tests := map[string]func(*domain.Task){
		"empty id":           func(k *domain.Task) { k.ID = "" },
		"unknown status":     func(k *domain.Task) { k.Status = "weird" },
		"terminal status":    func(k *domain.Task) { k.Status = domain.TaskCompleted; k.FinishedAt = &fin },
		"negative steps":     func(k *domain.Task) { k.StepCount, k.FastSteps, k.OpenAISteps = -1, -1, 0 },
		"negative tokens":    func(k *domain.Task) { k.InputTokens = -1 },
		"total != breakdown": func(k *domain.Task) { k.StepCount, k.FastSteps, k.OpenAISteps = 3, 1, 1 },
		"bad handoff":        func(k *domain.Task) { k.HandoffReason = "whatever" },
		"active finished_at": func(k *domain.Task) { k.FinishedAt = &fin },
	}
	for name, mutate := range tests {
		k := taskAt("bad", clock.Now())
		mutate(&k)
		if err := db.Tasks().Create(ctx, k); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: Create = %v, want ErrInvalidArgument", name, err)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agent_tasks WHERE id = 'bad'`); n != 0 {
		t.Errorf("rejected tasks were stored: %d", n)
	}
}

// agent_tasks.handoff_reason の CHECK は 8 値だけ。Go 側の検査と表の CHECK が食い違っていないことを確かめる。
func TestTasksHandoffReasonMatchesTableCheck(t *testing.T) {
	db, clock := openTestDB(t)
	reasons := []domain.HandoffReason{
		domain.HandoffAbstain, domain.HandoffLowConfidence, domain.HandoffNoCandidates, domain.HandoffInvalidChoice,
		domain.HandoffNoProgress, domain.HandoffGoalMaybeDone, domain.HandoffFastStepLimit, domain.HandoffChooserUnavailable,
	}
	for i, r := range reasons {
		k := taskAt(fmt.Sprintf("h%d", i), clock.Now())
		k.HandoffReason = r
		mustCreateTask(t, db, k) // 表の CHECK も通る
	}
	if _, err := db.sql.ExecContext(context.Background(),
		`UPDATE agent_tasks SET handoff_reason = 'bogus' WHERE id = 'h0'`); err == nil {
		t.Error("table accepted an unknown handoff_reason")
	}
}

func TestTasksUpdateMutableColumnsOnly(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	insertTestClient(t, db, "c1")
	orig := taskAt("u1", clock.Now())
	orig.ClientID = "c1"
	orig.Status = domain.TaskPendingApproval
	mustCreateTask(t, db, orig)

	started, finished := clock.Now().Add(time.Second), clock.Now().Add(time.Minute)
	upd := orig
	upd.Status, upd.StartedAt, upd.FinishedAt = domain.TaskCompleted, &started, &finished
	upd.StepCount, upd.FastSteps, upd.OpenAISteps = 5, 3, 2
	upd.HandoffReason = domain.HandoffNoProgress
	upd.InputTokens, upd.OutputTokens = 100, 200
	upd.ResultText = "done"
	// 不変の列は渡されても無視する。
	upd.Prompt, upd.Model, upd.ClientLabel, upd.MaxSteps, upd.ClientID = "other", "other", "other", 1, ""
	if err := db.Tasks().Update(ctx, upd); err != nil {
		t.Fatal(err)
	}

	want := upd
	want.Prompt, want.Model, want.ClientLabel, want.MaxSteps, want.ClientID = orig.Prompt, orig.Model, orig.ClientLabel, orig.MaxSteps, "c1"
	got, err := db.Tasks().Get(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after Update = %+v, want %+v", got, want)
	}

	if err := db.Tasks().Update(ctx, taskAt("missing", clock.Now())); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v, want ErrNotFound", err)
	}
}

func TestTasksUpdateRejects(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	mustCreateTask(t, db, taskAt("u1", clock.Now()))
	fin := clock.Now()

	tests := map[string]func(*domain.Task){
		"step_count != fast+openai": func(k *domain.Task) { k.StepCount, k.FastSteps, k.OpenAISteps = 2, 1, 0 },
		"terminal without finished": func(k *domain.Task) { k.Status = domain.TaskFailed },
		"running with finished":     func(k *domain.Task) { k.FinishedAt = &fin },
		"unknown status":            func(k *domain.Task) { k.Status = "weird" },
		"bad handoff":               func(k *domain.Task) { k.HandoffReason = "x" },
		"negative tokens":           func(k *domain.Task) { k.OutputTokens = -5 },
	}
	for name, mutate := range tests {
		k := taskAt("u1", clock.Now())
		mutate(&k)
		if err := db.Tasks().Update(ctx, k); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: Update = %v, want ErrInvalidArgument", name, err)
		}
	}
	got, _ := db.Tasks().Get(ctx, "u1")
	if got.Status != domain.TaskRunning || got.StepCount != 0 {
		t.Errorf("rejected updates changed the row: %+v", got)
	}
}

// 終了状態の行は二度と変わらない（cancel と完了の競合で、先に終わった結果が残る）。
func TestTasksTerminalIsImmutable(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	mustCreateTask(t, db, taskAt("t1", clock.Now()))

	fin := clock.Now().Add(time.Second)
	cancelled := taskAt("t1", clock.Now())
	cancelled.Status, cancelled.FinishedAt, cancelled.ErrorCode = domain.TaskCancelled, &fin, "cancelled_by_user"
	if err := db.Tasks().Update(ctx, cancelled); err != nil {
		t.Fatal(err)
	}

	for _, next := range []domain.TaskStatus{domain.TaskCompleted, domain.TaskCancelled, domain.TaskRunning} {
		k := taskAt("t1", clock.Now())
		k.Status, k.ResultText = next, "late"
		if next.Terminal() {
			later := fin.Add(time.Hour)
			k.FinishedAt = &later
		}
		if err := db.Tasks().Update(ctx, k); !errors.Is(err, ErrConflict) {
			t.Errorf("Update terminal -> %s = %v, want ErrConflict", next, err)
		}
	}
	got, err := db.Tasks().Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCancelled || got.ResultText != "" || got.ErrorCode != "cancelled_by_user" || !got.FinishedAt.Equal(fin) {
		t.Errorf("terminal row changed: %+v", got)
	}
}

func TestTasksListKeysetPagingStableWithEqualCreatedAt(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	insertTestClient(t, db, "c1")
	insertTestClient(t, db, "c2")
	base := clock.Now()
	// created_at が同じ行を複数含める。id の降順で安定して並ぶこと。
	for i := range 7 {
		k := taskAt(fmt.Sprintf("t%02d", i), base.Add(time.Duration(i/3)*time.Second))
		k.ClientID = "c1"
		if i%2 == 1 {
			k.ClientID = "c2"
		}
		mustCreateTask(t, db, k)
	}

	var ids []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging does not terminate")
		}
		page, err := db.Tasks().List(ctx, TaskQuery{Cursor: cursor, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range page.Tasks {
			ids = append(ids, k.ID)
		}
		if page.Next == "" {
			if len(page.Tasks) > 3 {
				t.Errorf("last page has %d rows", len(page.Tasks))
			}
			break
		}
		if len(page.Tasks) != 3 || page.Next != page.Tasks[2].ID {
			t.Fatalf("page = %d rows, next %q", len(page.Tasks), page.Next)
		}
		cursor = page.Next
	}
	want := []string{"t06", "t05", "t04", "t03", "t02", "t01", "t00"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("paged ids = %v, want %v", ids, want)
	}

	// ちょうど 1 ページ分なら Next は空（次が無いのに続きを案内しない）。
	page, err := db.Tasks().List(ctx, TaskQuery{Limit: 7})
	if err != nil || len(page.Tasks) != 7 || page.Next != "" {
		t.Errorf("exact page: rows=%d next=%q err=%v", len(page.Tasks), page.Next, err)
	}

	// クライアントで絞る。
	page, err = db.Tasks().List(ctx, TaskQuery{ClientID: "c2"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range page.Tasks {
		got = append(got, k.ID)
	}
	if !reflect.DeepEqual(got, []string{"t05", "t03", "t01"}) {
		t.Errorf("client filter = %v", got)
	}

	// 既定の件数と上限。
	for i := range 210 {
		mustCreateTask(t, db, taskAt(fmt.Sprintf("x%03d", i), base.Add(time.Hour)))
	}
	if page, _ = db.Tasks().List(ctx, TaskQuery{}); len(page.Tasks) != DefaultTaskLimit {
		t.Errorf("default limit = %d", len(page.Tasks))
	}
	if page, _ = db.Tasks().List(ctx, TaskQuery{Limit: 10000}); len(page.Tasks) != MaxTaskLimit {
		t.Errorf("max limit = %d", len(page.Tasks))
	}
}

// ページ送りの途中でカーソルの行が保守で消えても、エラーにせず空のページを返す。
func TestTasksListCursorRowPurged(t *testing.T) {
	db, clock := openTestDB(t)
	k := taskAt("t1", clock.Now())
	k.Status = domain.TaskDenied
	fin := clock.Now()
	k.FinishedAt = &fin
	// 終了状態の行は Create できないので、作ってから終わらせる。
	mustCreateTask(t, db, taskAt("t1", clock.Now()))
	if err := db.Tasks().Update(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Tasks().PurgeFinishedBefore(context.Background(), fin.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err := db.Tasks().List(context.Background(), TaskQuery{Cursor: "t1"})
	if err != nil || len(page.Tasks) != 0 || page.Next != "" {
		t.Errorf("List with purged cursor = %+v, %v", page, err)
	}
}

func TestTasksListActiveAndAbortActive(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	base := clock.Now()

	pending := taskAt("a-pending", base.Add(time.Second))
	pending.Status = domain.TaskPendingApproval
	mustCreateTask(t, db, taskAt("a-running", base))
	mustCreateTask(t, db, pending)
	mustCreateTask(t, db, taskAt("done", base))
	fin := base.Add(time.Minute)
	done := taskAt("done", base)
	done.Status, done.FinishedAt, done.ErrorCode = domain.TaskFailed, &fin, "openai_error"
	if err := db.Tasks().Update(ctx, done); err != nil {
		t.Fatal(err)
	}

	active, err := db.Tasks().ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range active {
		ids = append(ids, k.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a-running", "a-pending"}) {
		t.Errorf("ListActive = %v", ids)
	}

	// 起動時の回収: 終了済みの行は触らず、error_code は NULL のまま aborted にする。
	at := base.Add(time.Hour)
	n, err := db.Tasks().AbortActive(ctx, at)
	if err != nil || n != 2 {
		t.Fatalf("AbortActive = %d, %v; want 2", n, err)
	}
	for _, id := range []string{"a-running", "a-pending"} {
		k, _ := db.Tasks().Get(ctx, id)
		if k.Status != domain.TaskAborted || k.FinishedAt == nil || !k.FinishedAt.Equal(at) || k.ErrorCode != "" {
			t.Errorf("%s after abort = %+v", id, k)
		}
	}
	if k, _ := db.Tasks().Get(ctx, "done"); k.Status != domain.TaskFailed || !k.FinishedAt.Equal(fin) || k.ErrorCode != "openai_error" {
		t.Errorf("finished task touched: %+v", k)
	}
	if active, _ = db.Tasks().ListActive(ctx); len(active) != 0 {
		t.Errorf("ListActive after abort = %d rows", len(active))
	}
	if n, err = db.Tasks().AbortActive(ctx, at); err != nil || n != 0 {
		t.Errorf("second AbortActive = %d, %v", n, err)
	}
	if _, err = db.Tasks().AbortActive(ctx, time.Time{}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("AbortActive(zero) = %v, want ErrInvalidArgument", err)
	}
}

func TestTasksPurgeFinishedBeforeKeepsActiveAndNullsAuditTaskID(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	base := clock.Now()

	finish := func(id string, at time.Time) {
		mustCreateTask(t, db, taskAt(id, base))
		k := taskAt(id, base)
		k.Status, k.FinishedAt = domain.TaskCompleted, &at
		if err := db.Tasks().Update(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	finish("old", base.Add(-48*time.Hour))
	finish("edge", base.Add(-24*time.Hour))
	finish("new", base.Add(-time.Hour))
	mustCreateTask(t, db, taskAt("still-running", base.Add(-100*24*time.Hour)))

	for _, id := range []string{"old", "new", "still-running"} {
		step := 1
		if _, err := db.Audit().Append(ctx, AuditEntry{Kind: "agent", Event: "agent.step", ActorType: domain.ActorAgent,
			ClientLabel: "dev1", TaskID: id, StepNo: &step, Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := db.Tasks().PurgeFinishedBefore(ctx, base.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PurgeFinishedBefore = %d, %v; want 1 (boundary row stays)", n, err)
	}
	if _, err := db.Tasks().Get(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old task still exists: %v", err)
	}
	for _, id := range []string{"edge", "new", "still-running"} {
		if _, err := db.Tasks().Get(ctx, id); err != nil {
			t.Errorf("task %s was purged: %v", id, err)
		}
	}
	// 実行中は、どれだけ古くても消さない。
	if n, _ = db.Tasks().PurgeFinishedBefore(ctx, base.Add(24*365*time.Hour)); n != 2 {
		t.Errorf("purge with far cutoff = %d, want 2 (finished only)", n)
	}
	if _, err := db.Tasks().Get(ctx, "still-running"); err != nil {
		t.Errorf("active task was purged: %v", err)
	}

	// ログの行は残り、task_id だけ NULL になる。
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs`); n != 3 {
		t.Errorf("audit rows = %d, want 3", n)
	}
	if n := nullCount(t, db, "audit_logs", "task_id", "1 = 1"); n != 2 {
		t.Errorf("audit rows with NULL task_id = %d, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs WHERE task_id = 'still-running'`); n != 1 {
		t.Errorf("active task's audit rows = %d, want 1", n)
	}
}

func TestTasksClientDeletionNullsClientID(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	insertTestClient(t, db, "c1")
	k := taskAt("t1", clock.Now())
	k.ClientID = "c1"
	mustCreateTask(t, db, k)
	mustExec(t, db, `DELETE FROM clients WHERE id = 'c1'`)

	got, err := db.Tasks().Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != "" || got.ClientLabel != "dev1" {
		t.Errorf("after client delete: client_id=%q label=%q", got.ClientID, got.ClientLabel)
	}

	// 削除済みのクライアントの ID を渡しても挿入でき、client_id は NULL になる（ラベルが履歴を持つ）。
	k2 := taskAt("t2", clock.Now())
	k2.ClientID = "c1"
	mustCreateTask(t, db, k2)
	if got, _ = db.Tasks().Get(ctx, "t2"); got.ClientID != "" {
		t.Errorf("client_id of unknown client = %q, want empty", got.ClientID)
	}
}

func TestAuditListByTaskAndTaskIDFilter(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	mustCreateTask(t, db, taskAt("k1", clock.Now()))
	mustCreateTask(t, db, taskAt("k2", clock.Now()))

	add := func(task string, step *int, event string, detail string) int64 {
		t.Helper()
		e := AuditEntry{Kind: "agent", Event: event, ActorType: domain.ActorAgent, ClientLabel: "dev1",
			TaskID: task, StepNo: step, Result: "ok"}
		if detail != "" {
			e.DetailJSON = json.RawMessage(detail)
		}
		id, err := db.Audit().Append(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
		return id
	}
	n := func(i int) *int { return &i }
	// 挿入順は step_no の順と違う。task_id が NULL / 別タスクの行は含めない。
	step3 := add("k1", n(3), "agent.step", `{"route":"openai"}`)
	step1 := add("k1", n(1), "agent.step", `{"route":"fast"}`)
	taskEvt := add("k1", nil, "agent.task_start", "")
	step2a := add("k1", n(2), "agent.step", `{"route":"fast","chosen_id":"e1"}`)
	step2b := add("k1", n(2), "agent.step_result", "")
	add("k2", n(1), "agent.step", "")
	add("", nil, "lease.acquire", "")
	if err := db.Audit().AttachThumb(ctx, step1, ThumbMeta{RelPath: "2026/11/a.jpg", SizeBytes: 10, Width: 4, Height: 3}); err != nil {
		t.Fatal(err)
	}

	got, err := db.Audit().ListByTask(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if want := []int64{taskEvt, step1, step2a, step2b, step3}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ListByTask ids = %v, want %v (step_no asc, NULL first, ties by id)", ids, want)
	}
	if string(got[1].DetailJSON) != `{"route":"fast"}` {
		t.Errorf("detail_json = %q, want it included", got[1].DetailJSON)
	}
	if got[1].Thumb == nil || got[1].Thumb.RelPath != "2026/11/a.jpg" || got[2].Thumb != nil {
		t.Errorf("thumb meta = %+v / %+v", got[1].Thumb, got[2].Thumb)
	}
	if got[0].TaskID != "k1" || got[0].StepNo != nil {
		t.Errorf("task-level row = %+v", got[0])
	}

	if none, err := db.Audit().ListByTask(ctx, "unknown"); err != nil || none == nil || len(none) != 0 {
		t.Errorf("ListByTask(unknown) = %v, %v; want empty non-nil", none, err)
	}
	if _, err := db.Audit().ListByTask(ctx, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("ListByTask(empty) = %v, want ErrInvalidArgument", err)
	}

	// Query の TaskID は完全一致。
	entries, _, err := db.Audit().Query(ctx, AuditQuery{TaskID: "k2"})
	if err != nil || len(entries) != 1 || entries[0].TaskID != "k2" {
		t.Errorf("Query(TaskID=k2) = %+v, %v", entries, err)
	}
	if entries, _, _ = db.Audit().Query(ctx, AuditQuery{TaskID: "k"}); len(entries) != 0 {
		t.Errorf("Query(TaskID=k) matched %d rows; the filter must be exact", len(entries))
	}
}

func TestAuditListByTaskIsCapped(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	mustCreateTask(t, db, taskAt("k1", clock.Now()))
	mustExec(t, db, `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < ?)
		INSERT INTO audit_logs (ts, kind, event, actor_type, client_label, task_id, step_no, result)
		SELECT 1, 'agent', 'agent.step', 'agent', 'x', 'k1', i, 'ok' FROM s`, MaxTaskStepEntries+5)
	got, err := db.Audit().ListByTask(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxTaskStepEntries || *got[0].StepNo != 1 || *got[len(got)-1].StepNo != MaxTaskStepEntries {
		t.Errorf("rows = %d, first/last step = %d/%d; want the earliest %d steps",
			len(got), *got[0].StepNo, *got[len(got)-1].StepNo, MaxTaskStepEntries)
	}
}

// run_task の対象ウィンドウ（0006。#158）は Create・Get で往復し、capture_partial は Update で変わる。タイトルを持つ列は無い。
func TestTasksTargetRoundTripAndCapturePartial(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()
	task := taskAt("01TS", clock.Now())
	task.TargetWindowID, task.TargetProcess, task.TargetPID = "1001", "notepad.exe", 4242
	mustCreateTask(t, db, task)
	got, err := db.Tasks().Get(ctx, "01TS")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, task) {
		t.Errorf("Get = %+v, want %+v", got, task)
	}
	task.CapturePartial = true
	if err := db.Tasks().Update(ctx, task); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.Tasks().Get(ctx, "01TS"); !got.CapturePartial || !got.Scoped() {
		t.Errorf("after Update = %+v", got)
	}
	// スコープなしの行は対象の列が NULL（従来どおり）。
	mustCreateTask(t, db, taskAt("01TU", clock.Now()))
	if n := count(t, db, `SELECT COUNT(*) FROM agent_tasks WHERE id = '01TU' AND target_window_id IS NULL AND target_process IS NULL AND target_pid IS NULL`); n != 1 {
		t.Errorf("unscoped task has target columns set")
	}
}
