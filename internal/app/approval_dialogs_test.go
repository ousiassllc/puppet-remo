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

type dialogClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *dialogClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *dialogClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

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

// fireFirst は最初に張ったタイマー（最も古い承認要求の失効）だけを満了させる。
func (t *dialogTimers) fireFirst() {
	t.mu.Lock()
	if len(t.fns) == 0 {
		t.mu.Unlock()
		return
	}
	f := t.fns[0]
	t.fns = t.fns[1:]
	t.mu.Unlock()
	f()
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

// queueKit は approvalDialogs の sync を購読の goroutine を介さず直接呼ぶ（開閉の順序を決定的にする）。時計は要求ごとに進められる。
type queueKit struct {
	*dialogKit
	d     *approvalDialogs
	clock *dialogClock
}

func newQueueKit(t *testing.T) *queueKit {
	t.Helper()
	windows, timers, bus := NewFakeWindows(), &dialogTimers{}, service.NewBus()
	clock := &dialogClock{now: time.Unix(1_700_000_000, 0)}
	approval, err := service.NewApproval(service.ApprovalConfig{Clock: clock, IDs: &dialogIDs{}, Bus: bus, AfterFunc: timers.after})
	if err != nil {
		t.Fatal(err)
	}
	return &queueKit{
		dialogKit: &dialogKit{t: t, windows: windows, approval: approval, timers: timers, bus: bus},
		d:         newApprovalDialogs(windows, approval, bus, discardLogger()),
		clock:     clock,
	}
}

// requestAndSync は要求を出し、承認待ちに入ったあとで sync する。n は出したあとの承認待ちの件数。
func (q *queueKit) requestAndSync(who domain.Actor, n int) {
	q.t.Helper()
	q.request(context.Background(), who)
	eventually(q.t, "the request to be pending", func() bool { return len(q.approval.Pending()) == n })
	q.d.sync()
}

func (q *queueKit) opened() int { return len(q.windows.ApprovalWindows()) }

// ダイアログが同時に開くのは 1 つだけ。開いている間に来た別の要求は到着順に待たされ、今の要求が終わったら次が開く（#68）。
// 待たされた要求の 60 秒は、その要求自身の到着時刻から数える（ExpiresAt）。
func TestOverlappingApprovalsAreQueuedInArrivalOrder(t *testing.T) {
	q := newQueueKit(t)
	t0 := q.clock.Now()
	who := func(n string) domain.Actor {
		return domain.Actor{Kind: domain.ActorWeb, ClientID: "c" + n, Label: "web" + n, SessionID: "s" + n}
	}

	q.requestAndSync(who("1"), 1)
	w1 := q.nextWindow()
	q.clock.advance(10 * time.Second)
	q.requestAndSync(who("2"), 2)
	q.clock.advance(10 * time.Second)
	q.requestAndSync(who("3"), 3)
	if got := q.opened(); got != 1 {
		t.Fatalf("dialogs opened = %d while one is open, want 1", got)
	}

	// 1 つ目が終われば、そのダイアログは閉じ、到着順で次（2 つ目）が開く。3 つ目はまだ開かない。
	if err := q.approval.Resolve(w1.ID(), true); err != nil {
		t.Fatal(err)
	}
	q.d.sync()
	q.expectClosed(w1)
	w2 := q.nextWindow()
	pending := q.approval.Pending()
	if len(pending) != 2 || w2.ID() != pending[0].ID {
		t.Fatalf("second dialog %q, pending %+v: want the second arrival", w2.ID(), pending)
	}
	if got := q.opened(); got != 2 {
		t.Fatalf("dialogs opened = %d, want 2", got)
	}
	// 失効は各要求の到着から 60 秒（待っている間も進んでいる）。
	if want := t0.Add(10*time.Second + service.DefaultApprovalTimeout); !pending[0].ExpiresAt.Equal(want) {
		t.Errorf("second request ExpiresAt = %v, want %v", pending[0].ExpiresAt, want)
	}
	if want := t0.Add(20*time.Second + service.DefaultApprovalTimeout); !pending[1].ExpiresAt.Equal(want) {
		t.Errorf("third request ExpiresAt = %v, want %v", pending[1].ExpiresAt, want)
	}

	// 2 つ目が拒否で終われば 3 つ目が開く。3 つ目が終われば何も残らない。
	if err := q.approval.Resolve(w2.ID(), false); err != nil {
		t.Fatal(err)
	}
	q.d.sync()
	q.expectClosed(w2)
	w3 := q.nextWindow()
	if p := q.approval.Pending(); len(p) != 1 || w3.ID() != p[0].ID {
		t.Fatalf("third dialog %q, pending %+v", w3.ID(), p)
	}
	if err := q.approval.Resolve(w3.ID(), true); err != nil {
		t.Fatal(err)
	}
	q.d.sync()
	q.expectClosed(w3)
	if got := q.opened(); got != 3 {
		t.Errorf("dialogs opened = %d, want 3", got)
	}

	// 待ちが空になったあとの新しい要求は、すぐ開く。
	q.requestAndSync(who("4"), 1)
	w4 := q.nextWindow()
	_ = q.approval.Resolve(w4.ID(), false)
	q.d.sync()
	q.expectClosed(w4)
}

// 待たされている間に失効（60 秒）した要求は、ダイアログを開かない。今の要求の失効で次が開くが、その次がすでに失効していれば飛ばす。
func TestQueuedApprovalThatExpiredWhileWaitingIsNotShown(t *testing.T) {
	q := newQueueKit(t)
	other := domain.Actor{Kind: domain.ActorWeb, ClientID: "c2", Label: "web", SessionID: "s2"}
	third := domain.Actor{Kind: domain.ActorWeb, ClientID: "c3", Label: "web3", SessionID: "s3"}

	first := q.request(context.Background(), dialogWho)
	eventually(q.t, "the first request", func() bool { return len(q.approval.Pending()) == 1 })
	q.d.sync()
	w1 := q.nextWindow()
	second := q.request(context.Background(), other)
	eventually(q.t, "the second request", func() bool { return len(q.approval.Pending()) == 2 })
	q.requestAndSync(third, 3)

	// 1 つ目と 2 つ目の 60 秒が過ぎる（タイマーは要求の順に張られている）。3 つ目はまだ生きている。
	q.timers.fireFirst()
	if r := awaitResult(t, first); !errors.Is(r.err, domain.ErrApprovalTimeout) {
		t.Fatalf("first: err = %v, want approval_timeout", r.err)
	}
	q.timers.fireFirst()
	if r := awaitResult(t, second); !errors.Is(r.err, domain.ErrApprovalTimeout) {
		t.Fatalf("second: err = %v, want approval_timeout", r.err)
	}
	q.d.sync()
	q.expectClosed(w1)
	w3 := q.nextWindow()
	if p := q.approval.Pending(); len(p) != 1 || w3.ID() != p[0].ID {
		t.Fatalf("dialog %q, pending %+v: want the dialog for the third request only", w3.ID(), p)
	}
	if got := q.opened(); got != 2 {
		t.Errorf("dialogs opened = %d, want 2 (the expired second request is never shown)", got)
	}
}

// 利用者が今のダイアログを許可も拒否もせずに閉じても、その要求は終わるまで「今の要求」のまま（60 秒で拒否になる）。
// 待たせている次の要求のダイアログは、その要求が終わるまで開かず、閉じたダイアログも開き直さない。
func TestClosingTheCurrentDialogDoesNotOpenTheNextOneUntilTheRequestEnds(t *testing.T) {
	q := newQueueKit(t)
	other := domain.Actor{Kind: domain.ActorWeb, ClientID: "c2", Label: "web", SessionID: "s2"}

	first := q.request(context.Background(), dialogWho)
	eventually(q.t, "the first request", func() bool { return len(q.approval.Pending()) == 1 })
	q.d.sync()
	w1 := q.nextWindow()
	q.requestAndSync(other, 2)

	w1.Close() // 利用者が「×」で閉じた
	q.d.sync()
	if got := q.opened(); got != 1 {
		t.Fatalf("dialogs opened = %d after the user closed the dialog, want 1 (neither reopened nor the next one)", got)
	}
	if p := q.approval.Pending(); len(p) != 2 {
		t.Fatalf("pending = %+v, want both requests to keep waiting", p)
	}

	// 60 秒で最初の要求が拒否になれば、次の要求のダイアログが開く。閉じたダイアログは開き直さない。
	q.timers.fireFirst()
	if r := awaitResult(t, first); !errors.Is(r.err, domain.ErrApprovalTimeout) {
		t.Fatalf("first: err = %v, want approval_timeout", r.err)
	}
	q.d.sync()
	w2 := q.nextWindow()
	if p := q.approval.Pending(); len(p) != 1 || w2.ID() != p[0].ID {
		t.Fatalf("dialog %q, pending %+v: want the second request", w2.ID(), p)
	}
	if got := q.opened(); got != 2 {
		t.Errorf("dialogs opened = %d, want 2", got)
	}
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

// 承認の種類（lease・safety_check・judge_action。FR-H05）にかかわらず、要求ごとにダイアログが開く。開く種類が渡り、
// 到着順の待ち行列（同時に 1 つ）も種類をまたいで守られる。
func TestEveryApprovalKindOpensADialog(t *testing.T) {
	k := newDialogKit(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agentWho := domain.Actor{Kind: domain.ActorAgent, ClientID: "c1", Label: "claude", SessionID: "task-1"}
	kinds := []string{service.ApprovalKindSafetyCheck, service.ApprovalKindJudgeAction, service.ApprovalKindLease}
	results := make([]chan error, len(kinds))
	for i, kind := range kinds {
		results[i] = make(chan error, 1)
		go func() {
			_, err := k.approval.Request(ctx, service.ApprovalRequest{Kind: kind, Who: agentWho, Detail: "detail"})
			results[i] <- err
		}()
		eventually(t, "the request to be pending", func() bool { return len(k.approval.Pending()) == i+1 })
	}
	for i, kind := range kinds {
		w := k.nextWindow()
		if p := k.approval.Pending(); w.ID() != p[0].ID || w.Kind() != kind {
			t.Fatalf("dialog %q (kind %q), pending %+v: want the oldest request of kind %q", w.ID(), w.Kind(), p, kind)
		}
		if got := len(k.windows.ApprovalWindows()); got != i+1 {
			t.Fatalf("dialogs opened = %d, want %d (one at a time)", got, i+1)
		}
		if err := k.approval.Resolve(w.ID(), false); err != nil {
			t.Fatal(err)
		}
		k.expectClosed(w)
	}
	for i := range kinds {
		if err := <-results[i]; err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
}

// 承認の種類ごとのウィンドウの高さ: lease は M3 のまま、safety_check・judge_action は内容の説明の分だけ高い。
func TestApprovalWindowHeightByKind(t *testing.T) {
	lease := approvalWindowHeight(service.ApprovalKindLease)
	if lease != approvalHeightLease {
		t.Errorf("lease height = %d, want %d", lease, approvalHeightLease)
	}
	for _, kind := range []string{service.ApprovalKindSafetyCheck, service.ApprovalKindJudgeAction} {
		if got := approvalWindowHeight(kind); got != approvalHeightAgent || got <= lease {
			t.Errorf("%s height = %d, want %d (taller than lease)", kind, got, approvalHeightAgent)
		}
	}
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

	// 承認待ちのままシャットダウンすると、ダイアログが閉じる。拒否の直後は同じクライアントがクールダウンで承認なしに断られる（FR-L07）ので、先に解く。
	e.app.leases.ClearApprovalCooldown(clientID)
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
	if err := <-res; err == nil {
		t.Error("Acquire must not succeed when shutdown withdraws the pending approval")
	}
}
