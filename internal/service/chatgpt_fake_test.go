package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// このファイルは ChatGPT の認可・接続のテストの共通部品: 偽の auth / api サーバー（httptest）と、偽の SecretStore・ブラウザ・時計・設定を
// 束ねた環境。実際の OpenAI・ChatGPT は呼ばない（NFR-O06）。値は明らかな偽物で、テストの出力に出ない。

var (
	chatgptTestKey      = sync.OnceValue(func() *rsa.PrivateKey { return mustRSA() })
	chatgptTestOtherKey = sync.OnceValue(func() *rsa.PrivateKey { return mustRSA() })
)

func mustRSA() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}

const (
	testChatGPTClientID = "client-issued-1"
	testChatGPTSub      = "sub-account-1"
	testChatGPTEmail    = "user@example.com"
	testChatGPTScope    = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

// fakeChatGPTServer は auth.openai.com（探索・JWKS・トークン・失効）と api.openai.com（models）の偽物。
type fakeChatGPTServer struct {
	srv *httptest.Server
	now func() time.Time

	mu       sync.Mutex
	seq      int
	issued   []string // 発行したトークン（出力に出ていないことの確認用）
	clientID string
	sub      string
	email    string
	scope    string
	nonce    string

	claimsMod  func(map[string]any) // ID トークンのクレームを変える
	signKey    *rsa.PrivateKey      // nil なら正規の鍵
	noIDToken  bool
	exchange   func() (int, string) // nil なら成功
	refresh    func() (int, string) // nil なら成功
	revokeSeq  []int                // 失効の応答の列（使い切ったら 200）
	revokeHost string               // 探索が返す revocation_endpoint の基底（空は自分）
	apiStatus  int                  // 0 なら 200
	apiBody    string
	jwksKid    string
	headerKid  string // 空でなければ ID トークンのヘッダーの kid

	exchangeForms []url.Values
	refreshForms  []url.Values
	revokeForms   []url.Values
	apiAuth       []string
	refreshSeen   chan struct{} // 更新の要求が来るたびに送る（容量 16）
	refreshGate   chan struct{} // 非 nil なら、更新の応答はこれが閉じるまで待つ
}

func newFakeChatGPTServer(t *testing.T, now func() time.Time) *fakeChatGPTServer {
	f := &fakeChatGPTServer{
		now: now, clientID: testChatGPTClientID, sub: testChatGPTSub, email: testChatGPTEmail, scope: testChatGPTScope,
		jwksKid: "k1", refreshSeen: make(chan struct{}, 16),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", f.handleJWKS)
	mux.HandleFunc("GET /.well-known/openid-configuration", f.handleDiscovery)
	mux.HandleFunc("POST /api/accounts/oauth/token", f.handleToken)
	mux.HandleFunc("POST /api/accounts/oauth/revoke", f.handleRevoke)
	mux.HandleFunc("GET /v1/models", f.handleModels)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeChatGPTServer) url() string { return f.srv.URL }

func (f *fakeChatGPTServer) set(fn func(*fakeChatGPTServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeChatGPTServer) b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (f *fakeChatGPTServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	kid := f.jwksKid
	f.mu.Unlock()
	pub := chatgptTestKey().PublicKey
	e := f.b64(big.NewInt(int64(pub.E)).Bytes())
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": f.b64(pub.N.Bytes()), "e": e,
	}}})
}

func (f *fakeChatGPTServer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	base := f.revokeHost
	f.mu.Unlock()
	if base == "" {
		base = f.srv.URL
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"revocation_endpoint": base + "/api/accounts/oauth/revoke"})
}

func (f *fakeChatGPTServer) newToken(prefix string) string {
	f.seq++
	tok := fmt.Sprintf("%s-fake-%d", prefix, f.seq)
	f.issued = append(f.issued, tok)
	return tok
}

