package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// maybeRefresh はエンジンが導入済みでアカウントの状態をまだ読んでいないとき、背景で読み始める（app-server を起動する）。
// onlyIfSelected のときは、接続方式が chatgpt-codex のときだけ（起動直後・画面を開いただけで、使わない app-server を起こさない。FR-G37 (1)）。
func (s *CodexAuthService) maybeRefresh(onlyIfSelected bool) {
	s.mu.Lock()
	if s.known || s.refreshing || s.closed || s.removing || s.eng.state != CodexEngineInstalled {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if onlyIfSelected && !s.connectionIsCodex(s.baseCtx) {
		return
	}
	s.mu.Lock()
	if s.known || s.refreshing || s.closed || s.removing || s.eng.state != CodexEngineInstalled {
		s.mu.Unlock()
		return
	}
	s.refreshing = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		err := s.readAccount(s.baseCtx, false)
		s.mu.Lock()
		s.refreshing = false
		s.known = true // 読めなかったときも、再試行の連打をしない（ログインの操作・導入で改めて読む）
		s.mu.Unlock()
		if err != nil {
			s.log.Warn("codex account read failed", "err_kind", errKind(err))
		}
	}()
}

// readAccount は app-server に account/read（更新は頼まない）を頼み、結果を状態へ反映する。
// finishSignIn は、ログインの完了の処理（account/login/completed）から読むとき: ログインの途中の状態は、これだけが終わらせる。
func (s *CodexAuthService) readAccount(ctx context.Context, finishSignIn bool) error {
	se, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	var acc protocol.GetAccountResult
	if err := se.Call(ctx, protocol.MethodAccountRead, protocol.GetAccountParams{RefreshToken: false}, &acc); err != nil {
		return err
	}
	s.applyAccount(acc.Account, finishSignIn)
	return nil
}

// applyAccount は読んだアカウント（nil はログインなし）を状態へ反映する。
func (s *CodexAuthService) applyAccount(acc *protocol.Account, finishSignIn bool) {
	s.mu.Lock()
	s.known = true
	var toastMsg string
	if acc == nil {
		s.email, s.planType = "", ""
		if s.state == PlanConnected || s.state == PlanUsageLimited {
			s.state = PlanReauthRequired
			toastMsg = "Codex のログインが切れました。もう一度ログインしてください"
		}
	} else {
		s.email, s.planType = acc.Email, acc.PlanType
		if s.state != PlanSigningIn || finishSignIn {
			if s.state != PlanUsageLimited {
				s.state = PlanConnected
			}
			s.problem = PlanProblemNone
		}
	}
	s.mu.Unlock()
	if toastMsg != "" {
		s.toast("error", toastMsg)
	}
}

// onAccountNotification は app-server のアカウントの通知（読み取りの goroutine で呼ばれるので、ブロックしない）。
func (s *CodexAuthService) onAccountNotification(method string, params json.RawMessage) {
	switch method {
	case protocol.NotifLoginCompleted:
		var n protocol.LoginCompleted
		if json.Unmarshal(params, &n) != nil {
			return
		}
		s.goBackground(func() { s.completeSignIn(n) })
	case protocol.NotifAccountUpdated:
		var n protocol.AccountUpdated
		if json.Unmarshal(params, &n) != nil {
			return
		}
		if n.AuthMode == nil { // ログアウト
			s.goBackground(func() { s.applyAccount(nil, false) })
			return
		}
		s.goBackground(func() { _ = s.readAccount(s.baseCtx, false) })
	}
}

func (s *CodexAuthService) goBackground(f func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		f()
	}()
}

