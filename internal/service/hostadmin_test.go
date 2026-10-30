package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

type closedSessions struct {
	mu      sync.Mutex
	clients []string
	tokens  []string
}

func (c *closedSessions) CloseClient(id string, kind domain.ActorKind) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clients = append(c.clients, id+"/"+string(kind))
}

func (c *closedSessions) CloseToken(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens = append(c.tokens, id)
}

type adminEnv struct {
	ctx      context.Context
	admin    HostAdmin
	authz    Authz
	db       *repository.DB
	clock    *fakeClock
	audit    *recordingAudit
	sessions *closedSessions
	leases   LeaseManager
	bus      *Bus
	events   <-chan Event
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	ctx := context.Background()
	clock := newFakeClock()
	db, err := repository.Open(ctx, filepath.Join(t.TempDir(), "puppet-remo.db"), repository.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &adminEnv{ctx: ctx, db: db, clock: clock, audit: &recordingAudit{}, sessions: &closedSessions{}, bus: NewBus()}
	var cancel func()
	e.events, cancel = e.bus.Subscribe()
	t.Cleanup(cancel)
	ids := &seqIDs{}
	authz, err := NewAuthz(AuthzConfig{
		WhoIs: &fakeWhoIs{}, Clients: db.Clients(), Tokens: db.Tokens(), ConnRequests: db.ConnRequests(),
		Audit: e.audit, Clock: clock, IDs: ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.leases = mustLeaseManager(t, clock, nil)
	e.authz = authz
	e.admin, err = NewHostAdmin(HostAdminConfig{
		Clients: db.Clients(), Tokens: db.Tokens(), ConnRequests: NewNotifyingConnRequests(db.ConnRequests(), e.bus), Authz: authz,
		Leases: e.leases, Sessions: e.sessions, Audit: e.audit, Bus: e.bus, Clock: clock, IDs: ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *adminEnv) request(t *testing.T, node, name string) repository.ConnRequest {
	t.Helper()
	r, _, err := e.db.ConnRequests().UpsertPending(e.ctx, repository.ConnRequest{
		ID: "req-" + node, StableNodeID: node, LoginName: node + "@example.com", DisplayName: name, RemoteIP: "100.64.0.9",
		ExpiresAt: e.clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *adminEnv) approve(t *testing.T, node, name string, in ApproveInput) domain.Client {
	t.Helper()
	r := e.request(t, node, name)
	c, err := e.admin.ApproveRequest(e.ctx, r.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// auditDetail は event の監査エントリを 1 件返す（無ければ nil）。
func (e *adminEnv) auditEntry(event string) *repository.AuditEntry {
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	for i := range e.audit.entries {
		if e.audit.entries[i].Event == event {
			return &e.audit.entries[i]
		}
	}
	return nil
}

func TestNewHostAdminValidatesConfig(t *testing.T) {
	if _, err := NewHostAdmin(HostAdminConfig{}); err == nil {
		t.Error("empty config must fail")
	}
}

func TestHostAdminApproveRequestRegistersClient(t *testing.T) {
	e := newAdminEnv(t)
	r := e.request(t, "nodeA", "bob-laptop")

	c, err := e.admin.ApproveRequest(e.ctx, r.ID, ApproveInput{DisplayName: "  Bob の PC ", MCPEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.DisplayName != "Bob の PC" || c.StableNodeID != "nodeA" || c.LoginName != "nodeA@example.com" ||
		c.WebEnabled || !c.MCPEnabled || c.AgentEnabled || c.ApprovalMode != domain.ApprovalAlways {
		t.Errorf("client = %+v", c)
	}
	// 許可リストに登録され、要求は pending から外れる。
	if got, err := e.db.Clients().ByNode(e.ctx, "nodeA"); err != nil || got.ID != c.ID {
		t.Errorf("ByNode = %+v, %v", got, err)
	}
	pending, _ := e.admin.PendingRequests(e.ctx)
	if len(pending) != 0 {
		t.Errorf("pending = %+v", pending)
	}
	// 監査は host、トークンやハッシュを含まない。
	a := e.auditEntry("auth.connreq_approve")
	if a == nil || a.ActorType != domain.ActorHost || a.ClientID != c.ID || a.Result != "ok" || a.Kind != "auth" {
		t.Fatalf("audit = %+v", a)
	}
	var detail map[string]any
	if err := json.Unmarshal(a.DetailJSON, &detail); err != nil || detail["mcp_enabled"] != true || detail["web_enabled"] != false {
		t.Errorf("detail = %s (%v)", a.DetailJSON, err)
	}
	// connection-requests が通知される。
	if got := recvEvent(t, e.events); got.Topic != TopicConnectionRequests {
		t.Errorf("event = %+v", got)
	}
}

func TestHostAdminApproveUsesRequestNameWhenBlank(t *testing.T) {
	e := newAdminEnv(t)
	c := e.approve(t, "nodeA", "bob-laptop", ApproveInput{DisplayName: "   "})
	if c.DisplayName != "bob-laptop" {
		t.Errorf("DisplayName = %q", c.DisplayName)
	}
}

func TestHostAdminApproveAndDenyRequireAPendingRequest(t *testing.T) {
	e := newAdminEnv(t)
	r := e.request(t, "nodeA", "bob-laptop")
	if _, err := e.admin.ApproveRequest(e.ctx, "no-such", ApproveInput{}); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("unknown request: %v", err)
	}
	if err := e.admin.DenyRequest(e.ctx, "no-such"); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("deny unknown request: %v", err)
	}
	if err := e.admin.DenyRequest(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	// 拒否済みは許可できない・拒否し直せない。
	if _, err := e.admin.ApproveRequest(e.ctx, r.ID, ApproveInput{}); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("approve denied request: %v", err)
	}
	if err := e.admin.DenyRequest(e.ctx, r.ID); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("deny twice: %v", err)
	}
	if cs, _ := e.admin.Clients(e.ctx); len(cs) != 0 {
		t.Errorf("deny must not register a client: %+v", cs)
	}
	// 拒否後 24 時間は同デバイスの再要求を作らない（repository の規則）。
	if denied, err := e.db.ConnRequests().DeniedActive(e.ctx, "nodeA", e.clock.Now()); err != nil || !denied {
		t.Errorf("DeniedActive = %v, %v", denied, err)
	}
	a := e.auditEntry("auth.connreq_deny")
	if a == nil || a.ActorType != domain.ActorHost || a.ClientLabel != "bob-laptop" {
		t.Errorf("audit = %+v", a)
	}
}

// ログイン名が変わった登録済みデバイスの再許可は、古いクライアントを（トークン・セッション・リースごと）置き換える。
func TestHostAdminApproveReplacesClientWhenLoginNameChanged(t *testing.T) {
	e := newAdminEnv(t)
	old := e.approve(t, "nodeA", "first", ApproveInput{MCPEnabled: true})
	plaintext, err := e.admin.IssueToken(e.ctx, old.ID, "t1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.leases.Acquire(e.ctx, domain.Actor{Kind: domain.ActorMCP, ClientID: old.ID, SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	// 同じ node の新しい pending（ログイン名が違う）。先の要求は approved なので作れる。
	r2, _, err := e.db.ConnRequests().UpsertPending(e.ctx, repository.ConnRequest{
		ID: "req-again", StableNodeID: "nodeA", LoginName: "carol@example.com", DisplayName: "again",
		ExpiresAt: e.clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	c, err := e.admin.ApproveRequest(e.ctx, r2.ID, ApproveInput{WebEnabled: true})
	if err != nil {
		t.Fatalf("approve after login name change: %v", err)
	}
	if c.ID == old.ID || c.LoginName != "carol@example.com" || c.MCPEnabled || !c.WebEnabled {
		t.Errorf("replacement = %+v (old %+v)", c, old)
	}
	if cs, _ := e.admin.Clients(e.ctx); len(cs) != 1 || cs[0].ID != c.ID {
		t.Errorf("clients = %+v", cs)
	}
	if got, err := e.db.Clients().ByNode(e.ctx, "nodeA"); err != nil || got.ID != c.ID {
		t.Errorf("ByNode = %+v, %v", got, err)
	}
	if pending, _ := e.admin.PendingRequests(e.ctx); len(pending) != 0 {
		t.Errorf("pending = %+v", pending)
	}
	// 古いトークンは消え、新しいクライアントでも使えない。
	if toks, _ := e.db.Tokens().ListByClient(e.ctx, old.ID); len(toks) != 0 {
		t.Errorf("old tokens must be removed: %+v", toks)
	}
	p := Principal{Client: c, Identity: Identity{LoginName: c.LoginName, StableNodeID: c.StableNodeID}}
	if _, err := e.authz.VerifyBearer(e.ctx, "100.64.0.9:1", p, "Bearer "+plaintext); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("old token must be rejected: %v", err)
	}
	// 古いクライアントのセッションは切断し、リースは解放する。
	if _, held := e.leases.Holder(); held {
		t.Error("lease of the replaced client must be released")
	}
	if want := []string{old.ID + "/mcp", old.ID + "/web"}; !equalStrings(e.sessions.clients, want) {
		t.Errorf("closed = %v", e.sessions.clients)
	}
	// 置き換えと許可を監査する。
	rep := e.auditEntry("client.replace")
	if rep == nil || rep.ActorType != domain.ActorHost || rep.ClientID != c.ID {
		t.Fatalf("replace audit = %+v", rep)
	}
	var detail map[string]any
	if err := json.Unmarshal(rep.DetailJSON, &detail); err != nil || detail["old_client_id"] != old.ID ||
		detail["old_login_name"] != old.LoginName || detail["login_name"] != "carol@example.com" {
		t.Errorf("detail = %s (%v)", rep.DetailJSON, err)
	}
	if strings.Contains(string(rep.DetailJSON), plaintext) {
		t.Error("audit must not contain the token")
	}
	approved := false
	e.audit.mu.Lock()
	for _, a := range e.audit.entries {
		approved = approved || (a.Event == "auth.connreq_approve" && a.ClientID == c.ID)
	}
	e.audit.mu.Unlock()
	if !approved {
		t.Error("approve of the replacement must be audited")
	}
}

// 許可の確定に失敗したら、新しいクライアントは残さず、要求は pending のまま残る（置き換えた古いものは戻らない）。
func TestHostAdminApproveRollsBackNewClientWhenDecideFails(t *testing.T) {
	e := newAdminEnv(t)
	r := e.request(t, "nodeA", "pc")
	e.admin, _ = NewHostAdmin(HostAdminConfig{
		Clients: e.db.Clients(), Tokens: e.db.Tokens(), ConnRequests: failingDecide{e.db.ConnRequests()}, Authz: e.authz,
		Clock: e.clock, IDs: &seqIDs{},
	})
	if _, err := e.admin.ApproveRequest(e.ctx, r.ID, ApproveInput{}); err == nil {
		t.Fatal("approve must fail when the decision cannot be stored")
	}
	if cs, _ := e.admin.Clients(e.ctx); len(cs) != 0 {
		t.Errorf("clients = %+v", cs)
	}
	if pending, _ := e.admin.PendingRequests(e.ctx); len(pending) != 1 {
		t.Errorf("request must stay pending: %+v", pending)
	}
}

type failingDecide struct{ repository.ConnRequests }

func (failingDecide) Decide(context.Context, string, string, *string) error {
	return errors.New("db down")
}

// 期限切れの pending は一覧に出ず、許可もできない。
func TestHostAdminExpiredRequestIsNotListedNorApprovable(t *testing.T) {
	e := newAdminEnv(t)
	r := e.request(t, "nodeA", "pc") // 期限は 1 時間後
	e.clock.Advance(time.Hour)
	if pending, err := e.admin.PendingRequests(e.ctx); err != nil || len(pending) != 0 {
		t.Errorf("pending = %+v, %v", pending, err)
	}
	if _, err := e.admin.ApproveRequest(e.ctx, r.ID, ApproveInput{}); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("approve expired request: %v", err)
	}
	if err := e.admin.DenyRequest(e.ctx, r.ID); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("deny expired request: %v", err)
	}
	if cs, _ := e.admin.Clients(e.ctx); len(cs) != 0 {
		t.Errorf("clients = %+v", cs)
	}
}

// 同じデバイスの pending を 5 秒以内に更新済みなら DB に書かない。期限切れ後の再要求は新しい要求になる。
func TestRecordConnectionRequestSkipsRecentTouchAndRenewsExpired(t *testing.T) {
	e := newAdminEnv(t)
	id := Identity{LoginName: "alice@example.com", StableNodeID: "nodeA", NodeName: "pc"}
	record := func() {
		t.Helper()
		if err := e.authz.RecordConnectionRequest(e.ctx, id, "100.64.0.9"); err != nil {
			t.Fatal(err)
		}
	}
	only := func() repository.ConnRequest {
		t.Helper()
		rs, err := e.db.ConnRequests().ListPending(e.ctx)
		if err != nil || len(rs) != 1 {
			t.Fatalf("pending = %+v, %v", rs, err)
		}
		return rs[0]
	}

	record()
	first := only()
	e.clock.Advance(connRequestTouchInterval - time.Millisecond)
	record()
	if got := only(); got.RequestCount != 1 || !got.LastSeenAt.Equal(first.LastSeenAt) {
		t.Errorf("a request within %v must not write: %+v", connRequestTouchInterval, got)
	}
	e.clock.Advance(time.Millisecond)
	record()
	if got := only(); got.RequestCount != 2 || !got.LastSeenAt.Equal(e.clock.Now()) {
		t.Errorf("a request after %v must be recorded: %+v", connRequestTouchInterval, got)
	}

	// 期限（最初の要求から 24 時間）を過ぎたら、新しい要求を作る。
	e.clock.Advance(connRequestPendingTTL)
	if rs, _ := e.db.ConnRequests().ListPending(e.ctx); len(rs) != 0 {
		t.Fatalf("expired request is listed: %+v", rs)
	}
	record()
	if got := only(); got.ID == first.ID || got.RequestCount != 1 || !got.ExpiresAt.Equal(e.clock.Now().Add(connRequestPendingTTL)) {
		t.Errorf("renewed request = %+v (first %s)", got, first.ID)
	}
}

func TestHostAdminUpdateClientDisablesSessionsAndLease(t *testing.T) {
	e := newAdminEnv(t)
	c := e.approve(t, "nodeA", "pc", ApproveInput{MCPEnabled: true, WebEnabled: true})
	holder := domain.Actor{Kind: domain.ActorMCP, ClientID: c.ID, Label: "pc", SessionID: "s1"}
	if _, err := e.leases.Acquire(e.ctx, holder); err != nil {
		t.Fatal(err)
	}
	off := false

	// Web を無効にしても、MCP のリースは保持されたまま。
	got, err := e.admin.UpdateClient(e.ctx, c.ID, ClientUpdate{WebEnabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if got.WebEnabled || !got.MCPEnabled {
		t.Errorf("client = %+v", got)
	}
	if _, held := e.leases.Holder(); !held {
		t.Error("disabling web must not release the MCP lease")
	}
	if want := []string{c.ID + "/web"}; !equalStrings(e.sessions.clients, want) {
		t.Errorf("closed = %v, want %v", e.sessions.clients, want)
	}

	// MCP を無効にすると、MCP のセッションを切断してリースを解放する。
	got, err = e.admin.UpdateClient(e.ctx, c.ID, ClientUpdate{MCPEnabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if got.MCPEnabled {
		t.Errorf("client = %+v", got)
	}
	if _, held := e.leases.Holder(); held {
		t.Error("disabling mcp must release the lease")
	}
	if want := []string{c.ID + "/web", c.ID + "/mcp"}; !equalStrings(e.sessions.clients, want) {
		t.Errorf("closed = %v, want %v", e.sessions.clients, want)
	}

	// 有効にする・すでに無効のものを無効にするだけなら切断しない。
	on := true
	if _, err := e.admin.UpdateClient(e.ctx, c.ID, ClientUpdate{AgentEnabled: &on, MCPEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if len(e.sessions.clients) != 2 {
		t.Errorf("unexpected disconnects: %v", e.sessions.clients)
	}
	if a := e.auditEntry("client.update"); a == nil || a.ActorType != domain.ActorHost || a.ClientID != c.ID {
		t.Errorf("audit = %+v", a)
	}
	if _, err := e.admin.UpdateClient(e.ctx, "no-such", ClientUpdate{WebEnabled: &on}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown client: %v", err)
	}
}

func TestHostAdminDeleteClient(t *testing.T) {
	e := newAdminEnv(t)
	c := e.approve(t, "nodeA", "pc", ApproveInput{MCPEnabled: true})
	if _, err := e.admin.IssueToken(e.ctx, c.ID, "t1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.leases.Acquire(e.ctx, domain.Actor{Kind: domain.ActorMCP, ClientID: c.ID, SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}

	if err := e.admin.DeleteClient(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.Client(e.ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Client after delete = %v", err)
	}
	if toks, _ := e.db.Tokens().ListByClient(e.ctx, c.ID); len(toks) != 0 {
		t.Errorf("tokens must be removed with the client: %+v", toks)
	}
	if _, held := e.leases.Holder(); held {
		t.Error("lease must be released")
	}
	if want := []string{c.ID + "/mcp", c.ID + "/web"}; !equalStrings(e.sessions.clients, want) {
		t.Errorf("closed = %v", e.sessions.clients)
	}
	if a := e.auditEntry("client.delete"); a == nil || a.ActorType != domain.ActorHost || a.ClientLabel != "pc" {
		t.Errorf("audit = %+v", a)
	}
	if err := e.admin.DeleteClient(e.ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHostAdminIssueToken(t *testing.T) {
	e := newAdminEnv(t)
	c := e.approve(t, "nodeA", "pc", ApproveInput{MCPEnabled: true})

	week := 7 * 24 * time.Hour
	plaintext, err := e.admin.IssueToken(e.ctx, c.ID, " claude ", &week)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plaintext, "prm_") {
		t.Errorf("token format: %q", plaintext)
	}
	toks, err := e.admin.Tokens(e.ctx, c.ID)
	if err != nil || len(toks) != 1 {
		t.Fatalf("tokens = %+v, %v", toks, err)
	}
	if toks[0].Label != "claude" || toks[0].ExpiresAt == nil || !toks[0].ExpiresAt.Equal(e.clock.Now().Add(week)) || toks[0].Prefix != plaintext[:len(toks[0].Prefix)] {
		t.Errorf("token = %+v", toks[0])
	}

	// 監査に平文もハッシュも残さない。
	a := e.auditEntry("token.issue")
	if a == nil || a.ActorType != domain.ActorHost || a.Kind != "token" || a.ClientID != c.ID {
		t.Fatalf("audit = %+v", a)
	}
	if strings.Contains(string(a.DetailJSON), plaintext) || strings.Contains(string(a.DetailJSON), "prm_") {
		t.Errorf("audit detail leaks the token: %s", a.DetailJSON)
	}
	var detail map[string]any
	_ = json.Unmarshal(a.DetailJSON, &detail)
	if detail["label"] != "claude" || detail["expires_in_sec"] != float64(week/time.Second) {
		t.Errorf("detail = %v", detail)
	}

	// 既定は 90 日、0 は無期限。
	if _, err := e.admin.IssueToken(e.ctx, c.ID, "default", nil); err != nil {
		t.Fatal(err)
	}
	forever := time.Duration(0)
	if _, err := e.admin.IssueToken(e.ctx, c.ID, "forever", &forever); err != nil {
		t.Fatal(err)
	}
	toks, _ = e.admin.Tokens(e.ctx, c.ID)
	byLabel := map[string]repository.TokenRecord{}
	for _, tok := range toks {
		byLabel[tok.Label] = tok
	}
	if d := byLabel["default"].ExpiresAt; d == nil || !d.Equal(e.clock.Now().Add(DefaultTokenTTL)) {
		t.Errorf("default expiry = %v", d)
	}
	if byLabel["forever"].ExpiresAt != nil {
		t.Errorf("forever expiry = %v", byLabel["forever"].ExpiresAt)
	}
}

func TestHostAdminIssueTokenErrors(t *testing.T) {
	e := newAdminEnv(t)
	c := e.approve(t, "nodeA", "pc", ApproveInput{MCPEnabled: true})
	if _, err := e.admin.IssueToken(e.ctx, c.ID, "dup", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.IssueToken(e.ctx, c.ID, "dup", nil); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("duplicate label: %v", err)
	}
	if _, err := e.admin.IssueToken(e.ctx, c.ID, "  ", nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("blank label: %v", err)
	}
	if _, err := e.admin.IssueToken(e.ctx, "no-such", "x", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown client: %v", err)
	}
	// 失敗した発行は監査に記録しない（成功の 1 件だけ）。
	n := 0
	for _, ev := range e.audit.events() {
		if ev == "token.issue" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("token.issue audit entries = %d, want 1", n)
	}
}

func TestHostAdminRevokeToken(t *testing.T) {
	e := newAdminEnv(t)
	c := e.approve(t, "nodeA", "pc", ApproveInput{MCPEnabled: true})
	other := e.approve(t, "nodeB", "phone", ApproveInput{MCPEnabled: true})
	if _, err := e.admin.IssueToken(e.ctx, c.ID, "claude", nil); err != nil {
		t.Fatal(err)
	}
	toks, _ := e.admin.Tokens(e.ctx, c.ID)
	id := toks[0].ID

	// 別のクライアントの URL では失効できない。
	if err := e.admin.RevokeToken(e.ctx, other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke via another client: %v", err)
	}
	if err := e.admin.RevokeToken(e.ctx, c.ID, id); err != nil {
		t.Fatal(err)
	}
	toks, _ = e.admin.Tokens(e.ctx, c.ID)
	if toks[0].RevokedAt == nil || !toks[0].RevokedAt.Equal(e.clock.Now()) {
		t.Errorf("RevokedAt = %v", toks[0].RevokedAt)
	}
	if !equalStrings(e.sessions.tokens, []string{id}) {
		t.Errorf("closed tokens = %v", e.sessions.tokens)
	}
	if a := e.auditEntry("token.revoke"); a == nil || a.ActorType != domain.ActorHost || a.Kind != "token" {
		t.Errorf("audit = %+v", a)
	}
	// 失効済みへの再実行は元の時刻を保つ。
	e.clock.Advance(time.Hour)
	if err := e.admin.RevokeToken(e.ctx, c.ID, id); err != nil {
		t.Fatal(err)
	}
	again, _ := e.admin.Tokens(e.ctx, c.ID)
	if !again[0].RevokedAt.Equal(*toks[0].RevokedAt) {
		t.Errorf("RevokedAt changed: %v -> %v", toks[0].RevokedAt, again[0].RevokedAt)
	}
	if err := e.admin.RevokeToken(e.ctx, c.ID, "no-such"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: %v", err)
	}
	if err := e.admin.RevokeToken(e.ctx, "no-such", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown client: %v", err)
	}
}
