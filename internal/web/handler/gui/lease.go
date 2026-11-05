package gui

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// RevokeLease は POST /lease/revoke（ホスト GUI からの強制剥奪。FR-L06、FR-H02）。
// 操作主体は host（service.HostActor）で、監査（lease.revoke）は LeaseManager が記録する。
// 確認モーダル（ConfirmRevokeLease）の確定のボタンが送る。応答は #lease の中身（保持者は空きになる）にモーダルを閉じる OOB を添える。
// 保持者がいなければ何もせず 200（冪等。トーストも出さない）。
// 剥奪された側の以後の操作は lease_revoked を返す。GUI はキルスイッチ中でも使える（保持者はいないので何も起きない）。
func (h *Handler) RevokeLease(c *gin.Context) {
	_, held := h.d.Leases.Holder()
	if err := h.d.Leases.Revoke(service.HostActor); err != nil {
		h.fail(c, err)
		return
	}
	if !held {
		h.render(c, http.StatusOK, organisms.LeasePanelBody(h.leaseView()), organisms.ModalClear())
		return
	}
	h.render(c, http.StatusOK,
		organisms.LeasePanelBody(h.leaseView()),
		organisms.ModalClear(),
		organisms.ToastOOB(infoToast("操作権を剥奪しました。")),
	)
}

// ConfirmRevokeLease は GET /lease/revoke/confirm。他者が保持しているとき（剥奪できるとき）だけ描画する。
// agent の剥奪はそのタスクを中断するので、その旨（RevokeHint）をモーダルの本文に添える。
func (h *Handler) ConfirmRevokeLease(c *gin.Context) {
	v := h.leaseView()
	if !v.CanRevoke {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "剥奪できる操作権の保持者がいません。")
		return
	}
	message := "「" + v.HolderLabel + "」の操作権を強制的に外します。"
	if v.RevokeHint != "" {
		message += v.RevokeHint
	}
	h.confirm(c, view.ConfirmModal{
		Title: "操作権を強制剥奪しますか？", Message: message,
		ConfirmURL: "/lease/revoke", Method: "post", Target: "#lease", Swap: "innerHTML", Label: "剥奪する", Destructive: true,
	})
}
