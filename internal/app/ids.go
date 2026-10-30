package app

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ULIDGen は ULID（時刻順に並ぶ一意な ID）を返す domain.IDGen。クライアント・トークン・接続要求・リースの ID に使う
// （docs/architecture/overview.md「ID 生成」）。goroutine セーフ。同じミリ秒の ID も単調増加になる。
type ULIDGen struct {
	mu      sync.Mutex
	entropy io.Reader
	now     func() time.Time
}

// NewULIDGen は crypto/rand を乱数源にする ULIDGen を返す。
func NewULIDGen() *ULIDGen { return newULIDGen(rand.Reader, time.Now) }

func newULIDGen(r io.Reader, now func() time.Time) *ULIDGen {
	return &ULIDGen{entropy: ulid.Monotonic(r, 0), now: now}
}

// NewID は新しい ULID（26 文字）を返す。
func (g *ULIDGen) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(g.now()), g.entropy).String()
}
