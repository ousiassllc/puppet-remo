package gui

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは起動の方式（launch.mode）と拒否リスト（#88。FR-H12）のルート。方式の切り替えと拒否リストの書き換えは
// ホスト所有者だけで、この GUI ルーターにだけある（MCP・Web UI・リモートのルーターには作らない。NFR-S20）。
// 監査（settings.launch_mode.update・settings.launch_denylist.*）は service.AppRegistry が行う。拒否リストの値（パス・実行ファイル名）は
// ホスト所有者だけが見る GUI に出すが、ログ・エラーのメッセージには出さない。

const (
	launchModeURL        = appsURL + "/mode"
	launchModeConfirmURL = launchModeURL + "/confirm"
	deniedURL            = appsURL + "/denied"
)

// msgDeniedListFull は拒否リストが上限に達したときの 409 conflict のメッセージ。
const msgDeniedListFull = "登録できる拒否リストは 200 件までです。不要なものを削除してから追加してください。"

func deniedItemURL(id string) string   { return deniedURL + "/" + url.PathEscape(id) }
func deniedEditURL(id string) string   { return deniedItemURL(id) + "/edit" }
func deniedDeleteURL(id string) string { return deniedItemURL(id) + "/delete/confirm" }
func (h *Handler) launchModePanel(m domain.LaunchMode) view.LaunchModePanel {
	return view.LaunchModePanel{Denylist: m == domain.LaunchModeDenylist, ConfirmURL: launchModeConfirmURL + "?mode=" + string(domain.LaunchModeDenylist), ActionURL: launchModeURL}
}

// deniedState は拒否リストのセクションの描画の状態: 編集中の行（EditID。EditValue・EditError はその入力）と、追加フォームの入力。
type deniedState struct {
	EditID, EditValue, EditError string
	AddValue, AddError           string
}

// deniedSection は拒否リストのセクションの表示モデル。
func (h *Handler) deniedSection(ctx context.Context, st deniedState) (view.DeniedSection, error) {
	mode, err := h.d.Apps.LaunchMode(ctx)
	if err != nil {
		return view.DeniedSection{}, err
	}
	denied, err := h.d.Apps.ListDenied(ctx)
	if err != nil {
		return view.DeniedSection{}, err
	}
	rows := make([]view.DeniedRow, len(denied))
	for i, d := range denied {
		rows[i] = view.DeniedRow{
			ID: d.ID, IsPath: d.Kind == domain.DeniedPath, Value: d.Value,
			EditURL: deniedEditURL(d.ID), DeleteConfirm: deniedDeleteURL(d.ID), ActionURL: deniedItemURL(d.ID),
		}
		if st.EditID != "" && d.ID == st.EditID {
			rows[i].Editing, rows[i].EditValue, rows[i].EditError = true, st.EditValue, st.EditError
		}
	}
	return view.DeniedSection{
		Rows: rows, Count: len(denied), Max: domain.MaxDeniedApps, Active: mode == domain.LaunchModeDenylist,
		AddValue: st.AddValue, AddError: st.AddError, AddURL: deniedURL, ListURL: deniedURL,
	}, nil
}

// renderDenied は拒否リストのセクションを status で返す。extra は同じ応答に添える OOB の断片（トースト・モーダルを閉じる）。
func (h *Handler) renderDenied(c *gin.Context, status int, st deniedState, extra ...templ.Component) {
	v, err := h.deniedSection(c.Request.Context(), st)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, status, append([]templ.Component{organisms.DeniedSection(v)}, extra...)...)
}

// ConfirmLaunchMode は GET /apps/mode/confirm?mode=denylist。ブラックリスト方式へ切り替える確認モーダル。
func (h *Handler) ConfirmLaunchMode(c *gin.Context) {
	h.render(c, http.StatusOK, molecules.Modal(view.ConfirmModal{
		Title: "ブラックリスト方式に切り替えますか？",
		Message: "ブラックリスト方式にすると、拒否リストに載せたアプリ以外は、リモートの AI がパスを指定して何でも起動できるようになります" +
			"（cmd.exe・powershell.exe などのコマンドインタープリタも、拒否リストに載せない限り起動できます）。" +
			"引数と作業ディレクトリは渡せません。拒否リストはパス・実行ファイル名で照合します。この画面でいつでもホワイトリスト方式に戻せます。",
		ConfirmURL: launchModeURL, Method: "put", Vals: `{"mode":"denylist"}`, Target: "#launch-mode", Swap: "outerHTML", Label: "ブラックリスト方式にする", Destructive: true,
	}))
}

// SetLaunchMode は PUT /apps/mode（フォーム mode = allowlist / denylist）。応答は方式のパネルと、OOB で拒否リストのセクション。
func (h *Handler) SetLaunchMode(c *gin.Context) {
	ctx := c.Request.Context()
	mode := domain.LaunchMode(c.PostForm("mode"))
	if err := h.d.Apps.SetLaunchMode(ctx, mode); err != nil {
		var fe service.AppFieldErrors
		if errors.As(err, &fe) {
			h.failMessage(c, http.StatusUnprocessableEntity, string(domain.CodeInvalidArgument), fe[domain.AppFieldLaunchMode])
			return
		}
		h.fail(c, err)
		return
	}
	denied, err := h.deniedSection(ctx, deniedState{})
	if err != nil {
		h.fail(c, err)
		return
	}
	denied.OOB = true
	msg := "ホワイトリスト方式に戻しました。"
	if mode == domain.LaunchModeDenylist {
		msg = "ブラックリスト方式に切り替えました。"
	}
	h.render(c, http.StatusOK,
		organisms.LaunchModePanel(h.launchModePanel(mode)),
		organisms.DeniedSection(denied),
		organisms.ModalClear(),
		organisms.ToastOOB(infoToast(msg)),
	)
}

