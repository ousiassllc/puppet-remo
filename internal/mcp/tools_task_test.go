package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 内蔵エージェントのツール（FR-G02。API 仕様 §3.3「内蔵エージェント」）。Agent は偽物（fake_agent_test.go）で、
// この層の責務（引数の検査・出力の形・エラーの写し・監査・タスクのツールがリースを取らないこと）だけを確かめる。

// rawBody は structuredContent を JSON の語彙のまま（null を nil として）読む。
func (r result) rawBody(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	r.decode(t, &m)
	return m
}

var (
	taskCreated  = time.Date(2026, 10, 29, 3, 12, 0, 0, time.UTC)
	taskStarted  = taskCreated.Add(2 * time.Second)
	taskFinished = taskCreated.Add(time.Minute)
)

func TestRunTaskReturnsTaskIDAndStatus(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")

	res := callTool(t, cs, "run_task", map[string]any{"prompt": "open notepad", "max_steps": 5, "max_duration_sec": 60})
	if res.IsError {
		t.Fatal(res.text())
	}
	body := res.rawBody(t)
	if len(body) != 2 || body["task_id"] != "task-1" || body["status"] != "running" {
		t.Errorf("body = %v, want exactly {task_id, status}", body)
	}
	calls := e.agent.recorded()
	if len(calls) != 1 || calls[0].Method != "start" {
		t.Fatalf("agent calls = %+v", calls)
	}
	by, in := calls[0].By, calls[0].In
	if by.Kind != domain.ActorMCP || by.ClientID != "c1" || by.SessionID == "" {
		t.Errorf("by = %+v, want the MCP session actor of c1", by)
	}
	if in.Prompt != "open notepad" || in.MaxSteps != 5 || in.MaxDurationSec != 60 {
		t.Errorf("StartInput = %+v", in)
	}

	// 上限を省くと 0（設定値）で渡る。
	callTool(t, cs, "run_task", map[string]any{"prompt": "again"})
	if in := e.agent.recorded()[1].In; in.MaxSteps != 0 || in.MaxDurationSec != 0 {
		t.Errorf("StartInput without limits = %+v, want zero limits", in)
	}
}

func TestRunTaskRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		// code が空なら、SDK が入力スキーマで拒否する（本体に届かない）。それ以外は本体の検査で invalid_argument。
		code domain.Code
	}{
		{"prompt の欠落", map[string]any{}, ""},
		{"prompt が文字列でない", map[string]any{"prompt": 1}, ""},
		{"追加のプロパティ", map[string]any{"prompt": "x", "extra": 1}, ""},
		{"max_steps が整数でない", map[string]any{"prompt": "x", "max_steps": "5"}, ""},
		{"max_duration_sec が小数", map[string]any{"prompt": "x", "max_duration_sec": 1.5}, ""},
		{"空の prompt", map[string]any{"prompt": ""}, domain.CodeInvalidArgument},
		{"10,001 文字の prompt", map[string]any{"prompt": strings.Repeat("a", 10_001)}, domain.CodeInvalidArgument},
		{"10,001 文字（マルチバイト）の prompt", map[string]any{"prompt": strings.Repeat("あ", 10_001)}, domain.CodeInvalidArgument},
		{"max_steps が 0", map[string]any{"prompt": "x", "max_steps": 0}, domain.CodeInvalidArgument},
		{"max_steps が負", map[string]any{"prompt": "x", "max_steps": -1}, domain.CodeInvalidArgument},
		{"max_duration_sec が 0", map[string]any{"prompt": "x", "max_duration_sec": 0}, domain.CodeInvalidArgument},
		{"max_duration_sec が負", map[string]any{"prompt": "x", "max_duration_sec": -5}, domain.CodeInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			cs := e.connect("c1")
			res := callTool(t, cs, "run_task", tc.args)
			if !res.IsError {
				t.Fatalf("accepted: %s", res.text())
			}
			if tc.code != "" {
				if got := res.errCode(t); got != tc.code {
					t.Errorf("code = %s, want %s", got, tc.code)
				}
			}
			if n := len(e.agent.recorded()); n != 0 {
				t.Errorf("Agent.Start was called %d times for an invalid call", n)
			}
		})
	}
}

