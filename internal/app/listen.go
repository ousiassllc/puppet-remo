package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// listenSupervisor はリモートルーターの Listener を動かし、設定のポートが変わったら新しいポートで
// 待ち受け直す（設定画面の「保存すると新しいポートで待ち受け直します」）。tailnet.Listener は
// 1 度しか Serve できないので、再読み込みのたびに作り直す。
//
// Listen するアドレスは常に tailnet.Listener が決める（tailnet の IPv4・IPv6 だけ。loopback・0.0.0.0 は決して使わない）。
type listenSupervisor struct {
	newListener func() tailnet.Listener
	handler     http.Handler
	port        func() int
	log         *slog.Logger

	reload chan struct{}

	mu  sync.Mutex
	cur tailnet.Listener
}

func newListenSupervisor(newListener func() tailnet.Listener, h http.Handler, port func() int, log *slog.Logger) *listenSupervisor {
	return &listenSupervisor{newListener: newListener, handler: h, port: port, log: log, reload: make(chan struct{}, 1)}
}

// Reload は、設定のポートの変更を取り込んで待ち受け直すよう求める。待たずに戻る。
func (s *listenSupervisor) Reload() {
	select {
	case s.reload <- struct{}{}:
	default: // 再読み込みはすでに予約済み
	}
}

// State は今の Listener の状態（ServerStatus に出す）。まだ Listener がなければ空の状態。
func (s *listenSupervisor) State() tailnet.ListenState {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	if cur == nil {
		return tailnet.ListenState{Reason: "待ち受けを開始しています"}
	}
	return cur.State()
}

// Run は Listener を動かし続ける。ctx の終了で、待ち受けをすべて閉じてから nil を返す。
// Listener が異常終了（tailscaled の監視を始められないなど）したときは、そのエラーを返す。
func (s *listenSupervisor) Run(ctx context.Context) error {
	for {
		l := s.newListener()
		s.mu.Lock()
		s.cur = l
		s.mu.Unlock()

		serveCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- l.Serve(serveCtx, s.handler, s.port) }()

		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case err := <-done:
			cancel()
			if err != nil && ctx.Err() == nil {
				s.log.Error("tailnet listener stopped", "err", err)
			}
			return err
		case <-s.reload:
			s.log.Info("restarting the tailnet listener to apply the new settings")
			cancel()
			<-done
		}
	}
}
