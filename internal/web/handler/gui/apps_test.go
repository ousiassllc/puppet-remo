package gui_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

const (
	notepad   = `C:\Windows\notepad.exe`
	calc      = `C:\Windows\System32\calc.exe`
	workDir   = `C:\Work\sentinel-dir`
	argSecret = "--sentinel-arg"
)

// pickWait は「届くはずのものが届かない」ときにテストを失敗させる上限（待ち時間の仕様ではない）。
const pickWait = 5 * time.Second

func seedApp(f *guitest.Fakes, id, label, exe string, args ...string) {
	f.Apps.Rows = append(f.Apps.Rows, domain.LaunchableApp{ID: id, Label: label, ExePath: exe, Args: args})
}

func appForm(label, exe, args, dir string) url.Values {
	return url.Values{"label": {label}, "exe_path": {exe}, "args": {args}, "working_dir": {dir}}
}

func TestAppsPageListsAppsWithPathsArgsAndNotice(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Rows = []domain.LaunchableApp{
			{ID: "app1", Label: "メモ帳", ExePath: notepad, Args: []string{argSecret, "two words"}, WorkingDir: workDir},
			{ID: "app2", Label: "電卓", ExePath: calc},
		}
	})
	res := r.get("/apps")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", `aria-current="page"`, `href="/apps"`, "起動許可アプリ",
		`id="app-list"`, `data-app="app1"`, "メモ帳", notepad, argSecret, "two words", workDir,
		`data-app="app2"`, "電卓", calc, "実行ファイルのあるディレクトリ", `data-app-count="2"`, "登録 2 / 50 件",
		`hx-get="/apps/app1/edit"`, `hx-get="/apps/app1/delete/confirm"`,
		// 追加フォーム（常時表示の注意書き・ファイル選択・引数は textarea）
		`id="app-form-new"`, `hx-post="/apps"`, `hx-target="#app-list"`, `hx-target-422="this"`, ` novalidate`,
		`data-notice="interpreter"`, "cmd.exe", "powershell.exe", "任意のコード",
		`name="exe_path"`, `hx-post="/apps/pick-file"`, `name="label"`, `<textarea`, `name="args"`, `name="working_dir"`)
	lacks(t, res, `data-empty="apps"`)
}

