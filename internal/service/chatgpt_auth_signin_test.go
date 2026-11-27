package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func requireClosedPort(t *testing.T, port string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), time.Second)
		if err != nil {
			return
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("the callback listener is still open")
		}
		time.Sleep(10 * time.Millisecond) // 閉じるまでの上限つきの条件待ち（受信口の Shutdown の完了）
	}
}

func TestChatGPTSignInSuccessReachesConnected(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledChatGPTNotSignedIn {
		t.Fatalf("Ready before = %q, %v", reason, ok)
	}
	a := e.startSignIn()

	// 認可の要求（FR-G28 (3)）。
	want := map[string]string{
		"client_id": "dynamic_agent_client", "agent_name_hint": "puppet-remo", "response_type": "code",
		"scope":    "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct",
		"resource": "https://api.openai.com/v1", "code_challenge_method": "S256",
	}
	for k, v := range want {
		if got := a.q.Get(k); got != v {
			t.Errorf("authorize %s = %q, want %q", k, got, v)
		}
	}
	if !strings.HasPrefix(a.q.Get("ext_agent_host_id"), "urn:uuid:") {
		t.Errorf("ext_agent_host_id = %q", a.q.Get("ext_agent_host_id"))
	}
	if !strings.HasPrefix(a.redirect, "http://127.0.0.1:") || !strings.HasSuffix(a.redirect, "/auth/callback") {
		t.Errorf("redirect_uri = %q", a.redirect)
	}
	if a.state == "" || a.nonce == "" || a.q.Get("code_challenge") == "" || a.state == a.nonce {
		t.Errorf("state, nonce and code_challenge must be set freshly")
	}
	for _, k := range []string{"login_hint", "prompt", "id_token_hint"} {
		if a.q.Has(k) {
			t.Errorf("authorize has %s on the first sign-in", k)
		}
	}
	if got := e.svc.Status(); got.State != PlanSigningIn {
		t.Fatalf("state while signing in = %v", got.State)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledChatGPTSigningIn {
		t.Errorf("Ready while signing in = %q, %v", reason, ok)
	}
	hostID := e.setting(SettingChatGPTHostID)
	if hostID != a.q.Get("ext_agent_host_id") {
		t.Errorf("host_id is not saved before the sign-in")
	}

	res := e.deliver(a)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", res.StatusCode)
	}
	if tst := e.waitToast(); tst.Level != "info" {
		t.Fatalf("toast = %+v, want info", tst)
	}
	st := e.svc.Status()
	if st.State != PlanConnected || st.AccountEmail != testChatGPTEmail || !st.WelcomePending || st.Problem != PlanProblemNone {
		t.Fatalf("Status = %+v", st)
	}
	if _, ok := e.svc.Ready(); !ok {
		t.Error("Ready after sign-in = false")
	}
	if e.setting(SettingChatGPTClientID) != testChatGPTClientID || e.setting(SettingChatGPTAccountSub) != testChatGPTSub ||
		e.setting(SettingChatGPTAccountEmail) != testChatGPTEmail || e.setting(SettingChatGPTHostID) != hostID {
		t.Error("the identifiers are not saved")
	}
	rec, found, err := (chatgptTokenStore{secrets: e.secrets}).load()
	if err != nil || !found || rec.AccessToken == "" || rec.RefreshToken == "" || rec.IDToken == "" || rec.Scope != testChatGPTScope {
		t.Fatalf("saved record = found %v, err %v", found, err)
	}
	if !rec.ExpiresAt.Equal(e.clock.Now().Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v", rec.ExpiresAt)
	}

	// コードの交換（FR-G28 (5)）: PKCE の verifier は challenge と対で、redirect_uri は同じ値。
	ex, _, _ := e.fake.snapshot()
	if len(ex) != 1 {
		t.Fatalf("exchange requests = %d", len(ex))
	}
	f := ex[0]
	if f.Get("grant_type") != "authorization_code" || f.Get("client_id") != testChatGPTClientID || f.Get("code") != "code-for-test-1" ||
		f.Get("redirect_uri") != a.redirect || f.Get("resource") != "https://api.openai.com/v1" || f.Has("client_secret") {
		t.Errorf("exchange form is wrong (keys %v)", keysOf(f))
	}
	if pkceChallenge(f.Get("code_verifier")) != a.q.Get("code_challenge") {
		t.Error("code_verifier does not match code_challenge")
	}
	requireClosedPort(t, a.port)

	// 監査（FR-AU01）。
	events := e.audit.Events()
	if len(events) != 2 || events[0] != "agent.chatgpt.signin_start" || events[1] != "agent.chatgpt.signin" {
		t.Fatalf("audit events = %v", events)
	}
	if got := e.audit.Entries()[1]; got.Result != auditOK || !strings.Contains(string(got.DetailJSON), `"new_registration":true`) {
		t.Errorf("signin audit = %+v", got)
	}
	e.requireNoSecrets(a.state, a.nonce, f.Get("code_verifier"))
	for _, s := range []string{a.state, a.nonce, f.Get("code_verifier"), testChatGPTClientID} {
		if strings.Contains(e.allOutput(), s) {
			t.Errorf("a sign-in value appears in the output")
		}
	}

	// 初回の確認（FR-G28 (7)）。
	if err := e.svc.DismissWelcome(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.svc.Status().WelcomePending {
		t.Error("WelcomePending after DismissWelcome")
	}
	e.restart()
	if e.svc.Status().WelcomePending {
		t.Error("the welcome came back after a restart")
	}
}

func keysOf(v url.Values) []string {
	var out []string
	for k := range v {
		out = append(out, k)
	}
	return out
}

func TestChatGPTSignInReauthorizationUsesIssuedClientAndKeepsHostID(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	e.signInOK()
	hostID := e.setting(SettingChatGPTHostID)
	if _, err := e.svc.SignOut(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	a := e.startSignIn()
	if a.q.Get("client_id") != testChatGPTClientID || a.q.Has("agent_name_hint") || a.q.Get("login_hint") != testChatGPTEmail ||
		a.q.Get("ext_agent_host_id") != hostID {
		t.Errorf("the second authorize request is wrong: client_id=%q hint=%v", a.q.Get("client_id"), a.q.Has("agent_name_hint"))
	}
	// 再認可の戻りには client_id が無くてよい。
	e.fake.set(func(f *fakeChatGPTServer) { f.nonce = a.nonce })
	q := e.okQuery(a)
	if q.Has("client_id") {
		t.Fatal("test setup: a reauthorization callback must not carry client_id")
	}
	if res := e.callback(a, q, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", res.StatusCode)
	}
	if tst := e.waitToast(); tst.Level != "info" {
		t.Fatalf("toast = %+v", tst)
	}
	if e.svc.Status().State != PlanConnected {
		t.Error("not connected after the reauthorization")
	}
	if got := e.audit.Entries(); !strings.Contains(string(got[len(got)-1].DetailJSON), `"new_registration":false`) {
		t.Errorf("last audit = %+v", got[len(got)-1])
	}
}

func TestChatGPTSignInStateMismatchGets400AndKeepsWaiting(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	a := e.startSignIn()

	bad := []url.Values{
		{"code": {"code-for-test-1"}, "state": {"not-the-state"}},
		{"code": {"code-for-test-1"}},
		{"error": {"access_denied"}, "state": {"not-the-state"}},
	}
	for _, q := range bad {
		res := e.callback(a, q, "")
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("callback with %v: status = %d, want 400", keysOf(q), res.StatusCode)
		}
	}
	if e.svc.Status().State != PlanSigningIn {
		t.Fatal("a bad request ended the sign-in")
	}
	if ex, _, _ := e.fake.snapshot(); len(ex) != 0 {
		t.Fatal("a bad request reached the token endpoint")
	}
	// 正しい state の要求で、そのまま完了する。
	if res := e.deliver(a); res.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", res.StatusCode)
	}
	if tst := e.waitToast(); tst.Level != "info" {
		t.Fatalf("toast = %+v", tst)
	}
}

