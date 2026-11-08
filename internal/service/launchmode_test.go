package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

const (
	pathSys32Cmd = `C:\Windows\System32\cmd.exe`
	pathOK       = `C:\Tools\ok.exe`
	pathLink     = `C:\Link\run.exe`
	pathShort    = `C:\WINDOW~1\SYSTEM~1\CMD.EXE`
)

// newModeEnv は newAppEnv に、launch_app(path) の検査に使う実行ファイルとリンクを足したもの。
func newModeEnv(t *testing.T) *ctlEnv {
	t.Helper()
	e := newAppEnv(t)
	e.launcher.AddExecutable(pathSys32Cmd)
	e.launcher.AddExecutable(pathOK)
	e.launcher.AddLink(pathLink, pathSys32Cmd)
	e.launcher.AddLink(pathShort, pathSys32Cmd)
	e.launcher.AddLink(`C:\Junction\cmd.exe`, pathSys32Cmd)
	e.launcher.OnLaunch(func(pid uint32) {
		e.windows.SetWindows(domain.WindowInfo{ID: "88", Title: "w", Process: "ok.exe", PID: pid, Foreground: true})
	})
	return e
}

func setMode(t *testing.T, e *ctlEnv, m domain.LaunchMode) {
	t.Helper()
	if err := e.apps.SetLaunchMode(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil
}

func launchCount(e *ctlEnv) int { return len(e.launcher.CallsTo("Launch")) }

func TestLaunchModeDefaultsToAllowlistAndFailsClosed(t *testing.T) {
	e := newModeEnv(t)
	ctx := context.Background()
	if m, err := e.apps.LaunchMode(ctx); err != nil || m != domain.LaunchModeAllowlist {
		t.Fatalf("default mode = %q, %v, want allowlist", m, err)
	}
	// 手で書き換えられた値は、より厳しい方式として扱う。
	for _, raw := range []string{`"blacklist"`, `"DENYLIST"`, `1`, `null`, `{`, `""`} {
		e.settings.values[settingLaunchMode] = json.RawMessage(raw)
		if m, err := e.apps.LaunchMode(ctx); err != nil || m != domain.LaunchModeAllowlist {
			t.Errorf("mode with stored %s = %q, %v, want allowlist (fail closed)", raw, m, err)
		}
	}
	e.settings.values[settingLaunchMode] = json.RawMessage(`"denylist"`)
	if m, _ := e.apps.LaunchMode(ctx); m != domain.LaunchModeDenylist {
		t.Errorf("mode with stored denylist = %q", m)
	}
}

func TestSetLaunchModeAuditsOnlyRealChanges(t *testing.T) {
	e := newModeEnv(t)
	ctx := context.Background()

	if err := e.apps.SetLaunchMode(ctx, domain.LaunchModeAllowlist); err != nil {
		t.Fatal(err)
	}
	if n := len(e.audit.Entries()); n != 0 {
		t.Errorf("setting the current mode recorded %d audit entries, want 0", n)
	}
	if err := e.apps.SetLaunchMode(ctx, domain.LaunchModeDenylist); err != nil {
		t.Fatal(err)
	}
	entries := e.audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	en := entries[0]
	if en.Kind != "settings" || en.Event != "settings.launch_mode.update" || en.ActorType != domain.ActorHost || en.Result != "ok" {
		t.Errorf("entry = %+v", en)
	}
	if d := auditDetail(t, en); d["from"] != "allowlist" || d["to"] != "denylist" || len(d) != 2 {
		t.Errorf("detail = %v, want from/to only", d)
	}
	if m, _ := e.apps.LaunchMode(ctx); m != domain.LaunchModeDenylist {
		t.Errorf("mode = %q after switching", m)
	}

	var fe AppFieldErrors
	if err := e.apps.SetLaunchMode(ctx, "blacklist"); !errors.As(err, &fe) || fe[domain.AppFieldLaunchMode] == "" {
		t.Errorf("SetLaunchMode(invalid) = %v, want a field error", err)
	}
	if n := len(e.audit.Entries()); n != 1 {
		t.Errorf("an invalid mode was audited (%d entries)", n)
	}
}

func TestDeniedCRUDStoresResolvedPathsAndAudits(t *testing.T) {
	e := newModeEnv(t)
	ctx := context.Background()

	// リンク経由のパスは、実体のパスに解決して保存する。名前はそのまま。
	byLink, err := e.apps.CreateDenied(ctx, "  "+pathLink+" ")
	if err != nil {
		t.Fatal(err)
	}
	if byLink.Kind != domain.DeniedPath || byLink.Value != pathSys32Cmd {
		t.Errorf("created = %+v, want the resolved real path", byLink)
	}
	byName, err := e.apps.CreateDenied(ctx, "PowerShell.exe")
	if err != nil {
		t.Fatal(err)
	}
	if byName.Kind != domain.DeniedName || byName.Value != "PowerShell.exe" {
		t.Errorf("created = %+v", byName)
	}
	// まだ存在しないパスは、区切りだけそろえて保存する（拒否リストには置いておける）。
	missing, err := e.apps.CreateDenied(ctx, `D:/Later//thing.exe`)
	if err != nil || missing.Kind != domain.DeniedPath || missing.Value != `D:\Later\thing.exe` {
		t.Errorf("missing path = %+v, %v", missing, err)
	}

	entries := e.audit.Entries()
	if len(entries) != 3 || entries[0].Event != "settings.launch_denylist.create" || entries[0].Kind != "settings" || entries[0].ActorType != domain.ActorHost {
		t.Fatalf("audit = %+v", entries)
	}
	if d := auditDetail(t, entries[0]); d["kind"] != "path" || d["value"] != pathSys32Cmd || d["id"] != byLink.ID {
		t.Errorf("create detail = %v", d)
	}

	upd, err := e.apps.UpdateDenied(ctx, byName.ID, "pwsh.exe")
	if err != nil || upd.Value != "pwsh.exe" || upd.ID != byName.ID {
		t.Fatalf("UpdateDenied = %+v, %v", upd, err)
	}
	en := e.audit.Entries()[3]
	if d := auditDetail(t, en); en.Event != "settings.launch_denylist.update" || d["before_value"] != "PowerShell.exe" || d["value"] != "pwsh.exe" {
		t.Errorf("update entry = %+v detail %v", en, d)
	}
	if err := e.apps.DeleteDenied(ctx, byName.ID); err != nil {
		t.Fatal(err)
	}
	if en := e.audit.Entries()[4]; en.Event != "settings.launch_denylist.delete" {
		t.Errorf("delete entry = %+v", en)
	}
	list, _ := e.apps.ListDenied(ctx)
	if len(list) != 2 {
		t.Errorf("list = %+v, want 2 left", list)
	}
	for _, id := range []string{"missing"} {
		if _, err := e.apps.UpdateDenied(ctx, id, "x.exe"); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateDenied(missing) = %v, want ErrNotFound", err)
		}
		if err := e.apps.DeleteDenied(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteDenied(missing) = %v, want ErrNotFound", err)
		}
	}
}

