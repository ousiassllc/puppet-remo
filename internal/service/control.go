package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	//  4. InputGuard.Check → ErrInputBlocked（NFR-S06、UIPI。own_window には puppet-remo のウィンドウの範囲を DetailOwnWindows で付ける）。
	//     スコープ付きのリース（LeaseManager.AcquireScoped。#156）は、これの代わりに対象グループ G の検証を行う（control_target.go）:
	//     実行前に、クリップボード読み取りの拒否（clipboard_out_of_scope。書き込みだけ許可。platform の読み取りは呼ばない）・停止中の拒否
	//     （target_halted。Esc を含む一切の入力とクリップボード書き込み）・win 修飾と Alt+Tab・Alt+Esc・Ctrl+Esc・Ctrl+Shift+Esc の拒否
	//     （key_out_of_scope）。入力の注入の直前と、1 つの操作の複数回の注入の間は lockInput の中で、T の同一性・状態と、キー・文字は前面と
	//     フォーカスの葉、座標は最上位の At・葉・Rect を G で検証し、操作の最初に G の検証できたメンバーを GroupCapturer.GrabGroup で撮れるかを確かめる。
	//     どれかに通らなければ何も注入せず、ReleaseAll を呼び、リースを halted にして ErrInputBlocked（reason は検証の失敗の理由か capture_unavailable /
	//     capture_member_unavailable。監査 input.target_halted）。メンバーの増減だけでは停止しない。FocusWindow は G のメンバーだけ（それ以外はその操作だけの拒否）。
	//  5. platform の実行
	//  6. Audit（引数・結果・所要時間。type の本文・クリップボードは既定では文字数だけで、設定 audit.record_text が true のときだけ本文も
	//     detail_json の text に記録する（FR-AU03、FR-S07）。キー入力はキーの名前を記録しない）
	//     result=ok で画面を変える操作（opChangesScreen。カーソルの移動だけ・クリップボード・待ち・読み取りは除く）は、設定
	//     audit.thumbnails_enabled が有効なら操作の直後に非同期でサムネイルを撮り、撮影のあとに同じ行へ付けて記録する
	//     （FR-AU02。Do は撮影を待たず、撮影の失敗は結果を変えない。モニターは操作のもの、無指定・Web 入力はプライマリ。
	//     Web 入力は全体で 1 秒に 1 枚まで。control_thumb.go）
	//
	// Web UI（保持者種別 web。WebSocket の input.*）の入力は domain.PointerMove・MouseButton・Wheel・KeyEvent と、
	// KeyPress・TypeText・ClipboardRead・ClipboardWrite で渡す。PointerMove・MouseButton・Wheel の座標は縮小座標ではなくストリームの座標
	// （Monitor の物理ピクセル。3 で仮想デスクトップの物理座標へ変換し、モニターの外は ErrInvalidCoordinates）。
	// web は暗黙にリースを取らない（未保持は ErrLeaseRequired）。PointerMove は監査しない（連続するカーソル移動で監査ログを埋めない）。
	//
	// ActorAgent は 4 の後に Judge（FR-G12）を通る（ControlConfig.Judge。無ければ RequireApproval 扱い）: Deny は domain.ErrJudgeDenied、
	// RequireApproval は Approval.Request（ApprovalKindJudgeAction。クライアントの承認モードでは省略できない。ホストの拒否は
	// ErrApprovalDenied、失効は ErrApprovalTimeout）。許可のあとはリース・InputGuard・対象ウィンドウを確かめ直す。mcp・web は Judge を通らない。
	// Judge が見るウィンドウは、座標のある操作はその座標のウィンドウ（Drag は始点と終点）、FocusWindow・GetUITree は id のウィンドウ、
	// キー・文字入力・クリップボードは前面のウィンドウ。agent のアクションは 1 件ごとに agent.step（kind = agent）で監査し
	// （WithAgentStep の step_no・route。type の本文は記録しない）、Judge が allow 以外なら agent.judge も記録する（control_judge.go）。
	// Wait はリース不要（KillSwitch の後に待つだけ）。
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
	// LaunchableApps は起動の方式（launch.mode）と、許可リストの id と表示名だけを label の昇順で返す（M1.5。FR-M18。リース不要。キルスイッチ中は拒否）。
	// 方式によらず登録済みのアプリを返す。拒否リストの中身は返さない。
	LaunchableApps(ctx context.Context) (LaunchableList, error)
	// Launch はアプリを新しいプロセスとして起動する（M1.5。FR-M19）。呼び出し側が渡せるのは req の AppID（登録済みのアプリ）か、
	// denylist 方式のときだけ Path（固定ディスク上の実在の .exe の絶対パス。引数・作業ディレクトリは渡せない）。
	// mcp も内蔵エージェント（M4）も同じ検査を通る（方式・拒否リストは AppReader が持つ 1 つ）。
	// 検査の順序は固定で、1 つでも失敗したら起動しない（InputGuard・Judge・承認は通らず、起動を直列化しない）:
	//
	//  1. KillSwitch → ErrKillSwitchActive
	//  2. LeaseManager.Check（リース未保持の mcp は暗黙に Acquire を試みる。FR-L04）
	//  3. 方式（AppReader.LaunchMode）と対象:
	//     - allowlist: AppReader.Get と domain.CheckLaunchTarget（無い・規則外・読めない行は ErrAppNotFound。fail closed）。path の指定は ErrInvalidArgument
	//     - denylist: AppID なら同じ検査に加えて、Path なら domain.ValidateLaunchPath → AppLauncher.Resolve（実体のパス）→
	//       拒否リストとの照合（domain.MatchDenied。当たれば ErrAppDenied）。AppID と Path の同時指定は ErrInvalidArgument
	//  4. KillSwitch の再確認（3 までの間に発動したものを取りこぼさない → ErrKillSwitchActive）
	//  4b. 起動の「試行」の行を SyncAuditRecorder.RecordSync で同期に書く（書けなければ ErrAuditUnavailable。起動しない。fail-closed。#37・NFR-S20）
	//  4c. KillSwitch の再確認（同期書き込みの待ちの間に発動したものを取りこぼさない → ErrKillSwitchActive）
	//  5. platform.AppLauncher.Launch（*platform.LaunchError は ErrLaunchFailed{reason}）
	//  5b. 起動後のウィンドウの観測（起動の前後のウィンドウの一覧を比べる。LaunchResult.Window / ReusedExistingWindow。観測の失敗は起動の失敗にしない）
	//  6. 結果の行（tool.launch_app。非同期。書けなくても起動は取り消さない。パス・引数は記録しない。FR-AU06）
	Launch(ctx context.Context, who domain.Actor, req LaunchRequest) (LaunchResult, error)
	// ReleaseInput は押下中のキー・ボタンをすべて離す（InputInjector.ReleaseAll の唯一の入口）。キルスイッチのクローザー `input` と、
	// エージェントのタスクの終了処理（FR-G10）が呼ぶ。入力の直列化のロックは取らないので、実行中の入力があっても解放できる。
	ReleaseInput() error
	// VerifyTarget は who のスコープ付きリースの全条件（T の同一性・状態・G の求め直し・G の検証できたメンバーを撮れるか）を、入力せずに確かめる
	// （#156。保持者の表示・エージェントの観測が、撮れなくなったことを知るための明示の事前確認）。通れば active、通らなければ ReleaseAll・halted・
	// input.target_halted（入力のときと同じ。TargetStatus.State は halted、ErrInputBlocked も返す）。すでに halted なら再検証せずその状態を返す。
	// スコープなしのリースは ErrInvalidArgument、保持者でなければ LeaseManager.Check のエラー。
	VerifyTarget(ctx context.Context, who domain.Actor) (TargetStatus, error)
	// ResumeTarget は保持者の明示的な再開（#156。復帰は自動では起きない）。halted のリースの全条件（T の同一性・状態・G の求め直し・前面とフォーカスの葉が
	// G の中・G の検証できたメンバーを撮れるか）を再検証し、すべて通ったときだけ active に戻す。一つでも通らなければ halted のまま
	// （TargetStatus は halted と、停止の理由。ErrInputBlocked）。自動では前面にしない。target_closed / target_replaced / target_unsupported で止まった
	// リースは再開できない（ErrInputBlocked。解放して選び直す）。active のリースには何もしない。スコープなしは ErrInvalidArgument。
	ResumeTarget(ctx context.Context, who domain.Actor) (TargetStatus, error)
	// GrabGroupFrame は ref の T と、その時点の対象グループ G を撮って合成した画像（GroupFrame）を返す（#156・#157・#158。リース・保持者は問わない）。
	// 撮れない・検証できないときは画面全体に切り替えず、ErrInputBlocked（reason: capture_unavailable / capture_member_unavailable / target_*）を返す。
	GrabGroupFrame(ctx context.Context, ref domain.WindowRef) (GroupFrame, error)
}

