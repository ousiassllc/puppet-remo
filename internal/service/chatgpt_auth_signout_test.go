package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestChatGPTSignOutRevokesAndDeletesLocally(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	res, err := e.svc.SignOut(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Revoked || res.CancelledTask != "task-1" {
		t.Fatalf("result = %+v", res)
	}
	if len(e.cancelled) != 1 || e.cancelled[0] != "chatgpt_signout" {
		t.Errorf("CancelTasks reasons = %v", e.cancelled)
	}
	_, _, rv := e.fake.snapshot()
	if len(rv) != 1 || rv[0].Get("token") != "rt-seed" || rv[0].Get("token_type_hint") != "refresh_token" || rv[0].Get("client_id") != testChatGPTClientID {
		t.Fatalf("revoke requests = %v", rv)
	}
	if st := e.svc.Status(); st.State != PlanSignedOut || st.Problem != PlanProblemNone {
		t.Fatalf("Status = %+v", st)
	}
	if reason, ok := e.svc.Ready(); ok || reason != domain.AgentDisabledChatGPTNotSignedIn {
		t.Errorf("Ready = %q, %v", reason, ok)
	}
	if e.hasSecret(platformSecretChatGPT) || e.hasSecret(chatgptPartName(1)) {
		t.Error("tokens remain")
	}
	// client_id・sub・メール・ホスト ID は残す（解除は登録済みのクライアントを削除しない）。
	if e.setting(SettingChatGPTClientID) != testChatGPTClientID || e.setting(SettingChatGPTAccountSub) != testChatGPTSub ||
		e.setting(SettingChatGPTAccountEmail) != testChatGPTEmail || e.setting(SettingChatGPTHostID) == "" {
		t.Error("the account identifiers were removed")
	}
	last := e.audit.Entries()[len(e.audit.Entries())-1]
	if last.Event != "agent.chatgpt.signout" || last.Result != auditOK ||
		!strings.Contains(string(last.DetailJSON), `"revoked":true`) || !strings.Contains(string(last.DetailJSON), `"forget_account":false`) ||
		!strings.Contains(string(last.DetailJSON), `"cancelled_task":"task-1"`) {
		t.Errorf("audit = %s", last.DetailJSON)
	}
	if got := e.recordedSleeps(); len(got) != 0 {
		t.Errorf("slept %v on a first-try success", got)
	}
	e.requireNoSecrets()

	if _, err := e.svc.SignOut(context.Background(), false); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("SignOut when signed out = %v, want conflict", err)
	}
}

