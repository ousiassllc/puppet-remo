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
}

// Remote は API 仕様 §1 の連鎖を、hostcheck → csrfheader → killswitch → whois → allowlist → bearer の順で
// h の手前に組む。csp（最外）は呼び出し側が Remote の外側に置く。
func Remote(cfg RemoteConfig, h http.Handler) http.Handler {
	chain := []func(http.Handler) http.Handler{
		HostCheck(cfg.Hosts),
		RequireCSRFHeader(),
		KillSwitch(cfg.KillSwitch),
		WhoIs(cfg.WhoIs),
		Allowlist(cfg.Authz),
		Bearer(cfg.Authz),
	}
	for i := len(chain) - 1; i >= 0; i-- {
		h = chain[i](h)
	}
	return h
}
