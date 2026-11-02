package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

var ctlAgent = domain.Actor{Kind: domain.ActorAgent, ClientID: "client-a", Label: "agent", SessionID: "task-1"}

// judgeEnv は agent のアクションが Judge・承認を通る Control（実際の Approval。承認の窓は Bus の approval イベントで知る）。
type judgeEnv struct {
	*leaseApprovalEnv
	judge *stubJudge
}

// newJudgeEnv は承認モード mode のクライアント client-a の agent（task-1）がリースを保持した状態の Control を組む。
// 前面ウィンドウ（4）は front.exe、(0,0)〜(1600,900) の通常ウィンドウ（1）は normal.exe。
func newJudgeEnv(t *testing.T, mode domain.ApprovalMode, judge *stubJudge) *judgeEnv {
	t.Helper()
	le := newLeaseApprovalEnv(t, mode)
	windows := ctlDefaultWindows()
	windows[0].Process = "front.exe"
	windows[1].Process = "normal.exe"
	le.windows.SetWindows(windows...)
	je := &judgeEnv{leaseApprovalEnv: le, judge: judge}
	if judge != nil {
		je.rebuild(t, judge)
	} else {
		je.rebuild(t, nil)
	}
	if mode == domain.ApprovalAuto {
		mustAcquire(t, le.ctlEnv, ctlAgent)
	} else {
		out, req := le.acquire(t, context.Background(), ctlAgent)
		if req.Kind != ApprovalKindLease {
			t.Fatalf("request = %+v", req)
		}
		if err := le.approval.Resolve(req.ID, true); err != nil {
			t.Fatal(err)
		}
		if r := le.wait(t, out); r.err != nil {
			t.Fatal(r.err)
		}
	}
	le.audit.entries = nil
	return je
}

// rebuild は judge（nil なら Judge なし）と本物の Approval で Control を作り直す。
func (je *judgeEnv) rebuild(t *testing.T, judge Judge) {
	t.Helper()
	e := je.ctlEnv
	cfg := ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
		Approval: je.approval,
	}
	if judge != nil {
		cfg.Judge = judge
	}
	var err error
	if e.control, err = NewControl(cfg); err != nil {
		t.Fatal(err)
	}
}

type doResult struct {
	res Result
	err error
}

// doAsync は別 goroutine で agent の Do を呼ぶ。
func (je *judgeEnv) doAsync(ctx context.Context, who domain.Actor, op domain.Op) <-chan doResult {
	out := make(chan doResult, 1)
	go func() {
		r, err := je.control.Do(ctx, who, op)
		out <- doResult{r, err}
	}()
	return out
}

// nextApproval は承認の要求が登録された合図（approval イベント）を待って、その要求を返す。
func (je *judgeEnv) nextApproval(t *testing.T, out <-chan doResult) ApprovalRequest {
	t.Helper()
	select {
	case ev := <-je.approvals:
		data := ev.Data.(ApprovalEvent)
		for _, r := range je.approval.Pending() {
			if r.ID == data.ID {
				return r
			}
		}
		t.Fatalf("event %q is not pending", data.ID)
	case r := <-out:
		t.Fatalf("Do returned without asking for approval: %+v", r)
	case <-time.After(approvalWait):
		t.Fatal("approval event was not published")
	}
	return ApprovalRequest{}
}

func (je *judgeEnv) wait(t *testing.T, out <-chan doResult) doResult {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(approvalWait):
		t.Fatal("Do did not return")
	}
	return doResult{}
}

func (je *judgeEnv) expectNoApproval(t *testing.T) {
	t.Helper()
	select {
	case ev := <-je.approvals:
		t.Fatalf("unexpected approval request: %+v", ev)
	default:
	}
}

func (je *judgeEnv) entriesNamed(event string) []repository.AuditEntry {
	var out []repository.AuditEntry
	for _, e := range je.audit.Entries() {
		if e.Event == event {
			out = append(out, e)
		}
	}
	return out
}

