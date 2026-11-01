package service

import (
	"context"
	"errors"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// ConnRequestState は未登録デバイスの接続要求の状態（Web UI の待機ページの出し分け。FR-W02）。
type ConnRequestState string

const (
	// ConnRequestWaiting はホストの許可を待っている（既定。要求が見つからないときや状態を読めないときもこれ）。
	ConnRequestWaiting ConnRequestState = "pending"
	// ConnRequestDenied はホストが拒否した（拒否から 24 時間は同じデバイスから新しい要求を作らない。FR-A06）。
	ConnRequestDenied ConnRequestState = "denied"
	// ConnRequestExpired は要求が期限切れ（24 時間）になった。
	ConnRequestExpired ConnRequestState = "expired"
)

// ConnRequestStates はデバイスの接続要求の状態を読む。読み取り専用で、接続要求を作らず・更新せず、
// FR-A06 の集約・再要求の抑止には関わらない（それは Authz が許可リストの照合のときに行う）。
type ConnRequestStates interface {
	// State は stableNodeID の最新の接続要求の状態を返す。要求が無い、または許可済み（登録を外された後など）は
	// ConnRequestWaiting。読めなければ ConnRequestWaiting とエラーを返す（呼び出し側はログに残して待機ページを出す）。
	State(ctx context.Context, stableNodeID string) (ConnRequestState, error)
}

type connRequestStates struct {
	requests repository.ConnRequests
	clock    domain.Clock
}

// NewConnRequestStates は ConnRequestStates を作る。
func NewConnRequestStates(requests repository.ConnRequests, clock domain.Clock) (ConnRequestStates, error) {
	if requests == nil || clock == nil {
		return nil, errors.New("service: connection request states require ConnRequests and Clock")
	}
	return &connRequestStates{requests: requests, clock: clock}, nil
}

func (s *connRequestStates) State(ctx context.Context, stableNodeID string) (ConnRequestState, error) {
	r, err := s.requests.LatestByNode(ctx, stableNodeID)
	if errors.Is(err, repository.ErrNotFound) {
		return ConnRequestWaiting, nil
	}
	if err != nil {
		return ConnRequestWaiting, err
	}
	live := r.ExpiresAt.After(s.clock.Now())
	switch r.Status {
	case repository.ConnPending:
		if live {
			return ConnRequestWaiting, nil
		}
		return ConnRequestExpired, nil // Expire（保守）の前の期限切れ
	case repository.ConnDenied:
		if live {
			return ConnRequestDenied, nil
		}
		return ConnRequestExpired, nil // 拒否の抑止期間（24 時間）が過ぎた
	case repository.ConnExpired:
		return ConnRequestExpired, nil
	}
	return ConnRequestWaiting, nil
}
