package repository

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock は進行可能な domain.Clock。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// openTestDB は一時ディレクトリのファイルに DB を開く（閉じるのは t.Cleanup）。
func openTestDB(t *testing.T) (*DB, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "puppet-remo.db"), WithClock(clock))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, clock
}

// mustExec は生の SQL を実行する（制約・外部キーの検査用）。
func mustExec(t *testing.T, db *DB, query string, args ...any) {
	t.Helper()
	if _, err := db.sql.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// count は SELECT COUNT(*) の結果を返す。
func count(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}
