package repository

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestCleanThumbPath(t *testing.T) {
	good := []string{
		"a.jpg", "2026/10/28/01HXABC.jpg", "x/y/z.jpeg", "a b.jpg", "日本語.jpg", "a.b.c", "COM10.jpg", "console.jpg", "con-1.jpg",
	}
	for _, p := range good {
		if got, err := cleanThumbPath(p); err != nil || got != p {
			t.Errorf("cleanThumbPath(%q) = %q, %v; want it accepted unchanged", p, got, err)
		}
	}
	bad := map[string]string{
		"empty":                  "",
		"parent":                 "..",
		"parent prefix":          "../x.jpg",
		"parent in the middle":   "a/../../x.jpg",
		"parent at the end":      "a/..",
		"dot":                    ".",
		"dot element":            "a/./b.jpg",
		"absolute":               "/etc/passwd",
		"double slash":           "a//b.jpg",
		"trailing slash":         "a/",
		"windows drive":          `C:x.jpg`,
		"windows drive absolute": `C:\x.jpg`,
		"drive with slash":       "C:/x.jpg",
		"backslash separator":    `a\b.jpg`,
		"backslash parent":       `..\x.jpg`,
		"unc":                    `\\server\share\x.jpg`,
		"unc with slashes":       "//server/share/x.jpg",
		"device namespace":       `\\?\C:\x.jpg`,
		"alternate data stream":  "a.jpg:stream",
		"nul":                    "a\x00.jpg",
		"control character":      "a\nb.jpg",
		"wildcard":               "a*.jpg",
		"question":               "a?.jpg",
		"pipe":                   "a|b.jpg",
		"trailing dot":           "a.jpg.",
		"dots only element":      "a/.../b.jpg",
		"trailing space":         "a /b.jpg",
		"reserved nul":           "NUL",
		"reserved with ext":      "a/nul.jpg",
		"reserved lower":         "con.txt",
		"reserved com1":          "COM1.jpg",
		"reserved lpt9":          "x/lpt9",
		"reserved conin":         "CONIN$",
		"reserved superscript":   "COM¹.jpg",
	}
	for name, p := range bad {
		t.Run(name, func(t *testing.T) {
			if got, err := cleanThumbPath(p); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("cleanThumbPath(%q) = %q, %v; want ErrInvalidArgument", p, got, err)
			}
		})
	}
}

func newTestThumbs(t *testing.T) (ThumbStore, string, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	dir := filepath.Join(t.TempDir(), ThumbsDirName)
	return newThumbStore(dir, clock), dir, clock
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// listFiles は dir 以下の通常ファイル（dir からの `/` 区切りの相対パス）を返す。
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func TestThumbStoreSaveOpenRemove(t *testing.T) {
	ctx := context.Background()
	s, dir, _ := newTestThumbs(t)
	rel := "2026/10/30/a.jpg"

	if _, err := s.Open(rel); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open before Save (no directory yet): err = %v, want ErrNotFound", err)
	}
	if err := s.Save(ctx, rel, []byte("jpeg-1")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rc, err := s.Open(rel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := readAll(t, rc); got != "jpeg-1" {
		t.Errorf("content = %q", got)
	}
	// 同じパスは置き換える。一時ファイルは残らない。
	if err := s.Save(ctx, rel, []byte("jpeg-2")); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	rc, _ = s.Open(rel)
	if got := readAll(t, rc); got != "jpeg-2" {
		t.Errorf("content after overwrite = %q", got)
	}
	if got := listFiles(t, dir); !slices.Equal(got, []string{rel}) {
		t.Errorf("files = %v, want only %s", got, rel)
	}

	if err := s.Remove(rel); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := s.Remove(rel); err != nil {
		t.Errorf("Remove of a missing file must succeed: %v", err)
	}
	if _, err := s.Open(rel); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open after Remove: err = %v, want ErrNotFound", err)
	}
	if err := s.Remove("never/existed.jpg"); err != nil {
		t.Errorf("Remove of a never-created path must succeed: %v", err)
	}
}

func TestThumbStoreRemoveWithoutDirectoryIsIdempotent(t *testing.T) {
	s, dir, _ := newTestThumbs(t)
	if err := s.Remove("a.jpg"); err != nil {
		t.Errorf("Remove: %v", err)
	}
	if err := s.SweepOrphans(context.Background(), func(string) bool { return false }); err != nil {
		t.Errorf("SweepOrphans: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read-only operations must not create %s (stat err = %v)", dir, err)
	}
}

func TestThumbStoreCreatesPrivateDirectory(t *testing.T) {
	s, dir, _ := newTestThumbs(t)
	if err := s.Save(context.Background(), "2026/10/a.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, "2026"), filepath.Join(dir, "2026", "10")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
			t.Errorf("%s mode = %v, want no access for group/others (0700)", d, fi.Mode().Perm())
		}
	}
}

func TestThumbStoreRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	s, dir, _ := newTestThumbs(t)
	outside := filepath.Join(filepath.Dir(dir), "secret.jpg")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../secret.jpg", "a/../../secret.jpg", outside, `..\secret.jpg`, "C:secret.jpg", "x\x00y", `\\host\share\f`, ""} {
		if _, err := s.Open(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Open(%q) err = %v, want ErrInvalidArgument", p, err)
		}
		if err := s.Save(ctx, p, []byte("x")); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Save(%q) err = %v, want ErrInvalidArgument", p, err)
		}
		if err := s.Remove(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Remove(%q) err = %v, want ErrInvalidArgument", p, err)
		}
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "secret" {
		t.Errorf("a file outside thumbs/ was touched: %q, %v", b, err)
	}
}

func TestThumbStoreRejectsSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	s, dir, _ := newTestThumbs(t)
	if err := s.Save(ctx, "real/a.jpg", []byte("a")); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.jpg")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(dir, "dirlink")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "filelink.jpg")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "inlink")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}

	for _, p := range []string{"dirlink/secret.jpg", "filelink.jpg", "dirlink/new.jpg", "inlink/a.jpg"} {
		if rc, err := s.Open(p); !errors.Is(err, domain.ErrInvalidArgument) {
			if rc != nil {
				_ = rc.Close()
			}
			t.Errorf("Open(%q) err = %v, want ErrInvalidArgument", p, err)
		}
		if err := s.Save(ctx, p, []byte("overwritten")); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Save(%q) err = %v, want ErrInvalidArgument", p, err)
		}
		if err := s.Remove(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Remove(%q) err = %v, want ErrInvalidArgument", p, err)
		}
	}
	if b, err := os.ReadFile(secret); err != nil || string(b) != "secret" {
		t.Errorf("the file behind a symlink was changed: %q, %v", b, err)
	}
	if got := listFiles(t, outsideDir); !slices.Equal(got, []string{"secret.jpg"}) {
		t.Errorf("files outside thumbs/ = %v, want only secret.jpg", got)
	}
}

func TestThumbStoreSaveLeavesNoPartialFile(t *testing.T) {
	ctx := context.Background()
	s, dir, _ := newTestThumbs(t)

	// 保存先が既存のディレクトリ → リネームに失敗する。一時ファイルを残さない。
	if err := os.MkdirAll(filepath.Join(dir, "d.jpg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, "d.jpg", []byte("x")); err == nil {
		t.Error("Save onto a directory must fail")
	}
	// 途中のディレクトリが通常ファイル → 作れない。
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, "f/x.jpg", []byte("x")); err == nil {
		t.Error("Save below a regular file must fail")
	}
	// キャンセル済みの ctx。
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Save(cctx, "c.jpg", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Save with a canceled context: err = %v, want context.Canceled", err)
	}
	if got := listFiles(t, dir); !slices.Equal(got, []string{"f"}) {
		t.Errorf("files = %v, want only the pre-existing f (no temp or partial files)", got)
	}
}

func TestThumbStoreSweepOrphans(t *testing.T) {
	ctx := context.Background()
	s, dir, clock := newTestThumbs(t)
	files := []string{"2026/10/keep.jpg", "2026/10/orphan.jpg", "2026/11/orphan2.jpg", "fresh-orphan.jpg"}
	for _, rel := range files {
		if err := s.Save(ctx, rel, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	// 中断した Save の一時ファイル。
	tmp := filepath.Join(dir, "2026", "10", tmpPrefix+"abc")
	if err := os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 実時刻で作ったファイルは、偽の時計（2026-10-30）から見ると新しい。fresh 以外を古くする。
	old := clock.Now().Add(-time.Hour)
	for _, rel := range append(files[:3:3], "2026/10/"+tmpPrefix+"abc") {
		if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(rel)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	fresh := clock.Now().Add(-time.Second)
	if err := os.Chtimes(filepath.Join(dir, "fresh-orphan.jpg"), fresh, fresh); err != nil {
		t.Fatal(err)
	}

	var asked []string
	known := func(rel string) bool {
		asked = append(asked, rel)
		return rel == "2026/10/keep.jpg"
	}
	if err := s.SweepOrphans(ctx, known); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	want := []string{"2026/10/keep.jpg", "fresh-orphan.jpg"} // 猶予の内（保存の直後）のファイルは消さない
	if got := listFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("files after sweep = %v, want %v", got, want)
	}
	if slices.Contains(asked, ".") {
		t.Errorf("known was asked about the root: %v", asked)
	}

	// 猶予を過ぎれば消える。
	clock.Advance(2 * orphanGrace)
	if err := s.SweepOrphans(ctx, known); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if got := listFiles(t, dir); !slices.Equal(got, []string{"2026/10/keep.jpg"}) {
		t.Errorf("files after the grace period = %v", got)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.SweepOrphans(cctx, func(string) bool { return false }); !errors.Is(err, context.Canceled) {
		t.Errorf("SweepOrphans with a canceled context: err = %v", err)
	}
}

func TestThumbStoreSweepOrphansRemovesSymlinksWithoutFollowing(t *testing.T) {
	ctx := context.Background()
	s, dir, clock := newTestThumbs(t)
	if err := s.Save(ctx, "a.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.jpg")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(dir, "dirlink")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	clock.Advance(30 * 24 * time.Hour) // 実時刻で作ったリンクの更新時刻より後にする
	if err := s.SweepOrphans(ctx, func(rel string) bool { return rel == "a.jpg" }); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if b, err := os.ReadFile(secret); err != nil || string(b) != "secret" {
		t.Errorf("sweep followed a symlink out of thumbs/: %q, %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dirlink")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the symlink itself should have been removed (lstat err = %v)", err)
	}
}

func TestDBThumbsUsesDirectoryNextToDatabase(t *testing.T) {
	db, _ := openTestDB(t)
	if err := db.Thumbs().Save(context.Background(), "a.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(db.thumbsDir), "/"+ThumbsDirName) {
		t.Errorf("thumbsDir = %q", db.thumbsDir)
	}
	if got := listFiles(t, db.thumbsDir); !slices.Equal(got, []string{"a.jpg"}) {
		t.Errorf("files = %v", got)
	}
}
