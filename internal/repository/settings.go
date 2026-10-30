package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Settings はキー・値形式の設定（settings 表）のストア。値の型検証はサービス層が行う。
// OpenAI / TypeSafe の API キーは保存しない（資格情報マネージャーのみ。er.md）。
type Settings interface {
	// 行がなければ ok = false（呼び出し側が既定値を使う）。
	Get(ctx context.Context, key string) (value json.RawMessage, ok bool, err error)
	// 値は有効な JSON でなければならない（満たさなければ domain.ErrInvalidArgument）。
	Set(ctx context.Context, key string, value json.RawMessage) error
	All(ctx context.Context) (map[string]json.RawMessage, error)
}

type settingStore struct{ db *DB }

func (s settingStore) Get(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var v string
	err := s.db.sql.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return json.RawMessage(v), true, nil
}

func (s settingStore) Set(ctx context.Context, key string, value json.RawMessage) error {
	if !json.Valid(value) {
		return fmt.Errorf("%w: setting %q is not valid JSON", domain.ErrInvalidArgument, key)
	}
	_, err := s.db.sql.ExecContext(ctx,
		`INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		key, string(value), toMS(s.db.clock.Now()))
	return err
}

func (s settingStore) All(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT key, value_json FROM settings`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}
