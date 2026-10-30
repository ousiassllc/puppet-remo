package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// syncAudit は AuditSink を repository.Audit への同期追記で満たす（本番は非同期キュー）。
// repository に閲覧 API はないので、追記した内容も保持して検査できるようにする。
type syncAudit struct {
	t     *testing.T
	audit repository.Audit

	mu      sync.Mutex
	entries []repository.AuditEntry
}

func (a *syncAudit) Record(ctx context.Context, e repository.AuditEntry) {
	if _, err := a.audit.Append(ctx, e); err != nil {
		a.t.Errorf("audit append: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

// TestAuthorizeWithRepository は、本物の SQLite の repository で Authz の主要な流れを通す。
func TestAuthorizeWithRepository(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	db, err := repository.Open(ctx, filepath.Join(t.TempDir(), "puppet-remo.db"), repository.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	whois := &fakeWhoIs{ids: map[string]Identity{
		"100.64.0.9":  {LoginName: "alice@example.com", StableNodeID: "nodeA", NodeName: "laptop"},
		"100.64.0.10": {LoginName: "bob@example.com", StableNodeID: "nodeB", NodeName: "phone"},
	}}
	sink := &syncAudit{t: t, audit: db.Audit()}
	authz, err := NewAuthz(AuthzConfig{
		WhoIs: whois, Clients: db.Clients(), Tokens: db.Tokens(), ConnRequests: db.ConnRequests(),
		Audit: sink, Clock: clock, IDs: &seqIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(ip string, f Feature, authorization string) error {
		_, err := authz.Authorize(ctx, Request{RemoteAddr: ip + ":50000", Feature: f, Authorization: authorization})
		return err
	}
	pending := func() []repository.ConnRequest {
		rs, err := db.ConnRequests().ListPending(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return rs
	}
	expect := func(step string, err error, want domain.Code) {
		t.Helper()
		if got := domain.CodeOf(err); got != want {
			t.Fatalf("%s: code = %q (%v), want %q", step, got, err, want)
		}
	}

	// 未登録: device_not_registered + 接続要求（再試行は 1 件に集約）。
	for range 3 {
		expect("unregistered", authorize("100.64.0.9", FeatureWeb, ""), domain.CodeDeviceNotRegistered)
		clock.Advance(connRequestTouchInterval)
	}
	if rs := pending(); len(rs) != 1 || rs[0].RequestCount != 3 || rs[0].StableNodeID != "nodeA" ||
		rs[0].RemoteIP != "100.64.0.9" || !rs[0].ExpiresAt.Equal(clock.Now().Add(-3*connRequestTouchInterval).Add(connRequestPendingTTL)) {
		t.Fatalf("pending = %+v", rs)
	}

	// 拒否後は 24 時間、同デバイスから新しい要求を作らない。
	if err := db.ConnRequests().Decide(ctx, pending()[0].ID, repository.ConnDenied, nil); err != nil {
		t.Fatal(err)
	}
	expect("after denial", authorize("100.64.0.9", FeatureWeb, ""), domain.CodeDeviceNotRegistered)
	if n := len(pending()); n != 0 {
		t.Fatalf("pending after denial = %d", n)
	}
	clock.Advance(repository.DeniedTTL)
	expect("after 24h", authorize("100.64.0.9", FeatureWeb, ""), domain.CodeDeviceNotRegistered)
	if n := len(pending()); n != 1 {
		t.Fatalf("pending after the suppression window = %d, want 1", n)
	}

	// WhoIs に載らない接続元。
	expect("whois", authorize("100.64.0.99", FeatureWeb, ""), domain.CodeWhoisFailed)

	// 登録して Web を通す。MCP は Bearer が要る。
	if err := db.Clients().Create(ctx, domain.Client{ID: "c1", StableNodeID: "nodeA", LoginName: "alice@example.com",
		DisplayName: "laptop", WebEnabled: true, MCPEnabled: true}); err != nil {
		t.Fatal(err)
	}
	expect("registered web", authorize("100.64.0.9", FeatureWeb, ""), "")
	expect("mcp without bearer", authorize("100.64.0.9", FeatureMCP, ""), domain.CodeUnauthorized)

	token, err := authz.IssueToken(ctx, "c1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	expect("mcp with bearer", authorize("100.64.0.9", FeatureMCP, "Bearer "+token), "")
	recs, err := db.Tokens().ListByClient(ctx, "c1")
	if err != nil || len(recs) != 1 || recs[0].LastUsedAt == nil || recs[0].Prefix != token[:8] {
		t.Fatalf("tokens = %+v, %v", recs, err)
	}

	// 他デバイス（登録済み）からそのトークンは使えない。
	if err := db.Clients().Create(ctx, domain.Client{ID: "c2", StableNodeID: "nodeB", LoginName: "bob@example.com",
		DisplayName: "phone", WebEnabled: true, MCPEnabled: true}); err != nil {
		t.Fatal(err)
	}
	expect("token of another device", authorize("100.64.0.10", FeatureMCP, "Bearer "+token), domain.CodeUnauthorized)

	// MCP 無効なら Web は通り、MCP は client_disabled。
	off, on := false, true
	if err := db.Clients().Update(ctx, "c1", repository.ClientPatch{MCPEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	expect("mcp disabled", authorize("100.64.0.9", FeatureMCP, "Bearer "+token), domain.CodeClientDisabled)
	expect("web still enabled", authorize("100.64.0.9", FeatureWeb, ""), "")
	if err := db.Clients().Update(ctx, "c1", repository.ClientPatch{MCPEnabled: &on}); err != nil {
		t.Fatal(err)
	}

	// 失効したトークンは使えない。
	if err := authz.RevokeToken(ctx, recs[0].ID); err != nil {
		t.Fatal(err)
	}
	expect("revoked", authorize("100.64.0.9", FeatureMCP, "Bearer "+token), domain.CodeUnauthorized)

	// 認証失敗は 1 分に 10 回まで。ここまでの nodeA の失敗（Bearer なし・失効）は別の窓なので、窓を明けてから数える。
	clock.Advance(time.Minute)
	bad := "Bearer " + TokenPrefix + strings.Repeat("A", 43)
	for i := range bearerFailureLimit {
		expect(fmt.Sprintf("failure %d", i+1), authorize("100.64.0.9", FeatureMCP, bad), domain.CodeUnauthorized)
	}
	err = authorize("100.64.0.9", FeatureMCP, bad)
	expect("rate limited", err, domain.CodeRateLimited)
	if RetryAfter(err) != time.Minute {
		t.Fatalf("RetryAfter = %v", RetryAfter(err))
	}
	clock.Advance(time.Minute)
	expect("after the window", authorize("100.64.0.9", FeatureMCP, bad), domain.CodeUnauthorized)

	// 監査: 認証失敗が audit_logs に追記され（Record が DB への追記を確かめる）、トークン（平文・ハッシュ）を含まない。
	counts := map[string]int{}
	hash := repository.HashToken(token)
	for _, e := range sink.entries {
		counts[e.Event]++
		dump := fmt.Sprintf("%+v %s", e, e.DetailJSON)
		for _, secret := range []string{token, token[len(TokenPrefix):], fmt.Sprintf("%x", hash)} {
			if strings.Contains(dump, secret) {
				t.Fatalf("audit entry contains a token secret: %s", dump)
			}
		}
	}
	// 失敗: Bearer なし・他デバイス・失効 + 窓内 10 件 + 窓明け 1 件（MCP 無効の 1 回は client_disabled で数えない）。
	if counts["auth.bearer_failed"] != 14 || counts["auth.rate_limited"] != 1 {
		t.Fatalf("audit counts = %v", counts)
	}
}
