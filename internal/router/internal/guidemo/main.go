// Command guidemo は GUI ルーターを loopback で起動する、スクリーンショット・手動確認用のデモサーバー。
//
// 偽の service（internal/web/handler/gui/guitest）に、見た目の確認用の架空のデータを入れて配信する。
// 実際のトークン・個人情報は含まない（トークンの平文は発行のたびに乱数で作る）。cmd/puppet-remo には含まれず、
// 本番のバイナリには入らない。使い方:
//
//	task frontend:build && go run ./internal/router/internal/guidemo -addr 127.0.0.1:8899
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/router"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
	"github.com/ousiassllc/puppet-remo/static"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8899", "待ち受けるアドレス（loopback のみ）")
	flag.Parse()
	if err := run(*addr); err != nil {
		log.Fatal(err)
	}
}

func run(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("guidemo は loopback でだけ待ち受ける（-addr %q）", addr)
	}

	f := seed()
	deps := f.Deps()
	h, err := gui.New(deps)
	if err != nil {
		return err
	}
	assets, err := static.Handler()
	if err != nil {
		return err
	}
	handler, err := router.NewGUI(router.GUIConfig{Handler: h, Static: assets})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("guidemo: http://%s/", ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// seed は見た目の確認用の架空のデータ（実在しないホスト名・ユーザー・IP）を入れる。
func seed() *guitest.Fakes {
	f := guitest.New()
	now := time.Now()
	f.Clock.T = now
	f.KillSwitch.Now = time.Now

	// サーバー状態のカードで、長い値（Issue #43）が折り返されることを確認できるよう、IPv4 と完全形の長い IPv6、
	// [IPv6]:ポート の待ち受け、非常に長い MagicDNS 名（ラベル 1 つが 63 文字）を入れる。
	f.Status.V = service.ServerStatus{
		Tailscaled: service.TailscaledRunning,
		TailnetIPs: []string{"100.64.0.1", "fd7a:115c:a1e0:ab12:4840:3c75:8f2e:91d6"},
		DNSName:    "demo-host-with-a-very-long-machine-name-for-overflow-0123456789.tail-demo-example-network.ts.net",
		Port:       8765, Listening: true, ListenAddrs: []string{"100.64.0.1:8765", "[fd7a:115c:a1e0:ab12:4840:3c75:8f2e:91d6]:8765"},
	}
	f.Sessions.Items = []service.SessionInfo{
		{ID: "s1", ClientID: "client-001", Label: "demo-desktop", Kind: domain.ActorMCP, Since: now.Add(-12 * time.Minute)},
	}
	f.Leases.Held = true
	f.Leases.H = service.Holder{
		LeaseID: "lease-1", Actor: domain.Actor{Kind: domain.ActorMCP, ClientID: "client-001", Label: "demo-desktop", SessionID: "s1"},
		Since: now.Add(-3 * time.Minute), IdleRemaining: 4*time.Minute + 20*time.Second,
	}

	f.Admin.Cs = []domain.Client{
		{ID: "client-001", StableNodeID: "node-demo-1", LoginName: "demo-user@example.com", DisplayName: "demo-desktop", WebEnabled: true, MCPEnabled: true, ApprovalMode: domain.ApprovalAlways},
		{ID: "client-002", StableNodeID: "node-demo-2", LoginName: "demo-user@example.com", DisplayName: "demo-laptop", WebEnabled: true, MCPEnabled: false, ApprovalMode: domain.ApprovalAlways},
	}
	f.Admin.Requests = []service.ConnRequest{
		{ID: "req-001", StableNodeID: "node-demo-3", LoginName: "guest@example.com", DisplayName: "demo-phone", RemoteIP: "100.64.0.21", Status: service.ConnPending,
			RequestCount: 4, FirstSeenAt: now.Add(-40 * time.Minute), LastSeenAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(24 * time.Hour)},
		{ID: "req-002", StableNodeID: "node-demo-4", LoginName: "guest@example.com", DisplayName: "demo-tablet", RemoteIP: "100.64.0.22", Status: service.ConnPending,
			RequestCount: 1, FirstSeenAt: now.Add(-5 * time.Minute), LastSeenAt: now.Add(-5 * time.Minute), ExpiresAt: now.Add(24 * time.Hour)},
	}
	f.Admin.Requests = append(f.Admin.Requests, demoRequestHistory(now)...)
	f.Logs.Items = demoLogs(now)
	expires := now.Add(60 * 24 * time.Hour)
	used := now.Add(-time.Hour)
	revoked := now.Add(-24 * time.Hour)
	f.Admin.Toks = map[string][]service.TokenRecord{
		"client-001": {
			{ID: "token-001", ClientID: "client-001", Label: "claude-desktop", Prefix: "prm_Demo", ExpiresAt: &expires, LastUsedAt: &used, CreatedAt: now.Add(-30 * 24 * time.Hour)},
			{ID: "token-002", ClientID: "client-001", Label: "old-script", Prefix: "prm_Olds", ExpiresAt: &expires, RevokedAt: &revoked, CreatedAt: now.Add(-50 * 24 * time.Hour)},
		},
	}
	return f
}

// demoRequestHistory は接続要求の履歴の見た目の確認用（Issue #44）。許可・拒否・失効が混ざった処理済みの要求を、1 ページ（20 件）を
// 超える 26 件入れる（ページ送りが出る）。届いた日時は 40 分前の未処理の要求より前。
func demoRequestHistory(now time.Time) []service.ConnRequest {
	names := []string{"demo-phone", "demo-tablet", "demo-laptop", "demo-desktop", "demo-kiosk", "demo-tv", "demo-watch"}
	users := []string{"guest@example.com", "demo-user@example.com", "visitor@example.com"}
	var out []service.ConnRequest
	for i := range 26 {
		first := now.Add(-time.Hour - time.Duration(i)*5*time.Hour - 7*time.Minute)
		r := service.ConnRequest{
			ID: fmt.Sprintf("req-h%03d", i), StableNodeID: fmt.Sprintf("node-hist-%d", i),
			LoginName: users[i%len(users)], DisplayName: fmt.Sprintf("%s-%d", names[i%len(names)], i+1),
			RemoteIP: fmt.Sprintf("100.64.1.%d", 10+i), RequestCount: 1 + i%3, FirstSeenAt: first, LastSeenAt: first,
			ExpiresAt: first.Add(24 * time.Hour),
		}
		switch i % 4 {
		case 0, 1:
			decided := first.Add(time.Duration(2+i%7) * time.Minute)
			r.Status, r.DecidedAt = service.ConnApproved, &decided
		case 2:
			decided := first.Add(time.Duration(1+i%5) * time.Minute)
			r.Status, r.DecidedAt = service.ConnDenied, &decided
		default:
			r.Status = service.ConnExpired
		}
		out = append(out, r)
	}
	return out
}

// demoLogs はログ画面（Issue #45）の見た目の確認用。成功（INFO）・注意（WARN）・失敗（ERROR）が混ざった 60 件を新しい順に並べる
// （1 ページ 50 件を超えるのでページ送りが出る）。ホスト名・IP・トークンの ID は架空で、秘密（トークンの平文・ハッシュ）は入れない。
func demoLogs(now time.Time) []service.AppLogEntry {
	type spec struct {
		level  slog.Level
		msg    string
		attrs  []service.AppLogAttr
		offset time.Duration // 現在から遡る時間
	}
	a := func(kv ...string) []service.AppLogAttr {
		var out []service.AppLogAttr
		for i := 0; i+1 < len(kv); i += 2 {
			out = append(out, service.AppLogAttr{Key: kv[i], Value: kv[i+1]})
		}
		return out
	}
	specs := []spec{
		{slog.LevelError, "mcp tool failed", a("tool", "screenshot", "error", "capture failed: access denied"), 40 * time.Second},
		{slog.LevelInfo, "mcp tool succeeded", a("tool", "mouse_click", "client", "demo-desktop", "took_ms", "18"), 95 * time.Second},
		{slog.LevelWarn, "mcp tool was rejected", a("tool", "key_press", "client", "demo-laptop", "took_ms", "1", "code", "lease_held_by_other"), 2 * time.Minute},
		{slog.LevelInfo, "lease acquired", a("lease_id", "lease-1", "holder_kind", "mcp", "holder", "demo-desktop", "session", "s1"), 3 * time.Minute},
		{slog.LevelWarn, "bearer authentication failed", a("reason", "revoked", "device", "demo-laptop", "remote_ip", "100.64.0.12"), 6 * time.Minute},
		{slog.LevelInfo, "token issued", a("client_id", "client-001", "token_id", "tok-0007", "label", "desktop agent"), 9 * time.Minute},
		{slog.LevelInfo, "connection request approved", a("request_id", "req-h000", "client_id", "client-001", "device", "demo-desktop", "login_name", "demo-user@example.com", "replaced", "false"), 12 * time.Minute},
		{slog.LevelInfo, "connection request received", a("request_id", "req-h000", "device", "demo-desktop", "login_name", "demo-user@example.com", "remote_ip", "100.64.0.11"), 15 * time.Minute},
		{slog.LevelWarn, "kill switch triggered", a("source", "hotkey"), 20 * time.Minute},
		{slog.LevelInfo, "kill switch released", nil, 18 * time.Minute},
		{slog.LevelError, "tailnet listener stopped", a("err", "listen tcp 100.64.0.1:8765: bind: address already in use"), 31 * time.Minute},
		{slog.LevelWarn, "tailscaled is not reachable", a("state", "unavailable", "error", "pipe: The system cannot find the file specified."), 33 * time.Minute},
		{slog.LevelInfo, "tailnet listener is listening", a("addrs", "[100.64.0.1:8765 [fd7a:115c:a1e0:ab12:4840:3c75:8f2e:91d6]:8765]"), 44 * time.Minute},
		{slog.LevelInfo, "tailscaled is running", a("state", "running", "ips", "[100.64.0.1 fd7a:115c:a1e0:ab12:4840:3c75:8f2e:91d6]"), 45 * time.Minute},
		{slog.LevelInfo, "kill switch hotkey registered", a("hotkey", "Ctrl+Alt+Shift+K"), 46 * time.Minute},
		{slog.LevelInfo, "puppet-remo starting", a("version", "0.0.0-demo", "background", "false"), 47 * time.Minute},
	}
	entries := make([]service.AppLogEntry, 0, 60)
	add := func(s spec) {
		entries = append(entries, service.AppLogEntry{
			Time: now.Add(-s.offset), Level: s.level.String(), Severity: s.level, Message: s.msg, Attrs: s.attrs,
		})
	}
	for _, s := range specs {
		add(s)
	}
	// ページ送りを見せるための、古い時間帯の成功の記録。
	for i := range 44 {
		add(spec{slog.LevelInfo, "mcp tool succeeded", a("tool", "mouse_move", "client", "demo-desktop", "took_ms", fmt.Sprint(3+i%9)), time.Duration(50+i) * time.Minute})
	}
	slices.SortStableFunc(entries, func(x, y service.AppLogEntry) int { return y.Time.Compare(x.Time) })
	return entries
}
