package ws_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/ws"
)

// このファイルは ws のテストの道具（偽の Streamer・キルスイッチ・Control と、実 ViewerSessions・LeaseManager をつないだ環境）。
// 固定時間の待ちは使わない。メッセージはタイムアウト付きの受信、状態は上限付きの条件待ち（waitUntil）で同期する。

const wait = 5 * time.Second

// monitors は偽の Control が返すモニター。プライマリは Index 1。
var monitors = []domain.Monitor{
	{Index: 0, Name: "left", Origin: domain.Point{X: -1280}, Size: domain.Size{W: 1280, H: 720}, DPI: 96},
	{Index: 1, Name: "main", Primary: true, Size: domain.Size{W: 1920, H: 1080}, DPI: 144},
}

var alice = domain.Client{ID: "c-alice", StableNodeID: "nodeA", LoginName: "alice@example.com", DisplayName: "Alice", WebEnabled: true}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }

type seqIDs struct{ n atomic.Int64 }

func (s *seqIDs) NewID() string { return "id-" + string(rune('a'+s.n.Add(1))) }

// waitUntil は cond が真になるまで上限付きで待つ。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeViewer は fakeStreamer が登録した閲覧者。
type fakeViewer struct {
	frames chan []byte
	done   chan struct{}
	v      *service.Viewer

	mu  sync.Mutex
	cfg service.ViewerConfig
}

func (f *fakeViewer) config() service.ViewerConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

// end は Streamer が閲覧者を終えた（キャプチャの失敗）ことを模す。
func (f *fakeViewer) end() { closeOnce(f.done) }

var onces sync.Map

func closeOnce(ch chan struct{}) {
	if _, loaded := onces.LoadOrStore(ch, struct{}{}); !loaded {
		close(ch)
	}
}

// fakeStreamer は service.Streamer の偽物。モニターは 0・1 だけ、上限は max。
type fakeStreamer struct {
	mu      sync.Mutex
	max     int
	active  int
	viewers []*fakeViewer
	byID    map[string]*fakeViewer
}

func newFakeStreamer(max int) *fakeStreamer {
	return &fakeStreamer{max: max, byID: map[string]*fakeViewer{}}
}

func (s *fakeStreamer) Subscribe(ctx context.Context, c service.ViewerConfig) (*service.Viewer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active >= s.max {
		return nil, service.ErrTooManyViewers
	}
	if c.Monitor < 0 || c.Monitor > 1 {
		return nil, domain.ErrInvalidArgument
	}
	if c.Quality == 0 {
		c.Quality = 70
	}
	if c.FPS == 0 {
		c.FPS = 10
	}
	f := &fakeViewer{frames: make(chan []byte, 2), done: make(chan struct{}), cfg: c}
	id := "sv-" + string(rune('a'+len(s.viewers)))
	f.v = &service.Viewer{ID: id, Config: c, Frames: f.frames, Done: f.done}
	s.active++
	s.viewers = append(s.viewers, f)
	s.byID[id] = f
	context.AfterFunc(ctx, func() {
		closeOnce(f.done)
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	})
	return f.v, nil
}

func (s *fakeStreamer) Configure(id string, c service.ViewerConfig) error {
	s.mu.Lock()
	f, ok := s.byID[id]
	s.mu.Unlock()
	if !ok || c.Monitor < 0 || c.Monitor > 1 || c.Quality < 0 || c.Quality > 100 || c.FPS < 0 || c.FPS > 30 {
		return domain.ErrInvalidArgument
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg.Monitor = c.Monitor
	if c.Quality != 0 {
		f.cfg.Quality = c.Quality
	}
	if c.FPS != 0 {
		f.cfg.FPS = c.FPS
	}
	return nil
}

func (s *fakeStreamer) viewer(t *testing.T, i int) *fakeViewer {
	t.Helper()
	waitUntil(t, "a viewer to subscribe", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.viewers) > i
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewers[i]
}

func (s *fakeStreamer) activeViewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// fakeKill は service.ControlKillSwitch の偽物。
type fakeKill struct {
	active  atomic.Bool
	mu      sync.Mutex
	closers map[string]service.CloserFunc
}

func (k *fakeKill) Active() bool { return k.active.Load() }

func (k *fakeKill) RegisterCloser(name string, fn service.CloserFunc) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closers == nil {
		k.closers = map[string]service.CloserFunc{}
	}
	k.closers[name] = fn
}