var (
	clickFront  = domain.Click{At: &domain.Point{X: 100, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 0} // 物理 (204, 204): front.exe
	clickNormal = domain.Click{At: &domain.Point{X: 490, Y: 245}, Button: domain.ButtonLeft, Count: 1, Monitor: 0} // 物理 (1000, 500): normal.exe
)

func TestControlAgentAllowedActionExecutesAndIsAudited(t *testing.T) {
	judge := &stubJudge{v: domain.VerdictAllow}
	je := newJudgeEnv(t, domain.ApprovalAuto, judge)
	ctx := WithAgentStep(context.Background(), AgentStep{No: 3, Route: AgentRouteFast, ExtraDetail: map[string]any{"chosen_id": "c7", "route": "evil", "x": 1}})
	if _, err := je.control.Do(ctx, ctlAgent, clickFront); err != nil {
		t.Fatal(err)
	}
	if !hasInputCall(je.ctlEnv, "Click") {
		t.Fatal("the click was not executed")
	}
	if w := judge.last.Load().(domain.WindowInfo); w.Process != "front.exe" {
		t.Errorf("judge saw %+v, want the window at the click point", w)
	}
	je.expectNoApproval(t)
	steps := je.entriesNamed("agent.step")
	if len(steps) != 1 || len(je.audit.Entries()) != 1 {
		t.Fatalf("entries = %+v, want exactly one agent.step", je.audit.Entries())
	}
	e := steps[0]
	if e.Kind != "agent" || e.Result != "ok" || e.Tool != "left_click" || e.ActorType != domain.ActorAgent || e.TaskID != "task-1" ||
		e.SessionID != "task-1" || e.ClientID != "client-a" || e.StepNo == nil || *e.StepNo != 3 || e.LeaseID == "" || e.ErrorCode != "" || e.DurationMS == nil {
		t.Fatalf("entry = %+v", e)
	}
	d := auditDetail(t, e)
	if d["route"] != "fast" || d["chosen_id"] != "c7" || d["judge"] != "allow" || d["x"] != float64(100) || d["button"] != "left" {
		t.Errorf("detail = %v", d)
	}
	if _, ok := d["judge_reason"]; ok {
		t.Errorf("an allow has no reason: %v", d)
	}
}

func TestControlAgentJudgeSeesTheTargetWindow(t *testing.T) {
	tests := []struct {
		name string
		op   domain.Op
		want []string // Assess が見たウィンドウのプロセス（呼ばれた順）
	}{
		{"click uses the window at the point", clickNormal, []string{"normal.exe"}},
		{"scroll uses the window at the point", domain.Scroll{At: domain.Point{X: 490, Y: 245}, Dir: domain.DirDown, Amount: 1, Monitor: 0}, []string{"normal.exe"}},
		{"drag asks for the start and the end", domain.Drag{From: domain.Point{X: 100, Y: 100}, To: domain.Point{X: 490, Y: 245}, Monitor: 0}, []string{"front.exe", "normal.exe"}},
		{"type uses the foreground window", domain.TypeText{Text: "hello"}, []string{"front.exe"}},
		{"key uses the foreground window", domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}}, []string{"front.exe"}},
		{"clipboard uses the foreground window", domain.ClipboardWrite{Text: "x"}, []string{"front.exe"}},
		{"focus_window uses the window with that id", domain.FocusWindow{ID: "1"}, []string{"normal.exe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen []string
			judge := &stubJudge{fn: func(_ context.Context, a domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error) {
				seen = append(seen, w.Process)
				if a.Source != "agent" {
					t.Errorf("Action.Source = %q", a.Source)
				}
				return domain.VerdictAllow, Reason{}, nil
			}}
			je := newJudgeEnv(t, domain.ApprovalAuto, judge)
			if _, err := je.control.Do(context.Background(), ctlAgent, tt.op); err != nil {
				t.Fatal(err)
			}
			if strings.Join(seen, ",") != strings.Join(tt.want, ",") {
				t.Errorf("windows seen = %v, want %v", seen, tt.want)
			}
		})
	}
}

// Drag は始点か終点のどちらかが deny されたら拒否（厳しい方）。
func TestControlAgentDragIsJudgedByTheStrictestEndpoint(t *testing.T) {
	judge := &stubJudge{fn: func(_ context.Context, _ domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error) {
		if w.Process == "normal.exe" {
			return domain.VerdictDeny, Reason{Code: "d"}, nil
		}
		return domain.VerdictAllow, Reason{}, nil
	}}
	je := newJudgeEnv(t, domain.ApprovalAuto, judge)
	_, err := je.control.Do(context.Background(), ctlAgent, domain.Drag{From: domain.Point{X: 100, Y: 100}, To: domain.Point{X: 490, Y: 245}, Monitor: 0})
	if !errors.Is(err, domain.ErrJudgeDenied) || hasInputCall(je.ctlEnv, "Drag") {
		t.Fatalf("err = %v, drag executed = %v", err, hasInputCall(je.ctlEnv, "Drag"))
	}
}

