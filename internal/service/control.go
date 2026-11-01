package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// Control はすべての入力の唯一の入口（docs/components/overview.md §5.4）。
// platform.InputInjector を呼ぶのはここだけ。ステートレスで、検査の順序を定める。
type Control interface {
	// Do は入力系の操作を実行する。検査の順序は固定で、1 つでも失敗したら実行しない:
	//
	//  1. KillSwitch → ErrKillSwitchActive
	//  2. LeaseManager.Check（リース未保持の mcp は暗黙に Acquire を試みる。FR-L04）
	//  3. 引数の検査と縮小座標 → 物理座標の変換（FR-M16。範囲外は ErrInvalidCoordinates）
	//  4. InputGuard.Check → ErrInputBlocked（NFR-S06、UIPI。own_window には puppet-remo のウィンドウの範囲を DetailOwnWindows で付ける）
	//  5. platform の実行
	//  6. Audit（引数・結果・所要時間。type の本文・クリップボードは文字数のみ。FR-AU03）
	//
	// Judge（4 の後。agent のみ）は M4 で足す。Wait はリース不要（KillSwitch の後に待つだけ）。
	Do(ctx context.Context, who domain.Actor, op domain.Op) (Result, error)
	// Screenshot は閲覧系（リース不要）。monitor は Index か domain.MonitorPrimary。format は "png"（既定）か "jpeg"。
	Screenshot(ctx context.Context, monitor int, format string) (Shot, error)
	Monitors(ctx context.Context) ([]domain.Monitor, error)
	// Windows は物理座標のウィンドウ一覧。縮小座標への変換は呼び出し側（mcp）が行う。
	Windows(ctx context.Context) ([]domain.WindowInfo, error)
	// CursorPosition は現在のカーソル位置を縮小座標とモニターで返す（FR-M03。リース不要）。
	CursorPosition(ctx context.Context) (Cursor, error)
	// MaxDimension は縮小スクリーンショットの最大辺（設定 screenshot.max_dimension。既定 1568）。
	// get_screen_info の縮小率 domain.Monitor.Scale(maxDim) と、UI ツリー・ウィンドウ矩形の縮小座標への変換に使う。
	MaxDimension(ctx context.Context) (int, error)
	// LaunchableApps は許可リストの id と表示名だけを label の昇順で返す（M1.5。FR-M18。リース不要。キルスイッチ中は拒否）。
	LaunchableApps(ctx context.Context) ([]domain.AppSummary, error)
	// Launch は許可リストのアプリを新しいプロセスとして起動する（M1.5。FR-M19）。呼び出し側が渡せるのは appID だけ。
	// 検査の順序は固定で、1 つでも失敗したら起動しない（InputGuard・Judge・承認は通らず、起動を直列化しない）:
	//
	//  1. KillSwitch → ErrKillSwitchActive
	//  2. LeaseManager.Check（リース未保持の mcp は暗黙に Acquire を試みる。FR-L04）
	//  3. AppReader.Get と domain.CheckLaunchTarget（無い・規則外・読めない行は ErrAppNotFound。fail closed）
	//  4. KillSwitch の再確認（3 までの間に発動したものを取りこぼさない → ErrKillSwitchActive）
	//  5. platform.AppLauncher.Launch（*platform.LaunchError は ErrLaunchFailed{reason}）
	//  5b. 起動後のウィンドウの観測（LaunchResult.Window / ReusedExistingWindow。観測の失敗は起動の失敗にしない）
	//  6. Audit（tool.launch_app。パス・引数は記録しない。FR-AU06）
	Launch(ctx context.Context, who domain.Actor, appID string) (LaunchResult, error)
}

// LaunchResult は Launch の結果。
type LaunchResult struct {
	AppID string
	PID   uint32
	// Window は起動後に前面にあったウィンドウ（プロセスの作成後、pid のウィンドウが前面に出るまで最大およそ 3 秒待って観測する）。
	// 前面のウィンドウを確かめられなかったときは nil。pid のウィンドウとは限らない（Window.PID が PID と違うことがある）。
	Window *domain.WindowInfo
	// ReusedExistingWindow は、pid のトップレベルウィンドウが見つからないまま、別のプロセスのウィンドウが前面にあるとき true
	// （タブ式の単一ウィンドウのアプリなどで、既存のウィンドウが前に出ただけの可能性がある）。Window が nil のときは false。
	ReusedExistingWindow bool
}

