package gui_test

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

// ChatGPT のプランの接続方式（#247・FR-G25〜G30・FR-H11）の画面とルート。偽の service（guitest）で確かめる。

const demoEmail = "demo-user@example.com"

func chatgptState(st service.ChatGPTStatus) func(*guitest.Fakes) {
	return func(f *guitest.Fakes) {
		f.AgentSettings.SetConnectionDirect(domain.ConnectionChatGPT)
		f.ChatGPT.Set(st)
	}
}

func codexState(st service.CodexStatus, conn domain.AgentConnection) func(*guitest.Fakes) {
	return func(f *guitest.Fakes) {
		f.AgentSettings.SetConnectionDirect(conn)
		f.Codex.Set(st)
	}
}

func installed() service.CodexEngineStatus {
	return service.CodexEngineStatus{State: service.CodexEngineInstalled, Version: "rust-v0.164.0"}
}

// 接続方式の選択は 3 つ。現在の方式にバッジ、ほかには選ぶボタン。openai_api_key だけ確認モーダルを経る。
func TestAgentPageShowsConnectionChoice(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut}))
	res := r.get("/agent")
	status(t, res, http.StatusOK)
	contains(t, res, `id="connection-panel"`, `data-connection="chatgpt"`,
		`data-connection-option="chatgpt"`, `data-connection-option="chatgpt-codex"`, `data-connection-option="openai_api_key"`,
		"ChatGPT のプラン（Codex 経由）", "OpenAI API キー", "使用中の接続方式",
		// chatgpt-codex へは確認なしの PUT。openai_api_key は確認モーダルの GET。
		`hx-put="/agent/connection"`, `hx-get="/agent/connection/api-key/confirm?reason=manual"`)
	// カードは #agent-sections の先頭（接続方式 → ChatGPT → Codex → 既存の設定）。
	i := strings.Index(res.Body, `id="connection-panel"`)
	j := strings.Index(res.Body, `id="chatgpt-card"`)
	k := strings.Index(res.Body, `id="codex-card"`)
	l := strings.Index(res.Body, `id="agent-form"`)
	if i <= 0 || i >= j || j >= k || k >= l {
		t.Errorf("order: connection=%d chatgpt=%d codex=%d form=%d", i, j, k, l)
	}
	// 高速パスは接続方式がプランのあいだは効かない旨。
	contains(t, res, `data-fastpath="plan-inactive"`, "高速パスは効きません")
	// 有効化の同意の文面に、プランの使用量が使われる旨（FR-G14）。
	contains(t, res, `data-consent-usage="plan"`, "ChatGPT のプランの使用量が使われます")
}

func TestAgentPageWithAPIKeyConnectionHasNoPlanNotices(t *testing.T) {
	r := newRig(t, registered(true, false))
	res := r.get("/agent")
	status(t, res, http.StatusOK)
	contains(t, res, `data-connection="openai_api_key"`, "既定は無効です。有効にするには、OpenAI API キーの登録")
	lacks(t, res, `data-fastpath="plan-inactive"`, `data-consent-usage="plan"`)
}

func TestChatGPTCardStates(t *testing.T) {
	tests := []struct {
		name       string
		st         service.ChatGPTStatus
		key        bool
		want, lack []string
	}{
		{"signed out", service.ChatGPTStatus{State: service.PlanSignedOut}, false,
			[]string{`data-chatgpt-state="signed_out"`, "Continue with ChatGPT", `hx-post="/agent/chatgpt/signin"`, "未サインイン"},
			[]string{`hx-post="/agent/chatgpt/signin/cancel"`, "使用量を管理", demoEmail, "ChatGPT のプランを使用中", "再開"}},
		{"signing in", service.ChatGPTStatus{State: service.PlanSigningIn}, false,
			[]string{`data-chatgpt-state="signing_in"`, `data-chatgpt="signing-in"`, "中止", `hx-post="/agent/chatgpt/signin/cancel"`, "サインイン中"},
			[]string{"Continue with ChatGPT", demoEmail}},
		{"connected", service.ChatGPTStatus{State: service.PlanConnected, AccountEmail: demoEmail}, true,
			[]string{`data-chatgpt-state="connected"`, demoEmail, `data-chatgpt="using"`, "ChatGPT のプランを使用中", "使用量を管理", `hx-post="/agent/chatgpt/open-usage"`,
				"モデルを変更", `hx-get="/agent/chatgpt/models"`, `data-plan-model="gpt-6.1-terra"`, `hx-get="/agent/chatgpt/signout/confirm"`, "解除"},
			[]string{"Continue with ChatGPT", "API キーに切り替える", `data-chatgpt="usage-limited"`}},
		{"reauth required", service.ChatGPTStatus{State: service.PlanReauthRequired}, false,
			[]string{`data-chatgpt-state="reauth_required"`, `data-chatgpt="reauth-required"`, "Continue with ChatGPT", "再サインインが必要"},
			[]string{"使用量を管理", demoEmail}},
		{"usage limited with key", service.ChatGPTStatus{State: service.PlanUsageLimited, AccountEmail: demoEmail}, true,
			[]string{`data-chatgpt-state="usage_limited"`, `data-chatgpt="usage-limited"`, "ChatGPT のプランの使用量の上限に達しました", "使用量を管理", "再開", `hx-post="/agent/chatgpt/resume"`,
				"API キーに切り替える…", `/agent/connection/api-key/confirm?reason=usage_limit`},
			[]string{"Continue with ChatGPT", `data-chatgpt="using"`}},
		{"usage limited without key", service.ChatGPTStatus{State: service.PlanUsageLimited, AccountEmail: demoEmail}, false,
			[]string{`data-chatgpt="usage-limited"`, "再開", "使用量を管理"},
			[]string{"API キーに切り替える"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, chatgptState(tt.st), registered(tt.key, false))
			res := r.get("/agent")
			status(t, res, http.StatusOK)
			contains(t, res, `id="chatgpt-card"`, `name="rev_chatgpt"`)
			contains(t, res, tt.want...)
			lacks(t, res, tt.lack...)
		})
	}
}

