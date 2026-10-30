package tailnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// ListenState は Listener の現在の状態。GUI のダッシュボードに出す（FR-A01、NFR-S01）。
type ListenState struct {
	Listening bool     // 1 つ以上のアドレスで Listen 中
	Addrs     []string // Listen 中の "ip:port"（IPv4 → IPv6 の順）
	Reason    string   // Listen していない、または一部が欠けている理由。問題がなければ空
}

// Listener は tailnet アドレスだけで Listen する。
type Listener interface {
	// Serve は Watcher の状態に追従して、tailnet の IPv4・IPv6 のうち取れたアドレスのそれぞれで
	// h を配信する。取れたアドレスがない間は何も Listen せず、理由を State の Reason に出す。
	// port は Listen を張るたびに呼ぶ。ctx の終了で全て閉じて nil を返す。
	// Serve は 1 つの Listener に対して 1 度しか呼べない。
	Serve(ctx context.Context, h http.Handler, port func() int) error
	State() ListenState
}

// ListenFunc はアドレスで Listen する関数。network は "tcp4" か "tcp6"。
type ListenFunc func(ctx context.Context, network, address string) (net.Listener, error)

// ListenerConfig は NewListener の設定。
type ListenerConfig struct {
	Watcher Watcher
	// Clock は nil なら SystemClock。Listen 失敗の再試行の待機に使う。
	Clock Clock
	// RetryInterval は Listen に失敗したアドレスの再試行間隔。0 以下なら 1 秒。
	RetryInterval time.Duration
	// ListenFunc は nil なら net.ListenConfig.Listen。
	ListenFunc ListenFunc
	// OnChange は State が変わるたびに Serve の goroutine から呼ばれる。nil なら呼ばない。
	// 重い処理や Listener の呼び出しをしてはならない。
	OnChange func(ListenState)
}

type tailnetListener struct {
	watcher Watcher
	clock   Clock
	retry   time.Duration
	listen  ListenFunc
	onChg   func(ListenState)

	mu      sync.Mutex
	state   ListenState
	started bool
}

// NewListener は Watcher の状態に追従する Listener を返す。
// goroutine は、Listen しているアドレスごとに 1 本（最大 IPv4・IPv6 の 2 本）と
// http.Server の接続ごとのもの、Watcher の分だけ。
func NewListener(cfg ListenerConfig) Listener {
	l := &tailnetListener{
		watcher: cfg.Watcher,
		clock:   cfg.Clock,
		retry:   cfg.RetryInterval,
		listen:  cfg.ListenFunc,
		onChg:   cfg.OnChange,
		state:   ListenState{Reason: "tailscaled の状態を確認しています"},
	}
	if l.clock == nil {
		l.clock = SystemClock
	}
	if l.retry <= 0 {
		l.retry = defaultRetryInterval
	}
	if l.listen == nil {
		l.listen = func(ctx context.Context, network, address string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, address)
		}
	}
	return l
}

func (l *tailnetListener) State() ListenState {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state
	s.Addrs = slices.Clone(s.Addrs)
	return s
}

// tailnet のアドレス範囲（100.64.0.0/10 と fd7a:115c:a1e0::/48）。
var tailnetPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// tailnetAddrs は ips のうち Listen してよいもの（tailnet のアドレス。IPv4 → IPv6 の順、重複なし）を返す。
// ループバック・未指定（0.0.0.0、::）・tailnet 以外は含めない（FR-A01、NFR-S01）。
func tailnetAddrs(ips []netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || slices.Contains(out, ip) {
			continue
		}
		if slices.ContainsFunc(tailnetPrefixes, func(p netip.Prefix) bool { return p.Contains(ip) }) {
			out = append(out, ip)
		}
	}
	slices.SortStableFunc(out, func(a, b netip.Addr) int {
		switch {
		case a.Is4() && !b.Is4():
			return -1
		case !a.Is4() && b.Is4():
			return 1
		}
		return 0
	})
	return out
}

// bound は 1 つのアドレスでの Listen。
type bound struct {
	ap     netip.AddrPort
	srv    *http.Server
	closed chan struct{} // stop が閉じる。死亡通知の送信待ちを解くため
	done   chan struct{} // Serve の goroutine の終了
}

func (b *bound) stop() {
	close(b.closed)
	_ = b.srv.Close()
	<-b.done
}

func (l *tailnetListener) Serve(ctx context.Context, h http.Handler, port func() int) error {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return errors.New("tailnet: Serve was already called")
	}
	l.started = true
	l.mu.Unlock()

	if l.watcher == nil {
		return errors.New("tailnet: nil Watcher")
	}
	statuses, err := l.watcher.Watch(ctx)
	if err != nil {
		return err
	}
	s := &serveLoop{
		l: l, ctx: ctx, h: h, port: port,
		bounds: make(map[netip.AddrPort]*bound),
		died:   make(chan *bound),
	}
	defer s.stopAll()

	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case st, ok := <-statuses:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("tailnet: watcher stopped unexpectedly")
			}
			s.status = st
			retry = s.reconcile()
		case <-retry:
			retry = s.reconcile()
		case b := <-s.died:
			// 受け付けの異常終了。すぐに張り直すと異常が続くとき空回りするので、間隔を置いて再試行する。
			s.failures[b.ap] = fmt.Sprintf("%s の受け付けが終了しました", b.ap)
			b.stop()
			delete(s.bounds, b.ap)
			s.publish("")
			retry = s.clock().After(l.retry)
		}
	}
}

