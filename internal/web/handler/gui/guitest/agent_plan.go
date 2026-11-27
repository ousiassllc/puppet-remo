package guitest

import (
	"context"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは ChatGPT のプランの接続方式（#247）の偽実装: gui.ChatGPT（公式の Sign in with ChatGPT）・gui.Codex（Codex の app-server）と、
// AgentSettings の接続方式・モデルの保存。状態は Set で外から設定でき、操作は本物と同じ状態の規則（状態に合わなければ domain.CodeConflict）を持つ。
// トークン・認可コードは、どちらにも無い（メールは架空の example だけ）。

func conflict(msg string) error { return domain.NewError(domain.CodeConflict, msg, nil) }

// ChatGPT は gui.ChatGPT の偽実装。
type ChatGPT struct {
	mu sync.Mutex
	st service.ChatGPTStatus

	// Models・ModelsErr は Models が返す一覧と失敗。SignOutResult は SignOut の結果。
	ModelsList   []service.PlanModel
	ModelsErr    error
	SignOutRes   service.ChatGPTSignOutResult
	StartErr     error
	SignOutErr   error
	OpenUsageErr error
	// Calls は呼び出された操作名の履歴（引数は残さない）。LastForget は直近の SignOut の forgetAccount。
	Calls      []string
	LastForget bool
}

// NewChatGPT は signed_out の ChatGPT を返す。
func NewChatGPT() *ChatGPT {
	return &ChatGPT{
		st: service.ChatGPTStatus{State: service.PlanSignedOut},
		ModelsList: []service.PlanModel{
			{Slug: "gpt-6.1-terra", DisplayName: "GPT-6.1 Terra"}, {Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"},
		},
		SignOutRes: service.ChatGPTSignOutResult{Revoked: true},
	}
}

// Set は状態を直接設定する（テスト・デモの初期状態用）。
func (g *ChatGPT) Set(st service.ChatGPTStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.st = st
}

// State は現在の状態を返す（検査用）。
func (g *ChatGPT) State() service.ChatGPTStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.st
}

func (g *ChatGPT) Status() service.ChatGPTStatus { return g.State() }

func (g *ChatGPT) StartSignIn(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, "StartSignIn")
	if g.st.State != service.PlanSignedOut && g.st.State != service.PlanReauthRequired {
		return conflict("chatgpt sign-in is not available in this state")
	}
	if g.StartErr != nil {
		return g.StartErr
	}
	g.st.State, g.st.Problem = service.PlanSigningIn, service.PlanProblemNone
	return nil
}

func (g *ChatGPT) CancelSignIn() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, "CancelSignIn")
	if g.st.State == service.PlanSigningIn {
		g.st.State = service.PlanSignedOut
	}
}

func (g *ChatGPT) SignOut(_ context.Context, forgetAccount bool) (service.ChatGPTSignOutResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, "SignOut")
	g.LastForget = forgetAccount
	if g.st.State == service.PlanSignedOut {
		return service.ChatGPTSignOutResult{}, conflict("chatgpt is already signed out")
	}
	if g.SignOutErr != nil {
		return service.ChatGPTSignOutResult{}, g.SignOutErr
	}
	g.st = service.ChatGPTStatus{State: service.PlanSignedOut}
	return g.SignOutRes, nil
}

func (g *ChatGPT) Resume(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, "Resume")
	if g.st.State != service.PlanUsageLimited {
		return conflict("chatgpt is not usage limited")
	}
	g.st.State = service.PlanConnected
	return nil
}

func (g *ChatGPT) DismissWelcome(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, "DismissWelcome")
	g.st.WelcomePending = false
	return nil
}

func (g *ChatGPT) OpenUsage(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, "OpenUsage")
	return g.OpenUsageErr
}

func (g *ChatGPT) Models(context.Context) ([]service.PlanModel, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, "Models")
	if g.st.State != service.PlanConnected {
		return nil, conflict("chatgpt is not connected")
	}
	if g.ModelsErr != nil {
		return nil, g.ModelsErr
	}
	return append([]service.PlanModel(nil), g.ModelsList...), nil
}