// StartSignIn は app-server の ChatGPT のログイン（ブラウザ）を始める。完了は非同期（Status で読む）。
func (s *CodexAuthService) StartSignIn(ctx context.Context) error {
	s.mu.Lock()
	switch {
	case s.closed || s.removing:
		s.mu.Unlock()
		return conflict("Codex は今、使えません")
	case s.eng.state != CodexEngineInstalled:
		s.mu.Unlock()
		return conflict("先に Codex のエンジンを取得してください")
	case s.state != PlanSignedOut && s.state != PlanReauthRequired:
		s.mu.Unlock()
		return conflict("Codex は、すでにログインしているか、ログインの途中です")
	}
	s.prevState, s.state, s.problem = s.state, PlanSigningIn, PlanProblemNone
	s.mu.Unlock()

	s.record(codexSignInStartAuditEvent, auditOK, nil)
	revert := func(problem PlanProblem) {
		s.mu.Lock()
		if s.state == PlanSigningIn {
			s.state, s.problem = s.prevState, problem
		}
		s.mu.Unlock()
	}

	se, err := s.ensure(ctx)
	if err != nil {
		revert(PlanProblemFailed)
		return engineUnavailableError(err)
	}
	var res protocol.LoginResult
	if err := se.Call(ctx, protocol.MethodLoginStart, protocol.LoginParams{Type: protocol.LoginBrowser}, &res); err != nil {
		revert(PlanProblemFailed)
		return domain.NewError(domain.CodeInternal, "Codex のログインを始められませんでした", nil)
	}
	if !isHTTPSURL(res.AuthURL) {
		s.cancelLogin(res.LoginID)
		revert(PlanProblemFailed)
		return domain.NewError(domain.CodeInternal, "Codex のログインの URL が安全ではないため、開きませんでした", nil)
	}
	if err := s.cfg.Browser.OpenURL(ctx, res.AuthURL); err != nil {
		s.cancelLogin(res.LoginID)
		revert(PlanProblemFailed)
		return domain.NewError(domain.CodeInternal, "ブラウザを開けませんでした", nil)
	}

	tctx, stop := context.WithTimeout(s.baseCtx, s.cfg.SignInTimeout)
	s.mu.Lock()
	if s.state != PlanSigningIn || s.closed { // ブラウザを開いている間に、取り消し・完了・アプリの終了があった（Close の待ちの後に wg を増やさない）
		s.mu.Unlock()
		stop()
		return nil
	}
	s.loginID, s.signInStop = res.LoginID, stop
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer stop()
		<-tctx.Done()
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			s.failSignIn(res.LoginID, PlanProblemTimeout, "timeout")
		}
	}()
	return nil
}

func isHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// engineUnavailableError は app-server を起動できなかった失敗（理由の本文・パスは出さない）。
func engineUnavailableError(err error) error {
	if errors.Is(err, errCodexEngineMissing) {
		return conflict("Codex のエンジンを使えません。取得し直してください")
	}
	if errors.Is(err, appserver.ErrNotOurHome) {
		return domain.NewError(domain.CodeInternal, "Codex が専用の保存場所を使っていないため、止めました", nil)
	}
	return domain.NewError(domain.CodeInternal, "Codex のエンジンを起動できませんでした", nil)
}

// cancelLogin は app-server のログインの取り消しを頼む（ベストエフォート。動いていなければ何もしない）。
func (s *CodexAuthService) cancelLogin(loginID string) {
	se := s.srv.Current()
	if se == nil || loginID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(s.baseCtxOrBackground(), codexCleanupTimeout)
	defer cancel()
	_ = se.Call(ctx, protocol.MethodLoginCancel, protocol.CancelLoginParams{LoginID: loginID}, nil)
}

// CancelSignIn は進行中のログインを止める（冪等）。
func (s *CodexAuthService) CancelSignIn(_ context.Context) {
	s.mu.Lock()
	if s.state != PlanSigningIn {
		s.mu.Unlock()
		return
	}
	loginID, stop := s.loginID, s.signInStop
	s.state, s.problem, s.loginID, s.signInStop = s.prevState, PlanProblemNone, "", nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	s.cancelLogin(loginID)
	s.record(codexSignInAuditEvent, auditDenied, map[string]any{"reason": "cancelled"})
}

