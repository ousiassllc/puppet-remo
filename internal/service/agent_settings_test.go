package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// canaryKey は API キーの代わりに入れる目印。ログ・監査・エラー・戻り値のどこにも出てはならない。
const canaryKey = "sk-CANARY-0123456789-do-not-leak"

type agentSettingsEnv struct {
	s        *AgentSettingsService
	set      *ctlSettings
	secrets  *platformtest.FakeSecretStore
	audit    *ctlAudit
	clock    *ctlClock
	logs     *bytes.Buffer
	mu       sync.Mutex
	canceled []string
}

func newAgentSettingsEnv(t *testing.T) *agentSettingsEnv {
	t.Helper()
	e := &agentSettingsEnv{
		set: newCtlSettings(), secrets: platformtest.NewFakeSecretStore(), audit: &ctlAudit{}, clock: newCtlClock(), logs: &bytes.Buffer{},
	}
	s, err := NewAgentSettings(AgentSettingsConfig{
		Settings: e.set, Secrets: e.secrets, Audit: e.audit, Clock: e.clock,
		Logger: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Cancel: func(reason string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.canceled = append(e.canceled, reason)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	return e
}

func (e *agentSettingsEnv) cancels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.canceled)
}

func (e *agentSettingsEnv) entries(event string) []repository.AuditEntry {
	var out []repository.AuditEntry
	for _, en := range e.audit.Entries() {
		if en.Event == event {
			out = append(out, en)
		}
	}
	return out
}

func validAgentInput() AgentSettingsInput {
	return AgentSettingsInput{
		Model: "gpt-6.1-sol", MaxSteps: 50, MaxDurationSec: 600, KeepScreenshots: 3,
		DenyProcesses: []string{"KeePass.exe"}, ApproveProcesses: []string{},
	}
}

func validFastpathInput() FastpathSettingsInput {
	return FastpathSettingsInput{Model: "jev-1.13", MinConfidence: 0.7, MaxSteps: 15, ExcludedLabels: []string{"delete"}}
}

func (e *agentSettingsEnv) enableAgent(t *testing.T) {
	t.Helper()
	if err := e.s.SetOpenAIKey(context.Background(), canaryKey); err != nil {
		t.Fatal(err)
	}
	in := validAgentInput()
	in.Enabled, in.Consent = true, true
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil
}

func (e *agentSettingsEnv) enableFastpath(t *testing.T) {
	t.Helper()
	if err := e.s.SetTypeSafeKey(context.Background(), canaryKey); err != nil {
		t.Fatal(err)
	}
	in := validFastpathInput()
	in.Enabled, in.Consent = true, true
	if err := e.s.SaveFastpath(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil
}

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v (%T), want *ValidationError", err, err)
	}
	return ve.Fields
}

