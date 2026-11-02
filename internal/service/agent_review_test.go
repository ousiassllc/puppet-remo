package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/typesafe"
)

// このファイルは Agent・FastPath のレビュー指摘（リースの喪失・終了処理の順序・同意の取り消し・Start の競合・401・中断の待ち・
// 時間の上限とモデル呼び出し・ステップ番号）のテスト。待ちはすべてチャネルと上限つきの条件で行い、固定時間の sleep は使わない。

// ---- リースの喪失は Watch の有無によらず cancelled（FR-L08） ----

func TestAgentLeaseLossIsCancelledWithAndWithoutWatch(t *testing.T) {
	tests := []struct {
		name string
		// lose はモデルの応答の直前に呼ばれ、タスクのリースを失わせる。
		lose        func(ae *agentEnv)
		wantReason  []string // 許される reason（Watch があると ctx の取り消しの理由が先に入りうる）
		noWatchWant string   // Watch が無いときの reason（Do の失敗から決まる）
	}{
		{"idle timeout release", func(ae *agentEnv) {
			ae.clock.Advance(6 * time.Minute)
			ae.timers.FireAll() // アイドルタイムアウトで解放される（ErrLeaseRequired）
		}, []string{"lease_idle_timeout", "lease_lost"}, "lease_lost"},
		{"revoked and then another session acquires", func(ae *agentEnv) {
			if err := ae.leases.Revoke(HostActor); err != nil {
				t.Error(err)
			}
			if _, err := ae.leases.Acquire(context.Background(), ctlAlice); err != nil { // 他者が取得（ErrLeaseHeldByOther）
				t.Error(err)
			}
		}, []string{"lease_revoked", "lease_lost"}, "lease_lost"},
		{"revoked and nobody acquires", func(ae *agentEnv) {
			if err := ae.leases.Revoke(HostActor); err != nil {
				t.Error(err)
			}
		}, []string{"lease_revoked"}, "lease_revoked"},
	}
	for _, tt := range tests {
		for _, watch := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s (watch=%v)", tt.name, watch), func(t *testing.T) {
				var ae *agentEnv
				first := step("c1", agentClick)
				first.before = func() { tt.lose(ae) }
				ae = newAgentEnv(t, agentOpts{noWatch: !watch, script: []modelStepSpec{first, finalAnswer("never")}})
				task := ae.start(t, "p")
				got := ae.waitTask(t, task.ID)
				if got.Status != domain.TaskCancelled || got.ErrorCode != "" {
					t.Fatalf("task = %s / %q, want cancelled without an error_code", got.Status, got.ErrorCode)
				}
				if hasInputCall(ae.ctlEnv, "Click") {
					t.Error("the action was executed without the lease")
				}
				if ae.model.Calls() != 1 {
					t.Errorf("model calls = %d, want 1", ae.model.Calls())
				}
				end := lastEntry(t, ae, task.ID, "agent.task_end")
				if end.Result != auditOK || end.ErrorCode != "" {
					t.Errorf("task_end = result %q error_code %q, want ok without an error_code (not denied)", end.Result, end.ErrorCode)
				}
				want := tt.wantReason
				if !watch {
					want = []string{tt.noWatchWant}
				}
				ok := false
				for _, r := range want {
					ok = ok || strings.Contains(string(end.DetailJSON), `"reason":"`+r+`"`)
				}
				if !ok {
					t.Errorf("task_end = %s, want a reason in %v", end.DetailJSON, want)
				}
			})
		}
	}
}

// ---- 終了処理の順序: リースは保存・監査より先に解放する ----

// gateTasks は終了状態の Update を、gate が開くまで止める（保存が遅いストアの再現）。
type gateTasks struct {
	repository.Tasks
	entered chan struct{}
	gate    chan struct{}
}

func (g *gateTasks) Update(ctx context.Context, t domain.Task) error {
	if t.Status.Terminal() {
		g.entered <- struct{}{}
		<-g.gate
	}
	return g.Tasks.Update(ctx, t)
}

