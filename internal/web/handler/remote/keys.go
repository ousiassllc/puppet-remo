package remote

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
)

// SendKeys は POST /viewer/keys/send（ブラウザが先取りするショートカットの送信。FR-W06）。フォームの keys は API 仕様 §3.3 の
// 書式（`alt+tab` など）。操作権が要る。ctrl+alt+delete は InputGuard が拒否し、409 input_blocked（FR-W08）。
func (h *Handler) SendKeys(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	if !h.parseForm(c) {
		return
	}
	keys, _ := formValue(c, "keys")
	combo, err := domain.ParseKeyCombo(keys)
	if err != nil {
		h.fail(c, err)
		return
	}
	if _, err := h.d.Control.Do(c.Request.Context(), who, domain.KeyPress{Keys: combo}); err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.KeysSent(combo.String()))
}
