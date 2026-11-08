package service

import (
	"context"
	"errors"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは Control.Do のスコープ付きリース（入力スコープ。#156。target.go の冒頭を参照）の検査・停止・再開。
//
// スコープ付きリースの Do は、スコープなしの検査 4（InputGuard.Check）の代わりに、次を行う:
//
//   - 実行前（lockInput の外。scopeGate）: ClipboardRead は常に clipboard_out_of_scope（platform のクリップボード読み取りは呼ばない。
//     書き込みだけ許可）。停止中（halted）は、Esc を含む一切の入力とクリップボード書き込みを target_halted で拒否する。win 修飾・win キー・
//     Alt+Tab・Alt+Esc・Ctrl+Esc・Ctrl+Shift+Esc は key_out_of_scope。これらはその操作だけの拒否で、停止にはしない。Ctrl+Alt+Delete（SAS）は
//     スコープなしと同じ secure_attention_sequence（domain.CheckKeyCombo・CheckKeyEvent）で、同じくその操作だけの拒否。
//   - 実行中（lockInput の中。verifyInput）: 入力の注入の直前と、1 つの操作の複数回の注入の間（MoveTo → click、押下 → 離す、
//     文字列は scopedTypeChunkRunes 文字ごと）に、T の同一性（target_closed / target_replaced）・T の状態（自ウィンドウ・昇格・最小化）・
//     キー・文字は前面とフォーカスの葉、座標は最上位の At と葉・Rect を対象グループ G（domain.ResolveGroup）で検証する。
//     そのうえで、操作の最初の注入の前に 1 回だけ、G の検証できたメンバー（最小化されていないもの）を GroupCapturer.GrabGroup で撮れるかを確かめる
//     （撮影の事前確認。モニター撮影・画面コピーには切り替えない。GroupCapturer を持たない Capturer は capture_unavailable）。
//   - どちらかに通らなければ、何も注入せず ReleaseAll を呼び、リースを halted にして ErrInputBlocked を返す（監査 input.target_halted）。
//     メンバーが増減するだけでは停止しない（次の入力は新しい G で検証する）。検証そのものが platform の失敗で終わるときは、その操作がすでに
//     注入していれば ReleaseAll を呼んでからエラーを返す（停止にはしない）。At を省略した Click は、lockInput の中で読み直したカーソル位置を検証する。
//
// 停止後は自動では戻らない。ResumeTarget が保持者の明示的な再開で、全条件を再検証して通ったときだけ active に戻す。

// scopedTypeChunkRunes は、スコープ付きリースの TypeText を検証する間隔（文字数）。この文字数ごとに前面・フォーカスを確かめ直すので、
// 入力の途中で前面が G の外に移っても、続きは注入しない（移ったあとに入るのは最大でこの文字数未満）。
const scopedTypeChunkRunes = 8

const targetHaltedAuditEvent = "input.target_halted"

// TargetStatus は VerifyTarget・ResumeTarget の結果（スコープ付きリースの状態）。
type TargetStatus struct {
	State domain.ScopeState
	// HaltReason は State が halted のときの停止の理由。
	HaltReason domain.GroupReason
	// Members は検証したときの G のメンバー数（T を含む）。停止中の VerifyTarget は検証しないので 0。
	Members int
	// Notices は通知（停止ではない）。所有関係を確かめられないウィンドウを対象の表示から除外したときは domain.NoticeCapturePartial。
	Notices []string
	// Excluded は対象の表示から除外した、所有関係を確かめられないウィンドウ（unverifiable。撮らず、画像に入れない）。
	// 前面・フォーカス先・座標の最上位（入力先）になったときは停止する。
	Excluded []domain.BlockedBy
}

func statusOf(state domain.ScopeState, halt domain.GroupReason, g domain.Group) TargetStatus {
	st := TargetStatus{State: state, HaltReason: halt, Members: len(g.Members)}
	for _, n := range g.Unverifiable() {
		st.Excluded = append(st.Excluded, n.BlockedBy())
	}
	if len(st.Excluded) > 0 {
		st.Notices = []string{domain.NoticeCapturePartial}
	}
	return st
}

// scopeRun は 1 回の操作（Do・VerifyTarget・ResumeTarget）のスコープ付きリースの検証の文脈。
type scopeRun struct {
	who     domain.Actor
	leaseID string
	target  domain.WindowRef
	// captured は撮影の事前確認が通ったか。操作の最初の検証でだけ確かめる。
	captured bool
	// injected は、この操作がすでに 1 回以上入力を注入したか（押下中のキー・ボタンが残りうる）。以降の検証が platform の失敗で
	// 終わるときは、ReleaseAll を呼んでから返す。
	injected bool
}

// markInjected は、この操作が入力を注入したことを記録する。スコープなしの操作（sr が nil）では何もしない。
func (sr *scopeRun) markInjected() {
	if sr != nil {
		sr.injected = true
	}
}

// scopeCheck は 1 回の検証で確かめる入力の宛先。
type scopeCheck struct {
	focus   bool           // キー・文字: 前面とフォーカスの葉
	points  []domain.Point // 座標（物理座標）: 最上位の At と葉・Rect
	focusID string         // FocusWindow の宛先の ID（G のメンバーだけ。メンバーでなければ停止にはせず拒否する）
}

// scopeGoverned は op がスコープの検証を受ける入力かを返す。クリップボード・UI ツリー・待ちは含まない
// （クリップボードの規則は scopeGate、UI ツリーはスコープなしと同じ InputGuard）。
func scopeGoverned(op domain.Op) bool {
	switch op.(type) {
	case domain.MouseMove, domain.Click, domain.Drag, domain.Scroll, domain.PointerMove, domain.MouseButton, domain.Wheel,
		domain.KeyEvent, domain.KeyPress, domain.TypeText, domain.FocusWindow:
		return true
	}
	return false
}

// opPoints は op が注入する先の座標（物理座標）。
func opPoints(op domain.Op) []domain.Point {
	switch o := op.(type) {
	case domain.MouseMove:
		return []domain.Point{o.At}
	case domain.Click:
		if o.At != nil {
			return []domain.Point{*o.At}
		}
	case domain.Drag:
		return []domain.Point{o.From, o.To}
	case domain.Scroll:
		return []domain.Point{o.At}
	case domain.PointerMove:
		return []domain.Point{o.At}
	case domain.MouseButton:
		return []domain.Point{o.At}
	case domain.Wheel:
		return []domain.Point{o.At}
	}
	return nil
}

// scopeGate はスコープ付きリースの、実行前の拒否（lockInput の外。停止にはしない）を返す。
func scopeGate(s domain.LeaseScope, op domain.Op) error {
	switch op.(type) {
	case domain.ClipboardRead:
		return scopeBlocked(domain.ReasonClipboardOutOfScope, domain.BlockedBy{})
	case domain.GetUITree:
		return nil // 入力ではない。スコープなしと同じ InputGuard が見る
	}
	if s.Halted() {
		return haltedError(s)
	}
	var forbidden bool
	switch o := op.(type) {
	case domain.KeyPress:
		if err := domain.CheckKeyCombo(o.Keys); err != nil {
			return err // Ctrl+Alt+Delete（SAS）はスコープなしと同じ拒否（NFR-S06）
		}
		forbidden = domain.ScopedKeyForbidden(o.Keys.Mods, o.Keys.Key)
	case domain.KeyEvent:
		if err := domain.CheckKeyEvent(o); err != nil {
			return err
		}
		forbidden = domain.ScopedKeyForbidden(o.Mods, o.Code) || domain.ScopedKeyForbidden(o.Mods, o.Key)
	case domain.Click:
		forbidden = domain.ScopedKeyForbidden(o.Mods, "")
	case domain.Drag:
		forbidden = domain.ScopedKeyForbidden(o.Mods, "")
	}
	if forbidden {
		return scopeBlocked(domain.ReasonKeyOutOfScope, domain.BlockedBy{})
	}
	return nil
}

// haltedError は停止中の拒否。reason は target_halted、detail.halt_reason に停止の理由を入れる。
func haltedError(s domain.LeaseScope) error {
	return scopeBlocked(domain.ReasonTargetHalted, domain.BlockedBy{}).WithDetail("halt_reason", string(s.HaltReason))
}

// check は G を求め、sr.target の同一性・T の状態・chk の宛先・撮影の事前確認を検証する。通らなければ理由（stop）を返す（ここでは停止にしない）。
// platform の失敗（ウィンドウ一覧・前面・At・ctx のキャンセル）は err で返す（注入しない。fail closed）。g は T を特定できなくても返す。
func (c *control) check(ctx context.Context, sr *scopeRun, chk scopeCheck) (g domain.Group, stop *scopeStop, err error) {
	list, err := c.windows.ListAll(ctx)
	if err != nil {
		return domain.Group{}, nil, err
	}
	g = domain.ResolveGroup(sr.target, list)
	if !g.Resolved() {
		return g, &scopeStop{reason: g.TargetReason, by: domain.BlockedBy{WindowID: sr.target.ID, PID: sr.target.PID}}, nil
	}
	if s, blocked := targetStateStop(g.Target); blocked {
		return g, &s, nil
	}
	if chk.focus {
		if stop, err = c.checkFocus(g); stop != nil || err != nil {
			return g, stop, err
		}
	}
	for _, p := range chk.points {
		if stop, err = c.checkPoint(g, p); stop != nil || err != nil {
			return g, stop, err
		}
	}
	if chk.focusID != "" {
		if _, ok := g.Member(chk.focusID); !ok {
			s := scopeStop{reason: domain.ReasonOutsideTargetGroup, by: domain.BlockedBy{WindowID: chk.focusID}, refuse: true}
			if n, found := g.NonMember(chk.focusID); found {
				s.by = n.BlockedBy()
			}
			return g, &s, nil
		}
	}
	if !sr.captured {
		if stop, err = c.precheckCapture(ctx, g); stop != nil || err != nil {
			return g, stop, err
		}
		sr.captured = true
	}
	return g, nil, nil
}

// checkFocus はキー・文字の宛先（前面と hwndFocus の葉）が G の中かを確かめる。自ウィンドウ・昇格ウィンドウは既存の reason。
func (c *control) checkFocus(g domain.Group) (*scopeStop, error) {
	f, err := c.windows.Foreground()
	if err != nil {
		return nil, err
	}
	if r := domain.InputBlockReason(f.Foreground); r != "" {
		return &scopeStop{reason: domain.GroupReason(r), by: byWindow(f.Foreground)}, nil
	}
	if b := g.CheckFocus(f); b.Blocked() {
		return &scopeStop{reason: b.Reason, by: b.By}, nil
	}
	return nil, nil
}

// checkPoint は座標 p の宛先（最上位の At と引き上げ前の葉）が G の中かを確かめる。
func (c *control) checkPoint(g domain.Group, p domain.Point) (*scopeStop, error) {
	hit, ok, err := c.windows.At(p)
	if err != nil {
		return nil, err
	}
	if !ok {
		hit = domain.HitInfo{} // どのウィンドウにもない（デスクトップ）
	}
	if r := domain.InputBlockReason(hit.Top); hit.Top.ID != "" && r != "" {
		return &scopeStop{reason: domain.GroupReason(r), by: byWindow(hit.Top)}, nil
	}
	if b := g.CheckHit(p, hit); b.Blocked() {
		return &scopeStop{reason: b.Reason, by: b.By}, nil
	}
	return nil, nil
}

// precheckCapture は G の検証できたメンバー（T と、最小化されていない・矩形が空でないメンバー）を GroupCapturer.GrabGroup で撮れるかを確かめる
// （画像は使わない）。T が撮れない・GroupCapturer が無い・撮影そのものの失敗は capture_unavailable、T 以外のメンバーが撮れないのは
// capture_member_unavailable。所有関係を確かめられないウィンドウ（unverifiable）は撮らない（表示から除外する通知だけ）。
// モニター撮影（Capturer.Grab・Watch）には切り替えない。ctx のキャンセルは停止にせず err で返す。
func (c *control) precheckCapture(ctx context.Context, g domain.Group) (*scopeStop, error) {
	targetBy := byWindow(g.Target)
	gc, ok := c.capture.(platform.GroupCapturer)
	if !ok {
		return &scopeStop{reason: domain.ReasonCaptureUnavailable, by: targetBy}, nil
	}
	var refs []domain.WindowRef
	for _, m := range g.Members {
		if m.Relation == domain.RelationTarget || !m.Window.Minimized && !m.Window.Rect.Empty() {
			refs = append(refs, domain.RefOf(m.Window))
		}
	}
	img, err := gc.GrabGroup(ctx, refs)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return &scopeStop{reason: domain.ReasonCaptureUnavailable, by: targetBy}, nil
	}
	for _, ref := range refs {
		if mc, found := img.Status(ref.ID); found && mc.Status == platform.CaptureOK {
			continue
		}
		m, _ := g.Member(ref.ID)
		reason := domain.ReasonCaptureMemberUnavailable
		if ref.ID == g.Target.ID {
			reason = domain.ReasonCaptureUnavailable
		}
		return &scopeStop{reason: reason, by: byWindow(m.Window)}, nil
	}
	return nil, nil
}