func TestDeniedRejectsBadAndDuplicateInputWithoutEchoingIt(t *testing.T) {
	e := newModeEnv(t)
	ctx := context.Background()
	if _, err := e.apps.CreateDenied(ctx, pathSys32Cmd); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil
	for _, in := range []string{``, `   `, `cmd`, `notepad.bat`, `\\server\share\a.exe`, `..\a.exe`, `C:\x\..\a.exe`, `a/b.exe`, `con.exe`,
		pathSys32Cmd, strings.ToUpper(pathSys32Cmd), pathLink, `c:/windows//system32/cmd.exe`} {
		_, err := e.apps.CreateDenied(ctx, in)
		var fe AppFieldErrors
		if !errors.As(err, &fe) || fe[domain.AppFieldDeniedValue] == "" {
			t.Errorf("CreateDenied(%q) = %v, want a field error", in, err)
			continue
		}
		if len(strings.TrimSpace(in)) > 8 && strings.Contains(fe[domain.AppFieldDeniedValue], strings.TrimSpace(in)) {
			t.Errorf("the message for %q echoes the input", in)
		}
	}
	if n := len(e.audit.Entries()); n != 0 {
		t.Errorf("rejected input was audited (%d entries)", n)
	}
	// 名前の重複は大文字小文字を区別しない。
	if _, err := e.apps.CreateDenied(ctx, "cmd.exe"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.apps.CreateDenied(ctx, "CMD.EXE"); err == nil {
		t.Error("a name differing only in case was accepted twice")
	}
}

func TestDeniedListLimit(t *testing.T) {
	e := newModeEnv(t)
	ctx := context.Background()
	for i := range domain.MaxDeniedApps {
		if _, err := e.apps.CreateDenied(ctx, "app"+itoa(i)+".exe"); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
	}
	if _, err := e.apps.CreateDenied(ctx, "one-too-many.exe"); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("over the limit = %v, want conflict", err)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// ---- Control.Launch ----

// allowlist（既定）: path は今までどおり拒否され、起動しない。登録済みの app_id は今までどおり起動できる。
func TestLaunchAllowlistModeRejectsPathAndKeepsAppID(t *testing.T) {
	e := newModeEnv(t)
	app := registerNotepad(t, e)
	ctx := context.Background()

	for _, p := range []string{pathOK, pathSys32Cmd, `not even a path`} {
		_, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: p})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Launch(path %q) in allowlist mode = %v, want invalid_argument", p, err)
		}
		en := lastLaunchEntry(t, e)
		if en.Result != "denied" || en.ErrorCode != "invalid_argument" {
			t.Errorf("audit = result %q code %q, want denied/invalid_argument", en.Result, en.ErrorCode)
		}
	}
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{AppID: app.ID, Path: pathOK}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Launch(app_id + path) = %v, want invalid_argument", err)
	}
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Launch(nothing) = %v, want invalid_argument", err)
	}
	if n := launchCount(e); n != 0 {
		t.Fatalf("%d launches happened for rejected requests", n)
	}
	// 拒否リストは allowlist では使われない（Resolve も呼ばれない）。
	if _, err := e.apps.CreateDenied(ctx, "notepad.exe"); err != nil {
		t.Fatal(err)
	}
	e.launcher.Reset()
	res, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{AppID: app.ID})
	if err != nil || res.AppID != app.ID || launchCount(e) != 1 {
		t.Fatalf("Launch(app_id) = %+v, %v (launches %d), want started as before", res, err, launchCount(e))
	}
	if n := len(e.launcher.CallsTo("Resolve")); n != 0 {
		t.Errorf("allowlist mode called Resolve %d times", n)
	}
	spec, _ := e.launcher.CallsTo("Launch")[0].Args[0].(platform.LaunchSpec)
	if spec.ExePath != appNotepad || !slices.Equal(spec.Args, []string{appArgMark}) || spec.WorkingDir != appWorkMark {
		t.Errorf("spec = %+v, want the registered values", spec)
	}
	if d := auditDetail(t, lastLaunchEntry(t, e)); d["launch_mode"] != nil {
		t.Errorf("allowlist audit detail gained launch_mode: %v", d)
	}
}