func TestControlAgentJudgeDenyBlocksAndIsAudited(t *testing.T) {
	judge := &stubJudge{v: domain.VerdictDeny, r: Reason{Code: JudgeReasonDenyProcess, Message: "拒否リストのプロセスです: front.exe"}}
	je := newJudgeEnv(t, domain.ApprovalAuto, judge)
	ctx := WithAgentStep(context.Background(), AgentStep{No: 4, Route: AgentRouteOpenAI})
	_, err := je.control.Do(ctx, ctlAgent, domain.TypeText{Text: "TOP-SECRET-TEXT"})
	if !errors.Is(err, domain.ErrJudgeDenied) {
		t.Fatalf("err = %v, want ErrJudgeDenied", err)
	}
	if len(inputCalls(je.ctlEnv)) != 0 {
		t.Fatalf("the injector was called: %+v", inputCalls(je.ctlEnv))
	}
	je.expectNoApproval(t)
	if got := domain.TaskErrorCode(err); got != domain.TaskErrorJudgeDenied {
		t.Errorf("TaskErrorCode = %q", got)
	}

	steps, judges := je.entriesNamed("agent.step"), je.entriesNamed("agent.judge")
	if len(steps) != 1 || len(judges) != 1 {
		t.Fatalf("entries = %+v", je.audit.Entries())
	}
	if e := steps[0]; e.Kind != "agent" || e.Result != "denied" || e.ErrorCode != "judge_denied" || e.Tool != "type" || e.StepNo == nil || *e.StepNo != 4 {
		t.Errorf("step entry = %+v", e)
	}
	d := auditDetail(t, steps[0])
	if d["judge"] != "deny" || d["judge_reason"] != JudgeReasonDenyProcess || d["route"] != "openai" || d["text_len"] != float64(15) {
		t.Errorf("step detail = %v", d)
	}
	if e := judges[0]; e.Kind != "agent" || e.Result != "denied" || e.ErrorCode != "judge_denied" || e.TaskID != "task-1" || e.StepNo == nil || *e.StepNo != 4 {
		t.Errorf("judge entry = %+v", e)
	}
	jd := auditDetail(t, judges[0])
	if jd["judge"] != "deny" || jd["judge_reason"] != JudgeReasonDenyProcess || jd["route"] != "openai" {
		t.Errorf("judge detail = %v", jd)
	}
	// 入力した文字列はどの行にも残らない（audit.record_text が true でも）。
	for _, e := range je.audit.Entries() {
		if strings.Contains(string(e.DetailJSON), "TOP-SECRET") {
			t.Errorf("typed text leaked: %s", e.DetailJSON)
		}
	}
}

func TestControlAgentNeverRecordsTypedTextEvenWithRecordText(t *testing.T) {
	je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictAllow})
	je.settings.put(settingAuditRecordText, "true")
	if _, err := je.control.Do(context.Background(), ctlAgent, domain.TypeText{Text: "TOP-SECRET-TEXT"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range je.audit.Entries() {
		if strings.Contains(string(e.DetailJSON), "TOP-SECRET") {
			t.Errorf("typed text leaked: %s", e.DetailJSON)
		}
	}
	// 対照: mcp は audit.record_text が true なら本文が残る（設定が効いていること）。
	je.settings.put(settingAuditRecordText, "true")
	mustRelease := func() { je.leases.Release(ctlAgent) }
	mustRelease()
	if _, err := je.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "TOP-SECRET-TEXT"}); err != nil {
		t.Fatal(err)
	}
	last := je.audit.Entries()
	if !strings.Contains(string(last[len(last)-1].DetailJSON), "TOP-SECRET") {
		t.Error("the control: mcp should record the text with audit.record_text")
	}
}

