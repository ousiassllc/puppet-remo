package gui_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

func TestAppsPageShowsAllowlistModeByDefault(t *testing.T) {
	res := newRig(t).get("/apps")
	status(t, res, http.StatusOK)
	contains(t, res, `id="launch-mode"`, `data-launch-mode="allowlist"`, `data-mode-note="allowlist"`, "ホワイトリスト方式（既定）",
		"ブラックリスト方式に切り替える", `hx-get="/apps/mode/confirm?mode=denylist"`,
		// 拒否リストは、ホワイトリストでは使われない旨を添えて常に見える。
		`id="denied-section"`, `data-denied-active="false"`, "data-denied-inactive", `data-empty="denied"`, "登録 0 / 200 件",
		`id="denied-form-new"`, `hx-post="/apps/denied"`, `name="value"`)
	lacks(t, res, `data-mode-note="denylist"`, "ホワイトリスト方式に戻す")
}

func TestAppsPageShowsDenylistModeAndEntries(t *testing.T) {
	res := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Mode = domain.LaunchModeDenylist
		f.Apps.Denied = []domain.DeniedApp{
			{ID: "d1", Kind: domain.DeniedName, Value: "cmd.exe"},
			{ID: "d2", Kind: domain.DeniedPath, Value: `C:\Windows\System32\powershell.exe`},
		}
	}).get("/apps")
	status(t, res, http.StatusOK)
	contains(t, res, `data-launch-mode="denylist"`, `data-mode-note="denylist"`, "ホワイトリスト方式に戻す", `hx-put="/apps/mode"`,
		`data-denied-active="true"`, `data-denied="d1"`, "cmd.exe", "実行ファイル名", `data-denied="d2"`, `C:\Windows\System32\powershell.exe`, "パス",
		`data-denied-count="2"`, "登録 2 / 200 件", `hx-get="/apps/denied/d1/edit"`, `hx-get="/apps/denied/d2/delete/confirm"`)
	lacks(t, res, "data-denied-inactive", "ブラックリスト方式に切り替える")
}

func TestConfirmLaunchModeModalDoesNotSwitch(t *testing.T) {
	r := newRig(t)
	res := r.get("/apps/mode/confirm?mode=denylist", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "ブラックリスト方式に切り替えますか？", "何でも起動できる", `hx-put="/apps/mode"`, `{&#34;mode&#34;:&#34;denylist&#34;}`, `hx-target="#launch-mode"`)
	if len(r.f.Apps.ModeSets) != 0 {
		t.Error("opening the confirmation must not switch the mode")
	}
}

