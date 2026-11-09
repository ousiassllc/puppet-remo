package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// 待ちの上限。条件が満たされたらすぐ返るので、通常はほぼ待たない。
const waitLimit = 5 * time.Second

// testAuthorization は連鎖（Authz）が検証済みのときにリクエストに残る Authorization ヘッダーの代わり。
// 値は SDK のミドルウェアを通すためだけで、検証には使われない。obviously fake。
const testAuthorization = "Bearer fake-test-token"

// clientHeader は、テストの PrincipalFunc がクライアント ID を読むヘッダー。
const clientHeader = "X-Test-Client"

// fixedClock は固定時刻の Clock。
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC) }

type seqIDs struct{ n atomic.Int64 }

func (g *seqIDs) NewID() string { return fmt.Sprintf("lease-%d", g.n.Add(1)) }

type fakeKill struct{ active atomic.Bool }

func (k *fakeKill) Active() bool { return k.active.Load() }

// call は fakeControl が受けた Do の記録。
type call struct {
	Who domain.Actor
	Op  domain.Op
}

// fakeControl は service.Control の偽物。リースの確認は本物の LeaseManager で、Control と同じ順序で行う
// （KillSwitch → LeaseManager.Check、未保持の mcp は暗黙に Acquire。FR-L04）。
type fakeControl struct {
	leases service.LeaseManager
	kill   service.KillSwitchState

	mu    sync.Mutex
	calls []call

	monitors  []domain.Monitor
	maxDim    int
	windows   []domain.WindowInfo
	shot      service.Shot
	cursor    service.Cursor
	clipboard string
	tree      *domain.UINode

	// apps は LaunchableApps が返す許可リスト（id と表示名だけ）。launchPID は Launch が返すプロセス ID。
	apps      []domain.AppSummary
	mode      domain.LaunchMode // LaunchableApps が返す方式。空は allowlist
	launchPID uint32
	// launches は Launch が受けた app_id の記録。launchErr が非 nil なら、検査（キルスイッチ・リース・許可リスト）の後に Launch がこれを返す。
	launches  []string
	launchReq []service.LaunchRequest
	launchErr error
	// launchWindow・launchReused は Launch が返す、起動後の前面のウィンドウと再利用の印。
	launchWindow *domain.WindowInfo
	launchReused bool

	// err が非 nil なら、Do・Screenshot・Monitors・Windows・CursorPosition がこれを返す（リースの確認の後）。
	err error
	// blockUntilDone が true なら、CursorPosition は ctx が終わるまで返らない（ツールのタイムアウトの検査用）。
	blockUntilDone bool
	// cursorEntered が非 nil なら、CursorPosition は入るたびにここへ通知する（ブロックの開始を待つテスト用）。
	cursorEntered chan struct{}
	// cursorRelease が非 nil なら、CursorPosition は ctx を無視して、これが閉じるまで返らない（遅い platform 呼び出しの再現）。
	cursorRelease chan struct{}
	// cursorCtxErr が非 nil なら、blockUntilDone の CursorPosition は ctx の終了理由をここへ送る。
	cursorCtxErr chan error
	// screenshotArgs は直近の Screenshot の引数。
	screenshotMonitor int
	screenshotFormat  string
}

func newFakeControl(leases service.LeaseManager, kill service.KillSwitchState) *fakeControl {
	return &fakeControl{
		leases: leases,
		kill:   kill,
		monitors: []domain.Monitor{
			{Index: 0, Name: `\\.\DISPLAY1`, Primary: true, Origin: domain.Point{}, Size: domain.Size{W: 2560, H: 1440}, DPI: 120},
		},
		maxDim: 1568,
	}
}

func (f *fakeControl) lastCall(t *testing.T) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("Control.Do was not called")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeControl) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeControl) Do(ctx context.Context, who domain.Actor, op domain.Op) (service.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{Who: who, Op: op})
	f.mu.Unlock()

	if f.kill.Active() {
		return service.Result{}, domain.ErrKillSwitchActive
	}
	if _, ok := op.(domain.Wait); !ok {
		err := f.leases.Check(who)
		if errors.Is(err, domain.ErrLeaseRequired) && who.Kind == domain.ActorMCP {
			_, err = f.leases.Acquire(ctx, who)
		}
		if err != nil {
			return service.Result{}, err
		}
	}
	if f.err != nil {
		return service.Result{}, f.err
	}
	switch op.(type) {
	case domain.ClipboardRead:
		return service.Result{Text: f.clipboard}, nil
	case domain.GetUITree:
		return service.Result{Tree: f.tree}, nil
	}
	return service.Result{}, nil
}

