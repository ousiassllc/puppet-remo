package gui_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

func validSettings() url.Values {
	return url.Values{
		"server.port": {"9000"}, "killswitch.hotkey": {"Ctrl+Alt+Shift+Q"},
		"screenshot.max_dimension": {"2048"}, "lease.idle_timeout_sec": {"120"}, "autostart": {"on"},
	}
}

func TestSettingsPage(t *testing.T) {
	r := newRig(t)
	res := r.get("/settings")
	status(t, res, http.StatusOK)
	// ブラウザの入力検証でサーバーの 422 を迂回させない。
	contains(t, res, "<html", ` novalidate`, `hx-put="/settings"`, `hx-target-422="this"`,
		`name="server.port"`, `value="8765"`, `name="killswitch.hotkey"`, `value="Ctrl+Alt+Shift+K"`,
		`name="screenshot.max_dimension"`, `value="1568"`, `name="lease.idle_timeout_sec"`, `value="300"`, `name="autostart"`)
	lacks(t, res, "data-field-error", "checked")
	// M1 に無い設定（エージェント・監査・更新）は出さない。
	lacks(t, res, "agent", "audit", "update.", "OpenAI", "api_key")
}

// 自動起動の登録手段が無い環境では、チェックボックスを描画しない（状態駆動）。
func TestSettingsAutostartOnlyWhenAvailable(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Settings.CanAutostart = false })
	res := r.get("/settings")
	lacks(t, res, `name="autostart"`, "自動で起動")

	res = r.do(http.MethodPut, "/settings", validSettings(), hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, `name="autostart"`)
	if got := r.f.Settings.Values; got.Autostart {
		t.Errorf("autostart must stay off: %+v", got)
	}

	res = newRig(t).get("/settings")
	contains(t, res, `name="autostart"`, "自動で起動")
}

func TestSaveSettings(t *testing.T) {
	r := newRig(t)
	res := r.do(http.MethodPut, "/settings", validSettings(), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="settings-form"`, `value="9000"`, `value="Ctrl+Alt+Shift+Q"`, `value="2048"`, `value="120"`, "checked", "設定を保存しました。", `hx-swap-oob="beforeend:#toast-root"`)
	lacks(t, res, "data-field-error", "<html")
	want := service.HostSettings{Port: 9000, KillSwitchHotkey: "Ctrl+Alt+Shift+Q", ScreenshotMaxDimension: 2048, LeaseIdleTimeoutSec: 120, Autostart: true}
	if len(r.f.Settings.Saves) != 1 || r.f.Settings.Saves[0] != want {
		t.Errorf("saves = %+v, want [%+v]", r.f.Settings.Saves, want)
	}

	// チェックを外すと off で保存される。
	form := validSettings()
	form.Del("autostart")
	status(t, r.do(http.MethodPut, "/settings", form, hx...), http.StatusOK)
	if r.f.Settings.Values.Autostart {
		t.Error("autostart should be saved as off")
	}
}

func TestSaveSettingsValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(url.Values)
		errors []string // 項目別のエラーを出す入力の id
		keep   string   // 入力されたままで再描画される値
	}{
		{"port too low", func(v url.Values) { v.Set("server.port", "80") }, []string{"set-port"}, `value="80"`},
		{"port too high", func(v url.Values) { v.Set("server.port", "70000") }, []string{"set-port"}, `value="70000"`},
		{"port not a number", func(v url.Values) { v.Set("server.port", "abc") }, []string{"set-port"}, `value="abc"`},
		{"port empty", func(v url.Values) { v.Set("server.port", "") }, []string{"set-port"}, ``},
		{"max dimension out of range", func(v url.Values) { v.Set("screenshot.max_dimension", "100") }, []string{"set-maxdim"}, `value="100"`},
		{"idle timeout out of range", func(v url.Values) { v.Set("lease.idle_timeout_sec", "5") }, []string{"set-idle"}, `value="5"`},
		{"hotkey without modifier", func(v url.Values) { v.Set("killswitch.hotkey", "K") }, []string{"set-hotkey"}, `value="K"`},
		{"hotkey garbage", func(v url.Values) { v.Set("killswitch.hotkey", "Ctrl+Nope+??") }, []string{"set-hotkey"}, `value="Ctrl+Nope+??"`},
		{"several at once", func(v url.Values) {
			v.Set("server.port", "1")
			v.Set("lease.idle_timeout_sec", "x")
			v.Set("killswitch.hotkey", "")
		},
			[]string{"set-port", "set-idle", "set-hotkey"}, `value="1"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			form := validSettings()
			tt.mutate(form)
			res := r.do(http.MethodPut, "/settings", form, hx...)
			status(t, res, http.StatusUnprocessableEntity)
			for _, id := range tt.errors {
				contains(t, res, `data-field-error="`+id+`"`, `role="alert"`)
			}
			if tt.keep != "" {
				contains(t, res, tt.keep)
			}
			// フォームを再描画する（フルページではない）。保存されない。
			contains(t, res, `id="settings-form"`)
			lacks(t, res, "<html")
			if len(r.f.Settings.Saves) != 0 {
				t.Errorf("invalid settings were saved: %+v", r.f.Settings.Saves)
			}
		})
	}
}

func TestSaveSettingsServiceFailure(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Settings.SaveErr = errCleanup })
	res := r.do(http.MethodPut, "/settings", validSettings(), hx...)
	status(t, res, http.StatusInternalServerError)
	contains(t, res, `data-error-code="internal"`)
	lacks(t, res, "cleanup boom", `id="settings-form"`)
}

func TestSettingsPageLoadFailure(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Settings.LoadErr = errCleanup })
	res := r.get("/settings")
	status(t, res, http.StatusInternalServerError)
	contains(t, res, "<html", `data-error-code="internal"`)
	lacks(t, res, "cleanup boom")
	// 保存済みの値が壊れていても、入力で上書きして復旧できる。
	res = r.do(http.MethodPut, "/settings", validSettings(), hx...)
	status(t, res, http.StatusOK)
}
