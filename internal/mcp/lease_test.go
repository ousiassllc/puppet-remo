package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func status(t *testing.T, cs *mcpsdk.ClientSession) statusBody {
	t.Helper()
	res := callTool(t, cs, "get_control_status", nil)
	if res.IsError {
		t.Fatalf("get_control_status: %s", res.text())
	}
	var s statusBody
	res.decode(t, &s)
	return s
}

func TestControlStatusWhenIdle(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	got := status(t, cs)
	if got.State != "idle" || got.Holder != nil || got.Mine || got.IdleRemainingSec != nil {
		t.Errorf("status = %+v, want idle without holder", got)
	}
}

func TestExplicitAcquireAndRelease(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")

	res := callTool(t, cs, "acquire_control", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	var got statusBody
	res.decode(t, &got)
	want := statusBody{State: "held", Holder: &holderBody{Kind: "mcp", Label: "client-c1"}, Mine: true}
	if got.State != want.State || *got.Holder != *want.Holder || !got.Mine {
		t.Errorf("acquire_control = %+v, want %+v", got, want)
	}
	if got.IdleRemainingSec == nil || *got.IdleRemainingSec != 300 {
		t.Errorf("idle_remaining_sec = %v, want 300 (the 5 minute lease idle timeout)", got.IdleRemainingSec)
	}

	// 再取得は同じ保持者なので成功する。
	if res := callTool(t, cs, "acquire_control", nil); res.IsError {
		t.Errorf("re-acquire by the holder failed: %s", res.text())
	}
	if s := status(t, cs); s.State != "held" || !s.Mine {
		t.Errorf("status = %+v", s)
	}

	rel := callTool(t, cs, "release_control", nil)
	var ok okBody
	rel.decode(t, &ok)
	if rel.IsError || !ok.OK {
		t.Fatalf("release_control = %v %s", rel.IsError, rel.text())
	}
	if s := status(t, cs); s.State != "idle" {
		t.Errorf("status after release = %+v", s)
	}
	// 保持者でなければ何もしない。
	if res := callTool(t, cs, "release_control", nil); res.IsError {
		t.Errorf("release without holding failed: %s", res.text())
	}
	if e.ctl.callCount() != 0 {
		t.Error("lease tools must not go through Control.Do")
	}
}

// 操作系ツールは、リース未保持なら暗黙に取得する（FR-L04）。閲覧系・wait・リースのツールは取得しない。
func TestImplicitAcquisition(t *testing.T) {
	needLease := []struct {
		tool string
		args map[string]any
	}{
		{"mouse_move", map[string]any{"x": 1, "y": 1}},
		{"left_click", nil},
		{"right_click", nil},
		{"middle_click", nil},
		{"double_click", nil},
		{"triple_click", nil},
		{"left_click_drag", map[string]any{"from": map[string]any{"x": 1, "y": 1}, "to": map[string]any{"x": 2, "y": 2}}},
		{"scroll", map[string]any{"x": 1, "y": 1, "direction": "up", "amount": 1}},
		{"key", map[string]any{"keys": "enter"}},
		{"type", map[string]any{"text": "a"}},
		{"focus_window", map[string]any{"id": "1"}},
		{"clipboard_read", nil},
		{"clipboard_write", map[string]any{"text": "a"}},
		{"get_ui_tree", nil},
	}
	for _, tt := range needLease {
		t.Run("acquires/"+tt.tool, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.tree = sampleTree()
			cs := e.connect("c1")
			if res := callTool(t, cs, tt.tool, tt.args); res.IsError {
				t.Fatalf("%s: %s", tt.tool, res.text())
			}
			if s := status(t, cs); s.State != "held" || !s.Mine {
				t.Errorf("status after %s = %+v, want held by this session", tt.tool, s)
			}
		})
	}

	noLease := []struct {
		tool string
		args map[string]any
	}{
		{"get_screen_info", nil},
		{"screenshot", nil},
		{"cursor_position", nil},
		{"wait", map[string]any{"ms": 0}},
		{"list_windows", nil},
		{"get_control_status", nil},
		{"release_control", nil},
	}
	for _, tt := range noLease {
		t.Run("does not acquire/"+tt.tool, func(t *testing.T) {
			e := newEnv(t)
			cs := e.connect("c1")
			if res := callTool(t, cs, tt.tool, tt.args); res.IsError {
				t.Fatalf("%s: %s", tt.tool, res.text())
			}
			if _, held := e.leases.Holder(); held {
				t.Errorf("%s acquired the lease", tt.tool)
			}
		})
	}
}

