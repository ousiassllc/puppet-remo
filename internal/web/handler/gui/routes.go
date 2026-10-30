package gui

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

var errNoRoute = fmt.Errorf("no such route: %w", service.ErrNotFound)

// Register は GUI のルートを r に登録する（API 仕様 §7）。静的ファイル（/static）は呼び出し側（router）が配信する。
// /wails/* は Wails のランタイム用に予約されているため、登録しない。
func (h *Handler) Register(r gin.IRoutes) {
	// ページ（M1）
	r.GET("/", h.Dashboard)
	r.GET("/requests", h.Requests)
	r.GET("/clients", h.Clients)
	r.GET("/clients/:id", h.ClientDetail)
	r.GET("/settings", h.Settings)
	r.GET("/events", h.Events)
	r.GET("/poll", h.Poll)
	r.GET("/killswitch", h.KillSwitchPanel)

	// 接続要求・クライアント・トークン
	r.POST("/requests/:id/approve", h.ApproveRequest)
	r.POST("/requests/:id/deny", h.DenyRequest)
	r.PATCH("/clients/:id", h.UpdateClient)
	r.DELETE("/clients/:id", h.DeleteClient)
	r.POST("/clients/:id/tokens", h.IssueToken)
	r.DELETE("/clients/:id/tokens/:token_id", h.RevokeToken)

	// 破壊的な操作の確認モーダル（サーバーが描画し、確定のボタンだけが実際の操作を送る。§5.4）
	r.GET("/clients/:id/delete/confirm", h.ConfirmDeleteClient)
	r.GET("/clients/:id/tokens/:token_id/revoke/confirm", h.ConfirmRevokeToken)
	r.GET("/killswitch/release/confirm", h.ConfirmReleaseKillSwitch)

	// キルスイッチ・設定
	r.POST("/killswitch/trigger", h.TriggerKillSwitch)
	r.POST("/killswitch/release", h.ReleaseKillSwitch)
	r.PUT("/settings", h.SaveSettings)
}
