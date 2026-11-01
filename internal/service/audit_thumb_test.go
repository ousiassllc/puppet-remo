package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// thumbEvents は Append・Save・AttachThumb・容量の確認・Remove の呼び出し順を残す（複数の偽物で共有する）。
type thumbEvents struct {
	mu  sync.Mutex
	log []string
}

func (e *thumbEvents) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *thumbEvents) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

// thumbAuditStore は AttachThumb を足した ctlAuditStore。
type thumbAuditStore struct {
	*ctlAuditStore
	events    *thumbEvents
	attachErr error
	attached  []repository.ThumbMeta
	ids       []int64
}

func (s *thumbAuditStore) Append(ctx context.Context, e repository.AuditEntry) (int64, error) {
	id, err := s.ctlAuditStore.Append(ctx, e)
	s.events.add("append:" + e.Event)
	return id, err
}

func (s *thumbAuditStore) AttachThumb(_ context.Context, id int64, m repository.ThumbMeta) error {
	s.events.add("attach")
	if s.attachErr != nil {
		return s.attachErr
	}
	s.ids = append(s.ids, id)
	s.attached = append(s.attached, m)
	return nil
}

// thumbFiles は repository.ThumbStore の偽物。
type thumbFiles struct {
	repository.ThumbStore
	events  *thumbEvents
	saveErr error
	saved   map[string][]byte
	removed []string
}

func (f *thumbFiles) Save(_ context.Context, rel string, data []byte) error {
	f.events.add("save")
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.saved == nil {
		f.saved = map[string][]byte{}
	}
	f.saved[rel] = data
	return nil
}

func (f *thumbFiles) Remove(rel string) error {
	f.events.add("remove")
	f.removed = append(f.removed, rel)
	return nil
}

type thumbEnv struct {
	audit    *Audit
	store    *thumbAuditStore
	files    *thumbFiles
	events   *thumbEvents
	settings *ctlSettings
	logs     *bytes.Buffer
	clock    *ctlClock
}

func newThumbEnv(t *testing.T, queue, maxPending int) *thumbEnv {
	t.Helper()
	ev := &thumbEvents{}
	e := &thumbEnv{
		events:   ev,
		store:    &thumbAuditStore{ctlAuditStore: &ctlAuditStore{}, events: ev},
		files:    &thumbFiles{events: ev},
		settings: newCtlSettings(),
		logs:     &bytes.Buffer{},
		clock:    newCtlClock(),
	}
	a, err := NewAudit(AuditConfig{
		Store: e.store, Clock: e.clock, Logger: slog.New(slog.NewTextHandler(e.logs, nil)), QueueSize: queue,
		Thumbs: e.files, IDs: &ctlIDs{}, Settings: e.settings, MaxPendingThumbs: maxPending,
		PurgeOverCap: func(context.Context) error { ev.add("purge"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	e.audit = a
	return e
}

var testThumb = Thumbnail{JPEG: []byte("jpeg-bytes"), Width: 320, Height: 180}

func TestAuditThumbOrderAndMeta(t *testing.T) {
	e := newThumbEnv(t, 0, 0)
	ctx := context.Background()
	ts := time.Date(2026, 10, 28, 23, 59, 0, 0, time.UTC)
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Kind: "tool", Event: "tool.left_click", Result: "ok", TS: ts}, testThumb)
	if err := e.audit.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(e.events.get(), ","), "append:tool.left_click,save,attach,purge"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if len(e.store.attached) != 1 {
		t.Fatalf("attached = %+v", e.store.attached)
	}
	m := e.store.attached[0]
	if m.RelPath != "2026/10/28/lease-1.jpg" || m.SizeBytes != int64(len(testThumb.JPEG)) || m.Width != 320 || m.Height != 180 {
		t.Fatalf("meta = %+v", m)
	}
	if e.store.ids[0] != 1 {
		t.Fatalf("attached to id %d, want the appended row 1", e.store.ids[0])
	}
	if string(e.files.saved[m.RelPath]) != string(testThumb.JPEG) {
		t.Fatalf("saved = %v", e.files.saved)
	}
	if n := e.audit.thumbs.pending.Load(); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestAuditThumbSaveFailureKeepsTheLogRow(t *testing.T) {
	e := newThumbEnv(t, 0, 0)
	e.files.saveErr = errors.New("disk full")
	ctx := context.Background()
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "tool.key", Result: "ok"}, testThumb)
	if err := e.audit.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.store.Entries(); len(got) != 1 || got[0].Event != "tool.key" {
		t.Fatalf("entries = %+v", got)
	}
	if got, want := strings.Join(e.events.get(), ","), "append:tool.key,save"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if !strings.Contains(e.logs.String(), "audit thumbnail save failed") {
		t.Fatalf("no log line: %s", e.logs.String())
	}
}

func TestAuditThumbAttachFailureRemovesTheFile(t *testing.T) {
	e := newThumbEnv(t, 0, 0)
	e.store.attachErr = errors.New("conflict")
	ctx := context.Background()
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "tool.key", Result: "ok"}, testThumb)
	if err := e.audit.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(e.events.get(), ","), "append:tool.key,save,attach,remove"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if len(e.files.removed) != 1 || !strings.HasSuffix(e.files.removed[0], ".jpg") {
		t.Fatalf("removed = %v", e.files.removed)
	}
	if !strings.Contains(e.logs.String(), "audit thumbnail attach failed") {
		t.Fatalf("no log line: %s", e.logs.String())
	}
}

