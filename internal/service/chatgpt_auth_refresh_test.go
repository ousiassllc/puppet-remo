package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

func TestChatGPTAccessTokenNoRefreshWhenFresh(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	tok, err := e.svc.AccessToken(context.Background())
	if err != nil || tok != "at-seed" {
		t.Fatalf("AccessToken = %q, %v", tok, err)
	}
	e.clock.Advance(54 * time.Minute) // 残り 6 分
	if tok, _ := e.svc.AccessToken(context.Background()); tok != "at-seed" {
		t.Fatal("refreshed with 6 minutes left")
	}
	if _, rf, _ := e.fake.snapshot(); len(rf) != 0 {
		t.Fatalf("refresh requests = %d", len(rf))
	}
}

func TestChatGPTRefreshReplacesTokensTogether(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.clock.Advance(56 * time.Minute) // 残り 4 分
	tok, err := e.svc.AccessToken(context.Background())
	if err != nil || tok == "at-seed" || !strings.HasPrefix(tok, "at-fake-") {
		t.Fatalf("AccessToken = %q, %v", tok, err)
	}
	_, rf, _ := e.fake.snapshot()
	if len(rf) != 1 {
		t.Fatalf("refresh requests = %d", len(rf))
	}
	f := rf[0]
	if f.Get("grant_type") != "refresh_token" || f.Get("client_id") != testChatGPTClientID || f.Get("refresh_token") != "rt-seed" ||
		f.Get("resource") != "https://api.openai.com/v1" || f.Has("scope") {
		t.Errorf("refresh form keys = %v", keysOf(f))
	}
	rec, found, _ := (chatgptTokenStore{secrets: e.secrets}).load()
	if !found || rec.AccessToken != tok || rec.RefreshToken == "rt-seed" || !strings.HasPrefix(rec.RefreshToken, "rt-fake-") ||
		rec.IDToken != "idt-seed" || rec.Scope != testChatGPTScope || !rec.ExpiresAt.Equal(e.clock.Now().Add(time.Hour)) {
		t.Errorf("saved record is not the rotated set: found=%v", found)
	}
	// 再起動しても回転後のトークン。
	e.restart()
	if tok2, err := e.svc.AccessToken(context.Background()); err != nil || tok2 != tok {
		t.Errorf("after restart AccessToken = %q, %v", tok2, err)
	}
	if _, rf, _ := e.fake.snapshot(); len(rf) != 1 {
		t.Errorf("refresh requests after restart = %d", len(rf))
	}
	e.requireNoSecrets()
}

func TestChatGPTRefreshIsExclusive(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	gate := make(chan struct{})
	e.fake.set(func(f *fakeChatGPTServer) { f.refreshGate = gate })
	e.clock.Advance(58 * time.Minute)

	var wg sync.WaitGroup
	toks := make([]string, 2)
	errs := make([]error, 2)
	for i := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			toks[i], errs[i] = e.svc.AccessToken(context.Background())
		}()
	}
	// 最初の更新が偽サーバーに届いたことを確かめてから、2 つ目が排他で待つ間に応答を通す。
	select {
	case <-e.fake.refreshSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("no refresh request")
	}
	close(gate)
	wg.Wait()
	for i := range toks {
		if errs[i] != nil || toks[i] == "" || toks[i] == "at-seed" {
			t.Fatalf("call %d: %q, %v", i, toks[i], errs[i])
		}
	}
	if toks[0] != toks[1] {
		t.Error("the two callers got different tokens")
	}
	if _, rf, _ := e.fake.snapshot(); len(rf) != 1 {
		t.Fatalf("refresh requests = %d, want exactly 1", len(rf))
	}
}

