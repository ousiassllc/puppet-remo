package gui_test

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

var revInput = regexp.MustCompile(`name="(rev_[a-z]+)" value="([0-9a-f]+)"`)

// revOf は描画された領域の隠し入力（data-poll-rev）から、ポーリングが送り返す版を取り出す。
func revOf(t *testing.T, r resp, name string) string {
	t.Helper()
	for _, m := range revInput.FindAllStringSubmatch(r.Body, -1) {
		if m[1] == name {
			return m[2]
		}
	}
	t.Fatalf("no %s input in\n%s", name, r.Body)
	return ""
}

func pollURL(params url.Values) string {
	if len(params) == 0 {
		return "/poll"
	}
	return "/poll?" + params.Encode()
}

func TestPollOnDashboardSwapsEveryLiveRegion(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Status.V = service.ServerStatus{Tailscaled: service.TailscaledRunning, TailnetIPs: []string{"100.64.0.1"}, Port: 8765, Listening: true, ListenAddrs: []string{"100.64.0.1:8765"}}
		f.Sessions.Items = []service.SessionInfo{{ID: "s1", ClientID: "c1", Label: "claude-desktop", Kind: domain.ActorMCP, Since: f.Clock.T}}
		f.Leases.Held = true
		f.Leases.H = service.Holder{Actor: domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "claude-desktop"}, Since: f.Clock.T, IdleRemaining: 4 * time.Minute}
		seedRequest(f, "req-1", "bob-laptop")
	})
	// ダッシュボードの版は古い（= 画面の後でキルスイッチの状態が変わった）。
	res := r.get(pollURL(url.Values{"rev_killswitch": {"stale"}}), hx...)
	status(t, res, http.StatusOK)
	contains(t, res,
		`id="header-killswitch" hx-swap-oob="innerHTML"`, "稼働中",
		`id="nav-requests-badge" class="ml-auto" hx-swap-oob="innerHTML"`, `data-pending-count="1"`,
		`id="server-status" hx-swap-oob="innerHTML"`, "100.64.0.1:8765",
		`id="sessions" hx-swap-oob="innerHTML"`, "claude-desktop",
		`id="lease" hx-swap-oob="innerHTML"`, "保持中", "4分",
		`id="killswitch-panel"`, `hx-swap-oob="true"`, `data-killswitch="inactive"`, `hx-post="/killswitch/trigger"`,
	)
	// 表示していない領域（rev_requests を送っていない）と、トーストは含めない。
	lacks(t, res, `id="request-list"`, "bob-laptop", `hx-swap-oob="beforeend:#toast-root"`, "<html", "<body", "/killswitch/release")
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestPollRendersFromCurrentState(t *testing.T) {
	r := newRig(t)
	stale := url.Values{"rev_killswitch": {"stale"}}
	idle := r.get(pollURL(stale), hx...)
	contains(t, idle, "接続中のセッションはありません", "空き", "稼働中", `hx-post="/killswitch/trigger"`)
	lacks(t, idle, "data-session", "data-pending-count", "キルスイッチ発動中", "/killswitch/release")

	// 状態が変わると、次の応答が変わる（発動中は解除だけ。ヘッダーのバッジも変わる）。
	r.f.KillSwitch.Snapshot = service.KillSwitchSnapshot{Active: true, TriggeredAt: r.f.Clock.T, Source: "gui"}
	r.f.Sessions.Items = []service.SessionInfo{{ID: "s1", ClientID: "c1", Label: "web-viewer", Kind: domain.ActorWeb, Since: r.f.Clock.T}}
	seedRequest(r.f, "req-1", "bob-laptop")
	active := r.get(pollURL(stale), hx...)
	contains(t, active, "キルスイッチ発動中", `data-killswitch="active"`, `hx-get="/killswitch/release/confirm"`, "web-viewer", `data-pending-count="1"`)
	lacks(t, active, "/killswitch/trigger", "接続中のセッションはありません")
}

