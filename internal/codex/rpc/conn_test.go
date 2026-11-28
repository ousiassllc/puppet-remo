package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
)

type handler struct {
	mu     sync.Mutex
	notes  []string
	reqs   chan rpc.Request
	answer func(rpc.Request) (any, *rpc.Error)
}

func (h *handler) Notification(method string, _ json.RawMessage) {
	h.mu.Lock()
	h.notes = append(h.notes, method)
	h.mu.Unlock()
}

func (h *handler) Request(req rpc.Request) (any, *rpc.Error) {
	if h.reqs != nil {
		h.reqs <- req
	}
	return h.answer(req)
}

// pipePair は Conn と、その相手（テストが読み書きする側）。
type pipePair struct {
	conn       *rpc.Conn
	fromClient *io.PipeReader // クライアントが書いたものを読む
	toClient   *io.PipeWriter // クライアントへ書く
}

func newPair(h rpc.Handler) *pipePair {
	cr, cw := io.Pipe() // クライアント → サーバー
	sr, sw := io.Pipe() // サーバー → クライアント
	return &pipePair{conn: rpc.NewConn(sr, cw, h), fromClient: cr, toClient: sw}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func readLine(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var line []byte
	buf := make([]byte, 1)
	for {
		if _, err := r.Read(buf); err != nil {
			t.Fatal(err)
		}
		if buf[0] == '\n' {
			break
		}
		line = append(line, buf[0])
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return m
}

func send(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s+"\n"); err != nil {
		t.Error(err) // goroutine からも呼ぶので Fatal は使わない
	}
}

func TestCallは応答をidで対応づけて結果を読む(t *testing.T) {
	p := newPair(&handler{})
	done := make(chan error, 1)
	var res struct{ A int }
	go func() { done <- p.conn.Call(ctxT(t), "x/y", map[string]int{"n": 1}, &res) }()
	m := readLine(t, p.fromClient)
	if m["method"] != "x/y" || m["jsonrpc"] != nil {
		t.Fatalf("request = %v", m)
	}
	// 無関係な id の応答・JSON でない行は無視される。
	send(t, p.toClient, `{"id":999,"result":{}}`)
	send(t, p.toClient, `not json`)
	send(t, p.toClient, `{"id":1,"result":{"A":7}}`)
	if err := <-done; err != nil || res.A != 7 {
		t.Fatalf("err = %v, res = %+v", err, res)
	}
}

func TestCallはエラー応答を返す(t *testing.T) {
	p := newPair(&handler{})
	done := make(chan error, 1)
	go func() { done <- p.conn.Call(ctxT(t), "x", nil, nil) }()
	readLine(t, p.fromClient)
	send(t, p.toClient, `{"id":1,"error":{"code":-32000,"message":"nope"}}`)
	var rerr *rpc.Error
	if err := <-done; !errors.As(err, &rerr) || rerr.Code != -32000 || rerr.Message != "nope" {
		t.Fatalf("err = %v", err)
	}
}

func TestCallはctxの終了で戻り接続の終了ですべての呼び出しが失敗する(t *testing.T) {
	p := newPair(&handler{})
	ctx, cancel := context.WithCancel(ctxT(t))
	errCh := make(chan error, 1)
	go func() { errCh <- p.conn.Call(ctx, "slow", nil, nil) }()
	readLine(t, p.fromClient)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// 待っている呼び出しは、ストリームの終了（プロセスの終了）で ErrClosed になる。
	errCh2 := make(chan error, 1)
	go func() { errCh2 <- p.conn.Call(ctxT(t), "pending", nil, nil) }()
	readLine(t, p.fromClient)
	_ = p.toClient.Close()
	if err := <-errCh2; !errors.Is(err, rpc.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
	<-p.conn.Done()
	p.conn.Wait()
	if err := p.conn.Call(ctxT(t), "after", nil, nil); !errors.Is(err, rpc.ErrClosed) {
		t.Fatalf("call after the end: %v", err)
	}
}

func TestサーバーからのNotificationとRequestはハンドラへ届く(t *testing.T) {
	h := &handler{answer: func(req rpc.Request) (any, *rpc.Error) {
		if req.Method == "approve" {
			return map[string]string{"decision": "decline"}, nil
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "no"}
	}}
	p := newPair(h)
	go func() {
		send(t, p.toClient, `{"method":"note/one","params":{}}`)
		send(t, p.toClient, `{"id":"s1","method":"approve","params":{}}`)
		send(t, p.toClient, `{"id":"s2","method":"unknown","params":{}}`)
	}()
	a := readLine(t, p.fromClient)
	b := readLine(t, p.fromClient)
	if a["id"] != "s1" || a["result"].(map[string]any)["decision"] != "decline" {
		t.Fatalf("answer 1 = %v", a)
	}
	if b["id"] != "s2" || b["error"].(map[string]any)["code"].(float64) != rpc.CodeMethodNotFound {
		t.Fatalf("answer 2 = %v", b)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.notes) != 1 || h.notes[0] != "note/one" {
		t.Fatalf("notes = %v", h.notes)
	}
}

func TestDeferredを返した要求はRespondまで応答されず1回だけ応答できる(t *testing.T) {
	h := &handler{reqs: make(chan rpc.Request, 1), answer: func(rpc.Request) (any, *rpc.Error) { return nil, rpc.Deferred }}
	p := newPair(h)
	go send(t, p.toClient, `{"id":5,"method":"item/tool/call","params":{"a":1}}`)
	req := <-h.reqs
	if string(req.ID) != "5" || req.Method != "item/tool/call" || string(req.Params) != `{"a":1}` {
		t.Fatalf("request = %+v", req)
	}
	// Respond の前は何も書かれていない（次の通常の呼び出しの要求が先頭に来る）。
	errCh := make(chan error, 1)
	go func() { errCh <- p.conn.Call(ctxT(t), "next", nil, nil) }()
	first := readLine(t, p.fromClient)
	if first["method"] != "next" {
		t.Fatalf("the deferred request was answered early: %v", first)
	}
	send(t, p.toClient, `{"id":1,"result":{}}`)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	go func() {
		req.Respond(map[string]bool{"success": true}, nil)
		req.Respond(map[string]bool{"success": false}, nil) // 2 回目は無視される
	}()
	got := readLine(t, p.fromClient)
	if string(mustJSON(got["id"])) != "5" || got["result"].(map[string]any)["success"] != true {
		t.Fatalf("response = %v", got)
	}
	// 2 つ目の応答は書かれない: 次に読める行は、次の呼び出しの要求。
	go func() { _ = p.conn.Call(ctxT(t), "last", nil, nil) }()
	if m := readLine(t, p.fromClient); m["method"] != "last" {
		t.Fatalf("a second response was written: %v", m)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestCloseは待っている呼び出しを失敗させる(t *testing.T) {
	p := newPair(&handler{})
	errCh := make(chan error, 1)
	go func() { errCh <- p.conn.Call(ctxT(t), "x", nil, nil) }()
	readLine(t, p.fromClient)
	p.conn.Close()
	if err := <-errCh; !errors.Is(err, rpc.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}
