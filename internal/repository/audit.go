package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// AuditEntry は audit_logs に追記する 1 件のイベント（FR-AU01）。
// 空文字・nil の任意項目は NULL で保存する。トークンと API キーは DetailJSON に含めないこと。
type AuditEntry struct {
	TS          time.Time // ゼロなら現在時刻
	Kind        string    // auth / client / token / lease / tool / input / agent / killswitch / settings / update
	Event       string    // 例: lease.acquire, tool.left_click
	ActorType   domain.ActorKind
	ClientID    string // 未登録・host・system は空（NULL）。削除済みのクライアントも NULL（ラベルは残る）
	ClientLabel string // 表示名のスナップショット
	SessionID   string
	LeaseID     string
	TaskID      string // M4: agent_tasks.id
	StepNo      *int
	Tool        string
	Result      string // ok / error / denied
	ErrorCode   string
	DurationMS  *int64
	DetailJSON  json.RawMessage // nil / 空なら NULL。有効な JSON でなければならない
}

// Audit は監査ログ（audit_logs 表）のストア。
// M1 は追記だけ。閲覧クエリ・サムネイル・保持期間の削除は M3 で足す。
type Audit interface {
	Append(ctx context.Context, e AuditEntry) (id int64, err error)
}

type auditStore struct{ db *DB }

func (s auditStore) Append(ctx context.Context, e AuditEntry) (int64, error) {
	ts := e.TS
	if ts.IsZero() {
		ts = s.db.clock.Now()
	}
	var detail sql.NullString
	if len(e.DetailJSON) > 0 {
		if !json.Valid(e.DetailJSON) {
			return 0, fmt.Errorf("%w: audit detail is not valid JSON", domain.ErrInvalidArgument)
		}
		detail = sql.NullString{String: string(e.DetailJSON), Valid: true}
	}
	res, err := s.db.sql.ExecContext(ctx,
		`INSERT INTO audit_logs (ts, kind, event, actor_type, client_id, client_label, session_id,
			lease_id, task_id, step_no, tool, result, error_code, duration_ms, detail_json)
		VALUES (?, ?, ?, ?, (SELECT id FROM clients WHERE id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		toMS(ts), e.Kind, e.Event, string(e.ActorType), nullStr(e.ClientID), e.ClientLabel,
		nullStr(e.SessionID), nullStr(e.LeaseID), nullStr(e.TaskID), e.StepNo,
		nullStr(e.Tool), e.Result, nullStr(e.ErrorCode), e.DurationMS, detail)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.LastInsertId()
}