func TestAppsPageEmpty(t *testing.T) {
	res := newRig(t).get("/apps")
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="apps"`, "登録 0 / 50 件", `data-notice="interpreter"`)
	lacks(t, res, "data-app=")
}

func TestAppsFragmentsFollowHXRequest(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedApp(f, "app1", "メモ帳", notepad) })

	// /apps: 部分更新は一覧だけ（編集のキャンセルが使う）。boosted は #main の中身にサイドバーの強調を添える。
	frag := r.get("/apps", hx...)
	status(t, frag, http.StatusOK)
	contains(t, frag, `id="app-list"`, "メモ帳")
	lacks(t, frag, "<html", `id="app-form-new"`)
	boosted := r.get("/apps", append([]string{"HX-Boosted", "true"}, hx...)...)
	contains(t, boosted, "<title>起動許可アプリ - puppet-remo</title>", `id="sidebar-nav"`, `hx-swap-oob="true"`, `id="app-list"`, `id="app-form-new"`)
	lacks(t, boosted, "<html")

	// /apps/new: 部分更新は追加フォームだけ。それ以外はページ。
	newFrag := r.get("/apps/new", hx...)
	status(t, newFrag, http.StatusOK)
	contains(t, newFrag, `id="app-form-new"`, `data-app-form="new"`, `data-notice="interpreter"`)
	lacks(t, newFrag, "<html", `id="app-list"`)
	contains(t, r.get("/apps/new"), "<html", `id="app-list"`, `id="app-form-new"`)

	// /apps/{id}/edit: 部分更新は編集フォームだけ（現在の値入り。引数は 1 行 1 引数）。
	r.f.Apps.Rows[0].Args = []string{"a", "b c"}
	r.f.Apps.Rows[0].WorkingDir = workDir
	edit := r.get("/apps/app1/edit", hx...)
	status(t, edit, http.StatusOK)
	contains(t, edit, `id="app-form-app1"`, `data-app-form="edit"`, `hx-put="/apps/app1"`, `hx-target="this"`, `hx-target-422="this"`,
		`value="メモ帳"`, `value="`+notepad+`"`, "a\nb c</textarea>", `value="`+workDir+`"`, `data-notice="interpreter"`, `hx-get="/apps"`)
	lacks(t, edit, "<html", `id="app-list"`)
	// フルページでは一覧の中の該当の行が編集フォームになる。
	full := r.get("/apps/app1/edit")
	status(t, full, http.StatusOK)
	contains(t, full, "<html", `id="app-form-app1"`, `id="app-form-new"`)
	lacks(t, full, `id="app-app1"`)
}

func TestAppEditUnknownIs404(t *testing.T) {
	r := newRig(t)
	for _, hdr := range [][]string{nil, hx} {
		res := r.get("/apps/nope/edit", hdr...)
		status(t, res, http.StatusNotFound)
		contains(t, res, `data-error-code="not_found"`)
	}
}

func TestCreateAppParsesFormAndReturnsListWithFreshForm(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedApp(f, "app0", "電卓", calc) })
	form := appForm("  メモ帳 ", "  "+notepad+"  ", "-a\r\n\r\n two words\n", " "+workDir)
	r.f.Apps.Missing = map[string]bool{}
	res := r.post("/apps", form, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="app-list"`, "メモ帳", notepad, "電卓", `data-app-count="2"`,
		// 追加フォームは空に戻す（OOB）。トーストを添える。
		`id="app-form-new"`, `hx-swap-oob="true"`, "アプリを追加しました。", `hx-swap-oob="beforeend:#toast-root"`)
	lacks(t, res, "<html", "data-field-error")
	if len(r.f.Apps.Created) != 1 {
		t.Fatalf("created = %+v, want 1", r.f.Apps.Created)
	}
	got := r.f.Apps.Created[0]
	// 空行は捨て、空白だけの行は残す。パスの前後の空白は除く。
	if got.Label != "メモ帳" || got.ExePath != notepad || !slices.Equal(got.Args, []string{"-a", " two words"}) || got.WorkingDir != workDir {
		t.Errorf("created = %+v", got)
	}
}

func TestCreateAppValidationErrorsAre422WithValuesAndFieldMessages(t *testing.T) {
	cases := []struct {
		name  string
		form  url.Values
		field string // data-field-error の値（フォーム id + 項目）
	}{
		{"duplicate label ignoring case", appForm("NOTEPAD", calc, "", ""), "app-form-new-label"},
		{"relative path", appForm("x", `notepad.exe`, "", ""), "app-form-new-exe-path"},
		{"UNC path", appForm("x", `\\host\share\a.exe`, "", ""), "app-form-new-exe-path"},
		{"not exe", appForm("x", `C:\Windows\a.bat`, "", ""), "app-form-new-exe-path"},
		{"dotdot", appForm("x", `C:\Windows\..\a.exe`, "", ""), "app-form-new-exe-path"},
		{"missing file", appForm("x", `C:\Gone\gone.exe`, "", ""), "app-form-new-exe-path"},
		{"too many args", appForm("x", calc, strings.Repeat("a\n", 33), ""), "app-form-new-args"},
		{"relative working dir", appForm("x", calc, "", `work`), "app-form-new-working-dir"},
		{"empty label", appForm("   ", calc, "", ""), "app-form-new-label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) {
				seedApp(f, "app0", "notepad", notepad)
				f.Apps.Missing = map[string]bool{`C:\Gone\gone.exe`: true}
			})
			res := r.post("/apps", tc.form, hx...)
			status(t, res, http.StatusUnprocessableEntity)
			// フォーム自身を、入力値と項目別のメッセージ付きで再描画する。一覧は返さない。
			contains(t, res, `id="app-form-new"`, `data-field-error="`+tc.field+`"`, `role="alert"`)
			lacks(t, res, `id="app-list"`, "<html")
			if len(r.f.Apps.Created) != 0 || len(r.f.Apps.Rows) != 1 {
				t.Errorf("nothing may be created: %+v", r.f.Apps.Rows)
			}
			// 入力値を残す（パス・引数は HTML エスケープ後の形で出る）。
			if v := strings.TrimSpace(tc.form.Get("label")); v != "" {
				contains(t, res, `value="`+tc.form.Get("label")+`"`)
			}
			if v := tc.form.Get("exe_path"); !strings.Contains(v, `\\`) { // UNC は下のテストで見る
				contains(t, res, `value="`+strings.ReplaceAll(v, `&`, "&amp;")+`"`)
			}
		})
	}
}

