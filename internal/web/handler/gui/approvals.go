package gui

import (
	"errors"
	"math"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは承認ダイアログ（FR-H05）。ウィンドウを開くのは app（Bus の approval）で、ここは中身の描画（GET /approvals/{id}）と、
// 許可・拒否（POST /approvals/{id}/allow・deny）の受け付けだけ。M3 の kind は lease だけ（safety_check・judge_action は M4）。

// closeWindowTrigger は許可・拒否のあとにウィンドウを閉じる HX-Trigger のイベント名（app.js が受ける）。
const closeWindowTrigger = "closeWindow"

// approvalGoneMessage は、承認要求がすでに終わっているときの文言（API 仕様 §7.3: 410 approval_timeout）。
const approvalGoneMessage = "この承認要求はすでに終了しています（60 秒の時間切れ、または処理済みです）。"

// pendingApproval は id の承認待ちの要求（lease）を返す。終わっている・存在しない・lease 以外（M3 では扱わない）なら false。
func (h *Handler) pendingApproval(id string) (service.ApprovalRequest, bool) {
	for _, r := range h.d.Approval.Pending() {
		if r.ID == id && r.Kind == service.ApprovalKindLease {
			return r, true
		}
	}
	return service.ApprovalRequest{}, false
}

// approvalRemaining は ExpiresAt までの残り秒数（切り上げ。過ぎていれば 0）。
func (h *Handler) approvalRemaining(req service.ApprovalRequest) int {
	return max(0, int(math.Ceil(req.ExpiresAt.Sub(h.d.Clock.Now()).Seconds())))
}

func (h *Handler) approvalDialogView(req service.ApprovalRequest) view.ApprovalDialog {
	id := url.PathEscape(req.ID)
	base := "/approvals/" + id
	return view.ApprovalDialog{
		ID:          req.ID,
		ClientLabel: actorLabel(req.Who),
		KindLabel:   kindLabel(req.Who.Kind),
		Summary:     "操作権（リース）の取得",
		Remaining:   view.ApprovalRemaining{ID: req.ID, Seconds: h.approvalRemaining(req), RefreshURL: base},
		AllowURL:    base + "/allow",
		DenyURL:     base + "/deny",
	}
}

// ApprovalDialog は GET /approvals/:id（承認ダイアログのフルページ）。HTMX の部分更新（残り時間の再取得）は残り時間の要素だけ。
// 失効・解決済み・存在しない id・lease 以外の kind は 410 approval_timeout（フルページは終了した旨の表示）。
func (h *Handler) ApprovalDialog(c *gin.Context) {
	req, ok := h.pendingApproval(c.Param("id"))
	if !ok {
		h.approvalGone(c)
		return
	}
	v := h.approvalDialogView(req)
	c.Header("Cache-Control", "no-store")
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, organisms.ApprovalRemaining(v.Remaining))
		return
	}
	h.render(c, http.StatusOK, pages.ApprovalDialog(v))
}

// approvalGone は 410 approval_timeout を返す。フルページには終了した旨を出し、HTMX の応答（ボタン・残り時間の更新）は
// data-error-code 付きのトースト（HX-Retarget: #toast-root）にする。
func (h *Handler) approvalGone(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetHeader("HX-Request") != "" {
		h.failMessage(c, http.StatusGone, string(domain.CodeApprovalTimeout), approvalGoneMessage)
		return
	}
	h.render(c, http.StatusGone, pages.ApprovalGone(view.ApprovalGone{Code: string(domain.CodeApprovalTimeout), Message: approvalGoneMessage}))
}

// AllowApproval は POST /approvals/:id/allow（許可）。
func (h *Handler) AllowApproval(c *gin.Context) { h.resolveApproval(c, true) }

// DenyApproval は POST /approvals/:id/deny（拒否）。
func (h *Handler) DenyApproval(c *gin.Context) { h.resolveApproval(c, false) }

// resolveApproval は結果を Approval.Resolve へ渡す。成功は 200 で、HX-Trigger: closeWindow でウィンドウを閉じる。
// 失効（60 秒）・解決済み・存在しない id は 410 approval_timeout。lease 以外の kind は M3 では扱わず、同じく 410 にする。
func (h *Handler) resolveApproval(c *gin.Context, allow bool) {
	c.Header("Cache-Control", "no-store")
	if _, ok := h.pendingApproval(c.Param("id")); !ok {
		h.approvalGone(c)
		return
	}
	if err := h.d.Approval.Resolve(c.Param("id"), allow); err != nil {
		if errors.Is(err, domain.ErrApprovalTimeout) {
			h.approvalGone(c)
			return
		}
		h.fail(c, err)
		return
	}
	c.Header("HX-Trigger", closeWindowTrigger)
	h.render(c, http.StatusOK, organisms.ApprovalDone(view.ApprovalDone{Allowed: allow}))
}
