package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/catalog"
	"github.com/ousiassllc/puppet-remo/internal/codex/codextest"
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

const codexTestExe = "codex-app-server-test.exe"

var codexTestExeBody = bytes.Repeat([]byte("fake-codex"), 500)

func codexSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func codexZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(codexTestExe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(codexTestExeBody); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// codexBrowser は URL を記録する BrowserOpener の偽物。
type codexBrowser struct {
	mu   sync.Mutex
	urls []string
	err  error
}

func (b *codexBrowser) OpenURL(_ context.Context, u string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.urls = append(b.urls, u)
	return nil
}

func (b *codexBrowser) URLs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.urls...)
}

type codexEnv struct {
	t         *testing.T
	svc       *CodexAuthService
	audit     *ctlAudit
	settings  *ctlSettings
	browser   *codexBrowser
	toasts    <-chan Event
	bus       *Bus
	srv       *httptest.Server
	archive   []byte
	gate      chan struct{} // 非 nil の間、ダウンロードの応答はこれが閉じるまで待つ
	engDir    string
	dataDir   string
	cancelled []string
	cancelID  string

	mu      sync.Mutex
	account *protocol.Account // 偽の app-server が答えるログイン中のアカウント
	authURL string
	fakes   []*codextest.Fake
	procs   []*codextest.Proc
	script  func(*codextest.Fake)
	models  []protocol.Model
	started atomic.Int32
}

type codexOpt func(*codexEnv, *CodexAuthConfig)

func codexEnvNew(t *testing.T, opts ...codexOpt) *codexEnv {
	t.Helper()
	e := &codexEnv{t: t, audit: &ctlAudit{}, settings: newCtlSettings(), browser: &codexBrowser{}, bus: NewBus(),
		engDir: filepath.Join(t.TempDir(), "codex"), dataDir: t.TempDir(), authURL: "https://auth.example.invalid/login?x=1", archive: codexZip(t)}
	e.toasts, _ = e.bus.Subscribe(TopicToast)
	e.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if e.gate != nil {
			<-e.gate
		}
		_, _ = w.Write(e.archive)
	}))
	t.Cleanup(e.srv.Close)
	cfg := e.config()
	for _, o := range opts {
		o(e, &cfg)
	}
	e.svc = e.newService(cfg)
	return e
}

func (e *codexEnv) config() CodexAuthConfig {
	cat := catalog.Catalog{Version: "0.0.1", Tag: "rust-v0.0.1", Assets: map[string]catalog.Asset{"windows/amd64": {
		URL: e.srv.URL + "/codex.zip", Format: catalog.FormatZip, SHA256: codexSum(e.archive), Size: int64(len(e.archive)),
		Exe: codexTestExe, ExeSHA256: codexSum(codexTestExeBody), ExeSize: int64(len(codexTestExeBody)),
	}}}
	return CodexAuthConfig{
		EngineDir: e.engDir, DataDir: e.dataDir, Browser: e.browser, Settings: e.settings, Audit: e.audit, Bus: e.bus,
		Clock: newCtlClock(), Catalog: &cat, HTTPClient: e.srv.Client(), GOOS: "windows", GOARCH: "amd64",
		CancelTasks: func(reason string) string {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.cancelled = append(e.cancelled, reason)
			return e.cancelID
		},
		StartProcess: e.startProcess,
	}
}