// LaunchRequest は Launch の指定。AppID と Path はどちらか一方だけ（同時指定は ErrInvalidArgument）。
type LaunchRequest struct {
	AppID string // 許可リストの id（list_launchable_apps の id）
	Path  string // denylist 方式のときだけ。固定ディスク上の .exe の絶対パス
}

// LaunchableList は LaunchableApps の結果。
type LaunchableList struct {
	Mode domain.LaunchMode // 起動の方式（launch.mode）
	Apps []domain.AppSummary
}

// LaunchResult は Launch の結果。
type LaunchResult struct {
	AppID string // path 指定の起動では空
	PID   uint32
	// Window は起動後に前面にあったウィンドウ（プロセスの作成後、pid のウィンドウが前面に出るまで最大およそ 3 秒待って観測する）。
	// 前面のウィンドウを確かめられなかったときは nil。pid のウィンドウとは限らない（Window.PID が PID と違うことがある）。
	Window *domain.WindowInfo
	// ReusedExistingWindow は、pid のトップレベルウィンドウが見つからず、起動の前に無かった新しいウィンドウも開かないまま、
	// 別のプロセスのウィンドウが前面にあるとき true（タブ式の単一ウィンドウのアプリなどで、既存のウィンドウが前に出ただけの可能性がある）。Window が nil のときは false。
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
	// Audit は監査の記録先。nil なら記録しない（テスト用。本番の結線は必ず渡す）。launch_app の起動の前の「試行」の行は
	// RecordSync で書く（書けなければ起動しない。NFR-S20）。ThumbnailsEnabled・RecordWithThumb も持つ（*Audit）なら、画面を変える操作の
	// 成功時にサムネイルを撮って付ける（FR-AU02。control_thumb.go）。持たなければ撮らない。
	Audit  SyncAuditRecorder
	Clock  domain.Clock
	Logger *slog.Logger // nil なら slog.Default()。サムネイルの撮影の失敗などの出力先
	// Judge と Approval は ActorAgent のアクションだけが使う（mcp・web だけなら nil でよい）。agent のアクションで Judge が nil なら
	// RequireApproval 扱い、Approval が nil で承認が要るなら ErrApprovalDenied（fail-closed）。
	Judge    Judge
	Approval Approval
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
	audit     SyncAuditRecorder
	judge     Judge
	approval  Approval
	log       *slog.Logger
	thumbs    *thumbShooter // Audit がサムネイルに対応しないときは nil
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
		judge:     cfg.Judge,
		approval:  cfg.Approval,
		log:       cfg.Logger,
		clock:     cfg.Clock,
		sleep:     cfg.Sleep,
		inputSem:  make(chan struct{}, 1),
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.sleep == nil {
		c.sleep = sleepContext
	}
	if ta, ok := cfg.Audit.(thumbAuditor); ok {
		c.thumbs = &thumbShooter{audit: ta, capture: cfg.Capturer, clock: cfg.Clock, log: c.log, sem: make(chan struct{}, thumbWorkers)}
	}
	cfg.Kill.RegisterCloser("input", func(context.Context) error { return c.ReleaseInput() })
	return c, nil
}