func TestLeaseHeldByAnotherSession(t *testing.T) {
	e := newEnv(t)
	a, b := e.connect("a"), e.connect("b")

	if res := callTool(t, a, "mouse_move", map[string]any{"x": 1, "y": 1}); res.IsError {
		t.Fatal(res.text())
	}

	// 他のセッションの操作系ツールは lease_held_by_other。保持者の種別と表示名が structuredContent に入る。
	res := callTool(t, b, "mouse_move", map[string]any{"x": 2, "y": 2})
	if got := res.errCode(t); got != domain.CodeLeaseHeldByOther {
		t.Fatalf("code = %s, want lease_held_by_other", got)
	}
	var body errorBody
	res.decode(t, &body)
	if body.Holder == nil || *body.Holder != (holderBody{Kind: "mcp", Label: "client-a"}) {
		t.Errorf("holder = %+v", body.Holder)
	}
	if !strings.HasPrefix(res.text(), "lease_held_by_other: ") {
		t.Errorf("text = %q, want the code as a prefix (API 仕様 §3.4)", res.text())
	}

	// acquire_control も同じ。get_control_status は他者の保持を mine=false で見せる。
	if got := callTool(t, b, "acquire_control", nil).errCode(t); got != domain.CodeLeaseHeldByOther {
		t.Errorf("acquire_control code = %s", got)
	}
	if s := status(t, b); s.State != "held" || s.Mine || s.Holder == nil || s.Holder.Label != "client-a" {
		t.Errorf("b sees %+v", s)
	}
	if s := status(t, a); !s.Mine {
		t.Errorf("a sees %+v", s)
	}

	// 保持者でない b の release_control は何もしない。
	if res := callTool(t, b, "release_control", nil); res.IsError {
		t.Fatal(res.text())
	}
	if s := status(t, a); s.State != "held" || !s.Mine {
		t.Errorf("the lease was released by a non-holder: %+v", s)
	}

	// a が解放すれば b が取れる。
	callTool(t, a, "release_control", nil)
	if res := callTool(t, b, "left_click", nil); res.IsError {
		t.Errorf("b after release: %s", res.text())
	}
	if s := status(t, b); !s.Mine {
		t.Errorf("b sees %+v", s)
	}
}

// リースは MCP セッションに紐づく。同じクライアントの別セッションは別の保持者（FR-L05）。
func TestLeaseIsBoundToTheSession(t *testing.T) {
	e := newEnv(t)
	first, second := e.connect("same"), e.connect("same")
	callTool(t, first, "acquire_control", nil)

	if got := callTool(t, second, "mouse_move", map[string]any{"x": 1, "y": 1}).errCode(t); got != domain.CodeLeaseHeldByOther {
		t.Errorf("code = %s, want lease_held_by_other", got)
	}
	if s := status(t, second); s.Mine {
		t.Errorf("the second session shows the first session's lease as its own: %+v", s)
	}
}

func TestSessionCloseReleasesTheLease(t *testing.T) {
	e := newEnv(t)
	a := e.connect("a")
	callTool(t, a, "mouse_move", map[string]any{"x": 1, "y": 1}) // 暗黙取得
	e.waitEvent(func(ev domain.LeaseEvent) bool { return ev.To == domain.LeaseHeld })

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	ev := e.waitEvent(isRelease)
	if ev.Reason != domain.ReleaseDisconnect {
		t.Errorf("reason = %q, want %q (FR-L05)", ev.Reason, domain.ReleaseDisconnect)
	}
	if _, held := e.leases.Holder(); held {
		t.Error("the lease is still held after the session closed")
	}
}

// 見張りはツールを呼んだセッションだけに張る。リースを持たないセッションの切断で、他者のリースは解放されない。
func TestClosingAnotherSessionKeepsTheLease(t *testing.T) {
	e := newEnv(t)
	a, b := e.connect("a"), e.connect("b")
	callTool(t, a, "acquire_control", nil)
	callTool(t, b, "get_control_status", nil)

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	// b の切断は a のリースに影響しない。a の呼び出しが通る＝b の切断の処理が終わった後も保持している。
	if s := status(t, a); s.State != "held" || !s.Mine {
		t.Errorf("a sees %+v", s)
	}
}

func TestCloseSessionsReleasesLeasesAndEndsSessions(t *testing.T) {
	e := newEnv(t)
	a := e.connect("a")
	callTool(t, a, "acquire_control", nil)

	if err := e.srv.CloseSessions(context.Background()); err != nil {
		t.Fatalf("CloseSessions: %v", err)
	}
	ev := e.waitEvent(isRelease)
	if ev.Reason != domain.ReleaseDisconnect {
		t.Errorf("reason = %q", ev.Reason)
	}
	// 閉じたセッションでの呼び出しは通らない（クライアントは再初期化が必要）。
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if _, err := a.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_control_status"}); err == nil {
		t.Error("a call on a closed session succeeded")
	}
	// 再初期化すれば使える。
	again := e.connect("a")
	if s := status(t, again); s.State != "idle" {
		t.Errorf("status = %+v", s)
	}
}

