package service

import (
	"context"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// TailscaledState はダッシュボードに出す tailscaled の状態（FR-H02）。
type TailscaledState string

const (
	TailscaledUnknown     TailscaledState = "unknown"     // まだ状態を受け取っていない
	TailscaledRunning     TailscaledState = "running"     // ログイン済みで tailnet IP がある
	TailscaledNeedsLogin  TailscaledState = "needs_login" // tailscaled には届くが、ログインしていない
	TailscaledUnavailable TailscaledState = "unavailable" // tailscaled に届かない
)

// ServerStatus はダッシュボードのサーバー状態（tailnet IP・ポート・tailscaled 状態。FR-H02）。
type ServerStatus struct {
	Tailscaled  TailscaledState
	TailnetIPs  []string
	DNSName     string
	Port        int      // 設定のポート
	Listening   bool     // 1 つ以上のアドレスで Listen 中
	ListenAddrs []string // Listen 中の "ip:port"
	Reason      string   // 届かない・Listen できない理由（利用者向けの短い文。内部情報は含めない）
}

// ServerStatusSource はサーバー状態の読み取り口。
type ServerStatusSource interface {
	ServerStatus(ctx context.Context) ServerStatus
}

// SessionInfo は接続中のセッション 1 件（MCP セッション。M2 から Web も）。
type SessionInfo struct {
	ID       string
	ClientID string
	Label    string // クライアントの表示名
	Kind     domain.ActorKind
	Since    time.Time
}

// SessionSource は接続中セッション一覧の読み取り口（MCP サーバーのセッション表が実装する）。
type SessionSource interface {
	Sessions() []SessionInfo
}

// ListenStateSource は Listener の状態の読み取り口（tailnet.Listener の一部）。
type ListenStateSource interface {
	State() tailnet.ListenState
}

// StatusTrackerConfig は NewStatusTracker の設定。
type StatusTrackerConfig struct {
	Watcher  tailnet.Watcher
	Listener ListenStateSource // nil なら Listen 状態は空
	// Port は設定のポート（server.port）を返す。nil なら 0。
	Port func() int
	// Bus は状態が変わったときに server-status を流す。nil なら通知しない。
	Bus *Bus
}

// StatusTracker は tailnet.Watcher の最新の状態と Listener の状態を束ねて ServerStatus にする。
type StatusTracker struct {
	cfg StatusTrackerConfig

	mu     sync.Mutex
	status tailnet.Status
	seen   bool
}

// NewStatusTracker は StatusTracker を作る。状態を集めるには Run を goroutine で動かす。
func NewStatusTracker(cfg StatusTrackerConfig) *StatusTracker {
	return &StatusTracker{cfg: cfg}
}

// Run は Watcher の状態を受け取り続ける。ctx の終了、または Watcher のチャネルが閉じたら戻る。
// Watch を始められなかったときはそのエラーを返す。
func (t *StatusTracker) Run(ctx context.Context) error {
	ch, err := t.cfg.Watcher.Watch(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case st, ok := <-ch:
			if !ok {
				return nil
			}
			t.mu.Lock()
			t.status, t.seen = st, true
			t.mu.Unlock()
			t.Notify()
		}
	}
}

// Notify は server-status を Bus へ流す。Listener の OnChange から呼ぶ。
func (t *StatusTracker) Notify() {
	if t.cfg.Bus != nil {
		t.cfg.Bus.Publish(Event{Topic: TopicServerStatus})
	}
}

// ServerStatus は現在のサーバー状態を返す。
func (t *StatusTracker) ServerStatus(context.Context) ServerStatus {
	t.mu.Lock()
	st, seen := t.status, t.seen
	t.mu.Unlock()

	out := ServerStatus{Tailscaled: TailscaledUnknown, DNSName: st.DNSName}
	if t.cfg.Port != nil {
		out.Port = t.cfg.Port()
	}
	switch {
	case !seen:
	case st.Err != nil:
		out.Tailscaled = TailscaledUnavailable
		out.Reason = "tailscaled に接続できません。"
	case !st.LoggedIn:
		out.Tailscaled = TailscaledNeedsLogin
		out.Reason = "tailscaled にログインしていません。"
	default:
		out.Tailscaled = TailscaledRunning
	}
	for _, ip := range st.IPs {
		out.TailnetIPs = append(out.TailnetIPs, ip.String())
	}
	if t.cfg.Listener != nil {
		ls := t.cfg.Listener.State()
		out.Listening, out.ListenAddrs = ls.Listening, ls.Addrs
		if out.Reason == "" {
			out.Reason = ls.Reason
		}
	}
	return out
}
