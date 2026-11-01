package gui

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは操作中オーバーレイ（#69）の中身（GET /overlay?label=...）。ウィンドウを出し入れするのは app（Bus の lease）で、
// ここは枠と文言を描くだけ。状態を持たず、リースやクライアントの情報は読まない（文言は app が URL で渡す）。

// overlayLabelMaxRunes は label の長さの上限（文字数）。クライアント名は利用者が付けた任意の文字列なので、札が画面を覆わないよう切り詰める。
const overlayLabelMaxRunes = 80

// Overlay は GET /overlay。label は HTML エスケープして描画し（templ）、長ければ末尾を「…」にして切り詰める。
func (h *Handler) Overlay(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	h.render(c, http.StatusOK, pages.Overlay(view.Overlay{Label: truncateRunes(c.Query("label"), overlayLabelMaxRunes)}))
}

// truncateRunes は s が max 文字を超えるとき、max 文字（末尾の「…」を含む）に切り詰める。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