// scopeActive は sr のリースがまだ保持され、停止中でないことを確かめる（操作の間に解放・剥奪された、別の入力が停止させたときは注入しない）。
// スコープなしの操作（sr が nil）では何もしない。
func (c *control) scopeActive(sr *scopeRun) error {
	if sr == nil {
		return nil
	}
	h, ok := c.leases.Holder()
	switch {
	case !ok || h.LeaseID != sr.leaseID || h.Scope == nil:
		return domain.ErrLeaseRequired.WithMsg("the lease was released while the operation was running")
	case h.Scope.Halted():
		return haltedError(*h.Scope)
	}
	return nil
}

// verifyInput は入力の注入の直前の検証（スコープ付きリース）。scopeActive に通らなければ注入しない。検証に通らなければ
// halt（ReleaseAll・halted・監査）して ErrInputBlocked。スコープなしの操作（sr が nil）では何もしない。
// 検証そのものが platform の失敗（ウィンドウ一覧・前面・At・ctx のキャンセル）で終わるとき、この操作がすでに注入していれば
// （押下中のキー・ボタンが残りうる）、ReleaseAll を呼んでから返す。
func (c *control) verifyInput(ctx context.Context, sr *scopeRun, chk scopeCheck) error {
	if sr == nil {
		return nil
	}
	if err := c.scopeActive(sr); err != nil {
		return c.releaseIfInjected(sr, err)
	}
	g, stop, err := c.check(ctx, sr, chk)
	switch {
	case err != nil:
		return c.releaseIfInjected(sr, err)
	case stop == nil:
		return nil
	case stop.refuse:
		return c.withOwnWindows(ctx, stop.err())
	}
	return c.halt(ctx, sr, g, *stop)
}