func TestSetLaunchModeSwitchesBothWays(t *testing.T) {
	r := newRig(t)
	res := r.do(http.MethodPut, "/apps/mode", url.Values{"mode": {"denylist"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-launch-mode="denylist"`, `id="denied-section"`, `hx-swap-oob="true"`, `data-denied-active="true"`, "ブラックリスト方式に切り替えました。")
	if got, _ := r.f.Apps.LaunchMode(context.Background()); got != domain.LaunchModeDenylist {
		t.Errorf("mode = %q, want denylist", got)
	}
	res = r.do(http.MethodPut, "/apps/mode", url.Values{"mode": {"allowlist"}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-launch-mode="allowlist"`, `data-denied-active="false"`, "ホワイトリスト方式に戻しました。")
	if len(r.f.Apps.ModeSets) != 2 {
		t.Errorf("mode sets = %v", r.f.Apps.ModeSets)
	}
}

func TestSetLaunchModeRejectsUnknownValues(t *testing.T) {
	r := newRig(t)
	for _, m := range []string{"", "blacklist", "DENYLIST", "whitelist"} {
		res := r.do(http.MethodPut, "/apps/mode", url.Values{"mode": {m}}, hx...)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("mode %q: status = %d, want 422", m, res.StatusCode)
		}
	}
	if len(r.f.Apps.ModeSets) != 0 {
		t.Errorf("an unknown mode was applied: %v", r.f.Apps.ModeSets)
	}
}

func TestDeniedCreateUpdateDelete(t *testing.T) {
	r := newRig(t)
	res := r.post("/apps/denied", url.Values{"value": {"  cmd.exe "}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="denied-section"`, "cmd.exe", "実行ファイル名", `data-denied-count="1"`, "拒否リストに追加しました。")
	lacks(t, res, "<html")
	if len(r.f.Apps.Denied) != 1 || r.f.Apps.Denied[0].Kind != domain.DeniedName {
		t.Fatalf("denied = %+v", r.f.Apps.Denied)
	}
	id := r.f.Apps.Denied[0].ID

	edit := r.get("/apps/denied/"+id+"/edit", hx...)
	status(t, edit, http.StatusOK)
	contains(t, edit, `data-denied-form="edit"`, `hx-put="/apps/denied/`+id+`"`, `value="cmd.exe"`)

	res = r.do(http.MethodPut, "/apps/denied/"+id, url.Values{"value": {`C:\Windows\System32\cmd.exe`}}, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `C:\Windows\System32\cmd.exe`, "パス", "拒否リストを更新しました。")

	modal := r.get("/apps/denied/"+id+"/delete/confirm", hx...)
	status(t, modal, http.StatusOK)
	contains(t, modal, `role="dialog"`, `hx-delete="/apps/denied/`+id+`"`, `hx-target="#denied-section"`, "拒否リストから外しますか？")
	if len(r.f.Apps.Denied) != 1 {
		t.Error("opening the confirmation must not delete")
	}
	res = r.do(http.MethodDelete, "/apps/denied/"+id, nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="denied"`, "拒否リストから外しました。")
	if len(r.f.Apps.Denied) != 0 {
		t.Errorf("denied = %+v, want empty", r.f.Apps.Denied)
	}
}

func TestDeniedValidationErrorsAre422WithTheInputKept(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Denied = []domain.DeniedApp{{ID: "d1", Kind: domain.DeniedName, Value: "cmd.exe"}}
	})
	for _, bad := range []string{"", "notepad", `..\a.exe`, `\\h\s\a.exe`, "CMD.EXE"} {
		res := r.post("/apps/denied", url.Values{"value": {bad}}, hx...)
		status(t, res, http.StatusUnprocessableEntity)
		contains(t, res, `id="denied-section"`, "data-field-error")
	}
	res := r.post("/apps/denied", url.Values{"value": {`C:\x\..\bad.exe`}}, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `value="C:\x\..\bad.exe"`) // 入力は入れたまま戻す
	if len(r.f.Apps.Denied) != 1 {
		t.Errorf("an invalid value was saved: %+v", r.f.Apps.Denied)
	}
	res = r.do(http.MethodPut, "/apps/denied/d1", url.Values{"value": {"x"}}, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-denied-form="edit"`, "data-field-error")
}

func TestDeniedFullIs409AndUnknownIs404(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		for range domain.MaxDeniedApps {
			f.Apps.Denied = append(f.Apps.Denied, domain.DeniedApp{ID: "x" + string(rune('A'+len(f.Apps.Denied)%26)) + string(rune('a'+len(f.Apps.Denied)/26)), Kind: domain.DeniedName, Value: "a" + string(rune('A'+len(f.Apps.Denied)%26)) + string(rune('a'+len(f.Apps.Denied)/26)) + ".exe"})
		}
	})
	res := r.post("/apps/denied", url.Values{"value": {"one-more.exe"}}, hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, "200 件まで")
	for _, target := range []string{"/apps/denied/nope/edit", "/apps/denied/nope/delete/confirm"} {
		status(t, r.get(target, hx...), http.StatusNotFound)
	}
	status(t, r.do(http.MethodDelete, "/apps/denied/nope", nil, hx...), http.StatusNotFound)
	status(t, r.do(http.MethodPut, "/apps/denied/nope", url.Values{"value": {"a.exe"}}, hx...), http.StatusNotFound)
}

func TestDeniedValuesAreEscaped(t *testing.T) {
	res := newRig(t, func(f *guitest.Fakes) {
		f.Apps.Denied = []domain.DeniedApp{{ID: "d1", Kind: domain.DeniedName, Value: `<script>alert(1)</script>.exe`}}
	}).get("/apps")
	status(t, res, http.StatusOK)
	lacks(t, res, `<script>alert(1)`)
}
