package middleware

import (
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

const (
	// CSRFHeader は状態変更リクエストが付けるカスタムヘッダー（NFR-S03）。
	CSRFHeader = "X-Puppet-Remo"
	csrfValue  = "1"
)

// RequireCSRFHeader は、状態変更（GET / HEAD / OPTIONS 以外）のリクエストに X-Puppet-Remo: 1 を必須とし、
// 無ければ 403 missing_header を返す（NFR-S03）。MCP は Bearer 必須のため対象外。
func RequireCSRFHeader() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind := classify(r)
			if kind != routeMCP && stateChanging(r) && r.Header.Get(CSRFHeader) != csrfValue {
				writeReject(w, r, kind, reject{code: domain.CodeMissingHeader})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
