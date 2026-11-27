package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは ChatGPT へのサインイン（FR-G28）: 認可の要求をブラウザで開き、127.0.0.1 の受信口で結果を 1 回だけ受け取り、
// コードの交換・ID トークンの検証・保存までを行う。完了は非同期で、状態は Status、通知は Bus の toast で知らせる。

// chatgptSignIn は進行中のサインイン 1 回分。state・nonce・code_verifier はメモリだけに置き、終了で破棄する（NFR-S21 (3)）。
type chatgptSignIn struct {
	cb          *chatgptCallback
	ctx         context.Context
	cancel      context.CancelFunc
	verifier    string
	nonce       string
	redirectURI string
	clientID    string // 保存済みの client_id（無ければ空 = 新規登録）
	newReg      bool
}

// chatgptSignInOutcome はサインインの失敗・拒否の分類（監査と GUI のカードの Problem）。
type chatgptSignInOutcome struct {
	problem PlanProblem
	result  string // auditDenied / auditError
	reason  string // 監査の detail.reason
	toast   string // 利用者向けの日本語（秘密を含まない）
}

var errChatGPTSignInSuperseded = errors.New("service: the chatgpt sign-in is no longer current")

// StartSignIn はサインインを始める（FR-G28）。状態が signed_out・reauth_required 以外なら CodeConflict。
// ブラウザを開けなければ、始める前の状態のまま失敗する。
func (s *ChatGPTAuthService) StartSignIn(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("service: chatgpt auth is closed")
	}
	if s.state != PlanSignedOut && s.state != PlanReauthRequired {
		s.mu.Unlock()
		return chatgptConflict("chatgpt sign-in cannot start in the current state")
	}
	prevState, prevProblem := s.state, s.problem
	s.state, s.problem = PlanSigningIn, PlanProblemNone
	s.mu.Unlock()

	sess, err := s.prepareSignIn(ctx, prevProblem == PlanProblemScopeMissing)
	if err != nil {
		s.mu.Lock()
		if s.state == PlanSigningIn && s.signIn == nil {
			s.state, s.problem = prevState, prevProblem
		}
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		sess.cancel()
		sess.cb.close()
		return errors.New("service: chatgpt auth is closed")
	}
	s.signIn = sess
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runSignIn(sess)
	s.audit(ctx, domain.ActorHost, chatgptAuditSignInStart, auditOK, "", nil)
	return nil
}

// prepareSignIn はホスト ID・秘密の値・受信口を用意し、認可の URL をブラウザで開く。
func (s *ChatGPTAuthService) prepareSignIn(ctx context.Context, consent bool) (*chatgptSignIn, error) {
	hostID, err := s.getSetting(ctx, SettingChatGPTHostID)
	if err != nil {
		return nil, err
	}
	if hostID == "" { // 最初のサインインの前に作って保存し、以後は変えない（FR-G28 (2)）
		if hostID, err = newHostID(); err != nil {
			return nil, err
		}
		if err := s.putSetting(ctx, SettingChatGPTHostID, hostID); err != nil {
			return nil, err
		}
	}
	clientID, err := s.getSetting(ctx, SettingChatGPTClientID)
	if err != nil {
		return nil, err
	}
	loginHint, err := s.getSetting(ctx, SettingChatGPTAccountEmail)
	if err != nil {
		return nil, err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}

	ln, err := s.listenCallback()
	if err != nil {
		return nil, err
	}
	cb := newChatGPTCallback(ln, state)
	sessCtx, cancel := context.WithCancel(s.baseCtx)
	sess := &chatgptSignIn{
		cb: cb, ctx: sessCtx, cancel: cancel, verifier: verifier, nonce: nonce,
		redirectURI: "http://" + cb.hostPort + chatgptCallbackPath, clientID: clientID, newReg: clientID == "",
	}
	authClient := clientID
	if sess.newReg {
		authClient = chatgptDynamicClientID
	}
	target := s.authorizeURL(chatgptAuthorizeParams{
		ClientID: authClient, NewClient: sess.newReg, HostID: hostID, LoginHint: loginHint, RedirectURI: sess.redirectURI,
		State: state, Nonce: nonce, Challenge: pkceChallenge(verifier), Consent: consent,
	})
	if err := s.cfg.Browser.OpenURL(ctx, target); err != nil {
		cancel()
		cb.close()
		return nil, fmt.Errorf("service: open the browser for the chatgpt sign-in: %w", err)
	}
	return sess, nil
}

