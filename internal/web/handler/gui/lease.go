package gui

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
)

// RevokeLease は POST /lease/revoke（ホスト GUI からの強制剥奪。FR-L06、FR-H02）。
// 操作主体は host（service.HostActor）で、監査（lease.revoke）は LeaseManager が記録する。
// 応答は #lease の中身（保持者は空きになる）。保持者がいなければ何もせず 200（冪等。トーストも出さない）。
// 剥奪された側の以後の操作は lease_revoked を返す。GUI はキルスイッチ中でも使える（保持者はいないので何も起きない）。
func (h *Handler) RevokeLease(c *gin.Context) {
	_, held := h.d.Leases.Holder()
	if err := h.d.Leases.Revoke(service.HostActor); err != nil {
		h.fail(c, err)
		return
	}
	if !held {
		h.render(c, http.StatusOK, organisms.LeasePanelBody(h.leaseView()))
		return
	}
	h.render(c, http.StatusOK,
		organisms.LeasePanelBody(h.leaseView()),
		organisms.ToastOOB(infoToast("操作権を剥奪しました。")),
	)
}
