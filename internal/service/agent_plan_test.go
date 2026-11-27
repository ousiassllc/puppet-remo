package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは接続方式 chatgpt・chatgpt-codex の関数ツールのループ（agent_plan_run.go。FR-G25〜G30・#247）の試験。
// 偽の PlanGate・偽の Model（台本つき）で動かし、実際の ChatGPT・Codex には触れない。

// fakePlanGate は PlanGate の偽物（呼ばれた回数を数える）。
type fakePlanGate struct {
	mu                                   sync.Mutex
	reason                               string // 空なら Ready
	usageLimited, authFailures, policies int
	readyCalls                           int
}

func (g *fakePlanGate) Ready() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.readyCalls++
	return g.reason, g.reason == ""
}
func (g *fakePlanGate) MarkUsageLimited() { g.mu.Lock(); g.usageLimited++; g.mu.Unlock() }
func (g *fakePlanGate) ReportAuthFailure(context.Context) {
	g.mu.Lock()
	g.authFailures++
	g.mu.Unlock()
}
func (g *fakePlanGate) MarkPolicyDenied() { g.mu.Lock(); g.policies++; g.mu.Unlock() }
func (g *fakePlanGate) counts() (usage, auth, policy int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.usageLimited, g.authFailures, g.policies
}

// planModel は scriptedModel に、StepInput の指示と、呼び出しの時点の履歴（function_call_output・最新の画像の文）の記録、
// CloseTask（openai.TaskCloser）の記録を足した偽の関数ツールのモデル。
type planModel struct {
	*scriptedModel
	watch []string // 呼び出しごとに function_call_output を記録する call_id

	pmu    sync.Mutex
	ins    []string            // 呼び出しごとの Instructions
	outs   []map[string]string // 呼び出しごとの、watch の call_id → function_call_output
	notes  []string            // 呼び出しごとの、最新の AddUserImage の note
	shots  [][]byte            // 呼び出しごとの、最新の AddUserImage の画像
	closed []*openai.History   // CloseTask に渡された履歴
	first  *openai.History     // 最初の Step で受けた履歴
	reqTxt string              // 最初のユーザーメッセージの文
}

func newPlanModel(watch []string, script ...modelStepSpec) *planModel {
	return &planModel{scriptedModel: newScriptedModel(script...), watch: watch}
}

func (m *planModel) Step(ctx context.Context, in openai.StepInput) (openai.StepOutput, error) {
	outs := map[string]string{}
	for _, id := range m.watch {
		if o, ok := in.History.FunctionOutput(id); ok {
			outs[id] = o
		}
	}
	shot, note := in.History.LatestUserImage()
	m.pmu.Lock()
	m.ins = append(m.ins, in.Instructions)
	m.outs = append(m.outs, outs)
	m.notes = append(m.notes, note)
	m.shots = append(m.shots, shot)
	if m.first == nil {
		m.first = in.History
		m.reqTxt, _ = in.History.InitialRequest()
	}
	m.pmu.Unlock()
	return m.scriptedModel.Step(ctx, in)
}

func (m *planModel) CloseTask(h *openai.History) {
	m.pmu.Lock()
	m.closed = append(m.closed, h)
	m.pmu.Unlock()
}

func (m *planModel) closedCount() int {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	return len(m.closed)
}

// toolCall は function_call 1 件（Op が nil なら screenshot）。
func toolCall(id string, op domain.Op) openai.ToolCall {
	name := "screenshot"
	if op != nil {
		name = "left_click"
	}
	return openai.ToolCall{CallID: id, Name: name, Op: op}
}

// planStep は function_call を返す応答（出力アイテムも call ごとに足す）。
func planStep(calls ...openai.ToolCall) modelStepSpec {
	out := openai.StepOutput{Calls: calls, Usage: openai.Usage{InputTokens: 100, OutputTokens: 10}}
	for _, c := range calls {
		out.Items = append(out.Items, openai.OutputItem{Raw: []byte(`{"type":"function_call","call_id":"` + c.CallID + `"}`)})
	}
	return modelStepSpec{out: out}
}

func planSettings(conn domain.AgentConnection, more ...string) map[string]string {
	m := map[string]string{SettingAgentConnection: `"` + string(conn) + `"`}
	for i := 0; i+1 < len(more); i += 2 {
		m[more[i]] = more[i+1]
	}
	return m
}