func TestLaunchDenylistModeStartsUnlistedPathWithoutArgs(t *testing.T) {
	e := newModeEnv(t)
	setMode(t, e, domain.LaunchModeDenylist)
	if _, err := e.apps.CreateDenied(context.Background(), pathSys32Cmd); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil

	res, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{Path: pathOK})
	if err != nil || res.PID == 0 || res.AppID != "" {
		t.Fatalf("Launch(unlisted path) = %+v, %v", res, err)
	}
	calls := e.launcher.CallsTo("Launch")
	if len(calls) != 1 {
		t.Fatalf("launches = %d, want 1", len(calls))
	}
	spec, _ := calls[0].Args[0].(platform.LaunchSpec)
	if spec.ExePath != pathOK || len(spec.Args) != 0 || spec.WorkingDir != "" {
		t.Errorf("spec = %+v, want only the path", spec)
	}
	// 監査: 試行の行（同期）と結果の行の両方に path と launch_mode。引数は無い。
	sync := e.audit.SyncEntries()
	if len(sync) != 1 || sync[0].Event != "tool.launch_app.attempt" {
		t.Fatalf("sync entries = %+v", sync)
	}
	for _, en := range []repository.AuditEntry{sync[0], lastLaunchEntry(t, e)} {
		d := auditDetail(t, en)
		if d["path"] != pathOK || d["launch_mode"] != "denylist" || d["app_id"] != nil {
			t.Errorf("%s detail = %v, want path and launch_mode, no app_id", en.Event, d)
		}
	}
}

