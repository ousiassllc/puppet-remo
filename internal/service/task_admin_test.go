package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func newTaskAdminEnv(t *testing.T, ae *agentEnv) (TaskAdmin, *ctlAudit) {
	t.Helper()
	rec := &ctlAudit{}
	ta, err := NewTaskAdmin(TaskAdminConfig{Tasks: ae.db.Tasks(), Steps: ae.db.Audit(), Cancel: ae.agent, Audit: rec})
	if err != nil {
		t.Fatal(err)
	}
	return ta, rec
}

func TestNewTaskAdminRequiresItsParts(t *testing.T) {
	if _, err := NewTaskAdmin(TaskAdminConfig{}); err == nil {
		t.Error("an empty config was accepted")
	}
}

func TestTaskAdminListDetailAndNotFound(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{})
	ta, _ := newTaskAdminEnv(t, ae)
	ctx := context.Background()
	for i := range 3 {
		err := ae.db.Tasks().Create(ctx, domain.Task{
			ID: "t" + strconv.Itoa(i), ClientID: "client-a", ClientLabel: "alice", Prompt: "p" + strconv.Itoa(i), Status: domain.TaskRunning,
			Model: "m", MaxSteps: 50, MaxDurationSec: 600, CreatedAt: ae.clock.Now().Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := ta.List(ctx, TaskListQuery{Limit: 2})
	if err != nil || len(page.Tasks) != 2 || page.Tasks[0].ID != "t2" || page.Next == "" {
		t.Fatalf("List = %+v, %v", page, err)
	}
	rest, err := ta.List(ctx, TaskListQuery{Limit: 2, Cursor: page.Next})
	if err != nil || len(rest.Tasks) != 1 || rest.Tasks[0].ID != "t0" || rest.Next != "" {
		t.Fatalf("List page 2 = %+v, %v", rest, err)
	}
	active, err := ta.Running(ctx)
	if err != nil || len(active) != 3 {
		t.Fatalf("Running = %v, %v", active, err)
	}
	if _, err := ta.Detail(ctx, "nope"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Errorf("Detail(unknown): %v", err)
	}
	if _, err := ta.CancelByHost(ctx, "nope"); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Errorf("CancelByHost(unknown): %v", err)
	}
}

// 実際にタスクを動かし、本物の監査（SQLite）に書かれたステップを詳細として読む。route・要約・引数・サムネイルの有無・AuditID。
func TestTaskAdminDetailReadsTheStepsOfARealTask(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{step("c1", agentClick, agentType), finalAnswer("done")}})
	// 監査を SQLite の Audit へ向ける（既定の ctlAudit は記録だけ）。
	realAudit, err := NewAudit(AuditConfig{Store: ae.db.Audit(), Clock: ae.clock})
	if err != nil {
		t.Fatal(err)
	}
	ae.agent.cfg.Audit = realAudit
	ae.control = rebuildControlAudit(t, ae, realAudit)
	ae.agent.cfg.Control = ae.control

	task := ae.start(t, "p")
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskCompleted {
		t.Fatalf("task = %s", got.Status)
	}
	if err := realAudit.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	ta, _ := newTaskAdminEnv(t, ae)
	d, err := ta.Detail(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != task.ID || d.Status != domain.TaskCompleted || d.ResultText != "done" {
		t.Errorf("task part = %+v", d.Task)
	}
	if len(d.Steps) != 2 {
		t.Fatalf("steps = %+v, want the two actions (task_start / task_end rows are not steps)", d.Steps)
	}
	for i, tool := range []string{"left_click", "type"} {
		s := d.Steps[i]
		if s.No != 1 || s.Kind != TaskStepAction || s.Route != "openai" || s.Event != "agent.step" || s.Result != "ok" || s.Summary != tool || s.AuditID == 0 || s.At.IsZero() {
			t.Errorf("step %d = %+v", i, s)
		}
	}
	for _, s := range d.Steps {
		for _, a := range s.Args {
			if strings.Contains(a.Value, "hello-secret-body") {
				t.Errorf("arg %s carries typed text", a.Key)
			}
		}
	}
	if args := d.Steps[1].Args; !hasArg(args, "text_len", "17") {
		t.Errorf("type args = %+v, want text_len 17 (the body is never recorded)", args)
	}
}