func TestChatGPTSignInHostMismatchIsRejected(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	a := e.startSignIn()
	e.fake.set(func(f *fakeChatGPTServer) { f.nonce = a.nonce })

	for _, host := range []string{"localhost:" + a.port, "127.0.0.1:1", "evil.example"} {
		if res := e.callback(a, e.okQuery(a), host); res.StatusCode != http.StatusBadRequest {
			t.Errorf("Host %q: status = %d, want 400", host, res.StatusCode)
		}
	}
	if e.svc.Status().State != PlanSigningIn {
		t.Fatal("a request with another Host ended the sign-in")
	}
	if res := e.callback(a, e.okQuery(a), ""); res.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", res.StatusCode)
	}
	e.waitToast()
	if e.svc.Status().State != PlanConnected {
		t.Error("not connected")
	}
}

func TestChatGPTSignInFailures(t *testing.T) {
	tests := []struct {
		name       string
		prep       func(e *chatgptEnv)
		query      func(e *chatgptEnv, a chatgptAuthRequest) url.Values
		wantProb   PlanProblem
		wantResult string
		wantReason string
		exchanged  bool // トークンの交換まで進む
	}{
		{
			name: "同意されなかった（access_denied）",
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				return url.Values{"error": {"access_denied"}, "state": {a.state}, "error_description": {"no"}}
			},
			wantProb: PlanProblemAccessDenied, wantResult: auditDenied, wantReason: "access_denied",
		},
		{
			name: "ワークスペースのポリシーで拒否",
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				return url.Values{"error": {"3p_delegated_access_policy_denied"}, "state": {a.state}}
			},
			wantProb: PlanProblemPolicyDenied, wantResult: auditDenied, wantReason: "policy_denied",
		},
		{
			name: "資格がない（subscription_sharing_user_not_eligible）",
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				return url.Values{"error": {"subscription_sharing_user_not_eligible"}, "state": {a.state}}
			},
			wantProb: PlanProblemPolicyDenied, wantResult: auditDenied, wantReason: "policy_denied",
		},
		{
			name: "ポリシー拒否の識別子が error_description に来る（error は access_denied）",
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				return url.Values{"error": {"access_denied"}, "error_description": {"3p_delegated_access_policy_denied: workspace policy"}, "state": {a.state}}
			},
			wantProb: PlanProblemPolicyDenied, wantResult: auditDenied, wantReason: "policy_denied",
		},
		{
			name: "ポリシー拒否の識別子が error_description だけに来る（error なし・code なし）",
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				return url.Values{"error_description": {"subscription_sharing_user_not_eligible"}, "state": {a.state}}
			},
			wantProb: PlanProblemPolicyDenied, wantResult: auditDenied, wantReason: "policy_denied",
		},
		{
			name: "その他の error",
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				return url.Values{"error": {"server_error"}, "state": {a.state}}
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "authorization_failed",
		},
		{
			name: "新規登録なのに client_id が発行されない",
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				q := e.okQuery(a)
				q.Del("client_id")
				return q
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "registration_incomplete",
		},
		{
			name: "再認可で別の client_id が返る",
			prep: func(e *chatgptEnv) { e.settings.put(SettingChatGPTClientID, `"`+testChatGPTClientID+`"`) },
			query: func(e *chatgptEnv, a chatgptAuthRequest) url.Values {
				q := e.okQuery(a)
				q.Set("client_id", "client-other")
				return q
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "registration_incomplete",
		},
		{
			name: "コードの交換が invalid_grant",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) {
					f.exchange = func() (int, string) { return 400, `{"error":"invalid_grant"}` }
				})
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "token_exchange_failed", exchanged: true,
		},
		{
			name:     "ID トークンが無い",
			prep:     func(e *chatgptEnv) { e.fake.set(func(f *fakeChatGPTServer) { f.noIDToken = true }) },
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name:     "ID トークンの署名が違う",
			prep:     func(e *chatgptEnv) { e.fake.set(func(f *fakeChatGPTServer) { f.signKey = chatgptTestOtherKey() }) },
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "ID トークンの kid が JWKS に無い",
			// 署名は正規の鍵だが、ヘッダーの kid は JWKS にない値。
			prep:     func(e *chatgptEnv) { e.fake.set(func(f *fakeChatGPTServer) { f.headerKid = "k-unknown" }) },
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "ID トークンの aud が違う",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) { f.claimsMod = func(c map[string]any) { c["aud"] = "client-other" } })
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "ID トークンの iss が違う",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) { f.claimsMod = func(c map[string]any) { c["iss"] = "https://evil.example" } })
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "ID トークンの nonce が違う",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) { f.claimsMod = func(c map[string]any) { c["nonce"] = "another-nonce" } })
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "ID トークンの nonce が無い",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) { f.claimsMod = func(c map[string]any) { delete(c, "nonce") } })
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "ID トークンの期限切れ",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) {
					f.claimsMod = func(c map[string]any) { c["exp"] = e.clock.Now().Add(-time.Hour).Unix() }
				})
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "ID トークンに exp が無い",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) { f.claimsMod = func(c map[string]any) { delete(c, "exp") } })
			},
			wantProb: PlanProblemFailed, wantResult: auditError, wantReason: "id_token_invalid", exchanged: true,
		},
		{
			name: "sub が保存済みと違う",
			prep: func(e *chatgptEnv) {
				e.settings.put(SettingChatGPTClientID, `"`+testChatGPTClientID+`"`)
				e.settings.put(SettingChatGPTAccountSub, `"sub-another-account"`)
			},
			wantProb: PlanProblemAccountMismatch, wantResult: auditError, wantReason: "account_mismatch", exchanged: true,
		},
		{
			name: "chatgpt.tokens.use.direct が付与されない",
			prep: func(e *chatgptEnv) {
				e.fake.set(func(f *fakeChatGPTServer) { f.scope = "openid profile email offline_access resource.invoke" })
			},
			wantProb: PlanProblemScopeMissing, wantResult: auditError, wantReason: "plan_use_not_granted", exchanged: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newChatGPTEnv(t, chatgptEnvOpts{})
			if tc.prep != nil {
				tc.prep(e)
			}
			a := e.startSignIn()
			e.fake.set(func(f *fakeChatGPTServer) { f.nonce = a.nonce })
			q := e.okQuery(a)
			if tc.query != nil {
				q = tc.query(e, a)
			}
			if res := e.callback(a, q, ""); res.StatusCode != http.StatusOK {
				t.Fatalf("callback status = %d", res.StatusCode)
			}
			if tst := e.waitToast(); tst.Level != "error" {
				t.Fatalf("toast = %+v, want error", tst)
			}
			st := e.svc.Status()
			if st.State != PlanSignedOut || st.Problem != tc.wantProb || st.AccountEmail != "" {
				t.Fatalf("Status = %+v, want signed_out with %v", st, tc.wantProb)
			}
			if e.hasSecret(platformSecretChatGPT) {
				t.Error("tokens were saved")
			}
			if ex, _, _ := e.fake.snapshot(); (len(ex) > 0) != tc.exchanged {
				t.Errorf("token endpoint reached = %v, want %v", len(ex) > 0, tc.exchanged)
			}
			ents := e.audit.Entries()
			last := ents[len(ents)-1]
			if last.Event != "agent.chatgpt.signin" || last.Result != tc.wantResult || !strings.Contains(string(last.DetailJSON), `"reason":"`+tc.wantReason+`"`) {
				t.Errorf("audit = %s %s %s", last.Event, last.Result, last.DetailJSON)
			}
			if tc.wantProb == PlanProblemAccountMismatch && e.setting(SettingChatGPTAccountSub) != "sub-another-account" {
				t.Error("the saved sub was replaced")
			}
			requireClosedPort(t, a.port)
			e.requireNoSecrets(a.state, a.nonce)
		})
	}
}

