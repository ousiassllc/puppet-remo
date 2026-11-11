package gui

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルはページルート（GET。常に HTML）。HX-Request の有無でフルページとフラグメントを出し分ける
// （docs/architecture/frontend.md §5.1）。サイドバーの hx-boost の遷移（HX-Boosted）は #main の中身を返す。

// Dashboard は GET /。
func (h *Handler) Dashboard(c *gin.Context) {
	v := h.dashboardView(c.Request.Context())
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.DashboardBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.Dashboard(v))
}

// Requests は GET /requests。HTMX の部分更新は一覧のフラグメント。
func (h *Handler) Requests(c *gin.Context) {
	ctx := c.Request.Context()
	reqs, err := h.d.Admin.PendingRequests(ctx)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	list := h.requestListView(reqs)
	switch {
	case boostedRequest(c):
		h.render(c, http.StatusOK, pages.RequestsBoosted(view.Requests{Chrome: h.chrome(ctx, view.NavRequests), List: list}))
	case fragmentRequest(c):
		h.render(c, http.StatusOK, organisms.ConnectionRequestList(list))
	default:
		h.render(c, http.StatusOK, pages.Requests(view.Requests{Chrome: h.chrome(ctx, view.NavRequests), List: list}))
	}
}

// RequestsHistory は GET /requests/history?page=N（接続要求の履歴。1 始まりのページ番号。不正な値は 1 として扱う）。
// HTMX の部分更新（ページ送り）は履歴のフラグメント。
func (h *Handler) RequestsHistory(c *gin.Context) {
	ctx := c.Request.Context()
	page, err := h.d.Admin.RequestHistory(ctx, pageParam(c))
	if err != nil {
		h.pageFail(c, err)
		return
	}
	history := h.requestHistoryView(page)
	switch {
	case boostedRequest(c):
		h.render(c, http.StatusOK, pages.RequestsHistoryBoosted(view.RequestsHistory{Chrome: h.chrome(ctx, view.NavRequests), History: history}))
	case fragmentRequest(c):
		h.render(c, http.StatusOK, organisms.ConnectionRequestHistory(history))
	default:
		h.render(c, http.StatusOK, pages.RequestsHistory(view.RequestsHistory{Chrome: h.chrome(ctx, view.NavRequests), History: history}))
	}
}

// Clients は GET /clients。
func (h *Handler) Clients(c *gin.Context) {
	ctx := c.Request.Context()
	cs, err := h.d.Admin.Clients(ctx)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	list := h.clientListView(cs)
	switch {
	case boostedRequest(c):
		h.render(c, http.StatusOK, pages.ClientsBoosted(view.Clients{Chrome: h.chrome(ctx, view.NavClients), List: list}))
	case fragmentRequest(c):
		h.render(c, http.StatusOK, organisms.ClientList(list))
	default:
		h.render(c, http.StatusOK, pages.Clients(view.Clients{Chrome: h.chrome(ctx, view.NavClients), List: list}))
	}
}

// ClientDetail は GET /clients/:id（有効化・トークン）。
func (h *Handler) ClientDetail(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	client, err := h.d.Admin.Client(ctx, id)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	tokens, err := h.d.Admin.Tokens(ctx, id)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	v := view.ClientDetail{
		Chrome:  h.chrome(ctx, view.NavClients),
		Client:  h.clientView(client, false),
		Tokens:  h.tokenListView(id, tokens),
		Form:    tokenFormView(id, "", ""),
		BackURL: "/clients",
	}
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.ClientDetailBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.ClientDetail(v))
}

// Settings は GET /settings。
func (h *Handler) Settings(c *gin.Context) {
	ctx := c.Request.Context()
	s, err := h.d.Settings.Load(ctx)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	v := view.Settings{Chrome: h.chrome(ctx, view.NavSettings), Form: h.settingsFormView(settingsValues(s), view.SettingsErrors{})}
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.SettingsBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.Settings(v))
}

// KillSwitchPanel は GET /killswitch（キルスイッチのパネルのフラグメント。常にフラグメント）。
func (h *Handler) KillSwitchPanel(c *gin.Context) {
	h.render(c, http.StatusOK, organisms.KillSwitchPanel(h.killSwitchView()))
}

// NotFound は未登録のパスへの 404（ページ）。HTMX のリクエストにはエラーのアラートのフラグメント。
func (h *Handler) NotFound(c *gin.Context) {
	h.pageFail(c, errNoRoute)
}

// pageFail はページルートの失敗をエラーページ（HTMX の部分更新ではフラグメント）として返す。
func (h *Handler) pageFail(c *gin.Context, err error) {
	if fragmentRequest(c) && !boostedRequest(c) {
		h.fail(c, err)
		return
	}
	f := classify(err)
	if f.status >= 500 {
		h.log.Error("gui page failed", "path", c.Request.URL.Path, "error", err)
	}
	title := "エラー"
	if f.status == http.StatusNotFound {
		title = "ページが見つかりません"
	}
	v := view.ErrorPage{Chrome: h.chrome(c.Request.Context(), ""), Code: f.code, Title: title, Message: f.message}
	if boostedRequest(c) {
		h.render(c, f.status, pages.ErrorBoosted(v))
		return
	}
	h.render(c, f.status, pages.Error(v))
}

// Logs は GET /logs?level=all|warn|error&page=N（アプリのログ。FR-H13）。level は無い・未知の値なら all、page は 1 始まりで
// 不正な値は 1。HTMX の部分更新（絞り込み・ページ送り）はログのフラグメント。
func (h *Handler) Logs(c *gin.Context) {
	ctx := c.Request.Context()
	filter := service.ParseAppLogFilter(c.Query("level"))
	page, err := h.d.Logs.Entries(ctx, filter, pageParam(c))
	if err != nil {
		h.pageFail(c, err)
		return
	}
	log := h.appLogView(filter, page)
	switch {
	case boostedRequest(c):
		h.render(c, http.StatusOK, pages.LogsBoosted(view.AppLogPage{Chrome: h.chrome(ctx, view.NavLogs), Path: h.d.Logs.Path(), OpenFolderURL: logOpenFolderURL, Log: log}))
	case fragmentRequest(c):
		h.render(c, http.StatusOK, organisms.AppLog(log))
	default:
		h.render(c, http.StatusOK, pages.Logs(view.AppLogPage{Chrome: h.chrome(ctx, view.NavLogs), Path: h.d.Logs.Path(), OpenFolderURL: logOpenFolderURL, Log: log}))
	}
}
