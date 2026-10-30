-- v1: docs/architecture/er.md の全テーブル。
-- SQLite は後から外部キーを足せないため、M1 では使わない agent_tasks / audit_thumbnails も最初に作る。
-- 時刻は UTC の Unix ミリ秒、真偽値は 0/1。

CREATE TABLE clients (
    id             TEXT PRIMARY KEY,
    stable_node_id TEXT NOT NULL,
    login_name     TEXT NOT NULL,
    display_name   TEXT NOT NULL,
    web_enabled    INTEGER NOT NULL DEFAULT 1,
    mcp_enabled    INTEGER NOT NULL DEFAULT 1,
    agent_enabled  INTEGER NOT NULL DEFAULT 0,
    approval_mode  TEXT NOT NULL DEFAULT 'always',
    approved_at    INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    last_seen_at   INTEGER,
    UNIQUE (stable_node_id),
    CHECK (approval_mode IN ('always', 'first_time', 'auto')),
    CHECK (web_enabled IN (0, 1)),
    CHECK (mcp_enabled IN (0, 1)),
    CHECK (agent_enabled IN (0, 1))
);

CREATE TABLE client_tokens (
    id           TEXT PRIMARY KEY,
    client_id    TEXT NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    label        TEXT NOT NULL,
    token_hash   BLOB NOT NULL,
    token_prefix TEXT NOT NULL,
    expires_at   INTEGER,
    revoked_at   INTEGER,
    last_used_at INTEGER,
    created_at   INTEGER NOT NULL,
    UNIQUE (token_hash),
    UNIQUE (client_id, label),
    CHECK (length(token_hash) = 32)
);

CREATE TABLE connection_requests (
    id            TEXT PRIMARY KEY,
    stable_node_id TEXT NOT NULL,
    login_name    TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    remote_ip     TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    request_count INTEGER NOT NULL DEFAULT 1,
    first_seen_at INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    decided_at    INTEGER,
    client_id     TEXT REFERENCES clients (id) ON DELETE SET NULL,
    CHECK (status IN ('pending', 'approved', 'denied', 'expired'))
);

CREATE UNIQUE INDEX ux_connreq_pending_node
    ON connection_requests (stable_node_id) WHERE status = 'pending';
CREATE INDEX ix_connreq_status_expires
    ON connection_requests (status, expires_at);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (json_valid(value_json))
);

CREATE TABLE agent_tasks (
    id               TEXT PRIMARY KEY,
    client_id        TEXT REFERENCES clients (id) ON DELETE SET NULL,
    client_label     TEXT NOT NULL,
    prompt           TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending_approval',
    model            TEXT NOT NULL,
    max_steps        INTEGER NOT NULL,
    max_duration_sec INTEGER NOT NULL,
    step_count       INTEGER NOT NULL DEFAULT 0,
    fast_steps       INTEGER NOT NULL DEFAULT 0,
    openai_steps     INTEGER NOT NULL DEFAULT 0,
    handoff_reason   TEXT,
    input_tokens     INTEGER NOT NULL DEFAULT 0,
    output_tokens    INTEGER NOT NULL DEFAULT 0,
    result_text      TEXT,
    error_code       TEXT,
    created_at       INTEGER NOT NULL,
    started_at       INTEGER,
    finished_at      INTEGER,
    CHECK (status IN ('pending_approval', 'running', 'completed', 'failed', 'cancelled',
                      'limit_reached', 'aborted', 'denied')),
    CHECK (status IN ('pending_approval', 'running') OR finished_at IS NOT NULL),
    CHECK (step_count >= 0 AND fast_steps >= 0 AND openai_steps >= 0
           AND input_tokens >= 0 AND output_tokens >= 0),
    CHECK (step_count = fast_steps + openai_steps),
    CHECK (handoff_reason IS NULL OR handoff_reason IN ('abstain', 'low_confidence', 'no_candidates',
                      'invalid_choice', 'no_progress', 'goal_maybe_done', 'fast_step_limit',
                      'chooser_unavailable'))
);

CREATE INDEX ix_task_created ON agent_tasks (created_at DESC);
CREATE INDEX ix_task_client_created ON agent_tasks (client_id, created_at DESC);
CREATE INDEX ix_task_active ON agent_tasks (status) WHERE status IN ('pending_approval', 'running');

CREATE TABLE audit_logs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           INTEGER NOT NULL,
    kind         TEXT NOT NULL,
    event        TEXT NOT NULL,
    actor_type   TEXT NOT NULL,
    client_id    TEXT REFERENCES clients (id) ON DELETE SET NULL,
    client_label TEXT NOT NULL,
    session_id   TEXT,
    lease_id     TEXT,
    task_id      TEXT REFERENCES agent_tasks (id) ON DELETE SET NULL,
    step_no      INTEGER,
    tool         TEXT,
    result       TEXT NOT NULL,
    error_code   TEXT,
    duration_ms  INTEGER,
    detail_json  TEXT,
    CHECK (result IN ('ok', 'error', 'denied')),
    CHECK (actor_type IN ('mcp', 'web', 'agent', 'host', 'system')),
    CHECK (detail_json IS NULL OR json_valid(detail_json))
);

CREATE INDEX ix_audit_ts ON audit_logs (ts DESC);
CREATE INDEX ix_audit_client_ts ON audit_logs (client_id, ts DESC);
CREATE INDEX ix_audit_kind_ts ON audit_logs (kind, ts DESC);
CREATE INDEX ix_audit_result_ts ON audit_logs (result, ts DESC);
CREATE INDEX ix_audit_session ON audit_logs (session_id);
CREATE INDEX ix_audit_task_step ON audit_logs (task_id, step_no);

CREATE TABLE audit_thumbnails (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    audit_log_id INTEGER NOT NULL REFERENCES audit_logs (id) ON DELETE CASCADE,
    rel_path     TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL,
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (audit_log_id),
    CHECK (size_bytes >= 0)
);

CREATE INDEX ix_thumb_created ON audit_thumbnails (created_at);
