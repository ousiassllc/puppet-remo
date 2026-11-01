package gui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

func validSettings() url.Values {
	return url.Values{
		"server.port": {"9000"}, "killswitch.hotkey": {"Ctrl+Alt+Shift+Q"},
		"screenshot.max_dimension": {"2048"}, "lease.idle_timeout_sec": {"120"}, "viewer.default_quality": {"85"}, "viewer.default_fps": {"24"}, "autostart": {"on"},
		"audit.thumbnails_enabled": {"on"}, "audit.record_text": {"on"}, "audit.retention_days": {"7"},
		"audit.thumbs_max_bytes": {"536870912"}, "update.apply_mode": {"auto_idle"}, "update.channel": {"nightly"},
	}
}

func TestSettingsPage(t *testing.T) {
	r := newRig(t)
	res := r.get("/settings")
	status(t, res, http.StatusOK)
	// ブラウザの入力検証でサーバーの 422 を迂回させない。
	contains(t, res, "<html", ` novalidate`, `hx-put="/settings"`, `hx-target-422="this"`,
		`name="server.port"`, `value="8765"`, `name="killswitch.hotkey"`, `value="Ctrl+Alt+Shift+K"`,
		`name="screenshot.max_dimension"`, `value="1568"`, `name="lease.idle_timeout_sec"`, `value="300"`,
		`name="viewer.default_quality"`, `value="70"`, `name="viewer.default_fps"`, `value="10"`, `name="autostart"`)
	// 監査ログ（FR-S07）と更新（FR-S09）。既定: サムネイルは保存する・本文は記録しない・30 日・1 GiB・ホスト確認。
	contains(t, res, `data-settings-group="audit"`, `data-settings-group="update"`,
		`name="audit.thumbnails_enabled"`, `name="audit.record_text"`,
		`name="audit.retention_days"`, `value="30"`, `name="audit.thumbs_max_bytes"`, `value="1073741824"`,
		`name="update.apply_mode"`, `<option value="confirm" selected>`, `<option value="auto_idle">`,
		`name="update.channel"`, `<option value="stable" selected>`, `<option value="nightly">`)
	if got := strings.Count(res.Body, "checked"); got != 1 {
		t.Errorf("only audit.thumbnails_enabled is checked by default (autostart is off); checked count = %d", got)
	}
	// NFR-S11: サムネイルと本文は平文。BitLocker とサムネイル無効化の案内、本文の記録の警告は常に出す（確認ダイアログは作らない）。
	contains(t, res, `data-notice="audit-plaintext"`, "平文", "BitLocker", "サムネイルの保存を無効", `data-notice="audit-record-text"`, "パスワード")
	lacks(t, res, "data-field-error", "log.level", `role="dialog"`)
	// エージェント・API キーの設定は出さない（M4）。
	lacks(t, res, "OpenAI", "api_key")
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
	contains(t, res, `id="settings-form"`, `value="9000"`, `value="Ctrl+Alt+Shift+Q"`, `value="2048"`, `value="120"`, `value="85"`, `value="24"`, "checked", "設定を保存しました。", `hx-swap-oob="beforeend:#toast-root"`,
		`name="audit.retention_days"`, `value="7"`, `value="536870912"`, `<option value="auto_idle" selected>`, `<option value="nightly" selected>`)
	lacks(t, res, "data-field-error", "<html")
	want := service.HostSettings{Port: 9000, KillSwitchHotkey: "Ctrl+Alt+Shift+Q", ScreenshotMaxDimension: 2048, LeaseIdleTimeoutSec: 120, ViewerDefaultQuality: 85, ViewerDefaultFPS: 24,
		AuditThumbnailsEnabled: true, AuditRecordText: true, AuditRetentionDays: 7, AuditThumbsMaxBytes: 536870912, UpdateApplyMode: service.UpdateApplyAutoIdle, UpdateChannel: "nightly", Autostart: true}
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
		{"quality out of range", func(v url.Values) { v.Set("viewer.default_quality", "101") }, []string{"set-vquality"}, `value="101"`},
		{"quality not a number", func(v url.Values) { v.Set("viewer.default_quality", "high") }, []string{"set-vquality"}, `value="high"`},
		{"fps out of range", func(v url.Values) { v.Set("viewer.default_fps", "0") }, []string{"set-vfps"}, `value="0"`},
		{"fps too high", func(v url.Values) { v.Set("viewer.default_fps", "31") }, []string{"set-vfps"}, `value="31"`},
		{"hotkey without modifier", func(v url.Values) { v.Set("killswitch.hotkey", "K") }, []string{"set-hotkey"}, `value="K"`},
		{"hotkey garbage", func(v url.Values) { v.Set("killswitch.hotkey", "Ctrl+Nope+??") }, []string{"set-hotkey"}, `value="Ctrl+Nope+??"`},
		{"retention too low", func(v url.Values) { v.Set("audit.retention_days", "0") }, []string{"set-audit-days"}, `value="0"`},
		{"retention too high", func(v url.Values) { v.Set("audit.retention_days", "36501") }, []string{"set-audit-days"}, `value="36501"`},
		{"retention not a number", func(v url.Values) { v.Set("audit.retention_days", "abc") }, []string{"set-audit-days"}, `value="abc"`},
		{"thumbs cap too low", func(v url.Values) { v.Set("audit.thumbs_max_bytes", "0") }, []string{"set-audit-cap"}, `value="0"`},
		{"thumbs cap too high", func(v url.Values) { v.Set("audit.thumbs_max_bytes", "9007199254740993") }, []string{"set-audit-cap"}, `value="9007199254740993"`},
		{"thumbs cap beyond int64", func(v url.Values) { v.Set("audit.thumbs_max_bytes", "99999999999999999999") }, []string{"set-audit-cap"}, `value="99999999999999999999"`},
		{"thumbs cap not a number", func(v url.Values) { v.Set("audit.thumbs_max_bytes", "1GiB") }, []string{"set-audit-cap"}, `value="1GiB"`},
		{"apply mode unknown", func(v url.Values) { v.Set("update.apply_mode", "always") }, []string{"set-update-mode"}, ``},
		{"channel unknown", func(v url.Values) { v.Set("update.channel", "beta") }, []string{"set-update-channel"}, ``},
		{"channel missing", func(v url.Values) { v.Del("update.channel") }, []string{"set-update-channel"}, ``},
		{"apply mode missing", func(v url.Values) { v.Del("update.apply_mode") }, []string{"set-update-mode"}, ``},
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

// 422 の再描画は、検証エラーのない項目（チェックボックス・選択を含む）の入力を残す。
func TestSaveSettingsValidationKeepsOtherInputs(t *testing.T) {
	r := newRig(t)
	form := validSettings()
	form.Del("audit.thumbnails_enabled")
	form.Set("audit.retention_days", "0")
	res := r.do(http.MethodPut, "/settings", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="set-audit-days"`, `value="536870912"`, `<option value="auto_idle" selected>`, `name="audit.record_text"`, `name="autostart"`)
	// audit.thumbnails_enabled は外したまま、audit.record_text と autostart はチェックのまま。
	if got := strings.Count(res.Body, "checked"); got != 2 {
		t.Errorf("checked count = %d, want 2 (record_text, autostart)", got)
	}
}

// チェックボックスを外すと off で保存される（外れていると値が送られない）。
func TestSaveSettingsUncheckedAuditBoxes(t *testing.T) {
	r := newRig(t)
	form := validSettings()
	form.Del("audit.thumbnails_enabled")
	form.Del("audit.record_text")
	status(t, r.do(http.MethodPut, "/settings", form, hx...), http.StatusOK)
	if got := r.f.Settings.Values; got.AuditThumbnailsEnabled || got.AuditRecordText {
		t.Errorf("unchecked boxes must be saved as off: %+v", got)
	}
}

// 保存済みの値は HTML エスケープして描画する。
func TestSettingsFormEscapesValues(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Settings.Values.KillSwitchHotkey = `"><script>alert(1)</script>` })
	res := r.get("/settings")
	lacks(t, res, "<script>alert(1)")
	contains(t, res, "&lt;script&gt;")
}