// Result は Do の結果。入力系は空。
type Result struct {
	Text string         // ClipboardRead: クリップボードのテキスト（1 MiB まで）
	Tree *domain.UINode // GetUITree: 物理座標のツリー。縮小座標への変換は呼び出し側が行う
}

// Shot は縮小済みのスクリーンショット。
type Shot struct {
	Data     []byte
	MIME     string // image/png | image/jpeg
	Monitor  int
	Physical domain.Size // 元のサイズ
	Scaled   domain.Size // 縮小後のサイズ（Data の画像の大きさ）
	Scale    float64
	// OwnWindows は撮影時点で、このモニター上にある puppet-remo のウィンドウの範囲（縮小座標）。
	// ウィンドウは画像に写らない（NFR-S06）ため、入力を拒否される場所の手がかりとして返す。取れなかったときは nil。
	OwnWindows []domain.OwnRegion
}

// Cursor は縮小座標のカーソル位置。
type Cursor struct {
	Point   domain.Point
	Monitor int
}

// ControlKillSwitch は Control が使うキルスイッチの窓口。発動時に押下中のキー・ボタンを解放するため、
// Control がクローザーを登録する（InputInjector を触れるのは Control だけのため）。KillSwitchManager が満たす。
type ControlKillSwitch interface {
	KillSwitchState
	RegisterCloser(name string, fn CloserFunc)
}

// ControlConfig は NewControl の設定。Settings・Audit・Sleep 以外は必須。
type ControlConfig struct {
	Capturer  platform.Capturer
	Input     platform.InputInjector
	Windows   platform.WindowManager
	Clipboard platform.Clipboard
	UI        platform.UIAutomation
	// Launcher は Launch だけが呼ぶ（platform.AppLauncher を呼ぶのは Control だけ）。Apps は許可リストの読み取りに使う。
	Launcher platform.AppLauncher
	Apps     AppReader

	Leases LeaseManager
	Kill   ControlKillSwitch

	Settings repository.Settings // nil なら screenshot.max_dimension は既定値
	Audit    AuditRecorder       // nil なら記録しない
	Clock    domain.Clock
	// Sleep は Wait の待ち。nil なら ctx のキャンセルで終わるタイマー。テストが差し替える。
	Sleep func(ctx context.Context, d time.Duration) error
}

// 操作の上限（API 仕様 §3.3、NFR-S09）。
const (
	maxTypeTextRunes   = 10_000
	maxClipboardBytes  = 1 << 20
	maxWaitDuration    = 10 * time.Second
	minScrollAmount    = 1
	maxScrollAmount    = 50
	minClickCount      = 1
	maxClickCount      = 3
	defaultUITreeDepth = 5
	defaultUITreeNodes = 500
	maxUITreeDepth     = 20
	maxUITreeNodes     = 5000
)

type control struct {
	capture   platform.Capturer
	input     platform.InputInjector
	windows   platform.WindowManager
	clipboard platform.Clipboard
	ui        platform.UIAutomation
	launcher  platform.AppLauncher
	apps      AppReader
	leases    LeaseManager
	kill      KillSwitchState
	guard     InputGuard
	settings  repository.Settings
	audit     AuditRecorder
	clock     domain.Clock
	sleep     func(ctx context.Context, d time.Duration) error
	// inputSem は入力の実行を直列化する（容量 1。lockInput）。
	inputSem chan struct{}
}