// signJWT は RS256 の JWT を作る。
func (f *fakeChatGPTServer) signJWT(claims map[string]any) string {
	key := f.signKey
	if key == nil {
		key = chatgptTestKey()
	}
	kid := f.jwksKid
	if f.headerKid != "" {
		kid = f.headerKid
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	body, _ := json.Marshal(claims)
	signing := f.b64(hdr) + "." + f.b64(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	jwt := signing + "." + f.b64(sig)
	f.issued = append(f.issued, jwt)
	return jwt
}

func (f *fakeChatGPTServer) idToken() string {
	claims := map[string]any{
		"iss": f.srv.URL, "aud": f.clientID, "sub": f.sub, "email": f.email, "nonce": f.nonce,
		"exp": f.now().Add(time.Hour).Unix(), "iat": f.now().Unix(),
	}
	if f.claimsMod != nil {
		f.claimsMod(claims)
	}
	return f.signJWT(claims)
}

func (f *fakeChatGPTServer) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	form := r.PostForm
	f.mu.Lock()
	var hook func() (int, string)
	var gate chan struct{}
	switch form.Get("grant_type") {
	case "authorization_code":
		f.exchangeForms = append(f.exchangeForms, form)
		hook = f.exchange
	case "refresh_token":
		f.refreshForms = append(f.refreshForms, form)
		hook, gate = f.refresh, f.refreshGate
	}
	f.mu.Unlock()
	if form.Get("grant_type") == "refresh_token" {
		select {
		case f.refreshSeen <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if hook != nil {
		if status, body := hook(); status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
	}
	resp := map[string]any{
		"access_token": f.newToken("at"), "refresh_token": f.newToken("rt"), "token_type": "Bearer",
		"expires_in": 3600, "scope": f.scope,
	}
	if form.Get("grant_type") == "authorization_code" && !f.noIDToken {
		resp["id_token"] = f.idToken()
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeChatGPTServer) handleRevoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.revokeForms = append(f.revokeForms, r.PostForm)
	status := http.StatusOK
	if len(f.revokeSeq) > 0 {
		status, f.revokeSeq = f.revokeSeq[0], f.revokeSeq[1:]
	}
	f.mu.Unlock()
	w.WriteHeader(status)
}

func (f *fakeChatGPTServer) handleModels(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
	status, body := f.apiStatus, f.apiBody
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
	}
	if body == "" {
		body = `{"models":[{"slug":"m-a","display_name":"Model A","visibility":"list"},` +
			`{"slug":"m-hidden","display_name":"Hidden","visibility":"hide"},{"slug":"m-b","display_name":"Model B","visibility":"list"}]}`
	}
	_, _ = w.Write([]byte(body))
}

func (f *fakeChatGPTServer) snapshot() (exchange, refresh, revoke []url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.exchangeForms...), append([]url.Values(nil), f.refreshForms...), append([]url.Values(nil), f.revokeForms...)
}

func (f *fakeChatGPTServer) issuedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issued...)
}

// ---- 環境 ----

type chatgptEnvOpts struct {
	seed     bool // 起動時に、有効なトークンと client_id・sub・メールが保存済みの状態にする
	mod      func(*ChatGPTAuthConfig)
	noListen bool // Listen を偽物に差し替えない（Ports を使うテスト用）
}

type chatgptEnv struct {
	t        *testing.T
	svc      *ChatGPTAuthService
	fake     *fakeChatGPTServer
	settings *ctlSettings
	secrets  *platformtest.FakeSecretStore
	browser  *platformtest.FakeBrowserOpener
	audit    *ctlAudit
	clock    *ctlClock
	bus      *Bus
	toasts   <-chan Event
	logs     *syncBuffer

	mu        sync.Mutex
	requested []string // Listen に渡された addr
	sleeps    []time.Duration
	cancelled []string
}

func newChatGPTEnv(t *testing.T, o chatgptEnvOpts) *chatgptEnv {
	t.Helper()
	e := &chatgptEnv{
		t: t, settings: newCtlSettings(), secrets: platformtest.NewFakeSecretStore(), browser: platformtest.NewFakeBrowserOpener(),
		audit: &ctlAudit{}, clock: newCtlClock(), bus: NewBus(), logs: &syncBuffer{},
	}
	e.browser.AllowHTTP() // 偽の認可サーバーは http://127.0.0.1:…
	e.fake = newFakeChatGPTServer(t, e.clock.Now)
	e.toasts, _ = e.bus.Subscribe(TopicToast)
	if o.seed {
		e.seedTokens(chatgptTokenRecord{
			AccessToken: "at-seed", RefreshToken: "rt-seed", IDToken: "idt-seed",
			ExpiresAt: e.clock.Now().Add(time.Hour), Scope: testChatGPTScope,
		})
		e.settings.put(SettingChatGPTClientID, `"`+testChatGPTClientID+`"`)
		e.settings.put(SettingChatGPTAccountSub, `"`+testChatGPTSub+`"`)
		e.settings.put(SettingChatGPTAccountEmail, `"`+testChatGPTEmail+`"`)
		e.settings.put(SettingChatGPTHostID, `"urn:uuid:11111111-1111-4111-8111-111111111111"`)
	}
	e.build(o)
	t.Cleanup(e.svc.Close)
	return e
}

func (e *chatgptEnv) seedTokens(rec chatgptTokenRecord) {
	e.t.Helper()
	if err := (chatgptTokenStore{secrets: e.secrets}).save(rec); err != nil {
		e.t.Fatal(err)
	}
}

