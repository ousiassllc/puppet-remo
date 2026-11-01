package domain

import (
	"fmt"
	"time"
)

// DefaultLeaseIdleTimeout はリースのアイドルタイムアウトの既定値（FR-L05、設定 lease.idle_timeout_sec の既定 300 秒）。
const DefaultLeaseIdleTimeout = 5 * time.Minute

// LeaseState はリースの状態。idle → held → idle（FR-L03）。承認が要るクライアントは
// idle → pending_approval → held になり、拒否・タイムアウト・取り下げでは pending_approval → idle に戻る（FR-L07。M3）。
type LeaseState string

const (
	LeaseIdle            LeaseState = "idle"
	LeasePendingApproval LeaseState = "pending_approval"
	LeaseHeld            LeaseState = "held"
)

// ReleaseReason はリースが idle に戻った理由。held → idle（FR-L05）と、pending_approval → idle（承認待ちの終わり。FR-L07）の両方に使う。
type ReleaseReason string

const (
	ReleaseExplicit    ReleaseReason = "explicit"     // 保持者による明示解放
	ReleaseIdleTimeout ReleaseReason = "idle_timeout" // アイドルタイムアウト
	ReleaseDisconnect  ReleaseReason = "disconnect"   // セッション切断・クライアントの削除・無効化
	ReleaseKillSwitch  ReleaseReason = "kill_switch"  // キルスイッチ
	ReleaseRevoked     ReleaseReason = "revoked"      // 他者による強制剥奪（FR-L06。M2）

	// 以下は pending_approval → idle のときだけ使う（FR-L07）。キルスイッチ・セッション切断（クライアントの削除・無効化を含む）は上の値を使う。
	ReleaseApprovalDenied  ReleaseReason = "approval_denied"  // ホストが拒否した
	ReleaseApprovalTimeout ReleaseReason = "approval_timeout" // 60 秒以内に応答がなかった
	ReleaseWithdrawn       ReleaseReason = "withdrawn"        // 要求側が取り下げた（ctx のキャンセル）
)

// Lease は保持中のリース。
type Lease struct {
	ID           string
	Holder       Actor
	Since        time.Time
	LastActivity time.Time // アイドルタイムアウトの起点
}

// LeaseEvent はリースの状態遷移 1 件。呼び出し側（service の LeaseManager）が監査記録と通知に使う。
type LeaseEvent struct {
	From, To LeaseState
	Lease    Lease         // 遷移したリース（解放時は解放直前の内容。承認待ちの間は Since が要求の時刻）
	Reason   ReleaseReason // To == LeaseIdle のときだけ設定される
	// By は強制剥奪（Reason == ReleaseRevoked）をした操作主体（host または web）。それ以外では空。
	By Actor
	At time.Time
}

// LeaseMachine はリースの状態機械（idle → [pending_approval →] held → idle）。外部 I/O もタイマーも持たない。
//
// 承認待ち（FR-L07）は Request で始まり、Approve で held に、Withdraw で idle に進む。承認待ちの間は、要求者本人を含め
// どのセッションの Acquire / Check も ErrLeaseHeldByOther で断る（待機はしない）。アイドルタイマーは held の間だけ。
//
// 強制剥奪（Revoke）された保持者は、次の Acquire が成功するまで覚えておく。その保持者の Check は、その間
// （リースが空きのままなら）ErrLeaseRequired ではなく ErrLeaseRevoked を返す（FR-L06）。他者が保持中なら、
// 通常どおり ErrLeaseHeldByOther。剥奪された保持者が自分で Acquire し直すと、覚えていた印は消える。
// goroutine セーフではないため、排他とアイドルタイマーは呼び出し側（LeaseManager）が受け持つ。
//
// アイドル超過は、期限（LastActivity + IdleTimeout）の時刻に達した瞬間から有効になる。
// タイマーの遅れに依存しないよう、Acquire / Check は呼ばれた時点で期限切れを先に解放する。
type LeaseMachine struct {
	clock       Clock
	ids         IDGen
	idleTimeout time.Duration
	cur         *Lease
	pending     *Lease // 承認待ちの要求。cur と同時には持たない
	revoked     *Actor // 最後に強制剥奪された保持者。次の Acquire の成功で忘れる
}

