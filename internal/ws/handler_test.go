package ws_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/ws"
)

func TestNewRequiresDependencies(t *testing.T) {
	if _, err := ws.New(ws.Config{}); err == nil {
		t.Error("New with no dependencies must fail")
	}
}

func TestHandshakeRejectionsAreProblemJSON(t *testing.T) {
	t.Run("without the chain's client in the context", func(t *testing.T) {
		e := newEnv(t)
		e.setClient(nil)
		resp := get(t, strings.Replace(e.url(e.issue()), "ws", "http", 1), http.Header{"Sec-WebSocket-Protocol": {ws.Subprotocol}})
		assertProblem(t, resp, http.StatusInternalServerError, "internal")
	})
	t.Run("without the subprotocol", func(t *testing.T) {
		e := newEnv(t)
		resp := get(t, strings.Replace(e.url(e.issue()), "ws", "http", 1), nil)
		assertProblem(t, resp, http.StatusBadRequest, "invalid_argument")
		if e.streamer.activeViewers() != 0 {
			t.Error("a viewer was registered although the handshake was rejected")
		}
	})
	t.Run("a foreign subprotocol only", func(t *testing.T) {
		e := newEnv(t)
		resp := get(t, strings.Replace(e.url(e.issue()), "ws", "http", 1), http.Header{"Sec-WebSocket-Protocol": {"other.v1"}})
		assertProblem(t, resp, http.StatusBadRequest, "invalid_argument")
	})
}

func assertProblem(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	if resp.StatusCode != status || resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response = %d %q, want %d application/problem+json", resp.StatusCode, resp.Header.Get("Content-Type"), status)
	}
	var body struct{ Type, Code string }
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("body is not JSON: %s", b)
	}
	if body.Code != code || body.Type != "urn:puppet-remo:error:"+code {
		t.Errorf("problem = %+v, want code %q", body, code)
	}
}

func TestSubprotocolIsNegotiated(t *testing.T) {
	e := newEnv(t)
	c := e.dialWith(e.issue(), &websocket.DialOptions{Subprotocols: []string{"other.v0", ws.Subprotocol}})
	if got := c.Subprotocol(); got != ws.Subprotocol {
		t.Errorf("subprotocol = %q, want %q", got, ws.Subprotocol)
	}
}

