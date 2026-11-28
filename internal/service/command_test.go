package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/localfs"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// cmdEnv はコマンド実行のテストの部品: 本物のリース・キルスイッチ・ファイル転送（実ファイルシステム）・許可リストのファイル（一時フォルダ）と、偽の CommandRunner。
type cmdEnv struct {
	*ftEnv
	runner *platformtest.FakeCommandRunner
	cx     CommandExec
	list   string // 既定の許可リストのファイル
	exe    string // 許可リストの項目が使う実行ファイル（置き場所の外）
}

func newCmdEnv(t *testing.T) *cmdEnv {
	t.Helper()
	fe := newFtEnv(t)
	e := &cmdEnv{ftEnv: fe, runner: platformtest.NewFakeCommandRunner(), list: filepath.Join(fe.dataDir, "commands.json"), exe: filepath.Join(fe.base, "bin", "tool.exe")}
	if err := os.MkdirAll(fe.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.cx = e.newExec(fe.ctlEnv)
	return e
}

func (e *cmdEnv) newExec(c *ctlEnv) CommandExec {
	e.t.Helper()
	cx, err := NewCommandExec(CommandExecConfig{
		Runner: e.runner, Files: e.ft, Leases: c.leases, Kill: c.kill, Settings: c.settings,
		DefaultAllowlistPath: e.list, DefaultInboxRoot: e.root, Audit: c.audit, Clock: c.clock,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return cx
}

func (e *cmdEnv) enable() { e.settings.put(settingCommandEnabled, "true") }

// writeList は許可リストのファイルを書く。entries は commands の要素（実行ファイルの既定は e.exe）。
func (e *cmdEnv) writeList(entries ...map[string]any) {
	e.t.Helper()
	for _, en := range entries {
		if _, ok := en["executable"]; !ok {
			en["executable"] = e.exe
		}
	}
	b, err := json.Marshal(map[string]any{"version": 1, "commands": entries})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.list, b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *cmdEnv) run(who domain.Actor, name string, args map[string]any) (CommandRunResult, error) {
	e.t.Helper()
	return e.cx.Run(context.Background(), who, CommandRunRequest{Name: name, Args: args})
}

// sampleEntries は代表的な項目: 固定、型つき差し込み、無効、置き場所のインストーラー。
func (e *cmdEnv) sampleEntries() []map[string]any {
	return []map[string]any{
		{"name": "version", "description": "app version", "args": []string{"--version"}, "enabled": true, "timeout_sec": 7, "max_output_bytes": 4096, "working_dir": e.base},
		{"name": "kill-pid", "args": []string{"-p", "{pid}", "{name}"}, "enabled": true, "params": map[string]any{
			"pid":  map[string]any{"type": "int", "min": 1, "max": 4294967295},
			"name": map[string]any{"type": "string", "pattern": `[A-Za-z0-9._-]{1,64}`},
		}},
		{"name": "off", "args": []string{"x"}}, // enabled を省略 = 無効
		{"name": "install", "args": []string{"/i", "{file}", "/qn"}, "enabled": true, "params": map[string]any{
			"file": map[string]any{"type": "inbox_path", "extensions": []string{".msi"}},
		}},
	}
}

func (e *cmdEnv) detail(en repository.AuditEntry) map[string]any {
	e.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(en.DetailJSON, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

// lastToolEntry は最後の tool.<name> の監査行。
func (e *cmdEnv) lastToolEntry(name string) repository.AuditEntry {
	e.t.Helper()
	all := e.audit.Entries()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Kind == "tool" && all[i].Tool == name {
			return all[i]
		}
	}
	e.t.Fatalf("no tool.%s audit entry in %v", name, e.audit.Events())
	return repository.AuditEntry{}
}

func TestCommandExecIsOffByDefault(t *testing.T) {
	e := newCmdEnv(t)
	e.writeList(e.sampleEntries()...)
	if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandDisabled) {
		t.Fatalf("Run with no setting = %v, want command_disabled", err)
	}
	if _, err := e.cx.List(context.Background(), ctlAlice); !errors.Is(err, domain.ErrCommandDisabled) {
		t.Fatalf("List with no setting = %v, want command_disabled", err)
	}
	e.settings.put(settingCommandEnabled, "false")
	if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandDisabled) {
		t.Fatalf("Run when off = %v", err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatal("a command was started while command execution is off")
	}
	// OFF のときは、リースを取りに行かない（承認を求めない）。
	if _, held := e.leases.Holder(); held {
		t.Error("a rejected call acquired the lease")
	}
	// 拒否は監査に残る（denied、distinct な error_code）。
	en := e.lastToolEntry("run_command")
	if en.Result != auditDenied || en.ErrorCode != string(domain.CodeCommandDisabled) {
		t.Errorf("audit = %+v", en)
	}
	// 設定が壊れていても ON にはならない（fail closed）。
	e.settings.put(settingCommandEnabled, `"yes"`)
	if _, err := e.run(ctlAlice, "version", nil); err == nil {
		t.Fatal("a malformed command.enabled turned the feature on")
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatal("a command was started")
	}
}

func TestCommandExecRunsAllowlistedCommand(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	e.runner.SetResult(platform.CommandResult{
		ExitCode: 3, Stdout: []byte("secret-output"), Stderr: []byte("err"), StdoutTruncated: true, Duration: 15 * time.Millisecond,
	})
	res, err := e.run(ctlAlice, "version", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "version" || res.ExitCode != 3 || string(res.Stdout) != "secret-output" || string(res.Stderr) != "err" || !res.StdoutTruncated || res.StderrTruncated || res.TimedOut || res.Duration != 15*time.Millisecond {
		t.Fatalf("result = %+v", res)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("specs = %d", len(specs))
	}
	s := specs[0]
	if s.Executable != e.exe || strings.Join(s.Args, "|") != "--version" || s.Dir != e.base || s.Timeout != 7*time.Second || s.MaxOutputBytes != 4096 {
		t.Errorf("spec = %+v", s)
	}
	// タイムアウトの結果も、エラーではなく結果で返る。
	e.runner.SetResult(platform.CommandResult{ExitCode: -1, TimedOut: true})
	if res, err = e.run(ctlAlice, "version", nil); err != nil || !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("timed out result = %+v, %v", res, err)
	}
	// 起動できない場合のエラーはそのまま返る。
	e.runner.SetError("Run", domain.ErrCommandStartFailed)
	if _, err = e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandStartFailed) {
		t.Fatalf("start failure = %v", err)
	}
}

func TestCommandExecRejectsUnknownAndDisabledEntries(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	for name, in := range map[string]string{"unknown": "nope", "disabled entry": "off", "empty": "", "case differs": "VERSION", "path-like": "../version", "shell": "version & calc"} {
		if _, err := e.run(ctlAlice, in, nil); !errors.Is(err, domain.ErrCommandNotFound) {
			t.Errorf("%s: err = %v, want command_not_found", name, err)
		}
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatal("a command was started")
	}
	if en := e.lastToolEntry("run_command"); en.Result != auditDenied || en.ErrorCode != string(domain.CodeCommandNotFound) {
		t.Errorf("audit = %+v", en)
	}
}

func TestCommandExecRejectsBadArguments(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	good := func() map[string]any { return map[string]any{"pid": 42, "name": "svc.exe"} }
	if _, err := e.run(ctlAlice, "kill-pid", good()); err != nil {
		t.Fatalf("valid call: %v", err)
	}
	if got := e.runner.Specs()[0].Args; strings.Join(got, "|") != "-p|42|svc.exe" {
		t.Errorf("argv = %q", got)
	}
	e.runner.Reset()
	mut := func(k string, v any) map[string]any { m := good(); m[k] = v; return m }
	bad := map[string]map[string]any{
		"shell & in string": mut("name", "a & calc"),
		"command subst":     mut("name", "$(x)"),
		"pipe":              mut("name", "| more"),
		"option injection":  mut("name", "-rf"),
		"pid out of range":  mut("pid", 0),
		"pid shell":         mut("pid", "1 & calc"),
		"extra arg":         mut("cmdline", "calc.exe"),
		"missing pid":       {"name": "x"},
		"no args at all":    nil,
	}
	for name, args := range bad {
		if _, err := e.run(ctlAlice, "kill-pid", args); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
	// 固定の項目に引数を渡すのも拒否。
	if _, err := e.run(ctlAlice, "version", map[string]any{"x": "y"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("args for a fixed command: %v", err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatal("a command was started for a rejected call")
	}
	if en := e.lastToolEntry("run_command"); en.Result != auditDenied || en.ErrorCode != string(domain.CodeInvalidArgument) {
		t.Errorf("audit = %+v", en)
	}
}

func TestCommandExecRequiresTheLease(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	ctx := context.Background()

	// リースを持たない mcp は暗黙に取得して実行できる（FR-L04）。取得の行が残る。
	if _, err := e.run(ctlAlice, "version", nil); err != nil {
		t.Fatalf("implicit acquire: %v", err)
	}
	if h, ok := e.leases.Holder(); !ok || !h.Actor.SameSession(ctlAlice) {
		t.Fatalf("holder = %+v, %v", h, ok)
	}
	if en := e.lastToolEntry("run_command"); en.LeaseID == "" {
		t.Error("the audit row has no lease id")
	}
	// 他者が保持中は拒否。
	e.runner.Reset()
	if _, err := e.run(ctlBob, "version", nil); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Fatalf("Bob while Alice holds the lease = %v", err)
	}
	// mcp 以外（Web UI など）は、リースが無ければ取得せず拒否する。
	e.leases.Release(ctlAlice)
	web := domain.Actor{Kind: domain.ActorWeb, ClientID: "web", SessionID: "w1"}
	if _, err := e.cx.Run(ctx, web, CommandRunRequest{Name: "version"}); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Fatalf("a web actor without the lease = %v", err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatal("a command was started without the lease")
	}
	if en := e.lastToolEntry("run_command"); en.Result != auditDenied {
		t.Errorf("audit = %+v", en)
	}
}

func TestCommandExecIsUnavailableToScopedLeases(t *testing.T) {
	te := newTgtEnv(t)
	te.settings.put(settingCommandEnabled, "true")
	dir := t.TempDir()
	exe, list := filepath.Join(dir, "bin", "t.exe"), filepath.Join(dir, "commands.json")
	b, _ := json.Marshal(map[string]any{"version": 1, "commands": []any{map[string]any{"name": "c", "executable": exe, "enabled": true}}})
	if err := os.WriteFile(list, b, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := platformtest.NewFakeCommandRunner()
	root := filepath.Join(dir, "inbox")
	ft, err := NewFileTransfer(FileTransferConfig{Files: localfs.New(), Leases: te.leases, Kill: te.kill, DefaultRoot: root, Clock: te.clock})
	if err != nil {
		t.Fatal(err)
	}
	cx, err := NewCommandExec(CommandExecConfig{
		Runner: runner, Files: ft, Leases: te.leases, Kill: te.kill, Settings: te.settings,
		DefaultAllowlistPath: list, DefaultInboxRoot: root, Audit: te.audit, Clock: te.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	te.acquire(ctlAlice)
	_, err = cx.Run(context.Background(), ctlAlice, CommandRunRequest{Name: "c"})
	var de *domain.Error
	if !errors.Is(err, domain.ErrInputBlocked) || !errors.As(err, &de) || de.Detail["reason"] != string(domain.ReasonCommandOutOfScope) {
		t.Fatalf("Run with a scoped lease: %v", err)
	}
	if len(runner.Specs()) != 0 {
		t.Fatal("a command was started under a scoped lease")
	}
}

func TestCommandExecKillSwitch(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("Run = %v", err)
	}
	if _, err := e.cx.List(context.Background(), ctlAlice); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("List = %v", err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatal("a command was started while the kill switch was active")
	}
}

func TestCommandExecCancelAllStopsRunningCommands(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	started := make(chan struct{})
	e.runner.SetHandler(func(ctx context.Context, _ platform.CommandSpec) (platform.CommandResult, error) {
		close(started)
		<-ctx.Done()
		return platform.CommandResult{}, ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := e.run(ctlAlice, "version", nil)
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("Run returned before the command started: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the command did not start")
	}
	if err := e.cx.CancelAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, domain.ErrKillSwitchActive) {
			t.Fatalf("Run after CancelAll = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the running command was not stopped")
	}
}

func TestCommandExecLimitsConcurrentRuns(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(maxConcurrentCommands)
	e.runner.SetHandler(func(context.Context, platform.CommandSpec) (platform.CommandResult, error) {
		started.Done()
		<-release
		return platform.CommandResult{}, nil
	})
	var wg sync.WaitGroup
	for range maxConcurrentCommands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.run(ctlAlice, "version", nil); err != nil {
				t.Errorf("Run: %v", err)
			}
		}()
	}
	started.Wait()
	if _, err := e.run(ctlAlice, "version", nil); domain.CodeOf(err) != domain.CodeConflict {
		t.Errorf("one over the limit = %v, want conflict", err)
	}
	close(release)
	wg.Wait()
	e.runner.SetHandler(nil)
	if _, err := e.run(ctlAlice, "version", nil); err != nil {
		t.Errorf("after the others finished: %v", err)
	}
}

func TestCommandExecAllowlistProblemsFailClosed(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	check := func(name string) {
		t.Helper()
		if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
			t.Errorf("%s: Run = %v, want command_allowlist_invalid", name, err)
		}
		if _, err := e.cx.List(context.Background(), ctlAlice); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
			t.Errorf("%s: List = %v, want command_allowlist_invalid", name, err)
		}
	}
	check("missing file")
	for name, body := range map[string]string{
		"not JSON":        "nope",
		"relative exe":    `{"version":1,"commands":[{"name":"version","executable":"tool.exe","enabled":true}]}`,
		"UNC exe":         `{"version":1,"commands":[{"name":"version","executable":"\\\\host\\share\\t.exe","enabled":true}]}`,
		"batch file":      `{"version":1,"commands":[{"name":"version","executable":"` + filepath.ToSlash(e.exe) + `.bat","enabled":true}]}`,
		"one bad of many": `{"version":1,"commands":[{"name":"version","executable":"` + filepath.ToSlash(e.exe) + `","enabled":true},{"name":"bad","executable":"x"}]}`,
	} {
		if err := os.WriteFile(e.list, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		check(name)
	}
	// 許可リストが読めない種類のもの（フォルダ）。
	if err := os.Remove(e.list); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.list, 0o700); err != nil {
		t.Fatal(err)
	}
	check("directory")
	if len(e.runner.Specs()) != 0 {
		t.Fatal("a command was started")
	}
}

func TestCommandExecAllowlistIsRereadAndSettingChoosesTheFile(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(map[string]any{"name": "one", "enabled": true})
	if _, err := e.run(ctlAlice, "one", nil); err != nil {
		t.Fatal(err)
	}
	e.writeList(map[string]any{"name": "two", "enabled": true})
	if _, err := e.run(ctlAlice, "one", nil); !errors.Is(err, domain.ErrCommandNotFound) {
		t.Fatalf("an edited allowlist is not picked up: %v", err)
	}
	if _, err := e.run(ctlAlice, "two", nil); err != nil {
		t.Fatal(err)
	}
	// 設定で別のファイルを指す。
	other := filepath.Join(e.base, "other", "list.json")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"version": 1, "commands": []any{map[string]any{"name": "three", "executable": e.exe, "enabled": true}}})
	if err := os.WriteFile(other, b, 0o600); err != nil {
		t.Fatal(err)
	}
	e.settings.put(settingCommandAllowlistPath, mustJSON(t, other))
	if _, err := e.run(ctlAlice, "three", nil); err != nil {
		t.Fatalf("the configured allowlist is not used: %v", err)
	}
	// 設定が壊れていれば、既定の場所に切り替えず使えない。
	for name, v := range map[string]string{"number": `5`, "relative": `"commands.json"`, "unc": `"\\\\h\\s\\f.json"`} {
		e.settings.put(settingCommandAllowlistPath, v)
		if _, err := e.run(ctlAlice, "two", nil); err == nil {
			t.Errorf("%s: a malformed allowlist path fell back to the default", name)
		}
	}
}

