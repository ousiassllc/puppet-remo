//go:build windows

package localfs_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// TestJunctionEscapeIsRejected はジャンクション（管理者権限が要らない。Go の Lstat は ModeSymlink を立てない種類のリパースポイント）で
// 置き場所の外に出られないこと（NTFS の実機でだけ確かめられる。docs/operations/smoke-windows.md §4.18）。
func TestJunctionEscapeIsRejected(t *testing.T) {
	f := newFixture(t)
	if _, err := f.put("keep.txt", 0, "k"); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(f.outer, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(f.root, "junction"), f.outer).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v: %s", err, out)
	}
	ctx := context.Background()
	if _, err := f.put("junction/new.txt", 0, "pwned"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Put through a junction: %v", err)
	}
	if _, _, err := f.store.Get(ctx, f.root, "junction/secret.txt", 0, 100); err == nil {
		t.Error("Get through a junction succeeded")
	}
	if _, err := f.store.Delete(ctx, f.root, "junction/secret.txt"); err == nil {
		t.Error("Delete through a junction succeeded")
	}
	if _, err := f.store.Delete(ctx, f.root, "junction"); err == nil {
		t.Error("Delete of the junction itself succeeded")
	}
	if b, _ := os.ReadFile(secret); string(b) != "secret" {
		t.Errorf("the file outside the root was modified: %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.outer, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file was created outside the root: %v", err)
	}
	entries, _, err := f.store.List(ctx, f.root, "", 100)
	if err != nil || len(entries) != 1 || entries[0].Rel != "keep.txt" {
		t.Errorf("List = %+v, %v", entries, err)
	}
	if total, err := f.store.TotalSize(ctx, f.root); err != nil || total != 1 {
		t.Errorf("TotalSize = %d, %v", total, err)
	}
	var _ platform.FileStore = f.store
}