// 上限ちょうど（10,000 文字）は受け付ける。文字数は rune で数える。
func TestRunTaskAcceptsPromptOfExactlyTheLimit(t *testing.T) {
	for _, prompt := range []string{strings.Repeat("a", 10_000), strings.Repeat("あ", 10_000), "x"} {
		e := newEnv(t)
		cs := e.connect("c1")
		if res := callTool(t, cs, "run_task", map[string]any{"prompt": prompt}); res.IsError {
			t.Errorf("a prompt of %d characters was rejected: %s", len([]rune(prompt)), res.text())
		}
	}
}

func TestGetTaskOutputShapes(t *testing.T) {
	tests := []struct {
		name string
		task domain.Task
		want map[string]any
	}{
		{
			"実行中: 終了前の値はすべて null",
			domain.Task{
				ID: "t-run", Status: domain.TaskRunning, MaxSteps: 50, StepCount: 7, FastSteps: 4, OpenAISteps: 3,
				HandoffReason: domain.HandoffNoProgress, InputTokens: 41230, OutputTokens: 912,
				CreatedAt: taskCreated, StartedAt: new(taskStarted),
			},
			map[string]any{
				"task_id": "t-run", "status": "running", "step_count": 7.0, "fast_steps": 4.0, "openai_steps": 3.0,
				"handoff_reason": "no_progress", "max_steps": 50.0, "input_tokens": 41230.0, "output_tokens": 912.0,
				"created_at": "2026-10-29T03:12:00Z", "started_at": "2026-10-29T03:12:02Z", "finished_at": nil,
				"result_text": nil, "error_code": nil, "note": nil,
			},
		},
		{
			"承認待ち: started_at も null",
			domain.Task{ID: "t-pend", Status: domain.TaskPendingApproval, MaxSteps: 50, CreatedAt: taskCreated},
			map[string]any{
				"task_id": "t-pend", "status": "pending_approval", "step_count": 0.0, "fast_steps": 0.0, "openai_steps": 0.0,
				"handoff_reason": nil, "max_steps": 50.0, "input_tokens": 0.0, "output_tokens": 0.0,
				"created_at": "2026-10-29T03:12:00Z", "started_at": nil, "finished_at": nil,
				"result_text": nil, "error_code": nil, "note": nil,
			},
		},
		{
			"失敗: error_code だけ。result_text は付かない",
			domain.Task{
				ID: "t-fail", Status: domain.TaskFailed, MaxSteps: 50, StepCount: 2, OpenAISteps: 2, ErrorCode: "judge_denied",
				ResultText: "ignored", CreatedAt: taskCreated, StartedAt: new(taskStarted), FinishedAt: new(taskFinished),
			},
			map[string]any{
				"task_id": "t-fail", "status": "failed", "step_count": 2.0, "fast_steps": 0.0, "openai_steps": 2.0,
				"handoff_reason": nil, "max_steps": 50.0, "input_tokens": 0.0, "output_tokens": 0.0,
				"created_at": "2026-10-29T03:12:00Z", "started_at": "2026-10-29T03:12:02Z", "finished_at": "2026-10-29T03:13:00Z",
				"result_text": nil, "error_code": "judge_denied", "note": nil,
			},
		},
		{
			"リース承認の拒否: started_at は null、finished_at あり",
			domain.Task{
				ID: "t-den", Status: domain.TaskDenied, MaxSteps: 50, ErrorCode: "approval_denied",
				CreatedAt: taskCreated, FinishedAt: new(taskFinished),
			},
			map[string]any{
				"task_id": "t-den", "status": "denied", "step_count": 0.0, "fast_steps": 0.0, "openai_steps": 0.0,
				"handoff_reason": nil, "max_steps": 50.0, "input_tokens": 0.0, "output_tokens": 0.0,
				"created_at": "2026-10-29T03:12:00Z", "started_at": nil, "finished_at": "2026-10-29T03:13:00Z",
				"result_text": nil, "error_code": "approval_denied", "note": nil,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.agent.put("c1", tc.task)
			cs := e.connect("c1")

			res := callTool(t, cs, "get_task", map[string]any{"task_id": tc.task.ID})
			if res.IsError {
				t.Fatal(res.text())
			}
			got := res.rawBody(t)
			// 値が null のキーも出力に含まれる（省略しない）。
			for key, want := range tc.want {
				g, ok := got[key]
				if !ok {
					t.Errorf("key %s is missing from %v", key, got)
				} else if g != want {
					t.Errorf("%s = %#v, want %#v", key, g, want)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("keys = %v, want exactly %d", got, len(tc.want))
			}
		})
	}
}

// completed は result_text と、モデルの申告にすぎないことを述べる note を付ける（FR-G13）。
func TestGetTaskCompletedCarriesResultAndNote(t *testing.T) {
	e := newEnv(t)
	e.agent.put("c1", domain.Task{
		ID: "t-done", Status: domain.TaskCompleted, MaxSteps: 50, StepCount: 3, OpenAISteps: 3, ResultText: "done: notepad is open",
		CreatedAt: taskCreated, StartedAt: new(taskStarted), FinishedAt: new(taskFinished),
	})
	cs := e.connect("c1")

	res := callTool(t, cs, "get_task", map[string]any{"task_id": "t-done"})
	if res.IsError {
		t.Fatal(res.text())
	}
	body := res.rawBody(t)
	if body["status"] != "completed" || body["result_text"] != "done: notepad is open" || body["error_code"] != nil {
		t.Errorf("body = %v", body)
	}
	note, _ := body["note"].(string)
	for _, want := range []string{"completed", "claim", "caller's responsibility"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %q", want, note)
		}
	}
}

