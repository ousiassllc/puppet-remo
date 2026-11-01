package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

var logT0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// logLine は slog の JSON ハンドラが書くのと同じ形の 1 行（改行なし）。
func logLine(i int, level, msg string, attrs ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"time":%q,"level":%q,"msg":%q`, logT0.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), level, msg)
	for j := 0; j+1 < len(attrs); j += 2 {
		fmt.Fprintf(&b, `,%q:%s`, attrs[j], attrs[j+1])
	}
	b.WriteString("}")
	return b.String()
}

func writeLogFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func entries(t *testing.T, r *LogReader, f service.AppLogFilter, page int) service.AppLogPage {
	t.Helper()
	p, err := r.Entries(t.Context(), f, page)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func messages(p service.AppLogPage) []string {
	out := make([]string, len(p.Items))
	for i, e := range p.Items {
		out[i] = e.Message
	}
	return out
}

func TestLogReaderReturnsNewestFirstWithFieldsAndAttrs(t *testing.T) {
	dir := t.TempDir()
	r := NewLogReader(dir)
	writeLogFile(t, r.Path(),
		logLine(0, "INFO", "server started", "port", "8765"),
		logLine(1, "WARN", "bearer authentication failed", "reason", `"unknown"`, "device", `"alice-laptop"`),
		logLine(2, "ERROR", "tool failed", "error", `"boom"`, "nested", `{"a":1,"b":[true]}`),
	)
	p := entries(t, r, service.AppLogAll, 1)
	if got := messages(p); strings.Join(got, "|") != "tool failed|bearer authentication failed|server started" {
		t.Fatalf("messages = %q, want newest first", got)
	}
	if p.HasPrev || p.HasNext || p.Page != 1 {
		t.Errorf("paging = %+v", p)
	}
	e := p.Items[0]
	if e.Level != "ERROR" || e.Severity != slog.LevelError || !e.Time.Equal(logT0.Add(2*time.Second)) {
		t.Errorf("entry fields = %+v", e)
	}
	want := []service.AppLogAttr{{Key: "error", Value: "boom"}, {Key: "nested", Value: `{"a":1,"b":[true]}`}}
	if fmt.Sprint(e.Attrs) != fmt.Sprint(want) {
		t.Errorf("attrs = %v, want %v (in file order; non-strings as JSON)", e.Attrs, want)
	}
	if p.Items[2].Attrs[0] != (service.AppLogAttr{Key: "port", Value: "8765"}) {
		t.Errorf("attrs = %v", p.Items[2].Attrs)
	}
}

func TestLogReaderFiltersByLevel(t *testing.T) {
	r := NewLogReader(t.TempDir())
	writeLogFile(t, r.Path(),
		logLine(0, "DEBUG", "d"), logLine(1, "INFO", "i1"), logLine(2, "WARN", "w1"),
		logLine(3, "ERROR", "e1"), logLine(4, "INFO", "i2"), logLine(5, "ERROR+2", "e2"), logLine(6, "WARN", "w2"),
	)
	for _, tc := range []struct {
		filter service.AppLogFilter
		want   string
	}{
		{service.AppLogAll, "w2|e2|i2|e1|w1|i1|d"},
		{service.AppLogWarn, "w2|e2|e1|w1"},
		{service.AppLogError, "e2|e1"},
	} {
		if got := strings.Join(messages(entries(t, r, tc.filter, 1)), "|"); got != tc.want {
			t.Errorf("filter %q = %q, want %q", tc.filter, got, tc.want)
		}
	}
	if service.ParseAppLogFilter("ERROR") != service.AppLogError || service.ParseAppLogFilter("nope") != service.AppLogAll || service.ParseAppLogFilter("") != service.AppLogAll {
		t.Error("ParseAppLogFilter must be case-insensitive and fall back to all")
	}
}

// 1 ページは固定の件数で、ファイルが 64 KiB のブロックを何度もまたいでも、全件を新しい順に 1 件も落とさず重複させず返す。
func TestLogReaderPagesAreFixedSizeAndCoverEveryEntryAcrossChunks(t *testing.T) {
	r := NewLogReader(t.TempDir())
	const total = service.AppLogPageSize*2 + 7
	pad := strings.Repeat("x", 3000) // 約 3 KB × 107 行 ≒ 320 KB（ブロックを数回またぐ）
	var lines []string
	for i := range total {
		lines = append(lines, logLine(i, "INFO", fmt.Sprintf("m%03d", i), "pad", fmt.Sprintf("%q", pad)))
	}
	writeLogFile(t, r.Path(), lines...)

	var got []string
	for page := 1; page <= 3; page++ {
		p := entries(t, r, service.AppLogAll, page)
		wantLen := service.AppLogPageSize
		if page == 3 {
			wantLen = 7
		}
		if len(p.Items) != wantLen || p.Page != page || p.HasPrev != (page > 1) || p.HasNext != (page < 3) {
			t.Fatalf("page %d = %d items, %+v", page, len(p.Items), struct{ Prev, Next bool }{p.HasPrev, p.HasNext})
		}
		got = append(got, messages(p)...)
	}
	for i, m := range got {
		if want := fmt.Sprintf("m%03d", total-1-i); m != want {
			t.Fatalf("entry %d = %s, want %s", i, m, want)
		}
	}
	// 範囲外は空（エラーにしない）。下限・上限は丸める。
	if p := entries(t, r, service.AppLogAll, 4); len(p.Items) != 0 || p.HasNext || !p.HasPrev {
		t.Errorf("page beyond the end = %+v", p)
	}
	if p := entries(t, r, service.AppLogAll, -5); p.Page != 1 || len(p.Items) != service.AppLogPageSize {
		t.Errorf("page -5 = page %d with %d items", p.Page, len(p.Items))
	}
	if p := entries(t, r, service.AppLogAll, 1_000_000); p.Page != service.MaxAppLogPage {
		t.Errorf("huge page = %d, want clamp to %d", p.Page, service.MaxAppLogPage)
	}
}

// 途中で切れた行（書き込み中・クラッシュ）、JSON でない行、time・level の無い行、空行は読み飛ばし、前後の行は読める。
func TestLogReaderSkipsPartialAndGarbageLines(t *testing.T) {
	r := NewLogReader(t.TempDir())
	content := strings.Join([]string{
		logLine(0, "INFO", "first"),
		`not json at all`,
		`{"time":"2026-11-01T09:00:01Z","msg":"no level"}`,
		``,
		logLine(2, "WARN", "second"),
		`{"time":"2026-11-01T09:00:03Z","level":"INFO","ms`, // 途中で切れた行
		logLine(4, "ERROR", "third"),
		`{"time":"2026-11-01T09:00:05Z","level":"INFO","msg":"half written`, // 末尾の途中の行（改行なし）
	}, "\n")
	if err := os.MkdirAll(filepath.Dir(r.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Path(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(messages(entries(t, r, service.AppLogAll, 1)), "|"); got != "third|second|first" {
		t.Errorf("messages = %q", got)
	}
}

func TestLogReaderSkipsOversizedLines(t *testing.T) {
	r := NewLogReader(t.TempDir())
	huge := logLine(1, "INFO", "huge", "blob", fmt.Sprintf("%q", strings.Repeat("y", logMaxLineBytes+10)))
	writeLogFile(t, r.Path(), logLine(0, "INFO", "before"), huge, logLine(2, "INFO", "after"))
	if got := strings.Join(messages(entries(t, r, service.AppLogAll, 1)), "|"); got != "after|before" {
		t.Errorf("messages = %q, want the oversized line skipped", got)
	}
}

func TestLogReaderTruncatesLongValuesForDisplay(t *testing.T) {
	r := NewLogReader(t.TempDir())
	writeLogFile(t, r.Path(), logLine(0, "INFO", strings.Repeat("あ", 2000), "k", fmt.Sprintf("%q", strings.Repeat("v", 5000))))
	e := entries(t, r, service.AppLogAll, 1).Items[0]
	if n := len([]rune(e.Message)); n != logMaxValueRunes+1 || !strings.HasSuffix(e.Message, "…") {
		t.Errorf("message length = %d runes", n)
	}
	if n := len([]rune(e.Attrs[0].Value)); n != logMaxValueRunes+1 {
		t.Errorf("attr length = %d runes", n)
	}
}

func TestLogReaderMissingFileIsEmpty(t *testing.T) {
	r := NewLogReader(t.TempDir())
	if p := entries(t, r, service.AppLogAll, 1); len(p.Items) != 0 || p.HasNext {
		t.Errorf("page = %+v", p)
	}
}

func TestLogReaderHonorsCancellation(t *testing.T) {
	r := NewLogReader(t.TempDir())
	writeLogFile(t, r.Path(), logLine(0, "INFO", "x"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Entries(ctx, service.AppLogAll, 1); err == nil {
		t.Error("a canceled context must stop the read")
	}
}

// ローテーションされた古いファイル（puppet-remo-<時刻>.log）へ、今のファイルを読み切ったあと新しい順に続く。
// ページの境目がファイルの途中でも、欠けも重複もない。
func TestLogReaderContinuesIntoRotatedFilesNewestFirst(t *testing.T) {
	r := NewLogReader(t.TempDir())
	dir := filepath.Dir(r.Path())
	mk := func(from, to int) []string {
		var lines []string
		for i := from; i < to; i++ {
			level := "INFO"
			if i == 5 {
				level = "ERROR"
			}
			lines = append(lines, logLine(i, level, fmt.Sprintf("m%02d", i)))
		}
		return lines
	}
	writeLogFile(t, filepath.Join(dir, "puppet-remo-2026-11-01T08-00-00.000.log"), mk(0, 30)...)
	writeLogFile(t, filepath.Join(dir, "puppet-remo-2026-11-01T08-30-00.000.log"), mk(30, 50)...)
	writeLogFile(t, r.Path(), mk(50, 60)...)
	writeLogFile(t, filepath.Join(dir, "unrelated.log"), logLine(99, "INFO", "not ours"))

	var got []string
	for page := 1; page <= 2; page++ {
		got = append(got, messages(entries(t, r, service.AppLogAll, page))...)
	}
	if len(got) != 60 {
		t.Fatalf("got %d entries, want 60: %v", len(got), got)
	}
	for i, m := range got {
		if want := fmt.Sprintf("m%02d", 59-i); m != want {
			t.Fatalf("entry %d = %s, want %s", i, m, want)
		}
	}
	if got := strings.Join(messages(entries(t, r, service.AppLogError, 1)), "|"); got != "m05" {
		t.Errorf("the error filter must reach the oldest rotated file: %q", got)
	}
}

// 本物のローテーション（lumberjack の Rotate）のあとでも、前のファイルの記録が新しい順に続けて見える。
func TestLogReaderWorksWithRealLumberjackRotation(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := NewLogger(dir, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	log.Info("before-rotation-1")
	log.Error("before-rotation-2")
	if err := closer.(*lumberjack.Logger).Rotate(); err != nil {
		t.Fatal(err)
	}
	log.Info("after-rotation")

	r := NewLogReader(dir)
	if got := strings.Join(messages(entries(t, r, service.AppLogAll, 1)), "|"); got != "after-rotation|before-rotation-2|before-rotation-1" {
		t.Errorf("messages = %q", got)
	}
	if got := strings.Join(messages(entries(t, r, service.AppLogError, 1)), "|"); got != "before-rotation-2" {
		t.Errorf("error filter = %q", got)
	}
}