// NewLeaseMachine は idle 状態の LeaseMachine を作る。idleTimeout は正でなければならない。
func NewLeaseMachine(clock Clock, ids IDGen, idleTimeout time.Duration) (*LeaseMachine, error) {
	if clock == nil || ids == nil {
		return nil, ErrInvalidArgument.WithMsg("clock and id generator are required")
	}
	if idleTimeout <= 0 {
		return nil, ErrInvalidArgument.WithMsg(fmt.Sprintf("idle timeout must be positive, got %s", idleTimeout))
	}
	return &LeaseMachine{clock: clock, ids: ids, idleTimeout: idleTimeout}, nil
}

// SetIdleTimeout はアイドルタイムアウトを変える（設定 lease.idle_timeout_sec の変更）。timeout は正でなければならない。
// 保持中のリースの期限は、最後の活動時刻から新しい値で数え直す。新しい値で既に期限を過ぎていれば、次の
// Acquire / Check / Expire で解放される。
func (m *LeaseMachine) SetIdleTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return ErrInvalidArgument.WithMsg(fmt.Sprintf("idle timeout must be positive, got %s", timeout))
	}
	m.idleTimeout = timeout
	return nil
}

// State は現在の状態を返す。期限切れのリースは（まだ解放が記録されていなくても）idle とみなす。
func (m *LeaseMachine) State() LeaseState {
	if _, ok := m.Holder(); ok {
		return LeaseHeld
	}
	if m.pending != nil {
		return LeasePendingApproval
	}
	return LeaseIdle
}

// Pending は承認待ちの要求を返す（Lease.Holder が要求者、Since が要求の時刻）。承認待ちでなければ false。
func (m *LeaseMachine) Pending() (Lease, bool) {
	if m.pending == nil {
		return Lease{}, false
	}
	return *m.pending, true
}

// Holder は現在のリースを返す。期限切れのリースと idle のときは false。状態は変えない。
func (m *LeaseMachine) Holder() (Lease, bool) {
	if m.cur == nil || m.expired(m.clock.Now()) {
		return Lease{}, false
	}
	return *m.cur, true
}

// IdleDeadline は、保持中のリースがアイドル解放される時刻を返す。idle のときは false。
// LeaseManager がアイドルタイマーを張り直すために使う。
func (m *LeaseMachine) IdleDeadline() (time.Time, bool) {
	if m.cur == nil {
		return time.Time{}, false
	}
	return m.deadline(), true
}

// Acquire は who にリースを取得させる（idle → held。承認なし。FR-L03）。承認が要るときは Request を使う。
//   - 保持者になれない種別（mcp・web 以外）、セッション ID が空 → ErrInvalidArgument
//   - 他者が保持中、または承認待ち → ErrLeaseHeldByOther（待機しない）
//   - 自分が保持中 → 同じリースを返し、活動時刻を更新する（遷移なし）
//
// 期限切れのリースがあれば、先に idle_timeout で解放し、そのイベントを返す。
func (m *LeaseMachine) Acquire(who Actor) (Lease, []LeaseEvent, error) {
	if err := checkHolder(who); err != nil {
		return Lease{}, nil, err
	}
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.pending != nil {
		return Lease{}, events, m.heldByOther(m.pending.Holder)
	}
	if m.cur != nil {
		if !m.cur.Holder.SameSession(who) {
			return Lease{}, events, m.heldByOther(m.cur.Holder)
		}
		m.cur.LastActivity = now
		return *m.cur, events, nil
	}
	m.revoked = nil
	m.cur = &Lease{ID: m.ids.NewID(), Holder: who, Since: now, LastActivity: now}
	events = append(events, LeaseEvent{From: LeaseIdle, To: LeaseHeld, Lease: *m.cur, At: now})
	return *m.cur, events, nil
}

