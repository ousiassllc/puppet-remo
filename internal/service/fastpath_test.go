package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/typesafe"
)

// fastID は前面ウィンドウ（id 4）の Button の候補 ID。
func fastID(label string) string { return domain.CandidateID(domain.RoleButton, label, nil) }

// focusID は focus_window の候補 ID（ウィンドウのタイトルから決まる）。
func focusID(title string) string { return domain.CandidateID(domain.RoleWindow, title, nil) }

func fastElement(name string, x, y int) domain.UIElement {
	return domain.UIElement{ControlType: 50000, Name: name, Enabled: true, Rect: domain.Rect{X: x, Y: y, W: 100, H: 40}}
}

// newFastEnv は高速パスが有効（設定・TypeSafe キー・同意）な Agent を作る。前面ウィンドウ（id 4。front.exe）のスナップショットは
// Save・Open の 2 つのボタン。
func newFastEnv(t *testing.T, chooser *scriptedChooser, script []modelStepSpec, settings map[string]string) *agentEnv {
	t.Helper()
	merged := map[string]string{SettingFastpathEnabled: "true", SettingFastpathConsentAt: "1790000000001"}
	for k, v := range settings {
		merged[k] = v
	}
	ae := newAgentEnv(t, agentOpts{script: script, fast: chooser, settings: merged})
	if err := ae.secrets.Set(platform.SecretTypeSafe, "ts-test-placeholder"); err != nil {
		t.Fatal(err)
	}
	ae.ui.SetSnapshot("4", domain.UISnapshot{Hash: "h0", Elements: []domain.UIElement{fastElement("Save", 60, 60), fastElement("Open", 200, 60)}})
	return ae
}

// changeScreen は問い合わせの最中（行動の前）に画面を変える（クリックの効果）。
func (ae *agentEnv) changeScreen(hash string) func() {
	return func() {
		ae.ui.SetSnapshot("4", domain.UISnapshot{Hash: hash, Elements: []domain.UIElement{fastElement("Save", 60, 60), fastElement("Open", 200, 60)}})
	}
}

func choice(id string, confidence, goalMaybeDone float64) typesafe.ChoiceResult {
	return typesafe.ChoiceResult{Choice: id, Confidence: confidence, GoalMaybeDone: goalMaybeDone}
}

