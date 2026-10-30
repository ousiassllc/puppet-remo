package organisms_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

var hiddenAttr = regexp.MustCompile(`\s(hidden|disabled)[\s=>]`)

func has(t *testing.T, html string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(html, s) {
			t.Errorf("missing %q in:\n%s", s, html)
		}
	}
}

func hasNot(t *testing.T, html string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(html, s) {
			t.Errorf("unexpected %q in:\n%s", s, html)
		}
	}
}

// 状態駆動レンダリング: view の Can* が false の操作は、HTML に存在しない（hidden / disabled でも隠さない）。
func TestKillSwitchPanelRendersOnlyAvailableAction(t *testing.T) {
	tests := []struct {
		name string
		v    view.KillSwitch
		has  []string
		not  []string
	}{
		{"neither", view.KillSwitch{}, nil, []string{"/killswitch/trigger", "/killswitch/release", "<button"}},
		{"trigger", view.KillSwitch{CanTrigger: true}, []string{`hx-post="/killswitch/trigger"`}, []string{"/killswitch/release"}},
		{"release", view.KillSwitch{Active: true, CanRelease: true, ConfirmRoute: "/killswitch/release/confirm"},
			[]string{`hx-get="/killswitch/release/confirm"`, `data-killswitch="active"`}, []string{"/killswitch/trigger"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := webtest.Render(t, organisms.KillSwitchPanel(tt.v))
			has(t, out, tt.has...)
			hasNot(t, out, tt.not...)
			// hidden / disabled で隠さない（描画しない）。
			if hiddenAttr.MatchString(out) {
				t.Errorf("hidden/disabled attribute rendered:\n%s", out)
			}
			webtest.AssertCSPCompliantHTML(t, out)
		})
	}
}

