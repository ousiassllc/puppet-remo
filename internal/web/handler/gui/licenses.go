package gui

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/licenses"
)

// Licenses は GET /licenses（同梱する第三者のライセンスの全文。#133）。設定の画面から開く。
// 全文は go:embed した固定の文字列で、設定・状態を読まない（リモートのルーターには出さない）。
func (h *Handler) Licenses(c *gin.Context) {
	v := view.Licenses{Chrome: h.chrome(c.Request.Context(), view.NavSettings), Text: licenses.ThirdPartyNotices}
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.LicensesBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.Licenses(v))
}
