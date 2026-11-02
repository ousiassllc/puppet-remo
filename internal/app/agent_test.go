package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"aead.dev/minisign"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/service/agenttest"
)

// appModel は service.AgentModel の偽物。呼び出しごとに next を呼んで応答を決める。next が block を返したら ctx の終了まで待つ。
type appModel struct {
	mu      sync.Mutex
	n       int
	started chan struct{}
	next    func(n int) (agenttest.StepOutput, bool)
}

func newAppModel(next func(n int) (agenttest.StepOutput, bool)) *appModel {
	return &appModel{started: make(chan struct{}, 16), next: next}
}

func (m *appModel) Step(ctx context.Context, _ agenttest.StepInput) (agenttest.StepOutput, error) {
	m.mu.Lock()
	m.n++
	n := m.n
	m.mu.Unlock()
	m.started <- struct{}{}
	out, block := m.next(n)
	if block {
		<-ctx.Done()
		return agenttest.StepOutput{}, ctx.Err()
	}
	return out, nil
}

// waitAction は wait（0.01 秒）だけの computer_call。リースと agent.step の監査を通るが、画面は変えない。
func waitAction(callID string) agenttest.StepOutput {
	return agenttest.StepOutput{
		CallID: callID, Actions: []domain.Op{domain.Wait{Duration: 10 * time.Millisecond}},
		Items: []agenttest.OutputItem{{Raw: json.RawMessage(`{"type":"computer_call","call_id":"` + callID + `"}`)}},
		Usage: agenttest.Usage{InputTokens: 10, OutputTokens: 1},
	}
}

// agentEnv はエージェントが使える App（設定で有効・同意・OpenAI キー登録・クライアントの agent_enabled）と、その MCP クライアント。
type agentEnv struct {
	*env
	cs       *mcpsdk.ClientSession
	clientID string
}

func newAgentEnv(t *testing.T, model service.AgentModel, mutate ...func(*Config)) *agentEnv {
	t.Helper()
	e := newEnv(t, append([]func(*Config){func(c *Config) { c.OpenAIModel = model }}, mutate...)...)
	clientID, token := e.registerClient()
	agentOn := true
	if err := e.db.Clients().Update(context.Background(), clientID, repository.ClientPatch{AgentEnabled: &agentOn}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := e.app.agentCfg.SetOpenAIKey(ctx, "sk-test-dummy-presence-marker"); err != nil {
		t.Fatal(err)
	}
	if err := e.app.agentCfg.SaveAgent(ctx, service.AgentSettingsInput{
		Enabled: true, Consent: true, Model: "gpt-6.1-sol", MaxSteps: 50, MaxDurationSec: 600, KeepScreenshots: 3,
		DenyProcesses: []string{"KeePass.exe"},
	}); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	return &agentEnv{env: e, cs: e.dialMCP(token), clientID: clientID}
}

func (ae *agentEnv) runTask(prompt string) string {
	ae.t.Helper()
	res := callTool(ae.t, ae.cs, "run_task", map[string]any{"prompt": prompt})
	if res.IsError {
		ae.t.Fatalf("run_task: %+v", res.Content)
	}
	var out struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil || out.TaskID == "" {
		ae.t.Fatalf("run_task result = %s", raw)
	}
	return out.TaskID
}

func (ae *agentEnv) getTask(id string) map[string]any {
	ae.t.Helper()
	res := callTool(ae.t, ae.cs, "get_task", map[string]any{"task_id": id})
	if res.IsError {
		ae.t.Fatalf("get_task: %+v", res.Content)
	}
	return res.StructuredContent.(map[string]any)
}

func (ae *agentEnv) waitStatus(id, want string) map[string]any {
	ae.t.Helper()
	var last map[string]any
	eventually(ae.t, "task "+id+" to be "+want, func() bool {
		last = ae.getTask(id)
		return last["status"] == want
	})
	return last
}

func (ae *agentEnv) waitModel(m *appModel) {
	ae.t.Helper()
	select {
	case <-m.started:
	case <-time.After(waitLimit):
		ae.t.Fatal("the model was not called")
	}
}

// MCP の run_task で始めたタスクが、組み立てた App（本物の Agent・Control・LeaseManager・SQLite のタスクと監査）の上で最後まで進み、
// get_task・ホスト GUI の /tasks で読め、リースが解放される。
func TestRunTaskThroughTheAppCompletes(t *testing.T) {
	model := newAppModel(func(n int) (agenttest.StepOutput, bool) {
		if n == 1 {
			return waitAction("c1"), false
		}
		return agenttest.StepOutput{FinalText: "all done"}, false
	})
	ae := newAgentEnv(t, model)
	id := ae.runTask("do the thing")
	got := ae.waitStatus(id, "completed")
	if got["result_text"] != "all done" || got["step_count"] != 2.0 || got["openai_steps"] != 2.0 || got["fast_steps"] != 0.0 {
		t.Errorf("get_task = %v", got)
	}
	eventually(t, "the lease to be released", func() bool { _, held := ae.holder(); return !held })

	if err := ae.app.audit.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := ae.db.Audit().ListByTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, e := range entries {
		events = append(events, e.Event)
	}
	slices.Sort(events) // ListByTask は step_no 昇順（タスク単位の行が先頭）
	if want := []string{"agent.step", "agent.task_end", "agent.task_start"}; !slices.Equal(events, want) {
		t.Errorf("audit events = %v, want %v", events, want)
	}

	// ホスト GUI のタスク画面は、結線した TaskAdmin で動く（一覧・詳細。指示の本文は出る。ステップに入力した文字列は無い）。
	g := guiClient{t: t, h: ae.app.GUIHandler()}
	if rec := g.do(http.MethodGet, "/tasks", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "do the thing") {
		t.Errorf("GET /tasks = %d %s", rec.Code, rec.Body.String())
	}
	if rec := g.do(http.MethodGet, "/tasks/"+id, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "all done") {
		t.Errorf("GET /tasks/%s = %d", id, rec.Code)
	}
	if rec := g.do(http.MethodGet, "/tasks/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/nope = %d", rec.Code)
	}
}

