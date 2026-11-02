package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// 開くのは常に、ログファイルのあるフォルダー（LogFilePath の親）ちょうど 1 つ。
func TestLogReaderOpenFolderOpensExactlyTheLogDirectory(t *testing.T) {
	dir := t.TempDir()
	opener := platformtest.NewFakeFolderOpener()
	opener.AcceptAny()
	r := NewLogReader(dir, WithFolderOpener(opener))

	if err := r.OpenFolder(t.Context()); err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	want := filepath.Dir(LogFilePath(dir))
	if got := opener.Opened(); !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("opened = %v, want exactly [%s]", got, want)
	}
	if want == dir {
		t.Fatalf("the log directory must be a sub directory of the data dir, got %s", want)
	}
	// 何度呼んでも同じ場所（呼び出しごとに変わる入力が無い）。
	if err := r.OpenFolder(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := opener.Opened(); len(got) != 2 || got[0] != want || got[1] != want {
		t.Errorf("opened = %v, want the same directory twice", got)
	}
}

// 開くフォルダーは Path()（画面に出すログファイルのパス）の親と一致する。
func TestLogReaderOpenFolderMatchesTheDisplayedLogFilePath(t *testing.T) {
	opener := platformtest.NewFakeFolderOpener()
	opener.AcceptAny()
	r := NewLogReader(t.TempDir(), WithFolderOpener(opener))
	if err := r.OpenFolder(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := opener.Opened(); len(got) != 1 || got[0] != filepath.Dir(r.Path()) {
		t.Errorf("opened = %v, want the parent of %s", got, r.Path())
	}
}

// フォルダーが無い（opener が ErrNotFound）なら service.ErrNotFound に写し、パスを含めない。作ることもしない。
func TestLogReaderOpenFolderMissingDirectoryIsServiceNotFound(t *testing.T) {
	dir := t.TempDir()
	opener := platformtest.NewFakeFolderOpener() // フォルダーを 1 つも知らない
	r := NewLogReader(dir, WithFolderOpener(opener))

	err := r.OpenFolder(t.Context())
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("OpenFolder = %v, want service.ErrNotFound", err)
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("error leaks the path: %v", err)
	}
	if got := opener.Opened(); len(got) != 0 {
		t.Errorf("opened = %v, want none", got)
	}
}

// opener の失敗はそのまま返る（パスを足さない）。
func TestLogReaderOpenFolderPropagatesOtherErrors(t *testing.T) {
	boom := errors.New("explorer refused")
	opener := platformtest.NewFakeFolderOpener()
	opener.AcceptAny()
	opener.SetError("OpenFolder", boom)
	r := NewLogReader(t.TempDir(), WithFolderOpener(opener))
	if err := r.OpenFolder(t.Context()); !errors.Is(err, boom) || errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("OpenFolder = %v, want the opener's error", err)
	}
}

func TestLogReaderOpenFolderWithoutOpenerFails(t *testing.T) {
	dir := t.TempDir()
	err := NewLogReader(dir).OpenFolder(t.Context())
	if err == nil {
		t.Fatal("OpenFolder without an opener must fail")
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("error leaks the path: %v", err)
	}
}

func TestLogReaderOpenFolderHonorsCancellation(t *testing.T) {
	opener := platformtest.NewFakeFolderOpener()
	opener.AcceptAny()
	r := NewLogReader(t.TempDir(), WithFolderOpener(opener))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.OpenFolder(ctx); err == nil {
		t.Fatal("OpenFolder with a canceled context must fail")
	}
	if got := opener.Opened(); len(got) != 0 {
		t.Errorf("opened = %v, want none", got)
	}
}
