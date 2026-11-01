package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは承認ダイアログの開閉（approvalDialogs）のテスト。偽の Windows・Clock・タイマーで進める（固定時間の待ちを使わない）。

type dialogClock struct{ now time.Time }

func (c *dialogClock) Now() time.Time { return c.now }

type dialogIDs struct {
	mu sync.Mutex
	n  int
}

func (g *dialogIDs) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return "ap-" + string(rune('0'+g.n))
}

// dialogTimers は service.AfterFunc の偽物。fireAll で張ってあるタイマーをすべて満了させる。
type dialogTimers struct {
	mu  sync.Mutex
	fns []func()
}

func (t *dialogTimers) after(_ time.Duration, f func()) func() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fns = append(t.fns, f)
	return func() bool { return true }
}

func (t *dialogTimers) fireAll() {
	t.mu.Lock()
	fns := t.fns
	t.fns = nil
	t.mu.Unlock()
	for _, f := range fns {
		f()
	}
}

type dialogKit struct {
	t        *testing.T
	windows  *FakeWindows
	approval service.Approval
	timers   *dialogTimers
	bus      *service.Bus
	cancel   context.CancelFunc
	done     <-chan struct{}
}

// newDialogKit は承認と Bus を本物（service.Approval）、Windows を偽物にして approvalDialogs を動かす。
func newDialogKit(t *testing.T) *dialogKit {
	t.Helper()
	k := &dialogKit{t: t, windows: NewFakeWindows(), timers: &dialogTimers{}, bus: service.NewBus()}
	var err error
	k.approval, err = service.NewApproval(service.ApprovalConfig{
		Clock: &dialogClock{now: time.Unix(1_700_000_000, 0)}, IDs: &dialogIDs{}, Bus: k.bus, AfterFunc: k.timers.after,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	k.done = newApprovalDialogs(k.windows, k.approval, k.bus, discardLogger()).Watch(ctx)
	t.Cleanup(func() { cancel(); <-k.done })
	return k
}

var dialogWho = domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "claude", SessionID: "s1"}

type requestResult struct {
	d   service.Decision
	err error
}

// request は別 goroutine で承認を要求し、結果のチャネルを返す。
func (k *dialogKit) request(ctx context.Context, who domain.Actor) <-chan requestResult {
	out := make(chan requestResult, 1)
	go func() {
		d, err := k.approval.Request(ctx, service.ApprovalRequest{Kind: service.ApprovalKindLease, Who: who})
		out <- requestResult{d, err}
	}()
	return out
}

func (k *dialogKit) nextWindow() *FakeApprovalWindow {
	k.t.Helper()
	select {
	case w := <-k.windows.Opened():
		return w
	case <-time.After(waitLimit):
		k.t.Fatal("no approval dialog was opened")
		return nil
	}
}

func (k *dialogKit) expectClosed(w *FakeApprovalWindow) {
	k.t.Helper()
	select {
	case <-w.Closed():
	case <-time.After(waitLimit):
		k.t.Fatalf("the dialog %s was not closed", w.ID())
	}
}

func awaitResult(t *testing.T, ch <-chan requestResult) requestResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(waitLimit):
		t.Fatal("the approval request did not finish")
		return requestResult{}
	}
}

// 承認要求ごとにダイアログが開く（approval → OpenApproval。NFR-P06）。許可・拒否で承認が終わるとダイアログが閉じる。
func TestApprovalOpensADialogAndClosesItWhenResolved(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(map[bool]string{true: "allow", false: "deny"}[allow], func(t *testing.T) {
			k := newDialogKit(t)
			res := k.request(context.Background(), dialogWho)
			w := k.nextWindow()
			if w.ID() != k.approval.Pending()[0].ID {
				t.Errorf("dialog id = %q, want the pending request %q", w.ID(), k.approval.Pending()[0].ID)
			}
			select {
			case <-w.Closed():
				t.Fatal("the dialog was closed before the approval ended")
			default:
			}

			if err := k.approval.Resolve(w.ID(), allow); err != nil {
				t.Fatal(err)
			}
			k.expectClosed(w)
			if r := awaitResult(t, res); r.err != nil || r.d.Allow != allow {
				t.Errorf("result = %+v, want allow=%v", r, allow)
			}
		})
	}
}

// 60 秒の失効（タイマーの満了）と取り下げ（ctx のキャンセル）でも、ダイアログが閉じる。
func TestApprovalDialogClosesOnTimeoutAndWithdrawal(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		k := newDialogKit(t)
		res := k.request(context.Background(), dialogWho)
		w := k.nextWindow()
		k.timers.fireAll()
		k.expectClosed(w)
		if r := awaitResult(t, res); !errors.Is(r.err, domain.ErrApprovalTimeout) {
			t.Errorf("err = %v, want approval_timeout", r.err)
		}
	})
	t.Run("withdrawn", func(t *testing.T) {
		k := newDialogKit(t)
		ctx, cancel := context.WithCancel(context.Background())
		res := k.request(ctx, dialogWho)
		w := k.nextWindow()
		cancel()
		k.expectClosed(w)
		if r := awaitResult(t, res); !errors.Is(r.err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", r.err)
		}
	})
}

