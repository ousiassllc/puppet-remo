package windows

import (
	"context"
	"errors"
	"runtime"
	"sync"
)

var errUIAClosed = errors.New("uia: automation is closed")

// uiaWorker は OS スレッドに固定した 1 本の goroutine。COM のスレッドアフィニティのため、
// UI Automation の呼び出しをすべてここへ直列化して渡す（docs/components/overview.md §3 のスレッドの制約）。
type uiaWorker struct {
	jobs chan func()
	quit chan struct{}
	done chan struct{}
	once sync.Once
}

// startUIAWorker はスレッドを起動し、そのスレッドの上で init を 1 回実行する。
// init が返した cleanup は、スレッドの終了前に同じスレッドの上で呼ぶ。init が失敗したらスレッドは終了し、そのエラーを返す。
func startUIAWorker(init func() (cleanup func(), err error)) (*uiaWorker, error) {
	w := &uiaWorker{
		jobs: make(chan func()),
		quit: make(chan struct{}),
		done: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go w.run(init, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return w, nil
}

func (w *uiaWorker) run(init func() (func(), error), ready chan<- error) {
	runtime.LockOSThread()
	defer close(w.done)
	cleanup, err := init()
	ready <- err
	if err != nil {
		return // スレッドを固定したまま goroutine を終えると、ランタイムがスレッドを捨てる
	}
	if cleanup != nil {
		defer cleanup()
	}
	for {
		select {
		case job := <-w.jobs:
			job()
		case <-w.quit:
			return
		}
	}
}

// Do は fn を専用スレッドの上で実行し、終わるまで待つ。fn は ctx を見て早く終えること。
// ctx が取り消し済みなら fn を実行せずに ctx.Err() を返す。実行中に ctx が終わったら、fn の終了を待たずに ctx.Err() を返す。
// 閉じた後は errUIAClosed。
func (w *uiaWorker) Do(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res := make(chan error, 1)
	select {
	case w.jobs <- func() { res <- fn() }:
		// fn は ctx を見て早く終えるはずだが、見ない呼び出しに呼び出し側を縛られないよう ctx でも待ちを切る
		// （res は容量 1 なので、後から終わった fn の結果は捨てられる）。
		select {
		case err := <-res:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-w.done:
		return errUIAClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close はスレッドを止める。実行中の呼び出しが終わるのを待つ。複数回呼んでも安全。
func (w *uiaWorker) Close() {
	w.once.Do(func() { close(w.quit) })
	<-w.done
}
