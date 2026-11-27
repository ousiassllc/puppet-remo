package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex"
	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/catalog"
	"github.com/ousiassllc/puppet-remo/internal/codex/engine"
	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは Codex の app-server 経由の接続方式（chatgpt-codex。#247）のエンジン・ログイン・接続の状態の唯一の持ち主 CodexAuthService。
//   - codex_auth.go         設定・状態・窓口（Status・Ready・使用量の上限・認可切れ・Model・Close）
//   - codex_auth_engine.go  エンジンの取得・取り消し（固定した版。SHA-256・サイズの検証）
//   - codex_auth_signin.go  ログイン・ログアウト・再開・モデル一覧・アカウントの読み取り
//
// アプリは Codex のトークンを読まず、保存せず、持たない（資格情報は app-server が OS の資格情報マネージャーへ置く）。
// トークン・認可 URL・メール・planType は、ログ・監査・エラーに出さない（NFR-S13）。

// 監査のイベント名（kind = agent。docs/architecture/er.md のカタログ）。
const (
	codexInstallAuditEvent     = "agent.codex.install"
	codexSignInStartAuditEvent = "agent.codex.signin_start"
	codexSignInAuditEvent      = "agent.codex.signin"
	codexSignOutAuditEvent     = "agent.codex.signout"
	codexResumeAuditEvent      = "agent.codex.resume"
)

// 既定値。
const (
	defaultCodexSignInTimeout = 5 * time.Minute
	codexCancelTasksReason    = "codex_signout"
	codexCleanupTimeout       = 5 * time.Second
	codexHomeDirName          = "codex-home"
	codexWorkDirName          = "codex-work"
)

// CodexAuthConfig は NewCodexAuth の設定。Settings・Browser・Clock・DataDir は必須。
type CodexAuthConfig struct {
	// EngineDir はエンジンの置き場所（app が `<実行ファイルのディレクトリ>\codex` を渡す）。空ならこの環境では取得しない（unsupported）。
	EngineDir string
	// DataDir はアプリのデータディレクトリ（`%LOCALAPPDATA%\puppet-remo`）。専用の CODEX_HOME（codex-home）と
	// 空の作業ディレクトリ（codex-work）をその下に置く。相対パスは絶対パスにする。
	DataDir  string
	Browser  platform.BrowserOpener
	Settings repository.Settings
	Audit    AuditRecorder // nil なら記録しない
	Bus      *Bus          // nil なら通知しない
	Clock    domain.Clock
	Logger   *slog.Logger // nil なら slog.Default()
	// CancelTasks は実行中の Codex 経路のタスクを中断し、その ID（無ければ空）を返す（ログアウト）。nil 可。
	CancelTasks func(reason string) string

	// AppVersion は app-server へ名乗る clientInfo.version。空は "dev"。
	AppVersion string
	// SignInTimeout はログインの待ち。0 なら 5 分。
	SignInTimeout time.Duration

	// 以下はテスト用の差し替え口。
	// Catalog は固定した版（nil なら埋め込みのカタログ）。
	Catalog *catalog.Catalog
	// HTTPClient はエンジンのダウンロードに使うクライアント（nil なら既定。https 以外へのリダイレクトは常に拒否する）。
	HTTPClient *http.Client
	// StartProcess は app-server の起動（nil なら l.Start()）。
	StartProcess func(l appserver.Launch) (appserver.Process, error)
	// GOOS・GOARCH はエンジンの取得先の選択（空なら実行環境）。
	GOOS, GOARCH string
}

// codexEngineState のメモリ上の状態。
type codexEngineInfo struct {
	state    CodexEngineState
	progress int
	err      string
}

// CodexAuthService は CodexAuth の実装。
type CodexAuthService struct {
	cfg  CodexAuthConfig
	log  *slog.Logger
	cat  catalog.Catalog
	inst *engine.Installer // サポート外の環境では nil
	home string            // 専用の CODEX_HOME
	work string            // 専用の空の作業ディレクトリ
	srv  *appserver.Server
	mdl  *codex.Model

	baseCtx    context.Context
	baseCancel context.CancelFunc
	wg         sync.WaitGroup

	mu         sync.Mutex
	eng        codexEngineInfo
	state      PlanState
	prevState  PlanState // サインインを始める前の状態（取り消し・失敗で戻す）
	problem    PlanProblem
	email      string
	planType   string
	known      bool // アカウントの状態を app-server から読んだ（または読もうとした）
	refreshing bool
	loginID    string
	signInStop context.CancelFunc
	instStop   context.CancelFunc
	removing   bool // ログアウトに伴う削除の最中（取得・起動を断る）
	closed     bool
}

var _ CodexAuth = (*CodexAuthService)(nil)

