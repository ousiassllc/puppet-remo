package ws_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/ws"
)

// このファイルは、セッションの終わり（切断・置き換え）で押下中のボタン・キーが離されることのテスト。
// 偽の Control は実物と同じくリースを先に検査するので、解放が ReleaseSession の前に行われたことも確かめられる。

// pressSomeInputs は、左ボタン・Shift・A を押し、ポインタを (5, 6) へ動かした状態にして、Control に届いた入力の数を返す。
func pressSomeInputs(t *testing.T, e *env, c *websocket.Conn, id string) int {
	t.Helper()
	if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
		t.Fatal(err)
	}
	readType(t, c, "lease")
	for _, msg := range []map[string]any{
		{"type": "input.mouse", "action": "down", "x": 3, "y": 4, "button": "left"},
		{"type": "input.key", "action": "down", "code": "ShiftLeft", "key": "Shift"},
		{"type": "input.key", "action": "down", "code": "KeyA", "key": "a", "modifiers": []string{"shift"}},
		{"type": "input.mouse", "action": "move", "x": 5, "y": 6},
	} {
		send(t, c, msg)
		e.control.nextOp(t)
	}
	return e.control.opCount()
}

// releasedOps は押下の後に Control に届いた入力。
func releasedOps(e *env, after int) []domain.Op {
	e.control.mu.Lock()
	defer e.control.mu.Unlock()
	return append([]domain.Op(nil), e.control.ops[after:]...)
}

// wantReleased は pressSomeInputs の状態から離す入力（押した順の逆: A、Shift、左ボタンは最後のポインタの位置）。
var wantReleased = []domain.Op{
	domain.KeyEvent{Action: domain.KeyUp, Code: "KeyA", Key: "a", Mods: []domain.Modifier{domain.ModShift}},
	domain.KeyEvent{Action: domain.KeyUp, Code: "ShiftLeft", Key: "Shift", Mods: []domain.Modifier{}},
	domain.MouseButton{At: domain.Point{X: 5, Y: 6}, Button: domain.ButtonLeft, Action: domain.ButtonActionUp, Monitor: 1},
}

func TestDisconnectReleasesPressedInputsBeforeTheLease(t *testing.T) {
	for name, closeConn := range map[string]func(*websocket.Conn){
		"close handshake": func(c *websocket.Conn) { _ = c.Close(websocket.StatusNormalClosure, "") },
		"abrupt":          func(c *websocket.Conn) { _ = c.CloseNow() },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c, id := e.connect()
			n := pressSomeInputs(t, e, c, id)
			closeConn(c)
			waitUntil(t, "the lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
			if got := releasedOps(e, n); !reflect.DeepEqual(got, wantReleased) {
				t.Errorf("ops after the disconnect = %#v, want %#v", got, wantReleased)
			}
			e.control.mu.Lock()
			whos := append([]domain.Actor(nil), e.control.who[n:]...)
			e.control.mu.Unlock()
			for _, who := range whos {
				if who != e.viewerActor(id) {
					t.Errorf("release actor = %+v, want the viewer's web actor", who)
				}
			}
		})
	}
}

func TestReplacementReleasesThePressedInputsOfTheOldConnection(t *testing.T) {
	e := newEnv(t)
	old, id := e.connect()
	n := pressSomeInputs(t, e, old, id)

	fresh := e.dial(id)
	readType(t, fresh, "ready")
	expectClose(t, old, ws.CloseUnauthorized)
	if got := releasedOps(e, n); !reflect.DeepEqual(got, wantReleased) {
		t.Errorf("ops after the replacement = %#v, want %#v", got, wantReleased)
	}
}

func TestDisconnectSendsNothingWhenNothingIsPressed(t *testing.T) {
	t.Run("no input at all", func(t *testing.T) {
		e := newEnv(t)
		c, id := e.connect()
		if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
			t.Fatal(err)
		}
		_ = c.CloseNow()
		waitUntil(t, "the lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
		if n := e.control.opCount(); n != 0 {
			t.Errorf("%d ops reached Control, want none", n)
		}
	})
	t.Run("everything was released by up", func(t *testing.T) {
		e := newEnv(t)
		c, id := e.connect()
		n := pressSomeInputs(t, e, c, id)
		for _, msg := range []map[string]any{
			{"type": "input.mouse", "action": "up", "x": 5, "y": 6, "button": "left"},
			{"type": "input.key", "action": "up", "code": "KeyA", "key": "a"},
			{"type": "input.key", "action": "up", "code": "ShiftLeft", "key": "Shift"},
		} {
			send(t, c, msg)
			e.control.nextOp(t)
		}
		_ = c.CloseNow()
		waitUntil(t, "the lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
		if got := e.control.opCount(); got != n+3 {
			t.Errorf("%d ops reached Control, want %d (no release after the client's own up)", got, n+3)
		}
	})
	t.Run("a rejected down is not remembered", func(t *testing.T) {
		e := newEnv(t)
		c, id := e.connect()
		if _, err := e.leases.Acquire(context.Background(), e.viewerActor(id)); err != nil {
			t.Fatal(err)
		}
		readType(t, c, "lease")
		e.control.setDoErr(domain.ErrInputBlocked)
		send(t, c, map[string]any{"type": "input.mouse", "action": "down", "x": 1, "y": 1, "button": "left"})
		readType(t, c, "error")
		e.control.setDoErr(nil)
		_ = c.CloseNow()
		waitUntil(t, "the lease to be released", func() bool { _, held := e.leases.Holder(); return !held })
		if n := e.control.opCount(); n != 0 {
			t.Errorf("%d ops reached Control, want none", n)
		}
	})
}
