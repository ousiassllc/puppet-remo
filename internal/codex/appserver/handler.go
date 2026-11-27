package appserver

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

// AccountListener はアカウントの通知（ログイン完了・アカウントの更新・利用上限の更新）を受ける。読み取りの goroutine で
// 走るので、ブロックしてはならず、サーバーを呼んではならない（そのための goroutine を起こす）。
type AccountListener func(method string, params json.RawMessage)

// handler は 1 つのプロセスの rpc.Handler: 届いたものを振り分け、頼まれたものを拒否する。
type handler struct {
	logger *slog.Logger
	listen AccountListener

	mu   sync.Mutex
	subs map[string]*Sub // スレッド ID ごと
}

func newHandler(logger *slog.Logger, listen AccountListener) *handler {
	return &handler{logger: logger, listen: listen, subs: map[string]*Sub{}}
}

func (h *handler) subscribe(threadID string) *Sub {
	s := newSub()
	h.mu.Lock()
	h.subs[threadID] = s
	h.mu.Unlock()
	return s
}

func (h *handler) unsubscribe(threadID string) {
	h.mu.Lock()
	s := h.subs[threadID]
	delete(h.subs, threadID)
	h.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func (h *handler) sub(threadID string) *Sub {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subs[threadID]
}

// threadOf は残りを読まずに threadId だけを読む。
func threadOf(params json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.ThreadID
}

// Notification は rpc.Handler の実装。
func (h *handler) Notification(method string, params json.RawMessage) {
	switch method {
	case protocol.NotifLoginCompleted, protocol.NotifAccountUpdated, protocol.NotifRateLimits:
		if h.listen != nil {
			h.listen(method, params)
		}
	case protocol.NotifAgentDelta, protocol.NotifItemCompleted, protocol.NotifTurnCompleted, protocol.NotifError,
		protocol.NotifItemStarted, protocol.NotifTurnStarted:
		if s := h.sub(threadOf(params)); s != nil {
			s.push(Event{Method: method, Params: params})
		}
	}
}

// Request は rpc.Handler の実装。desktop の関数ツールの呼び出し（購読中のスレッドのもの）だけを、応答を保留して
// そのスレッドのイベントとして渡す。それ以外はすべて拒否する。ログに残すのはメソッドとスレッドだけ。
func (h *handler) Request(req rpc.Request) (any, *rpc.Error) {
	thread := threadOf(req.Params)
	s := h.sub(thread)
	if req.Method == protocol.ReqDynamicToolCall && s != nil {
		var p protocol.DynamicToolCallParams
		if json.Unmarshal(req.Params, &p) == nil && p.Namespace == openai.DesktopNamespace {
			s.push(Event{Method: req.Method, Params: req.Params, Respond: req.Respond})
			return nil, rpc.Deferred
		}
	}
	if s != nil {
		s.countRefusal()
	}
	h.logger.Warn("codex app-server request refused", "method", req.Method, "thread", thread)
	if answer, ok := protocol.Refusal(req.Method); ok {
		return answer, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "puppet-remo does not support " + req.Method}
}
