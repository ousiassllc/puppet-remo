package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// appPickLimit は「届くはずのものが届かない」ときにテストを失敗させる上限（待ち時間の仕様ではない）。
const appPickLimit = 5 * time.Second

// 登録のテストの固定値。引数・作業ディレクトリには、監査・エラーに漏れたら見つけられる目印を入れる。
const (
	appNotepad  = `C:\Tools\notepad.exe`
	appCalc     = `C:\Tools\calc.exe`
	appArgMark  = "--sentinel-arg-value"
	appWorkMark = `C:\Work\sentinel-dir`
)

// appEnv は ctlEnv に、appNotepad・appCalc の実行ファイルと appWorkMark のディレクトリがある状態を足したもの。
func newAppEnv(t *testing.T) *ctlEnv {
	t.Helper()
	e := newCtlEnv(t)
	e.launcher.AddExecutable(appNotepad)
	e.launcher.AddExecutable(appCalc)
	e.launcher.AddDir(appWorkMark)
	return e
}

func notepadInput() domain.AppInput {
	return domain.AppInput{Label: "メモ帳", ExePath: appNotepad, Args: []string{appArgMark}, WorkingDir: appWorkMark}
}

func fieldErrors(t *testing.T, err error) AppFieldErrors {
	t.Helper()
	var fe AppFieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v (%T), want AppFieldErrors", err, err)
	}
	return fe
}

func fieldNames(fe AppFieldErrors) []string {
	names := make([]string, 0, len(fe))
	for f := range fe {
		names = append(names, f)
	}
	slices.Sort(names)
	return names
}

func auditDetail(t *testing.T, e repository.AuditEntry) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(e.DetailJSON, &d); err != nil {
		t.Fatalf("detail %q: %v", e.DetailJSON, err)
	}
	return d
}

func TestNewAppRegistryRequiresDependencies(t *testing.T) {
	e := newCtlEnv(t)
	full := AppRegistryConfig{Apps: e.appStore, Denied: e.denyStore, Settings: e.settings, Launcher: e.launcher, Picker: e.picker, Clock: e.clock, IDs: &ctlIDs{}}
	cases := map[string]func(*AppRegistryConfig){
		"Apps":     func(c *AppRegistryConfig) { c.Apps = nil },
		"Denied":   func(c *AppRegistryConfig) { c.Denied = nil },
		"Settings": func(c *AppRegistryConfig) { c.Settings = nil },
		"Launcher": func(c *AppRegistryConfig) { c.Launcher = nil },
		"Picker":   func(c *AppRegistryConfig) { c.Picker = nil },
		"Clock":    func(c *AppRegistryConfig) { c.Clock = nil },
		"IDs":      func(c *AppRegistryConfig) { c.IDs = nil },
	}
	for name, clear := range cases {
		cfg := full
		clear(&cfg)
		if _, err := NewAppRegistry(cfg); err == nil {
			t.Errorf("NewAppRegistry without %s succeeded", name)
		}
	}
	if _, err := NewAppRegistry(full); err != nil {
		t.Errorf("Audit is optional: %v", err)
	}
}

