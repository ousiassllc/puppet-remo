package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

func TestNewAuthzRequiresDependencies(t *testing.T) {
	e := newEnv(t)
	full := AuthzConfig{
		WhoIs: e.whois, Clients: e.clients, Tokens: e.tokens, ConnRequests: e.reqs,
		Clock: e.clock, IDs: &seqIDs{},
	}
	tests := []struct {
		name   string
		mutate func(*AuthzConfig)
	}{
		{"WhoIs", func(c *AuthzConfig) { c.WhoIs = nil }},
		{"Clients", func(c *AuthzConfig) { c.Clients = nil }},
		{"Tokens", func(c *AuthzConfig) { c.Tokens = nil }},
		{"ConnRequests", func(c *AuthzConfig) { c.ConnRequests = nil }},
		{"Clock", func(c *AuthzConfig) { c.Clock = nil }},
		{"IDs", func(c *AuthzConfig) { c.IDs = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := full
			tt.mutate(&cfg)
			if _, err := NewAuthz(cfg); err == nil {
				t.Fatal("NewAuthz succeeded without a required dependency")
			}
		})
	}
	if _, err := NewAuthz(full); err != nil {
		t.Fatalf("NewAuthz(full) = %v", err)
	}
}

func TestIdentify(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(e *env)
		wantErr bool
	}{
		{"解決できる", func(e *env) { e.device("100.64.0.5", testNodeA, testUser) }, false},
		{"WhoIs の失敗", func(e *env) {}, true},
		{"WhoIs の別のエラーも whois_failed", func(e *env) { e.whois.err = errors.New("boom") }, true},
		{"node ID が空", func(e *env) { e.device("100.64.0.5", "", testUser) }, true},
		{"ログイン名が空", func(e *env) { e.device("100.64.0.5", testNodeA, "") }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			id, err := e.authz.Identify(context.Background(), netAddr("100.64.0.5"))
			if tt.wantErr {
				wantCode(t, err, domain.CodeWhoisFailed)
				if !errors.Is(err, tailnet.ErrWhoIsFailed) {
					t.Fatalf("err = %v, want tailnet.ErrWhoIsFailed", err)
				}
				return
			}
			if err != nil || id.StableNodeID != testNodeA {
				t.Fatalf("Identify = %+v, %v", id, err)
			}
		})
	}
}

func TestCheckAllowlist(t *testing.T) {
	tests := []struct {
		name         string
		feature      Feature
		client       func(c *domain.Client) // nil は未登録
		user         string                 // WhoIs が返すログイン名
		wantCode     domain.Code
		wantRequests int // 作られた接続要求の数
	}{
		{"Web 登録済み", FeatureWeb, func(*domain.Client) {}, testUser, "", 0},
		{"MCP 登録済み", FeatureMCP, func(*domain.Client) {}, testUser, "", 0},
		{"未登録", FeatureWeb, nil, testUser, domain.CodeDeviceNotRegistered, 1},
		{"未登録の MCP", FeatureMCP, nil, testUser, domain.CodeDeviceNotRegistered, 1},
		{"登録時と異なるユーザー", FeatureWeb, func(*domain.Client) {}, "bob@example.com", domain.CodeDeviceNotRegistered, 1},
		{"Web 無効", FeatureWeb, func(c *domain.Client) { c.WebEnabled = false }, testUser, domain.CodeClientDisabled, 0},
		{"Web 無効でも MCP は使える", FeatureMCP, func(c *domain.Client) { c.WebEnabled = false }, testUser, "", 0},
		{"MCP 無効", FeatureMCP, func(c *domain.Client) { c.MCPEnabled = false }, testUser, domain.CodeClientDisabled, 0},
		{"MCP 無効でも Web は使える", FeatureWeb, func(c *domain.Client) { c.MCPEnabled = false }, testUser, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			id := e.device("100.64.0.5", testNodeA, tt.user)
			if tt.client != nil {
				c := e.register("c1", testNodeA, testUser)
				tt.client(&c)
				e.clients.set(c)
			}
			client, err := e.authz.CheckAllowlist(context.Background(), netAddr("100.64.0.5"), id, tt.feature)
			if tt.wantCode != "" {
				wantCode(t, err, tt.wantCode)
			} else if err != nil || client.ID != "c1" {
				t.Fatalf("CheckAllowlist = %+v, %v", client, err)
			}
			reqs := e.reqs.snapshot()
			if len(reqs) != tt.wantRequests {
				t.Fatalf("connection requests = %d, want %d", len(reqs), tt.wantRequests)
			}
			if tt.wantRequests == 1 && (reqs[0].StableNodeID != testNodeA || reqs[0].RemoteIP != "100.64.0.5") {
				t.Fatalf("request = %+v", reqs[0])
			}
		})
	}
}

