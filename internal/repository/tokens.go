package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TokenPrefixLen は表示用に保存するトークン文字列の先頭の文字数（NFR-S04）。
const TokenPrefixLen = 8

// HashToken はトークン文字列（接頭辞 prm_ を含む）の UTF-8 バイト列の SHA-256 を返す（NFR-S04）。
// DB に保存・照合するのはこのハッシュだけで、平文は保存しない。
func HashToken(token string) [32]byte { return sha256.Sum256([]byte(token)) }

// TokenDisplayPrefix は表示用のトークンの先頭 TokenPrefixLen 文字を返す。識別用で認証には使わない。
func TokenDisplayPrefix(token string) string {
	r := []rune(token)
	if len(r) > TokenPrefixLen {
		r = r[:TokenPrefixLen]
	}
	return string(r)
}

// TokenRecord は client_tokens の 1 行。トークン本体は持たず、ハッシュだけを持つ。
type TokenRecord struct {
	ID         string
	ClientID   string
	Label      string
	TokenHash  [32]byte
	Prefix     string
	ExpiresAt  *time.Time // nil は無期限
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// Tokens は MCP 用 Bearer トークン（client_tokens 表）のストア。
type Tokens interface {
	Create(ctx context.Context, t TokenRecord) error // ハッシュのみ。(client_id, label)・ハッシュの重複は ErrConflict
	// 引数はトークン文字列（接頭辞 prm_ を含む）の SHA-256。未登録は ErrNotFound。
	// ハッシュの定数時間比較は Authz が行う。
	ByHash(ctx context.Context, hash [32]byte) (TokenRecord, error)
	ListByClient(ctx context.Context, clientID string) ([]TokenRecord, error)
	Revoke(ctx context.Context, id string, at time.Time) error // 失効済みなら元の失効時刻を保つ
	TouchUsed(ctx context.Context, id string, at time.Time) error
}

type tokenStore struct{ db *DB }

const tokenColumns = `id, client_id, label, token_hash, token_prefix,
	expires_at, revoked_at, last_used_at, created_at`

func scanToken(row interface{ Scan(...any) error }) (TokenRecord, error) {
	var (
		t                          TokenRecord
		hash                       []byte
		expires, revoked, lastUsed sql.NullInt64
		created                    int64
	)
	if err := row.Scan(&t.ID, &t.ClientID, &t.Label, &hash, &t.Prefix,
		&expires, &revoked, &lastUsed, &created); err != nil {
		return TokenRecord{}, err
	}
	if len(hash) != len(t.TokenHash) {
		return TokenRecord{}, fmt.Errorf("token %s: stored hash has %d bytes", t.ID, len(hash))
	}
	copy(t.TokenHash[:], hash)
	t.ExpiresAt, t.RevokedAt, t.LastUsedAt = fromNullMS(expires), fromNullMS(revoked), fromNullMS(lastUsed)
	t.CreatedAt = fromMS(created)
	return t, nil
}

func (s tokenStore) Create(ctx context.Context, t TokenRecord) error {
	created := t.CreatedAt
	if created.IsZero() {
		created = s.db.clock.Now()
	}
	_, err := s.db.sql.ExecContext(ctx,
		`INSERT INTO client_tokens (`+tokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ClientID, t.Label, t.TokenHash[:], t.Prefix,
		toNullMS(t.ExpiresAt), toNullMS(t.RevokedAt), toNullMS(t.LastUsedAt), toMS(created))
	return mapErr(err)
}

func (s tokenStore) ByHash(ctx context.Context, hash [32]byte) (TokenRecord, error) {
	t, err := scanToken(s.db.sql.QueryRowContext(ctx,
		`SELECT `+tokenColumns+` FROM client_tokens WHERE token_hash = ?`, hash[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return TokenRecord{}, ErrNotFound
	}
	return t, err
}

func (s tokenStore) ListByClient(ctx context.Context, clientID string) ([]TokenRecord, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM client_tokens WHERE client_id = ? ORDER BY created_at, id`, clientID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TokenRecord
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s tokenStore) Revoke(ctx context.Context, id string, at time.Time) error {
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE client_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, toMS(at), id))
}

func (s tokenStore) TouchUsed(ctx context.Context, id string, at time.Time) error {
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE client_tokens SET last_used_at = ? WHERE id = ?`, toMS(at), id))
}
