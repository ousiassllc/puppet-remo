package remote

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
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
	domain.CodeApprovalDenied:     "ホストが操作権の取得を拒否しました。",
	domain.CodeApprovalTimeout:    "ホストから時間内に応答がなく、操作権を取得できませんでした。",
	domain.CodeInvalidArgument:    "入力内容を確認してください。",
	domain.CodeConflict:           "現在の状態と合わないため実行できませんでした。",
	domain.CodeKillSwitchActive:   "キルスイッチが発動中です。",
	domain.CodeDesktopUnavailable: "ホストの画面を操作できません（ロック画面など）。",
	domain.CodeWindowNotFound:     "選んだウィンドウは閉じられたか、別のウィンドウに置き換わりました。一覧を更新してください。",
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
		case string(domain.ReasonTargetMinimized):
			return "選んだウィンドウは最小化されています。"
		case string(domain.ReasonTargetUnsupported):
			return "このウィンドウは対象にできません（検証前の種類）。"
		case string(domain.ReasonClipboardOutOfScope):
			return "ウィンドウを選んだ操作中は、クリップボードの読み取りは使えません（書き込みだけ使えます）。"
		case string(domain.ReasonKeyOutOfScope):
			return "ウィンドウを選んだ操作中は、このキー操作（Alt+Tab・Win など）は使えません。"
		case string(domain.ReasonTargetHalted):
			return "対象の操作は停止中です。ホスト側で原因を取り除き、「再開」を押してください。"
		}
	}
	return "ホストの前面のウィンドウには入力できません。"
}

// fail はアクションルートの失敗を、data-error-code 付きのインラインアラートのフラグメントとして、対応するステータスで返す。
// 画面の領域は現状維持で、HX-Retarget / HX-Reswap で #alert-root の中身を置き換える（response-targets は HX-Retarget 付きの
// 非 200 応答を差し込む）。想定外のエラー（5xx）はログに残す。
func (h *Handler) fail(c *gin.Context, err error) {
	f := classify(err)
	if f.status >= 500 && f.code == string(domain.CodeInternal) {
		h.log.Error("web action failed", "method", c.Request.Method, "path", c.Request.URL.Path, "error", err)
	}
	h.failMessage(c, f.status, f.code, f.message)
}

// failMessage は message を指定して fail と同じ形で返す。確認モーダル（強制剥奪）の確定（GET 以外）が失敗したとき、アラートが暗幕の
// 背面に隠れないように、応答にモーダルを閉じる OOB（organisms.ModalClear）を添える。GET には添えない: 確認モーダル自体の取得は失敗しても
// #modal-root が空で閉じる意味がなく、remo:lease-changed の GET /viewer/lease の失敗で、開いている強制剥奪のモーダルを閉じてしまうため。
func (h *Handler) failMessage(c *gin.Context, status int, code, message string) {
	c.Header("HX-Retarget", "#alert-root")
	c.Header("HX-Reswap", "innerHTML")
	comps := []templ.Component{atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Code: code, Heading: "エラー", Message: message, Dismissible: true})}
	if c.Request.Method != http.MethodGet {
		comps = append(comps, organisms.ModalClear())
	}
	h.render(c, status, comps...)
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
		h.render(c, f.status, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Code: f.code, Heading: "エラー", Message: f.message}))
		return
	}
	h.render(c, f.status, pages.RemoteError(view.RemoteError{Code: f.code, Title: "ビューアを表示できません", Message: f.message}))
}

// NotFound は登録のないパス。
func (h *Handler) NotFound(c *gin.Context) {
	h.renderErrorPage(c, failure{http.StatusNotFound, codeNotFound, "ページが見つかりません。"})
}
