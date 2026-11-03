package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// registerNotepad は notepadInput を許可リストに登録して返す。
func registerNotepad(t *testing.T, e *ctlEnv) domain.LaunchableApp {
	t.Helper()
	a, err := e.apps.Create(context.Background(), notepadInput())
	if err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil // 登録の監査は、起動の監査と混ぜない
	return a
}

// lastLaunchEntry は直近の監査エントリが起動の記録（mcp は tool.launch_app、agent は agent.step の tool = launch_app）であることを確かめて返す。
func lastLaunchEntry(t *testing.T, e *ctlEnv) repository.AuditEntry {
	t.Helper()
	entries := e.audit.Entries()
	if len(entries) == 0 {
		t.Fatal("no audit entry")
	}
	en := entries[len(entries)-1]
	isMCP := en.Kind == "tool" && en.Event == "tool.launch_app"
	isAgent := en.Kind == "agent" && en.Event == "agent.step"
	if (!isMCP && !isAgent) || en.Tool != "launch_app" {
		t.Fatalf("entry = %+v, want tool.launch_app (or agent.step with tool launch_app)", en)
	}
	return en
}

// assertNoSecrets は監査の detail とエラーに、パス・引数・作業ディレクトリが現れないことを確かめる。
func assertNoSecrets(t *testing.T, en repository.AuditEntry, err error) {
	t.Helper()
	for _, secret := range []string{appNotepad, `notepad.exe`, appArgMark, appWorkMark} {
		if strings.Contains(string(en.DetailJSON), secret) {
			t.Errorf("audit detail leaks %q: %s", secret, en.DetailJSON)
		}
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
		var de *domain.Error
		if errors.As(err, &de) {
			for k, v := range de.Detail {
				if s, ok := v.(string); ok && strings.Contains(s, secret) {
					t.Errorf("error detail %s leaks %q", k, secret)
				}
			}
		}
	}
}

func TestControlLaunchStartsTheRegisteredApp(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	ctx := context.Background()

	res, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.AppID != app.ID || res.PID == 0 {
		t.Errorf("result = %+v", res)
	}

	calls := e.launcher.CallsTo("Launch")
	if len(calls) != 1 {
		t.Fatalf("Launch calls = %+v", calls)
	}
	spec, _ := calls[0].Args[0].(platform.LaunchSpec)
	if spec.ExePath != appNotepad || !slices.Equal(spec.Args, []string{appArgMark}) || spec.WorkingDir != appWorkMark {
		t.Errorf("spec = %+v, want the registered values only", spec)
	}

	// 未保持の mcp はリースを暗黙に取得する（FR-L04）。
	h, ok := e.leases.Holder()
	if !ok || !h.Actor.SameSession(ctlAlice) {
		t.Fatalf("holder = %+v, %v; want alice", h, ok)
	}

	en := lastLaunchEntry(t, e)
	if en.Result != "ok" || en.ActorType != domain.ActorMCP || en.ClientID != ctlAlice.ClientID || en.SessionID != ctlAlice.SessionID ||
		en.LeaseID != h.LeaseID || en.ErrorCode != "" || en.DurationMS == nil {
		t.Errorf("entry = %+v", en)
	}
	d := auditDetail(t, en)
	if len(d) != 4 || d["app_id"] != app.ID || d["label"] != "メモ帳" || d["pid"] != float64(res.PID) || d["reused_existing_window"] != true {
		t.Errorf("detail = %v, want exactly app_id, label, pid and reused_existing_window", d)
	}
	assertNoSecrets(t, en, nil)
}

