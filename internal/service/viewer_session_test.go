package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

var (
	viewerClientA = domain.Client{ID: "client-a", DisplayName: "Alice PC", LoginName: "alice", WebEnabled: true}
	viewerClientB = domain.Client{ID: "client-b", DisplayName: "Bob PC", LoginName: "bob", WebEnabled: true}
)

type viewerEnv struct {
	*ctlEnv
	stream   *streamEnv
	sessions ViewerSessions
}

func newViewerEnv(t *testing.T) *viewerEnv {
	t.Helper()
	c := newCtlEnv(t)
	st := newStreamEnv(t)
	sessions, err := NewViewerSessions(ViewerSessionsConfig{
		Streamer: st.s, Leases: c.leases, Kill: c.kill, Clock: c.clock, IDs: &seqIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &viewerEnv{ctlEnv: c, stream: st, sessions: sessions}
}

var viewerCfg = ViewerConfig{Monitor: 0, Quality: 70, FPS: 10}

func (e *viewerEnv) open(t *testing.T, id string, client domain.Client) (ViewerSession, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := e.sessions.Open(ctx, id, client, viewerCfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, cancel
}

func waitClosed(t *testing.T, s ViewerSession) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(streamWait):
		t.Fatal("session did not close")
	}
}

func TestNewViewerSessionsValidatesConfig(t *testing.T) {
	e := newViewerEnv(t)
	full := ViewerSessionsConfig{Streamer: e.stream.s, Leases: e.leases, Kill: e.kill, Clock: e.clock, IDs: &seqIDs{}}
	for name, mutate := range map[string]func(*ViewerSessionsConfig){
		"streamer": func(c *ViewerSessionsConfig) { c.Streamer = nil },
		"leases":   func(c *ViewerSessionsConfig) { c.Leases = nil },
		"kill":     func(c *ViewerSessionsConfig) { c.Kill = nil },
		"clock":    func(c *ViewerSessionsConfig) { c.Clock = nil },
		"ids":      func(c *ViewerSessionsConfig) { c.IDs = nil },
	} {
		cfg := full
		mutate(&cfg)
		if _, err := NewViewerSessions(cfg); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("without %s: want ErrInvalidArgument, got %v", name, err)
		}
	}
}

func TestViewerIDIsBoundToTheIssuingClient(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)

	actor, err := e.sessions.Actor(id, viewerClientA)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Actor{Kind: domain.ActorWeb, ClientID: "client-a", Label: "Alice PC", SessionID: id}
	if actor != want {
		t.Fatalf("actor = %+v, want %+v", actor, want)
	}

	if _, err := e.sessions.Actor(id, viewerClientB); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("another client: want ErrInvalidArgument, got %v", err)
	}
	if _, err := e.sessions.Open(context.Background(), id, viewerClientB, viewerCfg); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Open from another client: want ErrInvalidArgument, got %v", err)
	}
	if err := e.sessions.Configure(id, viewerClientB, viewerCfg); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Configure from another client: want ErrInvalidArgument, got %v", err)
	}
	if _, err := e.sessions.Actor("nope", viewerClientA); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown viewer_id: want ErrInvalidArgument, got %v", err)
	}
}

func TestViewerActorFallsBackToLoginName(t *testing.T) {
	e := newViewerEnv(t)
	c := domain.Client{ID: "client-c", LoginName: "carol"}
	a, err := e.sessions.Actor(e.sessions.Issue(c), c)
	if err != nil {
		t.Fatal(err)
	}
	if a.Label != "carol" {
		t.Fatalf("label = %q", a.Label)
	}
}

func TestViewerIDExpiresFiveMinutesAfterIssueIfNeverConnected(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	e.clock.Advance(ViewerIDTTL - time.Second)
	if _, err := e.sessions.Actor(id, viewerClientA); err != nil {
		t.Fatalf("just before the TTL: %v", err)
	}
	e.clock.Advance(time.Second)
	if _, err := e.sessions.Actor(id, viewerClientA); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("at the TTL: want ErrInvalidArgument, got %v", err)
	}
	if _, err := e.sessions.Open(context.Background(), id, viewerClientA, viewerCfg); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Open after the TTL: want ErrInvalidArgument, got %v", err)
	}
}

// 接続中は期限が切れない。切れてから 5 分は再接続できる（docs/architecture/frontend.md §6.4 の再接続）。
func TestViewerIDStaysValidWhileConnectedAndForFiveMinutesAfterDisconnect(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	s, cancel := e.open(t, id, viewerClientA)

	e.clock.Advance(time.Hour) // 接続中は何時間でも有効
	if _, err := e.sessions.Actor(id, viewerClientA); err != nil {
		t.Fatalf("while connected: %v", err)
	}

	cancel() // WebSocket の切断
	waitClosed(t, s)
	e.clock.Advance(ViewerIDTTL - time.Second)
	if _, err := e.sessions.Actor(id, viewerClientA); err != nil {
		t.Fatalf("within 5 minutes of the disconnect: %v", err)
	}
	again, err := e.sessions.Open(context.Background(), id, viewerClientA, viewerCfg)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	again.Close()
	waitClosed(t, again)

	e.clock.Advance(ViewerIDTTL) // 2 回目の切断から 5 分
	if _, err := e.sessions.Actor(id, viewerClientA); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("5 minutes after the disconnect: want ErrInvalidArgument, got %v", err)
	}
}

