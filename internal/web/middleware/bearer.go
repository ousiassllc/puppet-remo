package middleware

import (
	"context"
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// Bearer は /mcp の Bearer トークンを検証する（形式・照合は NFR-S04、node ID 束縛は FR-A05）。
// 失敗は 401 unauthorized（WWW-Authenticate: Bearer）、同一デバイスの失敗が 1 分 10 回を超えたら
// 429 rate_limited（Retry-After 付き）。/mcp 以外は何もしない。
// allowlist を通っていないリクエストは whois_failed で止める（連鎖の組み間違いで素通りさせない）。
func Bearer(auth service.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind := classify(r)
			if kind != routeMCP {
				next.ServeHTTP(w, r)
				return
			}
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				writeReject(w, r, kind, reject{code: domain.CodeWhoisFailed})
				return
			}
			tok, err := auth.VerifyBearer(r.Context(), r.RemoteAddr, p, r.Header.Get("Authorization"))
			if err != nil {
				rejectFor(w, r, kind, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey, tok)))
		})
	}
}