// 未サインイン・再認可のカードにメールは出ない（接続中・上限のカードの本人確認だけ）。
func TestChatGPTCardHidesEmailUnlessConnected(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut, AccountEmail: demoEmail}))
	lacks(t, r.get("/agent"), demoEmail)
}

func TestChatGPTCardProblems(t *testing.T) {
	tests := []struct {
		problem service.PlanProblem
		want    string
	}{
		{service.PlanProblemAccessDenied, "許可されませんでした"},
		{service.PlanProblemScopeMissing, "ChatGPT のプランの使用が許可されませんでした"},
		{service.PlanProblemNotEligible, "利用資格がありません"},
		{service.PlanProblemAccountMismatch, "別の ChatGPT アカウント"},
		{service.PlanProblemTimeout, "5 分以内に終わらなかった"},
		{service.PlanProblemFailed, "サインインに失敗しました"},
	}
	for _, tt := range tests {
		t.Run(string(tt.problem), func(t *testing.T) {
			r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut, Problem: tt.problem}))
			res := r.get("/agent")
			contains(t, res, `data-chatgpt-problem="`+string(tt.problem)+`"`, tt.want)
			lacks(t, res, `data-chatgpt="offer-codex"`)
		})
	}
}

// policy_denied は「Codex 経由で使う」の案内。押すと接続方式を chatgpt-codex に変える PUT（自動では変えない）。
func TestChatGPTPolicyDeniedOffersCodex(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut, Problem: service.PlanProblemPolicyDenied}))
	res := r.get("/agent")
	contains(t, res, `data-chatgpt-problem="policy_denied"`, "ワークスペース", `data-chatgpt="offer-codex"`, "Codex 経由で使う",
		`data-chatgpt-use-codex`, `hx-put="/agent/connection"`, "chatgpt-codex")
	if got := r.f.AgentSettings.State().Connection; got != domain.ConnectionChatGPT {
		t.Fatalf("rendering must not change the connection: %q", got)
	}
	res = r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"chatgpt-codex"}}, hx...)
	status(t, res, http.StatusOK)
	if got := r.f.AgentSettings.State().Connection; got != domain.ConnectionChatGPTCodex {
		t.Errorf("connection = %q", got)
	}
}

func TestChatGPTWelcomeModalShowsOnceAndDismisses(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected, AccountEmail: demoEmail, WelcomePending: true}))
	contains(t, r.get("/agent"), `data-chatgpt="welcome"`, `hx-get="/agent/chatgpt/welcome"`, `hx-trigger="load"`)
	res := r.get("/agent/chatgpt/welcome", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "ChatGPT のプランを使っています", "わかりました", `hx-post="/agent/chatgpt/welcome/dismiss"`)
	lacks(t, res, "キャンセル")
	res = r.post("/agent/chatgpt/welcome/dismiss", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="modal-root" hx-swap-oob="true"`)
	// 閉じたあとは出さない（モーダルは 204 で何も差し込まない）。
	lacks(t, r.get("/agent"), `data-chatgpt="welcome"`)
	status(t, r.get("/agent/chatgpt/welcome", hx...), http.StatusNoContent)
}

func TestChatGPTSignIn(t *testing.T) {
	r := newRig(t)
	res := r.post("/agent/chatgpt/signin", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="chatgpt-card"`, `data-chatgpt-state="signing_in"`, "中止")
	lacks(t, res, "<html")
	// 進行中・接続済みでの開始は 409（状態ごとの文言）。
	res = r.post("/agent/chatgpt/signin", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "すでに進行中", `id="chatgpt-card"`, `hx-swap-oob`, `data-chatgpt-state="signing_in"`)
	r.f.ChatGPT.Set(service.ChatGPTStatus{State: service.PlanConnected})
	res = r.post("/agent/chatgpt/signin", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, "すでに ChatGPT にサインインしています")
}

// 開く URL・パラメーターは受け取らない（クエリ・本文を渡しても service の呼び出しは変わらず、応答にも出ない）。
func TestChatGPTSignInIgnoresQueryAndBody(t *testing.T) {
	r := newRig(t)
	res := r.do(http.MethodPost, "/agent/chatgpt/signin?url=https://evil.example/x&redirect_uri=http://evil.example", url.Values{"url": {"https://evil.example/y"}}, hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, "evil.example")
	if got := strings.Join(r.f.ChatGPT.Calls, ","); got != "StartSignIn" {
		t.Errorf("calls = %q", got)
	}
}

