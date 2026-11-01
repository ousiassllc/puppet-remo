package remote

import (
	"bytes"
	"net/http"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"
)

// render は comp を最後まで描画してから status で書く（描画の途中で失敗したときに、壊れた HTML を返さない）。
// 動的な応答は Cache-Control: no-store（クリップボードの内容などをキャッシュさせない）。
func (h *Handler) render(c *gin.Context, status int, comps ...templ.Component) {
	var buf bytes.Buffer
	for _, comp := range comps {
		if err := comp.Render(c.Request.Context(), &buf); err != nil {
			h.log.Error("render failed", "path", c.Request.URL.Path, "error", err)
			c.Header("Content-Type", "text/plain; charset=utf-8")
			c.String(http.StatusInternalServerError, "render error")
			return
		}
	}
	c.Header("Cache-Control", "no-store")
	c.Data(status, "text/html; charset=utf-8", buf.Bytes())
}

// fragmentRequest は HTMX の部分更新（履歴の復元を除く）か。履歴の復元はフルページを返す。
func fragmentRequest(c *gin.Context) bool {
	return c.GetHeader("HX-Request") != "" && c.GetHeader("HX-History-Restore-Request") == ""
}