func fastRows(ae *agentEnv, taskID string) []map[string]any {
	var out []map[string]any
	for _, e := range ae.auditOf(taskID) {
		if e.Event != "agent.step" {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal(e.DetailJSON, &d); err != nil {
			continue
		}
		if d["route"] == "fast" {
			d["_tool"], d["_result"] = e.Tool, e.Result
			if e.StepNo != nil {
				d["_step"] = float64(*e.StepNo)
			}
			out = append(out, d)
		}
	}
	return out
}

func TestFastPathClickThenHandoffToOpenAI(t *testing.T) {
	var ae *agentEnv
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0.1), mutate: func() { ae.changeScreen("h1")() }},
		chooserStepSpec{res: choice(typesafe.AbstainChoice, 0.9, 0.1)},
	)
	ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("done")}, nil)
	task := ae.start(t, "save the file")
	got := ae.waitTask(t, task.ID)

	if got.Status != domain.TaskCompleted || got.FastSteps != 1 || got.OpenAISteps != 1 || got.StepCount != 2 || got.HandoffReason != domain.HandoffAbstain {
		t.Fatalf("task = %s steps %d (fast %d, openai %d) handoff %q", got.Status, got.StepCount, got.FastSteps, got.OpenAISteps, got.HandoffReason)
	}
	// クリック: 中心 (110, 80) 物理 → 縮小 (37, 27)（縮小率 1/3）で Control に渡る。
	if clicks := ae.input.CallsTo("Click"); len(clicks) != 1 {
		t.Fatalf("clicks = %d", len(clicks))
	}
	// Jev へ送った内容: 指示・ウィンドウ・要素（ID・役割・ラベル・有効）。履歴は 1 回目は空、2 回目は 1 件の文。
	reqs := chooser.Requests()
	if len(reqs) != 2 {
		t.Fatalf("chooser calls = %d", len(reqs))
	}
	r0 := reqs[0]
	if r0.Goal != "save the file" || r0.Model != "jev-1.13" || r0.Window.Title != "front" || r0.Window.Process != "front.exe" || len(r0.History) != 0 {
		t.Errorf("first request = %+v", r0)
	}
	wantEls := []typesafe.ChoiceElement{
		{ID: fastID("Save"), Role: "button", Label: "Save", Enabled: true},
		{ID: fastID("Open"), Role: "button", Label: "Open", Enabled: true},
		{ID: focusID("normal"), Role: "window", Label: "normal", Enabled: true},
	}
	if !reflect.DeepEqual(r0.Elements, wantEls) {
		t.Errorf("elements = %+v, want %+v", r0.Elements, wantEls)
	}
	if want := []string{`Clicked the button labeled "Save".`}; !reflect.DeepEqual(reqs[1].History, want) {
		t.Errorf("history = %v, want %v", reqs[1].History, want)
	}

	rows := fastRows(ae, task.ID)
	if len(rows) != 2 {
		t.Fatalf("fast audit rows = %v", rows)
	}
	click, handoff := rows[0], rows[1]
	if click["_tool"] != "left_click" || click["candidate_count"] != 3.0 || click["dropped_count"] != 0.0 ||
		click["chosen_id"] != fastID("Save") || click["confidence"] != 0.9 || click["goal_maybe_done"] != 0.1 || click["_step"] != 1.0 {
		t.Errorf("click row = %v", click)
	}
	if _, has := click["handoff_reason"]; has {
		t.Errorf("the acting step has a handoff_reason: %v", click)
	}
	// 行動しないパスは、直前に実行したステップ（1）の番号で pass = true。次に実行する OpenAI のステップ（2）と番号が重ならない。
	if handoff["_tool"] != "" || handoff["handoff_reason"] != "abstain" || handoff["chosen_id"] != "abstain" || handoff["_step"] != 1.0 || handoff["pass"] != true {
		t.Errorf("handoff row = %v", handoff)
	}
	// 入力した文字列・要素の値・ラベルは監査に入らない。
	for _, e := range ae.auditOf(task.ID) {
		if d := string(e.DetailJSON); strings.Contains(d, "Save") || strings.Contains(d, "save the file") {
			t.Errorf("%s detail %s contains a label or the prompt", e.Event, d)
		}
	}
	if end := lastEntry(t, ae, task.ID, "agent.task_end"); !strings.Contains(string(end.DetailJSON), `"handoff_reason":"abstain"`) {
		t.Errorf("task_end = %s", end.DetailJSON)
	}
}

