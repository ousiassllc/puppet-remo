package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// 監査の閲覧の値型。web は repository を import しないので、service の別名で使う（AuditEntry の流儀）。
type (
	// AuditQuery は一覧の絞り込み（API 仕様 §7.3 の `GET /audit` のクエリ）。Limit 0 は既定の 50 件、最大 200 件。
	AuditQuery = repository.AuditQuery
	// AuditThumb はサムネイルのメタ情報（画像本体は AuditReader.Thumbnail）。
	AuditThumb = repository.ThumbMeta
)

// 一覧の件数（AuditQuery.Limit）。
const (
	DefaultAuditLimit = repository.DefaultAuditLimit
	MaxAuditLimit     = repository.MaxAuditLimit
)

// AuditPage は一覧の 1 ページ。Entries の DetailJSON は常に空（引数は AuditDetail.Args）。
type AuditPage struct {
	Entries []AuditEntry
	// Next は次のページがあるときの最後の id（AuditQuery.Cursor に渡す）。無ければ 0。
	Next int64
}

// AuditArg は詳細に出す引数 1 つ（キーと文字列にした値）。
type AuditArg struct{ Key, Value string }

// AuditDetail は監査ログ 1 件の詳細。生の detail_json は返さず（DetailJSON は空）、表示用の Args にする。
type AuditDetail struct {
	AuditEntry
	Args []AuditArg // キーの昇順
}

// auditRedacted は秘密を指すキーの値・トークンの形の文字列に入れる伏せ字。
const auditRedacted = "[REDACTED]"

// auditMaxValueRunes は詳細に出す 1 つの値の最大の文字数（超えた分は切って … を付ける）。
const auditMaxValueRunes = 500

// 秘密の安全網（NFR-S13）。トークン・API キー・type やクリップボードの本文は元から detail に入れない（FR-AU03）が、
// 入れてしまったときのために、読み出し側でも秘密を指すキーの値とトークンの形の文字列を伏せる。app.LogReader の伏せ字と同じ考え方。
var (
	auditTokenPattern   = regexp.MustCompile(`prm_[A-Za-z0-9_-]{8,}`)
	auditHexHashPattern = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
	auditBearerPattern  = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{20,}`)
)

// auditSensitiveKeyParts は、キー名にこれを含む値を丸ごと伏せる（小文字で比較）。token_id・task_id のような ID は含まない。
var auditSensitiveKeyParts = []string{
	"secret", "password", "passwd", "apikey", "api_key", "api-key", "authorization", "plaintext", "hash",
}

// auditBodyKeys は type・クリップボードの本文を指すキー（FR-AU03。完全一致か `_` 付きの接尾辞。文字数の `text_len` は伏せない）。
var auditBodyKeys = []string{"text", "content", "clipboard", "body"}

func auditSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if k == "token" || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "-token") {
		return true
	}
	for _, p := range auditSensitiveKeyParts {
		if strings.Contains(k, p) {
			return true
		}
	}
	for _, b := range auditBodyKeys {
		if k == b || strings.HasSuffix(k, "_"+b) {
			return true
		}
	}
	return false
}

func auditRedactString(s string) string {
	s = auditBearerPattern.ReplaceAllString(s, "Bearer "+auditRedacted)
	s = auditTokenPattern.ReplaceAllString(s, auditRedacted)
	return auditHexHashPattern.ReplaceAllString(s, auditRedacted)
}

// AuditReader は監査ログの閲覧の窓口（読み取り専用。FR-AU05）。GUI（web/handler/gui）が使う。
// 秘密を指すキーの値は読み出し側でも伏せる。
type AuditReader struct {
	store  repository.Audit
	thumbs repository.ThumbStore
}

// NewAuditReader は AuditReader を作る。
func NewAuditReader(store repository.Audit, thumbs repository.ThumbStore) *AuditReader {
	return &AuditReader{store: store, thumbs: thumbs}
}

// Query は条件に合う監査ログを新しい順に 1 ページ返す。Cursor が負なら domain.ErrInvalidArgument。
func (r *AuditReader) Query(ctx context.Context, q AuditQuery) (AuditPage, error) {
	entries, next, err := r.store.Query(ctx, q)
	if err != nil {
		return AuditPage{}, err
	}
	return AuditPage{Entries: entries, Next: next}, nil
}

// Get は 1 件の詳細（引数・サムネイルのメタを含む）。無ければ ErrNotFound。
func (r *AuditReader) Get(ctx context.Context, id int64) (AuditDetail, error) {
	e, err := r.store.Get(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return AuditDetail{}, errNotFound(fmt.Sprintf("audit log %d", id))
	}
	if err != nil {
		return AuditDetail{}, err
	}
	args := auditArgs(e.DetailJSON)
	e.DetailJSON = nil
	return AuditDetail{AuditEntry: e, Args: args}, nil
}

// Thumbnail は監査ログ id のサムネイル（JPEG）を開く。ログが無い・サムネイルが無い・ファイルが無いときは ErrNotFound。
// 呼び出し側が Close する。パスは thumbs/ 配下の相対パスだけが通る（ThumbStore が走査を拒否する）。
func (r *AuditReader) Thumbnail(ctx context.Context, id int64) (io.ReadCloser, error) {
	e, err := r.store.Get(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, errNotFound(fmt.Sprintf("audit log %d", id))
	}
	if err != nil {
		return nil, err
	}
	if e.Thumb == nil {
		return nil, errNotFound(fmt.Sprintf("thumbnail of audit log %d", id))
	}
	rc, err := r.thumbs.Open(e.Thumb.RelPath)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, errNotFound(fmt.Sprintf("thumbnail of audit log %d", id))
	}
	return rc, err
}

// auditArgs は detail_json を表示用のキー・値にする。オブジェクトはトップレベルのキーごと（キーの昇順）、
// それ以外の JSON は key = "detail" の 1 つにする。入れ子の値は秘密を伏せたうえで JSON の文字列にする。
func auditArgs(raw json.RawMessage) []AuditArg {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 整数を浮動小数にして桁をずらさない
	var v any
	if err := dec.Decode(&v); err != nil {
		return []AuditArg{{Key: "detail", Value: auditRedacted}} // 壊れた JSON は中身を出さない
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return []AuditArg{{Key: "detail", Value: auditValueString(redactAuditValue(v))}}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	args := make([]AuditArg, 0, len(keys))
	for _, k := range keys {
		val := auditRedacted
		if !auditSensitiveKey(k) {
			val = auditValueString(redactAuditValue(obj[k]))
		}
		args = append(args, AuditArg{Key: k, Value: val})
	}
	return args
}

// redactAuditValue は入れ子の値の、秘密のキーの値とトークンの形の文字列を伏せる。
func redactAuditValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if auditSensitiveKey(k) {
				out[k] = auditRedacted
			} else {
				out[k] = redactAuditValue(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redactAuditValue(val)
		}
		return out
	case string:
		return auditRedactString(x)
	}
	return v
}

// auditValueString は値を表示用の文字列にする（文字列はそのまま、null は空、それ以外は JSON）。長い値は切る。
func auditValueString(v any) string {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case nil:
		s = ""
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(x); err != nil {
			return auditRedacted
		}
		s = strings.TrimSuffix(buf.String(), "\n")
	}
	if r := []rune(s); len(r) > auditMaxValueRunes {
		s = string(r[:auditMaxValueRunes]) + "…"
	}
	return s
}