// failSignIn はログインの失敗（時間切れなど）で、サインインの前の状態へ戻す。loginID が今のものと違えば何もしない。
func (s *CodexAuthService) failSignIn(loginID string, problem PlanProblem, reason string) {
	s.mu.Lock()
	if s.state != PlanSigningIn || s.loginID != loginID {
		s.mu.Unlock()
		return
	}
	s.state, s.problem, s.loginID, s.signInStop = s.prevState, problem, "", nil
	s.mu.Unlock()
	s.cancelLogin(loginID)
	s.record(codexSignInAuditEvent, auditError, map[string]any{"reason": reason})
	s.toast("error", "Codex のログインが完了しませんでした")
}

// completeSignIn は account/login/completed の処理。成功ならアカウントを読んで connected にする。
func (s *CodexAuthService) completeSignIn(n protocol.LoginCompleted) {
	s.mu.Lock()
	if s.state != PlanSigningIn || (n.LoginID != nil && s.loginID != "" && *n.LoginID != s.loginID) {
		s.mu.Unlock()
		return
	}
	stop := s.signInStop
	s.loginID, s.signInStop = "", nil
	if !n.Success {
		s.state, s.problem = s.prevState, PlanProblemFailed
		reason, result := "failed", auditError
		if n.Error != nil && strings.Contains(strings.ToLower(*n.Error), "access_denied") {
			s.problem, reason, result = PlanProblemAccessDenied, "access_denied", auditDenied
		}
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
		s.record(codexSignInAuditEvent, result, map[string]any{"reason": reason})
		s.toast("error", "Codex のログインに失敗しました")
		return
	}
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if err := s.readAccount(s.baseCtx, true); err != nil {
		s.mu.Lock()
		s.state, s.problem = s.prevState, PlanProblemFailed
		s.mu.Unlock()
		s.record(codexSignInAuditEvent, auditError, map[string]any{"reason": "account_read_failed"})
		s.toast("error", "Codex のログイン後にアカウントを確認できませんでした")
		return
	}
	s.mu.Lock()
	ok := s.state == PlanConnected || s.state == PlanUsageLimited
	if !ok {
		s.state, s.problem = s.prevState, PlanProblemFailed
	}
	s.mu.Unlock()
	if !ok {
		s.record(codexSignInAuditEvent, auditError, map[string]any{"reason": "no_account"})
		return
	}
	s.record(codexSignInAuditEvent, auditOK, nil)
	s.toast("info", "Codex にログインしました")
}