func TestChatGPTSignOutForgetAccount(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	if _, err := e.svc.SignOut(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if e.setting(SettingChatGPTClientID) != "" || e.setting(SettingChatGPTAccountSub) != "" || e.setting(SettingChatGPTAccountEmail) != "" {
		t.Error("the account was not forgotten")
	}
	if e.setting(SettingChatGPTHostID) == "" {
		t.Error("the host id must be kept")
	}
	if st := e.svc.Status(); st.AccountEmail != "" {
		t.Errorf("Status = %+v", st)
	}
	last := e.audit.Entries()[len(e.audit.Entries())-1]
	if !strings.Contains(string(last.DetailJSON), `"forget_account":true`) {
		t.Errorf("audit = %s", last.DetailJSON)
	}
	// 次のサインインは新規登録で、login_hint は付かない。
	a := e.startSignIn()
	if a.q.Get("client_id") != chatgptDynamicClientID || a.q.Has("login_hint") {
		t.Error("the sign-in after forget_account is not a fresh registration")
	}
	// 別のアカウント（sub が違う）でサインインできる。
	e.fake.set(func(f *fakeChatGPTServer) { f.sub = "sub-account-2" })
	e.deliver(a)
	if tst := e.waitToast(); tst.Level != "info" {
		t.Fatalf("toast = %+v", tst)
	}
	if e.setting(SettingChatGPTAccountSub) != "sub-account-2" {
		t.Error("the new account is not saved")
	}
}

func TestChatGPTSignOutRetriesRevocationAndStillDeletes(t *testing.T) {
	tests := []struct {
		name       string
		seq        []int
		wantTries  int
		wantRevoke bool
		wantSleeps []int // 秒
	}{
		{"5xx のあと成功", []int{500, 200}, 2, true, []int{1}},
		{"2 回失敗して 3 回目に成功", []int{503, 502, 200}, 3, true, []int{1, 2}},
		{"3 回とも失敗", []int{500, 500, 500}, 3, false, []int{1, 2}},
		{"429 は再試行する", []int{429, 200}, 2, true, []int{1}},
		{"400 は再試行しない", []int{400}, 1, false, nil},
		{"すでに無効なトークンでも 200", []int{200}, 1, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
			e.fake.set(func(f *fakeChatGPTServer) { f.revokeSeq = tc.seq })
			res, err := e.svc.SignOut(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			if res.Revoked != tc.wantRevoke {
				t.Errorf("Revoked = %v, want %v", res.Revoked, tc.wantRevoke)
			}
			if _, _, rv := e.fake.snapshot(); len(rv) != tc.wantTries {
				t.Errorf("revoke requests = %d, want %d", len(rv), tc.wantTries)
			}
			var secs []int
			for _, d := range e.recordedSleeps() {
				secs = append(secs, int(d.Seconds()))
			}
			if len(secs) != len(tc.wantSleeps) {
				t.Fatalf("sleeps = %v, want %v", secs, tc.wantSleeps)
			}
			for i := range secs {
				if secs[i] != tc.wantSleeps[i] {
					t.Errorf("sleeps = %v, want %v", secs, tc.wantSleeps)
				}
			}
			// 失効の結果によらず、ローカルは消える。
			if e.hasSecret(platformSecretChatGPT) || e.svc.Status().State != PlanSignedOut {
				t.Error("local deletion was not completed")
			}
			wantProblem := PlanProblemNone
			if !tc.wantRevoke {
				wantProblem = PlanProblemRevokeFailed
			}
			if e.svc.Status().Problem != wantProblem {
				t.Errorf("Problem = %v, want %v", e.svc.Status().Problem, wantProblem)
			}
			last := e.audit.Entries()[len(e.audit.Entries())-1]
			want := `"revoked":` + map[bool]string{true: "true", false: "false"}[tc.wantRevoke]
			if !strings.Contains(string(last.DetailJSON), want) {
				t.Errorf("audit = %s, want %s", last.DetailJSON, want)
			}
		})
	}
}

func TestChatGPTSignOutNetworkFailureStillDeletes(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true, mod: func(c *ChatGPTAuthConfig) {
		c.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}
	}})
	res, err := e.svc.SignOut(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Revoked {
		t.Error("Revoked with an unreachable server")
	}
	if e.hasSecret(platformSecretChatGPT) || e.svc.Status().State != PlanSignedOut || e.setting(SettingChatGPTClientID) != "" {
		t.Error("local deletion was not completed")
	}
	if got := e.recordedSleeps(); len(got) != 2 {
		t.Errorf("sleeps = %v, want 2 waits", got)
	}
}

func TestChatGPTSignOutNeverSendsTheRefreshTokenToAForeignEndpoint(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.fake.set(func(f *fakeChatGPTServer) { f.revokeHost = "https://evil.example" })
	res, err := e.svc.SignOut(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Revoked {
		t.Error("Revoked via a foreign endpoint")
	}
	if _, _, rv := e.fake.snapshot(); len(rv) != 0 {
		t.Error("a revoke request reached the auth server although discovery pointed elsewhere")
	}
	if e.hasSecret(platformSecretChatGPT) {
		t.Error("tokens remain")
	}
}

func TestChatGPTSignOutFromReauthRequiredAndUsageLimited(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	e.fake.set(func(f *fakeChatGPTServer) {
		f.refresh = func() (int, string) { return 400, `{"error":"invalid_grant"}` }
	})
	e.svc.ReportAuthFailure(context.Background())
	if e.svc.Status().State != PlanReauthRequired {
		t.Fatal("setup: not reauth_required")
	}
	res, err := e.svc.SignOut(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Revoked { // 失効するトークンが無い
		t.Error("Revoked = false with nothing to revoke")
	}
	if _, _, rv := e.fake.snapshot(); len(rv) != 0 {
		t.Error("a revoke request was sent without a token")
	}
	if e.svc.Status().State != PlanSignedOut {
		t.Error("not signed out")
	}
}

func TestChatGPTSignOutDuringSignInCancelsIt(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{})
	a := e.startSignIn()
	if _, err := e.svc.SignOut(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if e.svc.Status().State != PlanSignedOut {
		t.Error("not signed out")
	}
	requireClosedPort(t, a.port)
}

func TestChatGPTSignOutWaitsForARefreshInFlight(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	gate := make(chan struct{})
	e.fake.set(func(f *fakeChatGPTServer) { f.refreshGate = gate })
	e.clock.Advance(58 * time.Minute)
	done := make(chan error, 1)
	go func() { _, err := e.svc.AccessToken(context.Background()); done <- err }()
	<-e.fake.refreshSeen
	signedOut := make(chan error, 1)
	go func() { _, err := e.svc.SignOut(context.Background(), false); signedOut <- err }()
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-signedOut; err != nil {
		t.Fatal(err)
	}
	// 解除のあとに、更新の結果が記録を復活させない。
	if e.hasSecret(platformSecretChatGPT) || e.svc.Status().State != PlanSignedOut {
		t.Error("a refresh resurrected the tokens after the sign-out")
	}
}