// リモートが書けるファイル転送の置き場所の中に、許可リスト・実行ファイル・作業フォルダがあれば使えない。
func TestCommandExecRefusesThingsRemoteClientsCanWrite(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	inside := filepath.Join(e.root, "evil.exe")

	// 許可リストのファイルが置き場所の中: put_file で書き換えられる。
	if err := os.MkdirAll(e.root, 0o700); err != nil {
		t.Fatal(err)
	}
	e.list = filepath.Join(e.root, "commands.json")
	e.cx = e.newExec(e.ctlEnv)
	e.writeList(map[string]any{"name": "version", "enabled": true})
	if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
		t.Errorf("allowlist inside the transfer folder: %v", err)
	}
	// 設定で指しても同じ。
	e.list = filepath.Join(e.dataDir, "commands.json")
	e.cx = e.newExec(e.ctlEnv)
	e.settings.put(settingCommandAllowlistPath, mustJSON(t, filepath.Join(e.root, "x.json")))
	if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
		t.Errorf("configured allowlist inside the transfer folder: %v", err)
	}
	e.settings.put(settingCommandAllowlistPath, `""`)

	// 実行ファイル・作業フォルダが置き場所の中。
	e.writeList(
		map[string]any{"name": "exe-inside", "executable": inside, "enabled": true},
		map[string]any{"name": "dir-inside", "working_dir": filepath.Join(e.root, "work"), "enabled": true},
		map[string]any{"name": "fine", "enabled": true},
	)
	for _, name := range []string{"exe-inside", "dir-inside"} {
		if _, err := e.run(ctlAlice, name, nil); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.run(ctlAlice, "fine", nil); err != nil {
		t.Errorf("fine: %v", err)
	}
	// 一覧にも出ない。
	infos, err := e.cx.List(context.Background(), ctlAlice)
	if err != nil || len(infos) != 1 || infos[0].Name != "fine" {
		t.Errorf("List = %+v, %v", infos, err)
	}
	// 置き場所の設定を変えて許可リストを覆っても同じ（置き場所が許可リストを含む）。
	e.settings.put(settingFileTransferRoot, mustJSON(t, e.dataDir))
	if _, err := e.run(ctlAlice, "fine", nil); err == nil {
		t.Error("ran with a transfer folder that contains the allowlist")
	}
}