// 同じアプリが起動中でも、毎回新しいプロセスを起動する。起動は入力のセマフォを使わない（リースの検査は活動として数えるので、
// アイドルタイマーは延びる）。
func TestControlLaunchStartsANewProcessEveryTime(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	first, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	if first.PID == second.PID {
		t.Errorf("both launches returned pid %d", first.PID)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 2 {
		t.Errorf("Launch calls = %d, want 2", n)
	}
}

func TestControlLaunchKillSwitchIsCheckedFirst(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	// 他者がリースを保持し、ID も未登録でも、最初に見るのはキルスイッチ。
	if _, err := e.leases.Acquire(context.Background(), ctlBob); err != nil {
		t.Fatal(err)
	}
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil

	for _, id := range []string{app.ID, "unknown"} {
		_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: id})
		if !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("err = %v, want kill_switch_active", err)
		}
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched %d times during the kill switch", n)
	}
	en := lastLaunchEntry(t, e)
	if en.Result != "denied" || en.ErrorCode != "kill_switch_active" {
		t.Errorf("entry = %+v", en)
	}
	if d := auditDetail(t, en); d["label"] != nil || d["pid"] != nil || d["reason"] != nil {
		t.Errorf("detail = %v, want only app_id", d)
	}
}

func TestControlLaunchLeaseHeldByOtherIsDeniedBeforeTheListIsRead(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	if _, err := e.leases.Acquire(context.Background(), ctlBob); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil

	// 未登録の ID でも lease_held_by_other（app_not_found で存在を探らせない）。
	for _, id := range []string{app.ID, "unknown"} {
		_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: id})
		if !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Fatalf("id %q: err = %v, want lease_held_by_other", id, err)
		}
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched %d times without the lease", n)
	}
	en := lastLaunchEntry(t, e)
	if en.Result != "denied" || en.ErrorCode != "lease_held_by_other" {
		t.Errorf("entry = %+v", en)
	}
	if h, _ := e.leases.Holder(); !h.Actor.SameSession(ctlBob) {
		t.Errorf("the holder changed: %+v", h)
	}
}

// mcp 以外は暗黙にリースを取らない。
func TestControlLaunchNeedsTheLeaseForNonMCPActors(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	web := domain.Actor{Kind: domain.ActorWeb, ClientID: "client-w", Label: "web", SessionID: "viewer-1"}
	_, err := e.control.Launch(context.Background(), web, LaunchRequest{AppID: app.ID})
	if !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("err = %v, want lease_required", err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched %d times without the lease", n)
	}
	if _, ok := e.leases.Holder(); ok {
		t.Error("a non-MCP actor must not acquire the lease implicitly")
	}
}

func TestControlLaunchUnknownAppIsDenied(t *testing.T) {
	e := newAppEnv(t)
	registerNotepad(t, e)
	long := strings.Repeat("あ", 100)

	_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: long})
	if !errors.Is(err, domain.ErrAppNotFound) {
		t.Fatalf("err = %v, want app_not_found", err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched %d times", n)
	}
	// ②のリースの確認は③より前なので、未登録の ID でもリースは取得済み。
	if h, ok := e.leases.Holder(); !ok || !h.Actor.SameSession(ctlAlice) {
		t.Errorf("holder = %+v, %v", h, ok)
	}
	en := lastLaunchEntry(t, e)
	if en.Result != "denied" || en.ErrorCode != "app_not_found" {
		t.Errorf("entry = %+v", en)
	}
	d := auditDetail(t, en)
	if len(d) != 1 || d["app_id"] != strings.Repeat("あ", 64) {
		t.Errorf("detail = %v, want only the app_id cut to 64 characters", d)
	}
}

// 保存済みの行が登録時の規則を満たさなくなっていたら（DB を手で書き換えたなど）、起動しない（fail closed。NFR-S20 (3)）。
func TestControlLaunchRechecksTheStoredRow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.LaunchableApp)
	}{
		{"relative exe path", func(a *domain.LaunchableApp) { a.ExePath = `notepad.exe` }},
		{"UNC exe path", func(a *domain.LaunchableApp) { a.ExePath = `\\host\share\notepad.exe` }},
		{"not an exe", func(a *domain.LaunchableApp) { a.ExePath = `C:\Tools\notepad.bat` }},
		{"empty argument", func(a *domain.LaunchableApp) { a.Args = []string{""} }},
		{"relative working dir", func(a *domain.LaunchableApp) { a.WorkingDir = `work` }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAppEnv(t)
			app := registerNotepad(t, e)
			// AppRegistry を通さずに、保存済みの行だけを書き換える。
			bad := app
			tt.mutate(&bad)
			if err := e.appStore.Update(context.Background(), bad); err != nil {
				t.Fatal(err)
			}

			_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
			if !errors.Is(err, domain.ErrAppNotFound) {
				t.Fatalf("err = %v, want app_not_found", err)
			}
			if n := len(e.launcher.CallsTo("Launch")); n != 0 {
				t.Errorf("launched a row that fails CheckLaunchTarget (%d calls)", n)
			}
			en := lastLaunchEntry(t, e)
			if en.Result != "denied" || en.ErrorCode != "app_not_found" {
				t.Errorf("entry = %+v", en)
			}
			assertNoSecrets(t, en, err)
		})
	}
}

