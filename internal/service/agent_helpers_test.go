package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/typesafe"
)

// このファイルは Agent・FastPath・TaskAdmin のテストの共通部品（agent 接頭辞）。本物の LeaseManager・Control・Approval・KillSwitch・
// SQLite の repository と、platformtest の偽物、台本つきの偽の openai.Model・typesafe.Chooser で組む。

// syncBuffer は並行に書ける bytes.Buffer（slog の取り込み用）。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// modelStepSpec は scriptedModel の 1 回分の応答。block は ctx が終わるまで待って ctx.Err() を返す（実際の HTTP の打ち切りの再現）。
type modelStepSpec struct {
	out    openai.StepOutput
	err    error
	block  bool
	before func() // 応答の前に呼ぶ（時計を進めるなど）
	panics bool   // Step が panic する
}

// scriptedModel は openai.Model の偽物。台本の順に応答し、呼び出しの開始を started へ知らせる（待ちはチャネルで行う）。
type scriptedModel struct {
	mu      sync.Mutex
	script  []modelStepSpec
	calls   int
	models  []string
	shots   []int         // 呼び出しごとの履歴の画像つきスクリーンショットの枚数
	started chan struct{} // 呼び出しの開始ごとに 1 つ送る（容量 64）
}

func newScriptedModel(script ...modelStepSpec) *scriptedModel {
	return &scriptedModel{script: script, started: make(chan struct{}, 64)}
}

func (m *scriptedModel) Step(ctx context.Context, in openai.StepInput) (openai.StepOutput, error) {
	m.mu.Lock()
	m.calls++
	m.models = append(m.models, in.Model)
	m.shots = append(m.shots, in.History.Screenshots())
	var spec modelStepSpec
	if len(m.script) == 0 {
		spec = modelStepSpec{err: errors.New("scripted model: the script is exhausted")}
	} else {
		spec, m.script = m.script[0], m.script[1:]
	}
	m.mu.Unlock()
	m.started <- struct{}{}
	if spec.before != nil {
		spec.before()
	}
	if spec.panics {
		panic("scripted model panic")
	}
	if spec.block {
		<-ctx.Done()
		return openai.StepOutput{}, ctx.Err()
	}
	return spec.out, spec.err
}

func (m *scriptedModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// waitStarted はモデルの呼び出しが始まるのを待つ。
func (m *scriptedModel) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-m.started:
	case <-time.After(approvalWait):
		t.Fatal("the model was not called")
	}
}

// step は computer_call を返す応答（callID は call-<n>）。
func step(callID string, ops ...domain.Op) modelStepSpec {
	return modelStepSpec{out: openai.StepOutput{
		CallID: callID, Actions: ops, Items: []openai.OutputItem{{Raw: []byte(`{"type":"computer_call","call_id":"` + callID + `"}`)}},
		Usage: openai.Usage{InputTokens: 100, OutputTokens: 10},
	}}
}

// finalAnswer は computer_call を返さない最終回答。
func finalAnswer(text string) modelStepSpec {
	return modelStepSpec{out: openai.StepOutput{FinalText: text, Usage: openai.Usage{InputTokens: 100, OutputTokens: 10}}}
}

// chooserStepSpec は scriptedChooser の 1 回分の応答。mutate は応答の前（問い合わせの最中）に呼ばれ、クリックの効果（画面の変化）を再現する。
type chooserStepSpec struct {
	res    typesafe.ChoiceResult
	err    error
	block  bool
	mutate func()
}

// scriptedChooser は typesafe.Chooser の偽物。受けた要求を記録する。台本が尽きたら abstain を返す。
type scriptedChooser struct {
	mu     sync.Mutex
	script []chooserStepSpec
	reqs   []typesafe.ChoiceRequest
}

func newScriptedChooser(script ...chooserStepSpec) *scriptedChooser {
	return &scriptedChooser{script: script}
}

func (c *scriptedChooser) Choose(ctx context.Context, req typesafe.ChoiceRequest) (typesafe.ChoiceResult, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	spec := chooserStepSpec{res: typesafe.ChoiceResult{Choice: typesafe.AbstainChoice}}
	if len(c.script) > 0 {
		spec, c.script = c.script[0], c.script[1:]
	}
	c.mu.Unlock()
	if spec.block {
		<-ctx.Done()
		return typesafe.ChoiceResult{}, ctx.Err()
	}
	if spec.mutate != nil {
		spec.mutate()
	}
	return spec.res, spec.err
}

func (c *scriptedChooser) Requests() []typesafe.ChoiceRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]typesafe.ChoiceRequest(nil), c.reqs...)
}

