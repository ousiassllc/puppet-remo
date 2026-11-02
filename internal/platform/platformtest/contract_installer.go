package platformtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// InstallerFixture は RunInstallerContract が使う、環境（偽の実装なら AddInstaller で用意したもの、実機なら実在のファイル）の記述。
type InstallerFixture struct {
	// Path は起動できる実在の .exe（絶対パス）。実機のスモークでは、`/S` を渡されても無害な実行ファイル（本物のインストーラではない）。
	Path string
	// MissingPath は存在しない .exe のパス（絶対パス）。
	MissingPath string
}

// RunInstallerContract は platform.Installer の契約を検査する。
func RunInstallerContract(t *testing.T, newInstaller func(t *testing.T) platform.Installer, fx InstallerFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("runs an existing installer", func(t *testing.T) {
		if err := newInstaller(t).Run(ctx, fx.Path); err != nil {
			t.Errorf("Run(existing): %v", err)
		}
	})
	t.Run("rejects an empty path", func(t *testing.T) {
		requireCode(t, newInstaller(t).Run(ctx, ""), domain.ErrInvalidArgument, "Run(empty)")
	})
	t.Run("rejects a relative path", func(t *testing.T) {
		requireCode(t, newInstaller(t).Run(ctx, "installer.exe"), domain.ErrInvalidArgument, "Run(relative)")
	})
	t.Run("rejects a path that is not an .exe", func(t *testing.T) {
		requireCode(t, newInstaller(t).Run(ctx, fx.Path+".txt"), domain.ErrInvalidArgument, "Run(not .exe)")
	})
	t.Run("reports a missing installer as ErrNotFound without the path", func(t *testing.T) {
		err := newInstaller(t).Run(ctx, fx.MissingPath)
		if !errors.Is(err, platform.ErrNotFound) {
			t.Fatalf("Run(missing) error = %v, want platform.ErrNotFound", err)
		}
		if msg := err.Error(); fx.MissingPath != "" && strings.Contains(msg, fx.MissingPath) {
			t.Errorf("error message leaks the path: %q", msg)
		}
	})
	t.Run("does nothing with a canceled context", func(t *testing.T) {
		requireCanceled(t, newInstaller(t).Run(canceledCtx(), fx.Path), "Run")
	})
}