func TestFastPathHandoffReasons(t *testing.T) {
	unavailable := typesafe.ErrUnavailable
	tests := []struct {
		name        string
		steps       []chooserStepSpec
		prepare     func(ae *agentEnv)
		want        domain.HandoffReason
		wantCalls   int
		wantFast    int
		wantClicked bool
	}{
		{"abstain", []chooserStepSpec{{res: choice(typesafe.AbstainChoice, 1, 0)}}, nil, domain.HandoffAbstain, 1, 0, false},
		{"low confidence", []chooserStepSpec{{res: choice(fastID("Save"), 0.69, 0)}}, nil, domain.HandoffLowConfidence, 1, 0, false},
		{"confidence at the threshold acts", []chooserStepSpec{{res: choice(fastID("Save"), 0.7, 0)}, {res: choice(typesafe.AbstainChoice, 1, 0)}}, nil, domain.HandoffAbstain, 2, 1, true},
		{"goal maybe done hands over without acting", []chooserStepSpec{{res: choice(fastID("Save"), 0.99, 0.5)}}, nil, domain.HandoffGoalMaybeDone, 1, 0, false},
		{"goal maybe done below 0.5 acts", []chooserStepSpec{{res: choice(fastID("Save"), 0.99, 0.49)}, {res: choice(typesafe.AbstainChoice, 1, 0)}}, nil, domain.HandoffAbstain, 2, 1, true},
		{"goal maybe done beats abstain", []chooserStepSpec{{res: choice(typesafe.AbstainChoice, 0.1, 0.9)}}, nil, domain.HandoffGoalMaybeDone, 1, 0, false},
		{"abstain beats low confidence", []chooserStepSpec{{res: choice(typesafe.AbstainChoice, 0.1, 0)}}, nil, domain.HandoffAbstain, 1, 0, false},
		{"unknown id", []chooserStepSpec{{res: choice("c_000000000000", 0.9, 0)}}, nil, domain.HandoffInvalidChoice, 1, 0, false},
		{"empty choice", []chooserStepSpec{{res: choice("", 0.9, 0)}}, nil, domain.HandoffInvalidChoice, 1, 0, false},
		{"bad response", []chooserStepSpec{{err: fmt.Errorf("wrapped: %w", typesafe.ErrBadResponse)}}, nil, domain.HandoffInvalidChoice, 1, 0, false},
		{"confidence out of range", []chooserStepSpec{{res: choice(fastID("Save"), 1.5, 0)}}, nil, domain.HandoffInvalidChoice, 1, 0, false},
		{"goal_maybe_done out of range", []chooserStepSpec{{res: choice(fastID("Save"), 0.9, -0.1)}}, nil, domain.HandoffInvalidChoice, 1, 0, false},
		{"excluded label is not in the table", []chooserStepSpec{{res: choice(fastID("Save"), 0.9, 0)}}, func(ae *agentEnv) {
			ae.settings.put(SettingFastpathExcludedLabels, `["save"]`)
		}, domain.HandoffInvalidChoice, 1, 0, false},
		{"chooser unavailable", []chooserStepSpec{{err: unavailable}}, nil, domain.HandoffChooserUnavailable, 1, 0, false},
		{"chooser unavailable (wrapped)", []chooserStepSpec{{err: fmt.Errorf("x: %w", unavailable)}}, nil, domain.HandoffChooserUnavailable, 1, 0, false},
		{"unexpected chooser error", []chooserStepSpec{{err: errors.New("boom")}}, nil, domain.HandoffChooserUnavailable, 1, 0, false},
		{"no candidates", nil, func(ae *agentEnv) {
			ae.ui.SetSnapshot("4", domain.UISnapshot{Hash: "empty"})
			ae.windows.SetWindows(domain.WindowInfo{ID: "4", Title: "front", Process: "front.exe", Rect: domain.Rect{W: 480, H: 360}, Foreground: true})
		}, domain.HandoffNoCandidates, 0, 0, false},
		{"the host's own window is in front", nil, func(ae *agentEnv) {
			ae.windows.SetWindows(domain.WindowInfo{ID: "4", Title: "front", Process: "front.exe", Own: true, Rect: domain.Rect{W: 480, H: 360}, Foreground: true})
		}, domain.HandoffNoCandidates, 0, 0, false},
		{"the screen cannot be observed", nil, func(ae *agentEnv) { ae.ui.SetError("Snapshot", errors.New("uia is down")) }, domain.HandoffNoCandidates, 0, 0, false},
		{"reobserve twice then act", []chooserStepSpec{
			{res: choice(typesafe.ReobserveChoice, 0.9, 0)}, {res: choice(typesafe.ReobserveChoice, 0.9, 0)},
			{res: choice(fastID("Save"), 0.9, 0)}, {res: choice(typesafe.AbstainChoice, 1, 0)},
		}, nil, domain.HandoffAbstain, 4, 1, true},
		{"the third consecutive reobserve is an abstain", []chooserStepSpec{
			{res: choice(typesafe.ReobserveChoice, 0.9, 0)}, {res: choice(typesafe.ReobserveChoice, 0.9, 0)}, {res: choice(typesafe.ReobserveChoice, 0.9, 0)},
		}, nil, domain.HandoffAbstain, 3, 0, false},
		{"low-confidence reobserve is a low-confidence handoff", []chooserStepSpec{{res: choice(typesafe.ReobserveChoice, 0.1, 0)}}, nil, domain.HandoffLowConfidence, 1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ae *agentEnv
			steps := append([]chooserStepSpec(nil), tt.steps...)
			for i := range steps {
				if steps[i].res.Choice == fastID("Save") && steps[i].err == nil {
					steps[i].mutate = func() { ae.changeScreen("h1")() }
				}
			}
			chooser := newScriptedChooser(steps...)
			ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, nil)
			if tt.prepare != nil {
				tt.prepare(ae)
			}
			got := ae.waitTask(t, ae.start(t, "p").ID)
			if got.Status != domain.TaskCompleted || got.HandoffReason != tt.want {
				t.Fatalf("task = %s handoff %q, want completed %q", got.Status, got.HandoffReason, tt.want)
			}
			if n := len(chooser.Requests()); n != tt.wantCalls {
				t.Errorf("chooser calls = %d, want %d", n, tt.wantCalls)
			}
			if got.FastSteps != tt.wantFast || got.OpenAISteps != 1 || got.StepCount != tt.wantFast+1 {
				t.Errorf("steps = %d (fast %d, openai %d), want fast %d", got.StepCount, got.FastSteps, got.OpenAISteps, tt.wantFast)
			}
			if hasInputCall(ae.ctlEnv, "Click") != tt.wantClicked {
				t.Errorf("clicked = %v, want %v", !tt.wantClicked, tt.wantClicked)
			}
			// 引き継ぎの理由は、引き継いだステップの監査にちょうど 1 回入る。
			n := 0
			for _, row := range fastRows(ae, got.ID) {
				if row["handoff_reason"] == string(tt.want) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("rows with handoff_reason %s = %d, want 1", tt.want, n)
			}
		})
	}
}

