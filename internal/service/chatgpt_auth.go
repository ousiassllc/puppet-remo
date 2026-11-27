package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは公式の Sign in with ChatGPT（接続方式 chatgpt。FR-G25〜G30・#247）の認可と接続の状態の唯一の持ち主 ChatGPTAuthService。
//   - chatgpt_auth.go          設定・状態・窓口（Status・Ready・Resume・使用量の上限・モデル一覧）
//   - chatgpt_auth_signin.go   サインイン（FR-G28）
//   - chatgpt_auth_refresh.go  更新・認可切れ（FR-G29 (2)〜(5)）
//   - chatgpt_auth_signout.go  解除（FR-G29 (6)）
//   - chatgpt_oauth.go・chatgpt_callback.go・chatgpt_idtoken.go・chatgpt_tokens.go  OAuth の部品・受信口・ID トークン・トークンの保管
//
// トークン・認可コード・state・nonce・メール・sub は、ログ・監査・エラーに出さない（NFR-S13）。起動時はネットワークを使わず、
// 保存済みのトークンがあれば connected にする（FR-G29 (5)）。

// 監査のイベント名（kind = agent。docs/architecture/er.md のカタログ）。
const (
	chatgptSignInStartAuditEvent    = "agent.chatgpt.signin_start"
	chatgptSignInAuditEvent         = "agent.chatgpt.signin"
	chatgptSignOutAuditEvent        = "agent.chatgpt.signout"
	chatgptResumeAuditEvent         = "agent.chatgpt.resume"
	chatgptReauthAuditEvent         = "agent.chatgpt.reauth_required"
	chatgptUsageLimitAuditEvent     = "agent.chatgpt.usage_limit" // AgentService が使用量の上限のタスクごとに記録する（task_id を持つため）
	chatgptUsageLimitAuditErrorCode = "subscription_sharing_usage_limit_exceeded"
)

// 既定値。
const (
	defaultChatGPTSignInTimeout = 5 * time.Minute
)

// ChatGPTAuthConfig は NewChatGPTAuth の設定。Settings・Secrets・Browser・Clock は必須。
type ChatGPTAuthConfig struct {
	Settings repository.Settings
	Secrets  platform.SecretStore
	Browser  platform.BrowserOpener
	Audit    AuditRecorder // nil なら記録しない
	Bus      *Bus          // nil なら通知しない
	Clock    domain.Clock
	Logger   *slog.Logger // nil なら slog.Default()
	// CancelTasks は実行中の chatgpt 経路のタスクを中断し、その ID（無ければ空）を返す（解除。FR-G29 (6) ①）。nil 可。
	CancelTasks func(reason string) string

	// AuthBaseURL・APIBaseURL はテスト用の差し替え。空は https://auth.openai.com / https://api.openai.com/v1。
	AuthBaseURL, APIBaseURL string
	// HTTPClient は認可サーバーと API への通信。nil ならリダイレクトを辿らない既定のクライアント。
	HTTPClient *http.Client
	// Listen はコールバックの受信口を開く（テストはループバックの偽を使う）。nil なら net.Listen。
	Listen func(network, addr string) (net.Listener, error)
	// SignInTimeout はサインインの待ち。0 なら 5 分。
	SignInTimeout time.Duration
	// Ports は受信口の候補（0 は空きポート）。空なら 1455・1457・0。
	Ports []int
	// Sleep は失効の再試行の間の待ち（1 秒・2 秒）。nil なら実時間。テストは待たずに記録するものを注入する。
	Sleep func(ctx context.Context, d time.Duration) error
}

// ChatGPTAuthService は ChatGPTAuth の実装。
type ChatGPTAuthService struct {
	cfg      ChatGPTAuthConfig
	log      *slog.Logger
	tokens   chatgptTokenStore
	authBase string
	apiBase  string
	jwks     chatgptJWKSCache

	baseCtx    context.Context
	baseCancel context.CancelFunc
	wg         sync.WaitGroup

	// refreshMu は更新・解除を直列化する（回転するリフレッシュトークンの競合を避ける。NFR-S21 (5)）。
	refreshMu chanMutex

	mu           sync.Mutex // 以下を守る
	state        PlanState
	problem      PlanProblem
	email        string
	welcomeShown bool
	rec          *chatgptTokenRecord // 保管済みの記録のメモリ上の写し（真実の源は Secrets）
	gen          uint64              // rec の世代。解除・認可切れ・サインインの確定で進む（古い更新の結果を捨てる）
	signIn       *chatgptSignIn
	closed       bool
}

