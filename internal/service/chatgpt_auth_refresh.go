package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

// このファイルは ChatGPT のアクセストークンの維持（FR-G29 (2)〜(5)）: 残りが 5 分以下なら更新（排他・10 秒・自動再試行なし）、
// 回転するトークンはまとめて置き換える。使えないと確認できたらトークンを全削除して reauth_required。通信の失敗・5xx では消さない。

// chatgptUnauthorized は、使えないトークンの失敗（openai.ErrPlanUnauthorized を包む。エージェントが chatgpt_unauthorized にする）。
func chatgptUnauthorized() error {
	return fmt.Errorf("service: chatgpt authorization is not usable: %w", openai.ErrPlanUnauthorized)
}

func (s *ChatGPTAuthService) expiresSoon(rec chatgptTokenRecord) bool {
	return rec.ExpiresAt.Sub(s.cfg.Clock.Now()) <= chatgptRefreshEarly
}

// currentRecord は使える状態（connected・usage_limited）の記録を返す。
func (s *ChatGPTAuthService) currentRecord() (chatgptTokenRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil || (s.state != PlanConnected && s.state != PlanUsageLimited) {
		return chatgptTokenRecord{}, false
	}
	return *s.rec, true
}

// AccessToken は Responses API の Bearer。残りが 5 分以下なら更新してから返す。
func (s *ChatGPTAuthService) AccessToken(ctx context.Context) (string, error) {
	rec, ok := s.currentRecord()
	if !ok {
		return "", chatgptUnauthorized()
	}
	if s.expiresSoon(rec) {
		var err error
		if rec, err = s.refresh(ctx, "refresh", s.expiresSoon); err != nil {
			return "", err
		}
	}
	return rec.AccessToken, nil
}

// ReportAuthFailure はモデルの呼び出しの認可エラー（401 など）で、更新を 1 回だけ試す（FR-G29 (4)）。
// 使えなければ reauth_required。ほかのだれかがすでに更新していれば（トークンが変わっていれば）更新しない。
func (s *ChatGPTAuthService) ReportAuthFailure(ctx context.Context) {
	rec, ok := s.currentRecord()
	if !ok {
		return
	}
	seen := rec.AccessToken
	if _, err := s.refresh(ctx, "request", func(r chatgptTokenRecord) bool { return r.AccessToken == seen }); err != nil &&
		!errors.Is(err, openai.ErrPlanUnauthorized) {
		s.log.Warn("chatgpt token refresh after an authorization failure did not succeed")
	}
}

// refresh は更新を 1 つだけ行う（排他）。排他を取ったあとで need が偽なら（先に別の呼び出しが更新した）何もせず今の記録を返す。
// 使えないトークンは openai.ErrPlanUnauthorized を包んだエラー、通信の失敗・5xx などは状態を変えずに通常のエラー。
func (s *ChatGPTAuthService) refresh(ctx context.Context, source string, need func(chatgptTokenRecord) bool) (chatgptTokenRecord, error) {
	unlock, err := s.refreshMu.lock(ctx)
	if err != nil {
		return chatgptTokenRecord{}, err
	}
	defer unlock()

	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	rec, ok := s.currentRecord()
	if !ok {
		return chatgptTokenRecord{}, chatgptUnauthorized()
	}
	if !need(rec) {
		return rec, nil
	}
	clientID, err := s.getSetting(ctx, SettingChatGPTClientID)
	if err != nil {
		return chatgptTokenRecord{}, err
	}
	if clientID == "" { // 発行済みの client_id が無ければ更新できない
		s.reauthRequired(gen, source, "invalid_client", false)
		return chatgptTokenRecord{}, chatgptUnauthorized()
	}
	tok, err := s.refreshTokens(ctx, clientID, rec.RefreshToken)
	if err != nil {
		var he *chatgptHTTPError
		if errors.As(err, &he) && he.Status < http.StatusInternalServerError {
			switch {
			case chatgptRefreshDeadCodes[he.Code]:
				s.reauthRequired(gen, source, he.Code, false)
				return chatgptTokenRecord{}, chatgptUnauthorized()
			case he.Code == "invalid_client":
				s.reauthRequired(gen, source, he.Code, true)
				return chatgptTokenRecord{}, chatgptUnauthorized()
			}
		}
		return chatgptTokenRecord{}, fmt.Errorf("service: chatgpt token refresh failed: %w", err)
	}
	next := chatgptTokenRecord{
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, IDToken: tok.IDToken, Scope: tok.Scope,
		ExpiresAt: s.cfg.Clock.Now().Add(chatgptDefaultExpiresIn),
	}
	if tok.ExpiresIn > 0 {
		next.ExpiresAt = s.cfg.Clock.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	if next.RefreshToken == "" {
		next.RefreshToken = rec.RefreshToken
	}
	if next.IDToken == "" {
		next.IDToken = rec.IDToken
	}
	if next.Scope == "" {
		next.Scope = rec.Scope
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen || s.rec == nil { // 更新の間に解除・認可切れ・サインインの確定があった。古い結果は捨てる
		return chatgptTokenRecord{}, chatgptUnauthorized()
	}
	if err := s.tokens.save(next); err != nil {
		// 回転したリフレッシュトークンはメモリが唯一の写し。保管に失敗しても使い続け、次の更新でまた書く。
		s.log.Warn("chatgpt tokens could not be saved after a refresh")
	}
	s.rec = &next
	return next, nil
}

// reauthRequired は使えないと確認したトークンをすべて消し、reauth_required にする（FR-G29 (3)）。dropClient なら client_id も捨てる。
// gen が今の世代と違えば（すでに別の操作が状態を変えた）何もしない。
func (s *ChatGPTAuthService) reauthRequired(gen uint64, source, code string, dropClient bool) {
	s.mu.Lock()
	if s.gen != gen || s.rec == nil {
		s.mu.Unlock()
		return
	}
	if err := s.tokens.delete(); err != nil {
		s.log.Warn("chatgpt tokens could not be deleted")
	}
	s.rec, s.state, s.problem = nil, PlanReauthRequired, PlanProblemNone
	s.gen++
	s.mu.Unlock()
	if dropClient {
		if err := s.putSetting(s.baseCtx, SettingChatGPTClientID, nil); err != nil {
			s.log.Warn("the chatgpt client id could not be dropped")
		}
	}
	s.audit(s.baseCtx, domain.ActorSystem, chatgptAuditReauth, auditError, safeAuditCode(code), map[string]any{"source": source})
	s.toast("error", "ChatGPT のサインインが無効になりました。もう一度サインインしてください。")
}

// safeAuditCode は error_code に入れる識別子を、英小文字・数字・_ だけの短いものに限る。
func safeAuditCode(code string) string {
	if len(code) > 64 || strings.TrimFunc(code, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' }) != "" {
		return "unknown"
	}
	return code
}