func TestChatGPTSignInScopeMissingKeepsClientAndSubThenAsksConsent(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	e.fake.set(func(f *fakeChatGPTServer) { f.scope = "openid profile email offline_access resource.invoke" })
	a := e.startSignIn()
	e.deliver(a)
	e.waitToast()
	if e.setting(SettingChatGPTClientID) != testChatGPTClientID || e.setting(SettingChatGPTAccountSub) != testChatGPTSub {
		t.Error("client_id and sub were not kept")
	}
	if e.setting(SettingChatGPTAccountEmail) != "" {
		t.Error("the email was saved without the plan being usable")
	}
	if e.svc.Status().Problem != PlanProblemScopeMissing {
		t.Fatal("Problem is not scope_missing")
	}

	// もう一度許可する: prompt=consent つき、発行済みの client_id で。
	e.fake.set(func(f *fakeChatGPTServer) { f.scope = testChatGPTScope })
	a2 := e.startSignIn()
	if a2.q.Get("prompt") != "consent" || a2.q.Get("client_id") != testChatGPTClientID || a2.q.Has("agent_name_hint") {
		t.Errorf("retry request: prompt=%q client_id=%q", a2.q.Get("prompt"), a2.q.Get("client_id"))
	}
	e.deliver(a2)
	if tst := e.waitToast(); tst.Level != "info" {
		t.Fatalf("toast = %+v", tst)
	}
	if e.svc.Status().State != PlanConnected {
		t.Error("not connected after the retry")
	}
}