// 関数ツールのループ: 呼び出しは 1 件ずつ Control.Do で実行され（screenshot は何もしない）、各 call に function_call_output が 1 件付き、
// 新しい画面が user の画像で続く。OpenAI キーが無くても chatgpt の接続で始められ、高速パスと API キーの Model は呼ばれない。
func TestAgentPlanLoopRunsCallsOneByOne(t *testing.T) {
	gate := &fakePlanGate{}
	pm := newPlanModel([]string{"c1", "c2", "c3"},
		planStep(toolCall("c1", nil), toolCall("c2", agentClick), toolCall("c3", agentType)),
		finalAnswer("all done"))
	chooser := newScriptedChooser()
	ae := newAgentEnv(t, agentOpts{
		noKey: true, chatgptModel: pm, chatgptGate: gate, fast: chooser,
		settings: planSettings(domain.ConnectionChatGPT, SettingFastpathEnabled, "true", SettingFastpathConsentAt, "1790000000001"),
	})
	task := ae.start(t, "open notepad")
	if task.Connection != domain.ConnectionChatGPT || task.Model != DefaultChatGPTModel {
		t.Fatalf("started = %+v", task)
	}
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskCompleted || got.ResultText != "all done" || got.Connection != domain.ConnectionChatGPT || got.Model != DefaultChatGPTModel {
		t.Fatalf("task = %+v", got)
	}
	if got.StepCount != 2 || got.OpenAISteps != 2 || got.FastSteps != 0 || got.HandoffReason != "" {
		t.Errorf("steps = %d (fast %d, openai %d, handoff %q)", got.StepCount, got.FastSteps, got.OpenAISteps, got.HandoffReason)
	}
	// Control.Do に届いたのは click と type の 2 件（screenshot は Op が nil で何も実行しない）。順に 1 件ずつ。
	var methods []string
	for _, c := range ae.input.Calls() {
		if c.Method == "MoveTo" || c.Method == "ReleaseAll" {
			continue
		}
		methods = append(methods, c.Method)
	}
	if want := []string{"Click", "Text"}; !reflect.DeepEqual(methods, want) {
		t.Errorf("input calls = %v, want %v", methods, want)
	}
	// 2 回目の呼び出し: 3 件の call のそれぞれに ok の出力が 1 件、新しい画面が user の画像（note なし）。
	if want := (map[string]string{"c1": openai.FunctionOutputOK, "c2": openai.FunctionOutputOK, "c3": openai.FunctionOutputOK}); !reflect.DeepEqual(pm.outs[1], want) {
		t.Errorf("function outputs at step 2 = %v, want %v", pm.outs[1], want)
	}
	if len(pm.shots[1]) == 0 || pm.notes[1] != "" || len(pm.outs[0]) != 0 || len(pm.shots[0]) != 0 {
		t.Errorf("user image at step 2: %d bytes, note %q; at step 1: outputs %v", len(pm.shots[1]), pm.notes[1], pm.outs[0])
	}
	// 最初の画面 + 各ステップのあとの新しい画面（最終回答のあとは撮らない）。
	if n := len(ae.capturer.CallsTo("Grab")); n != 2 {
		t.Errorf("screenshots = %d, want 2", n)
	}
	// 指示は StepInput.Instructions（desktop の関数・画面の文字を信用しない）、依頼は最初のユーザーメッセージ。
	for _, in := range pm.ins {
		if !strings.Contains(in, "desktop functions") || !strings.Contains(in, "untrusted") {
			t.Errorf("instructions = %q", in)
		}
	}
	if pm.reqTxt != "open notepad" {
		t.Errorf("first user message = %q", pm.reqTxt)
	}
	// 高速パス（Enabled も Run も）・API キーの Model は呼ばれない。OpenAI キーが無くても 401 のときのような無効化もない。
	if len(chooser.Requests()) != 0 || len(ae.ui.Calls()) != 0 || ae.model.Calls() != 0 {
		t.Errorf("chooser %d, UIA %d, api-key model %d calls; want none", len(chooser.Requests()), len(ae.ui.Calls()), ae.model.Calls())
	}
	if pm.closedCount() != 1 || pm.closed[0] != pm.first {
		t.Errorf("CloseTask calls = %d, want 1 with the task's history", pm.closedCount())
	}
	if u, a, p := gate.counts(); u+a+p != 0 {
		t.Errorf("gate marks = %d %d %d", u, a, p)
	}
	if d := detailOf(t, lastEntry(t, ae, task.ID, agentTaskStartAuditEvent)); d["connection"] != "chatgpt" {
		t.Errorf("task_start detail = %v", d)
	}
	if _, held := ae.leases.Holder(); held {
		t.Error("the lease is still held after the task")
	}
}