func TestAgentReleasesTheLeaseBeforePersistingTheEnd(t *testing.T) {
	g := &gateTasks{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	var once sync.Once
	open := func() { once.Do(func() { close(g.gate) }) }
	t.Cleanup(open)
	ae := newAgentEnv(t, agentOpts{
		script:    []modelStepSpec{finalAnswer("done")},
		wrapTasks: func(r repository.Tasks) repository.Tasks { g.Tasks = r; return g },
	})
	task := ae.start(t, "p")
	select {
	case <-g.entered: // 終了状態の保存が始まった（止まっている）
	case <-time.After(approvalWait):
		t.Fatal("the task did not reach its final update")
	}
	if _, held := ae.leases.Holder(); held {
		t.Error("the lease must be released before the end of the task is persisted")
	}
	if len(ae.input.CallsTo("ReleaseAll")) == 0 {
		t.Error("input must be released before the end of the task is persisted")
	}
	if ae.agent.RunningTasks() != 1 {
		t.Error("the run must stay registered until the end is persisted (no second task may start)")
	}
	if _, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "second"}); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("a second Start while the first one finishes: err = %v, want lease_held_by_other", err)
	}
	open()
	if got := ae.waitTask(t, task.ID); got.Status != domain.TaskCompleted {
		t.Fatalf("task = %s", got.Status)
	}
}

// ---- 高速パス: 同意の取り消しは次のステップから効く ----

func TestFastPathConsentWithdrawnMidTaskHandsOverBeforeTheNextChoose(t *testing.T) {
	tests := []struct {
		name     string
		withdraw func(ae *agentEnv)
	}{
		{"consent removed", func(ae *agentEnv) { ae.settings.put(SettingFastpathConsentAt, "null") }},
		{"disabled", func(ae *agentEnv) { ae.settings.put(SettingFastpathEnabled, "false") }},
		{"key deleted", func(ae *agentEnv) {
			if err := ae.secrets.Delete(platform.SecretTypeSafe); err != nil {
				t.Error(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ae *agentEnv
			chooser := newScriptedChooser(
				chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h1")(); tt.withdraw(ae) }},
				chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)}, // 呼ばれてはならない
			)
			ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, nil)
			got := ae.waitTask(t, ae.start(t, "p").ID)
			if got.Status != domain.TaskCompleted || got.HandoffReason != domain.HandoffChooserUnavailable || got.FastSteps != 1 {
				t.Fatalf("task = %s handoff %q fast %d, want completed / chooser_unavailable / 1", got.Status, got.HandoffReason, got.FastSteps)
			}
			if n := len(chooser.Requests()); n != 1 {
				t.Errorf("TypeSafe was called %d times, want 1 (nothing after the withdrawal)", n)
			}
			rows := fastRows(ae, got.ID)
			last := rows[len(rows)-1]
			if last["handoff_reason"] != "chooser_unavailable" || last["pass"] != true || last["_step"] != 1.0 {
				t.Errorf("handoff row = %v", last)
			}
		})
	}
}

// ---- 高速パスの引き継ぎの行は、最後の観測の候補数を持つ（FR-G23） ----

func TestFastPathNoProgressAndStepLimitRowsCarryTheLastCounts(t *testing.T) {
	t.Run("no_progress", func(t *testing.T) {
		chooser := newScriptedChooser(
			chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)}, chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
			chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
		)
		ae := newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, nil)
		got := ae.waitTask(t, ae.start(t, "p").ID)
		rows := fastRows(ae, got.ID)
		last := rows[len(rows)-1]
		if got.HandoffReason != domain.HandoffNoProgress || last["handoff_reason"] != "no_progress" || last["candidate_count"] != 3.0 || last["dropped_count"] != 0.0 {
			t.Fatalf("handoff %q, last row = %v", got.HandoffReason, last)
		}
		if last["_step"] != 3.0 || last["pass"] != true {
			t.Errorf("the pass row must use the last executed step number (3) and pass = true: %v", last)
		}
	})
	t.Run("fast_step_limit", func(t *testing.T) {
		var ae *agentEnv
		chooser := newScriptedChooser(
			chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h1")() }},
			chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h2")() }},
		)
		ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, map[string]string{SettingFastpathMaxSteps: "2"})
		got := ae.waitTask(t, ae.start(t, "p").ID)
		rows := fastRows(ae, got.ID)
		last := rows[len(rows)-1]
		if got.HandoffReason != domain.HandoffFastStepLimit || last["candidate_count"] != 3.0 || last["dropped_count"] != 0.0 {
			t.Fatalf("handoff %q, last row = %v", got.HandoffReason, last)
		}
	})
}

