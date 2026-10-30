package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// fakeClock は進行をテストが制御する domain.Clock。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// seqIDs は "id-1", "id-2", ... を返す domain.IDGen。
type seqIDs struct {
	mu sync.Mutex
	n  int
}

func (g *seqIDs) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("id-%d", g.n)
}

// fakeWhoIs は IP（ポートなし）→ Identity の表で解決する tailnet.WhoIs。表にない IP は失敗する。
type fakeWhoIs struct {
	mu    sync.Mutex
	ids   map[string]Identity
	err   error
	calls int
}

func (w *fakeWhoIs) Lookup(_ context.Context, remoteAddr string) (Identity, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.err != nil {
		return Identity{}, w.err
	}
	id, ok := w.ids[remoteIP(remoteAddr)]
	if !ok {
		return Identity{}, tailnet.ErrWhoIsFailed
	}
	return id, nil
}

// memClients は repository.Clients のメモリ実装（Authz が使う Get / ByNode だけ実装）。
type memClients struct {
	mu      sync.Mutex
	clients map[string]domain.Client
	err     error
}

func (m *memClients) Get(_ context.Context, id string) (domain.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Client{}, m.err
	}
	c, ok := m.clients[id]
	if !ok {
		return domain.Client{}, repository.ErrNotFound
	}
	return c, nil
}

func (m *memClients) ByNode(_ context.Context, node string) (domain.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Client{}, m.err
	}
	for _, c := range m.clients {
		if c.StableNodeID == node {
			return c, nil
		}
	}
	return domain.Client{}, repository.ErrNotFound
}

func (m *memClients) set(c domain.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients == nil {
		m.clients = map[string]domain.Client{}
	}
	m.clients[c.ID] = c
}

func (*memClients) List(context.Context) ([]domain.Client, error) { return nil, errors.New("unused") }
func (*memClients) Create(context.Context, domain.Client) error   { return errors.New("unused") }
func (*memClients) Replace(context.Context, string, domain.Client) error {
	return errors.New("unused")
}
func (*memClients) Update(context.Context, string, repository.ClientPatch) error {
	return errors.New("unused")
}
func (*memClients) Delete(context.Context, string) error               { return errors.New("unused") }
func (*memClients) TouchSeen(context.Context, string, time.Time) error { return errors.New("unused") }

// memTokens は repository.Tokens のメモリ実装。
type memTokens struct {
	mu      sync.Mutex
	records map[string]repository.TokenRecord // ID → 行
	touches int
	err     error // ByHash が返す障害
}

func (m *memTokens) Create(_ context.Context, t repository.TokenRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = map[string]repository.TokenRecord{}
	}
	for _, r := range m.records {
		if r.TokenHash == t.TokenHash || r.ClientID == t.ClientID && r.Label == t.Label {
			return repository.ErrConflict
		}
	}
	m.records[t.ID] = t
	return nil
}

func (m *memTokens) ByHash(_ context.Context, hash [32]byte) (repository.TokenRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return repository.TokenRecord{}, m.err
	}
	for _, r := range m.records {
		if r.TokenHash == hash {
			return r, nil
		}
	}
	return repository.TokenRecord{}, repository.ErrNotFound
}

func (m *memTokens) ListByClient(context.Context, string) ([]repository.TokenRecord, error) {
	return nil, errors.New("unused")
}

func (m *memTokens) Revoke(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return repository.ErrNotFound
	}
	if r.RevokedAt == nil {
		r.RevokedAt = &at
		m.records[id] = r
	}
	return nil
}

func (m *memTokens) TouchUsed(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return repository.ErrNotFound
	}
	r.LastUsedAt = &at
	m.records[id] = r
	m.touches++
	return nil
}

func (m *memTokens) get(id string) repository.TokenRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.records[id]
}

// memConnRequests は repository.ConnRequests のメモリ実装（集約と拒否後の抑止だけ再現する）。
type memConnRequests struct {
	mu   sync.Mutex
	rows []repository.ConnRequest
	err  error
}

