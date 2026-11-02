package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは操作中オーバーレイの出し入れ（overlayController。#69）のテスト。偽の Windows・Capturer・リースで進める（固定時間の待ちを使わない）。

// holderLeases は Holder だけを持つ偽の LeaseManager（ほかのメソッドは呼ばれない）。
type holderLeases struct {
	service.LeaseManager
	mu     sync.Mutex
	holder service.Holder
	held   bool
}

func (l *holderLeases) Holder() (service.Holder, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holder, l.held
}

func (l *holderLeases) hold(a domain.Actor) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holder, l.held = service.Holder{LeaseID: "l-" + a.ClientID, Actor: a}, true
}

func (l *holderLeases) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holder, l.held = service.Holder{}, false
}

var overlayMonitors = []domain.Monitor{
	{Index: 0, Name: "left", Primary: true, Origin: domain.Point{X: 0, Y: 0}, Size: domain.Size{W: 1920, H: 1080}, DPI: 96},
	{Index: 1, Name: "right", Origin: domain.Point{X: 1920, Y: -200}, Size: domain.Size{W: 3840, H: 2160}, DPI: 192},
}

type overlayKit struct {
	t       *testing.T
	windows *FakeWindows
	leases  *holderLeases
	capture *platformtest.FakeCapturer
	bus     *service.Bus
	c       *overlayController
}

func newOverlayKit(t *testing.T) *overlayKit {
	t.Helper()
	k := &overlayKit{
		t: t, windows: NewFakeWindows(), leases: &holderLeases{}, capture: platformtest.NewFakeCapturer(overlayMonitors...), bus: service.NewBus(),
	}
	k.c = newOverlayController(k.windows, k.leases, k.capture, k.bus, discardLogger())
	return k
}

func (k *overlayKit) sync() { k.c.sync(context.Background()) }

var (
	aliceMCP = domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "alice-laptop", SessionID: "s1"}
	bobWeb   = domain.Actor{Kind: domain.ActorWeb, ClientID: "c2", Label: "bob-phone", SessionID: "s2"}
)

// held で全モニターに、ラベル「操作中: <名前>（<種別>）」で出し、解放（held でなくなる）で隠す。
func TestOverlayShowsOnAllMonitorsWhileHeldAndHidesOnRelease(t *testing.T) {
	k := newOverlayKit(t)

	k.sync() // idle: 何もしない
	if calls := k.windows.OverlayCalls(); len(calls) != 0 {
		t.Fatalf("calls while idle = %+v, want none", calls)
	}

	k.leases.hold(aliceMCP)
	k.sync()
	calls := k.windows.OverlayCalls()
	if len(calls) != 1 || calls[0].Hide || calls[0].Label != "操作中: alice-laptop（MCP）" {
		t.Fatalf("calls = %+v, want one ShowOverlay with the holder's label", calls)
	}
	if len(calls[0].Monitors) != 2 || calls[0].Monitors[0] != overlayMonitors[0] || calls[0].Monitors[1] != overlayMonitors[1] {
		t.Errorf("monitors = %+v, want every monitor with its physical origin and size", calls[0].Monitors)
	}

	k.leases.release()
	k.sync()
	if shown, _ := k.windows.OverlayShown(); shown {
		t.Error("the overlay must be hidden after the lease is released")
	}
	if calls := k.windows.OverlayCalls(); len(calls) != 2 || !calls[1].Hide {
		t.Errorf("calls = %+v, want ShowOverlay then HideOverlay", calls)
	}

	k.sync() // 解放後に重ねて同期しても、Hide を重ねない
	if got := len(k.windows.OverlayCalls()); got != 2 {
		t.Errorf("calls = %d after a redundant sync, want 2", got)
	}
}

// 保持者が同じなら作り直さない（モニター一覧の取得もしない）。保持者が変われば、隠さずにラベルを更新する。
func TestOverlayUpdatesTheLabelWhenTheHolderChanges(t *testing.T) {
	k := newOverlayKit(t)
	k.leases.hold(aliceMCP)
	k.sync()
	k.sync()
	if got := len(k.windows.OverlayCalls()); got != 1 {
		t.Fatalf("calls = %d after syncing the same holder twice, want 1", got)
	}

	k.leases.hold(bobWeb)
	k.sync()
	calls := k.windows.OverlayCalls()
	if len(calls) != 2 || calls[1].Hide || calls[1].Label != "操作中: bob-phone（Web）" {
		t.Fatalf("calls = %+v, want the label updated to the new holder without hiding", calls)
	}
	if shown, label := k.windows.OverlayShown(); !shown || label != "操作中: bob-phone（Web）" {
		t.Errorf("shown = %v, label = %q", shown, label)
	}
}

// 承認待ち（pending_approval。held ではない）の間は出さない。承認されて held になれば出す。
func TestOverlayIsNotShownWhilePendingApproval(t *testing.T) {
	k := newOverlayKit(t)
	k.sync() // Holder は held のときだけ返すので、承認待ちは「held でない」と同じ
	if shown, _ := k.windows.OverlayShown(); shown {
		t.Fatal("the overlay must not be shown while the approval is pending")
	}
	k.leases.hold(aliceMCP)
	k.sync()
	if shown, _ := k.windows.OverlayShown(); !shown {
		t.Error("the overlay must be shown once the lease is held")
	}
}

