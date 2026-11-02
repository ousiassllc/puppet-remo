package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// agentCall は fakeAgent が受けた呼び出し。
type agentCall struct {
	Method string // start | get | cancel
	By     domain.Actor
	ID     string
	In     service.StartInput
}

// fakeAgent は service.Agent の偽物。タスクは依頼したクライアントごとに持ち、他のクライアントのタスクは ErrTaskNotFound にする
// （本物の Agent と同じ規則。docs §5.6）。err を置くと、次以降の呼び出しがそのエラーで失敗する。
type fakeAgent struct {
	mu     sync.Mutex
	tasks  map[string]domain.Task
	owners map[string]string // task id → client id
	calls  []agentCall
	err    error
	next   int
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{tasks: map[string]domain.Task{}, owners: map[string]string{}}
}

// put はクライアント clientID が依頼したタスクを足す。
func (f *fakeAgent) put(clientID string, t domain.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.ClientID = clientID
	f.tasks[t.ID], f.owners[t.ID] = t, clientID
}

func (f *fakeAgent) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeAgent) recorded() []agentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentCall(nil), f.calls...)
}

func (f *fakeAgent) Start(_ context.Context, by domain.Actor, in service.StartInput) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, agentCall{Method: "start", By: by, In: in})
	if f.err != nil {
		return domain.Task{}, f.err
	}
	f.next++
	t := domain.Task{
		ID: fmt.Sprintf("task-%d", f.next), ClientID: by.ClientID, Prompt: in.Prompt, Status: domain.TaskRunning,
		MaxSteps: 50, MaxDurationSec: 600, CreatedAt: time.Date(2026, 10, 29, 3, 12, 0, 0, time.UTC),
	}
	f.tasks[t.ID], f.owners[t.ID] = t, by.ClientID
	return t, nil
}

func (f *fakeAgent) lookup(by domain.Actor, id string) (domain.Task, error) {
	if f.err != nil {
		return domain.Task{}, f.err
	}
	if t, ok := f.tasks[id]; ok && f.owners[id] == by.ClientID {
		return t, nil
	}
	return domain.Task{}, domain.ErrTaskNotFound
}

func (f *fakeAgent) Get(_ context.Context, by domain.Actor, id string) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, agentCall{Method: "get", By: by, ID: id})
	return f.lookup(by, id)
}

// Cancel は終了済みのタスクを変えない（現在の状態をそのまま返す）。
func (f *fakeAgent) Cancel(_ context.Context, by domain.Actor, id string) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, agentCall{Method: "cancel", By: by, ID: id})
	t, err := f.lookup(by, id)
	if err != nil || t.Status.Terminal() {
		return t, err
	}
	now := time.Date(2026, 10, 29, 3, 13, 0, 0, time.UTC)
	t.Status, t.FinishedAt = domain.TaskCancelled, &now
	f.tasks[id] = t
	return t, nil
}

func (f *fakeAgent) CancelAll(string) {}