// タスクの実行中は、オーバーレイに「エージェント実行中: <指示>」が出て、ダッシュボードに実行中のタスクが出て、更新の適用は
// UpdateBusyTasks で断られ、ホストの中断ボタンで cancelled になる（FR-H07・H10・G10）。
func TestRunningTaskShowsOverlayDashboardAndCanBeCancelledByTheHost(t *testing.T) {
	model := newAppModel(func(int) (agenttest.StepOutput, bool) { return agenttest.StepOutput{}, true })
	pub, _, err := minisign.GenerateKey(rand.Reader) // 更新の適用の可否（RunningTasks の結線）を見るための、テスト専用の鍵
	if err != nil {
		t.Fatal(err)
	}
	pubText, _ := pub.MarshalText()
	ae := newAgentEnv(t, model, func(c *Config) {
		c.Version = "0.1.0"
		c.Update = UpdateOptions{PublicKey: pubText, TempDir: t.TempDir()}
	})
	id := ae.runTask("tidy the desktop")
	ae.waitModel(model)

	eventually(t, "the overlay to show the agent", func() bool {
		shown, label := ae.dialogs.OverlayShown()
		return shown && strings.Contains(label, "エージェント実行中: tidy the desktop")
	})
	g := guiClient{t: t, h: ae.app.GUIHandler()}
	if rec := g.do(http.MethodGet, "/", nil); !strings.Contains(rec.Body.String(), "tidy the desktop") {
		t.Errorf("the dashboard does not show the running task: %d", rec.Code)
	}
	if h, held := ae.holder(); !held || h.Actor.Kind != domain.ActorAgent || h.Actor.SessionID != id || h.Actor.ClientID != ae.clientID {
		t.Fatalf("holder = %+v, %v", h, held)
	}
	// 他の保持者は取れない（FR-L08）。
	if res := callTool(t, ae.cs, "acquire_control"); !res.IsError {
		t.Error("acquire_control succeeded while the agent holds the lease")
	}
	var busy *service.UpdateBusyError
	if err := ae.app.updater.Apply(context.Background(), domain.ActorHost); !errors.As(err, &busy) || !slices.Contains(busy.Reasons, service.UpdateBusyTasks) {
		t.Errorf("Apply while a task runs: %v, want the %s reason", err, service.UpdateBusyTasks)
	}

	rec := g.do(http.MethodPost, "/tasks/"+id+"/cancel", nil)
	if rec.Code >= 400 {
		t.Fatalf("POST /tasks/%s/cancel = %d %s", id, rec.Code, rec.Body.String())
	}
	got := ae.waitStatus(id, "cancelled")
	if got["finished_at"] == nil {
		t.Errorf("get_task = %v", got)
	}
	eventually(t, "the overlay to hide", func() bool { shown, _ := ae.dialogs.OverlayShown(); return !shown })
	eventually(t, "the lease to be released", func() bool { _, held := ae.holder(); return !held })
	// 監査: 中断はホスト（agent.task_cancel）。
	if err := ae.app.audit.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := ae.db.Audit().ListByTask(context.Background(), id)
	found := false
	for _, e := range entries {
		if e.Event == "agent.task_cancel" && e.ActorType == domain.ActorHost {
			found = true
		}
	}
	if !found {
		t.Error("the host's cancellation was not audited")
	}
}

// ホスト GUI からの強制剥奪（POST /lease/revoke）が agent の保持者に届き、タスクが cancelled になる（FR-L06・L08）。
func TestGUIRevokeOfTheAgentLeaseCancelsTheTask(t *testing.T) {
	model := newAppModel(func(int) (agenttest.StepOutput, bool) { return agenttest.StepOutput{}, true })
	ae := newAgentEnv(t, model)
	id := ae.runTask("p")
	ae.waitModel(model)
	g := guiClient{t: t, h: ae.app.GUIHandler()}
	if rec := g.do(http.MethodPost, "/lease/revoke", nil); rec.Code != http.StatusOK {
		t.Fatalf("POST /lease/revoke = %d", rec.Code)
	}
	ae.waitStatus(id, "cancelled")
}