// 行動しないパス（引き継ぎ）の行は、次に実行する OpenAI のステップと step_no が重ならない。
func TestFastPathPassRowsDoNotCollideWithExecutedSteps(t *testing.T) {
	var ae *agentEnv
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(typesafe.ReobserveChoice, 0.9, 0)},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h1")() }},
		chooserStepSpec{res: choice(typesafe.AbstainChoice, 1, 0)},
	)
	ae = newFastEnv(t, chooser, []modelStepSpec{step("c1", agentClick), finalAnswer("ok")}, nil)
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.StepCount != 3 || got.FastSteps != 1 || got.OpenAISteps != 2 {
		t.Fatalf("steps = %d (fast %d, openai %d)", got.StepCount, got.FastSteps, got.OpenAISteps)
	}
	seen := map[int]string{}
	for _, e := range ae.auditOf(got.ID) {
		if e.Event != "agent.step" || e.Tool == "" { // 実行したステップ（アクション）だけ
			continue
		}
		n := 0
		if e.StepNo != nil {
			n = *e.StepNo
		}
		if prev, dup := seen[n]; dup {
			t.Errorf("step %d is used by %s and %s", n, prev, e.Tool)
		}
		seen[n] = e.Tool
	}
	if len(seen) != 2 || seen[1] == "" || seen[2] == "" {
		t.Errorf("executed steps = %v, want steps 1 and 2", seen)
	}
	rows := fastRows(ae, got.ID)
	if first := rows[0]; first["pass"] != true || first["_step"] != nil {
		t.Errorf("the reobserve before any action has no step_no: %v", first)
	}
}

// ---- Get / Cancel は依頼元が空の by では何も見えない ----

func TestAgentGetAndCancelRefuseAnEmptyClient(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{{block: true}}})
	task := ae.start(t, "p")
	ae.model.waitStarted(t)
	anon := domain.Actor{Kind: domain.ActorMCP, SessionID: "s"}
	if _, err := ae.agent.Get(context.Background(), anon, task.ID); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Errorf("Get with an empty client: err = %v", err)
	}
	if _, err := ae.agent.Cancel(context.Background(), anon, task.ID); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Errorf("Cancel with an empty client: err = %v", err)
	}
	if got, err := ae.agent.Get(context.Background(), ae.by, task.ID); err != nil || got.Status != domain.TaskRunning {
		t.Fatalf("the task must be untouched: %+v, %v", got, err)
	}
	// ホストの中断の入口は残る。
	if got, err := ae.agent.CancelByHost(context.Background(), task.ID); err != nil || got.Status != domain.TaskCancelled {
		t.Errorf("CancelByHost = %+v, %v", got, err)
	}
	ae.waitTask(t, task.ID)
}

// ---- Start: 条件の検査と登録の間の設定変更を取りこぼさない ----

// holderHook は Holder（reserve が登録の前に呼ぶ）の 1 回目に hook を呼ぶ。検査（checkConditions）と登録の間の割り込みを再現する。
type holderHook struct {
	LeaseManager
	once sync.Once
	hook func()
}

func (h *holderHook) Holder() (Holder, bool) {
	h.once.Do(h.hook)
	return h.LeaseManager.Holder()
}

