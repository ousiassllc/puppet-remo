//go:build windows

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Wails v3.0.0 のホスト（ウィンドウ・トレイ・単一インスタンス）。実 Wails・WebView2 が要るため CI では動かず、
// 実機スモーク（docs/operations/smoke-windows.md）で確認する。振る舞いの決定は Shell（shell.go）にあり、偽物でテストしている。

// 単一インスタンスの識別子（Windows のミューテックス名の元になる）。
const singleInstanceID = "io.github.ousiassllc.puppet-remo"

// WailsOptions は NewWailsHost の設定。
type WailsOptions struct {
	// Background が true なら、起動時にメインウィンドウを開かずトレイだけに常駐する（ログオン時の自動起動）。
	Background bool
	// Icon はトレイのアイコン（PNG または ICO）。
	Icon []byte
	// WebviewDataDir は WebView2 のユーザーデータの置き場（%LOCALAPPDATA%\puppet-remo\webview。NFR-S11）。
	WebviewDataDir string
	Logger         *slog.Logger
}

// WailsHost は Wails のアプリ。NewWailsHost で単一インスタンスを確認し、Run で App を載せて動かす。
type WailsHost struct {
	opts  WailsOptions
	log   *slog.Logger
	wails *application.App

	// gui は GUI ルーター。Wails のアプリは単一インスタンスの確認のために（App の組み立てより前に）作るので、
	// ハンドラは Run で差し込む。ウィンドウが作られる前（Run の中）に必ず設定される。
	gui   atomic.Pointer[http.Handler]
	shell atomic.Pointer[Shell]
	// main はメインウィンドウ。ファイル選択ダイアログ（FilePicker）の親にする（MainWindowHandle）。Run が作る。
	main atomic.Pointer[application.WebviewWindow]
	// overlay は操作中オーバーレイ（#69）のウィンドウ。出し入れは Windows.ShowOverlay・HideOverlay（wails_overlay_windows.go）。
	overlay wailsOverlay
}

// NewWailsHost は Wails のアプリを作る。すでに別のインスタンスが動いていれば、そのインスタンスのウィンドウを
// 前面に出して、このプロセスは（アプリの組み立てへ進まずに）終了する。
func NewWailsHost(opts WailsOptions) *WailsHost {
	h := &WailsHost{opts: opts, log: opts.Logger}
	if h.log == nil {
		h.log = slog.Default()
	}
	h.wails = application.New(application.Options{
		Name:        "puppet-remo",
		Description: "tailnet 経由でこの PC を操作させる常駐アプリ",
		Icon:        opts.Icon,
		Logger:      h.log,
		LogLevel:    slog.LevelWarn,
		// ホスト GUI は GUI ルーターが配信する。ソケットは開かず、プロセス内の AssetOptions.Handler だけを使う。
		Assets: application.AssetOptions{Handler: http.HandlerFunc(h.serveGUI), DisableLogging: true},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: singleInstanceID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if s := h.shell.Load(); s != nil {
					s.SecondInstance()
				}
			},
		},
		Windows: application.WindowsOptions{
			// ウィンドウを閉じても終了しない（トレイに常駐する。FR-H01）。
			DisableQuitOnLastWindowClosed: true,
			WebviewUserDataPath:           opts.WebviewDataDir,
		},
	})
	return h
}

func (h *WailsHost) serveGUI(w http.ResponseWriter, r *http.Request) {
	g := h.gui.Load()
	if g == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	(*g).ServeHTTP(w, r)
}

// Windows は承認ダイアログ・オーバーレイを開く Windows を返す（app.Config.Windows に渡す）。ウィンドウの生成は Run が Wails を動かしたあとに起きる。
func (h *WailsHost) Windows() Windows { return wailsWindows{h} }

// ウィンドウの下地色（CSS の読み込み前に見える色）。static/src/css/app.css のトークン --bone と同じ値で、
// OS の外観に合わせる（ライト #f5f5f4 / ダーク #050505）。トークンを変えたらここも合わせる。
var (
	windowBackgroundLight = application.NewRGB(245, 245, 244)
	windowBackgroundDark  = application.NewRGB(5, 5, 5)
)