func TestKillSwitchCancelsTheTaskAndReleasesInput(t *testing.T) {
	model := newAppModel(func(int) (agenttest.StepOutput, bool) { return agenttest.StepOutput{}, true })
	ae := newAgentEnv(t, model)
	id := ae.runTask("p")
	ae.waitModel(model)
	if err := ae.input.Button(context.Background(), domain.ButtonLeft, domain.ButtonActionDown); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := ae.app.KillSwitch().Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(started); d > time.Second {
		t.Errorf("the kill switch took %s", d)
	}
	eventually(t, "the task to be cancelled", func() bool { // キルスイッチ中の MCP は 503。DB から読む
		task, err := ae.db.Tasks().Get(context.Background(), id)
		return err == nil && task.Status == domain.TaskCancelled
	})
	if b := ae.input.HeldButtons(); len(b) != 0 {
		t.Errorf("buttons still held: %v", b)
	}
}

// 設定画面（エージェントの無効化・OpenAI キーの削除）・クライアントの削除と無効化・agent_enabled の無効化は、実行中のタスクを中断する（FR-G10）。
func TestSettingsAndClientChangesCancelTheRunningTask(t *testing.T) {
	tests := []struct {
		name string
		do   func(t *testing.T, ae *agentEnv)
	}{
		{"agent disabled", func(t *testing.T, ae *agentEnv) {
			if err := ae.app.agentCfg.SaveAgent(context.Background(), service.AgentSettingsInput{
				Enabled: false, Model: "gpt-6.1-sol", MaxSteps: 50, MaxDurationSec: 600, KeepScreenshots: 3,
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"OpenAI key deleted", func(t *testing.T, ae *agentEnv) {
			if err := ae.app.agentCfg.DeleteOpenAIKey(context.Background()); err != nil {
				t.Fatal(err)
			}
		}},
		{"client deleted", func(t *testing.T, ae *agentEnv) {
			if err := ae.app.admin.DeleteClient(context.Background(), ae.clientID); err != nil {
				t.Fatal(err)
			}
		}},
		{"client's agent_enabled off", func(t *testing.T, ae *agentEnv) {
			off := false
			if _, err := ae.app.admin.UpdateClient(context.Background(), ae.clientID, service.ClientUpdate{AgentEnabled: &off}); err != nil {
				t.Fatal(err)
			}
		}},
		{"client's mcp_enabled off", func(t *testing.T, ae *agentEnv) {
			off := false
			if _, err := ae.app.admin.UpdateClient(context.Background(), ae.clientID, service.ClientUpdate{MCPEnabled: &off}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newAppModel(func(int) (agenttest.StepOutput, bool) { return agenttest.StepOutput{}, true })
			ae := newAgentEnv(t, model)
			// 削除・無効化でセッションが切れるので、タスクの読み取りは DB から行う。
			res := callTool(t, ae.cs, "run_task", map[string]any{"prompt": "p"})
			raw, _ := json.Marshal(res.StructuredContent)
			var out struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(raw, &out)
			ae.waitModel(model)
			started := time.Now()
			tt.do(t, ae)
			eventually(t, "the task to be cancelled", func() bool {
				task, err := ae.db.Tasks().Get(context.Background(), out.TaskID)
				return err == nil && task.Status == domain.TaskCancelled
			})
			if d := time.Since(started); d > time.Second {
				t.Errorf("cancelling took %s", d)
			}
		})
	}
}

// Shutdown は実行中のタスクを中断し、終了状態の保存・リースの解放を済ませてから監査・DB を閉じる（docs §9）。
func TestShutdownCancelsTheRunningTask(t *testing.T) {
	model := newAppModel(func(int) (agenttest.StepOutput, bool) { return agenttest.StepOutput{}, true })
	ae := newAgentEnv(t, model)
	id := ae.runTask("p")
	ae.waitModel(model)
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := ae.app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := ae.db.Tasks().Get(context.Background(), id)
	if err != nil || task.Status != domain.TaskCancelled || task.FinishedAt == nil {
		t.Fatalf("task after Shutdown = %+v, %v", task, err)
	}
	if _, held := ae.holder(); held {
		t.Error("the lease is still held after Shutdown")
	}
}

// 起動時に、前回の実行で残ったタスクを aborted にする。Agent が動く前に済む（NFR-A08）。
func TestStartAbortsTasksLeftByThePreviousRun(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	for _, st := range []domain.TaskStatus{domain.TaskPendingApproval, domain.TaskRunning} {
		err := db.Tasks().Create(ctx, domain.Task{
			ID: "old-" + string(st), ClientLabel: "x", Prompt: "p", Status: st, Model: "m", MaxSteps: 5, MaxDurationSec: 30,
			CreatedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	e := newEnvWithDB(t, db)
	e.start()
	for _, id := range []string{"old-pending_approval", "old-running"} {
		task, err := db.Tasks().Get(ctx, id)
		if err != nil || task.Status != domain.TaskAborted {
			t.Errorf("%s = %+v, %v; want aborted by the time Start returns", id, task, err)
		}
	}
}