func TestAppRegistryCreateStoresAndAudits(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	in := notepadInput()
	in.Label = "  メモ帳  "

	a, err := e.apps.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "lease-1" || a.Label != "メモ帳" || a.ExePath != appNotepad || !slices.Equal(a.Args, in.Args) || a.WorkingDir != appWorkMark {
		t.Errorf("created = %+v", a)
	}
	if !a.CreatedAt.Equal(e.clock.Now()) || !a.UpdatedAt.Equal(a.CreatedAt) {
		t.Errorf("times = %v / %v, want the clock's now", a.CreatedAt, a.UpdatedAt)
	}
	got, err := e.apps.Get(ctx, a.ID)
	if err != nil || got.ExePath != appNotepad || !slices.Equal(got.Args, in.Args) {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	entries := e.audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	en := entries[0]
	if en.Event != "settings.launchable_app.create" || en.Kind != "settings" || en.ActorType != domain.ActorHost || en.Result != "ok" {
		t.Errorf("entry = %+v", en)
	}
	d := auditDetail(t, en)
	want := map[string]any{"app_id": a.ID, "label": "メモ帳", "exe_path": appNotepad, "arg_count": float64(1)}
	if len(d) != len(want) {
		t.Errorf("detail = %v, want exactly %v", d, want)
	}
	for k, v := range want {
		if d[k] != v {
			t.Errorf("detail[%s] = %v, want %v", k, d[k], v)
		}
	}
	if s := string(en.DetailJSON); strings.Contains(s, appArgMark) || strings.Contains(s, appWorkMark) {
		t.Errorf("arguments or the working directory leaked into the audit detail: %s", s)
	}
}

func TestAppRegistryCreateDoesNotShareTheCallersArgs(t *testing.T) {
	e := newAppEnv(t)
	in := notepadInput()
	a, err := e.apps.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Args[0] = "changed"
	if a.Args[0] != appArgMark {
		t.Errorf("the stored args alias the caller's slice: %v", a.Args)
	}
}

func TestAppRegistryCreateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.AppInput)
		fields []string
	}{
		{"empty label", func(in *domain.AppInput) { in.Label = " " }, []string{"label"}},
		{"relative path", func(in *domain.AppInput) { in.ExePath = `notepad.exe` }, []string{"exe_path"}},
		{"UNC path", func(in *domain.AppInput) { in.ExePath = `\\host\share\a.exe` }, []string{"exe_path"}},
		{"not an exe", func(in *domain.AppInput) { in.ExePath = `C:\Tools\notepad.bat` }, []string{"exe_path"}},
		{"empty arg", func(in *domain.AppInput) { in.Args = []string{""} }, []string{"args"}},
		{"relative working dir", func(in *domain.AppInput) { in.WorkingDir = `work` }, []string{"working_dir"}},
		{"several", func(in *domain.AppInput) { in.Label = ""; in.ExePath = "sentinel-path"; in.Args = []string{""} }, []string{"args", "exe_path", "label"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAppEnv(t)
			in := notepadInput()
			tt.mutate(&in)
			_, err := e.apps.Create(context.Background(), in)
			fe := fieldErrors(t, err)
			if got := fieldNames(fe); !slices.Equal(got, tt.fields) {
				t.Errorf("fields = %v, want %v", got, tt.fields)
			}
			for _, msg := range fe {
				if strings.Contains(msg, appArgMark) || strings.Contains(msg, in.ExePath) && in.ExePath != "" {
					t.Errorf("message echoes the input: %q", msg)
				}
			}
			if strings.Contains(err.Error(), in.ExePath) && in.ExePath != "" {
				t.Errorf("Error() echoes the path: %q", err.Error())
			}
			if apps, _ := e.apps.List(context.Background()); len(apps) != 0 {
				t.Errorf("an invalid input was stored: %+v", apps)
			}
			if n := len(e.audit.Entries()); n != 0 {
				t.Errorf("%d audit entries for a rejected input", n)
			}
		})
	}
}

func TestAppRegistryCreateChecksExistenceThroughTheLauncher(t *testing.T) {
	t.Run("missing executable", func(t *testing.T) {
		e := newAppEnv(t)
		in := notepadInput()
		in.ExePath = `C:\Tools\gone.exe`
		fe := fieldErrors(t, mustFail(e.apps.Create(context.Background(), in)))
		// 実行ファイルが無いときは、作業ディレクトリの有無は確かめない（Inspect は区別できない）。
		if got := fieldNames(fe); !slices.Equal(got, []string{"exe_path"}) {
			t.Errorf("fields = %v", got)
		}
	})
	t.Run("missing working directory", func(t *testing.T) {
		e := newAppEnv(t)
		in := notepadInput()
		in.WorkingDir = `C:\Work\gone`
		fe := fieldErrors(t, mustFail(e.apps.Create(context.Background(), in)))
		if got := fieldNames(fe); !slices.Equal(got, []string{"working_dir"}) {
			t.Errorf("fields = %v", got)
		}
	})
	t.Run("malformed paths are not inspected", func(t *testing.T) {
		e := newAppEnv(t)
		in := notepadInput()
		in.ExePath, in.WorkingDir = "relative.exe", "relative"
		_ = mustFail(e.apps.Create(context.Background(), in))
		if calls := e.launcher.CallsTo("Inspect"); len(calls) != 0 {
			t.Errorf("Inspect was called with malformed paths: %+v", calls)
		}
	})
	t.Run("an inspection failure is not a field error", func(t *testing.T) {
		e := newAppEnv(t)
		boom := errors.New("inspect broke")
		e.launcher.SetError("Inspect", boom)
		err := mustFail(e.apps.Create(context.Background(), notepadInput()))
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the launcher's error", err)
		}
		var fe AppFieldErrors
		if errors.As(err, &fe) {
			t.Error("an infrastructure failure must not be shown as a form error")
		}
	})
}

func mustFail[T any](_ T, err error) error {
	if err == nil {
		panic("expected an error")
	}
	return err
}

