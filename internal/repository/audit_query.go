package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Query の件数。API 仕様 §7.3 の `GET /audit` の limit（既定 50、最大 200）。
const (
	DefaultAuditLimit = 50
	MaxAuditLimit     = 200
)

// AuditQuery は Query の絞り込み。空・ゼロの項目は絞り込まない。API 仕様 §7.3 の `GET /audit` のクエリに 1 対 1 で対応する。
type AuditQuery struct {
	From     time.Time // ts >= From
	To       time.Time // ts <= To
	ClientID string
	Kind     string
	Result   string
	Q        string // event の部分一致（LIKE のワイルドカードは文字どおりに扱う）
	Cursor   int64  // 直前のページの最後の id。0 なら先頭から
	Limit    int    // 0 以下は DefaultAuditLimit、MaxAuditLimit を超える値は MaxAuditLimit
}

// auditColumns は audit_logs（別名 l）と audit_thumbnails（別名 t）の読み出す列。
// 一覧（Query）は detail_json を読まない（引数は詳細画面の Get で読む）。
const auditColumns = `l.id, l.ts, l.kind, l.event, l.actor_type, l.client_id, l.client_label, l.session_id,
	l.lease_id, l.task_id, l.step_no, l.tool, l.result, l.error_code, l.duration_ms, %s,
	t.rel_path, t.size_bytes, t.width, t.height, t.created_at`

const auditFrom = ` FROM audit_logs l LEFT JOIN audit_thumbnails t ON t.audit_log_id = l.id`

type rowScanner interface{ Scan(dest ...any) error }

func scanAuditEntry(r rowScanner) (AuditEntry, error) {
	var (
		e                                   AuditEntry
		ts                                  int64
		actor                               string
		client, session, lease, task, tool  sql.NullString
		errCode, detail, relPath            sql.NullString
		step, duration                      sql.NullInt64
		size, width, height, thumbCreatedAt sql.NullInt64
	)
	if err := r.Scan(&e.ID, &ts, &e.Kind, &e.Event, &actor, &client, &e.ClientLabel, &session,
		&lease, &task, &step, &tool, &e.Result, &errCode, &duration, &detail,
		&relPath, &size, &width, &height, &thumbCreatedAt); err != nil {
		return AuditEntry{}, err
	}
	e.TS = fromMS(ts)
	e.ActorType = domain.ActorKind(actor)
	e.ClientID, e.SessionID, e.LeaseID, e.TaskID = client.String, session.String, lease.String, task.String
	e.Tool, e.ErrorCode = tool.String, errCode.String
	if step.Valid {
		n := int(step.Int64)
		e.StepNo = &n
	}
	if duration.Valid {
		e.DurationMS = &duration.Int64
	}
	if detail.Valid {
		e.DetailJSON = []byte(detail.String)
	}
	if relPath.Valid {
		e.Thumb = &ThumbMeta{
			RelPath: relPath.String, SizeBytes: size.Int64, Width: int(width.Int64), Height: int(height.Int64),
			CreatedAt: fromMS(thumbCreatedAt.Int64),
		}
	}
	return e, nil
}

// likeEscape は LIKE のパターンに入れる文字列の %・_・\ をエスケープする（ESCAPE '\' と組で使う）。
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s auditStore) Query(ctx context.Context, q AuditQuery) ([]AuditEntry, int64, error) {
	if q.Cursor < 0 {
		return nil, 0, fmt.Errorf("%w: audit cursor must not be negative", domain.ErrInvalidArgument)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultAuditLimit
	}
	limit = min(limit, MaxAuditLimit)

	var (
		where []string
		args  []any
	)
	add := func(cond string, arg any) {
		where = append(where, cond)
		args = append(args, arg)
	}
	if !q.From.IsZero() {
		add("l.ts >= ?", toMS(q.From))
	}
	if !q.To.IsZero() {
		add("l.ts <= ?", toMS(q.To))
	}
	if q.ClientID != "" {
		add("l.client_id = ?", q.ClientID)
	}
	if q.Kind != "" {
		add("l.kind = ?", q.Kind)
	}
	if q.Result != "" {
		add("l.result = ?", q.Result)
	}
	if q.Q != "" {
		add(`l.event LIKE ? ESCAPE '\'`, "%"+likeEscape(q.Q)+"%")
	}
	if q.Cursor > 0 {
		var cursorTS int64
		err := s.db.sql.QueryRowContext(ctx, `SELECT ts FROM audit_logs WHERE id = ?`, q.Cursor).Scan(&cursorTS)
		if errors.Is(err, sql.ErrNoRows) {
			// 閲覧の途中で保持期間の削除が走り、カーソルの行が消えた。続きは無い。
			return nil, 0, nil
		}
		if err != nil {
			return nil, 0, err
		}
		where = append(where, "(l.ts, l.id) < (?, ?)")
		args = append(args, cursorTS, q.Cursor)
	}

	query := `SELECT ` + fmt.Sprintf(auditColumns, "NULL") + auditFrom
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// 次のページの有無を知るために 1 件多く読む。
	query += ` ORDER BY l.ts DESC, l.id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]AuditEntry, 0, min(limit+1, 64))
	for rows.Next() {
		e, err := scanAuditEntry(rows)
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(entries) > limit {
		entries = entries[:limit]
		next = entries[limit-1].ID
	}
	return entries, next, nil
}

func (s auditStore) Get(ctx context.Context, id int64) (AuditEntry, error) {
	e, err := scanAuditEntry(s.db.sql.QueryRowContext(ctx,
		`SELECT `+fmt.Sprintf(auditColumns, "l.detail_json")+auditFrom+` WHERE l.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditEntry{}, ErrNotFound
	}
	return e, err
}