// ダイアログが同時に開くのは 1 つだけ。開いている間に来た別の要求は、最初のダイアログが閉じたあとでも開かず、失効で拒否になる
// （複数の要求の扱いは未決のため、順番に開くことはしない）。閉じたあとの新しい要求は開く。購読の goroutine を介さず sync を直接呼び、
// 開閉の順序を決定的にする。
func TestOnlyOneApprovalDialogIsOpenAtATime(t *testing.T) {
	windows, timers, bus := NewFakeWindows(), &dialogTimers{}, service.NewBus()
	approval, err := service.NewApproval(service.ApprovalConfig{Clock: &dialogClock{now: time.Unix(1_700_000_000, 0)}, IDs: &dialogIDs{}, Bus: bus, AfterFunc: timers.after})
	if err != nil {
		t.Fatal(err)
	}
	d := newApprovalDialogs(windows, approval, bus, discardLogger())
	k := &dialogKit{t: t, windows: windows, approval: approval, timers: timers, bus: bus}
	other := domain.Actor{Kind: domain.ActorWeb, ClientID: "c2", Label: "web", SessionID: "s2"}

	first := k.request(context.Background(), dialogWho)
	eventually(t, "the first request", func() bool { return len(approval.Pending()) == 1 })
	d.sync()
	w1 := k.nextWindow()

	second := k.request(context.Background(), other)
	eventually(t, "the second request", func() bool { return len(approval.Pending()) == 2 })
	d.sync()
	if got := len(windows.ApprovalWindows()); got != 1 {
		t.Fatalf("dialogs opened = %d while one is open, want 1", got)
	}

	// 最初が終わればそのダイアログは閉じるが、見送った 2 つ目のためのダイアログは開かない。
	if err := approval.Resolve(w1.ID(), true); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, first)
	d.sync()
	k.expectClosed(w1)
	if got := len(windows.ApprovalWindows()); got != 1 {
		t.Fatalf("dialogs opened = %d, want 1 (the skipped request is not shown later)", got)
	}
	timers.fireAll()
	if r := awaitResult(t, second); !errors.Is(r.err, domain.ErrApprovalTimeout) {
		t.Errorf("second request: err = %v, want approval_timeout", r.err)
	}
	d.sync()

	// 閉じたあとの新しい要求は開く。
	third := k.request(context.Background(), dialogWho)
	eventually(t, "the third request", func() bool { return len(approval.Pending()) == 1 })
	d.sync()
	w3 := k.nextWindow()
	_ = approval.Resolve(w3.ID(), false)
	awaitResult(t, third)
	d.sync()
	k.expectClosed(w3)
}

// 利用者がウィンドウを閉じても（許可も拒否もしない）、承認待ちは続き、60 秒で拒否になる。ダイアログは開き直さない。
func TestClosingTheDialogWithoutAnAnswerLeavesTheRequestToTimeOut(t *testing.T) {
	k := newDialogKit(t)
	res := k.request(context.Background(), dialogWho)
	w := k.nextWindow()
	w.Close() // 利用者が「×」で閉じた

	if p := k.approval.Pending(); len(p) != 1 {
		t.Fatalf("pending = %+v, want the request to stay pending", p)
	}
	k.timers.fireAll()
	if r := awaitResult(t, res); !errors.Is(r.err, domain.ErrApprovalTimeout) {
		t.Errorf("err = %v, want approval_timeout", r.err)
	}
	if got := len(k.windows.ApprovalWindows()); got != 1 {
		t.Errorf("dialogs opened = %d, want 1 (not reopened)", got)
	}
}

// M3 の kind は lease だけ。lease 以外の承認（M4）のダイアログは開かない。
func TestNonLeaseApprovalDoesNotOpenADialog(t *testing.T) {
	k := newDialogKit(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() {
		_, err := k.approval.Request(ctx, service.ApprovalRequest{Kind: "safety_check", Who: dialogWho})
		res <- err
	}()
	eventually(t, "the request to be pending", func() bool { return len(k.approval.Pending()) == 1 })
	// 後続の lease の要求が開けば、先の safety_check は（購読側が処理済みで）開かなかったと分かる。
	k.request(context.Background(), dialogWho)
	w := k.nextWindow()
	if p := k.approval.Pending(); len(p) != 2 || w.ID() != p[1].ID {
		t.Errorf("dialog %q, pending %+v: want the dialog for the lease request only", w.ID(), p)
	}
	cancel()
	<-res
}

// シャットダウン（ctx の終了）で、開いているダイアログをすべて閉じてから戻る。
func TestShutdownClosesTheOpenDialog(t *testing.T) {
	k := newDialogKit(t)
	k.request(context.Background(), dialogWho)
	w := k.nextWindow()

	k.cancel()
	select {
	case <-k.done:
	case <-time.After(waitLimit):
		t.Fatal("Watch did not stop")
	}
	select {
	case <-w.Closed():
	default:
		t.Error("the dialog must be closed before Watch returns")
	}
}

// Start 時にすでに承認待ちがあれば（購読の前に始まった要求）、ダイアログを開く。
func TestPendingApprovalAtStartOpensADialog(t *testing.T) {
	windows, timers, bus := NewFakeWindows(), &dialogTimers{}, service.NewBus()
	approval, err := service.NewApproval(service.ApprovalConfig{Clock: &dialogClock{now: time.Unix(1_700_000_000, 0)}, IDs: &dialogIDs{}, Bus: bus, AfterFunc: timers.after})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = approval.Request(ctx, service.ApprovalRequest{Kind: service.ApprovalKindLease, Who: dialogWho})
	}()
	eventually(t, "the request to be pending", func() bool { return len(approval.Pending()) == 1 })

	done := newApprovalDialogs(windows, approval, bus, discardLogger()).Watch(ctx)
	select {
	case w := <-windows.Opened():
		if w.ID() != approval.Pending()[0].ID {
			t.Errorf("dialog id = %q", w.ID())
		}
	case <-time.After(waitLimit):
		t.Fatal("no dialog for the request that was already pending")
	}
	cancel()
	<-done
}

