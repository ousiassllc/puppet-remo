package app

import (
	"context"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// noTasks はエージェント（service.Agent・service.TaskAdmin）を結線するまでの gui.Tasks。タスクが 1 件も無い状態として振る舞う
// （一覧・実行中は空、詳細・中断は domain.ErrTaskNotFound）。M4 でエージェントを結線するスライスが、service.TaskAdmin に置き換える。
type noTasks struct{}

func (noTasks) List(context.Context, service.TaskListQuery) (service.TaskPage, error) {
	return service.TaskPage{}, nil
}

func (noTasks) Detail(_ context.Context, id string) (service.TaskDetail, error) {
	return service.TaskDetail{}, fmt.Errorf("task %q: %w", id, domain.ErrTaskNotFound)
}

func (noTasks) Running(context.Context) ([]domain.Task, error) { return nil, nil }

func (noTasks) CancelByHost(_ context.Context, id string) (domain.Task, error) {
	return domain.Task{}, fmt.Errorf("task %q: %w", id, domain.ErrTaskNotFound)
}
