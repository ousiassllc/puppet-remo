package appserver_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
)

func TestArgsは資格情報の保管場所と道具の無効化とstdioを指定する(t *testing.T) {
	args := appserver.Launch{Exe: "codex-app-server-x.exe"}.Args()
	var cs []string // -c の値
	for i, a := range args {
		if a == "-c" {
			cs = append(cs, args[i+1])
		}
	}
	if !slices.Contains(cs, `cli_auth_credentials_store="keyring"`) {
		t.Errorf("credentials store is not keyring: %v", cs)
	}
	for _, s := range protocol.ToolSettings {
		if !slices.Contains(cs, s) {
			t.Errorf("missing -c %s", s)
		}
	}
	if n := len(args); n < 2 || args[n-2] != "--listen" || args[n-1] != "stdio://" {
		t.Errorf("the transport is not the last arguments: %v", args[max(0, len(args)-2):])
	}
	if slices.Contains(args, "app-server") {
		t.Error("the dedicated binary takes no subcommand")
	}
}

func TestEnvironはトークンとCODEX_HOMEを外してCODEX_HOMEだけを上書きする(t *testing.T) {
	base := []string{
		"PATH=/usr/bin", "HTTPS_PROXY=http://proxy.invalid", "OPENAI_BASE_URL=http://x.invalid",
		"CODEX_HOME=/home/user/.codex", "CODEX_SQLITE_HOME=/x", "CODEX_ACCESS_TOKEN=fake-access", "CODEX_API_KEY=fake-key",
		"OPENAI_API_KEY=fake-openai", "codex_home=/lower", "Openai_Api_Key=fake-mixed",
	}
	got := appserver.Environ(base, "/dedicated/home")
	want := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://proxy.invalid", "OPENAI_BASE_URL=http://x.invalid", "CODEX_HOME=/dedicated/home"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %v", got)
	}
}

// 本物の子プロセス（テストバイナリ自身を app-server の代わりに動かす）での起動・環境・終了の確認。
func TestLaunchは環境から秘密を外して起動しStopで終わる(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	work := filepath.Join(root, "codex-work")
	for _, d := range []string{home, work} {
		mkdir(t, d)
	}
	report := filepath.Join(root, "report.json")
	t.Setenv("CODEXTEST_HELPER", "1")
	t.Setenv("CODEXTEST_REPORT", report)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("CODEX_API_KEY", "fake-codex-key")
	t.Setenv("CODEX_ACCESS_TOKEN", "fake-access-token")
	t.Setenv("CODEX_SQLITE_HOME", filepath.Join(root, "sqlite"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "user-codex"))
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid")

	l := appserver.Launch{Exe: testExe(t), CodexHome: home, WorkDir: work}
	var proc appserver.Process
	srv := appserver.New(appserver.Options{CodexHome: home, Start: func() (appserver.Process, error) {
		p, err := l.Start()
		proc = p
		return p, err
	}})
	t.Cleanup(srv.Close)
	if _, err := srv.Ensure(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	rep, err := readReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_SQLITE_HOME"} {
		if slices.Contains(rep.EnvNames, name) {
			t.Errorf("%s reached the child", name)
		}
	}
	if !slices.Contains(rep.EnvNames, "HTTPS_PROXY") || !slices.Contains(rep.EnvNames, "PATH") {
		t.Error("the other variables must be passed on")
	}
	if rep.CodexDir != home {
		t.Errorf("CODEX_HOME = %q, want %q", rep.CodexDir, home)
	}
	if !slices.Contains(rep.Args, "--listen") || !slices.Contains(rep.Args, "stdio://") {
		t.Errorf("args = %v", rep.Args)
	}
	if !strings.HasSuffix(filepath.Clean(rep.Cwd), "codex-work") {
		t.Errorf("cwd = %q", rep.Cwd)
	}
	srv.Stop()
	select {
	case <-proc.Done():
	default:
		t.Fatal("Stop did not end the child process")
	}
	if info := proc.ExitInfo(); info == "" {
		t.Error("ExitInfo is empty after the exit")
	}
}
