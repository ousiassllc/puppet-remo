package service

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは、設定変更の fail-closed（#174・#182）のテスト。どの項目も、
//   - 監査の行を書けなければ、変更は残らず audit_unavailable（subject 付き。restored は付かない）になる。
//   - 監査に書けず、元に戻す書き込みも失敗したら、audit_unavailable の restored = false で伝える。

// failingAudit は RecordSync を、breakStores を呼んでから失敗させる SyncAuditRecorder（Record は ctlAudit に任せる）。
// breakStores は、変更を戻す書き込みを失敗させる細工（nil なら戻せる）。
type failingAudit struct {
	*ctlAudit
	breakStores func()
}

func (a *failingAudit) RecordSync(context.Context, repository.AuditEntry) error {
	if a.breakStores != nil {
		a.breakStores()
	}
	return errors.New("audit disk full")
}

var errStoreLocked = errors.New("store locked")

// wantAuditFailure は err が subject の audit_unavailable で、restored の値が want であることを確かめる。
func wantAuditFailure(t *testing.T, err error, subject string, wantRestored bool) {
	t.Helper()
	if !errors.Is(err, domain.ErrAuditUnavailable) {
		t.Fatalf("err = %v, want audit_unavailable", err)
	}
	got, restored, ok := AuditChangeFailure(err)
	if !ok || got != subject || restored != wantRestored {
		t.Errorf("AuditChangeFailure = (%q, restored=%v, ok=%v), want (%q, restored=%v, ok)", got, restored, ok, subject, wantRestored)
	}
}

func snapshotSettings(s *ctlSettings) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = string(v)
	}
	return out
}

func wantSettingsUnchanged(t *testing.T, s *ctlSettings, before map[string]string) {
	t.Helper()
	if after := snapshotSettings(s); !maps.Equal(after, before) {
		t.Errorf("settings changed after a refused change:\n before %v\n after  %v", before, after)
	}
}

// ---- 設定画面（settings.update） ----

func TestHostSettingsSaveIsRolledBackWhenTheAuditCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, true)
	// 一部のキーだけ保存済み（残りは行が無い）にしておく。戻すとき、行の無かったキーは行ごと消える。
	first := defaultHostSettings
	first.Port, first.Autostart = 9001, true
	if _, err := e.svc.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	delete(e.store.values, "viewer.default_fps")
	before := snapshotSettings(e.store)
	e.changes = nil

	failing := &failingAudit{ctlAudit: e.audit}
	svc, err := NewHostSettings(HostSettingsConfig{
		Settings: e.store, Autostart: e.auto, Audit: failing, Clock: newFakeClock(),
		OnChange: func(old, updated HostSettings) { e.changes = append(e.changes, [2]HostSettings{old, updated}) },
	})
	if err != nil {
		t.Fatal(err)
	}
	next := first
	next.Port, next.ViewerDefaultFPS, next.AuditRecordText, next.Autostart = 9100, 20, true, false
	_, err = svc.Save(ctx, next)

	wantAuditFailure(t, err, AuditSubjectHostSettings, true)
	wantSettingsUnchanged(t, e.store, before)
	if !e.auto.enabled {
		t.Error("autostart was not restored")
	}
	if len(e.changes) != 0 {
		t.Errorf("OnChange ran for a refused change: %+v", e.changes)
	}
	if got, err := svc.Load(ctx); err != nil || got != first {
		t.Errorf("Load = %+v, %v; want the previous settings %+v", got, err, first)
	}
}

func TestHostSettingsSaveReportsWhenTheRollbackFailsToo(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, true)
	failing := &failingAudit{ctlAudit: e.audit, breakStores: func() { e.store.setErrAfter(0, errStoreLocked); e.auto.setErr = errStoreLocked }}
	svc, err := NewHostSettings(HostSettingsConfig{
		Settings: e.store, Autostart: e.auto, Audit: failing, Clock: newFakeClock(),
		OnChange: func(old, updated HostSettings) { e.changes = append(e.changes, [2]HostSettings{old, updated}) },
	})
	if err != nil {
		t.Fatal(err)
	}
	next := defaultHostSettings
	next.Port = 9100
	_, err = svc.Save(ctx, next)

	wantAuditFailure(t, err, AuditSubjectHostSettings, false)
	if len(e.changes) != 0 {
		t.Errorf("OnChange ran for a refused change: %+v", e.changes)
	}
}

