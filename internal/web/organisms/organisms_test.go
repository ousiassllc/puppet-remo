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

// 状態駆動レンダリング（frontend.md §4.3）: 強制剥奪のボタンは、剥奪できる保持者がいる（CanRevoke）ときだけ HTML に出る。
func TestLeasePanelRendersRevokeOnlyWhenCanRevoke(t *testing.T) {
	held := view.Lease{Held: true, HolderLabel: "claude", KindLabel: "MCP", Since: "2026-10-30 12:00", IdleRemaining: "5分"}
	idle := webtest.Render(t, organisms.LeasePanel(view.Lease{}))
	has(t, idle, "空き")
	hasNot(t, idle, "保持中", "アイドル解放まで")

	revocable := held
	revocable.CanRevoke = true
	canRevoke := webtest.Render(t, organisms.LeasePanel(revocable))
	has(t, canRevoke, "保持中", "claude", "MCP", "5分", "強制剥奪", `hx-post="/lease/revoke"`, `hx-target="#lease"`, `hx-swap="innerHTML"`, `hx-disabled-elt="this"`)

	// 剥奪できない保持者（view の CanRevoke が false）と、空きのときはボタンが描画されない。
	cannot := webtest.Render(t, organisms.LeasePanel(held))
	has(t, cannot, "保持中", "claude")
	for _, out := range []string{idle, cannot} {
		hasNot(t, out, "<button", "hx-post", "hx-delete", "revoke", "強制剥奪")
	}
	for _, out := range []string{idle, canRevoke, cannot, webtest.Render(t, organisms.LeasePanelBody(revocable))} {
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// agent の剥奪はタスクを中断するので、その旨（RevokeHint）を添える。ヒントが無ければ何も添えない。
func TestLeasePanelRevokeHint(t *testing.T) {
	v := view.Lease{Held: true, CanRevoke: true, HolderLabel: "claude", KindLabel: "エージェント", Since: "2026-10-30 12:00", IdleRemaining: "5分"}
	hasNot(t, webtest.Render(t, organisms.LeasePanel(v)), "data-revoke-hint")
	v.RevokeHint = "タスクが中断されます"
	has(t, webtest.Render(t, organisms.LeasePanel(v)), `data-revoke-hint>タスクが中断されます<`)
	// 剥奪できない保持者には、ヒントがあってもボタンごと出さない。
	v.CanRevoke = false
	hasNot(t, webtest.Render(t, organisms.LeasePanel(v)), "data-revoke-hint", "強制剥奪")
}

// 状態駆動: 中断のボタンは、中断できる状態（CancelURL がある）のときだけ描画する。
func TestTaskStatusRendersCancelOnlyWhenCancellable(t *testing.T) {
	running := webtest.Render(t, organisms.TaskStatus(view.TaskStatus{Code: "running", Label: "実行中", Tone: view.ToneOK, Steps: "3 / 50", Elapsed: "10秒", CancelURL: "/tasks/t1/cancel"}))
	has(t, running, `data-task-status="running"`, `hx-post="/tasks/t1/cancel"`, `hx-target="closest [data-task-status]"`, `hx-swap="outerHTML"`, `hx-disabled-elt="this"`, "3 / 50", "10秒", "中断")
	done := webtest.Render(t, organisms.TaskStatus(view.TaskStatus{Code: "completed", Label: "完了", Tone: view.ToneOK, Steps: "3 / 50", Elapsed: "10秒"}))
	has(t, done, `data-task-status="completed"`)
	hasNot(t, done, "<button", "hx-post", "/cancel")
	for _, out := range []string{running, done} {
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// ダッシュボードの実行中タスク: 項目が無ければ中身を描画しない（差し込み先と版の隠し入力だけ）。OOB は同じ id を差し替える。
func TestRunningTasksPanel(t *testing.T) {
	item := view.RunningTask{ShortID: "00RUN001", DetailURL: "/tasks/t1", Client: "claude", PromptExcerpt: "天気を調べる",
		Status: view.TaskStatus{Code: "running", Label: "実行中", Tone: view.ToneOK, Steps: "1 / 50", Elapsed: "5秒", CancelURL: "/tasks/t1/cancel"}}
	empty := webtest.Render(t, organisms.RunningTasksPanel(view.RunningTasks{Rev: "abc"}))
	has(t, empty, `id="running-tasks"`, `name="rev_tasks" value="abc" data-poll-rev`)
	hasNot(t, empty, "data-running-task", "<button", "hx-swap-oob")
	full := webtest.Render(t, organisms.RunningTasksPanel(view.RunningTasks{Items: []view.RunningTask{item}, Rev: "def"}))
	has(t, full, "data-running-task", "00RUN001", "claude", "天気を調べる", `hx-post="/tasks/t1/cancel"`, `href="/tasks/t1"`)
	hasNot(t, full, "hx-swap-oob")
	oob := webtest.Render(t, organisms.RunningTasksOOB(view.RunningTasks{Items: []view.RunningTask{item}, Rev: "def"}))
	has(t, oob, `id="running-tasks"`, `hx-swap-oob="true"`, "data-running-task")
	for _, out := range []string{empty, full, oob} {
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// 承認ダイアログは種類ごとの見出し・内容を出し、Detail・Note が空なら枠を描画しない。
func TestApprovalDialogKinds(t *testing.T) {
	base := view.ApprovalDialog{ID: "a1", ClientLabel: "claude", KindLabel: "エージェント", Remaining: view.ApprovalRemaining{ID: "a1", Seconds: 30, RefreshURL: "/approvals/a1"},
		AllowURL: "/approvals/a1/allow", DenyURL: "/approvals/a1/deny"}
	lease := base
	lease.Kind, lease.Heading, lease.Summary = "lease", "操作権の取得を許可しますか？", "操作権（リース）の取得"
	out := webtest.Render(t, organisms.ApprovalDialog(lease))
	has(t, out, `data-approval-type="lease"`, "操作権の取得を許可しますか？")
	hasNot(t, out, "data-approval-detail", "data-approval-note")
	sc := base
	sc.Kind, sc.Heading, sc.Summary, sc.Detail, sc.Note = "safety_check", "安全チェック", "要約", "<b>detail</b>", "note"
	out2 := webtest.Render(t, organisms.ApprovalDialog(sc))
	has(t, out2, `data-approval-type="safety_check"`, `data-approval-detail>&lt;b&gt;detail&lt;/b&gt;<`, `data-approval-note>note<`)
	hasNot(t, out2, "<b>detail</b>")
	for _, o := range []string{out, out2} {
		webtest.AssertCSPCompliantHTML(t, o)
	}
}

// SSE の lease イベント（M2 でも変えない）は表示だけで、強制剥奪の操作を含まない。
func TestLeaseDisplayHasNoActions(t *testing.T) {
	out := webtest.Render(t, organisms.LeaseDisplay(view.Lease{Held: true, CanRevoke: true, HolderLabel: "claude", KindLabel: "MCP", Since: "2026-10-30 12:00", IdleRemaining: "5分"}))
	has(t, out, "保持中", "claude", "5分")
	hasNot(t, out, "<button", "hx-post", "revoke", "強制剥奪")
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

// 接続要求の履歴: 6 列の表・結果のバッジ・読み取り専用（操作なし）。前後のページへのリンクは、その向きのページがあるときだけ出る。
func TestConnectionRequestHistoryRowsAndPager(t *testing.T) {
	rows := []view.HistoryRow{
		{ID: "r1", DisplayName: "bob", LoginName: "bob@example.com", RemoteIP: "100.64.0.9", ReceivedAt: "2026-11-01 10:00",
			Result: "許可", ResultTone: view.ToneOK, ResultCode: "approved", DecidedAt: "2026-11-01 10:05"},
		{ID: "r2", DisplayName: "carol", LoginName: "carol@example.com", RemoteIP: "100.64.0.10", ReceivedAt: "2026-11-01 09:00",
			Result: "拒否", ResultTone: view.ToneDanger, ResultCode: "denied", DecidedAt: "2026-11-01 09:01"},
		{ID: "r3", DisplayName: "dave", LoginName: "dave@example.com", RemoteIP: "100.64.0.11", ReceivedAt: "2026-11-01 08:00",
			Result: "未処理", ResultTone: view.ToneWarn, ResultCode: "pending", DecidedAt: "-"},
	}
	out := webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Rows: rows, Page: 2, PrevURL: "/requests/history?page=1", NextURL: "/requests/history?page=3"}))
	has(t, out, `id="request-history"`, "届いた日時", "処理した日時", `data-history-request="r1" data-result="approved"`, "許可", "拒否", "未処理",
		"100.64.0.9", "bob@example.com", "2026-11-01 10:05",
		`hx-get="/requests/history?page=1"`, `hx-get="/requests/history?page=3"`, `hx-target="#request-history"`, `hx-push-url="true"`,
		"data-pager-prev", "data-pager-next", "2 ページ目")
	hasNot(t, out, "<button", "hx-post", "name=\"rev_requests\"")
	webtest.AssertCSPCompliantHTML(t, out)

	first := webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Rows: rows, Page: 1, NextURL: "/requests/history?page=2"}))
	has(t, first, "data-pager-next")
	hasNot(t, first, "data-pager-prev")
	single := webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Rows: rows, Page: 1}))
	hasNot(t, single, "data-pager")
	empty := webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Page: 1}))
	has(t, empty, `data-empty="request-history"`)
	hasNot(t, empty, "<table", "data-pager")

	tabs := webtest.Render(t, organisms.RequestsTabs(view.RequestsTabHistory))
	has(t, tabs, `href="/requests"`, `href="/requests/history"`, `hx-boost="true"`, `hx-target-error="#main"`, "data-requests-tabs")
	if got := strings.Count(tabs, `aria-current="page"`); got != 1 || !strings.Contains(tabs[strings.Index(tabs, `aria-current`):], "履歴") {
		t.Errorf("exactly the history tab must be current:\n%s", tabs)
	}
}

