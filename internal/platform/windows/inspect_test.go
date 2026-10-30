package windows

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

func TestInspectApp(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "gone")
	ctx := context.Background()

	ok := []struct{ name, exe, dir string }{
		{"file only", exe, ""},
		{"file and working directory", exe, dir},
	}
	for _, tc := range ok {
		if err := inspectApp(ctx, tc.exe, tc.dir); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	notFound := []struct{ name, exe, dir string }{
		{"missing file", missing + ".exe", ""},
		{"empty path", "", ""},
		{"directory as the executable", dir, ""},
		{"missing working directory", exe, missing},
		{"file as the working directory", exe, exe},
		{"path through a file", filepath.Join(exe, "child.exe"), ""},
	}
	for _, tc := range notFound {
		err := inspectApp(ctx, tc.exe, tc.dir)
		if !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("%s: error = %v, want ErrNotFound", tc.name, err)
			continue
		}
		if strings.Contains(err.Error(), dir) {
			t.Errorf("%s: error %q contains a path", tc.name, err)
		}
	}
}

func TestInspectAppCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := inspectApp(ctx, "x", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// os.Stat の失敗の分類。名前・パスの構文が正しくない（ERROR_INVALID_NAME・ERROR_BAD_PATHNAME）は「無い」、
// それ以外（権限など）は OS のエラーコードだけのエラーで、パスを含まない。
func TestMapStatError(t *testing.T) {
	const secretPath = `C:\secret-dir\a:b.exe`
	pathErr := func(errno syscall.Errno) error {
		return &fs.PathError{Op: "CreateFile", Path: secretPath, Err: errno}
	}
	// errno は Win32 の数値（Linux でも同じ値で渡す）。
	for name, errno := range map[string]syscall.Errno{
		"ERROR_INVALID_NAME (123)": errInvalidName,
		"ERROR_BAD_PATHNAME (161)": errBadPathname,
		"ENOTDIR":                  syscall.ENOTDIR,
	} {
		t.Run(name, func(t *testing.T) {
			if err := mapStatError(pathErr(errno)); !errors.Is(err, platform.ErrNotFound) {
				t.Errorf("error = %v, want ErrNotFound", err)
			}
		})
	}

	t.Run("access denied keeps only the code", func(t *testing.T) {
		err := mapStatError(pathErr(errAccessDenied))
		if err == nil || errors.Is(err, platform.ErrNotFound) {
			t.Fatalf("error = %v, want a failure that is not ErrNotFound", err)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), `C:\`) {
			t.Errorf("error %q contains the path", err)
		}
	})
}