func TestAuditThumbAppendFailureSkipsTheThumbnail(t *testing.T) {
	e := newThumbEnv(t, 0, 0)
	e.store.err = errors.New("db locked")
	ctx := context.Background()
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "tool.key", Result: "ok"}, testThumb)
	if err := e.audit.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(e.events.get(), ","), "append:tool.key"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if n := e.audit.thumbs.pending.Load(); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestAuditThumbDisabledSetting(t *testing.T) {
	e := newThumbEnv(t, 0, 0)
	ctx := context.Background()
	if !e.audit.ThumbnailsEnabled(ctx) {
		t.Fatal("thumbnails must be enabled by default")
	}
	e.settings.put(settingAuditThumbnailsEnabled, "false")
	if e.audit.ThumbnailsEnabled(ctx) {
		t.Fatal("audit.thumbnails_enabled=false must disable")
	}
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "tool.key", Result: "ok"}, testThumb)
	if err := e.audit.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(e.events.get(), ","), "append:tool.key"; got != want {
		t.Fatalf("order = %s, want %s（無効なら保存しない）", got, want)
	}
	// 値が真偽値でなければ無効（画面を残さない側）。
	e.settings.put(settingAuditThumbnailsEnabled, `"yes"`)
	if e.audit.ThumbnailsEnabled(ctx) {
		t.Fatal("invalid value must disable")
	}
}

func TestAuditWithoutThumbStoreBehavesLikeRecord(t *testing.T) {
	store := &ctlAuditStore{}
	a := newTestAudit(t, store, 0, nil)
	ctx := context.Background()
	if a.ThumbnailsEnabled(ctx) {
		t.Fatal("no thumb store: thumbnails are unavailable")
	}
	a.RecordWithThumb(ctx, repository.AuditEntry{Event: "tool.key", Result: "ok"}, testThumb)
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := store.Entries(); len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
}

func TestNewAuditThumbsRequireIDs(t *testing.T) {
	_, err := NewAudit(AuditConfig{Store: &ctlAuditStore{}, Clock: newCtlClock(), Thumbs: &thumbFiles{}})
	if err == nil {
		t.Fatal("thumbs without an id generator must be rejected")
	}
}

// サムネイル付きの件数に上限がある。超えた分は画像だけを捨て、エントリは追記する。
func TestAuditThumbPendingLimit(t *testing.T) {
	e := newThumbEnv(t, 8, 1)
	e.store.block = make(chan struct{})
	e.store.entered = make(chan struct{})
	ctx := context.Background()
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "e1", Result: "ok"}, testThumb)
	<-e.store.entered // 書き込み goroutine は e1 の Append で止まっている（枠は e1 が持つ）
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "e2", Result: "ok"}, testThumb)
	if !strings.Contains(e.logs.String(), "audit thumbnail dropped") {
		t.Fatalf("no drop log: %s", e.logs.String())
	}
	close(e.store.block)
	if err := e.audit.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.store.Entries(); len(got) != 2 {
		t.Fatalf("entries = %+v, want both rows", got)
	}
	if len(e.files.saved) != 1 {
		t.Fatalf("saved = %d files, want 1", len(e.files.saved))
	}
	if n := e.audit.thumbs.pending.Load(); n != 0 {
		t.Fatalf("pending = %d", n)
	}
}