func TestControlAgentRequireApprovalAsksTheHostWithASafeSummary(t *testing.T) {
	for _, mode := range []domain.ApprovalMode{domain.ApprovalAlways, domain.ApprovalFirstTime, domain.ApprovalAuto} {
		t.Run(string(mode), func(t *testing.T) {
			judge := &stubJudge{v: domain.VerdictRequireApproval, r: Reason{Code: JudgeReasonApproveProcess, Message: "承認リストのプロセスです: front.exe"}}
			je := newJudgeEnv(t, mode, judge)
			out := je.doAsync(WithAgentStep(context.Background(), AgentStep{No: 2, Route: AgentRouteOpenAI}), ctlAgent, domain.TypeText{Text: "TOP-SECRET-TEXT"})
			req := je.nextApproval(t, out)
			// クライアントの承認モード（auto を含む）では Judge の承認を省略できない（FR-G12）。
			if req.Kind != ApprovalKindJudgeAction || !req.Who.SameSession(ctlAgent) || req.Who.Kind != domain.ActorAgent {
				t.Fatalf("request = %+v", req)
			}
			for _, want := range []string{"type", "front.exe", "承認リストのプロセスです"} {
				if !strings.Contains(req.Detail, want) {
					t.Errorf("detail %q lacks %q", req.Detail, want)
				}
			}
			if strings.Contains(req.Detail, "TOP-SECRET") || strings.Contains(req.Detail, "front") && strings.Contains(req.Detail, "Title") {
				t.Errorf("detail leaks: %q", req.Detail)
			}
			if len(inputCalls(je.ctlEnv)) != 0 {
				t.Fatal("executed before the host approved")
			}
			if err := je.approval.Resolve(req.ID, true); err != nil {
				t.Fatal(err)
			}
			if r := je.wait(t, out); r.err != nil {
				t.Fatal(r.err)
			}
			if !hasInputCall(je.ctlEnv, "Text") {
				t.Error("the approved action was not executed")
			}
			steps := je.entriesNamed("agent.step")
			if len(steps) != 1 || steps[0].Result != "ok" {
				t.Fatalf("steps = %+v", steps)
			}
			d := auditDetail(t, steps[0])
			if d["judge"] != "require_approval" || d["judge_reason"] != JudgeReasonApproveProcess || d["approval"] != "granted" {
				t.Errorf("detail = %v", d)
			}
			judges := je.entriesNamed("agent.judge")
			if len(judges) != 1 || judges[0].Result != "ok" {
				t.Fatalf("judge entries = %+v", judges)
			}
		})
	}
}

func TestControlAgentApprovalOutcomes(t *testing.T) {
	newReq := func(t *testing.T) (*judgeEnv, <-chan doResult, ApprovalRequest, context.CancelFunc) {
		je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictRequireApproval, r: Reason{Code: "x", Message: "m"}})
		ctx, cancel := context.WithCancel(context.Background())
		out := je.doAsync(ctx, ctlAgent, domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}})
		return je, out, je.nextApproval(t, out), cancel
	}
	t.Run("host deny", func(t *testing.T) {
		je, out, req, cancel := newReq(t)
		defer cancel()
		_ = je.approval.Resolve(req.ID, false)
		r := je.wait(t, out)
		if !errors.Is(r.err, domain.ErrApprovalDenied) || len(inputCalls(je.ctlEnv)) != 0 {
			t.Fatalf("err = %v, input = %+v", r.err, inputCalls(je.ctlEnv))
		}
		e := je.entriesNamed("agent.step")[0]
		if e.Result != "denied" || e.ErrorCode != "approval_denied" || auditDetail(t, e)["approval"] != "denied" {
			t.Errorf("entry = %+v", e)
		}
		if got := domain.TaskErrorCode(r.err); got != domain.TaskErrorApprovalDenied {
			t.Errorf("TaskErrorCode = %q", got)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		je, out, _, cancel := newReq(t)
		defer cancel()
		je.aTimers.FireAll()
		r := je.wait(t, out)
		if !errors.Is(r.err, domain.ErrApprovalTimeout) || len(inputCalls(je.ctlEnv)) != 0 {
			t.Fatalf("err = %v", r.err)
		}
		e := je.entriesNamed("agent.step")[0]
		if e.Result != "denied" || e.ErrorCode != "approval_timeout" || auditDetail(t, e)["approval"] != "timeout" {
			t.Errorf("entry = %+v", e)
		}
	})
	t.Run("context cancellation", func(t *testing.T) {
		je, out, _, cancel := newReq(t)
		cancel()
		r := je.wait(t, out)
		if !errors.Is(r.err, context.Canceled) || len(inputCalls(je.ctlEnv)) != 0 {
			t.Fatalf("err = %v", r.err)
		}
		if p := je.approval.Pending(); len(p) != 0 {
			t.Errorf("pending = %+v", p)
		}
	})
}