func (f *fakeControl) ReleaseInput() error { return nil }

func (f *fakeControl) VerifyTarget(context.Context, domain.Actor) (service.TargetStatus, error) {
	return service.TargetStatus{}, nil
}

func (f *fakeControl) ResumeTarget(context.Context, domain.Actor) (service.TargetStatus, error) {
	return service.TargetStatus{}, nil
}

func (f *fakeControl) GrabGroupFrame(context.Context, domain.WindowRef) (service.GroupFrame, error) {
	return service.GroupFrame{}, nil
}

func (f *fakeControl) LaunchableApps(context.Context) (service.LaunchableList, error) {
	if err := f.viewErr(); err != nil {
		return service.LaunchableList{}, err
	}
	mode := f.mode
	if mode == "" {
		mode = domain.LaunchModeAllowlist
	}
	return service.LaunchableList{Mode: mode, Apps: f.apps}, nil
}

// Launch は Control.Launch と同じ順序（キルスイッチ → リース → 許可リスト）で検査する。監査は本物の Control の責務なので記録しない。
func (f *fakeControl) Launch(ctx context.Context, who domain.Actor, req service.LaunchRequest) (service.LaunchResult, error) {
	appID := req.AppID
	f.mu.Lock()
	f.launches = append(f.launches, appID)
	f.launchReq = append(f.launchReq, req)
	f.mu.Unlock()

	if f.kill.Active() {
		return service.LaunchResult{}, domain.ErrKillSwitchActive
	}
	err := f.leases.Check(who)
	if errors.Is(err, domain.ErrLeaseRequired) && who.Kind == domain.ActorMCP {
		_, err = f.leases.Acquire(ctx, who)
	}
	if err != nil {
		return service.LaunchResult{}, err
	}
	if (req.Path == "") == (appID == "") { // 同時指定・どちらも無し
		return service.LaunchResult{}, domain.ErrInvalidArgument
	}
	if req.Path != "" { // path は denylist 方式のときだけ受け付ける（本物の Control と同じ。検査の中身は service のテストが持つ）
		if f.mode != domain.LaunchModeDenylist {
			return service.LaunchResult{}, domain.ErrInvalidArgument
		}
		if f.launchErr != nil {
			return service.LaunchResult{}, f.launchErr
		}
		return service.LaunchResult{PID: f.launchPID}, nil
	}
	if !slices.ContainsFunc(f.apps, func(a domain.AppSummary) bool { return a.ID == appID }) {
		return service.LaunchResult{}, domain.ErrAppNotFound
	}
	if f.launchErr != nil {
		return service.LaunchResult{}, f.launchErr
	}
	return service.LaunchResult{AppID: appID, PID: f.launchPID, Window: f.launchWindow, ReusedExistingWindow: f.launchReused}, nil
}

// launchRequestsFull は Launch が受けた指定を順に返す。
func (f *fakeControl) launchRequestsFull() []service.LaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.launchReq)
}

// launchRequests は Launch が受けた app_id を順に返す。
func (f *fakeControl) launchRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.launches)
}

func (f *fakeControl) viewErr() error {
	if f.kill.Active() {
		return domain.ErrKillSwitchActive
	}
	return f.err
}

func (f *fakeControl) Screenshot(_ context.Context, monitor int, format string) (service.Shot, error) {
	f.mu.Lock()
	f.screenshotMonitor, f.screenshotFormat = monitor, format
	f.mu.Unlock()
	if err := f.viewErr(); err != nil {
		return service.Shot{}, err
	}
	return f.shot, nil
}

func (f *fakeControl) Monitors(context.Context) ([]domain.Monitor, error) {
	if err := f.viewErr(); err != nil {
		return nil, err
	}
	return f.monitors, nil
}

func (f *fakeControl) Windows(context.Context) ([]domain.WindowInfo, error) {
	if err := f.viewErr(); err != nil {
		return nil, err
	}
	return f.windows, nil
}

func (f *fakeControl) CursorPosition(ctx context.Context) (service.Cursor, error) {
	if f.cursorEntered != nil {
		f.cursorEntered <- struct{}{}
	}
	if f.cursorRelease != nil {
		<-f.cursorRelease
		return f.cursor, nil
	}
	if f.blockUntilDone {
		<-ctx.Done()
		if f.cursorCtxErr != nil {
			f.cursorCtxErr <- ctx.Err()
		}
		return service.Cursor{}, ctx.Err()
	}
	if err := f.viewErr(); err != nil {
		return service.Cursor{}, err
	}
	return f.cursor, nil
}