func TestNewAgentSettingsRequiresDependencies(t *testing.T) {
	full := AgentSettingsConfig{Settings: newCtlSettings(), Secrets: platformtest.NewFakeSecretStore(), Clock: newCtlClock()}
	for name, mut := range map[string]func(*AgentSettingsConfig){
		"settings": func(c *AgentSettingsConfig) { c.Settings = nil },
		"secrets":  func(c *AgentSettingsConfig) { c.Secrets = nil },
		"clock":    func(c *AgentSettingsConfig) { c.Clock = nil },
	} {
		c := full
		mut(&c)
		if _, err := NewAgentSettings(c); err == nil {
			t.Errorf("without %s: want an error", name)
		}
	}
	if _, err := NewAgentSettings(full); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSettingsLoadAppliesDefaults(t *testing.T) {
	e := newAgentSettingsEnv(t)
	v, err := e.s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := AgentConfig{
		Model: "gpt-6.1-sol", MaxSteps: 50, MaxDurationSec: 600, KeepScreenshots: 3,
		DenyProcesses: []string{"KeePass.exe", "KeePassXC.exe", "1Password.exe", "Bitwarden.exe"}, ApproveProcesses: []string{},
		Fastpath: FastpathConfig{Model: "jev-1.13", MinConfidence: 0.7, MaxSteps: 15, ExcludedLabels: defaultFastpathExcluded},
	}
	if v.Enabled || v.ConsentAt != nil || v.Fastpath.Enabled || v.Fastpath.ConsentAt != nil || v.OpenAIKeyRegistered || v.TypeSafeKeyRegistered {
		t.Errorf("view = %+v", v)
	}
	if v.Model != want.Model || v.MaxSteps != want.MaxSteps || v.MaxDurationSec != want.MaxDurationSec || v.KeepScreenshots != want.KeepScreenshots ||
		!slices.Equal(v.DenyProcesses, want.DenyProcesses) || v.ApproveProcesses == nil || len(v.ApproveProcesses) != 0 ||
		v.Fastpath.Model != "jev-1.13" || v.Fastpath.MinConfidence != 0.7 || v.Fastpath.MaxSteps != 15 || !slices.Equal(v.Fastpath.ExcludedLabels, want.Fastpath.ExcludedLabels) {
		t.Errorf("view = %+v, want defaults %+v", v, want)
	}
	if strings.Contains(e.logs.String(), "invalid agent setting") {
		t.Errorf("defaults must not warn: %s", e.logs)
	}
	// 戻した既定値のスライスを書き換えても、次の読み取りに影響しない。
	v.DenyProcesses[0] = "x"
	v.Fastpath.ExcludedLabels[0] = "x"
	again, _ := e.s.Config(context.Background())
	if again.DenyProcesses[0] != "KeePass.exe" || again.Fastpath.ExcludedLabels[0] != "delete" {
		t.Error("the defaults were aliased")
	}
}

func TestAgentSettingsLoadFallsBackToDefaultsWithAWarning(t *testing.T) {
	tests := []struct {
		key, raw string
		check    func(AgentConfig) bool
	}{
		{SettingAgentMaxSteps, `0`, func(c AgentConfig) bool { return c.MaxSteps == 50 }},
		{SettingAgentMaxSteps, `501`, func(c AgentConfig) bool { return c.MaxSteps == 50 }},
		{SettingAgentMaxSteps, `"many"`, func(c AgentConfig) bool { return c.MaxSteps == 50 }},
		{SettingAgentMaxSteps, `1.5`, func(c AgentConfig) bool { return c.MaxSteps == 50 }},
		{SettingAgentMaxDurationSec, `29`, func(c AgentConfig) bool { return c.MaxDurationSec == 600 }},
		{SettingAgentKeepScreenshots, `11`, func(c AgentConfig) bool { return c.KeepScreenshots == 3 }},
		{SettingAgentModel, `""`, func(c AgentConfig) bool { return c.Model == "gpt-6.1-sol" }},
		{SettingAgentModel, `5`, func(c AgentConfig) bool { return c.Model == "gpt-6.1-sol" }},
		{SettingAgentEnabled, `"yes"`, func(c AgentConfig) bool { return !c.Enabled }},
		{SettingAgentDenyProcesses, `"KeePass.exe"`, func(c AgentConfig) bool { return len(c.DenyProcesses) == 4 }},
		{SettingAgentDenyProcesses, `null`, func(c AgentConfig) bool { return len(c.DenyProcesses) == 4 }},
		{SettingFastpathMinConfidence, `1.5`, func(c AgentConfig) bool { return c.Fastpath.MinConfidence == 0.7 }},
		{SettingFastpathMinConfidence, `-0.1`, func(c AgentConfig) bool { return c.Fastpath.MinConfidence == 0.7 }},
		{SettingFastpathMaxSteps, `0`, func(c AgentConfig) bool { return c.Fastpath.MaxSteps == 15 }},
		{SettingAgentConsentAt, `"yesterday"`, func(c AgentConfig) bool { return c.ConsentAt == nil }},
		{SettingAgentConsentAt, `-5`, func(c AgentConfig) bool { return c.ConsentAt == nil }},
		{SettingFastpathConsentAt, `1.5`, func(c AgentConfig) bool { return c.Fastpath.ConsentAt == nil }},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.raw, func(t *testing.T) {
			e := newAgentSettingsEnv(t)
			e.set.put(tt.key, tt.raw)
			c, err := e.s.Config(context.Background())
			if err != nil {
				t.Fatalf("an invalid stored value must not fail the read: %v", err)
			}
			if !tt.check(c) {
				t.Errorf("config = %+v", c)
			}
			if tt.raw != `null` || strings.HasSuffix(tt.key, "processes") {
				if !strings.Contains(e.logs.String(), tt.key) {
					t.Errorf("no warning for %s: %s", tt.key, e.logs)
				}
			}
		})
	}
}

func TestAgentSettingsConfigReadsStoredValues(t *testing.T) {
	e := newAgentSettingsEnv(t)
	for k, v := range map[string]string{
		SettingAgentEnabled: `true`, SettingAgentConsentAt: `1793610000123`, SettingAgentModel: `"gpt-x"`, SettingAgentMaxSteps: `7`,
		SettingAgentMaxDurationSec: `90`, SettingAgentDenyProcesses: `["a.exe"]`, SettingAgentApproveProcesses: `["b.exe"]`,
		SettingAgentKeepScreenshots: `2`, SettingFastpathEnabled: `true`, SettingFastpathConsentAt: `1793610000456`,
		SettingFastpathModel: `"jev-x"`, SettingFastpathMinConfidence: `0.25`, SettingFastpathMaxSteps: `4`, SettingFastpathExcludedLabels: `["z"]`,
	} {
		e.set.put(k, v)
	}
	c, err := e.s.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.ConsentAt == nil || c.ConsentAt.UnixMilli() != 1793610000123 || c.ConsentAt.Location().String() != "UTC" ||
		c.Model != "gpt-x" || c.MaxSteps != 7 || c.MaxDurationSec != 90 || !slices.Equal(c.DenyProcesses, []string{"a.exe"}) ||
		!slices.Equal(c.ApproveProcesses, []string{"b.exe"}) || c.KeepScreenshots != 2 ||
		!c.Fastpath.Enabled || c.Fastpath.ConsentAt == nil || c.Fastpath.ConsentAt.UnixMilli() != 1793610000456 || c.Fastpath.Model != "jev-x" ||
		c.Fastpath.MinConfidence != 0.25 || c.Fastpath.MaxSteps != 4 || !slices.Equal(c.Fastpath.ExcludedLabels, []string{"z"}) {
		t.Errorf("config = %+v", c)
	}
}