// 拒否リストに当たるものは、どの綴り・経路でも起動しない（すり抜け対策）。
func TestLaunchDenylistModeBlocksEveryWayToTheDeniedExecutable(t *testing.T) {
	cases := []struct {
		name   string
		entry  string // 拒否リストへの登録
		launch string // launch_app の path
	}{
		{"exact path", pathSys32Cmd, pathSys32Cmd},
		{"upper case", pathSys32Cmd, strings.ToUpper(pathSys32Cmd)},
		{"lower case", pathSys32Cmd, strings.ToLower(pathSys32Cmd)},
		{"forward slashes and doubled separators", pathSys32Cmd, `C:/Windows//System32/cmd.exe`},
		{"symbolic link to the denied file", pathSys32Cmd, pathLink},
		{"directory junction", pathSys32Cmd, `C:\Junction\cmd.exe`},
		{"8.3 short name", pathSys32Cmd, pathShort},
		{"registered through a link, launched directly", pathLink, pathSys32Cmd},
		{"name entry, exact", "cmd.exe", pathSys32Cmd},
		{"name entry, upper case entry", "CMD.EXE", pathSys32Cmd},
		{"name entry, any folder", "cmd.exe", `C:\Tools\cmd.exe`},
		{"name entry, link with another name", "cmd.exe", pathLink},
		{"name entry, 8.3 short name", "cmd.exe", pathShort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newModeEnv(t)
			e.launcher.AddExecutable(`C:\Tools\cmd.exe`)
			setMode(t, e, domain.LaunchModeDenylist)
			ctx := context.Background()
			if _, err := e.apps.CreateDenied(ctx, tc.entry); err != nil {
				t.Fatal(err)
			}
			e.audit.entries = nil

			_, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: tc.launch})
			if !errors.Is(err, domain.ErrAppDenied) {
				t.Fatalf("Launch(%q) = %v, want app_denied", tc.launch, err)
			}
			if n := launchCount(e); n != 0 {
				t.Errorf("the denied executable was started (%d launches)", n)
			}
			en := lastLaunchEntry(t, e)
			if en.Result != "denied" || en.ErrorCode != "app_denied" {
				t.Errorf("audit = %q/%q, want denied/app_denied", en.Result, en.ErrorCode)
			}
			if len(e.audit.SyncEntries()) != 0 {
				t.Error("a denied launch wrote an attempt row (nothing was attempted)")
			}
			var de *domain.Error
			if errors.As(err, &de) && (strings.Contains(de.Msg, "cmd") || strings.Contains(de.Msg, `C:\`)) {
				t.Errorf("the error leaks the path or the denylist: %q", de.Msg)
			}
		})
	}
}

// 登録済みのアプリも、denylist 方式では拒否リストが優先する。拒否リストに当たらなければ app_id で今までどおり起動できる。
func TestLaunchDenylistModeAppliesToRegisteredApps(t *testing.T) {
	e := newModeEnv(t)
	notepad := registerNotepad(t, e)
	calc, err := e.apps.Create(context.Background(), domain.AppInput{Label: "電卓", ExePath: appCalc})
	if err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil
	setMode(t, e, domain.LaunchModeDenylist)
	ctx := context.Background()
	if _, err := e.apps.CreateDenied(ctx, "notepad.exe"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{AppID: notepad.ID}); !errors.Is(err, domain.ErrAppDenied) {
		t.Errorf("registered but denied app = %v, want app_denied", err)
	}
	if n := launchCount(e); n != 0 {
		t.Fatalf("launches = %d", n)
	}
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{AppID: calc.ID}); err != nil {
		t.Errorf("registered, not denied app = %v, want started", err)
	}
	// 一覧は方式によらず登録済みの全件を返す（拒否リストで絞らない = 中身を漏らさない）。
	list, err := e.control.LaunchableApps(ctx)
	if err != nil || list.Mode != domain.LaunchModeDenylist || len(list.Apps) != 2 {
		t.Errorf("LaunchableApps = %+v, %v, want mode denylist and both registered apps", list, err)
	}
}

func TestLaunchDenylistModeRejectsBadPathsBeforeTouchingTheOS(t *testing.T) {
	e := newModeEnv(t)
	setMode(t, e, domain.LaunchModeDenylist)
	ctx := context.Background()
	for _, p := range []string{`ok.exe`, `\Tools\ok.exe`, `..\ok.exe`, `C:\Tools\..\Tools\ok.exe`, `\\server\share\ok.exe`, `\\?\C:\Tools\ok.exe`,
		`C:\Tools\ok.bat`, `C:\Tools\ok.exe:stream`, `C:\Tools\ok.exe `, `C:\Tools\*.exe`, `C:\NUL\ok.exe`} {
		if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: p}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Launch(%q) = %v, want invalid_argument", p, err)
		}
	}
	if n := len(e.launcher.CallsTo("Resolve")) + launchCount(e); n != 0 {
		t.Errorf("a malformed path reached the launcher (%d calls)", n)
	}
	// app_id と path の同時指定。
	app := registerNotepad(t, e)
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{AppID: app.ID, Path: pathOK}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("app_id + path = %v, want invalid_argument", err)
	}
}

// 実在しない・固定ディスクでない・リンク先が固定ディスクでないパスは、起動せず launch_failed になる（#36 と同じ起動場所の検査）。
func TestLaunchDenylistModeRequiresAnExistingFixedDiskExecutable(t *testing.T) {
	e := newModeEnv(t)
	e.launcher.AddExecutable(`E:\usb\tool.exe`)
	e.launcher.MarkNonFixed(`E:\usb\tool.exe`)
	e.launcher.AddLink(`C:\Link\net.exe`, `Z:\share\net.exe`)
	e.launcher.MarkNonFixed(`Z:\share\net.exe`)
	setMode(t, e, domain.LaunchModeDenylist)
	ctx := context.Background()

	for p, want := range map[string]domain.LaunchFailure{
		`C:\Tools\missing.exe`: domain.LaunchFileMissing,
		`E:\usb\tool.exe`:      domain.LaunchAccessDenied,
		`C:\Link\net.exe`:      domain.LaunchAccessDenied,
	} {
		_, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: p})
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeLaunchFailed || de.Detail["reason"] != want {
			t.Errorf("Launch(%q) = %v, want launch_failed(%s)", p, err, want)
		}
	}
	if n := launchCount(e); n != 0 {
		t.Errorf("%d launches for paths that fail the location check", n)
	}
}

// .exe 名のリンクの実体が .exe でない（.bat など）ものは、解決した実体で起動することになるため起動しない（CreateProcess は .bat を cmd.exe 経由で実行しうる。
// 名前の項目に当たらない綴りでの拒否リストのすり抜けを防ぐ）。
func TestLaunchDenylistModeRejectsLinksToNonExeTargets(t *testing.T) {
	e := newModeEnv(t)
	e.launcher.AddExecutable(`C:\Tools\run.bat`)
	e.launcher.AddLink(`C:\Link\innocent.exe`, `C:\Tools\run.bat`)
	setMode(t, e, domain.LaunchModeDenylist)
	ctx := context.Background()

	_, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: `C:\Link\innocent.exe`})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeLaunchFailed || de.Detail["reason"] != domain.LaunchAccessDenied {
		t.Errorf("Launch(link to .bat) = %v, want launch_failed(access_denied)", err)
	}
	if de != nil && (strings.Contains(de.Msg, "bat") || strings.Contains(de.Msg, `C:\`)) {
		t.Errorf("the error leaks the path: %q", de.Msg)
	}
	if n := launchCount(e); n != 0 {
		t.Errorf("%d launches for a link to a non-.exe target", n)
	}
}

// 内蔵エージェント（M4）も、同じ方式・同じ拒否リストに従う（別の経路を持たない）。
func TestLaunchByAgentFollowsTheSameModeAndDenylist(t *testing.T) {
	e := newModeEnv(t)
	agent := domain.Actor{Kind: domain.ActorAgent, Label: "agent", ClientID: "client-a", SessionID: "task-1"}
	mustAcquire(t, e, agent)
	ctx := context.Background()

	if _, err := e.control.Launch(ctx, agent, LaunchRequest{Path: pathOK}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("agent path launch in allowlist mode = %v, want invalid_argument", err)
	}
	setMode(t, e, domain.LaunchModeDenylist)
	if _, err := e.apps.CreateDenied(ctx, pathSys32Cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Launch(ctx, agent, LaunchRequest{Path: pathLink}); !errors.Is(err, domain.ErrAppDenied) {
		t.Errorf("agent launch of a link to a denied file = %v, want app_denied", err)
	}
	if n := launchCount(e); n != 0 {
		t.Fatalf("launches = %d", n)
	}
	if _, err := e.control.Launch(ctx, agent, LaunchRequest{Path: pathOK}); err != nil {
		t.Fatalf("agent launch of an unlisted path = %v", err)
	}
	en := lastLaunchEntry(t, e)
	if en.Kind != "agent" || en.Event != "agent.step" || auditDetail(t, en)["path"] != pathOK {
		t.Errorf("agent audit entry = %+v", en)
	}
}

// キルスイッチ中は path 指定でも起動しない。リース保持者が他にいれば path でも起動できない。
func TestLaunchByPathHonoursKillSwitchAndLease(t *testing.T) {
	e := newModeEnv(t)
	setMode(t, e, domain.LaunchModeDenylist)
	ctx := context.Background()
	mustAcquire(t, e, ctlBob)
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: pathOK}); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("path launch while another holds the lease = %v, want lease_held_by_other", err)
	}
	e.leases.Release(ctlBob)
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: pathOK}); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("path launch under the kill switch = %v, want kill_switch_active", err)
	}
	if n := launchCount(e); n != 0 {
		t.Errorf("launches = %d", n)
	}
}

// 試行の監査を書けなければ、path 指定でも起動しない（fail-closed）。
func TestLaunchByPathFailsClosedWhenTheAttemptCannotBeAudited(t *testing.T) {
	e := newModeEnv(t)
	setMode(t, e, domain.LaunchModeDenylist)
	e.audit.SetSyncErr(errors.New("disk full"))
	if _, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{Path: pathOK}); !errors.Is(err, domain.ErrAuditUnavailable) {
		t.Errorf("Launch = %v, want audit_unavailable", err)
	}
	if n := launchCount(e); n != 0 {
		t.Errorf("launches = %d", n)
	}
}

// 登録時にファイルが無かったパス項目（入力の綴りのまま保存される）は、あとでそのパスがリンクやジャンクションになっても、
// 起動のたびに再解決して照合する（拒否が静かに効かなくならない）。
func TestLaunchDenylistModeReresolvesEntriesRegisteredBeforeTheFileExisted(t *testing.T) {
	e := newModeEnv(t)
	setMode(t, e, domain.LaunchModeDenylist)
	ctx := context.Background()

	const later, realFile = `C:\Later\x.exe`, `C:\Real\x.exe`
	entry, err := e.apps.CreateDenied(ctx, later) // まだ無い: 入力の綴りのまま保存される
	if err != nil || entry.Value != later {
		t.Fatalf("CreateDenied = %+v, %v", entry, err)
	}
	e.launcher.AddExecutable(realFile)
	e.launcher.AddLink(later, realFile) // 登録のあとで、そのパスが実体への経路になった
	e.audit.entries = nil

	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: realFile}); !errors.Is(err, domain.ErrAppDenied) {
		t.Errorf("Launch(the real file behind a denied path) = %v, want app_denied", err)
	}
	if n := launchCount(e); n != 0 {
		t.Errorf("the denied executable was started (%d launches)", n)
	}
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{Path: pathOK}); err != nil {
		t.Errorf("an unrelated path = %v, want started", err)
	}
}

// resolveFailsFor は特定のパスの Resolve だけを指定のエラーで失敗させる起動の偽物。
type resolveFailsFor struct {
	*platformtest.FakeAppLauncher
	path string
	err  error
}

func (r resolveFailsFor) Resolve(ctx context.Context, p string) (string, error) {
	if p == r.path {
		return "", r.err
	}
	return r.FakeAppLauncher.Resolve(ctx, p)
}

// 拒否リストのパス項目の再解決: 「解決できない項目」（ファイルが無い・固定ディスクにない・形が合わない）は元の綴りで照合し、
// それ以外の失敗（想定外のエラー・ctx の取り消し）は取りこぼしたまま進めずエラーを返す（fail closed）。
func TestWithReresolvedPathsIsFailClosed(t *testing.T) {
	const entry = `C:\Later\x.exe`
	denied := []domain.DeniedApp{{ID: "d1", Kind: domain.DeniedPath, Value: entry}, {ID: "d2", Kind: domain.DeniedName, Value: "cmd.exe"}}
	boom := errors.New("transient OS failure")
	run := func(err error) ([]domain.DeniedApp, error) {
		fake := platformtest.NewFakeAppLauncher()
		c := &control{launcher: resolveFailsFor{FakeAppLauncher: fake, path: entry, err: err}}
		return c.withReresolvedPaths(context.Background(), denied)
	}

	for name, err := range map[string]error{
		"file missing":         &platform.LaunchError{Reason: domain.LaunchFileMissing, Err: boom},
		"not a fixed drive":    &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: boom},
		"malformed for the OS": domain.ErrInvalidArgument,
	} {
		got, gotErr := run(err)
		if gotErr != nil || !slices.Equal(got, denied) {
			t.Errorf("%s: = %+v, %v, want the entries unchanged", name, got, gotErr)
		}
	}
	for name, err := range map[string]error{"unexpected failure": boom, "canceled": context.Canceled} {
		if got, gotErr := run(err); gotErr == nil || got != nil {
			t.Errorf("%s: = %+v, %v, want an error and no entries", name, got, gotErr)
		}
	}
}
