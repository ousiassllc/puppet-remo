//go:build windows && smoke

package windows_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// AppLauncher の起動場所の検査（NFR-S20 (3)。launcher_windows.go の verifyExecutableLocation）の実機のスモーク
// （docs/operations/smoke-windows.md §3.8 L10〜L19）。Linux で走る純粋関数のテスト（launchlocation_test.go）が見られない、
// GetDriveType・CreateFile・GetFinalPathNameByHandle の実際の結果を確かめる。
//
// 各ケースは前提（リンクの作成権限・subst・ネットワーク共有）を満たせなければ Skip し、**テスト自身が作ったものだけ**
// （一時ディレクトリ・リンク・subst のドライブ）を後始末する。起動したプロセスは Launch が返した pid だけを終了する。
// 起動する実行ファイルは、このテストのバイナリのコピー（-test.run=^$ で何もせず終わる）か、System32 の notepad.exe（L10 のみ）。
//   - PUPPET_REMO_SMOKE_LAUNCH_MAPPED: ネットワークのマップドドライブ上の実行ファイルのパス（例: Z:\tools\app.exe。L18。
//     GetDriveType が DRIVE_REMOTE でなければ飛ばす。未設定なら飛ばす）
//   - PUPPET_REMO_SMOKE_LAUNCH_UNC: UNC パスの実行ファイル（例: \\nas\share\app.exe。L19。未設定なら飛ばす。
//     管理者共有 \\localhost\C$ は自動では使わない）
//
// 期待値は現行の設計（finalPathMatches）に合わせる: 大文字小文字・"/" 区切りは許可、8.3 短縮名・リンク・subst・
// ネットワークは access_denied。結果が違う場合（許可のはずが拒否される、拒否のはずが通る）は、finalPathMatches を見直す。

const (
	smokeLaunchMappedEnv = "PUPPET_REMO_SMOKE_LAUNCH_MAPPED"
	smokeLaunchUNCEnv    = "PUPPET_REMO_SMOKE_LAUNCH_UNC"
)

// harmlessArgs は、テストのバイナリのコピーを何もせず終わらせる引数。
var harmlessArgs = []string{"-test.run=^$"}

// launchLocationScratch は一時ディレクトリの中の、テストのバイナリのコピー。
// パスは 8.3 短縮名を含まない形（GetLongPathName）で持つ（%TEMP% が短縮名のことがあるため）。
type launchLocationScratch struct {
	dir string // 実体のあるディレクトリ（名前が 8.3 に収まらない長さ）
	exe string // dir にある実行ファイル（名前が 8.3 に収まらない長さ・空白を含む）
}

func newLaunchLocationScratch(t *testing.T) launchLocationScratch {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(winLongPath(t, t.TempDir()), "long directory name")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "long application name.exe")
	copyFile(t, self, exe)
	return launchLocationScratch{dir: dir, exe: exe}
}

// winLongPath は p の 8.3 短縮名を長い名前へ直す（存在するパスだけ）。
func winLongPath(t *testing.T, p string) string {
	t.Helper()
	return convertWinPath(t, p, windows.GetLongPathName)
}

// winShortPath は p を 8.3 短縮名にする（存在するパスだけ。ボリュームで 8.3 が無効なら p のまま返る）。
func winShortPath(t *testing.T, p string) string {
	t.Helper()
	return convertWinPath(t, p, windows.GetShortPathName)
}

func convertWinPath(t *testing.T, p string, f func(in, out *uint16, size uint32) (uint32, error)) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := f(in, &buf[0], uint32(len(buf)))
		if err != nil {
			t.Fatalf("convert %q: %v", p, err)
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n+1)
	}
}

// requireLaunchAllowed は exe が Launch で起動できる（場所の検査を通る）ことを確かめ、起動したプロセスを終了する。
func requireLaunchAllowed(t *testing.T, exe string, args []string) {
	t.Helper()
	pid, err := pwin.NewLauncher().Launch(context.Background(), platform.LaunchSpec{ExePath: exe, Args: args})
	if err != nil {
		t.Fatalf("Launch(%q): %v, want success", exe, err)
	}
	t.Cleanup(func() { terminateOwnProcess(t, pid) })
}

