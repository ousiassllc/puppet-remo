package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/ws"
)

// このファイルは /ws/screen の組み立て（Streamer・ViewerSessions・ws・ルーター・キルスイッチ）を、リモートルーターの Listener
// 経由の実際の WebSocket で確かめる。

func (e *env) wsURL(viewerID string) string {
	return "ws://" + net.JoinHostPort(testTailnetIP, "8765") + "/ws/screen?viewer_id=" + viewerID
}

// dialWS は /ws/screen へ、tailnet の IP の Host ヘッダーで loopback の待ち受けへつなぐ。
func (e *env) dialWS(viewerID string) (*websocket.Conn, *http.Response, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, e.wsURL(viewerID), &websocket.DialOptions{
		HTTPClient:   &http.Client{Transport: remoteTransport{e: e}},
		Subprotocols: []string{ws.Subprotocol},
	})
	if c != nil {
		e.t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

func wsRead(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, data
}

func wsReadJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	typ, data := wsRead(t, c)
	if typ != websocket.MessageText {
		t.Fatalf("got a binary frame, want text")
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func wsWriteJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

// startedWS は登録済みのクライアントで、リモートルーターを動かして viewer_id を発行する。
func startedWS(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.registerClient()
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	client, err := e.db.Clients().Get(context.Background(), "client-1")
	if err != nil {
		t.Fatal(err)
	}
	return e, e.app.viewers.Issue(client)
}

// 接続 → ready → フレーム（Streamer が Capturer.Watch から作ったバイナリ）→ 入力（リースを取った後）→ 切断でリース解放。
func TestWebSocketStreamsFramesAndRelaysInputThroughControl(t *testing.T) {
	e, viewerID := startedWS(t)
	c, _, err := e.dialWS(viewerID)
	if err != nil {
		t.Fatal(err)
	}

	ready := wsReadJSON(t, c)
	if ready["type"] != "ready" || ready["width"] != 640.0 || ready["height"] != 480.0 || ready["quality"] != 70.0 || ready["fps"] != 10.0 {
		t.Fatalf("ready = %v, want the fake monitor (640x480) with the default quality 70 / fps 10", ready)
	}

	// 画面: 最初のバイナリフレームはキーフレーム（API 仕様 §5.4。バージョン 1・フラグ bit0）。
	for {
		typ, data := wsRead(t, c)
		if typ != websocket.MessageBinary {
			continue
		}
		if len(data) < 14 || data[0] != 1 || data[1]&1 == 0 {
			t.Fatalf("first frame header = %v, want version 1 keyframe", data[:min(len(data), 14)])
		}
		break
	}

	// 入力はリース（保持者が自身の viewer_id）が必要。
	wsWriteJSON(t, c, map[string]any{"type": "input.mouse", "action": "move", "x": 10, "y": 20})
	for m := wsReadJSON(t, c); m["type"] != "error"; m = wsReadJSON(t, c) {
		// 画面以外のメッセージ（lease など）は読み飛ばす。
	}
	if n := len(e.input.CallsTo("MoveTo")); n != 0 {
		t.Fatalf("an input without the lease reached the platform (%d calls)", n)
	}

	actor, err := e.app.viewers.Actor(viewerID, domain.Client{ID: "client-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.leases.Acquire(context.Background(), actor); err != nil {
		t.Fatal(err)
	}
	wsWriteJSON(t, c, map[string]any{"type": "input.mouse", "action": "move", "x": 10, "y": 20})
	eventually(t, "the move to reach the platform", func() bool { return len(e.input.CallsTo("MoveTo")) == 1 })

	_ = c.Close(websocket.StatusNormalClosure, "")
	eventually(t, "the web lease to be released on disconnect", func() bool { _, held := e.holder(); return !held })
}

// キルスイッチ（ホットキー）で、WebSocket は killswitch メッセージの後に閉じコード 4503 で閉じ、新しい接続は断られる。
func TestKillSwitchClosesWebSocketsWith4503(t *testing.T) {
	e, viewerID := startedWS(t)
	c, _, err := e.dialWS(viewerID)
	if err != nil {
		t.Fatal(err)
	}
	wsReadJSON(t, c) // ready

	if !e.hotkey.Trigger(defaultHotkey) {
		t.Fatal("the kill switch hotkey is not registered")
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	sawKillSwitch := false
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			if got := websocket.CloseStatus(err); got != ws.CloseKillSwitch {
				t.Fatalf("closed with %d (%v), want %d", got, err, ws.CloseKillSwitch)
			}
			break
		}
		if typ == websocket.MessageText {
			var m map[string]any
			if json.Unmarshal(data, &m) == nil && m["type"] == "killswitch" && m["active"] == true {
				sawKillSwitch = true
			}
		}
	}
	if !sawKillSwitch {
		t.Error("no killswitch message before the close")
	}

	// 受付停止後はハンドシェイクの前に 503（problem+json）。
	_, resp, err := e.dialWS(viewerID)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dial after the kill switch: resp=%v err=%v, want a 503 handshake rejection", resp, err)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
}

// クライアントの Web を無効にすると、接続中の WebSocket は 4510 で閉じ、同じ viewer_id では再接続できない。
func TestDisablingTheClientClosesWebSocketsWith4510(t *testing.T) {
	e, viewerID := startedWS(t)
	c, _, err := e.dialWS(viewerID)
	if err != nil {
		t.Fatal(err)
	}
	wsReadJSON(t, c) // ready

	off := false
	if _, err := e.app.admin.UpdateClient(context.Background(), "client-1", service.ClientUpdate{WebEnabled: &off}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if got := websocket.CloseStatus(err); got != ws.CloseClientRemoved {
				t.Fatalf("closed with %d (%v), want %d", got, err, ws.CloseClientRemoved)
			}
			break
		}
	}
	_, resp, err := e.dialWS(viewerID)
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dial after disabling the client: resp=%v err=%v, want a 403 client_disabled handshake rejection", resp, err)
	}
}

// 未登録のデバイスは、ハンドシェイクの前（連鎖）で 403 になり、ビューアは作られない。
func TestWebSocketFromAnUnregisteredDeviceIsRejectedBeforeTheHandshake(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	_, resp, err := e.dialWS("01JXXXXXXXXXXXXXXXXXXXXXXX")
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dial from an unregistered device: resp=%v err=%v, want 403", resp, err)
	}
}
