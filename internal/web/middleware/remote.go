package middleware

import (
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// RemoteConfig はリモートルーターの連鎖の依存先。すべて必須。
type RemoteConfig struct {
	Hosts      HostSource
	KillSwitch KillSwitchState
	WhoIs      service.WhoIs
	Authz      service.Authenticator
	// ConnStates は未登録デバイスのブラウザに返すページ（待機・拒否・失効。FR-W02）を選ぶための、接続要求の状態の読み取り専用の参照。
	// nil なら常に待機ページ。応答のステータス・エラーコードと接続要求の作成（FR-A06）には関わらない。
	ConnStates service.ConnRequestStates
}

// Remote は API 仕様 §1 の連鎖を、hostcheck → csrfheader → killswitch → whois → allowlist → bearer の順で
// h の手前に組む。csp（最外）は呼び出し側が Remote の外側に置く。
func Remote(cfg RemoteConfig, h http.Handler) http.Handler {
	chain := []func(http.Handler) http.Handler{
		HostCheck(cfg.Hosts),
		RequireCSRFHeader(),
		KillSwitch(cfg.KillSwitch),
		WhoIs(cfg.WhoIs),
		Allowlist(cfg.Authz, cfg.ConnStates),
		Bearer(cfg.Authz),
	}
	for i := len(chain) - 1; i >= 0; i-- {
		h = chain[i](h)
	}
	return h
}