// Denied は GET /apps/denied。HTMX の部分更新は拒否リストのセクション（編集のキャンセルが使う）。
func (h *Handler) Denied(c *gin.Context) {
	if fragmentRequest(c) && !boostedRequest(c) {
		h.renderDenied(c, http.StatusOK, deniedState{})
		return
	}
	h.Apps(c)
}

// DeniedEdit は GET /apps/denied/:id/edit。HTMX の部分更新は、その行を編集フォームにしたセクション。対象が無ければ 404。
func (h *Handler) DeniedEdit(c *gin.Context) {
	ctx := c.Request.Context()
	d, ok, err := h.findDenied(ctx, c.Param("id"))
	switch {
	case err != nil:
		h.pageFail(c, err)
	case !ok:
		h.pageFail(c, service.ErrNotFound)
	case fragmentRequest(c) && !boostedRequest(c):
		h.renderDenied(c, http.StatusOK, deniedState{EditID: d.ID, EditValue: d.Value})
	default:
		h.Apps(c)
	}
}

func (h *Handler) findDenied(ctx context.Context, id string) (domain.DeniedApp, bool, error) {
	all, err := h.d.Apps.ListDenied(ctx)
	if err != nil {
		return domain.DeniedApp{}, false, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, true, nil
		}
	}
	return domain.DeniedApp{}, false, nil
}

// deniedSaveFailed は拒否リストの追加・変更の失敗を返す。項目の不備・重複は 422 で、入力値とメッセージ付きにセクションを再描画する。200 件到達は 409。
func (h *Handler) deniedSaveFailed(c *gin.Context, err error, st deniedState, setErr func(*deniedState, string)) {
	var fe service.AppFieldErrors
	switch {
	case errors.As(err, &fe):
		setErr(&st, fe[domain.AppFieldDeniedValue])
		h.renderDenied(c, http.StatusUnprocessableEntity, st)
	case domain.CodeOf(err) == domain.CodeConflict:
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), msgDeniedListFull)
	default:
		h.fail(c, err)
	}
}

// CreateDenied は POST /apps/denied（フォーム value）。応答は拒否リストのセクション。
func (h *Handler) CreateDenied(c *gin.Context) {
	value := c.PostForm("value")
	if _, err := h.d.Apps.CreateDenied(c.Request.Context(), value); err != nil {
		h.deniedSaveFailed(c, err, deniedState{AddValue: value}, func(st *deniedState, m string) { st.AddError = m })
		return
	}
	h.renderDenied(c, http.StatusOK, deniedState{}, organisms.ToastOOB(infoToast("拒否リストに追加しました。")))
}

// UpdateDenied は PUT /apps/denied/:id（フォーム value）。応答は拒否リストのセクション。
func (h *Handler) UpdateDenied(c *gin.Context) {
	id, value := c.Param("id"), c.PostForm("value")
	if _, err := h.d.Apps.UpdateDenied(c.Request.Context(), id, value); err != nil {
		h.deniedSaveFailed(c, err, deniedState{EditID: id, EditValue: value}, func(st *deniedState, m string) { st.EditError = m })
		return
	}
	h.renderDenied(c, http.StatusOK, deniedState{}, organisms.ToastOOB(infoToast("拒否リストを更新しました。")))
}

// ConfirmDeleteDenied は GET /apps/denied/:id/delete/confirm。対象が無ければ 404。
func (h *Handler) ConfirmDeleteDenied(c *gin.Context) {
	d, ok, err := h.findDenied(c.Request.Context(), c.Param("id"))
	switch {
	case err != nil:
		h.fail(c, err)
	case !ok:
		h.fail(c, service.ErrNotFound)
	default:
		h.render(c, http.StatusOK, molecules.Modal(view.ConfirmModal{
			Title:      "拒否リストから外しますか？",
			Message:    "「" + d.Value + "」を拒否リストから外します。ブラックリスト方式のときは、以後このアプリを launch_app で起動できるようになります。",
			ConfirmURL: deniedItemURL(d.ID), Method: "delete", Target: "#denied-section", Swap: "outerHTML", Label: "外す", Destructive: true,
		}))
	}
}

// DeleteDenied は DELETE /apps/denied/:id。応答は拒否リストのセクション。
func (h *Handler) DeleteDenied(c *gin.Context) {
	if err := h.d.Apps.DeleteDenied(c.Request.Context(), c.Param("id")); err != nil {
		h.fail(c, err)
		return
	}
	h.renderDenied(c, http.StatusOK, deniedState{}, organisms.ModalClear(), organisms.ToastOOB(infoToast("拒否リストから外しました。")))
}
