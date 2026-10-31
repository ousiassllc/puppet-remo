// Package router は 2 つのルーター（GUI・リモート）を組み立てる（docs/architecture/overview.md、API 仕様 §1）。
package router

import (
	"errors"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// GUIConfig は NewGUI の依存先。
type GUIConfig struct {
	Handler *gui.Handler
	// Static は /static/* の配信（static.Handler。/static を外して渡す）。
	Static http.Handler
}

var ginModeOnce sync.Once

// GUIMaxBodyBytes は GUI の状態変更リクエストの本文の上限（1 MiB。リモートの MCP の MaxRequestBodyBytes と同じ）。
// 起動許可アプリのフォームは、パス（32,766 文字）・引数（合計 8,192 文字）を URL エンコードしても数百 KB に収まる。
const GUIMaxBodyBytes = 1 << 20

// NewGUI はホスト GUI のルーター（プロセス内。ソケットを開かない）を返す。Wails の AssetOptions.Handler に渡す。
//
// すべての応答（404・静的ファイルを含む）に csp ミドルウェアが CSP ヘッダーを付ける（NFR-S19）。
// 動的な応答は Cache-Control: no-store（トークンの平文などをキャッシュさせない）、/static は
// Cache-Control: no-cache + ETag（static が付ける）。/wails/* は Wails のランタイム用に予約し、登録しない。
func NewGUI(cfg GUIConfig) (http.Handler, error) {
	if cfg.Handler == nil || cfg.Static == nil {
		return nil, errors.New("router: GUI handler and static handler are required")
	}
	ginModeOnce.Do(func() { gin.SetMode(gin.ReleaseMode) })

	e := gin.New()
	e.RedirectTrailingSlash = false
	e.RedirectFixedPath = false
	e.HandleMethodNotAllowed = false
	e.Use(gin.Recovery())

	static := http.StripPrefix("/static", cfg.Static)
	e.GET("/static/*filepath", gin.WrapH(static))
	e.HEAD("/static/*filepath", gin.WrapH(static))

	dynamic := e.Group("", noStore)
	cfg.Handler.Register(dynamic)
	e.NoRoute(noStore, cfg.Handler.NotFound)

	return middleware.CSP()(middleware.LimitBody(GUIMaxBodyBytes)(e)), nil
}

// noStore は動的な応答をキャッシュさせない。
func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}