func TestChatGPTSignInTimeout(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{mod: func(c *ChatGPTAuthConfig) { c.SignInTimeout = 30 * time.Millisecond }})
	a := e.startSignIn()
	if tst := e.waitToast(); tst.Level != "error" {
		t.Fatalf("toast = %+v", tst)
	}
	if st := e.svc.Status(); st.State != PlanSignedOut || st.Problem != PlanProblemTimeout {
		t.Fatalf("Status = %+v", st)
	}
	requireClosedPort(t, a.port)
	last := e.audit.Entries()[len(e.audit.Entries())-1]
	if last.Result != auditError || !strings.Contains(string(last.DetailJSON), `"reason":"timeout"`) {
		t.Errorf("audit = %+v", last)
	}
}

func TestChatGPTSignInCancel(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	e.svc.CancelSignIn() // 進行中でなければ何もしない
	if len(e.audit.Entries()) != 0 {
		t.Fatal("CancelSignIn without a sign-in recorded an audit entry")
	}
	a := e.startSignIn()
	e.svc.CancelSignIn()
	e.svc.CancelSignIn() // 冪等
	if st := e.svc.Status(); st.State != PlanSignedOut || st.Problem != PlanProblemNone {
		t.Fatalf("Status = %+v", st)
	}
	requireClosedPort(t, a.port)
	ents := e.audit.Entries()
	if len(ents) != 2 || ents[1].Event != "agent.chatgpt.signin" || ents[1].Result != auditDenied || !strings.Contains(string(ents[1].DetailJSON), `"reason":"cancelled"`) {
		t.Fatalf("audit = %+v", ents)
	}
	// 中止のあとにもう一度始められる。
	e.startSignIn()
}

