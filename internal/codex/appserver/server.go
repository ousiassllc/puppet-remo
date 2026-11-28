// Package appserver は Codex の app-server をアプリの子プロセスとして動かし、JSON-RPC で話す（#247）。
//
// 最初に使うときにプロセスを起動し（CODEX_HOME はアプリ専用のディレクトリ）、initialize / initialized のハンドシェイクを行い、
// サーバーが本当にそのディレクトリを使っているか確かめ、サーバーの要求は（desktop の関数ツールの呼び出しを除いて）すべて拒否し、
// スレッドの通知を待っているターンへ、アカウントの通知を 1 つのリスナーへ振り分け、異常終了したら次の利用で起動し直す。
//
// このパッケージはトークンも、メッセージの本文も、読まず・保存せず・ログに出さない。
package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
)

// State はプロセスの状態。
type State string

// 状態。Failed は起動が終わらなかった・誰も頼んでいない終了で、次の利用で起動し直す。
const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateFailed   State = "failed"
)

// Status は画面・タスクが読むプロセスの状態。
type Status struct {
	State State
	// Error は Failed の理由（利用者向け）。
	Error string
}

// ErrStart はプロセスを起動できない・ハンドシェイクが終わらない。待てば直りうる失敗（次の呼び出しで起動し直す）。
var ErrStart = errors.New("codex app-server: could not start")

// ErrNotOurHome はサーバーが返した CODEX_HOME がアプリ専用のものではない: 利用者の資格情報と設定を共有してしまうので止める。
var ErrNotOurHome = errors.New("codex app-server: it does not use the dedicated CODEX_HOME")

// Options は Server の設定。
type Options struct {
	// Start はプロセスを起動する。初回の利用と、異常終了・停止のたびに呼ぶ。
	Start func() (Process, error)
	// CodexHome はプロセスに渡した CODEX_HOME。サーバーは同じディレクトリを返さなければならない。
	CodexHome string
	// Version はアプリの版（clientInfo.version）。
	Version string
	Logger  *slog.Logger
	// InitTimeout はハンドシェイクの上限（既定 30 秒）。
	InitTimeout time.Duration
}

// Server はアプリの app-server のプロセス 1 つ（必要になったとき起動する）。並行利用してよい。
type Server struct {
	opts Options

	listener atomic.Pointer[AccountListener]

	mu       sync.Mutex
	cur      *Session
	starting chan struct{} // 進行中の起動が終わったとき閉じる
	status   Status
	closed   bool
	gen      int // Stop で増える: それより前に始まった起動は片付ける
}

// New はプロセスを起動していない Server を返す。
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.InitTimeout <= 0 {
		o.InitTimeout = 30 * time.Second
	}
	return &Server{opts: o, status: Status{State: StateStopped}}
}

// SetAccountListener はアカウントの通知の受け手を設定する（nil で無し）。動いているプロセスにも効く。
func (s *Server) SetAccountListener(l AccountListener) {
	if l == nil {
		s.listener.Store(nil)
		return
	}
	s.listener.Store(&l)
}

func (s *Server) dispatchAccount(method string, params json.RawMessage) {
	if l := s.listener.Load(); l != nil {
		(*l)(method, params)
	}
}

// Status は今の状態。
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Session は動いていてハンドシェイクを終えたプロセス。
type Session struct {
	srv  *Server
	proc Process
	conn *rpc.Conn
	h    *handler
	// recorded は、監視が終了を状態に記録した（または、停止・入れ替えで記録しないと分かった）とき閉じる。
	recorded chan struct{}
}

// Call はプロセスへ要求を送る。
func (se *Session) Call(ctx context.Context, method string, params, result any) error {
	return se.conn.Call(ctx, method, params, result)
}

// Done はプロセスが終わったとき閉じる。
func (se *Session) Done() <-chan struct{} { return se.conn.Done() }

// endWait はプロセスの出力が終わってから、終了状態を待つ時間。
const endWait = 2 * time.Second

// recordWait は EndReason が監視の記録を待つ時間（監視はプロセスを止め、終了状態を待つ）。
const recordWait = 10 * time.Second

// EndReason はプロセスがどう終わったかを利用者向けに言う: 終了コードと標準エラーの末尾。終了を知った呼び出し
// （接続が閉じた）は、状態が Failed になってから受け取る（監視は非同期に記録する）ので、返す文と状態の表示が一致する。
func (se *Session) EndReason() string {
	select {
	case <-se.recorded:
	case <-time.After(recordWait):
	}
	return se.endReason()
}