// 最終回答は 10,000 文字（rune）で切り詰める。
func TestGetTaskTruncatesResultText(t *testing.T) {
	e := newEnv(t)
	e.agent.put("c1", domain.Task{ID: "t-long", Status: domain.TaskCompleted, ResultText: strings.Repeat("あ", 10_050), CreatedAt: taskCreated})
	cs := e.connect("c1")

	res := callTool(t, cs, "get_task", map[string]any{"task_id": "t-long"})
	if res.IsError {
		t.Fatal(res.text())
	}
	if got, _ := res.rawBody(t)["result_text"].(string); len([]rune(got)) != 10_000 {
		t.Errorf("result_text has %d characters, want 10000", len([]rune(got)))
	}
}

func TestGetTaskTimesAreUTC(t *testing.T) {
	e := newEnv(t)
	jst := time.FixedZone("JST", 9*3600)
	e.agent.put("c1", domain.Task{ID: "t-tz", Status: domain.TaskCancelled, CreatedAt: taskCreated.In(jst), FinishedAt: new(taskFinished.In(jst))})
	cs := e.connect("c1")

	body := callTool(t, cs, "get_task", map[string]any{"task_id": "t-tz"}).rawBody(t)
	if body["created_at"] != "2026-10-29T03:12:00Z" || body["finished_at"] != "2026-10-29T03:13:00Z" {
		t.Errorf("created_at = %v, finished_at = %v, want RFC 3339 UTC", body["created_at"], body["finished_at"])
	}
}

// 他のクライアントのタスクと存在しないタスクは区別できない（task_not_found）。
func TestTaskToolsHideOtherClientsTasks(t *testing.T) {
	e := newEnv(t)
	e.agent.put("owner", domain.Task{ID: "t-owner", Status: domain.TaskRunning, CreatedAt: taskCreated})
	other := e.connect("other")

	for _, tool := range []string{"get_task", "cancel_task"} {
		for _, id := range []string{"t-owner", "t-missing"} {
			if got := callTool(t, other, tool, map[string]any{"task_id": id}).errCode(t); got != domain.CodeTaskNotFound {
				t.Errorf("%s %s by another client: code = %s, want task_not_found", tool, id, got)
			}
		}
	}
	// 他者の cancel_task が実際には何も変えない。
	owner := e.connect("owner")
	body := callTool(t, owner, "get_task", map[string]any{"task_id": "t-owner"}).rawBody(t)
	if body["status"] != "running" {
		t.Errorf("the owner's task changed to %v", body["status"])
	}
}

