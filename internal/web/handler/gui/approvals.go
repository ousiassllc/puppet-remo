package gui

import (
	"errors"
	"math"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは承認ダイアログ（FR-H05）。ウィンドウを開くのは app（Bus の approval）で、ここは中身の描画（GET /approvals/{id}）と、
// 許可・拒否（POST /approvals/{id}/allow・deny）の受け付けだけ。kind は lease（M3）・safety_check・judge_action（M4）の 3 種で、
// 見出しと説明が違うだけで、許可・拒否の経路は同じ。

// closeWindowTrigger は許可・拒否のあとにウィンドウを閉じる HX-Trigger のイベント名（app.js が受ける）。
const closeWindowTrigger = "closeWindow"

// approvalGoneMessage は、承認要求がすでに終わっているときの文言（API 仕様 §7.3: 410 approval_timeout）。
const approvalGoneMessage = "この承認要求はすでに終了しています（60 秒の時間切れ、または処理済みです）。"

// pendingApproval は id の承認待ちの要求（lease・safety_check・judge_action）を返す。終わっている・存在しない・未知の kind なら false。
func (h *Handler) pendingApproval(id string) (service.ApprovalRequest, bool) {
	for _, r := range h.d.Approval.Pending() {
		if r.ID == id && knownApprovalKind(r.Kind) {
			return r, true
		}
	}
	return service.ApprovalRequest{}, false
}

func knownApprovalKind(kind string) bool {
	switch kind {
	case service.ApprovalKindLease, service.ApprovalKindSafetyCheck, service.ApprovalKindJudgeAction:
		return true
	}
	return false
}

// approvalTexts は承認の種類ごとの見出し・要求の内容・結果の説明（FR-H05・G08・G12）。Note は、許可しなかったときに何が起きるか。
// safety_check は「自動許可」の承認モードでも省略できず、拒否・60 秒の時間切れはタスクの失敗（safety_check_denied）になる（FR-G08）。
func approvalTexts(kind string) (heading, summary, note string) {
	switch kind {
	case service.ApprovalKindSafetyCheck:
		return "OpenAI の安全チェックを確認してください",
			"OpenAI が、次の操作に安全上の警告（pending_safety_checks）を付けました。許可すると、警告を承知したうえでタスクを続行します。",
			"この確認は、クライアントの承認モードが「自動許可」でも省略されません。拒否した場合、または 60 秒で応答がない場合は、タスクが失敗（safety_check_denied）します。"
	case service.ApprovalKindJudgeAction:
		return "エージェントのアクションを許可しますか？",
			"ルール（承認が必要なプロセス）に該当するアクションの実行を、エージェントが求めています。",
			"拒否した場合、または 60 秒で応答がない場合は、そのアクションは実行されず、タスクが失敗します。"
	}
	return "操作権の取得を許可しますか？", "操作権（リース）の取得", ""
}

// approvalRemaining は ExpiresAt までの残り秒数（切り上げ。過ぎていれば 0）。
func (h *Handler) approvalRemaining(req service.ApprovalRequest) int {
	return max(0, int(math.Ceil(req.ExpiresAt.Sub(h.d.Clock.Now()).Seconds())))
}

func (h *Handler) approvalDialogView(req service.ApprovalRequest) view.ApprovalDialog {
	id := url.PathEscape(req.ID)
	base := "/approvals/" + id
	heading, summary, note := approvalTexts(req.Kind)
	if req.Kind == service.ApprovalKindLease && req.Scope != nil {
		summary = leaseScopeSummary
	}
	return view.ApprovalDialog{
		ID:          req.ID,
		Kind:        req.Kind,
		Heading:     heading,
		ClientLabel: actorLabel(req.Who),
		KindLabel:   kindLabel(req.Who.Kind),
		Summary:     summary,
		Detail:      approvalDetail(req),
		Note:        note,
		Remaining:   view.ApprovalRemaining{ID: req.ID, Seconds: h.approvalRemaining(req), RefreshURL: base},
		AllowURL:    base + "/allow",
		DenyURL:     base + "/deny",
	}
}

// approvalDetail は safety_check・judge_action の内容の説明（ApprovalRequest.Detail。サービスが秘密・入力した文字列の本文・パス・引数を
// 除いて渡す）。lease は、スコープ付きのときだけ対象のプロセス名（ウィンドウのタイトルは出さない）。念のため、長さは 500 文字で切る（サービスの上限と同じ。画面を覆わせない）。
func approvalDetail(req service.ApprovalRequest) string {
	if req.Kind == service.ApprovalKindLease {
		if req.Scope == nil {
			return ""
		}
		return truncateRunes("対象のプロセス: "+req.Scope.Process, approvalDetailMaxRunes)
	}
	return truncateRunes(req.Detail, approvalDetailMaxRunes)
}

// leaseScopeSummary はスコープ付きリース（ビューアのウィンドウ選択。#157）の承認の要求の内容。対象のプロセス名は Detail に出す。
const leaseScopeSummary = "操作権（リース）の取得。操作はウィンドウ 1 枚と関連するダイアログ・メニューに限定されます。"

// approvalDetailMaxRunes は Detail の表示の上限（文字数）。
const approvalDetailMaxRunes = 500

// ApprovalDialog は GET /approvals/:id（承認ダイアログのフルページ）。HTMX の部分更新（残り時間の再取得）は残り時間の要素だけ。
// 失効・解決済み・存在しない id・未知の kind は 410 approval_timeout（フルページは終了した旨の表示）。
func (h *Handler) ApprovalDialog(c *gin.Context) {
	req, ok := h.pendingApproval(c.Param("id"))
	if !ok {
		h.approvalGone(c)
		return
	}
	v := h.approvalDialogView(req)
	c.Header("Cache-Control", "no-store")
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, organisms.ApprovalRemaining(v.Remaining))
		return
	}
	h.render(c, http.StatusOK, pages.ApprovalDialog(v))
}