func TestAgentSettingsReadFailureIsAnError(t *testing.T) {
	s, err := NewAgentSettings(AgentSettingsConfig{Settings: failingSettings{Settings: newCtlSettings()}, Secrets: platformtest.NewFakeSecretStore(), Clock: newCtlClock()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Config(context.Background()); err == nil {
		t.Fatal("a failing store must be reported, not replaced by defaults")
	}
	if _, err := s.Load(context.Background()); err == nil {
		t.Fatal("Load must report it too")
	}
}

func TestAgentSettingsSaveAgentValidation(t *testing.T) {
	tests := []struct {
		name   string
		mut    func(*AgentSettingsInput)
		fields []string
	}{
		{"valid", func(*AgentSettingsInput) {}, nil},
		{"max_steps 0", func(in *AgentSettingsInput) { in.MaxSteps = 0 }, []string{FieldAgentMaxSteps}},
		{"max_steps 501", func(in *AgentSettingsInput) { in.MaxSteps = 501 }, []string{FieldAgentMaxSteps}},
		{"max_steps 500", func(in *AgentSettingsInput) { in.MaxSteps = 500 }, nil},
		{"max_duration 29", func(in *AgentSettingsInput) { in.MaxDurationSec = 29 }, []string{FieldAgentMaxDurationSec}},
		{"max_duration 7201", func(in *AgentSettingsInput) { in.MaxDurationSec = 7201 }, []string{FieldAgentMaxDurationSec}},
		{"max_duration 30 and 7200", func(in *AgentSettingsInput) { in.MaxDurationSec = 30 }, nil},
		{"keep_screenshots 0", func(in *AgentSettingsInput) { in.KeepScreenshots = 0 }, []string{FieldAgentKeepScreenshots}},
		{"keep_screenshots 11", func(in *AgentSettingsInput) { in.KeepScreenshots = 11 }, []string{FieldAgentKeepScreenshots}},
		{"keep_screenshots 10", func(in *AgentSettingsInput) { in.KeepScreenshots = 10 }, nil},
		{"empty model", func(in *AgentSettingsInput) { in.Model = "" }, []string{FieldAgentModel}},
		{"blank model", func(in *AgentSettingsInput) { in.Model = "  \t " }, []string{FieldAgentModel}},
		{"control chars in model", func(in *AgentSettingsInput) { in.Model = "gpt\n6" }, []string{FieldAgentModel}},
		{"overlong model", func(in *AgentSettingsInput) { in.Model = strings.Repeat("m", 129) }, []string{FieldAgentModel}},
		{"several errors at once", func(in *AgentSettingsInput) { in.MaxSteps, in.Model = 0, "" }, []string{FieldAgentMaxSteps, FieldAgentModel}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAgentSettingsEnv(t)
			in := validAgentInput()
			tt.mut(&in)
			err := e.s.SaveAgent(context.Background(), in)
			if tt.fields == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			got := fieldsOf(t, err)
			names := make([]string, 0, len(got))
			for k := range got {
				names = append(names, k)
			}
			slices.Sort(names)
			want := slices.Clone(tt.fields)
			slices.Sort(want)
			if !slices.Equal(names, want) {
				t.Fatalf("fields = %v, want %v", names, want)
			}
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Error("a ValidationError must be invalid_argument")
			}
			if len(e.set.values) != 0 || len(e.audit.Entries()) != 0 {
				t.Errorf("an invalid input must save and audit nothing: %v", e.set.values)
			}
		})
	}
}