func TestFastPathNoProgressAfterThreeConsecutiveSteps(t *testing.T) {
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)}, chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)}, chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
	)
	ae := newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, nil)
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.HandoffReason != domain.HandoffNoProgress || got.FastSteps != 3 || len(chooser.Requests()) != 3 {
		t.Fatalf("handoff %q fast %d chooser calls %d, want no_progress 3 3", got.HandoffReason, got.FastSteps, len(chooser.Requests()))
	}
	if len(ae.input.CallsTo("Click")) != 3 {
		t.Errorf("clicks = %d", len(ae.input.CallsTo("Click")))
	}
}

// 進捗が 1 度あれば、進捗なしの連続は数え直す。進捗はツリーのハッシュだけ、前面ウィンドウだけでも成り立つ。
func TestFastPathProgressResetsTheNoProgressCount(t *testing.T) {
	var ae *agentEnv
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h1")() }}, // 3 回目に進捗
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
		chooserStepSpec{res: choice(typesafe.AbstainChoice, 1, 0)},
	)
	ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, nil)
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.HandoffReason != domain.HandoffAbstain || got.FastSteps != 5 {
		t.Fatalf("handoff %q fast %d, want abstain 5", got.HandoffReason, got.FastSteps)
	}
}

func TestFastPathFocusWindowChangeIsProgress(t *testing.T) {
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(focusID("normal"), 0.9, 0)},
		chooserStepSpec{res: choice(typesafe.AbstainChoice, 1, 0)},
	)
	ae := newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, nil)
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.FastSteps != 1 || got.HandoffReason != domain.HandoffAbstain {
		t.Fatalf("task = %+v", got)
	}
	if calls := ae.windows.CallsTo("Focus"); len(calls) != 1 || calls[0].Args[0] != "1" {
		t.Errorf("Focus calls = %v", calls)
	}
	rows := fastRows(ae, got.ID)
	if rows[0]["_tool"] != "focus_window" {
		t.Errorf("row = %v", rows[0])
	}
	// フォーカスで前面のウィンドウの ID が変わったので、2 回目の問い合わせの履歴に 1 件の文が入る。
	if h := chooser.Requests()[1].History; len(h) != 1 || h[0] != `Brought the window "normal" to the foreground.` {
		t.Errorf("history = %v", h)
	}
}

func TestFastPathStepLimit(t *testing.T) {
	var ae *agentEnv
	hashes := []string{"h1", "h2", "h3"}
	var steps []chooserStepSpec
	for _, h := range hashes {
		steps = append(steps, chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen(h)() }})
	}
	chooser := newScriptedChooser(steps...)
	ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, map[string]string{SettingFastpathMaxSteps: "2"})
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.HandoffReason != domain.HandoffFastStepLimit || got.FastSteps != 2 || len(chooser.Requests()) != 2 {
		t.Fatalf("handoff %q fast %d chooser calls %d, want fast_step_limit 2 2", got.HandoffReason, got.FastSteps, len(chooser.Requests()))
	}
}

// 高速ステップも agent.max_steps に数え、到達したら引き継がずに limit_reached で終わる（FR-G09・G19）。
func TestFastPathStepsCountTowardsTheTaskLimit(t *testing.T) {
	var ae *agentEnv
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h1")() }},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h2")() }},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h3")() }},
	)
	ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("never")}, nil)
	task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p", MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskLimitReached || got.FastSteps != 2 || got.StepCount != 2 || got.HandoffReason != "" {
		t.Fatalf("task = %s fast %d steps %d handoff %q", got.Status, got.FastSteps, got.StepCount, got.HandoffReason)
	}
	if ae.model.Calls() != 0 {
		t.Error("the model was called after the limit")
	}
}