// Ready は agent_disabled の detail.reason（本物の PlanGate.Ready と同じ対応）。AgentSettings の偽実装が有効化の可否に使う。
func (g *ChatGPT) Ready() (string, bool) {
	switch g.State().State {
	case service.PlanSignedOut:
		return domain.AgentDisabledChatGPTNotSignedIn, false
	case service.PlanSigningIn:
		return domain.AgentDisabledChatGPTSigningIn, false
	case service.PlanReauthRequired:
		return domain.AgentDisabledChatGPTReauthRequired, false
	case service.PlanUsageLimited:
		return domain.AgentDisabledChatGPTUsageLimited, false
	}
	return "", true
}

// Codex は gui.Codex の偽実装。
type Codex struct {
	mu sync.Mutex
	st service.CodexStatus

	ModelsList []service.PlanModel
	ModelsErr  error
	InstallErr error
	SignInErr  error
	SignOutErr error
	Calls      []string
	// LastRemoveEngine は直近の SignOut の removeEngine。
	LastRemoveEngine bool
}

// NewCodex は取得前（missing）・signed_out の Codex を返す。
func NewCodex() *Codex {
	return &Codex{
		st: service.CodexStatus{Engine: service.CodexEngineStatus{State: service.CodexEngineMissing, Version: "rust-v0.164.0"}, State: service.PlanSignedOut},
		ModelsList: []service.PlanModel{
			{Slug: "gpt-6.1-terra", DisplayName: "GPT-6.1 Terra"}, {Slug: "gpt-6.1-codex", DisplayName: "GPT-6.1 Codex"},
		},
	}
}

// Set は状態を直接設定する。
func (x *Codex) Set(st service.CodexStatus) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.st = st
}

// State は現在の状態を返す（検査用）。
func (x *Codex) State() service.CodexStatus {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.st
}

func (x *Codex) Status() service.CodexStatus { return x.State() }

func (x *Codex) engineReady() bool {
	return x.st.Engine.State == service.CodexEngineInstalled || x.st.Engine.State == service.CodexEngineOutdated
}

func (x *Codex) Install(context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Calls = append(x.Calls, "Install")
	if x.st.Engine.State == service.CodexEngineUnsupported || x.st.Engine.State == service.CodexEngineInstalling {
		return conflict("codex engine cannot be installed in this state")
	}
	if x.InstallErr != nil {
		return x.InstallErr
	}
	x.st.Engine.State, x.st.Engine.Progress, x.st.Engine.Error = service.CodexEngineInstalling, 0, ""
	return nil
}

func (x *Codex) CancelInstall() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Calls = append(x.Calls, "CancelInstall")
	if x.st.Engine.State == service.CodexEngineInstalling {
		x.st.Engine.State, x.st.Engine.Progress = service.CodexEngineMissing, 0
	}
}

func (x *Codex) StartSignIn(context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Calls = append(x.Calls, "StartSignIn")
	if !x.engineReady() || (x.st.State != service.PlanSignedOut && x.st.State != service.PlanReauthRequired) {
		return conflict("codex sign-in is not available in this state")
	}
	if x.SignInErr != nil {
		return x.SignInErr
	}
	x.st.State, x.st.Problem = service.PlanSigningIn, service.PlanProblemNone
	return nil
}

func (x *Codex) CancelSignIn(context.Context) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Calls = append(x.Calls, "CancelSignIn")
	if x.st.State == service.PlanSigningIn {
		x.st.State = service.PlanSignedOut
	}
}

func (x *Codex) SignOut(_ context.Context, removeEngine bool) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Calls = append(x.Calls, "SignOut")
	x.LastRemoveEngine = removeEngine
	if x.st.State == service.PlanSignedOut && !removeEngine {
		return conflict("codex is already signed out")
	}
	if x.SignOutErr != nil {
		return x.SignOutErr
	}
	x.st.State, x.st.AccountEmail, x.st.PlanType, x.st.Problem = service.PlanSignedOut, "", "", service.PlanProblemNone
	if removeEngine {
		x.st.Engine = service.CodexEngineStatus{State: service.CodexEngineMissing, Version: x.st.Engine.Version}
	}
	return nil
}

