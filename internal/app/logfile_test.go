package app

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/natefinch/lumberjack.v2"
)

// ログ画面が開いているファイルを、lumberjack のローテーション（リネーム）と削除が妨げない（#53）。
// Windows では、os.Open（FILE_SHARE_DELETE なし）で開いたままだとリネームが共有違反で失敗する。
// Windows 以外では元から成り立つので、この検査が意味を持つのは Windows の実機（smoke-windows.md）。
func TestOpenLogFileDoesNotBlockRenameOrDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "puppet-remo.log")
	if err := os.WriteFile(path, []byte("line1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	rotated := filepath.Join(dir, "puppet-remo-2026-11-02T00-00-00.000.log")
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename while the log screen has the file open: %v", err)
	}
	// 開いたままのハンドルは、リネーム後も同じ内容を読める。
	if got, err := io.ReadAll(f); err != nil || string(got) != "line1\n" {
		t.Errorf("read after rename = %q, %v", got, err)
	}
	// ローテーションのあとに作られた新しいファイルも書ける（lumberjack は新しい puppet-remo.log を作る）。
	if err := os.WriteFile(path, []byte("line2\n"), 0o600); err != nil {
		t.Fatalf("create the new log file: %v", err)
	}
	if err := os.Remove(rotated); err != nil {
		t.Fatalf("delete the rotated file while it is open: %v", err)
	}
}

// 無いファイルは fs.ErrNotExist（scanFileReverse が「無いだけ」と見分けられる）。
func TestOpenLogFileMissingIsNotExist(t *testing.T) {
	_, err := openLogFile(filepath.Join(t.TempDir(), "nope.log"))
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want not-exist", err)
	}
}

// 実際の lumberjack のローテーション（Rotate）が、ログ画面の読み出し（openLogFile で開いたまま）の最中でも成功し、書き込みも続く（#53）。
// Linux では元から成り立つので回帰は検出できない。意味があるのは Windows の実機（smoke-windows.md §4.15 F6）だけ。
func TestRotateSucceedsWhileTheLogScreenHasTheFileOpen(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := NewLogger(dir, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	log.Info("before-rotation")

	f, err := openLogFile(LogFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if err := closer.(*lumberjack.Logger).Rotate(); err != nil {
		t.Fatalf("Rotate while the log file is open for reading: %v", err)
	}
	log.Info("after-rotation")
	if st, err := os.Stat(LogFilePath(dir)); err != nil || st.Size() == 0 {
		t.Errorf("the new log file after rotation: %v, %v", st, err)
	}
}
