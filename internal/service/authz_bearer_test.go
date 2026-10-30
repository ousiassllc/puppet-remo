package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// bearerEnv は A（登録済み・MCP 有効）と、そのトークンを持つ env。
func bearerEnv(t *testing.T) (e *env, token string, p Principal) {
	t.Helper()
	e = newEnv(t)
	id := e.device("100.64.0.5", testNodeA, testUser)
	client := e.register("c1", testNodeA, testUser)
	token, err := e.authz.IssueToken(context.Background(), "c1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	return e, token, Principal{Client: client, Identity: id}
}

func TestVerifyBearer(t *testing.T) {
	tests := []struct {
		name       string
		header     func(tok string) string
		mutate     func(e *env, tok string)
		wantReason string // 空は成功
	}{
		{"有効", func(tok string) string { return "Bearer " + tok }, nil, ""},
		{"スキームは大文字小文字を区別しない", func(tok string) string { return "bEaRer " + tok }, nil, ""},
		{"ヘッダーなし", func(string) string { return "" }, nil, reasonMissing},
		{"Bearer 以外のスキーム", func(tok string) string { return "Basic " + tok }, nil, reasonMalformed},
		{"トークンなし", func(string) string { return "Bearer " }, nil, reasonMalformed},
		{"生のトークン", func(tok string) string { return tok }, nil, reasonMalformed},
		{"トークンの後ろに余分な語", func(tok string) string { return "Bearer " + tok + " x" }, nil, reasonMalformed},
		{"短い", func(tok string) string { return "Bearer " + tok[:20] }, nil, reasonMalformed},
		{"接頭辞違い", func(tok string) string { return "Bearer xyz_" + tok[4:] }, nil, reasonMalformed},
		{"base64url 以外の文字", func(tok string) string { return "Bearer " + tok[:46] + "=" }, nil, reasonMalformed},
		{"形式は正しいが未発行", func(tok string) string { return "Bearer " + TokenPrefix + strings.Repeat("A", 43) }, nil, reasonUnknown},
		{"失効", func(tok string) string { return "Bearer " + tok }, func(e *env, tok string) {
			for id := range e.tokens.records {
				_ = e.tokens.Revoke(context.Background(), id, e.clock.Now())
			}
		}, reasonRevoked},
		{"期限切れ（期限ちょうど）", func(tok string) string { return "Bearer " + tok }, func(e *env, tok string) {
			e.clock.Advance(DefaultTokenTTL)
		}, reasonExpired},
		{"期限の 1 ナノ秒前は有効", func(tok string) string { return "Bearer " + tok }, func(e *env, tok string) {
			e.clock.Advance(DefaultTokenTTL - time.Nanosecond)
		}, ""},
		{"他デバイスのトークン", func(tok string) string { return "Bearer " + tok }, func(e *env, tok string) {
			for id, r := range e.tokens.records {
				r.ClientID = "other"
				e.tokens.records[id] = r
			}
		}, reasonWrongDevice},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, tok, p := bearerEnv(t)
			if tt.mutate != nil {
				tt.mutate(e, tok)
			}
			rec, err := e.authz.VerifyBearer(context.Background(), netAddr("100.64.0.5"), p, tt.header(tok))
			if tt.wantReason == "" {
				if err != nil || rec == nil || rec.ClientID != "c1" {
					t.Fatalf("VerifyBearer = %+v, %v", rec, err)
				}
				if len(e.audit.events()) != 0 {
					t.Fatalf("audit on success: %v", e.audit.events())
				}
				return
			}
			wantCode(t, err, domain.CodeUnauthorized)
			if rec != nil {
				t.Fatalf("token returned on failure: %+v", rec)
			}
			if got := e.audit.events(); len(got) != 1 || got[0] != "auth.bearer_failed" {
				t.Fatalf("audit events = %v", got)
			}
			entry := e.audit.entries[0]
			var detail map[string]any
			if err := json.Unmarshal(entry.DetailJSON, &detail); err != nil {
				t.Fatal(err)
			}
			if detail["reason"] != tt.wantReason || detail["remote_ip"] != "100.64.0.5" {
				t.Fatalf("detail = %v, want reason %q", detail, tt.wantReason)
			}
			if entry.Kind != "auth" || entry.Result != "denied" || entry.ErrorCode != "unauthorized" ||
				entry.ActorType != domain.ActorMCP || entry.ClientID != "c1" || entry.TS != e.clock.Now() {
				t.Fatalf("entry = %+v", entry)
			}
		})
	}
}

func TestVerifyBearerAuditHasNoSecrets(t *testing.T) {
	e, tok, p := bearerEnv(t)
	ctx := context.Background()
	hash := repository.HashToken(tok)
	for _, h := range []string{"Bearer " + tok + "x", "Bearer " + tok[:30], "Bearer " + TokenPrefix + strings.Repeat("B", 43)} {
		if _, err := e.authz.VerifyBearer(ctx, netAddr("100.64.0.5"), p, h); err == nil {
			t.Fatal("expected a failure")
		}
	}
	for i := 0; i < bearerFailureLimit; i++ { // 429 の監査も含める
		_, _ = e.authz.VerifyBearer(ctx, netAddr("100.64.0.5"), p, "Bearer "+tok+"x")
	}
	var dump strings.Builder
	for _, en := range e.audit.entries {
		fmt.Fprintf(&dump, "%+v %s", en, en.DetailJSON)
	}
	for _, secret := range []string{tok, tok[4:], hex.EncodeToString(hash[:]), TokenDisplayPrefixForTest(tok)} {
		if strings.Contains(dump.String(), secret) {
			t.Fatalf("audit contains secret %q", secret)
		}
	}
}

