package windows

import (
	"context"
	"errors"
	"runtime"
	"sync"
)

var errClosed = errors.New("windows: the platform component is closed")

// threadRunner は呼び出しを専用の OS スレッド 1 本へ直列化する（docs/components/overview.md §3 のスレッドの制約）。
// 呼び出し側から見ると、スレッドセーフな同期呼び出し。
type threadRunner struct {
	calls     chan func()
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newThreadRunner() *threadRunner {
	r := &threadRunner{calls: make(chan func()), stop: make(chan struct{}), done: make(chan struct{})}
	started := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		close(started)
		defer close(r.done)
		for {
			select {
			case fn := <-r.calls:
				fn()
			case <-r.stop:
				return
			}
		}
	}()
	<-started
	return r
}

// do は fn を専用スレッドで実行して結果を返す。ctx が取り消し済みなら、fn を実行せずに ctx.Err() を返す。
// fn が始まった後は、完了まで待つ（fn は ctx を見て途中で止まれる）。
func (r *threadRunner) do(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res := make(chan error, 1)
	call := func() {
		if err := ctx.Err(); err != nil {
			res <- err
			return
		}
		res <- fn(ctx)
	}
	select {
	case r.calls <- call:
		return <-res
	case <-ctx.Done():
		return ctx.Err()
	case <-r.stop:
		return errClosed
	}
}

// close はスレッドを止める。実行中の呼び出しの完了を待つ。複数回呼んでも安全。
func (r *threadRunner) close() {
	r.closeOnce.Do(func() { close(r.stop) })
	<-r.done
}