func (e *codexEnv) newService(cfg CodexAuthConfig) *CodexAuthService {
	e.t.Helper()
	svc, err := NewCodexAuth(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return svc
}

func (e *codexEnv) startProcess(l appserver.Launch) (appserver.Process, error) {
	if _, err := os.Stat(l.Exe); err != nil {
		e.t.Errorf("the engine does not exist: %v", err)
	}
	f := &codextest.Fake{CodexHome: l.CodexHome}
	e.mu.Lock()
	f.AuthURL, f.Models = e.authURL, e.models
	f.SetAccount(e.account)
	f.Handlers = map[string]codextest.Handler{
		protocol.MethodLogout: func(*codextest.Fake, codextest.Message) (any, *rpc.Error) {
			e.mu.Lock()
			e.account = nil
			e.mu.Unlock()
			f.SetAccount(nil)
			return nil, nil
		},
	}
	if e.script != nil {
		e.script(f)
	}
	p := codextest.Start(f)
	e.fakes, e.procs = append(e.fakes, f), append(e.procs, p)
	e.mu.Unlock()
	e.started.Add(1)
	return p, nil
}

func (e *codexEnv) lastFake() *codextest.Fake {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fakes[len(e.fakes)-1]
}

func (e *codexEnv) setAccount(a *protocol.Account) {
	e.mu.Lock()
	e.account = a
	e.mu.Unlock()
	e.lastFake().SetAccount(a)
}

func codexCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// toast は次のトーストを待つ。
func (e *codexEnv) toast() Toast {
	e.t.Helper()
	select {
	case ev := <-e.toasts:
		return ev.Data.(Toast)
	case <-codexCtx(e.t).Done():
		e.t.Fatal("no toast")
		return Toast{}
	}
}

// eventually は cond が真になるまで、上限つきで待つ（状態の変化が通知を伴わないとき）。
func codexEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	ctx := codexCtx(t)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// install はエンジンを取得し、終わるまで待つ。
func (e *codexEnv) install() {
	e.t.Helper()
	if err := e.svc.Install(codexCtx(e.t)); err != nil {
		e.t.Fatal(err)
	}
	codexEventually(e.t, "the install to end", func() bool { return e.svc.Status().Engine.State != CodexEngineInstalling })
	if st := e.svc.Status(); st.Engine.State != CodexEngineInstalled {
		e.t.Fatalf("status = %+v", st)
	}
	if tt := e.toast(); tt.Level != "info" { // 取得の完了のトースト（状態より後に出る）
		e.t.Fatalf("install toast = %+v", tt)
	}
}

func (e *codexEnv) events() []string { return e.audit.Events() }

func codexAccount() *protocol.Account {
	return &protocol.Account{Type: "chatgpt", Email: "someone@example.invalid", PlanType: "business"}
}

func codexConflict(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domain.NewError(domain.CodeConflict, "", nil)) {
		t.Fatalf("err = %v, want a conflict", err)
	}
}

// --- テスト ---

