package service

import (
	"cmp"
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

// Audit は監査ログの追記（FR-AU01）。書き込みは goroutine 1 本が行い、Record は
// バッファ付きチャネルへ非ブロッキングで入れる。満杯のときは呼び出しを待たせずに捨て、ドロップ数を slog で警告する。
// サムネイル付きの RecordWithThumb は、同じ goroutine が Append → ThumbStore.Save → AttachThumb → 容量の確認の順に行う（audit_thumb.go）。
type Audit struct {
	store repository.Audit
	clock domain.Clock
	log   *slog.Logger

	mu     sync.RWMutex // closed と queue のクローズを守る
	closed bool
	queue  chan auditItem

	thumbs thumbSaver // Thumbs が無ければゼロ値（サムネイルは保存しない）

	dropped atomic.Int64
	done    chan struct{}
}

// auditItem はキューの 1 要素。flushed が非 nil のものは Flush の目印で、entry は無い。
// thumb が非 nil なら、entry の追記のあとに保存する（pending の枠を 1 つ使っている）。
type auditItem struct {
	ctx     context.Context
	entry   repository.AuditEntry
	thumb   *Thumbnail
	flushed chan struct{}
}

// AuditConfig は NewAudit の設定。
type AuditConfig struct {
	Store     repository.Audit
	Clock     domain.Clock
	Logger    *slog.Logger // nil なら slog.Default()
	QueueSize int          // 0 なら 256

	// Thumbs が非 nil なら、サムネイルを保存できる（FR-AU02）。nil なら RecordWithThumb は画像を捨てて Record と同じに動く。
	// Thumbs があるとき IDs（ファイル名の ULID）は必須。Settings は audit.thumbnails_enabled（既定 true）の読み取りで、nil なら常に有効。
	Thumbs   repository.ThumbStore
	IDs      domain.IDGen
	Settings repository.Settings
	// PurgeOverCap は保存のたびの容量の確認（Maintenance.PurgeThumbsOverCap）。nil なら確認しない。
	PurgeOverCap func(ctx context.Context) error
	// MaxPendingThumbs は、キューにある（まだ保存していない）サムネイルの件数の上限（画像のバイト列でメモリが増え続けないため）。
	// 超えた分は画像だけを捨てて、エントリは追記する。0 なら 32。
	MaxPendingThumbs int
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
	if cfg.Thumbs != nil {
		if cfg.IDs == nil {
			return nil, domain.ErrInvalidArgument.WithMsg("audit thumbnails require an id generator")
		}
		a.thumbs = thumbSaver{
			store: cfg.Thumbs, ids: cfg.IDs, settings: cfg.Settings, purgeOverCap: cfg.PurgeOverCap,
			maxPending: int32(cmp.Or(cfg.MaxPendingThumbs, defaultMaxPendingThumbs)),
		}
	}
	go a.run()
	return a, nil
}

// Record は e をキューへ入れる。TS がゼロなら現在時刻を入れる。ctx のキャンセルは書き込みに影響しない。
func (a *Audit) Record(ctx context.Context, e repository.AuditEntry) {
	a.enqueue(ctx, e, nil)
}

// enqueue は e を（thumb があれば付けて）キューへ入れる。満杯・Close 後は e を捨て、thumb の枠を返す。
func (a *Audit) enqueue(ctx context.Context, e repository.AuditEntry, thumb *Thumbnail) {
	if e.TS.IsZero() {
		e.TS = a.clock.Now()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		a.thumbs.release(thumb)
		a.drop(e, "audit is closed")
		return
	}
	select {
	case a.queue <- auditItem{ctx: context.WithoutCancel(ctx), entry: e, thumb: thumb}:
	default:
		a.thumbs.release(thumb)
		a.drop(e, "audit queue is full")
	}
}

// Dropped はキューの満杯・Close 後の Record で捨てた件数を返す。
func (a *Audit) Dropped() int64 { return a.dropped.Load() }

// drop は e を捨てたことを記録する。launch_app は、プロセスを作ったあとに記録する（Control.Launch）ため、捨てると、起動が
// 成立したのに監査に残らない（NFR-S20 の残存リスク）。運用で気づけるよう Error で出す。それ以外は Warn。
func (a *Audit) drop(e repository.AuditEntry, why string) {
	n := a.dropped.Add(1)
	level := slog.LevelWarn
	if e.Event == launchAppAuditEvent {
		level = slog.LevelError
	}
	a.log.Log(context.Background(), level, "audit entry dropped", "reason", why, "kind", e.Kind, "event", e.Event, "dropped_total", n)
}

// launchAppAuditEvent は launch_app の監査のイベント名（ToolAuditEntry の "tool." + ツール名）。
const launchAppAuditEvent = "tool.launch_app"

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
		id, err := a.store.Append(it.ctx, it.entry)
		if err != nil {
			a.log.Error("audit append failed", "kind", it.entry.Kind, "event", it.entry.Event, "err", err)
		}
		if it.thumb != nil {
			if err == nil {
				a.saveThumb(it.ctx, id, it.entry, *it.thumb)
			}
			a.thumbs.release(it.thumb)
		}
	}
}
