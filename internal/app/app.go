// Package app は main の組み立て（DI）と、Wails のシェル（ウィンドウ・トレイ・単一インスタンス）の橋渡しを持つ
// （docs/components/overview.md §7・§9）。
//
// New は OS 依存部（platform）・tailnet・永続化をインターフェースで受け取り、service・mcp・ルーターを組み立てる。
// 偽の platform・tailnet・一時 SQLite でテストできる（組み立て自体は OS に依存しない）。Wails に触れるコードは
// wails_windows.go だけにあり、cmd/puppet-remo が本物の部品を渡して動かす。
//
// 公開面は 2 つに分かれる: GUI ルーター（GUIHandler。Wails の AssetOptions.Handler に渡す。ソケットは開かない）と、
// リモートルーター（tailnet の IP だけで Listen。M1 は /mcp のみ）。
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
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// Repo は永続化の入口（*repository.DB が満たす）。
type Repo interface {
	Settings() repository.Settings
	Clients() repository.Clients
	Tokens() repository.Tokens
	ConnRequests() repository.ConnRequests
	Audit() repository.Audit
}

// Platform は OS 依存部。本番は platform/windows、テストは platformtest の偽物。
type Platform struct {
	Capturer  platform.Capturer
	Input     platform.InputInjector
	Windows   platform.WindowManager
	Clipboard platform.Clipboard
	UI        platform.UIAutomation
	Hotkey    platform.Hotkey
	// Autostart はログオン時の自動起動の登録（FR-S08）。nil なら設定画面に項目を出さない。
	Autostart service.Autostart
}

// Config は New の設定。Repo・Platform（Autostart 以外）・Watcher・WhoIs・Static は必須。
type Config struct {
	Repo     Repo
	Platform Platform
	// Watcher と WhoIs は tailscaled の LocalAPI の上に作る（tailnet.NewWatcher / NewWhoIs）。
	Watcher tailnet.Watcher
	WhoIs   tailnet.WhoIs
	// Static は /static/* を配信するハンドラ（static.Handler）。
	Static http.Handler

	Clock   tailnet.Clock // nil なら tailnet.SystemClock
	IDs     domain.IDGen  // nil なら ULID
	Version string
	Logger  *slog.Logger // nil なら slog.Default()

	// ListenFunc は Listener の Listen 関数。nil なら net.Listen（テストが差し替える）。
	ListenFunc tailnet.ListenFunc
	// ListenRetry は Listen に失敗したアドレスの再試行間隔。0 以下なら tailnet の既定。
	ListenRetry time.Duration

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
	authz    service.Authz
	audit    *service.Audit
	admin    service.HostAdmin
	mcp      *mcp.Server
	tracker  *service.StatusTracker
	maint    *service.Maintenance
	listen   *listenSupervisor
	hotkey   *KillSwitchHotkey
	settings service.HostSettingsService

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
	case p.Capturer == nil, p.Input == nil, p.Windows == nil, p.Clipboard == nil, p.UI == nil, p.Hotkey == nil:
		return errors.New("app: Platform requires Capturer, Input, Windows, Clipboard, UI and Hotkey")
	case c.Watcher == nil, c.WhoIs == nil:
		return errors.New("app: Watcher and WhoIs are required")
	case c.Static == nil:
		return errors.New("app: Static is required")
	}
	return nil
}