func TestChatGPTSignInConflictAndBrowserFailure(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	e.browser.SetError("OpenURL", errors.New("no browser"))
	err := e.svc.StartSignIn(context.Background())
	if err == nil || strings.Contains(err.Error(), "authorize") || strings.Contains(err.Error(), "state") {
		t.Fatalf("StartSignIn with a failing browser = %v", err)
	}
	if st := e.svc.Status(); st.State != PlanSignedOut {
		t.Fatalf("state after a browser failure = %v", st.State)
	}
	e.mu.Lock()
	n := len(e.requested)
	e.mu.Unlock()
	if n != 1 {
		t.Fatalf("listen requests = %d", n)
	}
	if ents := e.audit.Entries(); len(ents) != 0 {
		t.Errorf("a failed start was audited: %v", e.audit.Events())
	}

	e.browser.SetError("OpenURL", nil)
	e.startSignIn()
	for name, fn := range map[string]func() error{
		"signing_in への開始": func() error { return e.svc.StartSignIn(context.Background()) },
		"Resume":          func() error { return e.svc.Resume(context.Background()) },
		"Models":          func() error { _, err := e.svc.Models(context.Background()); return err },
	} {
		if err := fn(); err == nil || domain.CodeOf(err) != domain.CodeConflict {
			t.Errorf("%s = %v, want conflict", name, err)
		}
	}
	e.svc.CancelSignIn()

	connected := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	if err := connected.svc.StartSignIn(context.Background()); err == nil || domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("StartSignIn when connected = %v, want conflict", err)
	}
}

func TestChatGPTSignInPortFallback(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{mod: func(c *ChatGPTAuthConfig) {
		c.Listen = func(network, addr string) (net.Listener, error) {
			if addr == "127.0.0.1:1455" {
				return nil, errors.New("address already in use")
			}
			return net.Listen(network, "127.0.0.1:0")
		}
	}})
	var asked []string
	inner := e.svc.cfg.Listen
	e.svc.cfg.Listen = func(network, addr string) (net.Listener, error) {
		asked = append(asked, addr)
		return inner(network, addr)
	}
	a := e.startSignIn()
	if len(asked) != 2 || asked[0] != "127.0.0.1:1455" || asked[1] != "127.0.0.1:1457" {
		t.Fatalf("listen order = %v, want 1455 then 1457", asked)
	}
	if !strings.HasPrefix(a.redirect, "http://127.0.0.1:") {
		t.Errorf("redirect_uri = %q", a.redirect)
	}
	e.svc.CancelSignIn()

	// どちらも使えなければ、空きポート（0）。
	asked = nil
	e.svc.cfg.Listen = func(network, addr string) (net.Listener, error) {
		asked = append(asked, addr)
		if addr != "127.0.0.1:0" {
			return nil, errors.New("address already in use")
		}
		return net.Listen(network, addr)
	}
	e.startSignIn()
	if len(asked) != 3 || asked[2] != "127.0.0.1:0" {
		t.Fatalf("listen order = %v", asked)
	}
	e.svc.CancelSignIn()

	// 全部だめなら、始める前の状態のまま失敗する。
	e.svc.cfg.Listen = func(string, string) (net.Listener, error) { return nil, errors.New("address already in use") }
	if err := e.svc.StartSignIn(context.Background()); err == nil {
		t.Fatal("StartSignIn without any port succeeded")
	}
	if e.svc.Status().State != PlanSignedOut {
		t.Error("state changed after the listen failure")
	}
}

