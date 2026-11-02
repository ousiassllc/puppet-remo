package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは Control.Do の Judge（検査の 4 の後。agent のみ。FR-G07・G12）と、agent のアクションの監査（FR-G15・G23）。
//
// 監査のイベント（docs/architecture/er.md「イベントのカタログ」）:
//   - agent.step: ActorAgent のアクション 1 件ごとに 1 行（成功・拒否・失敗のどれも。kind = agent）。tool はアクション種別、
//     task_id は Actor.SessionID、step_no と detail.route は WithAgentStep から、detail.judge / judge_reason は Judge の判定。
//   - agent.judge: Judge が allow 以外を返したときだけ、判定の直後（承認待ちの前）に 1 行。
const (
	agentStepAuditEvent  = "agent.step"
	agentJudgeAuditEvent = "agent.judge"
)

// 承認の結果（agent.step の detail.approval）。
const (
	approvalOutcomeGranted     = "granted"
	approvalOutcomeDenied      = "denied"
	approvalOutcomeTimeout     = "timeout"
	approvalOutcomeCanceled    = "canceled"
	approvalOutcomeUnavailable = "unavailable"
)

// maxJudgeApprovalDetail は ApprovalRequest.Detail（承認ダイアログに出す要約）の最大文字数。
const maxJudgeApprovalDetail = 500

// judgeTrace は 1 回の Do の Judge の経過（監査の行に残す）。
type judgeTrace struct {
	ran      bool
	verdict  domain.Verdict
	reason   Reason
	approval string // "" は承認を求めていない
}

// judgeAgent は ActorAgent の plan（検査済み・物理座標）に Judge を適用する。
//
//   - Allow → nil。
//   - Deny → domain.ErrJudgeDenied。
//   - RequireApproval → Approval.Request（Kind ApprovalKindJudgeAction。クライアントの承認モードは見ない: 承認モードは
//     リース取得時の 1 回だけで、Judge の承認を省略する経路は無い。FR-G12）。ホストの拒否は ErrApprovalDenied、失効は
//     ErrApprovalTimeout、待ち中の ctx のキャンセルは ctx.Err()。許可のあとは、待ち中に変わりうるものを確かめ直す（recheckAfterApproval）。
//
// Judge が組み込まれていない・Judge の失敗・未知の判定値は RequireApproval に倒す（fail-closed）。Approval が無ければ ErrApprovalDenied。
func (c *control) judgeAgent(ctx context.Context, who domain.Actor, p plan, tr *judgeTrace) error {
	wins, err := c.judgeTargets(ctx, p.op)
	if err != nil {
		return err
	}
	tool, _ := describeOp(p.op, Result{})
	v, r := c.assess(ctx, who, p.op, wins)
	tr.ran, tr.verdict, tr.reason = true, v, r
	procs := processNames(wins)
	if v == domain.VerdictAllow {
		return nil
	}
	c.recordJudge(ctx, who, tool, procs, v, r)
	if v == domain.VerdictDeny {
		return domain.ErrJudgeDenied
	}
	if err := c.askApproval(ctx, who, tool, procs, r, tr); err != nil {
		return err
	}
	return c.recheckAfterApproval(ctx, who, p, procs)
}

// assess は wins のそれぞれを Judge に問い合わせ、最も厳しい判定とその理由を返す。失敗・未知の値は RequireApproval。
func (c *control) assess(ctx context.Context, who domain.Actor, op domain.Op, wins []domain.WindowInfo) (domain.Verdict, Reason) {
	if c.judge == nil {
		return domain.VerdictRequireApproval, Reason{Code: JudgeReasonUnconfigured, Message: "判定器が組み込まれていません。"}
	}
	act := domain.Action{Op: op, Source: string(who.Kind)}
	verdict, reason := domain.VerdictAllow, Reason{}
	for _, w := range wins {
		v, r, err := c.judge.Assess(ctx, act, w)
		switch {
		case err != nil:
			c.log.Warn("judge failed; requiring approval", "err", err)
			v, r = domain.VerdictRequireApproval, Reason{Code: JudgeReasonError, Message: "判定に失敗しました。"}
		case !validVerdict(v):
			v, r = domain.VerdictRequireApproval, Reason{Code: JudgeReasonInvalid, Message: "判定が不正でした。"}
		}
		if v > verdict {
			verdict, reason = v, r
		}
	}
	return verdict, reason
}