// NewCodexAuth は CodexAuthService を作る。ネットワークを使わず、app-server も起動しない（接続方式が chatgpt-codex で
// エンジンが導入済みのときだけ、アカウントの状態を背景で読む）。
func NewCodexAuth(cfg CodexAuthConfig) (*CodexAuthService, error) {
	switch {
	case cfg.Settings == nil:
		return nil, errors.New("service: CodexAuth needs Settings")
	case cfg.Browser == nil:
		return nil, errors.New("service: CodexAuth needs a BrowserOpener")
	case cfg.Clock == nil:
		return nil, errors.New("service: CodexAuth needs a Clock")
	case cfg.DataDir == "":
		return nil, errors.New("service: CodexAuth needs DataDir")
	}
	dataDir, err := filepath.Abs(cfg.DataDir) // 相対のままだと、子プロセスの作業ディレクトリで別の場所を指す
	if err != nil {
		return nil, err
	}
	s := &CodexAuthService{cfg: cfg, log: cfg.Logger, cat: catalog.Default(), state: PlanSignedOut}
	if s.log == nil {
		s.log = slog.Default()
	}
	if cfg.Catalog != nil {
		s.cat = *cfg.Catalog
	}
	if s.cfg.GOOS == "" {
		s.cfg.GOOS = runtime.GOOS
	}
	if s.cfg.GOARCH == "" {
		s.cfg.GOARCH = runtime.GOARCH
	}
	if s.cfg.SignInTimeout <= 0 {
		s.cfg.SignInTimeout = defaultCodexSignInTimeout
	}
	if s.cfg.AppVersion == "" {
		s.cfg.AppVersion = "dev"
	}
	s.home = filepath.Join(dataDir, codexHomeDirName)
	s.work = filepath.Join(dataDir, codexWorkDirName)
	s.baseCtx, s.baseCancel = context.WithCancel(context.Background())

	s.eng = codexEngineInfo{state: CodexEngineUnsupported}
	if asset, ok := s.cat.AssetFor(s.cfg.GOOS, s.cfg.GOARCH); ok && cfg.EngineDir != "" {
		engDir, err := filepath.Abs(cfg.EngineDir)
		if err != nil {
			return nil, err
		}
		s.inst = &engine.Installer{
			Layout: engine.Layout{Root: engDir}, Version: s.cat.Tag, Asset: asset, Client: cfg.HTTPClient,
			Now: cfg.Clock.Now,
		}
		s.inst.CleanLeftovers()
		s.eng = codexEngineInfo{state: s.engineStateOnDisk()}
	}

	s.srv = appserver.New(appserver.Options{
		Start: s.startProcess, CodexHome: s.home, Version: s.cfg.AppVersion, Logger: s.log,
	})
	s.srv.SetAccountListener(s.onAccountNotification)
	s.mdl = codex.NewModel(codex.ModelConfig{Server: s.srv, WorkDir: s.work, Logger: s.log})
	s.maybeRefresh(true)
	return s, nil
}

// engineStateOnDisk は導入の記録とファイルから見たエンジンの状態（installed・outdated・missing）。
func (s *CodexAuthService) engineStateOnDisk() CodexEngineState {
	st := s.inst.Status()
	switch {
	case st.Installed && st.Outdated:
		return CodexEngineOutdated
	case st.Installed:
		return CodexEngineInstalled
	}
	return CodexEngineMissing
}

// Model は Codex 経路のモデル（openai.Model と openai.TaskCloser）。
func (s *CodexAuthService) Model() openai.Model { return s.mdl }

// errCodexEngineMissing は app-server を起動しようとしたとき、使えるエンジンが無い。
var errCodexEngineMissing = errors.New("codex engine is not installed")

// startProcess は appserver.Server の起動関数。検証済みの固定した版のエンジンだけを、アプリ専用の CODEX_HOME・空の作業
// ディレクトリで起動する。導入・削除の最中は起動しない。
func (s *CodexAuthService) startProcess() (appserver.Process, error) {
	s.mu.Lock()
	busy := s.eng.state == CodexEngineInstalling || s.removing
	s.mu.Unlock()
	if busy || s.inst == nil {
		return nil, errCodexEngineMissing
	}
	exe, ok := s.inst.ExePath()
	if !ok {
		return nil, errCodexEngineMissing
	}
	for _, d := range []string{s.home, s.work} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	l := appserver.Launch{Exe: exe, CodexHome: s.home, WorkDir: s.work, Logger: s.log}
	if s.cfg.StartProcess != nil {
		return s.cfg.StartProcess(l)
	}
	return l.Start()
}

