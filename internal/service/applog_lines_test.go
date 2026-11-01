package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// 運用ログ（slog。FR-H13・NFR-O01）の行の契約: 主要な処理の成功と失敗が適切なレベルで出ること、秘密が出ないこと。

// logRecord は捕まえたログ 1 件。
type logRecord struct {
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

// logSink は slog.Handler。出た記録を溜める。
type logSink struct {
	mu   sync.Mutex
	recs []logRecord
}

func (s *logSink) logger() *slog.Logger { return slog.New(s) }

func (s *logSink) Enabled(context.Context, slog.Level) bool { return true }
func (s *logSink) WithAttrs([]slog.Attr) slog.Handler       { return s }
func (s *logSink) WithGroup(string) slog.Handler            { return s }
func (s *logSink) Handle(_ context.Context, r slog.Record) error {
	rec := logRecord{Level: r.Level, Msg: r.Message, Attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = fmt.Sprint(a.Value.Any())
		return true
	})
	s.mu.Lock()
	s.recs = append(s.recs, rec)
	s.mu.Unlock()
	return nil
}

func (s *logSink) all() []logRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]logRecord(nil), s.recs...)
}

// find は msg の記録を返す。
func (s *logSink) find(msg string) []logRecord {
	var out []logRecord
	for _, r := range s.all() {
		if r.Msg == msg {
			out = append(out, r)
		}
	}
	return out
}

// text は全記録を 1 つの文字列にする（秘密が含まれないことの確認用）。
func (s *logSink) text() string {
	var b strings.Builder
	for _, r := range s.all() {
		fmt.Fprintf(&b, "%v %s %v\n", r.Level, r.Msg, r.Attrs)
	}
	return b.String()
}

func (s *logSink) one(t *testing.T, msg string) logRecord {
	t.Helper()
	recs := s.find(msg)
	if len(recs) != 1 {
		t.Fatalf("log %q occurred %d times, want 1\n%s", msg, len(recs), s.text())
	}
	return recs[0]
}

func (r logRecord) want(t *testing.T, level slog.Level, attrs map[string]string) {
	t.Helper()
	if r.Level != level {
		t.Errorf("%q level = %v, want %v", r.Msg, r.Level, level)
	}
	for k, v := range attrs {
		if r.Attrs[k] != v {
			t.Errorf("%q attr %s = %q, want %q (attrs %v)", r.Msg, k, r.Attrs[k], v, r.Attrs)
		}
	}
}

