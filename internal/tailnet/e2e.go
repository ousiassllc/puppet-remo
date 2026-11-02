//go:build e2e

package tailnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
)

// このファイルは E2E（Playwright。docs/architecture/frontend.md §9）のテスト専用サーバーが使う、偽の tailnet の部品。
// ビルドタグ e2e を付けたときだけコンパイルされ、本番のバイナリには入らない（NFR-S01 の例外はこのタグに限る）。

// E2ETailnetIP は E2E の偽の tailnet が報告する IPv4。本物の Listener（NewListener）が Listen する対象に選ぶアドレス
// （100.64.0.0/10）で、LoopbackListenFunc がこの宛先を loopback に差し替える。
const E2ETailnetIP = "100.64.0.1"

// E2EDNSName は E2E の偽の tailnet が報告する MagicDNS 名。
const E2EDNSName = "e2e-host.tail-e2e.ts.net"

// E2EStatus は E2E の偽の tailnet の状態（ログイン済み。IP は E2ETailnetIP と 127.0.0.1、MagicDNS 名は E2EDNSName）。
//
// 127.0.0.1 を足すのは、ブラウザが loopback へ素直につなぎ、その Host（127.0.0.1:<port>）をリモートルーターの Host 検査
// （NFR-S02。報告された IP・名前と設定のポートだけを通す）に通すため。Listener は loopback を Listen の対象にしない
// （tailnetAddrs が除く）ので、Listen する宛先は E2ETailnetIP だけのまま。
func E2EStatus() Status {
	return Status{
		LoggedIn: true,
		DNSName:  E2EDNSName,
		IPs:      []netip.Addr{netip.MustParseAddr(E2ETailnetIP), netip.MustParseAddr("127.0.0.1")},
	}
}

// E2EWatcher は固定の状態を 1 度だけ流す偽の Watcher。ctx の終了でチャネルを閉じる。
type E2EWatcher struct{ st Status }

// NewE2EWatcher は st を流す偽の Watcher を返す。
func NewE2EWatcher(st Status) *E2EWatcher { return &E2EWatcher{st: st} }

// Watch は Watcher を満たす。
func (w *E2EWatcher) Watch(ctx context.Context) (<-chan Status, error) {
	ch := make(chan Status, 1)
	ch <- w.st
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// E2EWhoIs はどの接続元にも同じ（切り替えられる）デバイスを返す偽の WhoIs。
type E2EWhoIs struct {
	mu sync.Mutex
	id Identity
}

// NewE2EWhoIs は id を返す偽の WhoIs を返す。
func NewE2EWhoIs(id Identity) *E2EWhoIs { return &E2EWhoIs{id: id} }

// Set は以後の Lookup が返すデバイスを差し替える。
func (w *E2EWhoIs) Set(id Identity) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.id = id
}

// Lookup は WhoIs を満たす。
func (w *E2EWhoIs) Lookup(context.Context, string) (Identity, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.id, nil
}

// LoopbackListenFunc は、Listener が tailnet の IPv4 に Listen しようとするたびに、同じポートの 127.0.0.1 で待ち受ける ListenFunc
// （E2E のテスト専用サーバーの Listener。ListenerConfig.ListenFunc に渡す）。Listener の対象の絞り込み・再試行・State は本物のまま、
// ソケットだけが loopback になる。IPv6 の宛先は受けない（E2EStatus は IPv4 だけ）。
func LoopbackListenFunc() ListenFunc {
	return func(ctx context.Context, network, address string) (net.Listener, error) {
		if network != "tcp4" {
			return nil, fmt.Errorf("tailnet: e2e listener supports tcp4 only: %q", network)
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return nil, err
		}
		if !ap.Addr().Is4() {
			return nil, errors.New("tailnet: e2e listener supports IPv4 only: " + address)
		}
		var lc net.ListenConfig
		return lc.Listen(ctx, "tcp4", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ap.Port()).String())
	}
}
