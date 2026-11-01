package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// LeaseManager は操作リース（FR-L01〜L05）の状態の唯一の所有者（docs/components/overview.md §5.2）。
//
// 保持者種別は mcp と web（agent は M4）。解放はアイドルタイマー・キルスイッチ・明示解放・切断・強制剥奪（Revoke）。
// LeaseConfig に Approval と Clients があるとき（M3。FR-L07）、Acquire はクライアントの承認モードに従い、承認が要るなら
// idle → pending_approval にして、ロックを手放してから Approval.Request でホストの結果を待つ（最大 60 秒）。
// 許可で held、拒否・タイムアウト・取り下げ（ctx のキャンセル・キルスイッチ・セッション切断・クライアントの削除）で idle に戻る。
// 承認待ちの間は、要求者本人を含むすべての Acquire・Check を ErrLeaseHeldByOther で断り、明示解放（Release）は何もしない。
// 保持中は LeaseConfig.Power でスリープと画面オフを抑止する（NFR-A05）。どの解放の経路でも保持が終われば解除する。
type LeaseManager interface {
	// Acquire は idle → held。承認が要るクライアント（LeaseConfig.Approval があり、承認モードが always、または approved_at が無い
	// first_time）は idle → pending_approval → held で、ホストの結果が出るまで最大 60 秒ブロックする。
	// 拒否は ErrApprovalDenied、タイムアウトは ErrApprovalTimeout、待ち中の ctx のキャンセルは ctx.Err()、キルスイッチの発動は
	// ErrKillSwitchActive（後から許可されても held にならない）、セッション切断・クライアントの削除による取り下げは ErrLeaseRequired。
	// 他者が保持中、または承認待ちは ErrLeaseHeldByOther。同じ保持者の再取得は承認を要さず、活動として数える。
	// キルスイッチの発動中は ErrKillSwitchActive。承認系の監査（取得要求・拒否・タイムアウト）はイベント名が未決のため記録しない。
	Acquire(ctx context.Context, who domain.Actor) (Lease, error)
	// Release は保持者のみ。保持していなければ何もしない。
	Release(who domain.Actor)
	// ReleaseSession はセッション切断で保持者のリースを解放する（FR-L05）。who の承認待ちがあれば取り下げて idle に戻す。
	// どちらでもなければ何もしない。監査の理由が explicit ではなく disconnect になる以外は Release と同じ。
	ReleaseSession(who domain.Actor)
	// Revoke は by が保持者を強制剥奪する（FR-L06）。by は host（ホスト GUI）か web（Web クライアント）で、それ以外は ErrInvalidArgument。
	// 保持者がいなければ何もせず nil（冪等）。剥奪された保持者の Check は、次の Acquire が成功するまで（リースが空きのあいだ）ErrLeaseRevoked を返す。
	// 監査は by を操作主体とする lease.revoke。agent の保持者の剥奪（タスクの中断）は M4。
	Revoke(by domain.Actor) error
	// Check は以後の入力操作が許されるかの確認。活動時刻を更新する（アイドル解放の起点）。
	// ErrLeaseRequired / ErrLeaseHeldByOther / ErrLeaseRevoked。
	Check(who domain.Actor) error
	Holder() (Holder, bool)
	// Pending は承認待ち（pending_approval）の要求者を返す。承認待ちでなければ false。Holder は held のときだけ返す。
	Pending() (PendingLease, bool)
	// ReleaseAll はキルスイッチ・シャットダウン用。承認待ちがあれば取り下げる。reason は domain.ReleaseReason の値（監査に残る）。
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

// PendingLease は承認待ちの要求（GUI・状態表示用）。
type PendingLease struct {
	LeaseID string // 承認されたときのリースの ID
	Actor   domain.Actor
	Since   time.Time // 要求の時刻
}

// LeaseClients は LeaseManager が承認モードの適用に使うクライアントの窓口。repository.Clients が満たす。
type LeaseClients interface {
	Get(ctx context.Context, id string) (domain.Client, error)
	// MarkApproved は first_time のクライアントが承認された時刻を記録する（clients.approved_at）。
	MarkApproved(ctx context.Context, id string, at time.Time) error
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
	// Approval と Clients は両方あるときだけ承認モードを適用する（FR-L07。M3）。両方 nil なら承認なしで取得させる（M1・M2 の動き）。
	// 片方だけの指定はエラー。
	Approval Approval
	Clients  LeaseClients
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
	// approval・clients は nil なら承認なし。
	approval Approval
	clients  LeaseClients

	// mu は全遷移（machine・timer・pending）を直列化する。ロックを持ったまま Approval・Audit・購読者を呼ばない。
	mu       sync.Mutex
	machine  *domain.LeaseMachine
	timer    func() bool // アイドルタイマーの取り消し。未起動は nil
	timerGen uint64      // 古いタイマーのコールバックを無視するための世代
	pending  []domain.LeaseEvent
	// cancelWait は承認待ちの Acquire が Approval.Request を待つ ctx の取り消し。承認待ちが終わる遷移で呼んで外す。
	// 取り下げ（キルスイッチ・切断・クライアントの削除）で待ちを起こすために使う。
	cancelWait context.CancelFunc

	// emitMu は pending の配信（監査・購読者）を直列化し、遷移の順序どおりに届ける。
	emitMu sync.Mutex
	awake  bool // power へ最後に伝えた値。emitMu を持って読み書きする
	subMu  sync.Mutex
	subs   []chan domain.LeaseEvent
}

// NewLeaseManager は LeaseManager を返す。
func NewLeaseManager(cfg LeaseConfig) (LeaseManager, error) {
	if (cfg.Approval == nil) != (cfg.Clients == nil) {
		return nil, errors.New("service: lease manager requires both Approval and Clients, or neither")
	}
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

		approval: cfg.Approval,
		clients:  cfg.Clients,
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
	// クライアントの読み取りは I/O なので、ロックの外で行う。
	needs, err := m.approvalNeeds(ctx, who)
	if err != nil {
		m.logDenied(who, err)
		m.record(ctx, leaseDeniedAuditEntry(who, m.clock.Now(), err))
		return Lease{}, err
	}
	m.mu.Lock()
	// キルスイッチの確認はロックの中で行う。Trigger は状態を立ててから ReleaseAll（同じロック）を呼ぶため、
	// 取得が ReleaseAll の後に成功することはない。
	if m.kill != nil && m.kill.Active() {
		m.mu.Unlock()
		m.logDenied(who, domain.ErrKillSwitchActive)
		m.record(ctx, leaseDeniedAuditEntry(who, m.clock.Now(), domain.ErrKillSwitchActive))
		return Lease{}, domain.ErrKillSwitchActive
	}
	var (
		l       domain.Lease
		events  []domain.LeaseEvent
		waitCtx context.Context
	)
	if needs.required && !m.holdsLocked(who) {
		l, events, err = m.machine.Request(who)
		if err == nil {
			var cancel context.CancelFunc
			waitCtx, cancel = context.WithCancel(ctx)
			m.cancelWait = cancel
		}
	} else {
		l, events, err = m.machine.Acquire(who)
	}
	m.settleLocked(events)
	m.mu.Unlock()
	m.flush()
	if err != nil {
		m.logDenied(who, err)
		m.record(ctx, leaseDeniedAuditEntry(who, m.clock.Now(), err))
		return Lease{}, err
	}
	if waitCtx != nil {
		return m.awaitApproval(ctx, waitCtx, who, l.ID, needs)
	}
	return Lease{ID: l.ID, Holder: l.Holder, Since: l.Since}, nil
}

// approvalNeed は取得者のクライアントに対する承認の要否。
type approvalNeed struct {
	required  bool // 承認が要る
	firstTime bool // 承認されたら approved_at を記録する（first_time）
}

// approvalNeeds は who の取得に承認が要るかを、クライアントの承認モードと approved_at から決める（FR-L07）。
// 承認を適用しない構成、保持者になれない who（machine が ErrInvalidArgument にする）は要らない。
// クライアントが読めない（未登録・削除済み・読み取りの失敗）ときは取得させない（fail closed）。
func (m *leaseManager) approvalNeeds(ctx context.Context, who domain.Actor) (approvalNeed, error) {
	if m.approval == nil || !who.Kind.CanHoldLease() || who.SessionID == "" {
		return approvalNeed{}, nil
	}
	if who.ClientID == "" {
		return approvalNeed{}, ErrDeviceNotRegistered
	}
	c, err := m.clients.Get(ctx, who.ClientID)
	if errors.Is(err, repository.ErrNotFound) {
		return approvalNeed{}, ErrDeviceNotRegistered
	}
	if err != nil {
		return approvalNeed{}, fmt.Errorf("read client for the lease approval: %w", err)
	}
	return approvalNeed{
		required:  domain.NeedsApproval(c.ApprovalMode, c.ApprovedAt),
		firstTime: c.ApprovalMode == domain.ApprovalFirstTime,
	}, nil
}

// holdsLocked は who が保持者として held のときに true。mu を持って呼ぶ。
func (m *leaseManager) holdsLocked(who domain.Actor) bool {
	l, ok := m.machine.Holder()
	return ok && l.Holder.SameSession(who)
}

// awaitApproval は承認待ち（id）の結果を、ロックを持たずに待ち、結果でリースを held にするか idle に戻す。
// waitCtx は取り下げ（settleLocked が cancelWait を呼ぶ）と ctx のキャンセルの両方で終わる。
func (m *leaseManager) awaitApproval(ctx, waitCtx context.Context, who domain.Actor, id string, needs approvalNeed) (Lease, error) {
	dec, reqErr := m.approval.Request(waitCtx, ApprovalRequest{Kind: ApprovalKindLease, Who: who})

	m.mu.Lock()
	if p, ok := m.machine.Pending(); !ok || p.ID != id {
		// 待っている間に、キルスイッチ・切断・クライアントの削除などで取り下げられた（遷移は済んで配信されている）。
		m.mu.Unlock()
		err := m.withdrawnError(ctx)
		m.logDenied(who, err)
		return Lease{}, err
	}
	var (
		l      domain.Lease
		events []domain.LeaseEvent
		err    error
	)
	switch {
	case m.kill != nil && m.kill.Active():
		// 承認が後から許可されても held にしない。
		events, err = m.machine.Withdraw(id, domain.ReleaseKillSwitch), domain.ErrKillSwitchActive
	case errors.Is(reqErr, domain.ErrApprovalTimeout):
		events, err = m.machine.Withdraw(id, domain.ReleaseApprovalTimeout), domain.ErrApprovalTimeout
	case reqErr != nil: // ctx のキャンセル
		events, err = m.machine.Withdraw(id, domain.ReleaseWithdrawn), reqErr
	case !dec.Allow:
		events, err = m.machine.Withdraw(id, domain.ReleaseApprovalDenied), domain.ErrApprovalDenied
	default:
		l, events, err = m.machine.Approve(id)
	}
	m.settleLocked(events)
	m.mu.Unlock()
	m.flush()
	if err != nil {
		m.logDenied(who, err)
		return Lease{}, err
	}
	if needs.firstTime {
		m.markApproved(ctx, who)
	}
	return Lease{ID: l.ID, Holder: l.Holder, Since: l.Since}, nil
}

// markApproved は first_time のクライアントの承認を approved_at に記録する。記録に失敗しても、承認済みのリースは取り消さない
// （次の取得でもう一度承認を求めることになる）。
func (m *leaseManager) markApproved(ctx context.Context, who domain.Actor) {
	// 呼び出し側の ctx が承認の直後にキャンセルされても、承認の記録は残す。
	if err := m.clients.MarkApproved(context.WithoutCancel(ctx), who.ClientID, m.clock.Now()); err != nil {
		m.log.Warn("lease approval could not be recorded", "client_id", who.ClientID, "error", err)
	}
}

// withdrawnError は、承認待ちが他の経路で取り下げられたときに Acquire が返すエラー。
func (m *leaseManager) withdrawnError(ctx context.Context) error {
	switch {
	case m.kill != nil && m.kill.Active():
		return domain.ErrKillSwitchActive
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return domain.ErrLeaseRequired.WithMsg("the approval request was withdrawn")
}

func (m *leaseManager) Release(who domain.Actor) { m.release(who, domain.ReleaseExplicit) }

func (m *leaseManager) ReleaseSession(who domain.Actor) { m.release(who, domain.ReleaseDisconnect) }

func (m *leaseManager) release(who domain.Actor, reason domain.ReleaseReason) {
	m.mu.Lock()
	events := m.machine.Release(who, reason)
	if reason == domain.ReleaseDisconnect {
		// 切断は承認待ちも取り下げる。明示解放（Release）は承認待ちに触れない。
		events = append(events, m.machine.WithdrawSession(who, reason)...)
	}
	m.settleLocked(events)
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

func (m *leaseManager) Pending() (PendingLease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.machine.Pending()
	if !ok {
		return PendingLease{}, false
	}
	return PendingLease{LeaseID: p.ID, Actor: p.Holder, Since: p.Since}, true
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
	for _, ev := range events {
		if ev.From == domain.LeasePendingApproval && m.cancelWait != nil {
			// 承認待ちが終わった。待っている Acquire を起こす（終わらせた本人の場合は、すでに待っていないので無害）。
			m.cancelWait()
			m.cancelWait = nil
		}
	}
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
		if !isApprovalPhase(ev) {
			m.record(context.Background(), leaseAuditEntry(ev))
		}
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

// isApprovalPhase は承認待ちの開始（idle → pending_approval）と、held にならない終わり（pending_approval → idle）を返す。
// これらの監査のイベント名は未決（docs/architecture/er.md）なので記録しない。pending_approval → held は lease.acquire として記録する。
func isApprovalPhase(ev domain.LeaseEvent) bool {
	return ev.To == domain.LeasePendingApproval || ev.From == domain.LeasePendingApproval && ev.To == domain.LeaseIdle
}

// logEvent はリースの取得・解放・剥奪・承認待ちを運用ログに出す（監査とは別。Check の活動の更新は出さない）。
func (m *leaseManager) logEvent(ev domain.LeaseEvent) {
	h := ev.Lease.Holder
	if ev.To == domain.LeasePendingApproval {
		m.log.Info("lease approval requested", "lease_id", ev.Lease.ID, "holder_kind", string(h.Kind), "holder", h.Label, "session", h.SessionID)
		return
	}
	if ev.From == domain.LeasePendingApproval && ev.To == domain.LeaseIdle {
		m.log.Info("lease approval ended", "lease_id", ev.Lease.ID, "reason", string(ev.Reason), "holder_kind", string(h.Kind), "holder", h.Label,
			"waited", ev.At.Sub(ev.Lease.Since).Round(time.Second).String())
		return
	}
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