// Status は Codex のカードの表示。app-server を起動しない（エンジンが導入済みで状態が未取得なら、背景で読み始める）。
func (s *CodexAuthService) Status() CodexStatus {
	s.maybeRefresh(false)
	s.mu.Lock()
	defer s.mu.Unlock()
	return CodexStatus{
		Engine: CodexEngineStatus{State: s.eng.state, Version: s.cat.Tag, Progress: s.eng.progress, Error: s.eng.err},
		State:  s.state, AccountEmail: s.email, PlanType: s.planType, Problem: s.problem,
	}
}

// Ready は run_task を受け付けられるか。受け付けられなければ agent_disabled の detail.reason。ネットワークを使わない。
func (s *CodexAuthService) Ready() (string, bool) {
	s.maybeRefresh(false)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.eng.state == CodexEngineUnsupported:
		return domain.AgentDisabledCodexUnsupported, false
	case s.eng.state != CodexEngineInstalled:
		return domain.AgentDisabledCodexEngineMissing, false
	}
	switch s.state {
	case PlanConnected:
		return "", true
	case PlanSigningIn:
		return domain.AgentDisabledCodexSigningIn, false
	case PlanReauthRequired:
		return domain.AgentDisabledCodexReauthRequired, false
	case PlanUsageLimited:
		return domain.AgentDisabledCodexUsageLimited, false
	}
	return domain.AgentDisabledCodexNotSignedIn, false
}

// MarkUsageLimited は使用量の上限: connected を usage_limited にする（メモリだけ）。
func (s *CodexAuthService) MarkUsageLimited() {
	s.mu.Lock()
	changed := s.state == PlanConnected
	if changed {
		s.state = PlanUsageLimited
	}
	s.mu.Unlock()
	if changed {
		s.toast("error", "Codex の使用量の上限に達しました。上限が戻ったら「再開」を押してください")
	}
}

// ReportAuthFailure はモデルの呼び出しの認可エラー: app-server のアカウントを読み直し（更新は頼まない）、
// ログインが無ければ reauth_required にする。
func (s *CodexAuthService) ReportAuthFailure(ctx context.Context) {
	s.mu.Lock()
	active := s.state == PlanConnected || s.state == PlanUsageLimited
	s.mu.Unlock()
	if !active {
		return
	}
	if err := s.readAccount(ctx, true); err != nil {
		s.log.Warn("codex account check failed", "err_kind", errKind(err))
		// 読めないときも、認可エラーが出たのだから、確認が取れるまで再ログインを案内する。
		s.mu.Lock()
		if s.state == PlanConnected || s.state == PlanUsageLimited {
			s.state = PlanReauthRequired
		}
		s.mu.Unlock()
		s.toast("error", "Codex のログインを確認できませんでした。もう一度ログインしてください")
	}
}

// MarkPolicyDenied は公式の経路のワークスペースのポリシー拒否への反応で、Codex の経路には当てはまらない: 何もしない。
func (s *CodexAuthService) MarkPolicyDenied() {}

// Close は背景の処理を止め、app-server を終わらせる。ctx が先に終わったら、待たずに ctx のエラーを返す。
func (s *CodexAuthService) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.baseCancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	s.srv.Close()
	return err
}

// --- 内部 ---

func (s *CodexAuthService) toast(level, msg string) {
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(Event{Topic: TopicToast, Data: Toast{Level: level, Message: msg}})
	}
}

func (s *CodexAuthService) record(event, result string, detail map[string]any) {
	if s.cfg.Audit == nil {
		return
	}
	s.cfg.Audit.Record(s.baseCtxOrBackground(), repository.AuditEntry{
		TS: s.cfg.Clock.Now(), Kind: "agent", Event: event, ActorType: domain.ActorHost, ClientLabel: hostLabel,
		Result: result, DetailJSON: marshalDetail(detail),
	})
}

// baseCtxOrBackground は監査の記録に使う ctx（Close の後も記録は書く）。
func (s *CodexAuthService) baseCtxOrBackground() context.Context {
	return context.WithoutCancel(s.baseCtx)
}

func conflict(msg string) error { return domain.NewError(domain.CodeConflict, msg, nil) }

// errKind はエラーの種類の短い名前（ログ用。本文・URL・パスを含まない）。
func errKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, appserver.ErrNotOurHome):
		return "not_our_home"
	case errors.Is(err, appserver.ErrStart), errors.Is(err, errCodexEngineMissing):
		return "start"
	}
	return "error"
}

// codexSettingConnection は agent.connection の保存値（読めない・未設定は false）。
func (s *CodexAuthService) connectionIsCodex(ctx context.Context) bool {
	raw, ok, err := s.cfg.Settings.Get(ctx, SettingAgentConnection)
	if err != nil || !ok {
		return false
	}
	var v string
	return json.Unmarshal(raw, &v) == nil && domain.AgentConnection(v) == domain.ConnectionChatGPTCodex
}