func (a *App) build(ctx context.Context) error {
	cfg := a.cfg
	settings := cfg.Repo.Settings()

	audit, err := service.NewAudit(service.AuditConfig{Store: cfg.Repo.Audit(), Clock: cfg.Clock, Logger: a.log})
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	a.audit = audit

	// 接続要求の変化を GUI へ通知する。Authz と HostAdmin の両方へ同じラッパーを渡す。
	connRequests := service.NewNotifyingConnRequests(cfg.Repo.ConnRequests(), a.bus)

	killMgr, err := service.NewKillSwitch(ctx, service.KillSwitchConfig{Settings: settings, Clock: cfg.Clock, Audit: audit, Logger: a.log})
	if err != nil {
		return fmt.Errorf("kill switch: %w", err)
	}
	idle, err := service.LeaseIdleTimeout(ctx, settings)
	if err != nil {
		return fmt.Errorf("lease idle timeout: %w", err)
	}
	a.leases, err = service.NewLeaseManager(service.LeaseConfig{Clock: cfg.Clock, IDs: cfg.IDs, IdleTimeout: idle, Audit: audit, Kill: killMgr})
	if err != nil {
		return fmt.Errorf("lease manager: %w", err)
	}
	killMgr.BindLeases(a.leases)

	p := cfg.Platform
	control, err := service.NewControl(service.ControlConfig{
		Capturer: p.Capturer, Input: p.Input, Windows: p.Windows, Clipboard: p.Clipboard, UI: p.UI,
		Leases: a.leases, Kill: killMgr, Settings: settings, Audit: audit, Clock: cfg.Clock,
	})
	if err != nil {
		return fmt.Errorf("control: %w", err)
	}

	a.authz, err = service.NewAuthz(service.AuthzConfig{
		WhoIs: cfg.WhoIs, Clients: cfg.Repo.Clients(), Tokens: cfg.Repo.Tokens(), ConnRequests: connRequests,
		Audit: audit, Clock: cfg.Clock, IDs: cfg.IDs, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("authz: %w", err)
	}

	a.mcp, err = mcp.New(mcp.Config{
		Control: control, Leases: a.leases, Version: cfg.Version, Logger: a.log,
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

	a.admin, err = service.NewHostAdmin(service.HostAdminConfig{
		Clients: cfg.Repo.Clients(), Tokens: cfg.Repo.Tokens(), ConnRequests: connRequests, Authz: a.authz,
		Leases: a.leases, Sessions: a.mcp, Audit: audit, Bus: a.bus, Clock: cfg.Clock, IDs: cfg.IDs, Logger: a.log,
	})
	if err != nil {
		return fmt.Errorf("host admin: %w", err)
	}

	a.settings, err = service.NewHostSettings(service.HostSettingsConfig{
		Settings: settings, Autostart: p.Autostart, Audit: audit, Clock: cfg.Clock, OnChange: a.onSettingsChange,
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

	a.maint, err = service.NewMaintenance(service.MaintenanceConfig{ConnRequests: connRequests, Clock: cfg.Clock, Logger: a.log})
	if err != nil {
		return fmt.Errorf("maintenance: %w", err)
	}

	return a.buildRouters(killMgr)
}

func (a *App) buildRouters(kill service.KillSwitchState) error {
	cfg := a.cfg

	// リモートルーター: tailnet の IP だけで Listen する。M1 が配信するのは /mcp だけ。
	remote, err := router.NewRemote(router.RemoteConfig{
		Chain: middleware.RemoteConfig{Hosts: a.allowedHosts, KillSwitch: kill, WhoIs: cfg.WhoIs, Authz: a.authz},
		MCP:   a.mcp.Handler(),
	})
	if err != nil {
		return fmt.Errorf("remote router: %w", err)
	}
	a.remote = remote

	a.listen = newListenSupervisor(func() tailnet.Listener {
		return tailnet.NewListener(tailnet.ListenerConfig{
			Watcher: cfg.Watcher, Clock: cfg.Clock, RetryInterval: cfg.ListenRetry, ListenFunc: cfg.ListenFunc,
			OnChange: func(tailnet.ListenState) { a.tracker.Notify() },
		})
	}, a.remote, func() int { return int(a.port.Load()) }, a.log)

	a.tracker = service.NewStatusTracker(service.StatusTrackerConfig{
		Watcher: cfg.Watcher, Listener: a.listen, Port: func() int { return int(a.port.Load()) }, Bus: a.bus,
	})

	// GUI ルーター: プロセス内だけ（ソケットを開かない）。Wails の AssetOptions.Handler に渡す。
	h, err := gui.New(gui.Deps{
		Status: a.tracker, Sessions: a.mcp, Leases: a.leases, KillSwitch: a.kill, Admin: a.admin,
		Settings: a.settings, Events: a.bus, Clock: cfg.Clock, Logger: a.log,
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