// 監査ログと更新の項目（FR-S07・FR-S09）: 平文の注意書きは常に出る。値は入力のとおり描画し、エラーは項目別に出す。
func TestSettingsFormAuditAndUpdateFields(t *testing.T) {
	form := view.SettingsForm{
		Values: view.SettingsValues{AuditThumbnails: true, AuditRetentionDays: "0", AuditThumbsMaxBytes: "1073741824", UpdateApplyMode: "auto_idle", UpdateChannel: "nightly"},
		Errors: view.SettingsErrors{AuditRetentionDays: "範囲外", UpdateApplyMode: "選び直し", UpdateChannel: "選び直し 2"},
	}
	out := webtest.Render(t, organisms.SettingsForm(form))
	has(t, out, `data-notice="audit-plaintext"`, "BitLocker", `data-notice="audit-record-text"`,
		`name="audit.thumbnails_enabled"`, `name="audit.record_text"`, `name="audit.retention_days"`, `value="0"`,
		`name="audit.thumbs_max_bytes"`, `value="1073741824"`, `<option value="auto_idle" selected>`,
		`data-field-error="set-audit-days"`, `data-field-error="set-update-mode"`, `data-field-error="set-update-channel"`, `<option value="nightly" selected>`)
	hasNot(t, out, `data-field-error="set-audit-cap"`, `<option value="confirm" selected>`)
	if got := strings.Count(out, "checked"); got != 1 {
		t.Errorf("only audit.thumbnails_enabled must be checked, got %d", got)
	}
	webtest.AssertCSPCompliantHTML(t, out)
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

// 読めない行は、引数の代わりに警告を出す（「なし」とは出さない）。
func TestAppCardCorruptRowShowsWarning(t *testing.T) {
	card := webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a5", Label: "x", ExePath: `C:\c.exe`, Corrupt: true}))
	has(t, card, "data-app-corrupt", "読めません")
	hasNot(t, card, "data-app-args", ">なし<")
}

// 起動できない行は警告を出し、起動できる行には出さない。
func TestAppCardUnlaunchableRowShowsWarning(t *testing.T) {
	bad := webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a6", Label: "x", ExePath: "c.exe", Unlaunchable: true}))
	has(t, bad, "data-app-unlaunchable", "起動できません")
	hasNot(t, bad, "data-app-corrupt")
	good := webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a7", Label: "x", ExePath: `C:\c.exe`}))
	hasNot(t, good, "data-app-unlaunchable")
}

// 読めない行の編集フォームは、引数を入れ直す案内を出す。
func TestAppFormCorruptNotice(t *testing.T) {
	form := view.AppForm{ID: "a8", DOMID: "app-form-a8", ActionURL: "/apps/a8", Corrupt: true}
	has(t, webtest.Render(t, organisms.AppForm(form, false)), "data-app-form-corrupt", "保存された設定を読めませんでした。引数を入れ直してください")
	form.Corrupt = false
	hasNot(t, webtest.Render(t, organisms.AppForm(form, false)), "data-app-form-corrupt")
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
	out = webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Rows: []view.HistoryRow{{
		ID: "r1", DisplayName: evil, LoginName: evil, RemoteIP: evil, ReceivedAt: evil, Result: evil, DecidedAt: evil,
	}}}))
	hasNot(t, out, "<script>alert")
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

