package mcp

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// アプリ起動のツール（M1.5。FR-M18・M19。API 仕様 §3.3「アプリ起動」）。
// 起動できるのはホスト所有者が GUI で登録した許可リストのアプリだけで、入出力・エラーにパス・引数・作業ディレクトリは出ない。
// mcp は許可リストを書く経路（service.AppRegistry）を持たない。受け取るのは Control の LaunchableApps / Launch だけ。

type appBody struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type launchableAppsBody struct {
	Apps []appBody `json:"apps"`
}

type launchAppIn struct {
	AppID string `json:"app_id" jsonschema:"id of an app from list_launchable_apps"`
}

type launchAppBody struct {
	OK    bool   `json:"ok"`
	AppID string `json:"app_id"`
	PID   uint32 `json:"pid"`
}

// addAppTools はアプリ起動のツールを登録する: FR-M18・M19。
func (s *Server) addAppTools() {
	// list_launchable_apps はリース不要。Control.Launch を通らないので、この層が tool.list_launchable_apps を {count} で記録する。
	addAuditedTool(s, "list_launchable_apps",
		"List the apps the host owner allows to be launched with launch_app: id and label, sorted by label. "+
			"Paths and arguments are never exposed. Does not need the control lease.",
		true, noArgsSummary, func(ctx context.Context, _ domain.Actor, _ noArgs) (toolResult, error) {
			apps, err := s.control.LaunchableApps(ctx)
			if err != nil {
				return toolResult{}, err
			}
			out := launchableAppsBody{Apps: make([]appBody, 0, len(apps))}
			for _, a := range apps {
				out.Apps = append(out.Apps, appBody{ID: a.ID, Label: a.Label})
			}
			return toolResult{Structured: out, AuditDetail: map[string]any{"count": len(out.Apps)}}, nil
		})

	// launch_app は Control.Launch が tool.launch_app を記録する（成功・拒否・失敗）。この層では記録しない。
	addTool(s, "launch_app",
		"Launch an allowed app as a new process (not idempotent: every call starts a new process). "+
			"The app, its arguments and its working directory are fixed by the host owner; you can only pass app_id. "+
			"Needs the control lease (acquired implicitly). Returns the process id once the process is created; "+
			"it does not wait for a window (use list_windows). Fails with app_not_found, launch_failed (reason in structuredContent), "+
			"lease_held_by_other or kill_switch_active.",
		false, func(ctx context.Context, who domain.Actor, in launchAppIn) (toolResult, error) {
			res, err := s.control.Launch(ctx, who, in.AppID)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: launchAppBody{OK: true, AppID: res.AppID, PID: res.PID}}, nil
		})
}
