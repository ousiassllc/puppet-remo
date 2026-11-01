package gui_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

// seedApproval は承認待ちの lease の要求を入れる（残り 42 秒）。
func seedApproval(f *guitest.Fakes, id string, who domain.Actor) {
	f.Approval.Reqs = append(f.Approval.Reqs, service.ApprovalRequest{
		ID: id, Kind: service.ApprovalKindLease, Who: who, ExpiresAt: f.Clock.T.Add(42 * time.Second),
	})
}

var approvalWho = domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "claude-desktop", SessionID: "s1"}

// 承認ダイアログの中身: クライアント名・種別・要求の内容・残り時間・許可 / 拒否。サイドバーは無い。CSP は rig が全 HTML 応答で検査する。
func TestApprovalDialogPage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedApproval(f, "ap-1", approvalWho) })
	res := r.get("/approvals/ap-1")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", `data-approval="ap-1"`, "claude-desktop", "MCP</span>", "操作権（リース）の取得",
		`data-approval-remaining="42"`, `hx-get="/approvals/ap-1"`, `hx-trigger="every 1s"`,
		`hx-post="/approvals/ap-1/allow"`, `hx-post="/approvals/ap-1/deny"`, `hx-disabled-elt="#approval-actions button"`,
		`id="toast-root"`, "/static/js/app.js")
	// 専用ウィンドウ: サイドバー・ヘッダー・ポーリング・モーダルは無い。
	lacks(t, res, `id="live-poll"`, `id="modal-root"`, "ダッシュボード", "キルスイッチ", "接続要求")
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// クライアント名は HTML エスケープされる。秘密・パス・引数・セッション ID は出さない。
func TestApprovalDialogEscapesTheClientNameAndLeaksNothing(t *testing.T) {
	who := domain.Actor{Kind: domain.ActorWeb, ClientID: "c9", Label: `<script>alert(1)</script>`, SessionID: "viewer-session-secret"}
	r := newRig(t, func(f *guitest.Fakes) { seedApproval(f, "ap-1", who) })
	res := r.get("/approvals/ap-1")
	status(t, res, http.StatusOK)
	contains(t, res, "&lt;script&gt;alert(1)&lt;/script&gt;", "Web</span>")
	lacks(t, res, "<script>alert", "viewer-session-secret", "c9")
}

// ラベルが空ならクライアント ID を出す。
func TestApprovalDialogFallsBackToTheClientID(t *testing.T) {
	who := domain.Actor{Kind: domain.ActorMCP, ClientID: "client-77", SessionID: "s1"}
	r := newRig(t, func(f *guitest.Fakes) { seedApproval(f, "ap-1", who) })
	contains(t, r.get("/approvals/ap-1"), "client-77")
}

// HTMX の部分更新（残り時間の再取得）は残り時間の要素だけ。時計が進むと秒数が減り、期限を過ぎれば 0（負にしない）。
func TestApprovalRemainingCountsDown(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedApproval(f, "ap-1", approvalWho) })
	res := r.get("/approvals/ap-1", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="approval-remaining"`, `data-approval-remaining="42"`, ">42<")
	lacks(t, res, "<html", "許可", `id="approval-dialog"`)

	r.f.Clock.T = r.f.Clock.T.Add(41*time.Second + 500*time.Millisecond)
	contains(t, r.get("/approvals/ap-1", hx...), `data-approval-remaining="1"`)
	r.f.Clock.T = r.f.Clock.T.Add(10 * time.Second)
	contains(t, r.get("/approvals/ap-1", hx...), `data-approval-remaining="0"`)
}

// 許可・拒否は Approval.Resolve を呼び、200 + HX-Trigger: closeWindow。
func TestAllowAndDenyResolveTheApproval(t *testing.T) {
	for _, tt := range []struct {
		path  string
		allow bool
		text  string
	}{{"allow", true, "許可しました"}, {"deny", false, "拒否しました"}} {
		t.Run(tt.path, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) { seedApproval(f, "ap-1", approvalWho) })
			res := r.post("/approvals/ap-1/"+tt.path, nil, hx...)
			status(t, res, http.StatusOK)
			if got := res.Header.Get("HX-Trigger"); got != "closeWindow" {
				t.Errorf("HX-Trigger = %q, want closeWindow", got)
			}
			contains(t, res, `id="approval-dialog"`, tt.text)
			lacks(t, res, "<html", "/approvals/ap-1/allow")
			if got := r.f.Approval.Resolved; len(got) != 1 || got[0].ID != "ap-1" || got[0].Allow != tt.allow {
				t.Errorf("Resolve calls = %+v, want ap-1 allow=%v", got, tt.allow)
			}
			// 二度目は解決済みなので 410。結果は変わらない。
			res = r.post("/approvals/ap-1/"+tt.path, nil, hx...)
			status(t, res, http.StatusGone)
			if len(r.f.Approval.Resolved) != 1 {
				t.Errorf("a second Resolve was made: %+v", r.f.Approval.Resolved)
			}
		})
	}
}

// 失効（60 秒）・解決済み・存在しない id・Resolve が失効を返す競合は、410 approval_timeout（トースト）。closeWindow は出さない。
func TestApprovalGoneIs410(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		r := newRig(t)
		for _, path := range []string{"/approvals/nope/allow", "/approvals/nope/deny"} {
			res := r.post(path, nil, hx...)
			status(t, res, http.StatusGone)
			contains(t, res, `data-error-code="approval_timeout"`, `role="alert"`)
			lacks(t, res, "<html")
			if res.Header.Get("HX-Retarget") != "#toast-root" || res.Header.Get("HX-Trigger") != "" {
				t.Errorf("headers = %v", res.Header)
			}
		}
		if len(r.f.Approval.Resolved) != 0 {
			t.Errorf("Resolve was called: %+v", r.f.Approval.Resolved)
		}
	})
	t.Run("expired between the check and Resolve", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) {
			seedApproval(f, "ap-1", approvalWho)
			f.Approval.ResolveErr = domain.ErrApprovalTimeout
		})
		res := r.post("/approvals/ap-1/allow", nil, hx...)
		status(t, res, http.StatusGone)
		contains(t, res, `data-error-code="approval_timeout"`)
		if res.Header.Get("HX-Trigger") != "" {
			t.Errorf("HX-Trigger = %q", res.Header.Get("HX-Trigger"))
		}
	})
	t.Run("page", func(t *testing.T) {
		r := newRig(t)
		res := r.get("/approvals/nope")
		status(t, res, http.StatusGone)
		contains(t, res, "<html", `data-approval-gone`, `data-error-code="approval_timeout"`, "終了しています")
		lacks(t, res, "/allow", "/deny")
	})
	t.Run("not a lease request (M4)", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) {
			f.Approval.Reqs = append(f.Approval.Reqs, service.ApprovalRequest{ID: "ap-9", Kind: "safety_check", Who: approvalWho, ExpiresAt: f.Clock.T.Add(time.Minute)})
		})
		status(t, r.get("/approvals/ap-9"), http.StatusGone)
		status(t, r.post("/approvals/ap-9/allow", nil, hx...), http.StatusGone)
		if len(r.f.Approval.Resolved) != 0 {
			t.Errorf("Resolve was called: %+v", r.f.Approval.Resolved)
		}
	})
	t.Run("htmx countdown on an ended request", func(t *testing.T) {
		r := newRig(t)
		res := r.get("/approvals/nope", hx...)
		status(t, res, http.StatusGone)
		contains(t, res, `data-error-code="approval_timeout"`)
		lacks(t, res, "<html")
	})
}
