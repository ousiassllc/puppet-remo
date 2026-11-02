package platformtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// FolderOpenerFixture は RunFolderOpenerContract が使う、環境（偽の実装なら AddFolder で用意したもの、実機なら実在のフォルダー）の記述。
type FolderOpenerFixture struct {
	// Dir は開ける実在のディレクトリ（絶対パス）。
	Dir string
	// MissingDir は存在しないディレクトリのパス（絶対パス）。
	MissingDir string
}

// RunFolderOpenerContract は platform.FolderOpener の契約を検査する。
func RunFolderOpenerContract(t *testing.T, newOpener func(t *testing.T) platform.FolderOpener, fx FolderOpenerFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("opens an existing directory", func(t *testing.T) {
		if err := newOpener(t).OpenFolder(ctx, fx.Dir); err != nil {
			t.Errorf("OpenFolder(existing): %v", err)
		}
	})
	t.Run("rejects an empty path", func(t *testing.T) {
		requireCode(t, newOpener(t).OpenFolder(ctx, ""), domain.ErrInvalidArgument, "OpenFolder(empty)")
	})
	t.Run("rejects a relative path", func(t *testing.T) {
		requireCode(t, newOpener(t).OpenFolder(ctx, "logs"), domain.ErrInvalidArgument, "OpenFolder(relative)")
	})
	t.Run("rejects a path with a NUL character", func(t *testing.T) {
		requireCode(t, newOpener(t).OpenFolder(ctx, fx.Dir+"\x00"), domain.ErrInvalidArgument, "OpenFolder(NUL)")
	})
	t.Run("reports a missing directory as ErrNotFound without the path", func(t *testing.T) {
		err := newOpener(t).OpenFolder(ctx, fx.MissingDir)
		if !errors.Is(err, platform.ErrNotFound) {
			t.Fatalf("OpenFolder(missing) error = %v, want platform.ErrNotFound", err)
		}
		if msg := err.Error(); fx.MissingDir != "" && strings.Contains(msg, fx.MissingDir) {
			t.Errorf("error message leaks the path: %q", msg)
		}
	})
	t.Run("does nothing with a canceled context", func(t *testing.T) {
		requireCanceled(t, newOpener(t).OpenFolder(canceledCtx(), fx.Dir), "OpenFolder")
	})
}
