package localfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/localfs"
)

const (
	maxFile  = 1 << 20
	maxTotal = 4 << 20
)

type fixture struct {
	t     *testing.T
	store *localfs.Store
	root  string // 置き場所（まだ無い）
	outer string // 置き場所の外（同じ親の下）
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: localfs.New(), root: filepath.Join(base, "inbox"), outer: filepath.Join(base, "outside")}
	if err := os.Mkdir(f.outer, 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) put(rel string, offset int64, data string) (platform.FileInfo, error) {
	f.t.Helper()
	return f.store.Put(context.Background(), f.root, platform.FilePut{
		Rel: rel, Offset: offset, Data: []byte(data), MaxFileBytes: maxFile, MaxTotalBytes: maxTotal,
	})
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func wantCode(t *testing.T, err error, want *domain.Error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want code %s", err, want.Code)
	}
}

func TestPutGetListDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// ルートが無くても作る。途中のフォルダも作る。
	info, err := f.put("logs/app/run.log", 0, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 5 || info.SHA256 != sum("hello") || info.Rel != "logs/app/run.log" {
		t.Fatalf("put info = %+v", info)
	}

	data, got, err := f.store.Get(ctx, f.root, `logs\app\run.log`, 0, 100)
	if err != nil || string(data) != "hello" || got.Size != 5 || got.SHA256 != sum("hello") {
		t.Fatalf("get = %q, %+v, %v", data, got, err)
	}
	// 範囲の読み取りは、全体の大きさと SHA-256 を返す。
	data, got, err = f.store.Get(ctx, f.root, "logs/app/run.log", 1, 3)
	if err != nil || string(data) != "ell" || got.Size != 5 || got.SHA256 != sum("hello") {
		t.Fatalf("ranged get = %q, %+v, %v", data, got, err)
	}
	if data, _, err = f.store.Get(ctx, f.root, "logs/app/run.log", 5, 10); err != nil || len(data) != 0 {
		t.Fatalf("get at the end = %q, %v", data, err)
	}
	if _, _, err = f.store.Get(ctx, f.root, "logs/app/run.log", 6, 1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("get beyond the end: %v", err)
	}

	if _, err := f.put("a.txt", 0, "A"); err != nil {
		t.Fatal(err)
	}
	entries, truncated, err := f.store.List(ctx, f.root, "", 10)
	if err != nil || truncated {
		t.Fatalf("list = %v, %v", truncated, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Rel)
	}
	if !slices.Equal(names, []string{"a.txt", "logs"}) || entries[0].Size != 1 || !entries[1].IsDir {
		t.Fatalf("entries = %+v", entries)
	}
	if entries, _, err = f.store.List(ctx, f.root, "logs/app", 10); err != nil || len(entries) != 1 || entries[0].Rel != "logs/app/run.log" {
		t.Fatalf("nested list = %+v, %v", entries, err)
	}
	if entries, truncated, _ = f.store.List(ctx, f.root, "", 1); len(entries) != 1 || !truncated {
		t.Fatalf("limited list = %+v, %v", entries, truncated)
	}

	if total, err := f.store.TotalSize(ctx, f.root); err != nil || total != 6 {
		t.Fatalf("total = %d, %v", total, err)
	}

	if d, err := f.store.Delete(ctx, f.root, "a.txt"); err != nil || d.Size != 1 {
		t.Fatalf("delete = %+v, %v", d, err)
	}
	if _, _, err := f.store.Get(ctx, f.root, "a.txt", 0, 1); !errors.Is(err, domain.ErrFileNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := f.store.Delete(ctx, f.root, "a.txt"); !errors.Is(err, domain.ErrFileNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	_, err = f.store.Delete(ctx, f.root, "logs")
	wantCode(t, err, domain.NewError(domain.CodeConflict, "", nil)) // 空でないフォルダは消さない
}

func TestListMissingRootIsEmpty(t *testing.T) {
	f := newFixture(t)
	entries, truncated, err := f.store.List(context.Background(), f.root, "", 10)
	if err != nil || len(entries) != 0 || truncated {
		t.Fatalf("list = %v, %v, %v", entries, truncated, err)
	}
	if _, err := os.Stat(f.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list must not create the root: %v", err)
	}
}

func TestPutReplacesAndAppends(t *testing.T) {
	f := newFixture(t)
	if _, err := f.put("a.bin", 0, "0123456789"); err != nil {
		t.Fatal(err)
	}
	// 同じ名前は置き換える。
	info, err := f.put("a.bin", 0, "abc")
	if err != nil || info.Size != 3 || info.SHA256 != sum("abc") {
		t.Fatalf("replace = %+v, %v", info, err)
	}
	// 分割の転送: 大きさがちょうど offset のときだけ追記できる。
	info, err = f.put("a.bin", 3, "def")
	if err != nil || info.Size != 6 || info.SHA256 != sum("abcdef") {
		t.Fatalf("append = %+v, %v", info, err)
	}
	if _, err := f.put("a.bin", 4, "x"); !errors.Is(err, domain.NewError(domain.CodeConflict, "", nil)) {
		t.Fatalf("append at a wrong offset: %v", err)
	}
	if _, err := f.put("new.bin", 5, "x"); !errors.Is(err, domain.NewError(domain.CodeConflict, "", nil)) {
		t.Fatalf("offset on a new file: %v", err)
	}
	// 失敗した新規の書き込みは、空のファイルを残さない。
	if _, err := os.Stat(filepath.Join(f.root, "new.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new.bin exists after a rejected put: %v", err)
	}
}

func TestSizeLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	put := func(rel string, off int64, n int, maxFile, maxTotal int64) error {
		_, err := f.store.Put(ctx, f.root, platform.FilePut{
			Rel: rel, Offset: off, Data: bytes.Repeat([]byte("x"), n), MaxFileBytes: maxFile, MaxTotalBytes: maxTotal,
		})
		return err
	}
	if err := put("a", 0, 10, 10, 100); err != nil {
		t.Fatalf("exactly the per-file limit: %v", err)
	}
	wantCode(t, put("b", 0, 11, 10, 100), domain.ErrFileTooLarge)
	wantCode(t, put("a", 10, 1, 10, 100), domain.ErrFileTooLarge) // 追記して上限を超える
	if _, err := os.Stat(filepath.Join(f.root, "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected file must not exist: %v", err)
	}

	if err := put("c", 0, 10, 10, 20); err != nil { // 合計 20（上限ちょうど）
		t.Fatalf("exactly the total limit: %v", err)
	}
	wantCode(t, put("d", 0, 1, 10, 20), domain.ErrFileStoreFull)
	// 置き換えは、置き換える分を数えない。
	if err := put("c", 0, 10, 10, 20); err != nil {
		t.Fatalf("replace within the total limit: %v", err)
	}
	wantCode(t, put("c", 0, 11, 11, 20), domain.ErrFileStoreFull)
	// 消せば入る。
	if _, err := f.store.Delete(ctx, f.root, "c"); err != nil {
		t.Fatal(err)
	}
	if err := put("d", 0, 10, 10, 20); err != nil {
		t.Fatalf("after delete: %v", err)
	}
}

func TestRejectsBadPaths(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, rel := range []string{"../x", "a/../../x", "/etc/passwd", `C:\x`, "C:x", `\\host\share\x`, `\\?\C:\x`, "a.txt:s", "CON", "a/NUL.txt", ""} {
		if _, err := f.put(rel, 0, "x"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Put(%q): %v", rel, err)
		}
		if _, _, err := f.store.Get(ctx, f.root, rel, 0, 1); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Get(%q): %v", rel, err)
		}
		if _, err := f.store.Delete(ctx, f.root, rel); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Delete(%q): %v", rel, err)
		}
		if _, err := f.store.Resolve(ctx, f.root, rel); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Resolve(%q): %v", rel, err)
		}
	}
	if _, err := f.store.Put(ctx, "relative/root", platform.FilePut{Rel: "a", Data: []byte("x"), MaxFileBytes: 10, MaxTotalBytes: 10}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("relative root: %v", err)
	}
	if entries, _ := os.ReadDir(f.outer); len(entries) != 0 {
		t.Errorf("something was written outside the root: %v", entries)
	}
}

