package middleware

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

type ctxKey int

const (
	identityKey ctxKey = iota
	clientKey
	tokenKey
)

// IdentityFrom は whois を通ったリクエストの接続元を返す。
func IdentityFrom(ctx context.Context) (service.Identity, bool) {
	id, ok := ctx.Value(identityKey).(service.Identity)
	return id, ok
}

// ClientFrom は allowlist を通ったリクエストのクライアントを返す。
func ClientFrom(ctx context.Context) (domain.Client, bool) {
	c, ok := ctx.Value(clientKey).(domain.Client)
	return c, ok
}

// TokenFrom は bearer を通ったリクエストのトークン（行）を返す。
func TokenFrom(ctx context.Context) (*service.TokenRecord, bool) {
	t, ok := ctx.Value(tokenKey).(*service.TokenRecord)
	return t, ok && t != nil
}

// PrincipalFrom は連鎖を通ったリクエストの主体を返す。allowlist を通っていなければ false。
// Token は /mcp のときだけ入る。
func PrincipalFrom(ctx context.Context) (service.Principal, bool) {
	c, ok := ClientFrom(ctx)
	if !ok {
		return service.Principal{}, false
	}
	id, _ := IdentityFrom(ctx)
	p := service.Principal{Client: c, Identity: id}
	p.Token, _ = TokenFrom(ctx)
	return p, true
}
