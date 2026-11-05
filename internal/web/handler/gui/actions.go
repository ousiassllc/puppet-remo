package gui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルはアクションルート（常にフラグメントを返す）。
// 成功は 200 で、メインの更新に副作用の反映（ヘッダー・件数バッジ・モーダルを閉じる・トースト）を OOB で添える（§5.2）。
// 状態を変える操作はすべてホスト所有者の操作で、service が actor_type = host で監査に記録する。

func infoToast(msg string) view.Toast { return view.Toast{Message: msg} }

// checked はフォームのチェックボックス（チェック時だけ値が送られる）が入っているか。
func checked(c *gin.Context, name string) bool {
	_, ok := c.GetPostForm(name)
	return ok
}

// requestListResponse は一覧の更新に、ヘッダーの件数バッジとトーストを添える。
func (h *Handler) requestListResponse(c *gin.Context, toast string) {
	ctx := c.Request.Context()
	reqs, err := h.d.Admin.PendingRequests(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK,
		organisms.ConnectionRequestList(h.requestListView(reqs)),
		organisms.RequestsBadgeOOB(len(reqs)),
		organisms.ToastOOB(infoToast(toast)),
	)
}

// invalidApprovalModeMessage は定義外の承認モードを 422 にするときの文言。
const invalidApprovalModeMessage = "承認モードは「常時承認」「初回のみ承認」「自動許可」から選んでください。"

// ApproveRequest は POST /requests/:id/approve（クライアントとして登録し、要求を許可する）。
// 承認モード approval_mode（always / first_time / auto。空は always。定義外は 422）で clients.approval_mode を保存する（FR-H03）。
// 要求が pending でなければ 409。
func (h *Handler) ApproveRequest(c *gin.Context) {
	mode := domain.ApprovalMode(c.PostForm("approval_mode"))
	if mode != "" && !mode.Valid() {
		h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), invalidApprovalModeMessage)
		return
	}
	_, err := h.d.Admin.ApproveRequest(c.Request.Context(), c.Param("id"), service.ApproveInput{
		DisplayName:  c.PostForm("display_name"),
		WebEnabled:   checked(c, "web_enabled"),
		MCPEnabled:   checked(c, "mcp_enabled"),
		AgentEnabled: checked(c, "agent_enabled"),
		ApprovalMode: mode,
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	h.requestListResponse(c, "接続要求を許可し、クライアントに登録しました。")
}

// DenyRequest は POST /requests/:id/deny（24 時間は同デバイスの再要求を作らない）。
func (h *Handler) DenyRequest(c *gin.Context) {
	if err := h.d.Admin.DenyRequest(c.Request.Context(), c.Param("id")); err != nil {
		h.fail(c, err)
		return
	}
	h.requestListResponse(c, "接続要求を拒否しました。")
}

// UpdateClient は PATCH /clients/:id（web_enabled / mcp_enabled / agent_enabled / approval_mode のいずれか）。
// approval_mode（always / first_time / auto。定義外は 422）を変えると approved_at が NULL に戻る（FR-H04）。
// 応答はクライアントのカード。詳細ページからは ?view=detail が付き、削除・詳細への導線を出さないカードを返す。
func (h *Handler) UpdateClient(c *gin.Context) {
	var u service.ClientUpdate
	set := 0
	if raw, ok := c.GetPostForm("approval_mode"); ok {
		mode := domain.ApprovalMode(raw)
		if !mode.Valid() {
			h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), invalidApprovalModeMessage)
			return
		}
		u.ApprovalMode = &mode
		set++
	}
	for field, dst := range map[string]**bool{"web_enabled": &u.WebEnabled, "mcp_enabled": &u.MCPEnabled, "agent_enabled": &u.AgentEnabled} {
		raw, ok := c.GetPostForm(field)
		if !ok {
			continue
		}
		b, err := strconv.ParseBool(raw)
		if err != nil {
			h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), "値は true / false で指定してください。")
			return
		}
		*dst = &b
		set++
	}
	if set == 0 {
		h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), "更新する項目がありません。")
		return
	}
	updated, err := h.d.Admin.UpdateClient(c.Request.Context(), c.Param("id"), u)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.ClientCard(h.clientView(updated, c.Query("view") != "detail")))
}

// ResetApproval は POST /clients/:id/approval/reset（first_time の承認済みを取り消す。FR-H04、FR-S05）。応答はクライアントのカード。
// first_time で承認済みでなければ 409。破壊的ではないので確認モーダルは挟まない。監査のイベント名は docs に無く、記録しない
// （docs/components/overview.md §13.5）。
func (h *Handler) ResetApproval(c *gin.Context) {
	updated, err := h.d.Admin.ResetApproval(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.ClientCard(h.clientView(updated, c.Query("view") != "detail")), organisms.ToastOOB(infoToast("承認を取り消しました。")))
}

