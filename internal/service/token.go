package service

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// DefaultTokenTTL はトークンの既定の有効期間（FR-A08）。
const DefaultTokenTTL = 90 * 24 * time.Hour

// cryptoRand は暗号論的乱数源（NewAuthz の Rand の既定）。
var cryptoRand io.Reader = crand.Reader

func (a *authz) IssueToken(ctx context.Context, clientID, label string, ttl *time.Duration) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", domain.ErrInvalidArgument.WithMsg("token label is required")
	}
	if ttl != nil && *ttl < 0 {
		return "", domain.ErrInvalidArgument.WithMsg("token ttl must not be negative")
	}
	if _, err := a.cfg.Clients.Get(ctx, clientID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", domain.ErrInvalidArgument.WithMsg("client not found")
		}
		return "", err
	}
	now := a.cfg.Clock.Now()
	var expires *time.Time
	switch {
	case ttl == nil:
		t := now.Add(DefaultTokenTTL)
		expires = &t
	case *ttl > 0:
		t := now.Add(*ttl)
		expires = &t
	}
	token, err := newToken(a.rnd)
	if err != nil {
		return "", err
	}
	err = a.cfg.Tokens.Create(ctx, repository.TokenRecord{
		ID:        a.cfg.IDs.NewID(),
		ClientID:  clientID,
		Label:     label,
		TokenHash: repository.HashToken(token),
		Prefix:    repository.TokenDisplayPrefix(token),
		ExpiresAt: expires,
		CreatedAt: now,
	})
	if errors.Is(err, repository.ErrConflict) {
		return "", domain.NewError(domain.CodeConflict, "a token with this label already exists", nil)
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func (a *authz) RevokeToken(ctx context.Context, tokenID string) error {
	err := a.cfg.Tokens.Revoke(ctx, tokenID, a.cfg.Clock.Now())
	if errors.Is(err, repository.ErrNotFound) {
		return domain.ErrInvalidArgument.WithMsg("token not found")
	}
	return err
}

// newToken は NFR-S04 の形式（prm_ + base64url（パディングなし）の 32 バイト乱数）のトークンを作る。
func newToken(rnd io.Reader) (string, error) {
	var b [tokenRandomBytes]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}
