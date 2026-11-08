package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// 入力スコープ（#156）のリースへの束縛（LeaseManager.AcquireScoped）。

func TestAcquireScopedBindsTheTargetAndRecordsTargetSet(t *testing.T) {
	te := newTgtEnv(t)
	l := te.acquire(ctlViewer)
	if l.Scope == nil || l.Scope.Target != te.app.Ref() || l.Scope.State != domain.ScopeActive || l.Scope.Members != 1 {
		t.Fatalf("lease scope = %+v, want the target bound as active with 1 member", l.Scope)
	}
	if h, ok := te.leases.Holder(); !ok || h.Scope == nil || h.Scope.Target != te.app.Ref() {
		t.Errorf("Holder = %+v, %v", h, ok)
	}
	got := te.audit.Events()
	if len(got) != 2 || got[0] != "lease.acquire" || got[1] != "lease.target_set" {
		t.Errorf("audit events = %v, want [lease.acquire lease.target_set]", got)
	}
}

// 選べない対象は束縛しない。リースは取れず、入力も届かない。
func TestAcquireScopedRejectsTargetsThatCannotBeSelected(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*tgtEnv) domain.WindowRef // 束縛を求める ref を返す
		wantCode domain.Code
		reason   string
	}{
		{"一覧にない（閉じた）", func(te *tgtEnv) domain.WindowRef {
			r := te.app.Ref()
			te.windows.Remove(platformtest.FakeAppMainID)
			return r
		}, domain.CodeWindowNotFound, "target_closed"},
		{"同じ ID の別のウィンドウ（HWND の再利用）", func(te *tgtEnv) domain.WindowRef {
			r := te.app.Ref()
			te.windows.Replace(platformtest.FakeAppMainID, otherWindow("", platformtest.FakeAppLayout.Main, true))
			return r
		}, domain.CodeWindowNotFound, "target_replaced"},
		{"検証前の UWP（ApplicationFrameWindow）", func(te *tgtEnv) domain.WindowRef {
			te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Class = domain.ClassApplicationFrameWindow })
			w, _ := te.windows.ListAll(context.Background())
			return domain.RefOf(w[0])
		}, domain.CodeInputBlocked, "target_unsupported"},
		{"同一性を確かめる値（PID・開始時刻）が不明", func(te *tgtEnv) domain.WindowRef {
			r := te.app.Ref()
			r.ProcessStart = 0
			return r
		}, domain.CodeInputBlocked, "target_unsupported"},
		{"自ウィンドウ", func(te *tgtEnv) domain.WindowRef {
			te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Own = true })
			return te.app.Ref()
		}, domain.CodeInputBlocked, "own_window"},
		{"昇格ウィンドウ", func(te *tgtEnv) domain.WindowRef {
			te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Elevated = true })
			return te.app.Ref()
		}, domain.CodeInputBlocked, "elevated_window"},
		{"最小化", func(te *tgtEnv) domain.WindowRef {
			te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Minimized = true })
			return te.app.Ref()
		}, domain.CodeInputBlocked, "target_minimized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTgtEnv(t)
			ref := tt.mutate(te)
			_, err := te.leases.AcquireScoped(context.Background(), ctlViewer, ref)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != tt.wantCode || de.Detail["reason"] != tt.reason {
				t.Fatalf("AcquireScoped = %v, want %s with reason %q", err, tt.wantCode, tt.reason)
			}
			if _, ok := te.leases.Holder(); ok {
				t.Error("a lease was granted for a target that cannot be selected")
			}
			if len(injections(te.ctlEnv)) != 0 {
				t.Error("input was injected")
			}
			if got := te.audit.Entries(); len(got) != 1 || got[0].Event != "lease.acquire" || got[0].ErrorCode != string(tt.wantCode) {
				t.Errorf("audit = %+v, want one failed lease.acquire with error_code %s", got, tt.wantCode)
			}
		})
	}
}