// requireLaunchDenied は exe の Launch が access_denied（場所の検査での拒否）になり、プロセスが起動しないことを確かめる。
// エラーのメッセージにパスが入らないことも確かめる。
func requireLaunchDenied(t *testing.T, exe string) {
	t.Helper()
	pid, err := pwin.NewLauncher().Launch(context.Background(), platform.LaunchSpec{ExePath: exe, Args: harmlessArgs})
	if err == nil {
		t.Cleanup(func() { terminateOwnProcess(t, pid) })
		t.Fatalf("Launch(%q) succeeded (pid %d), want access_denied", exe, pid)
	}
	var le *platform.LaunchError
	if !errors.As(err, &le) {
		t.Fatalf("Launch(%q): error = %v, want *platform.LaunchError", exe, err)
	}
	if le.Reason != domain.LaunchAccessDenied {
		t.Errorf("Launch(%q): Reason = %q (%v), want %q", exe, le.Reason, le.Err, domain.LaunchAccessDenied)
	}
	if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(exe)) {
		t.Errorf("error message contains the path: %v", err)
	}
}

// requireResolvesTo は Resolve(requested) が want（実体のパス。大文字小文字は区別しない）を返すことを確かめる。
func requireResolvesTo(t *testing.T, requested, want string) {
	t.Helper()
	got, err := pwin.NewLauncher().Resolve(context.Background(), requested)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", requested, err)
	}
	if !strings.EqualFold(got, want) {
		t.Errorf("Resolve(%q) = %q, want %q", requested, got, want)
	}
}

// createSymlink は os.Symlink でシンボリックリンクを作る（ディレクトリ・ファイル共通）。作成の特権が無ければ（開発者モードでも管理者でもない）飛ばす。
func createSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("cannot create symbolic links (enable Developer Mode or run as administrator): %v", err)
		}
		t.Fatalf("Symlink(%q -> %q): %v", link, target, err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Errorf("remove symlink %q: %v", link, err)
		}
	})
}

// createJunction は cmd.exe の mklink /J でジャンクションを作る（権限は要らない）。作れなければ飛ばす。
func createJunction(t *testing.T, target, link string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction (mklink /J): %v: %s", err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() {
		// ジャンクション自体だけを消す（os.Remove は RemoveDirectory で、リンク先の中身には触れない）。
		if err := os.Remove(link); err != nil {
			t.Errorf("remove junction %q: %v", link, err)
		}
	})
}

// substDrive は未使用のドライブ文字（Z から D へ向けて探す）を dir へ subst し、"X:" を返す。
// 後始末では、このテストが割り当てたドライブだけを解除する。subst が使えなければ飛ばす。
func substDrive(t *testing.T, dir string) string {
	t.Helper()
	used, err := windows.GetLogicalDrives()
	if err != nil {
		t.Skipf("GetLogicalDrives: %v", err)
	}
	subst := filepath.Join(os.Getenv("SystemRoot"), "System32", "subst.exe")
	for letter := 'Z'; letter >= 'D'; letter-- {
		if used&(1<<(letter-'A')) != 0 {
			continue
		}
		drive := fmt.Sprintf("%c:", letter)
		if out, err := exec.Command(subst, drive, dir).CombinedOutput(); err != nil {
			t.Skipf("cannot subst %s: %v: %s", drive, err, strings.TrimSpace(string(out)))
		}
		t.Cleanup(func() {
			if out, err := exec.Command(subst, drive, "/d").CombinedOutput(); err != nil {
				t.Errorf("subst %s /d: %v: %s (remove the drive by hand)", drive, err, strings.TrimSpace(string(out)))
			}
		})
		return drive
	}
	t.Skip("no unused drive letter for subst")
	return ""
}

