package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// LaunchDenylist は拒否リスト（launch_denylist 表。launch.mode が denylist のときだけ効く）のストア。書き込みは service.AppRegistry だけ。
// ID と時刻は呼び出し側が付ける。値の検証（domain.ClassifyDenied）と実体パスへの解決は呼び出し側の責務で、
// ストアは重複（domain.DeniedKey が同じもの）と件数の上限だけを守る。
type LaunchDenylist interface {
	Get(ctx context.Context, id string) (domain.DeniedApp, error)    // 無ければ ErrNotFound
	List(ctx context.Context) ([]domain.DeniedApp, error)            // kind・value の昇順（NOCASE）
	Create(ctx context.Context, d domain.DeniedApp, limit int) error // 件数の確認と挿入は同じトランザクション。limit 件に達していれば ErrLimitReached、重複は ErrConflict
	Update(ctx context.Context, d domain.DeniedApp) error            // Kind・Value・UpdatedAt を置き換える。無ければ ErrNotFound、他の行との重複は ErrConflict
	Delete(ctx context.Context, id string) error                     // 無ければ ErrNotFound
}

type launchDenylistStore struct{ db *DB }

const launchDenylistColumns = `id, kind, value, created_at, updated_at`

func scanDenied(row interface{ Scan(...any) error }) (domain.DeniedApp, error) {
	var (
		d                    domain.DeniedApp
		kind                 string
		createdAt, updatedAt int64
	)
	if err := row.Scan(&d.ID, &kind, &d.Value, &createdAt, &updatedAt); err != nil {
		return domain.DeniedApp{}, err
	}
	d.Kind = domain.DeniedKind(kind)
	d.CreatedAt, d.UpdatedAt = fromMS(createdAt), fromMS(updatedAt)
	return d, nil
}

func (s launchDenylistStore) Get(ctx context.Context, id string) (domain.DeniedApp, error) {
	d, err := scanDenied(s.db.sql.QueryRowContext(ctx,
		`SELECT `+launchDenylistColumns+` FROM launch_denylist WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeniedApp{}, ErrNotFound
	}
	return d, err
}

func (s launchDenylistStore) List(ctx context.Context) ([]domain.DeniedApp, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT `+launchDenylistColumns+` FROM launch_denylist ORDER BY kind, value COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.DeniedApp
	for rows.Next() {
		d, err := scanDenied(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s launchDenylistStore) Create(ctx context.Context, d domain.DeniedApp, limit int) (err error) {
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM launch_denylist`).Scan(&n); err != nil {
		return err
	}
	if n >= limit {
		return ErrLimitReached
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO launch_denylist (id, kind, value, dedup_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, string(d.Kind), d.Value, domain.DeniedKey(d.Kind, d.Value), toMS(d.CreatedAt), toMS(d.UpdatedAt)); err != nil {
		return mapErr(err)
	}
	return tx.Commit()
}

func (s launchDenylistStore) Update(ctx context.Context, d domain.DeniedApp) error {
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE launch_denylist SET kind = ?, value = ?, dedup_key = ?, updated_at = ? WHERE id = ?`,
		string(d.Kind), d.Value, domain.DeniedKey(d.Kind, d.Value), toMS(d.UpdatedAt), d.ID))
}

func (s launchDenylistStore) Delete(ctx context.Context, id string) error {
	return requireOne(s.db.sql.ExecContext(ctx, `DELETE FROM launch_denylist WHERE id = ?`, id))
}