func (x *Codex) Resume(context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Calls = append(x.Calls, "Resume")
	if x.st.State != service.PlanUsageLimited {
		return conflict("codex is not usage limited")
	}
	x.st.State = service.PlanConnected
	return nil
}

func (x *Codex) Models(context.Context) ([]service.PlanModel, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Calls = append(x.Calls, "Models")
	if x.st.State != service.PlanConnected {
		return nil, conflict("codex is not connected")
	}
	if x.ModelsErr != nil {
		return nil, x.ModelsErr
	}
	return append([]service.PlanModel(nil), x.ModelsList...), nil
}

// Ready は agent_disabled の detail.reason（本物の PlanGate.Ready と同じ対応）。
func (x *Codex) Ready() (string, bool) {
	st := x.State()
	switch {
	case st.Engine.State == service.CodexEngineUnsupported:
		return domain.AgentDisabledCodexUnsupported, false
	case !x.ready(st):
		return domain.AgentDisabledCodexEngineMissing, false
	}
	switch st.State {
	case service.PlanSignedOut:
		return domain.AgentDisabledCodexNotSignedIn, false
	case service.PlanSigningIn:
		return domain.AgentDisabledCodexSigningIn, false
	case service.PlanReauthRequired:
		return domain.AgentDisabledCodexReauthRequired, false
	case service.PlanUsageLimited:
		return domain.AgentDisabledCodexUsageLimited, false
	}
	return "", true
}

func (x *Codex) ready(st service.CodexStatus) bool {
	return st.Engine.State == service.CodexEngineInstalled || st.Engine.State == service.CodexEngineOutdated
}

// ---- AgentSettings の接続方式・モデル ----

// ConnectionCall は SetConnection の直近の引数（検査用）。
type ConnectionCall struct {
	Conn      domain.AgentConnection
	Confirmed bool
	Reason    string
}

// SetConnection は本物と同じ規則: 定義外は ErrInvalidArgument、openai_api_key は confirmed が真でなければ ErrInvalidArgument・
// キー未登録は CodeConflict。保存するのは接続方式だけ（有効・同意は変えない）。
func (a *AgentSettings) SetConnection(_ context.Context, conn domain.AgentConnection, confirmed bool, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, "SetConnection")
	a.LastConnection = ConnectionCall{Conn: conn, Confirmed: confirmed, Reason: reason}
	if !conn.Valid() {
		return domain.ErrInvalidArgument
	}
	if conn == domain.ConnectionOpenAIAPIKey {
		if !confirmed {
			return domain.ErrInvalidArgument
		}
		if !a.view.OpenAIKeyRegistered {
			return conflict("openai api key is not registered")
		}
	}
	if a.SetConnectionErr != nil {
		return a.SetConnectionErr
	}
	a.view.Connection = conn
	return nil
}

// SavePlanModel は接続方式ごとのモデルを保存する（一覧との照合は GUI）。空は ValidationError。
func (a *AgentSettings) SavePlanModel(_ context.Context, conn domain.AgentConnection, model string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, "SavePlanModel")
	if model == "" {
		return &service.ValidationError{Fields: map[string]string{service.SettingChatGPTModel: "モデルを指定してください。"}}
	}
	switch conn {
	case domain.ConnectionChatGPT:
		a.view.ChatGPTModel = model
	case domain.ConnectionChatGPTCodex:
		a.view.CodexModel = model
	default:
		return domain.ErrInvalidArgument
	}
	return nil
}

// SetConnectionDirect は接続方式を確認なしで直接設定する（テスト・デモの初期状態用）。
func (a *AgentSettings) SetConnectionDirect(conn domain.AgentConnection) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.view.Connection = conn
}

// readyFor は選んだ接続方式で run_task を受け付けられるか。受け付けられなければ agent_disabled の detail.reason。
func (a *AgentSettings) readyFor() (string, bool) {
	switch a.view.Connection {
	case domain.ConnectionChatGPT:
		if a.ChatGPT != nil {
			return a.ChatGPT.Ready()
		}
	case domain.ConnectionChatGPTCodex:
		if a.Codex != nil {
			return a.Codex.Ready()
		}
	}
	if !a.view.OpenAIKeyRegistered {
		return domain.AgentDisabledOpenAIKeyMissing, false
	}
	return "", true
}