func TestLeasePanelIsDisplayOnly(t *testing.T) {
	held := webtest.Render(t, organisms.LeasePanel(view.Lease{Held: true, HolderLabel: "claude", KindLabel: "MCP", Since: "2026-10-30 12:00", IdleRemaining: "5分"}))
	has(t, held, "保持中", "claude", "MCP", "5分")
	idle := webtest.Render(t, organisms.LeasePanel(view.Lease{}))
	has(t, idle, "空き")
	hasNot(t, idle, "保持中", "アイドル解放まで")
	for _, out := range []string{held, idle} {
		hasNot(t, out, "<button", "hx-post", "hx-delete", "revoke", "強制剥奪")
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

func TestClientCardFlagsAndActions(t *testing.T) {
	flags := []view.ClientFlag{
		{Field: "web_enabled", Label: "Web", Enabled: true, ToggleURL: "/clients/c1", NextValue: "false"},
		{Field: "agent_enabled", Label: "エージェント", Enabled: false, ToggleURL: "/clients/c1", NextValue: "true"},
	}
	list := webtest.Render(t, organisms.ClientCard(view.Client{ID: "c1", DisplayName: "pc", Flags: flags, CanDelete: true, CanOpenDetail: true, DetailURL: "/clients/c1", DeleteConfirm: "/clients/c1/delete/confirm"}))
	has(t, list, "無効にする", "有効にする", `hx-get="/clients/c1/delete/confirm"`, `href="/clients/c1"`)
	detail := webtest.Render(t, organisms.ClientCard(view.Client{ID: "c1", DisplayName: "pc", Flags: flags}))
	hasNot(t, detail, "delete/confirm", `href="/clients/c1"`, "削除", "トークンと詳細")
	for _, out := range []string{list, detail} {
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

func TestTokenListRevokeOnlyForActiveTokens(t *testing.T) {
	out := webtest.Render(t, organisms.TokenList(view.TokenList{ClientID: "c1", Items: []view.Token{
		{ID: "a", Label: "active", Prefix: "prm_aaaa", Status: "有効", StatusTone: view.ToneOK, CanRevoke: true, RevokeConfirm: "/clients/c1/tokens/a/revoke/confirm"},
		{ID: "r", Label: "revoked", Prefix: "prm_rrrr", Status: "失効済み", StatusTone: view.ToneDanger},
	}}, false))
	has(t, out, "/tokens/a/revoke/confirm", "prm_aaaa…", "prm_rrrr…")
	hasNot(t, out, "/tokens/r/revoke", "hx-swap-oob")
	webtest.AssertCSPCompliantHTML(t, out)

	oob := webtest.Render(t, organisms.TokenList(view.TokenList{ClientID: "c1"}, true))
	has(t, oob, `hx-swap-oob="true"`, `data-empty="tokens"`)
}

func TestConnectionRequestListApprovalModeOnlyShowsAvailableModes(t *testing.T) {
	out := webtest.Render(t, organisms.ConnectionRequestList(view.RequestList{Rows: []view.RequestRow{{
		ID: "r1", DisplayName: "bob", ApproveURL: "/requests/r1/approve", DenyURL: "/requests/r1/deny",
		Modes: []view.ApprovalModeOption{{Value: "always", Label: "常時承認", Selected: true}},
	}}}))
	has(t, out, `<option value="always" selected>常時承認</option>`, `hx-post="/requests/r1/approve"`, `hx-post="/requests/r1/deny"`)
	hasNot(t, out, "first_time", "auto", "初回のみ")
	webtest.AssertCSPCompliantHTML(t, out)

	empty := webtest.Render(t, organisms.ConnectionRequestList(view.RequestList{}))
	has(t, empty, `data-empty="requests"`)
	hasNot(t, empty, "<button", "/approve")
}

func TestSettingsFormErrorsAndAutostart(t *testing.T) {
	form := view.SettingsForm{
		Values: view.SettingsValues{Port: "80", Hotkey: "K", ScreenshotMaxDim: "1568", LeaseIdleSec: "300"},
		Errors: view.SettingsErrors{Port: "範囲外", Hotkey: "書式"},
	}
	out := webtest.Render(t, organisms.SettingsForm(form))
	has(t, out, `data-field-error="set-port"`, "範囲外", `data-field-error="set-hotkey"`, "書式", `value="80"`, `aria-invalid`)
	hasNot(t, out, `data-field-error="set-maxdim"`, `data-field-error="set-idle"`, `name="autostart"`)

	form.CanSetAutostart = true
	form.Values.Autostart = true
	has(t, webtest.Render(t, organisms.SettingsForm(form)), `name="autostart"`, "checked")
	webtest.AssertCSPCompliantHTML(t, out)
}

// 起動許可アプリのフォーム: 追加は POST で一覧を差し替え、編集は PUT で自身を差し替える。422 はどちらもフォーム自身に出す。
// 注意書きは追加・編集のどちらにも常に出る。キャンセルは編集だけ。
func TestAppFormModesAndNotice(t *testing.T) {
	add := webtest.Render(t, organisms.AppForm(view.AppForm{DOMID: "app-form-new", ActionURL: "/apps", PickURL: "/apps/pick-file"}, false))
	has(t, add, `data-app-form="new"`, `hx-post="/apps"`, `hx-target="#app-list"`, `hx-target-422="this"`, `data-notice="interpreter"`,
		`id="app-form-new-exe-path"`, `hx-post="/apps/pick-file"`, `hx-target="closest [data-field=exe_path]"`, `<textarea`, "追加")
	hasNot(t, add, "hx-put", "キャンセル", "hx-swap-oob", "data-field-error")

	edit := webtest.Render(t, organisms.AppForm(view.AppForm{
		ID: "a1", DOMID: "app-form-a1", ActionURL: "/apps/a1", PickURL: "/apps/pick-file", CancelURL: "/apps",
		Values: view.AppFormValues{Label: "メモ帳", ExePath: `C:\x.exe`, Args: "a\nb"},
		Errors: view.AppFormErrors{ExePath: "見つかりません", Args: "多すぎます"},
	}, false))
	has(t, edit, `data-app-form="edit"`, `hx-put="/apps/a1"`, `hx-target="this"`, `data-notice="interpreter"`, "キャンセル", `hx-get="/apps"`,
		`data-field-error="app-form-a1-exe-path"`, "見つかりません", `data-field-error="app-form-a1-args"`, "多すぎます", "a\nb</textarea>", `value="メモ帳"`)
	hasNot(t, edit, "hx-post=\"/apps\"", `data-field-error="app-form-a1-label"`)
	webtest.AssertCSPCompliantHTML(t, add)
	webtest.AssertCSPCompliantHTML(t, edit)

	oob := webtest.Render(t, organisms.AppForm(view.AppForm{DOMID: "app-form-new", ActionURL: "/apps"}, true))
	has(t, oob, `id="app-form-new"`, `hx-swap-oob="true"`)
}

// 一覧: 空のときは案内だけ。行にはパス・引数・作業ディレクトリを出し、Form のある行はカードの代わりに編集フォーム。
func TestAppListRowsAndEditingRow(t *testing.T) {
	empty := webtest.Render(t, organisms.AppList(view.AppList{Max: 50}))
	has(t, empty, `data-empty="apps"`, `data-app-count="0"`)
	hasNot(t, empty, "<article")

	form := view.AppForm{ID: "a2", DOMID: "app-form-a2", ActionURL: "/apps/a2", PickURL: "/apps/pick-file", CancelURL: "/apps"}
	out := webtest.Render(t, organisms.AppList(view.AppList{Count: 2, Max: 50, Rows: []view.AppRow{
		{ID: "a1", Label: "メモ帳", ExePath: `C:\n.exe`, Args: []string{"--x", "y z"}, WorkingDir: `C:\w`, EditURL: "/apps/a1/edit", DeleteConfirm: "/apps/a1/delete/confirm"},
		{ID: "a2", Label: "電卓", ExePath: `C:\c.exe`, EditURL: "/apps/a2/edit", DeleteConfirm: "/apps/a2/delete/confirm", Form: &form},
	}}))
	has(t, out, `id="app-a1"`, "--x", "y z", `C:\w`, `hx-get="/apps/a1/edit"`, `hx-get="/apps/a1/delete/confirm"`, `id="app-form-a2"`, `data-app-count="2"`)
	hasNot(t, out, `id="app-a2"`, `data-empty`)
	// 引数も作業ディレクトリも無い行は、その旨を出す。
	card := webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a3", Label: "x", ExePath: `C:\c.exe`}))
	has(t, card, "なし", "実行ファイルのあるディレクトリ")
	hasNot(t, card, "data-app-args", "data-app-dir")
	// ユーザー入力はエスケープされる。
	evil := webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a4", Label: `<script>alert(1)</script>`, ExePath: `"><script>x</script>`, Args: []string{`<script>`}}))
	hasNot(t, evil, "<script>")
}

func TestSidebarHasTheAppsEntry(t *testing.T) {
	out := webtest.Render(t, organisms.Sidebar(view.Chrome{Active: view.NavApps}, false))
	has(t, out, `href="/apps"`, "起動許可アプリ")
	if strings.Count(out, `aria-current="page"`) != 1 || !strings.Contains(out[strings.Index(out, `href="/apps"`):], `aria-current="page"`) {
		t.Errorf("the apps entry must be the current page:\n%s", out)
	}
}

func TestHeaderAndBadges(t *testing.T) {
	has(t, webtest.Render(t, organisms.HeaderKillSwitch(true)), "キルスイッチ発動中")
	has(t, webtest.Render(t, organisms.HeaderKillSwitch(false)), "稼働中")
	if out := webtest.Render(t, organisms.RequestsBadge(0)); strings.TrimSpace(out) != "" {
		t.Errorf("badge with no requests must render nothing: %q", out)
	}
	has(t, webtest.Render(t, organisms.RequestsBadge(3)), `data-pending-count="3"`)
	has(t, webtest.Render(t, organisms.RequestsBadgeOOB(3)), `id="nav-requests-badge"`, `hx-swap-oob="innerHTML"`)
}

// 描画時の値は常にエスケープされる（Templ の既定）。
func TestUserControlledValuesAreEscaped(t *testing.T) {
	const evil = `"><script>alert(1)</script>`
	out := webtest.Render(t, organisms.ConnectionRequestList(view.RequestList{Rows: []view.RequestRow{{
		ID: "r1", DisplayName: evil, LoginName: evil, RemoteIP: evil, ApproveURL: "/requests/r1/approve", DenyURL: "/requests/r1/deny",
	}}}))
	hasNot(t, out, "<script>alert")
	webtest.AssertCSPCompliantHTML(t, out)
	out = webtest.Render(t, organisms.SessionListBody(view.Sessions{Items: []view.Session{{ClientLabel: evil, KindLabel: "MCP"}}}))
	hasNot(t, out, "<script>alert")
}

// GET /poll が返す OOB のフラグメント: 差し込み先の id と hx-swap-oob を持ち、SSE の属性を持たない。
func TestPollFragmentsAreOOBSwaps(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
	}{
		{"server-status", webtest.Render(t, organisms.ServerStatusOOB(view.ServerStatus{Port: 8765})), []string{`id="server-status" hx-swap-oob="innerHTML"`}},
		{"sessions", webtest.Render(t, organisms.SessionListOOB(view.Sessions{})), []string{`id="sessions" hx-swap-oob="innerHTML"`}},
		{"lease", webtest.Render(t, organisms.LeasePanelOOB(view.Lease{})), []string{`id="lease" hx-swap-oob="innerHTML"`}},
		{"killswitch panel", webtest.Render(t, organisms.KillSwitchPanelOOB(view.KillSwitch{CanTrigger: true, Rev: "r1"})),
			[]string{`id="killswitch-panel"`, `hx-swap-oob="true"`, `name="rev_killswitch" value="r1" data-poll-rev`}},
		{"request list", webtest.Render(t, organisms.ConnectionRequestListOOB(view.RequestList{Rev: "r2"})),
			[]string{`id="request-list"`, `hx-swap-oob="true"`, `name="rev_requests" value="r2" data-poll-rev`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			has(t, tt.html, tt.want...)
			hasNot(t, tt.html, "sse-swap", "sse:", "sse-connect")
			webtest.AssertCSPCompliantHTML(t, tt.html)
		})
	}
	// ページ内の通常の描画（OOB ではない）には hx-swap-oob を付けない。
	hasNot(t, webtest.Render(t, organisms.KillSwitchPanel(view.KillSwitch{})), "hx-swap-oob")
	hasNot(t, webtest.Render(t, organisms.ConnectionRequestList(view.RequestList{})), "hx-swap-oob")
}
