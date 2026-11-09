package remote

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルはビューアのウィンドウ選択（#157。API 仕様 §4.2）: 一覧の描画（GET /viewer/windows）と、選んだウィンドウ T を束縛した
// スコープ付きリースの取得（POST /viewer/target）。承認モード・クールダウン・他者の保持中の扱いは POST /viewer/lease/acquire と同じ。
// 配信の源の切り替えと前面化は、リースが held になったときに WebSocket（ws）が行う。

// windowPickerView は who から見たウィンドウ選択の領域。
func (h *Handler) windowPickerView(ctx context.Context, who domain.Actor) view.WindowPicker {
	if holder, ok := h.d.Leases.Holder(); ok {
		switch {
		case holder.Actor.SameSession(who) && holder.Scope != nil:
			return view.WindowPicker{ScopedProcess: holder.Scope.Process}
		case holder.Actor.SameSession(who):
			return view.WindowPicker{Message: "ウィンドウを選ぶには、先に操作権を解放してください。"}
		}
		return view.WindowPicker{Message: "ほかのセッションが操作権を保持しています。"}
	}
	if _, pending := h.d.Leases.Pending(); pending {
		return view.WindowPicker{Message: "ホストの承認を待っています。"}
	}
	cands, err := h.d.Windows.Candidates(ctx)
	if err != nil {
		h.log.Warn("window picker: listing windows failed", "error", err)
		return view.WindowPicker{Error: classify(err).message}
	}
	p := view.WindowPicker{CanSelect: true}
	for _, c := range cands {
		p.Rows = append(p.Rows, windowRow(c))
	}
	return p
}

func windowRow(c domain.PickCandidate) view.WindowRow {
	return view.WindowRow{
		ID: c.Window.ID, PID: strconv.FormatUint(uint64(c.Window.PID), 10), Start: strconv.FormatUint(c.Window.ProcessStart, 10),
		Title: c.Window.Title, Process: c.Window.Process, Foreground: c.Window.Foreground, Minimized: c.Window.Minimized,
		Selectable: c.Selectable(), Note: pickNote(c.Reason),
	}
}

// pickNote は選べない理由（灰色の行に添える短い文）。
func pickNote(r domain.GroupReason) string {
	switch r {
	case domain.ReasonElevatedWindow:
		return "管理者権限のため選べません"
	case domain.ReasonTargetMinimized:
		return "最小化されています"
	case domain.ReasonTargetUnsupported:
		return "このウィンドウは対象にできません（検証前の種類）"
	}
	return ""
}

// WindowPicker は GET /viewer/windows（ウィンドウ選択のフラグメント。更新ボタン・自動更新・remo:lease-changed で再取得される）。
func (h *Handler) WindowPicker(c *gin.Context) {
	if _, who, ok := h.viewerContext(c); ok {
		h.render(c, http.StatusOK, organisms.WindowPicker(h.windowPickerView(c.Request.Context(), who)))
	}
}

// SelectTarget は POST /viewer/target（ウィンドウ T を選んでスコープ付きのリースを取得）。フォームは window_id・pid・process_start
// （一覧が返した値）。サーバーは一覧から T を引き直し、pid・process_start が今の T と違えば（閉じた・置き換わった）404 window_not_found。
// 選べない T（昇格・最小化・検証前の種類）は 409 input_blocked。承認が要らなければ 200 のリースパネル、要るなら 202 の承認待ちのパネル
// （結果は WebSocket の lease で届く）。すでに保持中（自分を含む）は 409。
func (h *Handler) SelectTarget(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	if !h.parseForm(c) {
		return
	}
	ref, err := h.resolveTarget(c, c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	if holder, held := h.d.Leases.Holder(); held && holder.Actor.SameSession(who) {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "操作権を保持しています。先に解放してから、ウィンドウを選び直してください。")
		return
	}
	l, pending, err := h.d.Leases.RequestAcquireScoped(c.Request.Context(), who, ref)
	if err != nil {
		h.fail(c, err)
		return
	}
	if pending {
		h.renderLeasePanel(c, http.StatusAccepted, view.ViewerLease{
			Pending: true, PendingMine: true, HolderLabel: actorLabel(l.Holder), KindLabel: string(l.Holder.Kind),
		})
		return
	}
	h.leasePanel(c, who)
}

// ReleaseTarget は POST /viewer/target/release（「別のウィンドウを選ぶ」。対象を外す）。操作権（スコープ付きのリース）を解放する。
// 配信はモニター表示へ戻り、一覧が出る。保持していなければ何もしない。POST /viewer/lease/release と同じ結果で、バナーの
// remo:reselect が呼ぶ入口を分けているだけ。
func (h *Handler) ReleaseTarget(c *gin.Context) {
	_, who, ok := h.viewerContext(c)
	if !ok {
		return
	}
	h.d.Leases.Release(who)
	h.leasePanel(c, who)
}

// resolveTarget はフォームの window_id・pid・process_start から、一覧にいる T の WindowRef を引く。
func (h *Handler) resolveTarget(c *gin.Context, ctx context.Context) (domain.WindowRef, error) {
	id, _ := formValue(c, "window_id")
	pidText, _ := formValue(c, "pid")
	startText, _ := formValue(c, "process_start")
	pid, perr := strconv.ParseUint(pidText, 10, 32)
	start, serr := strconv.ParseUint(startText, 10, 64)
	if id == "" || perr != nil || serr != nil {
		return domain.WindowRef{}, domain.ErrInvalidArgument.WithMsg("window_id, pid and process_start are required")
	}
	cands, err := h.d.Windows.Candidates(ctx)
	if err != nil {
		return domain.WindowRef{}, err
	}
	for _, cand := range cands {
		if cand.Window.ID != id {
			continue
		}
		if uint64(cand.Window.PID) != pid || cand.Window.ProcessStart != start {
			break
		}
		if !cand.Selectable() {
			return domain.WindowRef{}, scopedBlocked(cand)
		}
		return cand.Ref, nil
	}
	return domain.WindowRef{}, domain.ErrWindowNotFound.WithMsg("the window is not available").WithDetail("reason", string(domain.ReasonTargetClosed))
}

// scopedBlocked は選べないウィンドウを選んだときの 409 input_blocked（束縛が断るのと同じ reason）。
func scopedBlocked(c domain.PickCandidate) error {
	return domain.ErrInputBlocked.WithMsg("the window cannot be selected: "+string(c.Reason)).WithDetail("reason", string(c.Reason))
}