// 途中の項目の書き込みに失敗して、そこまでの分を監査に書けなかったときも、書いた分は残さない。
func TestHostSettingsPartialSaveWithAuditFailureIsRolledBack(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, false)
	before := snapshotSettings(e.store)
	failing := &failingAudit{ctlAudit: e.audit, breakStores: func() { e.store.setErrAfter(-1, nil) }}
	svc, err := NewHostSettings(HostSettingsConfig{Settings: e.store, Audit: failing, Clock: newFakeClock()})
	if err != nil {
		t.Fatal(err)
	}
	e.store.setErrAfter(3, errStoreLocked) // 3 項目目まで書けて、4 項目目の書き込みが失敗する
	next := defaultHostSettings
	next.Port, next.KillSwitchHotkey, next.ScreenshotMaxDimension, next.LeaseIdleTimeoutSec = 9100, "Ctrl+Alt+Q", 2048, 100
	_, err = svc.Save(ctx, next)
	wantAuditFailure(t, err, AuditSubjectHostSettings, true)
	wantSettingsUnchanged(t, e.store, before)
}

// ---- エージェント・高速パス・API キー ----

func newFailingAgentSettings(t *testing.T, e *agentSettingsEnv, breakStores func()) *AgentSettingsService {
	t.Helper()
	s, err := NewAgentSettings(AgentSettingsConfig{
		Settings: e.set, Secrets: e.secrets, Audit: &failingAudit{ctlAudit: e.audit, breakStores: breakStores}, Clock: e.clock,
		Cancel: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func breakAgentStores(e *agentSettingsEnv) func() {
	return func() {
		e.set.setErrAfter(0, errStoreLocked)
		e.secrets.SetError("Set", errStoreLocked)
		e.secrets.SetError("Delete", errStoreLocked)
	}
}

func TestAgentSettingsChangesAreRolledBackWhenTheAuditCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		subject string
		setup   func(*testing.T, *agentSettingsEnv)
		change  func(*AgentSettingsService) error
	}{
		{"agent: enable with consent", AuditSubjectAgentSettings,
			func(t *testing.T, e *agentSettingsEnv) { _ = e.s.SetOpenAIKey(ctx, canaryKey) },
			func(s *AgentSettingsService) error {
				in := validAgentInput()
				in.Enabled, in.Consent = true, true
				return s.SaveAgent(ctx, in)
			}},
		{"agent: disable", AuditSubjectAgentSettings,
			func(t *testing.T, e *agentSettingsEnv) { e.enableAgent(t) },
			func(s *AgentSettingsService) error {
				in := validAgentInput()
				in.MaxSteps = 7
				return s.SaveAgent(ctx, in)
			}},
		{"fastpath: enable with consent", AuditSubjectFastpath,
			func(t *testing.T, e *agentSettingsEnv) { _ = e.s.SetTypeSafeKey(ctx, canaryKey) },
			func(s *AgentSettingsService) error {
				in := validFastpathInput()
				in.Enabled, in.Consent = true, true
				return s.SaveFastpath(ctx, in)
			}},
		{"fastpath: disable", AuditSubjectFastpath,
			func(t *testing.T, e *agentSettingsEnv) { e.enableFastpath(t) },
			func(s *AgentSettingsService) error { return s.SaveFastpath(ctx, validFastpathInput()) }},
		{"OpenAI key: register", AuditSubjectAPIKey, func(*testing.T, *agentSettingsEnv) {},
			func(s *AgentSettingsService) error { return s.SetOpenAIKey(ctx, canaryKey) }},
		{"OpenAI key: replace", AuditSubjectAPIKey,
			func(t *testing.T, e *agentSettingsEnv) { _ = e.s.SetOpenAIKey(ctx, "sk-old-key-0000000000") },
			func(s *AgentSettingsService) error { return s.SetOpenAIKey(ctx, canaryKey) }},
		{"OpenAI key: delete", AuditSubjectAPIKey,
			func(t *testing.T, e *agentSettingsEnv) { e.enableAgent(t) },
			func(s *AgentSettingsService) error { return s.DeleteOpenAIKey(ctx) }},
		{"TypeSafe key: register", AuditSubjectAPIKey, func(*testing.T, *agentSettingsEnv) {},
			func(s *AgentSettingsService) error { return s.SetTypeSafeKey(ctx, canaryKey) }},
		{"TypeSafe key: replace", AuditSubjectAPIKey,
			func(t *testing.T, e *agentSettingsEnv) { _ = e.s.SetTypeSafeKey(ctx, "ts-old-key-0000000000") },
			func(s *AgentSettingsService) error { return s.SetTypeSafeKey(ctx, canaryKey) }},
		{"TypeSafe key: delete", AuditSubjectAPIKey,
			func(t *testing.T, e *agentSettingsEnv) { e.enableFastpath(t) },
			func(s *AgentSettingsService) error { return s.DeleteTypeSafeKey(ctx) }},
	}
	secretValues := func(e *agentSettingsEnv) map[string]string {
		out := map[string]string{}
		for _, name := range []string{platform.SecretOpenAI, platform.SecretTypeSafe} {
			if v, err := e.secrets.Get(name); err == nil {
				out[name] = v
			}
		}
		return out
	}
	for _, tc := range tests {
		t.Run(tc.name+"/restored", func(t *testing.T) {
			e := newAgentSettingsEnv(t)
			tc.setup(t, e)
			beforeSettings, beforeSecrets := snapshotSettings(e.set), secretValues(e)
			e.audit.reset()

			err := tc.change(newFailingAgentSettings(t, e, nil))

			wantAuditFailure(t, err, tc.subject, true)
			wantSettingsUnchanged(t, e.set, beforeSettings)
			if after := secretValues(e); !maps.Equal(after, beforeSecrets) {
				t.Errorf("secrets changed after a refused change: before %d, after %d entries", len(beforeSecrets), len(after))
			}
			if got := e.audit.All(); len(got) != 0 {
				t.Errorf("an entry was recorded for a refused change: %+v", got)
			}
		})
		t.Run(tc.name+"/rollback failed", func(t *testing.T) {
			e := newAgentSettingsEnv(t)
			tc.setup(t, e)
			e.audit.reset()

			err := tc.change(newFailingAgentSettings(t, e, breakAgentStores(e)))

			wantAuditFailure(t, err, tc.subject, false)
		})
	}
}

// 失敗の文面に API キーを含めない。
func TestAuditFailureOfAnAPIKeyChangeDoesNotLeakTheKey(t *testing.T) {
	e := newAgentSettingsEnv(t)
	err := newFailingAgentSettings(t, e, breakAgentStores(e)).SetOpenAIKey(context.Background(), canaryKey)
	if err == nil {
		t.Fatal("want an error")
	}
	if msg := err.Error(); len(msg) == 0 || strings.Contains(msg, canaryKey) {
		t.Errorf("error = %q", msg)
	}
	if strings.Contains(e.logs.String(), canaryKey) {
		t.Error("the key leaked into the log")
	}
}

// ---- 起動許可リスト・拒否リスト ----

// flakyStores は LaunchableApps・LaunchDenylist の書き込みを、broken が立ったあとはすべて失敗させる（戻す書き込みの失敗を再現する）。
type flakyApps struct {
	repository.LaunchableApps
	broken *bool
}

func (f flakyApps) Create(ctx context.Context, a domain.LaunchableApp, limit int) error {
	if *f.broken {
		return errStoreLocked
	}
	return f.LaunchableApps.Create(ctx, a, limit)
}

func (f flakyApps) Update(ctx context.Context, a domain.LaunchableApp) error {
	if *f.broken {
		return errStoreLocked
	}
	return f.LaunchableApps.Update(ctx, a)
}

func (f flakyApps) Delete(ctx context.Context, id string) error {
	if *f.broken {
		return errStoreLocked
	}
	return f.LaunchableApps.Delete(ctx, id)
}

type flakyDenied struct {
	repository.LaunchDenylist
	broken *bool
}

func (f flakyDenied) Create(ctx context.Context, d domain.DeniedApp, limit int) error {
	if *f.broken {
		return errStoreLocked
	}
	return f.LaunchDenylist.Create(ctx, d, limit)
}

func (f flakyDenied) Update(ctx context.Context, d domain.DeniedApp) error {
	if *f.broken {
		return errStoreLocked
	}
	return f.LaunchDenylist.Update(ctx, d)
}

func (f flakyDenied) Delete(ctx context.Context, id string) error {
	if *f.broken {
		return errStoreLocked
	}
	return f.LaunchDenylist.Delete(ctx, id)
}

// newFailingRegistry は e のストアを使い、RecordSync が必ず失敗する AppRegistry を返す。breakRollback なら、監査の失敗と同時にストアの書き込みも壊す。
func newFailingRegistry(t *testing.T, e *ctlEnv, breakRollback bool) AppRegistry {
	t.Helper()
	broken := false
	audit := &failingAudit{ctlAudit: e.audit}
	if breakRollback {
		audit.breakStores = func() { broken = true; e.settings.setErrAfter(0, errStoreLocked) }
	}
	reg, err := NewAppRegistry(AppRegistryConfig{
		Apps: flakyApps{e.appStore, &broken}, Denied: flakyDenied{e.denyStore, &broken}, Settings: e.settings,
		Launcher: e.launcher, Picker: e.picker, Audit: audit, Clock: e.clock, IDs: &ctlIDs{n: 1000}, // e.apps が採る ID（lease-1…）と重ならないようにする
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestAppRegistryChangesAreRolledBackWhenTheAuditCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		subject string
		change  func(reg AppRegistry, appID, deniedID string) error
	}{
		{"launchable app create", AuditSubjectLaunchableApp, func(reg AppRegistry, _, _ string) error {
			_, err := reg.Create(ctx, domain.AppInput{Label: "電卓", ExePath: appCalc})
			return err
		}},
		{"launchable app update", AuditSubjectLaunchableApp, func(reg AppRegistry, appID, _ string) error {
			_, err := reg.Update(ctx, appID, domain.AppInput{Label: "メモ帳 2", ExePath: appCalc, Args: []string{"x"}})
			return err
		}},
		{"launchable app delete", AuditSubjectLaunchableApp, func(reg AppRegistry, appID, _ string) error { return reg.Delete(ctx, appID) }},
		{"denylist create", AuditSubjectLaunchDenylist, func(reg AppRegistry, _, _ string) error {
			_, err := reg.CreateDenied(ctx, "powershell.exe")
			return err
		}},
		{"denylist update", AuditSubjectLaunchDenylist, func(reg AppRegistry, _, deniedID string) error {
			_, err := reg.UpdateDenied(ctx, deniedID, "pwsh.exe")
			return err
		}},
		{"denylist delete", AuditSubjectLaunchDenylist, func(reg AppRegistry, _, deniedID string) error { return reg.DeleteDenied(ctx, deniedID) }},
		{"launch mode", AuditSubjectLaunchMode, func(reg AppRegistry, _, _ string) error { return reg.SetLaunchMode(ctx, domain.LaunchModeDenylist) }},
	}
	type state struct {
		apps   []domain.LaunchableApp
		denied []domain.DeniedApp
		mode   domain.LaunchMode
	}
	read := func(t *testing.T, e *ctlEnv) state {
		t.Helper()
		apps, err := e.apps.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		denied, err := e.apps.ListDenied(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mode, err := e.apps.LaunchMode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return state{apps, denied, mode}
	}
	seed := func(t *testing.T, e *ctlEnv) (appID, deniedID string) {
		t.Helper()
		a, err := e.apps.Create(ctx, notepadInput())
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.apps.CreateDenied(ctx, "cmd.exe")
		if err != nil {
			t.Fatal(err)
		}
		e.audit.reset()
		return a.ID, d.ID
	}
	for _, tc := range tests {
		t.Run(tc.name+"/restored", func(t *testing.T) {
			e := newAppEnv(t)
			appID, deniedID := seed(t, e)
			before := read(t, e)

			err := tc.change(newFailingRegistry(t, e, false), appID, deniedID)

			wantAuditFailure(t, err, tc.subject, true)
			after := read(t, e)
			if len(after.apps) != len(before.apps) || len(after.denied) != len(before.denied) || after.mode != before.mode {
				t.Fatalf("state changed after a refused change: before %+v, after %+v", before, after)
			}
			for i := range before.apps {
				a, b := before.apps[i], after.apps[i]
				if a.ID != b.ID || a.Label != b.Label || a.ExePath != b.ExePath || a.WorkingDir != b.WorkingDir || len(a.Args) != len(b.Args) {
					t.Errorf("launchable app %d changed: %+v -> %+v", i, a, b)
				}
			}
			for i := range before.denied {
				a, b := before.denied[i], after.denied[i]
				if a.ID != b.ID || a.Kind != b.Kind || a.Value != b.Value {
					t.Errorf("denied entry %d changed: %+v -> %+v", i, a, b)
				}
			}
			if got := e.audit.All(); len(got) != 0 {
				t.Errorf("an entry was recorded for a refused change: %+v", got)
			}
		})
		t.Run(tc.name+"/rollback failed", func(t *testing.T) {
			e := newAppEnv(t)
			appID, deniedID := seed(t, e)

			err := tc.change(newFailingRegistry(t, e, true), appID, deniedID)

			wantAuditFailure(t, err, tc.subject, false)
		})
	}
}

// 引数の JSON が壊れていて読めない行（Corrupt）は、読めた範囲で書き戻すと、起動を拒否されていた行が引数なしで起動できる行に変わる。
// 削除を監査に書けなかったときは、戻さずに戻せなかった（restored = false）と伝える。
func TestAppRegistryDoesNotRestoreAnUnreadableRow(t *testing.T) {
	ctx := context.Background()
	e := newAppEnv(t)
	a, err := e.apps.Create(ctx, notepadInput())
	if err != nil {
		t.Fatal(err)
	}
	corruptArgsJSON(t, e.appDB, a.ID, `[1]`)

	err = newFailingRegistry(t, e, false).Delete(ctx, a.ID)

	wantAuditFailure(t, err, AuditSubjectLaunchableApp, false)
	if rows, _ := e.apps.List(ctx); len(rows) != 0 {
		t.Errorf("rows = %+v; an unreadable row must not be re-created with empty args", rows)
	}
}
