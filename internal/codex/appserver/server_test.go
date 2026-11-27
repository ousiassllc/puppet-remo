package appserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/codextest"
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
)

func TestMain(m *testing.M) {
	codextest.HelperMain()
	os.Exit(m.Run())
}

// ctxT はテストの上限つきの ctx（固定の待ちではなく、止まったときに失敗させるため）。
func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type rig struct {
	srv   *appserver.Server
	home  string
	mu    sync.Mutex
	fakes []*codextest.Fake
	procs []*codextest.Proc
	setup func(*codextest.Fake)
}

func newRig(t *testing.T, setup func(*codextest.Fake)) *rig {
	t.Helper()
	r := &rig{home: t.TempDir(), setup: setup}
	r.srv = appserver.New(appserver.Options{
		CodexHome: r.home, Version: "9.9.9", InitTimeout: 10 * time.Second,
		Start: func() (appserver.Process, error) {
			f := &codextest.Fake{CodexHome: r.home}
			if r.setup != nil {
				r.setup(f)
			}
			p := codextest.Start(f)
			r.mu.Lock()
			r.fakes, r.procs = append(r.fakes, f), append(r.procs, p)
			r.mu.Unlock()
			return p, nil
		},
	})
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.procs)
}

func (r *rig) fake(i int) *codextest.Fake {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakes[i]
}

func (r *rig) proc(i int) *codextest.Proc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.procs[i]
}

