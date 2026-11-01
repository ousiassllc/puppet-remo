// Command remotedemo はリモート Web UI（/viewer・/ws/screen・/static）とホスト GUI を loopback で起動する、スクリーンショット・
// 手動確認用のデモサーバー。
//
// app.New で本物の service・ルーター・WebSocket を組み立て、OS 依存部（画面・入力・クリップボードなど）と tailnet
// （tailscaled の状態・WhoIs）だけを偽物にする。画面は時間で変わる偽のデスクトップ、入力は偽の注入器が受けて画面とログに出す。
// 実際のトークン・ホスト名・個人情報は含まない。cmd/puppet-remo には含まれず、本番のバイナリには入らない。使い方:
//
//	task remote:demo
//
// リモートルーターは tailnet の IP の Host ヘッダーしか受け付けないため、ブラウザは Host を偽の tailnet IP にして接続する
// （例: google-chrome --host-resolver-rules="MAP 100.64.0.1 127.0.0.1" http://100.64.0.1:8765/viewer）。
// ホスト GUI（ダッシュボード）は別のポートで配信する。接続元のデバイスは、ホスト GUI のポートの
// /__demo/identity?as=registered|unregistered で切り替える（未登録なら待機ページになる）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/app"
	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
	"github.com/ousiassllc/puppet-remo/static"
)

const (
	// demoPort は service の既定の待ち受けポート。Host ヘッダーの検査は設定のポートと比べるので、リモートの待ち受けはこれに揃える。
	demoPort     = "8765"
	demoTailnet  = "100.64.0.1"
	demoDNSName  = "demo-host.tail-demo.ts.net"
	registeredID = "client-demo-1"
)

func main() {
	remoteAddr := flag.String("addr", "127.0.0.1:"+demoPort, "リモートルーターの待ち受けアドレス（loopback のみ。ポートは "+demoPort+" 固定）")
	guiAddr := flag.String("gui-addr", "127.0.0.1:8899", "ホスト GUI の待ち受けアドレス（loopback のみ）")
	flag.Parse()
	if err := run(*remoteAddr, *guiAddr); err != nil {
		log.Fatal(err)
	}
}

func checkLoopback(flagName, addr, wantPort string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("remotedemo は loopback でだけ待ち受ける（-%s %q）", flagName, addr)
	}
	if wantPort != "" && port != wantPort {
		return fmt.Errorf("-%s のポートは %s 固定（Host ヘッダーの検査が設定のポートと比べるため）: %q", flagName, wantPort, addr)
	}
	return nil
}

