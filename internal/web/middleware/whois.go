package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// WhoIs は接続元 IP を WhoIs で解決し、ユーザーと node ID をコンテキストに載せる。
// 失敗は 403 whois_failed（fail closed。FR-A03、NFR-S05）。
func WhoIs(whois service.WhoIs) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := service.ResolveIdentity(r.Context(), whois, r.RemoteAddr)
			if err != nil {
				slog.Warn("whois failed; the request was rejected", "remote_addr", r.RemoteAddr, "error", err)
				writeReject(w, r, classify(r), reject{code: domain.CodeWhoisFailed})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
		})
	}
}
