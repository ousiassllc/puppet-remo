package tailnet

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// MaxWhoIsTTL は WhoIs 結果のキャッシュ期間の上限（NFR-S05）。
const MaxWhoIsTTL = 10 * time.Second

// WhoIs は接続元 IP → ユーザー・デバイスの解決（IP 単位に最大 10 秒キャッシュ）。
type WhoIs interface {
	// Lookup は remoteAddr（IP または IP:port）を解決する。失敗は ErrWhoIsFailed（fail closed）。
	Lookup(ctx context.Context, remoteAddr string) (Identity, error)
}

// WhoIsConfig は NewWhoIs の設定。
type WhoIsConfig struct {
	API LocalAPI
	// Clock は nil なら SystemClock。キャッシュ期限の判定に使う。
	Clock domain.Clock
	// TTL は 0 以下または MaxWhoIsTTL 超なら MaxWhoIsTTL。
	TTL time.Duration
}

type whoIsEntry struct {
	id      Identity
	expires time.Time
}

type whoIsCache struct {
	api   LocalAPI
	clock domain.Clock
	ttl   time.Duration

	mu        sync.Mutex
	entries   map[netip.Addr]whoIsEntry
	nextSweep time.Time
}

// NewWhoIs は LocalAPI の WhoIs にキャッシュを被せた WhoIs を返す。
// 失敗（エラー・不完全な応答）はキャッシュしない。
func NewWhoIs(cfg WhoIsConfig) WhoIs {
	c := &whoIsCache{
		api:     cfg.API,
		clock:   cfg.Clock,
		ttl:     cfg.TTL,
		entries: make(map[netip.Addr]whoIsEntry),
	}
	if c.clock == nil {
		c.clock = SystemClock
	}
	if c.ttl <= 0 || c.ttl > MaxWhoIsTTL {
		c.ttl = MaxWhoIsTTL
	}
	return c
}

func (c *whoIsCache) Lookup(ctx context.Context, remoteAddr string) (Identity, error) {
	ip, err := parseRemoteIP(remoteAddr)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrWhoIsFailed, err)
	}
	if id, ok := c.cached(ip); ok {
		return id, nil
	}
	id, err := c.api.WhoIs(ctx, ip.String())
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrWhoIsFailed, err)
	}
	c.store(ip, id)
	return id, nil
}

func (c *whoIsCache) cached(ip netip.Addr) (Identity, bool) {
	now := c.clock.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ip]
	if !ok {
		return Identity{}, false
	}
	if !now.Before(e.expires) {
		delete(c.entries, ip)
		return Identity{}, false
	}
	return e.id, true
}

func (c *whoIsCache) store(ip netip.Addr, id Identity) {
	now := c.clock.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !now.Before(c.nextSweep) { // 二度と来ない IP のエントリが溜まらないよう、期限切れを定期的に掃除する
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		c.nextSweep = now.Add(c.ttl)
	}
	c.entries[ip] = whoIsEntry{id: id, expires: now.Add(c.ttl)}
}

// parseRemoteIP は "ip" または "ip:port" から IP を取り出す。ゾーンは落とし、IPv4 射影アドレスは IPv4 に戻す。
func parseRemoteIP(remoteAddr string) (netip.Addr, error) {
	var ip netip.Addr
	if ap, err := netip.ParseAddrPort(remoteAddr); err == nil {
		ip = ap.Addr()
	} else if a, err := netip.ParseAddr(remoteAddr); err == nil {
		ip = a
	} else {
		return netip.Addr{}, fmt.Errorf("invalid remote address %q", remoteAddr)
	}
	return ip.WithZone("").Unmap(), nil
}