// ブラウザを開けなかったときは signed_out のカードとエラーのアラート。service のエラー文（URL・パラメーターを含みうる）は出さない。
func TestChatGPTSignInFailureShowsGenericAlert(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.ChatGPT.StartErr = errors.New("open https://auth.openai.com/api/accounts/authorize?state=SECRETSTATE: exec: not found")
	})
	res := r.post("/agent/chatgpt/signin", nil, hx...)
	status(t, res, http.StatusInternalServerError)
	contains(t, res, `data-error-code="internal"`, "ブラウザを開けなかった可能性")
	lacks(t, res, "SECRETSTATE", "auth.openai.com", "exec:")
	contains(t, res, `data-chatgpt-state="signed_out"`)
}

func TestChatGPTCancelSignInIsIdempotent(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSigningIn}))
	res := r.post("/agent/chatgpt/signin/cancel", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-chatgpt-state="signed_out"`)
	res = r.post("/agent/chatgpt/signin/cancel", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-chatgpt-state="signed_out"`)
}

func TestChatGPTSignOutConfirmAndDelete(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected, AccountEmail: demoEmail}))
	res := r.get("/agent/chatgpt/signout/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "解除しますか", "中断されます", "失効", "再びサインインするまで",
		`name="forget_account"`, "別のアカウントでサインインする", `hx-delete="/agent/chatgpt"`, `hx-include="#modal-checkbox"`, "キャンセル")
	// チェックなし: forget_account は false。
	res = r.do(http.MethodDelete, "/agent/chatgpt", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-chatgpt-state="signed_out"`, `id="modal-root" hx-swap-oob="true"`, "ChatGPT のサインインを解除しました")
	lacks(t, res, demoEmail, "失効を確認できませんでした")
	if r.f.ChatGPT.LastForget {
		t.Error("forget_account must be false without the checkbox")
	}
	// signed_out での確認・解除は 409（モーダルなし）。
	res = r.get("/agent/chatgpt/signout/confirm", hx...)
	status(t, res, http.StatusConflict)
	lacks(t, res, `role="dialog"`)
	status(t, r.do(http.MethodDelete, "/agent/chatgpt", nil, hx...), http.StatusConflict)
}

// htmx の DELETE はチェックボックスの値を URL のクエリに載せる。forget_account は on で伝わる。失効を確認できなかったときは文言を添える（200 のまま）。
func TestChatGPTSignOutForgetAccountAndRevokeFailure(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected}), func(f *guitest.Fakes) {
		f.ChatGPT.SignOutRes = service.ChatGPTSignOutResult{Revoked: false, CancelledTask: "01JTASK"}
	})
	res := r.do(http.MethodDelete, "/agent/chatgpt?forget_account=on", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, "ChatGPT 側の失効を確認できませんでした。ChatGPT の設定からアプリの接続を解除できます", "実行中のタスクを中断しました")
	if !r.f.ChatGPT.LastForget {
		t.Error("forget_account must be true")
	}
}

func TestChatGPTResume(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanUsageLimited, AccountEmail: demoEmail}))
	res := r.post("/agent/chatgpt/resume", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-chatgpt-state="connected"`)
	res = r.post("/agent/chatgpt/resume", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "使用量の上限による一時停止ではない")
}

func TestOpenChatGPTUsageOpensOnlyTheFixedPage(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected}))
	res := r.do(http.MethodPost, "/agent/chatgpt/open-usage?url=https://evil.example", url.Values{"url": {"https://evil.example"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, "ChatGPT の使用量の設定を開きました。")
	lacks(t, res, "evil.example")
	if got := strings.Join(r.f.ChatGPT.Calls, ","); got != "OpenUsage" {
		t.Errorf("calls = %q", got)
	}
	r.f.ChatGPT.OpenUsageErr = errors.New("exec: browser at C:/secret not found")
	res = r.post("/agent/chatgpt/open-usage", nil, hx...)
	status(t, res, http.StatusInternalServerError)
	contains(t, res, `data-error-code="internal"`, "使用量の設定を開けませんでした")
	lacks(t, res, "secret")
}

func TestChatGPTModelsAndSave(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected, AccountEmail: demoEmail}))
	res := r.get("/agent/chatgpt/models", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-plan-model-choices`, `hx-put="/agent/chatgpt"`, `name="model"`,
		`value="gpt-6.1-terra" selected`, "GPT-6.1 Sol", `value="gpt-6.1-sol"`)
	res = r.do(http.MethodPut, "/agent/chatgpt", url.Values{"model": {"gpt-6.1-sol"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="chatgpt-card"`, `data-plan-model="gpt-6.1-sol"`, "モデルを保存しました")
	if got := r.f.AgentSettings.State().ChatGPTModel; got != "gpt-6.1-sol" {
		t.Errorf("model = %q", got)
	}
	// 一覧に無い値は 422（保存しない）。
	res = r.do(http.MethodPut, "/agent/chatgpt", url.Values{"model": {"gpt-unknown"}}, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-error-code="invalid_argument"`, "一覧にありません")
	if got := r.f.AgentSettings.State().ChatGPTModel; got != "gpt-6.1-sol" {
		t.Errorf("model = %q after a rejected save", got)
	}
}

func TestChatGPTModelsConflictAndFailure(t *testing.T) {
	r := newRig(t)
	res := r.get("/agent/chatgpt/models", hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "モデルの一覧を取得できません")
	status(t, r.do(http.MethodPut, "/agent/chatgpt", url.Values{"model": {"gpt-6.1-terra"}}, hx...), http.StatusConflict)

	r = newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected}), func(f *guitest.Fakes) {
		f.ChatGPT.ModelsErr = errors.New("GET https://api.openai.com/v1/models: 500 body=eyJSECRETTOKEN")
	})
	res = r.get("/agent/chatgpt/models", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-plan-model-choices`, `value="gpt-6.1-terra" selected`, "モデルの一覧を取得できませんでした")
	lacks(t, res, "SECRETTOKEN", "api.openai.com", "gpt-6.1-sol")
}