// judgeTargets は op の対象ウィンドウを返す（1 件以上。どのウィンドウでもない座標はゼロ値のウィンドウ）。
// 座標のある操作は物理座標のウィンドウ（Drag は始点と終点）、FocusWindow・GetUITree（window_id あり）は id のウィンドウ、
// それ以外（キー・文字入力・クリップボード・window_id なしの GetUITree）は前面のウィンドウ。調べられなければエラー（拒否側に倒す）。
func (c *control) judgeTargets(ctx context.Context, op domain.Op) ([]domain.WindowInfo, error) {
	switch o := op.(type) {
	case domain.MouseMove:
		return c.windowsAt(o.At)
	case domain.Click:
		if o.At != nil {
			return c.windowsAt(*o.At)
		}
	case domain.Drag:
		return c.windowsAt(o.From, o.To)
	case domain.Scroll:
		return c.windowsAt(o.At)
	case domain.PointerMove:
		return c.windowsAt(o.At)
	case domain.MouseButton:
		return c.windowsAt(o.At)
	case domain.Wheel:
		return c.windowsAt(o.At)
	case domain.FocusWindow:
		return c.windowByID(ctx, o.ID)
	case domain.GetUITree:
		if o.WindowID != "" {
			return c.windowByID(ctx, o.WindowID)
		}
	}
	w, err := c.windows.Foreground()
	if err != nil {
		return nil, err
	}
	return []domain.WindowInfo{w}, nil
}

func (c *control) windowsAt(points ...domain.Point) ([]domain.WindowInfo, error) {
	out := make([]domain.WindowInfo, 0, len(points))
	for _, pt := range points {
		w, ok, err := c.windows.At(pt)
		if err != nil {
			return nil, err
		}
		if !ok {
			w = domain.WindowInfo{} // どのウィンドウにもない（デスクトップ）
		}
		out = append(out, w)
	}
	return out, nil
}

// windowByID は id のウィンドウを返す。無ければゼロ値（実行側が window_not_found にする。InputGuard と同じ扱い）。
func (c *control) windowByID(ctx context.Context, id string) ([]domain.WindowInfo, error) {
	list, err := c.windows.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, w := range list {
		if w.ID == id {
			return []domain.WindowInfo{w}, nil
		}
	}
	return []domain.WindowInfo{{}}, nil
}

// processNames は wins のプロセス名を、重複なく出現順に返す（空は除く）。
func processNames(wins []domain.WindowInfo) []string {
	var out []string
	for _, w := range wins {
		if w.Process != "" && !slices.Contains(out, w.Process) {
			out = append(out, w.Process)
		}
	}
	return out
}

// askApproval は Judge が求めた個別アクションの承認をホストに求める（FR-G12。60 秒で拒否扱い）。
func (c *control) askApproval(ctx context.Context, who domain.Actor, tool string, procs []string, r Reason, tr *judgeTrace) error {
	if c.approval == nil {
		tr.approval = approvalOutcomeUnavailable
		return domain.ErrApprovalDenied.WithMsg("no approval channel is configured")
	}
	dec, err := c.approval.Request(ctx, ApprovalRequest{
		Kind: ApprovalKindJudgeAction, Who: who, Detail: judgeApprovalDetail(tool, procs, r),
	})
	switch {
	case errors.Is(err, domain.ErrApprovalTimeout):
		tr.approval = approvalOutcomeTimeout
		return err
	case err != nil:
		tr.approval = approvalOutcomeCanceled
		return err
	case !dec.Allow:
		tr.approval = approvalOutcomeDenied
		return domain.ErrApprovalDenied
	}
	tr.approval = approvalOutcomeGranted
	return nil
}

// judgeApprovalDetail は承認ダイアログに出す要約: 操作の名前・対象のプロセス名・Judge の理由だけ。
// 入力した文字列・キーの名前・パス・引数・クリップボード・ウィンドウのタイトルは入れない。制御文字は空白にし、500 文字で切る。
func judgeApprovalDetail(tool string, procs []string, r Reason) string {
	target := "（なし）"
	if len(procs) > 0 {
		target = strings.Join(procs, ", ")
	}
	why := r.Message
	if why == "" {
		why = r.Code
	}
	d := fmt.Sprintf("操作: %s / 対象プロセス: %s / 理由: %s", tool, target, why)
	d = strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) {
			return ' '
		}
		return ch
	}, d)
	return domain.TruncateRunes(d, maxJudgeApprovalDetail)
}