func TestViewerDisconnectReleasesTheWebLease(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	s, cancel := e.open(t, id, viewerClientA)
	mustAcquire(t, e.ctlEnv, s.Actor())

	cancel()
	waitClosed(t, s)
	if _, held := e.leases.Holder(); held {
		t.Fatal("the lease must be released on disconnect")
	}
	if got := s.Reason(); got != ViewerClosedDisconnect {
		t.Fatalf("reason = %q", got)
	}
	if !hasLeaseEvent(e.audit.Events(), "lease.release") {
		t.Fatalf("audit events = %v", e.audit.Events())
	}
	waitUntil(t, "the viewer to leave the streamer", func() bool { return e.stream.viewerCount() == 0 })
}

// Done が閉じたときには、web のリースはもう解放済み（Done を待った側が、リースの解放済みを前提にしてよい）。
// 切断（ctx の取り消し）は別の goroutine で終了の後始末を走らせるので、Done が早く閉じると、待った側が解放前のリースを見ていた。
// OnEnd の fn（リースの解放の前に呼ばれる）で後始末を止めて、その間は Done が閉じていないことを確かめる。
func TestViewerDoneClosesOnlyAfterTheLeaseIsReleased(t *testing.T) {
	e := newViewerEnv(t)
	s, cancel := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	mustAcquire(t, e.ctlEnv, s.Actor())

	entered := make(chan struct{})
	proceed := make(chan struct{})
	s.OnEnd(func() {
		close(entered)
		<-proceed
	})
	cancel()
	select {
	case <-entered:
	case <-time.After(streamWait):
		t.Fatal("OnEnd was not called")
	}
	select {
	case <-s.Done():
		t.Fatal("Done closed before the end-of-session cleanup finished")
	default:
	}
	if _, held := e.leases.Holder(); !held {
		t.Fatal("the lease must still be held while OnEnd runs")
	}

	close(proceed)
	waitClosed(t, s)
	if _, held := e.leases.Holder(); held {
		t.Fatal("the lease must be released by the time Done closes")
	}
}

func TestViewerDisconnectKeepsOthersLease(t *testing.T) {
	e := newViewerEnv(t)
	s, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	mustAcquire(t, e.ctlEnv, ctlAlice)
	s.Close()
	waitClosed(t, s)
	if h, held := e.leases.Holder(); !held || h.Actor != ctlAlice {
		t.Fatalf("holder = %+v, %v", h, held)
	}
}

// 同じクライアントの旧ビューア（別の viewer_id）を切っても、新しいビューアが取ったばかりのリースは残る。
func TestClosingAnOlderViewerOfTheSameClientKeepsTheNewerViewersLease(t *testing.T) {
	e := newViewerEnv(t)
	older, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	newer, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	mustAcquire(t, e.ctlEnv, newer.Actor())
	older.Close()
	waitClosed(t, older)
	if h, held := e.leases.Holder(); !held || !h.Actor.SameSession(newer.Actor()) {
		t.Fatalf("holder = %+v, %v; want the newer viewer", h, held)
	}
}

func TestViewerSessionIsSingleConnectionAndReplacesTheOldOne(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	first, _ := e.open(t, id, viewerClientA)
	mustAcquire(t, e.ctlEnv, first.Actor())

	second, _ := e.open(t, id, viewerClientA)
	waitClosed(t, first)
	if first.Reason() != ViewerClosedReplaced {
		t.Fatalf("old reason = %q, want %q", first.Reason(), ViewerClosedReplaced)
	}
	if _, held := e.leases.Holder(); held {
		t.Fatal("replacing the connection must release the old connection's lease")
	}
	select {
	case <-second.Done():
		t.Fatal("the new connection must stay open")
	default:
	}
	if e.stream.viewerCount() != 1 {
		t.Fatalf("viewers = %d, want 1", e.stream.viewerCount())
	}
	// 古い接続の後始末が、新しい接続の登録を壊さない。
	first.Close()
	if err := e.sessions.Configure(id, viewerClientA, ViewerConfig{Monitor: 0, Quality: 50}); err != nil {
		t.Fatalf("Configure after the old one closed: %v", err)
	}
}