// Codex の接続は agent.codex.model のモデル名と CodexModel を使い、公式の Model は呼ばない。
func TestAgentPlanCodexUsesTheCodexModel(t *testing.T) {
	gate := &fakePlanGate{}
	codex := newPlanModel(nil, planStep(toolCall("c1", agentClick)), finalAnswer("ok"))
	chatgpt := newPlanModel(nil)
	ae := newAgentEnv(t, agentOpts{
		noKey: true, chatgptModel: chatgpt, codexModel: codex, chatgptGate: &fakePlanGate{reason: domain.AgentDisabledChatGPTNotSignedIn}, codexGate: gate,
		settings: planSettings(domain.ConnectionChatGPTCodex, SettingCodexModel, `"codex-test"`),
	})
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.Status != domain.TaskCompleted || got.Connection != domain.ConnectionChatGPTCodex || got.Model != "codex-test" {
		t.Fatalf("task = %+v", got)
	}
	if !reflect.DeepEqual(codex.models, []string{"codex-test", "codex-test"}) || chatgpt.Calls() != 0 || ae.model.Calls() != 0 {
		t.Errorf("models = %v, chatgpt calls %d, api-key calls %d", codex.models, chatgpt.Calls(), ae.model.Calls())
	}
}

// モデルの失敗は接続方式ごとのコードに写り、接続の状態を更新する。API キーの経路へ切り替えず、再試行せず、エージェントは無効にならない。
func TestAgentPlanModelFailureMapping(t *testing.T) {
	for _, conn := range []domain.AgentConnection{domain.ConnectionChatGPT, domain.ConnectionChatGPTCodex} {
		codex := conn == domain.ConnectionChatGPTCodex
		pick := func(chatgpt, codexCode string) string {
			if codex {
				return codexCode
			}
			return chatgpt
		}
		tests := []struct {
			name                string
			err                 error
			code                string
			usage, auth, policy int
		}{
			{name: "usage limit", err: fmt.Errorf("429: %w", openai.ErrUsageLimit),
				code: pick(domain.TaskErrorChatGPTUsageLimit, domain.TaskErrorCodexUsageLimit), usage: 1},
			{name: "unauthorized", err: fmt.Errorf("401: %w", openai.ErrPlanUnauthorized),
				code: pick(domain.TaskErrorChatGPTUnauthorized, domain.TaskErrorCodexUnauthorized), auth: 1},
			{name: "policy denied", err: fmt.Errorf("403: %w", openai.ErrPolicyDenied),
				code: pick(domain.TaskErrorChatGPT, domain.TaskErrorCodex), policy: 1},
			{name: "engine unavailable", err: fmt.Errorf("spawn: %w", openai.ErrEngineUnavailable),
				code: pick(domain.TaskErrorChatGPT, domain.TaskErrorCodexEngine)},
			{name: "unsupported action", err: fmt.Errorf("bad call: %w", domain.ErrUnsupportedAction), code: domain.TaskErrorUnsupportedAction},
			{name: "other", err: errors.New("connection reset"), code: pick(domain.TaskErrorChatGPT, domain.TaskErrorCodex)},
		}
		for _, tt := range tests {
			t.Run(string(conn)+"/"+tt.name, func(t *testing.T) {
				gate, other := &fakePlanGate{}, &fakePlanGate{}
				pm := newPlanModel(nil, modelStepSpec{err: tt.err}, finalAnswer("must not be reached (no retry)"))
				opts := agentOpts{noKey: false, settings: planSettings(conn)}
				if codex {
					opts.codexModel, opts.codexGate, opts.chatgptGate = pm, gate, other
				} else {
					opts.chatgptModel, opts.chatgptGate, opts.codexGate = pm, gate, other
				}
				ae := newAgentEnv(t, opts)
				got := ae.waitTask(t, ae.start(t, "p").ID)
				if got.Status != domain.TaskFailed || got.ErrorCode != tt.code {
					t.Fatalf("task = %s / %s, want failed / %s", got.Status, got.ErrorCode, tt.code)
				}
				if u, a, p := gate.counts(); u != tt.usage || a != tt.auth || p != tt.policy {
					t.Errorf("gate marks = usage %d auth %d policy %d, want %d %d %d", u, a, p, tt.usage, tt.auth, tt.policy)
				}
				if u, a, p := other.counts(); u+a+p != 0 {
					t.Errorf("the other connection's gate was touched: %d %d %d", u, a, p)
				}
				// 再試行しない・API キーの Model（OpenAI キーは登録済み）へ切り替えない・エージェントは有効のまま。
				if pm.Calls() != 1 || ae.model.Calls() != 0 || pm.closedCount() != 1 {
					t.Errorf("plan model calls %d, api-key model calls %d, CloseTask %d; want 1, 0, 1", pm.Calls(), ae.model.Calls(), pm.closedCount())
				}
				if c, _ := ae.cfg.Config(context.Background()); !c.Enabled || c.ConsentAt == nil || c.Connection != conn {
					t.Errorf("config after the failure = %+v, want the agent still enabled on %s", c, conn)
				}
				select {
				case ev := <-ae.toasts:
					t.Errorf("unexpected toast %+v (the API-key 401 handling must not run on the plan route)", ev)
				default:
				}
			})
		}
	}
}