// listenCallback は 127.0.0.1 の候補のポートを順に試す（使用中は次へ）。
func (s *ChatGPTAuthService) listenCallback() (net.Listener, error) {
	var last error
	for _, p := range s.cfg.Ports {
		ln, err := s.cfg.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err != nil {
			last = err
			continue
		}
		if _, ok := ln.Addr().(*net.TCPAddr); !ok {
			_ = ln.Close()
			last = errors.New("the listener is not a TCP listener")
			continue
		}
		return ln, nil
	}
	if last == nil {
		last = errors.New("no port to try")
	}
	return nil, fmt.Errorf("service: no free port for the chatgpt sign-in callback: %w", last)
}

// CancelSignIn は進行中のサインインを止めて受信口を閉じる（冪等）。
func (s *ChatGPTAuthService) CancelSignIn() {
	s.mu.Lock()
	sess := s.signIn
	if sess == nil {
		s.mu.Unlock()
		return
	}
	s.signIn, s.state, s.problem = nil, PlanSignedOut, PlanProblemNone
	s.mu.Unlock()
	sess.cancel()
	sess.cb.close()
	s.audit(context.Background(), domain.ActorHost, chatgptAuditSignIn, auditDenied, "", map[string]any{"reason": "cancelled"})
}

// runSignIn は受信口の結果を待ち、サインインを完了させる。
func (s *ChatGPTAuthService) runSignIn(sess *chatgptSignIn) {
	defer s.wg.Done()
	defer sess.cancel()
	waitCtx, cancel := context.WithTimeout(sess.ctx, s.cfg.SignInTimeout)
	res, err := sess.cb.wait(waitCtx)
	timedOut := errors.Is(waitCtx.Err(), context.DeadlineExceeded) && sess.ctx.Err() == nil
	cancel()
	sess.cb.close() // 結果を 1 回受け取った・時間切れ・中止のいずれでも閉じる
	if err != nil {
		if timedOut {
			s.failSignIn(sess, chatgptSignInOutcome{
				problem: PlanProblemTimeout, result: auditError, reason: "timeout",
				toast: "ChatGPT のサインインが時間内に終わりませんでした。",
			})
		}
		return // 中止・アプリの終了は CancelSignIn・Close が状態を直す
	}
	if out, ok := s.finishSignIn(sess, res); !ok {
		s.failSignIn(sess, out)
	}
}

// failSignIn はサインインを signed_out で終える（このサインインがまだ今のものであるときだけ）。
func (s *ChatGPTAuthService) failSignIn(sess *chatgptSignIn, o chatgptSignInOutcome) {
	s.mu.Lock()
	if s.signIn != sess {
		s.mu.Unlock()
		return
	}
	s.signIn, s.state, s.problem = nil, PlanSignedOut, o.problem
	s.mu.Unlock()
	s.audit(context.Background(), domain.ActorHost, chatgptAuditSignIn, o.result, "", map[string]any{"reason": o.reason})
	s.toast("error", o.toast)
}

func signInFailure(problem PlanProblem, reason, toast string) (chatgptSignInOutcome, bool) {
	return chatgptSignInOutcome{problem: problem, result: auditError, reason: reason, toast: toast}, false
}