// 接続方式の切り替え。chatgpt ↔ chatgpt-codex は確認なし。openai_api_key は confirmed=1（確認モーダルの確定ボタンだけが付ける）が無いと 422。
func TestSetConnection(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut}), registered(true, false))
	res := r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"chatgpt-codex"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="agent-sections"`, `data-connection="chatgpt-codex"`, `id="modal-root" hx-swap-oob="true"`, "接続方式を「ChatGPT のプラン（Codex 経由）」に変更しました")
	if got := r.f.AgentSettings.State().Connection; got != domain.ConnectionChatGPTCodex {
		t.Errorf("connection = %q", got)
	}
	res = r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"chatgpt"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-connection="chatgpt"`)

	// confirmed なしの openai_api_key は 422（service にも渡さない）。0 や true は確認とみなさない。
	r.f.AgentSettings.Calls = nil
	for _, confirmed := range []string{"", "0", "true", "on"} {
		form := url.Values{"connection": {"openai_api_key"}}
		if confirmed != "" {
			form.Set("confirmed", confirmed)
		}
		res = r.do(http.MethodPut, "/agent/connection", form, hx...)
		status(t, res, http.StatusUnprocessableEntity)
		contains(t, res, `data-error-code="invalid_argument"`)
	}
	if len(r.f.AgentSettings.Calls) != 0 || r.f.AgentSettings.State().Connection != domain.ConnectionChatGPT {
		t.Errorf("unconfirmed switch reached the service: calls=%v connection=%q", r.f.AgentSettings.Calls, r.f.AgentSettings.State().Connection)
	}

	// 定義外の値は 422。
	status(t, r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"nope"}}, hx...), http.StatusUnprocessableEntity)

	// confirmed=1 で切り替わる。reason は usage_limit・manual（既定）だけ。
	res = r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"openai_api_key"}, "confirmed": {"1"}, "reason": {"usage_limit"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-connection="openai_api_key"`)
	if c := r.f.AgentSettings.LastConnection; c.Conn != domain.ConnectionOpenAIAPIKey || !c.Confirmed || c.Reason != "usage_limit" {
		t.Errorf("last call = %+v", c)
	}
	r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"chatgpt"}}, hx...)
	r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"openai_api_key"}, "confirmed": {"1"}, "reason": {"<script>"}}, hx...)
	if c := r.f.AgentSettings.LastConnection; c.Reason != "manual" {
		t.Errorf("reason = %q, want manual", c.Reason)
	}
}

func TestSetConnectionToAPIKeyWithoutKeyIsConflict(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut}))
	res := r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"openai_api_key"}, "confirmed": {"1"}}, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "OpenAI API キーが登録されていない")
	if got := r.f.AgentSettings.State().Connection; got != domain.ConnectionChatGPT {
		t.Errorf("connection = %q", got)
	}
}

// 確認モーダル: 以後が従量課金になること・プランの使用量は使われないこと・ツールと安全チェックが変わること・止まったタスクは再開されないこと。
func TestConfirmAPIKeyConnection(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanUsageLimited}), registered(true, false))
	res := r.get("/agent/connection/api-key/confirm?reason=usage_limit", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "従量課金", "ChatGPT のプランの使用量は使われません", "computer", "安全チェック", "再開されません", "run_task",
		`hx-put="/agent/connection"`, `hx-target="#agent-sections"`, "切り替える")
	// 確定ボタンの隠し値（HTML 属性の中で " は &#34; になる）。
	vals := regexp.MustCompile(`hx-vals="([^"]*)"`).FindStringSubmatch(res.Body)
	if vals == nil {
		t.Fatalf("no hx-vals\n%s", res.Body)
	}
	v := strings.NewReplacer("&#34;", `"`, "&quot;", `"`).Replace(vals[1])
	for _, want := range []string{`"connection":"openai_api_key"`, `"confirmed":"1"`, `"reason":"usage_limit"`} {
		if !strings.Contains(v, want) {
			t.Errorf("hx-vals %q lacks %s", v, want)
		}
	}
	// reason が定義外なら manual。
	res = r.get("/agent/connection/api-key/confirm?reason=x", hx...)
	contains(t, res, "reason&#34;:&#34;manual")
}