// releaseIfInjected は sr の操作がすでに注入していれば ReleaseAll を呼び、err をそのまま返す。
func (c *control) releaseIfInjected(sr *scopeRun, err error) error {
	if sr.injected {
		if rerr := c.input.ReleaseAll(); rerr != nil {
			c.log.Warn("release input after the scoped verification failed", "error", rerr)
		}
	}
	return err
}

// halt は入力の検証・撮影の失敗でリースを停止にする: ReleaseAll → halted → 監査（input.target_halted）→ ErrInputBlocked。
// 注入は済んでいない（呼び出し側が、停止の前の注入だけを済ませている）。すでに停止中なら監査は重ねない。
func (c *control) halt(ctx context.Context, sr *scopeRun, g domain.Group, s scopeStop) error {
	if err := c.input.ReleaseAll(); err != nil {
		c.log.Warn("release input after the lease target halted failed", "error", err)
	}
	if c.leases.Halt(sr.leaseID, s.reason) {
		c.recordTargetHalted(ctx, sr, g, s)
	}
	return c.withOwnWindows(ctx, s.err())
}

// recordTargetHalted は input.target_halted を記録する（kind = input。result は denied）。detail は reason・window_id・process・pid（停止の原因のウィンドウ。
// なければ T）・メンバー数・target_window_id（T）だけで、ウィンドウのタイトル・入力した文字は入れない。
func (c *control) recordTargetHalted(ctx context.Context, sr *scopeRun, g domain.Group, s scopeStop) {
	if c.audit == nil {
		return
	}
	by := s.by
	if by.WindowID == "" {
		by = domain.BlockedBy{WindowID: sr.target.ID, PID: sr.target.PID, Process: g.Target.Process}
	}
	c.audit.Record(ctx, repository.AuditEntry{
		TS:          c.clock.Now(),
		Kind:        "input",
		Event:       targetHaltedAuditEvent,
		ActorType:   sr.who.Kind,
		ClientID:    sr.who.ClientID,
		ClientLabel: sr.who.Label,
		SessionID:   sr.who.SessionID,
		LeaseID:     sr.leaseID,
		Result:      auditDenied,
		ErrorCode:   string(domain.CodeInputBlocked),
		DetailJSON: marshalDetail(map[string]any{
			"reason": string(s.reason), "window_id": by.WindowID, "process": by.Process, "pid": by.PID,
			"members": len(g.Members), "target_window_id": sr.target.ID,
		}),
	})
}