func TestReadyIsTheFirstMessage(t *testing.T) {
	e := newEnv(t)
	c := e.dial(e.issue())
	got := readType(t, c, "ready")
	want := map[string]any{
		"type": "ready", "monitor": 1.0, "source": "monitor", "width": 1920.0, "height": 1080.0, "quality": 70.0, "fps": 10.0,
		"lease": map[string]any{"state": "idle", "mine": false},
		"monitors": []any{
			map[string]any{"index": 0.0, "name": "left", "primary": false, "origin": map[string]any{"x": -1280.0, "y": 0.0},
				"physical": map[string]any{"width": 1280.0, "height": 720.0}, "dpi_scale": 1.0},
			map[string]any{"index": 1.0, "name": "main", "primary": true, "origin": map[string]any{"x": 0.0, "y": 0.0},
				"physical": map[string]any{"width": 1920.0, "height": 1080.0}, "dpi_scale": 1.5},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ready = %#v\nwant   %#v", got, want)
	}
}

func TestReadyReportsTheLeaseState(t *testing.T) {
	e := newEnv(t)
	id := e.issue()
	if _, err := e.leases.Acquire(context.Background(), domain.Actor{Kind: domain.ActorMCP, ClientID: "c-bob", Label: "Bob", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	ready := readType(t, e.dial(id), "ready")
	want := map[string]any{"state": "held", "holder": map[string]any{"kind": "mcp", "label": "Bob"}, "mine": false}
	if !reflect.DeepEqual(ready["lease"], want) {
		t.Errorf("lease = %#v, want %#v", ready["lease"], want)
	}
}

func TestStreamerFramesAreDeliveredAsBinary(t *testing.T) {
	e := newEnv(t)
	c, _ := e.connect()
	v := e.streamer.viewer(t, 0)
	for _, f := range [][]byte{{1, 2, 3}, {4, 5}, {9}} {
		v.frames <- f
		if got := readBinary(t, c); string(got) != string(f) {
			t.Errorf("frame = %v, want %v", got, f)
		}
	}
}

func TestInvalidViewerIDIsClosedWith4401(t *testing.T) {
	e := newEnv(t)
	other := domain.Client{ID: "c-bob", WebEnabled: true}
	bobsID := e.sessions.Issue(other)
	for name, id := range map[string]string{
		"missing":                  "",
		"unknown":                  "01JXXXXXXXXXXXXXXXXXXXXXXX",
		"issued to another client": bobsID,
		"not a ULID":               "%20",
	} {
		t.Run(name, func(t *testing.T) {
			expectClose(t, e.dial(id), ws.CloseUnauthorized)
			if n := e.streamer.activeViewers(); n != 0 {
				t.Errorf("%d viewers registered for an invalid viewer_id", n)
			}
		})
	}
}

func TestViewerIDStaysValidAfterDisconnect(t *testing.T) {
	e := newEnv(t)
	id := e.issue()
	c := e.dial(id)
	readType(t, c, "ready")
	_ = c.Close(websocket.StatusNormalClosure, "")
	waitUntil(t, "the viewer to be unregistered", func() bool { return e.streamer.activeViewers() == 0 })

	readType(t, e.dial(id), "ready") // 切断後も 5 分は同じ viewer_id で再接続できる
}

func TestTooManyViewersIsClosedWith4429(t *testing.T) {
	e := newEnv(t)
	e.streamer.max = 1
	e.connect()
	expectClose(t, e.dial(e.issue()), ws.CloseTooManyViewers)
}

func TestKillSwitchActiveAtConnectIsClosedWith4503(t *testing.T) {
	e := newEnv(t)
	id := e.issue()
	e.kill.active.Store(true)
	expectClose(t, e.dial(id), ws.CloseKillSwitch)
}

func TestKillSwitchTriggerSendsKillswitchThenCloses4503(t *testing.T) {
	e := newEnv(t)
	c, id := e.connect()
	if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
		t.Fatal(err)
	}
	e.kill.trigger(t)

	m := readMsg(t, c)
	for m["type"] == "lease" { // リースの解放の通知が先に届くことがある
		m = readMsg(t, c)
	}
	if m["type"] != "killswitch" || m["active"] != true {
		t.Fatalf("message = %v, want killswitch active", m)
	}
	expectClose(t, c, ws.CloseKillSwitch)
	waitUntil(t, "the web lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
}

func TestClientRemovedIsClosedWith4510(t *testing.T) {
	e := newEnv(t)
	c, _ := e.connect()
	e.sessions.CloseClient(alice.ID, domain.ActorWeb)
	expectClose(t, c, ws.CloseClientRemoved)
}

func TestNewConnectionReplacesTheOldOneAndReleasesItsLease(t *testing.T) {
	e := newEnv(t)
	old, id := e.connect()
	if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
		t.Fatal(err)
	}
	if holder, _ := e.leases.Holder(); holder.Actor.SessionID != id {
		t.Fatalf("holder = %+v", holder)
	}

	fresh := e.dial(id)
	ready := readType(t, fresh, "ready")
	expectClose(t, old, ws.CloseUnauthorized) // 再接続しない閉じコード（置き換え合わない）
	if got := ready["lease"].(map[string]any)["state"]; got != "idle" {
		t.Errorf("the replaced connection's lease must be released before the new ready; got lease state %v", got)
	}
	if n := e.streamer.activeViewers(); n != 1 {
		t.Errorf("active viewers = %d, want 1 (the old connection is replaced, not added)", n)
	}
}

func TestStreamerEndingTheViewerClosesTheConnection(t *testing.T) {
	e := newEnv(t)
	c, _ := e.connect()
	e.streamer.viewer(t, 0).end()
	expectClose(t, c, websocket.StatusInternalError) // 再接続できる閉じコード
}

func TestMonitorsFailureAtConnectClosesWith1011(t *testing.T) {
	e := newEnv(t)
	id := e.issue()
	e.control.setMonitorsErr(domain.NewError(domain.CodeDesktopUnavailable, "locked", nil))
	expectClose(t, e.dial(id), websocket.StatusInternalError)
	if n := e.streamer.activeViewers(); n != 0 {
		t.Errorf("%d viewers registered although the monitors were unavailable", n)
	}
}

func TestDisconnectReleasesTheLeaseAndUnregistersTheViewer(t *testing.T) {
	e := newEnv(t)
	c, id := e.connect()
	if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
		t.Fatal(err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	waitUntil(t, "the lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
	waitUntil(t, "the viewer to be unregistered", func() bool { return e.streamer.activeViewers() == 0 })
}

func TestAbruptDisconnectReleasesTheLease(t *testing.T) {
	e := newEnv(t)
	c, id := e.connect()
	if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
		t.Fatal(err)
	}
	_ = c.CloseNow()
	waitUntil(t, "the lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
	waitUntil(t, "the viewer to be unregistered", func() bool { return e.streamer.activeViewers() == 0 })
}

func TestHandlerCloseClosesConnectionsAndRefusesNewOnes(t *testing.T) {
	e := newEnv(t)
	c, _ := e.connect()
	closed := make(chan struct{})
	go func() { e.handler.Close(); close(closed) }()
	expectClose(t, c, websocket.StatusGoingAway)
	<-closed
	waitUntil(t, "the viewer to be unregistered", func() bool { return e.streamer.activeViewers() == 0 })
	expectClose(t, e.dial(e.issue()), websocket.StatusTryAgainLater)
}

func TestPingGetsPong(t *testing.T) {
	e := newEnv(t)
	c, _ := e.connect()
	send(t, c, map[string]any{"type": "ping"})
	readType(t, c, "pong")
}

func TestConfigIsAppliedAndEchoed(t *testing.T) {
	e := newEnv(t)
	c, _ := e.connect()
	send(t, c, map[string]any{"type": "config", "monitor": 0, "quality": 40, "fps": 5})
	got := readType(t, c, "config")
	want := map[string]any{"type": "config", "monitor": 0.0, "source": "monitor", "width": 1280.0, "height": 720.0, "quality": 40.0, "fps": 5.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config = %#v, want %#v", got, want)
	}
	if cfg := e.streamer.viewer(t, 0).config(); cfg != (service.ViewerConfig{Monitor: 0, Quality: 40, FPS: 5}) {
		t.Errorf("streamer config = %+v", cfg)
	}

	// 項目を省くと今の値のまま（モニターも今のまま）。
	send(t, c, map[string]any{"type": "config", "fps": 20})
	got = readType(t, c, "config")
	want = map[string]any{"type": "config", "monitor": 0.0, "source": "monitor", "width": 1280.0, "height": 720.0, "quality": 40.0, "fps": 20.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config = %#v, want %#v", got, want)
	}
}

// POST /viewer/settings（ViewerSessions.Configure）で変えても、WebSocket に config が届く。
func TestConfigChangedFromTheHTTPPathIsPushed(t *testing.T) {
	e := newEnv(t)
	c, id := e.connect()
	if err := e.sessions.Configure(id, alice, service.ViewerConfig{Monitor: 1, Quality: 90}); err != nil {
		t.Fatal(err)
	}
	got := readType(t, c, "config")
	if got["quality"] != 90.0 || got["monitor"] != 1.0 || got["width"] != 1920.0 {
		t.Errorf("config = %v", got)
	}
}

func TestOutOfRangeConfigIsClosedWith4400(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"quality 0":       {"quality": 0},
		"quality 101":     {"quality": 101},
		"quality -1":      {"quality": -1},
		"fps 0":           {"fps": 0},
		"fps 31":          {"fps": 31},
		"monitor -1":      {"monitor": -1},
		"unknown monitor": {"monitor": 7},
		"not a number":    {"quality": "high"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c, _ := e.connect()
			cfg["type"] = "config"
			send(t, c, cfg)
			expectClose(t, c, ws.CloseInvalidMessage)
		})
	}
}

func TestLeaseChangesArePushed(t *testing.T) {
	e := newEnv(t)
	c, id := e.connect()
	me := e.viewerActor(id)
	ctx := context.Background()

	if _, err := e.leases.Acquire(ctx, me); err != nil {
		t.Fatal(err)
	}
	m := readType(t, c, "lease")
	want := map[string]any{"type": "lease", "state": "held", "holder": map[string]any{"kind": "web", "label": "Alice"}, "mine": true}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("lease = %#v, want %#v", m, want)
	}

	e.leases.Release(me)
	m = readType(t, c, "lease")
	if !reflect.DeepEqual(m, map[string]any{"type": "lease", "state": "idle", "mine": false}) {
		t.Errorf("lease = %#v, want idle", m)
	}

	other := domain.Actor{Kind: domain.ActorMCP, ClientID: "c-bob", Label: "Bob", SessionID: "s1"}
	if _, err := e.leases.Acquire(ctx, other); err != nil {
		t.Fatal(err)
	}
	m = readType(t, c, "lease")
	if m["state"] != "held" || m["mine"] != false || m["holder"].(map[string]any)["kind"] != "mcp" {
		t.Errorf("lease = %#v, want held by another (mcp)", m)
	}
}

func TestInputWithoutTheLeaseIsRejectedWithErrorAndKeepsTheConnection(t *testing.T) {
	e := newEnv(t)
	c, _ := e.connect()
	send(t, c, map[string]any{"type": "input.mouse", "action": "move", "x": 1, "y": 2})
	m := readType(t, c, "error")
	if m["code"] != "lease_required" || m["message"] == "" {
		t.Errorf("error = %v, want lease_required with a message", m)
	}
	if e.control.opCount() != 0 {
		t.Error("an input without the lease reached the platform")
	}
	send(t, c, map[string]any{"type": "ping"}) // 切断されていない
	readType(t, c, "pong")
}

func TestInputRejectionsAreReportedAsErrorMessages(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		wantCode string
	}{
		"input blocked":       {domain.ErrInputBlocked.WithDetail("reason", "own_window"), "input_blocked"},
		"invalid coordinates": {domain.ErrInvalidCoordinates, "invalid_coordinates"},
		"kill switch":         {domain.ErrKillSwitchActive, "kill_switch_active"},
		"desktop unavailable": {domain.NewError(domain.CodeDesktopUnavailable, "locked", nil), "desktop_unavailable"},
		"internal":            {io.ErrUnexpectedEOF, "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c, id := e.connect()
			if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
				t.Fatal(err)
			}
			readType(t, c, "lease")
			e.control.setDoErr(tc.err)
			send(t, c, map[string]any{"type": "input.text", "text": "x"})
			m := readType(t, c, "error")
			if m["code"] != tc.wantCode {
				t.Errorf("code = %v, want %s", m["code"], tc.wantCode)
			}
			if tc.wantCode == "internal" && m["message"] != "internal error" {
				t.Errorf("an unexpected error leaked its message: %v", m["message"])
			}
			send(t, c, map[string]any{"type": "ping"})
			readType(t, c, "pong")
		})
	}
}

