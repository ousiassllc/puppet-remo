//go:build windows && smoke

package windows_test

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// 実機（対話デスクトップ）でだけ動く契約テスト。`go test -tags smoke ./internal/platform/windows/ -run 'TestSmoke(UIA|Hotkey)'`。
// UIA は前面ウィンドウに 3 つ以上のノードがあること（メモ帳などを前面に出しておく）が前提。

func TestSmokeUIAutomation(t *testing.T) {
	windows.EnableDPIAwareness()
	platformtest.RunUIAutomationContract(t, func(t *testing.T) platform.UIAutomation {
		wm, err := windows.NewWindowManager()
		if err != nil {
			t.Fatalf("NewWindowManager: %v", err)
		}
		u, err := windows.NewUIAutomation(wm)
		if err != nil {
			t.Fatalf("NewUIAutomation: %v", err)
		}
		t.Cleanup(u.Close)
		return u
	})
}

func TestSmokeHotkey(t *testing.T) {
	platformtest.RunHotkeyContract(t, func(t *testing.T) platform.Hotkey {
		h, err := windows.NewHotkey()
		if err != nil {
			t.Fatalf("NewHotkey: %v", err)
		}
		t.Cleanup(h.Close)
		return h
	})
}
