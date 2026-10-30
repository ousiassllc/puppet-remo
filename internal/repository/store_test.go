package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func newClient(id, node string) domain.Client {
	return domain.Client{
		ID: id, StableNodeID: node, LoginName: "alice@example.com", DisplayName: "dev-" + id,
		WebEnabled: true, MCPEnabled: true,
	}
}

func TestClientsCRUD(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	cs := db.Clients()

	if _, err := cs.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: err = %v, want ErrNotFound", err)
	}
	if _, err := cs.ByNode(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByNode missing: err = %v, want ErrNotFound", err)
	}

	c1 := newClient("c1", "node1")
	if err := cs.Create(ctx, c1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	c2 := newClient("c2", "node2")
	c2.AgentEnabled = true
	c2.ApprovalMode = domain.ApprovalFirstTime
	approved := clock.Now().Truncate(time.Millisecond)
	c2.ApprovedAt = &approved
	if err := cs.Create(ctx, c2); err != nil {
		t.Fatal(err)
	}

	// 既定の承認モードは always。
	c1.ApprovalMode = domain.ApprovalAlways
	got, err := cs.Get(ctx, "c1")
	if err != nil || !reflect.DeepEqual(got, c1) {
		t.Errorf("Get c1 = %+v, %v; want %+v", got, err, c1)
	}
	got, err = cs.ByNode(ctx, "node2")
	if err != nil || got.ID != "c2" || !got.AgentEnabled || got.ApprovedAt == nil || !got.ApprovedAt.Equal(approved) {
		t.Errorf("ByNode node2 = %+v, %v", got, err)
	}

	list, err := cs.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "c1" || list[1].ID != "c2" {
		t.Errorf("List = %+v, %v; want [c1 c2] in creation order", list, err)
	}

	// 重複・不正値。
	if err := cs.Create(ctx, newClient("c3", "node1")); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate stable_node_id: err = %v, want ErrConflict", err)
	}
	if err := cs.Create(ctx, newClient("c1", "node9")); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate id: err = %v, want ErrConflict", err)
	}
	bad := newClient("c4", "node4")
	bad.ApprovalMode = "sometimes"
	if err := cs.Create(ctx, bad); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("bad approval mode: err = %v, want ErrInvalidArgument", err)
	}

	// 削除。
	if err := cs.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Delete(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete twice: err = %v, want ErrNotFound", err)
	}
}