func (m *memConnRequests) UpsertPending(_ context.Context, r repository.ConnRequest) (repository.ConnRequest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return repository.ConnRequest{}, false, m.err
	}
	for i, row := range m.rows {
		if row.StableNodeID == r.StableNodeID && row.Status == repository.ConnPending {
			row.LoginName, row.DisplayName, row.RemoteIP = r.LoginName, r.DisplayName, r.RemoteIP
			row.LastSeenAt = r.LastSeenAt
			row.RequestCount++
			m.rows[i] = row
			return row, false, nil
		}
	}
	r.Status, r.RequestCount = repository.ConnPending, 1
	m.rows = append(m.rows, r)
	return r, true, nil
}

func (m *memConnRequests) Decide(_ context.Context, id, status string, _ *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, row := range m.rows {
		if row.ID == id {
			row.Status = status
			row.ExpiresAt = row.LastSeenAt.Add(repository.DeniedTTL)
			m.rows[i] = row
			return nil
		}
	}
	return repository.ErrNotFound
}

func (m *memConnRequests) DeniedActive(_ context.Context, node string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	for _, row := range m.rows {
		if row.StableNodeID == node && row.Status == repository.ConnDenied && row.ExpiresAt.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func (m *memConnRequests) ListPending(context.Context) ([]repository.ConnRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []repository.ConnRequest
	for _, row := range m.rows {
		if row.Status == repository.ConnPending {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memConnRequests) Expire(context.Context, time.Time) error { return errors.New("unused") }

func (m *memConnRequests) snapshot() []repository.ConnRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]repository.ConnRequest(nil), m.rows...)
}

// recordingAudit は AuditSink の記録。
type recordingAudit struct {
	mu      sync.Mutex
	entries []repository.AuditEntry
}

func (a *recordingAudit) Record(_ context.Context, e repository.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func (a *recordingAudit) events() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.entries))
	for i, e := range a.entries {
		out[i] = e.Event
	}
	return out
}

// env はテスト 1 件分の Authz と、その偽の依存先。
type env struct {
	authz   Authz
	clock   *fakeClock
	whois   *fakeWhoIs
	clients *memClients
	tokens  *memTokens
	reqs    *memConnRequests
	audit   *recordingAudit
}

const (
	testNodeA = "nodeA"
	testNodeB = "nodeB"
	testUser  = "alice@example.com"
)

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		clock:   newFakeClock(),
		whois:   &fakeWhoIs{ids: map[string]Identity{}},
		clients: &memClients{},
		tokens:  &memTokens{},
		reqs:    &memConnRequests{},
		audit:   &recordingAudit{},
	}
	a, err := NewAuthz(AuthzConfig{
		WhoIs: e.whois, Clients: e.clients, Tokens: e.tokens, ConnRequests: e.reqs,
		Audit: e.audit, Clock: e.clock, IDs: &seqIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.authz = a
	return e
}

// device は ip の接続元を node / user として WhoIs に登録する。
func (e *env) device(ip, node, user string) Identity {
	id := Identity{LoginName: user, StableNodeID: node, NodeName: "host-" + node}
	e.whois.ids[ip] = id
	return id
}

// register は node のクライアントを許可リストに載せる（Web・MCP とも有効）。
func (e *env) register(clientID, node, user string) domain.Client {
	c := domain.Client{
		ID: clientID, StableNodeID: node, LoginName: user, DisplayName: "dev-" + node,
		WebEnabled: true, MCPEnabled: true,
	}
	e.clients.set(c)
	return c
}

func netAddr(ip string) string { return netip.AddrPortFrom(netip.MustParseAddr(ip), 51000).String() }

// wantCode は err が domain.Error で、そのコードが code であることを確かめる。
func wantCode(t *testing.T, err error, code domain.Code) {
	t.Helper()
	if got := domain.CodeOf(err); got != code {
		t.Fatalf("code = %q (err=%v), want %q", got, err, code)
	}
}
