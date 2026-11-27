package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは ChatGPT のプランの接続方式のルート（API §7.3。#247・FR-G25〜G30・FR-H11）。保存・検証・監査・認可は service
// （ChatGPTAuth・CodexAuth・AgentSettings）が行い、ここは描画と受け付けだけ。どのルートも、トークン・認可コード・開く URL を
// 受け取らず、応答・ログにも出さない（開く先は service が固定する。NFR-S13・S21）。エラーは既存の作法（data-error-code 付きのインラインアラート）で、
// 状態を変える操作が失敗したときは、画面の古いカードを直すため、現在のカードを OOB で添える。

const (
	msgPlanInternal      = "内部エラーが発生しました。"
	msgPlanAuditDown     = "監査ログに書き込めないため、操作は完了できませんでした。時間をおいてやり直してください。"
	msgConnectionInvalid = "接続方式を確認してください。"
	msgModelNotListed    = "選んだモデルはこのアカウントの一覧にありません。一覧を取得し直して、選び直してください。"
	msgModelInvalid      = "モデル名の形式が正しくありません。"
	msgModelsFailed      = "モデルの一覧を取得できませんでした。現在のモデルだけを表示しています。時間をおいてやり直してください。"
	msgUsageOpenFailed   = "ChatGPT の使用量の設定を開けませんでした。"
)

// asDomainError は err 連鎖の最初のドメインエラー。
func asDomainError(err error) (*domain.Error, bool) {
	var de *domain.Error
	ok := errors.As(err, &de)
	return de, ok
}

// formFlag は DELETE のチェックボックスなど、本文かクエリのどちらかで送られるフラグ（htmx 2 の DELETE は値を URL に載せる）。
// 値は機微でない真偽（forget_account・remove_engine）だけ。
func formFlag(c *gin.Context, name string) bool {
	_ = c.Request.ParseForm()
	_, ok := c.Request.Form[name]
	return ok
}

// planFailure は service のエラーを GUI の失敗にする。conflict は状態に合わせた文言（conflictMsg）、想定外は internalMsg。
func planFailure(err error, conflictMsg, internalMsg string) failure {
	switch {
	case errors.Is(err, domain.ErrInvalidArgument):
		return failure{http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), "入力内容を確認してください。"}
	}
	switch domain.CodeOf(err) {
	case domain.CodeConflict:
		return failure{http.StatusConflict, string(domain.CodeConflict), conflictMsg}
	case domain.CodeKillSwitchActive:
		return failure{http.StatusServiceUnavailable, string(domain.CodeKillSwitchActive), "キルスイッチが発動中です。"}
	case domain.CodeAuditUnavailable:
		return failure{http.StatusServiceUnavailable, string(domain.CodeAuditUnavailable), msgPlanAuditDown}
	}
	return failure{http.StatusInternalServerError, string(domain.CodeInternal), internalMsg}
}

// failPlan は planFailure をインラインアラートで返す。refresh は画面の古いカードを直す OOB（状態を変える操作が失敗したとき）。
func (h *Handler) failPlan(c *gin.Context, err error, conflictMsg, internalMsg string, refresh ...templ.Component) {
	f := planFailure(err, conflictMsg, internalMsg)
	if f.status >= http.StatusInternalServerError {
		h.log.Error("gui plan action failed", "method", c.Request.Method, "path", c.Request.URL.Path, "error", err)
	}
	h.failMessage(c, f.status, f.code, f.message, refresh...)
}

// loadSettingsOr は設定を読む。失敗したら fail して false。
func (h *Handler) loadSettingsOr(c *gin.Context) (service.AgentSettingsView, bool) {
	s, err := h.d.AgentSettings.Load(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return s, false
	}
	return s, true
}

// chatgptRefresh・codexRefresh は失敗の応答に添える、現在のカード（OOB）。設定を読めなければ添えない。
func (h *Handler) chatgptRefresh(ctx context.Context) []templ.Component {
	s, err := h.d.AgentSettings.Load(ctx)
	if err != nil {
		return nil
	}
	return []templ.Component{organisms.ChatGPTCardOOB(h.chatgptCardView(s))}
}

func (h *Handler) codexRefresh(ctx context.Context) []templ.Component {
	s, err := h.d.AgentSettings.Load(ctx)
	if err != nil {
		return nil
	}
	return []templ.Component{organisms.CodexCardOOB(h.codexCardView(s))}
}