// Request は who の取得要求を承認待ちにする（idle → pending_approval。FR-L07）。承認が要るときの Acquire の代わりに使う。
// 返す Lease.ID は、承認（Approve）されたときのリースの ID になる。
//   - 保持者になれない種別、セッション ID が空 → ErrInvalidArgument
//   - 他者が保持中、または承認待ち → ErrLeaseHeldByOther（待機しない）
//   - 自分が保持中 → ErrInvalidArgument（承認は要らない。Acquire を使う）
//
// 期限切れのリースがあれば、先に idle_timeout で解放し、そのイベントを返す。
func (m *LeaseMachine) Request(who Actor) (Lease, []LeaseEvent, error) {
	if err := checkHolder(who); err != nil {
		return Lease{}, nil, err
	}
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.pending != nil {
		return Lease{}, events, m.heldByOther(m.pending.Holder)
	}
	if m.cur != nil {
		if m.cur.Holder.SameSession(who) {
			return Lease{}, events, ErrInvalidArgument.WithMsg("the actor already holds the lease")
		}
		return Lease{}, events, m.heldByOther(m.cur.Holder)
	}
	m.pending = &Lease{ID: m.ids.NewID(), Holder: who, Since: now, LastActivity: now}
	events = append(events, LeaseEvent{From: LeaseIdle, To: LeasePendingApproval, Lease: *m.pending, At: now})
	return *m.pending, events, nil
}

// Approve は id の承認待ちを承認する（pending_approval → held。FR-L07）。取り下げ済み・別の要求の id は ErrLeaseRequired。
func (m *LeaseMachine) Approve(id string) (Lease, []LeaseEvent, error) {
	if m.pending == nil || m.pending.ID != id {
		return Lease{}, nil, ErrLeaseRequired.WithMsg("the approval request is no longer pending")
	}
	now := m.clock.Now()
	m.revoked = nil
	m.cur = &Lease{ID: m.pending.ID, Holder: m.pending.Holder, Since: now, LastActivity: now}
	m.pending = nil
	return *m.cur, []LeaseEvent{{From: LeasePendingApproval, To: LeaseHeld, Lease: *m.cur, At: now}}, nil
}

// Withdraw は id の承認待ちを取り下げて idle に戻す（pending_approval → idle。FR-L07）。reason は拒否・タイムアウト・
// 取り下げ（ReleaseApprovalDenied・ReleaseApprovalTimeout・ReleaseWithdrawn）、キルスイッチ、セッション切断など。
// 承認待ちでない・別の要求の id なら何もしない。
func (m *LeaseMachine) Withdraw(id string, reason ReleaseReason) []LeaseEvent {
	if m.pending == nil || m.pending.ID != id {
		return nil
	}
	ev := LeaseEvent{From: LeasePendingApproval, To: LeaseIdle, Lease: *m.pending, Reason: reason, At: m.clock.Now()}
	m.pending = nil
	return []LeaseEvent{ev}
}

// WithdrawSession は who のセッションの承認待ちを取り下げる（pending_approval → idle。セッション切断・クライアントの削除など）。
// who が承認待ちの要求者でなければ何もしない。
func (m *LeaseMachine) WithdrawSession(who Actor, reason ReleaseReason) []LeaseEvent {
	if m.pending == nil || !m.pending.Holder.SameSession(who) {
		return nil
	}
	return m.Withdraw(m.pending.ID, reason)
}

// Release は who が保持者のときだけ解放する（held → idle）。保持者でなければ何もしない。
// reason は明示解放なら ReleaseExplicit、セッション切断なら ReleaseDisconnect など。
func (m *LeaseMachine) Release(who Actor, reason ReleaseReason) []LeaseEvent {
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.cur == nil || !m.cur.Holder.SameSession(who) {
		return events
	}
	return append(events, m.release(now, reason))
}

// ReleaseAll は保持者を問わず解放する（キルスイッチ・クライアントの削除など）。承認待ちがあれば取り下げる
// （pending_approval → idle）。idle なら何もしない。
func (m *LeaseMachine) ReleaseAll(reason ReleaseReason) []LeaseEvent {
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.pending != nil {
		return append(events, m.Withdraw(m.pending.ID, reason)...)
	}
	if m.cur == nil {
		return events
	}
	return append(events, m.release(now, reason))
}

