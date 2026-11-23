package pages_test

import (
	"testing"

	"github.com/a-h/templ"

	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// 見出しの階層（#155）: どのページも h1 がちょうど 1 つで、h1 → h3 のような飛びがない。
// 項目（クライアント・アプリ・接続要求）が並ぶ場所は、見せない h2（sr-only）を挟む。ホスト GUI とリモート UI の全ページを、
// 項目・パネルが出る状態（空でない一覧・操作権あり）で描画して検査する。
func TestEveryPageHasAConsistentHeadingOutline(t *testing.T) {
	chrome := view.Chrome{Active: view.NavDashboard}
	client := view.Client{ID: "c1", DisplayName: "pc", Flags: []view.ClientFlag{{Field: "can_view", Label: "表示"}}}
	listed := client
	listed.CanDelete, listed.CanOpenDetail, listed.DetailURL = true, true, "/clients/c1"
	appForm := view.AppForm{DOMID: "app-form-new", ActionURL: "/apps", PickURL: "/apps/pick-file"}
	editForm := view.AppForm{DOMID: "app-form-a2", ActionURL: "/apps/a2", PickURL: "/apps/pick-file"}

	tests := []struct {
		name string
		comp templ.Component
		want string // 見出しのレベル列
	}{
		{"dashboard", pages.Dashboard(view.Dashboard{Chrome: chrome, Lease: view.Lease{Held: true, CanRevoke: true}, KillSwitch: view.KillSwitch{CanTrigger: true}}), "1 2 2 2 2"},
		{"requests", pages.Requests(view.Requests{Chrome: chrome, List: view.RequestList{Rows: []view.RequestRow{{ID: "r1", DisplayName: "d"}}}}), "1 2 3"},
		{"requests history", pages.RequestsHistory(view.RequestsHistory{Chrome: chrome}), "1"},
		{"logs", pages.Logs(view.AppLogPage{Chrome: chrome}), "1"},
		{"clients", pages.Clients(view.Clients{Chrome: chrome, List: view.ClientList{Items: []view.Client{listed}}}), "1 2 3"},
		{"client detail", pages.ClientDetail(view.ClientDetail{Chrome: chrome, Client: client, BackURL: "/clients"}), "1 2"},
		{"settings", pages.Settings(view.Settings{Chrome: chrome}), "1"},
		{"apps", pages.Apps(view.Apps{Chrome: chrome, NewForm: appForm,
			List:   view.AppList{Rows: []view.AppRow{{ID: "a1", Label: "x"}, {ID: "a2", Label: "y", Form: &editForm}}},
			Denied: view.DeniedSection{}}), "1 2 2 3 2 2 2 2"},
		{"agent", pages.AgentSettings(view.AgentSettings{Chrome: chrome}), "1 2 2"},
		{"update", pages.Update(view.Update{Chrome: chrome}), "1 2"},
		{"audit", pages.Audit(view.AuditListPage{Chrome: chrome}), "1"},
		{"audit detail", pages.AuditDetail(view.AuditDetail{Chrome: chrome, ID: 1, BackURL: "/audit", ThumbURL: "/audit/1/thumbnail", Args: []view.AuditArg{{Key: "k", Value: "v"}}}), "1 2 2"},
		{"tasks", pages.Tasks(view.TaskListPage{Chrome: chrome, List: sampleTaskList}), "1"},
		{"task detail", pages.TaskDetail(sampleTaskDetail(chrome)), "1 2 2 2"},
		{"error", pages.Error(view.ErrorPage{Chrome: chrome, Code: "not_found", Title: "x"}), "1"},
		{"approval dialog", pages.ApprovalDialog(view.ApprovalDialog{Heading: "承認"}), "1"},
		{"viewer", pages.Viewer(view.Viewer{ViewerID: "V", WSURL: "ws://h/ws/screen",
			Lease: view.ViewerLease{Mine: true, Held: true, CanRelease: true, Task: &view.ViewerTask{Status: "実行中"}},
			Ops:   view.ViewerOps{Enabled: true}, Picker: view.WindowPicker{CanSelect: true}}), "1 2 3 2 2 2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := webtest.AssertHeadingOutline(t, webtest.Render(t, tc.comp))
			if got != tc.want {
				t.Errorf("heading levels = %q, want %q", got, tc.want)
			}
		})
	}
}
