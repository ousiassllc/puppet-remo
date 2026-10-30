package gui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは起動許可アプリ（M1.5。FR-H12）のルート。許可リストを書き換えるのはホスト所有者だけで、この GUI ルーターにだけある（NFR-S20）。
// 追加・変更・削除の監査（settings.launchable_app.*。引数・作業ディレクトリは記録しない）は service.AppRegistry が行う。
// 実行ファイルのパス・引数・作業ディレクトリはホスト所有者だけが見る GUI に出すが、ログ・エラーのメッセージには出さない。

const (
	appsURL        = "/apps"
	appPickURL     = "/apps/pick-file"
	newAppFormID   = "app-form-new"
	editFormIDBase = "app-form-"
)

// msgAppListFull は 50 件に達したときの 409 conflict のメッセージ（NFR-C06）。
const msgAppListFull = "登録できるアプリは 50 件までです。不要なものを削除してから追加してください。"

// formIDPattern は POST /apps/pick-file が受け取る form_id（入力欄の id の元）の形。許可する文字だけ（id 属性に入れるため）。
var formIDPattern = regexp.MustCompile(`^app-form-[A-Za-z0-9_-]{1,64}$`)

func appEditURL(id string) string   { return appsURL + "/" + url.PathEscape(id) + "/edit" }
func appItemURL(id string) string   { return appsURL + "/" + url.PathEscape(id) }
func appDeleteURL(id string) string { return appItemURL(id) + "/delete/confirm" }

func (h *Handler) newAppForm(v view.AppFormValues, errs view.AppFormErrors) view.AppForm {
	return view.AppForm{DOMID: newAppFormID, ActionURL: appsURL, Values: v, Errors: errs, PickURL: appPickURL}
}

// editAppForm は行 id の編集フォーム。corrupt は保存済みの引数が読めなかった行（引数を入れ直す案内を出す）。
func (h *Handler) editAppForm(id string, v view.AppFormValues, errs view.AppFormErrors, corrupt bool) view.AppForm {
	return view.AppForm{
		ID: id, DOMID: editFormIDBase + id, ActionURL: appItemURL(id), Values: v, Errors: errs,
		PickURL: appPickURL, CancelURL: appsURL, Corrupt: corrupt,
	}
}

func appFormValues(a domain.LaunchableApp) view.AppFormValues {
	return view.AppFormValues{Label: a.Label, ExePath: a.ExePath, Args: strings.Join(a.Args, "\n"), WorkingDir: a.WorkingDir}
}

func (h *Handler) appRow(a domain.LaunchableApp) view.AppRow {
	return view.AppRow{
		ID: a.ID, Label: a.Label, ExePath: a.ExePath, Args: a.Args, WorkingDir: a.WorkingDir, Corrupt: a.Corrupt,
		Unlaunchable: a.Corrupt || domain.CheckLaunchTarget(a) != nil,
		EditURL:      appEditURL(a.ID), DeleteConfirm: appDeleteURL(a.ID),
	}
}

// appListView は一覧の表示モデル。editID の行は、カードの代わりに編集フォーム（edit）を出す。
func (h *Handler) appListView(apps []domain.LaunchableApp, editID string, edit *view.AppForm) view.AppList {
	rows := make([]view.AppRow, len(apps))
	for i, a := range apps {
		rows[i] = h.appRow(a)
		if editID != "" && a.ID == editID {
			rows[i].Form = edit
		}
	}
	return view.AppList{Rows: rows, Count: len(apps), Max: domain.MaxLaunchableApps, ListURL: appsURL}
}

// appsPage はページ（フル・boosted）の表示モデル。
func (h *Handler) appsPage(ctx context.Context, apps []domain.LaunchableApp, editID string, edit *view.AppForm) view.Apps {
	return view.Apps{
		Chrome:  h.chrome(ctx, view.NavApps),
		List:    h.appListView(apps, editID, edit),
		NewForm: h.newAppForm(view.AppFormValues{}, view.AppFormErrors{}),
	}
}

