package repository

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestOpenCreatesFullSchema(t *testing.T) {
	db, _ := openTestDB(t)

	for _, table := range []string{
		"clients", "client_tokens", "connection_requests", "settings",
		"agent_tasks", "audit_logs", "audit_thumbnails", "schema_migrations",
	} {
		if n := count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); n != 1 {
			t.Errorf("table %s: got %d, want 1", table, n)
		}
	}
	for _, index := range []string{
		"ux_connreq_pending_node", "ix_connreq_status_expires",
		"ix_task_created", "ix_task_client_created", "ix_task_active",
		"ix_audit_ts", "ix_audit_client_ts", "ix_audit_kind_ts", "ix_audit_result_ts",
		"ix_audit_session", "ix_audit_task_step", "ix_thumb_created",
	} {
		if n := count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index); n != 1 {
			t.Errorf("index %s: got %d, want 1", index, n)
		}
	}
	if v := count(t, db, `SELECT MAX(version) FROM schema_migrations`); v != 1 {
		t.Errorf("schema version = %d, want 1", v)
	}
}

func TestOpenSetsPragmas(t *testing.T) {
	db, _ := openTestDB(t)

	var journal string
	if err := db.sql.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	for pragma, want := range map[string]int{
		"foreign_keys": 1,
		"busy_timeout": 5000,
		"synchronous":  1, // NORMAL
	} {
		if got := count(t, db, "PRAGMA "+pragma); got != want {
			t.Errorf("PRAGMA %s = %d, want %d", pragma, got, want)
		}
	}
}

func TestOpenIsIdempotentAndKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "puppet-remo.db") // 親ディレクトリも作る
	ctx := context.Background()

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Settings().Set(ctx, "server.port", []byte("8765")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db.Close() }()
	if n := count(t, db, `SELECT COUNT(*) FROM schema_migrations`); n != 1 {
		t.Errorf("schema_migrations rows = %d, want 1 (reopen must not re-apply)", n)
	}
	if v, ok, err := db.Settings().Get(ctx, "server.port"); err != nil || !ok || string(v) != "8765" {
		t.Errorf("setting after reopen = %q, %v, %v", v, ok, err)
	}
	if db.QuarantinedPath() != "" {
		t.Errorf("healthy database was quarantined: %s", db.QuarantinedPath())
	}
}

func TestOpenQuarantinesCorruptDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "puppet-remo.db")
	garbage := []byte(strings.Repeat("this is not a sqlite database ", 20))
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()

	db, err := Open(context.Background(), path, WithClock(clock))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	q := db.QuarantinedPath()
	if !strings.HasPrefix(q, path+".corrupt-") {
		t.Fatalf("QuarantinedPath = %q", q)
	}
	if got, err := os.ReadFile(q); err != nil || string(got) != string(garbage) {
		t.Errorf("quarantined file not preserved: err=%v", err)
	}
	// 空の DB として使える。
	if n := count(t, db, `SELECT COUNT(*) FROM clients`); n != 0 {
		t.Errorf("fresh database has %d clients", n)
	}
}

func migrationsFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func appliedVersions(t *testing.T, conn *sql.DB) []int {
	t.Helper()
	rows, err := conn.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func TestMigrateAppliesPendingVersionsInOrder(t *testing.T) {
	ctx := context.Background()
	conn := openRaw(t)
	now := newFakeClock().Now

	v1 := map[string]string{"0001_a.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY); INSERT INTO a VALUES (1);`}
	if err := migrate(ctx, conn, migrationsFS(v1), now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, conn, migrationsFS(v1), now); err != nil { // 冪等: v1 を再適用しない（再適用すれば失敗する）
		t.Fatalf("second migrate: %v", err)
	}

	v2 := map[string]string{
		"0002_b.sql": `CREATE TABLE b (id INTEGER PRIMARY KEY); INSERT INTO a VALUES (2);`,
		"0001_a.sql": v1["0001_a.sql"],
	}
	if err := migrate(ctx, conn, migrationsFS(v2), now); err != nil {
		t.Fatalf("migrate v2: %v", err)
	}
	if got := appliedVersions(t, conn); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("applied versions = %v, want [1 2]", got)
	}
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM a`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows in a = %d, %v; want 2 (v1 must run once, v2 once)", n, err)
	}
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	ctx := context.Background()
	conn := openRaw(t)

	fsys := migrationsFS(map[string]string{
		"0001_ok.sql":  `CREATE TABLE ok (id INTEGER PRIMARY KEY);`,
		"0002_bad.sql": `CREATE TABLE half (id INTEGER PRIMARY KEY); THIS IS NOT SQL;`,
	})
	err := migrate(ctx, conn, fsys, newFakeClock().Now)
	if err == nil || !strings.Contains(err.Error(), "0002_bad") {
		t.Fatalf("err = %v, want failure naming 0002_bad", err)
	}
	if got := appliedVersions(t, conn); len(got) != 1 || got[0] != 1 {
		t.Errorf("applied versions = %v, want [1]", got)
	}
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'half'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("partially applied table 'half' survived: n=%d err=%v", n, err)
	}
}

func TestMigrateRejectsNewerDatabase(t *testing.T) {
	ctx := context.Background()
	conn := openRaw(t)
	now := newFakeClock().Now
	both := migrationsFS(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
		"0002_b.sql": `CREATE TABLE b (id INTEGER);`,
	})
	if err := migrate(ctx, conn, both, now); err != nil {
		t.Fatal(err)
	}
	older := migrationsFS(map[string]string{"0001_a.sql": `CREATE TABLE a (id INTEGER);`})
	if err := migrate(ctx, conn, older, now); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("err = %v, want 'newer than supported'", err)
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		ok    bool
	}{
		{"valid", map[string]string{"0001_init.sql": "", "0002_more.sql": ""}, true},
		{"gap", map[string]string{"0001_init.sql": "", "0003_more.sql": ""}, false},
		{"starts at 2", map[string]string{"0002_init.sql": ""}, false},
		{"duplicate version", map[string]string{"0001_a.sql": "", "0001_b.sql": ""}, false},
		{"bad name", map[string]string{"init.sql": ""}, false},
		{"zero version", map[string]string{"0000_init.sql": ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadMigrations(migrationsFS(tt.files))
			if (err == nil) != tt.ok {
				t.Errorf("loadMigrations err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestOpenRecordsMigrationTime(t *testing.T) {
	db, clock := openTestDB(t)
	var at int64
	if err := db.sql.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !time.UnixMilli(at).Equal(clock.Now()) {
		t.Errorf("applied_at = %v, want %v", time.UnixMilli(at), clock.Now())
	}
}