func TestChatGPTCloseStopsSignIn(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	a := e.startSignIn()
	e.svc.Close()
	e.svc.Close() // 冪等
	requireClosedPort(t, a.port)
	if e.svc.Status().State == PlanSigningIn {
		t.Error("still signing in after Close")
	}
	if err := e.svc.StartSignIn(context.Background()); err == nil {
		t.Error("StartSignIn after Close succeeded")
	}
}

func TestChatGPTSignInFromReauthRequired(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.fake.set(func(f *fakeChatGPTServer) {
		f.refresh = func() (int, string) { return 400, `{"error":"invalid_grant"}` }
	})
	e.clock.Advance(59 * time.Minute)
	if _, err := e.svc.AccessToken(context.Background()); !errors.Is(err, errPlanUnauthorized) {
		t.Fatalf("AccessToken = %v", err)
	}
	if e.svc.Status().State != PlanReauthRequired {
		t.Fatal("not reauth_required")
	}
	if tst := e.waitToast(); tst.Level != "error" { // 認可切れの通知
		t.Fatalf("toast = %+v", tst)
	}
	a := e.startSignIn() // reauth_required から始められる
	if a.q.Get("client_id") != testChatGPTClientID {
		t.Errorf("client_id = %q, want the issued one", a.q.Get("client_id"))
	}
	e.deliver(a)
	if tst := e.waitToast(); tst.Level != "info" {
		t.Fatalf("toast = %+v", tst)
	}
	if e.svc.Status().State != PlanConnected {
		t.Error("not connected")
	}
}

// 受信口のレスポンス（API 仕様 §7.6）。
func TestChatGPTCallbackResponses(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	a := e.startSignIn()
	e.fake.set(func(f *fakeChatGPTServer) { f.nonce = a.nonce })

	check := func(res *http.Response, status int) {
		t.Helper()
		if res.StatusCode != status {
			t.Errorf("status = %d, want %d", res.StatusCode, status)
		}
		h := res.Header
		if h.Get("Cache-Control") != "no-store" || h.Get("Content-Security-Policy") != "default-src 'none'" ||
			h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("headers = %v", h)
		}
	}
	// 400（不一致）・404（別のパス）・405（別のメソッド）。
	check(e.callback(a, url.Values{"state": {"x"}, "code": {"c"}}, ""), http.StatusBadRequest)
	other := a
	other.redirect = strings.TrimSuffix(a.redirect, "/auth/callback") + "/elsewhere"
	check(e.callback(other, e.okQuery(a), ""), http.StatusNotFound)
	req, _ := http.NewRequest(http.MethodPost, a.redirect+"?"+e.okQuery(a).Encode(), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	check(res, http.StatusMethodNotAllowed)
	if e.svc.Status().State != PlanSigningIn {
		t.Fatal("a non-callback request ended the sign-in")
	}
	// 最初の 1 回は静的な HTML で、コード・state を含まない。
	ok := e.callback(a, e.okQuery(a), "")
	check(ok, http.StatusOK)
	buf := make([]byte, 4096)
	n, _ := ok.Body.Read(buf)
	body := string(buf[:n])
	if !strings.Contains(body, "このタブを閉じてください") || strings.Contains(body, "code-for-test-1") || strings.Contains(body, a.state) ||
		strings.Contains(body, "http://") || strings.Contains(body, "https://") || strings.Contains(body, "src=") {
		t.Errorf("body = %q", body)
	}
	e.waitToast()
}

// 実際のソケットで、先頭のポートが使用中なら次の候補へ進む（既定の 1455 → 1457 → 空きと同じ規則）。
func TestChatGPTSignInSkipsABusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()

	e := newChatGPTEnv(t, chatgptEnvOpts{noListen: true, mod: func(c *ChatGPTAuthConfig) {
		c.Ports = []int{busy.Addr().(*net.TCPAddr).Port, freePort, 0}
	}})
	a := e.startSignIn()
	if a.port != strconv.Itoa(freePort) {
		t.Fatalf("callback port = %s, want the second candidate %d", a.port, freePort)
	}
	e.svc.CancelSignIn()
}
