// Package repository は SQLite（modernc.org/sqlite、cgo なし）への永続化を提供する。
//
// 1 つの *sql.DB（WAL）を共有し、起動時に Open がマイグレーションと整合性チェックを同期で行う。
// goroutine は持たない（保守処理のタイマーは service/maintenance が持つ）。
// スキーマは docs/architecture/er.md の全体を v1 マイグレーションで作るが（M1.5 の launchable_apps は v2）、
// CRUD を持つのは clients / client_tokens / connection_requests / settings / audit_logs / audit_thumbnails / launchable_apps のみ。
// サムネイルの画像本体は DB ではなく ThumbStore（thumbs/ 配下のファイル）が持つ。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 呼び出し側が errors.Is で判定するエラー。
var (
	// ErrNotFound は対象の行がないことを表す。
	ErrNotFound = errors.New("repository: not found")
	// ErrConflict は UNIQUE 制約・状態の競合（登録済み、決定済みなど）を表す。
	ErrConflict = errors.New("repository: conflict")
	// ErrLimitReached は登録数の上限に達していることを表す（起動許可リストの 50 件。NFR-C06）。
	ErrLimitReached = errors.New("repository: limit reached")
)

// DB は repository の接続とその上のストアの入口。
type DB struct {
	sql         *sql.DB
	clock       domain.Clock
	quarantined string
	thumbsDir   string // DB ファイルと同じディレクトリの thumbs/
}

// Option は Open の設定。
type Option func(*options)

type options struct {
	clock      domain.Clock
	migrations fs.FS
}

// WithClock は更新時刻などの供給元を差し替える（既定はシステム時計）。
func WithClock(c domain.Clock) Option { return func(o *options) { o.clock = c } }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Open は path の SQLite を開き、WAL・マイグレーション・整合性チェックを行う。
//
// PRAGMA は foreign_keys=ON、journal_mode=WAL、busy_timeout=5000、synchronous=NORMAL
// （docs/architecture/er.md）。接続は 1 本に絞り、書き込みを直列化する。
// 起動時の PRAGMA quick_check に失敗した（壊れている）DB は path を .corrupt-<時刻> にリネームして
// 隔離し、空の DB で開く。隔離したときは QuarantinedPath がそのパスを返す（NFR-A06）。
func Open(ctx context.Context, path string, opts ...Option) (*DB, error) {
	o := options{clock: systemClock{}}
	for _, opt := range opts {
		opt(&o)
	}
	if o.migrations == nil {
		sub, err := fs.Sub(embeddedMigrations, "migrations")
		if err != nil {
			return nil, err
		}
		o.migrations = sub
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	quarantined := ""
	d, err := openChecked(ctx, path, o)
	if isCorruption(err) {
		quarantined = fmt.Sprintf("%s.corrupt-%d", path, o.clock.Now().UnixMilli())
		if err := quarantine(path, quarantined); err != nil {
			return nil, err
		}
		d, err = openChecked(ctx, path, o)
	}
	if err != nil {
		return nil, err
	}
	d.quarantined = quarantined
	return d, nil
}

// openChecked は接続・quick_check・マイグレーションを行う。失敗したら接続を閉じる。
func openChecked(ctx context.Context, path string, o options) (_ *DB, err error) {
	conn, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, conn.Close())
		}
	}()
	conn.SetMaxOpenConns(1)

	var res string
	if err = conn.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil {
		return nil, fmt.Errorf("quick_check: %w", err)
	}
	if res != "ok" {
		return nil, fmt.Errorf("quick_check: %w: %s", errCorrupt, res)
	}
	if err = migrate(ctx, conn, o.migrations, o.clock.Now); err != nil {
		return nil, err
	}
	return &DB{sql: conn, clock: o.clock, thumbsDir: filepath.Join(filepath.Dir(path), ThumbsDirName)}, nil
}

// errCorrupt は quick_check が "ok" 以外を返したことを表す。
var errCorrupt = errors.New("database is corrupt")

func isCorruption(err error) bool {
	if errors.Is(err, errCorrupt) {
		return true
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return true
		}
	}
	return false
}

// quarantine は壊れた DB とその WAL / SHM を dst 側へリネームする。
func quarantine(path, dst string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(path+suffix, dst+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("quarantine corrupt database: %w", err)
		}
	}
	return nil
}

// dsn は modernc.org/sqlite 用の接続文字列。接続ごとに PRAGMA を適用させる。
func dsn(path string) string {
	return path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)" +
		"&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
}

// QuarantinedPath は Open が壊れた DB を隔離したときの退避先パスを返す。隔離していなければ空。
func (d *DB) QuarantinedPath() string { return d.quarantined }

// Close は接続を閉じる。WAL はチェックポイントされる。
func (d *DB) Close() error { return d.sql.Close() }

// 各ストアの入口。
func (d *DB) Clients() Clients               { return clientStore{d} }
func (d *DB) Tokens() Tokens                 { return tokenStore{d} }
func (d *DB) ConnRequests() ConnRequests     { return connRequestStore{d} }
func (d *DB) Settings() Settings             { return settingStore{d} }
func (d *DB) Audit() Audit                   { return auditStore{d} }
func (d *DB) LaunchableApps() LaunchableApps { return launchableAppStore{d} }

// Thumbs は監査のサムネイルの置き場（DB ファイルと同じディレクトリの thumbs/。ディレクトリは Save が初めて作る）。
func (d *DB) Thumbs() ThumbStore { return newThumbStore(d.thumbsDir, d.clock) }

// mapErr は SQLite の UNIQUE 制約違反を ErrConflict に写す。それ以外はそのまま返す。
func mapErr(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %w", ErrConflict, err)
		}
	}
	return err
}

// requireOne は更新・削除が 1 行に効いたかを確認し、効かなければ ErrNotFound を返す。
func requireOne(res sql.Result, err error) error {
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// 時刻は UTC の Unix ミリ秒で保存する。
func toMS(t time.Time) int64 { return t.UnixMilli() }

func fromMS(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func toNullMS(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: toMS(*t), Valid: true}
}

func fromNullMS(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := fromMS(n.Int64)
	return &t
}

// nullStr は空文字を NULL にする（任意の文字列列用）。
func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
