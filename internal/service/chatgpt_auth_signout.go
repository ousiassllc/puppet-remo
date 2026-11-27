package service

import (
	"context"
	"errors"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは ChatGPT の解除（FR-G29 (6)）: 実行中のタスクの中断 → リフレッシュトークンの失効 → ローカルの削除。
// 失効を確認できなくてもローカルの削除は完了させる。発行済みの client_id・sub・ホスト ID は残す（forgetAccount なら client_id・sub・メールも消す）。

const (
	chatgptSignOutReason = "chatgpt_signout"
	chatgptRevokeTries   = 3
)

// SignOut は解除する。状態が signed_out なら CodeConflict。
func (s *ChatGPTAuthService) SignOut(ctx context.Context, forgetAccount bool) (ChatGPTSignOutResult, error) {
	st := s.Status().State
	if st == PlanSignedOut {
		return ChatGPTSignOutResult{}, chatgptConflict("chatgpt is already signed out")
	}
	wasSigningIn := st == PlanSigningIn
	s.CancelSignIn() // サインインの途中の解除は、サインインを止めてから消す（監査は cancelled）

	var result ChatGPTSignOutResult
	if s.cfg.CancelTasks != nil {
		result.CancelledTask = s.cfg.CancelTasks(chatgptSignOutReason)
	}
	unlock, err := s.refreshMu.lock(ctx) // 更新の最中に消さない・消した後の更新で記録を復活させない
	if err != nil {
		return result, err
	}
	defer unlock()

	s.mu.Lock()
	if s.state == PlanSignedOut && !wasSigningIn {
		s.mu.Unlock()
		return result, chatgptConflict("chatgpt is already signed out")
	}
	rec := s.rec
	s.mu.Unlock()

	result.Revoked = true // 失効するトークンが無ければ（reauth_required）確認するものも無い
	if rec != nil {
		result.Revoked = s.revoke(ctx, rec.RefreshToken)
	}

	// ローカルの削除は、失効の結果によらず完了させる。
	delErr := s.tokens.delete()
	s.mu.Lock()
	s.rec, s.state, s.problem = nil, PlanSignedOut, PlanProblemNone
	if !result.Revoked {
		s.problem = PlanProblemRevokeFailed
	}
	s.gen++
	if forgetAccount {
		s.email = ""
	}
	s.mu.Unlock()
	if forgetAccount {
		for _, key := range []string{SettingChatGPTClientID, SettingChatGPTAccountSub, SettingChatGPTAccountEmail} {
			if err := s.putSetting(ctx, key, nil); err != nil && delErr == nil {
				delErr = err
			}
		}
	}
	s.audit(ctx, domain.ActorHost, chatgptSignOutAuditEvent, auditOK, "", map[string]any{
		"revoked": result.Revoked, "forget_account": forgetAccount, "cancelled_task": result.CancelledTask,
	})
	return result, delErr
}

// revoke はリフレッシュトークンの失効を、1 回 5 秒・最大 3 回（間隔 1 秒・2 秒）で試す。確認できたら true。
// 通信の失敗・5xx は再試行し、4xx（429 を除く）・信頼できない失効先は再試行しない。
func (s *ChatGPTAuthService) revoke(ctx context.Context, refreshToken string) bool {
	clientID, err := s.getSetting(ctx, SettingChatGPTClientID)
	if err != nil || clientID == "" {
		return false
	}
	for attempt := 0; attempt < chatgptRevokeTries; attempt++ {
		if attempt > 0 {
			if err := s.cfg.Sleep(ctx, time.Duration(attempt)*time.Second); err != nil {
				return false
			}
		}
		permanent, err := s.revokeOnce(ctx, clientID, refreshToken)
		if err == nil {
			return true
		}
		if permanent || errors.Is(ctx.Err(), context.Canceled) {
			return false
		}
	}
	return false
}