// SignOut は app-server にログアウトを頼み（実行中の Codex 経路のタスクは中断）、removeEngine ならエンジンと専用の CODEX_HOME も消す。
func (s *CodexAuthService) SignOut(ctx context.Context, removeEngine bool) error {
	s.mu.Lock()
	switch {
	case s.closed || s.removing:
		s.mu.Unlock()
		return conflict("Codex は今、使えません")
	case s.eng.state == CodexEngineInstalling:
		s.mu.Unlock()
		return conflict("Codex のエンジンを取得中です")
	case s.state == PlanSignedOut && !removeEngine:
		s.mu.Unlock()
		return conflict("Codex にログインしていません")
	}
	wasSigned := s.state != PlanSignedOut
	loginID, stop := s.loginID, s.signInStop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}

	detail := map[string]any{"remove_engine": removeEngine}
	if s.cfg.CancelTasks != nil {
		if id := s.cfg.CancelTasks(codexCancelTasksReason); id != "" {
			detail["cancelled_task"] = id
		}
	}
	if loginID != "" {
		s.cancelLogin(loginID)
	}

	// ログアウト（エンジンが使えるときだけ）。資格情報を消せなかったときは、何も消さず失敗にする（削除つきのときは続ける）。
	if wasSigned && s.engineUsable() {
		if err := s.logout(ctx); err != nil && !removeEngine {
			s.record(codexSignOutAuditEvent, auditError, detail)
			return domain.NewError(domain.CodeInternal, "Codex からログアウトできませんでした", nil)
		} else if err != nil {
			detail["logout_failed"] = true
		}
	}

	if removeEngine {
		// 削除の間は、取得・起動を断る（ログアウトは済んでいるので、ここから起動を止めてよい）。
		s.mu.Lock()
		if s.eng.state == CodexEngineInstalling || s.removing {
			s.mu.Unlock()
			return conflict("Codex のエンジンを取得中か、削除中です")
		}
		s.removing = true
		s.mu.Unlock()
		if err := s.removeEngineFiles(); err != nil {
			s.record(codexSignOutAuditEvent, auditError, detail)
			s.mu.Lock()
			s.removing = false
			s.mu.Unlock()
			return domain.NewError(domain.CodeInternal, "Codex のエンジンを削除できませんでした", nil)
		}
	} else {
		s.srv.Stop() // 次に使うときに起動し直す
	}

	s.mu.Lock()
	s.removing = false
	s.state, s.problem, s.email, s.planType, s.loginID, s.signInStop = PlanSignedOut, PlanProblemNone, "", "", "", nil
	s.known = true
	if removeEngine {
		s.eng = codexEngineInfo{state: s.engineStateOnDisk()}
		s.known = false
	}
	s.mu.Unlock()
	s.record(codexSignOutAuditEvent, auditOK, detail)
	s.toast("info", "Codex からログアウトしました")
	return nil
}

func (s *CodexAuthService) engineUsable() bool {
	if s.inst == nil {
		return false
	}
	_, ok := s.inst.ExePath()
	return ok
}

// logout は account/logout を頼む。
func (s *CodexAuthService) logout(ctx context.Context) error {
	se, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	return se.Call(ctx, protocol.MethodLogout, struct{}{}, nil)
}

// removeEngineFiles は app-server を止め、取得したエンジンと専用の CODEX_HOME・作業ディレクトリを消す。
func (s *CodexAuthService) removeEngineFiles() error {
	s.srv.Stop() // 通知の処理が済んでから消す（Stop は読み取りの終わりを待つ）
	if s.inst != nil {
		if err := s.inst.Remove(); err != nil {
			return err
		}
	}
	for _, d := range []string{s.home, s.work} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}

// Resume は usage_limited を connected に戻す。
func (s *CodexAuthService) Resume(_ context.Context) error {
	s.mu.Lock()
	if s.state != PlanUsageLimited {
		s.mu.Unlock()
		return conflict("Codex は使用量の上限の状態ではありません")
	}
	s.state = PlanConnected
	s.mu.Unlock()
	s.record(codexResumeAuditEvent, auditOK, nil)
	return nil
}

// Models は app-server の model/list（connected でなければ CodeConflict）。表示しないモデル（hidden）は除く。
func (s *CodexAuthService) Models(ctx context.Context) ([]PlanModel, error) {
	s.mu.Lock()
	ok := s.state == PlanConnected || s.state == PlanUsageLimited
	s.mu.Unlock()
	if !ok {
		return nil, conflict("Codex にログインしていません")
	}
	se, err := s.ensure(ctx)
	if err != nil {
		return nil, engineUnavailableError(err)
	}
	var out []PlanModel
	var cursor *string
	for range 20 { // ページの上限（無限のカーソルで止まらなくならないように）
		var res protocol.ModelListResult
		if err := se.Call(ctx, protocol.MethodModelList, protocol.ModelListParams{Cursor: cursor}, &res); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, domain.NewError(domain.CodeInternal, "Codex のモデルの一覧を取得できませんでした", nil)
		}
		for _, m := range res.Data {
			if m.Hidden || m.Model == "" {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = m.Model
			}
			out = append(out, PlanModel{Slug: m.Model, DisplayName: name})
		}
		if res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}
