package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

type readerEnv struct {
	reader *AuditReader
	db     *repository.DB
	dir    string
}

func newReaderEnv(t *testing.T) *readerEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	db, err := repository.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &readerEnv{
		reader: NewAuditReader(db.Audit(), db.Thumbs()), db: db,
		dir: filepath.Join(filepath.Dir(path), repository.ThumbsDirName),
	}
}

func (e *readerEnv) add(t *testing.T, detail string, mod func(*repository.AuditEntry)) int64 {
	t.Helper()
	entry := repository.AuditEntry{
		TS: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), Kind: "tool", Event: "tool.type", ActorType: domain.ActorMCP,
		ClientLabel: "dev", Result: "ok",
	}
	if detail != "" {
		entry.DetailJSON = json.RawMessage(detail)
	}
	if mod != nil {
		mod(&entry)
	}
	id, err := e.db.Audit().Append(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func argMap(args []AuditArg) map[string]string {
	m := map[string]string{}
	for _, a := range args {
		m[a.Key] = a.Value
	}
	return m
}

func TestAuditReaderGetShapesDetailAsKeyValues(t *testing.T) {
	e := newReaderEnv(t)
	id := e.add(t, `{"x":10,"y":20,"label":"メモ帳","ok":true,"none":null,"big":9007199254740993,"keys":["ctrl","a"],"nest":{"a":"<b>&"}}`, nil)

	d, err := e.reader.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if d.ID != id || d.Event != "tool.type" || d.ClientLabel != "dev" {
		t.Errorf("entry fields = %+v", d.AuditEntry)
	}
	if d.DetailJSON != nil {
		t.Errorf("raw detail_json must not be returned: %s", d.DetailJSON)
	}
	want := []AuditArg{
		{"big", "9007199254740993"}, {"keys", `["ctrl","a"]`}, {"label", "メモ帳"}, {"nest", `{"a":"<b>&"}`},
		{"none", ""}, {"ok", "true"}, {"x", "10"}, {"y", "20"},
	}
	if !slices.Equal(d.Args, want) {
		t.Errorf("Args = %v, want %v (key order, strings as-is, integers exact, nested as JSON)", d.Args, want)
	}
}

func TestAuditReaderGetWithoutDetail(t *testing.T) {
	e := newReaderEnv(t)
	id := e.add(t, "", nil)
	d, err := e.reader.Get(context.Background(), id)
	if err != nil || len(d.Args) != 0 {
		t.Errorf("Get = %+v, %v; want no args", d, err)
	}
	if _, err := e.reader.Get(context.Background(), id+1); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) err = %v, want ErrNotFound", err)
	}
}

func TestAuditReaderRedactsSecrets(t *testing.T) {
	e := newReaderEnv(t)
	hash := strings.Repeat("ab", 32)
	id := e.add(t, `{
		"token":"prm_abcdefgh12345678", "access_token":"x", "api_key":"sk-123", "Password":"hunter2", "token_hash":"`+hash+`",
		"text":"my secret typing", "clipboard":"copied", "content":"c", "label_text":"t",
		"text_len":16, "token_id":"01HX", "task_id":"t1",
		"note":"auth prm_abcdefgh12345678 end", "hdr":"Bearer abcdefghijklmnopqrstuvwxyz0123", "h":"`+hash+`",
		"nest":{"api_key":"k","ok":"v","list":[{"secret":"s","n":1},"prm_zzzzzzzzzzzz"]}
	}`, nil)
	d, err := e.reader.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	got := argMap(d.Args)
	for _, k := range []string{"token", "access_token", "api_key", "Password", "token_hash", "clipboard", "content", "label_text"} {
		if got[k] != "[REDACTED]" {
			t.Errorf("%s = %q, want [REDACTED]", k, got[k])
		}
	}
	for k, want := range map[string]string{"text_len": "16", "token_id": "01HX", "task_id": "t1", "text": "my secret typing"} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q (IDs, counts and the text recorded by audit.record_text are shown as recorded)", k, got[k], want)
		}
	}
	if got["note"] != "auth [REDACTED] end" || got["hdr"] != "Bearer [REDACTED]" || got["h"] != "[REDACTED]" {
		t.Errorf("string values = %q / %q / %q", got["note"], got["hdr"], got["h"])
	}
	if got["nest"] != `{"api_key":"[REDACTED]","list":[{"n":1,"secret":"[REDACTED]"},"[REDACTED]"],"ok":"v"}` {
		t.Errorf("nest = %s", got["nest"])
	}
	for _, a := range d.Args {
		for _, secret := range []string{"hunter2", "sk-123", "prm_abc", hash, "prm_zzz"} {
			if strings.Contains(a.Value, secret) {
				t.Errorf("arg %s leaks %q: %s", a.Key, secret, a.Value)
			}
		}
	}
}