func TestCreateAppValidationErrorKeepsArgsAndEveryFieldMessage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedApp(f, "app0", "メモ帳", notepad) })
	res := r.post("/apps", appForm("めも帳", `relative.exe`, "one\ntwo", "rel"), hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="app-form-new-exe-path"`, `data-field-error="app-form-new-working-dir"`,
		"one\ntwo</textarea>", `value="relative.exe"`, `value="rel"`, `value="めも帳"`)
	lacks(t, res, `data-field-error="app-form-new-label"`, `data-field-error="app-form-new-args"`)

	// 表示名の重複のメッセージは、一覧に既にある行の表示名とは別の入力のときだけ出る。
	res = r.post("/apps", appForm("メモ帳", calc, "", ""), hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="app-form-new-label"`, "同じ表示名")
}

func TestCreateAppConflictWhenFull(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		for i := range domain.MaxLaunchableApps {
			seedApp(f, fmt.Sprintf("app%02d", i), fmt.Sprintf("app-%02d", i), notepad)
		}
	})
	res := r.post("/apps", appForm("one more", calc, "", ""), hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "50 件")
	if got := res.Header.Get("HX-Retarget"); got != "#alert-root" {
		t.Errorf("HX-Retarget = %q", got)
	}
	if len(r.f.Apps.Rows) != domain.MaxLaunchableApps {
		t.Errorf("rows = %d", len(r.f.Apps.Rows))
	}
}

