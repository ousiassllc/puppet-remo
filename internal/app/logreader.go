package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// ログ画面の読み出し（FR-H13）。ログファイル（JSON の slog。NewLogger）を末尾から一定のブロックずつ逆向きに読み、
// 1 ページ分（service.AppLogPageSize 件）の新しい順の記録だけを取り出す。ファイル全体をメモリに載せない。
// ローテーション（lumberjack）された古いファイル（puppet-remo-<時刻>.log）へは、今のファイルを読み切ったあとに新しい順に続く。
const (
	// logReadChunk は逆向きに読む 1 ブロックの大きさ。
	logReadChunk = 64 << 10
	// logMaxLineBytes はこれを超える行は読み飛ばす（壊れた行・異常に長い行でメモリを使わない）。
	logMaxLineBytes = 1 << 20
	// 画面に出す値・メッセージの最大の長さ（文字数）と、1 件に出す属性の最大数。
	logMaxValueRunes = 500
	logMaxAttrs      = 24
)

// LogReader は service.AppLog の実装（ログファイルの読み出し）。
type LogReader struct{ path string }

var _ service.AppLog = (*LogReader)(nil)

// NewLogReader は dataDir のログ（LogFilePath）を読む LogReader を返す。
func NewLogReader(dataDir string) *LogReader { return &LogReader{path: LogFilePath(dataDir)} }

// Path は今のログファイルのパス。
func (r *LogReader) Path() string { return r.path }

// Entries は新しい順に、filter に合う記録の page ページ目を返す。ファイルが無ければ空のページ。
// 書き込み中の途中の行・JSON でない行・壊れた行は読み飛ばす。
func (r *LogReader) Entries(ctx context.Context, filter service.AppLogFilter, page int) (service.AppLogPage, error) {
	page = service.ClampAppLogPage(page)
	skip := (page - 1) * service.AppLogPageSize
	want := service.AppLogPageSize + 1 // 次のページの有無を知るために 1 件多く読む
	floor := filter.MinLevel()

	var items []service.AppLogEntry
	for _, file := range r.files() {
		err := scanFileReverse(ctx, file, func(line []byte) bool {
			e, ok := parseLogLine(line)
			if !ok || e.Severity < floor {
				return true
			}
			if skip > 0 {
				skip--
				return true
			}
			items = append(items, e)
			return len(items) < want
		})
		if err != nil {
			return service.AppLogPage{}, err
		}
		if len(items) >= want {
			break
		}
	}
	out := service.AppLogPage{Page: page, HasPrev: page > 1}
	if len(items) > service.AppLogPageSize {
		// 上限のページの「次」は、丸められて同じページになるので出さない。
		out.HasNext = page < service.MaxAppLogPage
		items = items[:service.AppLogPageSize]
	}
	out.Items = items
	return out, nil
}

// files は読む順（今のファイル → ローテーション済みを新しい順）のパス。名前の時刻が辞書順で時刻順になる。
func (r *LogReader) files() []string {
	dir, base := filepath.Split(r.path)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "-"
	var backups []string
	// filepath.Glob はディレクトリ名の [ ? * を式として扱うので、ディレクトリを直接読んで名前で選ぶ。
	if dirEntries, err := os.ReadDir(filepath.Clean(dir)); err == nil {
		for _, e := range dirEntries {
			name := e.Name()
			if !e.IsDir() && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ext) {
				backups = append(backups, filepath.Join(dir, name))
			}
		}
	}
	slices.Sort(backups)
	slices.Reverse(backups)
	return append([]string{r.path}, backups...)
}

// scanFileReverse はファイルの完結した行を末尾から順に fn へ渡す（fn が false を返すと止める）。ファイルが無ければ何もしない。
func scanFileReverse(ctx context.Context, path string, fn func(line []byte) bool) error {
	f, err := openLogFile(path) // Windows は FILE_SHARE_DELETE つき（ローテーションと競合しない。#53）
	if errors.Is(err, fs.ErrNotExist) {
		return nil // ローテーションの途中で消えた・まだ作られていない
	}
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat log file: %w", err)
	}
	return scanLinesReverse(ctx, f, st.Size(), fn)
}

