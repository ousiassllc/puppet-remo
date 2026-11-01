package service

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

const (
	// TokenPrefix は Bearer トークンの接頭辞（NFR-S04）。
	TokenPrefix = "prm_"
	// tokenRandomBytes はトークンの乱数部の長さ（256 bit）。
	tokenRandomBytes = 32
	// tokenLen は接頭辞を含むトークン文字列の長さ（NFR-S04: 47 文字）。
	tokenLen = len(TokenPrefix) + 43
	// tokenTouchInterval は last_used_at を書き換える最小の間隔（リクエストごとの書き込みを避ける）。
	tokenTouchInterval = time.Minute
)

// 認証失敗の理由（監査の detail_json.reason）。トークンの値は含めない。
const (
	reasonMissing     = "missing"
	reasonMalformed   = "malformed"
	reasonUnknown     = "unknown"
	reasonRevoked     = "revoked"
	reasonExpired     = "expired"
	reasonWrongDevice = "wrong_device"
)

func (a *authz) VerifyBearer(ctx context.Context, remoteAddr string, p Principal, authorization string) (*TokenRecord, error) {
	device := p.Identity.StableNodeID
	now := a.cfg.Clock.Now()
	if ok, first, retry := a.bearerFailures.check(device, now, false); !ok {
		if first {
			a.log.Warn("bearer authentication is rate limited", "device", p.Client.DisplayName, "remote_ip", remoteIP(remoteAddr))
			a.audit(ctx, bearerAudit("auth.rate_limited", domain.CodeRateLimited, p.Client),
				map[string]any{"remote_ip": remoteIP(remoteAddr)})
		}
		return nil, rateLimited(retry)
	}
	rec, reason, err := a.lookupToken(ctx, p, authorization, now)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		a.bearerFailures.check(device, now, true)
		// 理由（reason）だけを出す。Authorization ヘッダー・トークン・ハッシュは出さない（NFR-S13）。
		a.log.Warn("bearer authentication failed", "reason", reason, "device", p.Client.DisplayName, "remote_ip", remoteIP(remoteAddr))
		a.audit(ctx, bearerAudit("auth.bearer_failed", domain.CodeUnauthorized, p.Client),
			map[string]any{"reason": reason, "remote_ip": remoteIP(remoteAddr)})
		return nil, ErrUnauthorized
	}
	if rec.LastUsedAt == nil || now.Sub(*rec.LastUsedAt) >= tokenTouchInterval {
		if err := a.cfg.Tokens.TouchUsed(ctx, rec.ID, now); err != nil {
			a.log.Warn("touch token last_used_at failed", "token_id", rec.ID, "error", err)
		}
		rec.LastUsedAt = &now
	}
	return &rec, nil
}

// lookupToken はトークンを検証する。失敗の理由があれば reason に入れる。err は DB 障害など（失敗として数えない）。
func (a *authz) lookupToken(ctx context.Context, p Principal, authorization string, now time.Time) (rec TokenRecord, reason string, err error) {
	token, ok := bearerToken(authorization)
	if !ok {
		if strings.TrimSpace(authorization) == "" {
			return rec, reasonMissing, nil
		}
		return rec, reasonMalformed, nil
	}
	if !validTokenFormat(token) {
		return rec, reasonMalformed, nil
	}
	hash := repository.HashToken(token)
	rec, err = a.cfg.Tokens.ByHash(ctx, hash)
	if errors.Is(err, repository.ErrNotFound) {
		return rec, reasonUnknown, nil
	}
	if err != nil {
		return rec, "", err
	}
	if subtle.ConstantTimeCompare(rec.TokenHash[:], hash[:]) != 1 {
		return rec, reasonUnknown, nil
	}
	switch {
	case rec.RevokedAt != nil:
		return rec, reasonRevoked, nil
	case rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt):
		return rec, reasonExpired, nil
	case rec.ClientID != p.Client.ID: // 他デバイスのトークン（FR-A05）
		return rec, reasonWrongDevice, nil
	}
	return rec, "", nil
}

// bearerToken は "Bearer <token>" からトークンを取り出す（スキームは大文字小文字を区別しない）。
func bearerToken(authorization string) (string, bool) {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// validTokenFormat は NFR-S04 の形式（prm_ + base64url の 32 バイト、計 47 文字）かを返す。
func validTokenFormat(token string) bool {
	if len(token) != tokenLen || !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token[len(TokenPrefix):])
	return err == nil && len(raw) == tokenRandomBytes
}

func rateLimited(retry time.Duration) error {
	sec := int((retry + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	return domain.NewError(domain.CodeRateLimited, "too many authentication failures", map[string]any{DetailRetryAfterSec: sec})
}

// audit は認証系（kind=auth、result=denied）の監査イベントを書く。detail にトークンを入れてはならない（NFR-S13）。
func (a *authz) audit(ctx context.Context, e repository.AuditEntry, detail map[string]any) {
	if a.cfg.Audit == nil {
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		a.log.Warn("marshal audit detail failed", "error", err)
		return
	}
	e.TS = a.cfg.Clock.Now()
	e.Kind = "auth"
	if e.Result == "" {
		e.Result = auditDenied // 認証の失敗・超過が既定。接続要求の作成のような成功は呼び出し側が ok を渡す
	}
	e.DetailJSON = raw
	a.cfg.Audit.Record(ctx, e)
}

// bearerAudit は Bearer 検証の失敗・超過の監査エントリ（操作主体は MCP クライアント）。
func bearerAudit(event string, code domain.Code, c domain.Client) repository.AuditEntry {
	return repository.AuditEntry{
		Event: event, ActorType: domain.ActorMCP, ClientID: c.ID, ClientLabel: c.DisplayName, ErrorCode: string(code),
	}
}
