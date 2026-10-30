package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// Allowlist は許可リスト（node ID + ログイン名）を照合する。
// 未登録（または登録時と異なるユーザー）は 403 device_not_registered で、接続要求を作成・集約する（FR-A06）。
// 登録済みでも経路の機能（/mcp は MCP、それ以外は Web）が無効なら 403 client_disabled。
// whois を通っていないリクエストは 403 whois_failed にする（連鎖の組み間違いで素通りさせない）。
func Allowlist(auth service.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind := classify(r)
			id, ok := IdentityFrom(r.Context())
			if !ok {
				writeReject(w, r, kind, reject{code: domain.CodeWhoisFailed})
				return
			}
			client, err := auth.CheckAllowlist(r.Context(), r.RemoteAddr, id, kind.feature())
			if err != nil {
				rejectFor(w, r, kind, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey, client)))
		})
	}
}

// rejectFor は Authz のエラーを拒否応答にする。ドメインエラー以外（DB 障害など）は 500 internal にし、
// 詳細はログにだけ残す（応答には出さない）。
func rejectFor(w http.ResponseWriter, r *http.Request, kind routeKind, err error) {
	code := domain.CodeOf(err)
	if code == domain.CodeInternal {
		slog.Error("authorization failed", "error", err)
	}
	writeReject(w, r, kind, reject{code: code, retryAfter: service.RetryAfter(err)})
}