var _ ChatGPTAuth = (*ChatGPTAuthService)(nil)

// NewChatGPTAuth は ChatGPTAuthService を作る。保存済みのトークンがあれば connected（ネットワークは使わない）。
func NewChatGPTAuth(cfg ChatGPTAuthConfig) (*ChatGPTAuthService, error) {
	if cfg.Settings == nil || cfg.Secrets == nil || cfg.Browser == nil || cfg.Clock == nil {
		return nil, errors.New("service: chatgpt auth requires Settings, Secrets, Browser and Clock")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.AuthBaseURL == "" {
		cfg.AuthBaseURL = chatgptDefaultAuthBase
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = chatgptDefaultAPIBase
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if cfg.Listen == nil {
		cfg.Listen = net.Listen
	}
	if cfg.SignInTimeout <= 0 {
		cfg.SignInTimeout = defaultChatGPTSignInTimeout
	}
	if len(cfg.Ports) == 0 {
		cfg.Ports = []int{1455, 1457, 0}
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &ChatGPTAuthService{
		cfg: cfg, log: cfg.Logger, tokens: chatgptTokenStore{secrets: cfg.Secrets},
		authBase: trimSlash(cfg.AuthBaseURL), apiBase: trimSlash(cfg.APIBaseURL),
		baseCtx: ctx, baseCancel: cancel,
		refreshMu: make(chanMutex, 1), state: PlanSignedOut,
	}
	rec, found, err := s.tokens.load()
	if err != nil {
		s.log.Warn("chatgpt tokens could not be read; starting signed out")
	}
	if found {
		s.rec, s.state = &rec, PlanConnected
	}
	s.email, _ = s.getSetting(ctx, SettingChatGPTAccountEmail)
	s.welcomeShown = s.getBoolSetting(ctx, SettingChatGPTWelcomeShown)
	return s, nil
}

func trimSlash(u string) string {
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}

// Close は進行中のサインインを止め、受信口を閉じ、バックグラウンドの処理の終了を待つ（アプリの終了。冪等）。
func (s *ChatGPTAuthService) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.signIn != nil {
			s.signIn = nil
			s.state = PlanSignedOut
		}
	}
	s.mu.Unlock()
	s.baseCancel()
	s.wg.Wait()
}

// ---- 状態 ----

// Status は FR-H11 のカードの表示。
func (s *ChatGPTAuthService) Status() ChatGPTStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ChatGPTStatus{State: s.state, Problem: s.problem}
	if s.state == PlanConnected || s.state == PlanUsageLimited {
		st.AccountEmail = s.email
	}
	st.WelcomePending = s.state == PlanConnected && !s.welcomeShown
	return st
}

// Ready は run_task を受け付けられるか（FR-G01）。ネットワークを使わない。
func (s *ChatGPTAuthService) Ready() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case PlanConnected:
		return "", true
	case PlanSigningIn:
		return domain.AgentDisabledChatGPTSigningIn, false
	case PlanReauthRequired:
		return domain.AgentDisabledChatGPTReauthRequired, false
	case PlanUsageLimited:
		return domain.AgentDisabledChatGPTUsageLimited, false
	}
	return domain.AgentDisabledChatGPTNotSignedIn, false
}

// MarkUsageLimited は使用量の上限（FR-G30）。connected のときだけ usage_limited にする（メモリだけ）。
func (s *ChatGPTAuthService) MarkUsageLimited() {
	s.mu.Lock()
	changed := s.state == PlanConnected
	if changed {
		s.state = PlanUsageLimited
	}
	s.mu.Unlock()
	if changed {
		s.toast("error", "ChatGPT のプランの使用量の上限に達しました。")
	}
}

// MarkPolicyDenied はワークスペースのポリシーによる拒否。状態は変えず、カードに Codex への案内を出す。
func (s *ChatGPTAuthService) MarkPolicyDenied() {
	s.mu.Lock()
	s.problem = PlanProblemPolicyDenied
	s.mu.Unlock()
}

// Resume は usage_limited を connected に戻す（FR-G30 (3)）。
func (s *ChatGPTAuthService) Resume(ctx context.Context) error {
	s.mu.Lock()
	if s.state != PlanUsageLimited {
		s.mu.Unlock()
		return chatgptConflict("chatgpt is not usage limited")
	}
	s.state, s.problem = PlanConnected, PlanProblemNone
	s.mu.Unlock()
	s.audit(ctx, domain.ActorHost, chatgptResumeAuditEvent, auditOK, "", nil)
	return nil
}

