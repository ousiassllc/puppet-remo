package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

type memAutostart struct {
	enabled bool
	getErr  error
	setErr  error
	sets    []bool
}

func (a *memAutostart) Enabled(context.Context) (bool, error) { return a.enabled, a.getErr }
func (a *memAutostart) Set(_ context.Context, v bool) error {
	if a.setErr != nil {
		return a.setErr
	}
	a.enabled = v
	a.sets = append(a.sets, v)
	return nil
}

type settingsEnv struct {
	svc     HostSettingsService
	store   *ctlSettings
	auto    *memAutostart
	audit   *recordingAudit
	changes [][2]HostSettings
}

func newSettingsEnv(t *testing.T, withAutostart bool) *settingsEnv {
	t.Helper()
	e := &settingsEnv{store: newCtlSettings(), audit: &recordingAudit{}}
	cfg := HostSettingsConfig{
		Settings: e.store, Audit: e.audit, Clock: newFakeClock(),
		OnChange: func(old, updated HostSettings) { e.changes = append(e.changes, [2]HostSettings{old, updated}) },
	}
	if withAutostart {
		e.auto = &memAutostart{}
		cfg.Autostart = e.auto
	}
	var err error
	if e.svc, err = NewHostSettings(cfg); err != nil {
		t.Fatal(err)
	}
	return e
}

var defaultHostSettings = HostSettings{Port: 8765, KillSwitchHotkey: "Ctrl+Alt+Shift+K", ScreenshotMaxDimension: 1568, LeaseIdleTimeoutSec: 300}

func TestHostSettingsDefaults(t *testing.T) {
	e := newSettingsEnv(t, true)
	got, err := e.svc.Load(context.Background())
	if err != nil || got != defaultHostSettings {
		t.Errorf("Load = %+v, %v; want %+v", got, err, defaultHostSettings)
	}
	if !e.svc.AutostartAvailable() || newSettingsEnv(t, false).svc.AutostartAvailable() {
		t.Error("AutostartAvailable must follow the Autostart dependency")
	}
}

func TestHostSettingsSaveAndLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, true)
	want := HostSettings{Port: 9000, KillSwitchHotkey: "Ctrl+Shift+Q", ScreenshotMaxDimension: 2048, LeaseIdleTimeoutSec: 120, Autostart: true}

	saved, err := e.svc.Save(ctx, want)
	if err != nil || saved != want {
		t.Fatalf("Save = %+v, %v", saved, err)
	}
	got, err := e.svc.Load(ctx)
	if err != nil || got != want {
		t.Errorf("Load = %+v, %v", got, err)
	}
	// settings の既存の読み手（LeaseIdleTimeout・Control の最大辺）と同じキー・形式で保存される。
	if d, err := LeaseIdleTimeout(ctx, e.store); err != nil || d.Seconds() != 120 {
		t.Errorf("LeaseIdleTimeout = %v, %v", d, err)
	}
	for key, raw := range map[string]string{"server.port": "9000", "killswitch.hotkey": `"Ctrl+Shift+Q"`, "screenshot.max_dimension": "2048", "lease.idle_timeout_sec": "120"} {
		if got := string(e.store.values[key]); got != raw {
			t.Errorf("%s = %s, want %s", key, got, raw)
		}
	}
	// 自動起動は OS が真実の源で、DB には持たない。
	if _, ok := e.store.values["autostart"]; ok {
		t.Error("autostart must not be stored in settings")
	}
	if !e.auto.enabled {
		t.Error("autostart was not applied")
	}
	// 変更の通知（ポートの再 Listen・ホットキーの再登録など）と、host の監査。
	if len(e.changes) != 1 || e.changes[0][0] != defaultHostSettings || e.changes[0][1] != want {
		t.Errorf("changes = %+v", e.changes)
	}
	a := e.audit.entries
	if len(a) != 1 || a[0].Event != "settings.update" || a[0].ActorType != domain.ActorHost || a[0].Kind != "settings" {
		t.Fatalf("audit = %+v", a)
	}
	var detail map[string]any
	if err := json.Unmarshal(a[0].DetailJSON, &detail); err != nil || len(detail) != 5 || detail["server.port"] != float64(9000) || detail["autostart"] != true {
		t.Errorf("detail = %s (%v)", a[0].DetailJSON, err)
	}
}

