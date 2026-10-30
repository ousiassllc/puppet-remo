package domain_test

import (
	"fmt"
	"time"
)

// fakeClock は手動で進める Clock。
type fakeClock struct{ now time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// seqIDGen は "lease-1", "lease-2", ... を返す IDGen。
type seqIDGen struct{ n int }

func (g *seqIDGen) NewID() string {
	g.n++
	return fmt.Sprintf("lease-%d", g.n)
}