// DeleteClient は DELETE /clients/:id。応答は一覧のフラグメント。
func (h *Handler) DeleteClient(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.d.Admin.DeleteClient(ctx, c.Param("id")); err != nil {
		h.fail(c, err)
		return
	}
	cs, err := h.d.Admin.Clients(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK,
		organisms.ClientList(h.clientListView(cs)),
		organisms.ModalClear(),
		organisms.ToastOOB(infoToast("クライアントを削除しました。")),
	)
}

// parseExpiry は expires_in_days（日数、または never）をトークンの有効期間にする。空は既定（nil。service が 90 日にする）。
func parseExpiry(raw string) (*time.Duration, bool) {
	switch raw = strings.TrimSpace(raw); raw {
	case "":
		return nil, true
	case "never":
		var forever time.Duration
		return &forever, true
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 3650 {
		return nil, false
	}
	d := time.Duration(days) * 24 * time.Hour
	return &d, true
}

// IssueToken は POST /clients/:id/tokens。平文は、この応答に一度だけ出す（Cache-Control: no-store）。
// ラベルの不備は 422、重複は 409（どちらもフォームのエラー表示先へ）。応答にはトークン一覧の更新を OOB で添える。
func (h *Handler) IssueToken(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	label := strings.TrimSpace(c.PostForm("label"))
	if label == "" {
		h.tokenFormError(c, http.StatusUnprocessableEntity, "invalid_argument", "ラベルを入力してください。")
		return
	}
	ttl, ok := parseExpiry(c.PostForm("expires_in_days"))
	if !ok {
		h.tokenFormError(c, http.StatusUnprocessableEntity, "invalid_argument", "有効期限の指定が正しくありません。")
		return
	}
	plaintext, err := h.d.Admin.IssueToken(ctx, id, label, ttl)
	switch {
	case err == nil:
	case domain.CodeOf(err) == domain.CodeConflict:
		h.tokenFormError(c, http.StatusConflict, string(domain.CodeConflict), "同じラベルのトークンがすでにあります。別のラベルを指定してください。")
		return
	case errors.Is(err, domain.ErrInvalidArgument):
		h.tokenFormError(c, http.StatusUnprocessableEntity, "invalid_argument", "入力内容を確認してください。")
		return
	default:
		h.fail(c, err)
		return
	}
	tokens, err := h.d.Admin.Tokens(ctx, id)
	if err != nil {
		// 発行は済んでいる。平文を失わないよう、一覧の更新だけを諦めて平文を返す。
		h.log.Warn("list tokens after issue failed", "client_id", id, "error", err)
	}
	expires := "無期限"
	if ttl == nil {
		expires = h.formatTime(h.d.Clock.Now().Add(service.DefaultTokenTTL))
	} else if *ttl > 0 {
		expires = h.formatTime(h.d.Clock.Now().Add(*ttl))
	}
	comps := []templ.Component{
		molecules.TokenReveal(view.TokenReveal{Label: label, Plaintext: plaintext, ExpiresAt: expires}),
		organisms.TokenForm(tokenFormView(id, "", ""), true), // 古いエラーと入力したラベルを残さない
	}
	if err == nil {
		comps = append(comps, organisms.TokenList(h.tokenListView(id, tokens), true))
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	h.render(c, http.StatusOK, comps...)
}

func (h *Handler) tokenFormError(c *gin.Context, status int, code, msg string) {
	h.render(c, status, organisms.TokenFieldError(msg, code))
}

// RevokeToken は DELETE /clients/:id/tokens/:token_id。応答はトークン一覧のフラグメント。
func (h *Handler) RevokeToken(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	if err := h.d.Admin.RevokeToken(ctx, id, c.Param("token_id")); err != nil {
		h.fail(c, err)
		return
	}
	tokens, err := h.d.Admin.Tokens(ctx, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK,
		organisms.TokenList(h.tokenListView(id, tokens), false),
		organisms.ModalClear(),
		organisms.ToastOOB(infoToast("トークンを失効しました。")),
	)
}

// confirm は確認モーダルを返す。対象が無ければ 404。
func (h *Handler) confirm(c *gin.Context, m view.ConfirmModal) {
	h.render(c, http.StatusOK, molecules.Modal(m))
}

// ConfirmDeleteClient は GET /clients/:id/delete/confirm。
func (h *Handler) ConfirmDeleteClient(c *gin.Context) {
	client, err := h.d.Admin.Client(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.confirm(c, view.ConfirmModal{
		Title:      "クライアントを削除しますか？",
		Message:    "「" + client.DisplayName + "」を許可リストから外します。発行済みのトークンは削除され、接続中のセッションは切断されます。",
		ConfirmURL: "/clients/" + client.ID, Method: "delete", Target: "#client-list", Swap: "outerHTML", Label: "削除する", Destructive: true,
	})
}

// ConfirmRevokeToken は GET /clients/:id/tokens/:token_id/revoke/confirm。
func (h *Handler) ConfirmRevokeToken(c *gin.Context) {
	ctx := c.Request.Context()
	id, tokenID := c.Param("id"), c.Param("token_id")
	tokens, err := h.d.Admin.Tokens(ctx, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	for _, t := range tokens {
		if t.ID != tokenID {
			continue
		}
		h.confirm(c, view.ConfirmModal{
			Title:      "トークンを失効しますか？",
			Message:    "「" + t.Label + "」を失効します。このトークンで接続中の MCP セッションは切断され、元に戻せません。",
			ConfirmURL: "/clients/" + id + "/tokens/" + tokenID, Method: "delete", Target: "#token-list", Swap: "outerHTML", Label: "失効する", Destructive: true,
		})
		return
	}
	h.fail(c, service.ErrNotFound)
}

// ConfirmReleaseKillSwitch は GET /killswitch/release/confirm。解除できる状態（発動中）のときだけ描画する。
func (h *Handler) ConfirmReleaseKillSwitch(c *gin.Context) {
	if !h.d.KillSwitch.Active() {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "キルスイッチは発動していません。")
		return
	}
	h.confirm(c, view.ConfirmModal{
		Title:      "キルスイッチを解除しますか？",
		Message:    "リモートからの要求の受付を再開します。",
		ConfirmURL: "/killswitch/release", Method: "post", Target: "#killswitch-panel", Swap: "outerHTML", Label: "解除する",
	})
}

// killSwitchResponse はキルスイッチのパネルに、ヘッダーのバッジ・モーダルを閉じる・通知（トーストかアラート）を添える。
func (h *Handler) killSwitchResponse(c *gin.Context, notice templ.Component) {
	h.render(c, http.StatusOK,
		organisms.KillSwitchPanel(h.killSwitchView()),
		organisms.HeaderKillSwitchOOB(h.d.KillSwitch.Active()),
		organisms.ModalClear(),
		notice,
	)
}

// TriggerKillSwitch は POST /killswitch/trigger（FR-H06。1 秒以内）。
// 後始末の一部に失敗しても発動状態にはなるため、その場合は 200 でパネルを返し、エラーのアラートを添える。
func (h *Handler) TriggerKillSwitch(c *gin.Context) {
	err := h.d.KillSwitch.Trigger(killSwitchSource)
	switch {
	case err == nil:
		h.killSwitchResponse(c, organisms.ToastOOB(infoToast("キルスイッチを発動しました。")))
	case h.d.KillSwitch.Active():
		h.log.Error("kill switch triggered with errors", "error", err)
		h.killSwitchResponse(c, organisms.AlertOOB(errorAlert(string(domain.CodeInternal), "キルスイッチを発動しましたが、一部の後始末に失敗しました。")))
	default:
		h.fail(c, err)
	}
}

// ReleaseKillSwitch は POST /killswitch/release。永続化に失敗したら発動状態のまま 500。
func (h *Handler) ReleaseKillSwitch(c *gin.Context) {
	if err := h.d.KillSwitch.Release(); err != nil {
		h.fail(c, err)
		return
	}
	h.killSwitchResponse(c, organisms.ToastOOB(infoToast("キルスイッチを解除しました。")))
}

// SaveSettings は PUT /settings。検証エラーは 422 で、入力値と項目別のメッセージ付きでフォームを再描画する。
func (h *Handler) SaveSettings(c *gin.Context) {
	ctx := c.Request.Context()
	current, err := h.d.Settings.Load(ctx)
	if err != nil {
		// 保存済みの値が壊れていても、入力で上書きして復旧できるようにする。
		h.log.Warn("load settings failed; saving over", "error", err)
		current = service.HostSettings{}
	}
	in := view.SettingsValues{
		Port:             strings.TrimSpace(c.PostForm(service.FieldPort)),
		Hotkey:           strings.TrimSpace(c.PostForm(service.FieldKillSwitchHotkey)),
		ScreenshotMaxDim: strings.TrimSpace(c.PostForm(service.FieldScreenshotMaxDimension)),
		LeaseIdleSec:     strings.TrimSpace(c.PostForm(service.FieldLeaseIdleTimeoutSec)),
		ViewerQuality:    strings.TrimSpace(c.PostForm(service.FieldViewerDefaultQuality)),
		ViewerFPS:        strings.TrimSpace(c.PostForm(service.FieldViewerDefaultFPS)),
		// チェックボックスは、外れていると値が送られない。
		AuditThumbnails:     checked(c, service.FieldAuditThumbnails),
		AuditRecordText:     checked(c, service.FieldAuditRecordText),
		AuditRetentionDays:  strings.TrimSpace(c.PostForm(service.FieldAuditRetentionDays)),
		AuditThumbsMaxBytes: strings.TrimSpace(c.PostForm(service.FieldAuditThumbsMaxBytes)),
		UpdateApplyMode:     strings.TrimSpace(c.PostForm(service.FieldUpdateApplyMode)),
		UpdateChannel:       strings.TrimSpace(c.PostForm(service.FieldUpdateChannel)),
		Autostart:           current.Autostart,
	}
	if h.d.Settings.AutostartAvailable() {
		in.Autostart = checked(c, "autostart")
	}

	var errs view.SettingsErrors
	s := service.HostSettings{
		KillSwitchHotkey: in.Hotkey, Autostart: in.Autostart,
		AuditThumbnailsEnabled: in.AuditThumbnails, AuditRecordText: in.AuditRecordText, UpdateApplyMode: in.UpdateApplyMode, UpdateChannel: in.UpdateChannel,
	}
	s.Port, errs.Port = parseInt(in.Port)
	s.ScreenshotMaxDimension, errs.ScreenshotMaxDim = parseInt(in.ScreenshotMaxDim)
	s.LeaseIdleTimeoutSec, errs.LeaseIdleSec = parseInt(in.LeaseIdleSec)
	s.ViewerDefaultQuality, errs.ViewerQuality = parseInt(in.ViewerQuality)
	s.ViewerDefaultFPS, errs.ViewerFPS = parseInt(in.ViewerFPS)
	s.AuditRetentionDays, errs.AuditRetentionDays = parseInt(in.AuditRetentionDays)
	s.AuditThumbsMaxBytes, errs.AuditThumbsMaxBytes = parseInt64(in.AuditThumbsMaxBytes)

	// 数値に読めなかった項目のほかにも、範囲・書式の不備を同時に示す（読めなかった項目は 0 として検証し、メッセージは上書きしない）。
	var verr *service.ValidationError
	if err := s.Validate(); errors.As(err, &verr) {
		errs = mergeFieldErrors(errs, verr)
	}
	if errs == (view.SettingsErrors{}) {
		saved, err := h.d.Settings.Save(ctx, s)
		switch {
		case err == nil:
			h.render(c, http.StatusOK,
				organisms.SettingsForm(h.settingsFormView(settingsValues(saved), view.SettingsErrors{})),
				organisms.ToastOOB(infoToast("設定を保存しました。")),
			)
			return
		case errors.As(err, &verr):
			errs = mergeFieldErrors(errs, verr)
		default:
			h.fail(c, err)
			return
		}
	}
	h.render(c, http.StatusUnprocessableEntity, organisms.SettingsForm(h.settingsFormView(in, errs)))
}

// mergeFieldErrors は verr の項目別のメッセージを、まだメッセージの無い項目に足す。
func mergeFieldErrors(errs view.SettingsErrors, verr *service.ValidationError) view.SettingsErrors {
	fill := func(dst *string, field string) {
		if *dst == "" {
			*dst = verr.Fields[field]
		}
	}
	fill(&errs.Port, service.FieldPort)
	fill(&errs.Hotkey, service.FieldKillSwitchHotkey)
	fill(&errs.ScreenshotMaxDim, service.FieldScreenshotMaxDimension)
	fill(&errs.LeaseIdleSec, service.FieldLeaseIdleTimeoutSec)
	fill(&errs.ViewerQuality, service.FieldViewerDefaultQuality)
	fill(&errs.ViewerFPS, service.FieldViewerDefaultFPS)
	fill(&errs.AuditRetentionDays, service.FieldAuditRetentionDays)
	fill(&errs.AuditThumbsMaxBytes, service.FieldAuditThumbsMaxBytes)
	fill(&errs.UpdateApplyMode, service.FieldUpdateApplyMode)
	fill(&errs.UpdateChannel, service.FieldUpdateChannel)
	return errs
}

// parseInt は整数のフォーム値を読む。読めなければ 0 と利用者向けのメッセージ。
func parseInt(raw string) (int, string) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, "整数を指定してください。"
	}
	return n, ""
}

// parseInt64 は parseInt の 64 ビット版（バイト数など、32 ビットに収まらない値）。
func parseInt64(raw string) (int64, string) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, "整数を指定してください。"
	}
	return n, ""
}