func TestTaskToolsRejectBadTaskID(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	for _, tool := range []string{"get_task", "cancel_task"} {
		for name, args := range map[string]map[string]any{
			"task_id の欠落":     {},
			"空の task_id":      {"task_id": ""},
			"長すぎる task_id":    {"task_id": strings.Repeat("a", 65)},
			"task_id が文字列でない": {"task_id": 7},
		} {
			if res := callTool(t, cs, tool, args); !res.IsError {
				t.Errorf("%s with %s was accepted: %s", tool, name, res.text())
			}
		}
	}
	if n := len(e.agent.recorded()); n != 0 {
		t.Errorf("Agent was called %d times for invalid calls", n)
	}
}

// Agent のエラーは既存の写し（errors.go）で、そのまま MCP のコードになる。
func TestTaskToolsMapAgentErrors(t *testing.T) {
	tests := []struct {
		err  error
		code domain.Code
	}{
		{domain.ErrAgentDisabled, domain.CodeAgentDisabled},
		{domain.ErrTaskNotFound, domain.CodeTaskNotFound},
		{domain.ErrLeaseHeldByOther, domain.CodeLeaseHeldByOther},
		{domain.ErrKillSwitchActive, domain.CodeKillSwitchActive},
		{domain.ErrInvalidArgument.WithMsg("max_steps exceeds the host's setting"), domain.CodeInvalidArgument},
		{context.DeadlineExceeded, domain.CodeInternal},
	}
	calls := map[string]map[string]any{
		"run_task":    {"prompt": "x"},
		"get_task":    {"task_id": "t1"},
		"cancel_task": {"task_id": "t1"},
	}
	for _, tc := range tests {
		for tool, args := range calls {
			t.Run(string(tc.code)+"/"+tool, func(t *testing.T) {
				e := newEnv(t)
				e.agent.setErr(tc.err)
				cs := e.connect("c1")
				res := callTool(t, cs, tool, args)
				if !res.IsError {
					t.Fatalf("succeeded: %s", res.text())
				}
				if got := res.errCode(t); got != tc.code {
					t.Errorf("code = %s, want %s", got, tc.code)
				}
			})
		}
	}
}

// 想定外のエラーは内容をクライアントへ出さない（API 仕様 §2）。
func TestTaskToolsHideInternalErrors(t *testing.T) {
	e := newEnv(t)
	e.agent.setErr(context.Canceled)
	cs := e.connect("c1")
	res := callTool(t, cs, "run_task", map[string]any{"prompt": "x"})
	if got := res.errCode(t); got != domain.CodeInternal || strings.Contains(res.text(), "canceled") {
		t.Errorf("code = %s, text = %q", got, res.text())
	}
}

// Agent が組み込まれていない（Config.Agent が nil）と、三つのツールは載ったまま agent_disabled で失敗する。
func TestTaskToolsWithoutAgentAreAgentDisabled(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Agent = nil })
	cs := e.connect("c1")
	for tool, args := range map[string]map[string]any{
		"run_task": {"prompt": "x"}, "get_task": {"task_id": "t1"}, "cancel_task": {"task_id": "t1"},
	} {
		if got := callTool(t, cs, tool, args).errCode(t); got != domain.CodeAgentDisabled {
			t.Errorf("%s: code = %s, want agent_disabled", tool, got)
		}
	}
}

// 終了済みのタスクの cancel_task は何も変えず、現在の状態を返す（冪等）。実行中なら cancelled。
func TestCancelTaskIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.agent.put("c1", domain.Task{ID: "t-done", Status: domain.TaskCompleted, CreatedAt: taskCreated})
	e.agent.put("c1", domain.Task{ID: "t-run", Status: domain.TaskRunning, CreatedAt: taskCreated})
	cs := e.connect("c1")

	for _, tc := range []struct{ id, want string }{
		{"t-done", "completed"}, {"t-done", "completed"}, {"t-run", "cancelled"}, {"t-run", "cancelled"},
	} {
		res := callTool(t, cs, "cancel_task", map[string]any{"task_id": tc.id})
		if res.IsError {
			t.Fatalf("cancel %s: %s", tc.id, res.text())
		}
		body := res.rawBody(t)
		if len(body) != 2 || body["ok"] != true || body["status"] != tc.want {
			t.Errorf("cancel %s: body = %v, want {ok: true, status: %s}", tc.id, body, tc.want)
		}
	}
}

