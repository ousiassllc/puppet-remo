package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func TestIssueToken(t *testing.T) {
	day := 24 * time.Hour
	zero := time.Duration(0)
	week := 7 * day
	neg := -time.Second
	tests := []struct {
		name        string
		label       string
		ttl         *time.Duration
		wantExpires time.Duration // 発行時刻からの期間。0 は無期限
		wantCode    domain.Code
	}{
		{"既定は 90 日", "claude", nil, 90 * day, ""},
		{"期間を指定", "claude", &week, week, ""},
		{"0 は無期限", "claude", &zero, 0, ""},
		{"負の期間", "claude", &neg, 0, domain.CodeInvalidArgument},
		{"空のラベル", "  ", nil, 0, domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.register("c1", testNodeA, testUser)
			tok, err := e.authz.IssueToken(context.Background(), "c1", tt.label, tt.ttl)
			if tt.wantCode != "" {
				wantCode(t, err, tt.wantCode)
				if tok != "" || len(e.tokens.records) != 0 {
					t.Fatal("a token was issued")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(tok) != 47 || !strings.HasPrefix(tok, TokenPrefix) || !validTokenFormat(tok) {
				t.Fatalf("token format: len=%d", len(tok))
			}
			if len(e.tokens.records) != 1 {
				t.Fatalf("records = %d", len(e.tokens.records))
			}
			for _, r := range e.tokens.records {
				if r.TokenHash != repository.HashToken(tok) || r.Prefix != tok[:8] || r.ClientID != "c1" ||
					r.Label != strings.TrimSpace(tt.label) || !r.CreatedAt.Equal(e.clock.Now()) || r.ID == "" {
					t.Fatalf("record = %+v", r)
				}
				switch {
				case tt.wantExpires == 0 && r.ExpiresAt != nil:
					t.Fatalf("ExpiresAt = %v, want nil", r.ExpiresAt)
				case tt.wantExpires != 0 && (r.ExpiresAt == nil || !r.ExpiresAt.Equal(e.clock.Now().Add(tt.wantExpires))):
					t.Fatalf("ExpiresAt = %v", r.ExpiresAt)
				}
			}
		})
	}
}

func TestIssueTokenErrors(t *testing.T) {
	e := newEnv(t)
	e.register("c1", testNodeA, testUser)
	ctx := context.Background()

	_, err := e.authz.IssueToken(ctx, "missing", "x", nil)
	wantCode(t, err, domain.CodeInvalidArgument)

	if _, err := e.authz.IssueToken(ctx, "c1", "dup", nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.authz.IssueToken(ctx, "c1", "dup", nil)
	wantCode(t, err, domain.CodeConflict)

	e.clients.err = errors.New("db down")
	_, err = e.authz.IssueToken(ctx, "c1", "other", nil)
	wantCode(t, err, domain.CodeInternal)
}

func TestIssueTokenUniqueAndUsesRandomSource(t *testing.T) {
	e := newEnv(t)
	e.register("c1", testNodeA, testUser)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		tok, err := e.authz.IssueToken(context.Background(), "c1", string(rune('a'+i)), nil)
		if err != nil || seen[tok] {
			t.Fatalf("token %d: %v (dup=%v)", i, err, seen[tok])
		}
		seen[tok] = true
	}

	// 乱数源が失敗したら発行しない。
	a, err := NewAuthz(AuthzConfig{
		WhoIs: e.whois, Clients: e.clients, Tokens: e.tokens, ConnRequests: e.reqs,
		Clock: e.clock, IDs: &seqIDs{}, Rand: strings.NewReader("short"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.IssueToken(context.Background(), "c1", "zzz", nil); err == nil {
		t.Fatal("IssueToken succeeded with a failing random source")
	}
}

func TestRevokeToken(t *testing.T) {
	e, tok, p := bearerEnv(t)
	ctx := context.Background()
	var id string
	for k := range e.tokens.records {
		id = k
	}
	if err := e.authz.RevokeToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err := e.authz.VerifyBearer(ctx, netAddr("100.64.0.5"), p, "Bearer "+tok)
	wantCode(t, err, domain.CodeUnauthorized)

	// 失効済みの再失効は元の時刻を保つ。
	first := *e.tokens.get(id).RevokedAt
	e.clock.Advance(time.Hour)
	if err := e.authz.RevokeToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := *e.tokens.get(id).RevokedAt; !got.Equal(first) {
		t.Fatalf("RevokedAt changed: %v -> %v", first, got)
	}

	wantCode(t, e.authz.RevokeToken(ctx, "missing"), domain.CodeInvalidArgument)
}
