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
		c.Approval, c.Clients = ae.approval, approvalClients{}
	})
	return ae
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
			if err := e.approval.Resolve(e.approval.Pending()[0].ID, false); err != nil {
				e.t.Fatal(err)
			}
		}, domain.ErrApprovalDenied},
		{"timeout", func(e *approvalEnv) { e.timers.fire() }, domain.ErrApprovalTimeout},
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

	if err := e.approval.Resolve(e.approval.Pending()[0].ID, false); err != nil {
		t.Fatal(err)
	}
	_ = awaitErr(t, done)
}
