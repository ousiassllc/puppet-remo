package windows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

func TestCheckInstallerPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "installer.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "installer.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		path string
		want error // nil なら成功
	}{
		{"existing exe", ctx, exe, nil},
		{"empty", ctx, "", domain.ErrInvalidArgument},
		{"relative", ctx, "installer.exe", domain.ErrInvalidArgument},
		{"not exe", ctx, txt, domain.ErrInvalidArgument},
		{"NUL", ctx, exe + "\x00.exe", domain.ErrInvalidArgument},
		{"missing", ctx, filepath.Join(dir, "gone.exe"), platform.ErrNotFound},
		{"directory", ctx, dir + ".exe", platform.ErrNotFound},
		{"canceled", canceled, exe, context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkInstallerPath(tc.ctx, tc.path)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("checkInstallerPath = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("checkInstallerPath = %v, want %v", err, tc.want)
			}
			if tc.path != "" && err != nil && strings.Contains(err.Error(), tc.path) {
				t.Errorf("error leaks the path: %v", err)
			}
		})
	}
}