// NewControl は Control を返し、キルスイッチの発動時に入力を解放するクローザーを登録する。
func NewControl(cfg ControlConfig) (Control, error) {
	if cfg.Capturer == nil || cfg.Input == nil || cfg.Windows == nil || cfg.Clipboard == nil || cfg.UI == nil ||
		cfg.Launcher == nil || cfg.Apps == nil || cfg.Leases == nil || cfg.Kill == nil || cfg.Clock == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("control requires capturer, input, windows, clipboard, ui, launcher, apps, leases, kill switch and clock")
	}
	c := &control{
		capture:   cfg.Capturer,
		input:     cfg.Input,
		windows:   cfg.Windows,
		clipboard: cfg.Clipboard,
		ui:        cfg.UI,
		launcher:  cfg.Launcher,
		apps:      cfg.Apps,
		leases:    cfg.Leases,
		kill:      cfg.Kill,
		guard:     InputGuard{Windows: cfg.Windows},
		settings:  cfg.Settings,
		audit:     cfg.Audit,
		clock:     cfg.Clock,
		sleep:     cfg.Sleep,
		inputSem:  make(chan struct{}, 1),
	}
	if c.sleep == nil {
		c.sleep = sleepContext
	}
	cfg.Kill.RegisterCloser("input", func(context.Context) error { return c.input.ReleaseAll() })
	return c, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *control) Do(ctx context.Context, who domain.Actor, op domain.Op) (Result, error) {
	start := c.clock.Now()
	res, leaseID, err := c.do(ctx, who, op)
	if c.audit != nil {
		tool, args := describeOp(op, res)
		c.audit.Record(ctx, ToolAuditEntry(who, tool, args, leaseID, err, c.clock.Now().Sub(start)))
	}
	return res, err
}

func (c *control) do(ctx context.Context, who domain.Actor, op domain.Op) (Result, string, error) {
	if c.kill.Active() {
		return Result{}, "", domain.ErrKillSwitchActive
	}
	if w, ok := op.(domain.Wait); ok {
		return Result{}, "", c.wait(ctx, w)
	}
	leaseID, err := c.ensureLease(ctx, who)
	if err != nil {
		return Result{}, "", err
	}
	p, err := c.plan(ctx, op)
	if err != nil {
		return Result{}, leaseID, err
	}
	if err := c.guard.Check(ctx, p.op, p.monitors); err != nil {
		return Result{}, leaseID, c.withOwnWindows(ctx, err)
	}
	// リースの検査から実行までの間に発動したキルスイッチを取りこぼさない（Trigger は状態を先に立てる）。
	if c.kill.Active() {
		return Result{}, leaseID, domain.ErrKillSwitchActive
	}
	res, err := c.execute(ctx, p)
	return res, leaseID, err
}

// ensureLease は who のリースを確かめ、保持者のリース ID を返す。
// 未保持の mcp は暗黙に取得を試みる（FR-L04）。他の種別は取得せず ErrLeaseRequired。
func (c *control) ensureLease(ctx context.Context, who domain.Actor) (string, error) {
	err := c.leases.Check(who)
	if errors.Is(err, domain.ErrLeaseRequired) && who.Kind == domain.ActorMCP {
		l, aerr := c.leases.Acquire(ctx, who)
		if aerr != nil {
			return "", aerr
		}
		return l.ID, nil
	}
	if err != nil {
		return "", err
	}
	if h, ok := c.leases.Holder(); ok && h.Actor.SameSession(who) {
		return h.LeaseID, nil
	}
	return "", nil
}

func (c *control) wait(ctx context.Context, w domain.Wait) error {
	if w.Duration < 0 || w.Duration > maxWaitDuration {
		return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("wait must be between 0 and %s", maxWaitDuration))
	}
	return c.sleep(ctx, w.Duration)
}

func (c *control) Monitors(ctx context.Context) ([]domain.Monitor, error) {
	if c.kill.Active() {
		return nil, domain.ErrKillSwitchActive
	}
	return c.capture.Monitors(ctx)
}

func (c *control) Windows(ctx context.Context) ([]domain.WindowInfo, error) {
	if c.kill.Active() {
		return nil, domain.ErrKillSwitchActive
	}
	return c.windows.List(ctx)
}

func (c *control) MaxDimension(ctx context.Context) (int, error) {
	return loadPositiveInt(ctx, c.settings, settingScreenshotMaxDim, defaultMaxDimension)
}