func TestAgentSettingsSaveAgentStoresTrimmedDedupedLists(t *testing.T) {
	e := newAgentSettingsEnv(t)
	in := validAgentInput()
	in.Model = "  gpt-6.1-sol  "
	in.DenyProcesses = []string{" KeePass.exe ", "keepass.exe", "", "  ", `C:\Tools\Vault.exe`, "vault.exe", "Other.exe"}
	in.ApproveProcesses = []string{"a.exe", "A.EXE"}
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	c, _ := e.s.Config(context.Background())
	if c.Model != "gpt-6.1-sol" || !slices.Equal(c.DenyProcesses, []string{"KeePass.exe", `C:\Tools\Vault.exe`, "Other.exe"}) ||
		!slices.Equal(c.ApproveProcesses, []string{"a.exe"}) {
		t.Errorf("config = %+v", c)
	}
	// 空にした拒否リストは「既定に戻す」ではなく、空のまま保存される。
	in.DenyProcesses = nil
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.s.Config(context.Background()); len(c.DenyProcesses) != 0 {
		t.Errorf("deny list = %v, want empty", c.DenyProcesses)
	}
	if raw, _, _ := e.set.Get(context.Background(), SettingAgentDenyProcesses); string(raw) != `[]` {
		t.Errorf("stored = %s, want []", raw)
	}
}

func TestAgentSettingsEnableRequiresConsentAndKey(t *testing.T) {
	e := newAgentSettingsEnv(t)
	in := validAgentInput()
	in.Enabled = true

	// 同意なし: 項目エラー。
	if err := e.s.SaveAgent(context.Background(), in); err == nil || fieldsOf(t, err)[FieldAgentConsent] == "" {
		t.Fatalf("err = %v, want a consent field error", err)
	}
	// 同意はあるがキー未登録: agent_disabled。
	in.Consent = true
	err := e.s.SaveAgent(context.Background(), in)
	if !errors.Is(err, domain.ErrAgentDisabled) || domain.CodeOf(err) != domain.CodeAgentDisabled {
		t.Fatalf("err = %v, want ErrAgentDisabled", err)
	}
	if len(e.set.values) != 0 || len(e.audit.Entries()) != 0 {
		t.Errorf("a rejected enable must save and audit nothing: %v", e.set.values)
	}
	// キー登録後は有効にできる。
	if err := e.s.SetOpenAIKey(context.Background(), canaryKey); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	c, _ := e.s.Config(context.Background())
	if !c.Enabled || c.ConsentAt == nil || !c.ConsentAt.Equal(e.clock.Now()) {
		t.Fatalf("config = %+v", c)
	}
	if raw, _, _ := e.set.Get(context.Background(), SettingAgentConsentAt); string(raw) != fmt.Sprint(e.clock.Now().UnixMilli()) {
		t.Errorf("consent_at stored as %s, want UTC ms", raw)
	}
	consent := e.entries("agent.consent")
	if len(consent) != 1 {
		t.Fatalf("audit = %+v", e.audit.Entries())
	}
	if en := consent[0]; en.Kind != "agent" || en.ActorType != domain.ActorHost || en.Result != "ok" || en.ClientLabel != "(host)" ||
		auditDetail(t, en)["state"] != "granted" {
		t.Errorf("consent entry = %+v (%s)", en, en.DetailJSON)
	}
	if upd := e.entries("settings.update"); len(upd) != 1 || upd[0].Kind != "settings" || upd[0].ActorType != domain.ActorHost || auditDetail(t, upd[0])[SettingAgentEnabled] != true {
		t.Errorf("settings.update = %+v", upd)
	}
	if len(e.cancels()) != 0 {
		t.Errorf("enabling must not cancel tasks: %v", e.cancels())
	}
}

func TestAgentSettingsSavingWhileEnabledKeepsConsent(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	consent0, _, _ := e.set.Get(context.Background(), SettingAgentConsentAt)
	e.clock.Advance(time.Second)

	in := validAgentInput()
	in.Enabled, in.MaxSteps = true, 80 // Consent のチェックは不要（同意済み）
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	consent1, _, _ := e.set.Get(context.Background(), SettingAgentConsentAt)
	if string(consent0) != string(consent1) {
		t.Errorf("consent_at changed: %s -> %s", consent0, consent1)
	}
	if len(e.entries("agent.consent")) != 0 {
		t.Error("no consent event for a plain save")
	}
	upd := e.entries("settings.update")
	if len(upd) != 1 {
		t.Fatalf("audit = %+v", e.audit.Entries())
	}
	d := auditDetail(t, upd[0])
	if d[SettingAgentMaxSteps] != float64(80) || len(d) != 1 {
		t.Errorf("only the changed key is recorded: %v", d)
	}
	if len(e.cancels()) != 0 {
		t.Errorf("cancels = %v", e.cancels())
	}
	// 変更が無ければ settings.update は記録しない。
	e.audit.entries = nil
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(e.audit.Entries()) != 0 {
		t.Errorf("an unchanged save audited: %+v", e.audit.Entries())
	}
}