func TestRevokedViewerGetsLeaseRevoked(t *testing.T) {
	e := newEnv(t)
	c, id := e.connect()
	me := e.viewerActor(id)
	if _, err := e.leases.Acquire(context.Background(), me); err != nil {
		t.Fatal(err)
	}
	readType(t, c, "lease")
	if err := e.leases.Revoke(service.HostActor); err != nil {
		t.Fatal(err)
	}
	readType(t, c, "lease")
	send(t, c, map[string]any{"type": "input.key", "action": "down", "code": "KeyA"})
	if m := readType(t, c, "error"); m["code"] != "lease_revoked" {
		t.Errorf("error = %v, want lease_revoked", m)
	}
}

// 入力メッセージが domain.Op に写り、リース保持者（このビューアの web アクター）で Control に渡る。
func TestInputMessagesAreMappedToOps(t *testing.T) {
	e := newEnv(t)
	c, id := e.connect()
	me := e.viewerActor(id)
	if _, err := e.leases.Acquire(context.Background(), me); err != nil {
		t.Fatal(err)
	}
	readType(t, c, "lease")

	// 既定のモニターはプライマリ（Index 1）。config でモニターを変えたら、以降の入力はそのモニターで渡る。
	cases := []struct {
		name string
		msg  map[string]any
		want domain.Op
	}{
		{"move", map[string]any{"type": "input.mouse", "action": "move", "x": 100, "y": 200},
			domain.PointerMove{At: domain.Point{X: 100, Y: 200}, Monitor: 1}},
		{"down", map[string]any{"type": "input.mouse", "action": "down", "x": 3, "y": 4, "button": "right"},
			domain.MouseButton{At: domain.Point{X: 3, Y: 4}, Button: domain.ButtonRight, Action: domain.ButtonActionDown, Monitor: 1}},
		{"up", map[string]any{"type": "input.mouse", "action": "up", "x": 5, "y": 6, "button": "left"},
			domain.MouseButton{At: domain.Point{X: 5, Y: 6}, Button: domain.ButtonLeft, Action: domain.ButtonActionUp, Monitor: 1}},
		{"middle", map[string]any{"type": "input.mouse", "action": "down", "x": 0, "y": 0, "button": "middle"},
			domain.MouseButton{Button: domain.ButtonMiddle, Action: domain.ButtonActionDown, Monitor: 1}},
		{"wheel", map[string]any{"type": "input.mouse", "action": "wheel", "x": 7, "y": 8, "dx": 0.5, "dy": -1.25},
			domain.Wheel{At: domain.Point{X: 7, Y: 8}, DX: 0.5, DY: -1.25, Monitor: 1}},
		{"wheel at the limit", map[string]any{"type": "input.mouse", "action": "wheel", "x": 1, "y": 1, "dy": 50},
			domain.Wheel{At: domain.Point{X: 1, Y: 1}, DY: 50, Monitor: 1}},
		{"key down", map[string]any{"type": "input.key", "action": "down", "code": "KeyA", "key": "a", "modifiers": []string{"ctrl", "shift"}},
			domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a", Mods: []domain.Modifier{domain.ModCtrl, domain.ModShift}}},
		{"key up without modifiers", map[string]any{"type": "input.key", "action": "up", "code": "Enter", "key": "Enter"},
			domain.KeyEvent{Action: domain.KeyUp, Code: "Enter", Key: "Enter", Mods: []domain.Modifier{}}},
		{"text", map[string]any{"type": "input.text", "text": "こんにちは"}, domain.TypeText{Text: "こんにちは"}},
		{"empty text", map[string]any{"type": "input.text", "text": ""}, domain.TypeText{}},
	}
	for _, tc := range cases {
		send(t, c, tc.msg)
		who, op := e.control.nextOp(t)
		if !reflect.DeepEqual(op, tc.want) {
			t.Errorf("%s: op = %#v, want %#v", tc.name, op, tc.want)
		}
		if who != me {
			t.Errorf("%s: actor = %+v, want the viewer's web actor %+v", tc.name, who, me)
		}
	}

	send(t, c, map[string]any{"type": "config", "monitor": 0})
	readType(t, c, "config")
	send(t, c, map[string]any{"type": "input.mouse", "action": "move", "x": 9, "y": 9})
	if _, op := e.control.nextOp(t); !reflect.DeepEqual(op, domain.PointerMove{At: domain.Point{X: 9, Y: 9}, Monitor: 0}) {
		t.Errorf("op after switching the monitor = %#v", op)
	}
}

func TestInvalidMessagesAreClosedWith4400(t *testing.T) {
	text := func(s string) func(*testing.T, *websocket.Conn) {
		return func(t *testing.T, c *websocket.Conn) { sendRaw(t, c, websocket.MessageText, []byte(s)) }
	}
	cases := map[string]func(*testing.T, *websocket.Conn){
		"not JSON":                text("not json"),
		"a JSON array":            text(`[1,2]`),
		"no type":                 text(`{"action":"move"}`),
		"unknown type":            text(`{"type":"input.gamepad"}`),
		"server-only type":        text(`{"type":"ready"}`),
		"binary frame":            func(t *testing.T, c *websocket.Conn) { sendRaw(t, c, websocket.MessageBinary, []byte{1, 2, 3}) },
		"mouse without x":         text(`{"type":"input.mouse","action":"move","y":1}`),
		"mouse without y":         text(`{"type":"input.mouse","action":"move","x":1}`),
		"x is a string":           text(`{"type":"input.mouse","action":"move","x":"1","y":1}`),
		"x is fractional":         text(`{"type":"input.mouse","action":"move","x":1.5,"y":1}`),
		"unknown mouse action":    text(`{"type":"input.mouse","action":"drag","x":1,"y":1}`),
		"no mouse action":         text(`{"type":"input.mouse","x":1,"y":1}`),
		"down without button":     text(`{"type":"input.mouse","action":"down","x":1,"y":1}`),
		"down with unknown":       text(`{"type":"input.mouse","action":"down","x":1,"y":1,"button":"back"}`),
		"wheel dy over 50":        text(`{"type":"input.mouse","action":"wheel","x":1,"y":1,"dy":50.5}`),
		"wheel dx over 50":        text(`{"type":"input.mouse","action":"wheel","x":1,"y":1,"dx":-51}`),
		"wheel dy is a string":    text(`{"type":"input.mouse","action":"wheel","x":1,"y":1,"dy":"1"}`),
		"key without code":        text(`{"type":"input.key","action":"down","key":"a"}`),
		"key with unknown action": text(`{"type":"input.key","action":"press","code":"KeyA"}`),
		"key with unknown mod":    text(`{"type":"input.key","action":"down","code":"KeyA","modifiers":["hyper"]}`),
		"key modifiers not array": text(`{"type":"input.key","action":"down","code":"KeyA","modifiers":"ctrl"}`),
		"text without text":       text(`{"type":"input.text"}`),
		"text is a number":        text(`{"type":"input.text","text":5}`),
	}
	for name, sendBad := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c, id := e.connect()
			if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
				t.Fatal(err)
			}
			sendBad(t, c)
			expectClose(t, c, ws.CloseInvalidMessage)
			if e.control.opCount() != 0 {
				t.Error("an invalid message reached Control")
			}
		})
	}
}

