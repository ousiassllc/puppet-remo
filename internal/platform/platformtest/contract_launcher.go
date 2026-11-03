package platformtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// AppLauncherFixture は RunAppLauncherContract が使う、環境（偽の実装なら AddExecutable などで用意したもの、
// 実機なら実在のファイル）の記述。
type AppLauncherFixture struct {
	// ExePath は起動できる実在の実行ファイル（絶対パス）。
	ExePath string
	// Dir は実在のディレクトリ。
	Dir string
	// MissingPath は存在しない実行ファイルのパス。
	MissingPath string
	// MissingDir は存在しないディレクトリ。
	MissingDir string
	// DeniedPath は OS に起動を拒否される実行ファイル。空ならその項目を飛ばす。
	DeniedPath string
	// ElevatedPath は昇格が必要な実行ファイル（requireAdministrator）。空ならその項目を飛ばす。
	ElevatedPath string
	// Terminate は Launch が返した pid のプロセスを終了する（実機のスモークで、テスト自身が起動したものだけを終える）。
	// 偽の実装では nil。
	Terminate func(t *testing.T, pid uint32)
}

// 起動の失敗にパス・引数が出ないことを調べるための、引数の目印。
const launchArgMarker = "contract-arg-7f3a91"

// RunAppLauncherContract は platform.AppLauncher の契約を検査する。実機では fx の Terminate で起動したプロセスを終了する。
func RunAppLauncherContract(t *testing.T, newLauncher func(t *testing.T) platform.AppLauncher, fx AppLauncherFixture) {
	t.Helper()
	ctx := context.Background()

	launch := func(t *testing.T, l platform.AppLauncher, spec platform.LaunchSpec) (uint32, error) {
		t.Helper()
		pid, err := l.Launch(ctx, spec)
		if err == nil && fx.Terminate != nil {
			t.Cleanup(func() { fx.Terminate(t, pid) })
		}
		return pid, err
	}

	t.Run("Launch returns a pid", func(t *testing.T) {
		l := newLauncher(t)
		pid, err := launch(t, l, platform.LaunchSpec{ExePath: fx.ExePath, Args: []string{launchArgMarker}})
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		if pid == 0 {
			t.Error("Launch returned pid 0")
		}
	})

	t.Run("Launch with a working directory", func(t *testing.T) {
		l := newLauncher(t)
		if _, err := launch(t, l, platform.LaunchSpec{ExePath: fx.ExePath, WorkingDir: fx.Dir}); err != nil {
			t.Fatalf("Launch: %v", err)
		}
	})

	t.Run("every Launch starts a new process", func(t *testing.T) {
		l := newLauncher(t)
		a, err := launch(t, l, platform.LaunchSpec{ExePath: fx.ExePath})
		if err != nil {
			t.Fatalf("first Launch: %v", err)
		}
		b, err := launch(t, l, platform.LaunchSpec{ExePath: fx.ExePath})
		if err != nil {
			t.Fatalf("second Launch: %v", err)
		}
		if a == b {
			t.Errorf("both Launch calls returned pid %d, want distinct processes", a)
		}
	})

	failures := []struct {
		name   string
		path   string
		reason domain.LaunchFailure
	}{
		{"missing executable is file_missing", fx.MissingPath, domain.LaunchFileMissing},
		{"denied executable is access_denied", fx.DeniedPath, domain.LaunchAccessDenied},
		{"executable that needs elevation is elevation_required", fx.ElevatedPath, domain.LaunchElevationRequired},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			if tc.path == "" {
				t.Skip("no fixture for this failure")
			}
			l := newLauncher(t)
			spec := platform.LaunchSpec{ExePath: tc.path, Args: []string{launchArgMarker, `with space "quoted"`}, WorkingDir: fx.Dir}
			_, err := launch(t, l, spec)
			var le *platform.LaunchError
			if !errors.As(err, &le) {
				t.Fatalf("Launch: error = %v, want *platform.LaunchError", err)
			}
			if le.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", le.Reason, tc.reason)
			}
			requireNoLaunchSecrets(t, err, spec)
		})
	}

	t.Run("empty executable path is ErrInvalidArgument", func(t *testing.T) {
		l := newLauncher(t)
		_, err := l.Launch(ctx, platform.LaunchSpec{})
		requireCode(t, err, domain.ErrInvalidArgument, "Launch(empty)")
	})

	t.Run("Inspect", func(t *testing.T) {
		l := newLauncher(t)
		if err := l.Inspect(ctx, fx.ExePath, ""); err != nil {
			t.Errorf("Inspect(existing file): %v", err)
		}
		if err := l.Inspect(ctx, fx.ExePath, fx.Dir); err != nil {
			t.Errorf("Inspect(existing file, existing dir): %v", err)
		}
		for _, tc := range []struct{ name, exe, dir string }{
			{"missing file", fx.MissingPath, ""},
			{"directory as the executable", fx.Dir, ""},
			{"missing working directory", fx.ExePath, fx.MissingDir},
			{"file as the working directory", fx.ExePath, fx.ExePath},
		} {
			err := l.Inspect(ctx, tc.exe, tc.dir)
			if !errors.Is(err, platform.ErrNotFound) {
				t.Errorf("Inspect(%s): error = %v, want ErrNotFound", tc.name, err)
				continue
			}
			for _, secret := range []string{tc.exe, tc.dir} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Errorf("Inspect(%s): error %q contains a path", tc.name, err)
				}
			}
		}
	})

	t.Run("Resolve", func(t *testing.T) {
		l := newLauncher(t)
		got, err := l.Resolve(ctx, fx.ExePath)
		if err != nil {
			t.Fatalf("Resolve(existing file): %v", err)
		}
		if domain.NormalizePathForMatch(got) == "" || !strings.EqualFold(domain.NormalizePathForMatch(got), domain.NormalizePathForMatch(fx.ExePath)) {
			t.Errorf("Resolve(existing file) = %q, want the same file as the fixture (case may differ)", got)
		}
		// 解決した実体は、そのまま Launch の ExePath に渡せる形（もう一度解決しても変わらない）。
		again, err := l.Resolve(ctx, got)
		if err != nil || again != got {
			t.Errorf("Resolve(resolved) = %q, %v, want %q", again, err, got)
		}
		_, err = l.Resolve(ctx, fx.MissingPath)
		var le *platform.LaunchError
		if !errors.As(err, &le) || le.Reason != domain.LaunchFileMissing {
			t.Errorf("Resolve(missing): error = %v, want *LaunchError file_missing", err)
		} else if strings.Contains(err.Error(), fx.MissingPath) {
			t.Errorf("Resolve(missing): error %q contains a path", err)
		}
		_, err = l.Resolve(ctx, "")
		requireCode(t, err, domain.ErrInvalidArgument, "Resolve(empty)")
	})

	t.Run("canceled context", func(t *testing.T) {
		l := newLauncher(t)
		pid, err := l.Launch(canceledCtx(), platform.LaunchSpec{ExePath: fx.ExePath})
		requireCanceled(t, err, "Launch")
		if err == nil && fx.Terminate != nil {
			fx.Terminate(t, pid)
		}
		requireCanceled(t, l.Inspect(canceledCtx(), fx.ExePath, ""), "Inspect")
		_, err = l.Resolve(canceledCtx(), fx.ExePath)
		requireCanceled(t, err, "Resolve")
	})
}

// requireNoLaunchSecrets は、起動の失敗 err とその原因のメッセージに、実行ファイルのパス・引数が入っていないことを検査する。
func requireNoLaunchSecrets(t *testing.T, err error, spec platform.LaunchSpec) {
	t.Helper()
	secrets := append([]string{spec.ExePath, spec.WorkingDir}, spec.Args...)
	texts := []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err)}
	if cause := errors.Unwrap(err); cause != nil {
		texts = append(texts, cause.Error())
	}
	for _, text := range texts {
		for _, s := range secrets {
			if s != "" && strings.Contains(text, s) {
				t.Errorf("launch error text %q contains a path or an argument", text)
			}
		}
	}
}