// 指示（prompt）の本文は、監査にも運用ログにも出ない。監査には文字数だけを残す（FR-AU03）。
func TestRunTaskNeverRecordsPromptText(t *testing.T) {
	const secret = "SECRET-PROMPT-0f3a9c"
	prompt := "please log in with " + secret

	for name, setup := range map[string]func(*env){
		"成功":        func(*env) {},
		"Agent の拒否": func(e *env) { e.agent.setErr(domain.ErrAgentDisabled) },
		"検査の拒否":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			a := &auditLog{}
			var logs lockedBuffer
			e := newEnv(t, func(c *Config) {
				c.Audit = a
				c.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			})
			args := map[string]any{"prompt": prompt}
			if setup != nil {
				setup(e)
			} else {
				args["max_steps"] = 0 // 本体の検査で拒否される
			}
			cs := e.connect("c1")
			res := callTool(t, cs, "run_task", args)

			entries := a.all()
			if len(entries) != 1 {
				t.Fatalf("%d audit entries, want 1", len(entries))
			}
			raw, err := json.Marshal(entries[0])
			if err != nil {
				t.Fatal(err)
			}
			for what, text := range map[string]string{"audit entry": string(raw), "log": logs.String(), "result": res.text()} {
				if strings.Contains(text, secret) {
					t.Errorf("the prompt leaked into the %s: %s", what, text)
				}
			}
			var detail map[string]any
			if err := json.Unmarshal(entries[0].DetailJSON, &detail); err != nil {
				t.Fatal(err)
			}
			if detail["prompt_len"] != float64(len([]rune(prompt))) {
				t.Errorf("detail = %v, want prompt_len = %d", detail, len([]rune(prompt)))
			}
		})
	}
}

// 三つのツールの監査は 1 回の呼び出しにつき 1 行（tool.<名前>）。task_id は detail に入り、task_id 列には付けない
// （タスクの詳細の「ステップ」には Agent の行だけが出る）。
func TestTaskToolAuditEntries(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	e.agent.put("c1", domain.Task{ID: "t-1", Status: domain.TaskRunning, CreatedAt: taskCreated})
	cs := e.connect("c1")

	callTool(t, cs, "run_task", map[string]any{"prompt": "x", "max_steps": 3})
	callTool(t, cs, "get_task", map[string]any{"task_id": "t-1"})
	callTool(t, cs, "cancel_task", map[string]any{"task_id": "t-missing"})

	got := a.all()
	if len(got) != 3 {
		t.Fatalf("%d audit entries, want 3: %+v", len(got), got)
	}
	want := []struct {
		tool, result, code string
		detail             map[string]any
	}{
		{"run_task", "ok", "", map[string]any{"prompt_len": 1.0, "max_steps": 3.0, "task_id": "task-1"}},
		{"get_task", "ok", "", map[string]any{"task_id": "t-1"}},
		{"cancel_task", "error", "task_not_found", map[string]any{"task_id": "t-missing"}},
	}
	for i, w := range want {
		en := got[i]
		if en.Kind != "tool" || en.Event != "tool."+w.tool || en.Tool != w.tool || en.Result != w.result || en.ErrorCode != w.code {
			t.Errorf("entry %d = %+v", i, en)
		}
		if en.TaskID != "" {
			t.Errorf("entry %d has task_id column %q", i, en.TaskID)
		}
		if en.ActorType != domain.ActorMCP || en.ClientID != "c1" {
			t.Errorf("entry %d actor = %+v", i, en)
		}
		var detail map[string]any
		if err := json.Unmarshal(en.DetailJSON, &detail); err != nil {
			t.Fatal(err)
		}
		if len(detail) != len(w.detail) {
			t.Errorf("entry %d detail = %v, want %v", i, detail, w.detail)
		}
		for k, v := range w.detail {
			if detail[k] != v {
				t.Errorf("entry %d detail[%s] = %v, want %v", i, k, detail[k], v)
			}
		}
	}
}

