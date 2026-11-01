package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// LeaseManager は操作リース（FR-L01〜L05）の状態の唯一の所有者（docs/components/overview.md §5.2）。
//
// M2 の範囲: 保持者種別は mcp と web、承認なしで idle → held、アイドルタイマーとキルスイッチと明示解放・切断・強制剥奪（Revoke）で解放。
// pending_approval（M3）と agent の保持者（M4）は持たない。
// 保持中は LeaseConfig.Power でスリープと画面オフを抑止する（NFR-A05）。どの解放の経路でも保持が終われば解除する。
type LeaseManager interface {
	// Acquire は idle → held（承認なし）。他者が保持中は ErrLeaseHeldByOther。同じ保持者の再取得は活動として数える。
	// キルスイッチの発動中は ErrKillSwitchActive。
	Acquire(ctx context.Context, who domain.Actor) (Lease, error)
	// Release は保持者のみ。保持していなければ何もしない。
	Release(who domain.Actor)
	// ReleaseSession はセッション切断で保持者のリースを解放する（FR-L05）。保持者でなければ何もしない。
	// 監査の理由が explicit ではなく disconnect になる以外は Release と同じ。
	ReleaseSession(who domain.Actor)
	// Revoke は by が保持者を強制剥奪する（FR-L06）。by は host（ホスト GUI）か web（Web クライアント）で、それ以外は ErrInvalidArgument。
	// 保持者がいなければ何もせず nil（冪等）。剥奪された保持者の Check は、次の Acquire が成功するまで（リースが空きのあいだ）ErrLeaseRevoked を返す。
	// 監査は by を操作主体とする lease.revoke。agent の保持者の剥奪（タスクの中断）は M4。
	Revoke(by domain.Actor) error
	// Check は以後の入力操作が許されるかの確認。活動時刻を更新する（アイドル解放の起点）。
	// ErrLeaseRequired / ErrLeaseHeldByOther / ErrLeaseRevoked。
	Check(who domain.Actor) error
	Holder() (Holder, bool)
	// ReleaseAll はキルスイッチ・シャットダウン用。reason は domain.ReleaseReason の値（監査に残る）。
	ReleaseAll(reason string)
	// SetIdleTimeout はアイドルタイムアウトを変える（設定 lease.idle_timeout_sec の保存時）。timeout は正であること
	// （そうでなければ ErrInvalidArgument で何も変えない）。保持中のリースは新しい値で数え直し、既に期限を過ぎていれば
	// すぐ idle_timeout で解放する。
	SetIdleTimeout(timeout time.Duration) error
	// Subscribe はリースの変化（取得・解放・剥奪）を受け取るチャネルを返す。容量は小さく、遅い購読者には捨てる。
	// 購読の解除はない（購読者はプロセスの寿命と同じ GUI だけ）。
	Subscribe() <-chan domain.LeaseEvent
}

// Lease は取得したリース。
type Lease struct {
	ID     string
	Holder domain.Actor
	Since  time.Time
}

// Holder は現在のリース保持者（GUI・get_control_status 用）。
type Holder struct {
	LeaseID string
	Actor   domain.Actor
	Since   time.Time
	// IdleRemaining は、これ以上活動がなくてアイドル解放されるまでの残り時間（Holder を呼んだ時点）。
	IdleRemaining time.Duration
}

// KillSwitchState はキルスイッチの発動状態を読む窓口。
type KillSwitchState interface{ Active() bool }

// AfterFunc は d 後に f を別 goroutine で呼ぶタイマーを作り、その取り消し関数を返す（time.AfterFunc の差し替え口）。
type AfterFunc func(d time.Duration, f func()) (stop func() bool)

// LeaseConfig は NewLeaseManager の設定。
type LeaseConfig struct {
	Clock domain.Clock
	IDs   domain.IDGen
	// IdleTimeout は設定 lease.idle_timeout_sec の値（LeaseIdleTimeout で読む）。正であること。
	IdleTimeout time.Duration
	Audit       AuditRecorder   // nil なら記録しない
	Kill        KillSwitchState // nil なら常に非発動
	AfterFunc   AfterFunc       // nil なら time.AfterFunc
	// Power は保持中だけスリープと画面オフを抑止する（NFR-A05）。nil なら抑止しない。
	Power  platform.Power
	Logger *slog.Logger // nil なら slog.Default()
}

// HostActor はホスト所有者（ホスト GUI）の操作主体。強制剥奪（LeaseManager.Revoke）の by に使い、監査の actor_type は host になる。
var HostActor = domain.Actor{Kind: domain.ActorHost, Label: hostLabel}

