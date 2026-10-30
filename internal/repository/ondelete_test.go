package repository

import (
	"fmt"
	"strings"
	"testing"
)

// nullCount は column が NULL の行の数を返す。
func nullCount(t *testing.T, db *DB, table, column, where string) int {
	t.Helper()
	return count(t, db, "SELECT COUNT(*) FROM "+table+" WHERE "+column+" IS NULL AND "+where)
}

// クライアントを消したとき: client_tokens は CASCADE、他は SET NULL で履歴（表示名）を残す。
func TestDeleteClientCascadesTokensAndNullsReferences(t *testing.T) {
	db, _ := openTestDB(t)
	mustExec(t, db, `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES ('c1', 'n1', 'l', 'dev1', 1, 1), ('c2', 'n2', 'l', 'dev2', 1, 1)`)
	mustExec(t, db, `INSERT INTO client_tokens (id, client_id, label, token_hash, token_prefix, created_at)
		VALUES ('t1', 'c1', 'a', zeroblob(32), 'p', 1), ('t2', 'c1', 'b', x'`+hex32(1)+`', 'p', 1),
		       ('t3', 'c2', 'a', x'`+hex32(2)+`', 'p', 1)`)
	mustExec(t, db, `INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip,
		status, first_seen_at, last_seen_at, expires_at, client_id)
		VALUES ('r1', 'n1', 'l', 'dev1', 'ip', 'approved', 1, 1, 2, 'c1')`)
	mustExec(t, db, `INSERT INTO agent_tasks (id, client_id, client_label, prompt, model, max_steps, max_duration_sec, created_at)
		VALUES ('k1', 'c1', 'dev1', 'p', 'm', 1, 1, 1)`)
	mustExec(t, db, `INSERT INTO audit_logs (ts, kind, event, actor_type, client_id, client_label, result)
		VALUES (1, 'lease', 'lease.acquire', 'mcp', 'c1', 'dev1', 'ok')`)

	mustExec(t, db, `DELETE FROM clients WHERE id = 'c1'`)

	if n := count(t, db, `SELECT COUNT(*) FROM client_tokens WHERE client_id = 'c1'`); n != 0 {
		t.Errorf("tokens of deleted client = %d, want 0 (CASCADE)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM client_tokens WHERE client_id = 'c2'`); n != 1 {
		t.Errorf("tokens of other client = %d, want 1", n)
	}
	if n := nullCount(t, db, "connection_requests", "client_id", "id = 'r1'"); n != 1 {
		t.Error("connection_requests.client_id was not set to NULL")
	}
	if n := nullCount(t, db, "agent_tasks", "client_id", "id = 'k1'"); n != 1 {
		t.Error("agent_tasks.client_id was not set to NULL")
	}
	if n := nullCount(t, db, "audit_logs", "client_id", "client_label = 'dev1'"); n != 1 {
		t.Error("audit_logs.client_id was not set to NULL (row must remain with its label)")
	}
}

// タスクを消しても監査ログは残り、監査ログを消すとサムネイルの行も消える（削除順序: 親から消せる）。
func TestDeleteTaskAndAuditLogOrdering(t *testing.T) {
	db, _ := openTestDB(t)
	mustExec(t, db, `INSERT INTO agent_tasks (id, client_label, prompt, model, max_steps, max_duration_sec, created_at)
		VALUES ('k1', 'dev', 'p', 'm', 1, 1, 1)`)
	mustExec(t, db, `INSERT INTO audit_logs (ts, kind, event, actor_type, task_id, step_no, client_label, result)
		VALUES (1, 'agent', 'agent.step', 'agent', 'k1', 1, 'dev', 'ok')`)
	mustExec(t, db, `INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
		VALUES (1, 'a.jpg', 10, 320, 180, 1)`)

	mustExec(t, db, `DELETE FROM agent_tasks WHERE id = 'k1'`)
	if n := nullCount(t, db, "audit_logs", "task_id", "id = 1"); n != 1 {
		t.Error("audit_logs.task_id was not set to NULL when the task was deleted")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_thumbnails`); n != 1 {
		t.Errorf("thumbnails = %d, want 1 (deleting a task must not remove them)", n)
	}

	mustExec(t, db, `DELETE FROM audit_logs WHERE id = 1`)
	if n := count(t, db, `SELECT COUNT(*) FROM audit_thumbnails`); n != 0 {
		t.Errorf("thumbnails = %d, want 0 (CASCADE from audit_logs)", n)
	}
}

// 子が参照していても、親（クライアント・タスク・監査ログ）はどの順でも削除できる。
// 子側の ON DELETE（SET NULL / CASCADE）が働き、外部キーが削除を拒否しないことを確認する。
func TestDeletingParentsNeverBlockedByChildren(t *testing.T) {
	db, _ := openTestDB(t)
	mustExec(t, db, `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES ('c1', 'n1', 'l', 'd', 1, 1)`)
	mustExec(t, db, `INSERT INTO agent_tasks (id, client_id, client_label, prompt, model, max_steps, max_duration_sec, created_at)
		VALUES ('k1', 'c1', 'd', 'p', 'm', 1, 1, 1)`)
	mustExec(t, db, `INSERT INTO audit_logs (ts, kind, event, actor_type, client_id, task_id, client_label, result)
		VALUES (1, 'agent', 'agent.step', 'agent', 'c1', 'k1', 'd', 'ok')`)
	mustExec(t, db, `INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
		VALUES (1, 'a.jpg', 10, 320, 180, 1)`)

	// クライアント → タスク → 監査ログ の順に、依存の向きに関係なく消せる。
	for _, q := range []string{
		`DELETE FROM clients WHERE id = 'c1'`,
		`DELETE FROM agent_tasks WHERE id = 'k1'`,
		`DELETE FROM audit_logs WHERE id = 1`,
	} {
		mustExec(t, db, q)
	}
	for _, table := range []string{"clients", "agent_tasks", "audit_logs", "audit_thumbnails"} {
		if n := count(t, db, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Errorf("%s has %d rows, want 0", table, n)
		}
	}
}

// foreign_keys が接続に効いている（効いていなければ CASCADE / SET NULL が働かない）。
func TestForeignKeysEnforced(t *testing.T) {
	db, _ := openTestDB(t)
	_, err := db.sql.Exec(`INSERT INTO client_tokens (id, client_id, label, token_hash, token_prefix, created_at)
		VALUES ('t1', 'missing', 'a', zeroblob(32), 'p', 1)`)
	if err == nil {
		t.Fatal("token for a missing client was accepted")
	}
}

// hex32 は 32 バイトの BLOB リテラル用の 16 進文字列（b を 32 回繰り返す）。
func hex32(b byte) string { return strings.Repeat(fmt.Sprintf("%02x", b), 32) }