// approvalGone は 410 approval_timeout を返す。フルページには終了した旨を出し、HTMX の応答（ボタン・残り時間の更新）は
// data-error-code 付きのインラインアラート（HX-Retarget: #alert-root）にする。
func (h *Handler) approvalGone(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetHeader("HX-Request") != "" {
		h.failMessage(c, http.StatusGone, string(domain.CodeApprovalTimeout), approvalGoneMessage)
		return
	}
	h.render(c, http.StatusGone, pages.ApprovalGone(view.ApprovalGone{Code: string(domain.CodeApprovalTimeout), Message: approvalGoneMessage}))
}

// AllowApproval は POST /approvals/:id/allow（許可）。
func (h *Handler) AllowApproval(c *gin.Context) { h.resolveApproval(c, true) }

// DenyApproval は POST /approvals/:id/deny（拒否）。
func (h *Handler) DenyApproval(c *gin.Context) { h.resolveApproval(c, false) }

// resolveApproval は結果を Approval.Resolve へ渡す。成功は 200 で、HX-Trigger: closeWindow でウィンドウを閉じる。
// 失効（60 秒）・解決済み・存在しない id は 410 approval_timeout。未知の kind も同じく 410 にする。
func (h *Handler) resolveApproval(c *gin.Context, allow bool) {
	c.Header("Cache-Control", "no-store")
	if _, ok := h.pendingApproval(c.Param("id")); !ok {
		h.approvalGone(c)
		return
	}
	if err := h.d.Approval.Resolve(c.Param("id"), allow); err != nil {
		if errors.Is(err, domain.ErrApprovalTimeout) {
			h.approvalGone(c)
			return
		}
		h.fail(c, err)
		return
	}
	c.Header("HX-Trigger", closeWindowTrigger)
	h.render(c, http.StatusOK, organisms.ApprovalDone(view.ApprovalDone{Allowed: allow}))
}
