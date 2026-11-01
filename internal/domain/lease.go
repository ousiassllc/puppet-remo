package domain

import (
	"fmt"
	"time"
)

// DefaultLeaseIdleTimeout はリースのアイドルタイムアウトの既定値（FR-L05、設定 lease.idle_timeout_sec の既定 300 秒）。
const DefaultLeaseIdleTimeout = 5 * time.Minute

// LeaseState はリースの状態。M1・M2 は idle → held → idle（FR-L03）。
// pending_approval（承認待ち）は M3 で加わる。
type LeaseState string

const (
	LeaseIdle LeaseState = "idle"
	LeaseHeld LeaseState = "held"
)

// ReleaseReason はリースが held → idle になった理由（FR-L05）。
type ReleaseReason string

const (
	ReleaseExplicit    ReleaseReason = "explicit"     // 保持者による明示解放
	ReleaseIdleTimeout ReleaseReason = "idle_timeout" // アイドルタイムアウト
	ReleaseDisconnect  ReleaseReason = "disconnect"   // セッション切断・クライアントの削除・無効化
	ReleaseKillSwitch  ReleaseReason = "kill_switch"  // キルスイッチ
	ReleaseRevoked     ReleaseReason = "revoked"      // 他者による強制剥奪（FR-L06。M2）
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
	Lease    Lease         // 遷移したリース（解放時は解放直前の内容）
	Reason   ReleaseReason // To == LeaseIdle のときだけ設定される
	// By は強制剥奪（Reason == ReleaseRevoked）をした操作主体（host または web）。それ以外では空。
	By Actor
	At time.Time
}

// LeaseMachine はリースの状態機械（idle → held → idle）。外部 I/O もタイマーも持たない。
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
	return LeaseIdle
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

// Acquire は who にリースを取得させる（idle → held。承認なし。FR-L03）。
//   - 保持者になれない種別（mcp・web 以外）、セッション ID が空 → ErrInvalidArgument
//   - 他者が保持中 → ErrLeaseHeldByOther（待機しない）
//   - 自分が保持中 → 同じリースを返し、活動時刻を更新する（遷移なし）
//
// 期限切れのリースがあれば、先に idle_timeout で解放し、そのイベントを返す。
func (m *LeaseMachine) Acquire(who Actor) (Lease, []LeaseEvent, error) {
	if !who.Kind.CanHoldLease() {
		return Lease{}, nil, ErrInvalidArgument.WithMsg(fmt.Sprintf("actor kind %q cannot hold the lease", who.Kind))
	}
	if who.SessionID == "" {
		return Lease{}, nil, ErrInvalidArgument.WithMsg("session id is required")
	}
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.cur != nil {
		if !m.cur.Holder.SameSession(who) {
			return Lease{}, events, m.heldByOther()
		}
		m.cur.LastActivity = now
		return *m.cur, events, nil
	}
	m.revoked = nil
	m.cur = &Lease{ID: m.ids.NewID(), Holder: who, Since: now, LastActivity: now}
	events = append(events, LeaseEvent{From: LeaseIdle, To: LeaseHeld, Lease: *m.cur, At: now})
	return *m.cur, events, nil
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

// ReleaseAll は保持者を問わず解放する（キルスイッチ・クライアントの削除など）。idle なら何もしない。
func (m *LeaseMachine) ReleaseAll(reason ReleaseReason) []LeaseEvent {
	now := m.clock.Now()
	events := m.expireAt(now)
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
//   - 他者が保持中 → ErrLeaseHeldByOther
func (m *LeaseMachine) Check(who Actor) ([]LeaseEvent, error) {
	now := m.clock.Now()
	events := m.expireAt(now)
	if m.cur == nil {
		if m.revoked != nil && m.revoked.SameSession(who) {
			return events, ErrLeaseRevoked
		}
		return events, ErrLeaseRequired
	}
	if !m.cur.Holder.SameSession(who) {
		return events, m.heldByOther()
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

func (m *LeaseMachine) heldByOther() error {
	return ErrLeaseHeldByOther.
		WithMsg(fmt.Sprintf("the lease is held by %q", m.cur.Holder.Kind)).
		WithDetail("holder_kind", string(m.cur.Holder.Kind)).
		WithDetail("holder_label", m.cur.Holder.Label)
}