func TestUpdateAppKeepsIDAndReturnsCard(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedApp(f, "app1", "メモ帳", notepad)
		seedApp(f, "app2", "電卓", calc)
	})
	res := r.do(http.MethodPut, "/apps/app1", appForm("メモ帳 (新)", `C:\Tools\other.exe`, argSecret, workDir), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="app-app1"`, `data-app="app1"`, "メモ帳 (新)", `C:\Tools\other.exe`, argSecret, workDir, "アプリを更新しました。")
	lacks(t, res, "<html", `id="app-list"`, `data-app="app2"`)
	row, _ := r.f.Apps.Get(context.Background(), "app1")
	if row.ID != "app1" || row.ExePath != `C:\Tools\other.exe` || len(r.f.Apps.Rows) != 2 {
		t.Errorf("row = %+v", row)
	}
	// 自分自身の表示名のままの保存は重複にならない。
	res = r.do(http.MethodPut, "/apps/app1", appForm("メモ帳 (新)", notepad, "", ""), hx...)
	status(t, res, http.StatusOK)
}

func TestUpdateAppValidationAndNotFound(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedApp(f, "app1", "メモ帳", notepad)
		seedApp(f, "app2", "電卓", calc)
	})
	res := r.do(http.MethodPut, "/apps/app1", appForm("電卓", "bad.exe", "", ""), hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `id="app-form-app1"`, `data-app-form="edit"`, `hx-put="/apps/app1"`,
		`data-field-error="app-form-app1-label"`, `data-field-error="app-form-app1-exe-path"`, `value="電卓"`, `value="bad.exe"`)
	lacks(t, res, `id="app-app1"`)
	if row, _ := r.f.Apps.Get(context.Background(), "app1"); row.Label != "メモ帳" || row.ExePath != notepad {
		t.Errorf("a rejected update must not change the row: %+v", row)
	}

	res = r.do(http.MethodPut, "/apps/nope", appForm("x", calc, "", ""), hx...)
	status(t, res, http.StatusNotFound)
	contains(t, res, `data-error-code="not_found"`)
}

func TestConfirmDeleteAppModal(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedApp(f, "app1", "メモ帳", notepad) })
	res := r.get("/apps/app1/delete/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "メモ帳", `hx-delete="/apps/app1"`, `hx-target="#app-list"`, `hx-swap="outerHTML"`, "起動しているプロセスには影響しません")
	if len(r.f.Apps.Deleted) != 0 {
		t.Error("opening the confirmation must not delete")
	}
	res = r.get("/apps/nope/delete/confirm", hx...)
	status(t, res, http.StatusNotFound)
	contains(t, res, `data-error-code="not_found"`)
}

// 読めない行（Corrupt）があっても /apps は 200 で、行に警告を出し、ホスト所有者はその行を削除できる。
func TestAppsPageShowsCorruptRowAndAllowsDeleting(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Rows = []domain.LaunchableApp{
			{ID: "bad", Label: "壊れた行", ExePath: notepad, Args: []string{}, Corrupt: true},
			{ID: "ok", Label: "電卓", ExePath: calc},
		}
	})
	res := r.get("/apps")
	status(t, res, http.StatusOK)
	contains(t, res, `data-app="bad"`, "壊れた行", `data-app-corrupt`, `data-app-unlaunchable`, `hx-get="/apps/bad/delete/confirm"`, `data-app="ok"`)
	if n := strings.Count(res.Body, "data-app-corrupt"); n != 1 {
		t.Errorf("corrupt warnings = %d, want 1", n)
	}
	if n := strings.Count(res.Body, "data-app-unlaunchable"); n != 1 {
		t.Errorf("unlaunchable warnings = %d, want 1", n)
	}

	res = r.do(http.MethodDelete, "/apps/bad", nil, hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, `data-app="bad"`, "data-app-corrupt")
	if !slices.Equal(r.f.Apps.Deleted, []string{"bad"}) {
		t.Errorf("deleted = %v", r.f.Apps.Deleted)
	}
}

// 読めない行の編集フォームには、引数を入れ直す案内を出す（開いたときも、検証に失敗して再描画したときも）。
func TestAppEditFormOfACorruptRowAsksToReenterArgs(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Rows = []domain.LaunchableApp{
			{ID: "bad", Label: "壊れた行", ExePath: notepad, Args: []string{}, Corrupt: true},
			{ID: "ok", Label: "電卓", ExePath: calc},
		}
	})
	const notice = "保存された設定を読めませんでした。引数を入れ直してください"
	res := r.get("/apps/bad/edit", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-app-form="edit"`, `data-app-form-corrupt`, notice)

	res = r.do(http.MethodPut, "/apps/bad", appForm("壊れた行", "bad.exe", "", ""), hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-app-form-corrupt`, notice)

	// 壊れていない行の編集フォームには出さない。
	res = r.get("/apps/ok/edit", hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, "data-app-form-corrupt", notice)
}

// 手で書き換えられて起動できない行（Corrupt でなくても CheckLaunchTarget が拒否する）にも警告を出す。起動できる行には出さない。
func TestAppsPageWarnsAboutRowsThatCannotLaunch(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Rows = []domain.LaunchableApp{
			{ID: "rel", Label: "相対パス", ExePath: "notepad.exe"},
			{ID: "ok", Label: "電卓", ExePath: calc},
		}
	})
	res := r.get("/apps")
	status(t, res, http.StatusOK)
	contains(t, res, `data-app="rel"`, `data-app-unlaunchable`)
	if n := strings.Count(res.Body, "data-app-unlaunchable"); n != 1 {
		t.Errorf("unlaunchable warnings = %d, want 1", n)
	}
	lacks(t, res, "data-app-corrupt")
}

func TestDeleteApp(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedApp(f, "app1", "メモ帳", notepad)
		seedApp(f, "app2", "電卓", calc)
	})
	res := r.do(http.MethodDelete, "/apps/app1", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="app-list"`, `data-app="app2"`, `data-app-count="1"`, `id="modal-root" hx-swap-oob="true"`, "許可リストから外しました。")
	lacks(t, res, `data-app="app1"`, notepad, "<html")
	if !slices.Equal(r.f.Apps.Deleted, []string{"app1"}) {
		t.Errorf("deleted = %v", r.f.Apps.Deleted)
	}

	res = r.do(http.MethodDelete, "/apps/app1", nil, hx...)
	status(t, res, http.StatusNotFound)
	contains(t, res, `data-error-code="not_found"`)
}

