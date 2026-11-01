package remote

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
)

// leasePanel はリースパネル（#lease-panel）と、操作権の有無で変わるクリップボード・ショートカット（#viewer-ops。OOB）を返す。
func (h *Handler) leasePanel(c *gin.Context, who domain.Actor) {
	v := h.leaseView(who)
	h.render(c, http.StatusOK, organisms.ViewerLeasePanel(v), organisms.ViewerOpsOOB(opsView(v)))
}

// LeasePanel は GET /viewer/lease（リースパネルのフラグメント。remo:lease-changed で再取得される）。
func (h *Handler) LeasePanel(c *gin.Context) {
	if _, who, ok := h.viewerContext(c); ok {
		h.leasePanel(c, who)
	}
}

// AcquireLease は POST /viewer/lease/acquire（操作権を取得。FR-W08）。他者が保持中なら 409 lease_held_by_other。
// 承認待ち（pending_approval）は M3 で、M2 は承認なしで取得する。
func (h *Handler) AcquireLease(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	if _, err := h.d.Leases.Acquire(c.Request.Context(), who); err != nil {
		h.fail(c, err)
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
// 保持者がいなければ何もせず現在のパネルを返す（冪等）。
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
	h.leasePanel(c, who)
}