func TestAppRegistryCreateLabelMustBeUnique(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	if _, err := e.apps.Create(ctx, notepadInput()); err != nil {
		t.Fatal(err)
	}
	dup := domain.AppInput{Label: "メモ帳", ExePath: appCalc}
	fe := fieldErrors(t, mustFail(e.apps.Create(ctx, dup)))
	if got := fieldNames(fe); !slices.Equal(got, []string{"label"}) {
		t.Errorf("fields = %v, want only label", got)
	}
	// ASCII の大文字小文字は区別しない。
	if _, err := e.apps.Create(ctx, domain.AppInput{Label: "Calc", ExePath: appCalc}); err != nil {
		t.Fatal(err)
	}
	fe = fieldErrors(t, mustFail(e.apps.Create(ctx, domain.AppInput{Label: "cALC", ExePath: appCalc})))
	if got := fieldNames(fe); !slices.Equal(got, []string{"label"}) {
		t.Errorf("fields = %v, want only label", got)
	}
	if n := len(e.audit.Entries()); n != 2 {
		t.Errorf("audit entries = %d, want the 2 successful creates only", n)
	}
}

func TestAppRegistryCreateStopsAtTheLimit(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	for i := range domain.MaxLaunchableApps {
		if _, err := e.apps.Create(ctx, domain.AppInput{Label: fmt.Sprintf("app-%02d", i), ExePath: appCalc}); err != nil {
			t.Fatalf("create #%d: %v", i, err)
		}
	}
	_, err := e.apps.Create(ctx, domain.AppInput{Label: "one-too-many", ExePath: appCalc})
	if domain.CodeOf(err) != domain.CodeConflict {
		t.Fatalf("err = %v, want conflict", err)
	}
	var fe AppFieldErrors
	if errors.As(err, &fe) {
		t.Error("the limit is a conflict, not a form error")
	}
	if n := len(e.audit.Entries()); n != domain.MaxLaunchableApps {
		t.Errorf("audit entries = %d, want %d", n, domain.MaxLaunchableApps)
	}
	// 1 件消せば、また登録できる。
	apps, _ := e.apps.List(ctx)
	if err := e.apps.Delete(ctx, apps[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.apps.Create(ctx, domain.AppInput{Label: "fits-now", ExePath: appCalc}); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

func TestAppRegistryListIsSortedByLabel(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	for _, l := range []string{"b-app", "A-app", "c-app"} {
		if _, err := e.apps.Create(ctx, domain.AppInput{Label: l, ExePath: appCalc}); err != nil {
			t.Fatal(err)
		}
	}
	apps, err := e.apps.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, a := range apps {
		labels = append(labels, a.Label)
	}
	if want := []string{"A-app", "b-app", "c-app"}; !slices.Equal(labels, want) {
		t.Errorf("labels = %v, want %v", labels, want)
	}
}

func TestAppRegistryGetUnknownIsNotFound(t *testing.T) {
	e := newAppEnv(t)
	if _, err := e.apps.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAppRegistryUpdate(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	orig, err := e.apps.Create(ctx, notepadInput())
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)

	upd, err := e.apps.Update(ctx, orig.ID, domain.AppInput{Label: "電卓", ExePath: appCalc, Args: []string{"/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if upd.ID != orig.ID || !upd.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("id/created_at changed: %+v -> %+v", orig, upd)
	}
	if !upd.UpdatedAt.Equal(e.clock.Now()) || !upd.UpdatedAt.After(orig.CreatedAt) {
		t.Errorf("updated_at = %v, want the clock's now", upd.UpdatedAt)
	}
	got, _ := e.apps.Get(ctx, orig.ID)
	if got.Label != "電卓" || got.ExePath != appCalc || got.WorkingDir != "" || !slices.Equal(got.Args, []string{"/x"}) {
		t.Errorf("stored = %+v", got)
	}

	entries := e.audit.Entries()
	en := entries[len(entries)-1]
	if en.Event != "settings.launchable_app.update" || en.ActorType != domain.ActorHost || en.Result != "ok" {
		t.Errorf("entry = %+v", en)
	}
	d := auditDetail(t, en)
	if d["app_id"] != orig.ID || d["label"] != "電卓" || d["exe_path"] != appCalc || d["arg_count"] != float64(1) {
		t.Errorf("detail = %v", d)
	}
	changed, _ := d["changed"].([]any)
	if fmt.Sprint(changed) != "[label exe_path args working_dir]" {
		t.Errorf("changed = %v, want all four fields in the fixed order", changed)
	}
	if s := string(en.DetailJSON); strings.Contains(s, appArgMark) || strings.Contains(s, appWorkMark) || strings.Contains(s, "/x") {
		t.Errorf("arguments or the working directory leaked into the audit detail: %s", s)
	}
}

func TestAppRegistryUpdateChangedListsOnlyWhatChanged(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	orig, err := e.apps.Create(ctx, notepadInput())
	if err != nil {
		t.Fatal(err)
	}
	in := notepadInput()
	in.Args = []string{"--other"}
	if _, err := e.apps.Update(ctx, orig.ID, in); err != nil {
		t.Fatal(err)
	}
	entries := e.audit.Entries()
	d := auditDetail(t, entries[len(entries)-1])
	if changed, _ := d["changed"].([]any); fmt.Sprint(changed) != "[args]" {
		t.Errorf("changed = %v, want [args]", changed)
	}
	// 同じ表示名のまま更新するのは重複ではない。
	if _, err := e.apps.Update(ctx, orig.ID, in); err != nil {
		t.Errorf("update with an unchanged label: %v", err)
	}
	entries = e.audit.Entries()
	d = auditDetail(t, entries[len(entries)-1])
	if changed, ok := d["changed"].([]any); !ok || len(changed) != 0 {
		t.Errorf("changed = %v, want an empty array", d["changed"])
	}
}

func TestAppRegistryUpdateFailuresChangeNothing(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	a, err := e.apps.Create(ctx, notepadInput())
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.apps.Create(ctx, domain.AppInput{Label: "電卓", ExePath: appCalc})
	if err != nil {
		t.Fatal(err)
	}
	before := len(e.audit.Entries())

	if _, err := e.apps.Update(ctx, "nope", notepadInput()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	bad := notepadInput()
	bad.ExePath = `C:\Tools\gone.exe`
	_ = fieldErrors(t, mustFail(e.apps.Update(ctx, a.ID, bad)))
	dup := notepadInput()
	dup.Label = "電卓"
	if got := fieldNames(fieldErrors(t, mustFail(e.apps.Update(ctx, a.ID, dup)))); !slices.Equal(got, []string{"label"}) {
		t.Errorf("duplicate label fields = %v", got)
	}

	got, _ := e.apps.Get(ctx, a.ID)
	if got.Label != "メモ帳" || got.ExePath != appNotepad || !got.UpdatedAt.Equal(a.UpdatedAt) {
		t.Errorf("a failed update changed the row: %+v", got)
	}
	if other, _ := e.apps.Get(ctx, b.ID); other.Label != "電卓" {
		t.Errorf("the other row changed: %+v", other)
	}
	if n := len(e.audit.Entries()); n != before {
		t.Errorf("failed updates were audited: %d new entries", n-before)
	}
}

func TestAppRegistryDelete(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	a, err := e.apps.Create(ctx, notepadInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.apps.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.apps.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v, want ErrNotFound", err)
	}
	entries := e.audit.Entries()
	en := entries[len(entries)-1]
	if en.Event != "settings.launchable_app.delete" || en.ActorType != domain.ActorHost || en.Result != "ok" {
		t.Errorf("entry = %+v", en)
	}
	d := auditDetail(t, en)
	if len(d) != 2 || d["app_id"] != a.ID || d["label"] != "メモ帳" {
		t.Errorf("detail = %v, want only app_id and label", d)
	}

	n := len(e.audit.Entries())
	if err := e.apps.Delete(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
	if len(e.audit.Entries()) != n {
		t.Error("a failed delete was audited")
	}
}

func TestAppRegistryPickExecutableDelegatesToTheFilePicker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), appPickLimit)
	defer cancel()

	t.Run("chosen", func(t *testing.T) {
		e := newAppEnv(t)
		type pick struct {
			path string
			ok   bool
			err  error
		}
		done := make(chan pick, 1)
		go func() {
			p, ok, err := e.apps.PickExecutable(ctx)
			done <- pick{p, ok, err}
		}()
		if err := e.picker.Choose(ctx, appNotepad); err != nil {
			t.Fatal(err)
		}
		if got := <-done; got.err != nil || !got.ok || got.path != appNotepad {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("cancelled by the user", func(t *testing.T) {
		e := newAppEnv(t)
		done := make(chan bool, 1)
		go func() {
			_, ok, err := e.apps.PickExecutable(ctx)
			done <- ok && err == nil
		}()
		if err := e.picker.Dismiss(ctx); err != nil {
			t.Fatal(err)
		}
		if <-done {
			t.Error("a dismissed dialog must report ok=false")
		}
	})
	t.Run("context cancellation closes the dialog", func(t *testing.T) {
		e := newAppEnv(t)
		pctx, pcancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			_, _, err := e.apps.PickExecutable(pctx)
			done <- err
		}()
		if err := e.picker.WaitOpen(ctx); err != nil {
			t.Fatal(err)
		}
		pcancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

func TestAppFieldErrorsErrorNamesFieldsOnly(t *testing.T) {
	fe := AppFieldErrors{"exe_path": "メッセージ", "label": "別のメッセージ"}
	if got, want := fe.Error(), "service: invalid launchable app: exe_path, label"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