// 同時閲覧者が上限でも、同じ viewer_id の置き換えは上限に阻まれない。
func TestViewerReplacementIsNotBlockedByTheViewerLimit(t *testing.T) {
	e := newViewerEnv(t)
	for range MaxViewers - 1 {
		e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	}
	id := e.sessions.Issue(viewerClientA)
	e.open(t, id, viewerClientA)
	if e.stream.viewerCount() != MaxViewers {
		t.Fatalf("viewers = %d", e.stream.viewerCount())
	}
	e.open(t, id, viewerClientA)
	if _, err := e.sessions.Open(context.Background(), e.sessions.Issue(viewerClientA), viewerClientA, viewerCfg); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("another viewer over the limit: want ErrTooManyViewers, got %v", err)
	}
}

func TestViewerOpenPropagatesStreamerErrors(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	_, err := e.sessions.Open(context.Background(), id, viewerClientA, ViewerConfig{Monitor: 9, Quality: 70, FPS: 10})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown monitor: want ErrInvalidArgument, got %v", err)
	}
	// 失敗した接続は viewer_id を使い切らない。
	if _, err := e.sessions.Actor(id, viewerClientA); err != nil {
		t.Fatal(err)
	}
	if e.stream.viewerCount() != 0 {
		t.Fatal("a failed Open must not leave a viewer")
	}
}

func TestViewerOpenWithCancelledContext(t *testing.T) {
	e := newViewerEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.sessions.Open(ctx, e.sessions.Issue(viewerClientA), viewerClientA, viewerCfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if e.stream.viewerCount() != 0 {
		t.Fatal("no viewer must remain")
	}
}

func TestViewerConfigureReachesTheOpenSession(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	s, _ := e.open(t, id, viewerClientA)
	if got := s.Config(); got != viewerCfg {
		t.Fatalf("config = %+v", got)
	}

	// POST /viewer/settings の経路: viewer_id から接続中のセッションの Configure へ届く。
	if err := e.sessions.Configure(id, viewerClientA, ViewerConfig{Monitor: 0, Quality: 30}); err != nil {
		t.Fatal(err)
	}
	want := ViewerConfig{Monitor: 0, Quality: 30, FPS: 10} // FPS の 0 は今の値のまま
	select {
	case got := <-s.ConfigChanged():
		if got != want {
			t.Fatalf("changed = %+v, want %+v", got, want)
		}
	case <-time.After(streamWait):
		t.Fatal("ConfigChanged did not fire")
	}
	if got := s.Config(); got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}

	// WebSocket の config の経路。読まれていない通知は最新に置き換わる。
	for _, q := range []int{40, 50} {
		if err := s.Configure(ViewerConfig{Monitor: 0, FPS: q / 5}); err != nil {
			t.Fatal(err)
		}
	}
	if got := <-s.ConfigChanged(); got.FPS != 10 || got.Quality != 30 {
		t.Fatalf("latest = %+v, want fps 10 quality 30", got)
	}
}

func TestViewerConfigureErrors(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)

	if err := e.sessions.Configure(id, viewerClientA, viewerCfg); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("not connected: want ErrInvalidArgument, got %v", err)
	}
	s, _ := e.open(t, id, viewerClientA)
	for name, c := range map[string]ViewerConfig{
		"quality": {Monitor: 0, Quality: 101},
		"fps":     {Monitor: 0, FPS: 31},
		"monitor": {Monitor: 7},
	} {
		if err := s.Configure(c); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: want ErrInvalidArgument, got %v", name, err)
		}
	}
	if got := s.Config(); got != viewerCfg {
		t.Fatalf("a rejected Configure must not change the config: %+v", got)
	}
	select {
	case got := <-s.ConfigChanged():
		t.Fatalf("a rejected Configure must not notify: %+v", got)
	default:
	}

	s.Close()
	waitClosed(t, s)
	if err := s.Configure(viewerCfg); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("closed session: want ErrInvalidArgument, got %v", err)
	}
}

// クライアントの削除・無効化（HostAdmin の SessionCloser。種別 web）でセッションを閉じ、リースを解放し、viewer_id を無効にする。
func TestViewerCloseClientClosesItsSessionsOnly(t *testing.T) {
	e := newViewerEnv(t)
	idA := e.sessions.Issue(viewerClientA)
	idB := e.sessions.Issue(viewerClientB)
	idIdle := e.sessions.Issue(viewerClientA) // 未接続の viewer_id も無効にする
	a, _ := e.open(t, idA, viewerClientA)
	b, _ := e.open(t, idB, viewerClientB)
	mustAcquire(t, e.ctlEnv, a.Actor())

	e.sessions.CloseClient("client-a", domain.ActorMCP) // 種別が違えば何もしない
	select {
	case <-a.Done():
		t.Fatal("an mcp CloseClient must not close web sessions")
	default:
	}

	e.sessions.CloseClient("client-a", domain.ActorWeb)
	waitClosed(t, a)
	if a.Reason() != ViewerClosedClientRemoved {
		t.Fatalf("reason = %q", a.Reason())
	}
	if _, held := e.leases.Holder(); held {
		t.Fatal("the lease must be released")
	}
	for _, id := range []string{idA, idIdle} {
		if _, err := e.sessions.Actor(id, viewerClientA); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("viewer_id %s must be invalid: %v", id, err)
		}
	}
	select {
	case <-b.Done():
		t.Fatal("another client's session must stay open")
	default:
	}
	if _, err := e.sessions.Actor(idB, viewerClientB); err != nil {
		t.Fatal(err)
	}
	e.sessions.CloseToken("any") // 何も起きない
}