// authzWithLog は bearerEnv の Authz を、ログを捕まえる Logger つきで作り直す。
func authzWithLog(t *testing.T, e *env) *logSink {
	t.Helper()
	sink := &logSink{}
	a, err := NewAuthz(AuthzConfig{
		WhoIs: e.whois, Clients: e.clients, Tokens: e.tokens, ConnRequests: e.reqs,
		Audit: e.audit, Clock: e.clock, IDs: &seqIDs{}, Logger: sink.logger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.authz = a
	return sink
}

// 認証の失敗は WARN（理由・デバイス・IP だけ。トークンもハッシュも出さない）。成功は何も出さない（リクエストごとのログを避ける）。
// 失敗の連続でレート制限に入ったら、窓ごとに 1 回だけ WARN。
func TestAuthLogsBearerFailuresWithoutSecrets(t *testing.T) {
	e, tok, p := bearerEnv(t)
	sink := authzWithLog(t, e)
	ctx := context.Background()
	addr := netAddr("100.64.0.5")

	if _, err := e.authz.VerifyBearer(ctx, addr, p, "Bearer "+tok); err != nil {
		t.Fatal(err)
	}
	if got := sink.all(); len(got) != 0 {
		t.Fatalf("a successful verification must not log: %v", got)
	}

	bad := "Bearer " + TokenPrefix + strings.Repeat("A", 43)
	if _, err := e.authz.VerifyBearer(ctx, addr, p, bad); err == nil {
		t.Fatal("expected a failure")
	}
	sink.one(t, "bearer authentication failed").want(t, slog.LevelWarn,
		map[string]string{"reason": "unknown", "device": "dev-nodeA", "remote_ip": "100.64.0.5"})

	for range bearerFailureLimit + 3 {
		_, _ = e.authz.VerifyBearer(ctx, addr, p, bad)
	}
	sink.one(t, "bearer authentication is rate limited").want(t, slog.LevelWarn, map[string]string{"device": "dev-nodeA"})

	hash := repository.HashToken(tok)
	for _, secret := range []string{tok, tok[len(TokenPrefix):], fmt.Sprintf("%x", hash[:]), bad, strings.Repeat("A", 43)} {
		if strings.Contains(sink.text(), secret) {
			t.Fatalf("log contains the secret %q:\n%s", secret, sink.text())
		}
	}
}

// 新しい接続要求ができたときだけ INFO（同じデバイスの再要求では出さない）。
func TestAuthLogsNewConnectionRequestOnce(t *testing.T) {
	e := newEnv(t)
	sink := authzWithLog(t, e)
	e.device("100.64.0.9", testNodeB, testUser)
	ctx := context.Background()
	for range 3 {
		_, err := e.authz.Authorize(ctx, Request{RemoteAddr: netAddr("100.64.0.9"), Feature: FeatureMCP})
		wantCode(t, err, domain.CodeDeviceNotRegistered)
		e.clock.Advance(connRequestTouchInterval + time.Second)
	}
	sink.one(t, "connection request received").want(t, slog.LevelInfo,
		map[string]string{"device": "host-" + testNodeB, "login_name": testUser, "remote_ip": "100.64.0.9"})
}

// 接続要求の許可・拒否、トークンの発行・失効、クライアントの更新・削除を INFO で出す。トークンの平文・ハッシュは出さない。
func TestHostAdminLogsDecisionsAndTokenLifecycleWithoutSecrets(t *testing.T) {
	e := newAdminEnv(t)
	sink := &logSink{}
	ids := &seqIDs{}
	authz, err := NewAuthz(AuthzConfig{
		WhoIs: &fakeWhoIs{}, Clients: e.db.Clients(), Tokens: e.db.Tokens(), ConnRequests: e.db.ConnRequests(),
		Audit: e.audit, Clock: e.clock, IDs: ids, Logger: sink.logger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.admin, err = NewHostAdmin(HostAdminConfig{
		Clients: e.db.Clients(), Tokens: e.db.Tokens(), ConnRequests: e.db.ConnRequests(), Authz: authz,
		Leases: e.leases, Sessions: e.sessions, Audit: e.audit, Bus: e.bus, Clock: e.clock, IDs: ids, Logger: sink.logger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := e.ctx

	c := e.approve(t, "n1", "phone", ApproveInput{MCPEnabled: true})
	sink.one(t, "connection request approved").want(t, slog.LevelInfo,
		map[string]string{"request_id": "req-n1", "client_id": c.ID, "device": "phone", "replaced": "false"})

	denied := e.request(t, "n2", "tablet")
	if err := e.admin.DenyRequest(ctx, denied.ID); err != nil {
		t.Fatal(err)
	}
	sink.one(t, "connection request denied").want(t, slog.LevelInfo, map[string]string{"request_id": "req-n2", "device": "tablet"})

	plaintext, err := e.admin.IssueToken(ctx, c.ID, "my-laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	sink.one(t, "token issued").want(t, slog.LevelInfo, map[string]string{"client_id": c.ID, "label": "my-laptop"})
	tokens, err := e.admin.Tokens(ctx, c.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens = %v, %v", tokens, err)
	}
	if err := e.admin.RevokeToken(ctx, c.ID, tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	sink.one(t, "token revoked").want(t, slog.LevelInfo, map[string]string{"client_id": c.ID, "token_id": tokens[0].ID})

	hash := repository.HashToken(plaintext)
	for _, secret := range []string{plaintext, plaintext[len(TokenPrefix):], fmt.Sprintf("%x", hash[:]), repository.TokenDisplayPrefix(plaintext)} {
		if strings.Contains(sink.text(), secret) {
			t.Fatalf("log contains the secret %q:\n%s", secret, sink.text())
		}
	}

	if _, err := e.admin.UpdateClient(ctx, c.ID, ClientUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.DeleteClient(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	sink.one(t, "client updated")
	sink.one(t, "client deleted").want(t, slog.LevelInfo, map[string]string{"client_id": c.ID})
}

// リースの取得・解放（理由つき）は INFO、取得の拒否も出す。Check（活動の更新）では出さない。
func TestLeaseLogsAcquireReleaseAndDenial(t *testing.T) {
	sink := &logSink{}
	clock := newCtlClock()
	timers := &ctlTimers{}
	l, err := NewLeaseManager(LeaseConfig{Clock: clock, IDs: &ctlIDs{}, IdleTimeout: time.Minute, AfterFunc: timers.AfterFunc, Logger: sink.logger()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := l.Acquire(ctx, ctlAlice); err != nil {
		t.Fatal(err)
	}
	sink.one(t, "lease acquired").want(t, slog.LevelInfo, map[string]string{"holder": "alice", "holder_kind": "mcp", "session": "sess-a"})

	for range 3 {
		_ = l.Check(ctlAlice)
	}
	bob := domain.Actor{Kind: domain.ActorMCP, ClientID: "client-b", Label: "bob", SessionID: "sess-b"}
	if _, err := l.Acquire(ctx, bob); err == nil {
		t.Fatal("bob must not get the lease")
	}
	sink.one(t, "lease acquire denied").want(t, slog.LevelInfo, map[string]string{"code": string(domain.CodeLeaseHeldByOther), "holder": "bob"})

	clock.Advance(30 * time.Second)
	l.Release(ctlAlice)
	sink.one(t, "lease released").want(t, slog.LevelInfo, map[string]string{"reason": string(domain.ReleaseExplicit), "holder": "alice", "held_for": "30s"})
	if n := len(sink.all()); n != 3 {
		t.Errorf("logged %d records, want 3 (acquired, denied, released; Check must not log): %v", n, sink.all())
	}
}

// キルスイッチの発動は WARN、解除は INFO。失敗は ERROR（発動の失敗・解除の失敗）。
func TestKillSwitchLogsTriggerAndRelease(t *testing.T) {
	sink := &logSink{}
	settings := newCtlSettings()
	k, err := NewKillSwitch(context.Background(), KillSwitchConfig{Settings: settings, Clock: newCtlClock(), Logger: sink.logger()})
	if err != nil {
		t.Fatal(err)
	}
	k.BindLeases(mustLeaseManager(t, newCtlClock(), k))
	if err := k.Trigger("hotkey"); err != nil {
		t.Fatal(err)
	}
	sink.one(t, "kill switch triggered").want(t, slog.LevelWarn, map[string]string{"source": "hotkey"})
	if err := k.Release(); err != nil {
		t.Fatal(err)
	}
	sink.one(t, "kill switch released").want(t, slog.LevelInfo, nil)

	if err := k.Trigger("tray"); err != nil {
		t.Fatal(err)
	}
	settings.setErr = errors.New("disk full")
	if err := k.Release(); err == nil {
		t.Fatal("want a release error")
	}
	sink.one(t, "kill switch release failed").want(t, slog.LevelError, map[string]string{"err": "persist kill switch state: disk full"})
	if n := len(sink.find("kill switch released")); n != 1 {
		t.Errorf("a failed release must not log success (released logged %d times)", n)
	}
}

// tailscaled の状態が変わったときだけ出す（同じ状態の通知では出さない）。
func TestStatusTrackerLogsTailscaledStateChangesOnly(t *testing.T) {
	sink := &logSink{}
	bus := NewBus()
	ch, cancel := bus.Subscribe(TopicServerStatus)
	defer cancel()
	w := &statusWatcher{ch: make(chan tailnet.Status, 1)}
	tr := NewStatusTracker(StatusTrackerConfig{Watcher: w, Bus: bus, Logger: sink.logger()})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()

	ip := netip.MustParseAddr("100.64.0.1")
	for _, st := range []tailnet.Status{
		{LoggedIn: true, IPs: []netip.Addr{ip}},
		{LoggedIn: true, IPs: []netip.Addr{ip}, DNSName: "host.example.ts.net"}, // 同じ状態（実行中）
		{Err: errors.New("pipe closed")},
		{LoggedIn: false},
	} {
		w.ch <- st
		recvEvent(t, ch)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sink.one(t, "tailscaled is running").want(t, slog.LevelInfo, map[string]string{"state": "running"})
	sink.one(t, "tailscaled is not reachable").want(t, slog.LevelWarn, map[string]string{"state": "unavailable", "error": "pipe closed"})
	sink.one(t, "tailscaled is not logged in").want(t, slog.LevelWarn, map[string]string{"state": "needs_login"})
}