// renderChatGPTCard・renderCodexCard はカードを 200 で返す（extra は OOB のモーダルを閉じる・トースト）。
func (h *Handler) renderChatGPTCard(c *gin.Context, extra ...templ.Component) {
	s, ok := h.loadSettingsOr(c)
	if !ok {
		return
	}
	h.render(c, http.StatusOK, append([]templ.Component{organisms.ChatGPTCard(h.chatgptCardView(s))}, extra...)...)
}

func (h *Handler) renderCodexCard(c *gin.Context, extra ...templ.Component) {
	s, ok := h.loadSettingsOr(c)
	if !ok {
		return
	}
	h.render(c, http.StatusOK, append([]templ.Component{organisms.CodexCard(h.codexCardView(s))}, extra...)...)
}

// failAgentDisabled は 409 agent_disabled を、data-error-code に加えて detail.reason の data-error-reason を付けて返す（API 仕様 §1・§3.4）。
func (h *Handler) failAgentDisabled(c *gin.Context, err error) {
	alert := errorAlert(string(domain.CodeAgentDisabled), agentDisabledMessage(err))
	alert.Reason = agentDisabledReason(err)
	h.failAlert(c, http.StatusConflict, alert)
}

// ---- 接続方式 ----

// ConfirmAPIKeyConnection は GET /agent/connection/api-key/confirm（FR-G30 (4)）。OpenAI API キーへの切り替えの確認モーダル。
// OpenAI API キーが未登録、または接続方式がすでに openai_api_key なら 409（モーダルなし）。
func (h *Handler) ConfirmAPIKeyConnection(c *gin.Context) {
	s, ok := h.loadSettingsOr(c)
	if !ok {
		return
	}
	switch {
	case connectionOf(s) == domain.ConnectionOpenAIAPIKey:
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "接続方式はすでに OpenAI API キーです。")
		return
	case !s.OpenAIKeyRegistered:
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "OpenAI API キーが登録されていないため、切り替えられません。先に API キーを登録してください。")
		return
	}
	reason := reasonManual
	if c.Query("reason") == reasonUsageLimit {
		reason = reasonUsageLimit
	}
	h.confirm(c, view.ConfirmModal{
		Title: "OpenAI API キーに切り替えますか？",
		Message: "以後のタスクは OpenAI API の従量課金になり、ChatGPT のプランの使用量は使われません。ツール（computer）と API の安全チェックが変わります。" +
			"止まったタスクは再開されません（もう一度 run_task が要ります）。戻すには、この画面で ChatGPT のプランを選び直してください。",
		ConfirmURL: connectionRoute, Method: "put",
		Vals:   jsonObject(map[string]string{"connection": string(domain.ConnectionOpenAIAPIKey), "confirmed": "1", "reason": reason}),
		Target: "#agent-sections", Swap: "outerHTML", Label: "切り替える", Destructive: true,
	})
}

// SetConnection は PUT /agent/connection（FR-G25・S20）。chatgpt ↔ chatgpt-codex は確認なし（どちらもプランの使用量）、
// openai_api_key は確認モーダルの確定ボタンだけが付ける confirmed=1 が無ければ 422（設定は変わらない。service に渡さない）。
func (h *Handler) SetConnection(c *gin.Context) {
	ctx := c.Request.Context()
	conn := domain.AgentConnection(strings.TrimSpace(c.PostForm("connection")))
	if !conn.Valid() {
		h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), msgConnectionInvalid)
		return
	}
	confirmed := c.PostForm("confirmed") == "1"
	if conn == domain.ConnectionOpenAIAPIKey && !confirmed {
		h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), "OpenAI API キーへの切り替えは、確認モーダルで承認してください。")
		return
	}
	reason := reasonManual
	if c.PostForm("reason") == reasonUsageLimit {
		reason = reasonUsageLimit
	}
	if err := h.d.AgentSettings.SetConnection(ctx, conn, confirmed, reason); err != nil {
		h.failPlan(c, err, "OpenAI API キーが登録されていないため、切り替えられません。先に API キーを登録してください。", msgPlanInternal)
		return
	}
	s, ok := h.loadSettingsOr(c)
	if !ok {
		return
	}
	h.render(c, http.StatusOK, organisms.AgentSections(h.agentSettingsPage(ctx, s)), organisms.ModalClear(),
		organisms.ToastOOB(infoToast("接続方式を「"+connectionLabel(conn)+"」に変更しました。")))
}

