package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// AuditEntry は監査エントリ（repository.AuditEntry の別名）。repository を import できない層（mcp など）が
// AuditRecorder を実装・利用するときの名前。
type AuditEntry = repository.AuditEntry

// AuditRecorder は監査ログへ追記する側の最小の窓口。LeaseManager・KillSwitch・Control が使う。
// Record は呼び出しを遅らせず、失敗を返さない（Audit が非同期のキューへ入れる）。
type AuditRecorder interface {
	Record(ctx context.Context, e repository.AuditEntry)
}

const defaultAuditQueueSize = 256

// Audit は監査ログの追記（M1。FR-AU01）。書き込みは goroutine 1 本が行い、Record は
// バッファ付きチャネルへ非ブロッキングで入れる。満杯のときは呼び出しを待たせずに捨て、ドロップ数を slog で警告する。
// 閲覧（Query）・サムネイル・保持期間の削除は M3。
type Audit struct {
	store repository.Audit
	clock domain.Clock
	log   *slog.Logger

	mu     sync.RWMutex // closed と queue のクローズを守る
	closed bool
	queue  chan auditItem

	dropped atomic.Int64
	done    chan struct{}
}

// auditItem はキューの 1 要素。flushed が非 nil のものは Flush の目印で、entry は無い。
type auditItem struct {
	ctx     context.Context
	entry   repository.AuditEntry
	flushed chan struct{}
}

// AuditConfig は NewAudit の設定。
type AuditConfig struct {
	Store     repository.Audit
	Clock     domain.Clock
	Logger    *slog.Logger // nil なら slog.Default()
	QueueSize int          // 0 なら 256
}

// ErrAuditClosed は Close 済みの Audit への Flush で返る。
var ErrAuditClosed = errors.New("service: audit is closed")

// NewAudit は書き込み goroutine を起動した Audit を返す。止めるには Close を呼ぶ。
func NewAudit(cfg AuditConfig) (*Audit, error) {
	if cfg.Store == nil || cfg.Clock == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("audit store and clock are required")
	}
	size := cfg.QueueSize
	if size <= 0 {
		size = defaultAuditQueueSize
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	a := &Audit{
		store: cfg.Store,
		clock: cfg.Clock,
		log:   log,
		queue: make(chan auditItem, size),
		done:  make(chan struct{}),
	}
	go a.run()
	return a, nil
}

// Record は e をキューへ入れる。TS がゼロなら現在時刻を入れる。ctx のキャンセルは書き込みに影響しない。
func (a *Audit) Record(ctx context.Context, e repository.AuditEntry) {
	if e.TS.IsZero() {
		e.TS = a.clock.Now()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		a.drop(e, "audit is closed")
		return
	}
	select {
	case a.queue <- auditItem{ctx: context.WithoutCancel(ctx), entry: e}:
	default:
		a.drop(e, "audit queue is full")
	}
}

// Dropped はキューの満杯・Close 後の Record で捨てた件数を返す。
func (a *Audit) Dropped() int64 { return a.dropped.Load() }

func (a *Audit) drop(e repository.AuditEntry, why string) {
	n := a.dropped.Add(1)
	a.log.Warn("audit entry dropped", "reason", why, "kind", e.Kind, "event", e.Event, "dropped_total", n)
}

// Flush は、呼び出し前に Record された件がすべて書き込まれるまで待つ。
func (a *Audit) Flush(ctx context.Context) error {
	flushed := make(chan struct{})
	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		return ErrAuditClosed
	}
	select {
	case a.queue <- auditItem{flushed: flushed}:
		a.mu.RUnlock()
	case <-ctx.Done():
		a.mu.RUnlock()
		return ctx.Err()
	}
	select {
	case <-flushed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close は新しい Record を断り、キューに残った件を書き込んでから goroutine を止める。
func (a *Audit) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Audit) run() {
	defer close(a.done)
	for it := range a.queue {
		if it.flushed != nil {
			close(it.flushed)
			continue
		}
		if _, err := a.store.Append(it.ctx, it.entry); err != nil {
			a.log.Error("audit append failed", "kind", it.entry.Kind, "event", it.entry.Event, "err", err)
		}
	}
}