func TestEnsureはハンドシェイクをして実験APIを宣言する(t *testing.T) {
	r := newRig(t, nil)
	se, err := r.srv.Ensure(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if st := r.srv.Status(); st.State != appserver.StateRunning {
		t.Fatalf("status = %+v", st)
	}
	init := r.fake(0).Calls(protocol.MethodInitialize)
	if len(init) != 1 {
		t.Fatalf("initialize calls = %d", len(init))
	}
	var p struct {
		ClientInfo   protocol.ClientInfo   `json:"clientInfo"`
		Capabilities protocol.Capabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(init[0].Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.ClientInfo.Name != protocol.ClientInfoName || p.ClientInfo.Version != "9.9.9" || !p.Capabilities.ExperimentalAPI {
		t.Errorf("initialize params = %+v", p)
	}
	if len(r.fake(0).WaitFor(ctxT(t), protocol.NotifInitialized, 1)) != 1 {
		t.Error("initialized was not sent")
	}
	// 2 回目は同じセッション（プロセスは 1 つ）。
	se2, err := r.srv.Ensure(ctxT(t))
	if err != nil || se2 != se || r.count() != 1 {
		t.Fatalf("second Ensure: %v, same=%v, procs=%d", err, se2 == se, r.count())
	}
}

func TestEnsureはcodexHomeが違うと止めて起動を失敗にする(t *testing.T) {
	other := t.TempDir()
	home := t.TempDir()
	var procs []*codextest.Proc
	srv := appserver.New(appserver.Options{
		CodexHome: home,
		Start: func() (appserver.Process, error) {
			p := codextest.Start(&codextest.Fake{CodexHome: other})
			procs = append(procs, p)
			return p, nil
		},
	})
	t.Cleanup(srv.Close)
	_, err := srv.Ensure(ctxT(t))
	if !errors.Is(err, appserver.ErrNotOurHome) || !errors.Is(err, appserver.ErrStart) {
		t.Fatalf("err = %v", err)
	}
	if !procs[0].Stopped() {
		t.Error("the process must be stopped")
	}
	select {
	case <-procs[0].Done():
	default:
		t.Error("the process did not end")
	}
	if st := srv.Status(); st.State != appserver.StateFailed || st.Error == "" {
		t.Fatalf("status = %+v", st)
	}
	// 次の利用は起動し直す（もう一度失敗する）。
	if _, err := srv.Ensure(ctxT(t)); !errors.Is(err, appserver.ErrNotOurHome) || len(procs) != 2 {
		t.Fatalf("second: %v, procs = %d", err, len(procs))
	}
}

func TestEnsureはcodexHomeが空のときと存在しない別の綴りのとき止める(t *testing.T) {
	home := t.TempDir()
	for name, reported := range map[string]string{"空": "", "別の場所": filepath.Join(home, "sub")} {
		t.Run(name, func(t *testing.T) {
			srv := appserver.New(appserver.Options{
				CodexHome: home,
				Start:     func() (appserver.Process, error) { return codextest.Start(&codextest.Fake{CodexHome: reported}), nil },
			})
			t.Cleanup(srv.Close)
			if _, err := srv.Ensure(ctxT(t)); !errors.Is(err, appserver.ErrNotOurHome) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestEnsureはシンボリックリンク経由の同じディレクトリを受け入れる(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(home, link); err != nil {
		t.Skipf("symlink is not available: %v", err)
	}
	srv := appserver.New(appserver.Options{
		CodexHome: home,
		Start:     func() (appserver.Process, error) { return codextest.Start(&codextest.Fake{CodexHome: link}), nil },
	})
	t.Cleanup(srv.Close)
	if _, err := srv.Ensure(ctxT(t)); err != nil {
		t.Fatal(err)
	}
}

func Testサーバーからの要求は拒否する(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.srv.Ensure(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	f := r.fake(0)
	decide := func(method string) string {
		raw, rerr, err := f.Ask(ctxT(t), method, map[string]any{"threadId": "nobody"})
		if err != nil || rerr != nil {
			t.Fatalf("%s: %v %v", method, err, rerr)
		}
		var d struct{ Decision, Action string }
		_ = json.Unmarshal(raw, &d)
		return d.Decision + d.Action
	}
	if got := decide(protocol.ReqCommandApproval); got != "decline" {
		t.Errorf("command approval = %q", got)
	}
	if got := decide(protocol.ReqFileChangeApproval); got != "decline" {
		t.Errorf("file change approval = %q", got)
	}
	if got := decide(protocol.ReqLegacyExecApproval); got != "abort" {
		t.Errorf("legacy exec approval = %q", got)
	}
	if got := decide(protocol.ReqMCPElicitation); got != "decline" {
		t.Errorf("mcp elicitation = %q", got)
	}
	// トークンの供給と未知の要求はエラー応答。
	for _, m := range []string{protocol.ReqAuthTokensRefresh, protocol.ReqAttestation, "something/new"} {
		if _, rerr, err := f.Ask(ctxT(t), m, map[string]any{}); err != nil || rerr == nil || rerr.Code != rpc.CodeMethodNotFound {
			t.Errorf("%s: rerr = %v, err = %v", m, rerr, err)
		}
	}
	// 購読していないスレッドの desktop の呼び出しも、他の namespace の呼び出しも拒否（success=false）。
	for name, params := range map[string]map[string]any{
		"購読なし":       {"threadId": "t-none", "turnId": "u", "callId": "c", "namespace": "desktop", "tool": "left_click", "arguments": map[string]any{}},
		"desktop 以外": {"threadId": "t-none", "turnId": "u", "callId": "c", "namespace": "shell", "tool": "run", "arguments": map[string]any{}},
	} {
		raw, rerr, err := f.Ask(ctxT(t), protocol.ReqDynamicToolCall, params)
		var resp protocol.DynamicToolResponse
		if err != nil || rerr != nil || json.Unmarshal(raw, &resp) != nil || resp.Success || len(resp.ContentItems) != 0 {
			t.Errorf("%s: resp = %+v rerr=%v err=%v", name, resp, rerr, err)
		}
	}
}

func TestSubscribeしたスレッドのdesktopの呼び出しはイベントとして渡し応答を保留できる(t *testing.T) {
	r := newRig(t, nil)
	se, err := r.srv.Ensure(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	sub := se.Subscribe("th-1")
	f := r.fake(0)
	type result struct {
		resp protocol.DynamicToolResponse
		rerr *rpc.Error
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, rerr, err := f.ToolCall(ctxT(t), "th-1", "tu-1", "call-1", "left_click", `{"x":1,"y":2}`)
		got <- result{resp, rerr, err}
	}()
	ev, ok := sub.Next(ctxT(t), se.Done())
	if !ok || ev.Method != protocol.ReqDynamicToolCall || ev.Respond == nil {
		t.Fatalf("event = %+v ok=%v", ev, ok)
	}
	var p protocol.DynamicToolCallParams
	if err := json.Unmarshal(ev.Params, &p); err != nil || p.CallID != "call-1" || p.Tool != "left_click" || string(p.Arguments) != `{"x":1,"y":2}` {
		t.Fatalf("params = %+v err=%v", p, err)
	}
	select {
	case <-got:
		t.Fatal("the response was sent before Respond")
	default:
	}
	ev.Respond(protocol.DynamicToolResponse{Success: true, ContentItems: []protocol.ContentItem{{Type: "inputText", Text: "ok"}}}, nil)
	res := <-got
	if res.err != nil || res.rerr != nil || !res.resp.Success || len(res.resp.ContentItems) != 1 || res.resp.ContentItems[0].Text != "ok" {
		t.Fatalf("result = %+v", res)
	}
	// 通知はイベントに届き、Unsubscribe 後は捨てられる。
	f.Notify(protocol.NotifTurnStarted, map[string]any{"threadId": "th-1"})
	if ev, ok := sub.Next(ctxT(t), se.Done()); !ok || ev.Method != protocol.NotifTurnStarted {
		t.Fatalf("notification = %+v ok=%v", ev, ok)
	}
	se.Unsubscribe("th-1")
	if _, ok := sub.TryNext(); ok {
		t.Error("an unsubscribed sub must be empty")
	}
}

func Testプロセスの異常終了を検知し次の利用で起動し直す(t *testing.T) {
	r := newRig(t, nil)
	se, err := r.srv.Ensure(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	r.proc(0).Crash()
	select {
	case <-se.Done():
	case <-ctxT(t).Done():
		t.Fatal("the end of the process was not noticed")
	}
	if reason := se.EndReason(); !strings.Contains(reason, "crashed") {
		t.Errorf("EndReason = %q", reason)
	}
	if st := r.srv.Status(); st.State != appserver.StateFailed || st.Error == "" {
		t.Fatalf("status = %+v", st)
	}
	if err := se.Call(ctxT(t), protocol.MethodAccountRead, protocol.GetAccountParams{}, nil); !errors.Is(err, rpc.ErrClosed) {
		t.Errorf("call on the dead session: %v", err)
	}
	se2, err := r.srv.Ensure(ctxT(t))
	if err != nil || se2 == se || r.count() != 2 {
		t.Fatalf("restart: %v, new=%v, procs=%d", err, se2 != se, r.count())
	}
	if st := r.srv.Status(); st.State != appserver.StateRunning {
		t.Fatalf("status after the restart = %+v", st)
	}
	if err := se2.Call(ctxT(t), protocol.MethodAccountRead, protocol.GetAccountParams{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStopはプロセスを終わらせ次の利用で起動しCloseは永久に止める(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.srv.Ensure(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	r.srv.Stop()
	select {
	case <-r.proc(0).Done():
	default:
		t.Fatal("Stop did not end the process")
	}
	if st := r.srv.Status(); st.State != appserver.StateStopped {
		t.Fatalf("status = %+v", st)
	}
	if r.srv.Running() {
		t.Fatal("Running after Stop")
	}
	if _, err := r.srv.Ensure(ctxT(t)); err != nil || r.count() != 2 {
		t.Fatalf("Ensure after Stop: %v procs=%d", err, r.count())
	}
	r.srv.Close()
	select {
	case <-r.proc(1).Done():
	default:
		t.Fatal("Close did not end the process")
	}
	if _, err := r.srv.Ensure(ctxT(t)); !errors.Is(err, appserver.ErrStart) {
		t.Fatalf("Ensure after Close: %v", err)
	}
}

func TestEnsureは起動の失敗をErrStartにしFailedにする(t *testing.T) {
	boom := errors.New("boom")
	srv := appserver.New(appserver.Options{CodexHome: t.TempDir(), Start: func() (appserver.Process, error) { return nil, boom }})
	t.Cleanup(srv.Close)
	if _, err := srv.Ensure(ctxT(t)); !errors.Is(err, appserver.ErrStart) || !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if st := srv.Status(); st.State != appserver.StateFailed {
		t.Fatalf("status = %+v", st)
	}
}

func TestアカウントのリスナーはアカウントのNotificationを受ける(t *testing.T) {
	r := newRig(t, nil)
	methods := make(chan string, 4)
	r.srv.SetAccountListener(func(method string, _ json.RawMessage) { methods <- method })
	if _, err := r.srv.Ensure(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	f := r.fake(0)
	f.Notify(protocol.NotifLoginCompleted, map[string]any{"success": true})
	f.Notify(protocol.NotifAccountUpdated, map[string]any{"authMode": "chatgpt"})
	f.Notify(protocol.NotifRateLimits, map[string]any{})
	got := []string{<-methods, <-methods, <-methods}
	want := []string{protocol.NotifLoginCompleted, protocol.NotifAccountUpdated, protocol.NotifRateLimits}
	if !slices.Equal(got, want) {
		t.Fatalf("methods = %v", got)
	}
}