// DismissWelcome は初回の確認を閉じたことを保存する（FR-G28 (7)）。
func (s *ChatGPTAuthService) DismissWelcome(ctx context.Context) error {
	if err := s.putSetting(ctx, SettingChatGPTWelcomeShown, true); err != nil {
		return err
	}
	s.mu.Lock()
	s.welcomeShown = true
	s.mu.Unlock()
	return nil
}

// OpenUsage は使用量の設定（固定の URL）を既定のブラウザで開く（FR-G30 (3)・NFR-S21）。
func (s *ChatGPTAuthService) OpenUsage(ctx context.Context) error {
	if err := s.cfg.Browser.OpenURL(ctx, ChatGPTUsageURL); err != nil {
		return fmt.Errorf("service: open the chatgpt usage page: %w", err)
	}
	return nil
}

// Models はアカウントのモデルの一覧（FR-G26）。必要なら更新してから、同じ Bearer で GET {API}/models を呼ぶ。
func (s *ChatGPTAuthService) Models(ctx context.Context) ([]PlanModel, error) {
	if s.Status().State != PlanConnected {
		return nil, chatgptConflict("chatgpt is not connected")
	}
	token, err := s.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	body, err := s.doHTTP(ctx, http.MethodGet, s.apiBase+"/models", nil, token, chatgptModelsTimeout)
	if err != nil {
		var he *chatgptHTTPError
		if errors.As(err, &he) {
			switch {
			case he.Status == http.StatusUnauthorized || he.Code == "subscription_sharing_invalid_user":
				s.ReportAuthFailure(ctx)
				return nil, fmt.Errorf("service: list chatgpt models: %w", openai.ErrPlanUnauthorized)
			case chatgptIsPolicyDeniedCode(he.Code):
				s.MarkPolicyDenied()
				return nil, fmt.Errorf("service: list chatgpt models: %w", openai.ErrPolicyDenied)
			}
		}
		return nil, fmt.Errorf("service: list chatgpt models: %w", err)
	}
	var out struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &out) != nil {
		return nil, errors.New("service: list chatgpt models: undecodable answer")
	}
	var models []PlanModel
	for _, m := range out.Models {
		if m.Visibility == "list" && m.Slug != "" {
			models = append(models, PlanModel{Slug: m.Slug, DisplayName: m.DisplayName})
		}
	}
	return models, nil
}

// ---- 設定・監査・通知の部品 ----

func chatgptConflict(msg string) error { return domain.NewError(domain.CodeConflict, msg, nil) }

// getSetting は文字列の設定を読む。無い・null は空。
func (s *ChatGPTAuthService) getSetting(ctx context.Context, key string) (string, error) {
	raw, found, err := s.cfg.Settings.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("service: read setting %s: %w", key, err)
	}
	if !found {
		return "", nil
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return "", nil
	}
	return v, nil
}

func (s *ChatGPTAuthService) getBoolSetting(ctx context.Context, key string) bool {
	raw, found, err := s.cfg.Settings.Get(ctx, key)
	if err != nil || !found {
		return false
	}
	var v bool
	return json.Unmarshal(raw, &v) == nil && v
}

// putSetting は設定を保存する。val が nil なら JSON の null（値なし）。
func (s *ChatGPTAuthService) putSetting(ctx context.Context, key string, val any) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("service: encode setting %s: %w", key, err)
	}
	if err := s.cfg.Settings.Set(ctx, key, raw); err != nil {
		return fmt.Errorf("service: save setting %s: %w", key, err)
	}
	return nil
}

// audit は kind = agent の記録を残す。detail・errorCode にトークン・メール・sub・client_id を入れないこと（呼び出し側の責務）。
func (s *ChatGPTAuthService) audit(ctx context.Context, actor domain.ActorKind, event, result, errorCode string, detail map[string]any) {
	if s.cfg.Audit == nil {
		return
	}
	label := hostLabel
	if actor == domain.ActorSystem {
		label = systemLabel
	}
	s.cfg.Audit.Record(ctx, repository.AuditEntry{
		TS: s.cfg.Clock.Now(), Kind: "agent", Event: event, ActorType: actor, ClientLabel: label,
		Result: result, ErrorCode: errorCode, DetailJSON: marshalDetail(detail),
	})
}

// toast は GUI へ通知する（文言に秘密を入れない）。
func (s *ChatGPTAuthService) toast(level, msg string) {
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(Event{Topic: TopicToast, Data: Toast{Level: level, Message: msg}})
	}
}
