package gui

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// codeNotFound は対象（クライアント・トークン）が無いときの data-error-code。API 仕様 §2 に該当するコードが無いため GUI 内で定める。
const codeNotFound = "not_found"

// failure はエラーの HTTP 応答の内容。message は利用者向けの固定文で、内部のエラー文は出さない。
type failure struct {
	status  int
	code    string
	message string
}

func classify(err error) failure {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return failure{http.StatusNotFound, codeNotFound, "対象が見つかりません。すでに削除されている可能性があります。"}
	case errors.Is(err, domain.ErrInvalidArgument):
		return failure{http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), "入力内容を確認してください。"}
	}
	switch domain.CodeOf(err) {
	case domain.CodeTaskNotFound:
		return failure{http.StatusNotFound, codeNotFound, "対象が見つかりません。すでに削除されている可能性があります。"}
	case domain.CodeConflict:
		return failure{http.StatusConflict, string(domain.CodeConflict), "すでに処理済みか、現在の状態と合わないため実行できませんでした。"}
	case domain.CodeKillSwitchActive:
		return failure{http.StatusServiceUnavailable, string(domain.CodeKillSwitchActive), "キルスイッチが発動中です。"}
	}
	return failure{http.StatusInternalServerError, string(domain.CodeInternal), "内部エラーが発生しました。"}
}

// fail はアクションルートの失敗を、data-error-code 付きのトーストのフラグメントとして同じステータスで返す。
// メインの領域は現状維持で、HX-Retarget / HX-Reswap で #toast-root へ追加する（response-targets は
// HX-Retarget 付きの非 200 応答を差し込む）。想定外のエラー（5xx）はログに残す。
func (h *Handler) fail(c *gin.Context, err error) {
	f := classify(err)
	if f.status >= 500 {
		h.log.Error("gui action failed", "method", c.Request.Method, "path", c.Request.URL.Path, "error", err)
	}
	c.Header("HX-Retarget", "#toast-root")
	c.Header("HX-Reswap", "beforeend")
	h.render(c, f.status, atoms.Toast(view.Toast{Level: view.ToastError, Message: f.message, ErrorCode: f.code}))
}

// failMessage は message を指定して fail と同じ形で返す。
func (h *Handler) failMessage(c *gin.Context, status int, code, message string) {
	c.Header("HX-Retarget", "#toast-root")
	c.Header("HX-Reswap", "beforeend")
	h.render(c, status, atoms.Toast(view.Toast{Level: view.ToastError, Message: message, ErrorCode: code}))
}