const leaseSubscriberBuffer = 16

type leaseManager struct {
	clock domain.Clock
	after AfterFunc
	kill  KillSwitchState
	audit AuditRecorder
	power platform.Power
	log   *slog.Logger

	// mu は全遷移（machine・timer・pending）を直列化する。ロックを持ったまま Audit・購読者を呼ばない。
	mu       sync.Mutex
	machine  *domain.LeaseMachine
	timer    func() bool // アイドルタイマーの取り消し。未起動は nil
	timerGen uint64      // 古いタイマーのコールバックを無視するための世代
	pending  []domain.LeaseEvent

	// emitMu は pending の配信（監査・購読者）を直列化し、遷移の順序どおりに届ける。
	emitMu sync.Mutex
	awake  bool // power へ最後に伝えた値。emitMu を持って読み書きする
	subMu  sync.Mutex
	subs   []chan domain.LeaseEvent
}

// NewLeaseManager は LeaseManager を返す。
func NewLeaseManager(cfg LeaseConfig) (LeaseManager, error) {
	machine, err := domain.NewLeaseMachine(cfg.Clock, cfg.IDs, cfg.IdleTimeout)
	if err != nil {
		return nil, err
	}
	m := &leaseManager{
		clock:   cfg.Clock,
		after:   cfg.AfterFunc,
		kill:    cfg.Kill,
		audit:   cfg.Audit,
		power:   cfg.Power,
		log:     cfg.Logger,
		machine: machine,
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.after == nil {
		m.after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return m, nil
}

func (m *leaseManager) Acquire(ctx context.Context, who domain.Actor) (Lease, error) {
	m.mu.Lock()
	// キルスイッチの確認はロックの中で行う。Trigger は状態を立ててから ReleaseAll（同じロック）を呼ぶため、
	// 取得が ReleaseAll の後に成功することはない。
	if m.kill != nil && m.kill.Active() {
		m.mu.Unlock()
		m.logDenied(who, domain.ErrKillSwitchActive)
		m.record(ctx, leaseDeniedAuditEntry(who, m.clock.Now(), domain.ErrKillSwitchActive))
		return Lease{}, domain.ErrKillSwitchActive
	}
	l, events, err := m.machine.Acquire(who)
	m.settleLocked(events)
	m.mu.Unlock()
	m.flush()
	if err != nil {
		m.logDenied(who, err)
		m.record(ctx, leaseDeniedAuditEntry(who, m.clock.Now(), err))
		return Lease{}, err
	}
	return Lease{ID: l.ID, Holder: l.Holder, Since: l.Since}, nil
}

func (m *leaseManager) Release(who domain.Actor) { m.release(who, domain.ReleaseExplicit) }

func (m *leaseManager) ReleaseSession(who domain.Actor) { m.release(who, domain.ReleaseDisconnect) }

func (m *leaseManager) release(who domain.Actor, reason domain.ReleaseReason) {
	m.mu.Lock()
	m.settleLocked(m.machine.Release(who, reason))
	m.mu.Unlock()
	m.flush()
}

func (m *leaseManager) Revoke(by domain.Actor) error {
	m.mu.Lock()
	events, err := m.machine.Revoke(by)
	m.settleLocked(events)
	m.mu.Unlock()
	m.flush()
	return err
}

func (m *leaseManager) ReleaseAll(reason string) {
	m.mu.Lock()
	m.settleLocked(m.machine.ReleaseAll(domain.ReleaseReason(reason)))
	m.mu.Unlock()
	m.flush()
}

func (m *leaseManager) SetIdleTimeout(timeout time.Duration) error {
	m.mu.Lock()
	if err := m.machine.SetIdleTimeout(timeout); err != nil {
		m.mu.Unlock()
		return err
	}
	// 張ってあるタイマーは古い期限のままなので取り消し、新しい期限で張り直す。
	m.stopTimerLocked()
	m.settleLocked(m.machine.Expire())
	m.mu.Unlock()
	m.flush()
	return nil
}

func (m *leaseManager) Check(who domain.Actor) error {
	m.mu.Lock()
	events, err := m.machine.Check(who)
	m.settleLocked(events)
	m.mu.Unlock()
	m.flush()
	return err
}

func (m *leaseManager) Holder() (Holder, bool) {
	m.mu.Lock()
	m.settleLocked(m.machine.Expire())
	l, ok := m.machine.Holder()
	remaining := time.Duration(0)
	if ok {
		if deadline, has := m.machine.IdleDeadline(); has {
			remaining = max(0, deadline.Sub(m.clock.Now()))
		}
	}
	m.mu.Unlock()
	m.flush()
	if !ok {
		return Holder{}, false
	}
	return Holder{LeaseID: l.ID, Actor: l.Holder, Since: l.Since, IdleRemaining: remaining}, true
}

func (m *leaseManager) Subscribe() <-chan domain.LeaseEvent {
	ch := make(chan domain.LeaseEvent, leaseSubscriberBuffer)
	m.subMu.Lock()
	m.subs = append(m.subs, ch)
	m.subMu.Unlock()
	return ch
}

// settleLocked は遷移で出たイベントを配信待ちに積み、タイマーを状態に合わせる。mu を持って呼ぶ。
func (m *leaseManager) settleLocked(events []domain.LeaseEvent) {
	m.pending = append(m.pending, events...)
	if _, held := m.machine.IdleDeadline(); !held {
		m.stopTimerLocked()
		return
	}
	m.armTimerLocked()
}

func (m *leaseManager) stopTimerLocked() {
	if m.timer != nil {
		m.timer()
		m.timer = nil
	}
	m.timerGen++
}

// armTimerLocked はアイドルタイマーを 1 本だけ起動する。Check のたびに作り直さず、満了時に残り時間を見て延長する。
func (m *leaseManager) armTimerLocked() {
	if m.timer != nil {
		return
	}
	deadline, held := m.machine.IdleDeadline()
	if !held {
		return
	}
	m.timerGen++
	gen := m.timerGen
	m.timer = m.after(max(0, deadline.Sub(m.clock.Now())), func() { m.onTimer(gen) })
}

func (m *leaseManager) onTimer(gen uint64) {
	m.mu.Lock()
	if gen != m.timerGen {
		m.mu.Unlock()
		return
	}
	m.timer = nil
	m.settleLocked(m.machine.Expire())
	m.mu.Unlock()
	m.flush()
}

// flush は積まれたイベントを、遷移の順序どおりに監査と購読者へ配る。
func (m *leaseManager) flush() {
	m.emitMu.Lock()
	defer m.emitMu.Unlock()
	m.mu.Lock()
	events := m.pending
	m.pending = nil
	_, held := m.machine.IdleDeadline()
	m.mu.Unlock()
	m.keepAwake(held)
	for _, ev := range events {
		m.logEvent(ev)
		m.record(context.Background(), leaseAuditEntry(ev))
		m.publish(ev)
	}
}

// keepAwake は保持の有無に合わせてスリープ抑止を切り替える（NFR-A05）。変わったときだけ power を呼ぶので、二重に入れない。
// 取得・解放・アイドル満了・キルスイッチ・切断のどれも flush を通るため、ここ 1 か所で足りる。emitMu を持って呼ぶ。
func (m *leaseManager) keepAwake(on bool) {
	if m.power == nil || m.awake == on {
		return
	}
	m.awake = on
	m.power.KeepAwake(on)
}

// logEvent はリースの取得・解放・剥奪を運用ログに出す（監査とは別。Check の活動の更新は出さない）。
func (m *leaseManager) logEvent(ev domain.LeaseEvent) {
	h := ev.Lease.Holder
	if ev.To == domain.LeaseHeld {
		m.log.Info("lease acquired", "lease_id", ev.Lease.ID, "holder_kind", string(h.Kind), "holder", h.Label, "session", h.SessionID)
		return
	}
	attrs := []any{"lease_id", ev.Lease.ID, "reason", string(ev.Reason), "holder_kind", string(h.Kind), "holder", h.Label,
		"held_for", ev.At.Sub(ev.Lease.Since).Round(time.Second).String()}
	if ev.Reason == domain.ReleaseRevoked {
		attrs = append(attrs, "revoked_by_kind", string(ev.By.Kind), "revoked_by", ev.By.Label)
	}
	m.log.Info("lease released", attrs...)
}

func (m *leaseManager) logDenied(who domain.Actor, err error) {
	m.log.Info("lease acquire denied", "code", string(domain.CodeOf(err)), "holder_kind", string(who.Kind), "holder", who.Label)
}

func (m *leaseManager) publish(ev domain.LeaseEvent) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (m *leaseManager) record(ctx context.Context, e repository.AuditEntry) {
	if m.audit != nil {
		m.audit.Record(ctx, e)
	}
}
