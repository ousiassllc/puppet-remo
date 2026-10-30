package windows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
