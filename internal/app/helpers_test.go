package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
	"github.com/ousiassllc/puppet-remo/static"
)

// waitLimit は「届くはずのものが届かない」ときにテストを失敗させる上限。待ち時間の仕様ではない。
const waitLimit = 5 * time.Second

const (
	testTailnetIP  = "100.64.0.1"
	testTailnetIP6 = "fd7a:115c:a1e0::1"
	testDNSName    = "host.tail1234.ts.net"
	testPort       = 8765 // service の既定の待ち受けポート（FR-S01）
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// eventually は cond が真になるまで、上限つきで繰り返し確認する。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeWatcher は tailnet.Watcher の偽物。Watch のたびに購読者を足し、Set で全員へ状態を流す（直近の状態は後から来た購読者にも届く）。
type fakeWatcher struct {
	mu   sync.Mutex
	last *tailnet.Status
	subs []chan tailnet.Status
}

func (w *fakeWatcher) Watch(ctx context.Context) (<-chan tailnet.Status, error) {
	ch := make(chan tailnet.Status, 8)
	w.mu.Lock()
	w.subs = append(w.subs, ch)
	if w.last != nil {
		ch <- *w.last
	}
	w.mu.Unlock()
	go func() {
		<-ctx.Done()
		w.mu.Lock()
		defer w.mu.Unlock()
		w.subs = slices.DeleteFunc(w.subs, func(c chan tailnet.Status) bool { return c == ch })
		close(ch)
	}()
	return ch, nil
}

func (w *fakeWatcher) Set(st tailnet.Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last = &st
	for _, ch := range w.subs {
		select {
		case ch <- st:
		default:
		}
	}
}

// runningStatus は tailnet にログイン済みで、ips を持つ状態。
func runningStatus(ips ...string) tailnet.Status {
	st := tailnet.Status{LoggedIn: true, DNSName: testDNSName}
	for _, ip := range ips {
		st.IPs = append(st.IPs, netip.MustParseAddr(ip))
	}
	return st
}

// loggedOutStatus は tailscaled には届くが、tailnet にログインしていない状態。
func loggedOutStatus() tailnet.Status { return tailnet.Status{} }

type fakeWhoIs struct {
	mu sync.Mutex
	id tailnet.Identity
}

func (f *fakeWhoIs) Lookup(context.Context, string) (tailnet.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id, nil
}

// listenLog は ListenFunc の呼び出しの記録。要求されたアドレスのそれぞれに、loopback の本物の Listener を返す
// （偽の Listener だけでは配信の経路を通せないため。要求されたアドレス自体には Listen しない）。
type listenLog struct {
	mu        sync.Mutex
	requested []string // "network address"
	bound     map[string]net.Listener
}

func (l *listenLog) listen(_ context.Context, network, address string) (net.Listener, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requested = append(l.requested, network+" "+address)
	if l.bound == nil {
		l.bound = map[string]net.Listener{}
	}
	l.bound[address] = ln
	return &advertisedListener{Listener: ln, advertised: tcpAddr(address)}, nil
}

func tcpAddr(address string) net.Addr {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		panic(err)
	}
	return net.TCPAddrFromAddrPort(ap)
}

// advertisedListener は、受け付けた接続の LocalAddr を、要求されたアドレス（tailnet の IP）に見せる。
// 実際には loopback で待ち受けているが、MCP SDK は「ローカルアドレスが loopback の接続」にだけ自前の Host 検査をかけるので、
// 本番（tailnet の IP で待ち受ける）と同じ条件にするため。
type advertisedListener struct {
	net.Listener
	advertised net.Addr
}

func (l *advertisedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &advertisedConn{Conn: c, local: l.advertised}, nil
}

type advertisedConn struct {
	net.Conn
	local net.Addr
}

func (c *advertisedConn) LocalAddr() net.Addr { return c.local }

func (l *listenLog) Requested() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.requested)
}

// localAddr は address として Listen を要求されたときに返した、loopback 上の実際の待ち受け先。
func (l *listenLog) localAddr(address string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ln, ok := l.bound[address]
	if !ok {
		return "", false
	}
	return ln.Addr().String(), true
}

// env は組み立て済みの App と、それを操作する偽物。
type env struct {
	t         *testing.T
	app       *App
	db        *repository.DB
	watcher   *fakeWatcher
	whois     *fakeWhoIs
	listens   *listenLog
	hotkey    *platformtest.FakeHotkey
	windows   *platformtest.FakeWindows
	power     *platformtest.FakePower
	input     *platformtest.FakeInput
	autostart *platformtest.FakeAutostart
	launcher  *platformtest.FakeAppLauncher
	picker    *platformtest.FakeFilePicker
	closers   *closeLog
}

// closeLog は Shutdown が閉じた資源の順序の記録。
type closeLog struct {
	mu    sync.Mutex
	names []string
}

func (c *closeLog) closer(name string) closerFunc {
	return func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.names = append(c.names, name)
		return nil
	}
}