func TestCommandExecInboxPath(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	if _, err := e.put(ctlAlice, "builds/app.msi", "MSI"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.put(ctlAlice, "builds/app.bat", "BAT"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(ctlAlice, "install", map[string]any{"file": "builds/app.msi"}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.root, "builds", "app.msi")
	if got := e.runner.Specs()[0].Args; strings.Join(got, "|") != "/i|"+want+"|/qn" {
		t.Errorf("argv = %q, want the resolved absolute path %q", got, want)
	}
	e.runner.Reset()

	// 置き場所の外・不正な形・拡張子違い・無いファイル・フォルダは起動しない。
	for name, c := range map[string]struct {
		file string
		code domain.Code
	}{
		"traversal":      {"../escape.msi", domain.CodeInvalidArgument},
		"nested":         {"builds/../../escape.msi", domain.CodeInvalidArgument},
		"absolute":       {filepath.Join(e.base, "x.msi"), domain.CodeInvalidArgument},
		"drive":          {`C:\Windows\x.msi`, domain.CodeInvalidArgument},
		"UNC":            {`\\host\share\x.msi`, domain.CodeInvalidArgument},
		"stream":         {"builds/app.msi:evil", domain.CodeInvalidArgument},
		"wrong ext":      {"builds/app.bat", domain.CodeInvalidArgument},
		"missing file":   {"builds/none.msi", domain.CodeFileNotFound},
		"dir with ext":   {"builds", domain.CodeInvalidArgument},
		"empty":          {"", domain.CodeInvalidArgument},
		"dir named .msi": {"d.msi", domain.CodeFileNotFound},
	} {
		if name == "dir named .msi" {
			if err := os.MkdirAll(filepath.Join(e.root, "d.msi"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		_, err := e.run(ctlAlice, "install", map[string]any{"file": c.file})
		// 実体が通常ファイルでないもの（フォルダ）は、無いものと同じく file_not_found でも、形の拒否の invalid_argument でもよい。
		got := domain.CodeOf(err)
		if got != c.code && (c.code != domain.CodeFileNotFound || got != domain.CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want %s", name, err, c.code)
		}
	}
	// 置き場所の中のリンクで外へ出す: 拒否される。
	outside := filepath.Join(e.base, "outside.msi")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.root, "link.msi")); err == nil {
		if _, err := e.run(ctlAlice, "install", map[string]any{"file": "link.msi"}); err == nil {
			t.Error("a symbolic link out of the transfer folder was run")
		}
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("a command was started for a rejected path: %+v", e.runner.Specs())
	}
}

func TestCommandExecAudit(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	e.runner.SetResult(platform.CommandResult{ExitCode: 2, Stdout: []byte("TOP-SECRET-STDOUT"), Stderr: []byte("TOP-SECRET-STDERR"), StderrTruncated: true, TimedOut: false})
	long := strings.Repeat("a", 500)
	if _, err := e.run(ctlAlice, "kill-pid", map[string]any{"pid": 42, "name": "svc"}); err != nil {
		t.Fatal(err)
	}
	en := e.lastToolEntry("run_command")
	d := e.detail(en)
	args, _ := d["args"].(map[string]any)
	if en.Event != "tool.run_command" || en.Kind != "tool" || en.Result != auditOK || en.ClientID != ctlAlice.ClientID || en.LeaseID == "" || en.DurationMS == nil {
		t.Errorf("entry = %+v", en)
	}
	if d["name"] != "kill-pid" || args["pid"] != float64(42) || args["name"] != "svc" || d["exit_code"] != float64(2) || d["timed_out"] != false ||
		d["stdout_bytes"] != float64(len("TOP-SECRET-STDOUT")) || d["stderr_bytes"] != float64(len("TOP-SECRET-STDERR")) || d["stdout_truncated"] != false || d["stderr_truncated"] != true {
		t.Errorf("detail = %s", en.DetailJSON)
	}
	// 検査で拒否された呼び出しも、長さを切った引数の値つきで残る。出力の中身はどの行にも入らない。
	if _, err := e.run(ctlAlice, "kill-pid", map[string]any{"pid": 1, "name": long, "junk": []any{1}, "obj": map[string]any{"a": 1}}); err == nil {
		t.Fatal("accepted")
	}
	en = e.lastToolEntry("run_command")
	d = e.detail(en)
	args, _ = d["args"].(map[string]any)
	if got, _ := args["name"].(string); len(got) != 128 || en.Result != auditDenied {
		t.Errorf("rejected entry: name len = %d, result = %s, detail = %s", len(got), en.Result, en.DetailJSON)
	}
	if args["junk"] != "<[]interface {}>" || args["obj"] != "<map[string]interface {}>" {
		t.Errorf("non-scalar values are not summarized by type: %v", args)
	}
	for _, en := range e.audit.All() {
		if strings.Contains(string(en.DetailJSON), "TOP-SECRET") {
			t.Errorf("command output leaked into the audit row: %s", en.DetailJSON)
		}
	}
	// 実行ファイルのパスも監査に入らない。
	for _, en := range e.audit.All() {
		if strings.Contains(string(en.DetailJSON), "tool.exe") {
			t.Errorf("the executable path leaked into the audit row: %s", en.DetailJSON)
		}
	}
}

func TestCommandExecList(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	e.writeList(e.sampleEntries()...)
	infos, err := e.cx.List(context.Background(), ctlAlice)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range infos {
		names = append(names, i.Name)
	}
	if strings.Join(names, ",") != "install,kill-pid,version" {
		t.Fatalf("names = %v (disabled entries must not be listed)", names)
	}
	for _, i := range infos {
		if i.Name == "version" && (i.Timeout != 7*time.Second || i.MaxOutputBytes != 4096 || i.Description != "app version") {
			t.Errorf("version info = %+v", i)
		}
		if i.Name == "kill-pid" && (len(i.Params) != 2 || i.Params[0].Name != "name" || i.Params[1].Max != 4294967295) {
			t.Errorf("kill-pid params = %+v", i.Params)
		}
	}
	// 一覧にはリースが要らない。
	if _, held := e.leases.Holder(); held {
		t.Error("List acquired the lease")
	}
	if en := e.lastToolEntry("list_commands"); en.Result != auditOK || e.detail(en)["count"] != float64(3) {
		t.Errorf("audit = %+v %s", en, en.DetailJSON)
	}
}

func TestNewCommandExecValidatesConfig(t *testing.T) {
	e := newCmdEnv(t)
	base := CommandExecConfig{Runner: e.runner, Files: e.ft, Leases: e.leases, Kill: e.kill, DefaultAllowlistPath: e.list, DefaultInboxRoot: e.root, Clock: e.clock}
	if _, err := NewCommandExec(base); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CommandExecConfig){
		"no runner":          func(c *CommandExecConfig) { c.Runner = nil },
		"no files":           func(c *CommandExecConfig) { c.Files = nil },
		"no leases":          func(c *CommandExecConfig) { c.Leases = nil },
		"no kill":            func(c *CommandExecConfig) { c.Kill = nil },
		"no clock":           func(c *CommandExecConfig) { c.Clock = nil },
		"relative allowlist": func(c *CommandExecConfig) { c.DefaultAllowlistPath = "commands.json" },
		"relative inbox":     func(c *CommandExecConfig) { c.DefaultInboxRoot = "inbox" },
	} {
		c := base
		mutate(&c)
		if _, err := NewCommandExec(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCommandSettingsLoad(t *testing.T) {
	ctx := context.Background()
	s := newCtlSettings()
	if got, err := loadCommandSettings(ctx, s); err != nil || got.Enabled || got.AllowlistPath != "" {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	s.put(settingCommandEnabled, "true")
	s.put(settingCommandAllowlistPath, mustJSON(t, filepath.Join(t.TempDir(), "c.json")))
	if got, err := loadCommandSettings(ctx, s); err != nil || !got.Enabled || got.AllowlistPath == "" {
		t.Fatalf("loaded = %+v, %v", got, err)
	}
	for _, v := range []string{`1`, `"x/y"`, `{}`} {
		s.put(settingCommandAllowlistPath, v)
		if _, err := loadCommandSettings(ctx, s); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("allowlist path %s: err = %v", v, err)
		}
	}
	s.put(settingCommandAllowlistPath, `""`)
	s.put(settingCommandEnabled, `"on"`)
	if _, err := loadCommandSettings(ctx, s); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("enabled \"on\": err = %v", err)
	}
	if got, err := loadCommandSettings(ctx, nil); err != nil || got.Enabled {
		t.Errorf("nil settings = %+v, %v", got, err)
	}
	var _ repository.Settings = s
}

// symlinkTo は dir の下に target を指すシンボリックリンク name を作り、そのパスを返す（作れない環境では飛ばす）。
func symlinkTo(t *testing.T, target, name string) string {
	t.Helper()
	link := filepath.Join(filepath.Dir(target), name)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	return link
}

// 許可リストが置き場所の中にあるとき、置き場所へのリンクを経由して指しても拒否し、コマンドは起動しない。
func TestCommandExecAllowlistInsideInboxThroughLinkIsRefused(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	if err := os.MkdirAll(e.root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := symlinkTo(t, e.root, "inbox-link")

	// 許可リストの実体は置き場所の中。設定にはリンクを経由したパスで書く。
	e.list = filepath.Join(e.root, "commands.json")
	e.writeList(map[string]any{"name": "version", "enabled": true})
	e.settings.put(settingCommandAllowlistPath, mustJSON(t, filepath.Join(link, "commands.json")))
	if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
		t.Errorf("allowlist reached through a link into the transfer folder: %v", err)
	}
	if _, err := e.cx.List(context.Background(), ctlAlice); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
		t.Errorf("List through a link: %v", err)
	}

	// 置き場所の設定がリンク、許可リストは実体のパス。
	e.settings.put(settingCommandAllowlistPath, mustJSON(t, e.list))
	e.settings.put(settingFileTransferRoot, mustJSON(t, link))
	if _, err := e.run(ctlAlice, "version", nil); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
		t.Errorf("transfer folder configured as a link: %v", err)
	}
	if n := len(e.runner.Specs()); n != 0 {
		t.Errorf("%d commands were started", n)
	}
}

// 実行ファイル・作業フォルダが置き場所へのリンクを経由して置き場所の中にあっても、使えず、起動しない。
func TestCommandExecExecutableAndWorkingDirInsideInboxThroughLinkAreRefused(t *testing.T) {
	e := newCmdEnv(t)
	e.enable()
	if err := os.MkdirAll(filepath.Join(e.root, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := symlinkTo(t, e.root, "inbox-link")
	e.writeList(
		map[string]any{"name": "exe-inside", "executable": filepath.Join(link, "evil.exe"), "enabled": true},
		map[string]any{"name": "dir-inside", "working_dir": filepath.Join(link, "work"), "enabled": true},
		map[string]any{"name": "fine", "enabled": true},
	)
	for _, name := range []string{"exe-inside", "dir-inside"} {
		if _, err := e.run(ctlAlice, name, nil); !errors.Is(err, domain.ErrCommandAllowlistInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(e.runner.Specs()); n != 0 {
		t.Errorf("%d commands were started", n)
	}
	infos, err := e.cx.List(context.Background(), ctlAlice)
	if err != nil || len(infos) != 1 || infos[0].Name != "fine" {
		t.Errorf("List = %+v, %v", infos, err)
	}
	if _, err := e.run(ctlAlice, "fine", nil); err != nil {
		t.Errorf("fine: %v", err)
	}
}
