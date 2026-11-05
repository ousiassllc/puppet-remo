package gui_test

import (
	"net/http"
	"strings"
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
		// 全 HTMX リクエストに付くヘッダーと、GET /poll のポーリング（SSE は購読しない。Wails の応答バッファのため）。
		`hx-ext="response-targets"`, `X-Puppet-Remo`,
		`id="live-poll"`, `hx-get="/poll"`, `hx-trigger="every 3s"`, `hx-swap="none"`, `hx-include="[data-poll-rev]"`,
		`id="server-status"`, `id="sessions"`, `id="lease"`, `id="header-killswitch"`, `name="rev_killswitch"`,
	)
	lacks(t, res, "sse-connect", "sse-swap", "sse:", "sse.min.js", "/events")
	// 保持者（MCP）がいるので強制剥奪のボタンが出る（FR-H02）。実行中のタスクが無ければ、実行中タスクのパネルの中身は出ない
	// （差し込み先 #running-tasks と版の隠し入力だけ。サイドバーの「タスク」は常にある）。
	contains(t, res, `hx-get="/lease/revoke/confirm"`, "強制剥奪", `href="/tasks"`, `id="running-tasks"`, `name="rev_tasks"`)
	lacks(t, res, "data-running-task", "実行中のエージェントタスク", "/cancel")
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
		{"no sessions and idle lease", func(*guitest.Fakes) {}, []string{`data-empty="sessions"`, "接続中のセッションはありません", "空き", "確認中", "停止中"}, []string{"保持中", "data-session"}},
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

// 狭い幅（約 360px）でサイドバーがページを横に広げない（#51）。md 未満のナビは本文の外に出る左のドロワー（fixed。ハンバーガーで開き、
// スクラムで閉じる。P0）で、md 以上だけ常時見える左のカラム（static）になる。見た目は Go のテストで測れないので、クラスで押さえる。
func TestSidebarIsADrawerBelowMd(t *testing.T) {
	res := newRig(t).get("/")
	start := strings.Index(res.Body, `<nav id="sidebar-nav"`)
	if start < 0 {
		t.Fatal("no sidebar nav")
	}
	nav := res.Body[start : start+strings.Index(res.Body[start:], ">")]
	for _, want := range []string{"nav-drawer", "fixed", "inset-y-0", "left-0", "z-(--z-drawer)", "w-[78%]", "md:w-60", "md:static"} {
		if !strings.Contains(nav, want) {
			t.Errorf("sidebar nav lacks %q: %s", want, nav)
		}
	}
	contains(t, res, `id="nav-scrim"`, "md:hidden", `data-nav-toggle`, `aria-controls="sidebar-nav"`, `aria-expanded="false"`, "flex-col md:flex-row", "p-4 md:p-6")
	if strings.Index(res.Body, `id="nav-scrim"`) > start {
		t.Error("the scrim must come before the nav (both are fixed at the same z-index)")
	}
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
	contains(t, res, `id="killswitch-panel"`, `name="rev_killswitch"`)
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
		contains(t, res, `data-killswitch="active"`, `id="alert-root" hx-swap-oob="innerHTML"`, `data-error-code="internal"`, "一部の後始末に失敗しました。")
		lacks(t, res, "cleanup boom")
	})
	t.Run("trigger that could not take effect is a 500 error fragment", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) { f.KillSwitch.TriggerErr = guitest.ErrNotTriggered })
		res := r.post("/killswitch/trigger", nil, hx...)
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		if res.Header.Get("HX-Retarget") != "#alert-root" || res.Header.Get("HX-Reswap") != "innerHTML" {
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
	for _, target := range []string{"/nope", "/lease/revoke", "/wails/runtime.js", "/viewer", "/tasks/t1/cancel"} {
		res := r.get(target)
		status(t, res, http.StatusNotFound)
		contains(t, res, "<html", `data-error-code="not_found"`, "ページが見つかりません")
	}
	// 無い操作の経路は作らない（メソッド違いでも 404。/lease/revoke と /tasks/{id}/cancel は POST だけ、/tasks は GET だけ、/agent は GET と PUT だけ）。
	for _, route := range [][2]string{{http.MethodPut, "/lease/revoke"}, {http.MethodPut, "/tasks/t1/cancel"}, {http.MethodPost, "/tasks"}, {http.MethodDelete, "/tasks/t1"}, {http.MethodPost, "/agent"}, {http.MethodGet, "/agent/api-key"}, {http.MethodPost, "/agent/fastpath"}} {
		res := r.do(route[0], route[1], nil, hx...)
		status(t, res, http.StatusNotFound)
	}
	// HTMX の部分更新の 404 はフラグメント（トースト）。
	res := r.get("/nope", hx...)
	status(t, res, http.StatusNotFound)
	lacks(t, res, "<html")
	contains(t, res, `data-error-code="not_found"`)
}

