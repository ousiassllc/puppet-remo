//go:build windows && smoke

package windows_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// Installer の実機のスモーク（docs/operations/smoke-windows.md §4.13）。本物のインストーラは動かさない:
// %SystemRoot%\System32\whoami.exe を一時ディレクトリへ複製したものを `/S` つきで起動する（whoami は値のない `/S`（リモートシステムの指定）を
// 構文エラーとして終わるだけで、何も変更しない。昇格も要らない）。本物の NSIS での確認は §4.13 の手動手順。
func TestSmokeInstaller(t *testing.T) {
	src := filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe")
	dir := t.TempDir()
	dst := filepath.Join(dir, "installer.exe")
	copyInstallerFixture(t, src, dst)
	platformtest.RunInstallerContract(t, func(*testing.T) platform.Installer { return pwin.NewInstaller() },
		platformtest.InstallerFixture{Path: dst, MissingPath: filepath.Join(dir, "missing", "gone.exe")})
}

func copyInstallerFixture(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Skipf("cannot open %s: %v", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
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