// 結線の確認（実 App）: 承認が要るクライアントのリース取得で承認ダイアログが開き、GUI ルーターの POST /approvals/{id}/allow で
// リースが held になり、ダイアログが閉じる。拒否は approval_denied。ダイアログの URL が指す GET /approvals/{id} は、クライアント名と
// 種別を出す。Shutdown でも閉じる。
func TestAppWiresTheApprovalDialogToTheLeaseAndTheGUIRouter(t *testing.T) {
	e := newEnv(t)
	e.start()
	clientID, _ := e.registerClient()
	always := domain.ApprovalAlways
	if err := e.db.Clients().Update(t.Context(), clientID, repository.ClientPatch{ApprovalMode: &always}); err != nil {
		t.Fatal(err)
	}
	gui := guiClient{t: t, h: e.app.GUIHandler()}
	who := domain.Actor{Kind: domain.ActorMCP, ClientID: clientID, Label: "alice-laptop", SessionID: "mcp-session-1"}

	acquire := func() <-chan error {
		out := make(chan error, 1)
		go func() { _, err := e.app.leases.Acquire(context.Background(), who); out <- err }()
		return out
	}
	nextDialog := func() *FakeApprovalWindow {
		select {
		case w := <-e.dialogs.Opened():
			return w
		case <-time.After(waitLimit):
			t.Fatal("no approval dialog was opened")
			return nil
		}
	}

	// 許可: ダイアログのページ → allow → held。
	res := acquire()
	w := nextDialog()
	page := httptest.NewRecorder()
	e.app.GUIHandler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/approvals/"+url.PathEscape(w.ID()), nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "alice-laptop") || !strings.Contains(page.Body.String(), "MCP") ||
		!strings.Contains(page.Body.String(), `hx-post="/approvals/`+w.ID()+`/allow"`) {
		t.Fatalf("GET /approvals/%s = %d\n%s", w.ID(), page.Code, page.Body)
	}
	rec := gui.do(http.MethodPost, "/approvals/"+url.PathEscape(w.ID())+"/allow", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Trigger") != "closeWindow" {
		t.Fatalf("allow = %d, HX-Trigger %q\n%s", rec.Code, rec.Header().Get("HX-Trigger"), rec.Body)
	}
	if err := <-res; err != nil {
		t.Fatalf("Acquire = %v, want success after the host allowed", err)
	}
	if h, ok := e.holder(); !ok || h.Actor.ClientID != clientID {
		t.Errorf("holder = %+v, %v", h, ok)
	}
	select {
	case <-w.Closed():
	case <-time.After(waitLimit):
		t.Fatal("the dialog was not closed after the answer")
	}
	// 二度目は解決済みなので 410 approval_timeout。
	if rec := gui.do(http.MethodPost, "/approvals/"+url.PathEscape(w.ID())+"/allow", nil); rec.Code != http.StatusGone {
		t.Errorf("second allow = %d, want 410", rec.Code)
	}

	// 拒否: approval_denied、リースは取れない。
	e.app.leases.Release(who)
	res = acquire()
	w = nextDialog()
	if rec := gui.do(http.MethodPost, "/approvals/"+url.PathEscape(w.ID())+"/deny", nil); rec.Code != http.StatusOK {
		t.Fatalf("deny = %d\n%s", rec.Code, rec.Body)
	}
	if err := <-res; domain.CodeOf(err) != domain.CodeApprovalDenied {
		t.Errorf("Acquire = %v, want approval_denied", err)
	}
	if _, ok := e.holder(); ok {
		t.Error("the lease must not be held after a denial")
	}

	// 承認待ちのままシャットダウンすると、ダイアログが閉じる。
	res = acquire()
	w = nextDialog()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := e.app.Shutdown(ctx); err != nil {
		t.Logf("shutdown: %v", err)
	}
	select {
	case <-w.Closed():
	default:
		t.Error("Shutdown must close the open dialog")
	}
	<-res
}
