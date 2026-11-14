package organisms_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
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
	has(t, canRevoke, "保持中", "claude", "MCP", "5分", "強制剥奪", `hx-get="/lease/revoke/confirm"`, `hx-target="#modal-root"`, `hx-swap="innerHTML"`)

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
	// 警告は Notice（アイコン + 本文色 + 状態色の枠）で出す（C4）。
	has(t, webtest.Render(t, organisms.LeasePanel(v)), `data-revoke-hint`, "border-l-warning", "<svg", "タスクが中断されます")
	// 剥奪できない保持者には、ヒントがあってもボタンごと出さない。
	v.CanRevoke = false
	hasNot(t, webtest.Render(t, organisms.LeasePanel(v)), "data-revoke-hint", "強制剥奪")
}

// 状態駆動: 中断のボタンは、中断できる状態（CancelURL がある）のときだけ描画する。
func TestTaskStatusRendersCancelOnlyWhenCancellable(t *testing.T) {
	running := webtest.Render(t, organisms.TaskStatus(view.TaskStatus{Code: "running", Label: "実行中", Tone: view.ToneOK, Steps: "3 / 50", Elapsed: "10秒", DOMID: "task-status-t1", CancelURL: "/tasks/t1/cancel", ConfirmURL: "/tasks/t1/cancel/confirm"}))
	// 中断は破壊的なので、ボタンは確認モーダルを開く（確定のボタンだけが POST を送る。O2）。
	has(t, running, `id="task-status-t1"`, `data-task-status="running"`, `hx-get="/tasks/t1/cancel/confirm"`, `hx-target="#modal-root"`, `hx-swap="innerHTML"`, "3 / 50", "10秒", "中断")
	hasNot(t, running, "hx-post")
	done := webtest.Render(t, organisms.TaskStatus(view.TaskStatus{Code: "completed", Label: "完了", Tone: view.ToneOK, Steps: "3 / 50", Elapsed: "10秒"}))
	has(t, done, `data-task-status="completed"`)
	hasNot(t, done, "<button", "hx-post", "/cancel")
	for _, out := range []string{running, done} {
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// タスク詳細のステップの引数: キーだけが補助（text-mute）で、値は内容なので本文色（text-ink・text-sm）にする（C6）。
func TestTaskDetailStepArgsValueIsBodyColor(t *testing.T) {
	out := webtest.Render(t, organisms.TaskDetail(view.TaskDetail{ID: "t1", Steps: []view.TaskStepRow{{No: 1, Summary: "クリック", Args: []view.AuditArg{{Key: "target", Value: "送信ボタン"}}}}}))
	has(t, out, `<dl class="mt-1 space-y-2 text-sm text-ink" data-step-args>`, `<dt class="text-mute">target:</dt>`, `<dd class="break-all">送信ボタン</dd>`)
	hasNot(t, out, `class="mt-1 space-y-2 text-xs text-mute"`)
}

// タスク・監査ログの詳細: 題付きのセクション（molecules.Card。#166）同士は、24（--gap-card-section。space-y-6）で区切る。
// 題は Card の h2（lg 18）で、直書きの text-2xl の h2 ではない。
func TestDetailPagesSeparateCardsByGapCardSection(t *testing.T) {
	task := webtest.Render(t, organisms.TaskDetail(view.TaskDetail{ID: "t1"}))
	has(t, task, `class="space-y-6" data-task-detail="t1"`)
	audit := webtest.Render(t, organisms.AuditDetail(view.AuditDetail{Event: "click"}))
	has(t, audit, `class="space-y-6" data-audit-detail="click"`)
	hasNot(t, task, `class="space-y-4" data-task-detail`, `class="space-y-12"`)
	hasNot(t, audit, `class="space-y-4" data-audit-detail`, `class="space-y-12"`)
	const cardTitle = `<h2 class="border-b border-border px-6 py-4 text-lg font-semibold leading-[1.4] text-ink">`
	has(t, task, cardTitle+"指示</h2>", cardTitle+"最終回答</h2>", cardTitle+"ステップ</h2>")
	has(t, audit, cardTitle+"引数</h2>", cardTitle+"サムネイル</h2>")
}

// ダッシュボードの実行中タスク: 項目が無ければ中身を描画しない（差し込み先と版の隠し入力だけ）。OOB は同じ id を差し替える。
func TestRunningTasksPanel(t *testing.T) {
	item := view.RunningTask{ShortID: "00RUN001", DetailURL: "/tasks/t1", Client: "claude", PromptExcerpt: "天気を調べる",
		Status: view.TaskStatus{Code: "running", Label: "実行中", Tone: view.ToneOK, Steps: "1 / 50", Elapsed: "5秒", CancelURL: "/tasks/t1/cancel", ConfirmURL: "/tasks/t1/cancel/confirm"}}
	empty := webtest.Render(t, organisms.RunningTasksPanel(view.RunningTasks{Rev: "abc"}))
	has(t, empty, `id="running-tasks"`, `name="rev_tasks" value="abc" data-poll-rev`)
	hasNot(t, empty, "data-running-task", "<button", "hx-swap-oob")
	full := webtest.Render(t, organisms.RunningTasksPanel(view.RunningTasks{Items: []view.RunningTask{item}, Rev: "def"}))
	has(t, full, "data-running-task", "00RUN001", "claude", "天気を調べる", `hx-get="/tasks/t1/cancel/confirm"`, `href="/tasks/t1"`)
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
	has(t, out2, `data-approval-type="safety_check"`, `data-approval-detail>&lt;b&gt;detail&lt;/b&gt;<`, `data-approval-note`, `>note</div>`)
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

// 補助テキスト（text-mute）の段落に入れた <code> チップは、--accent-soft 地の上で AA 4.5:1 を保つため文字色を --ink にする（C6）。
func TestMuteParagraphCodeChipsAreInk(t *testing.T) {
	chip := func(v string) string {
		return `<code class="rounded-sm bg-accent-soft px-2 py-1 text-ink">` + v + `</code>`
	}
	client := webtest.Render(t, organisms.ClientCard(view.Client{ID: "c1", DisplayName: "pc", NodeID: "node-1"}))
	has(t, client, chip("node-1"))
	request := webtest.Render(t, organisms.ConnectionRequestList(view.RequestList{Rows: []view.RequestRow{{ID: "r1", RemoteIP: "100.64.0.9"}}}))
	has(t, request, chip("100.64.0.9"))
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
	has(t, first, "data-pager-next", "disabled data-pager-prev")
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
	// O7・P0: タブは非選択が --mute・選択が --ink（下線 2px の --accent と合わせて現在のタブを伝える）。色の変化は --motion-base。
	has(t, tabs, "border-accent text-ink", "border-transparent text-mute hover:border-mute hover:text-ink", "motion-hover")
	hasNot(t, tabs, "border-transparent text-ink")
	// O7: タブの 2px の下線がタブ列の 1px の罫線に重なる（-mb-px）。重ならないと罫線の上に積まれて 1px 厚くなる。
	if got := strings.Count(tabs, "-mb-px"); got != 2 {
		t.Errorf("both tabs must overlap the tab row border with -mb-px, got %d:\n%s", got, tabs)
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
	// 引数の並びの行間は 8（--list-gap。space-y-2）。
	has(t, webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a5", Label: "x", ExePath: `C:\c.exe`, Args: []string{"--x"}})), `<ul class="space-y-2" data-app-args>`)
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
		`data-log-level="ERROR"`, `data-log-level="WARN"`, `data-log-level="INFO"`, ">tool<", ">screenshot<", `<dl class="mt-1 flex flex-wrap gap-x-4 gap-y-2 text-sm text-ink">`,
		`data-log-filter-link="error"`, `hx-get="/logs?level=error"`, `aria-current="true"`,
		"data-pager-prev", "data-pager-next", "2 ページ目", `hx-target="#app-log"`)
	has(t, out, "auth failed &lt;b&gt;")
	hasNot(t, out, "auth failed <b>", "<form", "hx-post")
	webtest.AssertCSPCompliantHTML(t, out)

	empty := webtest.Render(t, organisms.AppLog(view.AppLog{Filter: "all", Page: 1}))
	has(t, empty, `data-empty="app-log"`)
	hasNot(t, empty, "<tbody", "data-pager")
}

// API キーのパネル（NFR-S14）: 入力欄は伏せ字で value を持たない。未登録は最初から入力欄（バッジなし）、登録済みは入力欄の代わりに
// 「登録済み」バッジと「変更」（入力欄・登録ボタンは hidden）。登録の失敗の再描画は、変更を押したあとの状態（入力欄が見える・空）を保つ。
func TestAPIKeyPanelNeverRendersValueAndFollowsState(t *testing.T) {
	p := view.APIKeyPanel{ID: "agent-key", Provider: "OpenAI", Target: "puppet-remo/openai", PutURL: "/agent/api-key", ConfirmURL: "/agent/api-key/delete/confirm"}
	out := webtest.Render(t, organisms.APIKeyPanel(p))
	has(t, out, `id="agent-key"`, `data-key-state="unregistered"`, `type="password"`, `autocomplete="off"`, `name="api_key"`,
		`hx-put="/agent/api-key"`, `hx-target-422="#agent-key"`, "puppet-remo/openai", "資格情報マネージャー", "表示しません", ">登録<",
		`data-secret-toggle`, `aria-pressed="false"`, `aria-controls="agent-key-input"`)
	hasNot(t, out, "value=", "data-key-delete", "削除", "data-field-error", "登録済み", "data-secret-saved", "data-secret-change")
	if hiddenAttr.MatchString(out) {
		t.Errorf("first-time panel must show the input and the submit button:\n%s", out)
	}
	webtest.AssertCSPCompliantHTML(t, out)

	// 登録済み: 入力欄を見せず、バッジ + 変更。入力欄と登録（置き換える）ボタンは hidden のまま描画する（「変更」で app.ts が出す）。
	p.Registered = true
	out = webtest.Render(t, organisms.APIKeyPanel(p))
	has(t, out, `data-key-state="registered"`, `data-secret-saved`, "登録済み", "bg-accent-soft", "text-mute-on-soft", `data-secret-change`, ">変更<",
		`data-key-delete="OpenAI"`, `hx-get="/agent/api-key/delete/confirm"`, `hx-target="#modal-root"`, "置き換える", "新しい OpenAI API キー")
	hasNot(t, out, "value=", "data-field-error", "未登録")
	savedTag := regexp.MustCompile(`<div[^>]*data-secret-saved[^>]*>`).FindString(out)
	if strings.Contains(savedTag, "hidden") {
		t.Errorf("the saved row must be visible: %s", savedTag)
	}
	editTags := regexp.MustCompile(`<[a-z]+[^>]*data-secret-edit[^>]*>`).FindAllString(out, -1)
	if len(editTags) != 2 {
		t.Fatalf("data-secret-edit elements = %d, want 2 (input group and submit button):\n%s", len(editTags), out)
	}
	for _, tag := range editTags {
		if !hiddenAttr.MatchString(tag) {
			t.Errorf("the edit side must be hidden while saved: %s", tag)
		}
	}
	webtest.AssertCSPCompliantHTML(t, out)

	// 登録済みで登録の失敗（422）の再描画: 変更を押したあとの状態を保ち、入力欄は空（value なし）。
	p.Error = "API キーを入力してください。"
	out = webtest.Render(t, organisms.APIKeyPanel(p))
	has(t, out, `data-key-state="registered"`, `data-key-delete="OpenAI"`, `data-field-error="agent-key-input"`, "入力してください", `aria-invalid`, "置き換える")
	hasNot(t, out, "value=")
	if !strings.Contains(regexp.MustCompile(`<div[^>]*data-secret-saved[^>]*>`).FindString(out), "hidden") {
		t.Errorf("after a failed save the badge row must stay hidden:\n%s", out)
	}
	for _, tag := range regexp.MustCompile(`<[a-z]+[^>]*data-secret-edit[^>]*>`).FindAllString(out, -1) {
		if hiddenAttr.MatchString(tag) {
			t.Errorf("after a failed save the input must stay visible: %s", tag)
		}
	}
	webtest.AssertCSPCompliantHTML(t, out)
}

// S2: API キーのパネルとクリップボードの送信フォームは、項目 → 項目を 16px（`--gap-field` = space-y-4）で並べる。
// クリップボードはラベル → テキスト欄だけを 8px（space-y-2）にまとめ、その外側（テキスト欄のブロック → 送信ボタン）を 16px にする。
func TestSubmitFormsUseFieldGap(t *testing.T) {
	key := webtest.Render(t, organisms.APIKeyPanel(view.APIKeyPanel{ID: "agent-key", Provider: "OpenAI", PutURL: "/agent/api-key"}))
	has(t, key, `<div id="agent-key" class="space-y-4"`)
	hasNot(t, key, "space-y-3", "space-y-2")
	clip := webtest.Render(t, organisms.ClipboardPanel(false))
	has(t, clip, `class="space-y-4" novalidate hx-post="/viewer/clipboard/send"`, `<div class="space-y-2"><label for="clipboard-text"`)
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
	// 同意の枠の段落同士は 16（mt-4。S2）。
	has(t, agent, `<p class="mt-4">API のデータは`)
	has(t, fast, `<p class="mt-4">スクリーンショット、要素の値`)
	webtest.AssertCSPCompliantHTML(t, agent)
	webtest.AssertCSPCompliantHTML(t, fast)
}

// 同意のエラー文も、入力のエラーと同じ FieldError で描画する（出現はフェードイン。M5）。
func TestConsentErrorsUseFieldError(t *testing.T) {
	agent := webtest.Render(t, organisms.AgentForm(view.AgentForm{Errors: view.AgentErrors{Consent: "同意が必要です"}}))
	has(t, agent, `class="motion-rise-in mt-1 text-sm text-danger" role="alert" data-field-error="agent-consent"`)

	fast := webtest.Render(t, organisms.FastpathForm(view.FastpathForm{Errors: view.FastpathErrors{Consent: "同意が必要です"}}))
	has(t, fast, `class="motion-rise-in mt-1 text-sm text-danger" role="alert" data-field-error="fastpath-consent"`)
}

// トースト・アラートの置き場: トーストは画面下の中央（端から 24px・--z-toast）、アラートは #alert-root（中身が無ければ隠す）と、
// app.ts が複製する雛形 #alert-template（role なし）。
func TestToastAndAlertRoots(t *testing.T) {
	toast := webtest.Render(t, organisms.ToastRoot())
	has(t, toast, `id="toast-root"`, "bottom-6", "left-1/2", "-translate-x-1/2", "z-(--z-toast)")
	hasNot(t, toast, "right-4", "z-50")
	alert := webtest.Render(t, organisms.AlertRoot("px-4"))
	has(t, alert, `id="alert-root"`, "empty:hidden", "px-4", `id="alert-template"`, "<template", "data-alert-message")
	hasNot(t, alert, `role="alert"`)
	oob := webtest.Render(t, organisms.AlertOOB(atoms.AlertProps{Tone: atoms.AlertDanger, Code: "internal", Heading: "エラー", Message: "失敗しました。"}))
	has(t, oob, `id="alert-root" hx-swap-oob="innerHTML"`, `data-error-code="internal"`, "失敗しました。")
	for _, out := range []string{toast, alert, oob} {
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// サイドナビ: 幅 240（md 未満のドロワーは約 78%）・項目高さ 40・間隔 8・現在の項目は --accent-soft + --ink。ヘッダーは高さ 56。
func TestSidebarAndHeaderFollowTheSkeleton(t *testing.T) {
	nav := webtest.Render(t, organisms.Sidebar(view.Chrome{Active: view.NavClients}, false))
	has(t, nav, "w-[78%]", "md:w-60", "space-y-2", "h-10", "bg-accent-soft text-ink", `aria-current="page"`, "text-mute")
	hasNot(t, nav, "bg-slate", "md:w-56")
	header := webtest.Render(t, organisms.Header(view.Header{}))
	has(t, header, "h-14", "border-b", "border-border")
}

// モバイルナビ（P0）: ヘッダーのハンバーガーはドロワーを開閉し（aria-expanded・aria-controls）、md 以上では出さない。
// スクラムはナビの外にあり、md 以上では出さない。
func TestMobileNavToggleAndScrim(t *testing.T) {
	header := webtest.Render(t, organisms.Header(view.Header{}))
	has(t, header, "data-nav-toggle", `aria-controls="sidebar-nav"`, `aria-expanded="false"`, `aria-label="メニューを開く"`, "md:hidden", "<svg", `d="M3 12H21"`)
	scrim := webtest.Render(t, organisms.NavScrim())
	has(t, scrim, `id="nav-scrim"`, "nav-scrim", "bg-scrim", "md:hidden", "z-(--z-drawer)", `aria-hidden="true"`)
	for _, out := range []string{header, scrim} {
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// 通知バッジ数（P1）: --accent 地 + --bone 字・11px Mono・最小 18px。状態色にしない。99 を超える件数は 99+。
func TestRequestsBadgeFollowsTheNotificationBadgeSpec(t *testing.T) {
	out := webtest.Render(t, organisms.RequestsBadge(3))
	has(t, out, "bg-accent", "text-bone", "font-mono", "text-2xs", "min-w-4.5", `data-pending-count="3"`, ">3<")
	hasNot(t, out, "bg-danger", "text-xs")
	has(t, webtest.Render(t, organisms.RequestsBadge(99)), ">99<")
	over := webtest.Render(t, organisms.RequestsBadge(100))
	has(t, over, ">99+<", `data-pending-count="100"`)
	hasNot(t, webtest.Render(t, organisms.RequestsBadge(0)), "data-pending-count")
}

// 前へ／次へだけのページ送りのボタンは Secondary・Small（高さ 32・rounded-md）。
func TestPagerButtonsAreSmallSecondary(t *testing.T) {
	history := webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Page: 2, PrevURL: "/requests/history?page=1", NextURL: "/requests/history?page=3",
		Rows: []view.HistoryRow{{ID: "r1"}}}))
	has(t, history, "h-8", "rounded-md", "data-pager-prev", "data-pager-next")
	hasNot(t, history, "h-9")
	audit := webtest.Render(t, organisms.AuditList(view.AuditList{NextURL: "/audit?cursor=1", Rows: []view.AuditRow{{ID: 1, DetailURL: "/audit/1"}}}))
	has(t, audit, "h-8", "data-pager-next")
	log := webtest.Render(t, organisms.AppLog(view.AppLog{Filter: "all", Page: 1, NextURL: "/logs?page=2", Rows: []view.LogRow{{Time: "t", Level: "INFO"}}}))
	has(t, log, "h-8", "data-pager-next")
	tasks := webtest.Render(t, organisms.TaskList(view.TaskList{NextURL: "/tasks?page=2", Rows: []view.TaskRow{{DetailURL: "/tasks/t1"}}}))
	has(t, tasks, "h-8", "data-pager-next")
}

// 絞り込みの「条件をクリア」は本文のリンク（.link。下線 --mute）で、ほかのリンクと同じ見た目。
func TestAuditResetIsABodyLink(t *testing.T) {
	out := webtest.Render(t, organisms.AuditFilters(view.AuditFilters{}))
	has(t, out, `class="text-sm link"`, "data-audit-reset")
	hasNot(t, out, "hover:underline")
}

// 表: 見出し行は SemiBold 12 の --mute・背景なし。行ホバーは --accent-soft。padding は --table-pad-*。数値列（所要時間）は Mono・右寄せ。
func TestAuditTableFollowsTheTableSpec(t *testing.T) {
	out := webtest.Render(t, organisms.AuditList(view.AuditList{Rows: []view.AuditRow{{ID: 1, DetailURL: "/audit/1", Time: "t", Client: "c", Kind: "tool", Event: "e", Result: "ok", ResultTone: view.ToneOK, Duration: "12 ms"}}}))
	has(t, out, "text-left text-xs font-semibold text-mute", "hover:bg-accent-soft", "px-(--table-pad-x) py-(--table-pad-y)", "text-right font-mono")
	hasNot(t, out, "bg-slate-50", "shadow-sm", "px-3 py-2")
}

// 日時の列（桁を揃えて比べる値）は JetBrains Mono（T5）。左寄せのままでよい。
func TestTableDateTimeCellsUseMono(t *testing.T) {
	const mono = `font-mono text-ink">`
	audit := webtest.Render(t, organisms.AuditList(view.AuditList{Rows: []view.AuditRow{{ID: 1, DetailURL: "/audit/1", Time: "2026-11-01 10:00:00"}}}))
	has(t, audit, mono+"2026-11-01 10:00:00<")
	history := webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Page: 1, Rows: []view.HistoryRow{{ID: "r1", ReceivedAt: "2026-11-01 10:00", DecidedAt: "2026-11-01 10:05"}}}))
	has(t, history, mono+"2026-11-01 10:00<", mono+"2026-11-01 10:05<")
	log := webtest.Render(t, organisms.AppLog(view.AppLog{Filter: "all", Page: 1, Rows: []view.LogRow{{Time: "2026-11-01 09:00:03", Level: "INFO"}}}))
	has(t, log, mono+"2026-11-01 09:00:03<")
	tasks := webtest.Render(t, organisms.TaskList(view.TaskList{Rows: []view.TaskRow{{DetailURL: "/tasks/t1", Created: "2026-11-01 08:00"}}}))
	has(t, tasks, mono+"2026-11-01 08:00<")
	tokens := webtest.Render(t, organisms.TokenList(view.TokenList{ClientID: "c1", Items: []view.Token{{ID: "a", CreatedAt: "2026-10-01", ExpiresAt: "2027-10-01", LastUsedAt: "2026-11-01"}}}, false))
	has(t, tokens, mono+"2026-10-01<", mono+"2027-10-01<", mono+"2026-11-01<")
}

// 空状態は molecules.EmptyState（破線・アイコン + 文言）を使う。
func TestEmptyListsUseTheEmptyState(t *testing.T) {
	out := webtest.Render(t, organisms.AuditList(view.AuditList{}))
	has(t, out, `data-empty="audit-list"`, "border-dashed", "<svg")
}

// C4: 状態メッセージは独自の段落でなく Notice（左の状態色の線 + アイコン + 本文色）で出す。文字を状態色にしない。
// 題なしのインラインアラートの色は、常設の注意書き = warning・同意の枠・ただの補足 = info（ガイド）。
func TestStatusMessagesUseNotice(t *testing.T) {
	tests := []struct {
		name  string
		out   string
		attrs []string
		tone  string
		rises bool
	}{
		{"agent form error", webtest.Render(t, organisms.AgentForm(view.AgentForm{Errors: view.AgentErrors{General: "保存できません"}})), []string{`data-form-error="agent"`, `role="alert"`}, "danger", true},
		{"fastpath form error", webtest.Render(t, organisms.FastpathForm(view.FastpathForm{Errors: view.FastpathErrors{General: "保存できません"}})), []string{`data-form-error="fastpath"`, `role="alert"`}, "danger", true},
		{"app unlaunchable", webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a6", Label: "x", ExePath: "c.exe", Unlaunchable: true})), []string{"data-app-unlaunchable", `role="alert"`}, "danger", false},
		{"app corrupt", webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a5", Label: "x", ExePath: "c.exe", Corrupt: true})), []string{"data-app-corrupt", `role="alert"`}, "danger", false},
		{"update error", webtest.Render(t, organisms.UpdatePanel(view.UpdatePanel{Updatable: true, Error: "確認できません"})), []string{`data-update="error"`, `role="status"`}, "warning", true},
		{"update key unset", webtest.Render(t, organisms.UpdatePanel(view.UpdatePanel{Updatable: true, KeyNotConfigured: true})), []string{`data-update="key-unset"`}, "warning", false},
		{"update applying", webtest.Render(t, organisms.UpdatePanel(view.UpdatePanel{Updatable: true, Applying: true})), []string{`data-update="applying"`}, "info", true},
		{"killswitch active", webtest.Render(t, organisms.KillSwitchPanel(view.KillSwitch{Active: true})), []string{`data-killswitch="active"`}, "danger", true},
		{"lease pending", webtest.Render(t, organisms.ViewerLeasePanel(view.ViewerLease{Pending: true, PendingMine: true})), []string{"data-lease-pending-message", `role="status"`}, "warning", false},
		{"agent consent frame", webtest.Render(t, organisms.AgentForm(view.AgentForm{})), []string{`data-consent="openai"`}, "info", false},
		{"fastpath consent frame", webtest.Render(t, organisms.FastpathForm(view.FastpathForm{})), []string{`data-consent="typesafe"`}, "info", false},
		{"task result note", webtest.Render(t, organisms.TaskDetail(view.TaskDetail{ID: "t1", ResultText: "r", ResultNote: "n", Status: view.TaskStatus{Code: "completed"}})), []string{"data-task-result-note"}, "info", false},
		{"audit plaintext note", webtest.Render(t, organisms.SettingsForm(view.SettingsForm{})), []string{`data-notice="audit-plaintext"`}, "warning", false},
		{"audit record-text note", webtest.Render(t, organisms.SettingsForm(view.SettingsForm{})), []string{`data-notice="audit-record-text"`}, "warning", false},
		{"token error", webtest.Render(t, organisms.TokenFieldError("入力してください", "validation")), []string{`data-error-code="validation"`, `role="alert"`}, "danger", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 色は、その注意書き自身の枠に付いている（同じ出力にある別の注意書きの色で通らない）。
			if open := regexp.MustCompile(`<div class="[^"]*" ` + regexp.QuoteMeta(tt.attrs[0])).FindString(tt.out); !strings.Contains(open, "border-l-"+tt.tone) {
				t.Errorf("%s: its own frame is not border-l-%s: %q", tt.attrs[0], tt.tone, open)
			}
			has(t, tt.out, append(tt.attrs, "border-l-"+tt.tone, "bg-surface", "text-ink", "<svg")...)
			// 操作の結果として現れるもの（422 の再描画・アクションの応答・状態の切り替わり）はフェードイン、常設のものは静か（M5）。
			if tt.rises {
				has(t, tt.out, "motion-rise-in")
			} else {
				hasNot(t, tt.out, "motion-rise-in")
			}
			hasNot(t, tt.out, `class="text-sm text-`+tt.tone+`"`)
		})
	}
}

// S1・P1: ログのレベルの絞り込みはチップ（rounded-full）。選択中は --accent-soft + 通常の枠（太い黒枠にしない）。
func TestAppLogFilterChips(t *testing.T) {
	out := webtest.Render(t, organisms.AppLog(view.AppLog{Page: 1, Filter: "warn", Filters: []view.LogFilterLink{{Code: "all", Label: "すべて", URL: "/logs"}, {Code: "warn", Label: "WARN", URL: "/logs?level=warn", Active: true}}}))
	has(t, out, "rounded-full", "border-border bg-accent-soft")
	hasNot(t, out, "border-accent ", "h-8 items-center rounded-md")
}

// S2: カード状の枠の padding は 16（小さいカード）。入れ子のパネルは --bone 地（枠線なし）。
func TestCardPaddingsFollowScale(t *testing.T) {
	update := webtest.Render(t, organisms.UpdatePanel(view.UpdatePanel{Updatable: true, Latest: &view.UpdateLatest{Version: "v1.2.3", Verified: true}}))
	has(t, update, `class="space-y-2 rounded-md bg-bone p-4" data-update="latest"`)
	// カード内のまとまり同士は 24（--gap-card-section。space-y-6）。
	has(t, update, `class="p-6"><div class="space-y-6">`)
	hasNot(t, update, `class="space-y-4 p-6"`, `<div class="space-y-4">`)
	// 操作権のパネルも molecules.Card（題 lg 18・本文 --s-6。#166）。
	lease := webtest.Render(t, organisms.ViewerLeasePanel(view.ViewerLease{}))
	has(t, lease, `<h2 class="border-b border-border px-6 py-4 text-lg font-semibold leading-[1.4] text-ink">操作権</h2><div class="p-6">`)
	hasNot(t, lease, "bg-surface p-3", "bg-surface p-4", "text-2xl")
}

// T5: ポート番号は桁を揃える値なので font-mono（隣の IP・MagicDNS 名の <code> と同じ書体）。
func TestServerStatusPortIsMono(t *testing.T) {
	out := webtest.Render(t, organisms.ServerStatusBody(view.ServerStatus{Port: 8765}))
	has(t, out, `<dd data-field="port" class="mb-1 min-w-0 font-mono sm:mb-0">8765</dd>`)
}

// S2: ラベル→入力は 8（mb-2）、項目→項目は 16（gap-4）、入れ子のパネルの padding は 16（p-4）。
func TestFormAndNestedPanelSpacing(t *testing.T) {
	has(t, webtest.Render(t, organisms.ClipboardFetched("x")), `class="mb-2 block text-sm text-ink"`)
	has(t, webtest.Render(t, organisms.AuditFilters(view.AuditFilters{})), `class="space-y-4 `, `class="grid gap-4 `)
	has(t, webtest.Render(t, organisms.TokenForm(view.TokenForm{}, false)), `class="grid gap-4 `)
	req := webtest.Render(t, organisms.ConnectionRequestList(view.RequestList{Rows: []view.RequestRow{{ID: "r1", DisplayName: "x"}}}))
	has(t, req, `class="mt-3 grid gap-4 sm:grid-cols-2"`, `<fieldset class="flex min-w-0 flex-col gap-4 border-t border-border pt-4 sm:col-span-2">`, `<legend class="pr-2 text-sm font-semibold leading-[1.4]">使える機能</legend>`)
	// h3 の見出し行 → 1 行目の補足は 8（mt-2。起動許可アプリのカードと同じ）。
	has(t, req, `<p class="mt-2 text-xs text-mute">`)
	has(t, webtest.Render(t, organisms.ClientCard(view.Client{ID: "c1", DisplayName: "pc"})), `<p class="mt-2 text-xs text-mute">`)
	dlg := webtest.Render(t, organisms.ApprovalDialog(view.ApprovalDialog{ID: "a1", Detail: "詳細", Remaining: view.ApprovalRemaining{ID: "a1", Seconds: 30}}))
	has(t, dlg, `rounded-md bg-bone p-4 text-sm text-ink" data-approval-detail`)
}

// S2: 一覧画面の「絞り込み → 表 → ページ送り」を縦に積む外側のラッパーは、他の一覧と同じ 16px（space-y-4）にする。
func TestListWrappersUseSectionGap(t *testing.T) {
	row := view.LogRow{Time: "2026-11-01 09:00:01", Level: "INFO", LevelTone: view.ToneOK, Message: "m"}
	for _, c := range map[string]struct {
		out string
		id  string
	}{
		"app_log":         {webtest.Render(t, organisms.AppLog(view.AppLog{Rows: []view.LogRow{row}})), "app-log"},
		"audit":           {webtest.Render(t, organisms.AuditList(view.AuditList{Rows: []view.AuditRow{{ID: 1, DetailURL: "/audit/1"}}})), "audit-list"},
		"tasks":           {webtest.Render(t, organisms.TaskList(view.TaskList{Rows: []view.TaskRow{{DetailURL: "/tasks/t1"}}})), "task-list"},
		"request_history": {webtest.Render(t, organisms.ConnectionRequestHistory(view.RequestHistory{Rows: []view.HistoryRow{{ID: "r1"}}})), "request-history"},
	} {
		has(t, c.out, `<div id="`+c.id+`" class="space-y-4"`)
		hasNot(t, c.out, `id="`+c.id+`" class="space-y-3"`)
	}
}

// #131 案件ガイド（frontend.md §8.7）: 承認ダイアログ（専用ウィンドウのカード。3 つの状態すべて）は、確認モーダルの本体と同じ形
// （角丸 --r-xl・--border の枠・--surface 地・p-6・shadow-modal）で、題は lg 18px SemiBold。ボタンは右寄せで、拒否（副）が左・許可（主）が右
// （モーダルのキャンセルが左・確定が右）。
func TestApprovalDialogMatchesModalBody(t *testing.T) {
	const body = "rounded-xl border border-border bg-surface p-6 shadow-modal"
	modal := webtest.Render(t, molecules.Modal(view.ConfirmModal{Title: "t", Message: "m", Label: "実行", ConfirmURL: "/x"}))
	has(t, modal, body, `<h2 id="modal-title" class="text-lg font-semibold text-ink">`, `class="mt-6 flex justify-end gap-2"`)

	dlg := webtest.Render(t, organisms.ApprovalDialog(view.ApprovalDialog{ID: "a1", Heading: "h", AllowURL: "/a/allow", DenyURL: "/a/deny", Remaining: view.ApprovalRemaining{ID: "a1", Seconds: 30}}))
	has(t, dlg, body, `<h1 class="text-lg font-semibold text-ink">`, `id="approval-actions" class="flex justify-end gap-2`)
	if deny, allow := strings.Index(dlg, `hx-post="/a/deny"`), strings.Index(dlg, `hx-post="/a/allow"`); deny < 0 || allow < deny {
		t.Errorf("want the deny button before the allow button (deny at %d, allow at %d)", deny, allow)
	}
	has(t, webtest.Render(t, organisms.ApprovalDone(view.ApprovalDone{Allowed: true})), body)
	has(t, webtest.Render(t, organisms.ApprovalGone(view.ApprovalGone{Message: "m"})), body)
}

// #169: アクション以外の行（Judge の判定など）のステップ番号の「-」は、title 属性ではなくツールチップ（atoms.Tooltip）で「ステップ N に付く行」を出す。
// 行ごとに id が違い、aria-describedby が自分の本文を指す。
func TestTaskDetailAuxRowUsesATooltipNotTitle(t *testing.T) {
	out := webtest.Render(t, organisms.TaskDetail(view.TaskDetail{ID: "t1", Steps: []view.TaskStepRow{
		{No: 1, Summary: "クリック"},
		{No: 1, KindLabel: "判定", Summary: "Judge"},
		{No: 2, KindLabel: "判定", Summary: "Judge"},
	}}))
	has(t, out,
		`<span class="tip-host" tabindex="0" aria-describedby="step-row-tip-1"><span class="text-mute">-</span><span role="tooltip" id="step-row-tip-1" class="tip">ステップ 1 に付く行</span></span>`,
		`aria-describedby="step-row-tip-2"><span class="text-mute">-</span><span role="tooltip" id="step-row-tip-2" class="tip">ステップ 2 に付く行</span>`)
	hasNot(t, out, "title=", `id="step-row-tip-0"`)
}

// #166: 項目名の見出し（クライアント名・アプリ名・接続要求のデバイス名）は h3 の base 16 SemiBold（ガイドの見出しの段の h3）で、
// カードの題（h2・lg 18・下罫線）とは別。32px・24px の直書きにしない。詳細ページのクライアントカードは名前を出さない（名前はページの h1）。
func TestItemNameHeadingsAreH3Base16(t *testing.T) {
	const want = "text-base font-semibold text-ink"
	for name, tc := range map[string]struct{ out, title string }{
		"client":  {webtest.Render(t, organisms.ClientCard(view.Client{ID: "c1", DisplayName: "pc-main", CanOpenDetail: true})), "pc-main"},
		"app":     {webtest.Render(t, organisms.AppCard(view.AppRow{ID: "a1", Label: "メモ帳"})), "メモ帳"},
		"request": {webtest.Render(t, organisms.ConnectionRequestList(view.RequestList{Rows: []view.RequestRow{{ID: "r1", DisplayName: "laptop"}}})), "laptop"},
	} {
		found := false
		for _, e := range webtest.Elements(t, tc.out, "h1", "h2", "h3") {
			if e.Text == tc.title {
				found = true
				if e.Tag != "h3" || e.Class != want {
					t.Errorf("%s: %s %q, want h3 %q", name, e.Tag, e.Class, want)
				}
			}
		}
		if !found {
			t.Errorf("%s: no heading %q in:\n%s", name, tc.title, tc.out)
		}
	}
	detail := webtest.Render(t, organisms.ClientCard(view.Client{ID: "c1", DisplayName: "pc-main"}))
	hasNot(t, detail, "<h1", "<h3", "pc-main</h")
}