// 状態駆動レンダリング: 強制剥奪のボタンは、他者（mcp・web・agent）が保持中のときだけ HTML に出る（FR-H02、FR-L06、FR-L08）。
func TestDashboardRevokeButtonIsStateDriven(t *testing.T) {
	holder := func(kind domain.ActorKind) func(*guitest.Fakes) {
		return func(f *guitest.Fakes) {
			f.Leases.Held = true
			f.Leases.H = service.Holder{LeaseID: "l1", Actor: domain.Actor{Kind: kind, ClientID: "c1", Label: "laptop"}, Since: f.Clock.T, IdleRemaining: time.Minute}
		}
	}
	tests := []struct {
		name   string
		mutate func(*guitest.Fakes)
		want   []string
		not    []string
	}{
		{"idle", func(*guitest.Fakes) {}, []string{"空き"}, []string{"/lease/revoke", "強制剥奪"}},
		{"held by mcp", holder(domain.ActorMCP), []string{`hx-get="/lease/revoke/confirm"`, "強制剥奪", "MCP"}, []string{"タスクが中断されます"}},
		{"held by web", holder(domain.ActorWeb), []string{`hx-get="/lease/revoke/confirm"`, "強制剥奪", "Web", "laptop"}, []string{"タスクが中断されます"}},
		// agent の剥奪はそのタスクの中断を伴う（FR-L06・L08）ので、ボタンに加えてその旨を添える。
		{"held by agent", holder(domain.ActorAgent), []string{`hx-get="/lease/revoke/confirm"`, "強制剥奪", "エージェント", "保持中", "タスクが中断されます"}, nil},
		// 保持者になれない種別（host・system）は、仮に保持者として返っても剥奪のボタンを出さない（種別から決める）。
		{"held by host", holder(domain.ActorHost), []string{"保持中", "ホスト"}, []string{"/lease/revoke", "強制剥奪", "タスクが中断されます"}},
		{"held by system", holder(domain.ActorSystem), []string{"保持中"}, []string{"/lease/revoke", "強制剥奪"}},
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

func TestRevokeLease(t *testing.T) {
	held := func(f *guitest.Fakes) {
		f.Leases.Held = true
		f.Leases.H = service.Holder{LeaseID: "l1", Actor: domain.Actor{Kind: domain.ActorWeb, ClientID: "c1", Label: "laptop"}, Since: f.Clock.T, IdleRemaining: time.Minute}
	}
	t.Run("revokes as the host and returns the lease body", func(t *testing.T) {
		r := newRig(t, held)
		res := r.post("/lease/revoke", nil, hx...)
		status(t, res, http.StatusOK)
		must(t, r.f.Leases.RevokeCalls == 1, "Revoke calls = %d, want 1", r.f.Leases.RevokeCalls)
		must(t, r.f.Leases.LastBy == service.HostActor && r.f.Leases.LastBy.Kind == domain.ActorHost, "Revoke by = %+v, want the host actor", r.f.Leases.LastBy)
		// 保持者は空きになり、ボタンは消える。完了のトーストが OOB で添えられる。
		contains(t, res, "空き", "操作権を剥奪しました。", `hx-swap-oob="beforeend:#toast-root"`, `id="modal-root" hx-swap-oob="true"`)
		lacks(t, res, "/lease/revoke", "強制剥奪", "保持中", "<html", "laptop")
	})
	t.Run("idle is an idempotent 200 without a toast", func(t *testing.T) {
		r := newRig(t)
		res := r.post("/lease/revoke", nil, hx...)
		status(t, res, http.StatusOK)
		must(t, r.f.Leases.RevokeCalls == 1, "Revoke calls = %d, want 1", r.f.Leases.RevokeCalls)
		contains(t, res, "空き", `id="modal-root" hx-swap-oob="true"`)
		lacks(t, res, "/lease/revoke", "操作権を剥奪しました。", "toast-root")
	})
	t.Run("a service error is a fragment error and keeps the holder", func(t *testing.T) {
		r := newRig(t, held, func(f *guitest.Fakes) { f.Leases.RevokeErr = errCleanup })
		res := r.post("/lease/revoke", nil, hx...)
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, "cleanup boom")
		if _, ok := r.f.Leases.Holder(); !ok {
			t.Error("the holder was removed although Revoke failed")
		}
	})
}

// 強制剥奪は確認モーダルを経る（O2。クライアント・トークンの削除などと同じ）: 確認の GET がモーダルを描画し、確定のボタンだけが POST を送る。
func TestConfirmRevokeLease(t *testing.T) {
	holder := func(kind domain.ActorKind) func(*guitest.Fakes) {
		return func(f *guitest.Fakes) {
			f.Leases.Held = true
			f.Leases.H = service.Holder{LeaseID: "l1", Actor: domain.Actor{Kind: kind, ClientID: "c1", Label: "laptop"}, Since: f.Clock.T, IdleRemaining: time.Minute}
		}
	}
	t.Run("a holder that can be revoked gets a confirm modal that posts the revoke", func(t *testing.T) {
		r := newRig(t, holder(domain.ActorWeb))
		res := r.get("/lease/revoke/confirm", hx...)
		status(t, res, http.StatusOK)
		contains(t, res, `role="dialog"`, "操作権を強制剥奪しますか？", "laptop", `hx-post="/lease/revoke"`, `hx-target="#lease"`, `hx-swap="innerHTML"`, "剥奪する")
		lacks(t, res, "タスクが中断されます", "<html")
		must(t, r.f.Leases.RevokeCalls == 0, "Revoke calls = %d, want 0 (only the confirm button revokes)", r.f.Leases.RevokeCalls)
	})
	t.Run("an agent holder adds the task interruption notice", func(t *testing.T) {
		res := newRig(t, holder(domain.ActorAgent)).get("/lease/revoke/confirm", hx...)
		status(t, res, http.StatusOK)
		contains(t, res, "タスクが中断されます")
	})
	t.Run("without a revocable holder it is a 409 alert and no modal", func(t *testing.T) {
		for name, mutate := range map[string]func(*guitest.Fakes){"idle": func(*guitest.Fakes) {}, "host": holder(domain.ActorHost)} {
			res := newRig(t, mutate).get("/lease/revoke/confirm", hx...)
			status(t, res, http.StatusConflict)
			contains(t, res, `data-error-code="conflict"`)
			lacks(t, res, `role="dialog"`)
			if res.Header.Get("HX-Retarget") != "#alert-root" {
				t.Errorf("%s: HX-Retarget = %q", name, res.Header.Get("HX-Retarget"))
			}
		}
	})
}

// 承認待ち（pending_approval。FR-L07）: 「承認待ち: <名前>（種別）」を出す。保持中ではないので強制剥奪のボタンは出さない
// （docs に承認待ちの剥奪の記述が無く、held のときだけ出す現行の動きを変えない）。
func TestDashboardShowsPendingApproval(t *testing.T) {
	pending := func(f *guitest.Fakes) {
		f.Leases.HasPending = true
		f.Leases.PendingLease = service.PendingLease{LeaseID: "l1", Actor: domain.Actor{Kind: domain.ActorWeb, ClientID: "c1", Label: "alice-laptop"}, Since: f.Clock.T}
	}
	res := newRig(t, pending).get("/")
	status(t, res, http.StatusOK)
	contains(t, res, "承認待ち", "alice-laptop", "Web", "ホストの承認を待っています", "data-lease-pending")
	lacks(t, res, "/lease/revoke", "強制剥奪", "保持中", "アイドル解放まで")

	// ラベルが空ならクライアント ID を出す。
	res = newRig(t, pending, func(f *guitest.Fakes) { f.Leases.PendingLease.Actor.Label = "" }).get("/")
	contains(t, res, "承認待ち", "c1")

	// 保持者がいれば、承認待ちより保持を優先して出す（同時にはならない）。
	res = newRig(t, pending, func(f *guitest.Fakes) {
		f.Leases.Held = true
		f.Leases.H = service.Holder{LeaseID: "l2", Actor: domain.Actor{Kind: domain.ActorMCP, ClientID: "c2", Label: "claude"}, Since: f.Clock.T, IdleRemaining: time.Minute}
	}).get("/")
	contains(t, res, "保持中", "claude")
	lacks(t, res, "承認待ち")
}