// typeScoped は text を scopedTypeChunkRunes 文字ごとに、前面・フォーカスを検証してから注入する。
func (c *control) typeScoped(ctx context.Context, sr *scopeRun, text string) error {
	rs := []rune(text)
	for first := true; first || len(rs) > 0; first = false {
		n := min(len(rs), scopedTypeChunkRunes)
		if err := c.verifyInput(ctx, sr, scopeCheck{focus: true}); err != nil {
			return err
		}
		if err := c.input.Text(ctx, string(rs[:n])); err != nil {
			return err
		}
		sr.injected = true
		rs = rs[n:]
	}
	return nil
}

// targetLease は who のスコープ付きリース（VerifyTarget・ResumeTarget）を確かめ、検証の文脈と現在のスコープを返す。
// キルスイッチ中は ErrKillSwitchActive、保持者でなければ LeaseManager.Check のエラー、スコープなしのリースは ErrInvalidArgument。
func (c *control) targetLease(who domain.Actor) (*scopeRun, domain.LeaseScope, error) {
	if c.kill.Active() {
		return nil, domain.LeaseScope{}, domain.ErrKillSwitchActive
	}
	if err := c.leases.Check(who); err != nil {
		return nil, domain.LeaseScope{}, err
	}
	h, ok := c.leases.Holder()
	switch {
	case !ok || !h.Actor.SameSession(who):
		return nil, domain.LeaseScope{}, domain.ErrLeaseRequired
	case h.Scope == nil:
		return nil, domain.LeaseScope{}, domain.ErrInvalidArgument.WithMsg("the lease has no target")
	}
	return &scopeRun{who: who, leaseID: h.LeaseID, target: h.Scope.Target}, *h.Scope, nil
}