// finishSignIn はコールバックの結果を検証してトークンを保存する（FR-G28 (4)〜(7)）。成功は ok = true。
func (s *ChatGPTAuthService) finishSignIn(sess *chatgptSignIn, res chatgptCallbackResult) (chatgptSignInOutcome, bool) {
	ctx := sess.ctx
	switch {
	case res.ErrCode == "access_denied":
		return chatgptSignInOutcome{
			problem: PlanProblemAccessDenied, result: auditDenied, reason: "access_denied",
			toast: "ChatGPT のサインインが許可されませんでした。",
		}, false
	case chatgptIsPolicyDeniedCode(res.ErrCode):
		return chatgptSignInOutcome{
			problem: PlanProblemPolicyDenied, result: auditDenied, reason: "policy_denied",
			toast: "ワークスペースのポリシーにより、このサインインは許可されていません。",
		}, false
	case res.ErrCode != "":
		return signInFailure(PlanProblemFailed, "authorization_failed", "ChatGPT のサインインに失敗しました。")
	}

	clientID := sess.clientID
	if sess.newReg {
		if res.ClientID == "" { // 新規登録なのに client_id が発行されなかった
			return signInFailure(PlanProblemFailed, "registration_incomplete", "ChatGPT へのアプリの登録を完了できませんでした。")
		}
		clientID = res.ClientID
	} else if res.ClientID != "" && res.ClientID != sess.clientID { // 再認可で別の client_id が返れば拒否する
		return signInFailure(PlanProblemFailed, "registration_incomplete", "ChatGPT のサインインに失敗しました。")
	}

	tok, err := s.exchangeCode(ctx, clientID, res.Code, sess.verifier, sess.redirectURI)
	if err != nil {
		var he *chatgptHTTPError
		if errors.As(err, &he) && chatgptIsPolicyDeniedCode(he.Code) {
			return chatgptSignInOutcome{
				problem: PlanProblemPolicyDenied, result: auditDenied, reason: "policy_denied",
				toast: "ワークスペースのポリシーにより、このサインインは許可されていません。",
			}, false
		}
		return signInFailure(PlanProblemFailed, "token_exchange_failed", "ChatGPT のサインインに失敗しました。")
	}
	if tok.IDToken == "" {
		return signInFailure(PlanProblemFailed, "id_token_invalid", "ChatGPT のサインインの検証に失敗しました。")
	}
	claims, err := s.verifyIDToken(ctx, tok.IDToken, clientID, sess.nonce)
	if err != nil {
		if errors.Is(err, errChatGPTIDToken) {
			return signInFailure(PlanProblemFailed, "id_token_invalid", "ChatGPT のサインインの検証に失敗しました。")
		}
		return signInFailure(PlanProblemFailed, "token_exchange_failed", "ChatGPT のサインインに失敗しました。")
	}
	savedSub, err := s.getSetting(ctx, SettingChatGPTAccountSub)
	if err != nil {
		return signInFailure(PlanProblemFailed, "token_exchange_failed", "ChatGPT のサインインに失敗しました。")
	}
	if savedSub != "" && savedSub != claims.Subject {
		return signInFailure(PlanProblemAccountMismatch, "account_mismatch",
			"以前サインインしたものとは別の ChatGPT アカウントです。解除で「別のアカウントでサインインする」を選んでください。")
	}

	scope := tok.Scope
	if scope == "" {
		scope = res.Scope
	}
	if !strings.Contains(" "+scope+" ", " "+chatgptScopeUseDirect+" ") {
		// 使用が許可されなかった: トークンは保存せず、client_id と sub だけ残す（FR-G28 (6)）。
		if err := s.commitSignIn(sess, clientID, claims, nil); err != nil {
			return signInFailure(PlanProblemFailed, "token_save_failed", "ChatGPT のサインインの結果を保存できませんでした。")
		}
		return signInFailure(PlanProblemScopeMissing, "plan_use_not_granted", "ChatGPT のプランの使用が許可されていません。")
	}
	expiresIn := chatgptDefaultExpiresIn
	if tok.ExpiresIn > 0 {
		expiresIn = time.Duration(tok.ExpiresIn) * time.Second
	}
	rec := chatgptTokenRecord{
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, IDToken: tok.IDToken,
		ExpiresAt: s.cfg.Clock.Now().Add(expiresIn), Scope: scope,
	}
	if rec.RefreshToken == "" { // リフレッシュトークンが無ければ維持できない
		return signInFailure(PlanProblemFailed, "token_exchange_failed", "ChatGPT のサインインに失敗しました。")
	}
	if err := s.commitSignIn(sess, clientID, claims, &rec); err != nil {
		if errors.Is(err, errChatGPTSignInSuperseded) {
			return chatgptSignInOutcome{}, false // 中止・終了。failSignIn も今のサインインでなければ何もしない
		}
		return signInFailure(PlanProblemFailed, "token_save_failed", "ChatGPT のサインインの結果を保存できませんでした。")
	}
	s.audit(ctx, domain.ActorHost, chatgptAuditSignIn, auditOK, "", map[string]any{"new_registration": sess.newReg})
	s.toast("info", "ChatGPT にサインインしました。")
	return chatgptSignInOutcome{}, true
}

// commitSignIn は検証済みのサインインの結果を保存する。rec が nil なら client_id と sub だけ（scope が足りなかったとき）。
// このサインインが今のものでなければ errChatGPTSignInSuperseded（中止・終了の後に保存しない）。
func (s *ChatGPTAuthService) commitSignIn(sess *chatgptSignIn, clientID string, claims chatgptIDClaims, rec *chatgptTokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signIn != sess || s.closed {
		return errChatGPTSignInSuperseded
	}
	ctx := s.baseCtx
	if sess.newReg {
		if err := s.putSetting(ctx, SettingChatGPTClientID, clientID); err != nil {
			return err
		}
	}
	if err := s.putSetting(ctx, SettingChatGPTAccountSub, claims.Subject); err != nil {
		return err
	}
	if rec == nil {
		return nil
	}
	if claims.Email != "" {
		if err := s.putSetting(ctx, SettingChatGPTAccountEmail, claims.Email); err != nil {
			return err
		}
		s.email = claims.Email
	}
	if err := s.tokens.save(*rec); err != nil {
		return err
	}
	s.rec, s.state, s.problem, s.signIn = rec, PlanConnected, PlanProblemNone, nil
	s.gen++
	return nil
}
