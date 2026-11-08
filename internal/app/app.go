// Package app は main の組み立て（DI）と、Wails のシェル（ウィンドウ・トレイ・単一インスタンス）の橋渡しを持つ
// （docs/components/overview.md §7・§9）。
//
// New は OS 依存部（platform）・tailnet・永続化をインターフェースで受け取り、service・mcp・ルーターを組み立てる。
// 偽の platform・tailnet・一時 SQLite でテストできる（組み立て自体は OS に依存しない）。Wails に触れるコードは
// wails_windows.go だけにあり、cmd/puppet-remo が本物の部品を渡して動かす。
//
// 公開面は 2 つに分かれる: GUI ルーター（GUIHandler。Wails の AssetOptions.Handler に渡す。ソケットは開かない）と、
// リモートルーター（tailnet の IP だけで Listen。/mcp・/ws/screen・Web UI の /viewer・/static/*）。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/mcp"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/router"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
	webremote "github.com/ousiassllc/puppet-remo/internal/web/handler/remote"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
	"github.com/ousiassllc/puppet-remo/internal/ws"
)

// Repo は永続化の入口（*repository.DB が満たす）。
type Repo interface {
	Settings() repository.Settings
	Clients() repository.Clients
	Tokens() repository.Tokens
	ConnRequests() repository.ConnRequests
	Audit() repository.Audit
	Thumbs() repository.ThumbStore
	LaunchableApps() repository.LaunchableApps
	LaunchDenylist() repository.LaunchDenylist
	Tasks() repository.Tasks
}

// Platform は OS 依存部。本番は platform/windows、テストは platformtest の偽物。
type Platform struct {
	Capturer  platform.Capturer
	Input     platform.InputInjector
	Windows   platform.WindowManager
	Clipboard platform.Clipboard
	UI        platform.UIAutomation
	// Launcher は許可リストのアプリの起動と存在確認、FilePicker はホスト GUI のファイル選択（M1.5。FR-M19・H12）。
	// Launcher.Launch を呼ぶのは service.Control だけ。
	Launcher   platform.AppLauncher
	FilePicker platform.FilePicker
	Hotkey     platform.Hotkey
	// Power はリース保持中のスリープ・画面オフの抑止（NFR-A05）。
	Power platform.Power
	// Autostart はログオン時の自動起動の登録（FR-S08）。nil なら設定画面に項目を出さない。
	Autostart service.Autostart
	// Installer は署名検証済みの更新のインストーラの起動（FR-H09。#74）。service.Updater が唯一の呼び出し元。
	Installer platform.Installer
	// Secrets は API キー（OpenAI・TypeSafe）を置く資格情報マネージャー（NFR-S14。FR-G14）。エージェント（openai.Model）・高速パス
	// （typesafe.Chooser）は呼び出しのたびにここから読み、ホスト GUI のキー管理（service.AgentSettings）が登録・削除する。
	Secrets platform.SecretStore
}

// UpdateOptions は更新（service.Updater。#74）の設定の上書き。ゼロ値なら本番の既定（埋め込みの公開鍵・GitHub・6 時間ごと）。
// テストが偽の GitHub・テスト専用の鍵・偽のタイマーを差し込むためのもの。
type UpdateOptions struct {
	PublicKey    []byte // nil なら service.EmbeddedUpdatePublicKey()
	APIBase      string
	DownloadBase string
	HTTPClient   *http.Client
	TempDir      string
	StartDelay   time.Duration
	Interval     time.Duration
	IdlePoll     time.Duration
	AfterFunc    service.AfterFunc
}

