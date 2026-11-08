package domain_test

import (
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

var scopeTarget = domain.WindowRef{ID: "100", PID: 10, ProcessStart: 1000, Class: "Notepad"}

func scopeOf(target domain.WindowRef) domain.LeaseScope {
	return domain.LeaseScope{Target: target, Process: "notepad.exe", Members: 1}
}

func TestLeaseMachineAcquireScopedBindsTargetAsActive(t *testing.T) {
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	l, events, err := m.AcquireScoped(mcpActor("a"), scopeOf(scopeTarget))
	if err != nil {
		t.Fatal(err)
	}
	if l.Scope == nil || l.Scope.Target != scopeTarget || l.Scope.State != domain.ScopeActive {
		t.Fatalf("scope = %+v, want the target bound as active", l.Scope)
	}
	if len(events) != 1 || events[0].To != domain.LeaseHeld || events[0].Lease.Scope == nil || events[0].ScopeChange() {
		t.Fatalf("events = %+v, want one idle → held acquire event carrying the scope", events)
	}
	if got, _ := m.Holder(); got.Scope == nil || got.Scope.Target != scopeTarget {
		t.Errorf("Holder().Scope = %+v", got.Scope)
	}
}

func TestLeaseMachineScopedTargetIsFixed(t *testing.T) {
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	first, _, err := m.AcquireScoped(mcpActor("a"), scopeOf(scopeTarget))
	if err != nil {
		t.Fatal(err)
	}
	again, events, err := m.AcquireScoped(mcpActor("a"), scopeOf(scopeTarget))
	if err != nil || again.ID != first.ID || len(events) != 0 {
		t.Errorf("same target: lease %q events %v err %v, want the same lease and no event", again.ID, events, err)
	}
	other := scopeTarget
	other.ID = "200"
	if _, _, err := m.AcquireScoped(mcpActor("a"), scopeOf(other)); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("a different target while holding = %v, want ErrInvalidArgument (release and acquire again)", err)
	}
	if _, _, err := m.AcquireScoped(mcpActor("b"), scopeOf(scopeTarget)); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("another session = %v, want ErrLeaseHeldByOther", err)
	}

	m2, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	mustAcquire(t, m2, mcpActor("a"))
	if _, _, err := m2.AcquireScoped(mcpActor("a"), scopeOf(scopeTarget)); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("scoped acquire over an unscoped lease = %v, want ErrInvalidArgument", err)
	}
}

func TestLeaseMachineScopeSurvivesApproval(t *testing.T) {
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	p, _, err := m.RequestScoped(mcpActor("a"), scopeOf(scopeTarget))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := m.Pending(); !ok || got.Scope == nil || got.Scope.Target != scopeTarget {
		t.Fatalf("Pending().Scope = %+v", got.Scope)
	}
	l, _, err := m.Approve(p.ID)
	if err != nil || l.Scope == nil || l.Scope.Target != scopeTarget || l.Scope.State != domain.ScopeActive {
		t.Fatalf("approved lease = %+v, %v; want the scope bound as active", l, err)
	}
}

func TestLeaseMachineHaltAndResume(t *testing.T) {
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	l, _, err := m.AcquireScoped(mcpActor("a"), scopeOf(scopeTarget))
	if err != nil {
		t.Fatal(err)
	}

	if ev := m.Halt("other-lease", domain.ReasonTargetNotForeground); len(ev) != 0 {
		t.Errorf("Halt of another lease id = %v, want nothing", ev)
	}
	ev := m.Halt(l.ID, domain.ReasonTargetNotForeground)
	if len(ev) != 1 || !ev[0].ScopeChange() || !ev[0].Lease.Scope.Halted() || ev[0].Lease.Scope.HaltReason != domain.ReasonTargetNotForeground {
		t.Fatalf("Halt events = %+v", ev)
	}
	if ev := m.Halt(l.ID, domain.ReasonOutsideTargetGroup); len(ev) != 0 {
		t.Errorf("Halt of a halted lease = %v, want nothing (the first reason stays)", ev)
	}
	if h, _ := m.Holder(); h.Scope.HaltReason != domain.ReasonTargetNotForeground {
		t.Errorf("HaltReason = %q, want the first reason", h.Scope.HaltReason)
	}
	// 再取得では再開しない。
	if again, _, err := m.AcquireScoped(mcpActor("a"), scopeOf(scopeTarget)); err != nil || !again.Scope.Halted() {
		t.Errorf("re-acquire = %+v, %v; want the lease to stay halted", again.Scope, err)
	}

	if _, err := m.Resume(mcpActor("b")); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("Resume by another session = %v, want ErrLeaseHeldByOther", err)
	}
	events, err := m.Resume(mcpActor("a"))
	if err != nil || len(events) != 1 || !events[0].ScopeChange() || events[0].Lease.Scope.State != domain.ScopeActive || events[0].Lease.Scope.HaltReason != "" {
		t.Fatalf("Resume = %+v, %v", events, err)
	}
	if events, err := m.Resume(mcpActor("a")); err != nil || len(events) != 0 {
		t.Errorf("Resume of an active lease = %v, %v; want nothing", events, err)
	}
}

