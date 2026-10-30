package app

import (
	"sync/atomic"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func TestKillSwitchHotkeyBindRebindAndClose(t *testing.T) {
	fake := platformtest.NewFakeHotkey()
	var fired atomic.Int32
	k := NewKillSwitchHotkey(fake, func() { fired.Add(1) })

	if k.Spec() != "" {
		t.Fatalf("Spec before Bind = %q", k.Spec())
	}
	if err := k.Bind("Ctrl+Alt+Shift+K"); err != nil {
		t.Fatal(err)
	}
	if !fake.Trigger("Ctrl+Alt+Shift+K") || fired.Load() != 1 {
		t.Fatalf("the hotkey did not fire the trigger (fired %d)", fired.Load())
	}

	// 同じ spec（大文字小文字違いを含む）は登録し直さない。
	before := len(fake.CallsTo("Register"))
	if err := k.Bind("ctrl+alt+shift+k"); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.CallsTo("Register")); got != before {
		t.Errorf("rebinding the same spec registered again (%d -> %d)", before, got)
	}

	if err := k.Bind("Ctrl+Alt+Shift+L"); err != nil {
		t.Fatal(err)
	}
	if fake.Trigger("Ctrl+Alt+Shift+K") {
		t.Error("the old hotkey is still registered")
	}
	if !fake.Trigger("Ctrl+Alt+Shift+L") || fired.Load() != 2 {
		t.Errorf("the new hotkey did not fire (fired %d)", fired.Load())
	}

	k.Close()
	k.Close() // 複数回呼んでよい
	if fake.Trigger("Ctrl+Alt+Shift+L") || k.Spec() != "" {
		t.Error("Close did not unregister the hotkey")
	}
}

func TestKillSwitchHotkeyBindFailureKeepsTheCurrentRegistration(t *testing.T) {
	fake := platformtest.NewFakeHotkey()
	var fired atomic.Int32
	k := NewKillSwitchHotkey(fake, func() { fired.Add(1) })
	if err := k.Bind("Ctrl+Alt+Shift+K"); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Register("Ctrl+Alt+Shift+J", func() {}); err != nil { // 他のアプリが使用中
		t.Fatal(err)
	}

	if err := k.Bind("Ctrl+Alt+Shift+J"); err == nil {
		t.Fatal("Bind of an unavailable hotkey must fail")
	}
	if k.Spec() != "Ctrl+Alt+Shift+K" || !fake.Trigger("Ctrl+Alt+Shift+K") || fired.Load() != 1 {
		t.Error("a failed rebind must keep the working hotkey")
	}
	if err := k.Bind(""); err == nil {
		t.Error("an empty spec must be rejected")
	}
}
