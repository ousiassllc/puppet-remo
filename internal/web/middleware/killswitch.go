package middleware

import (
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// KillSwitchState はキルスイッチの発動状態（service の KillSwitch が満たす）。
type KillSwitchState interface {
	Active() bool
}

// KillSwitch は、キルスイッチ発動中の全リクエストに 503 kill_switch_active を返す（FR-H06、NFR-S07）。
// WhoIs・許可リスト照合・接続要求の作成より前に置くので、未登録デバイスの接続要求も作らない（FR-A06）。
func KillSwitch(ks KillSwitchState) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ks.Active() {
				writeReject(w, r, classify(r), reject{code: domain.CodeKillSwitchActive})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