// windowBackground は OS の外観（ライト / ダーク）に合う下地色を返す。
func (h *WailsHost) windowBackground() application.RGBA {
	if h.wails.Env.IsDarkMode() {
		return windowBackgroundDark
	}
	return windowBackgroundLight
}

// 承認ダイアログのウィンドウの幅（固定）。高さは種類ごと（approvalWindowHeight）。
const approvalWidth = 440

// wailsWindows は Windows の Wails 実装。
type wailsWindows struct{ h *WailsHost }

// Main はメインウィンドウ（Run が作ったもの）。Run の前は nil を返す（Shell は Run の中で作るので問題にならない）。
func (w wailsWindows) Main() MainWindow {
	win := w.h.main.Load()
	if win == nil {
		return nil
	}
	return wailsWindow{win}
}

// OpenApproval は承認ダイアログのウィンドウを開く（FR-H05）。メインウィンドウと同じ serveGUI が /approvals/{id} を配信する。
//   - 固定サイズ・最前面・センタリング・最小化 / 最大化なし
//   - ContentProtectionEnabled: true（WDA_EXCLUDEFROMCAPTURE）。スクリーンショットにもライブ画面にも写さない（NFR-S06）
//   - プロセス内のウィンドウなので、platform.WindowManager の Own（プロセス ID の一致）で入力拒否（own_window）になる
//
// 許可も拒否も押さずに閉じられた場合は、承認待ちが 60 秒の失効で拒否になる（approvalDialogs の判断）。トレイのメニューに項目は足さない。
func (w wailsWindows) OpenApproval(id, kind string) Window {
	win := w.h.wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "approval-" + id,
		Title:            "puppet-remo 承認",
		Width:            approvalWidth,
		Height:           approvalWindowHeight(kind),
		DisableResize:    true,
		AlwaysOnTop:      true,
		InitialPosition:  application.WindowCentered,
		URL:              "/approvals/" + url.PathEscape(id),
		BackgroundColour: w.h.windowBackground(),
		// 承認ダイアログは AI の入力を受けず、画面にも写さない（NFR-S06）。
		ContentProtectionEnabled: true,
		MinimiseButtonState:      application.ButtonHidden,
		MaximiseButtonState:      application.ButtonHidden,
	})
	return wailsWindow{win}
}

// MainWindowHandle はメインウィンドウの HWND を返す。ウィンドウがまだ無い（Run の前）か、破棄済みなら 0。
// platform/windows.NewFilePicker の owner に渡し、ファイル選択ダイアログをメインウィンドウに対するモーダルにする（FR-H12）。
// ダイアログは別のスレッドから開かれるが、親ウィンドウは Wails の UI スレッドのものでよい。
func (h *WailsHost) MainWindowHandle() uintptr {
	w := h.main.Load()
	if w == nil {
		return 0
	}
	return uintptr(w.NativeWindow())
}

// shutdownTimeout は終了処理（監査の書き切りなど）の上限。
const shutdownTimeout = 10 * time.Second

