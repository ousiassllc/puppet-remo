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
	"net"
	"net/http"
	"os"
	"os/signal"
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