func TestClientsUpdate(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	mode := func(m domain.ApprovalMode) *domain.ApprovalMode { return &m }

	tests := []struct {
		name         string
		patch        ClientPatch
		want         func(c *domain.Client) // 期待する変更を初期値に適用する
		wantApproval bool                   // approved_at が残るか
		wantErr      error
	}{
		{"display name and login", ClientPatch{DisplayName: str("new"), LoginName: str("bob@example.com")},
			func(c *domain.Client) { c.DisplayName, c.LoginName = "new", "bob@example.com" }, true, nil},
		{"toggle flags", ClientPatch{WebEnabled: boolp(false), MCPEnabled: boolp(false), AgentEnabled: boolp(true)},
			func(c *domain.Client) { c.WebEnabled, c.MCPEnabled, c.AgentEnabled = false, false, true }, true, nil},
		{"empty patch changes nothing", ClientPatch{}, func(*domain.Client) {}, true, nil},
		{"same approval mode keeps approved_at", ClientPatch{ApprovalMode: mode(domain.ApprovalFirstTime)},
			func(*domain.Client) {}, true, nil},
		{"changing approval mode clears approved_at", ClientPatch{ApprovalMode: mode(domain.ApprovalAuto)},
			func(c *domain.Client) { c.ApprovalMode = domain.ApprovalAuto }, false, nil},
		{"invalid approval mode", ClientPatch{ApprovalMode: mode("sometimes")}, func(*domain.Client) {}, true,
			domain.ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, clock := openTestDB(t)
			cs := db.Clients()
			base := newClient("c1", "node1")
			base.ApprovalMode = domain.ApprovalFirstTime
			approved := clock.Now()
			base.ApprovedAt = &approved
			if err := cs.Create(ctx, base); err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Minute)

			err := cs.Update(ctx, "c1", tt.patch)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := base
			tt.want(&want)
			if !tt.wantApproval {
				want.ApprovedAt = nil
			}
			got, err := cs.Get(ctx, "c1")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("after update = %+v, %v; want %+v", got, err, want)
			}
			var updated int64
			if err := db.sql.QueryRow(`SELECT updated_at FROM clients WHERE id = 'c1'`).Scan(&updated); err != nil {
				t.Fatal(err)
			}
			if !time.UnixMilli(updated).Equal(clock.Now()) {
				t.Errorf("updated_at = %v, want %v", time.UnixMilli(updated), clock.Now())
			}
		})
	}

	t.Run("missing client", func(t *testing.T) {
		db, _ := openTestDB(t)
		if err := db.Clients().Update(ctx, "nope", ClientPatch{}); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestClientsTouchSeen(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	cs := db.Clients()
	if err := cs.Create(ctx, newClient("c1", "node1")); err != nil {
		t.Fatal(err)
	}
	var seen sql.NullInt64
	if err := db.sql.QueryRow(`SELECT last_seen_at FROM clients WHERE id = 'c1'`).Scan(&seen); err != nil || seen.Valid {
		t.Fatalf("last_seen_at before touch = %+v, %v; want NULL", seen, err)
	}
	at := clock.Now().Add(time.Hour)
	if err := cs.TouchSeen(ctx, "c1", at); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT last_seen_at FROM clients WHERE id = 'c1'`).Scan(&seen); err != nil ||
		!seen.Valid || !time.UnixMilli(seen.Int64).Equal(at) {
		t.Errorf("last_seen_at = %+v, %v; want %v", seen, err, at)
	}
	if err := cs.TouchSeen(ctx, "nope", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("TouchSeen missing: err = %v, want ErrNotFound", err)
	}
}

// 実行時に生成する偽のトークン（形式のみ prm_ + base64url の 32 バイト）。
func fakeToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "prm_" + base64.RawURLEncoding.EncodeToString(b)
}

func newTokenRecord(id, clientID, label, token string) TokenRecord {
	return TokenRecord{
		ID: id, ClientID: clientID, Label: label,
		TokenHash: HashToken(token), Prefix: TokenDisplayPrefix(token),
	}
}

func TestTokensLifecycle(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	if err := db.Clients().Create(ctx, newClient("c1", "node1")); err != nil {
		t.Fatal(err)
	}
	if err := db.Clients().Create(ctx, newClient("c2", "node2")); err != nil {
		t.Fatal(err)
	}
	ts := db.Tokens()

	tok1, tok2 := fakeToken(t), fakeToken(t)
	expires := clock.Now().Add(90 * 24 * time.Hour)
	rec1 := newTokenRecord("t1", "c1", "claude-code", tok1)
	rec1.ExpiresAt = &expires
	if err := ts.Create(ctx, rec1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	if err := ts.Create(ctx, newTokenRecord("t2", "c1", "other", tok2)); err != nil {
		t.Fatal(err)
	}
	// 同じラベルは別クライアントなら使える。同一クライアント・同一ハッシュは衝突。
	if err := ts.Create(ctx, newTokenRecord("t3", "c2", "claude-code", fakeToken(t))); err != nil {
		t.Errorf("same label on another client: %v", err)
	}
	if err := ts.Create(ctx, newTokenRecord("t4", "c1", "claude-code", fakeToken(t))); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate label: err = %v, want ErrConflict", err)
	}
	if err := ts.Create(ctx, newTokenRecord("t5", "c2", "x", tok1)); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate hash: err = %v, want ErrConflict", err)
	}

	got, err := ts.ByHash(ctx, HashToken(tok1))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t1" || got.ClientID != "c1" || got.Label != "claude-code" || got.Prefix != tok1[:8] ||
		got.TokenHash != sha256.Sum256([]byte(tok1)) || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) ||
		got.RevokedAt != nil || got.LastUsedAt != nil || !got.CreatedAt.Equal(newFakeClock().Now()) {
		t.Errorf("ByHash = %+v", got)
	}
	if _, err := ts.ByHash(ctx, HashToken(fakeToken(t))); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByHash unknown: err = %v, want ErrNotFound", err)
	}

	list, err := ts.ListByClient(ctx, "c1")
	if err != nil || len(list) != 2 || list[0].ID != "t1" || list[1].ID != "t2" {
		t.Errorf("ListByClient c1 = %+v, %v", list, err)
	}

	// 失効: 最初の失効時刻を保つ。
	revokedAt := clock.Now().Add(time.Minute)
	if err := ts.Revoke(ctx, "t1", revokedAt); err != nil {
		t.Fatal(err)
	}
	if err := ts.Revoke(ctx, "t1", revokedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	usedAt := revokedAt.Add(time.Second)
	if err := ts.TouchUsed(ctx, "t1", usedAt); err != nil {
		t.Fatal(err)
	}
	got, _ = ts.ByHash(ctx, HashToken(tok1))
	if got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) || got.LastUsedAt == nil || !got.LastUsedAt.Equal(usedAt) {
		t.Errorf("after revoke/touch = %+v", got)
	}
	for _, f := range []func(context.Context, string, time.Time) error{ts.Revoke, ts.TouchUsed} {
		if err := f(ctx, "nope", usedAt); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing token: err = %v, want ErrNotFound", err)
		}
	}

	// クライアントを消すとトークンも消える。
	if err := db.Clients().Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.ByHash(ctx, HashToken(tok2)); !errors.Is(err, ErrNotFound) {
		t.Errorf("token after client delete: err = %v, want ErrNotFound", err)
	}
	if list, _ := ts.ListByClient(ctx, "c2"); len(list) != 1 {
		t.Errorf("tokens of other client = %d, want 1", len(list))
	}
}

func TestHashTokenAndPrefix(t *testing.T) {
	// SHA-256("abc") の既知値（FIPS 180-2）。接頭辞を含む文字列全体をそのままハッシュする。
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	h := HashToken("abc")
	if got := bytesToHex(h[:]); got != abc {
		t.Errorf("HashToken(abc) = %s", got)
	}
	tests := []struct{ in, want string }{
		{"prm_Ab12cdefgh", "prm_Ab12"},
		{"short", "short"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := TokenDisplayPrefix(tt.in); got != tt.want {
			t.Errorf("TokenDisplayPrefix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func bytesToHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return string(out)
}

// NFR-S04: DB には SHA-256 のハッシュと表示用の先頭 8 文字だけが残り、平文は DB ファイル（WAL を含む）に出ない。
func TestTokenStoredAsHashOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	db, err := Open(ctx, path, WithClock(newFakeClock()))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Clients().Create(ctx, newClient("c1", "node1")); err != nil {
		t.Fatal(err)
	}
	token := fakeToken(t)
	if err := db.Tokens().Create(ctx, newTokenRecord("t1", "c1", "claude-code", token)); err != nil {
		t.Fatal(err)
	}

	var hash []byte
	var prefix string
	if err := db.sql.QueryRow(`SELECT token_hash, token_prefix FROM client_tokens WHERE id = 't1'`).Scan(&hash, &prefix); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(token))
	if !bytes.Equal(hash, want[:]) {
		t.Errorf("stored hash = %x, want SHA-256 of the token string %x", hash, want)
	}
	if prefix != token[:TokenPrefixLen] {
		t.Errorf("stored prefix = %q, want %q", prefix, token[:TokenPrefixLen])
	}

	assertNoPlaintext := func(stage string) {
		t.Helper()
		files, _ := filepath.Glob(path + "*")
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte(token[TokenPrefixLen:])) {
				t.Errorf("%s: plaintext token found in %s", stage, filepath.Base(f))
			}
		}
	}
	assertNoPlaintext("open (WAL present)")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext("closed")
}

func TestConnRequests(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	rs := db.ConnRequests()
	if err := db.Clients().Create(ctx, newClient("c1", "node1")); err != nil {
		t.Fatal(err)
	}

	req := func(id, node, ip string) ConnRequest {
		return ConnRequest{
			ID: id, StableNodeID: node, LoginName: "alice@example.com", DisplayName: "dev-" + node,
			RemoteIP: ip, ExpiresAt: clock.Now().Add(24 * time.Hour),
		}
	}

	// 1 デバイス 1 件に集約する。
	first, created, err := rs.UpsertPending(ctx, req("r1", "node1", "100.64.0.1"))
	if err != nil || !created || first.ID != "r1" || first.Status != ConnPending || first.RequestCount != 1 ||
		!first.FirstSeenAt.Equal(clock.Now()) || !first.LastSeenAt.Equal(clock.Now()) || first.DecidedAt != nil || first.ClientID != nil {
		t.Fatalf("first upsert = %+v, created=%v, err=%v", first, created, err)
	}
	clock.Advance(time.Hour)
	again := req("r2", "node1", "100.64.0.9")
	again.DisplayName = "renamed"
	again.LastSeenAt = clock.Now()
	again.ExpiresAt = clock.Now().Add(24 * time.Hour)
	got, created, err := rs.UpsertPending(ctx, again)
	if err != nil || created || got.ID != "r1" || got.RequestCount != 2 || got.RemoteIP != "100.64.0.9" ||
		got.DisplayName != "renamed" || !got.FirstSeenAt.Equal(first.FirstSeenAt) ||
		!got.LastSeenAt.Equal(clock.Now()) || !got.ExpiresAt.Equal(first.ExpiresAt) {
		t.Errorf("aggregated upsert = %+v, created=%v, err=%v", got, created, err)
	}
	if _, created, err := rs.UpsertPending(ctx, req("r3", "node2", "100.64.0.2")); err != nil || !created {
		t.Errorf("other node: created=%v err=%v", created, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM connection_requests`); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
	pending, err := rs.ListPending(ctx)
	if err != nil || len(pending) != 2 || pending[0].ID != "r1" || pending[1].ID != "r3" {
		t.Errorf("ListPending = %+v, %v", pending, err)
	}

	// 許可: client_id を記録し、同じデバイスから新しい pending を作れる。
	cid := "c1"
	if err := rs.Decide(ctx, "r1", ConnApproved, &cid); err != nil {
		t.Fatal(err)
	}
	var status string
	var decided sql.NullInt64
	var clientID sql.NullString
	if err := db.sql.QueryRow(`SELECT status, decided_at, client_id FROM connection_requests WHERE id = 'r1'`).
		Scan(&status, &decided, &clientID); err != nil {
		t.Fatal(err)
	}
	if status != ConnApproved || !decided.Valid || !time.UnixMilli(decided.Int64).Equal(clock.Now()) || clientID.String != "c1" {
		t.Errorf("after approve: %s %+v %+v", status, decided, clientID)
	}
	if err := rs.Decide(ctx, "r1", ConnDenied, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("re-deciding: err = %v, want ErrConflict", err)
	}
	if err := rs.Decide(ctx, "nope", ConnDenied, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("deciding missing: err = %v, want ErrNotFound", err)
	}
	if err := rs.Decide(ctx, "r3", ConnPending, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("deciding with pending status: err = %v, want ErrInvalidArgument", err)
	}
	if _, created, err := rs.UpsertPending(ctx, req("r4", "node1", "100.64.0.1")); err != nil || !created {
		t.Errorf("new pending after decision: created=%v err=%v", created, err)
	}

	// 拒否: 24 時間は抑止、過ぎたら解除。
	if active, err := rs.DeniedActive(ctx, "node2", clock.Now()); err != nil || active {
		t.Errorf("DeniedActive before deny = %v, %v", active, err)
	}
	if err := rs.Decide(ctx, "r3", ConnDenied, nil); err != nil {
		t.Fatal(err)
	}
	deniedAt := clock.Now()
	for _, tt := range []struct {
		name string
		at   time.Time
		node string
		want bool
	}{
		{"just denied", deniedAt, "node2", true},
		{"just before expiry", deniedAt.Add(DeniedTTL - time.Millisecond), "node2", true},
		{"at expiry", deniedAt.Add(DeniedTTL), "node2", false},
		{"other node", deniedAt, "node1", false},
	} {
		if got, err := rs.DeniedActive(ctx, tt.node, tt.at); err != nil || got != tt.want {
			t.Errorf("DeniedActive(%s) = %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
}

func TestConnRequestsExpire(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	rs := db.ConnRequests()
	start := clock.Now()

	mk := func(id, node string, expires time.Duration) {
		t.Helper()
		_, _, err := rs.UpsertPending(ctx, ConnRequest{
			ID: id, StableNodeID: node, LoginName: "l", DisplayName: "d", RemoteIP: "ip",
			ExpiresAt: start.Add(expires),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("short", "n1", time.Hour)
	mk("long", "n2", 48*time.Hour)

	// 失効: 期限を過ぎた pending だけが expired になる。
	if err := rs.Expire(ctx, start.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	rows, err := db.sql.Query(`SELECT id, status FROM connection_requests`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			t.Fatal(err)
		}
		statuses[id] = st
	}
	rows.Close()
	if !reflect.DeepEqual(statuses, map[string]string{"short": ConnExpired, "long": ConnPending}) {
		t.Errorf("statuses = %v", statuses)
	}
	// expired になったので、同じデバイスの新しい pending を作れる。
	if _, created, err := rs.UpsertPending(ctx, ConnRequest{
		ID: "again", StableNodeID: "n1", LoginName: "l", DisplayName: "d", RemoteIP: "ip",
		FirstSeenAt: start.Add(2 * time.Hour), ExpiresAt: start.Add(26 * time.Hour),
	}); err != nil || !created {
		t.Errorf("pending after expire: created=%v err=%v", created, err)
	}

	// 保持期間: last_seen_at から 7 日を過ぎた行を削除する。
	if err := rs.Expire(ctx, start.Add(ConnRequestRetention)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM connection_requests WHERE id IN ('short', 'long')`); n != 2 {
		t.Errorf("rows at exactly the retention boundary = %d, want 2 (kept)", n)
	}
	if err := rs.Expire(ctx, start.Add(ConnRequestRetention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM connection_requests WHERE id IN ('short', 'long')`); n != 0 {
		t.Errorf("old rows = %d, want 0 (deleted)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM connection_requests WHERE id = 'again'`); n != 1 {
		t.Errorf("recent row deleted")
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	ss := db.Settings()

	if v, ok, err := ss.Get(ctx, "server.port"); err != nil || ok || v != nil {
		t.Errorf("Get missing = %q, %v, %v", v, ok, err)
	}
	if all, err := ss.All(ctx); err != nil || len(all) != 0 {
		t.Errorf("All empty = %v, %v", all, err)
	}

	state := json.RawMessage(`{"active":true,"triggered_at":1,"source":"hotkey"}`)
	for _, kv := range []struct {
		k string
		v json.RawMessage
	}{{"server.port", json.RawMessage(`8765`)}, {"killswitch.state", state}} {
		if err := ss.Set(ctx, kv.k, kv.v); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Minute)
	if err := ss.Set(ctx, "server.port", json.RawMessage(`9000`)); err != nil { // 上書き
		t.Fatal(err)
	}

	if v, ok, err := ss.Get(ctx, "server.port"); err != nil || !ok || string(v) != "9000" {
		t.Errorf("Get server.port = %q, %v, %v", v, ok, err)
	}
	all, err := ss.All(ctx)
	if err != nil || len(all) != 2 || string(all["killswitch.state"]) != string(state) || string(all["server.port"]) != "9000" {
		t.Errorf("All = %v, %v", all, err)
	}
	var updated int64
	if err := db.sql.QueryRow(`SELECT updated_at FROM settings WHERE key = 'server.port'`).Scan(&updated); err != nil ||
		!time.UnixMilli(updated).Equal(clock.Now()) {
		t.Errorf("updated_at = %v, %v; want %v", time.UnixMilli(updated), err, clock.Now())
	}

	for _, bad := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{oops`)} {
		if err := ss.Set(ctx, "k", bad); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Set(%q): err = %v, want ErrInvalidArgument", bad, err)
		}
	}
	if _, ok, _ := ss.Get(ctx, "k"); ok {
		t.Error("invalid value was stored")
	}
}

func TestAuditAppend(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	if err := db.Clients().Create(ctx, newClient("c1", "node1")); err != nil {
		t.Fatal(err)
	}
	as := db.Audit()

	step, dur := 3, int64(42)
	full := AuditEntry{
		TS: clock.Now().Add(time.Second), Kind: "tool", Event: "tool.left_click", ActorType: domain.ActorMCP,
		ClientID: "c1", ClientLabel: "dev-c1", SessionID: "sess", LeaseID: "lease",
		StepNo: &step, Tool: "left_click", Result: "ok", ErrorCode: "", DurationMS: &dur,
		DetailJSON: json.RawMessage(`{"x":10,"y":20}`),
	}
	id1, err := as.Append(ctx, full)
	if err != nil || id1 <= 0 {
		t.Fatalf("Append full = %d, %v", id1, err)
	}
	// 最小の項目（未登録デバイスの認証失敗）。TS 省略は現在時刻。
	id2, err := as.Append(ctx, AuditEntry{
		Kind: "auth", Event: "auth.bearer_failed", ActorType: domain.ActorSystem,
		ClientLabel: "unknown-device", Result: "denied", ErrorCode: "unauthorized",
	})
	if err != nil || id2 <= id1 {
		t.Fatalf("Append minimal = %d, %v (previous %d)", id2, err, id1)
	}

	type row struct {
		ts                                                    int64
		kind, event, actor, label, result                     string
		clientID, session, lease, task, tool, errCode, detail sql.NullString
		step, dur                                             sql.NullInt64
	}
	read := func(id int64) (r row) {
		t.Helper()
		err := db.sql.QueryRow(`SELECT ts, kind, event, actor_type, client_label, result, client_id, session_id,
			lease_id, task_id, tool, error_code, detail_json, step_no, duration_ms FROM audit_logs WHERE id = ?`, id).
			Scan(&r.ts, &r.kind, &r.event, &r.actor, &r.label, &r.result, &r.clientID, &r.session,
				&r.lease, &r.task, &r.tool, &r.errCode, &r.detail, &r.step, &r.dur)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := read(id1)
	if !time.UnixMilli(r1.ts).Equal(full.TS) || r1.kind != "tool" || r1.event != "tool.left_click" || r1.actor != "mcp" ||
		r1.label != "dev-c1" || r1.result != "ok" || r1.clientID.String != "c1" || r1.session.String != "sess" ||
		r1.lease.String != "lease" || r1.tool.String != "left_click" || r1.step.Int64 != 3 || r1.dur.Int64 != 42 ||
		r1.detail.String != `{"x":10,"y":20}` || r1.task.Valid || r1.errCode.Valid {
		t.Errorf("full row = %+v", r1)
	}
	r2 := read(id2)
	if !time.UnixMilli(r2.ts).Equal(clock.Now()) || r2.errCode.String != "unauthorized" {
		t.Errorf("minimal row = %+v", r2)
	}
	if r2.clientID.Valid || r2.session.Valid || r2.lease.Valid || r2.task.Valid || r2.tool.Valid ||
		r2.detail.Valid || r2.step.Valid || r2.dur.Valid {
		t.Errorf("optional fields of minimal row must be NULL: %+v", r2)
	}

	bad := []struct {
		name string
		mod  func(*AuditEntry)
		want error
	}{
		{"result", func(e *AuditEntry) { e.Result = "maybe" }, nil},
		{"actor", func(e *AuditEntry) { e.ActorType = "robot" }, nil},
		{"detail", func(e *AuditEntry) { e.DetailJSON = json.RawMessage(`{oops`) }, domain.ErrInvalidArgument},
		{"unknown client", func(e *AuditEntry) { e.ClientID = "ghost" }, nil},
	}
	for _, tt := range bad {
		e := AuditEntry{Kind: "lease", Event: "lease.acquire", ActorType: domain.ActorMCP, ClientLabel: "d", Result: "ok"}
		tt.mod(&e)
		_, err := as.Append(ctx, e)
		if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs`); n != 2 {
		t.Errorf("rows = %d, want 2 (rejected entries must not be stored)", n)
	}

	// クライアントを消しても履歴は残り、表示名で辿れる。
	if err := db.Clients().Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if r := read(id1); r.clientID.Valid || r.label != "dev-c1" {
		t.Errorf("after client delete: %+v", r)
	}
}
