package ws_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// approvalClients はどのクライアントも承認モード always の service.LeaseClients。
type approvalClients struct{}

func (approvalClients) Get(_ context.Context, id string) (domain.Client, error) {
	return domain.Client{ID: id, ApprovalMode: domain.ApprovalAlways}, nil
}

func (approvalClients) MarkApproved(context.Context, string, time.Time) error { return nil }

// manualTimers は service.AfterFunc の偽物。タイマーは進めず、fire で最後に張ったものを満了させる。
type manualTimers struct {
	mu  sync.Mutex
	fns []func()
}

func (m *manualTimers) after(_ time.Duration, f func()) func() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fns = append(m.fns, f)
	return func() bool { return true }
}

func (m *manualTimers) fire() {
	m.mu.Lock()
	f := m.fns[len(m.fns)-1]
	m.mu.Unlock()
	f()
}

// approvalEnv は承認が要る（always）LeaseManager と実の service.Approval をつないだ環境。
type approvalEnv struct {
	*env
	approval service.Approval
	timers   *manualTimers
}

func newApprovalEnv(t *testing.T) *approvalEnv {
	t.Helper()
	ae := &approvalEnv{timers: &manualTimers{}}
	ae.env = newEnvWithLeases(t, func(e *env, c *service.LeaseConfig) {
		var err error
		ae.approval, err = service.NewApproval(service.ApprovalConfig{Clock: fixedClock{}, IDs: &seqIDs{}, AfterFunc: ae.timers.after})
		if err != nil {
			t.Fatal(err)
		}
		c.Approval, c.Clients, c.Bus = ae.approval, approvalClients{}, e.bus
	})
	return ae
}

// awaitPendingApproval は承認待ちの要求が 1 つ以上になるまで待ち、最初のものを返す。
// lease の pending_approval は、承認の要求（Approval.Request）が登録されるより先に通知される（承認の登録は別の goroutine）。
// lease の通知を読んだ直後に Pending() を読むと、まだ空のことがある。
func (e *approvalEnv) awaitPendingApproval() service.ApprovalRequest {
	e.t.Helper()
	var first service.ApprovalRequest
	waitUntil(e.t, "the approval request to be registered", func() bool {
		p := e.approval.Pending()
		if len(p) == 0 {
			return false
		}
		first = p[0]
		return true
	})
	return first
}

// fireTimeout は承認の要求が登録（タイマーを張る）されるのを待ってから、そのタイマーを満了させる。
func (e *approvalEnv) fireTimeout() {
	e.t.Helper()
	e.awaitPendingApproval() // 要求とタイマーは同じロックの中で登録されるので、要求が見えればタイマーもある
	e.timers.fire()
}

// requestLease は actor の取得要求を別 goroutine で始め、承認待ちになるまで待つ。結果は返したチャネルに届く。
func (e *approvalEnv) requestLease(actor domain.Actor) <-chan error {
	e.t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := e.leases.Acquire(context.Background(), actor)
		done <- err
	}()
	waitUntil(e.t, "the lease to become pending_approval", func() bool { _, ok := e.leases.Pending(); return ok })
	e.awaitPendingApproval()
	return done
}

func awaitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(wait):
		t.Fatal("Acquire did not return")
		return nil
	}
}

// readLeaseState は lease メッセージを、state が want になるまで読む（遷移の通知は、書く時点の状態で合流することがある）。
func readLeaseState(t *testing.T, c *websocket.Conn, want string) map[string]any {
	t.Helper()
	for {
		m := readType(t, c, "lease")
		if m["state"] == want {
			return m
		}
	}
}

func TestPendingApprovalIsPushedToEveryViewerWithMineForTheRequester(t *testing.T) {
	e := newApprovalEnv(t)
	cMine, idMine := e.connect()
	// 同じクライアントの 2 本目のビューア（同時接続は viewer_id ごとに 1 本）。承認待ちの本人ではない。
	idOther := e.issue()
	cOther := e.dial(idOther)
	readType(t, cOther, "ready")

	done := e.requestLease(e.viewerActor(idMine))
	holder := map[string]any{"kind": "web", "label": "Alice"}

	if got, want := readLeaseState(t, cMine, "pending_approval"), (map[string]any{"type": "lease", "state": "pending_approval", "holder": holder, "mine": true}); !reflect.DeepEqual(got, want) {
		t.Errorf("requester's lease = %#v, want %#v", got, want)
	}
	if got, want := readLeaseState(t, cOther, "pending_approval"), (map[string]any{"type": "lease", "state": "pending_approval", "holder": holder, "mine": false}); !reflect.DeepEqual(got, want) {
		t.Errorf("other viewer's lease = %#v, want %#v", got, want)
	}

	// 承認待ちの間に接続し直したビューアの ready の lease も pending_approval。
	ready := readType(t, e.dial(e.issue()), "ready")
	if got, want := ready["lease"], (map[string]any{"state": "pending_approval", "holder": holder, "mine": false}); !reflect.DeepEqual(got, want) {
		t.Errorf("ready lease = %#v, want %#v", got, want)
	}

	pending := e.approval.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(pending))
	}
	if err := e.approval.Resolve(pending[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := awaitErr(t, done); err != nil {
		t.Fatalf("Acquire = %v, want success", err)
	}
	if got := readLeaseState(t, cMine, "held"); got["mine"] != true {
		t.Errorf("lease after approval = %#v, want held with mine true", got)
	}
	if got := readLeaseState(t, cOther, "held"); got["mine"] != false {
		t.Errorf("lease after approval (other viewer) = %#v, want held with mine false", got)
	}
}

// 拒否・タイムアウトで idle に戻り、holder は省かれる。
func TestDeniedAndTimedOutApprovalReturnToIdle(t *testing.T) {
	tests := []struct {
		name    string
		finish  func(e *approvalEnv)
		wantErr error
	}{
		{"denied", func(e *approvalEnv) {
			if err := e.approval.Resolve(e.awaitPendingApproval().ID, false); err != nil {
				e.t.Fatal(err)
			}
		}, domain.ErrApprovalDenied},
		{"timeout", func(e *approvalEnv) { e.fireTimeout() }, domain.ErrApprovalTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newApprovalEnv(t)
			c, id := e.connect()
			done := e.requestLease(e.viewerActor(id))
			readLeaseState(t, c, "pending_approval")

			tt.finish(e)
			if err := awaitErr(t, done); err == nil || domain.CodeOf(err) != domain.CodeOf(tt.wantErr) {
				t.Fatalf("Acquire = %v, want %v", err, tt.wantErr)
			}
			got := readLeaseState(t, c, "idle")
			if want := (map[string]any{"type": "lease", "state": "idle", "mine": false}); !reflect.DeepEqual(got, want) {
				t.Errorf("lease = %#v, want %#v (no holder)", got, want)
			}
		})
	}
}

