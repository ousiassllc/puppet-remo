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
//
// -agent-demo で、内蔵エージェント（run_task / get_task / cancel_task）をオフラインで最後まで動かせる。OpenAI も TypeSafe も呼ばない:
// app.Config.OpenAIModel（テスト・デモ専用の差し替え口）に台本つきの偽のモデル（クリック → 文字入力 → 最終回答。1 ステップ約 1.5 秒）を渡し、
// 偽の SecretStore には「登録済み」を示すだけのダミーのキーを入れる。設定でエージェントを有効にして同意を記録し、デモのクライアントの
// agent_enabled を有効にし、Bearer トークン（明らかなダミー。ログにも出る）を作る。タスクの実行中は、オーバーレイ（偽のウィンドウのログ）・
// ダッシュボードの実行中のタスク・/tasks が見られ、ホスト GUI の中断ボタンやキルスイッチで中断できる。-agent-safety-check を足すと、
// 台本の 2 ステップ目に OpenAI の安全チェックが付き、承認要求（kind=safety_check）の id がログに出る（-approval always のときはリース承認も同じ要領）。
//
// run_task の呼び出し（MCP の Streamable HTTP。Host は偽の tailnet IP。応答は JSON か SSE のどちらでもよい）:
//
//	T=prm_DEMO-dummy-token-not-a-secret-0123456789xxA
//	H='-H Host:100.64.0.1:8765 -H Authorization:Bearer'" $T"' -H Content-Type:application/json -H Accept:application/json,text/event-stream'
//	SID=$(curl -si $H -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}' //	  http://127.0.0.1:8765/mcp | tr -d '\r' | sed -n 's/^[Mm]cp-[Ss]ession-[Ii]d: //p')
//	curl -s $H -H "Mcp-Session-Id: $SID" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' http://127.0.0.1:8765/mcp
//	curl -s $H -H "Mcp-Session-Id: $SID" -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_task","arguments":{"prompt":"Type a greeting into Notes"}}}' http://127.0.0.1:8765/mcp
//	curl -s $H -H "Mcp-Session-Id: $SID" -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_task","arguments":{"task_id":"<task_id>"}}}' http://127.0.0.1:8765/mcp
//
// （リモートのルーターへ直接 curl する。ブラウザと違い、ホスト名の解決は要らない: Host ヘッダーだけを偽の tailnet IP にする。）
//
// -approval always にすると、デモのクライアントは毎回承認が要る（既定の auto は承認なしでリースを取る）。ビューアの取得ボタンは
// 決まるまで応答せず、承認要求の id がログに出る。ホスト GUI の /approvals/{id} で許可・拒否する（60 秒放置するとタイムアウト）。
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
	"github.com/ousiassllc/puppet-remo/internal/service"
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
	agentDemo := flag.Bool("agent-demo", false, "内蔵エージェントをオフラインで動かす（台本つきの偽のモデル。OpenAI・TypeSafe は呼ばない。使い方はこのファイルの先頭のコメント）")
	agentSafety := flag.Bool("agent-safety-check", false, "-agent-demo の台本の 2 ステップ目に OpenAI の安全チェック（承認ダイアログ）を付ける")
	approval := flag.String("approval", string(domain.ApprovalAuto), "デモのクライアントの承認モード（auto: 承認なしでリースを取る / always: 毎回承認。承認要求の id はログに出る）")
	flag.Parse()
	if err := run(*remoteAddr, *guiAddr, domain.ApprovalMode(*approval), *agentDemo, *agentSafety); err != nil {
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

func run(remoteAddr, guiAddr string, approval domain.ApprovalMode, agentDemo, agentSafety bool) error {
	if approval != domain.ApprovalAuto && approval != domain.ApprovalAlways {
		return fmt.Errorf("remotedemo: -approval は auto / always のどちらか（%q）", approval)
	}
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
		WebEnabled: true, MCPEnabled: true, AgentEnabled: agentDemo, ApprovalMode: approval, // 既定の auto は承認を待たずにリースを取る。always は承認ダイアログ（偽のウィンドウ）を開き、ホスト GUI の /approvals/{id} で決める
	}); err != nil {
		return err
	}

	secrets := platformtest.NewFakeSecretStore()
	var model service.AgentModel // nil なら本物（デモではキーが無いので呼ばれない）
	if agentDemo {
		if err := registerAgentDemo(ctx, db, secrets, registeredID); err != nil {
			return err
		}
		model = &demoModel{safetyCheck: agentSafety}
	}

	sc := newScene()
	clip := platformtest.NewFakeClipboard()
	clip.SetText("demo clipboard text (fictional)\nsecond line: https://example.com/demo")
	// 前面のウィンドウが無いと、入力は「ホストの画面を操作できません（ロック画面など）」で拒否される。
	hostWindows := platformtest.NewFakeWindows(domain.WindowInfo{
		ID: "1001", Title: "Notes - demo-notes.txt", Process: "notes.exe", PID: 4242, Foreground: true,
		Rect: domain.Rect{X: 153, Y: 57, W: 640, H: 446},
	})
	whois := &switchableWhoIs{}
	whois.set(registeredIdentity)
	watcher := &staticWatcher{st: tailnet.Status{LoggedIn: true, DNSName: demoDNSName, IPs: []netip.Addr{netip.MustParseAddr(demoTailnet)}}}

	windows := app.NewFakeWindows() // デモは Wails を使わない（承認ダイアログのウィンドウは開かず、id をログに出すだけ）
	go logApprovals(ctx, windows)

	// /apps（起動許可アプリ・起動の方式・拒否リスト）の手動確認用に、偽の実行ファイルをいくつか置く（実在のファイルではない）。
	launcher := platformtest.NewFakeAppLauncher()
	for _, p := range []string{`C:\Windows\System32\notepad.exe`, `C:\Windows\System32\calc.exe`, `C:\Windows\System32\cmd.exe`} {
		launcher.AddExecutable(p)
	}

	a, err := app.New(ctx, app.Config{
		Repo:    db,
		Windows: windows,
		Platform: app.Platform{
			Capturer: newCapturer(sc), Input: &input{FakeInput: platformtest.NewFakeInput(), scene: sc, log: logger},
			Windows: hostWindows, Clipboard: clip, UI: platformtest.NewFakeUIAutomation(hostWindows),
			Launcher: launcher, FilePicker: platformtest.NewFakeFilePicker(), Installer: platformtest.NewFakeInstaller(),
			Secrets: secrets,
			Hotkey:  platformtest.NewFakeHotkey(), Power: platformtest.NewFakePower(),
		},
		Watcher: watcher, WhoIs: whois,
		Static: mustStatic(), Logs: app.NewLogReader(dir, app.WithFolderOpener(platformtest.NewFakeFolderOpener())),
		Version: "demo", Logger: logger, OpenAIModel: model,
		// tailnet の IP の代わりに、-addr の loopback で待ち受ける（IPv4 の 1 本だけ）。
		// 接続の LocalAddr は偽の tailnet アドレスに見せる（MCP の SDK は loopback 宛ての Host 検査をするため。agentdemo.go）。
		ListenFunc: func(_ context.Context, _, addr string) (net.Listener, error) {
			ln, err := net.Listen("tcp4", remoteAddr)
			if err != nil {
				return nil, err
			}
			return tailnetFacade{Listener: ln, local: addr}, nil
		},
		Closers: []io.Closer{db},
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
	if agentDemo {
		logAgentDemo(remoteAddr)
	}
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

// logApprovals は承認ダイアログが開かれたこと（承認要求の id）をログに出す。-approval always で、ホスト GUI の
// /approvals/{id} を開いて許可・拒否するための手がかり。
func logApprovals(ctx context.Context, w *app.FakeWindows) {
	for {
		select {
		case <-ctx.Done():
			return
		case win := <-w.Opened():
			log.Printf("remotedemo: approval requested id=%s (host GUI /approvals/%s)", win.ID(), win.ID())
		}
	}
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
