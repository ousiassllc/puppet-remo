package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// taskMaint は実 DB（agent_tasks・settings）の上の Maintenance（Tasks つき）。
type taskMaint struct {
	*Maintenance
	db    *repository.DB
	clock *maintClock
}

func newTaskMaint(t *testing.T) *taskMaint {
	t.Helper()
	clock := newMaintClock()
	db, err := repository.Open(context.Background(), filepath.Join(t.TempDir(), "puppet-remo.db"), repository.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, err := NewMaintenance(MaintenanceConfig{
		ConnRequests: db.ConnRequests(), Tasks: db.Tasks(), Settings: db.Settings(), Clock: clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &taskMaint{Maintenance: m, db: db, clock: clock}
}

// addTask は now から age 前に作ったタスクを status で追加する。終了状態なら finishedAge 前に終了したことにする。
func (m *taskMaint) addTask(t *testing.T, id string, status domain.TaskStatus, age, finishedAge time.Duration) {
	t.Helper()
	ctx := context.Background()
	task := domain.Task{
		ID: id, ClientLabel: "dev", Prompt: "p", Status: domain.TaskRunning, Model: "m", MaxSteps: 50, MaxDurationSec: 600,
		CreatedAt: m.clock.Now().Add(-age),
	}
	if status == domain.TaskPendingApproval {
		task.Status = status
	}
	if err := m.db.Tasks().Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if status.Terminal() {
		fin := m.clock.Now().Add(-finishedAge)
		task.Status, task.FinishedAt = status, &fin
		if err := m.db.Tasks().Update(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
}

func (m *taskMaint) statuses(t *testing.T) map[string]domain.TaskStatus {
	t.Helper()
	page, err := m.db.Tasks().List(context.Background(), repository.TaskQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.TaskStatus{}
	for _, task := range page.Tasks {
		out[task.ID] = task.Status
	}
	return out
}

func TestMaintenanceRunOnceDeletesOnlyFinishedTasksPastRetention(t *testing.T) {
	m := newTaskMaint(t)
	m.addTask(t, "old-done", domain.TaskCompleted, 40*day, 40*day)
	m.addTask(t, "old-failed", domain.TaskFailed, 100*day, 31*day)
	m.addTask(t, "recent-done", domain.TaskCompleted, 40*day, 29*day) // 作成は古いが、終了が保持期間内
	m.addTask(t, "old-running", domain.TaskRunning, 90*day, 0)        // 実行中・承認待ちは古くても消さない
	m.addTask(t, "old-pending", domain.TaskPendingApproval, 90*day, 0)
	if err := m.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := m.statuses(t)
	want := map[string]domain.TaskStatus{"recent-done": domain.TaskCompleted, "old-running": domain.TaskRunning, "old-pending": domain.TaskPendingApproval}
	if len(got) != len(want) {
		t.Fatalf("tasks = %v, want %v", got, want)
	}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("task %s = %q, want %q", id, got[id], st)
		}
	}
}

func TestMaintenanceTaskRetentionFollowsAuditRetentionDays(t *testing.T) {
	m := newTaskMaint(t)
	if err := m.db.Settings().Set(context.Background(), "audit.retention_days", []byte("7")); err != nil {
		t.Fatal(err)
	}
	m.addTask(t, "d10", domain.TaskCompleted, 10*day, 10*day)
	m.addTask(t, "d5", domain.TaskCompleted, 5*day, 5*day)
	if err := m.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.statuses(t); len(got) != 1 || got["d5"] != domain.TaskCompleted {
		t.Fatalf("tasks = %v, want only d5", got)
	}
	// 値が不正なら既定の 30 日（監査と同じ規則）。
	if err := m.db.Settings().Set(context.Background(), "audit.retention_days", []byte(`"x"`)); err != nil {
		t.Fatal(err)
	}
	m.addTask(t, "d29", domain.TaskCompleted, 29*day, 29*day)
	m.addTask(t, "d31", domain.TaskFailed, 31*day, 31*day)
	if err := m.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.statuses(t); got["d31"] != "" || got["d29"] != domain.TaskCompleted {
		t.Fatalf("tasks = %v", got)
	}
}

func TestMaintenanceAbortActiveTasks(t *testing.T) {
	m := newTaskMaint(t)
	m.addTask(t, "running", domain.TaskRunning, time.Hour, 0)
	m.addTask(t, "pending", domain.TaskPendingApproval, time.Hour, 0)
	m.addTask(t, "done", domain.TaskCompleted, time.Hour, time.Hour)
	n, err := m.AbortActiveTasks(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("AbortActiveTasks = %d, %v, want 2", n, err)
	}
	got := m.statuses(t)
	if got["running"] != domain.TaskAborted || got["pending"] != domain.TaskAborted || got["done"] != domain.TaskCompleted {
		t.Fatalf("statuses = %v", got)
	}
	task, _ := m.db.Tasks().Get(context.Background(), "running")
	if task.FinishedAt == nil || !task.FinishedAt.Equal(m.clock.Now()) || task.ErrorCode != "" {
		t.Errorf("task = %+v, want finished_at = now and no error code", task)
	}
	if n, err := m.AbortActiveTasks(context.Background()); err != nil || n != 0 {
		t.Errorf("second call = %d, %v", n, err)
	}
}

func TestMaintenanceWithoutTasksTouchesNoTasks(t *testing.T) {
	m, err := NewMaintenance(MaintenanceConfig{ConnRequests: &expireRecorder{calls: make(chan struct{}, 4)}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := m.AbortActiveTasks(context.Background()); err != nil || n != 0 {
		t.Errorf("AbortActiveTasks = %d, %v", n, err)
	}
	if err := m.PurgeTasksBeforeRetention(context.Background()); err != nil {
		t.Error(err)
	}
	if _, err := NewMaintenance(MaintenanceConfig{ConnRequests: &expireRecorder{}, Tasks: &orderTasks{}}); err == nil {
		t.Error("Tasks without Settings must fail")
	}
}

// 呼び出しの順序を記録する偽物。
type opLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *opLog) add(op string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

func (l *opLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.ops)
}

type orderConn struct {
	repository.ConnRequests
	log     *opLog
	expired chan struct{}
}

func (c *orderConn) Expire(context.Context, time.Time) error {
	c.log.add("expire")
	c.expired <- struct{}{}
	return nil
}

type orderTasks struct {
	repository.Tasks
	log      *opLog
	abortErr error
	abortAt  time.Time
}

func (o *orderTasks) AbortActive(_ context.Context, at time.Time) (int, error) {
	o.log.add("abort")
	o.abortAt = at
	return 3, o.abortErr
}

func (o *orderTasks) PurgeFinishedBefore(context.Context, time.Time) (int, error) {
	o.log.add("purge")
	return 0, nil
}

// Run は最初の保守（RunOnce）の前に AbortActive を 1 回だけ呼ぶ（NFR-A08）。以後の保守では呼ばない。
func TestMaintenanceRunAbortsActiveTasksOnceBeforeTheFirstMaintenance(t *testing.T) {
	for name, abortErr := range map[string]error{"ok": nil, "failure does not stop maintenance": errors.New("database is locked")} {
		t.Run(name, func(t *testing.T) {
			clock := newMaintClock()
			log := &opLog{}
			conn := &orderConn{log: log, expired: make(chan struct{}, 4)}
			tasks := &orderTasks{log: log, abortErr: abortErr}
			m, err := NewMaintenance(MaintenanceConfig{
				ConnRequests: conn, Tasks: tasks, Settings: newCtlSettings(), Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); m.Run(ctx) }()

			recvOrFail(t, conn.expired, "the first maintenance")
			recvOrFail(t, clock.waits, "the first wait")
			clock.Advance(time.Hour)
			clock.fire <- clock.Now()
			recvOrFail(t, conn.expired, "the second maintenance")
			recvOrFail(t, clock.waits, "the second wait")
			cancel()
			recvOrFail(t, done, "Run to return")

			want := []string{"abort", "expire", "purge", "expire", "purge"}
			if got := log.all(); !slices.Equal(got, want) {
				t.Errorf("order = %v, want %v", got, want)
			}
			if !tasks.abortAt.Equal(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)) {
				t.Errorf("AbortActive at %v, want the clock's time at start", tasks.abortAt)
			}
		})
	}
}

// App.Start が先に回収したなら、Run は回収しない（Run の開始より先に動き出したタスクを aborted にしない）。
func TestMaintenanceRunDoesNotAbortAgainAfterTheAppRecovered(t *testing.T) {
	clock := newMaintClock()
	log := &opLog{}
	conn := &orderConn{log: log, expired: make(chan struct{}, 4)}
	tasks := &orderTasks{log: log}
	m, err := NewMaintenance(MaintenanceConfig{
		ConnRequests: conn, Tasks: tasks, Settings: newCtlSettings(), Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AbortActiveTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	recvOrFail(t, conn.expired, "the first maintenance")
	recvOrFail(t, clock.waits, "the first wait")
	cancel()
	recvOrFail(t, done, "Run to return")
	if got, want := log.all(), []string{"abort", "expire", "purge"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v (one abort only, from the app)", got, want)
	}
}
