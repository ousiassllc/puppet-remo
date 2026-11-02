package guitest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// Tasks の偽物が service.TaskAdmin の取り決め（新しい順・cursor・Limit 0 は 50・無い id は task_not_found・中断は冪等）を守る。
func TestTasksFollowsTheTaskAdminContract(t *testing.T) {
	f := New()
	ctx := context.Background()
	for i := range 120 {
		f.Tasks.Items = append(f.Tasks.Items, domain.Task{
			ID: fmt.Sprintf("t%03d", i), Status: domain.TaskCompleted, CreatedAt: f.Clock.T.Add(-time.Duration(i) * time.Minute),
		})
	}
	f.Tasks.Items[5].Status = domain.TaskRunning

	var ids []string
	cursor, pages := "", 0
	for {
		p, err := f.Tasks.List(ctx, service.TaskListQuery{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		want := 50
		if pages == 3 {
			want = 20
		}
		if len(p.Tasks) != want {
			t.Fatalf("page %d has %d tasks, want %d", pages, len(p.Tasks), want)
		}
		for _, task := range p.Tasks {
			ids = append(ids, task.ID)
		}
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	for i, id := range ids {
		if id != fmt.Sprintf("t%03d", i) {
			t.Fatalf("ids[%d] = %s: not newest first without gaps or duplicates", i, id)
		}
	}
	if _, err := f.Tasks.List(ctx, service.TaskListQuery{Cursor: "garbage"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("invalid cursor: err = %v, want invalid_argument", err)
	}

	if _, err := f.Tasks.Detail(ctx, "nope"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Errorf("Detail(unknown) = %v, want task_not_found", err)
	}
	running, err := f.Tasks.Running(ctx)
	if err != nil || len(running) != 1 || running[0].ID != "t005" {
		t.Fatalf("Running = %+v, %v", running, err)
	}

	got, err := f.Tasks.CancelByHost(ctx, "t005")
	if err != nil || got.Status != domain.TaskCancelled || got.FinishedAt == nil {
		t.Fatalf("CancelByHost = %+v, %v", got, err)
	}
	again, err := f.Tasks.CancelByHost(ctx, "t005")
	if err != nil || again.Status != domain.TaskCancelled {
		t.Errorf("second cancel = %+v, %v", again, err)
	}
	done, err := f.Tasks.CancelByHost(ctx, "t006")
	if err != nil || done.Status != domain.TaskCompleted {
		t.Errorf("cancelling a finished task = %+v, %v; want the current state", done, err)
	}
	if _, err := f.Tasks.CancelByHost(ctx, "nope"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Errorf("CancelByHost(unknown) = %v, want task_not_found", err)
	}
	if run, _ := f.Tasks.Running(ctx); len(run) != 0 {
		t.Errorf("Running after the cancel = %+v, want none", run)
	}
}

func TestTasksDetailOrdersSteps(t *testing.T) {
	f := New()
	f.Tasks.Items = []domain.Task{{ID: "t1", Status: domain.TaskCompleted}}
	f.Tasks.Steps = map[string][]service.TaskStep{"t1": {{No: 2}, {No: 1}, {No: 3}}}
	d, err := f.Tasks.Detail(context.Background(), "t1")
	if err != nil || len(d.Steps) != 3 || d.Steps[0].No != 1 || d.Steps[2].No != 3 {
		t.Errorf("Detail = %+v, %v", d, err)
	}
}
