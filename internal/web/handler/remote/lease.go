package remote

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// leasePanel はリースパネル（#lease-panel）と、操作権の有無で変わるクリップボード・ショートカット（#viewer-ops。OOB）を返す。
func (h *Handler) leasePanel(c *gin.Context, who domain.Actor) {
	h.renderLeasePanel(c, http.StatusOK, h.leaseView(c.Request.Context(), who))
}

func (h *Handler) renderLeasePanel(c *gin.Context, status int, v view.ViewerLease) {
	h.render(c, status, organisms.ViewerLeasePanel(v), organisms.ViewerOpsOOB(opsView(v)))
}

// ConfirmRevokeLease は GET /viewer/lease/revoke/confirm（強制剥奪の確認モーダル。ホスト GUI と同じく中央のモーダルで確認する）。
// 剥奪できるとき（他者の保持中。リースパネルが「強制剥奪」を出す状態）だけ描画し、そうでなければ 409 conflict。
// 確定のボタンは POST /viewer/lease/revoke を送る（モーダルは #viewer-root の中にあるので X-Viewer-Id が付く）。
func (h *Handler) ConfirmRevokeLease(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	v := h.leaseView(c.Request.Context(), who)
	if !v.CanRevoke {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "剥奪できる操作権の保持者がいません。")
		return
	}
	message := "「" + v.HolderLabel + "」の操作権を強制的に外します。"
	if v.Task != nil {
		message += "実行中の AI エージェントのタスクは中断されます。"
	}
	h.render(c, http.StatusOK, molecules.Modal(view.ConfirmModal{
		Title: "操作権を強制剥奪しますか？", Message: message,
		ConfirmURL: "/viewer/lease/revoke", Method: "post", Target: "#lease-panel", Swap: "outerHTML", Label: "剥奪する", Destructive: true,
	}))
}

// LeasePanel は GET /viewer/lease（リースパネルのフラグメント。remo:lease-changed で再取得される）。
func (h *Handler) LeasePanel(c *gin.Context) {
	if _, who, ok := h.viewerContext(c); ok {
		h.leasePanel(c, who)
	}
}

// AcquireLease は POST /viewer/lease/acquire（操作権を取得。FR-W08）。他者が保持中・承認待ちなら 409 lease_held_by_other。
// 承認が要らなければ 200 のリースパネル。承認が要るクライアントは、ホストの結果を待たずに 202 の承認待ちのパネル
// （state pending_approval、要求者本人）をすぐ返す。結果は WebSocket で届く: 許可は lease（held）、拒否・タイムアウトは
// error（approval_denied / approval_timeout）。承認待ちのあいだの再試行（408 後の再送など）は、新しい承認を作らず同じ 202 を返す。
// 拒否・タイムアウト直後のクールダウン（30 秒）の取得は、承認を求めず 403 approval_denied。
func (h *Handler) AcquireLease(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	l, pending, err := h.d.Leases.RequestAcquire(c.Request.Context(), who)
	if err != nil {
		h.fail(c, err)
		return
	}
	if pending {
		h.renderLeasePanel(c, http.StatusAccepted, view.ViewerLease{
			Pending: true, PendingMine: true, HolderLabel: actorLabel(l.Holder), KindLabel: string(l.Holder.Kind),
		})
		return
	}
	h.leasePanel(c, who)
}

// ReleaseLease は POST /viewer/lease/release（操作権を解放）。保持していなければ何もしない。
func (h *Handler) ReleaseLease(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	h.d.Leases.Release(who)
	h.leasePanel(c, who)
}

// RevokeLease は POST /viewer/lease/revoke（他者の操作権を強制剥奪。FR-L06）。保持者は idle になり、このビューアが
// 取得するには改めて acquire する。自分が保持しているときは、剥奪ではなく解放を使うべきなので 409 conflict。
// 保持者がいなければ何もせず現在のパネルを返す（冪等）。確認モーダル（ConfirmRevokeLease）の確定のボタンが送るので、応答にモーダルを閉じる OOB を添える。
func (h *Handler) RevokeLease(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	if holder, held := h.d.Leases.Holder(); held && holder.Actor.SameSession(who) {
		h.fail(c, domain.NewError(domain.CodeConflict, "the viewer holds the lease; release it instead", nil))
		return
	}
	if err := h.d.Leases.Revoke(who); err != nil {
		h.fail(c, err)
		return
	}
	v := h.leaseView(c.Request.Context(), who)
	h.render(c, http.StatusOK, organisms.ViewerLeasePanel(v), organisms.ViewerOpsOOB(opsView(v)), organisms.ModalClear())
}