// Config は New の設定。Repo・Platform（Autostart 以外）・Windows・Watcher・WhoIs・Static・Logs は必須。
type Config struct {
	Repo     Repo
	Platform Platform
	// Windows はウィンドウの生成・破棄（承認ダイアログ。§7）。本番は Wails（WailsHost.Windows）、テストは FakeWindows。
	Windows Windows
	// Watcher と WhoIs は tailscaled の LocalAPI の上に作る（tailnet.NewWatcher / NewWhoIs）。
	Watcher tailnet.Watcher
	WhoIs   tailnet.WhoIs
	// Static は /static/* を配信するハンドラ（static.Handler）。
	Static http.Handler
	// Logs はホスト GUI のログ画面が読むアプリのログ（NewLogReader。FR-H13）。
	Logs service.AppLog

	Clock   tailnet.Clock // nil なら tailnet.SystemClock
	IDs     domain.IDGen  // nil なら ULID
	Version string
	Logger  *slog.Logger // nil なら slog.Default()
	// Update は更新の設定の上書き（テスト用）。
	Update UpdateOptions

	// ListenFunc は Listener の Listen 関数。nil なら net.Listen（テストが差し替える）。
	ListenFunc tailnet.ListenFunc
	// ListenRetry は Listen に失敗したアドレスの再試行間隔。0 以下なら tailnet の既定。
	ListenRetry time.Duration

	// OpenAIModel と TypeSafeChooser は、エージェントが呼ぶ OpenAI・TypeSafe を差し替える口。**テストとオフラインのデモ専用**で、
	// 利用者の設定項目ではない（config・GUI には出さない）。nil なら本物のクライアント（service.NewAgentModel・NewAgentChooser。キーは
	// Platform.Secrets の puppet-remo/openai・puppet-remo/typesafe から呼び出しのたびに読む）を使う。
	OpenAIModel     service.AgentModel
	TypeSafeChooser service.AgentChooser

	// Closers は Shutdown の最後（監査のキューを書き切ったあと）に閉じる。DB・プラットフォームの資源を渡す。
	Closers []io.Closer
}

// KillSwitchControl は、シェル（トレイ）とホットキーが使うキルスイッチの窓口。
type KillSwitchControl interface {
	Active() bool
	Trigger(source string) error
}

// App は組み立て済みのアプリ。New で作り、Start で動かし、Shutdown で止める。
type App struct {
	cfg  Config
	log  *slog.Logger
	bus  *service.Bus
	kill service.HostKillSwitch

	leases   service.LeaseManager
	approval service.Approval              // リース取得の承認待ち（FR-L07）
	dialogs  *approvalDialogs              // 承認待ちごとのダイアログの開閉（FR-H05）
	overlay  *overlayController            // リース保持中の操作中オーバーレイの出し入れ（#69）
	agent    *service.AgentService         // 内蔵エージェント（FR-G。run_task / get_task / cancel_task の実体）
	tasks    service.TaskAdmin             // ホスト GUI のタスク画面・ダッシュボード（FR-H10）
	agentCfg *service.AgentSettingsService // エージェント設定（FR-H11）と、Agent・FastPath・RuleJudge が設定を読む窓口
	authz    service.Authz
	audit    *service.Audit
	admin    service.HostAdmin
	apps     service.AppRegistry
	mcp      *mcp.Server
	viewers  service.ViewerSessions // viewer_id の発行・検証（/viewer と /ws/screen）
	ws       *ws.Handler
	web      *webremote.Handler // リモートの Web UI（/viewer）
	// connStates は未登録デバイスのブラウザに返す待機・拒否・失効のページの選択に使う、接続要求の状態の読み取り専用の参照。
	connStates service.ConnRequestStates
	tracker    *service.StatusTracker
	maint      *service.Maintenance
	listen     *listenSupervisor
	hotkey     *KillSwitchHotkey
	settings   service.HostSettingsService
	updater    service.Updater        // 更新の確認・適用（FR-H09）
	quit       atomic.Pointer[func()] // アプリを終える関数（Wails の起動後に SetQuit で入る）

	gui    http.Handler
	remote http.Handler

	port       atomic.Int32 // 設定 server.port の今の値（Host 検査と Listener がリクエスト・Listen のたびに読む）
	initialKey string       // 起動時のホットキーの設定値

	mu      sync.Mutex
	cancel  context.CancelFunc
	running sync.WaitGroup
	started bool
	stopped bool
}