// TokenDisplayPrefixForTest はトークンの表示用の接頭辞（監査にも出さないことの確認用）。
func TokenDisplayPrefixForTest(tok string) string { return repository.TokenDisplayPrefix(tok) }

func TestVerifyBearerRateLimit(t *testing.T) {
	e, tok, p := bearerEnv(t)
	ctx := context.Background()
	addr := netAddr("100.64.0.5")
	bad := "Bearer " + TokenPrefix + strings.Repeat("A", 43)
	good := "Bearer " + tok

	// 成功は数えない。
	for i := 0; i < 3*bearerFailureLimit; i++ {
		if _, err := e.authz.VerifyBearer(ctx, addr, p, good); err != nil {
			t.Fatalf("success %d: %v", i, err)
		}
	}
	// 10 回までは 401。
	for i := 1; i <= bearerFailureLimit; i++ {
		_, err := e.authz.VerifyBearer(ctx, addr, p, bad)
		wantCode(t, err, domain.CodeUnauthorized)
	}
	// 11 回目は 429（Retry-After は窓の残り）。正しいトークンでも拒否する。
	e.clock.Advance(20 * time.Second)
	for _, h := range []string{bad, good} {
		_, err := e.authz.VerifyBearer(ctx, addr, p, h)
		wantCode(t, err, domain.CodeRateLimited)
		if got := RetryAfter(err); got != 40*time.Second {
			t.Fatalf("RetryAfter = %v, want 40s", got)
		}
	}
	// 他デバイスは影響を受けない。
	other := Principal{Client: domain.Client{ID: "c2", DisplayName: "dev-B"}, Identity: Identity{StableNodeID: testNodeB}}
	_, err := e.authz.VerifyBearer(ctx, addr, other, bad)
	wantCode(t, err, domain.CodeUnauthorized)

	// 監査: 失敗 10 件 + 他デバイスの 1 件 + 429 は窓ごとに 1 回だけ。
	counts := map[string]int{}
	for _, ev := range e.audit.events() {
		counts[ev]++
	}
	if counts["auth.bearer_failed"] != bearerFailureLimit+1 || counts["auth.rate_limited"] != 1 {
		t.Fatalf("audit counts = %v", counts)
	}

	// 窓が終われば回復し、新しい窓で再び数える。
	e.clock.Advance(40 * time.Second)
	if _, err := e.authz.VerifyBearer(ctx, addr, p, good); err != nil {
		t.Fatalf("after window: %v", err)
	}
	_, err = e.authz.VerifyBearer(ctx, addr, p, bad)
	wantCode(t, err, domain.CodeUnauthorized)
}

func TestVerifyBearerRepositoryFailureIsNotCounted(t *testing.T) {
	e, tok, p := bearerEnv(t)
	e.tokens.err = fmt.Errorf("db down")
	for i := 0; i < 2*bearerFailureLimit; i++ {
		_, err := e.authz.VerifyBearer(context.Background(), netAddr("100.64.0.5"), p, "Bearer "+tok)
		wantCode(t, err, domain.CodeInternal)
	}
	if len(e.audit.events()) != 0 {
		t.Fatalf("audit = %v", e.audit.events())
	}
	e.tokens.err = nil
	if _, err := e.authz.VerifyBearer(context.Background(), netAddr("100.64.0.5"), p, "Bearer "+tok); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyBearerTouchesLastUsedAtAtMostOncePerInterval(t *testing.T) {
	e, tok, p := bearerEnv(t)
	ctx := context.Background()
	verify := func() *repository.TokenRecord {
		t.Helper()
		rec, err := e.authz.VerifyBearer(ctx, netAddr("100.64.0.5"), p, "Bearer "+tok)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	first := verify()
	if first.LastUsedAt == nil || e.tokens.touches != 1 {
		t.Fatalf("first: touches = %d, record = %+v", e.tokens.touches, first)
	}
	e.clock.Advance(tokenTouchInterval - time.Second)
	verify()
	if e.tokens.touches != 1 {
		t.Fatalf("touched again within the interval: %d", e.tokens.touches)
	}
	e.clock.Advance(time.Second)
	verify()
	if e.tokens.touches != 2 {
		t.Fatalf("touches = %d, want 2", e.tokens.touches)
	}
}

func TestValidTokenFormat(t *testing.T) {
	tok, err := newToken(cryptoRand)
	if err != nil {
		t.Fatal(err)
	}
	if !validTokenFormat(tok) {
		t.Fatalf("generated token rejected: len=%d", len(tok))
	}
	// 末尾の未使用ビットが 0 でない（正規形でない）文字列は拒否する。
	if validTokenFormat(tok[:len(tok)-1] + "B") { // 43 文字目の下位 2 ビットが非 0
		t.Fatal("non-canonical base64url accepted")
	}
}