// trigger は発動を模す: 状態を active にして、登録済みのクローザーを呼ぶ。
func (k *fakeKill) trigger(t *testing.T) {
	t.Helper()
	k.active.Store(true)
	k.mu.Lock()
	defer k.mu.Unlock()
	for name, fn := range k.closers {
		if err := fn(context.Background()); err != nil {
			t.Errorf("closer %s: %v", name, err)
		}
	}
}

// fakeControl は service.Control の偽物。Do は実 Control と同じく先にリースを検査し、通った入力を記録する。
type fakeControl struct {
	service.Control // 使わない窓口は nil（呼ばれたら panic で気づく）

	leases service.LeaseManager

	mu       sync.Mutex
	ops      []domain.Op
	who      []domain.Actor
	doErr    error
	monErr   error
	opSignal chan struct{}
}

func (c *fakeControl) Monitors(context.Context) ([]domain.Monitor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.monErr != nil {
		return nil, c.monErr
	}
	return monitors, nil
}

func (c *fakeControl) Do(_ context.Context, who domain.Actor, op domain.Op) (service.Result, error) {
	if err := c.leases.Check(who); err != nil {
		return service.Result{}, err
	}
	c.mu.Lock()
	err := c.doErr
	if err == nil {
		c.ops = append(c.ops, op)
		c.who = append(c.who, who)
	}
	c.mu.Unlock()
	if err == nil {
		c.opSignal <- struct{}{}
	}
	return service.Result{}, err
}

func (c *fakeControl) setDoErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.doErr = err
}

func (c *fakeControl) setMonitorsErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.monErr = err
}

// nextOp は Control に届いた次の入力を待って返す。
func (c *fakeControl) nextOp(t *testing.T) (domain.Actor, domain.Op) {
	t.Helper()
	select {
	case <-c.opSignal:
	case <-time.After(wait):
		t.Fatal("no input reached Control")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	i := len(c.ops) - 1
	return c.who[i], c.ops[i]
}

func (c *fakeControl) opCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ops)
}

// env は実 ViewerSessions・LeaseManager・Bus と偽の Streamer・Control・キルスイッチをつないだ /ws/screen のサーバー。
type env struct {
	t        *testing.T
	srv      *httptest.Server
	handler  *ws.Handler
	streamer *fakeStreamer
	kill     *fakeKill
	control  *fakeControl
	leases   service.LeaseManager
	sessions service.ViewerSessions
	bus      *service.Bus

	mu     sync.Mutex
	client *domain.Client // ClientFrom が返すクライアント。nil なら連鎖を通っていない
}

type envOption func(*ws.Config)

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	return newEnvWithLeases(t, nil, opts...)
}

