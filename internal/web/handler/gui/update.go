package gui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは更新の画面（FR-H09、FR-S09）。確認・適用は service.Updater が行い、ここは描画と受け付けだけ。
// 監査（kind = update）は実体が記録する。

// updateConfirmRoute は適用の確認モーダルの URL。
const updateConfirmRoute = "/update/apply/confirm"

// Update は GET /update。
func (h *Handler) Update(c *gin.Context) {
	ctx := c.Request.Context()
	v := view.Update{Chrome: h.chrome(ctx, view.NavUpdate), Panel: h.updatePanelView(h.d.Updates.Status(ctx))}
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.UpdateBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.Update(v))
}

// CheckUpdate は POST /update/check（「今すぐ確認」）。GitHub への確認と、新版があれば署名の検証まで済ませて、パネルを返す。
// 通信の失敗はパネルのメッセージに出す（200。確認の失敗はアプリの異常ではない）。
func (h *Handler) CheckUpdate(c *gin.Context) {
	ctx := c.Request.Context()
	st := h.d.Updates.Check(ctx, domain.ActorHost)
	toast := infoToast("新しい版はありません。")
	switch {
	case !st.Updatable:
		toast = infoToast("この版（開発用のビルド）は更新の対象外です。")
	case st.LastError != "":
		toast = view.Toast{Level: view.ToastError, Message: "更新の確認に失敗しました。", ErrorCode: st.LastError}
	case st.Latest != nil && st.Latest.Verified:
		toast = infoToast("新しい版 " + st.Latest.Version + " があります。")
	case st.Latest != nil:
		toast = view.Toast{Level: view.ToastError, Message: "新しい版 " + st.Latest.Version + " は、署名を確認できないため適用できません。"}
	}
	h.render(c, http.StatusOK, organisms.UpdatePanel(h.updatePanelView(st)), organisms.ToastOOB(toast))
}

// ConfirmApplyUpdate は GET /update/apply/confirm。適用できる新版があるときだけ描画する。
func (h *Handler) ConfirmApplyUpdate(c *gin.Context) {
	st := h.d.Updates.Status(c.Request.Context())
	if st.Latest == nil || !st.Latest.Verified {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "適用できる更新がありません。")
		return
	}
	h.confirm(c, view.ConfirmModal{
		Title:      "更新を適用しますか？",
		Message:    "版 " + st.Latest.Version + " のインストーラを起動し、アプリを終了して更新します。更新中はリモートからの操作を受け付けません。",
		ConfirmURL: "/update/apply", Method: "post", Target: "#update-panel", Swap: "outerHTML", Label: "更新する",
	})
}

// ApplyUpdate は POST /update/apply。署名検証済みの新版を適用する（インストーラを起動し、アプリを終了する）。
// リース保持中・承認待ち・接続中・タスク実行中、適用できる更新がない、公開鍵が未設定、署名の再検証に失敗したときは 409 conflict で理由を示す。
func (h *Handler) ApplyUpdate(c *gin.Context) {
	ctx := c.Request.Context()
	err := h.d.Updates.Apply(ctx, domain.ActorHost)
	if err != nil {
		h.failUpdate(c, err)
		return
	}
	h.render(c, http.StatusOK,
		organisms.UpdatePanel(h.updatePanelView(h.d.Updates.Status(ctx))),
		organisms.ModalClear(),
		organisms.ToastOOB(infoToast("更新を開始しました。アプリを終了して更新します。")),
	)
}

