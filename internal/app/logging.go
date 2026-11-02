package app

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// アプリ運用ログ（NFR-O01）: slog の JSON を logs\ に 10 MiB × 5 世代でローテーションして出力する。
// 監査ログ（業務記録。repository.Audit）とは別物。トークン・API キーなどの秘密は出力しない（呼び出し側が渡さないことが原則。
// 渡してしまったときのために、NewLogger は redactAttr で秘密のキーの値とトークン・ハッシュの形の文字列を隠す）。
// 閲覧は GUI のログ画面（LogReader。FR-H13）。
const (
	logFileName     = "puppet-remo.log"
	logMaxSizeMiB   = 10
	logMaxBackups   = 4 // 現在のファイルと合わせて 5 世代
	logDirMode      = 0o700
	dataDirName     = "puppet-remo"
	DatabaseFile    = "puppet-remo.db"
	logSubdirectory = "logs"
)

// DataDir はデータのディレクトリ（%LOCALAPPDATA%\puppet-remo。NFR-S11）を返す。localAppData が空ならエラー。
func DataDir(localAppData string) (string, error) {
	if localAppData == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}
	return filepath.Join(localAppData, dataDirName), nil
}

// ParseLogLevel は "debug" / "info" / "warn" / "error"（大文字小文字を区別しない）を slog.Level にする。空は info。
func ParseLogLevel(s string) (slog.Level, error) {
	if strings.TrimSpace(s) == "" {
		return slog.LevelInfo, nil
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo, fmt.Errorf("invalid log level %q (want debug, info, warn or error)", s)
	}
	return l, nil
}

// NewLogger は dataDir\logs\ にローテーションして書く JSON の slog.Logger と、その書き込み先を閉じる関数を返す。
// ディレクトリは現在のユーザーだけが読めるモード（0700）で作る。
func NewLogger(dataDir string, level slog.Level) (*slog.Logger, io.Closer, error) {
	dir := filepath.Join(dataDir, logSubdirectory)
	if err := os.MkdirAll(dir, logDirMode); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &lumberjack.Logger{
		Filename:   LogFilePath(dataDir),
		MaxSize:    logMaxSizeMiB,
		MaxBackups: logMaxBackups,
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr})), w, nil
}

// LogFilePath は今のログファイル（dataDir\logs\puppet-remo.log。ローテーションされた古いものは同じ場所の puppet-remo-<時刻>.log）。
func LogFilePath(dataDir string) string {
	return filepath.Join(dataDir, logSubdirectory, logFileName)
}
