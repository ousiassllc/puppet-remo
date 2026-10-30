package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// LaunchableApps は起動許可リスト（launchable_apps 表。M1.5）のストア。書き込みは service.AppRegistry だけ。
// ID と時刻（CreatedAt・UpdatedAt）は呼び出し側が付ける。検証（domain.ValidateAppInput）は呼び出し側の責務で、
// ストアは label の一意性と件数の上限だけを守る。
type LaunchableApps interface {
	Get(ctx context.Context, id string) (domain.LaunchableApp, error) // 無ければ ErrNotFound
	List(ctx context.Context) ([]domain.LaunchableApp, error)         // label の昇順（NOCASE）
	// Create は件数の確認と挿入を同じトランザクションで行う。limit 件に達していれば ErrLimitReached、
	// label の重複（ASCII の大文字小文字を無視）か id の重複は ErrConflict。
	Create(ctx context.Context, a domain.LaunchableApp, limit int) error
	// Update は label・ExePath・Args・WorkingDir・UpdatedAt を置き換える（ID・CreatedAt は変えない）。
	// 無ければ ErrNotFound、他の行との label の重複は ErrConflict。
	Update(ctx context.Context, a domain.LaunchableApp) error
	Delete(ctx context.Context, id string) error // 無ければ ErrNotFound
}

type launchableAppStore struct{ db *DB }

const launchableAppColumns = `id, label, exe_path, args_json, working_dir, created_at, updated_at`

func scanLaunchableApp(row interface{ Scan(...any) error }) (domain.LaunchableApp, error) {
	var (
		a                    domain.LaunchableApp
		argsJSON             string
		createdAt, updatedAt int64
	)
	if err := row.Scan(&a.ID, &a.Label, &a.ExePath, &argsJSON, &a.WorkingDir, &createdAt, &updatedAt); err != nil {
		return domain.LaunchableApp{}, err
	}
	// 手で書き換えられた行（配列だが要素が文字列でない）は読めない。内容（パス・引数）はエラーに含めない。
	if err := json.Unmarshal([]byte(argsJSON), &a.Args); err != nil {
		return domain.LaunchableApp{}, fmt.Errorf("launchable_apps %s: args_json is not a JSON array of strings", a.ID)
	}
	if a.Args == nil { // JSON null（配列でない行は CHECK が拒む）
		a.Args = []string{}
	}
	a.CreatedAt, a.UpdatedAt = fromMS(createdAt), fromMS(updatedAt)
	return a, nil
}

func marshalArgs(args []string) (string, error) {
	if args == nil {
		args = []string{} // nil は "null" になり CHECK に落ちる
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s launchableAppStore) Get(ctx context.Context, id string) (domain.LaunchableApp, error) {
	a, err := scanLaunchableApp(s.db.sql.QueryRowContext(ctx,
		`SELECT `+launchableAppColumns+` FROM launchable_apps WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LaunchableApp{}, ErrNotFound
	}
	return a, err
}

func (s launchableAppStore) List(ctx context.Context) ([]domain.LaunchableApp, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT `+launchableAppColumns+` FROM launchable_apps ORDER BY label COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.LaunchableApp
	for rows.Next() {
		a, err := scanLaunchableApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s launchableAppStore) Create(ctx context.Context, a domain.LaunchableApp, limit int) (err error) {
	args, err := marshalArgs(a.Args)
	if err != nil {
		return err
	}
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
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM launchable_apps`).Scan(&n); err != nil {
		return err
	}
	if n >= limit {
		return ErrLimitReached
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO launchable_apps (`+launchableAppColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Label, a.ExePath, args, a.WorkingDir, toMS(a.CreatedAt), toMS(a.UpdatedAt)); err != nil {
		return mapErr(err)
	}
	return tx.Commit()
}

func (s launchableAppStore) Update(ctx context.Context, a domain.LaunchableApp) error {
	args, err := marshalArgs(a.Args)
	if err != nil {
		return err
	}
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE launchable_apps SET label = ?, exe_path = ?, args_json = ?, working_dir = ?, updated_at = ?
		WHERE id = ?`,
		a.Label, a.ExePath, args, a.WorkingDir, toMS(a.UpdatedAt), a.ID))
}

func (s launchableAppStore) Delete(ctx context.Context, id string) error {
	return requireOne(s.db.sql.ExecContext(ctx, `DELETE FROM launchable_apps WHERE id = ?`, id))
}