// サーバー状態: 長い IPv4/IPv6・[IPv6]:ポート・長い MagicDNS 名は、カードの幅を超えず折り返せる
// （<dd> が縮められ、<code> が文字の途中でも改行できる）。Issue #43。
func TestServerStatusBodyWrapsLongValues(t *testing.T) {
	const (
		ipv4   = "100.64.0.1"
		ipv6   = "fd7a:115c:a1e0:ab12:4840:3c75:8f2e:91d6"
		dns    = "demo-host-with-a-very-long-machine-name-for-overflow-0123456789.tail-demo-example-network.ts.net"
		listen = "[fd7a:115c:a1e0:ab12:4840:3c75:8f2e:91d6]:8765"
	)
	out := webtest.Render(t, organisms.ServerStatusBody(view.ServerStatus{
		TailscaledLabel: "稼働中", TailnetIPs: []string{ipv4, ipv6}, DNSName: dns, Port: 8765,
		ListenLabel: "待ち受け中", ListenAddrs: []string{ipv4 + ":8765", listen},
	}))
	// <dd> ごとの断片に分け、値を含む <code> と、それを囲む <dd> の class を調べる。
	ddOpen := regexp.MustCompile(`<dd[^>]*class="([^"]*)"`)
	codeOpen := regexp.MustCompile(`<code class="([^"]*)">([^<]*)</code>`)
	wrapped := map[string]bool{}
	for _, dd := range strings.Split(out, "</dd>") {
		open := ddOpen.FindStringSubmatch(dd)
		for _, c := range codeOpen.FindAllStringSubmatch(dd, -1) {
			if open == nil || !strings.Contains(" "+open[1]+" ", " min-w-0 ") {
				t.Errorf("<dd> of %q lacks min-w-0 (grid/flex item cannot shrink): %s", c[2], dd)
			}
			if !strings.Contains(" "+c[1]+" ", " break-all ") {
				t.Errorf("<code> of %q lacks break-all: %s", c[2], c[0])
			}
			wrapped[c[2]] = true
		}
	}
	for _, v := range []string{ipv4, ipv6, dns, ipv4 + ":8765", listen} {
		if !wrapped[v] {
			t.Errorf("%q is not rendered inside a <code>: %s", v, out)
		}
	}
}