// キューが満杯のときと Close 後は、エントリごと捨て、サムネイルの枠を返す。Record は待たない。
func TestAuditThumbQueueFullAndClosedReleaseTheSlot(t *testing.T) {
	e := newThumbEnv(t, 1, 8)
	e.store.block = make(chan struct{})
	e.store.entered = make(chan struct{})
	ctx := context.Background()
	e.audit.Record(ctx, repository.AuditEntry{Event: "e1", Result: "ok"})
	<-e.store.entered
	e.audit.Record(ctx, repository.AuditEntry{Event: "e2", Result: "ok"}) // キューの容量 1 を埋める
	done := make(chan struct{})
	go func() {
		e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "e3", Result: "ok"}, testThumb)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RecordWithThumb blocked on a full queue")
	}
	if n := e.audit.thumbs.pending.Load(); n != 0 {
		t.Fatalf("pending = %d after a dropped entry, want 0", n)
	}
	if e.audit.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", e.audit.Dropped())
	}
	close(e.store.block)
	if err := e.audit.Close(ctx); err != nil {
		t.Fatal(err)
	}
	e.audit.RecordWithThumb(ctx, repository.AuditEntry{Event: "e4", Result: "ok"}, testThumb)
	if n := e.audit.thumbs.pending.Load(); n != 0 {
		t.Fatalf("pending = %d after close, want 0", n)
	}
	if e.audit.Dropped() != 2 {
		t.Fatalf("Dropped = %d, want 2", e.audit.Dropped())
	}
	if got := e.store.Entries(); len(got) != 2 {
		t.Fatalf("entries = %+v", got)
	}
}

// 実物の SQLite・ThumbStore・Maintenance で、Append → Save → AttachThumb → 容量の確認が通る。
// 容量の上限（audit.thumbs_max_bytes）を 1 枚分にすると、2 枚目の保存で古い 1 枚目のファイルと行が消え、ログ行は残る。
func TestAuditThumbEndToEndWithRealStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	db, err := repository.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := newCtlClock()
	m, err := NewMaintenance(MaintenanceConfig{ConnRequests: db.ConnRequests(), Audit: db.Audit(), Thumbs: db.Thumbs(), Settings: db.Settings()})
	if err != nil {
		t.Fatal(err)
	}
	th, err := NewThumbnail(gradientImage(800, 450))
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAudit(AuditConfig{
		Store: db.Audit(), Clock: clock, Thumbs: db.Thumbs(), IDs: &ctlIDs{}, Settings: db.Settings(), PurgeOverCap: m.PurgeThumbsOverCap,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(ctx) })

	a.RecordWithThumb(ctx, repository.AuditEntry{Kind: "tool", Event: "tool.left_click", ActorType: domain.ActorMCP, ClientLabel: "dev", Result: "ok"}, th)
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, err := db.Audit().Query(ctx, repository.AuditQuery{})
	if err != nil || len(rows) != 1 || rows[0].Thumb == nil {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	first := rows[0].Thumb
	if first.Width != 320 || first.Height != 180 || first.SizeBytes != int64(len(th.JPEG)) || !strings.HasSuffix(first.RelPath, ".jpg") {
		t.Fatalf("thumb = %+v", first)
	}
	if want := clock.Now().UTC().Format("2006/01/02") + "/"; !strings.HasPrefix(first.RelPath, want) {
		t.Fatalf("rel path %q should start with %q", first.RelPath, want)
	}
	rc, err := db.Thumbs().Open(first.RelPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, th.JPEG) {
		t.Fatal("stored file differs from the encoded thumbnail")
	}

	// 上限を 1 枚分にして、もう 1 枚保存する。
	raw := []byte(strconv.Itoa(len(th.JPEG)))
	if err := db.Settings().Set(ctx, settingAuditThumbsMaxBytes, raw); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	a.RecordWithThumb(ctx, repository.AuditEntry{Kind: "tool", Event: "tool.key", ActorType: domain.ActorMCP, ClientLabel: "dev", Result: "ok"}, th)
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, err = db.Audit().Query(ctx, repository.AuditQuery{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %+v, %v (log rows must remain)", rows, err)
	}
	if rows[0].Thumb == nil || rows[1].Thumb != nil {
		t.Fatalf("newest keeps its thumbnail, oldest loses it: %+v / %+v", rows[0].Thumb, rows[1].Thumb)
	}
	if _, err := db.Thumbs().Open(first.RelPath); err == nil {
		t.Fatal("the oldest thumbnail file should have been removed")
	}
}