// symlink は Linux・macOS では本物のシンボリックリンクを作る（Windows の実機では権限が要るので、できなければ飛ばす）。
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
}

func TestSymlinkEscapeIsRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.put("keep.txt", 0, "k"); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(f.outer, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, f.outer, filepath.Join(f.root, "dirlink"))                                // 外のフォルダへのリンク
	symlink(t, secret, filepath.Join(f.root, "filelink.txt"))                            // 外のファイルへのリンク
	symlink(t, filepath.Join(f.root, "keep.txt"), filepath.Join(f.root, "inlink.txt"))   // 中のファイルへのリンクも拒否
	symlink(t, filepath.Join(f.outer, "new.txt"), filepath.Join(f.root, "dangling.txt")) // 先が無いリンクへ書いても外に作らない

	for _, rel := range []string{"dirlink/secret.txt", "dirlink/new.txt", "filelink.txt", "inlink.txt", "dangling.txt", "dirlink"} {
		if _, err := f.put(rel, 0, "pwned"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Put(%q): %v", rel, err)
		}
		if _, _, err := f.store.Get(ctx, f.root, rel, 0, 100); err == nil {
			t.Errorf("Get(%q) succeeded", rel)
		}
		if _, err := f.store.Resolve(ctx, f.root, rel); err == nil {
			t.Errorf("Resolve(%q) succeeded", rel)
		}
		if _, err := f.store.Delete(ctx, f.root, rel); err == nil {
			t.Errorf("Delete(%q) succeeded", rel)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "secret" {
		t.Errorf("the file outside the root was modified: %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.outer, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file was created outside the root through a dangling link: %v", err)
	}
	// リンク自身は消されていない（Delete が拒否した）。
	if _, err := os.Lstat(filepath.Join(f.root, "filelink.txt")); err != nil {
		t.Errorf("the link was removed: %v", err)
	}

	// 一覧にリンクは出ない。合計にも数えない。
	entries, _, err := f.store.List(ctx, f.root, "", 100)
	if err != nil || len(entries) != 1 || entries[0].Rel != "keep.txt" {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	if _, _, err := f.store.List(ctx, f.root, "dirlink", 100); err == nil {
		t.Error("List through a link succeeded")
	}
	if total, err := f.store.TotalSize(ctx, f.root); err != nil || total != 1 {
		t.Errorf("total = %d, %v", total, err)
	}
}

func TestRootMayBeReachedThroughALink(t *testing.T) {
	// ルート自体はホストが選ぶ場所なので、リンクを経由していてもよい（中身はリンクを使えない）。
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	symlink(t, realDir, link)
	store := localfs.New()
	if _, err := store.Put(context.Background(), link, platform.FilePut{Rel: "a.txt", Data: []byte("x"), MaxFileBytes: 10, MaxTotalBytes: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "a.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestResolveReturnsAFileInsideTheRoot(t *testing.T) {
	f := newFixture(t)
	if _, err := f.put("sub/app.exe", 0, "MZ"); err != nil {
		t.Fatal(err)
	}
	abs, err := f.store.Resolve(context.Background(), f.root, "sub/app.exe")
	if err != nil || abs != filepath.Join(f.root, "sub", "app.exe") {
		t.Fatalf("Resolve = %q, %v", abs, err)
	}
	if _, err := f.store.Resolve(context.Background(), f.root, "sub"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Resolve of a folder: %v", err)
	}
	if _, err := f.store.Resolve(context.Background(), f.root, "nope.exe"); !errors.Is(err, domain.ErrFileNotFound) {
		t.Fatalf("Resolve of a missing file: %v", err)
	}
}

func TestFileWhereAFolderIsNeeded(t *testing.T) {
	f := newFixture(t)
	if _, err := f.put("a", 0, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.put("a/b", 0, "x"); !errors.Is(err, domain.NewError(domain.CodeConflict, "", nil)) {
		t.Fatalf("put under a file: %v", err)
	}
	if _, _, err := f.store.Get(context.Background(), f.root, "a/b", 0, 1); !errors.Is(err, domain.ErrFileNotFound) {
		t.Fatalf("get under a file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(f.root, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.put("d", 0, "x"); !errors.Is(err, domain.NewError(domain.CodeConflict, "", nil)) {
		t.Fatalf("put over a folder: %v", err)
	}
	if _, _, err := f.store.Get(context.Background(), f.root, "d", 0, 1); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("get of a folder: %v", err)
	}
}

func TestCanceledContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.store.Put(ctx, f.root, platform.FilePut{Rel: "a", Data: []byte("x"), MaxFileBytes: 10, MaxTotalBytes: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v", err)
	}
}