func (c *control) ReleaseInput() error { return c.input.ReleaseAll() }

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
	var tr judgeTrace
	res, leaseID, executed, err := c.do(ctx, who, op, &tr)
	if _, isMove := op.(domain.PointerMove); c.audit != nil && !isMove {
		tool, args := describeOp(op, res)
		took := c.clock.Now().Sub(start)
		var entry repository.AuditEntry
		if who.Kind == domain.ActorAgent {
			entry = agentStepEntry(ctx, who, tool, args, leaseID, err, tr, took.Milliseconds())
		} else {
			c.addRecordedText(ctx, op, res, args)
			entry = ToolAuditEntry(who, tool, args, leaseID, err, took)
		}
		var shot *thumbTarget
		// スコープ付きのリースの操作は、監査のサムネイルを撮らない（Capturer.Grab はモニター全体＝他アプリの画素を含む。モニター撮影に切り替えない規則）。
		if err == nil && !tr.scoped && opChangesScreen(executed) {
			t := thumbTargetOf(who, executed)
			shot = &t
		}
		c.recordAudit(ctx, entry, shot)
	}
	return res, err
}

// recordAudit は監査エントリを記録する。shot が非 nil（画面を変える操作が成功した）で、サムネイルに対応する構成なら、
// 撮影を非同期に行い、エントリはそのあとに記録する（呼び出しは待たない）。
func (c *control) recordAudit(ctx context.Context, e repository.AuditEntry, shot *thumbTarget) {
	if c.thumbs == nil {
		c.audit.Record(ctx, e)
		return
	}
	c.thumbs.record(ctx, e, shot)
}

