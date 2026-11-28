package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func newTestPauser(t *testing.T, idle time.Duration) (*HostInputPauser, *ctlClock, *ctlAudit) {
	t.Helper()
	clock, audit := newCtlClock(), &ctlAudit{}
	p, err := NewHostInputPauser(HostInputPauserConfig{Clock: clock, Audit: audit, Policy: HostInputPolicy{Enabled: true, Idle: idle}})
	if err != nil {
		t.Fatal(err)
	}
	return p, clock, audit
}

func hostInputEntries(a *ctlAudit) []repository.AuditEntry {
	var out []repository.AuditEntry
	for _, e := range a.Entries() {
		if e.Kind == "input" && (e.Event == hostInputPausedAuditEvent || e.Event == hostInputResumedAuditEvent) {
			out = append(out, e)
		}
	}
	return out
}

func TestHostInputPauserIsIdleUntilRealInput(t *testing.T) {
	p, _, audit := newTestPauser(t, 3*time.Second)
	if err := p.Check(); err != nil {
		t.Fatalf("Check without host input = %v", err)
	}
	if st := p.Status(); st.Paused || !st.Enabled {
		t.Errorf("Status = %+v, want enabled and not paused", st)
	}
	if got := hostInputEntries(audit); len(got) != 0 {
		t.Errorf("audit = %+v, want none", got)
	}
}

// 実入力で停止し、最後の実入力から待ち時間のあいだ入力がなければ（偽の時計で）自動で再開する。
func TestHostInputPauserPausesOnRealInputAndResumesAfterIdle(t *testing.T) {
	p, clock, audit := newTestPauser(t, 3*time.Second)
	start := clock.Now()
	p.NoteHostInput(platform.HostInputMouse)

	err := p.Check()
	if !errors.Is(err, domain.ErrInputPaused) {
		t.Fatalf("Check after host input = %v, want input_paused", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatal("not a domain error")
	}
	if got, want := de.Detail["resumes_at"], start.Add(3*time.Second).UTC().Format(time.RFC3339Nano); got != want {
		t.Errorf("resumes_at = %v, want %v", got, want)
	}
	if got := de.Detail["retry_after_ms"]; got != int64(3000) {
		t.Errorf("retry_after_ms = %v, want 3000", got)
	}
	st := p.Status()
	if !st.Paused || !st.Since.Equal(start) || !st.ResumesAt.Equal(start.Add(3*time.Second)) || st.ResumesIn != 3*time.Second {
		t.Errorf("Status = %+v", st)
	}

	clock.Advance(2999 * time.Millisecond)
	if !errors.Is(p.Check(), domain.ErrInputPaused) {
		t.Error("resumed 1 ms early")
	}
	if got := p.Status().ResumesIn; got != time.Millisecond {
		t.Errorf("ResumesIn = %v, want 1ms", got)
	}
	clock.Advance(time.Millisecond)
	if err := p.Check(); err != nil {
		t.Errorf("Check after the idle time = %v, want nil", err)
	}

	got := hostInputEntries(audit)
	if len(got) != 2 || got[0].Event != hostInputPausedAuditEvent || got[1].Event != hostInputResumedAuditEvent {
		t.Fatalf("audit = %+v, want paused then resumed", got)
	}
	if got[0].ActorType != domain.ActorHost || got[0].Result != auditOK || !got[0].TS.Equal(start) {
		t.Errorf("paused entry = %+v", got[0])
	}
	if want := `{"idle_sec":3,"source":"mouse"}`; string(got[0].DetailJSON) != want {
		t.Errorf("paused detail = %s, want %s", got[0].DetailJSON, want)
	}
	if want := `{"paused_ms":3000,"reason":"idle"}`; string(got[1].DetailJSON) != want {
		t.Errorf("resumed detail = %s, want %s", got[1].DetailJSON, want)
	}
	if !got[1].TS.Equal(start.Add(3 * time.Second)) {
		t.Errorf("resumed at %v, want the moment the idle time passed", got[1].TS)
	}
}

// 実入力が続くかぎり再開は延び、停止の行は 1 回だけ。
func TestHostInputPauserKeepsPausedWhileInputContinues(t *testing.T) {
	p, clock, audit := newTestPauser(t, 3*time.Second)
	p.NoteHostInput(platform.HostInputMouse)
	for range 4 {
		clock.Advance(2 * time.Second)
		p.NoteHostInput(platform.HostInputKeyboard)
		if !errors.Is(p.Check(), domain.ErrInputPaused) {
			t.Fatal("resumed while host input continues")
		}
	}
	if got := hostInputEntries(audit); len(got) != 1 || got[0].Event != hostInputPausedAuditEvent {
		t.Fatalf("audit = %+v, want a single paused entry", got)
	}
	clock.Advance(3 * time.Second)
	if err := p.Check(); err != nil {
		t.Errorf("Check = %v, want resumed", err)
	}
	if got := hostInputEntries(audit); len(got) != 2 || got[1].Event != hostInputResumedAuditEvent {
		t.Fatalf("audit = %+v", got)
	}
}

// 誰も Check しなくても、Poll が再開の行を（待ち時間が過ぎた時刻で）残す。
func TestHostInputPauserPollRecordsResume(t *testing.T) {
	p, clock, audit := newTestPauser(t, 2*time.Second)
	p.NoteHostInput(platform.HostInputKeyboard)
	clock.Advance(time.Second)
	p.Poll()
	if got := hostInputEntries(audit); len(got) != 1 {
		t.Fatalf("audit = %+v, want only paused before the idle time", got)
	}
	clock.Advance(10 * time.Second)
	p.Poll()
	p.Poll()
	got := hostInputEntries(audit)
	if len(got) != 2 || got[1].Event != hostInputResumedAuditEvent || !got[1].TS.Equal(got[0].TS.Add(2*time.Second)) {
		t.Fatalf("audit = %+v, want one resumed entry at pause + idle", got)
	}
}

// 待ち時間が過ぎたあとの最初の実入力は、古い停止の再開を先に残し、新しい停止を始める。
func TestHostInputPauserInputAfterIdleStartsNewPause(t *testing.T) {
	p, clock, audit := newTestPauser(t, time.Second)
	p.NoteHostInput(platform.HostInputMouse)
	clock.Advance(5 * time.Second)
	p.NoteHostInput(platform.HostInputMouse)
	if !errors.Is(p.Check(), domain.ErrInputPaused) {
		t.Fatal("not paused by the new input")
	}
	var events []string
	for _, e := range hostInputEntries(audit) {
		events = append(events, e.Event)
	}
	want := []string{hostInputPausedAuditEvent, hostInputResumedAuditEvent, hostInputPausedAuditEvent}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] || events[2] != want[2] {
		t.Errorf("events = %v, want %v", events, want)
	}
}