func (f *fakeControl) MaxDimension(context.Context) (int, error) { return f.maxDim, nil }

// env はテスト 1 件分の MCP サーバー（httptest）と、その依存。
type env struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	ctl    *fakeControl
	leases service.LeaseManager
	kill   *fakeKill
	agent  *fakeAgent
	events <-chan domain.LeaseEvent
}

func newEnv(t *testing.T, mutate ...func(*Config)) *env {
	t.Helper()
	return newEnvWithLeases(t, nil, mutate...)
}

// newEnvWithLeases は newEnv の LeaseManager の設定を leaseMutate で変える版（承認の結合テスト用）。
func newEnvWithLeases(t *testing.T, leaseMutate func(*service.LeaseConfig), mutate ...func(*Config)) *env {
	t.Helper()
	kill := &fakeKill{}
	leaseCfg := service.LeaseConfig{
		Clock: fixedClock{}, IDs: &seqIDs{}, IdleTimeout: 5 * time.Minute, Kill: kill,
		AfterFunc: func(time.Duration, func()) func() bool { return func() bool { return true } },
	}
	if leaseMutate != nil {
		leaseMutate(&leaseCfg)
	}
	leases, err := service.NewLeaseManager(leaseCfg)
	if err != nil {
		t.Fatal(err)
	}
	ctl := newFakeControl(leases, kill)
	agent := newFakeAgent()
	cfg := Config{
		Control:   ctl,
		Leases:    leases,
		Agent:     agent,
		Principal: testPrincipal,
		Version:   "9.9.9-test",
		Logger:    discardLogger(),
	}
	for _, m := range mutate {
		m(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, srv: srv, http: ts, ctl: ctl, leases: leases, kill: kill, agent: agent, events: leases.Subscribe()}
}

// auditLog は mcp が記録した監査エントリを溜める service.AuditRecorder の偽物。
type auditLog struct {
	mu      sync.Mutex
	entries []service.AuditEntry
}

func (a *auditLog) Record(_ context.Context, e service.AuditEntry) {
	a.mu.Lock()
	a.entries = append(a.entries, e)
	a.mu.Unlock()
}

func (a *auditLog) all() []service.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.entries)
}

// testPrincipal はヘッダー X-Test-Client のクライアント ID を主体にする。ヘッダーが無ければ認証なし。
func testPrincipal(r *http.Request) (service.Principal, bool) {
	id := r.Header.Get(clientHeader)
	if id == "" {
		return service.Principal{}, false
	}
	return service.Principal{Client: domain.Client{ID: id, DisplayName: "client-" + id}}, true
}

// headerTransport は全リクエストにクライアント ID を付ける。
type headerTransport struct{ clientID string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(clientHeader, h.clientID)
	r.Header.Set("Authorization", testAuthorization)
	return http.DefaultTransport.RoundTrip(r)
}

// connect は clientID の MCP クライアントを初期化して返す。終了時に閉じる。
func (e *env) connect(clientID string) *mcpsdk.ClientSession {
	e.t.Helper()
	cs, err := e.dial(clientID)
	if err != nil {
		e.t.Fatalf("connect %s: %v", clientID, err)
	}
	e.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (e *env) dial(clientID string) (*mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	return client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             e.http.URL,
		HTTPClient:           &http.Client{Transport: headerTransport{clientID: clientID}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
}

// waitEvent は条件に合うリースのイベントが来るまで待つ。
func (e *env) waitEvent(match func(domain.LeaseEvent) bool) domain.LeaseEvent {
	e.t.Helper()
	timeout := time.After(waitLimit)
	for {
		select {
		case ev := <-e.events:
			if match(ev) {
				return ev
			}
		case <-timeout:
			e.t.Fatal("timed out waiting for a lease event")
		}
	}
}

func isRelease(ev domain.LeaseEvent) bool { return ev.To == domain.LeaseIdle }

// result は呼び出し結果。
type result struct {
	*mcpsdk.CallToolResult
}

// text は先頭のテキストコンテンツ。
func (r result) text() string {
	for _, c := range r.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// decode は structuredContent を v にデコードする。
func (r result) decode(t *testing.T, v any) {
	t.Helper()
	raw, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

// errCode は isError の結果から structuredContent の code を返す。
func (r result) errCode(t *testing.T) domain.Code {
	t.Helper()
	if !r.IsError {
		t.Fatalf("result is not an error: %s", r.text())
	}
	var body errorBody
	r.decode(t, &body)
	return body.Code
}

func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args any) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result{res}
}
