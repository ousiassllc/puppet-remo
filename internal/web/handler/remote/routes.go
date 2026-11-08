package remote

import "github.com/gin-gonic/gin"

// Register は Web UI のルートを r に登録する（API 仕様 §4.2）。/ws/screen と /static/* は呼び出し側（router）が配信する。
// ホスト専用の操作（承認・トークン・キルスイッチなど）は、ここにも作らない。
func (h *Handler) Register(r gin.IRoutes) {
	r.GET("/", h.Root)
	r.GET("/viewer", h.Viewer)
	r.GET("/viewer/lease", h.LeasePanel)
	r.POST("/viewer/lease/acquire", h.AcquireLease)
	r.POST("/viewer/lease/release", h.ReleaseLease)
	r.POST("/viewer/lease/revoke", h.RevokeLease)
	r.GET("/viewer/windows", h.WindowPicker)
	r.POST("/viewer/target", h.SelectTarget)
	r.POST("/viewer/target/release", h.ReleaseTarget)
	r.POST("/viewer/settings", h.Settings)
	r.POST("/viewer/clipboard/send", h.SendClipboard)
	r.POST("/viewer/clipboard/fetch", h.FetchClipboard)
	r.POST("/viewer/keys/send", h.SendKeys)
}
