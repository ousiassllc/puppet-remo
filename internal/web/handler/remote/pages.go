package remote

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
)

// Root は GET /（/viewer へ 302）。
func (h *Handler) Root(c *gin.Context) {
	c.Redirect(http.StatusFound, "/viewer")
}

// Viewer は GET /viewer（ビューアのページ。FR-W01）。viewer_id を発行して埋め込む。
// HX-Request ならフラグメント（#viewer-root）、それ以外はフルページ（frontend.md §5.1）。
func (h *Handler) Viewer(c *gin.Context) {
	client, ok := middleware.ClientFrom(c.Request.Context())
	if !ok {
		h.renderErrorPage(c, failure{http.StatusForbidden, string(domain.CodeWhoisFailed), "接続元を特定できませんでした。"})
		return
	}
	v, err := h.viewerPage(c.Request.Context(), c.Request.Host, client)
	if err != nil {
		h.failPage(c, err)
		return
	}
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, organisms.ViewerRoot(v))
		return
	}
	h.render(c, http.StatusOK, pages.Viewer(v))
}