func TestPollSendsRegionsOnlyWhenTheClientShowsThem(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedRequest(f, "req-1", "bob-laptop") })

	// どのページでも、ヘッダーのバッジと件数のバッジだけは常に返す。
	bare := r.get("/poll", hx...)
	status(t, bare, http.StatusOK)
	contains(t, bare, `id="header-killswitch"`, `id="nav-requests-badge"`, `data-pending-count="1"`)
	lacks(t, bare, `id="server-status"`, `id="sessions"`, `id="lease"`, `id="killswitch-panel"`, `id="request-list"`)

	// 接続要求のページ: 版が違えば一覧を返し、同じなら返さない（入力中のフォームを消さない）。
	stale := r.get(pollURL(url.Values{"rev_requests": {"stale"}}), hx...)
	contains(t, stale, `id="request-list" class="space-y-4" hx-swap-oob="true"`, "bob-laptop", `hx-post="/requests/req-1/approve"`)
	lacks(t, stale, `id="server-status"`, `id="killswitch-panel"`)

	rev := revOf(t, r.get("/requests", hx...), "rev_requests")
	same := r.get(pollURL(url.Values{"rev_requests": {rev}}), hx...)
	lacks(t, same, `id="request-list"`, "bob-laptop")
	contains(t, same, `data-pending-count="1"`)

	// 新しい要求が届くと版が変わり、一覧が差し替わる。
	seedRequest(r.f, "req-2", "carol-phone")
	changed := r.get(pollURL(url.Values{"rev_requests": {rev}}), hx...)
	contains(t, changed, `id="request-list"`, "bob-laptop", "carol-phone", `data-pending-count="2"`)
	if got := revOf(t, changed, "rev_requests"); got == rev {
		t.Errorf("rev did not change: %s", got)
	}

	// ダッシュボード: キルスイッチのパネルも、版が同じなら返さない。
	ksRev := revOf(t, r.get("/killswitch", hx...), "rev_killswitch")
	ksSame := r.get(pollURL(url.Values{"rev_killswitch": {ksRev}}), hx...)
	contains(t, ksSame, `id="server-status"`, `id="sessions"`, `id="lease"`)
	lacks(t, ksSame, `id="killswitch-panel"`)
	r.f.KillSwitch.Snapshot = service.KillSwitchSnapshot{Active: true, TriggeredAt: r.f.Clock.T, Source: "gui"}
	ksChanged := r.get(pollURL(url.Values{"rev_killswitch": {ksRev}}), hx...)
	contains(t, ksChanged, `id="killswitch-panel"`, `data-killswitch="active"`)
}

func TestPollDeliversToastsOnce(t *testing.T) {
	r := newRig(t)
	r.f.Bus.Publish(service.Event{Topic: service.TopicToast, Data: service.Toast{Level: "error", Message: "ホットキーを登録できませんでした。"}})
	r.f.Bus.Publish(service.Event{Topic: service.TopicToast, Data: service.Toast{Level: "info", Message: "設定を保存しました。"}})

	first := r.get("/poll", hx...)
	contains(t, first, `hx-swap-oob="beforeend:#toast-root"`, "ホットキーを登録できませんでした。", `data-toast-level="error"`,
		"設定を保存しました。", `data-toast-level="info"`)

	// 取り出し済みのトーストは、次の応答に出ない。
	second := r.get("/poll", hx...)
	lacks(t, second, "toast-root", "ホットキーを登録できませんでした。", "設定を保存しました。")
}

func TestPollLeaksNoSecrets(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedClient(f, "c1", "alice-pc", true, true, true)
		seedToken(f, "c1", "t1", "ci", nil, nil)
		seedRequest(f, "req-1", "bob-laptop")
	})
	res := r.get(pollURL(url.Values{"rev_killswitch": {"x"}, "rev_requests": {"x"}}), hx...)
	status(t, res, http.StatusOK)
	// トークン（平文・接頭辞）・ハッシュ・Bearer は、どの領域にも出ない。
	lacks(t, res, "prm_", "Bearer", "token", "secret", "hash", "password")
}

// 承認待ちは、ダッシュボードの #lease の OOB（ポーリングのたびに現在の状態で描く）に出る。
func TestPollShowsPendingApprovalInTheLeasePanel(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Leases.HasPending = true
		f.Leases.PendingLease = service.PendingLease{LeaseID: "l1", Actor: domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "claude-desktop"}, Since: f.Clock.T}
	})
	res := r.get(pollURL(url.Values{"rev_killswitch": {"stale"}}), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="lease" hx-swap-oob="innerHTML"`, "承認待ち", "claude-desktop", "MCP")
	lacks(t, res, "強制剥奪", "/lease/revoke")
}
