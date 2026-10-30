// Package tailnet はホストの tailscaled との連携（LocalAPI クライアント）を担う。
//
// 構成:
//   - LocalAPI: tailscaled の LocalAPI を包む小さな境界。本物は NewLocalAPI、テストは偽実装に差し替える。
//   - Watcher: ログイン状態と tailnet IP の変化を流す（WatchIPNBus。切断時は再接続する）。
//   - WhoIs: 接続元 IP から ユーザー・デバイスを解決する（IP 単位に最大 10 秒キャッシュ、失敗は fail closed）。
//   - Listener: tailnet の IPv4・IPv6 アドレスだけで Listen し、状態の変化に追従して張り直す。
//
// 設計: docs/components/overview.md §4.2、FR-A01〜A03、NFR-S01/S05/A03。
package tailnet

import (
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Clock は現在時刻と待機の供給元。テストでは進行を制御できる実装に差し替える。
type Clock interface {
	domain.Clock
	// After は d 経過後に現在時刻を送るチャネルを返す。
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// SystemClock は実時間の Clock。
var SystemClock Clock = systemClock{}

// retryInterval の既定値。再接続・Listen の再試行を 1 秒間隔にして、
// tailscaled の再起動後 10 秒以内の復旧（NFR-A03）に収める。
const defaultRetryInterval = time.Second