// 拒否・ブロックされた呼び出しで中断し、その後ろの呼び出しは実行しない（失敗。FR-G07）。
func TestAgentPlanBlockedCallStopsTheRest(t *testing.T) {
	pm := newPlanModel(nil, planStep(toolCall("c1", agentClick), toolCall("c2", ownWindowClick), toolCall("c3", agentType)))
	ae := newAgentEnv(t, agentOpts{noKey: true, chatgptModel: pm, chatgptGate: &fakePlanGate{}, settings: planSettings(domain.ConnectionChatGPT)})
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.Status != domain.TaskFailed || got.ErrorCode != domain.TaskErrorInputBlocked {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
	if n, text := len(ae.input.CallsTo("Click")), len(ae.input.CallsTo("Text")); n != 1 || text != 0 {
		t.Errorf("clicks = %d, text = %d; want the first click only", n, text)
	}
	if pm.Calls() != 1 || pm.closedCount() != 1 {
		t.Errorf("model calls %d, CloseTask %d", pm.Calls(), pm.closedCount())
	}
}

// スコープ付きで対象グループが変わったら、実行済みの呼び出しは ok、残りは not_executed の出力を 1 件ずつ付け、note に件数を載せて
// 新しい画像から計画し直す（agent.batch_yielded が 1 行）。
func TestAgentPlanScopedYieldOutputsNotExecutedForEveryCall(t *testing.T) {
	pm := newPlanModel([]string{"c1", "c2", "c3"},
		planStep(toolCall("c1", leftClick(mp(physFileButton, boundsT))), toolCall("c2", leftClick(mp(physFileItem, boundsT))), toolCall("c3", nil)),
		finalAnswer("replanned"))
	ae := newAgentEnv(t, agentOpts{
		app: true, noKey: true, chatgptModel: pm, chatgptGate: &fakePlanGate{}, settings: planSettings(domain.ConnectionChatGPT),
	})
	got := ae.waitTask(t, ae.startScoped(t).ID)
	if got.Status != domain.TaskCompleted {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
	want := map[string]string{"c1": openai.FunctionOutputOK, "c2": openai.FunctionOutputNotExecuted, "c3": openai.FunctionOutputNotExecuted}
	if !reflect.DeepEqual(pm.outs[1], want) {
		t.Errorf("function outputs = %v, want %v", pm.outs[1], want)
	}
	if want := yieldNote(1, 2, false); pm.notes[1] != want || len(pm.shots[1]) == 0 {
		t.Errorf("note = %q, want %q (and a new image)", pm.notes[1], want)
	}
	if y := batchYields(t, ae, got.ID); len(y) != 1 || y[0]["executed"] != float64(1) || y[0]["not_executed"] != float64(1) {
		t.Errorf("batch_yielded = %v", y)
	}
	if n := len(ae.input.CallsTo("Click")); n != 1 {
		t.Errorf("clicks = %d, want 1 (the 2nd click was not injected)", n)
	}
}

// 接続方式が使えない状態では、タスクの行を作らずに agent_disabled（detail.reason に理由）で断る。
func TestAgentPlanStartRefusals(t *testing.T) {
	tests := []struct {
		name   string
		opts   agentOpts
		reason string
	}{
		{"chatgpt not signed in", agentOpts{noKey: false, chatgptModel: newPlanModel(nil), chatgptGate: &fakePlanGate{reason: domain.AgentDisabledChatGPTNotSignedIn},
			settings: planSettings(domain.ConnectionChatGPT)}, domain.AgentDisabledChatGPTNotSignedIn},
		{"chatgpt usage limited", agentOpts{chatgptModel: newPlanModel(nil), chatgptGate: &fakePlanGate{reason: domain.AgentDisabledChatGPTUsageLimited},
			settings: planSettings(domain.ConnectionChatGPT)}, domain.AgentDisabledChatGPTUsageLimited},
		{"codex engine missing", agentOpts{codexModel: newPlanModel(nil), codexGate: &fakePlanGate{reason: domain.AgentDisabledCodexEngineMissing},
			settings: planSettings(domain.ConnectionChatGPTCodex)}, domain.AgentDisabledCodexEngineMissing},
		{"api key missing", agentOpts{noKey: true, settings: planSettings(domain.ConnectionOpenAIAPIKey)}, domain.AgentDisabledOpenAIKeyMissing},
		{"no plan model wired", agentOpts{chatgptGate: &fakePlanGate{}, settings: planSettings(domain.ConnectionChatGPT)}, ""},
		{"no plan gate wired", agentOpts{chatgptModel: newPlanModel(nil), settings: planSettings(domain.ConnectionChatGPT)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t, tt.opts)
			_, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p"})
			var de *domain.Error
			if !errors.Is(err, domain.ErrAgentDisabled) || !errors.As(err, &de) {
				t.Fatalf("err = %v, want agent_disabled", err)
			}
			if reason, _ := de.Detail["reason"].(string); reason != tt.reason {
				t.Errorf("detail.reason = %q, want %q", reason, tt.reason)
			}
			page, err := ae.db.Tasks().List(context.Background(), repository.TaskQuery{})
			if err != nil || len(page.Tasks) != 0 {
				t.Errorf("tasks = %v, %v; want none", page.Tasks, err)
			}
		})
	}
}

// API キーの接続で、OpenAI キーがあっても従来どおり動く（タスクの接続方式は openai_api_key で、プランの窓口は呼ばれない）。
func TestAgentAPIKeyConnectionIgnoresThePlanGates(t *testing.T) {
	gate := &fakePlanGate{}
	ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{finalAnswer("done")}, chatgptGate: gate, codexGate: gate, chatgptModel: newPlanModel(nil)})
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.Status != domain.TaskCompleted || got.Connection != domain.ConnectionOpenAIAPIKey || got.Model != "gpt-6.1-sol" {
		t.Fatalf("task = %+v", got)
	}
	if gate.readyCalls != 0 {
		t.Errorf("Ready was called %d times on the API-key route", gate.readyCalls)
	}
}

