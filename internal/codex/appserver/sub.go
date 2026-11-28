package appserver

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
)

// Event はスレッドの通知、またはスレッドに結び付いたサーバーの要求（desktop の item/tool/call）。
// 要求のときだけ Respond が非 nil で、タスク側が 1 回だけ呼んで応答する。
type Event struct {
	Method  string
	Params  json.RawMessage
	Respond func(result any, rerr *rpc.Error)
}

// Sub は 1 つのスレッドのイベントを、順番に、上限なしで受ける（接続の読み取り goroutine が遅いターンで止まらない）。
type Sub struct {
	mu     sync.Mutex
	queue  []Event
	signal chan struct{}
	closed bool
	// refused はこのスレッドで拒否したサーバーの要求の数。
	refused int
}

func newSub() *Sub { return &Sub{signal: make(chan struct{}, 1)} }

func (s *Sub) push(e Event) {
	s.mu.Lock()
	if !s.closed {
		s.queue = append(s.queue, e)
	}
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *Sub) pop() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return Event{}, false
	}
	e := s.queue[0]
	s.queue = s.queue[1:]
	return e, true
}

// TryNext は届いているイベントを待たずに 1 つ取る。
func (s *Sub) TryNext() (Event, bool) { return s.pop() }

// Next は次のイベントを待って返す。ctx が終わるか done が閉じたら ok = false（done が閉じる前に届いたものは返す）。
func (s *Sub) Next(ctx context.Context, done <-chan struct{}) (e Event, ok bool) {
	for {
		if e, ok := s.pop(); ok {
			return e, true
		}
		select {
		case <-s.signal:
		case <-ctx.Done():
			return Event{}, false
		case <-done:
			return s.pop()
		}
	}
}

// Refused はこのスレッドでこれまでに拒否したサーバーの要求（承認・道具の呼び出し）の数。
func (s *Sub) Refused() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

func (s *Sub) countRefusal() {
	s.mu.Lock()
	s.refused++
	s.mu.Unlock()
}

func (s *Sub) close() {
	s.mu.Lock()
	s.closed = true
	s.queue = nil
	s.mu.Unlock()
}