func connectionLabel(c domain.AgentConnection) string {
	switch c {
	case domain.ConnectionChatGPTCodex:
		return "ChatGPT のプラン（Codex 経由）"
	case domain.ConnectionOpenAIAPIKey:
		return "OpenAI API キー"
	}
	return "ChatGPT のプラン"
}

func jsonObject(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil { // map[string]string は常に変換できる
		panic(err)
	}
	return string(b)
}

// ---- 公式の Sign in with ChatGPT（#chatgpt-card） ----

// ChatGPTSignIn は POST /agent/chatgpt/signin（FR-G28）。本文・クエリを読まない（認可の URL は service が組み立てる 1 つだけ）。
func (h *Handler) ChatGPTSignIn(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.d.ChatGPT.StartSignIn(ctx); err != nil {
		conflict := "今の状態ではサインインを始められません。"
		switch h.d.ChatGPT.Status().State {
		case service.PlanSigningIn:
			conflict = "ChatGPT のサインインはすでに進行中です。完了するか、「中止」してください。"
		case service.PlanConnected, service.PlanUsageLimited:
			conflict = "すでに ChatGPT にサインインしています。"
		}
		h.failPlan(c, err, conflict, "ChatGPT のサインインを始められませんでした。ブラウザを開けなかった可能性があります。もう一度お試しください。", h.chatgptRefresh(ctx)...)
		return
	}
	h.renderChatGPTCard(c)
}

// ChatGPTCancelSignIn は POST /agent/chatgpt/signin/cancel。進行中でなければ何もせず 200（冪等）。
func (h *Handler) ChatGPTCancelSignIn(c *gin.Context) {
	h.d.ChatGPT.CancelSignIn()
	h.renderChatGPTCard(c)
}

const msgChatGPTNotSignedIn = "ChatGPT にサインインしていないため、操作できません。"

// ConfirmChatGPTSignOut は GET /agent/chatgpt/signout/confirm（FR-G29 (6)）。signed_out なら 409（モーダルなし）。
func (h *Handler) ConfirmChatGPTSignOut(c *gin.Context) {
	if h.d.ChatGPT.Status().State == service.PlanSignedOut {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), msgChatGPTNotSignedIn, h.chatgptRefresh(c.Request.Context())...)
		return
	}
	h.confirm(c, view.ConfirmModal{
		Title: "ChatGPT のサインインを解除しますか？",
		Message: "実行中の ChatGPT 経路のタスクは中断されます。ChatGPT 側のアプリの接続の失効を試み、この PC に保存したトークンを消します。" +
			"再びサインインするまで、エージェントは使えません（有効・同意・接続方式の設定は変わりません）。",
		ConfirmURL: chatgptRoute, Method: "delete", Target: "#" + chatgptCardID, Swap: "outerHTML", Label: "解除する", Destructive: true,
		Checkbox: &view.ConfirmCheckbox{Name: "forget_account", Label: "別のアカウントでサインインする（保存したアカウントの情報も消します）"},
	})
}

// ChatGPTSignOut は DELETE /agent/chatgpt（FR-G29 (6)）。失効の失敗は 200 のまま（ローカルの削除は完了する。その旨をトーストに書く）。
func (h *Handler) ChatGPTSignOut(c *gin.Context) {
	ctx := c.Request.Context()
	res, err := h.d.ChatGPT.SignOut(ctx, formFlag(c, "forget_account"))
	if err != nil {
		h.failPlan(c, err, msgChatGPTNotSignedIn, "ChatGPT のサインインを解除できませんでした。", h.chatgptRefresh(ctx)...)
		return
	}
	msg := "ChatGPT のサインインを解除しました。"
	if !res.Revoked {
		msg += "ChatGPT 側の失効を確認できませんでした。ChatGPT の設定からアプリの接続を解除できます。"
	}
	if res.CancelledTask != "" {
		msg += "実行中のタスクを中断しました。"
	}
	h.renderChatGPTCard(c, organisms.ModalClear(), organisms.ToastOOB(infoToast(msg)))
}

