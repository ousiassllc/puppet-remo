package gui_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

// 設定変更を監査に書けなかったとき（#174・#182）、どの項目でも 503 audit_unavailable のアラートを返す。元に戻せたときは「変更しませんでした」、
// 戻す書き込みも失敗したときは、現在の状態を確認するよう伝える警告文にする。

func auditFailure(subject string, restored bool) error {
	err := domain.ErrAuditUnavailable.WithDetail(service.DetailAuditSubject, subject)
	if !restored {
		err = err.WithDetail(service.DetailAuditRestored, false)
	}
	return err
}

func TestSettingChangesAreRefusedWith503WhenTheAuditCannotBeWritten(t *testing.T) {
	appSeed := func(f *guitest.Fakes) {
		f.Apps.Rows = []domain.LaunchableApp{{ID: "a1", Label: "Notepad", ExePath: `C:\Windows\notepad.exe`}}
		f.Apps.Denied = []domain.DeniedApp{{ID: "d1", Kind: domain.DeniedName, Value: "cmd.exe"}}
	}
	tests := []struct {
		name    string
		subject string
		label   string // アラートに入る対象の呼び名
		inject  func(f *guitest.Fakes, err error)
		method  string
		target  string
		form    url.Values
		seed    func(*guitest.Fakes)
	}{
		{"launch mode", service.AuditSubjectLaunchMode, "起動の方式", func(f *guitest.Fakes, err error) { f.Apps.ModeSwitchedThenErr = err },
			http.MethodPut, "/apps/mode", url.Values{"mode": {"denylist"}}, nil},
		{"launchable app create", service.AuditSubjectLaunchableApp, "起動許可リスト", func(f *guitest.Fakes, err error) { f.Apps.WriteErr = err },
			http.MethodPost, "/apps", appForm("Calc", `C:\Windows\calc.exe`, "", ""), appSeed},
		{"launchable app update", service.AuditSubjectLaunchableApp, "起動許可リスト", func(f *guitest.Fakes, err error) { f.Apps.WriteErr = err },
			http.MethodPut, "/apps/a1", appForm("Notepad2", `C:\Windows\notepad.exe`, "", ""), appSeed},
		{"launchable app delete", service.AuditSubjectLaunchableApp, "起動許可リスト", func(f *guitest.Fakes, err error) { f.Apps.WriteErr = err },
			http.MethodDelete, "/apps/a1", nil, appSeed},
		{"denylist create", service.AuditSubjectLaunchDenylist, "拒否リスト", func(f *guitest.Fakes, err error) { f.Apps.WriteErr = err },
			http.MethodPost, "/apps/denied", url.Values{"value": {"powershell.exe"}}, appSeed},
		{"denylist update", service.AuditSubjectLaunchDenylist, "拒否リスト", func(f *guitest.Fakes, err error) { f.Apps.WriteErr = err },
			http.MethodPut, "/apps/denied/d1", url.Values{"value": {"x.exe"}}, appSeed},
		{"denylist delete", service.AuditSubjectLaunchDenylist, "拒否リスト", func(f *guitest.Fakes, err error) { f.Apps.WriteErr = err },
			http.MethodDelete, "/apps/denied/d1", nil, appSeed},
		{"host settings", service.AuditSubjectHostSettings, "設定", func(f *guitest.Fakes, err error) { f.Settings.SaveErr = err },
			http.MethodPut, "/settings", validSettings(), nil},
		{"agent settings", service.AuditSubjectAgentSettings, "エージェントの設定", func(f *guitest.Fakes, err error) { f.AgentSettings.SaveErr = err },
			http.MethodPut, "/agent", validAgentForm(), registered(true, true)},
		{"fastpath settings", service.AuditSubjectFastpath, "高速パスの設定", func(f *guitest.Fakes, err error) { f.AgentSettings.SaveErr = err },
			http.MethodPut, "/agent/fastpath", validFastpathForm(), registered(true, true)},
		{"openai key set", service.AuditSubjectAPIKey, "API キーの登録状態", func(f *guitest.Fakes, err error) { f.AgentSettings.SetKeyErr = err },
			http.MethodPut, "/agent/api-key", url.Values{"api_key": {"sk-test-0123456789"}}, registered(true, true)},
		{"typesafe key set", service.AuditSubjectAPIKey, "API キーの登録状態", func(f *guitest.Fakes, err error) { f.AgentSettings.SetKeyErr = err },
			http.MethodPut, "/agent/fastpath/api-key", url.Values{"api_key": {"ts-test-0123456789"}}, registered(true, true)},
		{"openai key delete", service.AuditSubjectAPIKey, "API キーの登録状態", func(f *guitest.Fakes, err error) { f.AgentSettings.DeleteKeyErr = err },
			http.MethodDelete, "/agent/api-key", nil, registered(true, true)},
		{"typesafe key delete", service.AuditSubjectAPIKey, "API キーの登録状態", func(f *guitest.Fakes, err error) { f.AgentSettings.DeleteKeyErr = err },
			http.MethodDelete, "/agent/fastpath/api-key", nil, registered(true, true)},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/restored", func(t *testing.T) {
			r := newRig(t, withSeed(tc.seed), func(f *guitest.Fakes) { tc.inject(f, auditFailure(tc.subject, true)) })
			res := r.do(tc.method, tc.target, tc.form, hx...)
			status(t, res, http.StatusServiceUnavailable)
			contains(t, res, `data-error-code="audit_unavailable"`, "監査ログに書き込めないため、"+tc.label+"は変更しませんでした。")
			lacks(t, res, "元に戻すこともできません")
		})
		t.Run(tc.name+"/rollback failed", func(t *testing.T) {
			r := newRig(t, withSeed(tc.seed), func(f *guitest.Fakes) { tc.inject(f, auditFailure(tc.subject, false)) })
			res := r.do(tc.method, tc.target, tc.form, hx...)
			status(t, res, http.StatusServiceUnavailable)
			contains(t, res, `data-error-code="audit_unavailable"`, "監査ログに書き込めず、"+tc.label+"を元に戻すこともできませんでした。現在の状態を確認してください。")
			lacks(t, res, "変更しませんでした")
		})
	}
}

func withSeed(seed func(*guitest.Fakes)) func(*guitest.Fakes) {
	return func(f *guitest.Fakes) {
		if seed != nil {
			seed(f)
		}
	}
}
