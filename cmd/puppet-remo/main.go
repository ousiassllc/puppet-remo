//go:build windows

// puppet-remo は tailnet 経由でこの PC を操作させる常駐アプリ（Wails v3）。
//
// このファイルは composition root で、OS 依存の部品（platform/windows・tailscaled の LocalAPI・SQLite・Wails）を
// 作って internal/app に渡す。組み立て自体（service・mcp・ルーターの結線）は internal/app にあり、偽物でテストされている。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"unsafe"

	"github.com/ousiassllc/puppet-remo/internal/app"
	"github.com/ousiassllc/puppet-remo/internal/platform/windows"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
	"github.com/ousiassllc/puppet-remo/static"
)

// version はビルド時に -ldflags "-X main.version=..." で埋める。
var version = "dev"

// logLevelEnv はアプリ運用ログのレベル（debug / info / warn / error。既定 info。NFR-O01）。
const logLevelEnv = "PUPPET_REMO_LOG_LEVEL"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "puppet-remo:", err)
		showError("puppet-remo を起動できません", err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	// ログオン時の自動起動（--background）は、ウィンドウを開かずトレイだけに常駐する。
	background := slices.Contains(args, windows.BackgroundArg)

	windows.EnableDPIAwareness()

	dataDir, err := app.DataDir(os.Getenv("LOCALAPPDATA"))
	if err != nil {
		return err
	}
	level, err := app.ParseLogLevel(os.Getenv(logLevelEnv))
	if err != nil {
		return err
	}
	log, logCloser, err := app.NewLogger(dataDir, level)
	if err != nil {
		return err
	}
	slog.SetDefault(log)

	// 単一インスタンス: すでに動いていれば、ここで（既存のウィンドウを前面に出して）終了する。
	// DB・ホットキーなどの資源は、1 つ目のインスタンスだけが持つ。
	host := app.NewWailsHost(app.WailsOptions{
		Background:     background,
		Icon:           app.TrayIcon,
		WebviewDataDir: filepath.Join(dataDir, "webview"),
		Logger:         log,
	})

	a, err := compose(context.Background(), dataDir, log, logCloser, host)
	if err != nil {
		return err
	}
	log.Info("puppet-remo starting", "version", version, "background", background)
	return host.Run(a)
}

// compose は本物の部品を作って app.New に渡す。作った資源は、成功すれば app.Shutdown が閉じる。
// host の MainWindowHandle は、メインウィンドウの HWND を返す（Wails のウィンドウは Run で作られるため、ファイル選択のたびに呼ぶ関数で渡す）。
// host の Windows は、承認ダイアログなどのウィンドウの生成（Wails）。
func compose(ctx context.Context, dataDir string, log *slog.Logger, logCloser io.Closer, host *app.WailsHost) (*app.App, error) {
	var closers []io.Closer
	fail := func(err error) (*app.App, error) {
		for _, c := range slices.Backward(closers) {
			_ = c.Close()
		}
		return nil, err
	}

	db, err := repository.Open(ctx, filepath.Join(dataDir, app.DatabaseFile))
	if err != nil {
		return fail(fmt.Errorf("open database: %w", err))
	}
	closers = append(closers, db)
	if q := db.QuarantinedPath(); q != "" {
		// 壊れた DB を隔離して空の DB で起動した（NFR-A06）。起動時はまだ GUI が無いので、ログとメッセージで知らせる。
		log.Error("the database was corrupt and has been quarantined", "path", q)
		go showError("データベースを初期化しました", "データベースが壊れていたため、次のファイルへ退避して空の状態で起動しました。\n"+q)
	}

	wm, err := windows.NewWindowManager()
	if err != nil {
		return fail(fmt.Errorf("window manager: %w", err))
	}
	input := windows.NewInput()
	closers = append(closers, input)
	ui, err := windows.NewUIAutomation(wm)
	if err != nil {
		return fail(fmt.Errorf("ui automation: %w", err))
	}
	closers = append(closers, closerFunc(func() error { ui.Close(); return nil }))
	hotkey, err := windows.NewHotkey()
	if err != nil {
		return fail(fmt.Errorf("hotkey: %w", err))
	}
	closers = append(closers, closerFunc(func() error { hotkey.Close(); return nil }))
	power := windows.NewPower()
	closers = append(closers, power)
	autostart, err := windows.NewAutostart()
	if err != nil {
		return fail(fmt.Errorf("autostart: %w", err))
	}
	staticHandler, err := static.Handler()
	if err != nil {
		return fail(fmt.Errorf("static files: %w", err))
	}

	api := tailnet.NewLocalAPI(nil) // 既定の接続先（Windows は名前付きパイプ）

	// ログは最後に閉じる（Shutdown の途中の出力を取りこぼさない）。
	closers = append(closers, logCloser)

	a, err := app.New(ctx, app.Config{
		Repo: db,
		Platform: app.Platform{
			Capturer: windows.NewCapturer(), Input: input, Windows: wm, Clipboard: windows.NewClipboard(),
			UI: ui, Hotkey: hotkey, Power: power, Autostart: autostart,
			// ファイル選択ダイアログ（ホスト GUI の /apps。FR-H12）は、Wails のメインウィンドウを親にしたモーダルにする。
			Launcher: windows.NewLauncher(), FilePicker: windows.NewFilePicker(host.MainWindowHandle),
		},
		// 承認ダイアログ（FR-H05）は専用の Wails ウィンドウ（キャプチャ除外）で開く。
		Windows: host.Windows(),
		Watcher: tailnet.NewWatcher(tailnet.WatcherConfig{API: api}),
		WhoIs:   tailnet.NewWhoIs(tailnet.WhoIsConfig{API: api}),
		Static:  staticHandler,
		Logs:    app.NewLogReader(dataDir),
		Version: version,
		Logger:  log,
		Closers: closers,
	})
	if err != nil {
		return fail(err)
	}
	return a, nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// showError は起動の失敗をメッセージボックスで知らせる（ウィンドウがまだ無いため）。
func showError(title, text string) {
	t, err1 := syscall.UTF16PtrFromString(title)
	m, err2 := syscall.UTF16PtrFromString(text)
	if err := errors.Join(err1, err2); err != nil {
		return
	}
	const mbOKIconError = 0x10
	user32 := syscall.NewLazyDLL("user32.dll")
	_, _, _ = user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOKIconError)
}
