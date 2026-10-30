package platformtest

import (
	"context"
	"sync"
)

// Call は偽の実装が受けた呼び出し 1 件。
type Call struct {
	Method string
	Args   []any
}

// Recorder は呼び出しの記録とエラーの注入を担う。偽の実装が埋め込む。
type Recorder struct {
	mu    sync.Mutex
	calls []Call
	errs  map[string]error
}

// Calls は記録された呼び出しを、呼ばれた順に複製して返す。
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

// CallsTo は method の呼び出しだけを返す。
func (r *Recorder) CallsTo(method string) []Call {
	var out []Call
	for _, c := range r.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Reset は記録を消す（注入したエラーは残る）。
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

// SetError は method の呼び出しが常に err で失敗するようにする。nil で解除する。
// 注入したエラーは、引数の検査や状態の変更より前に返る。
func (r *Recorder) SetError(method string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		delete(r.errs, method)
		return
	}
	if r.errs == nil {
		r.errs = make(map[string]error)
	}
	r.errs[method] = err
}

// enter は呼び出しを記録し、ctx が取り消し済みなら ctx.Err()、そうでなければ注入されたエラーを返す。
// 取り消し済みの呼び出しも記録に残る（呼ばれたかどうかを観測できるように）。
func (r *Recorder) enter(ctx context.Context, method string, args ...any) error {
	r.mu.Lock()
	r.calls = append(r.calls, Call{Method: method, Args: args})
	injected := r.errs[method]
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return injected
}

// enterSync は ctx を取らない呼び出し用の enter。
func (r *Recorder) enterSync(method string, args ...any) error {
	return r.enter(context.Background(), method, args...)
}