// Revoke は by が保持者を強制剥奪する（held → idle。FR-L06）。idle なら何もせず成功する（冪等）。
// by の種別は host か web のみ（それ以外は ErrInvalidArgument）。agent の保持者の剥奪（タスクの中断を伴う）は M4。
// 剥奪された保持者は、次の Acquire が成功するまで Check が ErrLeaseRevoked を返す。
func (m *LeaseMachine) Revoke(by Actor) ([]LeaseEvent, error) {
	if !by.Kind.CanRevokeLease() {
		return nil, ErrInvalidArgument.WithMsg(fmt.Sprintf("actor kind %q cannot revoke the lease", by.Kind))
	}
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.cur == nil {
		return events, nil
	}
	holder := m.cur.Holder
	ev := m.release(now, ReleaseRevoked)
	ev.By = by
	m.revoked = &holder
	return append(events, ev), nil
}

// Check は who に以後の入力操作が許されるかを確認し、許されるなら活動時刻を更新する
// （アイドル解放の起点。FR-L05）。
//   - idle → ErrLeaseRequired（呼び出し側が暗黙取得を試みる。FR-L04）。who が強制剥奪された保持者なら ErrLeaseRevoked（次の Acquire まで。FR-L06）
//   - 他者が保持中、または承認待ち（要求者本人を含む）→ ErrLeaseHeldByOther
func (m *LeaseMachine) Check(who Actor) ([]LeaseEvent, error) {
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.pending != nil {
		return events, m.heldByOther(m.pending.Holder)
	}
	if m.cur == nil {
		if m.revoked != nil && m.revoked.SameSession(who) {
			return events, ErrLeaseRevoked
		}
		return events, ErrLeaseRequired
	}
	if !m.cur.Holder.SameSession(who) {
		return events, m.heldByOther(m.cur.Holder)
	}
	m.cur.LastActivity = now
	return events, nil
}

// Expire は期限切れのリースを idle_timeout で解放する。アイドルタイマーの発火時に呼ぶ。
// 期限に達していなければ何もしない。
func (m *LeaseMachine) Expire() []LeaseEvent {
	return m.expireAt(m.clock.Now())
}

func (m *LeaseMachine) deadline() time.Time { return m.cur.LastActivity.Add(m.idleTimeout) }

// expired は期限（LastActivity + idleTimeout）に達しているかを返す。ちょうど期限の時刻も超過とみなす。
func (m *LeaseMachine) expired(now time.Time) bool { return !now.Before(m.deadline()) }

func (m *LeaseMachine) expireAt(now time.Time) []LeaseEvent {
	if m.cur == nil || !m.expired(now) {
		return nil
	}
	return []LeaseEvent{m.release(now, ReleaseIdleTimeout)}
}

func (m *LeaseMachine) release(now time.Time, reason ReleaseReason) LeaseEvent {
	ev := LeaseEvent{From: LeaseHeld, To: LeaseIdle, Lease: *m.cur, Reason: reason, At: now}
	m.cur = nil
	return ev
}

// checkHolder は who がリースの保持者（または承認待ちの要求者）になれるかを確かめる。
func checkHolder(who Actor) error {
	if !who.Kind.CanHoldLease() {
		return ErrInvalidArgument.WithMsg(fmt.Sprintf("actor kind %q cannot hold the lease", who.Kind))
	}
	if who.SessionID == "" {
		return ErrInvalidArgument.WithMsg("session id is required")
	}
	return nil
}

// heldByOther は holder（保持者、または承認待ちの要求者）がいるために断るときのエラーを返す。
func (m *LeaseMachine) heldByOther(holder Actor) error {
	return ErrLeaseHeldByOther.
		WithMsg(fmt.Sprintf("the lease is held by %q", holder.Kind)).
		WithDetail("holder_kind", string(holder.Kind)).
		WithDetail("holder_label", holder.Label)
}
