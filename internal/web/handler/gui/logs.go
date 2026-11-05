package gui

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
)

// logOpenFolderURL は POST /logs/open-folder（ログフォルダーを開く。#54）。
const logOpenFolderURL = "/logs/open-folder"

// 失敗のアラートの固定文。OS のエラー文・パスは出さない。
const (
	msgLogFolderMissing = "ログフォルダーが見つかりません。"
	msgLogFolderFailed  = "ログフォルダーを開けませんでした。"
)

// OpenLogFolder は POST /logs/open-folder（FR-H13。#54）。ログ画面のボタンが送る。
// 本文・クエリ・パスは読まない: 開く場所は service.AppLog の実装が固定し（ログファイルのあるフォルダー）、
// リクエストの値で決まるものは無い。ログの内容には触れず（読み取り専用）、監査もしない（設定・状態を変えない）。
// 成功は 200 で、トーストだけを OOB で返す（ボタンの hx-swap="none"）。フォルダーが無ければ 404、その他の失敗は 500
// （どちらもエラーのインラインアラート。固定の文言で、パス・OS のエラー文は出さない）。
func (h *Handler) OpenLogFolder(c *gin.Context) {
	err := h.d.Logs.OpenFolder(c.Request.Context())
	switch {
	case err == nil:
		h.render(c, http.StatusOK, organisms.ToastOOB(infoToast("ログフォルダーを開きました。")))
	case errors.Is(err, service.ErrNotFound):
		h.failMessage(c, http.StatusNotFound, codeNotFound, msgLogFolderMissing)
	default:
		h.log.Error("open log folder failed", "error", err)
		h.failMessage(c, http.StatusInternalServerError, string(domain.CodeInternal), msgLogFolderFailed)
	}
}