// agentEnv は Agent とその依存一式（本物のリース・Control・Approval・キルスイッチ・SQLite のタスクとクライアント）。
// 依頼元は client-a（agent_enabled）。承認モードは lease の偽クライアント（leaseApprovalEnv.clients）が持つ。
type agentEnv struct {
	*leaseApprovalEnv
	db      *repository.DB
	secrets *platformtest.FakeSecretStore
	cfg     *AgentSettingsService
	judge   *stubJudge
	model   *scriptedModel
	chooser *scriptedChooser
	agent   *AgentService
	fast    *FastPath
	logs    *syncBuffer
	toasts  <-chan Event
	by      domain.Actor // 依頼元（client-a の MCP セッション）
}

// agentOpts は newAgentEnv の設定。ゼロ値は「承認なし（auto）・高速パスなし・台本なしの偽モデル」。
type agentOpts struct {
	mode   domain.ApprovalMode
	script []modelStepSpec
	model  openai.Model // 非 nil なら script の代わりに使う
	fast   *scriptedChooser
	// 設定の上書き（ctlSettings へ生の JSON）。
	settings map[string]string
	// wrapTasks はタスクの repository を包む（更新の回数を数えるなど）。
	wrapTasks func(repository.Tasks) repository.Tasks
}

func newAgentEnv(t *testing.T, o agentOpts) *agentEnv {
	t.Helper()
	if o.mode == "" {
		o.mode = domain.ApprovalAuto
	}
	le := newLeaseApprovalEnv(t, o.mode)
	e := le.ctlEnv

	// 画像の縮小・PNG の符号化は -race で遅いので、小さなモニター（960x540。最大辺 320 で 1/3 に縮小）で動かす。
	e.capturer = platformtest.NewFakeCapturer(agentMonitor)
	e.windows.SetWindows(agentWindows()...)
	e.settings.put(settingScreenshotMaxDim, "320")

	db, err := repository.Open(context.Background(), filepath.Join(t.TempDir(), "puppet-remo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, c := range []domain.Client{
		{ID: "client-a", StableNodeID: "n-a", LoginName: "a@example.com", DisplayName: "alice", MCPEnabled: true, AgentEnabled: true, ApprovalMode: o.mode},
		{ID: "client-b", StableNodeID: "n-b", LoginName: "b@example.com", DisplayName: "bob", MCPEnabled: true, AgentEnabled: false, ApprovalMode: domain.ApprovalAlways},
	} {
		if err := db.Clients().Create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}

	ae := &agentEnv{
		leaseApprovalEnv: le, db: db, secrets: platformtest.NewFakeSecretStore(), judge: &stubJudge{v: domain.VerdictAllow},
		logs: &syncBuffer{}, by: domain.Actor{Kind: domain.ActorMCP, ClientID: "client-a", Label: "alice", SessionID: "sess-a"},
	}
	if err := ae.secrets.Set(platform.SecretOpenAI, "sk-test-placeholder"); err != nil {
		t.Fatal(err)
	}
	e.settings.put(SettingAgentEnabled, "true")
	e.settings.put(SettingAgentConsentAt, "1790000000000")
	e.settings.put(SettingAgentKeepScreenshots, "2")
	for k, v := range o.settings {
		e.settings.put(k, v)
	}
	logger := slog.New(slog.NewTextHandler(ae.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ae.cfg, err = NewAgentSettings(AgentSettingsConfig{Settings: e.settings, Secrets: ae.secrets, Audit: e.audit, Clock: e.clock, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}

	// Control は agent のアクションが Judge・承認を通る構成にする（Judge は許可する偽物）。
	e.control, err = NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
		Judge: ae.judge, Approval: le.approval, Logger: logger,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	bus := NewBus()
	var cancelToasts func()
	ae.toasts, cancelToasts = bus.Subscribe(TopicToast)
	t.Cleanup(cancelToasts)

	model := o.model
	if model == nil {
		ae.model = newScriptedModel(o.script...)
		model = ae.model
	}
	if o.fast != nil {
		ae.chooser = o.fast
		ae.fast, err = NewFastPath(FastPath{
			UI: e.ui, Control: e.control, Chooser: o.fast, Settings: ae.cfg, Disabler: ae.cfg, Audit: e.audit, Bus: bus, Logger: logger,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks := db.Tasks()
	if o.wrapTasks != nil {
		tasks = o.wrapTasks(tasks)
	}
	ae.agent, err = NewAgent(AgentServiceConfig{
		Tasks: tasks, Clients: db.Clients(), Settings: ae.cfg, Control: e.control, Leases: e.leases, Kill: e.kill,
		Approval: le.approval, Model: model, Fast: ae.fast, Audit: e.audit, Bus: bus, Clock: e.clock, IDs: &taskIDs{}, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.kill.RegisterCloser("agent", func(context.Context) error { ae.agent.CancelAll("kill_switch"); return nil })

	watchCtx, stopWatch := context.WithCancel(context.Background())
	watchDone := ae.agent.Watch(watchCtx)
	t.Cleanup(func() {
		stopWatch()
		<-watchDone
		closeCtx, cancel := context.WithTimeout(context.Background(), approvalWait)
		defer cancel()
		_ = ae.agent.Close(closeCtx)
	})
	return ae
}

// taskIDs は "task-1", "task-2", ... を返す IDGen。
type taskIDs struct {
	mu sync.Mutex
	n  int
}

func (g *taskIDs) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return "task-" + string(rune('0'+g.n))
}

// start はタスクを始める。
func (ae *agentEnv) start(t *testing.T, prompt string) domain.Task {
	t.Helper()
	task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: prompt})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return task
}

// waitTask はタスクが終了状態になり、終了処理（リースの解放を含む）が済むのを、上限つきで待つ。
func (ae *agentEnv) waitTask(t *testing.T, id string) domain.Task {
	t.Helper()
	deadline := time.Now().Add(approvalWait)
	for {
		task, err := ae.db.Tasks().Get(context.Background(), id)
		if err != nil {
			t.Fatalf("read the task: %v", err)
		}
		if task.Status.Terminal() && ae.agent.RunningTasks() == 0 {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not finish: status %s, running %d", id, task.Status, ae.agent.RunningTasks())
		}
		time.Sleep(time.Millisecond)
	}
}

// nextApprovalRequest は承認の要求が登録された合図（Bus の approval）を待って、その要求を返す。
func (ae *agentEnv) nextApprovalRequest(t *testing.T) ApprovalRequest {
	t.Helper()
	select {
	case ev := <-ae.approvals:
		data := ev.Data.(ApprovalEvent)
		for _, r := range ae.approval.Pending() {
			if r.ID == data.ID {
				return r
			}
		}
		t.Fatalf("event %q is not pending", data.ID)
	case <-time.After(approvalWait):
		t.Fatal("no approval request")
	}
	return ApprovalRequest{}
}

// auditOf は task のタスク単位・ステップの監査の行を、追記された順に返す。
func (ae *agentEnv) auditOf(taskID string) []repository.AuditEntry {
	var out []repository.AuditEntry
	for _, e := range ae.audit.Entries() {
		if e.TaskID == taskID {
			out = append(out, e)
		}
	}
	return out
}

func (ae *agentEnv) eventsOf(taskID string) []string {
	var out []string
	for _, e := range ae.auditOf(taskID) {
		out = append(out, e.Event)
	}
	return out
}

// agentMonitor は Agent のテストのモニター（物理 960x540）。縮小座標 = 物理 ÷ 3。
var agentMonitor = domain.Monitor{Index: 0, Name: "m0", Primary: true, Size: domain.Size{W: 960, H: 540}, DPI: 96}

// agentWindows は前面の front.exe（4）・normal.exe（1）・puppet-remo 自身（2）・昇格（3）が左から並ぶウィンドウ。
func agentWindows() []domain.WindowInfo {
	return []domain.WindowInfo{
		{ID: "4", Title: "front", Process: "front.exe", Rect: domain.Rect{X: 0, Y: 0, W: 480, H: 360}, Foreground: true},
		{ID: "1", Title: "normal", Process: "normal.exe", Rect: domain.Rect{X: 0, Y: 0, W: 480, H: 540}},
		{ID: "2", Title: "puppet-remo", Own: true, Rect: domain.Rect{X: 480, Y: 0, W: 240, H: 540}},
		{ID: "3", Title: "admin", Elevated: true, Rect: domain.Rect{X: 720, Y: 0, W: 240, H: 540}},
	}
}

var (
	// 台本で使う操作。クリックは前面の front.exe（縮小 (20,20) = 物理 (60,60)）、type・key は前面のウィンドウへ。
	agentClick = domain.Click{At: &domain.Point{X: 20, Y: 20}, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}
	agentType  = domain.TypeText{Text: "hello-secret-body"}
	agentKey   = domain.KeyPress{Keys: domain.KeyCombo{Key: "enter"}}
	// ownWindowClick は puppet-remo 自身のウィンドウ（id 2。物理 (480,0)-(720,540)）へのクリック（InputGuard が own_window で拒否）。
	ownWindowClick = domain.Click{At: &domain.Point{X: 200, Y: 20}, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}
)