func TestControlLaunchFailureReasons(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*ctlEnv)
		reason domain.LaunchFailure
	}{
		// 登録のあとで実行ファイルが消えた（登録時の存在確認は起動を保証しない）: 偽の launcher に実行ファイルが無い状態。
		{"file missing", func(e *ctlEnv) { e.launcher = platformtest.NewFakeAppLauncher(); e.rebuildControl(t) }, domain.LaunchFileMissing},
		{"access denied", func(e *ctlEnv) { e.launcher.DenyAccess(appNotepad) }, domain.LaunchAccessDenied},
		{"elevation required", func(e *ctlEnv) { e.launcher.RequireElevation(appNotepad) }, domain.LaunchElevationRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAppEnv(t)
			app := registerNotepad(t, e)
			tt.setup(e)

			_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
			if !errors.Is(err, domain.ErrLaunchFailed) {
				t.Fatalf("err = %v, want launch_failed", err)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Detail["reason"] != tt.reason || len(de.Detail) != 1 {
				t.Fatalf("err = %v, want only reason=%s", err, tt.reason)
			}
			en := lastLaunchEntry(t, e)
			if en.Result != "error" || en.ErrorCode != "launch_failed" {
				t.Errorf("entry = %+v", en)
			}
			d := auditDetail(t, en)
			if len(d) != 3 || d["app_id"] != app.ID || d["label"] != "メモ帳" || d["reason"] != string(tt.reason) {
				t.Errorf("detail = %v, want app_id, label and reason", d)
			}
			assertNoSecrets(t, en, err)
		})
	}
}