// CancelConnection はその接続方式の実行中のタスクだけを中断して ID を返す。
func TestAgentCancelConnection(t *testing.T) {
	pm := newPlanModel(nil, modelStepSpec{block: true})
	ae := newAgentEnv(t, agentOpts{noKey: true, chatgptModel: pm, chatgptGate: &fakePlanGate{}, settings: planSettings(domain.ConnectionChatGPT)})
	if id := ae.agent.CancelConnection(domain.ConnectionChatGPT, "x"); id != "" {
		t.Fatalf("CancelConnection with no task = %q", id)
	}
	task := ae.start(t, "p")
	pm.waitStarted(t)
	if id := ae.agent.CancelConnection(domain.ConnectionChatGPTCodex, "signed_out"); id != "" {
		t.Errorf("the codex connection has no task, got %q", id)
	}
	if id := ae.agent.CancelConnection(domain.ConnectionChatGPT, "signed_out"); id != task.ID {
		t.Fatalf("CancelConnection = %q, want %q", id, task.ID)
	}
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskCancelled {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
	if d := detailOf(t, lastEntry(t, ae, task.ID, agentTaskEndAuditEvent)); d["reason"] != "signed_out" {
		t.Errorf("task_end detail = %v", d)
	}
	if pm.closedCount() != 1 {
		t.Errorf("CloseTask calls = %d, want 1 even when cancelled", pm.closedCount())
	}
}

// OpenAI キーが未登録でも、chatgpt の接続のタスクは最後まで動く（プランの経路は OpenAI キーを使わない。FR-G25）。
func TestAgentPlanDoesNotNeedTheOpenAIKey(t *testing.T) {
	pm := newPlanModel(nil, finalAnswer("done"))
	ae := newAgentEnv(t, agentOpts{noKey: true, chatgptModel: pm, chatgptGate: &fakePlanGate{}, settings: planSettings(domain.ConnectionChatGPT)})
	if reg, _ := ae.cfg.OpenAIKeyRegistered(context.Background()); reg {
		t.Fatal("the key must not be registered in this test")
	}
	if got := ae.waitTask(t, ae.start(t, "p").ID); got.Status != domain.TaskCompleted {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
}
