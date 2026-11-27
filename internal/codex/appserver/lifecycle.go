package appserver

import (
	"errors"
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
)

// watch はプロセスの終わりに気づく。誰も頼んでいない終了は失敗（状態に記録し、次の利用で起動し直す）。
func (s *Server) watch(se *Session) {
	defer close(se.recorded)
	select {
	case <-se.proc.Done():
	case <-se.conn.Done():
	}
	s.mu.Lock()
	if s.cur != se {
		s.mu.Unlock()
		return
	}
	s.cur = nil
	s.mu.Unlock()
	se.proc.Stop() // 接続だけが終わって、プロセスが動いたままのこともある
	err := fmt.Errorf("%w", ErrStart)
	if info := se.endReason(); info != "" {
		err = errors.New(info)
	}
	s.opts.Logger.Warn("codex app-server ended", "state", "failed")
	s.failEnded(err)
}

// failEnded は誰も頼んでいない終了を記録する。ただし新しいプロセスが動いている・起動中ならその状態が今の状態
// （停止と終了状態の待ちに時間がかかり、その間に呼び出しが新しいプロセスを起動していることがある）。
func (s *Server) failEnded(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil && s.starting == nil && !s.closed {
		s.status = Status{State: StateFailed, Error: err.Error()}
	}
}

func (s *Server) fail(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.status = Status{State: StateFailed, Error: err.Error()}
	}
	return err
}

// Stop はプロセスを終わらせる。Close でない限り、次の利用でまた起動する。起動の途中なら、ハンドシェイクが終わったところで
// 片付ける（Stop より長く生きてはならない）。
func (s *Server) Stop() {
	s.mu.Lock()
	se := s.cur
	s.cur = nil
	s.gen++
	if se != nil || s.status.State == StateFailed {
		s.status = Status{State: StateStopped}
	}
	s.mu.Unlock()
	if se != nil {
		se.conn.Close()
		se.proc.Stop()
		// すでに読んだ通知が、Stop の後にアプリの状態へ触ってはならない（削除つきのログアウトが消す）。
		waitReader(se.conn)
	}
}

// waitReader はプロセスが消えたあと、接続の読み取り goroutine を待つ。プロセスと一緒に閉じないストリーム
// （パイプを持ったままの子孫）で Stop が永久に止まらないよう、数秒で諦める。
func waitReader(c *rpc.Conn) {
	ended := make(chan struct{})
	go func() { c.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(readerGrace):
	}
}

const readerGrace = 5 * time.Second

// Close はプロセスを永久に止める（アプリの終了）。以後の呼び出しは ErrStart で失敗する。
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.Stop()
}

// Running はプロセスを起動せずに、動いているかを返す。
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil
}

// stopped は Stop（Close）に追い越された起動のエラー。状態は Stop のもの。
func (s *Server) stopped() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.status = Status{State: StateStopped}
	}
	return fmt.Errorf("%w: stopped while starting", ErrStart)
}

// Current は動いているセッション（無ければ nil）。Ensure と違い、プロセスを起動しない。
func (s *Server) Current() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}
