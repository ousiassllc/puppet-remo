package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// auditMaint は実 DB（audit_logs・audit_thumbnails・settings・thumbs/）の上の Maintenance。
type auditMaint struct {
	*Maintenance
	db    *repository.DB
	dir   string // thumbs/ の実ディレクトリ
	clock *maintClock
	logs  *bytes.Buffer
}

func newAuditMaint(t *testing.T) *auditMaint {
	t.Helper()
	clock := newMaintClock()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	db, err := repository.Open(context.Background(), path, repository.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logs := &bytes.Buffer{}
	m, err := NewMaintenance(MaintenanceConfig{
		ConnRequests: db.ConnRequests(), Audit: db.Audit(), Thumbs: db.Thumbs(), Settings: db.Settings(),
		Clock: clock, Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &auditMaint{Maintenance: m, db: db, dir: filepath.Join(filepath.Dir(path), repository.ThumbsDirName), clock: clock, logs: logs}
}

// addLog は now から age だけ前の監査ログを追記する。thumb が空でなければサムネイル（ファイルと行）も作る。
func (a *auditMaint) addLog(t *testing.T, age time.Duration, thumb string, size int64) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := a.db.Audit().Append(ctx, repository.AuditEntry{
		TS: a.clock.Now().Add(-age), Kind: "tool", Event: "tool.left_click", ActorType: domain.ActorMCP,
		ClientLabel: "dev", Result: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if thumb != "" {
		if err := a.db.Thumbs().Save(ctx, thumb, []byte("jpeg")); err != nil {
			t.Fatal(err)
		}
		err := a.db.Audit().AttachThumb(ctx, id, repository.ThumbMeta{
			RelPath: thumb, SizeBytes: size, Width: 320, Height: 180, CreatedAt: a.clock.Now().Add(-age),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func (a *auditMaint) setSetting(t *testing.T, key, rawJSON string) {
	t.Helper()
	if err := a.db.Settings().Set(context.Background(), key, json.RawMessage(rawJSON)); err != nil {
		t.Fatal(err)
	}
}

func (a *auditMaint) logIDs(t *testing.T) []int64 {
	t.Helper()
	es, _, err := a.db.Audit().Query(context.Background(), repository.AuditQuery{Limit: repository.MaxAuditLimit})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(es))
	for _, e := range es {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	return ids
}

func (a *auditMaint) thumbFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(a.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(a.dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func (a *auditMaint) thumbRows(t *testing.T) []string {
	t.Helper()
	paths, err := a.db.Audit().ThumbPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	return paths
}

const day = 24 * time.Hour

func TestMaintenanceWithAuditRequiresThumbsAndSettings(t *testing.T) {
	db, _ := openMaintDB(t)
	for name, cfg := range map[string]MaintenanceConfig{
		"no thumbs":   {ConnRequests: db.ConnRequests(), Audit: db.Audit(), Settings: db.Settings()},
		"no settings": {ConnRequests: db.ConnRequests(), Audit: db.Audit(), Thumbs: db.Thumbs()},
	} {
		if _, err := NewMaintenance(cfg); err == nil {
			t.Errorf("%s: NewMaintenance must fail", name)
		}
	}
	// Audit が無ければ Thumbs・Settings は要らない（M1 までの構成）。
	if _, err := NewMaintenance(MaintenanceConfig{ConnRequests: db.ConnRequests()}); err != nil {
		t.Errorf("without Audit: %v", err)
	}
}

func openMaintDB(t *testing.T) (*repository.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	db, err := repository.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestMaintenanceRunOncePurgesAuditPastDefaultRetention(t *testing.T) {
	a := newAuditMaint(t)
	oldWith := a.addLog(t, 31*day, "old/a.jpg", 10)
	oldPlain := a.addLog(t, 40*day, "", 0)
	boundary := a.addLog(t, 30*day, "boundary.jpg", 10) // ts = cutoff ちょうど → 残る（ts < cutoff だけ削除）
	recent := a.addLog(t, day, "recent.jpg", 10)

	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got, want := a.logIDs(t), []int64{boundary, recent}; !slices.Equal(got, want) {
		t.Errorf("remaining logs = %v, want %v (deleted %d, %d)", got, want, oldWith, oldPlain)
	}
	if got, want := a.thumbFiles(t), []string{"boundary.jpg", "recent.jpg"}; !slices.Equal(got, want) {
		t.Errorf("thumb files = %v, want %v", got, want)
	}
	if got, want := a.thumbRows(t), []string{"boundary.jpg", "recent.jpg"}; !slices.Equal(got, want) {
		t.Errorf("thumb rows = %v, want %v", got, want)
	}
}

func TestMaintenanceRetentionDaysSetting(t *testing.T) {
	a := newAuditMaint(t)
	a.setSetting(t, "audit.retention_days", "7")
	old := a.addLog(t, 8*day, "old.jpg", 10)
	recent := a.addLog(t, 6*day, "recent.jpg", 10)
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := a.logIDs(t); !slices.Equal(got, []int64{recent}) {
		t.Errorf("remaining logs = %v, want only %d (not %d)", got, recent, old)
	}
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"recent.jpg"}) {
		t.Errorf("thumb files = %v", got)
	}
}

func TestMaintenanceInvalidSettingsFallBackToDefaultsAndWarn(t *testing.T) {
	for _, raw := range []string{"0", "-3", "1.5", `"7"`, "null", "true", "[1]", "36501", "1e400", `{"a":1}`} {
		t.Run(raw, func(t *testing.T) {
			a := newAuditMaint(t)
			a.setSetting(t, "audit.retention_days", raw)
			a.setSetting(t, "audit.thumbs_max_bytes", raw)
			old := a.addLog(t, 31*day, "old.jpg", 5)
			recent := a.addLog(t, 29*day, "recent.jpg", 5)
			if err := a.RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce must not fail for an invalid setting: %v", err)
			}
			// 保持期間は 30 日、容量は 1 GiB（どちらも既定）で動く。
			if got := a.logIDs(t); !slices.Equal(got, []int64{recent}) {
				t.Errorf("remaining logs = %v, want only %d (not %d)", got, recent, old)
			}
			if got := a.thumbRows(t); !slices.Equal(got, []string{"recent.jpg"}) {
				t.Errorf("thumb rows = %v (the 1 GiB default cap must not delete a 5-byte thumbnail)", got)
			}
			keys := []string{"audit.retention_days", "audit.thumbs_max_bytes"}
			if raw == "36501" {
				keys = keys[:1] // 36501 は保持期間の上限（36500 日）の外だが、容量（バイト）としては有効
			}
			for _, key := range keys {
				if !strings.Contains(a.logs.String(), "level=WARN") || !strings.Contains(a.logs.String(), key) {
					t.Errorf("no warning for %s:\n%s", key, a.logs.String())
				}
			}
		})
	}
}

func TestMaintenanceThumbsOverCapDeletesOldestAndKeepsLogs(t *testing.T) {
	a := newAuditMaint(t)
	// 古い順に a(30 日前) b c d(1 日前)。各 100 バイトで合計 400。
	var logs []int64
	for i, name := range []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg"} {
		logs = append(logs, a.addLog(t, time.Duration(4-i)*day, name, 100))
	}
	a.setSetting(t, "audit.thumbs_max_bytes", "250")
	if err := a.PurgeThumbsOverCap(context.Background()); err != nil {
		t.Fatalf("PurgeThumbsOverCap: %v", err)
	}
	if got := a.thumbRows(t); !slices.Equal(got, []string{"c.jpg", "d.jpg"}) {
		t.Errorf("thumb rows = %v, want [c.jpg d.jpg]", got)
	}
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"c.jpg", "d.jpg"}) {
		t.Errorf("thumb files = %v, want [c.jpg d.jpg]", got)
	}
	if got := a.logIDs(t); !slices.Equal(got, logs) {
		t.Errorf("logs = %v, want all %v kept", got, logs)
	}
	// 2 回目は何もしない。
	if err := a.PurgeThumbsOverCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.thumbRows(t); len(got) != 2 {
		t.Errorf("second call changed the rows: %v", got)
	}
}

func TestMaintenanceThumbsUnderDefaultCapAreKept(t *testing.T) {
	a := newAuditMaint(t)
	a.addLog(t, day, "a.jpg", 50)
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"a.jpg"}) {
		t.Errorf("thumb files = %v", got)
	}
}

func TestMaintenanceWithoutAuditOnlyExpiresConnRequests(t *testing.T) {
	rec := &expireRecorder{calls: make(chan struct{}, 4)}
	m, err := NewMaintenance(MaintenanceConfig{ConnRequests: rec, Clock: newMaintClock(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	recvOrFail(t, rec.calls, "Expire")
	for name, f := range map[string]func(context.Context) error{
		"PurgeAuditBeforeRetention": m.PurgeAuditBeforeRetention, "PurgeThumbsOverCap": m.PurgeThumbsOverCap, "SweepOrphans": m.SweepOrphans,
	} {
		if err := f(ctx); err != nil {
			t.Errorf("%s without Audit: %v", name, err)
		}
	}
}

func TestMaintenanceSweepOrphans(t *testing.T) {
	a := newAuditMaint(t)
	a.addLog(t, day, "keep.jpg", 10)
	lost := a.addLog(t, day, "lost.jpg", 10)
	if err := os.Remove(filepath.Join(a.dir, "lost.jpg")); err != nil { // 行はあるのにファイルが無い
		t.Fatal(err)
	}
	if err := a.db.Thumbs().Save(context.Background(), "orphan/x.jpg", []byte("x")); err != nil { // ファイルはあるのに行が無い
		t.Fatal(err)
	}
	// 実時刻で作ったファイルを猶予の外にする。
	a.clock.Advance(365 * day)

	if err := a.SweepOrphans(context.Background()); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if got := a.thumbRows(t); !slices.Equal(got, []string{"keep.jpg"}) {
		t.Errorf("thumb rows = %v, want [keep.jpg]", got)
	}
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"keep.jpg"}) {
		t.Errorf("thumb files = %v, want [keep.jpg]", got)
	}
	// ログの行は残る（サムネイルだけが消える）。
	if got := a.logIDs(t); len(got) != 2 || !slices.Contains(got, lost) {
		t.Errorf("logs = %v, want both kept", got)
	}
}

func TestMaintenanceSweepOrphansKeepsFreshFiles(t *testing.T) {
	a := newAuditMaint(t)
	// 保存の直後（DB に紐づける前）のファイルは孤児と取り違えない。
	if err := a.db.Thumbs().Save(context.Background(), "fresh.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	a.clock.Advance(-365 * day) // 時計を戻して、更新時刻が「未来」でも猶予の内として扱うことを見る
	if err := a.SweepOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"fresh.jpg"}) {
		t.Errorf("thumb files = %v, want the fresh file kept", got)
	}
}

// failingThumbs は Remove / Open を差し替えられる ThumbStore。
type failingThumbs struct {
	repository.ThumbStore
	removeErr map[string]error
	openErr   error
	removed   []string
}

func (f *failingThumbs) Remove(rel string) error {
	if err := f.removeErr[rel]; err != nil {
		return err
	}
	f.removed = append(f.removed, rel)
	return f.ThumbStore.Remove(rel)
}

func (f *failingThumbs) Open(rel string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.ThumbStore.Open(rel)
}

func (a *auditMaint) withThumbs(t *testing.T, th repository.ThumbStore) *Maintenance {
	t.Helper()
	m, err := NewMaintenance(MaintenanceConfig{
		ConnRequests: a.db.ConnRequests(), Audit: a.db.Audit(), Thumbs: th, Settings: a.db.Settings(),
		Clock: a.clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMaintenanceFileRemovalFailureDoesNotStopOthers(t *testing.T) {
	a := newAuditMaint(t)
	a.addLog(t, 40*day, "a.jpg", 10)
	a.addLog(t, 40*day, "b.jpg", 10)
	a.addLog(t, 40*day, "c.jpg", 10)
	th := &failingThumbs{ThumbStore: a.db.Thumbs(), removeErr: map[string]error{"b.jpg": errors.New("sharing violation")}}

	err := a.withThumbs(t, th).PurgeAuditBeforeRetention(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sharing violation") {
		t.Fatalf("err = %v, want the removal failure reported", err)
	}
	// 行は削除済み。消せなかった 1 ファイルだけが残り（次の起動の孤児掃除で消える）、残りは消えている。
	if got := a.logIDs(t); len(got) != 0 {
		t.Errorf("logs = %v, want all purged", got)
	}
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"b.jpg"}) {
		t.Errorf("thumb files = %v, want only the failed b.jpg", got)
	}
	a.clock.Advance(365 * day)
	if err := a.SweepOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.thumbFiles(t); len(got) != 0 {
		t.Errorf("orphan sweep left %v", got)
	}
}

func TestMaintenanceSweepKeepsRowsWhenFileCannotBeChecked(t *testing.T) {
	a := newAuditMaint(t)
	a.addLog(t, day, "a.jpg", 10)
	th := &failingThumbs{ThumbStore: a.db.Thumbs(), openErr: errors.New("access denied")}
	if err := a.withThumbs(t, th).SweepOrphans(context.Background()); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if got := a.thumbRows(t); !slices.Equal(got, []string{"a.jpg"}) {
		t.Errorf("thumb rows = %v, want the row kept when the file state is unknown", got)
	}
}

// failingSettings は Get を失敗させる repository.Settings。
type failingSettings struct{ repository.Settings }

func (failingSettings) Get(context.Context, string) (json.RawMessage, bool, error) {
	return nil, false, errors.New("database is locked")
}

func TestMaintenanceRunOnceContinuesAfterEachFailure(t *testing.T) {
	a := newAuditMaint(t)
	old := a.addLog(t, 40*day, "old.jpg", 10)
	rec := &expireRecorder{calls: make(chan struct{}, 4), err: errors.New("expire failed")}
	m, err := NewMaintenance(MaintenanceConfig{
		ConnRequests: rec, Audit: a.db.Audit(), Thumbs: a.db.Thumbs(), Settings: a.db.Settings(),
		Clock: a.clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = m.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "expire failed") {
		t.Fatalf("err = %v, want the expire failure", err)
	}
	if got := a.logIDs(t); slices.Contains(got, old) {
		t.Errorf("the audit purge must still run after Expire fails; logs = %v", got)
	}

	// 設定の読み出しに失敗しても Expire は行い、エラーに設定名を含める。
	rec2 := &expireRecorder{calls: make(chan struct{}, 4)}
	m2, err := NewMaintenance(MaintenanceConfig{
		ConnRequests: rec2, Audit: a.db.Audit(), Thumbs: a.db.Thumbs(), Settings: failingSettings{a.db.Settings()},
		Clock: a.clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = m2.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "audit.retention_days") || !strings.Contains(err.Error(), "audit.thumbs_max_bytes") {
		t.Errorf("err = %v, want both settings' read failures", err)
	}
	recvOrFail(t, rec2.calls, "Expire")
}

func TestMaintenanceRunSweepsOrphansBeforeFirstMaintenance(t *testing.T) {
	a := newAuditMaint(t)
	a.clock.Advance(365 * day)
	a.addLog(t, day, "keep.jpg", 10)
	if err := a.db.Thumbs().Save(context.Background(), "orphan.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	// 孤児ファイルを猶予の外にする。
	old := a.clock.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(a.dir, "orphan.jpg"), old, old); err != nil {
		t.Fatal(err)
	}

	rec := &expireRecorder{calls: make(chan struct{}, 8)}
	m, err := NewMaintenance(MaintenanceConfig{
		ConnRequests: rec, Audit: a.db.Audit(), Thumbs: a.db.Thumbs(), Settings: a.db.Settings(),
		Clock: a.clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()

	recvOrFail(t, rec.calls, "the startup maintenance")
	recvOrFail(t, a.clock.waits, "the first wait")
	// 起動時の保守が済んだ時点で孤児は掃除済み。
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"keep.jpg"}) {
		t.Errorf("thumb files after startup = %v, want [keep.jpg]", got)
	}

	// 毎時の保守は孤児の掃除をしない（起動時だけ）。
	if err := a.db.Thumbs().Save(ctx, "late-orphan.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	old = a.clock.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(a.dir, "late-orphan.jpg"), old, old); err != nil {
		t.Fatal(err)
	}
	a.clock.Advance(time.Hour)
	a.clock.fire <- a.clock.Now()
	recvOrFail(t, rec.calls, "the hourly maintenance")
	recvOrFail(t, a.clock.waits, "the second wait")
	if got := a.thumbFiles(t); !slices.Equal(got, []string{"keep.jpg", "late-orphan.jpg"}) {
		t.Errorf("thumb files after the hourly run = %v, want the late orphan left for the next startup", got)
	}
	cancel()
	recvOrFail(t, done, "Run to return")
}