func TestCodexNewCodexAuthは必須の設定がないとき失敗する(t *testing.T) {
	e := codexEnvNew(t)
	good := e.config()
	for name, mut := range map[string]func(*CodexAuthConfig){
		"Settings": func(c *CodexAuthConfig) { c.Settings = nil },
		"Browser":  func(c *CodexAuthConfig) { c.Browser = nil },
		"Clock":    func(c *CodexAuthConfig) { c.Clock = nil },
		"DataDir":  func(c *CodexAuthConfig) { c.DataDir = "" },
	} {
		cfg := good
		mut(&cfg)
		if _, err := NewCodexAuth(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCodexAuthはサポート外の環境でunsupportedになる(t *testing.T) {
	e := codexEnvNew(t, func(_ *codexEnv, c *CodexAuthConfig) { c.GOOS = "linux" })
	st := e.svc.Status()
	if st.Engine.State != CodexEngineUnsupported || st.State != PlanSignedOut {
		t.Fatalf("status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledCodexUnsupported {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	codexConflict(t, e.svc.Install(codexCtx(t)))
	codexConflict(t, e.svc.StartSignIn(codexCtx(t)))
	if e.svc.Model() == nil {
		t.Fatal("Model is nil")
	}
	// エンジンの置き場所が渡されない環境も取得しない。
	e2 := codexEnvNew(t, func(_ *codexEnv, c *CodexAuthConfig) { c.EngineDir = "" })
	if st := e2.svc.Status(); st.Engine.State != CodexEngineUnsupported {
		t.Fatalf("no engine dir: %+v", st)
	}
}

func TestCodexAuthはエンジンが無いときengine_missingでログインを断る(t *testing.T) {
	e := codexEnvNew(t)
	st := e.svc.Status()
	if st.Engine.State != CodexEngineMissing || st.Engine.Version != "rust-v0.0.1" {
		t.Fatalf("status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledCodexEngineMissing {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	codexConflict(t, e.svc.StartSignIn(codexCtx(t)))
	if e.started.Load() != 0 {
		t.Fatal("a process was started")
	}
}

func TestCodexInstallはエンジンを取得して検証し監査とトーストを残す(t *testing.T) {
	e := codexEnvNew(t)
	e.install()
	if _, err := os.Stat(filepath.Join(e.engDir, "engine", codexTestExe)); err != nil {
		t.Fatal(err)
	}
	if got := e.events(); len(got) != 1 || got[0] != "agent.codex.install" {
		t.Fatalf("events = %v", got)
	}
	ent := e.audit.Entries()[0]
	if ent.Kind != "agent" || ent.Result != auditOK || ent.ActorType != domain.ActorHost {
		t.Errorf("entry = %+v", ent)
	}
	// 取得のあと、状態を読む（app-server を起動する）。未ログインなので not_signed_in。
	codexEventually(t, "the account state", func() bool { r, _ := e.svc.Ready(); return r == domain.AgentDisabledCodexNotSignedIn })
	if st := e.svc.Status(); st.State != PlanSignedOut || st.Engine.Progress != 0 {
		t.Fatalf("status = %+v", st)
	}
	// 2 回目は取り直しで上書きできる。
	e.install()
}

func TestCodexInstallは取得中の重複を断り取り消しで元に戻す(t *testing.T) {
	e := codexEnvNew(t)
	e.gate = make(chan struct{})
	if err := e.svc.Install(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	if st := e.svc.Status(); st.Engine.State != CodexEngineInstalling {
		t.Fatalf("status = %+v", st)
	}
	codexConflict(t, e.svc.Install(codexCtx(t)))
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledCodexEngineMissing {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	e.svc.CancelInstall()
	e.svc.CancelInstall() // 冪等
	codexEventually(t, "the install to stop", func() bool { return e.svc.Status().Engine.State == CodexEngineMissing })
	close(e.gate)
	codexEventually(t, "the cancel audit", func() bool { return len(e.events()) == 1 })
	ent := e.audit.Entries()[0]
	if ent.Event != "agent.codex.install" || ent.Result != auditDenied || !strings.Contains(string(ent.DetailJSON), "cancelled") {
		t.Fatalf("entry = %+v %s", ent, ent.DetailJSON)
	}
	if st := e.svc.Status(); st.Engine.Error != "" {
		t.Fatalf("a cancel is not a failure: %+v", st)
	}
	if e.started.Load() != 0 {
		t.Fatal("a process was started")
	}
}

func TestCodexInstallはハッシュの不一致をfailedにして理由からURLを外す(t *testing.T) {
	e := codexEnvNew(t)
	// 固定したハッシュ・サイズは元のアーカイブのまま、サーバーが配るものだけを変える。
	e.archive = append(append([]byte(nil), e.archive...), 0)
	svc := e.svc
	if err := svc.Install(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	if tt := e.toast(); tt.Level != "error" {
		t.Fatalf("toast = %+v", tt)
	}
	st := svc.Status()
	if st.Engine.State != CodexEngineFailed || st.Engine.Error == "" || strings.Contains(st.Engine.Error, e.srv.URL) {
		t.Fatalf("status = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(e.engDir, "engine")); err == nil {
		t.Fatal("an unverified engine was left")
	}
	ent := e.audit.Entries()[0]
	if ent.Result != auditError || !strings.Contains(string(ent.DetailJSON), "mismatch") {
		t.Fatalf("entry = %+v %s", ent, ent.DetailJSON)
	}
	if reason, ok := svc.Ready(); ok || reason != domain.AgentDisabledCodexEngineMissing {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
}

func TestCodexStartSignInはブラウザでauthUrlを開き完了通知でconnectedになる(t *testing.T) {
	e := codexEnvNew(t)
	e.install()
	codexEventually(t, "the account state", func() bool { r, _ := e.svc.Ready(); return r == domain.AgentDisabledCodexNotSignedIn })
	if err := e.svc.StartSignIn(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	if urls := e.browser.URLs(); len(urls) != 1 || urls[0] != e.authURL {
		t.Fatalf("urls = %v", urls)
	}
	if st := e.svc.Status(); st.State != PlanSigningIn {
		t.Fatalf("status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledCodexSigningIn {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	codexConflict(t, e.svc.StartSignIn(codexCtx(t)))

	e.setAccount(codexAccount())
	e.lastFake().Notify(protocol.NotifLoginCompleted, map[string]any{"success": true, "loginId": "login-1"})
	if tt := e.toast(); tt.Level != "info" {
		t.Fatalf("toast = %+v", tt)
	}
	st := e.svc.Status()
	if st.State != PlanConnected || st.AccountEmail != "someone@example.invalid" || st.PlanType != "business" || st.Problem != PlanProblemNone {
		t.Fatalf("status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); !ok || reason != "" {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	events := e.events()
	if len(events) < 3 || events[len(events)-2] != "agent.codex.signin_start" || events[len(events)-1] != "agent.codex.signin" {
		t.Fatalf("events = %v", events)
	}
	for _, ent := range e.audit.Entries() {
		if strings.Contains(string(ent.DetailJSON), "example.invalid") || strings.Contains(ent.ErrorCode, "example") {
			t.Fatalf("the audit entry holds the account: %+v", ent)
		}
	}
	// 接続済みではログインを始められない。
	codexConflict(t, e.svc.StartSignIn(codexCtx(t)))
}

func TestCodexStartSignInはブラウザを開けないとき取り消してsigned_outのままにする(t *testing.T) {
	e := codexEnvNew(t)
	e.install()
	e.browser.err = errors.New("no browser")
	err := e.svc.StartSignIn(codexCtx(t))
	if err == nil || strings.Contains(err.Error(), e.authURL) {
		t.Fatalf("err = %v", err)
	}
	if st := e.svc.Status(); st.State != PlanSignedOut || st.Problem != PlanProblemFailed {
		t.Fatalf("status = %+v", st)
	}
	if n := len(e.lastFake().WaitFor(codexCtx(t), protocol.MethodLoginCancel, 1)); n != 1 {
		t.Fatalf("account/login/cancel calls = %d", n)
	}
	// 直せば、もう一度始められる。
	e.browser.err = nil
	if err := e.svc.StartSignIn(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCodexStartSignInはhttpsでないauthUrlを開かない(t *testing.T) {
	e := codexEnvNew(t)
	e.authURL = "http://insecure.example.invalid/login"
	e.install()
	if err := e.svc.StartSignIn(codexCtx(t)); err == nil {
		t.Fatal("an insecure url was accepted")
	}
	if len(e.browser.URLs()) != 0 {
		t.Fatal("the browser was opened")
	}
	if st := e.svc.Status(); st.State != PlanSignedOut {
		t.Fatalf("status = %+v", st)
	}
}

func TestCodexログインがaccessDeniedで終わるとProblemになりsigned_outへ戻る(t *testing.T) {
	e := codexEnvNew(t)
	e.install()
	if err := e.svc.StartSignIn(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	e.lastFake().Notify(protocol.NotifLoginCompleted, map[string]any{"success": false, "loginId": "login-1", "error": "access_denied: the user said no"})
	if tt := e.toast(); tt.Level != "error" {
		t.Fatalf("toast = %+v", tt)
	}
	st := e.svc.Status()
	if st.State != PlanSignedOut || st.Problem != PlanProblemAccessDenied {
		t.Fatalf("status = %+v", st)
	}
	ents := e.audit.Entries()
	last := ents[len(ents)-1]
	if last.Event != "agent.codex.signin" || last.Result != auditDenied || !strings.Contains(string(last.DetailJSON), "access_denied") {
		t.Fatalf("entry = %+v %s", last, last.DetailJSON)
	}
}

func TestCodexログインがタイムアウトするとtimeoutのProblemでsigned_outへ戻る(t *testing.T) {
	e := codexEnvNew(t, func(_ *codexEnv, c *CodexAuthConfig) { c.SignInTimeout = 20 * time.Millisecond })
	e.install()
	if err := e.svc.StartSignIn(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	if tt := e.toast(); tt.Level != "error" {
		t.Fatalf("toast = %+v", tt)
	}
	st := e.svc.Status()
	if st.State != PlanSignedOut || st.Problem != PlanProblemTimeout {
		t.Fatalf("status = %+v", st)
	}
	if n := len(e.lastFake().WaitFor(codexCtx(t), protocol.MethodLoginCancel, 1)); n != 1 {
		t.Fatalf("account/login/cancel calls = %d", n)
	}
}

func TestCodexCancelSignInはログインを取り消す(t *testing.T) {
	e := codexEnvNew(t)
	e.install()
	e.svc.CancelSignIn(codexCtx(t)) // 進行中でなければ何もしない
	if err := e.svc.StartSignIn(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	e.svc.CancelSignIn(codexCtx(t))
	if st := e.svc.Status(); st.State != PlanSignedOut || st.Problem != PlanProblemNone {
		t.Fatalf("status = %+v", st)
	}
	calls := e.lastFake().WaitFor(codexCtx(t), protocol.MethodLoginCancel, 1)
	var p protocol.CancelLoginParams
	if len(calls) != 1 || json.Unmarshal(calls[0].Params, &p) != nil || p.LoginID != "login-1" {
		t.Fatalf("cancel calls = %+v", calls)
	}
	ents := e.audit.Entries()
	if last := ents[len(ents)-1]; last.Event != "agent.codex.signin" || last.Result != auditDenied {
		t.Fatalf("entry = %+v", last)
	}
	// 取り消したあとの完了通知は無視する。
	e.lastFake().Notify(protocol.NotifLoginCompleted, map[string]any{"success": true, "loginId": "login-1"})
	if st := e.svc.Status(); st.State != PlanSignedOut {
		t.Fatalf("status = %+v", st)
	}
}

// codexSignedIn はエンジンを取得し、ログイン済みの状態にする。
func codexSignedIn(t *testing.T, opts ...codexOpt) *codexEnv {
	t.Helper()
	e := codexEnvNew(t, opts...)
	e.account = codexAccount()
	e.install()
	codexEventually(t, "connected", func() bool { _, ok := e.svc.Ready(); return ok })
	return e
}

func TestCodexSignOutはタスクを中断しログアウトして監査を残す(t *testing.T) {
	e := codexSignedIn(t)
	e.cancelID = "task-1"
	if err := e.svc.SignOut(codexCtx(t), false); err != nil {
		t.Fatal(err)
	}
	if len(e.lastFake().Calls(protocol.MethodLogout)) != 1 {
		t.Fatal("account/logout was not sent")
	}
	st := e.svc.Status()
	if st.State != PlanSignedOut || st.AccountEmail != "" || st.PlanType != "" || st.Engine.State != CodexEngineInstalled {
		t.Fatalf("status = %+v", st)
	}
	if len(e.cancelled) != 1 || e.cancelled[0] != "codex_signout" {
		t.Fatalf("cancelled = %v", e.cancelled)
	}
	ents := e.audit.Entries()
	last := ents[len(ents)-1]
	if last.Event != "agent.codex.signout" || last.Result != auditOK || !strings.Contains(string(last.DetailJSON), "task-1") {
		t.Fatalf("entry = %+v %s", last, last.DetailJSON)
	}
	codexConflict(t, e.svc.SignOut(codexCtx(t), false)) // ログインしていない
}

func TestCodexSignOutのremoveEngineはエンジンと専用のCODEX_HOMEを消す(t *testing.T) {
	e := codexSignedIn(t)
	home := filepath.Join(e.dataDir, "codex-home")
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("codex-home was not created: %v", err)
	}
	proc := e.procs[0]
	if err := e.svc.SignOut(codexCtx(t), true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.Done():
	default:
		t.Fatal("the process did not end")
	}
	for _, p := range []string{home, filepath.Join(e.dataDir, "codex-work"), e.engDir} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was not removed", p)
		}
	}
	st := e.svc.Status()
	if st.State != PlanSignedOut || st.Engine.State != CodexEngineMissing {
		t.Fatalf("status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledCodexEngineMissing {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	// エンジンだけの削除は、ログインしていなくてもできる。
	e.install()
	if err := e.svc.SignOut(codexCtx(t), true); err != nil {
		t.Fatal(err)
	}
	if st := e.svc.Status(); st.Engine.State != CodexEngineMissing {
		t.Fatalf("status = %+v", st)
	}
}

func TestCodexSignOutのremoveEngineはログアウトに失敗しても削除を続けdetailに残す(t *testing.T) {
	e := codexSignedIn(t)
	e.mu.Lock()
	e.lastFakeLocked().Handlers[protocol.MethodLogout] = func(*codextest.Fake, codextest.Message) (any, *rpc.Error) {
		return nil, &rpc.Error{Code: -32603, Message: "keyring is locked"}
	}
	e.mu.Unlock()
	if err := e.svc.SignOut(codexCtx(t), true); err != nil {
		t.Fatal(err)
	}
	if st := e.svc.Status(); st.Engine.State != CodexEngineMissing || st.State != PlanSignedOut {
		t.Fatalf("status = %+v", st)
	}
	ents := e.audit.Entries()
	if last := ents[len(ents)-1]; last.Event != "agent.codex.signout" || last.Result != auditOK ||
		!strings.Contains(string(last.DetailJSON), "logout_failed") || strings.Contains(string(last.DetailJSON), "keyring") {
		t.Fatalf("entry = %+v %s", last, last.DetailJSON)
	}
}

func (e *codexEnv) lastFakeLocked() *codextest.Fake { return e.fakes[len(e.fakes)-1] }

func TestCodexSignOutはログアウトに失敗したら削除なしのとき状態もエンジンも変えない(t *testing.T) {
	e := codexSignedIn(t)
	e.mu.Lock()
	e.lastFakeLocked().Handlers[protocol.MethodLogout] = func(*codextest.Fake, codextest.Message) (any, *rpc.Error) {
		return nil, &rpc.Error{Code: -32603, Message: "keyring is locked"}
	}
	e.mu.Unlock()
	err := e.svc.SignOut(codexCtx(t), false)
	if err == nil || strings.Contains(err.Error(), "keyring") {
		t.Fatalf("a failed logout must be an error without the cause: %v", err)
	}
	if st := e.svc.Status(); st.State != PlanConnected || st.Engine.State != CodexEngineInstalled {
		t.Fatalf("status = %+v", st)
	}
	ents := e.audit.Entries()
	if last := ents[len(ents)-1]; last.Event != "agent.codex.signout" || last.Result != auditError {
		t.Fatalf("entry = %+v", last)
	}
}

func TestCodexMarkUsageLimitedとResume(t *testing.T) {
	e := codexSignedIn(t)
	codexConflict(t, e.svc.Resume(codexCtx(t)))
	e.svc.MarkUsageLimited()
	if tt := e.toast(); tt.Level != "error" {
		t.Fatalf("toast = %+v", tt)
	}
	if st := e.svc.Status(); st.State != PlanUsageLimited {
		t.Fatalf("status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledCodexUsageLimited {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	if err := e.svc.Resume(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.svc.Ready(); !ok {
		t.Fatal("not ready after Resume")
	}
	if got := e.events(); got[len(got)-1] != "agent.codex.resume" {
		t.Fatalf("events = %v", got)
	}
	// 使用量の上限は再起動で消える（メモリだけ）: 新しいサービスは connected から始まる。
	e.svc.MarkUsageLimited()
	e.settings.mu.Lock()
	e.settings.values[SettingAgentConnection] = json.RawMessage(`"chatgpt-codex"`)
	e.settings.mu.Unlock()
	svc2 := e.newService(e.config())
	codexEventually(t, "the restart state", func() bool { _, ok := svc2.Ready(); return ok })
}

func TestCodexReportAuthFailureはログインが無ければreauth_requiredにし再ログインで戻る(t *testing.T) {
	e := codexSignedIn(t)
	e.setAccount(nil) // app-server 側でログインが無効になった
	e.svc.ReportAuthFailure(codexCtx(t))
	if tt := e.toast(); tt.Level != "error" {
		t.Fatalf("toast = %+v", tt)
	}
	if st := e.svc.Status(); st.State != PlanReauthRequired {
		t.Fatalf("status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledCodexReauthRequired {
		t.Fatalf("Ready = %q, %v", reason, ok)
	}
	if err := e.svc.StartSignIn(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	e.setAccount(codexAccount())
	e.lastFake().Notify(protocol.NotifLoginCompleted, map[string]any{"success": true, "loginId": "login-1"})
	if tt := e.toast(); tt.Level != "info" {
		t.Fatalf("toast = %+v", tt)
	}
	if _, ok := e.svc.Ready(); !ok {
		t.Fatal("not ready after the new sign-in")
	}
}

func TestCodexReportAuthFailureはログインが有効なら状態を変えない(t *testing.T) {
	e := codexSignedIn(t)
	e.svc.ReportAuthFailure(codexCtx(t))
	if st := e.svc.Status(); st.State != PlanConnected {
		t.Fatalf("status = %+v", st)
	}
	e.svc.MarkPolicyDenied() // Codex の経路には当てはまらない
	if st := e.svc.Status(); st.State != PlanConnected {
		t.Fatalf("status = %+v", st)
	}
}

func TestCodexアカウントのログアウトNotificationでreauth_requiredになる(t *testing.T) {
	e := codexSignedIn(t)
	e.setAccount(nil)
	e.lastFake().Notify(protocol.NotifAccountUpdated, map[string]any{"authMode": nil})
	if tt := e.toast(); tt.Level != "error" {
		t.Fatalf("toast = %+v", tt)
	}
	if st := e.svc.Status(); st.State != PlanReauthRequired {
		t.Fatalf("status = %+v", st)
	}
}

func TestCodexModelsは接続済みのときだけ隠しモデルを除いて返す(t *testing.T) {
	e := codexEnvNew(t)
	e.models = []protocol.Model{
		{ID: "a", Model: "gpt-a", DisplayName: "GPT A"},
		{ID: "b", Model: "gpt-b", Hidden: true},
		{ID: "c", Model: "gpt-c"},
	}
	e.account = codexAccount()
	_, err := e.svc.Models(codexCtx(t))
	codexConflict(t, err)
	e.install()
	codexEventually(t, "connected", func() bool { _, ok := e.svc.Ready(); return ok })
	got, err := e.svc.Models(codexCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (PlanModel{Slug: "gpt-a", DisplayName: "GPT A"}) || got[1] != (PlanModel{Slug: "gpt-c", DisplayName: "gpt-c"}) {
		t.Fatalf("models = %+v", got)
	}
}

func TestCodexModelはサービスのapp_serverでタスクを実行しログインが無ければErrPlanUnauthorized(t *testing.T) {
	e := codexSignedIn(t, func(e *codexEnv, _ *CodexAuthConfig) {
		e.script = func(f *codextest.Fake) {
			f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
				f.AgentMessage(th, turn, "完了")
				f.CompleteTurn(th, turn, "completed", "")
			}
		}
	})
	h := openai.NewHistory("やって", "", []byte("png"), 3)
	out, err := e.svc.Model().Step(codexCtx(t), openai.StepInput{Model: "m", History: h})
	if err != nil || out.FinalText != "完了" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	e.svc.Model().(openai.TaskCloser).CloseTask(h)

	e.setAccount(nil)
	h2 := openai.NewHistory("やって", "", nil, 3)
	if _, err := e.svc.Model().Step(codexCtx(t), openai.StepInput{Model: "m", History: h2}); !errors.Is(err, openai.ErrPlanUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexModelはエンジンが無いときErrEngineUnavailableを返す(t *testing.T) {
	e := codexEnvNew(t)
	h := openai.NewHistory("やって", "", nil, 3)
	if _, err := e.svc.Model().Step(codexCtx(t), openai.StepInput{Model: "m", History: h}); !errors.Is(err, openai.ErrEngineUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if e.started.Load() != 0 {
		t.Fatal("a process was started")
	}
}

func TestCodexAuthは起動時に接続方式がcodexのときだけ状態を読む(t *testing.T) {
	e := codexSignedIn(t)
	before := e.started.Load()
	// 接続方式が chatgpt（未設定）: 起動しても app-server は起動しない。
	svc := e.newService(e.config())
	if e.started.Load() != before {
		t.Fatal("a process was started at boot for another connection")
	}
	if st := svc.Status(); st.Engine.State != CodexEngineInstalled {
		t.Fatalf("status = %+v", st)
	}
	// 接続方式が chatgpt-codex: 背景で状態を読み、再ログインなしで connected になる。
	e.settings.mu.Lock()
	e.settings.values[SettingAgentConnection] = json.RawMessage(`"chatgpt-codex"`)
	e.settings.mu.Unlock()
	svc2 := e.newService(e.config())
	codexEventually(t, "the boot refresh", func() bool { _, ok := svc2.Ready(); return ok })
	if st := svc2.Status(); st.State != PlanConnected || st.AccountEmail == "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestCodexCloseはapp_serverを終わらせる(t *testing.T) {
	e := codexSignedIn(t)
	e.mu.Lock()
	proc := e.procs[0]
	e.mu.Unlock()
	if err := e.svc.Close(codexCtx(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.Done():
	default:
		t.Fatal("the process did not end")
	}
	// Close のあとは、状態の読み直しも起動もしない。
	e.svc.Status()
	if e.started.Load() != 1 {
		t.Fatalf("started = %d", e.started.Load())
	}
}