func TestAgentSettingsDisableRemovesConsentAndCancelsTasks(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	in := validAgentInput() // Enabled false
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	c, _ := e.s.Config(context.Background())
	if c.Enabled || c.ConsentAt != nil {
		t.Fatalf("config = %+v", c)
	}
	if got := e.cancels(); len(got) != 1 || got[0] != "agent_disabled" {
		t.Errorf("cancels = %v, want one agent_disabled", got)
	}
	consent := e.entries("agent.consent")
	if len(consent) != 1 || auditDetail(t, consent[0])["state"] != "revoked" || consent[0].ActorType != domain.ActorHost || consent[0].Kind != "agent" {
		t.Errorf("audit = %+v", e.audit.Entries())
	}
	// すでに無効なら、保存してもタスクを中断せず、同意の取り消しも記録しない。
	e.audit.entries = nil
	if err := e.s.SaveAgent(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(e.cancels()) != 1 || len(e.entries("agent.consent")) != 0 {
		t.Errorf("cancels = %v, audit = %+v", e.cancels(), e.audit.Entries())
	}
}

func TestAgentSettingsAPIKeyValidation(t *testing.T) {
	tests := []struct {
		name string
		key  string
		ok   bool
	}{
		{"plain", "sk-abc", true},
		{"surrounding whitespace is trimmed", "  sk-abc\n", true},
		{"empty", "", false},
		{"blank", " \t\n", false},
		{"embedded newline", "sk-a\nbc", false},
		{"embedded NUL", "sk-a\x00bc", false},
		{"invalid utf-8", "sk-\xff\xfe", false},
		{"2560 bytes", strings.Repeat("k", 2560), true},
		{"2561 bytes", strings.Repeat("k", 2561), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAgentSettingsEnv(t)
			err := e.s.SetOpenAIKey(context.Background(), tt.key)
			if tt.ok {
				if err != nil {
					t.Fatal(err)
				}
				got, gerr := e.secrets.Get(platform.SecretOpenAI)
				if gerr != nil || got != strings.TrimSpace(tt.key) {
					t.Errorf("stored key mismatch (err %v)", gerr)
				}
				return
			}
			if fieldsOf(t, err)[FieldAPIKey] == "" {
				t.Fatalf("err = %v, want an api_key field error", err)
			}
			if len(e.secrets.CallsTo("Set")) != 0 || len(e.audit.Entries()) != 0 {
				t.Error("an invalid key must be neither stored nor audited")
			}
			if tt.key != "" && strings.TrimSpace(tt.key) != "" && strings.Contains(err.Error(), strings.TrimSpace(tt.key)) {
				t.Errorf("the error echoes the key: %v", err)
			}
		})
	}
}

// どのキーも、ログ・監査・エラー・View・Config のどこにも出ない（canary）。
func TestAgentSettingsNeverLeaksAPIKeys(t *testing.T) {
	e := newAgentSettingsEnv(t)
	ctx := context.Background()
	check := func(step string) {
		t.Helper()
		if strings.Contains(e.logs.String(), canaryKey) {
			t.Errorf("%s: the key reached the logs: %s", step, e.logs)
		}
		for _, en := range e.audit.Entries() {
			if strings.Contains(fmt.Sprintf("%+v %s", en, en.DetailJSON), canaryKey) {
				t.Errorf("%s: the key reached the audit: %+v", step, en)
			}
		}
		for k, v := range e.set.values {
			if strings.Contains(string(v), canaryKey) {
				t.Errorf("%s: the key reached the setting %s", step, k)
			}
		}
	}
	for _, step := range []struct {
		name string
		do   func() error
	}{
		{"SetOpenAIKey", func() error { return e.s.SetOpenAIKey(ctx, canaryKey) }},
		{"SetTypeSafeKey", func() error { return e.s.SetTypeSafeKey(ctx, canaryKey) }},
		{"SaveAgent", func() error {
			in := validAgentInput()
			in.Enabled, in.Consent = true, true
			return e.s.SaveAgent(ctx, in)
		}},
		{"SaveFastpath", func() error {
			in := validFastpathInput()
			in.Enabled, in.Consent = true, true
			return e.s.SaveFastpath(ctx, in)
		}},
		{"DisableFastpath", func() error {
			return e.s.DisableFastpath(ctx, domain.Actor{Kind: domain.ActorSystem}, FastpathDisableUnauthorized)
		}},
		{"DeleteTypeSafeKey", func() error { return e.s.DeleteTypeSafeKey(ctx) }},
		{"DeleteOpenAIKey", func() error { return e.s.DeleteOpenAIKey(ctx) }},
	} {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		check(step.name)
	}
	// 登録中の読み取り: View・Config・エラーにも値は無い。
	if err := e.s.SetOpenAIKey(ctx, canaryKey); err != nil {
		t.Fatal(err)
	}
	v, err := e.s.Load(ctx)
	if err != nil || !v.OpenAIKeyRegistered {
		t.Fatalf("view = %+v, err = %v", v, err)
	}
	if s := fmt.Sprintf("%+v %#v", v, v); strings.Contains(s, canaryKey) {
		t.Errorf("the view carries the key: %s", s)
	}
	if reg, err := e.s.OpenAIKeyRegistered(ctx); err != nil || !reg {
		t.Errorf("registered = %v, %v", reg, err)
	}
	// SecretStore の失敗も、キーを含まないメッセージで返す。
	e.secrets.SetError("Set", errors.New("credential manager unavailable"))
	err = e.s.SetOpenAIKey(ctx, canaryKey)
	if err == nil || strings.Contains(err.Error(), canaryKey) {
		t.Fatalf("err = %v", err)
	}
	check("failing SecretStore")
}