// endReason は記録を待たない EndReason（監視が記録を作るのに使う）。出力はプロセスが終わる直前に閉じるので、終了状態を少し待つ。
func (se *Session) endReason() string {
	select {
	case <-se.proc.Done():
	case <-time.After(endWait):
	}
	if info := se.proc.ExitInfo(); info != "" {
		return info
	}
	if err := se.conn.Err(); err != nil {
		return err.Error()
	}
	return ""
}

// Subscribe はスレッドの通知・要求の配送を始める。thread/start の答えのあと、ターンを始める前に呼ぶ
// （誰も購読していないスレッドのイベントは捨てられ、要求は拒否される）。
func (se *Session) Subscribe(threadID string) *Sub { return se.h.subscribe(threadID) }

// Unsubscribe はスレッドの配送を終える。
func (se *Session) Unsubscribe(threadID string) { se.h.unsubscribe(threadID) }

// Ensure は動いているセッションを返し、無ければプロセスを起動して（ハンドシェイクまで）返す。起動中に来た呼び出しは
// それを待つ。起動の失敗は（ErrStart を包んだ）エラーと Failed 状態。すでに終わったプロセスは使わず、起動し直す。
func (s *Server) Ensure(ctx context.Context) (*Session, error) {
	for {
		s.mu.Lock()
		switch {
		case s.closed:
			s.mu.Unlock()
			return nil, fmt.Errorf("%w: shutting down", ErrStart)
		case s.cur != nil && !s.cur.ended():
			cur := s.cur
			s.mu.Unlock()
			return cur, nil
		case s.cur != nil:
			// 終わったのに監視がまだ片付けていない: 片付けを待って再判定する。
			dead := s.cur
			s.mu.Unlock()
			select {
			case <-dead.recorded:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		case s.starting != nil:
			wait := s.starting
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.starting = make(chan struct{})
		s.status = Status{State: StateStarting}
		gen := s.gen
		s.mu.Unlock()
		return s.start(ctx, gen)
	}
}

// ended はプロセスか接続が終わっている（監視がまだ片付けていなくても）。
func (se *Session) ended() bool { return isDone(se.conn.Done()) || isDone(se.proc.Done()) }

func isDone(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func (s *Server) start(ctx context.Context, gen int) (*Session, error) {
	defer func() {
		s.mu.Lock()
		close(s.starting)
		s.starting = nil
		s.mu.Unlock()
	}()
	p, err := s.opts.Start()
	if err != nil {
		return nil, s.fail(fmt.Errorf("%w: %w", ErrStart, err))
	}
	h := newHandler(s.opts.Logger, s.dispatchAccount)
	conn := rpc.NewConn(p.Stdout(), p.Stdin(), h)
	se := &Session{srv: s, proc: p, conn: conn, h: h, recorded: make(chan struct{})}
	if err := s.handshake(ctx, se); err != nil {
		info := p.ExitInfo()
		p.Stop()
		conn.Close()
		if info != "" {
			err = fmt.Errorf("%w (%s)", err, info)
		}
		return nil, s.fail(err)
	}
	s.mu.Lock()
	if s.closed || s.gen != gen {
		s.mu.Unlock()
		p.Stop()
		conn.Close()
		return nil, s.stopped()
	}
	s.cur, s.status = se, Status{State: StateRunning}
	s.mu.Unlock()
	go s.watch(se)
	return se, nil
}

func (s *Server) handshake(ctx context.Context, se *Session) error {
	ctx, cancel := context.WithTimeout(ctx, s.opts.InitTimeout)
	defer cancel()
	var res protocol.InitializeResult
	err := se.conn.Call(ctx, protocol.MethodInitialize, protocol.InitializeParams{
		ClientInfo:   protocol.ClientInfo{Name: protocol.ClientInfoName, Title: protocol.ClientInfoTitle, Version: s.opts.Version},
		Capabilities: &protocol.Capabilities{ExperimentalAPI: true},
	}, &res)
	if err != nil {
		return fmt.Errorf("%w: initialize: %w", ErrStart, err)
	}
	if !sameDir(res.CodexHome, s.opts.CodexHome) {
		return fmt.Errorf("%w: %w", ErrStart, ErrNotOurHome)
	}
	if err := se.conn.Notify(protocol.NotifInitialized, struct{}{}); err != nil {
		return fmt.Errorf("%w: initialized: %w", ErrStart, err)
	}
	return nil
}

// sameDir は 2 つのパスが同じディレクトリか。サーバーは解決した CODEX_HOME（絶対パスにし、リンクをたどったもの）を返すので、
// アプリのディレクトリと綴りが違いうる。綴りが同じ（Windows は大文字小文字を無視）なら受け入れ、そうでなければ
// 両方をディスクで引いて os.SameFile で比べる。
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb || (runtime.GOOS == "windows" && strings.EqualFold(ca, cb)) {
		return true
	}
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}