func TestAuditReaderNonObjectAndLongDetail(t *testing.T) {
	e := newReaderEnv(t)
	arr := e.add(t, `["a","prm_abcdefgh12345678"]`, nil)
	long := e.add(t, `{"v":"`+strings.Repeat("あ", auditMaxValueRunes+10)+`"}`, nil)
	ctx := context.Background()

	if d, _ := e.reader.Get(ctx, arr); !slices.Equal(d.Args, []AuditArg{{"detail", `["a","[REDACTED]"]`}}) {
		t.Errorf("array detail = %v", d.Args)
	}
	d, _ := e.reader.Get(ctx, long)
	if v := d.Args[0].Value; v != strings.Repeat("あ", auditMaxValueRunes)+"…" {
		t.Errorf("long value has %d runes, want it cut to %d + an ellipsis", len([]rune(v)), auditMaxValueRunes)
	}
}

func TestAuditReaderQueryHasNoRawDetailAndPages(t *testing.T) {
	e := newReaderEnv(t)
	for i := range 3 {
		e.add(t, `{"text":"secret"}`, func(en *repository.AuditEntry) { en.TS = en.TS.Add(time.Duration(i) * time.Second) })
	}
	p1, err := e.reader.Query(context.Background(), AuditQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Entries) != 2 || p1.Next != p1.Entries[1].ID {
		t.Fatalf("page 1 = %d entries, next %d", len(p1.Entries), p1.Next)
	}
	for _, en := range p1.Entries {
		if en.DetailJSON != nil {
			t.Errorf("Query must not return detail_json: %s", en.DetailJSON)
		}
	}
	p2, err := e.reader.Query(context.Background(), AuditQuery{Limit: 2, Cursor: p1.Next})
	if err != nil || len(p2.Entries) != 1 || p2.Next != 0 {
		t.Errorf("page 2 = %+v, %v", p2, err)
	}
	if _, err := e.reader.Query(context.Background(), AuditQuery{Cursor: -1}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("negative cursor err = %v", err)
	}
}

func TestAuditReaderThumbnail(t *testing.T) {
	ctx := context.Background()
	e := newReaderEnv(t)
	withThumb := e.add(t, "", nil)
	noThumb := e.add(t, "", nil)
	fileGone := e.add(t, "", nil)
	if err := e.db.Thumbs().Save(ctx, "2026/a.jpg", []byte("JPEGDATA")); err != nil {
		t.Fatal(err)
	}
	for id, rel := range map[int64]string{withThumb: "2026/a.jpg", fileGone: "2026/gone.jpg"} {
		err := e.db.Audit().AttachThumb(ctx, id, repository.ThumbMeta{RelPath: rel, SizeBytes: 8, Width: 4, Height: 3})
		if err != nil {
			t.Fatal(err)
		}
	}

	rc, err := e.reader.Thumbnail(ctx, withThumb)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "JPEGDATA" {
		t.Errorf("thumbnail = %q", b)
	}
	for name, id := range map[string]int64{"no thumbnail": noThumb, "file missing": fileGone, "no such log": fileGone + 100} {
		if rc, err := e.reader.Thumbnail(ctx, id); !errors.Is(err, ErrNotFound) {
			if rc != nil {
				_ = rc.Close()
			}
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	// 行の rel_path が細工されていても thumbs/ の外は読めない。
	outside := filepath.Join(filepath.Dir(e.dir), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	evil := e.add(t, "", nil)
	tampered := NewAuditReader(tamperedAudit{e.db.Audit()}, e.db.Thumbs())
	if rc, err := tampered.Thumbnail(ctx, evil); err == nil {
		_ = rc.Close()
		t.Error("a tampered rel_path outside thumbs/ must not open")
	} else if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("tampered rel_path: err = %v, want ErrInvalidArgument", err)
	}
}

// tamperedAudit は Get の結果のサムネイルの rel_path を thumbs/ の外へ向ける（DB の行が細工された場合の模擬）。
type tamperedAudit struct{ repository.Audit }

func (a tamperedAudit) Get(ctx context.Context, id int64) (repository.AuditEntry, error) {
	e, err := a.Audit.Get(ctx, id)
	e.Thumb = &repository.ThumbMeta{RelPath: "../secret", SizeBytes: 1, Width: 1, Height: 1}
	return e, err
}

// audit.record_text が記録した text は見せるが、本文の中のトークンの形は伏せる。
func TestAuditReaderShowsRecordedTextButRedactsTokensInIt(t *testing.T) {
	e := newReaderEnv(t)
	id := e.add(t, `{"text":"hello prm_abcdefgh12345678 <b>","text_len":30,"text_truncated":true}`, nil)
	d, err := e.reader.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	got := argMap(d.Args)
	if got["text"] != "hello [REDACTED] <b>" || got["text_len"] != "30" || got["text_truncated"] != "true" {
		t.Fatalf("args = %v", got)
	}
}
