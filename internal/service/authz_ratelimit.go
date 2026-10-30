package service

import (
	"sync"
	"time"
)

// windowLimiter はキーごとの固定窓カウンタ（メモリのみ。docs/components/overview.md §5.1）。
// 窓は、そのキーの最初の計上から window 後に終わる。
type windowLimiter struct {
	window time.Duration
	limit  int

	mu        sync.Mutex
	entries   map[string]*windowEntry
	nextSweep time.Time
}

type windowEntry struct {
	start    time.Time
	count    int
	notified bool // 上限超過を 1 度通知した（窓ごとに 1 回だけ監査に書くため）
}

func newWindowLimiter(window time.Duration, limit int) *windowLimiter {
	return &windowLimiter{window: window, limit: limit, entries: make(map[string]*windowEntry)}
}

// check は key が現在の窓で上限に達しているかを調べる。consume が true で上限未満なら 1 件計上する。
// allowed が false のとき、firstOver はその窓での最初の超過、retryAfter は窓が終わるまでの時間。
func (l *windowLimiter) check(key string, now time.Time, consume bool) (allowed, firstOver bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	e := l.entries[key]
	if e == nil || !now.Before(e.start.Add(l.window)) {
		e = &windowEntry{start: now}
		l.entries[key] = e
	}
	if e.count < l.limit {
		if consume {
			e.count++
		}
		return true, false, 0
	}
	firstOver = !e.notified
	e.notified = true
	return false, firstOver, e.start.Add(l.window).Sub(now)
}

// sweepLocked は窓の終わったエントリを窓 1 つ分の間隔で掃除する（二度と来ないキーが溜まらないように）。
func (l *windowLimiter) sweepLocked(now time.Time) {
	if now.Before(l.nextSweep) {
		return
	}
	for k, e := range l.entries {
		if !now.Before(e.start.Add(l.window)) {
			delete(l.entries, k)
		}
	}
	l.nextSweep = now.Add(l.window)
}