func run(remoteAddr, guiAddr string) error {
	if err := checkLoopback("addr", remoteAddr, demoPort); err != nil {
		return err
	}
	if err := checkLoopback("gui-addr", guiAddr, ""); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	dir, err := os.MkdirTemp("", "remotedemo-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	db, err := repository.Open(ctx, filepath.Join(dir, "puppet-remo.db"))
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := db.Clients().Create(ctx, domain.Client{
		ID: registeredID, StableNodeID: "node-demo-1", LoginName: "demo-user@example.com", DisplayName: "demo-laptop",
		WebEnabled: true, MCPEnabled: true, ApprovalMode: domain.ApprovalAuto, // デモは承認ダイアログを開けない（Wails が無い）ので、承認を待たずにリースを取る
	}); err != nil {
		return err
	}

	sc := newScene()
	clip := platformtest.NewFakeClipboard()
	clip.SetText("demo clipboard text (fictional)\nsecond line: https://example.com/demo")
	// 前面のウィンドウが無いと、入力は「ホストの画面を操作できません（ロック画面など）」で拒否される。
	windows := platformtest.NewFakeWindows(domain.WindowInfo{
		ID: "1001", Title: "Notes - demo-notes.txt", Process: "notes.exe", PID: 4242, Foreground: true,
		Rect: domain.Rect{X: 153, Y: 57, W: 640, H: 446},
	})
	whois := &switchableWhoIs{}
	whois.set(registeredIdentity)
	watcher := &staticWatcher{st: tailnet.Status{LoggedIn: true, DNSName: demoDNSName, IPs: []netip.Addr{netip.MustParseAddr(demoTailnet)}}}

	a, err := app.New(ctx, app.Config{
		Repo:    db,
		Windows: app.NewFakeWindows(), // デモは Wails を使わない（承認ダイアログは開かない。リースは承認なしで取る）
		Platform: app.Platform{
			Capturer: newCapturer(sc), Input: &input{FakeInput: platformtest.NewFakeInput(), scene: sc, log: logger},
			Windows: windows, Clipboard: clip, UI: platformtest.NewFakeUIAutomation(windows),
			Launcher: platformtest.NewFakeAppLauncher(), FilePicker: platformtest.NewFakeFilePicker(),
			Hotkey: platformtest.NewFakeHotkey(), Power: platformtest.NewFakePower(),
		},
		Watcher: watcher, WhoIs: whois,
		Static: mustStatic(), Logs: app.NewLogReader(dir),
		Version: "demo", Logger: logger,
		// tailnet の IP の代わりに、-addr の loopback で待ち受ける（IPv4 の 1 本だけ）。
		ListenFunc: func(_ context.Context, _, _ string) (net.Listener, error) { return net.Listen("tcp4", remoteAddr) },
		Closers:    []io.Closer{db},
	})
	if err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/__demo/identity", whois)
	mux.Handle("/", a.GUIHandler())
	gui := &http.Server{Addr: guiAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", guiAddr)
	if err != nil {
		return err
	}
	log.Printf("remotedemo: viewer http://%s:%s/viewer (Host は %s: --host-resolver-rules=\"MAP %s %s\")", demoTailnet, demoPort, demoTailnet, demoTailnet, "127.0.0.1")
	log.Printf("remotedemo: host GUI http://%s/  identity switch http://%s/__demo/identity?as=unregistered", ln.Addr(), ln.Addr())

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = gui.Shutdown(shutdown)
		_ = a.Shutdown(shutdown)
	}()
	if err := gui.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-ctx.Done()
	return nil
}

func mustStatic() http.Handler {
	h, err := static.Handler()
	if err != nil {
		log.Fatal(err)
	}
	return h
}

var (
	registeredIdentity   = tailnet.Identity{LoginName: "demo-user@example.com", StableNodeID: "node-demo-1", NodeName: "demo-laptop"}
	unregisteredIdentity = tailnet.Identity{LoginName: "guest@example.com", StableNodeID: "node-demo-9", NodeName: "demo-phone"}
)

// switchableWhoIs はどの接続元にも同じ（切り替えられる）デバイスを返す偽の WhoIs。
type switchableWhoIs struct {
	mu sync.Mutex
	id tailnet.Identity
}

func (w *switchableWhoIs) set(id tailnet.Identity) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.id = id
}

func (w *switchableWhoIs) Lookup(context.Context, string) (tailnet.Identity, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.id, nil
}

// ServeHTTP は /__demo/identity?as=registered|unregistered で接続元のデバイスを切り替える。
func (w *switchableWhoIs) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("as") {
	case "registered":
		w.set(registeredIdentity)
	case "unregistered":
		w.set(unregisteredIdentity)
	default:
		http.Error(rw, "as=registered|unregistered", http.StatusBadRequest)
		return
	}
	_, _ = fmt.Fprintf(rw, "identity: %s\n", r.URL.Query().Get("as"))
}

// staticWatcher は固定の tailnet の状態（ログイン済み・偽の IP と MagicDNS 名）を 1 度だけ流す偽の Watcher。
type staticWatcher struct{ st tailnet.Status }

func (w *staticWatcher) Watch(ctx context.Context) (<-chan tailnet.Status, error) {
	ch := make(chan tailnet.Status, 1)
	ch <- w.st
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}