func TestFastPathDurationCountsTowardsTheTaskLimit(t *testing.T) {
	var ae *agentEnv
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() { ae.changeScreen("h1")(); ae.clock.Advance(31 * time.Second) }},
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0)},
	)
	ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("never")}, nil)
	task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p", MaxDurationSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	got := ae.waitTask(t, task.ID)
	if got.Status != domain.TaskLimitReached || got.FastSteps != 1 || got.HandoffReason != "" || ae.model.Calls() != 0 {
		t.Fatalf("task = %s fast %d handoff %q model calls %d", got.Status, got.FastSteps, got.HandoffReason, ae.model.Calls())
	}
}

// Control の拒否・ブロックは、OpenAI への引き継ぎで救済せず、タスクを failed で終える（handoff_reason は NULL）。FR-G07。
func TestFastPathRejectionEndsTheTaskWithoutHandoff(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(ae *agentEnv)
		wantCode string
	}{
		{"input blocked (own window under the click)", func(ae *agentEnv) {
			ae.ui.SetSnapshot("4", domain.UISnapshot{Hash: "h0", Elements: []domain.UIElement{fastElement("Save", 550, 60)}})
		}, domain.TaskErrorInputBlocked},
		{"judge denied", func(ae *agentEnv) { ae.judge.v = domain.VerdictDeny }, domain.TaskErrorJudgeDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chooser := newScriptedChooser(chooserStepSpec{res: choice(fastID("Save"), 0.99, 0)}, chooserStepSpec{res: choice(fastID("Save"), 0.99, 0)})
			ae := newFastEnv(t, chooser, []modelStepSpec{finalAnswer("never")}, nil)
			tt.prepare(ae)
			got := ae.waitTask(t, ae.start(t, "p").ID)
			if got.Status != domain.TaskFailed || got.ErrorCode != tt.wantCode || got.HandoffReason != "" {
				t.Fatalf("task = %s / %s handoff %q", got.Status, got.ErrorCode, got.HandoffReason)
			}
			if ae.model.Calls() != 0 || len(chooser.Requests()) != 1 {
				t.Errorf("model calls %d, chooser calls %d: the task must stop at the rejection", ae.model.Calls(), len(chooser.Requests()))
			}
			if hasInputCall(ae.ctlEnv, "Click") {
				t.Error("the rejected click was executed")
			}
			rows := fastRows(ae, got.ID)
			if len(rows) != 1 || rows[0]["_result"] != auditDenied || rows[0]["chosen_id"] != fastID("Save") {
				t.Errorf("fast rows = %v", rows)
			}
		})
	}
}

