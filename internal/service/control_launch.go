package service

import (
	"context"
	"errors"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
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
		out = append(out, a.Summary())
	}
	return out, nil
}

func (c *control) Launch(ctx context.Context, who domain.Actor, appID string) (LaunchResult, error) {
	start := c.clock.Now()
	res, label, leaseID, err := c.launch(ctx, who, appID)
	if c.audit != nil {
		c.audit.Record(ctx, ToolAuditEntry(who, "launch_app", launchAuditDetail(appID, label, res, err), leaseID, err, c.clock.Now().Sub(start)))
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
	pid, err := c.launcher.Launch(ctx, platform.LaunchSpec{ExePath: app.ExePath, Args: app.Args, WorkingDir: app.WorkingDir})
	if err != nil {
		return LaunchResult{}, label, leaseID, launchFailure(err)
	}
	return LaunchResult{AppID: app.ID, PID: pid}, label, leaseID, nil
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
// label は許可リストを引けたときだけ、pid は成功のときだけ、reason は launch_failed のときだけ入る。
func launchAuditDetail(appID, label string, res LaunchResult, err error) map[string]any {
	detail := map[string]any{"app_id": truncateRunes(appID, maxAuditAppIDRunes)}
	if label != "" {
		detail["label"] = label
	}
	if err == nil {
		detail["pid"] = res.PID
		return detail
	}
	var de *domain.Error
	if errors.As(err, &de) && de.Code == domain.CodeLaunchFailed {
		if reason, ok := de.Detail["reason"].(domain.LaunchFailure); ok {
			detail["reason"] = string(reason)
		}
	}
	return detail
}

// truncateRunes は s を最大 n 文字（rune）にする。
func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
