package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func TestNewConnRequestStatesRequiresDependencies(t *testing.T) {
	if _, err := NewConnRequestStates(nil, newFakeClock()); err == nil {
		t.Error("NewConnRequestStates without ConnRequests must fail")
	}
	if _, err := NewConnRequestStates(&memConnRequests{}, nil); err == nil {
		t.Error("NewConnRequestStates without Clock must fail")
	}
}

// State は最新の要求から待機ページの出し分けを決め、要求を作らない・更新しない（読み取り専用）。
func TestConnRequestStates(t *testing.T) {
	clock := newFakeClock()
	now := clock.Now()
	row := func(status string, expiresIn time.Duration) repository.ConnRequest {
		return repository.ConnRequest{ID: "r1", StableNodeID: testNodeA, Status: status, ExpiresAt: now.Add(expiresIn)}
	}
	tests := []struct {
		name string
		rows []repository.ConnRequest
		err  error
		want ConnRequestState
	}{
		{"要求が無い", nil, nil, ConnRequestWaiting},
		{"有効な pending", []repository.ConnRequest{row(repository.ConnPending, time.Hour)}, nil, ConnRequestWaiting},
		{"期限切れの pending（保守の前）", []repository.ConnRequest{row(repository.ConnPending, -time.Second)}, nil, ConnRequestExpired},
		{"期限ちょうどの pending", []repository.ConnRequest{row(repository.ConnPending, 0)}, nil, ConnRequestExpired},
		{"抑止中の denied", []repository.ConnRequest{row(repository.ConnDenied, time.Hour)}, nil, ConnRequestDenied},
		{"抑止期間を過ぎた denied", []repository.ConnRequest{row(repository.ConnDenied, -time.Second)}, nil, ConnRequestExpired},
		{"expired", []repository.ConnRequest{row(repository.ConnExpired, -time.Hour)}, nil, ConnRequestExpired},
		{"許可済み（登録を外された後）は待機", []repository.ConnRequest{row(repository.ConnApproved, time.Hour)}, nil, ConnRequestWaiting},
		{"最新の行を使う（古い denied のあとの pending）", []repository.ConnRequest{
			row(repository.ConnDenied, -time.Hour), row(repository.ConnPending, time.Hour),
		}, nil, ConnRequestWaiting},
		{"読めなければ待機とエラー", nil, errors.New("db down"), ConnRequestWaiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := &memConnRequests{rows: tt.rows, err: tt.err}
			states, err := NewConnRequestStates(reqs, clock)
			if err != nil {
				t.Fatal(err)
			}
			before := reqs.snapshot()
			got, err := states.State(context.Background(), testNodeA)
			if (err != nil) != (tt.err != nil) {
				t.Errorf("err = %v, want %v", err, tt.err)
			}
			if got != tt.want {
				t.Errorf("State = %q, want %q", got, tt.want)
			}
			if after := reqs.snapshot(); len(after) != len(before) {
				t.Errorf("State must not change the requests: %d rows -> %d rows", len(before), len(after))
			}
		})
	}
}