func TestAgentStartRechecksTheConditionsAfterReserving(t *testing.T) {
	tests := []struct {
		name string
		flip func(ae *agentEnv)
	}{
		{"agent disabled", func(ae *agentEnv) { ae.settings.put(SettingAgentEnabled, "false") }},
		{"consent removed", func(ae *agentEnv) { ae.settings.put(SettingAgentConsentAt, "null") }},
		{"client agent_enabled turned off", func(ae *agentEnv) {
			off := false
			if err := ae.db.Clients().Update(context.Background(), "client-a", repository.ClientPatch{AgentEnabled: &off}); err != nil {
				t.Error(err)
			}
		}},
		{"client deleted", func(ae *agentEnv) {
			if err := ae.db.Clients().Delete(context.Background(), "client-a"); err != nil {
				t.Error(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{finalAnswer("never")}})
			ae.agent.cfg.Leases = &holderHook{LeaseManager: ae.leases, hook: func() { tt.flip(ae) }}
			_, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p"})
			if !errors.Is(err, domain.ErrAgentDisabled) {
				t.Fatalf("err = %v, want agent_disabled", err)
			}
			assertNoTaskRows(t, ae)
			if ae.agent.RunningTasks() != 0 {
				t.Error("the reservation was not cleared")
			}
			if _, held := ae.leases.Holder(); held {
				t.Error("a lease was created")
			}
			for _, e := range ae.audit.Entries() {
				if e.Event == "lease.acquire" {
					t.Errorf("lease audit entry %+v: no lease may be requested", e)
				}
			}
			if ae.model.Calls() != 0 {
				t.Error("the model was called")
			}
		})
	}
}

// ---- OpenAI の 401（NFR-S16）: GUI へ通知し、エージェントを無効にする。タスクは failed / openai_error ----

func TestAgentOpenAIUnauthorizedNotifiesAndDisablesTheAgent(t *testing.T) {
	unauthorized := fmt.Errorf("step: %w", &openai.APIError{Status: 401, Type: "invalid_request_error", Code: "invalid_api_key"})
	ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{{err: unauthorized}, finalAnswer("never")}})
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.Status != domain.TaskFailed || got.ErrorCode != domain.TaskErrorOpenAI {
		t.Fatalf("task = %s / %s, want failed / openai_error", got.Status, got.ErrorCode)
	}
	if ae.model.Calls() != 1 {
		t.Errorf("model calls = %d, want 1 (no retry)", ae.model.Calls())
	}
	select {
	case ev := <-ae.toasts:
		if tt, ok := ev.Data.(Toast); !ok || tt.Level != "error" || !strings.Contains(tt.Message, "OpenAI") {
			t.Errorf("toast = %+v", ev.Data)
		}
	default:
		t.Error("the host was not notified")
	}
	cfg, err := ae.cfg.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.ConsentAt != nil {
		t.Errorf("agent = enabled %v consent %v, want disabled without consent (FR-G14)", cfg.Enabled, cfg.ConsentAt)
	}
	var consent *repository.AuditEntry
	for _, e := range ae.audit.Entries() {
		if e.Event == "agent.consent" {
			consent = &e
		}
	}
	if consent == nil || consent.ActorType != domain.ActorSystem || consent.Result != auditError || consent.ErrorCode != AgentDisableOpenAIUnauthorized ||
		!strings.Contains(string(consent.DetailJSON), `"state":"revoked"`) {
		t.Fatalf("consent entry = %+v", consent)
	}
	if _, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "again"}); !errors.Is(err, domain.ErrAgentDisabled) {
		t.Errorf("Start after the 401: err = %v, want agent_disabled", err)
	}
	if strings.Contains(ae.logs.String(), "sk-test-placeholder") {
		t.Error("the key leaked into the log")
	}
}

// 401 以外の OpenAI の失敗では、エージェントを無効にしない。
func TestAgentOpenAIServerErrorDoesNotDisableTheAgent(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{{err: &openai.APIError{Status: 500}}}})
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.Status != domain.TaskFailed || got.ErrorCode != domain.TaskErrorOpenAI {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
	if cfg, _ := ae.cfg.Config(context.Background()); !cfg.Enabled || cfg.ConsentAt == nil {
		t.Errorf("config = %+v, want still enabled", cfg)
	}
}

// ---- cancel_task は 1 秒以内に返る: ctx を無視するモデルでも、待ちの上限で現在の状態を返す ----

