package windows

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// fakeUIABackend は hwnd ごとの木を返す。
type fakeUIABackend struct {
	trees map[uintptr]func() *fakeUIANode
	opens atomic.Int32
}

func (b *fakeUIABackend) Root(hwnd uintptr) (uiaNode, error) {
	b.opens.Add(1)
	mk, ok := b.trees[hwnd]
	if !ok {
		return nil, domain.ErrWindowNotFound.WithMsg("gone")
	}
	return mk(), nil
}

func uiaFixtureWindows() *platformtest.FakeWindows {
	return platformtest.NewFakeWindows(
		domain.WindowInfo{ID: "100", Title: "Notepad", Process: "notepad.exe", PID: 7, Foreground: true},
		domain.WindowInfo{ID: "200", Title: "Other", Process: "other.exe", PID: 8},
	)
}

func uiaFixtureBackend() *fakeUIABackend {
	return &fakeUIABackend{trees: map[uintptr]func() *fakeUIANode{
		100: func() *fakeUIANode {
			w := uiaTestNode("Notepad", uiaTestNode("File", uiaTestNode("Open")), uiaTestNode("Edit"), uiaTestNode("Help"))
			w.props.ControlType = 50032
			return w
		},
		200: func() *fakeUIANode { return uiaTestNode("Other") },
	}}
}

func newFakeUIAutomation(t *testing.T, windows platform.WindowManager, backend uiaBackend) *UIAutomation {
	t.Helper()
	u, err := newUIAutomation(windows, func() (uiaBackend, func(), error) { return backend, func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.Close)
	return u
}

func TestUIAutomationContract(t *testing.T) {
	platformtest.RunUIAutomationContract(t, func(t *testing.T) platform.UIAutomation {
		return newFakeUIAutomation(t, uiaFixtureWindows(), uiaFixtureBackend())
	})
}

func TestUIAutomationTargetsTheRequestedWindow(t *testing.T) {
	u := newFakeUIAutomation(t, uiaFixtureWindows(), uiaFixtureBackend())
	ctx := context.Background()

	fg, err := u.Tree(ctx, "", 5, 100)
	if err != nil || fg.Name != "Notepad" {
		t.Fatalf("foreground tree = %q, %v; want Notepad", fg.Name, err)
	}
	other, err := u.Tree(ctx, "200", 5, 100)
	if err != nil || other.Name != "Other" {
		t.Fatalf("tree(200) = %q, %v; want Other", other.Name, err)
	}
	snap, err := u.Snapshot(ctx, "200", 100)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Window.ID != "200" || snap.Window.Process != "other.exe" || len(snap.Elements) != 1 || snap.Hash == "" {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestUIAutomationWindowGoneBetweenListAndOpen(t *testing.T) {
	windows := platformtest.NewFakeWindows(domain.WindowInfo{ID: "300", Foreground: true}) // backend に木が無い
	u := newFakeUIAutomation(t, windows, uiaFixtureBackend())
	_, err := u.Tree(context.Background(), "300", 5, 10)
	if !errors.Is(err, domain.ErrWindowNotFound) {
		t.Errorf("error = %v, want ErrWindowNotFound", err)
	}
}

func TestUIAutomationNonHandleWindowID(t *testing.T) {
	windows := platformtest.NewFakeWindows(domain.WindowInfo{ID: "not-a-handle", Foreground: true})
	backend := uiaFixtureBackend()
	u := newFakeUIAutomation(t, windows, backend)
	_, err := u.Snapshot(context.Background(), "", 10)
	if !errors.Is(err, domain.ErrWindowNotFound) {
		t.Errorf("error = %v, want ErrWindowNotFound", err)
	}
	if backend.opens.Load() != 0 {
		t.Error("the backend was opened for a window id that is not a handle")
	}
}

func TestUIAutomationNoForegroundWindow(t *testing.T) {
	u := newFakeUIAutomation(t, platformtest.NewFakeWindows(), uiaFixtureBackend())
	_, err := u.Tree(context.Background(), "", 5, 10)
	if !errors.Is(err, domain.ErrDesktopUnavailable) {
		t.Errorf("error = %v, want the window manager's ErrDesktopUnavailable", err)
	}
}

func TestUIAutomationInvalidLimitsDoNotTouchTheBackend(t *testing.T) {
	backend := uiaFixtureBackend()
	u := newFakeUIAutomation(t, uiaFixtureWindows(), backend)
	if _, err := u.Tree(context.Background(), "", 0, 10); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Tree error = %v", err)
	}
	if _, err := u.Snapshot(context.Background(), "", 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("Snapshot error = %v", err)
	}
	if backend.opens.Load() != 0 {
		t.Error("the backend was opened for invalid limits")
	}
}

func TestUIAutomationCloseIsIdempotentAndStopsCalls(t *testing.T) {
	u, err := newUIAutomation(uiaFixtureWindows(), func() (uiaBackend, func(), error) {
		return uiaFixtureBackend(), func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	u.Close()
	if _, err := u.Tree(context.Background(), "", 5, 10); !errors.Is(err, errUIAClosed) {
		t.Errorf("error after Close = %v, want errUIAClosed", err)
	}
}

func TestUIAutomationOpenFailure(t *testing.T) {
	boom := errors.New("no com")
	if _, err := newUIAutomation(uiaFixtureWindows(), func() (uiaBackend, func(), error) { return nil, nil, boom }); !errors.Is(err, boom) {
		t.Errorf("error = %v, want boom", err)
	}
}