// scanLinesReverse は r の先頭 size バイトの行を、末尾から順に fn へ渡す。logReadChunk バイトずつ読み、
// logMaxLineBytes を超える行は読み飛ばす。最後の改行のない行（書き込み途中かもしれない）も 1 行として渡す（壊れていれば parseLogLine が捨てる）。
func scanLinesReverse(ctx context.Context, r io.ReaderAt, size int64, fn func(line []byte) bool) error {
	var carry []byte // まだ行頭が見つかっていない行の後ろの部分
	skipping := false
	for pos := size; pos > 0; {
		n := min(int64(logReadChunk), pos)
		pos -= n
		if err := ctx.Err(); err != nil {
			return err
		}
		buf := make([]byte, int(n)+len(carry))
		if _, err := r.ReadAt(buf[:n], pos); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read log file: %w", err)
		}
		copy(buf[n:], carry)
		end := len(buf)
		for {
			i := bytes.LastIndexByte(buf[:end], '\n')
			if i < 0 {
				break
			}
			if skipping {
				skipping = false // 長すぎる行の最後の部分。読み飛ばす
			} else if line := buf[i+1 : end]; len(line) <= logMaxLineBytes && !fn(line) {
				return nil
			}
			end = i
		}
		rest := buf[:end]
		switch {
		case skipping:
			carry = nil
		case len(rest) > logMaxLineBytes:
			skipping, carry = true, nil
		default:
			carry = bytes.Clone(rest)
		}
	}
	if !skipping && len(carry) > 0 && len(carry) <= logMaxLineBytes {
		fn(carry)
	}
	return nil
}

// parseLogLine は slog の JSON の 1 行を AppLogEntry にする。time・level が無い・JSON でない・途中で切れた行は false。
// time・level・msg 以外の属性は、出現順に文字列にして並べる（ネストした値は JSON のまま）。値は redactString で秘密を隠す。
func parseLogLine(line []byte) (service.AppLogEntry, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return service.AppLogEntry{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	if _, err := dec.Token(); err != nil { // '{'
		return service.AppLogEntry{}, false
	}
	var e service.AppLogEntry
	var haveTime, haveLevel bool
	for dec.More() {
		keyTok, err := dec.Token()
		key, isKey := keyTok.(string)
		if err != nil || !isKey {
			return service.AppLogEntry{}, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return service.AppLogEntry{}, false
		}
		val := rawText(raw)
		switch key {
		case slog.TimeKey:
			t, err := time.Parse(time.RFC3339Nano, val)
			if err != nil {
				return service.AppLogEntry{}, false
			}
			e.Time, haveTime = t, true
		case slog.LevelKey:
			var l slog.Level
			if err := l.UnmarshalText([]byte(val)); err != nil {
				return service.AppLogEntry{}, false
			}
			e.Level, e.Severity, haveLevel = val, l, true
		case slog.MessageKey:
			e.Message = truncateRunes(redactString(val), logMaxValueRunes)
		default:
			if len(e.Attrs) < logMaxAttrs {
				v := redactString(val)
				if sensitiveKey(key) {
					v = redactedValue
				}
				e.Attrs = append(e.Attrs, service.AppLogAttr{Key: key, Value: truncateRunes(v, logMaxValueRunes)})
			}
		}
	}
	if _, err := dec.Token(); err != nil { // '}'。途中で切れた行はここまで来ない
		return service.AppLogEntry{}, false
	}
	return e, haveTime && haveLevel
}

// rawText は JSON の値を表示用の文字列にする。文字列はそのまま、それ以外は JSON の文字列表記。
func rawText(raw json.RawMessage) string {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

// truncateRunes は s を最大 n 文字にする（超えたら末尾に … を付ける）。
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