func (h *Handler) renderApps(c *gin.Context, v view.Apps) {
	if boostedRequest(c) {
		h.render(c, http.StatusOK, pages.AppsBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.Apps(v))
}

// Apps は GET /apps。HTMX の部分更新は一覧のフラグメント（編集のキャンセルが使う）。
func (h *Handler) Apps(c *gin.Context) {
	ctx := c.Request.Context()
	apps, err := h.d.Apps.List(ctx)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	if fragmentRequest(c) && !boostedRequest(c) {
		h.render(c, http.StatusOK, organisms.AppList(h.appListView(apps, "", nil)))
		return
	}
	h.renderApps(c, h.appsPage(ctx, apps, "", nil))
}

// AppNew は GET /apps/new。HTMX の部分更新は追加フォームのフラグメント。
func (h *Handler) AppNew(c *gin.Context) {
	if fragmentRequest(c) && !boostedRequest(c) {
		h.render(c, http.StatusOK, organisms.AppForm(h.newAppForm(view.AppFormValues{}, view.AppFormErrors{}), false))
		return
	}
	h.Apps(c)
}

// AppEdit は GET /apps/:id/edit。HTMX の部分更新は編集フォームのフラグメント（行のカードを置き換える）。対象が無ければ 404。
func (h *Handler) AppEdit(c *gin.Context) {
	ctx := c.Request.Context()
	app, err := h.d.Apps.Get(ctx, c.Param("id"))
	if err != nil {
		h.pageFail(c, err)
		return
	}
	form := h.editAppForm(app.ID, appFormValues(app), view.AppFormErrors{}, app.Corrupt)
	if fragmentRequest(c) && !boostedRequest(c) {
		h.render(c, http.StatusOK, organisms.AppForm(form, false))
		return
	}
	apps, err := h.d.Apps.List(ctx)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	h.renderApps(c, h.appsPage(ctx, apps, app.ID, &form))
}

// readAppForm はフォームの入力を読む。表示用の値（引数は入力されたままの文字列）と、service に渡す入力を返す。
func readAppForm(c *gin.Context) (view.AppFormValues, domain.AppInput) {
	v := view.AppFormValues{
		Label:      c.PostForm("label"),
		ExePath:    strings.TrimSpace(c.PostForm("exe_path")),
		Args:       c.PostForm("args"),
		WorkingDir: strings.TrimSpace(c.PostForm("working_dir")),
	}
	return v, domain.AppInput{Label: v.Label, ExePath: v.ExePath, Args: domain.ParseArgsLines(v.Args), WorkingDir: v.WorkingDir}
}

func appFormErrors(fe service.AppFieldErrors) view.AppFormErrors {
	return view.AppFormErrors{
		Label: fe[domain.AppFieldLabel], ExePath: fe[domain.AppFieldExePath],
		Args: fe[domain.AppFieldArgs], WorkingDir: fe[domain.AppFieldWorkingDir],
	}
}

// saveFailed は追加・変更の失敗を返す。項目別の不備は 422 で、入力値と項目別のメッセージ付きで form を再描画する。
// 50 件に達していれば 409 conflict。
func (h *Handler) saveFailed(c *gin.Context, err error, form func(view.AppFormErrors) view.AppForm) {
	var fe service.AppFieldErrors
	switch {
	case errors.As(err, &fe):
		h.render(c, http.StatusUnprocessableEntity, organisms.AppForm(form(appFormErrors(fe)), false))
	case domain.CodeOf(err) == domain.CodeConflict:
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), msgAppListFull)
	default:
		h.fail(c, err)
	}
}

// CreateApp は POST /apps。応答は一覧のフラグメントで、追加フォームを空に戻す（OOB）。
func (h *Handler) CreateApp(c *gin.Context) {
	ctx := c.Request.Context()
	values, in := readAppForm(c)
	if _, err := h.d.Apps.Create(ctx, in); err != nil {
		h.saveFailed(c, err, func(e view.AppFormErrors) view.AppForm { return h.newAppForm(values, e) })
		return
	}
	apps, err := h.d.Apps.List(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK,
		organisms.AppList(h.appListView(apps, "", nil)),
		organisms.AppForm(h.newAppForm(view.AppFormValues{}, view.AppFormErrors{}), true),
		organisms.ToastOOB(infoToast("アプリを追加しました。")),
	)
}

// UpdateApp は PUT /apps/:id（全項目を置き換える）。応答は行のカード。id は変わらない。
func (h *Handler) UpdateApp(c *gin.Context) {
	id := c.Param("id")
	values, in := readAppForm(c)
	ctx := c.Request.Context()
	app, err := h.d.Apps.Update(ctx, id, in)
	if err != nil {
		h.saveFailed(c, err, func(e view.AppFormErrors) view.AppForm {
			// 検証に失敗したフォームにも、読めなかった行への案内を残す（行が取れなければ案内なし）。
			prev, getErr := h.d.Apps.Get(ctx, id)
			return h.editAppForm(id, values, e, getErr == nil && prev.Corrupt)
		})
		return
	}
	h.render(c, http.StatusOK,
		organisms.AppCard(h.appRow(app)),
		organisms.ToastOOB(infoToast("アプリを更新しました。")),
	)
}

// ConfirmDeleteApp は GET /apps/:id/delete/confirm。対象が無ければ 404。
func (h *Handler) ConfirmDeleteApp(c *gin.Context) {
	app, err := h.d.Apps.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, molecules.Modal(view.ConfirmModal{
		Title:      "アプリを許可リストから外しますか？",
		Message:    "「" + app.Label + "」を許可リストから外します。以後は launch_app で起動できなくなります。すでに起動しているプロセスには影響しません。",
		ConfirmURL: appItemURL(app.ID), Method: "delete", Target: "#app-list", Swap: "outerHTML", Label: "外す",
	}))
}

// DeleteApp は DELETE /apps/:id。応答は一覧のフラグメント。起動済みのプロセスには触れない。
func (h *Handler) DeleteApp(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.d.Apps.Delete(ctx, c.Param("id")); err != nil {
		h.fail(c, err)
		return
	}
	apps, err := h.d.Apps.List(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK,
		organisms.AppList(h.appListView(apps, "", nil)),
		organisms.ModalClear(),
		organisms.ToastOOB(infoToast("アプリを許可リストから外しました。")),
	)
}

// PickAppFile は POST /apps/pick-file。ネイティブのダイアログを開き、閉じるまで応答を返さない。
// 選ばれたパスを入れた実行ファイルの入力欄を 200 で返す（選択だけでは登録しない。操作は監査しない）。
// キャンセルは 204（差し替えない）。呼び出し元が応答を待たずに切断したら、ダイアログを閉じて何も返さない。
func (h *Handler) PickAppFile(c *gin.Context) {
	ctx := c.Request.Context()
	path, ok, err := h.d.Apps.PickExecutable(ctx)
	switch {
	case ctx.Err() != nil:
		c.Status(http.StatusNoContent)
	case err != nil:
		h.fail(c, err)
	case !ok:
		c.Status(http.StatusNoContent)
	default:
		formID := c.PostForm("form_id")
		if !formIDPattern.MatchString(formID) {
			formID = newAppFormID
		}
		h.render(c, http.StatusOK, organisms.AppExePathField(view.AppExePathField{FormID: formID, Value: path, PickURL: appPickURL}))
	}
}