func TestChatGPTRefreshDeadTokenDeletesEverythingAndRequiresReauth(t *testing.T) {
	for _, code := range []string{"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused"} {
		t.Run(code, func(t *testing.T) {
			e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
			e.fake.set(func(f *fakeChatGPTServer) {
				f.refresh = func() (int, string) { return 400, `{"error":"` + code + `"}` }
			})
			e.clock.Advance(58 * time.Minute)
			_, err := e.svc.AccessToken(context.Background())
			if !errors.Is(err, openai.ErrPlanUnauthorized) {
				t.Fatalf("AccessToken = %v, want ErrPlanUnauthorized", err)
			}
			if st := e.svc.Status(); st.State != PlanReauthRequired {
				t.Fatalf("Status = %+v", st)
			}
			if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledChatGPTReauthRequired {
				t.Errorf("Ready = %q, %v", reason, ok)
			}
			for _, name := range []string{platformSecretChatGPT, chatgptPartName(1)} {
				if e.hasSecret(name) {
					t.Errorf("%s remains", name)
				}
			}
			if e.setting(SettingChatGPTClientID) != testChatGPTClientID {
				t.Error("client_id must be kept for invalid_grant")
			}
			if tst := e.waitToast(); tst.Level != "error" {
				t.Errorf("toast = %+v", tst)
			}
			last := e.audit.Entries()[len(e.audit.Entries())-1]
			if last.Event != "agent.chatgpt.reauth" || last.ErrorCode != code || !strings.Contains(string(last.DetailJSON), `"source":"refresh"`) {
				t.Errorf("audit = %+v", last)
			}
			// 削除後は更新を試みない。
			if _, err := e.svc.AccessToken(context.Background()); !errors.Is(err, openai.ErrPlanUnauthorized) {
				t.Errorf("AccessToken after = %v", err)
			}
			if _, rf, _ := e.fake.snapshot(); len(rf) != 1 {
				t.Errorf("refresh requests = %d", len(rf))
			}
			e.requireNoSecrets()
		})
	}
}

func TestChatGPTRefreshInvalidClientDropsClientID(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.fake.set(func(f *fakeChatGPTServer) {
		f.refresh = func() (int, string) { return 401, `{"error":"invalid_client"}` }
	})
	e.clock.Advance(58 * time.Minute)
	if _, err := e.svc.AccessToken(context.Background()); !errors.Is(err, openai.ErrPlanUnauthorized) {
		t.Fatalf("AccessToken = %v", err)
	}
	if e.svc.Status().State != PlanReauthRequired || e.hasSecret(platformSecretChatGPT) {
		t.Error("not reauth_required with the tokens removed")
	}
	if e.setting(SettingChatGPTClientID) != "" {
		t.Error("client_id was kept for invalid_client")
	}
	if e.setting(SettingChatGPTAccountSub) != testChatGPTSub {
		t.Error("sub must be kept")
	}
	// 次のサインインは新規登録。
	e.waitToast()
	if a := e.startSignIn(); a.q.Get("client_id") != chatgptDynamicClientID || a.q.Get("agent_name_hint") != "puppet-remo" {
		t.Error("the next sign-in is not a new registration")
	}
}

func TestChatGPTRefreshTransientFailuresKeepTokens(t *testing.T) {
	tests := map[string]func() (int, string){
		"500":             func() (int, string) { return 500, `{"error":"server_error"}` },
		"503":             func() (int, string) { return 503, `` },
		"429":             func() (int, string) { return 429, `{"error":"rate_limited"}` },
		"400 の別のコード":      func() (int, string) { return 400, `{"error":"invalid_request"}` },
		"アクセストークンの無い 200": func() (int, string) { return 200, `{}` },
	}
	for name, hook := range tests {
		t.Run(name, func(t *testing.T) {
			e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
			e.fake.set(func(f *fakeChatGPTServer) { f.refresh = hook })
			e.clock.Advance(58 * time.Minute)
			_, err := e.svc.AccessToken(context.Background())
			if err == nil || errors.Is(err, openai.ErrPlanUnauthorized) {
				t.Fatalf("AccessToken = %v, want a plain failure", err)
			}
			if st := e.svc.Status(); st.State != PlanConnected {
				t.Fatalf("state = %v, want connected", st.State)
			}
			if rec, found, _ := (chatgptTokenStore{secrets: e.secrets}).load(); !found || rec.RefreshToken != "rt-seed" {
				t.Error("the saved tokens changed")
			}
			if _, rf, _ := e.fake.snapshot(); len(rf) != 1 {
				t.Errorf("refresh requests = %d, want 1 (no automatic retry)", len(rf))
			}
			e.requireNoSecrets()
		})
	}
}

