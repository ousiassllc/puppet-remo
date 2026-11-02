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

func TestCheckFolderPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.log")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
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
		{"existing directory", ctx, dir, nil},
		{"empty", ctx, "", domain.ErrInvalidArgument},
		{"relative", ctx, "logs", domain.ErrInvalidArgument},
		{"NUL", ctx, dir + "\x00", domain.ErrInvalidArgument},
		{"missing", ctx, filepath.Join(dir, "gone"), platform.ErrNotFound},
		{"a file is never opened", ctx, file, platform.ErrNotFound},
		{"canceled", canceled, dir, context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkFolderPath(tc.ctx, tc.path)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("checkFolderPath = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("checkFolderPath = %v, want %v", err, tc.want)
			}
			if tc.path != "" && strings.Contains(err.Error(), tc.path) {
				t.Errorf("error leaks the path: %v", err)
			}
		})
	}
}
