package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// RemoteConfig は NewRemote の依存先。すべて必須。
type RemoteConfig struct {
	// Chain は API 仕様 §1 のミドルウェア連鎖（hostcheck → csrfheader → killswitch → whois → allowlist → bearer）の依存先。
	Chain middleware.RemoteConfig
	// MCP は /mcp の http.Handler（mcp.Server.Handler）。
	MCP http.Handler
	// WS は /ws/screen の http.Handler（ws.Handler）。GET だけを受け付ける。
	WS http.Handler
	// Web は Web UI（/ と /viewer*）のハンドラ。
	Web *remote.Handler
	// Static は /static/* の配信（static.Handler。/static を外して渡す）。
	Static http.Handler
}

// NewRemote はリモートルーター（tailnet IP の TCP ポートで配信する）を返す。
//
// 配信するのは /mcp・GET /ws/screen・Web UI（GET /、/viewer*。API 仕様 §4.2）・GET /static/*（M2）で、/api/v1 は M2 以降の
// 別の段階で足す。連鎖は全リクエストの手前に置くので、経路が存在しなくても、未登録デバイスの接続要求・キルスイッチの 503・
// Host / Origin の検査は同じように働く。連鎖を通った未知の経路は 404。
// ホスト GUI のハンドラは、ここには決して登録しない（構造で分離する）。
// csp は連鎖の外側（連鎖が返す拒否の応答にも付く位置）に置く。
func NewRemote(cfg RemoteConfig) (http.Handler, error) {
	c := cfg.Chain
	if c.Hosts == nil || c.KillSwitch == nil || c.WhoIs == nil || c.Authz == nil || cfg.MCP == nil || cfg.WS == nil ||
		cfg.Web == nil || cfg.Static == nil {
		return nil, errors.New("router: remote router requires the host source, kill switch, WhoIs, Authz, the MCP, WebSocket and Web UI handlers and the static handler")
	}
	web := newWebEngine(cfg.Web)
	mux := http.NewServeMux()
	mux.Handle("/mcp", cfg.MCP)
	mux.Handle("GET /ws/screen", cfg.WS)
	// GET のパターンは HEAD も受ける。/static は外して渡す（static は Cache-Control: no-cache + ETag を付ける）。
	mux.Handle("GET /static/", http.StripPrefix("/static", cfg.Static))
	mux.Handle("GET /{$}", web)
	mux.Handle("/viewer", web)
	mux.Handle("/viewer/", web)
	mux.Handle("/", http.NotFoundHandler())
	return middleware.CSP()(middleware.Remote(c, mux)), nil
}

// newWebEngine は Web UI のルートを Gin で配信する（動的な応答の Cache-Control: no-store はハンドラが付ける）。
func newWebEngine(h *remote.Handler) http.Handler {
	ginModeOnce.Do(func() { gin.SetMode(gin.ReleaseMode) })
	e := gin.New()
	e.RedirectTrailingSlash = false
	e.RedirectFixedPath = false
	e.HandleMethodNotAllowed = false
	e.Use(gin.Recovery())
	h.Register(e)
	e.NoRoute(h.NotFound)
	return e
}