// do は検査と実行を行い、結果・保持者のリース ID・実行した計画の op（物理座標へ変換済み。実行まで進まなければ nil）を返す。
// tr には agent の Judge の経過を入れる（監査の行用）。
func (c *control) do(ctx context.Context, who domain.Actor, op domain.Op, tr *judgeTrace) (Result, string, domain.Op, error) {
	if c.kill.Active() {
		return Result{}, "", nil, domain.ErrKillSwitchActive
	}
	if w, ok := op.(domain.Wait); ok {
		return Result{}, "", nil, c.wait(ctx, w)
	}
	leaseID, scope, err := c.ensureLease(ctx, who)
	if err != nil {
		return Result{}, "", nil, err
	}
	tr.scoped = scope != nil
	p, err := c.plan(ctx, op)
	if err != nil {
		return Result{}, leaseID, nil, err
	}
	sr, err := c.guardOrScope(ctx, who, leaseID, scope, p)
	if err != nil {
		return Result{}, leaseID, nil, err
	}
	if who.Kind == domain.ActorAgent {
		if err := c.judgeAgent(ctx, who, p, tr); err != nil {
			return Result{}, leaseID, nil, err
		}
	}
	// リースの検査から実行までの間に発動したキルスイッチを取りこぼさない（Trigger は状態を先に立てる）。
	if c.kill.Active() {
		return Result{}, leaseID, nil, domain.ErrKillSwitchActive
	}
	res, err := c.execute(ctx, p, sr)
	return res, leaseID, p.op, err
}

// guardOrScope は検査 4。スコープなしのリースは InputGuard.Check（NFR-S06）で、検証の文脈は nil。スコープ付きのリースは、実行前の拒否
// （scopeGate。クリップボードの規則・停止中・win 修飾などのキー）と座標がモニター上にあることを確かめ、入力の検証は実行の中
// （lockInput の中。execute）に任せる。入力の検証を受けない操作（クリップボード書き込み・UI ツリー）は InputGuard も通す。
func (c *control) guardOrScope(ctx context.Context, who domain.Actor, leaseID string, scope *domain.LeaseScope, p plan) (*scopeRun, error) {
	if scope == nil {
		if err := c.guard.Check(ctx, p.op, p.monitors); err != nil {
			return nil, c.withOwnWindows(ctx, err)
		}
		return nil, nil
	}
	if err := scopeGate(*scope, p.op); err != nil {
		return nil, err
	}
	sr := &scopeRun{who: who, leaseID: leaseID, target: scope.Target}
	if !scopeGoverned(p.op) {
		if err := c.guard.Check(ctx, p.op, p.monitors); err != nil {
			return nil, c.withOwnWindows(ctx, err)
		}
		return sr, nil
	}
	for _, pt := range opPoints(p.op) {
		if err := checkOnMonitor(p.monitors, pt); err != nil {
			return nil, err
		}
	}
	return sr, nil
}

// ensureLease は who のリースを確かめ、保持者のリース ID と、束縛した対象（スコープなしは nil）を返す。
// 未保持の mcp は暗黙に（スコープなしで）取得を試みる（FR-L04）。他の種別は取得せず ErrLeaseRequired。
func (c *control) ensureLease(ctx context.Context, who domain.Actor) (string, *domain.LeaseScope, error) {
	err := c.leases.Check(who)
	if errors.Is(err, domain.ErrLeaseRequired) && who.Kind == domain.ActorMCP {
		l, aerr := c.leases.Acquire(ctx, who)
		if aerr != nil {
			return "", nil, aerr
		}
		return l.ID, l.Scope, nil
	}
	if err != nil {
		return "", nil, err
	}
	if h, ok := c.leases.Holder(); ok && h.Actor.SameSession(who) {
		return h.LeaseID, h.Scope, nil
	}
	return "", nil, nil
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
