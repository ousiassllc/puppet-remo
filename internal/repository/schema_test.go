package repository

import (
	"context"
	"strings"
	"testing"
)

// 生の SQL で、er.md の制約が DB に効いていることを確認する。
// 各ケースは setup の後に stmt を 1 つ実行し、wantErr（部分一致。空なら成功）を検査する。
func TestSchemaConstraints(t *testing.T) {
	const (
		client = `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
			VALUES ('c1', 'node1', 'a@example.com', 'dev', 1, 1)`
		token = `INSERT INTO client_tokens (id, client_id, label, token_hash, token_prefix, created_at)
			VALUES ('t1', 'c1', 'claude-code', zeroblob(32), 'prm_xxxx', 1)`
		pendingReq = `INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip,
			first_seen_at, last_seen_at, expires_at) VALUES ('r1', 'nodeX', 'a', 'dev', '100.64.0.1', 1, 1, 2)`
		task = `INSERT INTO agent_tasks (id, client_label, prompt, model, max_steps, max_duration_sec, created_at)
			VALUES ('k1', 'dev', 'p', 'm', 50, 600, 1)`
		app = `INSERT INTO launchable_apps (id, label, exe_path, created_at, updated_at)
			VALUES ('a1', 'Notepad', 'C:/Windows/notepad.exe', 1, 1)`
		audit = `INSERT INTO audit_logs (ts, kind, event, actor_type, client_label, result)
			VALUES (1, 'lease', 'lease.acquire', 'mcp', 'dev', 'ok')`
	)
	tests := []struct {
		name    string
		setup   []string
		stmt    string
		wantErr string
	}{
		{"client ok", nil, client, ""},
		{"client duplicate stable_node_id", []string{client},
			strings.Replace(client, "'c1'", "'c2'", 1), "UNIQUE constraint failed"},
		{"client bad approval_mode", nil,
			`INSERT INTO clients (id, stable_node_id, login_name, display_name, approval_mode, created_at, updated_at)
			VALUES ('c1', 'n', 'l', 'd', 'sometimes', 1, 1)`, "CHECK constraint failed"},
		{"client web_enabled out of range", nil,
			`INSERT INTO clients (id, stable_node_id, login_name, display_name, web_enabled, created_at, updated_at)
			VALUES ('c1', 'n', 'l', 'd', 2, 1, 1)`, "CHECK constraint failed"},

		{"token ok", []string{client}, token, ""},
		{"token needs existing client", nil, token, "FOREIGN KEY constraint failed"},
		{"token duplicate label in client", []string{client, token},
			strings.Replace(token, "'t1'", "'t2'", 1), "UNIQUE constraint failed"},
		{"token duplicate hash", []string{client, token},
			strings.Replace(strings.Replace(token, "'t1'", "'t2'", 1), "'claude-code'", "'other'", 1), "UNIQUE constraint failed"},
		{"token hash must be 32 bytes", []string{client},
			strings.Replace(token, "zeroblob(32)", "zeroblob(16)", 1), "CHECK constraint failed"},

		{"connreq ok", nil, pendingReq, ""},
		{"connreq second pending for same node", []string{pendingReq},
			strings.Replace(pendingReq, "'r1'", "'r2'", 1), "UNIQUE constraint failed"},
		{"connreq pending for other node", []string{pendingReq},
			strings.Replace(strings.Replace(pendingReq, "'r1'", "'r2'", 1), "'nodeX'", "'nodeY'", 1), ""},
		{"connreq denied alongside pending", []string{pendingReq},
			`INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip,
				status, first_seen_at, last_seen_at, expires_at) VALUES ('r2', 'nodeX', 'a', 'd', 'ip', 'denied', 1, 1, 2)`, ""},
		{"connreq unknown status", nil,
			`INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip,
				status, first_seen_at, last_seen_at, expires_at) VALUES ('r2', 'n', 'a', 'd', 'ip', 'bogus', 1, 1, 2)`, "CHECK constraint failed"},

		{"settings valid json", nil, `INSERT INTO settings VALUES ('k', '{"a":1}', 1)`, ""},
		{"settings invalid json", nil, `INSERT INTO settings VALUES ('k', '{oops', 1)`, "CHECK constraint failed"},

		{"audit ok", nil, audit, ""},
		{"audit bad result", nil, strings.Replace(audit, "'ok'", "'maybe'", 1), "CHECK constraint failed"},
		{"audit bad actor_type", nil, strings.Replace(audit, "'mcp'", "'robot'", 1), "CHECK constraint failed"},
		{"audit invalid detail_json", nil,
			`INSERT INTO audit_logs (ts, kind, event, actor_type, client_label, result, detail_json)
			VALUES (1, 'lease', 'e', 'mcp', 'dev', 'ok', '{oops')`, "CHECK constraint failed"},
		{"audit unknown client", nil,
			`INSERT INTO audit_logs (ts, kind, event, actor_type, client_id, client_label, result)
			VALUES (1, 'lease', 'e', 'mcp', 'nope', 'dev', 'ok')`, "FOREIGN KEY constraint failed"},

		{"app ok", nil, app, ""},
		{"app label unique ignoring ASCII case", []string{app},
			strings.Replace(strings.Replace(app, "'a1'", "'a2'", 1), "'Notepad'", "'NOTEPAD'", 1), "UNIQUE constraint failed"},
		{"app label differs by full-width letters", []string{app},
			strings.Replace(strings.Replace(app, "'a1'", "'a2'", 1), "'Notepad'", "'Ｎotepad'", 1), ""},
		{"app label differs by non-ASCII case", []string{`INSERT INTO launchable_apps (id, label, exe_path, created_at, updated_at)
			VALUES ('a1', 'ÄBC', 'C:/a.exe', 1, 1)`},
			`INSERT INTO launchable_apps (id, label, exe_path, created_at, updated_at)
			VALUES ('a2', 'äbc', 'C:/a.exe', 1, 1)`, ""},
		{"app duplicate id", []string{app},
			strings.Replace(app, "'Notepad'", "'Other'", 1), "UNIQUE constraint failed"},
		{"app empty label", nil, strings.Replace(app, "'Notepad'", "''", 1), "CHECK constraint failed"},
		{"app label of 65 chars", nil,
			strings.Replace(app, "'Notepad'", "'"+strings.Repeat("a", 65)+"'", 1), "CHECK constraint failed"},
		{"app label of 64 chars", nil,
			strings.Replace(app, "'Notepad'", "'"+strings.Repeat("a", 64)+"'", 1), ""},
		{"app empty exe_path", nil, strings.Replace(app, "'C:/Windows/notepad.exe'", "''", 1), "CHECK constraint failed"},
		{"app without optional columns", nil, `INSERT INTO launchable_apps (id, label, exe_path, created_at, updated_at)
			VALUES ('a1', 'x', 'C:/a.exe', 1, 1)`, ""},
		{"app invalid args_json", nil,
			`INSERT INTO launchable_apps (id, label, exe_path, args_json, created_at, updated_at)
			VALUES ('a1', 'x', 'C:/a.exe', '[oops', 1, 1)`, "CHECK constraint failed"},
		{"app args_json must be an array", nil,
			`INSERT INTO launchable_apps (id, label, exe_path, args_json, created_at, updated_at)
			VALUES ('a1', 'x', 'C:/a.exe', '{"a":1}', 1, 1)`, "CHECK constraint failed"},
		{"app args_json array", nil,
			`INSERT INTO launchable_apps (id, label, exe_path, args_json, created_at, updated_at)
			VALUES ('a1', 'x', 'C:/a.exe', '["a","b"]', 1, 1)`, ""},

		{"task ok", nil, task, ""},
		{"task unknown status", nil,
			`INSERT INTO agent_tasks (id, client_label, prompt, status, model, max_steps, max_duration_sec, created_at, finished_at)
			VALUES ('k1', 'd', 'p', 'bogus', 'm', 1, 1, 1, 2)`, "CHECK constraint failed"},
		{"task finished without finished_at", nil,
			`INSERT INTO agent_tasks (id, client_label, prompt, status, model, max_steps, max_duration_sec, created_at)
			VALUES ('k1', 'd', 'p', 'completed', 'm', 1, 1, 1)`, "CHECK constraint failed"},
		{"task finished with finished_at", nil,
			`INSERT INTO agent_tasks (id, client_label, prompt, status, model, max_steps, max_duration_sec, created_at, finished_at)
			VALUES ('k1', 'd', 'p', 'completed', 'm', 1, 1, 1, 2)`, ""},
		{"task step_count must equal fast + openai", nil,
			`INSERT INTO agent_tasks (id, client_label, prompt, model, max_steps, max_duration_sec, created_at,
				step_count, fast_steps, openai_steps) VALUES ('k1', 'd', 'p', 'm', 1, 1, 1, 3, 1, 1)`, "CHECK constraint failed"},
		{"task consistent step counts", nil,
			`INSERT INTO agent_tasks (id, client_label, prompt, model, max_steps, max_duration_sec, created_at,
				step_count, fast_steps, openai_steps) VALUES ('k1', 'd', 'p', 'm', 1, 1, 1, 2, 1, 1)`, ""},
		{"task negative tokens", nil,
			`INSERT INTO agent_tasks (id, client_label, prompt, model, max_steps, max_duration_sec, created_at, input_tokens)
			VALUES ('k1', 'd', 'p', 'm', 1, 1, 1, -1)`, "CHECK constraint failed"},
		{"task unknown handoff_reason", nil,
			`INSERT INTO agent_tasks (id, client_label, prompt, model, max_steps, max_duration_sec, created_at, handoff_reason)
			VALUES ('k1', 'd', 'p', 'm', 1, 1, 1, 'because')`, "CHECK constraint failed"},

		{"thumbnail ok", []string{audit},
			`INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
			VALUES (1, '2026/10/30/a.jpg', 10, 320, 180, 1)`, ""},
		{"thumbnail needs audit log", nil,
			`INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
			VALUES (99, 'a.jpg', 10, 320, 180, 1)`, "FOREIGN KEY constraint failed"},
		{"thumbnail one per audit log", []string{audit,
			`INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
			VALUES (1, 'a.jpg', 10, 320, 180, 1)`},
			`INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
			VALUES (1, 'b.jpg', 10, 320, 180, 1)`, "UNIQUE constraint failed"},
		{"thumbnail negative size", []string{audit},
			`INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
			VALUES (1, 'a.jpg', -1, 320, 180, 1)`, "CHECK constraint failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := openTestDB(t)
			for _, s := range tt.setup {
				mustExec(t, db, s)
			}
			_, err := db.sql.ExecContext(context.Background(), tt.stmt)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