// ChatGPTResume は POST /agent/chatgpt/resume（FR-G30 (3)）。usage_limited でなければ 409。
func (h *Handler) ChatGPTResume(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.d.ChatGPT.Resume(ctx); err != nil {
		h.failPlan(c, err, "使用量の上限による一時停止ではないため、再開できません。", "再開できませんでした。", h.chatgptRefresh(ctx)...)
		return
	}
	h.renderChatGPTCard(c)
}

// OpenChatGPTUsage は POST /agent/chatgpt/open-usage（FR-G30 (3)）。本文・クエリ・パスを読まない: 開く先は service が固定する
// https://chatgpt.com/settings/usage の 1 つ（POST /logs/open-folder と同じ作り。NFR-S21）。Codex のカードの「使用量を管理」も同じ。
func (h *Handler) OpenChatGPTUsage(c *gin.Context) {
	if err := h.d.ChatGPT.OpenUsage(c.Request.Context()); err != nil {
		h.log.Error("open chatgpt usage failed", "error", err)
		h.failMessage(c, http.StatusInternalServerError, string(domain.CodeInternal), msgUsageOpenFailed)
		return
	}
	h.render(c, http.StatusOK, organisms.ToastOOB(infoToast("ChatGPT の使用量の設定を開きました。")))
}

// ChatGPTWelcome は GET /agent/chatgpt/welcome。初回のサインイン後の確認モーダル（FR-G28 (7)）。カードが現れたとき htmx が取りに来る。
// すでに閉じた（出す必要が無い）なら 204（何も差し込まない。別のモーダルを閉じない）。
func (h *Handler) ChatGPTWelcome(c *gin.Context) {
	if st := h.d.ChatGPT.Status(); !st.WelcomePending || st.State != service.PlanConnected {
		c.Status(http.StatusNoContent)
		return
	}
	h.confirm(c, view.ConfirmModal{
		Title:      "ChatGPT のプランを使っています",
		Message:    "エージェントは、サインインした ChatGPT のアカウントのプランの使用量を使います。使用量は、ChatGPT の使用量の設定（この画面の「使用量を管理」）で確認できます。",
		ConfirmURL: chatgptRoute + "/welcome/dismiss", Method: "post", Target: "#modal-root", Swap: "none", Label: "わかりました", NoCancel: true,
	})
}

// DismissChatGPTWelcome は POST /agent/chatgpt/welcome/dismiss。確認を閉じたことを保存し（以後は出さない）、モーダルを閉じる。
func (h *Handler) DismissChatGPTWelcome(c *gin.Context) {
	if err := h.d.ChatGPT.DismissWelcome(c.Request.Context()); err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.ModalClear())
}

// ChatGPTModels は GET /agent/chatgpt/models（FR-G26）。connected でなければ 409。取得に失敗したら現在の値だけの選択肢とエラーのアラート。
func (h *Handler) ChatGPTModels(c *gin.Context) {
	h.planModels(c, "chatgpt-model", chatgptRoute, chatgptCardID, func(s service.AgentSettingsView) string { return s.ChatGPTModel },
		h.d.ChatGPT.Models, "ChatGPT にサインインしていないため、モデルの一覧を取得できません。", h.chatgptRefresh)
}

// SaveChatGPTModel は PUT /agent/chatgpt（FR-G26・S21）。一覧に無い値は 422。
func (h *Handler) SaveChatGPTModel(c *gin.Context) {
	h.savePlanModel(c, domain.ConnectionChatGPT, h.d.ChatGPT.Models, "ChatGPT にサインインしていないため、モデルを保存できません。", h.chatgptRefresh, h.renderChatGPTCard)
}

// ---- Codex 経由（#codex-card） ----

const msgCodexNotSignedIn = "Codex にログインしていないため、操作できません。"

func (h *Handler) codexReady() bool {
	switch h.d.Codex.Status().Engine.State {
	case service.CodexEngineInstalled, service.CodexEngineOutdated:
		return true
	}
	return false
}

