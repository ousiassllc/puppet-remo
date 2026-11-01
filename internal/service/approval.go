package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// DefaultApprovalTimeout は承認待ちのタイムアウト（FR-L07、FR-H05: 60 秒で拒否扱い）。
const DefaultApprovalTimeout = 60 * time.Second

// 承認要求の種類（ApprovalRequest.Kind）。M3 で使うのは lease だけ。safety_check・judge_action は M4。
const ApprovalKindLease = "lease"

// Approval はホストの承認待ちの要求を持つ（docs/components/overview.md §5.3）。UI を知らない:
// 要求を作るたびに Bus へ approval を出し、ダイアログを開くのは購読側（app）、結果を返すのは Resolve の呼び出し側（GUI ルーター）。
// 状態（承認待ちの要求）は sync.Mutex で守り、ロックを持ったまま Bus・呼び出し側を呼ばない。
type Approval interface {
	// Request は要求を登録し、ホストの結果を待つ。許可は Decision{Allow: true}、拒否は Decision{Allow: false}（エラーなし）。
	// 60 秒（ApprovalConfig.Timeout）で ErrApprovalTimeout。ctx がキャンセルされたら要求を取り下げて ctx.Err() を返す。
	// req.ID と req.ExpiresAt は Approval が決める（呼び出し側の値は使わない）。
	Request(ctx context.Context, req ApprovalRequest) (Decision, error)
	// Resolve は GUI ルーターから呼ばれ、id の要求に結果を伝える。解決済み・失効・取り下げ済み・存在しない id は ErrApprovalTimeout。
	Resolve(id string, allow bool) error
	// Pending は承認待ちの要求を、要求した順に返す。
	Pending() []ApprovalRequest
}

// ApprovalRequest は承認待ちの要求 1 件。
type ApprovalRequest struct {
	ID        string
	Kind      string // ApprovalKindLease（M3）
	Who       domain.Actor
	ExpiresAt time.Time // この時刻までに結果がなければ ErrApprovalTimeout（ダイアログのカウントダウン用）
}

// Decision はホストの結果。
type Decision struct{ Allow bool }

// ApprovalEvent は Bus の approval イベントの Data（要求を作るたびに出す）。購読者は Pending で現在の要求を読む。
type ApprovalEvent struct {
	ID   string
	Kind string
}

// ApprovalConfig は NewApproval の設定。Clock・IDs は必須。
type ApprovalConfig struct {
	Clock domain.Clock
	IDs   domain.IDGen
	// Bus は nil なら何も出さない。出すときは待たない（購読者が遅くても要求は進む。NFR-P06）。
	Bus *Bus
	// AfterFunc は失効のタイマー。nil なら time.AfterFunc。
	AfterFunc AfterFunc
	// Timeout は承認待ちの時間。0 なら DefaultApprovalTimeout。
	Timeout time.Duration
}

type approval struct {
	clock   domain.Clock
	ids     domain.IDGen
	bus     *Bus
	after   AfterFunc
	timeout time.Duration

	mu      sync.Mutex
	seq     uint64
	entries map[string]*approvalEntry
}

type approvalEntry struct {
	req  ApprovalRequest
	seq  uint64
	stop func() bool
	done chan approvalResult // 容量 1。finish が 1 度だけ送る
}

type approvalResult struct {
	allow bool
	err   error
}

// NewApproval は承認待ちのない Approval を返す。
func NewApproval(cfg ApprovalConfig) (Approval, error) {
	if cfg.Clock == nil || cfg.IDs == nil {
		return nil, errors.New("service: approval requires Clock and IDs")
	}
	a := &approval{
		clock:   cfg.Clock,
		ids:     cfg.IDs,
		bus:     cfg.Bus,
		after:   cfg.AfterFunc,
		timeout: cfg.Timeout,
		entries: map[string]*approvalEntry{},
	}
	if a.timeout <= 0 {
		a.timeout = DefaultApprovalTimeout
	}
	if a.after == nil {
		a.after = timeAfterFunc
	}
	return a, nil
}

// timeAfterFunc は AfterFunc の既定（time.AfterFunc）。
func timeAfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

func (a *approval) Request(ctx context.Context, req ApprovalRequest) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	req.ID = a.ids.NewID()
	req.ExpiresAt = a.clock.Now().Add(a.timeout)
	e := &approvalEntry{req: req, done: make(chan approvalResult, 1)}

	a.mu.Lock()
	a.seq++
	e.seq = a.seq
	a.entries[req.ID] = e
	// タイマーの満了は finish のロックを待つので、stop を入れ終えるまでは進まない。
	e.stop = a.after(a.timeout, func() { a.finish(req.ID, approvalResult{err: domain.ErrApprovalTimeout}) })
	a.mu.Unlock()

	if a.bus != nil {
		a.bus.Publish(Event{Topic: TopicApproval, Data: ApprovalEvent{ID: req.ID, Kind: req.Kind}})
	}

	var r approvalResult
	select {
	case r = <-e.done:
	case <-ctx.Done():
		if a.finish(req.ID, approvalResult{err: ctx.Err()}) {
			return Decision{}, ctx.Err()
		}
		r = <-e.done // 取り下げの直前に解決された。その結果を返す
	}
	if r.err != nil {
		return Decision{}, r.err
	}
	return Decision{Allow: r.allow}, nil
}

func (a *approval) Resolve(id string, allow bool) error {
	if !a.finish(id, approvalResult{allow: allow}) {
		return domain.ErrApprovalTimeout
	}
	return nil
}

func (a *approval) Pending() []ApprovalRequest {
	a.mu.Lock()
	entries := make([]*approvalEntry, 0, len(a.entries))
	for _, e := range a.entries {
		entries = append(entries, e)
	}
	a.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
	out := make([]ApprovalRequest, len(entries))
	for i, e := range entries {
		out[i] = e.req
	}
	return out
}

// finish は id の要求を 1 度だけ終わらせ、r を Request へ渡す。すでに終わっている（または無い）要求なら false。
func (a *approval) finish(id string, r approvalResult) bool {
	a.mu.Lock()
	e, ok := a.entries[id]
	if ok {
		delete(a.entries, id)
		e.stop()
	}
	a.mu.Unlock()
	if ok {
		e.done <- r // 容量 1 で、送るのはここだけ（ブロックしない）
	}
	return ok
}
