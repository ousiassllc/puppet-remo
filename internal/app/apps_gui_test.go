package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// auditSink は監査の書き込みを記録する（Repo の Audit を差し替える。ほかの表は本物の SQLite のまま）。
type auditSink struct {
	repository.Audit // Append 以外（閲覧・サムネイル・保守）は本物へ
	mu               sync.Mutex
	entries          []repository.AuditEntry
}

func (s *auditSink) Append(_ context.Context, e repository.AuditEntry) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return int64(len(s.entries)), nil
}

func (s *auditSink) all() []repository.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]repository.AuditEntry(nil), s.entries...)
}

type auditRepo struct {
	*repository.DB
	sink *auditSink
}

func newAuditRepo(r Repo, sink *auditSink) auditRepo {
	db := r.(*repository.DB)
	sink.Audit = db.Audit()
	return auditRepo{DB: db, sink: sink}
}

func (r auditRepo) Audit() repository.Audit { return r.sink }

const (
	guiExe  = `C:\Tools\notepad.exe`
	guiExe2 = `C:\Tools\wordpad.exe`
	guiArg  = "--sentinel-arg"
	guiDir  = `C:\Work\sentinel-dir`
)

type guiClient struct {
	t *testing.T
	h http.Handler
}

func (c guiClient) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	c.t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