func (c *closeLog) Names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.names)
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func openDB(t *testing.T) *repository.DB {
	t.Helper()
	db, err := repository.Open(context.Background(), filepath.Join(t.TempDir(), "puppet-remo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func staticFiles() http.Handler {
	return static.NewHandler(fstest.MapFS{
		"js/app.js":         {Data: []byte("export {}")},
		"css/app.css":       {Data: []byte("body{}")},
		"js/htmx.min.js":    {Data: []byte("htmx")},
		"js/htmx-config.js": {Data: []byte("htmx.config.allowEval=false")},
	}, true)
}

// newEnv は偽の platform・tailnet と一時ファイルの SQLite で App を組み立てる（動かすのは env.start）。
func newEnv(t *testing.T, mutate ...func(*Config)) *env {
	t.Helper()
	return newEnvWithDB(t, openDB(t), mutate...)
}

func newEnvWithDB(t *testing.T, db *repository.DB, mutate ...func(*Config)) *env {
	t.Helper()
	windows := platformtest.NewFakeWindows()
	e := &env{
		t: t, db: db, windows: windows,
		watcher: &fakeWatcher{},
		whois:   &fakeWhoIs{id: tailnet.Identity{LoginName: "alice@example.com", StableNodeID: "nodeA", NodeName: "alice-laptop"}},
		listens: &listenLog{}, hotkey: platformtest.NewFakeHotkey(), power: platformtest.NewFakePower(), input: platformtest.NewFakeInput(),
		autostart: platformtest.NewFakeAutostart(), closers: &closeLog{},
		launcher: platformtest.NewFakeAppLauncher(), picker: platformtest.NewFakeFilePicker(),
	}
	cfg := Config{
		Repo: db,
		Platform: Platform{
			Capturer: platformtest.NewFakeCapturer(), Input: e.input, Windows: windows,
			Clipboard: platformtest.NewFakeClipboard(), UI: platformtest.NewFakeUIAutomation(windows),
			Launcher: e.launcher, FilePicker: e.picker,
			Hotkey: e.hotkey, Power: e.power, Autostart: e.autostart,
		},
		Watcher:     e.watcher,
		WhoIs:       e.whois,
		Static:      staticFiles(),
		Logs:        NewLogReader(t.TempDir()),
		Version:     "0.0.0-test",
		Logger:      discardLogger(),
		ListenFunc:  e.listens.listen,
		ListenRetry: time.Millisecond,
		Closers:     []io.Closer{e.closers.closer("a"), e.closers.closer("b")},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	a, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.app = a
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		_ = a.Shutdown(ctx)
	})
	return e
}

// start は App を動かす。
func (e *env) start() {
	e.t.Helper()
	if err := e.app.Start(e.t.Context()); err != nil {
		e.t.Fatal(err)
	}
}

// waitListening は count 個のアドレスで Listen するまで待つ。
func (e *env) waitListening(count int) {
	e.t.Helper()
	eventually(e.t, "the listener to bind", func() bool { return len(e.app.listen.State().Addrs) == count })
}

// registerClient は alice の端末（WhoIs の偽物が返す node）を許可リストに登録し、Bearer トークンの平文を返す。
func (e *env) registerClient() (clientID, token string) {
	e.t.Helper()
	ctx := context.Background()
	c := domain.Client{ID: "client-1", StableNodeID: "nodeA", LoginName: "alice@example.com", DisplayName: "alice-laptop", MCPEnabled: true, WebEnabled: true}
	if err := e.db.Clients().Create(ctx, c); err != nil {
		e.t.Fatal(err)
	}
	token, err := e.app.admin.IssueToken(ctx, c.ID, "test", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID, token
}

// remoteTransport は loopback の実際の待ち受け先へ接続しつつ、tailnet の IP の Host ヘッダーと Bearer を付ける。
type remoteTransport struct {
	e     *env
	token string
}

func (rt remoteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = net.JoinHostPort(testTailnetIP, "8765")
	r.URL.Host = rt.localAddr()
	if rt.token != "" {
		r.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (rt remoteTransport) localAddr() string {
	rt.e.t.Helper()
	addr, ok := rt.e.listens.localAddr(net.JoinHostPort(testTailnetIP, "8765"))
	if !ok {
		rt.e.t.Fatal("the remote router is not listening on the tailnet IPv4 address")
	}
	return addr
}

// dialMCP は Bearer トークン token の MCP クライアントを、リモートルーターの Listener 経由でつなぐ。
func (e *env) dialMCP(token string) *mcpsdk.ClientSession {
	e.t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             "http://" + net.JoinHostPort(testTailnetIP, "8765") + "/mcp",
		HTTPClient:           &http.Client{Transport: remoteTransport{e: e, token: token}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		e.t.Fatalf("connect MCP: %v", err)
	}
	e.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args ...map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	params := &mcpsdk.CallToolParams{Name: name}
	if len(args) > 0 {
		params.Arguments = args[0]
	}
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// holder は今のリース保持者。
func (e *env) holder() (service.Holder, bool) { return e.app.leases.Holder() }