func TestLeaseMachineResumeRefusesReselectOnlyReasons(t *testing.T) {
	for _, reason := range []domain.GroupReason{domain.ReasonTargetClosed, domain.ReasonTargetReplaced, domain.ReasonTargetUnsupported} {
		t.Run(string(reason), func(t *testing.T) {
			m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
			l, _, _ := m.AcquireScoped(mcpActor("a"), scopeOf(scopeTarget))
			m.Halt(l.ID, reason)
			_, err := m.Resume(mcpActor("a"))
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked || de.Detail["reason"] != string(reason) {
				t.Fatalf("Resume = %v, want input_blocked with reason %q", err, reason)
			}
			if h, _ := m.Holder(); !h.Scope.Halted() {
				t.Error("the lease must stay halted")
			}
		})
	}
}

func TestLeaseMachineResumeWithoutScopeOrLease(t *testing.T) {
	m, _ := newMachine(t, domain.DefaultLeaseIdleTimeout)
	if _, err := m.Resume(mcpActor("a")); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Errorf("Resume while idle = %v, want ErrLeaseRequired", err)
	}
	mustAcquire(t, m, mcpActor("a"))
	if _, err := m.Resume(mcpActor("a")); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Resume of an unscoped lease = %v, want ErrInvalidArgument", err)
	}
	if ev := m.Halt("lease-1", domain.ReasonTargetNotForeground); len(ev) != 0 {
		t.Errorf("Halt of an unscoped lease = %v, want nothing", ev)
	}
}

func TestScopedKeyForbidden(t *testing.T) {
	tests := []struct {
		name string
		mods []domain.Modifier
		key  string
		want bool
	}{
		{"win+r", []domain.Modifier{domain.ModWin}, "r", true},
		{"win+shift+s", []domain.Modifier{domain.ModWin, domain.ModShift}, "s", true},
		{"alt+tab", []domain.Modifier{domain.ModAlt}, "tab", true},
		{"alt+esc", []domain.Modifier{domain.ModAlt}, "escape", true},
		{"ctrl+esc", []domain.Modifier{domain.ModCtrl}, "escape", true},
		{"ctrl+shift+esc", []domain.Modifier{domain.ModCtrl, domain.ModShift}, "escape", true},
		{"KeyboardEvent Escape with ctrl", []domain.Modifier{domain.ModCtrl}, "Escape", true},
		{"MetaLeft (win key)", nil, "MetaLeft", true},
		{"plain escape", nil, "escape", false},
		{"shift+escape", []domain.Modifier{domain.ModShift}, "escape", false},
		{"plain tab", nil, "tab", false},
		{"ctrl+tab", []domain.Modifier{domain.ModCtrl}, "tab", false},
		{"ctrl+c", []domain.Modifier{domain.ModCtrl}, "c", false},
		{"alt+f4", []domain.Modifier{domain.ModAlt}, "f4", false},
		{"no key (click modifiers)", []domain.Modifier{domain.ModCtrl}, "", false},
		{"win modifier of a click", []domain.Modifier{domain.ModWin}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.ScopedKeyForbidden(tt.mods, tt.key); got != tt.want {
				t.Errorf("ScopedKeyForbidden(%v, %q) = %v, want %v", tt.mods, tt.key, got, tt.want)
			}
		})
	}
}
