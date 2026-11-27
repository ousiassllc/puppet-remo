// Package codextest は Codex の app-server の偽物（テスト用）。
//
// Fake は JSON-RPC（改行区切り）を標準入出力の代わりの io.Reader / io.Writer で話す: テストは io.Pipe でつなぐ
// （Start が appserver.Process として包む）か、HelperMain で本物の子プロセスの代わりに動かす。
// 受けた要求はすべて記録し、サーバーから通知・要求を送る口を持つ。本物の Codex には一切つながらない。
package codextest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
)

// Message はクライアントが送ってきたメッセージ（要求・通知）。
type Message struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

// Handler はメソッドの答えを決める。
type Handler func(f *Fake, m Message) (any, *rpc.Error)

// Fake は偽の app-server。フィールドは Serve の前に設定する（Serve 中に変えるものは SetAccount を使う）。
type Fake struct {
	// CodexHome は initialize の答えに入れる値。
	CodexHome string
	// Models は model/list の答え。
	Models []protocol.Model
	// AuthURL は account/login/start の答えの authUrl。
	AuthURL string
	// Handlers はメソッドごとの答えの上書き。
	Handlers map[string]Handler
	// OnTurnStart は turn/start に答えたあと、別の goroutine で呼ばれる（イベントを出す台本）。
	OnTurnStart func(f *Fake, thread, turn string, p protocol.TurnStartParams)

	mu      sync.Mutex
	account *protocol.Account
	log     []Message
	changed chan struct{}
	w       io.Writer
	nextReq int64
	waiting map[string]chan reply
	threads int
	turns   int
	wmu     sync.Mutex
}

type reply struct {
	result json.RawMessage
	err    *rpc.Error
}

// SetAccount はログイン中のアカウントを設定する（nil はログインなし）。
func (f *Fake) SetAccount(a *protocol.Account) {
	f.mu.Lock()
	f.account = a
	f.mu.Unlock()
}

// Calls は method を送ってきたメッセージを順に返す。
func (f *Fake) Calls(method string) []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Message
	for _, m := range f.log {
		if m.Method == method {
			out = append(out, m)
		}
	}
	return out
}

// WaitFor は method が n 回以上届くまで待って、届いたものを返す（ctx が終わったら nil）。
func (f *Fake) WaitFor(ctx context.Context, method string, n int) []Message {
	for {
		f.mu.Lock()
		var out []Message
		for _, m := range f.log {
			if m.Method == method {
				out = append(out, m)
			}
		}
		if f.changed == nil {
			f.changed = make(chan struct{})
		}
		ch := f.changed
		f.mu.Unlock()
		if len(out) >= n {
			return out
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		}
	}
}

func (f *Fake) record(m Message) {
	f.mu.Lock()
	f.log = append(f.log, m)
	if f.changed != nil {
		close(f.changed)
	}
	f.changed = make(chan struct{})
	f.mu.Unlock()
}

func (f *Fake) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = f.w.Write(append(b, '\n'))
}

// Notify はクライアントへ通知を送る。
func (f *Fake) Notify(method string, params any) {
	f.write(map[string]any{"method": method, "params": params})
}

