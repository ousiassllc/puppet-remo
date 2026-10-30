//go:build windows && smoke

package windows_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// AppLauncher・FilePicker の実機のスモーク（docs/operations/smoke-windows.md §3.8）。
//
// TestSmokeAppLauncher は %SystemRoot%\System32\notepad.exe を起動し、終了時に**テスト自身が起動したプロセスだけ**を
// （Launch が返した pid で）終了する。ほかのメモ帳には触れない。
//   - PUPPET_REMO_SMOKE_LAUNCH_ELEVATED: requireAdministrator のマニフェストを持つ実行ファイルのパス（L4。未設定なら飛ばす）
//   - PUPPET_REMO_SMOKE_LAUNCH_DENIED: 起動を拒否される実行ファイルのパス（L5。未設定なら飛ばす）
//
// TestSmokeFilePicker は PUPPET_REMO_SMOKE_PICKER=1 のときだけ走り、ダイアログを手で操作する（L9）。

func TestSmokeAppLauncher(t *testing.T) {
	system32 := filepath.Join(os.Getenv("SystemRoot"), "System32")
	scratch := t.TempDir()
	platformtest.RunAppLauncherContract(t, func(*testing.T) platform.AppLauncher { return pwin.NewLauncher() },
		platformtest.AppLauncherFixture{
			ExePath:      filepath.Join(system32, "notepad.exe"),
			Dir:          system32,
			MissingPath:  filepath.Join(scratch, "missing", "app.exe"),
			MissingDir:   filepath.Join(scratch, "missing"),
			DeniedPath:   os.Getenv("PUPPET_REMO_SMOKE_LAUNCH_DENIED"),
			ElevatedPath: os.Getenv("PUPPET_REMO_SMOKE_LAUNCH_ELEVATED"),
			Terminate:    terminateOwnProcess,
		})
}

// TestSmokeAppLauncherPid は、Launch が返した pid が実在するプロセスを指すことを確かめる（L7）。
func TestSmokeAppLauncherPid(t *testing.T) {
	notepad := filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	pid, err := pwin.NewLauncher().Launch(context.Background(), platform.LaunchSpec{ExePath: notepad})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { terminateOwnProcess(t, pid) })

	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatalf("OpenProcess(pid %d): %v", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	got, err := windows.GetProcessId(h)
	if err != nil || got != pid {
		t.Errorf("GetProcessId = (%d, %v), want %d", got, err, pid)
	}
}

// smokeArgsOutEnv が設定されているとき、TestSmokeArgsHelper は受け取った引数をそのファイルへ JSON で書く。
const smokeArgsOutEnv = "PUPPET_REMO_SMOKE_ARGS_OUT"

// TestSmokeArgsHelper は TestSmokeAppLauncherArgs が Launch で起動する子プロセスとして動く（通常の実行では何もしない）。
func TestSmokeArgsHelper(t *testing.T) {
	out := os.Getenv(smokeArgsOutEnv)
	if out == "" {
		t.Skip("helper for TestSmokeAppLauncherArgs")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out+".tmp", b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(out+".tmp", out); err != nil {
		t.Fatal(err)
	}
}

// TestSmokeAppLauncherArgs は、空白・日本語を含むパスの実行ファイル（このテストのバイナリのコピー）へ、空白・引用符・
// バックスラッシュ・日本語・シェルの特殊文字を含む引数を Launch し、子プロセスが同じ引数を受け取ることを確かめる（L2）。
func TestSmokeAppLauncherArgs(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "空白 あり dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "引数 テスト.exe")
	copyFile(t, self, exe)

	want := []string{
		"plain", "", "with space", `say "hi"`, `back\slash`, `trail \`, `quote"inside`, "日本語 の 引数",
		"a&b", "%PATH%", "a | b", "100% > 5", "^x", "$(id)",
	}
	out := filepath.Join(t.TempDir(), "args.json")
	t.Setenv(smokeArgsOutEnv, out) // 環境変数は子プロセスへ引き継がれる
	spec := platform.LaunchSpec{ExePath: exe, Args: append([]string{"-test.run=^TestSmokeArgsHelper$", "--"}, want...)}
	pid, err := pwin.NewLauncher().Launch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { terminateOwnProcess(t, pid) })

	deadline := time.Now().Add(30 * time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil {
			var got []string
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("child received %q, want %q", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child did not write its arguments: %v", err)
		}
		time.Sleep(50 * time.Millisecond) // 子プロセスの完了待ち（上限つきのポーリング）
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// terminateOwnProcess は Launch が返した pid のプロセスを終了する。すでに終わっていても失敗にしない
// （Windows 11 のメモ帳は、起動後に別のプロセスへ引き継いで終わることがある。残ったウィンドウは手で閉じる）。
func terminateOwnProcess(t *testing.T, pid uint32) {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		t.Logf("pid %d: already gone (OpenProcess: %v)", pid, err)
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.TerminateProcess(h, 0); err != nil {
		t.Logf("pid %d: TerminateProcess: %v", pid, err)
	}
}

func TestSmokeFilePicker(t *testing.T) {
	if os.Getenv("PUPPET_REMO_SMOKE_PICKER") != "1" {
		t.Skip("set PUPPET_REMO_SMOKE_PICKER=1 to run (opens a dialog that needs manual operation)")
	}
	picker := pwin.NewFilePicker(nil) // 親ウィンドウなし（メインウィンドウとの結線は §4.8 で確かめる）

	t.Run("choose an executable", func(t *testing.T) {
		t.Log("a dialog opens: choose any .exe file (the filter shows only .exe)")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		path, ok, err := picker.PickExecutable(ctx)
		if err != nil || !ok {
			t.Fatalf("PickExecutable = (%q, %v, %v), want a selection", path, ok, err)
		}
		if !strings.EqualFold(filepath.Ext(path), ".exe") {
			t.Errorf("selected %q is not an .exe", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("selected file does not exist: %v", err)
		}
	})

	t.Run("cancel the dialog", func(t *testing.T) {
		t.Log("a dialog opens: press Cancel")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		path, ok, err := picker.PickExecutable(ctx)
		if err != nil || ok || path != "" {
			t.Errorf("PickExecutable = (%q, %v, %v), want (\"\", false, nil)", path, ok, err)
		}
	})

	t.Run("canceling the context closes the dialog", func(t *testing.T) {
		t.Log("a dialog opens: leave it alone; it must close by itself after 3 seconds")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		path, ok, err := picker.PickExecutable(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || ok || path != "" {
			t.Errorf("PickExecutable = (%q, %v, %v), want (\"\", false, DeadlineExceeded)", path, ok, err)
		}
	})
}
