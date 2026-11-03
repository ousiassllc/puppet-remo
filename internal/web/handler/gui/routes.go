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
	r.GET("/requests/history", h.RequestsHistory)
	r.GET("/logs", h.Logs)
	// 監査ログの閲覧（M3。FR-AU05、FR-H08）。GUI ルーターだけにあり、リモートのルーターには同名の経路を作らない
	r.GET("/audit", h.Audit)
	r.GET("/audit/:id", h.AuditDetail)
	r.GET("/audit/:id/thumbnail", h.AuditThumbnail)
	// エージェントのタスク（M4。FR-H10）。GUI ルーターだけにある
	r.GET("/tasks", h.Tasks)
	r.GET("/tasks/:id", h.TaskDetail)
	r.GET("/clients", h.Clients)
	r.GET("/clients/:id", h.ClientDetail)
	r.GET("/settings", h.Settings)
	r.GET("/update", h.Update)
	r.GET("/apps", h.Apps)
	r.GET("/apps/new", h.AppNew)
	r.GET("/apps/:id/edit", h.AppEdit)
	r.GET("/agent", h.AgentSettings)
	r.GET("/events", h.Events)
	r.GET("/poll", h.Poll)
	r.GET("/killswitch", h.KillSwitchPanel)

	// 接続要求・クライアント・トークン
	r.POST("/requests/:id/approve", h.ApproveRequest)
	r.POST("/requests/:id/deny", h.DenyRequest)
	r.PATCH("/clients/:id", h.UpdateClient)
	r.POST("/clients/:id/approval/reset", h.ResetApproval)
	r.DELETE("/clients/:id", h.DeleteClient)
	r.POST("/clients/:id/tokens", h.IssueToken)
	r.DELETE("/clients/:id/tokens/:token_id", h.RevokeToken)

	// 破壊的な操作の確認モーダル（サーバーが描画し、確定のボタンだけが実際の操作を送る。§5.4）
	r.GET("/clients/:id/delete/confirm", h.ConfirmDeleteClient)
	r.GET("/clients/:id/tokens/:token_id/revoke/confirm", h.ConfirmRevokeToken)
	r.GET("/agent/api-key/delete/confirm", h.ConfirmDeleteOpenAIKey)
	r.GET("/agent/fastpath/api-key/delete/confirm", h.ConfirmDeleteTypeSafeKey)
	r.GET("/killswitch/release/confirm", h.ConfirmReleaseKillSwitch)
	r.GET("/apps/:id/delete/confirm", h.ConfirmDeleteApp)
	r.GET("/update/apply/confirm", h.ConfirmApplyUpdate)

	// 起動許可アプリ（M1.5。FR-H12）。許可リストを書き換える経路は、この GUI ルーターだけにある（NFR-S20）
	r.POST("/apps", h.CreateApp)
	r.PUT("/apps/:id", h.UpdateApp)
	r.DELETE("/apps/:id", h.DeleteApp)
	r.POST("/apps/pick-file", h.PickAppFile)

	// ログフォルダーを開く（#54。FR-H13）。パスは受け取らない（開く場所は service.AppLog の実装が固定する）
	r.POST(logOpenFolderURL, h.OpenLogFolder)

	// 承認ダイアログ（M3。FR-H05）。専用の Wails ウィンドウの中身と、許可・拒否
	r.GET("/approvals/:id", h.ApprovalDialog)
	r.POST("/approvals/:id/allow", h.AllowApproval)
	r.POST("/approvals/:id/deny", h.DenyApproval)

	// リース（M2。FR-L06）。ホスト所有者の強制剥奪
	r.POST("/lease/revoke", h.RevokeLease)

	// タスクの中断（M4。FR-H02・H10）。ホスト所有者の中断のボタン
	r.POST("/tasks/:id/cancel", h.CancelTask)

	// キルスイッチ・設定
	r.POST("/killswitch/trigger", h.TriggerKillSwitch)
	r.POST("/killswitch/release", h.ReleaseKillSwitch)
	r.PUT("/settings", h.SaveSettings)

	// 更新（M3。FR-H09）。署名検証済みの新版の確認と適用
	r.POST("/update/check", h.CheckUpdate)
	r.POST("/update/apply", h.ApplyUpdate)

	// エージェント設定（M4。FR-H11）。API キーは PUT の本文だけで受け、応答に出さない（NFR-S14）
	r.PUT("/agent", h.SaveAgent)
	r.PUT("/agent/api-key", h.SetOpenAIKey)
	r.DELETE("/agent/api-key", h.DeleteOpenAIKey)
	r.PUT("/agent/fastpath", h.SaveFastpath)
	r.PUT("/agent/fastpath/api-key", h.SetTypeSafeKey)
	r.DELETE("/agent/fastpath/api-key", h.DeleteTypeSafeKey)
}