// Run は a を動かし、メインウィンドウとトレイを作って、アプリが終了するまで戻らない。
func (h *WailsHost) Run(a *App) error {
	if a == nil {
		return errors.New("app: nil App")
	}
	gui := a.GUIHandler()
	h.gui.Store(&gui)

	win := h.wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "main",
		Title:  "puppet-remo",
		Width:  1120,
		Height: 780, MinWidth: 800, MinHeight: 560,
		URL:              "/",
		Hidden:           h.opts.Background,
		BackgroundColour: h.windowBackground(),
		// ホスト GUI（トークンの平文などが映る）は、スクリーンショット・ライブ配信に写さない（WDA_EXCLUDEFROMCAPTURE。NFR-S06）。
		ContentProtectionEnabled: true,
	})

	h.main.Store(win)
	// トレイに常駐する間に OS の外観が変わっても、ウィンドウの下地をページの背景（--bone）に合わせ続ける。
	h.wails.Event.OnApplicationEvent(events.Windows.SystemThemeChanged, func(*application.ApplicationEvent) {
		win.SetBackgroundColour(h.windowBackground())
	})

	tray := newWailsTray(h.wails, h.opts.Icon)
	shell := NewShell(a, h.Windows().Main(), tray, h.wails.Quit, h.log)
	h.shell.Store(shell)
	tray.bind(shell)
	// 更新の適用の開始後（インストーラを起動したあと）にアプリを終える。トレイの「終了」と同じ経路（以後の「閉じる」は取り消さない）。
	a.SetQuit(shell.Quit)

	// 「閉じる」はトレイへ隠す。終了はトレイメニューの「終了」から。
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { shell.WindowClosing(e.Cancel) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWatch := shell.WatchOnStart(ctx)
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	// トレイの更新はメインスレッドへ回すので、Wails のイベントループが動き出してから始める
	// （Run より前に始めると、Wails の内部がまだ無く InvokeSync が落ちる）。
	h.wails.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		startWatch()
	})

	h.wails.OnShutdown(func() {
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer scancel()
		if err := a.Shutdown(sctx); err != nil {
			h.log.Error("shutdown reported errors", "err", err)
		}
	})
	return h.wails.Run()
}

// wailsWindow は Wails のウィンドウを MainWindow に合わせる。
type wailsWindow struct{ w *application.WebviewWindow }

func (w wailsWindow) Show(path string) {
	if path != "" {
		w.w.SetURL(path)
	}
	w.w.Show()
	w.w.Restore()
	w.w.Focus()
}

func (w wailsWindow) Hide() { w.w.Hide() }

// Close は承認ダイアログを閉じる（Window）。破棄済みなら Wails が何もしない。
func (w wailsWindow) Close() { w.w.Close() }

func (w wailsWindow) Visible() bool { return w.w.IsVisible() }

// wailsTray はトレイアイコンとそのメニュー。
type wailsTray struct {
	tray     *application.SystemTray
	menu     *application.Menu
	dash     *application.MenuItem
	requests *application.MenuItem
	kill     *application.MenuItem
	quit     *application.MenuItem
}

func newWailsTray(app *application.App, icon []byte) *wailsTray {
	t := &wailsTray{tray: app.SystemTray.New(), menu: app.NewMenu()}
	if len(icon) > 0 {
		t.tray.SetIcon(icon)
	}
	t.dash = t.menu.Add("ダッシュボードを開く")
	t.requests = t.menu.Add("接続要求を確認").SetHidden(true)
	t.menu.AddSeparator()
	t.kill = t.menu.Add("キルスイッチを発動")
	t.menu.AddSeparator()
	t.quit = t.menu.Add("終了")
	t.tray.SetMenu(t.menu)
	t.tray.SetTooltip("puppet-remo")
	return t
}

// bind はメニューとトレイのクリックを shell へ結ぶ。
func (t *wailsTray) bind(s *Shell) {
	t.dash.OnClick(func(*application.Context) { s.OpenDashboard() })
	t.requests.OnClick(func(*application.Context) { s.OpenRequests() })
	t.kill.OnClick(func(*application.Context) { s.TriggerKillSwitch() })
	t.quit.OnClick(func(*application.Context) { s.Quit() })
	t.tray.OnClick(s.OpenDashboard)
}

// Apply はトレイの表示を更新する。Wails のメニューは UI スレッドから触る。
func (t *wailsTray) Apply(st TrayState) {
	application.InvokeSync(func() {
		t.tray.SetTooltip(st.Tooltip)
		t.kill.SetLabel(st.KillSwitchLabel).SetEnabled(st.KillSwitchEnabled)
		t.requests.SetLabel(st.RequestsLabel).SetHidden(!st.RequestsVisible)
		t.menu.Update()
	})
}
