package gui_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

func TestDashboardShowsServerState(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Status.V = service.ServerStatus{
			Tailscaled: service.TailscaledRunning, TailnetIPs: []string{"100.64.0.1", "fd7a:115c:a1e0::1"}, DNSName: "host.tail1234.ts.net",
			Port: 8765, Listening: true, ListenAddrs: []string{"100.64.0.1:8765"},
		}
		f.Sessions.Items = []service.SessionInfo{{ID: "s1", ClientID: "c1", Label: "claude-desktop", Kind: domain.ActorMCP, Since: f.Clock.T.Add(-10 * time.Minute)}}
		f.Leases.Held = true
		f.Leases.H = service.Holder{LeaseID: "l1", Actor: domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "claude-desktop"}, Since: f.Clock.T.Add(-time.Minute), IdleRemaining: 4*time.Minute + 30*time.Second}
	})
	res := r.get("/")
	status(t, res, http.StatusOK)
	contains(t, res,
		"<!doctype html>", "<html", "稼働中", "100.64.0.1", "fd7a:115c:a1e0::1", "host.tail1234.ts.net", ">8765<", "100.64.0.1:8765",
		"claude-desktop", "MCP", "保持中", "4分30秒",
		// 全 HTMX リクエストに付くヘッダーと SSE の購読（ウィンドウごとに 1 本）。
		`sse-connect="/events"`, `hx-ext="sse,response-targets"`, `X-Puppet-Remo`,
		`sse-swap="server-status"`, `sse-swap="sessions"`, `sse-swap="lease"`, `sse-swap="killswitch"`,
	)
	// M1 のダッシュボードには強制剥奪も実行中タスクも無い。
	lacks(t, res, "/lease/revoke", "強制剥奪", "タスク", "/tasks")
	if got := res.Header.Get("Cache-Control"); got != "no-store" && got != "" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestDashboardEmptyAndDegradedStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*guitest.Fakes)
		want   []string
		not    []string
	}{
		{"no sessions and idle lease", func(*guitest.Fakes) {}, []string{"接続中のセッションはありません", "空き", "確認中", "停止中"}, []string{"保持中", "data-session"}},
		{"tailscaled needs login", func(f *guitest.Fakes) {
			f.Status.V = service.ServerStatus{Tailscaled: service.TailscaledNeedsLogin, Port: 8765, Reason: "tailscaled にログインしていません。"}
		}, []string{"要ログイン", "tailscaled にログインしていません。", "未取得"}, nil},
		{"tailscaled unavailable", func(f *guitest.Fakes) {
			f.Status.V = service.ServerStatus{Tailscaled: service.TailscaledUnavailable, Port: 9000}
		}, []string{"接続できません", ">9000<"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := newRig(t, tt.mutate).get("/")
			status(t, res, http.StatusOK)
			contains(t, res, tt.want...)
			lacks(t, res, tt.not...)
		})
	}
}

// 状態駆動レンダリング: キルスイッチは、発動前は発動のボタンだけ、発動中は解除のボタンだけを HTML に出す。
func TestDashboardKillSwitchIsStateDriven(t *testing.T) {
	t.Run("inactive renders trigger only", func(t *testing.T) {
		res := newRig(t).get("/")
		contains(t, res, `hx-post="/killswitch/trigger"`, "キルスイッチを発動", `data-killswitch="inactive"`, "稼働中")
		lacks(t, res, "/killswitch/release", "キルスイッチを解除", `data-killswitch="active"`, "キルスイッチ発動中")
	})
	t.Run("active renders release only", func(t *testing.T) {
		res := newRig(t, func(f *guitest.Fakes) {
			f.KillSwitch.Snapshot = service.KillSwitchSnapshot{Active: true, TriggeredAt: f.Clock.T, Source: "hotkey"}
		}).get("/")
		contains(t, res, `hx-get="/killswitch/release/confirm"`, "キルスイッチを解除", `data-killswitch="active"`, "キルスイッチ発動中", "ホットキー")
		lacks(t, res, "/killswitch/trigger", "キルスイッチを発動", `data-killswitch="inactive"`)
	})
}

func TestDashboardHTMXRequestsReturnFragments(t *testing.T) {
	r := newRig(t)
	t.Run("boosted navigation returns main content with sidebar OOB and no document", func(t *testing.T) {
		res := r.get("/", "HX-Request", "true", "HX-Boosted", "true")
		status(t, res, http.StatusOK)
		contains(t, res, "<title>ダッシュボード - puppet-remo</title>", `id="sidebar-nav"`, `hx-swap-oob="true"`, "サーバー状態")
		lacks(t, res, "<html", "<body", "<!doctype")
	})
	t.Run("history restore gets the full page", func(t *testing.T) {
		res := r.get("/", "HX-Request", "true", "HX-History-Restore-Request", "true")
		contains(t, res, "<html", "<body")
	})
}

