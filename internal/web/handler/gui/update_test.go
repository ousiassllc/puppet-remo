package gui_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

func verifiedLatest(f *guitest.Fakes) {
	f.Updates.Current.Latest = &service.UpdateInfo{
		Version: "0.1.1", Tag: "v0.1.1", Asset: "puppet-remo-0.1.1-amd64-installer.exe", Notes: "修正: foo\nbar",
		PublishedAt: f.Clock.T.Add(-time.Hour), Verified: true,
	}
}

func TestUpdatePageNoUpdate(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Updates.Current.LastCheckedAt = f.Clock.T.Add(-time.Minute) })
	res := r.get("/update")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", `id="update-panel"`, `data-field="current-version"`, "0.1.0", "正式版", "ホストが確認してから適用する",
		"2026-10-30 11:59", `data-update="up-to-date"`, `hx-post="/update/check"`, "今すぐ確認",
		`href="/update"`, `aria-current="page"`)
	lacks(t, res, "更新する", `data-update="latest"`, `data-update="error"`, `data-update="key-unset"`)
}

func TestUpdatePageNeverChecked(t *testing.T) {
	res := newRig(t).get("/update")
	contains(t, res, "未確認")
	lacks(t, res, `data-update="up-to-date"`)
}

func TestUpdatePageBoostedAndFragment(t *testing.T) {
	r := newRig(t)
	res := r.get("/update", "HX-Request", "true", "HX-Boosted", "true")
	status(t, res, http.StatusOK)
	contains(t, res, `id="update-panel"`)
	lacks(t, res, "<html")
}

func TestUpdatePageShowsVerifiedUpdate(t *testing.T) {
	r := newRig(t, verifiedLatest)
	res := r.get("/update")
	status(t, res, http.StatusOK)
	contains(t, res, `data-update="latest"`, `data-field="latest-version"`, "0.1.1", "署名を確認済み", "修正: foo", "更新する",
		`hx-get="/update/apply/confirm"`, `hx-target="#modal-root"`)
	lacks(t, res, `data-update="reject"`)
}

func TestUpdatePageUnverifiedUpdateCannotBeApplied(t *testing.T) {
	for reason, want := range map[string]string{
		service.UpdateRejectSignatureInvalid: "署名の検証に失敗",
		service.UpdateRejectSignatureMissing: "署名ファイルがありません",
		service.UpdateRejectVersionMismatch:  "この版・ファイルのものではありません",
		service.UpdateRejectKeyUnset:         "署名鍵が未設定",
	} {
		r := newRig(t, func(f *guitest.Fakes) {
			f.Updates.Current.Latest = &service.UpdateInfo{Version: "0.1.1", RejectReason: reason}
			f.Updates.Current.KeyConfigured = reason != service.UpdateRejectKeyUnset
		})
		res := r.get("/update")
		contains(t, res, `data-update="reject"`, want, "適用できません")
		lacks(t, res, "更新する", "署名を確認済み")
	}
}

func TestUpdatePageKeyNotConfiguredAndDevBuild(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Updates.Current.KeyConfigured = false })
	contains(t, r.get("/update"), `data-update="key-unset"`, "署名鍵が未設定")

	r = newRig(t, func(f *guitest.Fakes) { f.Updates.Current.Updatable, f.Updates.Current.CurrentVersion = false, "dev" })
	res := r.get("/update")
	contains(t, res, `data-update="not-updatable"`, "dev")
	lacks(t, res, "今すぐ確認", "更新する", "新しい版はありません")
}

func TestUpdatePageShowsCheckFailure(t *testing.T) {
	for code, want := range map[string]string{
		service.UpdateErrNotFound:    "非公開の可能性",
		service.UpdateErrNetwork:     "GitHub に接続できませんでした",
		service.UpdateErrBadResponse: "応答を読み取れませんでした",
		service.UpdateErrHTTP:        "確認できませんでした",
	} {
		r := newRig(t, func(f *guitest.Fakes) {
			f.Updates.Current.LastError, f.Updates.Current.LastCheckedAt = code, f.Clock.T
		})
		res := r.get("/update")
		status(t, res, http.StatusOK)
		contains(t, res, `data-update="error"`, want)
		lacks(t, res, `data-update="up-to-date"`)
	}
}

// リリースノートは外部（GitHub）由来の文字列なので、HTML としては解釈しない。
func TestUpdatePageEscapesReleaseNotes(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		verifiedLatest(f)
		f.Updates.Current.Latest.Notes = `<script>alert("xss")</script><img src=x onerror=alert(1)>`
		f.Updates.Current.Latest.Version = `0.1.1"><script>x</script>`
	})
	for _, res := range []resp{r.get("/update"), r.post("/update/check", nil, hx...)} {
		status(t, res, http.StatusOK)
		lacks(t, res, "<script>alert", "<img src=x", `"><script>x`)
		contains(t, res, "&lt;script&gt;alert(&#34;xss&#34;)&lt;/script&gt;")
	}
}