func rebuildControlAudit(t *testing.T, ae *agentEnv, a SyncAuditRecorder) Control {
	t.Helper()
	e := ae.ctlEnv
	c, err := NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: a, Clock: e.clock,
		Judge: ae.judge, Approval: ae.approval,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func hasArg(args []AuditArg, k, v string) bool {
	for _, a := range args {
		if a.Key == k && a.Value == v {
			return true
		}
	}
	return false
}

// 引数の表示は AuditReader と同じ伏せ字（秘密のキー・トークンの形）を通る。
func TestTaskAdminStepArgsAreRedactedLikeTheAuditReader(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{})
	ta, _ := newTaskAdminEnv(t, ae)
	ctx := context.Background()
	if err := ae.db.Tasks().Create(ctx, domain.Task{ID: "t1", ClientLabel: "x", Prompt: "p", Status: domain.TaskRunning, Model: "m", MaxSteps: 1, MaxDurationSec: 30}); err != nil {
		t.Fatal(err)
	}
	n := 1
	detail, _ := json.Marshal(map[string]any{"route": "fast", "api_key": "sk-canary", "note": "prm_abcdefgh12345678", "chosen_id": "c_1", "handoff_reason": "abstain"})
	for _, e := range []repository.AuditEntry{
		{Kind: "agent", Event: "agent.task_start", ActorType: domain.ActorAgent, TaskID: "t1", Result: "ok"},
		{Kind: "agent", Event: "agent.step", ActorType: domain.ActorAgent, TaskID: "t1", StepNo: &n, Result: "ok", DetailJSON: detail},
		{Kind: "agent", Event: "agent.judge", ActorType: domain.ActorAgent, TaskID: "t1", StepNo: &n, Tool: "left_click", Result: "denied", ErrorCode: "judge_denied",
			DetailJSON: json.RawMessage(`{"judge":"deny","judge_reason":"deny_process"}`)},
		{Kind: "agent", Event: "agent.safety_check", ActorType: domain.ActorAgent, TaskID: "t1", StepNo: &n, Result: "denied", DetailJSON: json.RawMessage(`{"result":"denied"}`)},
		{Kind: "tool", Event: "tool.get_task", ActorType: domain.ActorMCP, TaskID: "t1", Result: "ok"},
	} {
		if _, err := ae.db.Audit().Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	d, err := ta.Detail(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Steps) != 3 {
		t.Fatalf("steps = %+v", d.Steps)
	}
	s := d.Steps[0]
	if s.Route != "fast" || s.No != 1 || s.Kind != TaskStepPass || s.Summary != "chosen: c_1 → handoff to OpenAI (abstain)" {
		t.Errorf("fast step = %+v", s)
	}
	if !hasArg(s.Args, "api_key", "[REDACTED]") || !hasArg(s.Args, "note", "[REDACTED]") {
		t.Errorf("args = %+v, want secrets redacted", s.Args)
	}
	if j := d.Steps[1]; j.Kind != TaskStepJudge || j.Summary != "judge: deny (deny_process) [judge_denied]" || j.Result != "denied" {
		t.Errorf("judge step = %+v", j)
	}
	if sc := d.Steps[2]; sc.Kind != TaskStepSafetyCheck || sc.Summary != "safety check: denied" {
		t.Errorf("safety step = %+v", sc)
	}
}

func TestTaskAdminCancelByHostCancelsAndAudits(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{{block: true}}})
	ta, rec := newTaskAdminEnv(t, ae)
	task := ae.start(t, "p")
	ae.model.waitStarted(t)
	got, err := ta.CancelByHost(context.Background(), task.ID)
	if err != nil || got.Status != domain.TaskCancelled {
		t.Fatalf("CancelByHost = %+v, %v", got, err)
	}
	entries := rec.Entries()
	if len(entries) != 1 {
		t.Fatalf("audit = %+v", entries)
	}
	if e := entries[0]; e.Event != "agent.task_cancel" || e.Kind != "agent" || e.ActorType != domain.ActorHost || e.TaskID != task.ID || e.Result != auditOK {
		t.Errorf("entry = %+v", e)
	}
	ae.waitTask(t, task.ID)
	// 終了済みなら現在の状態をそのまま返す。
	if again, err := ta.CancelByHost(context.Background(), task.ID); err != nil || again.Status != domain.TaskCancelled {
		t.Errorf("CancelByHost again = %+v, %v", again, err)
	}
	if running, err := ta.Running(context.Background()); err != nil || len(running) != 0 {
		t.Errorf("Running after = %v, %v", running, err)
	}
}

// API キー（OpenAI・TypeSafe）の値は、タスクの行・監査・運用ログのどこにも出ない（NFR-S14）。
func TestAgentSecretsNeverReachRowsAuditOrLogs(t *testing.T) {
	const canary = "sk-canary-0123456789-DO-NOT-LEAK"
	const tsCanary = "ts-canary-9876543210-DO-NOT-LEAK"
	var ae *agentEnv
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h1")() }},
		chooserStepSpec{err: errors.New("unavailable")},
	)
	ae = newFastEnv(t, chooser, []modelStepSpec{
		step("c1", agentClick, agentType), {err: errors.New("upstream said no")},
	}, nil)
	for name, v := range map[string]string{"puppet-remo/openai": canary, "puppet-remo/typesafe": tsCanary} {
		if err := ae.secrets.Set(name, v); err != nil {
			t.Fatal(err)
		}
	}
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.Status != domain.TaskFailed || got.ErrorCode != domain.TaskErrorOpenAI {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
	page, err := ae.db.Tasks().List(context.Background(), repository.TaskQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	rows, _ := json.Marshal(page.Tasks)
	dump.Write(rows)
	for _, e := range ae.audit.Entries() {
		b, _ := json.Marshal(e)
		dump.Write(b)
	}
	dump.WriteString(ae.logs.String())
	for _, secret := range []string{canary, tsCanary} {
		if strings.Contains(dump.String(), secret) {
			t.Fatalf("the API key %q leaked into a task row, an audit entry or the log", secret)
		}
	}
	if !strings.Contains(ae.logs.String(), "agent task finished") {
		t.Error("the capture did not see the agent's log lines")
	}
}