// アプリのログ: 3 列の表・レベルのバッジ（成功の INFO・WARN・ERROR）・属性・絞り込みのリンク。HTML は CSP に適合する。
func TestAppLogRendersLevelsAttrsFiltersAndPager(t *testing.T) {
	v := view.AppLog{
		Page: 2, Filter: "warn",
		Filters: []view.LogFilterLink{
			{Code: "all", Label: "すべて", URL: "/logs"},
			{Code: "warn", Label: "WARN 以上", URL: "/logs?level=warn", Active: true},
			{Code: "error", Label: "ERROR のみ", URL: "/logs?level=error"},
		},
		Rows: []view.LogRow{
			{Time: "2026-11-01 09:00:03", Level: "ERROR", LevelTone: view.ToneDanger, Message: "mcp tool failed", Attrs: []view.LogAttr{{Key: "tool", Value: "screenshot"}}},
			{Time: "2026-11-01 09:00:02", Level: "WARN", LevelTone: view.ToneWarn, Message: "auth failed <b>"},
			{Time: "2026-11-01 09:00:01", Level: "INFO", LevelTone: view.ToneOK, Message: "lease acquired"},
		},
		PrevURL: "/logs?level=warn", NextURL: "/logs?level=warn&page=3",
	}
	out := webtest.Render(t, organisms.AppLog(v))
	has(t, out, `id="app-log"`, `data-log-filter="warn"`, ">日時<", ">レベル<", ">内容<",
		`data-log-level="ERROR"`, `data-log-level="WARN"`, `data-log-level="INFO"`, ">tool<", ">screenshot<",
		`data-log-filter-link="error"`, `hx-get="/logs?level=error"`, `aria-current="true"`,
		"data-pager-prev", "data-pager-next", "2 ページ目", `hx-target="#app-log"`)
	has(t, out, "auth failed &lt;b&gt;")
	hasNot(t, out, "auth failed <b>", "<form", "hx-post")
	webtest.AssertCSPCompliantHTML(t, out)

	empty := webtest.Render(t, organisms.AppLog(view.AppLog{Filter: "all", Page: 1}))
	has(t, empty, `data-empty="app-log"`)
	hasNot(t, empty, "<tbody", "data-pager")
}