// New は依存先を組み立てる。ネットワークには触れず、goroutine は監査の書き込みだけを起動する（動かすのは Start）。
// 組み立ての順は NewKillSwitch → NewLeaseManager → BindLeases（キルスイッチが取得時に参照し、発動時にリースを剥奪する）。
func New(ctx context.Context, cfg Config) (*App, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = tailnet.SystemClock
	}
	if cfg.IDs == nil {
		cfg.IDs = NewULIDGen()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	a := &App{cfg: cfg, log: cfg.Logger, bus: service.NewBus()}
	if err := a.build(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

func (c Config) validate() error {
	p := c.Platform
	switch {
	case c.Repo == nil:
		return errors.New("app: Repo is required")
	case p.Capturer == nil, p.Input == nil, p.Windows == nil, p.Clipboard == nil, p.UI == nil, p.Launcher == nil, p.FilePicker == nil,
		p.Hotkey == nil, p.Power == nil, p.Installer == nil, p.Secrets == nil:
		return errors.New("app: Platform requires Capturer, Input, Windows, Clipboard, UI, Launcher, FilePicker, Hotkey, Power, Installer and Secrets")
	case c.Windows == nil:
		return errors.New("app: Windows is required")
	case c.Watcher == nil, c.WhoIs == nil:
		return errors.New("app: Watcher and WhoIs are required")
	case c.Static == nil:
		return errors.New("app: Static is required")
	case c.Logs == nil:
		return errors.New("app: Logs is required")
	}
	return nil
}

func (a *App) build(ctx context.Context) error {
	cfg := a.cfg
	settings := cfg.Repo.Settings()

	// 接続要求の変化を GUI へ通知する。Authz と HostAdmin の両方へ同じラッパーを渡す。
	connRequests := service.NewNotifyingConnRequests(cfg.Repo.ConnRequests(), a.bus)

	// 保守は、サムネイルを保存するたびの容量の確認（PurgeThumbsOverCap）を Audit へ渡すため、Audit より先に作る。
	var err error
	a.maint, err = service.NewMaintenance(service.MaintenanceConfig{
		ConnRequests: connRequests, Audit: cfg.Repo.Audit(), Thumbs: cfg.Repo.Thumbs(), Tasks: cfg.Repo.Tasks(), Settings: settings,
		Clock: cfg.Clock, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("maintenance: %w", err)
	}

	audit, err := service.NewAudit(service.AuditConfig{
		Store: cfg.Repo.Audit(), Clock: cfg.Clock, Logger: a.log,
		Thumbs: cfg.Repo.Thumbs(), IDs: cfg.IDs, Settings: settings, PurgeOverCap: a.maint.PurgeThumbsOverCap,
	})
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	a.audit = audit

	killMgr, err := service.NewKillSwitch(ctx, service.KillSwitchConfig{Settings: settings, Clock: cfg.Clock, Audit: audit, Logger: a.log})
	if err != nil {
		return fmt.Errorf("kill switch: %w", err)
	}
	idle, err := service.LeaseIdleTimeout(ctx, settings)
	if err != nil {
		return fmt.Errorf("lease idle timeout: %w", err)
	}
	// 承認待ちの要求（FR-L07）。要求のたびに Bus へ approval を出し、終わるたびに approval-closed を出す。ダイアログを開閉するのは
	// 購読側の approvalDialogs（Start が動かす）、Resolve を呼ぶのは GUI ルーター（/approvals/{id}/allow・deny）。
	a.approval, err = service.NewApproval(service.ApprovalConfig{Clock: cfg.Clock, IDs: cfg.IDs, Bus: a.bus})
	if err != nil {
		return fmt.Errorf("approval: %w", err)
	}
	a.dialogs = newApprovalDialogs(cfg.Windows, a.approval, a.bus, a.log)
	a.leases, err = service.NewLeaseManager(service.LeaseConfig{
		Clock: cfg.Clock, IDs: cfg.IDs, IdleTimeout: idle, Audit: audit, Kill: killMgr, Power: cfg.Platform.Power, Logger: a.log,
		Approval: a.approval, Clients: cfg.Repo.Clients(), Bus: a.bus, Windows: cfg.Platform.Windows,
	})
	if err != nil {
		return fmt.Errorf("lease manager: %w", err)
	}
	killMgr.BindLeases(a.leases)

	p := cfg.Platform
	// 許可リストの管理。書き込みは GUI だけが呼ぶ（mcp・リモートルーターには渡さない。NFR-S20）。
	a.apps, err = service.NewAppRegistry(service.AppRegistryConfig{
		Apps: cfg.Repo.LaunchableApps(), Denied: cfg.Repo.LaunchDenylist(), Settings: settings,
		Launcher: p.Launcher, Picker: p.FilePicker, Audit: audit, Clock: cfg.Clock, IDs: cfg.IDs,
	})
	if err != nil {
		return fmt.Errorf("app registry: %w", err)
	}
	// エージェント設定。Cancel（無効化・OpenAI キーの削除でタスクを中断）は Agent の組み立て後に結ばれる（a.agent は Start の前に必ず入る）。
	a.agentCfg, err = service.NewAgentSettings(service.AgentSettingsConfig{
		Settings: settings, Secrets: p.Secrets, Audit: audit, Clock: cfg.Clock, Logger: a.log,
		Cancel: func(reason string) { a.agent.CancelAll(reason) },
	})
	if err != nil {
		return fmt.Errorf("agent settings: %w", err)
	}
	// agent のアクションだけが Judge（RuleJudge。失敗・2 秒の超過は承認を求める fail-closed）と承認（judge_action）を通る。
	control, err := service.NewControl(service.ControlConfig{
		Capturer: p.Capturer, Input: p.Input, Windows: p.Windows, Clipboard: p.Clipboard, UI: p.UI,
		Launcher: p.Launcher, Apps: a.apps,
		Leases: a.leases, Kill: killMgr, Settings: settings, Audit: audit, Clock: cfg.Clock, Logger: a.log,
		Judge:    service.FailClosed(service.Chain(service.NewRuleJudge(a.agentCfg)), judgeTimeout),
		Approval: a.approval,
	})
	if err != nil {
		return fmt.Errorf("control: %w", err)
	}
	if err := a.buildAgent(control, killMgr); err != nil {
		return err
	}
	// agent のオーバーレイの 2 行目（指示の要約）は TaskAdmin から読む。
	a.overlay = newOverlayController(cfg.Windows, a.leases, p.Capturer, a.bus, a.tasks, a.log)

	a.authz, err = service.NewAuthz(service.AuthzConfig{
		WhoIs: cfg.WhoIs, Clients: cfg.Repo.Clients(), Tokens: cfg.Repo.Tokens(), ConnRequests: connRequests,
		Audit: audit, Clock: cfg.Clock, IDs: cfg.IDs, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("authz: %w", err)
	}

	a.mcp, err = mcp.New(mcp.Config{
		Control: control, Leases: a.leases, Audit: audit, Agent: a.agent, Version: cfg.Version, Logger: a.log,
		// 連鎖（web/middleware）が認証した主体を MCP サーバーへ渡す。mcp は web を import できない。
		Principal:        func(r *http.Request) (service.Principal, bool) { return middleware.PrincipalFrom(r.Context()) },
		OnSessionsChange: func() { a.bus.Publish(service.Event{Topic: service.TopicSessions}) },
	})
	if err != nil {
		return fmt.Errorf("mcp: %w", err)
	}
	// キルスイッチの発動で全 MCP セッションを閉じる（各セッションのリースは切断の検知で解放される）。
	killMgr.RegisterCloser("mcp-sessions", a.mcp.CloseSessions)
	a.kill = service.NewNotifyingKillSwitch(killMgr, a.bus)

	// 画面配信とビューアのセッション（viewer_id）。キルスイッチの発動で全ビューアを閉じるクローザー（web-sessions）は
	// NewViewerSessions が登録する。WebSocket は閉じた理由から閉じコード 4503 で閉じる。
	streamer, err := service.NewStreamer(service.StreamerConfig{
		Capturer: p.Capturer, Clock: cfg.Clock, IDs: cfg.IDs, Settings: settings, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("streamer: %w", err)
	}
	a.viewers, err = service.NewViewerSessions(service.ViewerSessionsConfig{
		Streamer: streamer, Leases: a.leases, Kill: killMgr, Clock: cfg.Clock, IDs: cfg.IDs,
	})
	if err != nil {
		return fmt.Errorf("viewer sessions: %w", err)
	}
	a.ws, err = ws.New(ws.Config{
		Sessions: a.viewers, Control: control, Leases: a.leases, Events: a.bus,
		// 連鎖（web/middleware）が認可したクライアントを渡す。ws は web を import できない。
		ClientFrom: middleware.ClientFrom,
		Logger:     a.log,
	})
	if err != nil {
		return fmt.Errorf("websocket: %w", err)
	}

	a.admin, err = service.NewHostAdmin(service.HostAdminConfig{
		Clients: cfg.Repo.Clients(), Tokens: cfg.Repo.Tokens(), ConnRequests: connRequests, Authz: a.authz,
		// クライアントの削除・無効化・トークンの失効で、MCP のセッションと WebSocket のビューアの両方を閉じる。
		// 削除・無効化したクライアントの実行中のタスクも中断する（FR-A09・G10）。
		Leases: a.leases, Sessions: sessionClosers{a.mcp, a.viewers}, Tasks: a.agent, Audit: audit, Bus: a.bus, Clock: cfg.Clock, IDs: cfg.IDs, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("host admin: %w", err)
	}

	a.settings, err = service.NewHostSettings(service.HostSettingsConfig{
		Settings: settings, Autostart: p.Autostart, Audit: audit, Clock: cfg.Clock, Logger: a.log, OnChange: a.onSettingsChange,
	})
	if err != nil {
		return fmt.Errorf("host settings: %w", err)
	}
	initial, err := a.settings.Load(ctx)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	a.port.Store(int32(initial.Port))
	a.initialKey = initial.KillSwitchHotkey
	a.hotkey = NewKillSwitchHotkey(p.Hotkey, func() { a.triggerKillSwitch("hotkey") })

	// 更新（FR-H09）。確認は Start が定期的に始める。適用の可否はリース・MCP の接続（ダッシュボードと同じ一覧）で見る。
	// 適用の開始後にアプリを終える（quit は Wails の起動後に SetQuit で入る）。
	u := cfg.Update
	if u.PublicKey == nil {
		u.PublicKey = service.EmbeddedUpdatePublicKey()
	}
	a.updater, err = service.NewUpdater(service.UpdaterConfig{
		Settings: settings, Audit: audit, Clock: cfg.Clock, Installer: p.Installer, Leases: a.leases, Sessions: a.mcp, RunningTasks: a.agent.RunningTasks, Bus: a.bus,
		Quit: a.requestQuit, CurrentVersion: cfg.Version, PublicKey: u.PublicKey, APIBase: u.APIBase, DownloadBase: u.DownloadBase,
		HTTPClient: u.HTTPClient, TempDir: u.TempDir, StartDelay: u.StartDelay, Interval: u.Interval, IdlePoll: u.IdlePoll,
		AfterFunc: u.AfterFunc, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("updater: %w", err)
	}

	a.connStates, err = service.NewConnRequestStates(connRequests, cfg.Clock)
	if err != nil {
		return fmt.Errorf("connection request states: %w", err)
	}
	a.web, err = webremote.New(webremote.Deps{
		Sessions: a.viewers, Leases: a.leases, Control: control, Settings: a.settings, Tasks: a.tasks, Clock: cfg.Clock, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("web ui: %w", err)
	}

	return a.buildRouters(killMgr)
}

func (a *App) buildRouters(kill service.KillSwitchState) error {
	cfg := a.cfg

	// リモートルーター: tailnet の IP だけで Listen する。配信するのは /mcp・/ws/screen・Web UI（/viewer）・/static/*。
	remote, err := router.NewRemote(router.RemoteConfig{
		Chain: middleware.RemoteConfig{
			Hosts: a.allowedHosts, KillSwitch: kill, WhoIs: cfg.WhoIs, Authz: a.authz, ConnStates: a.connStates,
		},
		MCP:    a.mcp.Handler(),
		WS:     a.ws,
		Web:    a.web,
		Static: cfg.Static,
	})
	if err != nil {
		return fmt.Errorf("remote router: %w", err)
	}
	a.remote = remote

	a.listen = newListenSupervisor(func() tailnet.Listener {
		return tailnet.NewListener(tailnet.ListenerConfig{
			Watcher: cfg.Watcher, Clock: cfg.Clock, RetryInterval: cfg.ListenRetry, ListenFunc: cfg.ListenFunc,
			OnChange: func(st tailnet.ListenState) { logListenState(a.log, st); a.tracker.Notify() },
		})
	}, a.remote, func() int { return int(a.port.Load()) }, a.log)

	a.tracker = service.NewStatusTracker(service.StatusTrackerConfig{
		Watcher: cfg.Watcher, Listener: a.listen, Port: func() int { return int(a.port.Load()) }, Bus: a.bus, Logger: a.log,
	})

	// GUI ルーター: プロセス内だけ（ソケットを開かない）。Wails の AssetOptions.Handler に渡す。
	h, err := gui.New(gui.Deps{
		Status: a.tracker, Sessions: a.mcp, Leases: a.leases, KillSwitch: a.kill, Admin: a.admin, Approval: a.approval,
		Settings: a.settings, Apps: a.apps, Logs: cfg.Logs, Updates: a.updater, Tasks: a.tasks, AgentSettings: a.agentCfg, Audit: service.NewAuditReader(cfg.Repo.Audit(), cfg.Repo.Thumbs()), Events: a.bus, Clock: cfg.Clock, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("gui handler: %w", err)
	}
	a.gui, err = router.NewGUI(router.GUIConfig{Handler: h, Static: cfg.Static})
	if err != nil {
		return fmt.Errorf("gui router: %w", err)
	}
	return nil
}

// judgeTimeout は Judge の判定の上限。超えたら承認を求める（fail-closed。FR-G12）。
const judgeTimeout = 2 * time.Second

// buildAgent は内蔵エージェント（Agent・FastPath・TaskAdmin）を組み立てる。OpenAI・TypeSafe のクライアントは組み立てるだけで、
// 通信も goroutine も起動しない（TypeSafe は高速パスが有効なタスクの最初のステップまで通信しない。docs §9）。
// 組み立てと同時に、キルスイッチのクローザー agent（実行中のタスクを中断する）を登録する。
func (a *App) buildAgent(control service.Control, kill *service.KillSwitchManager) error {
	cfg, p := a.cfg, a.cfg.Platform
	model := cfg.OpenAIModel
	if model == nil {
		model = service.NewAgentModel(p.Secrets)
	}
	chooser := cfg.TypeSafeChooser
	if chooser == nil {
		chooser = service.NewAgentChooser(p.Secrets)
	}
	fast, err := service.NewFastPath(service.FastPath{
		UI: p.UI, Control: control, Chooser: chooser, Settings: a.agentCfg, Disabler: a.agentCfg, Audit: a.audit, Bus: a.bus, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("fast path: %w", err)
	}
	a.agent, err = service.NewAgent(service.AgentServiceConfig{
		Tasks: cfg.Repo.Tasks(), Clients: cfg.Repo.Clients(), Settings: a.agentCfg, Control: control, Leases: a.leases, Kill: kill,
		Approval: a.approval, Model: model, Fast: fast, Audit: a.audit, Bus: a.bus, Disabler: a.agentCfg, Clock: cfg.Clock, IDs: cfg.IDs, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	kill.RegisterCloser("agent", func(context.Context) error { a.agent.CancelAll("kill_switch"); return nil })
	a.tasks, err = service.NewTaskAdmin(service.TaskAdminConfig{Tasks: cfg.Repo.Tasks(), Steps: cfg.Repo.Audit(), Cancel: a.agent, Audit: a.audit})
	if err != nil {
		return fmt.Errorf("task admin: %w", err)
	}
	return nil
}

// sessionClosers は複数のセッションの表（MCP・WebSocket のビューア）を、1 つの service.SessionCloser として扱う。
type sessionClosers []service.SessionCloser

func (cs sessionClosers) CloseClient(clientID string, kind domain.ActorKind) {
	for _, c := range cs {
		c.CloseClient(clientID, kind)
	}
}

func (cs sessionClosers) CloseToken(tokenID string) {
	for _, c := range cs {
		c.CloseToken(tokenID)
	}
}