func TestChatGPTRefreshNetworkFailureKeepsTokens(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true, mod: func(c *ChatGPTAuthConfig) {
		c.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		})}
	}})
	e.clock.Advance(58 * time.Minute)
	_, err := e.svc.AccessToken(context.Background())
	if err == nil || errors.Is(err, openai.ErrPlanUnauthorized) || strings.Contains(err.Error(), "http") {
		t.Fatalf("AccessToken = %v", err)
	}
	if e.svc.Status().State != PlanConnected || !e.hasSecret(platformSecretChatGPT) {
		t.Error("a network failure changed the state or removed the tokens")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChatGPTReportAuthFailureRefreshesOnce(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.svc.ReportAuthFailure(context.Background())
	if st := e.svc.Status(); st.State != PlanConnected {
		t.Fatalf("state = %v", st.State)
	}
	_, rf, _ := e.fake.snapshot()
	if len(rf) != 1 {
		t.Fatalf("refresh requests = %d, want 1 even though the token is fresh", len(rf))
	}
	if tok, _ := e.svc.AccessToken(context.Background()); tok == "at-seed" {
		t.Error("the token was not replaced")
	}

	// 使えなければ reauth_required（監査の source は request）。
	e.fake.set(func(f *fakeChatGPTServer) {
		f.refresh = func() (int, string) { return 400, `{"error":"invalid_grant"}` }
	})
	e.svc.ReportAuthFailure(context.Background())
	if e.svc.Status().State != PlanReauthRequired || e.hasSecret(platformSecretChatGPT) {
		t.Fatal("not reauth_required with the tokens removed")
	}
	last := e.audit.Entries()[len(e.audit.Entries())-1]
	if last.Event != "agent.chatgpt.reauth" || !strings.Contains(string(last.DetailJSON), `"source":"request"`) {
		t.Errorf("audit = %+v", last)
	}
	e.svc.ReportAuthFailure(context.Background()) // もう無い。何もしない
}

// 401 を見たトークンが、待っている間に別の呼び出しの更新で置き換わっていれば、重ねて更新しない。
func TestChatGPTReportAuthFailureSkipsWhenAlreadyRefreshed(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.clock.Advance(58 * time.Minute)
	if _, err := e.svc.AccessToken(context.Background()); err != nil { // at-seed → 新しいトークン
		t.Fatal(err)
	}
	rec, err := e.svc.refresh(context.Background(), "request", func(r chatgptTokenRecord) bool { return r.AccessToken == "at-seed" })
	if err != nil || rec.AccessToken == "at-seed" {
		t.Fatalf("refresh = %v, %v", rec.AccessToken != "at-seed", err)
	}
	if _, rf, _ := e.fake.snapshot(); len(rf) != 1 {
		t.Fatalf("refresh requests = %d, want 1", len(rf))
	}
}

func TestChatGPTUsageLimitAndResume(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.svc.MarkUsageLimited()
	if st := e.svc.Status(); st.State != PlanUsageLimited {
		t.Fatalf("state = %v", st.State)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledChatGPTUsageLimited {
		t.Errorf("Ready = %q, %v", reason, ok)
	}
	if tst := e.waitToast(); tst.Level != "error" {
		t.Errorf("toast = %+v", tst)
	}
	// メモリだけ: 再起動で connected。
	e.restart()
	if e.svc.Status().State != PlanConnected {
		t.Fatal("usage_limited survived a restart")
	}
	e.svc.MarkUsageLimited()
	if err := e.svc.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.svc.Status().State != PlanConnected {
		t.Error("not connected after Resume")
	}
	if last := e.audit.Entries()[len(e.audit.Entries())-1]; last.Event != "agent.chatgpt.resume" {
		t.Errorf("audit = %+v", last)
	}
	if err := e.svc.Resume(context.Background()); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("Resume when connected = %v, want conflict", err)
	}
	// 上限のままでも解除はできる。
	e.svc.MarkUsageLimited()
	if _, err := e.svc.SignOut(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// signed_out では usage_limited にならない。
	e.svc.MarkUsageLimited()
	if e.svc.Status().State != PlanSignedOut {
		t.Error("MarkUsageLimited changed a signed out state")
	}
}

func TestChatGPTPolicyDeniedKeepsState(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.svc.MarkPolicyDenied()
	if st := e.svc.Status(); st.State != PlanConnected || st.Problem != PlanProblemPolicyDenied {
		t.Fatalf("Status = %+v", st)
	}
}

func TestChatGPTModels(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	ms, err := e.svc.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0] != (PlanModel{"m-a", "Model A"}) || ms[1] != (PlanModel{"m-b", "Model B"}) {
		t.Fatalf("Models = %+v, want the visibility=list ones in server order", ms)
	}
	if len(e.fake.apiAuth) != 1 || e.fake.apiAuth[0] != "Bearer at-seed" {
		t.Errorf("Authorization = %v", e.fake.apiAuth)
	}
	// 更新が要るときは先に更新して、新しい Bearer で呼ぶ。
	e.clock.Advance(58 * time.Minute)
	if _, err := e.svc.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, rf, _ := e.fake.snapshot(); len(rf) != 1 {
		t.Errorf("refresh requests = %d", len(rf))
	}
	if got := e.fake.apiAuth[1]; got == "Bearer at-seed" || !strings.HasPrefix(got, "Bearer at-fake-") {
		t.Errorf("second Authorization is not the refreshed token")
	}
	// 失敗は本文・トークンを含まない。
	e.fake.set(func(f *fakeChatGPTServer) { f.apiStatus, f.apiBody = 500, `{"error":"boom","detail":"SECRET-BODY"}` })
	if _, err := e.svc.Models(context.Background()); err == nil || strings.Contains(err.Error(), "SECRET-BODY") {
		t.Errorf("Models on 500 = %v", err)
	}
	// 401 は更新して認可切れの分類。
	e.fake.set(func(f *fakeChatGPTServer) { f.apiStatus, f.apiBody = 401, `{}` })
	if _, err := e.svc.Models(context.Background()); !errors.Is(err, openai.ErrPlanUnauthorized) {
		t.Errorf("Models on 401 = %v", err)
	}
	// ポリシー拒否。
	e.fake.set(func(f *fakeChatGPTServer) {
		f.apiStatus, f.apiBody = 403, `{"error":{"code":"subscription_sharing_user_not_eligible"}}`
	})
	if _, err := e.svc.Models(context.Background()); !errors.Is(err, openai.ErrPolicyDenied) {
		t.Errorf("Models on 403 = %v", err)
	}
	if e.svc.Status().Problem != PlanProblemPolicyDenied {
		t.Error("Problem is not policy_denied")
	}
	e.requireNoSecrets()
}

func TestChatGPTOpenUsageIsFixedURL(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	if err := e.svc.OpenUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.browser.Opened(); len(got) != 1 || got[0] != "https://chatgpt.com/settings/usage" {
		t.Fatalf("opened = %v", got)
	}
}

func TestChatGPTAccessTokenWithoutTokens(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	if _, err := e.svc.AccessToken(context.Background()); !errors.Is(err, openai.ErrPlanUnauthorized) {
		t.Errorf("AccessToken = %v", err)
	}
}