// キルスイッチ中の run_task は kill_switch_active（denied として監査する）。
func TestRunTaskDuringKillSwitchIsAuditedAsDenied(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	e.agent.setErr(domain.ErrKillSwitchActive)
	cs := e.connect("c1")

	if got := callTool(t, cs, "run_task", map[string]any{"prompt": "x"}).errCode(t); got != domain.CodeKillSwitchActive {
		t.Fatalf("code = %s", got)
	}
	if got := a.all(); len(got) != 1 || got[0].Result != "denied" || got[0].ErrorCode != "kill_switch_active" {
		t.Errorf("audit = %+v", got)
	}
}

// タスクのツールはリースを取らず、取得しうるツールの長いタイムアウトの対象でもない（run_task は 1 秒以内に返る）。
func TestTaskToolsDoNotAcquireTheLease(t *testing.T) {
	for _, tool := range []string{"run_task", "get_task", "cancel_task"} {
		if leaseAcquiringTools[tool] {
			t.Errorf("%s is in leaseAcquiringTools", tool)
		}
	}
	e := newEnv(t)
	e.agent.put("c1", domain.Task{ID: "t-1", Status: domain.TaskRunning, CreatedAt: taskCreated})
	cs := e.connect("c1")
	callTool(t, cs, "run_task", map[string]any{"prompt": "x"})
	callTool(t, cs, "get_task", map[string]any{"task_id": "t-1"})
	callTool(t, cs, "cancel_task", map[string]any{"task_id": "t-1"})
	if _, held := e.leases.Holder(); held {
		t.Error("a task tool acquired the lease for the MCP session")
	}
}

// タスクの実行中は agent がリースを持つ。get_control_status は holder.kind = agent を返し、依頼した本人にも mine は false。
func TestGetControlStatusReportsAgentHolder(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	agent := domain.Actor{Kind: domain.ActorAgent, ClientID: "c1", Label: "agent for c1", SessionID: "task-1"}
	if _, err := e.leases.Acquire(context.Background(), agent); err != nil {
		t.Fatal(err)
	}

	var body statusBody
	res := callTool(t, cs, "get_control_status", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	res.decode(t, &body)
	if body.State != stateHeld || body.Holder == nil || body.Holder.Kind != "agent" || body.Holder.TaskID != "task-1" || body.Mine {
		t.Errorf("status = %+v (holder %+v)", body, body.Holder)
	}

	// ほかのツールはリース保持者が agent の間 lease_held_by_other（holder.kind = agent）。
	blocked := callTool(t, cs, "mouse_move", map[string]any{"x": 1, "y": 2})
	if got := blocked.errCode(t); got != domain.CodeLeaseHeldByOther {
		t.Fatalf("code = %s", got)
	}
	var eb errorBody
	blocked.decode(t, &eb)
	if eb.Holder == nil || eb.Holder.Kind != "agent" || eb.Holder.TaskID != "" {
		t.Errorf("holder = %+v, want kind agent", eb.Holder)
	}
}

// run_task の window_id は StartInput.WindowID に届き、監査の detail に残る（ウィンドウのタイトルは無い）。get_task は対象を返す。
func TestRunTaskPassesWindowIDAndGetTaskShowsTarget(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	res := callTool(t, cs, "run_task", map[string]any{"prompt": "save the file", "window_id": "1001"})
	if res.IsError {
		t.Fatal(res.text())
	}
	if in := e.agent.recorded()[0].In; in.WindowID != "1001" {
		t.Errorf("StartInput.WindowID = %q, want 1001", in.WindowID)
	}
	e.agent.put("c1", domain.Task{
		ID: "t-scoped", Status: domain.TaskRunning, MaxSteps: 50, CreatedAt: taskCreated, StartedAt: new(taskStarted),
		TargetWindowID: "1001", TargetProcess: "notepad.exe", TargetPID: 4242,
	})
	body := callTool(t, cs, "get_task", map[string]any{"task_id": "t-scoped"}).rawBody(t)
	target, _ := body["target"].(map[string]any)
	if target["window_id"] != "1001" || target["process"] != "notepad.exe" || target["pid"] != float64(4242) || len(target) != 3 {
		t.Errorf("target = %v", target)
	}
}