// CodexInstall は POST /agent/codex/install。固定した版のエンジンの取得を始める（非同期。進みは /poll の rev_codex で反映）。
func (h *Handler) CodexInstall(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.d.Codex.Install(ctx); err != nil {
		conflict := "今の状態ではエンジンを取得できません。"
		switch h.d.Codex.Status().Engine.State {
		case service.CodexEngineUnsupported:
			conflict = "この PC の OS・CPU には Codex のエンジンがないため、取得できません。"
		case service.CodexEngineInstalling:
			conflict = "エンジンの取得はすでに進行中です。"
		}
		h.failPlan(c, err, conflict, "エンジンの取得を始められませんでした。", h.codexRefresh(ctx)...)
		return
	}
	h.renderCodexCard(c)
}

// CodexCancelInstall は POST /agent/codex/install/cancel（冪等）。
func (h *Handler) CodexCancelInstall(c *gin.Context) {
	h.d.Codex.CancelInstall()
	h.renderCodexCard(c)
}

// CodexSignIn は POST /agent/codex/signin。エンジンが無ければ 409。
func (h *Handler) CodexSignIn(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.d.Codex.StartSignIn(ctx); err != nil {
		conflict := "今の状態ではログインを始められません。"
		switch st := h.d.Codex.Status(); {
		case !h.codexReady():
			conflict = "先に Codex のエンジンを取得してください。"
		case st.State == service.PlanSigningIn:
			conflict = "Codex のログインはすでに進行中です。完了するか、「中止」してください。"
		case st.State == service.PlanConnected || st.State == service.PlanUsageLimited:
			conflict = "すでに Codex にログインしています。"
		}
		h.failPlan(c, err, conflict, "Codex のログインを始められませんでした。もう一度お試しください。", h.codexRefresh(ctx)...)
		return
	}
	h.renderCodexCard(c)
}

// CodexCancelSignIn は POST /agent/codex/signin/cancel（冪等）。
func (h *Handler) CodexCancelSignIn(c *gin.Context) {
	h.d.Codex.CancelSignIn(c.Request.Context())
	h.renderCodexCard(c)
}

// ConfirmCodexSignOut は GET /agent/codex/signout/confirm。ログイン中はログアウト（エンジンも消すチェックつき）、ログアウト済みでエンジンがあれば
// エンジンだけの削除、どちらも無ければ 409（モーダルなし）。
func (h *Handler) ConfirmCodexSignOut(c *gin.Context) {
	st := h.d.Codex.Status()
	switch {
	case st.State != service.PlanSignedOut:
		h.confirm(c, view.ConfirmModal{
			Title:      "Codex からログアウトしますか？",
			Message:    "実行中の Codex 経路のタスクは中断されます。再びログインするまで、エージェントは使えません（有効・同意・接続方式の設定は変わりません）。",
			ConfirmURL: codexRoute, Method: "delete", Target: "#" + codexCardID, Swap: "outerHTML", Label: "ログアウトする", Destructive: true,
			Checkbox: &view.ConfirmCheckbox{Name: "remove_engine", Label: "取得したエンジンと専用のデータも削除する"},
		})
	case h.codexReady():
		h.confirm(c, view.ConfirmModal{
			Title:      "Codex のエンジンを削除しますか？",
			Message:    "取得したエンジンと、専用のデータ（Codex 用に分けた保存先）を削除します。もう一度使うには、エンジンを取得し直してください。",
			ConfirmURL: codexRoute, Method: "delete", Vals: jsonObject(map[string]string{"remove_engine": "on"}),
			Target: "#" + codexCardID, Swap: "outerHTML", Label: "削除する", Destructive: true,
		})
	default:
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), msgCodexNotSignedIn, h.codexRefresh(c.Request.Context())...)
	}
}

// CodexSignOut は DELETE /agent/codex（remove_engine。取得したエンジンと専用の CODEX_HOME も消す）。
func (h *Handler) CodexSignOut(c *gin.Context) {
	ctx := c.Request.Context()
	removeEngine := formFlag(c, "remove_engine")
	if err := h.d.Codex.SignOut(ctx, removeEngine); err != nil {
		h.failPlan(c, err, msgCodexNotSignedIn, "Codex からログアウトできませんでした。", h.codexRefresh(ctx)...)
		return
	}
	msg := "Codex からログアウトしました。"
	if removeEngine {
		msg = "Codex からログアウトし、エンジンを削除しました。"
	}
	h.renderCodexCard(c, organisms.ModalClear(), organisms.ToastOOB(infoToast(msg)))
}