// 承認を待っている間にリースが剥奪されたら、許可されても実行しない。
func TestControlAgentApprovalGrantedAfterRevokeDoesNotExecute(t *testing.T) {
	je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictRequireApproval, r: Reason{Code: "x"}})
	out := je.doAsync(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
	req := je.nextApproval(t, out)
	if err := je.leases.Revoke(HostActor); err != nil {
		t.Fatal(err)
	}
	if err := je.approval.Resolve(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := je.wait(t, out); !errors.Is(r.err, domain.ErrLeaseRevoked) {
		t.Fatalf("err = %v, want ErrLeaseRevoked", r.err)
	}
	if len(inputCalls(je.ctlEnv)) != 0 {
		t.Fatal("executed after the lease was revoked")
	}
}

// 承認を待っている間にキルスイッチが発動したら、許可されても実行しない。
func TestControlAgentApprovalGrantedAfterKillSwitchDoesNotExecute(t *testing.T) {
	je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictRequireApproval, r: Reason{Code: "x"}})
	out := je.doAsync(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
	req := je.nextApproval(t, out)
	if err := je.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	_ = je.approval.Resolve(req.ID, true)
	r := je.wait(t, out)
	if !errors.Is(r.err, domain.ErrKillSwitchActive) && !errors.Is(r.err, domain.ErrLeaseRequired) {
		t.Fatalf("err = %v, want kill switch / lease released", r.err)
	}
	if hasInputCall(je.ctlEnv, "Text") {
		t.Fatal("executed after the kill switch")
	}
}