func (s auditStore) AttachThumb(ctx context.Context, auditID int64, t ThumbMeta) error {
	rel, err := cleanThumbPath(t.RelPath)
	if err != nil {
		return err
	}
	if t.SizeBytes < 0 || t.Width <= 0 || t.Height <= 0 {
		return fmt.Errorf("%w: thumbnail size %d / %dx%d", domain.ErrInvalidArgument, t.SizeBytes, t.Width, t.Height)
	}
	created := t.CreatedAt
	if created.IsZero() {
		created = s.db.clock.Now()
	}
	_, err = s.db.sql.ExecContext(ctx,
		`INSERT INTO audit_thumbnails (audit_log_id, rel_path, size_bytes, width, height, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		auditID, rel, t.SizeBytes, t.Width, t.Height, toMS(created))
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		return fmt.Errorf("%w: audit log %d", ErrNotFound, auditID)
	}
	return mapErr(err)
}

func (s auditStore) PurgeBefore(ctx context.Context, t time.Time) (removed []string, err error) {
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			removed = nil
			err = errors.Join(err, tx.Rollback())
		}
	}()
	cutoff := toMS(t)
	// CASCADE で消える前に、ファイルの相対パスを集める。
	rows, err := tx.QueryContext(ctx,
		`SELECT rel_path FROM audit_thumbnails WHERE audit_log_id IN (SELECT id FROM audit_logs WHERE ts < ?)`, cutoff)
	if err != nil {
		return nil, err
	}
	removed, err = scanStrings(rows)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM audit_logs WHERE ts < ?`, cutoff); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return removed, nil
}

func (s auditStore) PurgeThumbsOver(ctx context.Context, maxBytes int64) (removed []string, err error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: thumbnail size cap must be positive", domain.ErrInvalidArgument)
	}
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			removed = nil
			err = errors.Join(err, tx.Rollback())
		}
	}()
	var total int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes), 0) FROM audit_thumbnails`).Scan(&total); err != nil {
		return nil, err
	}
	if total <= maxBytes {
		return nil, tx.Commit()
	}
	// 古い順（ix_thumb_created）に、上限以下になるまでの行を選ぶ。
	rows, err := tx.QueryContext(ctx, `SELECT id, rel_path, size_bytes FROM audit_thumbnails ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for total > maxBytes && rows.Next() {
		var (
			id, size int64
			rel      string
		)
		if err = rows.Scan(&id, &rel, &size); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		removed = append(removed, rel)
		total -= size
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `DELETE FROM audit_thumbnails WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return removed, nil
}

func (s auditStore) ThumbPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT rel_path FROM audit_thumbnails`)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

func (s auditStore) PurgeMissingThumbs(ctx context.Context, exists func(relPath string) bool) (int, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id, rel_path FROM audit_thumbnails`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id  int64
		rel string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.rel); err != nil {
			_ = rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	// 接続は 1 本なので、行を読み終えてから（DB の外で）ファイルを確かめる。
	var missing []int64
	for _, r := range all {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !exists(r.rel) {
			missing = append(missing, r.id)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	for _, id := range missing {
		if _, err := tx.ExecContext(ctx, `DELETE FROM audit_thumbnails WHERE id = ?`, id); err != nil {
			return 0, errors.Join(err, tx.Rollback())
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(missing), nil
}

// scanStrings は 1 列の文字列の結果を読み切って rows を閉じる。
func scanStrings(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