func TestHostSettingsUnchangedSaveIsQuiet(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, true)
	if _, err := e.svc.Save(ctx, defaultHostSettings); err != nil {
		t.Fatal(err)
	}
	if len(e.changes) != 0 || len(e.audit.entries) != 0 {
		t.Errorf("a no-op save must not notify or audit: %+v %+v", e.changes, e.audit.entries)
	}
}

func TestHostSettingsValidation(t *testing.T) {
	ok := defaultHostSettings
	tests := []struct {
		name   string
		mutate func(*HostSettings)
		fields []string
	}{
		{"port below range", func(s *HostSettings) { s.Port = 1023 }, []string{FieldPort}},
		{"port above range", func(s *HostSettings) { s.Port = 65536 }, []string{FieldPort}},
		{"port lower bound", func(s *HostSettings) { s.Port = 1024 }, nil},
		{"port upper bound", func(s *HostSettings) { s.Port = 65535 }, nil},
		{"max dimension below", func(s *HostSettings) { s.ScreenshotMaxDimension = 255 }, []string{FieldScreenshotMaxDimension}},
		{"max dimension above", func(s *HostSettings) { s.ScreenshotMaxDimension = 4097 }, []string{FieldScreenshotMaxDimension}},
		{"max dimension bounds", func(s *HostSettings) { s.ScreenshotMaxDimension = 256 }, nil},
		{"idle below", func(s *HostSettings) { s.LeaseIdleTimeoutSec = 29 }, []string{FieldLeaseIdleTimeoutSec}},
		{"idle above", func(s *HostSettings) { s.LeaseIdleTimeoutSec = 3601 }, []string{FieldLeaseIdleTimeoutSec}},
		{"idle bounds", func(s *HostSettings) { s.LeaseIdleTimeoutSec = 3600 }, nil},
		{"hotkey empty", func(s *HostSettings) { s.KillSwitchHotkey = "" }, []string{FieldKillSwitchHotkey}},
		{"hotkey without modifier", func(s *HostSettings) { s.KillSwitchHotkey = "K" }, []string{FieldKillSwitchHotkey}},
		{"hotkey unknown modifier", func(s *HostSettings) { s.KillSwitchHotkey = "Hyper+K" }, []string{FieldKillSwitchHotkey}},
		{"hotkey too long", func(s *HostSettings) { s.KillSwitchHotkey = "Ctrl+" + string(make([]byte, 100)) }, []string{FieldKillSwitchHotkey}},
		{"hotkey case-insensitive", func(s *HostSettings) { s.KillSwitchHotkey = "ctrl+alt+k" }, nil},
		{"all invalid", func(s *HostSettings) { *s = HostSettings{} }, []string{FieldPort, FieldScreenshotMaxDimension, FieldLeaseIdleTimeoutSec, FieldKillSwitchHotkey}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSettingsEnv(t, true)
			s := ok
			tt.mutate(&s)
			_, err := e.svc.Save(context.Background(), s)
			if tt.fields == nil {
				if err != nil {
					t.Fatalf("Save: %v", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Save error = %v, want *ValidationError", err)
			}
			if len(verr.Fields) != len(tt.fields) {
				t.Errorf("fields = %v, want %v", verr.Fields, tt.fields)
			}
			for _, f := range tt.fields {
				if verr.Fields[f] == "" {
					t.Errorf("no message for %s: %v", f, verr.Fields)
				}
			}
			// invalid_argument として扱え、何も保存しない。
			if !errors.Is(err, domain.ErrInvalidArgument) || domain.CodeOf(err) != domain.CodeInvalidArgument {
				t.Errorf("code = %q", domain.CodeOf(err))
			}
			if len(e.store.values) != 0 || len(e.auto.sets) != 0 || len(e.changes) != 0 {
				t.Errorf("an invalid save must change nothing: %v %v %v", e.store.values, e.auto.sets, e.changes)
			}
		})
	}
}

func TestHostSettingsWithoutAutostartIgnoresIt(t *testing.T) {
	e := newSettingsEnv(t, false)
	s := defaultHostSettings
	s.Autostart = true
	saved, err := e.svc.Save(context.Background(), s)
	if err != nil || saved.Autostart {
		t.Errorf("Save = %+v, %v", saved, err)
	}
}

func TestHostSettingsFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("store write failure", func(t *testing.T) {
		e := newSettingsEnv(t, true)
		boom := errors.New("disk full")
		e.store.setErr = boom
		s := defaultHostSettings
		s.Port = 9000
		if _, err := e.svc.Save(ctx, s); !errors.Is(err, boom) {
			t.Errorf("Save = %v", err)
		}
		if len(e.changes) != 0 {
			t.Error("a failed save must not notify")
		}
	})
	t.Run("autostart failure", func(t *testing.T) {
		e := newSettingsEnv(t, true)
		boom := errors.New("registry denied")
		e.auto.setErr = boom
		s := defaultHostSettings
		s.Autostart = true
		if _, err := e.svc.Save(ctx, s); !errors.Is(err, boom) {
			t.Errorf("Save = %v", err)
		}
	})
	t.Run("autostart read failure", func(t *testing.T) {
		e := newSettingsEnv(t, true)
		e.auto.getErr = errors.New("registry denied")
		if _, err := e.svc.Load(ctx); err == nil {
			t.Error("Load must report the failure")
		}
	})
	t.Run("corrupt stored value", func(t *testing.T) {
		for key, raw := range map[string]string{"server.port": `"x"`, "screenshot.max_dimension": "0", "lease.idle_timeout_sec": "1.5", "killswitch.hotkey": "7"} {
			e := newSettingsEnv(t, true)
			e.store.put(key, raw)
			if _, err := e.svc.Load(ctx); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("%s=%s: Load = %v, want invalid_argument", key, raw, err)
			}
			// 検証済みの入力で上書きして復旧できる。
			if _, err := e.svc.Save(ctx, defaultHostSettings); err != nil {
				t.Errorf("%s=%s: recovery Save = %v", key, raw, err)
			}
		}
	})
}

