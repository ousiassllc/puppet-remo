package guitest

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// 一覧の 1 ページの既定の件数と上限（service.TaskAdmin.List の取り決め: Limit 0 は 50）。
const (
	defaultTaskPage = 50
	maxTaskPage     = 200
)

// Tasks は gui.Tasks のインメモリ実装。本物（service.TaskAdmin）と同じ取り決めを守る:
//   - List は新しい順（作成日時の降順。同時刻は ID の降順）。Cursor は不透明な文字列で、不正な値は domain.ErrInvalidArgument。Limit 0 は 50
//   - Detail は無ければ domain.ErrTaskNotFound。Steps は Steps[id] の step_no 昇順
//   - Running は実行中・承認待ちのタスク
//   - CancelByHost は実行中・承認待ちを cancelled にして終了時刻を入れる。終了済みは現在の状態をそのまま返す（冪等）。無ければ domain.ErrTaskNotFound
type Tasks struct {
	mu    sync.Mutex
	clock *FixedClock

	Items []domain.Task
	Steps map[string][]service.TaskStep

	// ListErr・DetailErr・RunningErr・CancelErr は、あれば対応するメソッドがそのエラーを返す。
	ListErr, DetailErr, RunningErr, CancelErr error

	LastQuery   service.TaskListQuery
	CancelCalls []string
}

func (t *Tasks) sorted() []domain.Task {
	out := slices.Clone(t.Items)
	slices.SortFunc(out, func(a, b domain.Task) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out
}

func taskCursor(t domain.Task) string {
	return strconv.FormatInt(t.CreatedAt.UnixNano(), 10) + ":" + t.ID
}

func parseTaskCursor(s string) (nano int64, id string, err error) {
	n, rest, ok := strings.Cut(s, ":")
	if !ok {
		return 0, "", fmt.Errorf("cursor %q: %w", s, domain.ErrInvalidArgument)
	}
	nano, err = strconv.ParseInt(n, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("cursor %q: %w", s, domain.ErrInvalidArgument)
	}
	return nano, rest, nil
}

func (t *Tasks) List(_ context.Context, q service.TaskListQuery) (service.TaskPage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.LastQuery = q
	if t.ListErr != nil {
		return service.TaskPage{}, t.ListErr
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultTaskPage
	}
	limit = min(limit, maxTaskPage)
	rows := t.sorted()
	if q.Cursor != "" {
		nano, id, err := parseTaskCursor(q.Cursor)
		if err != nil {
			return service.TaskPage{}, err
		}
		// カーソルは「直前のページの最後の行」。それより古い行だけを返す。
		rows = slices.DeleteFunc(rows, func(r domain.Task) bool {
			n := r.CreatedAt.UnixNano()
			return n > nano || (n == nano && r.ID >= id)
		})
	}
	page := service.TaskPage{}
	if len(rows) > limit {
		rows = rows[:limit]
		page.Next = taskCursor(rows[limit-1])
	}
	page.Tasks = rows
	return page, nil
}

func (t *Tasks) find(id string) (int, bool) {
	for i := range t.Items {
		if t.Items[i].ID == id {
			return i, true
		}
	}
	return 0, false
}

func (t *Tasks) Detail(_ context.Context, id string) (service.TaskDetail, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.DetailErr != nil {
		return service.TaskDetail{}, t.DetailErr
	}
	i, ok := t.find(id)
	if !ok {
		return service.TaskDetail{}, fmt.Errorf("task %q: %w", id, domain.ErrTaskNotFound)
	}
	steps := slices.Clone(t.Steps[id])
	slices.SortFunc(steps, func(a, b service.TaskStep) int { return a.No - b.No })
	return service.TaskDetail{Task: t.Items[i], Steps: steps}, nil
}

func (t *Tasks) Running(context.Context) ([]domain.Task, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.RunningErr != nil {
		return nil, t.RunningErr
	}
	var out []domain.Task
	for _, task := range t.sorted() {
		if !task.Status.Terminal() {
			out = append(out, task)
		}
	}
	return out, nil
}

func (t *Tasks) CancelByHost(_ context.Context, id string) (domain.Task, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.CancelCalls = append(t.CancelCalls, id)
	if t.CancelErr != nil {
		return domain.Task{}, t.CancelErr
	}
	i, ok := t.find(id)
	if !ok {
		return domain.Task{}, fmt.Errorf("task %q: %w", id, domain.ErrTaskNotFound)
	}
	if t.Items[i].Status.Terminal() {
		return t.Items[i], nil
	}
	now := time.Now()
	if t.clock != nil {
		now = t.clock.Now()
	}
	t.Items[i].Status = domain.TaskCancelled
	t.Items[i].FinishedAt = &now
	return t.Items[i], nil
}

// 契約（service.TaskAdmin）と同じメソッド集合であること。
var _ service.TaskAdmin = (*Tasks)(nil)