// recheckAfterApproval は承認を待っている間に変わりうるもの（リース・InputGuard・対象のウィンドウ）を、許可のあとに確かめ直す。
// 待ち中にリースが剥奪・解放された、入力が許されない対象になった、Judge が Deny を返すようになったら実行しない。
// 対象のプロセスが承認したものと違う場合も、承認の範囲外なので ErrApprovalDenied にする。
func (c *control) recheckAfterApproval(ctx context.Context, who domain.Actor, p plan, approved []string) error {
	if err := c.leases.Check(who); err != nil {
		return err
	}
	if err := c.guard.Check(ctx, p.op, p.monitors); err != nil {
		return c.withOwnWindows(ctx, err)
	}
	wins, err := c.judgeTargets(ctx, p.op)
	if err != nil {
		return err
	}
	if v, _ := c.assess(ctx, who, p.op, wins); v == domain.VerdictDeny {
		return domain.ErrJudgeDenied
	}
	if !slices.Equal(processNames(wins), approved) {
		return domain.ErrApprovalDenied.WithMsg("the target window changed while waiting for approval")
	}
	return nil
}

// agentAuditBase は agent の監査の行の共通部分（操作主体・タスク・ステップ・リース）。
func agentAuditBase(ctx context.Context, who domain.Actor, event, leaseID string) repository.AuditEntry {
	e := repository.AuditEntry{
		Kind: "agent", Event: event, ActorType: who.Kind, ClientID: who.ClientID, ClientLabel: who.Label,
		SessionID: who.SessionID, LeaseID: leaseID, TaskID: who.SessionID,
	}
	if step, ok := AgentStepFrom(ctx); ok && step.No > 0 {
		n := step.No
		e.StepNo = &n
	}
	return e
}

// recordJudge は Judge が allow 以外を返したことを agent.judge として記録する（result: deny は denied、require_approval は ok）。
func (c *control) recordJudge(ctx context.Context, who domain.Actor, tool string, procs []string, v domain.Verdict, r Reason) {
	if c.audit == nil {
		return
	}
	e := agentAuditBase(ctx, who, agentJudgeAuditEvent, "")
	e.TS = c.clock.Now()
	e.Tool = tool
	e.Result = auditOK
	detail := map[string]any{"judge": v.String(), "judge_reason": r.Code, "processes": procs}
	if step, ok := AgentStepFrom(ctx); ok && step.Route != "" {
		detail["route"] = step.Route
	}
	if v == domain.VerdictDeny {
		e.Result, e.ErrorCode = auditDenied, domain.TaskErrorJudgeDenied
	}
	e.DetailJSON = marshalDetail(detail)
	c.audit.Record(ctx, e)
}

// agentStepEntry は agent のアクション 1 件の agent.step の行を作る。args は describeOp の引数の要約（type の本文は
// audit.record_text でも入れない。FR-G23）。route・judge・judge_reason・approval は ExtraDetail・args より優先する。
func agentStepEntry(ctx context.Context, who domain.Actor, tool string, args map[string]any, leaseID string, err error, tr judgeTrace, took int64) repository.AuditEntry {
	e := agentAuditBase(ctx, who, agentStepAuditEvent, leaseID)
	e.Tool = tool
	e.Result = auditResult(err)
	e.DurationMS = &took
	if err != nil {
		e.ErrorCode = auditErrorCode(err)
	}
	detail := map[string]any{}
	if step, ok := AgentStepFrom(ctx); ok {
		maps.Copy(detail, step.ExtraDetail)
	}
	maps.Copy(detail, args)
	if step, ok := AgentStepFrom(ctx); ok && step.Route != "" {
		detail["route"] = step.Route
	}
	if tr.ran {
		detail["judge"] = tr.verdict.String()
		if tr.reason.Code != "" {
			detail["judge_reason"] = tr.reason.Code
		}
	}
	if tr.approval != "" {
		detail["approval"] = tr.approval
	}
	e.DetailJSON = marshalDetail(detail)
	return e
}
