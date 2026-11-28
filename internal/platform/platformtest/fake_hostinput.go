package platformtest

import (
	"errors"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.HostInputWatcher = (*FakeHostInput)(nil)

// FakeHostInput は platform.HostInputWatcher の偽の実装。実機の「実入力だけを検知し、自分の注入は検知しない」という区別は
// プラットフォーム側の責務なので、ここでは区別せず、Emit が実入力 1 件を模擬する。自分の注入（FakeInput の操作）は Emit を呼ばないので検知しない。
type FakeHostInput struct {
	Recorder

	mu sync.Mutex
	fn func(kind platform.HostInputKind) // 監視中だけ非 nil
}

func NewFakeHostInput() *FakeHostInput { return &FakeHostInput{} }

func (f *FakeHostInput) Watch(fn func(kind platform.HostInputKind)) (func(), error) {
	if err := f.enterSync("Watch"); err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("host input callback is nil")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fn != nil {
		return nil, errors.New("host input is already being watched")
	}
	f.fn = fn
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.fn = nil
		})
	}, nil
}

// Emit は実入力 1 件を模擬し、監視中なら fn を呼んで true を返す。fn はロックの外で、呼び出し元の goroutine で同期的に呼ぶ。
func (f *FakeHostInput) Emit(kind platform.HostInputKind) bool {
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	if fn == nil {
		return false
	}
	fn(kind)
	return true
}

// Watching は監視中かを返す。
func (f *FakeHostInput) Watching() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fn != nil
}
