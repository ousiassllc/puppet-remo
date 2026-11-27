// Package rpc は Codex の app-server への JSON-RPC の接続（標準入出力。1 行 1 メッセージ。app-server に合わせて
// "jsonrpc":"2.0" は付けない）。
//
// 応答を id で呼び出しに対応づけ、通知・サーバーからの要求を Handler へ到着順に渡し、ストリームが終わったら
// 待っている呼び出しをすべて失敗させる。接続はメッセージを一切ログに出さない（ログイン URL やプロンプトを含むため）。
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// maxMessageBytes は 1 行の上限。
const maxMessageBytes = 16 << 20

// ErrClosed は接続が終わった（プロセスの終了・パイプの切断・Close）。
var ErrClosed = errors.New("codex rpc: connection closed")

// Error は相手のエラー応答。
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("codex rpc error %d: %s", e.Code, e.Message) }

// 使う JSON-RPC のエラーコード。
const (
	CodeMethodNotFound = -32601
	CodeInternal       = -32603
)

// Deferred は Handler.Request が返す目印: 応答を今は送らず、あとで Request.Respond で送る。
var Deferred = &Error{Code: CodeInternal, Message: "deferred"}

// Request はサーバーからの要求 1 件。
type Request struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	// Respond は応答を送る（Handler が Deferred を返したときだけ使う。1 回だけ呼ぶ）。
	Respond func(result any, rerr *Error)
}

// Handler はサーバーが自分から送ってくるものを受ける。どちらのメソッドも読み取りの goroutine で 1 件ずつ順に
// 呼ばれるので、ブロックしてはならず、Conn.Call も呼んではならない（作業はキューへ積む）。
type Handler interface {
	// Notification は id のないメッセージ。
	Notification(method string, params json.RawMessage)
	// Request はサーバーの要求（承認など）。結果は応答として返り、*Error はエラー応答になる。
	// Deferred を返したら応答は送らない（Request.Respond で後から送る）。
	Request(req Request) (result any, rerr *Error)
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type outgoing struct {
	ID     any    `json:"id,omitempty"`
	Method string `json:"method,omitempty"`
	Params any    `json:"params,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

type response struct {
	result json.RawMessage
	err    *Error
}

// Conn は 1 本の接続。並行利用してよい。
type Conn struct {
	w       io.Writer
	handler Handler

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	err     error
	done    chan struct{}
	readEnd chan struct{} // 読み取りの goroutine が戻ったとき閉じる: 以後ハンドラは走らない
}

// NewConn は r を読み始める。ストリームを閉じるのは呼び出し側。r が終わると Done が閉じる。
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	c := &Conn{w: w, handler: h, pending: map[int64]chan response{}, done: make(chan struct{}), readEnd: make(chan struct{})}
	go func() {
		defer close(c.readEnd)
		c.read(r)
	}()
	return c
}

// Done は接続が終わったとき閉じる。
func (c *Conn) Done() <-chan struct{} { return c.done }

// Wait は読み取りの goroutine が戻るまで待つ（r がエラー・EOF を返す必要があるので、呼び出し側が先にストリームを終わらせる）。
// 戻った後はハンドラの呼び出しが走っておらず、始まりもしない。ハンドラの中から呼んではならない。
func (c *Conn) Wait() { <-c.readEnd }

// Err は接続が終わった理由（ErrClosed を包む）。開いている間は nil。
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = fmt.Errorf("%w: %w", ErrClosed, err)
	close(c.done)
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// Call は要求を送り、結果を result へ読む（nil なら捨てる）。エラー応答は *Error、ctx が先に終われば ctx のエラー、
// 接続が終われば ErrClosed を包んだエラー。
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(outgoing{ID: id, Method: method, Params: params}); err != nil {
		c.forget(id)
		return err
	}
	var res response
	var ok bool
	select {
	case res, ok = <-ch:
	case <-ctx.Done():
		// ctx が終わる前に届いていた応答は呼び出しの答え（開始したターンの id など）: 呼び出し側が何を取り消すかはそれで決まる。
		select {
		case res, ok = <-ch:
		default:
			c.forget(id)
			return ctx.Err()
		}
	}
	switch {
	case !ok:
		return c.Err()
	case res.err != nil:
		return res.err
	case result != nil && len(res.result) > 0:
		if err := json.Unmarshal(res.result, result); err != nil {
			return fmt.Errorf("codex rpc: undecodable result of %s: %w", method, err)
		}
	}
	return nil
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Notify は通知を送る。
func (c *Conn) Notify(method string, params any) error {
	return c.send(outgoing{Method: method, Params: params})
}

func (c *Conn) send(m outgoing) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("codex rpc: encode %s: %w", m.Method, err)
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(b); err != nil {
		c.fail(err)
		return c.Err()
	}
	return nil
}

func (c *Conn) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxMessageBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			continue // JSON-RPC のメッセージではない（紛れ込んだログ行）: ストリームは続ける
		}
		c.dispatch(m)
	}
	if err := sc.Err(); err != nil {
		c.fail(err)
		return
	}
	c.fail(io.EOF)
}

func (c *Conn) dispatch(m message) {
	switch {
	case m.Method != "" && len(m.ID) > 0:
		c.answer(m)
	case m.Method != "":
		c.handler.Notification(m.Method, m.Params)
	case len(m.ID) > 0:
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- response{result: m.Result, err: m.Error}
		}
	}
}

// answer はサーバーの要求に、ハンドラの決めた応答を返す。
func (c *Conn) answer(m message) {
	id := m.ID
	var once sync.Once
	respond := func(result any, rerr *Error) {
		once.Do(func() {
			out := outgoing{ID: id}
			if rerr != nil {
				out.Error = rerr
			} else {
				out.Result = result
				if result == nil {
					out.Result = struct{}{}
				}
			}
			_ = c.send(out) // 切れたストリームは send が接続の終了にする
		})
	}
	result, rerr := c.handler.Request(Request{ID: id, Method: m.Method, Params: m.Params, Respond: respond})
	if rerr == Deferred {
		return
	}
	respond(result, rerr)
}

// Close はこちら側から接続を終わらせる: 待っている呼び出しは ErrClosed で失敗する。ストリームは閉じない。
func (c *Conn) Close() { c.fail(errors.New("closed")) }