func TestConfirmAPIKeyConnectionConflicts(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut}))
	res := r.get("/agent/connection/api-key/confirm", hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, "OpenAI API キーが登録されていない")
	lacks(t, res, `role="dialog"`)
	r2 := newRig(t, registered(true, false)) // すでに openai_api_key
	res = r2.get("/agent/connection/api-key/confirm", hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, "すでに OpenAI API キー")
}

// PUT /agent の 409 agent_disabled は detail.reason で文言を変える。
func TestSaveAgentConflictMessageFollowsTheReason(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*guitest.Fakes)
		want   string
		reason string // data-error-reason（API 仕様 §1・§3.4）
	}{
		{"openai key missing", func(f *guitest.Fakes) {}, "OpenAI API キーが登録されていない", domain.AgentDisabledOpenAIKeyMissing},
		{"chatgpt not signed in", chatgptState(service.ChatGPTStatus{State: service.PlanSignedOut}), "ChatGPT にサインインしていない", domain.AgentDisabledChatGPTNotSignedIn},
		{"chatgpt signing in", chatgptState(service.ChatGPTStatus{State: service.PlanSigningIn}), "サインインが進行中", domain.AgentDisabledChatGPTSigningIn},
		{"chatgpt reauth", chatgptState(service.ChatGPTStatus{State: service.PlanReauthRequired}), "再サインインが必要", domain.AgentDisabledChatGPTReauthRequired},
		{"chatgpt usage limited", chatgptState(service.ChatGPTStatus{State: service.PlanUsageLimited}), "使用量の上限に達している", domain.AgentDisabledChatGPTUsageLimited},
		{"codex unsupported", codexState(service.CodexStatus{Engine: service.CodexEngineStatus{State: service.CodexEngineUnsupported}}, domain.ConnectionChatGPTCodex), "使えない", domain.AgentDisabledCodexUnsupported},
		{"codex engine missing", codexState(service.CodexStatus{Engine: service.CodexEngineStatus{State: service.CodexEngineMissing}}, domain.ConnectionChatGPTCodex), "エンジンを取得していない", domain.AgentDisabledCodexEngineMissing},
		{"codex not signed in", codexState(service.CodexStatus{Engine: installed(), State: service.PlanSignedOut}, domain.ConnectionChatGPTCodex), "Codex にログインしていない", domain.AgentDisabledCodexNotSignedIn},
		{"codex signing in", codexState(service.CodexStatus{Engine: installed(), State: service.PlanSigningIn}, domain.ConnectionChatGPTCodex), "ログインが進行中", domain.AgentDisabledCodexSigningIn},
		{"codex reauth", codexState(service.CodexStatus{Engine: installed(), State: service.PlanReauthRequired}, domain.ConnectionChatGPTCodex), "再ログインが必要", domain.AgentDisabledCodexReauthRequired},
		{"codex usage limited", codexState(service.CodexStatus{Engine: installed(), State: service.PlanUsageLimited}, domain.ConnectionChatGPTCodex), "使用量の上限に達している", domain.AgentDisabledCodexUsageLimited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, tt.setup)
			res := r.do(http.MethodPut, "/agent", validAgentForm(), hx...)
			status(t, res, http.StatusConflict)
			contains(t, res, `data-error-code="agent_disabled"`, `data-error-reason="`+tt.reason+`"`, tt.want)
			if r.f.AgentSettings.State().Enabled {
				t.Error("the agent was enabled")
			}
		})
	}
}