// 束縛は固定。変えるには解放して取り直す。
func TestAcquireScopedTargetIsFixedUntilReleased(t *testing.T) {
	te := newTgtEnv(t)
	first := te.acquire(ctlViewer)
	again, err := te.leases.AcquireScoped(context.Background(), ctlViewer, te.app.Ref())
	if err != nil || again.ID != first.ID {
		t.Fatalf("same target: %+v, %v; want the same lease", again, err)
	}

	te.windows.Add(otherWindow("2001", rectOutside, false))
	otherRef := domain.WindowRef{ID: "2001", PID: 7000, ProcessStart: 7_000, Class: "OtherClass"}
	if _, err := te.leases.AcquireScoped(context.Background(), ctlViewer, otherRef); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("another target while holding = %v, want ErrInvalidArgument", err)
	}
	if h, _ := te.leases.Holder(); h.Scope.Target != te.app.Ref() {
		t.Errorf("target = %+v, want it unchanged", h.Scope.Target)
	}

	te.leases.Release(ctlViewer)
	l, err := te.leases.AcquireScoped(context.Background(), ctlViewer, otherRef)
	if err != nil || l.Scope.Target != otherRef {
		t.Fatalf("after release: %+v, %v; want the new target bound", l, err)
	}
}

func TestAcquireScopedOverAnUnscopedLeaseIsRefused(t *testing.T) {
	te := newTgtEnv(t)
	mustAcquire(t, te.ctlEnv, ctlViewer)
	if _, err := te.leases.AcquireScoped(context.Background(), ctlViewer, te.app.Ref()); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("AcquireScoped over an unscoped lease = %v, want ErrInvalidArgument", err)
	}
}

func TestAcquireScopedWithoutWindowManagerIsInvalid(t *testing.T) {
	e := newCtlEnv(t)
	leases, err := NewLeaseManager(LeaseConfig{Clock: e.clock, IDs: &ctlIDs{}, IdleTimeout: domain.DefaultLeaseIdleTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leases.AcquireScoped(context.Background(), ctlViewer, domain.WindowRef{ID: "1", PID: 1, ProcessStart: 1, Class: "C"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("AcquireScoped = %v, want ErrInvalidArgument", err)
	}
	if _, ok := leases.Holder(); ok {
		t.Error("a lease was granted")
	}
}

func TestRequestAcquireScopedBindsTheTargetWithoutApproval(t *testing.T) {
	te := newTgtEnv(t)
	l, pending, err := te.leases.RequestAcquireScoped(context.Background(), ctlViewer, te.app.Ref())
	if err != nil || pending || l.Scope == nil || l.Scope.Target != te.app.Ref() {
		t.Fatalf("RequestAcquireScoped = %+v, %v, %v; want an immediate scoped lease", l, pending, err)
	}
	if _, _, err := te.leases.RequestAcquireScoped(context.Background(), ctlBob, domain.WindowRef{ID: "9"}); err == nil {
		t.Error("an unselectable target must be refused")
	}
}

// 承認が要るクライアントでも、承認されたリースに束縛が引き継がれる（承認の監査 → lease.acquire → lease.target_set）。
func TestAcquireScopedKeepsTheTargetThroughApproval(t *testing.T) {
	le := newLeaseApprovalEnv(t, domain.ApprovalAlways)
	app := platformtest.NewFakeApp(le.windows, le.input, le.clipboard)
	out := make(chan acquireResult, 1)
	go func() {
		l, err := le.leases.AcquireScoped(context.Background(), ctlAlice, app.Ref())
		out <- acquireResult{l, err}
	}()
	select {
	case ev := <-le.approvals:
		p, ok := le.leases.Pending()
		if !ok || p.Scope == nil || p.Scope.Target != app.Ref() || p.Scope.State != domain.ScopeActive {
			t.Fatalf("Pending = %+v, %v; want the target carried by the request", p, ok)
		}
		if err := le.approval.Resolve(ev.Data.(ApprovalEvent).ID, true); err != nil {
			t.Fatal(err)
		}
	case r := <-out:
		t.Fatalf("AcquireScoped returned without approval: %+v", r)
	case <-time.After(approvalWait):
		t.Fatal("approval event was not published")
	}
	r := le.wait(t, out)
	if r.err != nil || r.l.Scope == nil || r.l.Scope.Target != app.Ref() {
		t.Fatalf("AcquireScoped = %+v, %v", r.l, r.err)
	}
	h, ok := le.leases.Holder()
	if !ok || h.Scope == nil || h.Scope.State != domain.ScopeActive {
		t.Errorf("Holder = %+v, %v", h, ok)
	}
	got := le.audit.Events()
	want := []string{"approval.requested", "approval.granted", "lease.acquire", "lease.target_set"}
	if len(got) != len(want) {
		t.Fatalf("audit events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit events = %v, want %v", got, want)
		}
	}
}