// serveLoop は Serve の状態。Serve の goroutine だけが触る。
type serveLoop struct {
	l      *tailnetListener
	ctx    context.Context
	h      http.Handler
	port   func() int
	status Status
	bounds map[netip.AddrPort]*bound
	died   chan *bound
	// failures は直近の reconcile で Listen できなかった（または死んだ）アドレスとその理由。
	failures map[netip.AddrPort]string
}

func (s *serveLoop) clock() Clock { return s.l.clock }

// reconcile は最新の status と port に合わせて Listen の集合を作り直す。
// 失敗したアドレスがあれば再試行用のタイマーを返す。なければ nil。
func (s *serveLoop) reconcile() <-chan time.Time {
	want, reason := s.desired()
	for ap, b := range s.bounds {
		if !slices.Contains(want, ap) {
			b.stop()
			delete(s.bounds, ap)
		}
	}
	s.failures = make(map[netip.AddrPort]string)
	for _, ap := range want {
		if s.bounds[ap] != nil {
			continue
		}
		b, err := s.bind(ap)
		if err != nil {
			s.failures[ap] = fmt.Sprintf("%s で Listen できません: %v", ap, err)
			continue
		}
		s.bounds[ap] = b
	}
	s.publish(reason)
	if len(s.failures) > 0 {
		return s.clock().After(s.l.retry)
	}
	return nil
}

// desired は Listen すべき "ip:port" と、何も取れないときの理由（取れたら空）を返す。
func (s *serveLoop) desired() ([]netip.AddrPort, string) {
	st := s.status
	switch {
	case st.Err != nil:
		return nil, fmt.Sprintf("tailscaled に接続できません: %v", st.Err)
	case !st.LoggedIn:
		return nil, "tailscaled が tailnet にログインしていません"
	}
	ips := tailnetAddrs(st.IPs)
	if len(ips) == 0 {
		return nil, "tailnet の IP アドレスがありません"
	}
	p := s.port()
	if p < 1 || p > 65535 {
		return nil, fmt.Sprintf("ポート番号が不正です: %d", p)
	}
	aps := make([]netip.AddrPort, len(ips))
	for i, ip := range ips {
		aps[i] = netip.AddrPortFrom(ip, uint16(p))
	}
	return aps, ""
}

// newHTTPServer は tailnet 向けの http.Server を作る。WriteTimeout は付けない（SSE などの長い応答があるため）。
func newHTTPServer(ctx context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

func (s *serveLoop) bind(ap netip.AddrPort) (*bound, error) {
	network := "tcp6"
	if ap.Addr().Is4() {
		network = "tcp4"
	}
	ln, err := s.l.listen(s.ctx, network, ap.String())
	if err != nil {
		return nil, err
	}
	b := &bound{
		ap:     ap,
		srv:    newHTTPServer(s.ctx, s.h),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(b.done)
		if err := b.srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.died <- b:
			case <-b.closed:
			}
		}
	}()
	return b, nil
}

func (s *serveLoop) stopAll() {
	for ap, b := range s.bounds {
		b.stop()
		delete(s.bounds, ap)
	}
	s.publish("")
}

// publish は Listen の集合と理由から State を更新し、変化があれば OnChange を呼ぶ。
// reason が空のとき、失敗したアドレスがあればその旨を理由にする。
func (s *serveLoop) publish(reason string) {
	addrs := make([]string, 0, len(s.bounds))
	for _, ap := range sortedAddrPorts(s.bounds) {
		addrs = append(addrs, ap.String())
	}
	if reason == "" && len(s.failures) > 0 {
		var msgs []string
		for _, ap := range sortedAddrPorts(s.failures) {
			msgs = append(msgs, s.failures[ap])
		}
		reason = strings.Join(msgs, "; ")
	}
	if reason == "" && len(addrs) == 0 {
		reason = "Listen を停止しました"
	}
	next := ListenState{Listening: len(addrs) > 0, Addrs: addrs, Reason: reason}

	s.l.mu.Lock()
	changed := next.Listening != s.l.state.Listening || next.Reason != s.l.state.Reason ||
		!slices.Equal(next.Addrs, s.l.state.Addrs)
	s.l.state = next
	s.l.mu.Unlock()
	if changed && s.l.onChg != nil {
		cp := next
		cp.Addrs = slices.Clone(next.Addrs)
		s.l.onChg(cp)
	}
}

// sortedAddrPorts は m のキーを IPv4 → IPv6、アドレス昇順に並べる。
func sortedAddrPorts[V any](m map[netip.AddrPort]V) []netip.AddrPort {
	keys := make([]netip.AddrPort, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b netip.AddrPort) int {
		if a.Addr().Is4() != b.Addr().Is4() {
			if a.Addr().Is4() {
				return -1
			}
			return 1
		}
		return a.Compare(b)
	})
	return keys
}