// 承認を待っている間に対象のウィンドウが変わったら（承認したのと違うプロセスになったら）実行しない。
func TestControlAgentApprovalGrantedAfterTargetChangeDoesNotExecute(t *testing.T) {
	judge := &stubJudge{fn: func(_ context.Context, _ domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error) {
		return domain.VerdictRequireApproval, Reason{Code: "x"}, nil
	}}
	je := newJudgeEnv(t, domain.ApprovalAuto, judge)
	out := je.doAsync(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
	req := je.nextApproval(t, out)
	windows := ctlDefaultWindows()
	windows[0].Process = "other.exe"
	je.windows.SetWindows(windows...)
	_ = je.approval.Resolve(req.ID, true)
	if r := je.wait(t, out); !errors.Is(r.err, domain.ErrApprovalDenied) {
		t.Fatalf("err = %v, want ErrApprovalDenied", r.err)
	}
	if len(inputCalls(je.ctlEnv)) != 0 {
		t.Fatal("executed against a different target than the approved one")
	}
}

// 承認の待ちの後の再確認で Judge が deny するようになっていたら、許可されても実行しない。
func TestControlAgentApprovalGrantedButNowDeniedDoesNotExecute(t *testing.T) {
	var n int
	judge := &stubJudge{fn: func(context.Context, domain.Action, domain.WindowInfo) (domain.Verdict, Reason, error) {
		n++
		if n == 1 {
			return domain.VerdictRequireApproval, Reason{Code: "x"}, nil
		}
		return domain.VerdictDeny, Reason{Code: "d"}, nil
	}}
	je := newJudgeEnv(t, domain.ApprovalAuto, judge)
	out := je.doAsync(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
	req := je.nextApproval(t, out)
	_ = je.approval.Resolve(req.ID, true)
	if r := je.wait(t, out); !errors.Is(r.err, domain.ErrJudgeDenied) {
		t.Fatalf("err = %v, want ErrJudgeDenied", r.err)
	}
	if len(inputCalls(je.ctlEnv)) != 0 {
		t.Fatal("executed")
	}
}

func TestControlAgentFailsClosedWithoutJudgeOrApproval(t *testing.T) {
	t.Run("no judge: requires approval", func(t *testing.T) {
		je := newJudgeEnv(t, domain.ApprovalAuto, nil)
		out := je.doAsync(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
		req := je.nextApproval(t, out)
		if req.Kind != ApprovalKindJudgeAction || !strings.Contains(req.Detail, "判定器が組み込まれていません") {
			t.Fatalf("request = %+v", req)
		}
		_ = je.approval.Resolve(req.ID, false)
		if r := je.wait(t, out); !errors.Is(r.err, domain.ErrApprovalDenied) {
			t.Fatalf("err = %v", r.err)
		}
		if len(inputCalls(je.ctlEnv)) != 0 {
			t.Fatal("executed")
		}
		if d := auditDetail(t, je.entriesNamed("agent.step")[0]); d["judge_reason"] != JudgeReasonUnconfigured {
			t.Errorf("detail = %v", d)
		}
	})
	t.Run("no judge and no approval: denied", func(t *testing.T) {
		je := newJudgeEnv(t, domain.ApprovalAuto, nil)
		e := je.ctlEnv
		var err error
		e.control, err = NewControl(ControlConfig{
			Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
			Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.control.Do(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
		if !errors.Is(err, domain.ErrApprovalDenied) || len(inputCalls(e)) != 0 {
			t.Fatalf("err = %v, input = %+v", err, inputCalls(e))
		}
		if d := auditDetail(t, je.entriesNamed("agent.step")[0]); d["approval"] != "unavailable" {
			t.Errorf("detail = %v", d)
		}
	})
	t.Run("judge error requires approval", func(t *testing.T) {
		je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictAllow, err: errors.New("boom")})
		out := je.doAsync(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
		req := je.nextApproval(t, out)
		_ = je.approval.Resolve(req.ID, false)
		if r := je.wait(t, out); !errors.Is(r.err, domain.ErrApprovalDenied) {
			t.Fatalf("err = %v", r.err)
		}
		if d := auditDetail(t, je.entriesNamed("agent.step")[0]); d["judge_reason"] != JudgeReasonError || d["judge"] != "require_approval" {
			t.Errorf("detail = %v", d)
		}
	})
	t.Run("unknown verdict requires approval", func(t *testing.T) {
		je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.Verdict(42)})
		out := je.doAsync(context.Background(), ctlAgent, domain.TypeText{Text: "a"})
		_ = je.nextApproval(t, out)
		je.cancelAll(t, out)
	})
	t.Run("a window that cannot be inspected is not executed", func(t *testing.T) {
		je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictAllow})
		je.windows.SetError("Foreground", errors.New("no desktop"))
		if _, err := je.control.Do(context.Background(), ctlAgent, domain.TypeText{Text: "a"}); err == nil {
			t.Fatal("want an error")
		}
		if len(inputCalls(je.ctlEnv)) != 0 {
			t.Fatal("executed")
		}
	})
}

// cancelAll は待っている承認を拒否して Do を終わらせる（テストの後始末）。
func (je *judgeEnv) cancelAll(t *testing.T, out <-chan doResult) {
	t.Helper()
	for _, r := range je.approval.Pending() {
		_ = je.approval.Resolve(r.ID, false)
	}
	je.wait(t, out)
}

// 検査の順序: KillSwitch → Lease → plan → InputGuard → Judge。先の検査で拒否されたら Judge を呼ばない。
func TestControlAgentJudgeRunsAfterTheEarlierChecks(t *testing.T) {
	own := domain.Click{At: &domain.Point{X: 980, Y: 100}, Button: domain.ButtonLeft, Count: 1, Monitor: 0} // 自ウィンドウ
	tests := []struct {
		name    string
		prepare func(je *judgeEnv)
		op      domain.Op
		wantErr error
	}{
		{"kill switch", func(je *judgeEnv) { _ = je.kill.Trigger("t") }, clickFront, domain.ErrKillSwitchActive},
		{"lease", func(je *judgeEnv) { je.leases.Release(ctlAgent) }, clickFront, domain.ErrLeaseRequired},
		{"plan (bad button)", nil, domain.Click{At: &domain.Point{X: 1, Y: 1}, Button: "bogus", Count: 1}, domain.ErrInvalidArgument},
		{"input guard", nil, own, domain.ErrInputBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			judge := &stubJudge{v: domain.VerdictDeny}
			je := newJudgeEnv(t, domain.ApprovalAuto, judge)
			if tt.prepare != nil {
				tt.prepare(je)
			}
			_, err := je.control.Do(context.Background(), ctlAgent, tt.op)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if judge.calls.Load() != 0 {
				t.Errorf("the judge ran before %s rejected the action", tt.name)
			}
			steps := je.entriesNamed("agent.step")
			if len(steps) != 1 || steps[0].Kind != "agent" || steps[0].Result != "denied" && tt.wantErr != domain.ErrInvalidArgument {
				t.Errorf("steps = %+v", steps)
			}
			if _, ok := auditDetail(t, steps[0])["judge"]; ok {
				t.Errorf("no judge verdict can be recorded: %s", steps[0].DetailJSON)
			}
			if len(je.entriesNamed("agent.judge")) != 0 {
				t.Error("unexpected agent.judge row")
			}
		})
	}
}

// mcp・web の入力は Judge も承認も通らない（Judge が deny でも実行される）。
func TestControlJudgeIsNeverRunForOtherActors(t *testing.T) {
	judge := &stubJudge{v: domain.VerdictDeny}
	je := newJudgeEnv(t, domain.ApprovalAuto, judge)
	je.leases.Release(ctlAgent)
	if _, err := je.control.Do(context.Background(), ctlAlice, domain.TypeText{Text: "a"}); err != nil {
		t.Fatal(err)
	}
	je.leases.Release(ctlAlice)
	viewer := domain.Actor{Kind: domain.ActorWeb, ClientID: "client-a", Label: "viewer", SessionID: "viewer-9"}
	mustAcquire(t, je.ctlEnv, viewer)
	if _, err := je.control.Do(context.Background(), viewer, domain.KeyPress{Keys: domain.KeyCombo{Key: "a"}}); err != nil {
		t.Fatal(err)
	}
	if judge.calls.Load() != 0 {
		t.Errorf("the judge ran %d times for non-agent actors", judge.calls.Load())
	}
	je.expectNoApproval(t)
	for _, e := range je.audit.Entries() {
		if e.Kind == "agent" {
			t.Errorf("a non-agent action produced an agent row: %+v", e)
		}
	}
}