// newEnvWithLeases は newEnv の LeaseManager の設定を leaseMutate で変える版（承認の経路のテスト用）。
func newEnvWithLeases(t *testing.T, leaseMutate func(*env, *service.LeaseConfig), opts ...envOption) *env {
	t.Helper()
	e := &env{t: t, streamer: newFakeStreamer(5), kill: &fakeKill{}, bus: service.NewBus()}
	c := alice
	e.client = &c

	var err error
	leaseCfg := service.LeaseConfig{Clock: fixedClock{}, IDs: &seqIDs{}, IdleTimeout: time.Hour, Kill: e.kill}
	if leaseMutate != nil {
		leaseMutate(e, &leaseCfg)
	}
	e.leases, err = service.NewLeaseManager(leaseCfg)
	if err != nil {
		t.Fatal(err)
	}
	e.sessions, err = service.NewViewerSessions(service.ViewerSessionsConfig{
		Streamer: e.streamer, Leases: e.leases, Kill: e.kill, Clock: fixedClock{}, IDs: &seqIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.control = &fakeControl{leases: e.leases, opSignal: make(chan struct{}, 64)}

	cfg := ws.Config{
		Sessions: e.sessions, Control: e.control, Leases: e.leases, Events: e.bus,
		ClientFrom: func(context.Context) (domain.Client, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.client == nil {
				return domain.Client{}, false
			}
			return *e.client, true
		},
	}
	for _, o := range opts {
		o(&cfg)
	}
	e.handler, err = ws.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(e.handler)
	ctx, cancel := context.WithCancel(context.Background())
	bridged := service.BridgeLeaseEvents(ctx, e.leases, e.bus)
	t.Cleanup(func() {
		cancel()
		<-bridged
		e.handler.Close()
		e.srv.Close()
	})
	return e
}

func (e *env) setClient(c *domain.Client) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.client = c
}

// issue は alice の viewer_id を発行する。
func (e *env) issue() string { return e.sessions.Issue(alice) }

func (e *env) url(viewerID string) string {
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws/screen"
	if viewerID != "" {
		u += "?viewer_id=" + viewerID
	}
	return u
}

// dial は /ws/screen につなぐ。
func (e *env) dial(viewerID string) *websocket.Conn {
	e.t.Helper()
	return e.dialWith(viewerID, &websocket.DialOptions{Subprotocols: []string{ws.Subprotocol}})
}

func (e *env) dialWith(viewerID string, opts *websocket.DialOptions) *websocket.Conn {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c, _, err := websocket.Dial(ctx, e.url(viewerID), opts)
	if err != nil {
		e.t.Fatalf("dial: %v", err)
	}
	e.t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

// connect は viewer_id を発行してつなぎ、ready を読み捨てる。
func (e *env) connect() (*websocket.Conn, string) {
	e.t.Helper()
	id := e.issue()
	c := e.dial(id)
	readType(e.t, c, "ready")
	return c, id
}

// viewerActor は viewer_id のリース保持者。
func (e *env) viewerActor(viewerID string) domain.Actor {
	e.t.Helper()
	a, err := e.sessions.Actor(viewerID, alice)
	if err != nil {
		e.t.Fatalf("Actor: %v", err)
	}
	return a
}

// readMsg は次のテキストフレームを JSON として読む。
func readMsg(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("got a %v frame, want text", typ)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("message is not JSON: %v: %s", err, data)
	}
	return m
}

// readType は次のテキストフレームを読み、type を確かめる。
func readType(t *testing.T, c *websocket.Conn, want string) map[string]any {
	t.Helper()
	m := readMsg(t, c)
	if m["type"] != want {
		t.Fatalf("got message %v, want type %q", m, want)
	}
	return m
}

// readBinary は次のバイナリフレームを読む。
func readBinary(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("got a %v frame, want binary", typ)
	}
	return data
}

// expectClose はサーバーが code で閉じることを確かめる（その前のメッセージは読み捨てる）。
func expectClose(t *testing.T, c *websocket.Conn, want websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	for {
		_, _, err := c.Read(ctx)
		if err == nil {
			continue
		}
		if got := websocket.CloseStatus(err); got != want {
			t.Fatalf("closed with %d (%v), want %d", got, err, want)
		}
		return
	}
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	sendRaw(t, c, websocket.MessageText, b)
}

func sendRaw(t *testing.T, c *websocket.Conn, typ websocket.MessageType, b []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := c.Write(ctx, typ, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// get は plain な GET（WebSocket ではない）。ハンドシェイク前の拒否の確認に使う。
func get(t *testing.T, url string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if header != nil {
		req.Header = header
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