func TestHostInputPauserDisabledIgnoresInputAndDisablingResumes(t *testing.T) {
	p, _, audit := newTestPauser(t, 3*time.Second)
	if err := p.SetPolicy(HostInputPolicy{Enabled: false, Idle: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	p.NoteHostInput(platform.HostInputMouse)
	if err := p.Check(); err != nil || p.Status().Enabled {
		t.Errorf("disabled pauser paused or reports enabled: err=%v status=%+v", err, p.Status())
	}
	if got := hostInputEntries(audit); len(got) != 0 {
		t.Errorf("audit = %+v, want none while disabled", got)
	}

	if err := p.SetPolicy(HostInputPolicy{Enabled: true, Idle: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	p.NoteHostInput(platform.HostInputMouse)
	if !errors.Is(p.Check(), domain.ErrInputPaused) {
		t.Fatal("not paused")
	}
	if err := p.SetPolicy(HostInputPolicy{Enabled: false, Idle: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(); err != nil {
		t.Errorf("Check after disabling = %v, want nil", err)
	}
	got := hostInputEntries(audit)
	if len(got) != 2 || got[1].Event != hostInputResumedAuditEvent || string(got[1].DetailJSON) != `{"paused_ms":0,"reason":"disabled"}` {
		t.Errorf("audit = %+v, want paused then resumed(disabled)", got)
	}
}

func TestHostInputPauserIdleChangeMovesResumeTime(t *testing.T) {
	p, clock, _ := newTestPauser(t, 10*time.Second)
	p.NoteHostInput(platform.HostInputMouse)
	clock.Advance(4 * time.Second)
	if err := p.SetPolicy(HostInputPolicy{Enabled: true, Idle: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(); err != nil {
		t.Errorf("Check = %v, want resumed under the shorter idle time", err)
	}
}

func TestHostInputPauserOnPauseRunsOncePerPause(t *testing.T) {
	p, clock, _ := newTestPauser(t, time.Second)
	calls := 0
	p.SetOnPause(func() { calls++ })
	p.NoteHostInput(platform.HostInputMouse)
	p.NoteHostInput(platform.HostInputMouse)
	if calls != 1 {
		t.Errorf("onPause calls = %d, want 1", calls)
	}
	clock.Advance(2 * time.Second)
	p.NoteHostInput(platform.HostInputKeyboard)
	if calls != 2 {
		t.Errorf("onPause calls = %d, want 2 after a new pause", calls)
	}
}

func TestHostInputPauserValidation(t *testing.T) {
	if _, err := NewHostInputPauser(HostInputPauserConfig{Policy: HostInputPolicy{Enabled: true, Idle: time.Second}}); err == nil {
		t.Error("NewHostInputPauser without a clock succeeded")
	}
	for _, idle := range []time.Duration{0, time.Second - 1, 601 * time.Second} {
		if _, err := NewHostInputPauser(HostInputPauserConfig{Clock: newCtlClock(), Policy: HostInputPolicy{Enabled: true, Idle: idle}}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("idle %v: err = %v, want invalid_argument", idle, err)
		}
	}
}

func TestLoadHostInputPolicy(t *testing.T) {
	ctx := context.Background()
	// 既定: 有効・5 秒。settings が nil でも同じ。
	for _, s := range []repository.Settings{nil, newCtlSettings()} {
		got, err := LoadHostInputPolicy(ctx, s)
		if err != nil || got != (HostInputPolicy{Enabled: true, Idle: 5 * time.Second}) {
			t.Errorf("default policy = %+v, %v", got, err)
		}
	}
	s := newCtlSettings()
	s.put("hostinput.pause_enabled", "false")
	s.put("hostinput.pause_idle_sec", "12")
	if got, err := LoadHostInputPolicy(ctx, s); err != nil || got != (HostInputPolicy{Enabled: false, Idle: 12 * time.Second}) {
		t.Errorf("policy = %+v, %v", got, err)
	}
	for _, raw := range []string{"0", "601", "1.5", `"x"`} {
		bad := newCtlSettings()
		bad.put("hostinput.pause_idle_sec", raw)
		if _, err := LoadHostInputPolicy(ctx, bad); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("idle %s: err = %v, want invalid_argument", raw, err)
		}
	}
}