// API キーのパネル（NFR-S14）: 入力欄は伏せ字で value を持たない。登録の有無で状態・ボタンが変わる。エラーは項目別に出す。
func TestAPIKeyPanelNeverRendersValueAndFollowsState(t *testing.T) {
	p := view.APIKeyPanel{ID: "agent-key", Provider: "OpenAI", Target: "puppet-remo/openai", PutURL: "/agent/api-key", ConfirmURL: "/agent/api-key/delete/confirm"}
	out := webtest.Render(t, organisms.APIKeyPanel(p))
	has(t, out, `id="agent-key"`, `data-key-state="unregistered"`, "未登録", `type="password"`, `autocomplete="off"`, `name="api_key"`,
		`hx-put="/agent/api-key"`, `hx-target-422="#agent-key"`, "puppet-remo/openai", "資格情報マネージャー", "表示しません", ">登録<")
	hasNot(t, out, "value=", "data-key-delete", "削除", "data-field-error")
	webtest.AssertCSPCompliantHTML(t, out)

	p.Registered, p.Error = true, "API キーを入力してください。"
	out = webtest.Render(t, organisms.APIKeyPanel(p))
	has(t, out, `data-key-state="registered"`, "登録済み", `data-key-delete="OpenAI"`, `hx-get="/agent/api-key/delete/confirm"`, `hx-target="#modal-root"`,
		`data-field-error="agent-key-input"`, "入力してください", `aria-invalid`, "置き換える")
	hasNot(t, out, "value=")
	webtest.AssertCSPCompliantHTML(t, out)
}

// エージェントと高速パスのフォームは、別々の同意（別々のチェックボックスと文言）を持つ。既定の値は入力のとおりに描画する。
func TestAgentAndFastpathFormsHaveSeparateConsent(t *testing.T) {
	agent := webtest.Render(t, organisms.AgentForm(view.AgentForm{
		Values: view.AgentValues{Model: "m", MaxSteps: "50", MaxDurationSec: "600", KeepScreenshots: "3", DenyProcesses: "A.exe\nB.exe"},
		Errors: view.AgentErrors{Consent: "同意が必要です", General: "全体の不備"},
	}))
	has(t, agent, `hx-put="/agent"`, `data-consent="openai"`, "OpenAI に送信される", `data-field-error="agent-consent"`, "同意が必要です",
		`data-form-error="agent"`, "全体の不備", `<textarea`, "A.exe\nB.exe", `min="1"`, `max="500"`, `min="30"`, `max="7200"`, `max="10"`)
	hasNot(t, agent, "TypeSafe", "checked", "同意済み")

	fast := webtest.Render(t, organisms.FastpathForm(view.FastpathForm{
		Values:    view.FastpathValues{Enabled: true, Consent: true, Model: "jev-1.13", MinConfidence: "0.7", MaxSteps: "15", ExcludedLabels: "delete"},
		ConsentAt: "2026-10-30 12:00",
	}))
	has(t, fast, `hx-put="/agent/fastpath"`, `data-consent="typesafe"`, "TypeSafe（api.typesafe.ai）に送信される", "ウィンドウのタイトル", "直近 5 件",
		"スクリーンショット、要素の値", `data-consent-at="typesafe"`, "2026-10-30 12:00", `step="0.05"`, `max="1"`, "別の同意", "checked")
	hasNot(t, fast, "OpenAI に送信される", `data-consent="openai"`)
	webtest.AssertCSPCompliantHTML(t, agent)
	webtest.AssertCSPCompliantHTML(t, fast)
}
