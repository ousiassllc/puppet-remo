package tailnet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Watcher は tailscaled の状態を監視する。
type Watcher interface {
	// Watch は状態（ログイン済みか、tailnet IP、MagicDNS 名）の変化を流す。
	// 接続できた直後と、前回の通知から変化があったときだけ送る。tailscaled に届かないときは
	// Err つきの Status を 1 度送り、切断時は再接続する（接続できるまで RetryInterval ごと）。
	// チャネルは受け手が遅れたら古い値を捨てて最新だけを残し、ctx の終了で閉じる。
	Watch(ctx context.Context) (<-chan Status, error)
}

// WatcherConfig は NewWatcher の設定。
type WatcherConfig struct {
	API LocalAPI
	// Clock は nil なら SystemClock。
	Clock Clock
	// RetryInterval は再接続の間隔。0 以下なら 1 秒。
	RetryInterval time.Duration
}

type ipWatcher struct {
	api   LocalAPI
	clock Clock
	retry time.Duration
}

// NewWatcher は WatchIPNBus で tailscaled を監視する Watcher を返す。
// Watch 1 回につき goroutine 1 本（バスの読み取り）を持つ。
func NewWatcher(cfg WatcherConfig) Watcher {
	w := &ipWatcher{api: cfg.API, clock: cfg.Clock, retry: cfg.RetryInterval}
	if w.clock == nil {
		w.clock = SystemClock
	}
	if w.retry <= 0 {
		w.retry = defaultRetryInterval
	}
	return w
}

func (w *ipWatcher) Watch(ctx context.Context) (<-chan Status, error) {
	if w.api == nil {
		return nil, errors.New("tailnet: nil LocalAPI")
	}
	out := make(chan Status, 1)
	go w.run(ctx, out)
	return out, nil
}

func (w *ipWatcher) run(ctx context.Context, out chan Status) {
	defer close(out)
	var last *Status
	emit := func(s Status) {
		if last != nil && statusEqual(*last, s) {
			return
		}
		last = &s
		// 単一の送り手: 溜まった古い値を捨ててから最新を入れる。
		select {
		case <-out:
		default:
		}
		out <- s
	}
	for {
		err := w.session(ctx, emit)
		if ctx.Err() != nil {
			return
		}
		emit(Status{Err: fmt.Errorf("tailscaled LocalAPI: %w", err)})
		select {
		case <-ctx.Done():
			return
		case <-w.clock.After(w.retry):
		}
	}
}

// session は 1 回の購読。購読が切れる・状態を取得できないとエラーを返す。
func (w *ipWatcher) session(ctx context.Context, emit func(Status)) error {
	bw, err := w.api.WatchIPNBus(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = bw.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = bw.Close() })
	defer stop()
	for {
		// 購読を張ってから取得するので、取得後の変化は必ず通知として届く。
		st, err := w.api.StatusWithoutPeers(ctx)
		if err != nil {
			return err
		}
		emit(st)
		if err := bw.Next(); err != nil {
			return err
		}
	}
}

func statusEqual(a, b Status) bool {
	return a.LoggedIn == b.LoggedIn &&
		a.DNSName == b.DNSName &&
		slices.Equal(a.IPs, b.IPs) &&
		errString(a.Err) == errString(b.Err)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