// rebuildControl は e.launcher を差し替えたあとに、Control（と許可リストの AppRegistry）を作り直す。許可リストの行は残る。
func (e *ctlEnv) rebuildControl(t *testing.T) {
	t.Helper()
	apps, err := NewAppRegistry(AppRegistryConfig{
		Apps: e.appStore, Denied: e.denyStore, Settings: e.settings, Launcher: e.launcher, Picker: e.picker, Audit: e.audit, Clock: e.clock, IDs: &ctlIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.apps = apps
	e.control, err = NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// launcher のその他の失敗: 組めない値（ErrInvalidArgument）は launch_failed(other)、想定外のエラーはそのまま（mcp が internal にする）。
func TestControlLaunchOtherLauncherFailures(t *testing.T) {
	t.Run("invalid argument becomes launch_failed other", func(t *testing.T) {
		e := newAppEnv(t)
		app := registerNotepad(t, e)
		e.launcher.SetError("Launch", domain.ErrInvalidArgument.WithMsg(`bad command line for `+appNotepad))
		_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeLaunchFailed || de.Detail["reason"] != domain.LaunchOther {
			t.Fatalf("err = %v, want launch_failed(other)", err)
		}
		assertNoSecrets(t, lastLaunchEntry(t, e), err)
	})
	t.Run("a LaunchError with an unknown reason is other", func(t *testing.T) {
		e := newAppEnv(t)
		app := registerNotepad(t, e)
		e.launcher.SetError("Launch", &platform.LaunchError{Reason: domain.LaunchFailure(appNotepad), Err: errors.New("os")})
		_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
		var de *domain.Error
		if !errors.As(err, &de) || de.Detail["reason"] != domain.LaunchOther {
			t.Fatalf("err = %v, want reason other", err)
		}
		assertNoSecrets(t, lastLaunchEntry(t, e), err)
	})
	t.Run("an unexpected error passes through and is audited as an error", func(t *testing.T) {
		e := newAppEnv(t)
		app := registerNotepad(t, e)
		boom := errors.New("boom")
		e.launcher.SetError("Launch", boom)
		_, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		en := lastLaunchEntry(t, e)
		if en.Result != "error" || en.ErrorCode != "internal" {
			t.Errorf("entry = %+v", en)
		}
		if d := auditDetail(t, en); d["reason"] != nil {
			t.Errorf("detail = %v, want no reason", d)
		}
	})
}

// 取得済みのリースの検査のあとにキルスイッチが発動しても、起動しない。
func TestControlLaunchRechecksTheKillSwitchBeforeStarting(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	// 許可リストの読み取りの最中に発動したことにする。
	triggering := &triggerOnGetApps{AppReader: e.apps, trigger: func() { _ = e.kill.Trigger("test") }}
	ctl, err := NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: triggering, Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctl.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
	if !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("err = %v, want kill_switch_active", err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched %d times after the kill switch fired", n)
	}
	if en := lastLaunchEntry(t, e); en.Result != "denied" || en.ErrorCode != "kill_switch_active" {
		t.Errorf("entry = %+v", en)
	}
}

type triggerOnGetApps struct {
	AppReader
	trigger func()
}

func (a *triggerOnGetApps) Get(ctx context.Context, id string) (domain.LaunchableApp, error) {
	app, err := a.AppReader.Get(ctx, id)
	a.trigger()
	return app, err
}

// 許可リストの読み取りが「無い」以外のエラーで失敗したら、起動せず、監査は error/internal で、エラーにパスが出ない。
func TestControlLaunchAppReaderFailureDoesNotLaunch(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	boom := errors.New("db unavailable")
	ctl, err := NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: failingAppReader{AppReader: e.apps, err: boom}, Leases: e.leases, Kill: e.kill,
		Settings: e.settings, Audit: e.audit, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctl.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the reader's error", err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched %d times although the list could not be read", n)
	}
	en := lastLaunchEntry(t, e)
	if en.Result != "error" || en.ErrorCode != "internal" {
		t.Errorf("entry = %+v, want result=error error_code=internal", en)
	}
	if d := auditDetail(t, en); len(d) != 1 || d["app_id"] != app.ID {
		t.Errorf("detail = %v, want only app_id", d)
	}
	assertNoSecrets(t, en, err)
}

// failingAppReader は Get だけが err で失敗する AppReader。
type failingAppReader struct {
	AppReader
	err error
}

func (r failingAppReader) Get(context.Context, string) (domain.LaunchableApp, error) {
	return domain.LaunchableApp{}, r.err
}

// agent 種別の主体。リースを保持していない agent は、暗黙の取得もできない（取得するのは Agent の run_task。FR-L08）:
// lease_required で拒否され、起動しない。監査は agent.step（kind = agent）で actor_type は agent。
func TestControlLaunchAgentWithoutLeaseIsRejectedAndAuditedAsAgent(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	agent := domain.Actor{Kind: domain.ActorAgent, Label: "agent", ClientID: "client-a", SessionID: "task-1"}

	_, err := e.control.Launch(context.Background(), agent, LaunchRequest{AppID: app.ID})
	if !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("err = %v, want lease_required", err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched %d times without the lease", n)
	}
	if _, ok := e.leases.Holder(); ok {
		t.Error("an agent must not acquire the lease implicitly")
	}
	en := lastLaunchEntry(t, e)
	if en.Kind != "agent" || en.Result != "denied" || en.ErrorCode != "lease_required" || en.ActorType != domain.ActorAgent ||
		en.SessionID != agent.SessionID || en.TaskID != agent.SessionID {
		t.Errorf("entry = %+v", en)
	}
}

// 読めない行（args_json が文字列の配列でない）は app_not_found で拒否し（監査 result=denied）、
// 一覧と削除は成功する（ホスト所有者が GUI で消せる）。
func TestControlLaunchCorruptRowIsDeniedButListedAndDeletable(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	other, err := e.apps.Create(context.Background(), domain.AppInput{Label: "電卓", ExePath: appCalc})
	if err != nil {
		t.Fatal(err)
	}
	corruptArgsJSON(t, e.appDB, app.ID, `[1]`)
	e.audit.entries = nil

	_, err = e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: app.ID})
	if !errors.Is(err, domain.ErrAppNotFound) {
		t.Fatalf("err = %v, want app_not_found", err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("launched a corrupt row (%d calls)", n)
	}
	en := lastLaunchEntry(t, e)
	if en.Result != "denied" || en.ErrorCode != "app_not_found" {
		t.Errorf("entry = %+v", en)
	}
	assertNoSecrets(t, en, err)

	// 一覧は全体が失敗せず、壊れていない行は起動できる。
	list, err := e.apps.List(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %+v, %v; want both rows", list, err)
	}
	if _, err := e.control.Launch(context.Background(), ctlAlice, LaunchRequest{AppID: other.ID}); err != nil {
		t.Errorf("the healthy row failed to launch: %v", err)
	}

	if err := e.apps.Delete(context.Background(), app.ID); err != nil {
		t.Fatalf("Delete of a corrupt row = %v", err)
	}
	if _, err := e.apps.Get(context.Background(), app.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
}

// 読めない行（args_json が壊れている）を、有効な入力で更新すると直る: Corrupt が外れ、引数は入力どおりになり、
// 起動できる。監査の changed には、壊れていた引数が破棄されたことを示す args が必ず入る（引数・作業ディレクトリは載せない）。
func TestAppRegistryUpdateRepairsACorruptRow(t *testing.T) {
	ctx := context.Background()
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	corruptArgsJSON(t, e.appDB, app.ID, `[1]`)
	if got, err := e.apps.Get(ctx, app.ID); err != nil || !got.Corrupt {
		t.Fatalf("Get = %+v, %v; want a Corrupt row", got, err)
	}
	e.audit.entries = nil

	// 表示名・実行ファイル・作業ディレクトリは変えず、引数だけ入れ直す。
	in := notepadInput() // Args は appArgMark のまま（壊す前と同じ値を入れ直す）
	upd, err := e.apps.Update(ctx, app.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Corrupt {
		t.Error("Update returned a Corrupt row")
	}
	got, err := e.apps.Get(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Corrupt || !slices.Equal(got.Args, in.Args) {
		t.Errorf("stored = Corrupt %v, Args %q; want not Corrupt with Args %q", got.Corrupt, got.Args, in.Args)
	}
	if err := domain.CheckLaunchTarget(got); err != nil {
		t.Errorf("CheckLaunchTarget = %v, want nil", err)
	}
	if _, err := e.control.Launch(ctx, ctlAlice, LaunchRequest{AppID: app.ID}); err != nil {
		t.Errorf("Launch after the repair = %v", err)
	}

	// 監査: 変わったのは args だけ（表示名などは同じ）だが、壊れていた引数の破棄として args を必ず記録する。
	var en repository.AuditEntry
	for _, x := range e.audit.Entries() {
		if x.Event == "settings.launchable_app.update" {
			en = x
		}
	}
	if en.Event == "" {
		t.Fatal("no settings.launchable_app.update entry")
	}
	d := auditDetail(t, en)
	if d["app_id"] != app.ID || d["label"] != in.Label || d["exe_path"] != in.ExePath || d["arg_count"] != float64(1) {
		t.Errorf("detail = %v", d)
	}
	if changed, _ := d["changed"].([]any); fmt.Sprint(changed) != "[args]" {
		t.Errorf("changed = %v, want [args]", changed)
	}
	if s := string(en.DetailJSON); strings.Contains(s, appArgMark) || strings.Contains(s, appWorkMark) {
		t.Errorf("arguments or the working directory leaked into the audit detail: %s", s)
	}
}

// corruptArgsJSON は DB ファイルの行の args_json を AppRegistry を通さずに書き換える（手で書き換えられた状態を作る）。
func corruptArgsJSON(t *testing.T, dbPath, id, argsJSON string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	res, err := db.Exec(`UPDATE launchable_apps SET args_json = ? WHERE id = ?`, argsJSON, id)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("rows updated = %d, want 1", n)
	}
}

func TestControlLaunchableApps(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()

	empty, err := e.control.LaunchableApps(ctx)
	if err != nil || empty.Apps == nil || len(empty.Apps) != 0 || empty.Mode != domain.LaunchModeAllowlist {
		t.Fatalf("empty list = %#v, %v; want a non-nil empty slice in allowlist mode", empty, err)
	}

	var ids = map[string]string{}
	for _, in := range []domain.AppInput{
		{Label: "b-app", ExePath: appCalc, Args: []string{appArgMark}},
		{Label: "A-app", ExePath: appNotepad, WorkingDir: appWorkMark},
	} {
		a, err := e.apps.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		ids[in.Label] = a.ID
	}
	e.audit.entries = nil

	got, err := e.control.LaunchableApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.AppSummary{{ID: ids["A-app"], Label: "A-app"}, {ID: ids["b-app"], Label: "b-app"}}
	if !slices.Equal(got.Apps, want) {
		t.Errorf("apps = %+v, want %+v", got.Apps, want)
	}
	// リース不要で、リースを取らない。監査は mcp 層が tool.list_launchable_apps として記録する（Control は記録しない）。
	if _, ok := e.leases.Holder(); ok {
		t.Error("listing must not acquire the lease")
	}
	if n := len(e.audit.Entries()); n != 0 {
		t.Errorf("Control recorded %d audit entries for the listing", n)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 0 {
		t.Errorf("listing launched %d times", n)
	}
}

// 起動できない行（読めない Corrupt の行）は、MCP に見せる一覧に載せない。
func TestControlLaunchableAppsOmitsRowsThatCannotLaunch(t *testing.T) {
	ctx := context.Background()
	e := newAppEnv(t)
	bad := registerNotepad(t, e)
	good, err := e.apps.Create(ctx, domain.AppInput{Label: "電卓", ExePath: appCalc})
	if err != nil {
		t.Fatal(err)
	}
	corruptArgsJSON(t, e.appDB, bad.ID, `[1]`)

	got, err := e.control.LaunchableApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []domain.AppSummary{{ID: good.ID, Label: "電卓"}}; !slices.Equal(got.Apps, want) {
		t.Errorf("apps = %+v, want %+v", got.Apps, want)
	}
}

func TestControlLaunchableAppsListingWorksWhileAnotherSessionHoldsTheLease(t *testing.T) {
	e := newAppEnv(t)
	registerNotepad(t, e)
	if _, err := e.leases.Acquire(context.Background(), ctlBob); err != nil {
		t.Fatal(err)
	}
	if got, err := e.control.LaunchableApps(context.Background()); err != nil || len(got.Apps) != 1 {
		t.Errorf("apps = %+v, %v", got, err)
	}
}

func TestControlLaunchableAppsKillSwitch(t *testing.T) {
	e := newAppEnv(t)
	registerNotepad(t, e)
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.LaunchableApps(context.Background()); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("err = %v, want kill_switch_active", err)
	}
}

func TestNewControlRequiresLauncherAndApps(t *testing.T) {
	e := newCtlEnv(t)
	base := ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Clock: e.clock,
	}
	if _, err := NewControl(base); err != nil {
		t.Fatal(err)
	}
	for name, clear := range map[string]func(*ControlConfig){
		"Launcher": func(c *ControlConfig) { c.Launcher = nil },
		"Apps":     func(c *ControlConfig) { c.Apps = nil },
	} {
		cfg := base
		clear(&cfg)
		if _, err := NewControl(cfg); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("NewControl without %s: err = %v", name, err)
		}
	}
}

// 監査の reason は API の 4 値だけ。型は LaunchFailure でも未知の値（パスが紛れ込みうる）は載せない。
func TestLaunchAuditDetailOnlyRecordsKnownReasons(t *testing.T) {
	tests := []struct {
		name   string
		reason any
		want   string // 空なら reason を載せない
	}{
		{"known", domain.LaunchAccessDenied, "access_denied"},
		{"typed but unknown", domain.LaunchFailure(appNotepad), ""},
		{"typed empty", domain.LaunchFailure(""), ""},
		{"untyped string", appNotepad, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.ErrLaunchFailed.WithDetail("reason", tt.reason)
			d := launchAuditDetail(LaunchRequest{AppID: "app-1"}, launchTrace{label: "メモ帳"}, LaunchResult{}, err)
			got, has := d["reason"]
			if tt.want == "" {
				if has {
					t.Errorf("detail = %v, want no reason", d)
				}
			} else if got != tt.want {
				t.Errorf("reason = %v, want %s", got, tt.want)
			}
			for _, v := range d {
				if s, ok := v.(string); ok && strings.Contains(s, `notepad`) {
					t.Errorf("detail leaks the injected value: %v", d)
				}
			}
		})
	}
}

