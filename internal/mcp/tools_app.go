package mcp

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// アプリ起動のツール（M1.5。FR-M18・M19。API 仕様 §3.3「アプリ起動」）。
// 既定の allowlist 方式では、起動できるのはホスト所有者が GUI で登録した許可リストのアプリ（app_id）だけで、入出力・エラーに
// パス・引数・作業ディレクトリは出ない。ホストが GUI で denylist 方式にしたときだけ、launch_app は path（固定ディスク上の .exe の絶対パス）
// も受け付け、ホストの拒否リストに当たるものは app_denied で拒否する（拒否リストの中身は返さない）。引数・作業ディレクトリはどちらの方式でも渡せない。
// mcp は許可リスト・拒否リスト・方式を書く経路（service.AppRegistry）を持たない。受け取るのは Control の LaunchableApps / Launch だけ。

type appBody struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// launchableAppsBody の mode は起動の方式（"allowlist" / "denylist"）。方式によらず apps は登録済みのアプリ。
type launchableAppsBody struct {
	Mode string    `json:"mode"`
	Apps []appBody `json:"apps"`
}

// launchAppIn は app_id か path の一方だけを渡す（同時指定・どちらも無しは invalid_argument）。path は mode が denylist のときだけ受け付ける。
type launchAppIn struct {
	AppID string `json:"app_id,omitempty" jsonschema:"id of an app from list_launchable_apps. Pass either app_id or path, not both"`
	Path  string `json:"path,omitempty" jsonschema:"absolute path of a .exe on a fixed local drive, e.g. C:/Windows/System32/notepad.exe. Accepted only when list_launchable_apps reports mode denylist; no arguments or working directory can be passed"`
}

// launchWindowBody は launch_app の結果に付く、起動したアプリのウィンドウ（list_windows の id・title・pid・process_name と同じ値）。
type launchWindowBody struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	PID     uint32 `json:"pid"`
	Process string `json:"process_name"`
}

// launchAppBody の window は、起動したアプリのウィンドウが見つからなかったときは付かない（前面にある別のアプリのウィンドウは返さない。#85）。
// reused_existing_window は、pid のウィンドウも起動の前に無かった新しいウィンドウも見つからないまま別のプロセスのウィンドウが前面にあるとき true（FR-M19）。このとき window は付かない。
type launchAppBody struct {
	OK                   bool              `json:"ok"`
	AppID                string            `json:"app_id,omitempty"` // path 指定の起動では無い
	PID                  uint32            `json:"pid"`
	Window               *launchWindowBody `json:"window,omitempty"`
	ReusedExistingWindow bool              `json:"reused_existing_window"`
}

// addAppTools はアプリ起動のツールを登録する: FR-M18・M19。
func (s *Server) addAppTools() {
	// list_launchable_apps はリース不要。Control.Launch を通らないので、この層が tool.list_launchable_apps を {count} で記録する。
	addAuditedTool(s, "list_launchable_apps",
		"List the apps the host owner registered for launch_app: id and label, sorted by label, and the launch mode. "+
			"mode is allowlist (default: only the listed apps can be launched, by app_id) or denylist "+
			"(every app can be launched, by app_id or by path, except the ones the host owner denied; the denied list is never exposed). "+
			"Paths and arguments are never exposed. Does not need the control lease.",
		true, noArgsSummary, func(ctx context.Context, _ domain.Actor, _ noArgs) (toolResult, error) {
			list, err := s.control.LaunchableApps(ctx)
			if err != nil {
				return toolResult{}, err
			}
			out := launchableAppsBody{Mode: string(list.Mode), Apps: make([]appBody, 0, len(list.Apps))}
			for _, a := range list.Apps {
				out.Apps = append(out.Apps, appBody{ID: a.ID, Label: a.Label})
			}
			return toolResult{Structured: out, AuditDetail: map[string]any{"count": len(out.Apps)}}, nil
		})

	// launch_app は Control.Launch が tool.launch_app を記録する（成功・拒否・失敗）。この層では記録しない。
	addTool(s, "launch_app",
		"Launch an app as a new process (not idempotent: every call starts a new process). "+
			"In allowlist mode (default) pass the app_id of an app from list_launchable_apps; the app, its arguments and its working directory are fixed by the host owner. "+
			"In denylist mode (see mode in list_launchable_apps) you may instead pass path, the absolute path of a .exe on a fixed local drive "+
			"(no arguments or working directory can be passed; the app starts with no arguments); apps the host owner denied fail with app_denied. "+
			"Pass app_id or path, never both. "+
			"Needs the control lease (acquired implicitly). Returns the process id once the process is created, "+
			"then waits up to about 3 seconds for a window of the launched app to appear, brings it to the foreground if needed and reports it "+
			"(window: id, title, pid, process_name; the window of the returned pid, or a new window of the same executable when the app handed over to another process; "+
			"omitted when no such window was found: another app's foreground window is never reported in its place). "+
			"Some apps (for example tabbed single-window apps such as Windows 11 Notepad, browsers and Windows Terminal) only bring an "+
			"already open window forward instead of opening a new one: reused_existing_window is true when no window of the returned pid "+
			"was found, no window that did not exist before the launch has appeared, and a window of another process is in the foreground. "+
			"That window may hold the user's unsaved work. "+
			"Before typing or clicking, ALWAYS confirm the target window with a screenshot or list_windows "+
			"(a window pid different from the returned pid may mean the existing window was reused, or that the app handed over to a new process that opened a new window; "+
			"reused_existing_window=true is a strong hint, not proof). "+
			"Fails with invalid_argument, app_not_found, app_denied, launch_failed (reason in structuredContent), lease_held_by_other, kill_switch_active or audit_unavailable "+
			"(the attempt could not be written to the audit log, so nothing was launched).",
		false, func(ctx context.Context, who domain.Actor, in launchAppIn) (toolResult, error) {
			res, err := s.control.Launch(ctx, who, service.LaunchRequest{AppID: in.AppID, Path: in.Path})
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