func (e *chatgptEnv) build(o chatgptEnvOpts) {
	e.t.Helper()
	cfg := ChatGPTAuthConfig{
		Settings: e.settings, Secrets: e.secrets, Browser: e.browser, Audit: e.audit, Bus: e.bus, Clock: e.clock,
		Logger:      slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		AuthBaseURL: e.fake.url(), APIBaseURL: e.fake.url() + "/v1",
		CancelTasks: func(reason string) string {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.cancelled = append(e.cancelled, reason)
			return "task-1"
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.sleeps = append(e.sleeps, d)
			return nil
		},
	}
	if !o.noListen {
		cfg.Listen = func(network, addr string) (net.Listener, error) {
			e.mu.Lock()
			e.requested = append(e.requested, addr)
			e.mu.Unlock()
			return net.Listen(network, "127.0.0.1:0")
		}
	}
	if o.mod != nil {
		o.mod(&cfg)
	}
	svc, err := NewChatGPTAuth(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.svc = svc
}

// restart は同じ設定・資格情報で、サービスを作り直す（アプリの再起動）。
func (e *chatgptEnv) restart() {
	e.t.Helper()
	e.svc.Close()
	e.build(chatgptEnvOpts{})
	e.t.Cleanup(e.svc.Close)
}

func (e *chatgptEnv) waitToast() Toast {
	e.t.Helper()
	select {
	case ev := <-e.toasts:
		return ev.Data.(Toast)
	case <-time.After(15 * time.Second):
		e.t.Fatal("no toast within 15s")
		return Toast{}
	}
}

// chatgptAuthRequest はブラウザへ渡された認可の URL を分解したもの。
type chatgptAuthRequest struct {
	raw      string
	q        url.Values
	redirect string
	state    string
	nonce    string
	port     string
}

// startSignIn はサインインを始め、ブラウザが開いた認可の URL を返す。
func (e *chatgptEnv) startSignIn() chatgptAuthRequest {
	e.t.Helper()
	if err := e.svc.StartSignIn(context.Background()); err != nil {
		e.t.Fatalf("StartSignIn: %v", err)
	}
	return e.lastAuthRequest()
}

func (e *chatgptEnv) lastAuthRequest() chatgptAuthRequest {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := e.browser.Next(ctx)
	if err != nil {
		e.t.Fatalf("the browser was not opened: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	if !strings.HasPrefix(raw, e.fake.url()+"/api/accounts/authorize?") {
		e.t.Fatalf("the authorize url does not point at the authorization server's authorize endpoint")
	}
	q := u.Query()
	ru, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		e.t.Fatal(err)
	}
	return chatgptAuthRequest{raw: raw, q: q, redirect: q.Get("redirect_uri"), state: q.Get("state"), nonce: q.Get("nonce"), port: ru.Port()}
}

// callback はブラウザの戻りとして受信口へ GET する。host が空でなければ Host ヘッダーを差し替える。
func (e *chatgptEnv) callback(a chatgptAuthRequest, q url.Values, host string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, a.redirect+"?"+q.Encode(), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("callback request: %v", err)
	}
	e.t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// okQuery は正しい結果のクエリ（新規登録なら client_id つき）。
func (e *chatgptEnv) okQuery(a chatgptAuthRequest) url.Values {
	q := url.Values{"code": {"code-for-test-1"}, "state": {a.state}, "scope": {testChatGPTScope}}
	if a.q.Get("client_id") == chatgptDynamicClientID {
		q.Set("client_id", testChatGPTClientID)
	}
	return q
}

// deliver は a の nonce を偽サーバーに覚えさせ、正しい結果を渡す。
func (e *chatgptEnv) deliver(a chatgptAuthRequest) *http.Response {
	e.t.Helper()
	e.fake.set(func(f *fakeChatGPTServer) { f.nonce = a.nonce })
	return e.callback(a, e.okQuery(a), "")
}

// signInOK は新規登録のサインインを最後まで通す。
func (e *chatgptEnv) signInOK() {
	e.t.Helper()
	a := e.startSignIn()
	if res := e.deliver(a); res.StatusCode != http.StatusOK {
		e.t.Fatalf("callback status = %d", res.StatusCode)
	}
	if tst := e.waitToast(); tst.Level != "info" {
		e.t.Fatalf("sign-in did not succeed: %v", tst)
	}
}

func (e *chatgptEnv) recordedSleeps() []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Duration(nil), e.sleeps...)
}

// allOutput は、運用ログ・監査・通知の全文（秘密が出ていないことの確認用）。
func (e *chatgptEnv) allOutput() string {
	var b strings.Builder
	b.WriteString(e.logs.String())
	for _, en := range e.audit.Entries() {
		raw, _ := json.Marshal(en)
		b.Write(raw)
	}
	for {
		select {
		case ev := <-e.toasts:
			fmt.Fprint(&b, ev.Data)
		default:
			return b.String()
		}
	}
}

// requireNoSecrets は、発行したトークンと既知の秘密の値が、運用ログ・監査・通知・extra に出ていないことを確かめる。
func (e *chatgptEnv) requireNoSecrets(extra ...string) {
	e.t.Helper()
	out := e.allOutput() + strings.Join(extra, "\n")
	secrets := append(e.fake.issuedTokens(), "at-seed", "rt-seed", "idt-seed", "code-for-test-1", testChatGPTEmail, testChatGPTSub)
	for _, s := range secrets {
		if strings.Contains(out, s) {
			e.t.Errorf("a secret value appears in the output (len %d)", len(s))
		}
	}
}

func (e *chatgptEnv) hasSecret(name string) bool {
	_, err := e.secrets.Get(name)
	return err == nil
}

func (e *chatgptEnv) setting(key string) string {
	v, err := e.svc.getSetting(context.Background(), key)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// テスト内の短い別名。
const platformSecretChatGPT = "puppet-remo/chatgpt"

var errPlanUnauthorized = openai.ErrPlanUnauthorized