// リースを保持している agent は、許可リストの同じ AppRegistry のアプリを起動できる（M4 の Agent も別の許可リスト・起動経路を持たない）。
// 起動するのは登録された値だけで、監査は agent.step（actor_type=agent・task_id・保持者のリース ID 付き・パスや引数なし）。
// リースを保持していない他の agent は起動できない（M1.5 の mcp と同じ検査）。agent は暗黙にリースを取得しない。
func TestControlLaunchByLeaseHoldingAgentSucceeds(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	agent := domain.Actor{Kind: domain.ActorAgent, Label: "agent", ClientID: "client-a", SessionID: "task-1"}
	lease := mustAcquire(t, e, agent)
	e.audit.entries = nil
	ctx := context.Background()

	res, err := e.control.Launch(WithAgentStep(ctx, AgentStep{No: 2, Route: AgentRouteOpenAI}), agent, LaunchRequest{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.AppID != app.ID || res.PID == 0 {
		t.Errorf("result = %+v", res)
	}
	calls := e.launcher.CallsTo("Launch")
	if len(calls) != 1 {
		t.Fatalf("Launch calls = %+v", calls)
	}
	spec, _ := calls[0].Args[0].(platform.LaunchSpec)
	if spec.ExePath != appNotepad || !slices.Equal(spec.Args, []string{appArgMark}) || spec.WorkingDir != appWorkMark {
		t.Errorf("spec = %+v, want the registered values only", spec)
	}

	en := lastLaunchEntry(t, e)
	if en.Kind != "agent" || en.Result != "ok" || en.ActorType != domain.ActorAgent || en.SessionID != agent.SessionID || en.TaskID != agent.SessionID ||
		en.ClientLabel != agent.Label || en.LeaseID != lease.ID || en.ErrorCode != "" || en.DurationMS == nil ||
		en.StepNo == nil || *en.StepNo != 2 {
		t.Errorf("entry = %+v", en)
	}
	d := auditDetail(t, en)
	if len(d) != 5 || d["route"] != "openai" || d["app_id"] != app.ID || d["label"] != "メモ帳" || d["pid"] != float64(res.PID) || d["reused_existing_window"] != true {
		t.Errorf("detail = %v, want route, app_id, label, pid and reused_existing_window", d)
	}
	assertNoSecrets(t, en, nil)

	// 保持者でない別の agent は、同じ検査で拒否され、起動しない。
	other := domain.Actor{Kind: domain.ActorAgent, Label: "agent", ClientID: "client-a", SessionID: "task-2"}
	if _, err := e.control.Launch(ctx, other, LaunchRequest{AppID: app.ID}); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Fatalf("other agent: err = %v, want lease_held_by_other", err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 1 {
		t.Errorf("Launch calls = %d, want still 1", n)
	}
	if h, ok := e.leases.Holder(); !ok || !h.Actor.SameSession(agent) {
		t.Error("a rejected agent changed the lease holder")
	}
	if en := lastLaunchEntry(t, e); en.Result != "denied" || en.ErrorCode != "lease_held_by_other" || en.ActorType != domain.ActorAgent {
		t.Errorf("entry = %+v", en)
	}
}
