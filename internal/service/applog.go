package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// AppLog はアプリ運用ログ（slog。NFR-O01）の閲覧の窓口。ホスト GUI のログ画面（FR-H13）が使う。
// 監査ログ（DB。repository.Audit。閲覧 UI は FR-AU01・M3）とは別のもの。
type AppLog interface {
	// Entries は条件に合う記録を新しい順に 1 ページ返す（1 ページ AppLogPageSize 件）。page は 1 始まりで、範囲外は丸める。
	Entries(ctx context.Context, filter AppLogFilter, page int) (AppLogPage, error)
	// Path は今のログファイルのパス（画面に出す）。
	Path() string
}

const (
	// AppLogPageSize はログ画面の 1 ページの件数（固定）。
	AppLogPageSize = 50
	// MaxAppLogPage は読むページの上限。深いページでも読む量が増え続けないようにする。
	MaxAppLogPage = 100
)

// AppLogFilter はレベルの絞り込み。名前の「以上」を出す。
type AppLogFilter string

const (
	AppLogAll   AppLogFilter = "all"   // すべて（ファイルにある DEBUG を含む）
	AppLogWarn  AppLogFilter = "warn"  // WARN 以上
	AppLogError AppLogFilter = "error" // ERROR 以上
)

// ParseAppLogFilter はクエリの値を AppLogFilter にする。空・未知の値は AppLogAll。
func ParseAppLogFilter(s string) AppLogFilter {
	switch f := AppLogFilter(strings.ToLower(strings.TrimSpace(s))); f {
	case AppLogWarn, AppLogError:
		return f
	}
	return AppLogAll
}

// MinLevel は絞り込みの下限のレベル。
func (f AppLogFilter) MinLevel() slog.Level {
	switch f {
	case AppLogWarn:
		return slog.LevelWarn
	case AppLogError:
		return slog.LevelError
	}
	return slog.Level(-1 << 30)
}

// ClampAppLogPage は page を 1〜MaxAppLogPage に丸める。
func ClampAppLogPage(page int) int { return min(max(page, 1), MaxAppLogPage) }

// AppLogEntry はログ 1 行。
type AppLogEntry struct {
	Time time.Time
	// Level はファイルの表記（DEBUG・INFO・WARN・ERROR。slog の標準以外のレベルは "INFO+2" など）。
	Level string
	// Severity は Level の数値（絞り込みに使う）。
	Severity slog.Level
	Message  string
	Attrs    []AppLogAttr
}

// AppLogAttr は記録に付いた属性（キーと値の文字列）。
type AppLogAttr struct{ Key, Value string }

// AppLogPage は新しい順のログの 1 ページ。
type AppLogPage struct {
	Items   []AppLogEntry
	Page    int
	HasPrev bool
	HasNext bool
}
