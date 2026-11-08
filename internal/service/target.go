package service

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// このファイルは入力スコープ（リースに束縛した 1 つのウィンドウ T と、その対象グループ G だけに入力を限る仕組み。#156）の、
// LeaseManager（束縛の検査）と Control（入力の検証・停止・再開。control_target.go）が共有する部品。
//
// スコープは誤入力を止めるガードレールで、セキュリティ境界ではない（認可済みクライアントの screenshot・モニター表示は止めない）。
// 停止（halted）の原因は 2 つだけ: (1) 入力の直前の検証の失敗、(2) T または検証できたメンバーの撮影の失敗。
// 復帰は自動では起きず、保持者の明示的な再開（Control.ResumeTarget）だけが全条件を再検証して active に戻す。

// scopeStop はスコープの検証に通らなかったときの理由と、原因のウィンドウ。
type scopeStop struct {
	reason domain.GroupReason
	by     domain.BlockedBy
	// refuse が true なら、その操作だけの拒否で停止にはしない（FocusWindow がメンバー以外を宛先にした）。
	refuse bool
}

// err は s を ErrInputBlocked にする。detail は reason（API 仕様 §3.4）、原因のウィンドウがあれば window_id と
// blocked_by{window_id, process, pid, reason}。ウィンドウのタイトルは入れない。
func (s scopeStop) err() error { return scopeBlocked(s.reason, s.by) }

func scopeBlocked(reason domain.GroupReason, by domain.BlockedBy) *domain.Error {
	e := domain.ErrInputBlocked.
		WithMsg("input is outside the lease target scope: "+string(reason)).
		WithDetail("reason", string(reason))
	if by.WindowID == "" && by.PID == 0 && by.Process == "" && by.Reason == "" {
		return e
	}
	if by.WindowID != "" {
		e = e.WithDetail("window_id", by.WindowID)
	}
	return e.WithDetail("blocked_by", map[string]any{
		"window_id": by.WindowID, "process": by.Process, "pid": by.PID, "reason": string(by.Reason),
	})
}

// byWindow は w を blocked_by の原因として表す（分類の理由は付けない）。
func byWindow(w domain.WindowInfo) domain.BlockedBy {
	return domain.BlockedBy{WindowID: w.ID, Process: w.Process, PID: w.PID}
}

// targetStateStop は T 自身の状態（自ウィンドウ・昇格・最小化。既存の reason と target_minimized）で入力できないとき、その理由を返す。
func targetStateStop(t domain.WindowInfo) (scopeStop, bool) {
	switch {
	case domain.InputBlockReason(t) != "":
		return scopeStop{reason: domain.GroupReason(domain.InputBlockReason(t)), by: byWindow(t)}, true
	case t.Minimized:
		return scopeStop{reason: domain.ReasonTargetMinimized, by: byWindow(t)}, true
	}
	return scopeStop{}, false
}

// bindTarget は束縛する対象 ref を検査し、スコープの初期値（active）を返す。選べないとき（束縛しない）:
//   - 一覧にない・同じ ID の別のウィンドウ → ErrWindowNotFound（reason target_closed / target_replaced）
//   - 検証前の UWP（ApplicationFrameWindow）・同一性を確かめる値（PID・開始時刻・クラス）が不明 → ErrInputBlocked（target_unsupported）
//   - 自ウィンドウ・昇格・最小化 → ErrInputBlocked（own_window / elevated_window / target_minimized）
func bindTarget(ctx context.Context, windows platform.WindowManager, ref domain.WindowRef) (domain.LeaseScope, error) {
	if windows == nil {
		return domain.LeaseScope{}, domain.ErrInvalidArgument.WithMsg("scoped leases are not available")
	}
	list, err := windows.ListAll(ctx)
	if err != nil {
		return domain.LeaseScope{}, err
	}
	g := domain.ResolveGroup(ref, list)
	switch g.TargetReason {
	case "":
	case domain.ReasonTargetClosed, domain.ReasonTargetReplaced:
		return domain.LeaseScope{}, domain.ErrWindowNotFound.
			WithMsg("the target window is not available: "+string(g.TargetReason)).
			WithDetail("reason", string(g.TargetReason))
	default:
		return domain.LeaseScope{}, scopeBlocked(g.TargetReason, domain.BlockedBy{WindowID: ref.ID, PID: ref.PID})
	}
	if s, blocked := targetStateStop(g.Target); blocked {
		return domain.LeaseScope{}, s.err()
	}
	return domain.LeaseScope{Target: ref, Process: g.Target.Process, Members: len(g.Members), State: domain.ScopeActive}, nil
}

// bindScope は AcquireScoped・RequestAcquireScoped の束縛の検査。失敗は、取得の失敗と同じく監査（lease.acquire の denied）とログに残す。
func (m *leaseManager) bindScope(ctx context.Context, who domain.Actor, target domain.WindowRef) (domain.LeaseScope, error) {
	scope, err := bindTarget(ctx, m.windows, target)
	if err != nil {
		m.logDenied(who, err)
		m.record(ctx, leaseDeniedAuditEntry(who, m.clock.Now(), err))
	}
	return scope, err
}