func TestKillSwitchPanelRoute(t *testing.T) {
	r := newRig(t)
	res := r.get("/killswitch", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="killswitch-panel"`, `hx-trigger="sse:killswitch"`)
	lacks(t, res, "<html")
}

func TestTriggerAndReleaseKillSwitch(t *testing.T) {
	r := newRig(t)

	res := r.post("/killswitch/trigger", nil, hx...)
	status(t, res, http.StatusOK)
	must(t, r.f.KillSwitch.TriggerCalls == 1 && r.f.KillSwitch.LastSource == "gui", "Trigger calls=%d source=%q", r.f.KillSwitch.TriggerCalls, r.f.KillSwitch.LastSource)
	// パネルは解除だけ、ヘッダーのバッジと完了のトーストが OOB で添えられる。
	contains(t, res, `data-killswitch="active"`, `hx-get="/killswitch/release/confirm"`, `id="header-killswitch" hx-swap-oob="innerHTML"`,
		"キルスイッチ発動中", "キルスイッチを発動しました。", `hx-swap-oob="beforeend:#toast-root"`)
	lacks(t, res, "/killswitch/trigger", "<html")

	res = r.get("/killswitch/release/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, `hx-post="/killswitch/release"`, `hx-target="#killswitch-panel"`, "解除する", `data-clear="#modal-root"`)

	res = r.post("/killswitch/release", nil, hx...)
	status(t, res, http.StatusOK)
	must(t, r.f.KillSwitch.ReleaseCalls == 1, "Release calls = %d", r.f.KillSwitch.ReleaseCalls)
	contains(t, res, `data-killswitch="inactive"`, `hx-post="/killswitch/trigger"`, `id="modal-root" hx-swap-oob="true"`, "キルスイッチを解除しました。")
	lacks(t, res, "/killswitch/release\"")
}

func TestKillSwitchErrors(t *testing.T) {
	t.Run("cleanup failure still shows active state with an error toast", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) { f.KillSwitch.TriggerErr = errCleanup })
		res := r.post("/killswitch/trigger", nil, hx...)
		status(t, res, http.StatusOK)
		contains(t, res, `data-killswitch="active"`, `data-toast-level="error"`, "一部の後始末に失敗しました。")
		lacks(t, res, "cleanup boom")
	})
	t.Run("trigger that could not take effect is a 500 error fragment", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) { f.KillSwitch.TriggerErr = guitest.ErrNotTriggered })
		res := r.post("/killswitch/trigger", nil, hx...)
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		if res.Header.Get("HX-Retarget") != "#toast-root" || res.Header.Get("HX-Reswap") != "beforeend" {
			t.Errorf("retarget headers = %q / %q", res.Header.Get("HX-Retarget"), res.Header.Get("HX-Reswap"))
		}
		lacks(t, res, "kill switch could not be persisted")
	})
	t.Run("release failure keeps the active state", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) {
			f.KillSwitch.Snapshot = service.KillSwitchSnapshot{Active: true, TriggeredAt: f.Clock.T, Source: "gui"}
			f.KillSwitch.ReleaseErr = errCleanup
		})
		res := r.post("/killswitch/release", nil, hx...)
		status(t, res, http.StatusInternalServerError)
		must(t, r.f.KillSwitch.Active(), "kill switch must stay active")
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, "cleanup boom")
	})
	t.Run("release confirmation is not offered when nothing is active", func(t *testing.T) {
		res := newRig(t).get("/killswitch/release/confirm", hx...)
		status(t, res, http.StatusConflict)
		contains(t, res, `data-error-code="conflict"`)
		lacks(t, res, "role=\"dialog\"")
	})
}

func TestUnknownRoutes(t *testing.T) {
	r := newRig(t)
	for _, target := range []string{"/nope", "/lease/revoke", "/wails/runtime.js", "/viewer", "/audit", "/tasks", "/agent", "/approvals/x"} {
		res := r.get(target)
		status(t, res, http.StatusNotFound)
		contains(t, res, "<html", `data-error-code="not_found"`, "ページが見つかりません")
	}
	// M1 に無い操作の経路は作らない（メソッド違いでも 404）。
	for _, route := range [][2]string{{http.MethodPost, "/lease/revoke"}, {http.MethodPost, "/tasks/t1/cancel"}, {http.MethodPut, "/agent"}} {
		res := r.do(route[0], route[1], nil, hx...)
		status(t, res, http.StatusNotFound)
	}
	// HTMX の部分更新の 404 はフラグメント（トースト）。
	res := r.get("/nope", hx...)
	status(t, res, http.StatusNotFound)
	lacks(t, res, "<html")
	contains(t, res, `data-error-code="not_found"`)
}