func TestNewHostSettingsValidatesConfig(t *testing.T) {
	if _, err := NewHostSettings(HostSettingsConfig{}); err == nil {
		t.Error("missing Settings must fail")
	}
	if _, err := NewHostSettings(HostSettingsConfig{Settings: newCtlSettings(), Audit: &recordingAudit{}}); err == nil {
		t.Error("Audit without Clock must fail")
	}
}

// failKeySettings は failKey の書き込みだけが失敗する設定ストア。
type failKeySettings struct {
	*ctlSettings
	failKey string
	err     error
}

func (s failKeySettings) Set(ctx context.Context, key string, value json.RawMessage) error {
	if key == s.failKey {
		return s.err
	}
	return s.ctlSettings.Set(ctx, key, value)
}

// 書き込みの途中（3 つ目）で失敗したら、書き込めた分（ポート・ホットキー）だけを OnChange と監査へ渡す。
// 書けなかった項目まで通知すると、動いているコンポーネントが保存済みの値とずれる。
func TestHostSettingsPartialWriteNotifiesOnlyWhatWasApplied(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk full")
	store := failKeySettings{ctlSettings: newCtlSettings(), failKey: settingScreenshotMaxDim, err: boom}
	audit := &recordingAudit{}
	var changes [][2]HostSettings
	svc, err := NewHostSettings(HostSettingsConfig{
		Settings: store, Audit: audit, Clock: newFakeClock(),
		OnChange: func(old, updated HostSettings) { changes = append(changes, [2]HostSettings{old, updated}) },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := HostSettings{Port: 9000, KillSwitchHotkey: "Ctrl+Shift+Q", ScreenshotMaxDimension: 2048, LeaseIdleTimeoutSec: 120}

	if _, err := svc.Save(ctx, want); !errors.Is(err, boom) {
		t.Fatalf("Save = %v, want %v", err, boom)
	}
	applied := defaultHostSettings
	applied.Port, applied.KillSwitchHotkey = 9000, "Ctrl+Shift+Q"
	if len(changes) != 1 || changes[0][0] != defaultHostSettings || changes[0][1] != applied {
		t.Errorf("changes = %+v, want one change to %+v", changes, applied)
	}
	got, err := svc.Load(ctx)
	if err != nil || got != applied {
		t.Errorf("Load after the failed save = %+v, %v; want %+v", got, err, applied)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("audit = %+v", audit.entries)
	}
	var detail map[string]any
	if err := json.Unmarshal(audit.entries[0].DetailJSON, &detail); err != nil || len(detail) != 2 || detail["server.port"] != float64(9000) {
		t.Errorf("audit detail = %s (%v)", audit.entries[0].DetailJSON, err)
	}
}