func TestWriteFailureIsAnInternalErrorAlertWithoutDetails(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedApp(f, "app1", "メモ帳", notepad)
		f.Apps.WriteErr = errors.New("db: " + notepad + " " + argSecret)
	})
	for _, res := range []resp{
		r.post("/apps", appForm("x", calc, argSecret, ""), hx...),
		r.do(http.MethodPut, "/apps/app1", appForm("x", calc, argSecret, ""), hx...),
		r.do(http.MethodDelete, "/apps/app1", nil, hx...),
	} {
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, notepad, argSecret)
	}
}

func TestAppsWorkWhileKillSwitchIsActive(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.KillSwitch.Snapshot = service.KillSwitchSnapshot{Active: true, Source: "test"}
	})
	status(t, r.post("/apps", appForm("メモ帳", notepad, "", ""), hx...), http.StatusOK)
	status(t, r.do(http.MethodPut, "/apps/app001", appForm("メモ帳2", notepad, "", ""), hx...), http.StatusOK)
	status(t, r.do(http.MethodDelete, "/apps/app001", nil, hx...), http.StatusOK)
	status(t, r.get("/apps"), http.StatusOK)
}

func TestAppValuesAreEscaped(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Rows = []domain.LaunchableApp{{ID: "app1", Label: `<img src=x onerror=alert(1)>`, ExePath: `C:\a&b\"x".exe`, Args: []string{`<script>alert(1)</script>`}, WorkingDir: `C:\<w>`}}
	})
	for _, res := range []resp{r.get("/apps"), r.get("/apps/app1/edit", hx...), r.get("/apps/app1/delete/confirm", hx...)} {
		status(t, res, http.StatusOK)
		lacks(t, res, "<img src=x", "<script>alert", `<w>`)
	}
}

func TestPickFileSelectedReturnsExePathFieldWithoutRegistering(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.PickFunc = func(context.Context) (string, bool, error) { return notepad, true, nil }
	})
	res := r.post("/apps/pick-file", url.Values{"form_id": {"app-form-app1"}, "exe_path": {"stale"}}, hx...)
	status(t, res, http.StatusOK)
	// 入力欄だけのフラグメント。手で編集できる（readonly ではない）。選ばれたパスを入れる。
	contains(t, res, `data-field="exe_path"`, `id="app-form-app1-exe-path"`, `name="exe_path"`, `value="`+notepad+`"`, `hx-post="/apps/pick-file"`)
	lacks(t, res, "<html", "<form", " readonly", "stale", `id="app-list"`)
	if r.f.Apps.Picks != 1 || len(r.f.Apps.Created) != 0 || len(r.f.Apps.Rows) != 0 {
		t.Errorf("picking must not register: picks=%d created=%v", r.f.Apps.Picks, r.f.Apps.Created)
	}
}