// stuckModel は ctx を無視して release まで戻らない openai.Model（実際の HTTP が打ち切れないときの再現）。
type stuckModel struct {
	started chan struct{}
	release chan struct{}
}

func (m *stuckModel) Step(context.Context, openai.StepInput) (openai.StepOutput, error) {
	m.started <- struct{}{}
	<-m.release
	return openai.StepOutput{}, errors.New("released")
}

func TestAgentCancelReturnsTheCurrentStateWhenTheLoopIsStuck(t *testing.T) {
	if DefaultAgentCancelSettleWait > 500*time.Millisecond {
		t.Fatalf("DefaultAgentCancelSettleWait = %v, want at most 500ms (cancel_task returns within 1 second)", DefaultAgentCancelSettleWait)
	}
	m := &stuckModel{started: make(chan struct{}, 1), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(m.release) }) }
	t.Cleanup(release)
	ae := newAgentEnv(t, agentOpts{model: m, cancelWait: 20 * time.Millisecond})
	task := ae.start(t, "p")
	select {
	case <-m.started:
	case <-time.After(approvalWait):
		t.Fatal("the model was not called")
	}
	type result struct {
		task domain.Task
		err  error
	}
	done := make(chan result, 1)
	go func() {
		got, err := ae.agent.Cancel(context.Background(), ae.by, task.ID)
		done <- result{got, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || r.task.Status != domain.TaskRunning {
			t.Fatalf("Cancel = %+v, %v, want the current (still running) state", r.task, r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel did not return within 1 second")
	}
	release() // ループが戻れば、タスクは cancelled で終わる
	if got := ae.waitTask(t, task.ID); got.Status != domain.TaskCancelled {
		t.Fatalf("task = %s, want cancelled", got.Status)
	}
}

// ---- 実行時間の上限はモデル呼び出しも縮める（FR-G09） ----

// deadlineModel は呼び出しごとの ctx の期限までの残りを記録して inner へ渡す。
type deadlineModel struct {
	inner     *scriptedModel
	mu        sync.Mutex
	remaining []time.Duration
}

func (m *deadlineModel) Step(ctx context.Context, in openai.StepInput) (openai.StepOutput, error) {
	d, ok := ctx.Deadline()
	m.mu.Lock()
	if ok {
		m.remaining = append(m.remaining, time.Until(d))
	} else {
		m.remaining = append(m.remaining, -1)
	}
	m.mu.Unlock()
	return m.inner.Step(ctx, in)
}

func (m *deadlineModel) Remaining() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Duration(nil), m.remaining...)
}

func TestAgentModelCallIsBoundedByTheRemainingTime(t *testing.T) {
	t.Run("clamped to the remaining time", func(t *testing.T) {
		var ae *agentEnv
		first := step("c1", agentClick)
		first.before = func() { ae.clock.Advance(20 * time.Second) } // 30 秒の上限のうち 20 秒を使う
		dm := &deadlineModel{inner: newScriptedModel(first, finalAnswer("ok"))}
		ae = newAgentEnv(t, agentOpts{model: dm})
		task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p", MaxDurationSec: 30})
		if err != nil {
			t.Fatal(err)
		}
		if got := ae.waitTask(t, task.ID); got.Status != domain.TaskCompleted {
			t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
		}
		rem := dm.Remaining()
		if len(rem) != 2 {
			t.Fatalf("model calls = %d", len(rem))
		}
		// 1 回目: 残り 30 秒（60 秒より短い）、2 回目: 残り 10 秒。
		if rem[0] < 29*time.Second || rem[0] > 30*time.Second {
			t.Errorf("first call deadline in %v, want about 30s", rem[0])
		}
		if rem[1] < 9*time.Second || rem[1] > 10*time.Second {
			t.Errorf("second call deadline in %v, want about 10s (the remaining time, not 60s)", rem[1])
		}
	})
	t.Run("the model timeout is kept when the remaining time is longer", func(t *testing.T) {
		dm := &deadlineModel{inner: newScriptedModel(finalAnswer("ok"))}
		ae := newAgentEnv(t, agentOpts{model: dm, modelTimeout: 5 * time.Second})
		if got := ae.waitTask(t, ae.start(t, "p").ID); got.Status != domain.TaskCompleted {
			t.Fatalf("task = %s", got.Status)
		}
		if rem := dm.Remaining(); len(rem) != 1 || rem[0] < 4*time.Second || rem[0] > 5*time.Second {
			t.Errorf("deadline = %v, want about 5s (ModelTimeout)", rem)
		}
	})
}