func TestViewerKillSwitchClosesAllSessionsAndRefusesNewOnes(t *testing.T) {
	e := newViewerEnv(t)
	a, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	b, _ := e.open(t, e.sessions.Issue(viewerClientB), viewerClientB)
	mustAcquire(t, e.ctlEnv, a.Actor())
	idLater := e.sessions.Issue(viewerClientA)

	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []ViewerSession{a, b} {
		waitClosed(t, s)
		if s.Reason() != ViewerClosedKillSwitch {
			t.Errorf("reason = %q", s.Reason())
		}
	}
	if _, err := e.sessions.Open(context.Background(), idLater, viewerClientA, viewerCfg); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("Open during the kill switch: want ErrKillSwitchActive, got %v", err)
	}
	waitUntil(t, "viewers to leave the streamer", func() bool { return e.stream.viewerCount() == 0 })
}

// キャプチャが終わって Streamer が閲覧者を外したら、セッションも終わる（リースが残らない）。
func TestViewerSessionEndsWhenTheStreamerDropsTheViewer(t *testing.T) {
	e := newViewerEnv(t)
	s, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	mustAcquire(t, e.ctlEnv, s.Actor())
	e.stream.cap.nextWatch(t).end()
	waitClosed(t, s)
	if _, held := e.leases.Holder(); held {
		t.Fatal("the lease must be released")
	}
}

func TestViewerCloseIsIdempotent(t *testing.T) {
	e := newViewerEnv(t)
	s, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	s.Close()
	s.Close()
	waitClosed(t, s)
	if got := s.Reason(); got != ViewerClosedDisconnect {
		t.Fatalf("reason = %q", got)
	}
}

// 解放済みの viewer_id の保持者は、Control で web として入力できる（Actor が Control の who と一致する）。
func TestViewerActorWorksWithControl(t *testing.T) {
	e := newViewerEnv(t)
	s, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	mustAcquire(t, e.ctlEnv, s.Actor())
	if _, err := e.control.Do(context.Background(), s.Actor(), domain.TypeText{Text: "a"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	waitClosed(t, s)
	if _, err := e.control.Do(context.Background(), s.Actor(), domain.TypeText{Text: "a"}); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("after the disconnect: want ErrLeaseRequired, got %v", err)
	}
}

func hasLeaseEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

// OnEnd の fn は、どの終わり方でも、リースを保持しているうちに 1 度だけ呼ばれる。
func TestViewerOnEndRunsOnceBeforeTheLeaseIsReleased(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	s, _ := e.open(t, id, viewerClientA)
	mustAcquire(t, e.ctlEnv, s.Actor())

	calls, heldInHook := 0, false
	s.OnEnd(func() {
		calls++
		_, heldInHook = e.leases.Holder()
	})
	s.Close()
	s.Close()
	waitClosed(t, s)
	if calls != 1 || !heldInHook {
		t.Fatalf("calls = %d, lease held in the hook = %v, want 1 and true", calls, heldInHook)
	}
	if _, held := e.leases.Holder(); held {
		t.Fatal("the lease must be released after the hook")
	}
}

func TestViewerOnEndRunsWhenTheSessionIsReplaced(t *testing.T) {
	e := newViewerEnv(t)
	id := e.sessions.Issue(viewerClientA)
	old, _ := e.open(t, id, viewerClientA)
	mustAcquire(t, e.ctlEnv, old.Actor())
	heldInHook := false
	old.OnEnd(func() { _, heldInHook = e.leases.Holder() })

	e.open(t, id, viewerClientA)
	if old.Reason() != ViewerClosedReplaced || !heldInHook {
		t.Fatalf("reason = %q, lease held in the hook = %v", old.Reason(), heldInHook)
	}
}

func TestViewerOnEndOnAnEndedSessionRunsImmediately(t *testing.T) {
	e := newViewerEnv(t)
	s, _ := e.open(t, e.sessions.Issue(viewerClientA), viewerClientA)
	s.Close()
	called := false
	s.OnEnd(func() { called = true })
	if !called {
		t.Fatal("OnEnd on an ended session must run fn at once")
	}
}