func TestCheckAllowlistRepositoryErrorIsNotRegistration(t *testing.T) {
	e := newEnv(t)
	id := e.device("100.64.0.5", testNodeA, testUser)
	e.clients.err = errors.New("db down")
	_, err := e.authz.CheckAllowlist(context.Background(), netAddr("100.64.0.5"), id, FeatureWeb)
	wantCode(t, err, domain.CodeInternal)
	if len(e.reqs.snapshot()) != 0 {
		t.Fatal("a connection request was created on a repository failure")
	}
}

func TestCheckAllowlistConnRequestFailureStillRejects(t *testing.T) {
	e := newEnv(t)
	id := e.device("100.64.0.5", testNodeA, testUser)
	e.reqs.err = errors.New("db down")
	_, err := e.authz.CheckAllowlist(context.Background(), netAddr("100.64.0.5"), id, FeatureWeb)
	wantCode(t, err, domain.CodeDeviceNotRegistered)
}

func TestAuthorize(t *testing.T) {
	tests := []struct {
		name     string
		feature  Feature
		header   func(tok string) string
		setup    func(e *env)
		wantCode domain.Code
		wantTok  bool
	}{
		{"Web は Bearer 不要", FeatureWeb, func(string) string { return "" }, func(e *env) { e.register("c1", testNodeA, testUser) }, "", false},
		{"MCP は Bearer 必須", FeatureMCP, func(string) string { return "" }, func(e *env) { e.register("c1", testNodeA, testUser) }, domain.CodeUnauthorized, false},
		{"MCP の有効なトークン", FeatureMCP, func(tok string) string { return "Bearer " + tok }, func(e *env) { e.register("c1", testNodeA, testUser) }, "", true},
		{"WhoIs 失敗が最初", FeatureMCP, func(tok string) string { return "Bearer " + tok }, func(e *env) { e.whois.err = errors.New("x") }, domain.CodeWhoisFailed, false},
		{"未登録は Bearer を見ない", FeatureMCP, func(tok string) string { return "Bearer " + tok }, func(e *env) {}, domain.CodeDeviceNotRegistered, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.device("100.64.0.5", testNodeA, testUser)
			tt.setup(e)
			e.register("other", "other-node", testUser) // 別デバイスのクライアントがいても影響しない
			tok := ""
			if c, err := e.clients.Get(context.Background(), "c1"); err == nil {
				var ierr error
				if tok, ierr = e.authz.IssueToken(context.Background(), c.ID, "t", nil); ierr != nil {
					t.Fatal(ierr)
				}
			}
			p, err := e.authz.Authorize(context.Background(), Request{
				RemoteAddr: netAddr("100.64.0.5"), Feature: tt.feature, Authorization: tt.header(tok),
			})
			if tt.wantCode != "" {
				wantCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Client.ID != "c1" || p.Identity.StableNodeID != testNodeA || (p.Token != nil) != tt.wantTok {
				t.Fatalf("principal = %+v", p)
			}
			if tt.wantTok && p.Token.ClientID != "c1" {
				t.Fatalf("token = %+v", p.Token)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	if got := RetryAfter(rateLimited(61500 * time.Millisecond)); got.Seconds() != 62 {
		t.Fatalf("RetryAfter = %v, want 62s", got)
	}
	if got := RetryAfter(ErrUnauthorized); got != 0 {
		t.Fatalf("RetryAfter(unauthorized) = %v", got)
	}
	if got := RetryAfter(nil); got != 0 {
		t.Fatalf("RetryAfter(nil) = %v", got)
	}
}