func TestAgentSettingsKeyRegistrationChecksThePresenceOnly(t *testing.T) {
	e := newAgentSettingsEnv(t)
	ctx := context.Background()
	if reg, err := e.s.OpenAIKeyRegistered(ctx); err != nil || reg {
		t.Fatalf("registered = %v, %v", reg, err)
	}
	_ = e.s.SetTypeSafeKey(ctx, "k")
	if reg, _ := e.s.TypeSafeKeyRegistered(ctx); !reg {
		t.Error("typesafe key not seen")
	}
	if reg, _ := e.s.OpenAIKeyRegistered(ctx); reg {
		t.Error("the two keys are independent")
	}
	if got := e.secrets.CallsTo("Get"); len(got) != 3 || got[0].Args[0] != platform.SecretOpenAI || got[1].Args[0] != platform.SecretTypeSafe {
		t.Errorf("Get calls = %+v", got)
	}
	// 登録の有無を調べられない（NotFound 以外の失敗）ときは、未登録として扱わずエラー。
	e.secrets.SetError("Get", errors.New("locked"))
	if _, err := e.s.OpenAIKeyRegistered(ctx); err == nil {
		t.Error("an unreadable credential store must be an error")
	}
	if _, err := e.s.Load(ctx); err == nil {
		t.Error("Load must fail too")
	}
	in := validAgentInput()
	in.Enabled, in.Consent = true, true
	if err := e.s.SaveAgent(ctx, in); err == nil || errors.Is(err, domain.ErrAgentDisabled) {
		t.Errorf("enabling with an unreadable store: err = %v, want a plain failure", err)
	}
}

func TestAgentSettingsDeleteOpenAIKeyDisablesTheAgent(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	if err := e.s.DeleteOpenAIKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.secrets.Get(platform.SecretOpenAI); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("the key is still stored: %v", err)
	}
	c, _ := e.s.Config(context.Background())
	if c.Enabled || c.ConsentAt != nil {
		t.Errorf("config = %+v", c)
	}
	if got := e.cancels(); len(got) != 1 || got[0] != "api_key_deleted" {
		t.Errorf("cancels = %v", got)
	}
	if len(e.entries("agent.consent")) != 1 || len(e.entries("settings.update")) != 1 {
		t.Fatalf("audit = %+v", e.audit.Entries())
	}
	d := auditDetail(t, e.entries("settings.update")[0])
	if d[auditKeyOpenAIAPIKey] != "deleted" || d[SettingAgentEnabled] != false {
		t.Errorf("detail = %v", d)
	}
	if len(e.cancels()) != 1 {
		t.Errorf("cancels = %v", e.cancels())
	}
	// 登録が無くても成功する（冪等）。実行中のタスクの中断は毎回行う（念のため）。
	if err := e.s.DeleteOpenAIKey(context.Background()); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAgentSettingsDeleteOpenAIKeyDisablesBeforeDeleting(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	e.secrets.SetError("Delete", errors.New("locked"))
	if err := e.s.DeleteOpenAIKey(context.Background()); err == nil {
		t.Fatal("want the delete failure")
	}
	if c, _ := e.s.Config(context.Background()); c.Enabled {
		t.Error("a failed key deletion must not leave an enabled agent without consent")
	}
}

func TestAgentSettingsFastpathValidation(t *testing.T) {
	tests := []struct {
		name   string
		mut    func(*FastpathSettingsInput)
		fields []string
	}{
		{"valid", func(*FastpathSettingsInput) {}, nil},
		{"confidence 0 and 1", func(in *FastpathSettingsInput) { in.MinConfidence = 0 }, nil},
		{"confidence 1", func(in *FastpathSettingsInput) { in.MinConfidence = 1 }, nil},
		{"confidence 1.01", func(in *FastpathSettingsInput) { in.MinConfidence = 1.01 }, []string{FieldFastpathMinConfidence}},
		{"confidence -0.01", func(in *FastpathSettingsInput) { in.MinConfidence = -0.01 }, []string{FieldFastpathMinConfidence}},
		{"max_steps 0", func(in *FastpathSettingsInput) { in.MaxSteps = 0 }, []string{FieldFastpathMaxSteps}},
		{"max_steps 501", func(in *FastpathSettingsInput) { in.MaxSteps = 501 }, []string{FieldFastpathMaxSteps}},
		{"empty model", func(in *FastpathSettingsInput) { in.Model = " " }, []string{FieldFastpathModel}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAgentSettingsEnv(t)
			in := validFastpathInput()
			tt.mut(&in)
			err := e.s.SaveFastpath(context.Background(), in)
			if tt.fields == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			got := fieldsOf(t, err)
			if len(got) != len(tt.fields) {
				t.Fatalf("fields = %v, want %v", got, tt.fields)
			}
			for _, f := range tt.fields {
				if got[f] == "" {
					t.Errorf("missing field error %s", f)
				}
			}
			if len(e.set.values) != 0 {
				t.Errorf("saved %v", e.set.values)
			}
		})
	}
}

