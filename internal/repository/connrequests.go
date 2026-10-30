package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// connection_requests.status の値。
const (
	ConnPending  = "pending"
	ConnApproved = "approved"
	ConnDenied   = "denied"
	ConnExpired  = "expired"
)

const (
	// DeniedTTL は拒否した接続要求が同じデバイスからの新しい要求を抑止する期間（er.md）。
	DeniedTTL = 24 * time.Hour
	// ConnRequestRetention は last_seen_at からこの期間を過ぎた接続要求を Expire が削除する（er.md）。
	ConnRequestRetention = 7 * 24 * time.Hour
)

// ConnRequest は未登録デバイスからの接続要求。
type ConnRequest struct {
	ID           string
	StableNodeID string
	LoginName    string
	DisplayName  string
	RemoteIP     string
	Status       string
	RequestCount int
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	ExpiresAt    time.Time
	DecidedAt    *time.Time
	ClientID     *string // 許可により作成した clients.id
}

// ConnRequests は connection_requests 表のストア。
type ConnRequests interface {
	// 1 デバイス 1 件に集約する。同じ stable_node_id の pending があれば、その行の
	// login_name・display_name・remote_ip・last_seen_at を更新して request_count を 1 増やし
	// （id・first_seen_at・expires_at は保つ）、created = false で返す。なければ r を pending で作る。
	// FirstSeenAt / LastSeenAt がゼロなら現在時刻を使う。
	UpsertPending(ctx context.Context, r ConnRequest) (req ConnRequest, created bool, err error)
	// pending の要求を approved / denied にする。decided_at を現在時刻にし、denied は
	// expires_at を DeniedTTL 後にする。ない id は ErrNotFound、pending でなければ ErrConflict。
	Decide(ctx context.Context, id string, status string, clientID *string) error
	// stable_node_id に、まだ有効（expires_at > now）な denied があるか。
	DeniedActive(ctx context.Context, stableNodeID string, now time.Time) (bool, error)
	ListPending(ctx context.Context) ([]ConnRequest, error)
	// expires_at を過ぎた pending を expired にし、last_seen_at から ConnRequestRetention を
	// 過ぎた行を削除する。
	Expire(ctx context.Context, now time.Time) error
}

type connRequestStore struct{ db *DB }

const connRequestColumns = `id, stable_node_id, login_name, display_name, remote_ip, status,
	request_count, first_seen_at, last_seen_at, expires_at, decided_at, client_id`

func scanConnRequest(row interface{ Scan(...any) error }) (ConnRequest, error) {
	var (
		r                    ConnRequest
		first, last, expires int64
		decided              sql.NullInt64
		clientID             sql.NullString
	)
	if err := row.Scan(&r.ID, &r.StableNodeID, &r.LoginName, &r.DisplayName, &r.RemoteIP, &r.Status,
		&r.RequestCount, &first, &last, &expires, &decided, &clientID); err != nil {
		return ConnRequest{}, err
	}
	r.FirstSeenAt, r.LastSeenAt, r.ExpiresAt = fromMS(first), fromMS(last), fromMS(expires)
	r.DecidedAt = fromNullMS(decided)
	if clientID.Valid {
		r.ClientID = &clientID.String
	}
	return r, nil
}

func (s connRequestStore) UpsertPending(ctx context.Context, r ConnRequest) (ConnRequest, bool, error) {
	first := r.FirstSeenAt
	if first.IsZero() {
		first = s.db.clock.Now()
	}
	last := r.LastSeenAt
	if last.IsZero() {
		last = first
	}
	// 部分 UNIQUE インデックス ux_connreq_pending_node との衝突を 1 文で集約に変える（競合しない）。
	got, err := scanConnRequest(s.db.sql.QueryRowContext(ctx,
		`INSERT INTO connection_requests (id, stable_node_id, login_name, display_name, remote_ip,
			status, request_count, first_seen_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, 'pending', 1, ?, ?, ?)
		ON CONFLICT (stable_node_id) WHERE status = 'pending' DO UPDATE SET
			login_name = excluded.login_name,
			display_name = excluded.display_name,
			remote_ip = excluded.remote_ip,
			request_count = request_count + 1,
			last_seen_at = excluded.last_seen_at
		RETURNING `+connRequestColumns,
		r.ID, r.StableNodeID, r.LoginName, r.DisplayName, r.RemoteIP,
		toMS(first), toMS(last), toMS(r.ExpiresAt)))
	if err != nil {
		return ConnRequest{}, false, mapErr(err)
	}
	return got, got.ID == r.ID, nil
}

func (s connRequestStore) Decide(ctx context.Context, id string, status string, clientID *string) error {
	if status != ConnApproved && status != ConnDenied {
		return fmt.Errorf("%w: decision status %q", domain.ErrInvalidArgument, status)
	}
	now := s.db.clock.Now()
	expires := sql.NullInt64{}
	if status == ConnDenied {
		expires = sql.NullInt64{Int64: toMS(now.Add(DeniedTTL)), Valid: true}
	}
	var cid sql.NullString
	if clientID != nil {
		cid = sql.NullString{String: *clientID, Valid: true}
	}
	res, err := s.db.sql.ExecContext(ctx,
		`UPDATE connection_requests
		SET status = ?, decided_at = ?, client_id = ?, expires_at = COALESCE(?, expires_at)
		WHERE id = ? AND status = 'pending'`,
		status, toMS(now), cid, expires, id)
	if err != nil {
		return mapErr(err)
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	var exists bool
	if err := s.db.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM connection_requests WHERE id = ?)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return fmt.Errorf("%w: connection request %s is not pending", ErrConflict, id)
}

func (s connRequestStore) DeniedActive(ctx context.Context, stableNodeID string, now time.Time) (bool, error) {
	var active bool
	err := s.db.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM connection_requests
			WHERE stable_node_id = ? AND status = 'denied' AND expires_at > ?)`,
		stableNodeID, toMS(now)).Scan(&active)
	return active, err
}

func (s connRequestStore) ListPending(ctx context.Context) ([]ConnRequest, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT `+connRequestColumns+` FROM connection_requests
		WHERE status = 'pending' ORDER BY first_seen_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConnRequest
	for rows.Next() {
		r, err := scanConnRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s connRequestStore) Expire(ctx context.Context, now time.Time) (err error) {
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err = tx.ExecContext(ctx,
		`UPDATE connection_requests SET status = 'expired' WHERE status = 'pending' AND expires_at <= ?`,
		toMS(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM connection_requests WHERE last_seen_at < ?`,
		toMS(now.Add(-ConnRequestRetention))); err != nil {
		return err
	}
	return tx.Commit()
}