// 実際の RuleJudge と AgentSettings をつなぐ: deny_processes に載った前面のプロセスへの入力は拒否される。
func TestControlAgentWithRuleJudge(t *testing.T) {
	s, set, _, _ := newTestAgentSettings(t)
	je := newJudgeEnv(t, domain.ApprovalAuto, nil)
	je.rebuild(t, Chain(FailClosed(NewRuleJudge(s), time.Second)))
	set.put(SettingAgentDenyProcesses, `["front.exe"]`)
	if _, err := je.control.Do(context.Background(), ctlAgent, domain.TypeText{Text: "a"}); !errors.Is(err, domain.ErrJudgeDenied) {
		t.Fatalf("err = %v, want ErrJudgeDenied", err)
	}
	// GUI での変更は次のアクションから効く。
	set.put(SettingAgentDenyProcesses, `[]`)
	if _, err := je.control.Do(context.Background(), ctlAgent, domain.TypeText{Text: "a"}); err != nil {
		t.Fatalf("after the change: %v", err)
	}
}

// 起動（Launch）は agent.step の行で監査し、Judge は通らない（座標もウィンドウも持たない操作。許可リストが唯一の制限）。
func TestControlLaunchByAgentDoesNotRunTheJudge(t *testing.T) {
	judge := &stubJudge{v: domain.VerdictDeny}
	je := newJudgeEnv(t, domain.ApprovalAuto, judge)
	je.launcher.AddExecutable(appNotepad)
	je.launcher.AddDir(appWorkMark)
	app := registerNotepad(t, je.ctlEnv)
	je.audit.entries = nil
	if _, err := je.control.Launch(context.Background(), ctlAgent, app.ID); err != nil {
		t.Fatal(err)
	}
	if judge.calls.Load() != 0 {
		t.Error("Launch must not run the judge")
	}
	steps := je.entriesNamed("agent.step")
	if len(steps) != 1 || steps[0].Tool != "launch_app" {
		t.Fatalf("entries = %+v", je.audit.Entries())
	}
}

func TestAuditResultMapsJudgeDenial(t *testing.T) {
	err := domain.ErrJudgeDenied
	if auditResult(err) != auditDenied || auditErrorCode(err) != "judge_denied" {
		t.Errorf("judge denial: result %q code %q", auditResult(err), auditErrorCode(err))
	}
	wrapped := errors.Join(errors.New("ctx"), domain.ErrJudgeDenied)
	if auditResult(wrapped) != auditDenied || auditErrorCode(wrapped) != "judge_denied" {
		t.Error("a wrapped judge denial must map the same way")
	}
	if e := ToolAuditEntry(ctlAlice, "type", nil, "", err, time.Millisecond); e.Result != "denied" || e.ErrorCode != "judge_denied" {
		t.Errorf("entry = %+v", e)
	}
	// 既存の写像は変えない。
	if auditResult(domain.ErrInputBlocked) != auditDenied || auditErrorCode(domain.ErrInputBlocked) != "input_blocked" || auditResult(errors.New("x")) != auditError {
		t.Error("existing mapping changed")
	}
}

func TestJudgeApprovalDetailIsSanitisedAndBounded(t *testing.T) {
	long := strings.Repeat("あ", 2000)
	d := judgeApprovalDetail("type", []string{"a.exe"}, Reason{Code: "c", Message: long})
	if n := len([]rune(d)); n != 500 {
		t.Errorf("detail has %d runes, want 500", n)
	}
	d = judgeApprovalDetail("type", []string{"a\r\n.exe"}, Reason{Message: "x\ty"})
	if strings.ContainsAny(d, "\r\n\t") {
		t.Errorf("control characters survived: %q", d)
	}
	if d := judgeApprovalDetail("key", nil, Reason{Code: "c"}); !strings.Contains(d, "（なし）") || !strings.Contains(d, "c") {
		t.Errorf("detail = %q", d)
	}
}