func TestSmokeAppLauncherLocation(t *testing.T) {
	system32 := filepath.Join(os.Getenv("SystemRoot"), "System32")
	scratch := newLaunchLocationScratch(t)

	t.Run("L10 System32 notepad is allowed", func(t *testing.T) {
		requireLaunchAllowed(t, filepath.Join(system32, "notepad.exe"), nil)
	})

	t.Run("L11 plain path in a scratch directory is allowed (control)", func(t *testing.T) {
		requireLaunchAllowed(t, scratch.exe, harmlessArgs)
		requireResolvesTo(t, scratch.exe, scratch.exe)
	})

	t.Run("L12 letter case differences are allowed", func(t *testing.T) {
		requireLaunchAllowed(t, strings.ToUpper(scratch.exe), harmlessArgs)
		requireLaunchAllowed(t, strings.ToLower(scratch.exe), harmlessArgs)
	})

	t.Run("L13 forward slashes are allowed", func(t *testing.T) {
		requireLaunchAllowed(t, filepath.ToSlash(scratch.exe), harmlessArgs)
		requireLaunchAllowed(t, strings.ToLower(filepath.ToSlash(scratch.exe)), harmlessArgs)
	})

	t.Run("L14 8.3 short name is denied (fail closed)", func(t *testing.T) {
		short := winShortPath(t, scratch.exe)
		if strings.EqualFold(short, scratch.exe) {
			t.Skip("8.3 short names are not available on this volume")
		}
		t.Logf("short name: %s", short)
		requireLaunchDenied(t, short)
		requireResolvesTo(t, short, scratch.exe) // 実体のパスへは解決できる（拒否リストの照合用）
	})

	t.Run("L15 junction is denied", func(t *testing.T) {
		link := filepath.Join(winLongPath(t, t.TempDir()), "junction")
		createJunction(t, scratch.dir, link)
		viaLink := filepath.Join(link, filepath.Base(scratch.exe))
		requireLaunchDenied(t, viaLink)
		requireResolvesTo(t, viaLink, scratch.exe)
	})

	t.Run("L16 symbolic link is denied", func(t *testing.T) {
		base := winLongPath(t, t.TempDir())
		t.Run("directory", func(t *testing.T) {
			link := filepath.Join(base, "dirlink")
			createSymlink(t, scratch.dir, link)
			requireLaunchDenied(t, filepath.Join(link, filepath.Base(scratch.exe)))
		})
		t.Run("file", func(t *testing.T) {
			link := filepath.Join(base, "filelink.exe")
			createSymlink(t, scratch.exe, link)
			requireLaunchDenied(t, link)
		})
	})

	t.Run("L17 subst drive is denied", func(t *testing.T) {
		drive := substDrive(t, scratch.dir)
		viaSubst := drive + `\` + filepath.Base(scratch.exe)
		if _, err := os.Stat(viaSubst); err != nil {
			t.Skipf("the subst drive is not visible to this process: %v", err)
		}
		root, _ := windows.UTF16PtrFromString(drive + `\`)
		t.Logf("GetDriveType(%s\\) = %d (DRIVE_FIXED = %d)", drive, windows.GetDriveType(root), windows.DRIVE_FIXED)
		requireLaunchDenied(t, viaSubst)
		requireResolvesTo(t, viaSubst, scratch.exe)
	})

	t.Run("L18 mapped network drive is denied", func(t *testing.T) {
		exe := os.Getenv(smokeLaunchMappedEnv)
		if exe == "" {
			t.Skipf("set %s to an executable on a mapped network drive (e.g. Z:\\tools\\app.exe)", smokeLaunchMappedEnv)
		}
		root, err := windows.UTF16PtrFromString(filepath.VolumeName(exe) + `\`)
		if err != nil {
			t.Fatal(err)
		}
		if dt := windows.GetDriveType(root); dt != windows.DRIVE_REMOTE {
			t.Skipf("%s is not a network drive (GetDriveType = %d)", filepath.VolumeName(exe), dt)
		}
		requireLaunchDenied(t, exe)
	})

	t.Run("L19 UNC path is denied", func(t *testing.T) {
		exe := os.Getenv(smokeLaunchUNCEnv)
		if exe == "" {
			t.Skipf("set %s to an executable on a network share (e.g. \\\\nas\\share\\app.exe)", smokeLaunchUNCEnv)
		}
		if !strings.HasPrefix(exe, `\\`) || strings.HasPrefix(exe, `\\?\`) || strings.HasPrefix(exe, `\\.\`) {
			t.Skipf("%s must be a UNC path (\\\\server\\share\\...): %q", smokeLaunchUNCEnv, exe)
		}
		requireLaunchDenied(t, exe)
	})
}