// 承認待ちの間の入力は、承認待ちの本人でも lease_held_by_other（承認を待たずに held にしない）。
func TestInputDuringPendingApprovalIsRejectedAsLeaseHeldByOther(t *testing.T) {
	e := newApprovalEnv(t)
	c, id := e.connect()
	done := e.requestLease(e.viewerActor(id))
	readLeaseState(t, c, "pending_approval")

	send(t, c, map[string]any{"type": "input.mouse", "action": "move", "x": 1, "y": 1})
	if m := readType(t, c, "error"); m["code"] != "lease_held_by_other" {
		t.Errorf("error = %v, want lease_held_by_other", m)
	}
	if e.control.opCount() != 0 {
		t.Error("an input during pending_approval reached the platform")
	}

	if err := e.approval.Resolve(e.awaitPendingApproval().ID, false); err != nil {
		t.Fatal(err)
	}
	_ = awaitErr(t, done)
}

// RequestAcquire の承認待ちの拒否・タイムアウトは、要求したビューアだけに error メッセージ（approval_denied / approval_timeout）で届く。
// 同じクライアントの別のビューアには届かない。
func TestRequestAcquireFailureIsSentToTheRequestingViewerOnly(t *testing.T) {
	tests := []struct {
		name     string
		finish   func(e *approvalEnv)
		wantCode string
	}{
		{"denied", func(e *approvalEnv) {
			if err := e.approval.Resolve(e.awaitPendingApproval().ID, false); err != nil {
				e.t.Fatal(err)
			}
		}, "approval_denied"},
		{"timeout", func(e *approvalEnv) { e.fireTimeout() }, "approval_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newApprovalEnv(t)
			cMine, idMine := e.connect()
			idOther := e.issue()
			cOther := e.dial(idOther)
			readType(t, cOther, "ready")

			_, pending, err := e.leases.RequestAcquire(context.Background(), e.viewerActor(idMine))
			if err != nil || !pending {
				t.Fatalf("RequestAcquire = pending %v, %v; want pending without error", pending, err)
			}
			readLeaseState(t, cMine, "pending_approval")
			readLeaseState(t, cOther, "pending_approval")

			tt.finish(e)
			// lease（idle）と error は別のトピックなので、届く順は決まっていない。idle の lease と error を両方読む。
			msg := readUntilLeaseAndError(t, cMine, "idle")
			if msg["code"] != tt.wantCode {
				t.Errorf("error = %v, want code %s", msg, tt.wantCode)
			}
			if s, _ := msg["message"].(string); s == "" {
				t.Errorf("error message is empty: %v", msg)
			}

			// 別のビューアには error を送らない: 続けて別のビューアが取得を要求し、承認待ちの lease が届くまでに error が無い。
			readLeaseState(t, cOther, "idle")
			e.leases.ClearApprovalCooldown(alice.ID)
			done := e.requestLease(e.viewerActor(idOther))
			readLeaseState(t, cOther, "pending_approval")
			if err := e.approval.Resolve(e.awaitPendingApproval().ID, false); err != nil {
				t.Fatal(err)
			}
			_ = awaitErr(t, done)
		})
	}
}

// 許可は lease メッセージ（held）で届き、error は送らない。
func TestRequestAcquireGrantIsSentAsLeaseMessage(t *testing.T) {
	e := newApprovalEnv(t)
	c, id := e.connect()
	if _, pending, err := e.leases.RequestAcquire(context.Background(), e.viewerActor(id)); err != nil || !pending {
		t.Fatalf("RequestAcquire = pending %v, %v", pending, err)
	}
	readLeaseState(t, c, "pending_approval")
	if err := e.approval.Resolve(e.awaitPendingApproval().ID, true); err != nil {
		t.Fatal(err)
	}
	if got := readLeaseState(t, c, "held"); got["mine"] != true {
		t.Errorf("lease after approval = %#v", got)
	}
}

// readUntilLeaseAndError は、state が want の lease メッセージと error メッセージの両方が届くまで読み、error メッセージを返す。
func readUntilLeaseAndError(t *testing.T, c *websocket.Conn, want string) map[string]any {
	t.Helper()
	var errMsg map[string]any
	sawLease := false
	for !sawLease || errMsg == nil {
		m := readMsg(t, c)
		switch {
		case m["type"] == "error":
			errMsg = m
		case m["type"] == "lease" && m["state"] == want:
			sawLease = true
		case m["type"] == "lease":
		default:
			t.Fatalf("unexpected message %v", m)
		}
	}
	return errMsg
}
