package remote

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// codeNotFound は存在しないパスの data-error-code。API 仕様 §2 に該当するコードが無いため、ホスト GUI と同じく Web UI 内で定める。
const codeNotFound = "not_found"

// failure はエラーの HTTP 応答の内容。message は利用者向けの固定文で、内部のエラー文（クリップボードの本文を含みうる）は出さない。
type failure struct {
	status  int
	code    string
	message string
}

// failureMessages は API 仕様 §2 のコードごとの利用者向けの文。表に無いコードは internal。
var failureMessages = map[domain.Code]string{
	domain.CodeLeaseRequired:      "操作権がありません。先に「操作権を取得」を押してください。",
	domain.CodeLeaseHeldByOther:   "ほかのセッションが操作権を保持しています。",
	domain.CodeLeaseRevoked:       "操作権が強制的に外されました。改めて取得してください。",
	domain.CodeInvalidArgument:    "入力内容を確認してください。",
	domain.CodeConflict:           "現在の状態と合わないため実行できませんでした。",
	domain.CodeKillSwitchActive:   "キルスイッチが発動中です。",
	domain.CodeDesktopUnavailable: "ホストの画面を操作できません（ロック画面など）。",
}

func classify(err error) failure {
	code := domain.CodeOf(err)
	if code == domain.CodeInputBlocked {
		return failure{http.StatusConflict, string(code), inputBlockedMessage(err)}
	}
	if msg, ok := failureMessages[code]; ok {
		return failure{code.HTTPStatus(), string(code), msg}
	}
	return failure{http.StatusInternalServerError, string(domain.CodeInternal), "内部エラーが発生しました。"}
}

// inputBlockedMessage は input_blocked の理由（detail.reason。API 仕様 §2）に応じた文。
func inputBlockedMessage(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		switch de.Detail["reason"] {
		case "secure_attention_sequence":
			return "Ctrl+Alt+Del は送れません。"
		case domain.ReasonElevatedWindow:
			return "ホストの前面のウィンドウは管理者権限で動いているため、入力できません。"
		case domain.ReasonOwnWindow:
			return "ホストの前面のウィンドウは puppet-remo 自身のため、入力できません。"
		}
	}
	return "ホストの前面のウィンドウには入力できません。"
}

// fail はアクションルートの失敗を、data-error-code 付きのトーストのフラグメントとして、対応するステータスで返す。
// 画面の領域は現状維持で、HX-Retarget / HX-Reswap で #toast-root へ追加する（response-targets は HX-Retarget 付きの
// 非 200 応答を差し込む）。想定外のエラー（5xx）はログに残す。
func (h *Handler) fail(c *gin.Context, err error) {
	f := classify(err)
	if f.status >= 500 && f.code == string(domain.CodeInternal) {
		h.log.Error("web action failed", "method", c.Request.Method, "path", c.Request.URL.Path, "error", err)
	}
	h.failMessage(c, f.status, f.code, f.message)
}

// failMessage は message を指定して fail と同じ形で返す。
func (h *Handler) failMessage(c *gin.Context, status int, code, message string) {
	c.Header("HX-Retarget", "#toast-root")
	c.Header("HX-Reswap", "beforeend")
	h.render(c, status, atoms.Toast(view.Toast{Level: view.ToastError, Message: message, ErrorCode: code}))
}

// failPage はページルートの失敗を、エラーページ（HX-Request なら同じ領域に出すフラグメント）として返す。
func (h *Handler) failPage(c *gin.Context, err error) {
	f := classify(err)
	if f.status >= 500 && f.code == string(domain.CodeInternal) {
		h.log.Error("web page failed", "path", c.Request.URL.Path, "error", err)
	}
	h.renderErrorPage(c, f)
}

func (h *Handler) renderErrorPage(c *gin.Context, f failure) {
	if fragmentRequest(c) {
		h.render(c, f.status, atoms.Alert(f.code, f.message))
		return
	}
	h.render(c, f.status, pages.RemoteError(view.RemoteError{Code: f.code, Title: "ビューアを表示できません", Message: f.message}))
}

// NotFound は登録のないパス。
func (h *Handler) NotFound(c *gin.Context) {
	h.renderErrorPage(c, failure{http.StatusNotFound, codeNotFound, "ページが見つかりません。"})
}