// 上限の残りを使い切ったモデル呼び出しの打ち切りは failed ではなく limit_reached。
func TestAgentModelCallTimedOutByTheRemainingTimeIsLimitReached(t *testing.T) {
	var ae *agentEnv
	first := step("c1", agentClick)
	first.before = func() { ae.clock.Advance(29*time.Second + 999*time.Millisecond) } // 残り 1 ms
	ae = newAgentEnv(t, agentOpts{script: []modelStepSpec{first, {block: true}}})
	task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p", MaxDurationSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskLimitReached || got.ErrorCode != "" {
		t.Fatalf("task = %s / %q, want limit_reached", got.Status, got.ErrorCode)
	}
}

// ---- キルスイッチ: リースの承認待ちの間に発動しても、タスクは cancelled ----

func TestAgentKillSwitchWhileTheLeaseApprovalIsPending(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{mode: domain.ApprovalAlways, script: []modelStepSpec{finalAnswer("never")}})
	task := ae.start(t, "p")
	ae.nextApprovalRequest(t)
	if err := ae.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskCancelled || got.ErrorCode != "" || got.StartedAt != nil {
		t.Fatalf("task = %s / %q / started %v, want cancelled, never started", got.Status, got.ErrorCode, got.StartedAt)
	}
	if end := lastEntry(t, ae, task.ID, "agent.task_end"); !strings.Contains(string(end.DetailJSON), `"reason":"kill_switch"`) || end.Result != auditOK {
		t.Errorf("task_end = %+v (%s)", end, end.DetailJSON)
	}
	if ae.model.Calls() != 0 {
		t.Error("the model was called")
	}
	if len(ae.approval.Pending()) != 0 {
		t.Error("the lease approval is still pending")
	}
}

// セッション切断・クライアントの削除による取り下げは cancelled（lease_withdrawn）。
func TestAgentLeaseWithdrawnWhileTheApprovalIsPending(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{noWatch: true, mode: domain.ApprovalAlways, script: []modelStepSpec{finalAnswer("never")}})
	task := ae.start(t, "p")
	ae.nextApprovalRequest(t)
	ae.leases.ReleaseSession(domain.Actor{Kind: domain.ActorAgent, ClientID: "client-a", Label: "alice", SessionID: task.ID})
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskCancelled || got.ErrorCode != "" {
		t.Fatalf("task = %s / %q, want cancelled", got.Status, got.ErrorCode)
	}
	if end := lastEntry(t, ae, task.ID, "agent.task_end"); !strings.Contains(string(end.DetailJSON), `"reason":"lease_withdrawn"`) {
		t.Errorf("task_end = %s", end.DetailJSON)
	}
}

// ---- キルスイッチ: 入力のクローザーが無くても、Agent 自身が押下中の入力を解放する ----

func TestAgentReleasesHeldInputWithoutTheInputCloser(t *testing.T) {
	ae := newAgentEnv(t, agentOpts{script: []modelStepSpec{{block: true}}})
	task := ae.start(t, "p")
	ae.model.waitStarted(t)
	if err := ae.input.KeyEvent(context.Background(), domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := ae.input.Button(context.Background(), domain.ButtonLeft, domain.ButtonActionDown); err != nil {
		t.Fatal(err)
	}
	// CancelAll はキルスイッチのクローザー（input）を通らない。解放するのは Agent の終了処理（Control.ReleaseInput）だけ。
	ae.agent.CancelAll("kill_switch")
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskCancelled {
		t.Fatalf("task = %s", got.Status)
	}
	if k, b := ae.input.HeldKeys(), ae.input.HeldButtons(); len(k)+len(b) != 0 {
		t.Errorf("still held: keys %v buttons %v", k, b)
	}
}
