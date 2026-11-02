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

// launchWindowBody は launch_app の結果に付く、起動後に前面にあったウィンドウ（list_windows の id・title・pid・process_name と同じ値）。
type launchWindowBody struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	PID     uint32 `json:"pid"`
	Process string `json:"process_name"`
}

// launchAppBody の window は、前面のウィンドウを確かめられなかったときは付かない。
// reused_existing_window は、pid のウィンドウも起動の前に無かった新しいウィンドウも見つからないまま別のプロセスのウィンドウが前面にあるとき true（FR-M19）。
type launchAppBody struct {
	OK                   bool              `json:"ok"`
	AppID                string            `json:"app_id"`
	PID                  uint32            `json:"pid"`
	Window               *launchWindowBody `json:"window,omitempty"`
	ReusedExistingWindow bool              `json:"reused_existing_window"`
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
			"Needs the control lease (acquired implicitly). Returns the process id once the process is created, "+
			"then waits up to about 3 seconds for a window of that process to come to the foreground and reports the foreground window "+
			"(window: id, title, pid, process_name; omitted when it could not be determined). "+
			"Some apps (for example tabbed single-window apps such as Windows 11 Notepad, browsers and Windows Terminal) only bring an "+
			"already open window forward instead of opening a new one: reused_existing_window is true when no window of the returned pid "+
			"was found, no window that did not exist before the launch has appeared, and a window of another process is in the foreground. "+
			"That window may hold the user's unsaved work. "+
			"Before typing or clicking, ALWAYS confirm the target window with a screenshot or list_windows "+
			"(a window pid different from the returned pid, or reused_existing_window, means the existing window was reused). "+
			"Fails with app_not_found, launch_failed (reason in structuredContent), lease_held_by_other or kill_switch_active.",
		false, func(ctx context.Context, who domain.Actor, in launchAppIn) (toolResult, error) {
			res, err := s.control.Launch(ctx, who, in.AppID)
			if err != nil {
				return toolResult{}, err
			}
			body := launchAppBody{OK: true, AppID: res.AppID, PID: res.PID, ReusedExistingWindow: res.ReusedExistingWindow}
			if w := res.Window; w != nil {
				body.Window = &launchWindowBody{ID: w.ID, Title: w.Title, PID: w.PID, Process: w.Process}
			}
			return toolResult{Structured: body}, nil
		})
}
