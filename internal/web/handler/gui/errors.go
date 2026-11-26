package gui

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
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
	case domain.CodeAuditUnavailable:
		// 設定の変更を監査に書けなかった（fail-closed。変更は元に戻した。#174・#182）。
		return auditChangeFailure(err)
	}
	return failure{http.StatusInternalServerError, string(domain.CodeInternal), "内部エラーが発生しました。"}
}

// auditSubjectLabels は、設定変更の対象（service.AuditSubject*）の、アラートの文に入れる呼び名。
var auditSubjectLabels = map[string]string{
	service.AuditSubjectLaunchMode:     "起動の方式",
	service.AuditSubjectLaunchableApp:  "起動許可リスト",
	service.AuditSubjectLaunchDenylist: "拒否リスト",
	service.AuditSubjectHostSettings:   "設定",
	service.AuditSubjectAgentSettings:  "エージェントの設定",
	service.AuditSubjectFastpath:       "高速パスの設定",
	service.AuditSubjectAPIKey:         "API キーの登録状態",
}

// auditChangeFailure は設定変更の audit_unavailable（503）の応答。元に戻せたときは「変更しませんでした」、
// 戻す書き込みも失敗したときは、変更が残っているかもしれないので、現在の状態を確認するよう伝える（警告文。#182）。
func auditChangeFailure(err error) failure {
	subject, restored, ok := service.AuditChangeFailure(err)
	restored = restored || !ok // 対象の情報が無いときは、戻せた扱い（戻せなかった場合だけ、サービスが Detail に印を付ける）
	label := auditSubjectLabels[subject]
	if label == "" {
		label = "設定"
	}
	code := string(domain.CodeAuditUnavailable)
	if !restored {
		return failure{http.StatusServiceUnavailable, code, "監査ログに書き込めず、" + label + "を元に戻すこともできませんでした。現在の状態を確認してください。"}
	}
	return failure{http.StatusServiceUnavailable, code, "監査ログに書き込めないため、" + label + "は変更しませんでした。時間をおいてやり直してください。"}
}

// errorAlert はエラーのインラインアラート（data-error-code 付き）。アクションの失敗と、応答に添えるエラー（OOB）の共通の見た目。
func errorAlert(code, message string) atoms.AlertProps {
	return atoms.AlertProps{Tone: atoms.AlertDanger, Code: code, Heading: "エラー", Message: message, Dismissible: true}
}

// fail はアクションルートの失敗を、data-error-code 付きのインラインアラートのフラグメントとして同じステータスで返す。
// メインの領域は現状維持で、HX-Retarget / HX-Reswap で #alert-root の中身を置き換える（response-targets は
// HX-Retarget 付きの非 200 応答を差し込む）。想定外のエラー（5xx）はログに残す。
func (h *Handler) fail(c *gin.Context, err error) {
	f := classify(err)
	if f.status >= 500 {
		h.log.Error("gui action failed", "method", c.Request.Method, "path", c.Request.URL.Path, "error", err)
	}
	h.failMessage(c, f.status, f.code, f.message)
}

// failMessage は message を指定して fail と同じ形で返す。確認モーダルの確定（GET 以外）が失敗したとき、アラートが暗幕の背面に
// 隠れないように、応答にモーダルを閉じる OOB（organisms.ModalClear）を添える。GET には添えない: 確認モーダル自体の取得は失敗しても
// #modal-root が空で閉じる意味がなく、開いている別のモーダルを巻き込んで閉じてしまうため。承認ダイアログのウィンドウ（#modal-root が無い）では、
// OOB の対象が無いので添えても無害。
func (h *Handler) failMessage(c *gin.Context, status int, code, message string, extra ...templ.Component) {
	c.Header("HX-Retarget", "#alert-root")
	c.Header("HX-Reswap", "innerHTML")
	comps := []templ.Component{atoms.Alert(errorAlert(code, message))}
	if c.Request.Method != http.MethodGet {
		comps = append(comps, organisms.ModalClear())
	}
	h.render(c, status, append(comps, extra...)...)
}
