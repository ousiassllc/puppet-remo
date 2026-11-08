package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestOpenCreatesFullSchema(t *testing.T) {
	db, _ := openTestDB(t)

	for _, table := range []string{
		"clients", "client_tokens", "connection_requests", "settings",
		"agent_tasks", "audit_logs", "audit_thumbnails", "launch_denylist", "launchable_apps", "schema_migrations",
	} {
		if n := count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); n != 1 {
			t.Errorf("table %s: got %d, want 1", table, n)
		}
	}
	for _, index := range []string{
		"ux_connreq_pending_node", "ix_connreq_status_expires", "ix_connreq_first_seen",
		"ix_task_created", "ix_task_client_created", "ix_task_active",
		"ix_audit_ts", "ix_audit_client_ts", "ix_audit_kind_ts", "ix_audit_result_ts",
		"ix_audit_session", "ix_audit_task_step", "ix_thumb_created",
	} {
		if n := count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index); n != 1 {
			t.Errorf("index %s: got %d, want 1", index, n)
		}
	}
	if v := count(t, db, `SELECT MAX(version) FROM schema_migrations`); v != 6 {
		t.Errorf("schema version = %d, want 6", v)
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
	if n := count(t, db, `SELECT COUNT(*) FROM schema_migrations`); n != 6 {
		t.Errorf("schema_migrations rows = %d, want 6 (reopen must not re-apply)", n)
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

// M1 の DB（v1 だけ適用済み）を Open すると v2（launchable_apps）が足され、既存の行は残る。
func TestOpenUpgradesM1Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")

	full, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	v1Body, err := fs.ReadFile(full, "0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	if err := migrate(ctx, conn, migrationsFS(map[string]string{"0001_init.sql": string(v1Body)}), newFakeClock().Now); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES ('c1', 'node1', 'a@example.com', 'dev', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'launchable_apps'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the M1 database already has launchable_apps: n=%d err=%v", n, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open M1 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if v := count(t, db, `SELECT MAX(version) FROM schema_migrations`); v != 6 {
		t.Errorf("schema version = %d, want 6", v)
	}
	if got, err := db.Clients().Get(ctx, "c1"); err != nil || got.StableNodeID != "node1" {
		t.Errorf("existing client after upgrade = %+v, %v", got, err)
	}
	app := domain.LaunchableApp{ID: "a1", Label: "メモ帳", ExePath: `C:\Windows\notepad.exe`}
	if err := db.LaunchableApps().Create(ctx, app, domain.MaxLaunchableApps); err != nil {
		t.Errorf("Create on upgraded database: %v", err)
	}
}

// v2 の DB（接続要求が許可・拒否・未処理で残っている）を Open すると v3（履歴の索引）が足され、既存の行は 1 行も変わらず、
// 処理済みの要求（結果と処理日時）が履歴に出る。
func TestOpenUpgradesV2DatabaseKeepsDecidedConnRequests(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")

	full, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, name := range []string{"0001_init.sql", "0002_launchable_apps.sql"} {
		body, err := fs.ReadFile(full, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(body)
	}
	conn, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	if err := migrate(ctx, conn, migrationsFS(files), newFakeClock().Now); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
			VALUES ('c1', 'nodeA', 'a@example.com', 'dev-a', 1, 1)`,
		`INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip, status,
			first_seen_at, last_seen_at, expires_at, decided_at, client_id)
			VALUES ('r1', 'nodeA', 'a@example.com', 'dev-a', '100.64.0.1', 'approved', 1000, 1000, 9000, 2000, 'c1')`,
		`INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip, status,
			first_seen_at, last_seen_at, expires_at, decided_at)
			VALUES ('r2', 'nodeB', 'b@example.com', 'dev-b', '100.64.0.2', 'denied', 3000, 3000, 9000, 4000)`,
		`INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip, status,
			first_seen_at, last_seen_at, expires_at)
			VALUES ('r3', 'nodeC', 'c@example.com', 'dev-c', '100.64.0.3', 'pending', 5000, 5000, 9000)`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if v := count(t, db, `SELECT MAX(version) FROM schema_migrations`); v != 6 {
		t.Errorf("schema version = %d, want 6", v)
	}
	got, err := db.ConnRequests().ListHistory(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids, statuses []string
	for _, r := range got {
		ids, statuses = append(ids, r.ID), append(statuses, r.Status)
	}
	if want := []string{"r3", "r2", "r1"}; !slices.Equal(ids, want) {
		t.Fatalf("history ids = %v, want %v (newest first)", ids, want)
	}
	if want := []string{ConnPending, ConnDenied, ConnApproved}; !slices.Equal(statuses, want) {
		t.Errorf("history statuses = %v, want %v", statuses, want)
	}
	if r := got[2]; r.DecidedAt == nil || r.DecidedAt.UnixMilli() != 2000 || r.ClientID == nil || *r.ClientID != "c1" {
		t.Errorf("approved row after upgrade = %+v", r)
	}
	if r := got[1]; r.DecidedAt == nil || r.DecidedAt.UnixMilli() != 4000 {
		t.Errorf("denied row after upgrade = %+v", r)
	}
	if r := got[0]; r.DecidedAt != nil {
		t.Errorf("pending row has decided_at = %v", r.DecidedAt)
	}
}

// v3 の DB（M1・M2 の間に登録された既存のクライアント。承認モードは DB の既定値 'always'）を Open すると v4 で 'auto'（自動許可）に
// 移り、v4 の後に登録した新しいクライアントの既定は 'always'（常時承認）のまま。M3 で選んだ 'first_time' は移行で変えない。
func TestOpenMigratesExistingClientsToAutoApproval(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")

	full, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, name := range []string{"0001_init.sql", "0002_launchable_apps.sql", "0003_connreq_history_index.sql"} {
		body, err := fs.ReadFile(full, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(body)
	}
	conn, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	if err := migrate(ctx, conn, migrationsFS(files), newFakeClock().Now); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		// M1・M2 で登録された行（approval_mode を指定しない = 既定値 'always'）
		`INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
			VALUES ('old1', 'nodeA', 'a@example.com', 'dev-a', 1, 1)`,
		`INSERT INTO clients (id, stable_node_id, login_name, display_name, approval_mode, created_at, updated_at)
			VALUES ('old2', 'nodeB', 'b@example.com', 'dev-b', 'always', 1, 1)`,
		// 移行の前に 'first_time' を選んでいた行は、選んだ値を保つ
		`INSERT INTO clients (id, stable_node_id, login_name, display_name, approval_mode, created_at, updated_at)
			VALUES ('ft', 'nodeC', 'c@example.com', 'dev-c', 'first_time', 1, 1)`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open v3 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	for id, want := range map[string]domain.ApprovalMode{
		"old1": domain.ApprovalAuto, "old2": domain.ApprovalAuto, "ft": domain.ApprovalFirstTime,
	} {
		c, err := db.Clients().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if c.ApprovalMode != want {
			t.Errorf("client %s approval_mode = %q, want %q", id, c.ApprovalMode, want)
		}
	}

	// 移行後に登録した新しいクライアントの既定は always（承認モードを選ばなければ常時承認）。
	if err := db.Clients().Create(ctx, domain.Client{ID: "new1", StableNodeID: "nodeD", LoginName: "d@example.com", DisplayName: "dev-d"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES ('new2', 'nodeE', 'e@example.com', 'dev-e', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"new1", "new2"} {
		c, err := db.Clients().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if c.ApprovalMode != domain.ApprovalAlways {
			t.Errorf("new client %s approval_mode = %q, want always", id, c.ApprovalMode)
		}
	}

	// 再び開いても、新しい行（と、のちに always へ戻した行）は書き換わらない。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	if c, err := db2.Clients().Get(ctx, "new1"); err != nil || c.ApprovalMode != domain.ApprovalAlways {
		t.Errorf("new client after reopen = %+v, %v; want always", c, err)
	}
}

// v5 の DB（agent_tasks に行がある）を Open すると 0006 が適用され、既存の行は対象なし・capture_partial = 0 のまま読める。
func TestOpenUpgradesV5DatabaseKeepsAgentTasks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	full, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, name := range []string{"0001_init.sql", "0002_launchable_apps.sql", "0003_connreq_history_index.sql",
		"0004_existing_clients_auto_approval.sql", "0005_launch_denylist.sql"} {
		body, err := fs.ReadFile(full, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(body)
	}
	conn, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	if err := migrate(ctx, conn, migrationsFS(files), newFakeClock().Now); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO agent_tasks (id, client_label, prompt, status, model, max_steps, max_duration_sec, created_at)
		VALUES ('old-task', 'dev', 'p', 'running', 'm', 10, 60, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open v5 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if v := count(t, db, `SELECT MAX(version) FROM schema_migrations`); v != 6 {
		t.Errorf("schema version = %d, want 6", v)
	}
	got, err := db.Tasks().Get(ctx, "old-task")
	if err != nil {
		t.Fatal(err)
	}
	if got.Scoped() || got.TargetProcess != "" || got.TargetPID != 0 || got.CapturePartial {
		t.Errorf("old task after upgrade = %+v", got)
	}
}