// Ask はクライアントへ要求を送り、応答を待つ。
func (f *Fake) Ask(ctx context.Context, method string, params any) (json.RawMessage, *rpc.Error, error) {
	f.mu.Lock()
	f.nextReq++
	id := fmt.Sprintf("srv-%d", f.nextReq)
	ch := make(chan reply, 1)
	if f.waiting == nil {
		f.waiting = map[string]chan reply{}
	}
	f.waiting[id] = ch
	f.mu.Unlock()
	f.write(map[string]any{"id": id, "method": method, "params": params})
	select {
	case r := <-ch:
		return r.result, r.err, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// Serve は r の要求を処理して w へ答える。r が終わる（EOF）まで戻らない。
func (f *Fake) Serve(r io.Reader, w io.Writer) {
	f.mu.Lock()
	f.w = w
	f.mu.Unlock()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpc.Error      `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method == "":
			var id string
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			f.mu.Lock()
			ch := f.waiting[id]
			delete(f.waiting, id)
			f.mu.Unlock()
			if ch != nil {
				ch <- reply{result: m.Result, err: m.Error}
			}
		case len(m.ID) == 0:
			f.record(Message{Method: m.Method, Params: m.Params})
		default:
			msg := Message{ID: m.ID, Method: m.Method, Params: m.Params}
			f.record(msg)
			f.handle(msg)
		}
	}
}

func (f *Fake) handle(m Message) {
	h := f.Handlers[m.Method]
	var result any
	var rerr *rpc.Error
	if h != nil {
		result, rerr = h(f, m)
	} else {
		result, rerr = f.defaultHandler(m)
	}
	out := map[string]any{"id": m.ID}
	if rerr != nil {
		out["error"] = rerr
	} else {
		if result == nil {
			result = struct{}{}
		}
		out["result"] = result
	}
	f.write(out)
	if m.Method == protocol.MethodTurnStart && rerr == nil && f.OnTurnStart != nil {
		var p protocol.TurnStartParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		turn := fmt.Sprintf("turn-%d", f.turns)
		f.mu.Unlock()
		go f.OnTurnStart(f, p.ThreadID, turn, p)
	}
}

func (f *Fake) defaultHandler(m Message) (any, *rpc.Error) {
	switch m.Method {
	case protocol.MethodInitialize:
		return map[string]any{"codexHome": f.CodexHome, "userAgent": "codextest"}, nil
	case protocol.MethodAccountRead:
		f.mu.Lock()
		defer f.mu.Unlock()
		return protocol.GetAccountResult{Account: f.account}, nil
	case protocol.MethodLoginStart:
		return protocol.LoginResult{Type: protocol.LoginBrowser, LoginID: "login-1", AuthURL: f.AuthURL}, nil
	case protocol.MethodLoginCancel, protocol.MethodThreadUnsubscribe, protocol.MethodTurnInterrupt:
		return nil, nil
	case protocol.MethodLogout:
		f.SetAccount(nil)
		return nil, nil
	case protocol.MethodModelList:
		return protocol.ModelListResult{Data: f.Models}, nil
	case protocol.MethodRateLimitsRead:
		return protocol.RateLimitsResult{}, nil
	case protocol.MethodThreadStart:
		f.mu.Lock()
		f.threads++
		id := fmt.Sprintf("thread-%d", f.threads)
		f.mu.Unlock()
		return map[string]any{"thread": map[string]any{"id": id}}, nil
	case protocol.MethodTurnStart:
		f.mu.Lock()
		f.turns++
		id := fmt.Sprintf("turn-%d", f.turns)
		f.mu.Unlock()
		return map[string]any{"turn": map[string]any{"id": id, "status": "inProgress"}}, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "codextest: no handler for " + m.Method}
}

// --- 台本の部品 ---

// ItemStarted は項目の開始の通知。
func (f *Fake) ItemStarted(thread, turn, itemType string) {
	f.Notify(protocol.NotifItemStarted, map[string]any{
		"threadId": thread, "turnId": turn, "item": map[string]any{"type": itemType, "id": "item-" + itemType},
	})
}

// AgentMessage はエージェントのメッセージ（差分 1 回と、final_answer の完了）を出す。
func (f *Fake) AgentMessage(thread, turn, text string) {
	f.Notify(protocol.NotifAgentDelta, map[string]any{"threadId": thread, "turnId": turn, "itemId": "msg-1", "delta": text})
	f.Notify(protocol.NotifItemCompleted, map[string]any{
		"threadId": thread, "turnId": turn,
		"item": map[string]any{"type": protocol.ItemAgentMessage, "id": "msg-1", "text": text, "phase": protocol.PhaseFinalAnswer},
	})
}

// CompleteTurn はターンの終了の通知。errInfo が空でなければ失敗（codexErrorInfo がその文字列）。
func (f *Fake) CompleteTurn(thread, turn, status, errInfo string) {
	t := map[string]any{"id": turn, "status": status}
	if errInfo != "" {
		t["error"] = map[string]any{"message": "failed: " + errInfo, "codexErrorInfo": errInfo}
	}
	f.Notify(protocol.NotifTurnCompleted, map[string]any{"threadId": thread, "turn": t})
}

// ToolCall は desktop の関数ツールの呼び出しを要求として送り、応答を待つ。
func (f *Fake) ToolCall(ctx context.Context, thread, turn, callID, tool, args string) (protocol.DynamicToolResponse, *rpc.Error, error) {
	raw, rerr, err := f.Ask(ctx, protocol.ReqDynamicToolCall, map[string]any{
		"threadId": thread, "turnId": turn, "callId": callID, "namespace": "desktop", "tool": tool, "arguments": json.RawMessage(args),
	})
	var resp protocol.DynamicToolResponse
	if err == nil && rerr == nil {
		err = json.Unmarshal(raw, &resp)
	}
	return resp, rerr, err
}
