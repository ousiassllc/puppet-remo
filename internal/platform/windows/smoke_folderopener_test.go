//go:build windows && smoke

package windows_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// TestSmokeFolderOpener は FolderOpener の契約を実機で確かめる（docs/operations/smoke-windows.md §4.14 F1）。
// 一時ディレクトリをエクスプローラーで開くため、PUPPET_REMO_SMOKE_FOLDER=1 のときだけ走る（ウィンドウが 1 つ開く。確認したら手で閉じる）。
func TestSmokeFolderOpener(t *testing.T) {
	if os.Getenv("PUPPET_REMO_SMOKE_FOLDER") != "1" {
		t.Skip("set PUPPET_REMO_SMOKE_FOLDER=1 to run (opens an Explorer window on a temporary directory)")
	}
	dir := t.TempDir()
	t.Log("an Explorer window opens on the temporary directory: close it by hand")
	platformtest.RunFolderOpenerContract(t, func(*testing.T) platform.FolderOpener { return pwin.NewFolderOpener() },
		platformtest.FolderOpenerFixture{Dir: dir, MissingDir: filepath.Join(dir, "missing")})
}
