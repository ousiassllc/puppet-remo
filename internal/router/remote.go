package router

import (
	"errors"
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// RemoteConfig は NewRemote の依存先。すべて必須。
type RemoteConfig struct {
	// Chain は API 仕様 §1 のミドルウェア連鎖（hostcheck → csrfheader → killswitch → whois → allowlist → bearer）の依存先。
	Chain middleware.RemoteConfig
	// MCP は /mcp の http.Handler（mcp.Server.Handler）。
	MCP http.Handler
}

// NewRemote はリモートルーター（tailnet IP の TCP ポートで配信する）を返す。
//
// M1 が配信するのは /mcp だけで、Web UI・WebSocket・/api/v1 は M2 から足す。連鎖は全リクエストの手前に置くので、
// 経路が存在しなくても、未登録デバイスの接続要求・キルスイッチの 503・Host / Origin の検査は同じように働く。
// 連鎖を通った未知の経路は 404。ホスト GUI のハンドラは、ここには決して登録しない（構造で分離する）。
// csp は連鎖の外側（連鎖が返す拒否の応答にも付く位置）に置く。
func NewRemote(cfg RemoteConfig) (http.Handler, error) {
	c := cfg.Chain
	if c.Hosts == nil || c.KillSwitch == nil || c.WhoIs == nil || c.Authz == nil || cfg.MCP == nil {
		return nil, errors.New("router: remote router requires the host source, kill switch, WhoIs, Authz and the MCP handler")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", cfg.MCP)
	mux.Handle("/", http.NotFoundHandler())
	return middleware.CSP()(middleware.Remote(c, mux)), nil
}
