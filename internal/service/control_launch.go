package service

import (
	"context"
	"errors"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// maxAuditAppIDRunes は監査に記録する、呼び出し側が渡した app_id の最大文字数（許可リストに無い値を無制限に残さない）。
const maxAuditAppIDRunes = 64

func (c *control) LaunchableApps(ctx context.Context) (LaunchableList, error) {
	if c.kill.Active() {
		return LaunchableList{}, domain.ErrKillSwitchActive
	}
	mode, err := c.apps.LaunchMode(ctx)
	if err != nil {
		return LaunchableList{}, err
	}
	apps, err := c.apps.List(ctx)
	if err != nil {
		return LaunchableList{}, err
	}
	out := LaunchableList{Mode: mode, Apps: make([]domain.AppSummary, 0, len(apps))}
	for _, a := range apps {
		// 起動できない行（読めない Corrupt の行を含む）は MCP に見せない。
		if domain.CheckLaunchTarget(a) != nil {
			continue
		}
		out.Apps = append(out.Apps, a.Summary())
	}
	return out, nil
}

func (c *control) Launch(ctx context.Context, who domain.Actor, req LaunchRequest) (LaunchResult, error) {
	start := c.clock.Now()
	res, tr, err := c.launch(ctx, who, req)
	if c.audit != nil {
		took := c.clock.Now().Sub(start)
		detail := launchAuditDetail(req, tr, res, err)
		var entry repository.AuditEntry
		if who.Kind == domain.ActorAgent {
			// agent の起動も、Do のアクションと同じ agent.step の行（Judge は通らないので judge は付かない）。
			entry = agentStepEntry(ctx, who, "launch_app", detail, tr.leaseID, err, judgeTrace{}, took.Milliseconds())
		} else {
			entry = ToolAuditEntry(who, "launch_app", detail, tr.leaseID, err, took)
		}
		if domain.CodeOf(err) == domain.CodeInvalidArgument {
			entry.Result = auditDenied // 指定の不備（app_id と path の同時指定・allowlist での path）は、入力スキーマ違反と同じく拒否として残す
		}
		var shot *thumbTarget
		if err == nil {
			shot = &thumbTarget{monitor: domain.MonitorPrimary} // 起動したウィンドウのモニターは問わず、プライマリを撮る
		}
		c.recordAudit(ctx, entry, shot)
	}
	return res, err
}

// launchTrace は launch が監査のために返す、検査の途中で分かった値。
type launchTrace struct {
	label   string            // 許可リストを引けたときの表示名
	leaseID string            // 保持者のリース ID
	mode    domain.LaunchMode // 検査した方式（読めたとき）
}

// launch は検査と起動を行い、結果と監査用の値を返す。
func (c *control) launch(ctx context.Context, who domain.Actor, req LaunchRequest) (res LaunchResult, tr launchTrace, err error) {
	if c.kill.Active() {
		return LaunchResult{}, tr, domain.ErrKillSwitchActive
	}
	tr.leaseID, err = c.ensureLease(ctx, who)
	if err != nil {
		return LaunchResult{}, tr, err
	}
	tr.mode, err = c.apps.LaunchMode(ctx)
	if err != nil {
		return LaunchResult{}, tr, err
	}
	spec, appID, err := c.resolveLaunchTarget(ctx, req, &tr)
	if err != nil {
		return LaunchResult{}, tr, err
	}
	// リースの検査から起動までの間に発動したキルスイッチを取りこぼさない（Trigger は状態を先に立てる）。
	if c.kill.Active() {
		return LaunchResult{}, tr, domain.ErrKillSwitchActive
	}
	// 起動の前のウィンドウの一覧（起動後に新しく開いたウィンドウを見分ける。FR-M19。#55）。読めなければ nil（後の判定は新規の有無を使わない）。
	before := c.windowIDs(ctx)
	// 起動の「試行」の行を、プロセスを作る前に同期で書く。書けなければ起動しない（fail-closed。NFR-S20）。
	if err := c.recordLaunchAttempt(ctx, who, req, tr); err != nil {
		return LaunchResult{}, tr, err
	}
	// 同期書き込みは DB のロック待ちなどで伸びうる。その間に発動したキルスイッチを取りこぼさない（試行の行は残るが、起動しない）。
	if c.kill.Active() {
		return LaunchResult{}, tr, domain.ErrKillSwitchActive
	}
	pid, err := c.launcher.Launch(ctx, spec)
	if err != nil {
		return LaunchResult{}, tr, launchFailure(err)
	}
	obs := c.observeLaunchedWindow(ctx, pid, spec.ExePath, before)
	win := obs.launched
	// 観測の間（最大およそ 3 秒）に発動したキルスイッチを取りこぼさない: 発動していたら前面を奪わない（起動は成立しているので結果は返す）。
	if win != nil && !win.Foreground && !c.kill.Active() {
		// 起動したアプリのウィンドウが背面にあるなら前面に出す（失敗しても起動は成功のまま。Foreground は成功したときだけ立てる）。
		if c.windows.Focus(ctx, win.ID) == nil {
			focused := *win
			focused.Foreground = true
			win = &focused
		}
	}
	return LaunchResult{AppID: appID, PID: pid, Window: win, ReusedExistingWindow: obs.reusedExistingWindow()}, tr, nil
}

// resolveLaunchTarget は req と方式から、起動する LaunchSpec を決める（起動はしない）。
//
//   - app_id と path の同時指定、path の指定（allowlist）は ErrInvalidArgument。どちらも無いときも ErrInvalidArgument（app_id の欠落は今までも入力スキーマ違反の invalid_argument）
//   - app_id: 許可リストの行を引き、domain.CheckLaunchTarget で再検査する（無い・規則外・読めない行は ErrAppNotFound）。
//     denylist 方式では、そのうえで拒否リストにも当たらないことを確かめる（拒否が優先）
//   - path（denylist だけ）: 絶対パスのローカルの .exe の形（domain.ValidateLaunchPath）→ AppLauncher.Resolve（固定ディスク上の実在。
//     リンク・8.3 短縮名を解決）→ 拒否リストとの照合（ErrAppDenied）。起動するのは解決した実体のパスで、引数・作業ディレクトリは渡さない
func (c *control) resolveLaunchTarget(ctx context.Context, req LaunchRequest, tr *launchTrace) (platform.LaunchSpec, string, error) {
	switch {
	case req.AppID != "" && req.Path != "":
		return platform.LaunchSpec{}, "", domain.ErrInvalidArgument.WithMsg("app_id and path cannot be combined")
	case req.AppID == "" && req.Path == "":
		return platform.LaunchSpec{}, "", domain.ErrInvalidArgument.WithMsg("app_id is required")
	}
	if req.Path != "" {
		if tr.mode != domain.LaunchModeDenylist {
			return platform.LaunchSpec{}, "", domain.ErrInvalidArgument.WithMsg("path is not accepted: pass app_id of an app from list_launchable_apps")
		}
		return c.resolveLaunchPath(ctx, req.Path)
	}
	app, err := c.apps.Get(ctx, req.AppID)
	switch {
	case errors.Is(err, ErrNotFound):
		return platform.LaunchSpec{}, "", domain.ErrAppNotFound
	case err != nil:
		return platform.LaunchSpec{}, "", err
	}
	tr.label = app.Label
	// 保存済みの行も、起動のたびに登録時と同じ規則で確かめる（NFR-S20 (3)。手で書き換えられた行を起動しない）。
	if err := domain.CheckLaunchTarget(app); err != nil {
		return platform.LaunchSpec{}, "", err
	}
	if tr.mode == domain.LaunchModeDenylist {
		if _, err := c.checkNotDenied(ctx, app.ExePath); err != nil {
			return platform.LaunchSpec{}, "", err
		}
	}
	return platform.LaunchSpec{ExePath: app.ExePath, Args: app.Args, WorkingDir: app.WorkingDir}, app.ID, nil
}

// resolveLaunchPath は denylist 方式の path 指定の起動の検査（resolveLaunchTarget）。
func (c *control) resolveLaunchPath(ctx context.Context, path string) (platform.LaunchSpec, string, error) {
	if !domain.ValidateLaunchPath(path) {
		return platform.LaunchSpec{}, "", domain.ErrInvalidArgument.WithMsg("path must be an absolute path of a local .exe (drive letter, no UNC, no \"..\")")
	}
	resolved, err := c.checkNotDenied(ctx, path)
	if err != nil {
		return platform.LaunchSpec{}, "", err
	}
	return platform.LaunchSpec{ExePath: resolved}, "", nil
}

// checkNotDenied は exePath を実体のパスに解決し（固定ディスク上の実在でなければ launch_failed）、拒否リストに当たらないことを確かめる。
// 当たれば ErrAppDenied。戻り値は解決した実体のパス。
func (c *control) checkNotDenied(ctx context.Context, exePath string) (string, error) {
	resolved, err := c.launcher.Resolve(ctx, exePath)
	if err != nil {
		return "", launchFailure(err)
	}
	denied, err := c.apps.ListDenied(ctx)
	if err != nil {
		return "", err
	}
	if domain.MatchDenied(denied, exePath, resolved) {
		return "", domain.ErrAppDenied
	}
	return resolved, nil
}

// 起動の「試行」の監査のイベント名。結果の行（tool.launch_app・agent.step）とは別の行で、追記だけで更新しない。
const (
	launchAttemptAuditEvent      = "tool.launch_app.attempt"
	agentLaunchAttemptAuditEvent = "agent.launch_attempt"
)

// recordLaunchAttempt は起動の「試行」の行を同期で書く。書けなければ ErrAuditUnavailable を返し（原因は運用ログにだけ出す）、
// 呼び出し側は起動しない。audit が nil（テスト用）なら何もしない。
func (c *control) recordLaunchAttempt(ctx context.Context, who domain.Actor, req LaunchRequest, tr launchTrace) error {
	if c.audit == nil {
		return nil
	}
	if err := c.audit.RecordSync(ctx, launchAttemptEntry(ctx, who, req, tr)); err != nil {
		c.log.Error("launch_app refused: the attempt could not be written to the audit log", "client", who.Label, "err", err)
		return domain.ErrAuditUnavailable
	}
	return nil
}

// launchAttemptEntry は起動の「試行」の監査エントリ（detail は app_id と、許可リストを引けたときの label だけ。結果の行の detail の部分集合）。
// result の ok は「起動の試みを記録した」の意味で、起動の成否は結果の行が持つ。MCP は kind = tool、agent は kind = agent（タスク・ステップに紐づく）。
func launchAttemptEntry(ctx context.Context, who domain.Actor, req LaunchRequest, tr launchTrace) repository.AuditEntry {
	detail := launchTargetDetail(req, tr)
	if who.Kind == domain.ActorAgent {
		e := agentAuditBase(ctx, who, agentLaunchAttemptAuditEvent, tr.leaseID)
		e.Tool = launchAppTool
		e.Result = auditOK
		e.DetailJSON = marshalDetail(detail)
		return e
	}
	e := ToolAuditEntry(who, launchAppTool, detail, tr.leaseID, nil, 0)
	e.Event = launchAttemptAuditEvent
	e.DurationMS = nil
	return e
}

// maxAuditLaunchPathRunes は監査に記録する、呼び出し側が渡した path の最大文字数。
const maxAuditLaunchPathRunes = 1024

// launchTargetDetail は起動の指定の監査の detail（試行と結果の行で共通）。app_id は指定があったときだけ、path は指定があったときだけ
// （形が正しいものだけ、呼び出し側が渡した綴りのまま・長さを切る。引数・作業ディレクトリは受け取らないので無い）、label は許可リストを引けたときだけ入る。
func launchTargetDetail(req LaunchRequest, tr launchTrace) map[string]any {
	detail := map[string]any{}
	if domain.ValidateLaunchPath(req.Path) { // 形が不正な値は、未検証の入力なので記録しない（NFR-S13）
		detail["path"] = domain.TruncateRunes(req.Path, maxAuditLaunchPathRunes)
	}
	if req.Path == "" || req.AppID != "" { // path だけの指定は app_id を持たない
		detail["app_id"] = domain.TruncateRunes(req.AppID, maxAuditAppIDRunes)
	}
	if tr.label != "" {
		detail["label"] = tr.label
	}
	if tr.mode == domain.LaunchModeDenylist {
		detail["launch_mode"] = string(tr.mode) // allowlist は今まで通り記録しない
	}
	return detail
}

// launchFailure は AppLauncher.Launch の失敗を API の launch_failed に写す。理由（4 値）以外は載せない。
// 登録済みの値がコマンドラインに組めない（domain.ErrInvalidArgument）のも、プロセスを作れなかった失敗として扱う。
func launchFailure(err error) error {
	var le *platform.LaunchError
	switch {
	case errors.As(err, &le):
		return domain.NewLaunchFailed(le.Reason)
	case errors.Is(err, domain.ErrInvalidArgument):
		return domain.NewLaunchFailed(domain.LaunchOther)
	}
	return err
}

// launchAuditDetail は tool.launch_app の detail（API 仕様・er.md）。登録済みのアプリは引数・作業ディレクトリを入れない
// （path 指定の起動は、呼び出し側が渡した path だけを launchTargetDetail が入れる）。
// label は許可リストを引けたときだけ、pid と reused_existing_window は成功のときだけ、reason は launch_failed のときだけ入る。
// 前面のウィンドウのタイトル・id は記録しない。
func launchAuditDetail(req LaunchRequest, tr launchTrace, res LaunchResult, err error) map[string]any {
	detail := launchTargetDetail(req, tr)
	if err == nil {
		detail["pid"] = res.PID
		detail["reused_existing_window"] = res.ReusedExistingWindow
		return detail
	}
	var de *domain.Error
	if errors.As(err, &de) && de.Code == domain.CodeLaunchFailed {
		if reason, ok := de.Detail["reason"].(domain.LaunchFailure); ok && reason.Valid() {
			detail["reason"] = string(reason)
		}
	}
	return detail
}