func TestCheckUpdate(t *testing.T) {
	tests := []struct {
		name   string
		result service.UpdateStatus
		want   []string
		lack   []string
	}{
		{"none", service.UpdateStatus{CurrentVersion: "0.1.0", Updatable: true, KeyConfigured: true, Channel: domain.ChannelStable},
			[]string{"新しい版はありません。", `hx-swap-oob="beforeend:#toast-root"`}, []string{"data-error-code"}},
		{"verified", service.UpdateStatus{CurrentVersion: "0.1.0", Updatable: true, KeyConfigured: true, Channel: domain.ChannelStable,
			Latest: &service.UpdateInfo{Version: "0.1.1", Verified: true}},
			[]string{"新しい版 0.1.1 があります。", "更新する"}, nil},
		{"unverified", service.UpdateStatus{CurrentVersion: "0.1.0", Updatable: true, KeyConfigured: true, Channel: domain.ChannelStable,
			Latest: &service.UpdateInfo{Version: "0.1.1", RejectReason: service.UpdateRejectSignatureInvalid}},
			[]string{"署名を確認できないため適用できません。", "署名の検証に失敗"}, []string{"更新する"}},
		{"failure", service.UpdateStatus{CurrentVersion: "0.1.0", Updatable: true, KeyConfigured: true, Channel: domain.ChannelStable,
			LastError: service.UpdateErrNetwork},
			[]string{"更新の確認に失敗しました。", `data-error-code="network"`, "GitHub に接続できませんでした"}, nil},
		{"dev", service.UpdateStatus{CurrentVersion: "dev"}, []string{"更新の対象外"}, []string{"今すぐ確認"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) { res := tc.result; f.Updates.CheckResult = &res })
			res := r.post("/update/check", nil, hx...)
			status(t, res, http.StatusOK)
			contains(t, res, append(tc.want, `id="update-panel"`)...)
			lacks(t, res, append(tc.lack, "<html")...)
			if got := r.f.Updates.Checked; len(got) != 1 || got[0] != domain.ActorHost {
				t.Errorf("Check calls = %v, want one by the host", got)
			}
		})
	}
}

func TestConfirmApplyUpdate(t *testing.T) {
	r := newRig(t, verifiedLatest)
	res := r.get("/update/apply/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "更新を適用しますか？", "0.1.1", `hx-post="/update/apply"`, `hx-target="#update-panel"`, "キャンセル", `data-clear="#modal-root"`)

	// 適用できる更新がなければ、モーダルを出さない。
	r = newRig(t)
	res = r.get("/update/apply/confirm", hx...)
	status(t, res, http.StatusConflict)
	lacks(t, res, `role="dialog"`)
	contains(t, res, "適用できる更新がありません。", `data-error-code="conflict"`)
}

func TestApplyUpdate(t *testing.T) {
	r := newRig(t, verifiedLatest)
	res := r.post("/update/apply", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="update-panel"`, `data-update="applying"`, "更新を開始しました。", `id="modal-root"`)
	lacks(t, res, "<html", `hx-get="/update/apply/confirm"`) // 適用中は「更新する」を出さない
	if got := r.f.Updates.Applied; len(got) != 1 || got[0] != domain.ActorHost {
		t.Errorf("Apply calls = %v, want one by the host", got)
	}
}

func TestApplyUpdateRefusals(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		want []string
	}{
		{"lease held", &service.UpdateBusyError{Reasons: []string{service.UpdateBusyLeaseHeld}}, http.StatusConflict,
			[]string{"いまは更新を適用できません。", "操作権（リース）を保持しているクライアントがいます。", `data-error-code="conflict"`}},
		{"connected and running", &service.UpdateBusyError{Reasons: []string{service.UpdateBusySessions, service.UpdateBusyTasks}}, http.StatusConflict,
			[]string{"接続中のクライアントがいます。", "実行中のタスクがあります。"}},
		{"no update", service.ErrUpdateUnavailable, http.StatusConflict, []string{"適用できる更新がありません。"}},
		{"key unset", service.ErrUpdateKeyNotConfigured, http.StatusConflict, []string{"署名鍵が未設定"}},
		{"installer failed", errors.New(`start installer: C:\Users\x\Temp\puppet-remo-update-1\a.exe: boom`), http.StatusInternalServerError,
			[]string{"インストーラを起動できませんでした。", `data-error-code="internal"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, verifiedLatest, func(f *guitest.Fakes) { f.Updates.ApplyErr = tc.err })
			res := r.post("/update/apply", nil, hx...)
			status(t, res, tc.code)
			contains(t, res, tc.want...)
			lacks(t, res, "puppet-remo-update", "boom", `C:\`) // 内部のパス・エラー文は出さない
			if got := res.Header.Get("HX-Retarget"); got != "#alert-root" {
				t.Errorf("HX-Retarget = %q", got)
			}
			if len(r.f.Updates.Applied) != 0 {
				t.Error("nothing may be applied")
			}
		})
	}
}

func TestUpdateRoutesAreWiredAndRejectWrongMethods(t *testing.T) {
	r := newRig(t)
	for _, target := range []string{"/update/check", "/update/apply"} {
		res := r.get(target)
		if res.StatusCode == http.StatusOK || strings.Contains(res.Body, `id="update-panel"`) {
			t.Errorf("GET %s must not run the action: %d", target, res.StatusCode)
		}
	}
	if len(r.f.Updates.Checked)+len(r.f.Updates.Applied) != 0 {
		t.Error("a GET ran an update action")
	}
}

func TestSidebarHasUpdateEntry(t *testing.T) {
	res := newRig(t).get("/")
	contains(t, res, `href="/update"`, "更新")
}