func TestFastPathCancelDuringTheChooserCallIsACancellation(t *testing.T) {
	chooser := newScriptedChooser(chooserStepSpec{block: true})
	ae := newFastEnv(t, chooser, []modelStepSpec{finalAnswer("never")}, nil)
	task := ae.start(t, "p")
	chooser.waitStarted(t)
	done := make(chan domain.Task, 1)
	go func() {
		got, _ := ae.agent.Cancel(context.Background(), ae.by, task.ID)
		done <- got
	}()
	select {
	case got := <-done:
		if got.Status != domain.TaskCancelled || got.HandoffReason != "" {
			t.Fatalf("task = %s handoff %q, want cancelled without handoff", got.Status, got.HandoffReason)
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel did not finish within 1 second")
	}
	if ae.model.Calls() != 0 {
		t.Error("the model was called after the cancellation")
	}
}

func TestFastPathUnauthorizedDisablesItAndHandsOver(t *testing.T) {
	chooser := newScriptedChooser(chooserStepSpec{err: typesafe.ErrUnauthorized})
	ae := newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok"), finalAnswer("ok again")}, nil)
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.Status != domain.TaskCompleted || got.HandoffReason != domain.HandoffChooserUnavailable {
		t.Fatalf("task = %s handoff %q, want completed chooser_unavailable (the task must not fail)", got.Status, got.HandoffReason)
	}
	select {
	case ev := <-ae.toasts:
		if tt, ok := ev.Data.(Toast); !ok || tt.Level != "error" || !strings.Contains(tt.Message, "TypeSafe") {
			t.Errorf("toast = %+v", ev.Data)
		}
	default:
		t.Error("the host was not notified")
	}
	if v, _, _ := ae.settings.Get(context.Background(), SettingFastpathEnabled); string(v) != "false" {
		t.Errorf("agent.fastpath.enabled = %s, want false", v)
	}
	var disable *repository.AuditEntry
	for _, e := range ae.audit.Entries() {
		if e.Event == "agent.fastpath.disable" {
			disable = &e
		}
	}
	if disable == nil || disable.ActorType != domain.ActorSystem || disable.Result != auditError || disable.ErrorCode != FastpathDisableUnauthorized {
		t.Fatalf("disable entry = %+v", disable)
	}
	// 以後のタスクは高速パスを使わない（TypeSafe を呼ばない）。
	again := ae.waitTask(t, ae.start(t, "again").ID)
	if again.Status != domain.TaskCompleted || again.HandoffReason != "" || again.FastSteps != 0 || len(chooser.Requests()) != 1 {
		t.Errorf("the next task = %s/%s handoff %q fast %d, chooser calls %d: the fast path must stay off", again.Status, again.ErrorCode, again.HandoffReason, again.FastSteps, len(chooser.Requests()))
	}
}

func TestFastPathIsSkippedSilently(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		prepare  func(ae *agentEnv)
	}{
		{"disabled", map[string]string{SettingFastpathEnabled: "false"}, nil},
		{"no consent", map[string]string{SettingFastpathConsentAt: "null"}, nil},
		{"no TypeSafe key", nil, func(ae *agentEnv) {
			if err := ae.secrets.Delete(platform.SecretTypeSafe); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chooser := newScriptedChooser(chooserStepSpec{res: choice(fastID("Save"), 0.99, 0)})
			ae := newFastEnv(t, chooser, []modelStepSpec{step("c1", agentClick), finalAnswer("ok")}, tt.settings)
			if tt.prepare != nil {
				tt.prepare(ae)
			}
			got := ae.waitTask(t, ae.start(t, "p").ID)
			if got.Status != domain.TaskCompleted || got.HandoffReason != "" || got.FastSteps != 0 || got.OpenAISteps != 2 {
				t.Fatalf("task = %s handoff %q fast %d openai %d", got.Status, got.HandoffReason, got.FastSteps, got.OpenAISteps)
			}
			if n := len(chooser.Requests()); n != 0 {
				t.Errorf("TypeSafe was called %d times although the fast path is not enabled", n)
			}
			if len(fastRows(ae, got.ID)) != 0 {
				t.Error("fast rows were recorded")
			}
		})
	}
}

// 高速パスの設定はタスクの開始時に読み、そのタスクでは固定する（途中で除外ラベルを変えても、実行中のタスクの表は変わらない）。
func TestFastPathUsesTheSettingsAtTaskStart(t *testing.T) {
	var ae *agentEnv
	chooser := newScriptedChooser(
		chooserStepSpec{res: choice(fastID("Save"), 0.9, 0), mutate: func() {
			ae.changeScreen("h1")()
			ae.settings.put(SettingFastpathExcludedLabels, `["save","open"]`) // 実行中の変更
			ae.settings.put(SettingFastpathMinConfidence, "0.99")
		}},
		chooserStepSpec{res: choice(fastID("Open"), 0.8, 0)},
		chooserStepSpec{res: choice(typesafe.AbstainChoice, 1, 0)},
	)
	ae = newFastEnv(t, chooser, []modelStepSpec{finalAnswer("ok")}, nil)
	got := ae.waitTask(t, ae.start(t, "p").ID)
	if got.FastSteps != 2 || got.HandoffReason != domain.HandoffAbstain {
		t.Fatalf("task = %+v", got)
	}
}

func TestFastPathChooserRequestHasNoValuesOrTypedText(t *testing.T) {
	chooser := newScriptedChooser(chooserStepSpec{res: choice(typesafe.AbstainChoice, 1, 0)})
	ae := newFastEnv(t, chooser, []modelStepSpec{step("c1", agentType), finalAnswer("ok")}, nil)
	ae.waitTask(t, ae.start(t, "p").ID)
	b, err := json.Marshal(chooser.Requests())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hello-secret-body") {
		t.Errorf("the request carries typed text: %s", b)
	}
}

func TestJoinSummary(t *testing.T) {
	if joinSummary(nil) != "" {
		t.Error("an empty summary must stay empty")
	}
	got := joinSummary([]string{`Clicked the button labeled "Save".`, `Brought the window "x" to the foreground.`})
	for _, want := range []string{`- Clicked the button labeled "Save".`, `- Brought the window "x" to the foreground.`} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

func TestNewFastPathRequiresItsParts(t *testing.T) {
	if _, err := NewFastPath(FastPath{}); err == nil {
		t.Error("an empty FastPath was accepted")
	}
}