func TestAgentSettingsFastpathEnableRequiresConsentAndKeyAndRecordsItsOwnConsent(t *testing.T) {
	e := newAgentSettingsEnv(t)
	ctx := context.Background()
	in := validFastpathInput()
	in.Enabled = true
	if err := e.s.SaveFastpath(ctx, in); fieldsOf(t, err)[FieldFastpathConsent] == "" {
		t.Fatalf("err = %v, want a consent field error", err)
	}
	in.Consent = true
	if err := e.s.SaveFastpath(ctx, in); !errors.Is(err, domain.ErrAgentDisabled) {
		t.Fatalf("err = %v, want ErrAgentDisabled (no TypeSafe key)", err)
	}
	// エージェントの同意・OpenAI キーでは足りない（独立）。
	e.enableAgent(t)
	if err := e.s.SaveFastpath(ctx, in); !errors.Is(err, domain.ErrAgentDisabled) {
		t.Fatalf("err = %v, want ErrAgentDisabled (agent consent is separate)", err)
	}
	if err := e.s.SetTypeSafeKey(ctx, canaryKey); err != nil {
		t.Fatal(err)
	}
	e.audit.entries = nil
	e.clock.Advance(5 * time.Second)
	if err := e.s.SaveFastpath(ctx, in); err != nil {
		t.Fatal(err)
	}
	c, _ := e.s.Config(ctx)
	if !c.Fastpath.Enabled || c.Fastpath.ConsentAt == nil || !c.Fastpath.ConsentAt.Equal(e.clock.Now()) {
		t.Fatalf("fastpath = %+v", c.Fastpath)
	}
	if c.ConsentAt == nil || c.ConsentAt.Equal(e.clock.Now()) {
		t.Errorf("the agent's own consent_at must not change: %v", c.ConsentAt)
	}
	en := e.entries("agent.fastpath.enable")
	if len(en) != 1 || en[0].Kind != "agent" || en[0].ActorType != domain.ActorHost || en[0].Result != "ok" {
		t.Fatalf("audit = %+v", e.audit.Entries())
	}
	if len(e.entries("agent.consent")) != 0 {
		t.Error("the fast path must not write agent.consent")
	}
	if len(e.cancels()) != 0 {
		t.Errorf("cancels = %v", e.cancels())
	}
	// 同意済みなら保存のたびにチェックは要らず、consent_at も変わらない。
	e.audit.entries = nil
	in2 := validFastpathInput()
	in2.Enabled, in2.MinConfidence = true, 0.9
	if err := e.s.SaveFastpath(ctx, in2); err != nil {
		t.Fatal(err)
	}
	if c2, _ := e.s.Config(ctx); !c2.Fastpath.ConsentAt.Equal(*c.Fastpath.ConsentAt) || c2.Fastpath.MinConfidence != 0.9 {
		t.Errorf("fastpath = %+v", c2.Fastpath)
	}
	if len(e.entries("agent.fastpath.enable")) != 0 {
		t.Error("no enable event for a plain save")
	}
	// 無効化: consent_at が消え、別イベントで記録。実行中のタスクは中断しない。
	e.audit.entries = nil
	off := validFastpathInput()
	if err := e.s.SaveFastpath(ctx, off); err != nil {
		t.Fatal(err)
	}
	c3, _ := e.s.Config(ctx)
	if c3.Fastpath.Enabled || c3.Fastpath.ConsentAt != nil || c3.ConsentAt == nil || !c3.Enabled {
		t.Errorf("config = %+v", c3)
	}
	if dis := e.entries("agent.fastpath.disable"); len(dis) != 1 || dis[0].ActorType != domain.ActorHost || dis[0].ErrorCode != "" {
		t.Errorf("audit = %+v", e.audit.Entries())
	}
	if len(e.cancels()) != 0 {
		t.Errorf("disabling the fast path must not cancel tasks: %v", e.cancels())
	}
}