func TestAgentStepContext(t *testing.T) {
	if _, ok := AgentStepFrom(context.Background()); ok {
		t.Fatal("empty context has no step")
	}
	ctx := WithAgentStep(context.Background(), AgentStep{No: 7, Route: AgentRouteFast, ExtraDetail: map[string]any{"k": "v"}})
	s, ok := AgentStepFrom(ctx)
	if !ok || s.No != 7 || s.Route != "fast" || s.ExtraDetail["k"] != "v" {
		t.Fatalf("step = %+v, %v", s, ok)
	}
	raw, _ := json.Marshal(s.ExtraDetail)
	if string(raw) != `{"k":"v"}` {
		t.Fatal(string(raw))
	}
}

// agent のアクションも、成功（result = ok）して画面を変えるならサムネイルが付く（FR-AU02）。拒否・失敗・移動だけには付かない。
func TestControlAgentActionsGetThumbnails(t *testing.T) {
	newCtl := func(t *testing.T, judge Judge) *thumbCtl {
		tc := newThumbCtl(t)
		ctl, err := NewControl(ControlConfig{
			Capturer: tc.cap, Input: tc.input, Windows: tc.windows, Clipboard: tc.clipboard, UI: tc.ui,
			Launcher: tc.launcher, Apps: tc.apps, Leases: tc.leases, Kill: tc.kill, Settings: tc.settings,
			Audit: tc.audit, Clock: tc.clock, Judge: judge,
			Sleep: func(context.Context, time.Duration) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		tc.ctl, tc.shooter = ctl, ctl.(*control).thumbs
		mustAcquire(t, tc.ctlEnv, ctlAgent)
		return tc
	}
	t.Run("click is captured on the clicked monitor", func(t *testing.T) {
		tc := newCtl(t, &stubJudge{v: domain.VerdictAllow})
		ctx := WithAgentStep(context.Background(), AgentStep{No: 1, Route: AgentRouteOpenAI})
		if _, err := tc.ctl.Do(ctx, ctlAgent, domain.Click{At: &domain.Point{X: 10, Y: 10}, Button: domain.ButtonLeft, Count: 1, Monitor: 1}); err != nil {
			t.Fatal(err)
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Entry.Event != "agent.step" || got[0].Entry.Kind != "agent" || got[0].Entry.Result != auditOK {
			t.Fatalf("records = %+v", got)
		}
		if got[0].Thumb == nil || len(got[0].Thumb.JPEG) == 0 || got[0].Entry.TaskID != "task-1" || got[0].Entry.TS.IsZero() {
			t.Fatalf("record = %+v", got[0])
		}
		if g := tc.grabs(); !equalInts(g, []int{1}) {
			t.Fatalf("grabbed monitors = %v, want [1]", g)
		}
	})
	t.Run("type is captured on the primary monitor", func(t *testing.T) {
		tc := newCtl(t, &stubJudge{v: domain.VerdictAllow})
		if _, err := tc.ctl.Do(context.Background(), ctlAgent, domain.TypeText{Text: "hi"}); err != nil {
			t.Fatal(err)
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Thumb == nil || !equalInts(tc.grabs(), []int{1}) {
			t.Fatalf("records = %+v, grabs = %v", got, tc.grabs())
		}
	})
	t.Run("a judge denial is recorded without a thumbnail", func(t *testing.T) {
		tc := newCtl(t, &stubJudge{v: domain.VerdictDeny, r: Reason{Code: "d"}})
		if _, err := tc.ctl.Do(context.Background(), ctlAgent, domain.TypeText{Text: "hi"}); !errors.Is(err, domain.ErrJudgeDenied) {
			t.Fatalf("err = %v", err)
		}
		got := tc.finish()
		if len(got) != 2 || got[0].Entry.Event != "agent.judge" || got[1].Entry.Event != "agent.step" {
			t.Fatalf("records = %+v", got)
		}
		for _, r := range got {
			if r.Thumb != nil {
				t.Errorf("unexpected thumbnail on %s", r.Entry.Event)
			}
		}
		if len(tc.grabs()) != 0 {
			t.Errorf("grabs = %v", tc.grabs())
		}
	})
	t.Run("mouse_move and clipboard have none", func(t *testing.T) {
		tc := newCtl(t, &stubJudge{v: domain.VerdictAllow})
		if _, err := tc.ctl.Do(context.Background(), ctlAgent, domain.ClipboardWrite{Text: "x"}); err != nil {
			t.Fatal(err)
		}
		got := tc.finish()
		if len(got) != 1 || got[0].Thumb != nil || len(tc.grabs()) != 0 {
			t.Fatalf("records = %+v, grabs = %v", got, tc.grabs())
		}
	})
}