func TestKillSwitchActive(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	callTool(t, cs, "acquire_control", nil)
	e.kill.active.Store(true)

	tests := []struct {
		tool string
		args map[string]any
	}{
		{"get_screen_info", nil},
		{"screenshot", nil},
		{"cursor_position", nil},
		{"list_windows", nil},
		{"mouse_move", map[string]any{"x": 1, "y": 1}},
		{"left_click", nil},
		{"key", map[string]any{"keys": "enter"}},
		{"type", map[string]any{"text": "a"}},
		{"wait", map[string]any{"ms": 0}},
		{"clipboard_read", nil},
		{"get_ui_tree", nil},
		{"acquire_control", nil},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			res := callTool(t, cs, tt.tool, tt.args)
			if got := res.errCode(t); got != domain.CodeKillSwitchActive {
				t.Errorf("code = %s, want kill_switch_active", got)
			}
		})
	}
}

func TestErrorMapping(t *testing.T) {
	wrapped := fmt.Errorf("while clicking: %w", domain.ErrInputBlocked.WithDetail("reason", "elevated_window"))
	tests := []struct {
		name        string
		err         error
		wantCode    domain.Code
		wantMessage string
		wantDetail  map[string]any
	}{
		{"lease_required", domain.ErrLeaseRequired, domain.CodeLeaseRequired, "the operation requires the control lease", nil},
		{"lease_revoked", domain.ErrLeaseRevoked, domain.CodeLeaseRevoked, "the lease was revoked", nil},
		{"kill_switch_active", domain.ErrKillSwitchActive, domain.CodeKillSwitchActive, "the kill switch is active", nil},
		{"invalid_coordinates", domain.ErrInvalidCoordinates.WithMsg("(9, 9) is outside the 10x10 screenshot").WithDetail("monitor", 0),
			domain.CodeInvalidCoordinates, "(9, 9) is outside the 10x10 screenshot", map[string]any{"monitor": float64(0)}},
		{"invalid_argument", domain.ErrInvalidArgument.WithMsg("bad"), domain.CodeInvalidArgument, "bad", nil},
		{"window_not_found", domain.ErrWindowNotFound, domain.CodeWindowNotFound, "window not found", nil},
		{"input_blocked wrapped", wrapped, domain.CodeInputBlocked, "input is blocked for this target", map[string]any{"reason": "elevated_window"}},
		{"desktop_unavailable", domain.ErrDesktopUnavailable, domain.CodeDesktopUnavailable, "the desktop is unavailable", nil},
		{"approval_denied", domain.ErrApprovalDenied, domain.CodeApprovalDenied, "the host denied the request", nil},
		{"approval_timeout", domain.ErrApprovalTimeout, domain.CodeApprovalTimeout, "no approval response within the time limit", nil},
		// ドメインエラーでないもの（想定外）は internal。内容はクライアントへ出さない（API 仕様 §2）。
		{"unexpected error", errors.New(`open C:\Users\secret\path: access denied`), domain.CodeInternal, "an internal error occurred", nil},
		{"internal domain error", domain.NewError(domain.CodeInternal, "stack: secret detail", nil), domain.CodeInternal, "an internal error occurred", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.err = tt.err
			cs := e.connect("c1")

			// 同じ失敗が、操作系（Do）と閲覧系（Screenshot）の両方で同じ形になる。
			for _, tool := range []struct {
				name string
				args map[string]any
			}{{"left_click", nil}, {"screenshot", nil}} {
				res := callTool(t, cs, tool.name, tool.args)
				if got := res.errCode(t); got != tt.wantCode {
					t.Fatalf("%s: code = %s, want %s", tool.name, got, tt.wantCode)
				}
				var body errorBody
				res.decode(t, &body)
				if body.Message != tt.wantMessage {
					t.Errorf("%s: message = %q, want %q", tool.name, body.Message, tt.wantMessage)
				}
				if want := fmt.Sprintf("%s: %s", tt.wantCode, tt.wantMessage); res.text() != want {
					t.Errorf("%s: text = %q, want %q", tool.name, res.text(), want)
				}
				if len(body.Detail) != len(tt.wantDetail) {
					t.Errorf("%s: detail = %v, want %v", tool.name, body.Detail, tt.wantDetail)
				}
				for k, v := range tt.wantDetail {
					if body.Detail[k] != v {
						t.Errorf("%s: detail[%s] = %v, want %v", tool.name, k, body.Detail[k], v)
					}
				}
				if strings.Contains(res.text(), "secret") {
					t.Errorf("%s: the error text leaks internal detail: %q", tool.name, res.text())
				}
			}
		})
	}
}

// keepalive の ping に応答できないセッション（サーバーからの ping を受ける GET ストリームが無い）は閉じられ、
// リースが解放される（NFR-A04）。
func TestKeepAliveFailureReleasesTheLease(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.KeepAlive = 20 * time.Millisecond })
	a := e.connect("a")
	callTool(t, a, "acquire_control", nil)

	ev := e.waitEvent(isRelease)
	if ev.Reason != domain.ReleaseDisconnect {
		t.Errorf("reason = %q, want %q", ev.Reason, domain.ReleaseDisconnect)
	}
}
