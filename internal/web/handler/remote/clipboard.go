package remote

import (
	"net/http"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
)

// maxClipboardBytes はクリップボードに送れるテキストの上限（1 MiB。API 仕様 §4.2、service.Control の上限と同じ）。
const maxClipboardBytes = 1 << 20

// SendClipboard は POST /viewer/clipboard/send（ブラウザ → ホスト。FR-W07）。フォームの text をホストのクリップボードに設定する。
// 操作権が要る（持っていなければ 409 lease_required）。本文は応答にも監査にもログにも出さない（文字数だけ）。
func (h *Handler) SendClipboard(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	if !h.parseForm(c) {
		return
	}
	text, present := formValue(c, "text")
	if !present {
		h.failMessage(c, http.StatusBadRequest, string(domain.CodeInvalidArgument), "送るテキストがありません。")
		return
	}
	if len(text) > maxClipboardBytes {
		h.failMessage(c, http.StatusBadRequest, string(domain.CodeInvalidArgument), "テキストは 1 MiB までです。")
		return
	}
	if _, err := h.d.Control.Do(c.Request.Context(), who, domain.ClipboardWrite{Text: text}); err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.ClipboardSent(utf8.RuneCountInString(text)))
}

// FetchClipboard は POST /viewer/clipboard/fetch（ホスト → ブラウザ。FR-W07）。ホストのクリップボードを読み取り専用の
// 表示欄（<textarea readonly>）に出し、利用者が手動でコピーする。操作権が要る。
func (h *Handler) FetchClipboard(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	res, err := h.d.Control.Do(c.Request.Context(), who, domain.ClipboardRead{})
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.ClipboardFetched(res.Text))
}