func TestMessageSizeLimit(t *testing.T) {
	pad := func(total int) []byte {
		prefix, suffix := `{"type":"ping","pad":"`, `"}`
		return []byte(prefix + strings.Repeat("x", total-len(prefix)-len(suffix)) + suffix)
	}
	t.Run("exactly 64 KiB is accepted", func(t *testing.T) {
		e := newEnv(t)
		c, _ := e.connect()
		sendRaw(t, c, websocket.MessageText, pad(ws.MaxMessageBytes))
		readType(t, c, "pong")
	})
	t.Run("one byte over is closed with 4400", func(t *testing.T) {
		e := newEnv(t)
		c, _ := e.connect()
		sendRaw(t, c, websocket.MessageText, pad(ws.MaxMessageBytes+1))
		expectClose(t, c, ws.CloseInvalidMessage)
	})
	t.Run("far over is closed with 4400", func(t *testing.T) {
		e := newEnv(t)
		c, _ := e.connect()
		c.SetReadLimit(-1)
		sendRaw(t, c, websocket.MessageText, pad(ws.MaxMessageBytes*8))
		expectClose(t, c, ws.CloseInvalidMessage)
	})
}

func TestServerPingsAndDisconnectsAClientThatDoesNotAnswer(t *testing.T) {
	short := func(c *ws.Config) { c.PingInterval = 5 * time.Millisecond; c.PongTimeout = 50 * time.Millisecond }

	t.Run("a client that answers stays connected", func(t *testing.T) {
		e := newEnv(t, short)
		var pings atomic.Int32
		c := e.dialWith(e.issue(), &websocket.DialOptions{
			Subprotocols:   []string{ws.Subprotocol},
			OnPingReceived: func(context.Context, []byte) bool { pings.Add(1); return true },
		})
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		// 読み続けると、ライブラリが ping に pong を返す。ping を数回受けてもつながっている。
		readErr := make(chan error, 1)
		go func() {
			for {
				if _, _, err := c.Read(ctx); err != nil {
					readErr <- err
					return
				}
			}
		}()
		waitUntil(t, "several pings", func() bool { return pings.Load() >= 4 })
		select {
		case err := <-readErr:
			t.Fatalf("the connection ended although the client answered pings: %v", err)
		default:
		}
		if n := e.streamer.activeViewers(); n != 1 {
			t.Errorf("active viewers = %d, want 1", n)
		}
	})

	t.Run("no pong disconnects, releases the lease and unregisters the viewer", func(t *testing.T) {
		e := newEnv(t, short)
		id := e.issue()
		// 接続の前に取得する（短い間隔で切れても、取得が切断の後にならないように）。
		if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
			t.Fatal(err)
		}
		var pings atomic.Int32
		c := e.dialWith(id, &websocket.DialOptions{
			Subprotocols:   []string{ws.Subprotocol},
			OnPingReceived: func(context.Context, []byte) bool { pings.Add(1); return false }, // pong を返さない
		})
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				break // サーバーが切った
			}
		}
		if pings.Load() == 0 {
			t.Error("the server never pinged")
		}
		waitUntil(t, "the lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
		waitUntil(t, "the viewer to be unregistered", func() bool { return e.streamer.activeViewers() == 0 })
	})
}

func TestDefaultKeepaliveIsFifteenAndFortyFiveSeconds(t *testing.T) {
	if ws.DefaultPingInterval != 15*time.Second || ws.DefaultPongTimeout != 45*time.Second {
		t.Errorf("keepalive = %s / %s, want 15s / 45s (NFR-A04)", ws.DefaultPingInterval, ws.DefaultPongTimeout)
	}
	if ws.MaxMessageBytes != 64<<10 {
		t.Errorf("max message = %d, want 64 KiB (NFR-S09)", ws.MaxMessageBytes)
	}
}
