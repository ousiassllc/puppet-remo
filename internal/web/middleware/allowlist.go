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
//
// states は、未登録デバイスのブラウザ（HTML の経路）へ返すページの出し分け（待機・拒否・失効。FR-W02）のための、
// デバイス ID による接続要求の状態の読み取り専用の参照。照合と接続要求の作成・集約（FR-A06）のあとに引くだけで、
// 応答の状態コード・エラーコードと FR-A06 の挙動は変えない。nil なら常に待機ページ。
func Allowlist(auth service.Authenticator, states service.ConnRequestStates) func(http.Handler) http.Handler {
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
				rj := rejectFrom(err)
				if rj.code == domain.CodeDeviceNotRegistered && !kind.problem() {
					rj.connState = connState(r.Context(), states, id)
				}
				writeReject(w, r, kind, rj)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey, client)))
		})
	}
}

// connState は未登録デバイスの接続要求の状態を読む。読めなければ待機（ログにだけ残す）。
func connState(ctx context.Context, states service.ConnRequestStates, id service.Identity) service.ConnRequestState {
	if states == nil {
		return service.ConnRequestWaiting
	}
	st, err := states.State(ctx, id.StableNodeID)
	if err != nil {
		slog.Warn("reading the connection request state failed; showing the waiting page", "error", err)
	}
	return st
}

// rejectFrom は Authz のエラーを拒否の内容にする。ドメインエラー以外（DB 障害など）は 500 internal にし、
// 詳細はログにだけ残す（応答には出さない）。
func rejectFrom(err error) reject {
	code := domain.CodeOf(err)
	if code == domain.CodeInternal {
		slog.Error("authorization failed", "error", err)
	}
	return reject{code: code, retryAfter: service.RetryAfter(err)}
}

// rejectFor は Authz のエラーを拒否応答にする。
func rejectFor(w http.ResponseWriter, r *http.Request, kind routeKind, err error) {
	writeReject(w, r, kind, rejectFrom(err))
}