func TestSaveAgentWithConnectedPlanSucceeds(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected, AccountEmail: demoEmail}))
	res := r.do(http.MethodPut, "/agent", validAgentForm(), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="agent-form"`, `data-consent-usage="plan"`, "ChatGPT のプランの使用量が使われます")
	if !r.f.AgentSettings.State().Enabled {
		t.Error("not enabled")
	}
}

// ---- Codex ----

func TestCodexCardEngineStates(t *testing.T) {
	tests := []struct {
		name       string
		engine     service.CodexEngineStatus
		want, lack []string
	}{
		{"unsupported", service.CodexEngineStatus{State: service.CodexEngineUnsupported},
			[]string{`data-codex-engine="unsupported"`, `data-codex="unsupported"`, "Windows の x64・ARM64 だけ"},
			[]string{"エンジンを取得する", "Codex でログイン"}},
		{"missing", service.CodexEngineStatus{State: service.CodexEngineMissing, Version: "rust-v0.164.0"},
			[]string{`data-codex-engine="missing"`, "エンジンを取得する", `hx-post="/agent/codex/install"`, "rust-v0.164.0", "SHA-256"},
			[]string{"Codex でログイン", `data-codex="installing"`}},
		{"installing", service.CodexEngineStatus{State: service.CodexEngineInstalling, Version: "rust-v0.164.0", Progress: 42},
			[]string{`data-codex-engine="installing"`, `data-codex="installing"`, `<progress`, `value="42"`, "42%", "中止", `hx-post="/agent/codex/install/cancel"`},
			[]string{"エンジンを取得する", "Codex でログイン"}},
		{"failed", service.CodexEngineStatus{State: service.CodexEngineFailed, Version: "rust-v0.164.0", Error: "SHA-256 が一致しませんでした。"},
			[]string{`data-codex-engine="failed"`, `data-codex="install-failed"`, "SHA-256 が一致しませんでした。", "もう一度取得する"},
			[]string{"Codex でログイン"}},
		{"outdated", service.CodexEngineStatus{State: service.CodexEngineOutdated, Version: "rust-v0.164.0"},
			[]string{`data-codex-engine="outdated"`, `data-codex="outdated"`, "取り直す", "Codex でログイン"}, nil},
		{"installed", installed(),
			[]string{`data-codex-engine="installed"`, `data-field="codex-version"`, "rust-v0.164.0", "Codex でログイン", `hx-post="/agent/codex/signin"`}, []string{"エンジンを取得する"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, codexState(service.CodexStatus{Engine: tt.engine, State: service.PlanSignedOut}, domain.ConnectionChatGPTCodex))
			res := r.get("/agent")
			status(t, res, http.StatusOK)
			contains(t, res, `id="codex-card"`, `name="rev_codex"`, `data-codex-state="signed_out"`)
			contains(t, res, tt.want...)
			lacks(t, res, tt.lack...)
		})
	}
}

// 利用条件の表示: ローカル・個人の利用、ワークスペースの Codex Local の許可、アプリは Codex のトークンを読まない。
func TestCodexCardShowsTerms(t *testing.T) {
	res := newRig(t).get("/agent")
	contains(t, res, `data-codex="terms"`, "ローカルでの個人の利用", "Codex Local", "Codex のトークンを読まず")
}

func TestCodexCardAccountStates(t *testing.T) {
	tests := []struct {
		name       string
		st         service.CodexStatus
		key        bool
		want, lack []string
	}{
		{"signing in", service.CodexStatus{Engine: installed(), State: service.PlanSigningIn},
			false, []string{`data-codex-state="signing_in"`, `data-codex="signing-in"`, "中止", `hx-post="/agent/codex/signin/cancel"`}, []string{"Codex でログイン"}},
		{"connected", service.CodexStatus{Engine: installed(), State: service.PlanConnected, AccountEmail: demoEmail, PlanType: "plus"},
			true, []string{`data-codex-state="connected"`, demoEmail, `data-field="codex-plan"`, "plus", "ChatGPT のプランを使用中", "モデルを変更", `hx-get="/agent/codex/models"`,
				`hx-get="/agent/codex/signout/confirm"`, "ログアウト"}, []string{"Codex でログイン", "API キーに切り替える"}},
		{"usage limited", service.CodexStatus{Engine: installed(), State: service.PlanUsageLimited, AccountEmail: demoEmail},
			true, []string{`data-codex="usage-limited"`, "使用量の上限に達しました", "使用量を管理", "再開", `hx-post="/agent/codex/resume"`, "API キーに切り替える…"}, nil},
		{"usage limited without key", service.CodexStatus{Engine: installed(), State: service.PlanUsageLimited},
			false, []string{`data-codex="usage-limited"`}, []string{"API キーに切り替える"}},
		{"reauth", service.CodexStatus{Engine: installed(), State: service.PlanReauthRequired},
			false, []string{`data-codex="reauth-required"`, "再ログイン", "Codex でログイン"}, nil},
		{"policy denied", service.CodexStatus{Engine: installed(), State: service.PlanSignedOut, Problem: service.PlanProblemPolicyDenied},
			false, []string{`data-codex-problem="policy_denied"`, "Codex Local の許可"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, codexState(tt.st, domain.ConnectionChatGPTCodex), registered(tt.key, false))
			res := r.get("/agent")
			contains(t, res, tt.want...)
			lacks(t, res, tt.lack...)
		})
	}
}

func TestCodexInstallFlow(t *testing.T) {
	r := newRig(t)
	res := r.post("/agent/codex/install", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="codex-card"`, `data-codex-engine="installing"`)
	res = r.post("/agent/codex/install", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "すでに進行中")
	res = r.post("/agent/codex/install/cancel", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-codex-engine="missing"`)
	status(t, r.post("/agent/codex/install/cancel", nil, hx...), http.StatusOK)

	r.f.Codex.Set(service.CodexStatus{Engine: service.CodexEngineStatus{State: service.CodexEngineUnsupported}})
	res = r.post("/agent/codex/install", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, "OS・CPU には Codex のエンジンがない")
}

func TestCodexSignInRequiresEngine(t *testing.T) {
	r := newRig(t)
	res := r.post("/agent/codex/signin", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "先に Codex のエンジンを取得してください")
	r.f.Codex.Set(service.CodexStatus{Engine: installed(), State: service.PlanSignedOut})
	res = r.post("/agent/codex/signin", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-codex-state="signing_in"`)
	res = r.post("/agent/codex/signin", nil, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, "すでに進行中")
	res = r.post("/agent/codex/signin/cancel", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-codex-state="signed_out"`)
}

func TestCodexSignOutWithAndWithoutEngineRemoval(t *testing.T) {
	r := newRig(t, codexState(service.CodexStatus{Engine: installed(), State: service.PlanConnected, AccountEmail: demoEmail}, domain.ConnectionChatGPTCodex))
	res := r.get("/agent/codex/signout/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "ログアウトしますか", "中断されます", `name="remove_engine"`, "エンジンと専用のデータも削除", `hx-delete="/agent/codex"`, `hx-include="#modal-checkbox"`)
	res = r.do(http.MethodDelete, "/agent/codex", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-codex-state="signed_out"`, `data-codex-engine="installed"`, `id="modal-root" hx-swap-oob="true"`, "ログアウトしました")
	if r.f.Codex.LastRemoveEngine {
		t.Error("remove_engine must be false without the checkbox")
	}
	// ログアウト済みでエンジンがあれば、エンジンだけの削除の確認。
	res = r.get("/agent/codex/signout/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, "エンジンを削除しますか", `hx-delete="/agent/codex"`)
	lacks(t, res, `name="remove_engine"`)
	res = r.do(http.MethodDelete, "/agent/codex?remove_engine=on", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-codex-engine="missing"`, "エンジンを削除しました")
	if !r.f.Codex.LastRemoveEngine {
		t.Error("remove_engine must be true")
	}
	// エンジンもログインも無ければ 409（モーダルなし）。
	res = r.get("/agent/codex/signout/confirm", hx...)
	status(t, res, http.StatusConflict)
	lacks(t, res, `role="dialog"`)
	status(t, r.do(http.MethodDelete, "/agent/codex", nil, hx...), http.StatusConflict)
}

func TestCodexResumeAndModels(t *testing.T) {
	r := newRig(t, codexState(service.CodexStatus{Engine: installed(), State: service.PlanUsageLimited, AccountEmail: demoEmail}, domain.ConnectionChatGPTCodex))
	res := r.get("/agent/codex/models", hx...)
	status(t, res, http.StatusConflict)
	res = r.post("/agent/codex/resume", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-codex-state="connected"`)
	status(t, r.post("/agent/codex/resume", nil, hx...), http.StatusConflict)

	res = r.get("/agent/codex/models", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `hx-put="/agent/codex"`, "GPT-6.1 Codex", `value="gpt-6.1-terra" selected`)
	res = r.do(http.MethodPut, "/agent/codex", url.Values{"model": {"gpt-6.1-codex"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="codex-card"`, `data-plan-model="gpt-6.1-codex"`)
	if got := r.f.AgentSettings.State().CodexModel; got != "gpt-6.1-codex" {
		t.Errorf("codex model = %q", got)
	}
	status(t, r.do(http.MethodPut, "/agent/codex", url.Values{"model": {"nope"}}, hx...), http.StatusUnprocessableEntity)
}

// ---- 秘密・poll ----

// トークンらしき値は、どの状態の画面にも、失敗の応答にも出ない（service のエラー文が秘密を含んでいても出さない）。
func TestPlanScreensNeverShowTokens(t *testing.T) {
	secret := regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}|access_token|refresh_token|id_token|Bearer |authorization_code|code_verifier|client_secret`)
	check := func(t *testing.T, res resp) {
		t.Helper()
		if m := secret.FindString(res.Body); m != "" {
			t.Errorf("response shows a token-like value %q\n%s", m, res.Body)
		}
	}
	states := []service.ChatGPTStatus{
		{State: service.PlanSignedOut}, {State: service.PlanSigningIn}, {State: service.PlanConnected, AccountEmail: demoEmail, WelcomePending: true},
		{State: service.PlanReauthRequired}, {State: service.PlanUsageLimited, AccountEmail: demoEmail}, {State: service.PlanSignedOut, Problem: service.PlanProblemPolicyDenied},
	}
	for _, st := range states {
		r := newRig(t, chatgptState(st), codexState(service.CodexStatus{Engine: installed(), State: service.PlanConnected, AccountEmail: demoEmail, PlanType: "plus"}, domain.ConnectionChatGPT), registered(true, true))
		check(t, r.get("/agent"))
		check(t, r.get("/poll?rev_chatgpt=x&rev_codex=x", hx...))
	}
	leak := errors.New("token eyJhbGciOiJSUzI1NiJ9.SECRET refresh_token=rt_SECRET")
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanConnected}), func(f *guitest.Fakes) {
		f.ChatGPT.StartErr, f.ChatGPT.SignOutErr, f.ChatGPT.OpenUsageErr, f.ChatGPT.ModelsErr = leak, leak, leak, leak
		f.Codex.InstallErr, f.Codex.SignInErr, f.Codex.SignOutErr, f.Codex.ModelsErr = leak, leak, leak, leak
		f.AgentSettings.SetConnectionErr = leak
	})
	r.f.ChatGPT.Set(service.ChatGPTStatus{State: service.PlanSignedOut})
	for _, res := range []resp{
		r.post("/agent/chatgpt/signin", nil, hx...),
		r.post("/agent/codex/install", nil, hx...),
		r.do(http.MethodPut, "/agent/connection", url.Values{"connection": {"chatgpt-codex"}}, hx...),
	} {
		check(t, res)
		lacks(t, res, "SECRET")
	}
	r.f.ChatGPT.Set(service.ChatGPTStatus{State: service.PlanConnected})
	r.f.Codex.Set(service.CodexStatus{Engine: installed(), State: service.PlanConnected})
	for _, res := range []resp{
		r.do(http.MethodDelete, "/agent/chatgpt", nil, hx...),
		r.post("/agent/chatgpt/open-usage", nil, hx...),
		r.get("/agent/chatgpt/models", hx...),
		r.do(http.MethodDelete, "/agent/codex", nil, hx...),
		r.get("/agent/codex/models", hx...),
	} {
		check(t, res)
		lacks(t, res, "SECRET")
	}
}

