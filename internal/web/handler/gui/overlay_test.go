package gui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// 操作中オーバーレイ（#69）のページ: 枠と文言だけ。背景は透明で、サイドバー・ポーリング・スクリプトは無い。CSP は rig が全 HTML 応答で検査する。
func TestOverlayPage(t *testing.T) {
	r := newRig(t)
	res := r.get("/overlay?label=" + url.QueryEscape("操作中: claude-desktop（MCP）"))
	status(t, res, http.StatusOK)
	contains(t, res, "<html", `id="operation-overlay"`, "pointer-events-none", "bg-transparent", "操作中: claude-desktop（MCP）", `href="/static/css/app.css"`)
	lacks(t, res, `id="live-poll"`, `id="modal-root"`, "<script", "ダッシュボード", "キルスイッチ")
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// label は HTML エスケープされ、長すぎれば切り詰められる。label が無くても描画できる。
func TestOverlayEscapesAndLimitsTheLabel(t *testing.T) {
	r := newRig(t)

	res := r.get("/overlay?label=" + url.QueryEscape(`<script>alert(1)</script>`))
	status(t, res, http.StatusOK)
	contains(t, res, "&lt;script&gt;alert(1)&lt;/script&gt;")
	lacks(t, res, "<script>alert", "<script")

	long := strings.Repeat("あ", 500)
	res = r.get("/overlay?label=" + url.QueryEscape(long))
	status(t, res, http.StatusOK)
	contains(t, res, strings.Repeat("あ", 79)+"…")
	lacks(t, res, strings.Repeat("あ", 80))

	exact := strings.Repeat("a", 80)
	res = r.get("/overlay?label=" + exact)
	status(t, res, http.StatusOK)
	contains(t, res, exact)
	lacks(t, res, "…")

	status(t, r.get("/overlay"), http.StatusOK)
}