func (c *control) VerifyTarget(ctx context.Context, who domain.Actor) (TargetStatus, error) {
	sr, scope, err := c.targetLease(who)
	if err != nil {
		return TargetStatus{}, err
	}
	if scope.Halted() {
		return statusOf(domain.ScopeHalted, scope.HaltReason, domain.Group{}), nil
	}
	unlock, err := c.lockInput(ctx)
	if err != nil {
		return TargetStatus{}, err
	}
	defer unlock()
	g, stop, err := c.check(ctx, sr, scopeCheck{})
	switch {
	case err != nil:
		return TargetStatus{}, err
	case stop != nil:
		return statusOf(domain.ScopeHalted, stop.reason, g), c.halt(ctx, sr, g, *stop)
	}
	return statusOf(domain.ScopeActive, "", g), nil
}

func (c *control) ResumeTarget(ctx context.Context, who domain.Actor) (TargetStatus, error) {
	sr, scope, err := c.targetLease(who)
	if err != nil {
		return TargetStatus{}, err
	}
	if !scope.Halted() {
		return statusOf(scope.State, "", domain.Group{}), nil
	}
	if scope.HaltReason.ReselectOnly() {
		return statusOf(domain.ScopeHalted, scope.HaltReason, domain.Group{}),
			scopeBlocked(scope.HaltReason, domain.BlockedBy{WindowID: scope.Target.ID, PID: scope.Target.PID})
	}
	unlock, err := c.lockInput(ctx)
	if err != nil {
		return TargetStatus{}, err
	}
	defer unlock()
	g, stop, err := c.check(ctx, sr, scopeCheck{focus: true})
	switch {
	case err != nil:
		return TargetStatus{}, err
	case stop != nil:
		return statusOf(domain.ScopeHalted, scope.HaltReason, g), c.withOwnWindows(ctx, stop.err())
	}
	if err := c.leases.ResumeVerified(who); err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return statusOf(domain.ScopeHalted, scope.HaltReason, g), err
		}
		return TargetStatus{}, err
	}
	return statusOf(domain.ScopeActive, "", g), nil
}