func TestPickFileFormIDIsRestrictedToSafeIDs(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.PickFunc = func(context.Context) (string, bool, error) { return notepad, true, nil }
	})
	for _, bad := range []string{"", `x"><script>`, "other-id", "app-form-" + strings.Repeat("a", 65)} {
		res := r.post("/apps/pick-file", url.Values{"form_id": {bad}}, hx...)
		status(t, res, http.StatusOK)
		contains(t, res, `id="app-form-new-exe-path"`)
		lacks(t, res, "<script>")
	}
}

func TestPickFileCancelIs204AndLeavesTheFieldAlone(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.PickFunc = func(context.Context) (string, bool, error) { return "", false, nil }
	})
	res := r.post("/apps/pick-file", url.Values{"form_id": {"app-form-new"}}, hx...)
	status(t, res, http.StatusNoContent)
	if res.Body != "" {
		t.Errorf("a cancelled pick has no body: %q", res.Body)
	}
}

func TestPickFileErrorIsAnInternalErrorAlertWithoutThePath(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.PickFunc = func(context.Context) (string, bool, error) { return "", false, errors.New("dialog failed " + notepad) }
	})
	res := r.post("/apps/pick-file", nil, hx...)
	status(t, res, http.StatusInternalServerError)
	contains(t, res, `data-error-code="internal"`)
	lacks(t, res, notepad)
}

// ダイアログが閉じるまで応答を返さない。閉じたあとに、選ばれたパスで応答する。
func TestPickFileWaitsForTheDialogToClose(t *testing.T) {
	opened, closeDialog := make(chan struct{}), make(chan struct{})
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.PickFunc = func(context.Context) (string, bool, error) {
			close(opened)
			<-closeDialog
			return calc, true, nil
		}
	})
	done := make(chan resp, 1)
	go func() { done <- r.post("/apps/pick-file", url.Values{"form_id": {"app-form-new"}}, hx...) }()

	waitFor(t, opened, "the dialog to open")
	select {
	case res := <-done:
		t.Fatalf("responded while the dialog was open: %d", res.StatusCode)
	default:
	}
	close(closeDialog)
	select {
	case res := <-done:
		status(t, res, http.StatusOK)
		contains(t, res, `value="`+calc+`"`)
	case <-time.After(pickWait):
		t.Fatal("no response after the dialog closed")
	}
}

// 呼び出し元が応答を待たずに切断したら、ダイアログを閉じて（ctx の取り消し）、何も返さない。
func TestPickFileClientDisconnectClosesTheDialog(t *testing.T) {
	opened := make(chan struct{})
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.PickFunc = func(ctx context.Context) (string, bool, error) {
			close(opened)
			<-ctx.Done()
			return "", false, ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/apps/pick-file", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { r.h.ServeHTTP(rec, req); close(done) }()
	waitFor(t, opened, "the dialog to open")
	cancel()
	waitFor(t, done, "the handler to return")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("got %d %q, want an empty 204", rec.Code, rec.Body.String())
	}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(pickWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// 許可リストは操作の応答のフラグメントで更新する。/poll の対象に含めない。
func TestPollDoesNotCarryTheAppList(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedApp(f, "app1", "メモ帳", notepad, argSecret) })
	for _, q := range []url.Values{nil, {"rev_killswitch": {"stale"}, "rev_requests": {"stale"}, "rev_apps": {"stale"}}} {
		res := r.get(pollURL(q), hx...)
		status(t, res, http.StatusOK)
		lacks(t, res, "app-list", "メモ帳", notepad, argSecret, "data-app")
	}
	// 一覧の画面も、/poll へ送り返す版の入力を持たない。
	lacks(t, r.get("/apps"), `name="rev_`)
}

func TestAppsRoutesRequireDependencies(t *testing.T) {
	d := guitest.New().Deps()
	d.Apps = nil
	if _, err := newHandler(d); err == nil {
		t.Error("gui.New without Apps must fail")
	}
}