// CodexResume は POST /agent/codex/resume。usage_limited でなければ 409。
func (h *Handler) CodexResume(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.d.Codex.Resume(ctx); err != nil {
		h.failPlan(c, err, "使用量の上限による一時停止ではないため、再開できません。", "再開できませんでした。", h.codexRefresh(ctx)...)
		return
	}
	h.renderCodexCard(c)
}

// CodexModels は GET /agent/codex/models。connected でなければ 409。
func (h *Handler) CodexModels(c *gin.Context) {
	h.planModels(c, "codex-model", codexRoute, codexCardID, func(s service.AgentSettingsView) string { return s.CodexModel },
		h.d.Codex.Models, "Codex にログインしていないため、モデルの一覧を取得できません。", h.codexRefresh)
}

// SaveCodexModel は PUT /agent/codex（model）。一覧に無い値は 422。
func (h *Handler) SaveCodexModel(c *gin.Context) {
	h.savePlanModel(c, domain.ConnectionChatGPTCodex, h.d.Codex.Models, "Codex にログインしていないため、モデルを保存できません。", h.codexRefresh, h.renderCodexCard)
}

// ---- モデルの選択（2 つの経路の共通） ----

type modelLister func(context.Context) ([]service.PlanModel, error)

func (h *Handler) planModels(c *gin.Context, id, route, cardID string, current func(service.AgentSettingsView) string,
	list modelLister, conflictMsg string, refresh func(context.Context) []templ.Component) {
	ctx := c.Request.Context()
	s, ok := h.loadSettingsOr(c)
	if !ok {
		return
	}
	sec := planModelSection(id, current(s), route, cardID)
	models, err := list(ctx)
	switch {
	case err == nil:
		h.render(c, http.StatusOK, organisms.PlanModelChoices(planModelChoices(sec, models)))
	case domain.CodeOf(err) == domain.CodeConflict:
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), conflictMsg, refresh(ctx)...)
	default:
		// 本文・トークンは出さない。現在の値だけの選択肢と、エラーのアラートを返す。
		h.log.Warn("list plan models failed", "path", c.Request.URL.Path, "code", domain.CodeOf(err))
		h.render(c, http.StatusOK, organisms.PlanModelChoices(planModelCurrentOnly(sec)),
			organisms.AlertOOB(errorAlert(string(domain.CodeInternal), msgModelsFailed)))
	}
}

func (h *Handler) savePlanModel(c *gin.Context, conn domain.AgentConnection, list modelLister, conflictMsg string,
	refresh func(context.Context) []templ.Component, renderCard func(*gin.Context, ...templ.Component)) {
	ctx := c.Request.Context()
	model := strings.TrimSpace(c.PostForm("model"))
	models, err := list(ctx)
	if err != nil {
		h.failPlan(c, err, conflictMsg, msgModelsFailed, refresh(ctx)...)
		return
	}
	if !slices.ContainsFunc(models, func(m service.PlanModel) bool { return m.Slug == model }) {
		h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), msgModelNotListed)
		return
	}
	if err := h.d.AgentSettings.SavePlanModel(ctx, conn, model); err != nil {
		var verr *service.ValidationError
		if errors.As(err, &verr) {
			h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), msgModelInvalid)
			return
		}
		h.fail(c, err)
		return
	}
	renderCard(c, organisms.ToastOOB(infoToast("モデルを保存しました。")))
}

// planPollComponents は GET /poll の rev_chatgpt・rev_codex の OOB（表示モデルのハッシュが違うカードだけ）。
func (h *Handler) planPollComponents(c *gin.Context) []templ.Component {
	revChatGPT, showChatGPT := c.GetQuery("rev_chatgpt")
	revCodex, showCodex := c.GetQuery("rev_codex")
	if !showChatGPT && !showCodex {
		return nil
	}
	s, err := h.d.AgentSettings.Load(c.Request.Context())
	if err != nil {
		h.log.Warn("poll: load agent settings failed", "error", err)
		return nil
	}
	var out []templ.Component
	if showChatGPT {
		if v := h.chatgptCardView(s); v.Rev != revChatGPT {
			out = append(out, organisms.ChatGPTCardOOB(v))
		}
	}
	if showCodex {
		if v := h.codexCardView(s); v.Rev != revCodex {
			out = append(out, organisms.CodexCardOOB(v))
		}
	}
	return out
}