// failUpdate は適用の失敗を、理由つきのトーストにする。内部のエラー文（パス・OS のメッセージ）は出さない。
func (h *Handler) failUpdate(c *gin.Context, err error) {
	conflict := string(domain.CodeConflict)
	var busy *service.UpdateBusyError
	switch {
	case errors.As(err, &busy):
		var reasons []string
		for _, r := range busy.Reasons {
			reasons = append(reasons, service.UpdateBusyReasonText(r))
		}
		h.failMessage(c, http.StatusConflict, conflict, "いまは更新を適用できません。"+strings.Join(reasons, ""))
	case errors.Is(err, service.ErrUpdateKeyNotConfigured):
		h.failMessage(c, http.StatusConflict, conflict, updateRejectText(service.UpdateRejectKeyUnset))
	case errors.Is(err, service.ErrUpdateUnavailable):
		if reason, ok := service.UpdateRejectionReason(err); ok {
			h.failMessage(c, http.StatusConflict, conflict, "適用の直前の検証に失敗したため、更新しませんでした。"+updateRejectText(reason))
			return
		}
		h.failMessage(c, http.StatusConflict, conflict, "適用できる更新がありません。")
	default:
		h.log.Error("gui apply update failed", "error", err)
		h.failMessage(c, http.StatusInternalServerError, string(domain.CodeInternal), "インストーラを起動できませんでした。もう一度お試しください。")
	}
}

func (h *Handler) updatePanelView(st service.UpdateStatus) view.UpdatePanel {
	v := view.UpdatePanel{
		CurrentVersion: st.CurrentVersion, ChannelLabel: updateChannelLabel(st.Channel), ApplyModeLabel: updateApplyModeLabel(st.ApplyMode),
		Updatable: st.Updatable, KeyNotConfigured: !st.KeyConfigured, Applying: st.Applying,
		Error: updateErrorText(st.LastError), ConfirmRoute: updateConfirmRoute,
	}
	if !st.LastCheckedAt.IsZero() {
		v.LastChecked = h.formatTime(st.LastCheckedAt)
	}
	if l := st.Latest; l != nil {
		lv := &view.UpdateLatest{Version: l.Version, Notes: l.Notes, Verified: l.Verified}
		if !l.PublishedAt.IsZero() {
			lv.PublishedAt = h.formatTime(l.PublishedAt)
		}
		if !l.Verified {
			lv.Reject = updateRejectText(l.RejectReason)
		}
		v.Latest = lv
		v.CanApply = l.Verified && st.KeyConfigured
	}
	return v
}

func updateChannelLabel(ch domain.Channel) string {
	if ch == domain.ChannelNightly {
		return "ナイトリー"
	}
	return "正式版"
}

func updateApplyModeLabel(mode string) string {
	if mode == service.UpdateApplyAutoIdle {
		return "アイドル時に自動で適用する"
	}
	return "ホストが確認してから適用する"
}

// updateErrorText は最後の確認の失敗の分類（service.UpdateErr*）の、利用者向けの文。
func updateErrorText(code string) string {
	switch code {
	case "":
		return ""
	case service.UpdateErrNotFound, service.UpdateErrUnauthorize, service.UpdateErrForbidden:
		return "最新の版を取得できませんでした（リリースが公開されていないか、リポジトリが非公開の可能性があります）。"
	case service.UpdateErrNetwork:
		return "GitHub に接続できませんでした。ネットワークを確認してください。"
	case service.UpdateErrBadResponse, service.UpdateErrTooLarge, service.UpdateErrNoInstaller:
		return "GitHub の応答を読み取れませんでした。"
	}
	return "最新の版を確認できませんでした。"
}

// updateRejectText は新版を適用できない理由（service.UpdateReject*）の、利用者向けの文。
func updateRejectText(reason string) string {
	switch reason {
	case service.UpdateRejectKeyUnset:
		return "署名鍵が未設定のため、署名を検証できず適用できません。"
	case service.UpdateRejectSignatureMissing:
		return "署名ファイルがありません。適用しません。"
	case service.UpdateRejectSignatureInvalid:
		return "署名の検証に失敗しました（改ざん・別の鍵・壊れた署名）。適用しません。"
	case service.UpdateRejectVersionMismatch:
		return "署名が、この版・ファイルのものではありません。適用しません。"
	case service.UpdateRejectDownloadFailed:
		return "インストーラまたは署名を取得できませんでした。次の確認でやり直します。"
	}
	return "署名を確認できないため適用できません。"
}