// /poll は rev_chatgpt・rev_codex が古いときだけ、そのカードを OOB で差し替える（サインインの完了・取得の進みを反映）。
func TestPollSwapsPlanCardsOnlyWhenTheRevisionChanged(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanSigningIn}),
		func(f *guitest.Fakes) {
			f.Codex.Set(service.CodexStatus{Engine: service.CodexEngineStatus{State: service.CodexEngineInstalling, Version: "rust-v0.164.0", Progress: 10}})
		})
	page := r.get("/agent")
	revGPT, revCodex := revOf(t, page, "rev_chatgpt"), revOf(t, page, "rev_codex")

	// 版が同じなら差し替えない。
	res := r.get(pollURL(url.Values{"rev_chatgpt": {revGPT}, "rev_codex": {revCodex}}), hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, `id="chatgpt-card"`, `id="codex-card"`)

	// サインインの完了（メールを含む表示モデルの変化）と、取得の進み。
	r.f.ChatGPT.Set(service.ChatGPTStatus{State: service.PlanConnected, AccountEmail: demoEmail})
	r.f.Codex.Set(service.CodexStatus{Engine: service.CodexEngineStatus{State: service.CodexEngineInstalling, Version: "rust-v0.164.0", Progress: 55}})
	res = r.get(pollURL(url.Values{"rev_chatgpt": {revGPT}, "rev_codex": {revCodex}}), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="chatgpt-card"`, `hx-swap-oob="true"`, `data-chatgpt-state="connected"`, demoEmail,
		`id="codex-card"`, `data-codex-engine="installing"`, `value="55"`, "55%")
	lacks(t, res, "<html")

	// 表示していないカード（rev を送っていない）は差し替えない。
	res = r.get(pollURL(url.Values{"rev_codex": {"stale"}}), hx...)
	contains(t, res, `id="codex-card"`)
	lacks(t, res, `id="chatgpt-card"`)
	res = r.get(pollURL(nil), hx...)
	lacks(t, res, `id="chatgpt-card"`, `id="codex-card"`)

	// 新しい版を送れば、また差し替わらない。
	page = r.get("/agent")
	res = r.get(pollURL(url.Values{"rev_chatgpt": {revOf(t, page, "rev_chatgpt")}, "rev_codex": {revOf(t, page, "rev_codex")}}), hx...)
	lacks(t, res, `id="chatgpt-card"`, `id="codex-card"`)
}

// 版は OpenAI API キーの登録の有無・選んだモデル・初回の確認の表示でも変わる（表示モデルのハッシュ。FR-H11 の版の定義）。
func TestChatGPTCardRevisionTracksKeyModelAndWelcome(t *testing.T) {
	r := newRig(t, chatgptState(service.ChatGPTStatus{State: service.PlanUsageLimited, AccountEmail: demoEmail}))
	rev0 := revOf(t, r.get("/agent"), "rev_chatgpt")
	r.f.AgentSettings.Registered(true, false)
	rev1 := revOf(t, r.get("/agent"), "rev_chatgpt")
	r.f.ChatGPT.Set(service.ChatGPTStatus{State: service.PlanConnected, AccountEmail: demoEmail, WelcomePending: true})
	rev2 := revOf(t, r.get("/agent"), "rev_chatgpt")
	if rev0 == rev1 || rev1 == rev2 || rev0 == rev2 {
		t.Errorf("revisions should differ: %s %s %s", rev0, rev1, rev2)
	}
	// 秘密は含まない: 版は 16 桁の 16 進数だけ。
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(rev2) {
		t.Errorf("rev = %q", rev2)
	}
}

// 画面の各ボタンの hx-* が指すルートは、すべて登録されている（404 にならない）。
func TestPlanRoutesAreRegistered(t *testing.T) {
	r := newRig(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodPut, "/agent/connection"}, {http.MethodGet, "/agent/connection/api-key/confirm"},
		{http.MethodPost, "/agent/chatgpt/signin"}, {http.MethodPost, "/agent/chatgpt/signin/cancel"}, {http.MethodGet, "/agent/chatgpt/signout/confirm"},
		{http.MethodDelete, "/agent/chatgpt"}, {http.MethodPost, "/agent/chatgpt/resume"}, {http.MethodPost, "/agent/chatgpt/open-usage"},
		{http.MethodGet, "/agent/chatgpt/models"}, {http.MethodPut, "/agent/chatgpt"}, {http.MethodPost, "/agent/chatgpt/welcome/dismiss"},
		{http.MethodPost, "/agent/codex/install"}, {http.MethodPost, "/agent/codex/install/cancel"}, {http.MethodPost, "/agent/codex/signin"},
		{http.MethodPost, "/agent/codex/signin/cancel"}, {http.MethodGet, "/agent/codex/signout/confirm"}, {http.MethodDelete, "/agent/codex"},
		{http.MethodPost, "/agent/codex/resume"}, {http.MethodGet, "/agent/codex/models"}, {http.MethodPut, "/agent/codex"},
	} {
		if res := r.do(route.method, route.path, nil, hx...); res.StatusCode == http.StatusNotFound {
			t.Errorf("%s %s is not registered", route.method, route.path)
		}
	}
}