// ホスト GUI の /apps は、結線した AppRegistry（本物の service・SQLite の許可リスト）で動く: 追加・編集・削除の結果が
// 許可リストに入り、actor_type = host・kind = settings で監査され、引数・作業ディレクトリはどの記録にも入らない（FR-AU06）。
func TestGUIAppsManageTheAllowListAndAuditAsHost(t *testing.T) {
	sink := &auditSink{}
	e := newEnv(t, func(c *Config) { c.Repo = newAuditRepo(c.Repo, sink) })
	e.launcher.AddExecutable(guiExe)
	e.launcher.AddExecutable(guiExe2)
	e.launcher.AddDir(guiDir)
	gui := guiClient{t: t, h: e.app.GUIHandler()}

	rec := gui.do(http.MethodPost, "/apps", url.Values{"label": {"メモ帳"}, "exe_path": {guiExe}, "args": {guiArg}, "working_dir": {guiDir}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /apps = %d\n%s", rec.Code, rec.Body)
	}
	apps, err := e.app.apps.List(t.Context())
	if err != nil || len(apps) != 1 {
		t.Fatalf("allow list = %+v, %v", apps, err)
	}
	id := apps[0].ID
	if apps[0].ExePath != guiExe || len(apps[0].Args) != 1 || apps[0].Args[0] != guiArg || apps[0].WorkingDir != guiDir {
		t.Errorf("stored = %+v", apps[0])
	}
	if body := rec.Body.String(); !strings.Contains(body, guiExe) || !strings.Contains(body, guiArg) {
		t.Errorf("the host GUI shows the registered path and arguments: %s", body)
	}

	// 存在しない実行ファイルは項目別のエラー（422）で、許可リストは変わらない。
	if rec := gui.do(http.MethodPost, "/apps", url.Values{"label": {"別"}, "exe_path": {`C:\Gone\gone.exe`}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `data-field-error="app-form-new-exe-path"`) {
		t.Errorf("missing file: %d\n%s", rec.Code, rec.Body)
	}
	// 表示名の重複（大文字小文字違いを含む）は 422。
	gui.do(http.MethodPost, "/apps", url.Values{"label": {"Notepad"}, "exe_path": {guiExe}})
	if rec := gui.do(http.MethodPost, "/apps", url.Values{"label": {"NOTEPAD"}, "exe_path": {guiExe2}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `data-field-error="app-form-new-label"`) {
		t.Errorf("duplicate label: %d\n%s", rec.Code, rec.Body)
	}

	// 編集しても id は変わらない。
	rec = gui.do(http.MethodPut, "/apps/"+id, url.Values{"label": {"メモ帳"}, "exe_path": {guiExe2}, "args": {guiArg + "-2"}, "working_dir": {guiDir}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /apps/{id} = %d\n%s", rec.Code, rec.Body)
	}
	got, err := e.app.apps.Get(t.Context(), id)
	if err != nil || got.ID != id || got.ExePath != guiExe2 {
		t.Errorf("after update = %+v, %v", got, err)
	}
	if rec := gui.do(http.MethodPut, "/apps/01JZ0000000000000000000000", url.Values{"label": {"x"}, "exe_path": {guiExe}}); rec.Code != http.StatusNotFound {
		t.Errorf("PUT unknown = %d, want 404", rec.Code)
	}

	// 確認モーダルは削除しない。DELETE で消える。
	if rec := gui.do(http.MethodGet, "/apps/"+id+"/delete/confirm", nil); rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d", rec.Code)
	}
	if _, err := e.app.apps.Get(t.Context(), id); err != nil {
		t.Fatalf("the confirmation deleted the app: %v", err)
	}
	if rec := gui.do(http.MethodDelete, "/apps/"+id, nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d\n%s", rec.Code, rec.Body)
	}
	if rec := gui.do(http.MethodDelete, "/apps/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE again = %d, want 404", rec.Code)
	}

	if err := e.app.audit.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, en := range sink.all() {
		if !strings.HasPrefix(en.Event, "settings.launchable_app.") {
			continue
		}
		events = append(events, en.Event)
		if en.ActorType != domain.ActorHost || en.Kind != "settings" || en.ClientID != "" || en.Result != "ok" {
			t.Errorf("%s: actor=%q kind=%q client=%q result=%q, want host / settings", en.Event, en.ActorType, en.Kind, en.ClientID, en.Result)
		}
		for _, secret := range []string{guiArg, guiDir} {
			if strings.Contains(string(en.DetailJSON), secret) {
				t.Errorf("%s leaks %q: %s", en.Event, secret, en.DetailJSON)
			}
		}
	}
	want := []string{"settings.launchable_app.create", "settings.launchable_app.create", "settings.launchable_app.update", "settings.launchable_app.delete"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Errorf("audit events = %v, want %v (rejected inputs and the picker are not audited)", events, want)
	}
}

// ファイル選択は FilePicker に届き、選ばれたパスが入力欄に入る。選択だけでは登録も監査もしない。キャンセルは 204。
func TestGUIPickFileUsesTheWiredFilePickerAndIsNotAudited(t *testing.T) {
	sink := &auditSink{}
	e := newEnv(t, func(c *Config) { c.Repo = newAuditRepo(c.Repo, sink) })
	gui := guiClient{t: t, h: e.app.GUIHandler()}

	pick := func(answer func()) *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- gui.do(http.MethodPost, "/apps/pick-file", url.Values{"form_id": {"app-form-new"}}) }()
		answer()
		return <-done
	}
	rec := pick(func() {
		if err := e.picker.Choose(t.Context(), guiExe); err != nil {
			t.Error(err)
		}
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="`+guiExe+`"`) {
		t.Errorf("select: %d\n%s", rec.Code, rec.Body)
	}
	rec = pick(func() {
		if err := e.picker.Dismiss(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if rec.Code != http.StatusNoContent {
		t.Errorf("cancel = %d, want 204", rec.Code)
	}
	if apps, _ := e.app.apps.List(t.Context()); len(apps) != 0 {
		t.Errorf("picking registered %+v", apps)
	}
	if err := e.app.audit.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.all()); n != 0 {
		t.Errorf("picking wrote %d audit entries, want none", n)
	}
}

// 許可リストの画面はホスト GUI だけにある: ページはサイドバーに出て、/poll には含まれない。
func TestGUIAppsPageAndPollIsolation(t *testing.T) {
	e := newEnv(t)
	e.launcher.AddExecutable(guiExe)
	if _, err := e.app.apps.Create(t.Context(), domain.AppInput{Label: "メモ帳", ExePath: guiExe, Args: []string{guiArg}}); err != nil {
		t.Fatal(err)
	}
	h := e.app.GUIHandler()
	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	if page := get("/apps"); !strings.Contains(page, guiExe) || !strings.Contains(page, `href="/apps"`) {
		t.Errorf("/apps lacks the registered app or the sidebar entry:\n%s", page)
	}
	if poll := get("/poll"); strings.Contains(poll, "メモ帳") || strings.Contains(poll, guiExe) || strings.Contains(poll, guiArg) {
		t.Errorf("/poll carries the allow list:\n%s", poll)
	}
}

// エージェントを結線するまでの GUI のタスク画面は、タスクが 1 件も無い状態（一覧は空、詳細・中断は 404）で動く。
func TestGUITasksAreEmptyUntilTheAgentIsWired(t *testing.T) {
	e := newEnv(t)
	gui := guiClient{t: t, h: e.app.GUIHandler()}
	if rec := gui.do(http.MethodGet, "/tasks", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-empty="task-list"`) {
		t.Errorf("GET /tasks = %d\n%s", rec.Code, rec.Body)
	}
	if rec := gui.do(http.MethodGet, "/tasks/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/nope = %d, want 404", rec.Code)
	}
	if rec := gui.do(http.MethodPost, "/tasks/nope/cancel", nil); rec.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/nope/cancel = %d, want 404", rec.Code)
	}
	if rec := gui.do(http.MethodGet, "/", nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "data-running-task") {
		t.Errorf("GET / = %d", rec.Code)
	}
}