func TestAgentSettingsDeleteTypeSafeKeyDisablesTheFastpathOnly(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	e.enableFastpath(t)
	if err := e.s.DeleteTypeSafeKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.secrets.Get(platform.SecretTypeSafe); !errors.Is(err, platform.ErrNotFound) {
		t.Error("the TypeSafe key is still stored")
	}
	if _, err := e.secrets.Get(platform.SecretOpenAI); err != nil {
		t.Error("the OpenAI key must stay")
	}
	c, _ := e.s.Config(context.Background())
	if c.Fastpath.Enabled || c.Fastpath.ConsentAt != nil || !c.Enabled || c.ConsentAt == nil {
		t.Errorf("config = %+v", c)
	}
	if len(e.cancels()) != 0 {
		t.Errorf("running tasks keep going on OpenAI: cancels = %v", e.cancels())
	}
	if len(e.entries("agent.fastpath.disable")) != 1 || len(e.entries("agent.consent")) != 0 {
		t.Errorf("audit = %+v", e.audit.Entries())
	}
	if d := auditDetail(t, e.entries("settings.update")[0]); d[auditKeyTypeSafeAPIKey] != "deleted" {
		t.Errorf("detail = %v", d)
	}
	// 冪等。
	if err := e.s.DeleteTypeSafeKey(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSettingsDisableFastpathBySystemAfter401(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	e.enableFastpath(t)
	system := domain.Actor{Kind: domain.ActorSystem, Label: "typesafe"}
	if err := e.s.DisableFastpath(context.Background(), system, FastpathDisableUnauthorized); err != nil {
		t.Fatal(err)
	}
	c, _ := e.s.Config(context.Background())
	if c.Fastpath.Enabled || c.Fastpath.ConsentAt != nil {
		t.Errorf("fastpath = %+v", c.Fastpath)
	}
	if !c.Enabled {
		t.Error("the agent itself stays enabled")
	}
	if _, err := e.secrets.Get(platform.SecretTypeSafe); err != nil {
		t.Error("the TypeSafe key stays (the GUI asks for a new one)")
	}
	if len(e.cancels()) != 0 {
		t.Errorf("cancels = %v", e.cancels())
	}
	dis := e.entries("agent.fastpath.disable")
	if len(dis) != 1 {
		t.Fatalf("audit = %+v", e.audit.Entries())
	}
	if d := dis[0]; d.Kind != "agent" || d.ActorType != domain.ActorSystem || d.ErrorCode != "typesafe_unauthorized" || d.Result != "error" || d.ClientLabel != "typesafe" {
		t.Errorf("entry = %+v", d)
	}
	// すでに無効なら何もしない。
	e.audit.entries = nil
	if err := e.s.DisableFastpath(context.Background(), system, FastpathDisableUnauthorized); err != nil {
		t.Fatal(err)
	}
	if len(e.audit.Entries()) != 0 {
		t.Errorf("audit = %+v", e.audit.Entries())
	}
	// 操作主体を渡さなければ system になる。
	e.enableFastpath(t)
	if err := e.s.DisableFastpath(context.Background(), domain.Actor{}, FastpathDisableUnauthorized); err != nil {
		t.Fatal(err)
	}
	if d := e.entries("agent.fastpath.disable"); len(d) != 1 || d[0].ActorType != domain.ActorSystem {
		t.Errorf("audit = %+v", e.audit.Entries())
	}
}

func TestAgentSettingsConcurrentSavesAreSerialised(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := validAgentInput()
			in.Enabled, in.MaxSteps = i%2 == 0, 10+i
			in.Consent = true
			if err := e.s.SaveAgent(context.Background(), in); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	c, err := e.s.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled != (c.ConsentAt != nil) {
		t.Errorf("enabled = %v but consent_at = %v: they must change together", c.Enabled, c.ConsentAt)
	}
}

func TestAgentSettingsContextCancelledWhileWaitingForTheLock(t *testing.T) {
	e := newAgentSettingsEnv(t)
	unlock, err := e.s.mu.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.s.SaveAgent(ctx, validAgentInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestAgentSettingsImplementsBothInterfaces(t *testing.T) {
	e := newAgentSettingsEnv(t)
	var _ AgentSettings = e.s
	var _ AgentConfigReader = e.s
	// DisableFastpath のシグネチャ（Agent のスライスが依存する）。
	if err := e.s.DisableFastpath(context.Background(), domain.Actor{}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSettingsAuditDetailIsValidJSON(t *testing.T) {
	e := newAgentSettingsEnv(t)
	e.enableAgent(t)
	_ = e.s.DeleteOpenAIKey(context.Background())
	for _, en := range e.audit.Entries() {
		if len(en.DetailJSON) > 0 && !json.Valid(en.DetailJSON) {
			t.Errorf("%s: invalid detail %s", en.Event, en.DetailJSON)
		}
	}
}