// ラベルが空なら、クライアント ID を名前に使う。
func TestOverlayLabelFallsBackToTheClientID(t *testing.T) {
	if got, want := overlayLabel(domain.Actor{Kind: domain.ActorAgent, ClientID: "client-77"}), "操作中: client-77（エージェント）"; got != want {
		t.Errorf("overlayLabel = %q, want %q", got, want)
	}
	if got, want := overlayLabel(domain.Actor{Kind: domain.ActorHost, Label: "host"}), "操作中: host（ホスト）"; got != want {
		t.Errorf("overlayLabel = %q, want %q", got, want)
	}
}

// モニター一覧が取れない・0 台のときは出さない（リースは妨げない）。前の保持者のオーバーレイを出したままにしない。
func TestOverlayIsSkippedWhenMonitorsAreUnavailable(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		k := newOverlayKit(t)
		k.capture.SetError("Monitors", errors.New("no desktop"))
		k.leases.hold(aliceMCP)
		k.sync()
		if calls := k.windows.OverlayCalls(); len(calls) != 0 {
			t.Fatalf("calls = %+v, want none", calls)
		}
		// 回復したら、次の変化で出る。
		k.capture.SetError("Monitors", nil)
		k.sync()
		if shown, _ := k.windows.OverlayShown(); !shown {
			t.Error("the overlay must be shown once the monitors can be listed")
		}
	})
	t.Run("no monitors", func(t *testing.T) {
		k := newOverlayKit(t)
		k.c.capture = emptyMonitors{}
		k.leases.hold(aliceMCP)
		k.sync()
		if calls := k.windows.OverlayCalls(); len(calls) != 0 {
			t.Fatalf("calls = %+v, want none", calls)
		}
	})
	t.Run("holder change while monitors fail hides the stale overlay", func(t *testing.T) {
		k := newOverlayKit(t)
		k.leases.hold(aliceMCP)
		k.sync()
		k.capture.SetError("Monitors", errors.New("no desktop"))
		k.leases.hold(bobWeb)
		k.sync()
		if shown, _ := k.windows.OverlayShown(); shown {
			t.Error("the previous holder's overlay must not stay on screen")
		}
	})
}

type emptyMonitors struct{ *platformtest.FakeCapturer }

func (emptyMonitors) Monitors(context.Context) ([]domain.Monitor, error) { return nil, nil }

// Watch は Bus の lease で出し入れし、ctx の終了（シャットダウン）で隠してから戻る。
func TestOverlayWatchFollowsLeaseEventsAndHidesOnShutdown(t *testing.T) {
	k := newOverlayKit(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := k.c.Watch(ctx)

	next := func() OverlayCall {
		t.Helper()
		select {
		case c := <-k.windows.OverlayEvents():
			return c
		case <-time.After(waitLimit):
			t.Fatal("no overlay call")
			return OverlayCall{}
		}
	}
	k.leases.hold(aliceMCP)
	k.bus.Publish(service.Event{Topic: service.TopicLease})
	if c := next(); c.Hide || c.Label != "操作中: alice-laptop（MCP）" {
		t.Fatalf("call = %+v, want ShowOverlay", c)
	}
	k.leases.release()
	k.bus.Publish(service.Event{Topic: service.TopicLease})
	if c := next(); !c.Hide {
		t.Fatalf("call = %+v, want HideOverlay", c)
	}

	k.leases.hold(bobWeb)
	k.bus.Publish(service.Event{Topic: service.TopicLease})
	next()
	cancel()
	select {
	case <-done:
	case <-time.After(waitLimit):
		t.Fatal("Watch did not stop")
	}
	if shown, _ := k.windows.OverlayShown(); shown {
		t.Error("the overlay must be hidden before Watch returns")
	}
}

// 結線の確認（実 App）: リースを取るとオーバーレイが出て、解放・キルスイッチ・Shutdown で隠れる。
func TestAppShowsTheOverlayWhileTheLeaseIsHeld(t *testing.T) {
	e := newEnv(t)
	e.start()
	clientID, _ := e.registerClient()
	who := domain.Actor{Kind: domain.ActorMCP, ClientID: clientID, Label: "alice-laptop", SessionID: "mcp-session-1"}
	next := func() OverlayCall {
		t.Helper()
		select {
		case c := <-e.dialogs.OverlayEvents():
			return c
		case <-time.After(waitLimit):
			t.Fatal("no overlay call")
			return OverlayCall{}
		}
	}

	if _, err := e.app.leases.Acquire(context.Background(), who); err != nil {
		t.Fatal(err)
	}
	if c := next(); c.Hide || c.Label != "操作中: alice-laptop（MCP）" || len(c.Monitors) != 1 {
		t.Fatalf("call = %+v, want ShowOverlay on the monitor", c)
	}
	e.app.leases.Release(who)
	if c := next(); !c.Hide {
		t.Fatalf("call = %+v, want HideOverlay after the release", c)
	}

	// キルスイッチ（全リースの解放）でも隠れる。
	if _, err := e.app.leases.Acquire(context.Background(), who); err != nil {
		t.Fatal(err)
	}
	next()
	if err := e.app.KillSwitch().Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if c := next(); !c.Hide {
		t.Fatalf("call = %+v, want HideOverlay after the kill switch", c)
	}
}
