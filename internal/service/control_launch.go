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

func (c *control) LaunchableApps(ctx context.Context) ([]domain.AppSummary, error) {
	if c.kill.Active() {
		return nil, domain.ErrKillSwitchActive
	}
	apps, err := c.apps.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AppSummary, 0, len(apps))
	for _, a := range apps {
		// 起動できない行（読めない Corrupt の行を含む）は MCP に見せない。
		if domain.CheckLaunchTarget(a) != nil {
			continue
		}
		out = append(out, a.Summary())
	}
	return out, nil
}

func (c *control) Launch(ctx context.Context, who domain.Actor, appID string) (LaunchResult, error) {
	start := c.clock.Now()
	res, label, leaseID, err := c.launch(ctx, who, appID)
	if c.audit != nil {
		took := c.clock.Now().Sub(start)
		detail := launchAuditDetail(appID, label, res, err)
		var entry repository.AuditEntry
		if who.Kind == domain.ActorAgent {
			// agent の起動も、Do のアクションと同じ agent.step の行（Judge は通らないので judge は付かない）。
			entry = agentStepEntry(ctx, who, "launch_app", detail, leaseID, err, judgeTrace{}, took.Milliseconds())
		} else {
			entry = ToolAuditEntry(who, "launch_app", detail, leaseID, err, took)
		}
		var shot *thumbTarget
		if err == nil {
			shot = &thumbTarget{monitor: domain.MonitorPrimary} // 起動したウィンドウのモニターは問わず、プライマリを撮る
		}
		c.recordAudit(ctx, entry, shot)
	}
	return res, err
}

// launch は検査と起動を行い、結果・（許可リストを引けたときの）表示名・保持者のリース ID を返す。
func (c *control) launch(ctx context.Context, who domain.Actor, appID string) (res LaunchResult, label, leaseID string, err error) {
	if c.kill.Active() {
		return LaunchResult{}, "", "", domain.ErrKillSwitchActive
	}
	leaseID, err = c.ensureLease(ctx, who)
	if err != nil {
		return LaunchResult{}, "", "", err
	}
	app, err := c.apps.Get(ctx, appID)
	switch {
	case errors.Is(err, ErrNotFound):
		return LaunchResult{}, "", leaseID, domain.ErrAppNotFound
	case err != nil:
		return LaunchResult{}, "", leaseID, err
	}
	label = app.Label
	// 保存済みの行も、起動のたびに登録時と同じ規則で確かめる（NFR-S20 (3)。手で書き換えられた行を起動しない）。
	if err := domain.CheckLaunchTarget(app); err != nil {
		return LaunchResult{}, label, leaseID, err
	}
	// リースの検査から起動までの間に発動したキルスイッチを取りこぼさない（Trigger は状態を先に立てる）。
	if c.kill.Active() {
		return LaunchResult{}, label, leaseID, domain.ErrKillSwitchActive
	}
	// 起動の前のウィンドウの一覧（起動後に新しく開いたウィンドウを見分ける。FR-M19。#55）。読めなければ nil（後の判定は新規の有無を使わない）。
	before := c.windowIDs(ctx)
	// 起動の「試行」の行を、プロセスを作る前に同期で書く。書けなければ起動しない（fail-closed。NFR-S20）。
	if err := c.recordLaunchAttempt(ctx, who, appID, label, leaseID); err != nil {
		return LaunchResult{}, label, leaseID, err
	}
	// 同期書き込みは DB のロック待ちなどで伸びうる。その間に発動したキルスイッチを取りこぼさない（試行の行は残るが、起動しない）。
	if c.kill.Active() {
		return LaunchResult{}, label, leaseID, domain.ErrKillSwitchActive
	}
	pid, err := c.launcher.Launch(ctx, platform.LaunchSpec{ExePath: app.ExePath, Args: app.Args, WorkingDir: app.WorkingDir})
	if err != nil {
		return LaunchResult{}, label, leaseID, launchFailure(err)
	}
	obs := c.observeLaunchedWindow(ctx, pid, app.ExePath, before)
	win := obs.launched
	if win != nil && !win.Foreground {
		// 起動したアプリのウィンドウが背面にあるなら前面に出す（失敗しても起動は成功のまま。Foreground は成功したときだけ立てる）。
		if c.windows.Focus(ctx, win.ID) == nil {
			focused := *win
			focused.Foreground = true
			win = &focused
		}
	}
	return LaunchResult{AppID: app.ID, PID: pid, Window: win, ReusedExistingWindow: obs.reusedExistingWindow()}, label, leaseID, nil
}

// 起動の「試行」の監査のイベント名。結果の行（tool.launch_app・agent.step）とは別の行で、追記だけで更新しない。
const (
	launchAttemptAuditEvent      = "tool.launch_app.attempt"
	agentLaunchAttemptAuditEvent = "agent.launch_attempt"
)

// recordLaunchAttempt は起動の「試行」の行を同期で書く。書けなければ ErrAuditUnavailable を返し（原因は運用ログにだけ出す）、
// 呼び出し側は起動しない。audit が nil（テスト用）なら何もしない。
func (c *control) recordLaunchAttempt(ctx context.Context, who domain.Actor, appID, label, leaseID string) error {
	if c.audit == nil {
		return nil
	}
	if err := c.audit.RecordSync(ctx, launchAttemptEntry(ctx, who, appID, label, leaseID)); err != nil {
		c.log.Error("launch_app refused: the attempt could not be written to the audit log", "client", who.Label, "err", err)
		return domain.ErrAuditUnavailable
	}
	return nil
}

// launchAttemptEntry は起動の「試行」の監査エントリ（detail は app_id と、許可リストを引けたときの label だけ。結果の行の detail の部分集合）。
// result の ok は「起動の試みを記録した」の意味で、起動の成否は結果の行が持つ。MCP は kind = tool、agent は kind = agent（タスク・ステップに紐づく）。
func launchAttemptEntry(ctx context.Context, who domain.Actor, appID, label, leaseID string) repository.AuditEntry {
	detail := map[string]any{"app_id": domain.TruncateRunes(appID, maxAuditAppIDRunes)}
	if label != "" {
		detail["label"] = label
	}
	if who.Kind == domain.ActorAgent {
		e := agentAuditBase(ctx, who, agentLaunchAttemptAuditEvent, leaseID)
		e.Tool = launchAppTool
		e.Result = auditOK
		e.DetailJSON = marshalDetail(detail)
		return e
	}
	e := ToolAuditEntry(who, launchAppTool, detail, leaseID, nil, 0)
	e.Event = launchAttemptAuditEvent
	e.DurationMS = nil
	return e
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

// launchAuditDetail は tool.launch_app の detail（API 仕様・er.md）。パス・引数・作業ディレクトリは入れない。
// label は許可リストを引けたときだけ、pid と reused_existing_window は成功のときだけ、reason は launch_failed のときだけ入る。
// 前面のウィンドウのタイトル・id は記録しない。
func launchAuditDetail(appID, label string, res LaunchResult, err error) map[string]any {
	detail := map[string]any{"app_id": domain.TruncateRunes(appID, maxAuditAppIDRunes)}
	if label != "" {
		detail["label"] = label
	}
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
