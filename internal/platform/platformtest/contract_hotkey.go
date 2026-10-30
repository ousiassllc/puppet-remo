package platformtest

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// hotkeySpec は通常は使われない組み合わせ（スモークで他のアプリと衝突しにくいもの）。
const hotkeySpec = "Ctrl+Alt+Shift+F23"

// RunHotkeyContract は platform.Hotkey の契約を検査する。キーの押下は模擬できないので、登録と解除だけを検査する
// （押下で fn が呼ばれることは、実機では手動で確かめる）。
func RunHotkeyContract(t *testing.T, newHotkey func(t *testing.T) platform.Hotkey) {
	t.Helper()
	noop := func() {}

	t.Run("Register returns an unregister that is safe to call twice", func(t *testing.T) {
		h := newHotkey(t)
		unregister, err := h.Register(hotkeySpec, noop)
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		if unregister == nil {
			t.Fatal("Register returned a nil unregister")
		}
		unregister()
		unregister()
	})

	t.Run("a registered spec cannot be registered again until unregistered", func(t *testing.T) {
		h := newHotkey(t)
		unregister, err := h.Register(hotkeySpec, noop)
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		defer unregister()
		if again, err := h.Register("ctrl+alt+shift+f23", noop); err == nil {
			again()
			t.Error("registering the same spec twice succeeded")
		}
		unregister()
		reregistered, err := h.Register(hotkeySpec, noop)
		if err != nil {
			t.Fatalf("Register after unregister: %v", err)
		}
		reregistered()
	})

	t.Run("invalid arguments", func(t *testing.T) {
		h := newHotkey(t)
		for _, spec := range []string{"", "ctrl+", "+k", "ctrl++k"} {
			_, err := h.Register(spec, noop)
			requireCode(t, err, domain.ErrInvalidArgument, "Register("+spec+")")
		}
		_, err := h.Register(hotkeySpec, nil)
		requireCode(t, err, domain.ErrInvalidArgument, "Register(nil fn)")
	})
}
