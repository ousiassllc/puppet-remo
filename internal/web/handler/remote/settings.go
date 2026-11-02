package remote

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

const (
	minQuality, maxQuality = 1, 100
	minFPS, maxFPS         = 1, 30
)

// maxFormBytes はフォームの本文の上限（1 MiB。API 仕様 §1 のサイズ上限、NFR-S09）。
const maxFormBytes = 1 << 20

// parseForm は状態変更のフォーム（application/x-www-form-urlencoded）を読む。本文が上限を超える、または読めないときは
// 400 invalid_argument を返して false。ほかの Content-Type の本文は読まれず、項目は空になる。
func (h *Handler) parseForm(c *gin.Context) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxFormBytes)
	if err := c.Request.ParseForm(); err != nil {
		h.failMessage(c, http.StatusBadRequest, string(domain.CodeInvalidArgument), "送信内容を読めないか、大きすぎます（1 MiB まで）。")
		return false
	}
	return true
}

// formValue は本文のフォームの項目（クエリは見ない）。
func formValue(c *gin.Context, name string) (string, bool) {
	v, ok := c.Request.PostForm[name]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// Settings は POST /viewer/settings（このビューアのモニター・画質・FPS。FR-W04・W05）。
// 範囲外・存在しないモニターは 422 で、入力値とメッセージ付きのツールバーを再描画する（API 仕様 §2 の検証エラー）。
// 成功は、反映後のツールバー（200）。サーバーが WebSocket の config で反映し、ブラウザへ届ける。
func (h *Handler) Settings(c *gin.Context) {
	client, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	if !h.parseForm(c) {
		return
	}
	ctx := c.Request.Context()
	mons, err := h.d.Control.Monitors(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	monitorIn, _ := formValue(c, "monitor")
	qualityIn, _ := formValue(c, "quality")
	fpsIn, _ := formValue(c, "fps")
	monitorIn, qualityIn, fpsIn = strings.TrimSpace(monitorIn), strings.TrimSpace(qualityIn), strings.TrimSpace(fpsIn)

	var errs view.ViewerToolbarErrors
	monitor, mErr := strconv.Atoi(monitorIn)
	if mErr != nil || !hasMonitor(mons, monitor) {
		errs.Monitor = "存在しないモニターです。"
		monitor = -1
	}
	quality, ok := parseRange(qualityIn, minQuality, maxQuality)
	if !ok {
		errs.Quality = "1〜100 の整数を指定してください。"
	}
	fps, ok := parseRange(fpsIn, minFPS, maxFPS)
	if !ok {
		errs.FPS = "1〜30 の整数を指定してください。"
	}
	tb := toolbarView(mons, monitor, qualityIn, fpsIn)
	if errs != (view.ViewerToolbarErrors{}) {
		tb.Errors = errs
		h.render(c, http.StatusUnprocessableEntity, organisms.ViewerToolbar(tb))
		return
	}
	if err := h.d.Sessions.Configure(who.SessionID, client, service.ViewerConfig{Monitor: monitor, Quality: quality, FPS: fps}); err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.ViewerToolbar(tb))
}

func hasMonitor(mons []domain.Monitor, index int) bool {
	for _, m := range mons {
		if m.Index == index {
			return true
		}
	}
	return false
}

// parseRange は s を min〜max の整数として読む。
func parseRange(s string, min, max int) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, false
	}
	return n, true
}
