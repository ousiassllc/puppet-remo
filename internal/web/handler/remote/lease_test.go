package remote_test

import (
	"net/http"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote/remotetest"
)

func TestLeasePanelIdle(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	res := r.get("/viewer/lease", h...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="lease-panel"`, `hx-get="/viewer/lease"`, `remo:lease-changed from:#viewer-root`, "空き", `data-lease-state="idle"`, `hx-post="/viewer/lease/acquire"`)
	lacks(t, res, "/viewer/lease/release", "/viewer/lease/revoke", "<html", "clipboard")
	// 操作権が無いので #viewer-ops は空（OOB で差し替える）。
	contains(t, res, `id="viewer-ops"`, `hx-swap-oob="true"`)
}

func TestLeaseAcquireReleaseCycle(t *testing.T) {
	r := newRig(t)
	id, h := r.viewer()

	res := r.post("/viewer/lease/acquire", nil, h...)
	status(t, res, http.StatusOK)
	contains(t, res, "保持中", "alice-laptop", ">web<", "あなた", `data-lease-state="mine"`, `hx-post="/viewer/lease/release"`,
		// 操作権を持つと、クリップボードとショートカット送信が OOB で現れる（FR-W06・W07）。
		`id="viewer-ops"`, `id="clipboard-panel"`, `id="key-menu"`)
	lacks(t, res, "/viewer/lease/acquire", "/viewer/lease/revoke", "<html")
	if h, ok := r.f.Leases.Holding(); !ok || !h.SameSession(actor(id)) {
		t.Errorf("holder = %+v, %v; want this viewer", h, ok)
	}
	// 取得し直しても失敗しない（同じ保持者の再取得）。
	status(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusOK)

	res = r.post("/viewer/lease/release", nil, h...)
	status(t, res, http.StatusOK)
	contains(t, res, "空き", `hx-post="/viewer/lease/acquire"`)
	lacks(t, res, "/viewer/lease/release", `id="clipboard-panel"`, `id="key-menu"`)
	if _, ok := r.f.Leases.Holding(); ok {
		t.Error("the lease must be released")
	}
	// 解放済みでの解放は何もせず 200。
	status(t, r.post("/viewer/lease/release", nil, h...), http.StatusOK)
}

func TestLeaseAcquireHeldByOtherIs409(t *testing.T) {
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Leases.Hold(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", Label: "agent-x", SessionID: "s1"})
	})
	_, h := r.viewer()
	failed(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusConflict, "lease_held_by_other")
}

// 強制剥奪（FR-L06）: 他者（mcp）の保持者を idle にし、このビューアは改めて取得する。
func TestLeaseRevokeOthers(t *testing.T) {
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Leases.Hold(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", Label: "agent-x", SessionID: "s1"})
	})
	id, h := r.viewer()
	res := r.post("/viewer/lease/revoke", nil, h...)
	status(t, res, http.StatusOK)
	contains(t, res, "空き", `hx-post="/viewer/lease/acquire"`)
	lacks(t, res, "/viewer/lease/revoke", "agent-x")
	if _, ok := r.f.Leases.Holding(); ok {
		t.Error("the holder must be revoked")
	}
	// 剥奪の操作主体は、このビューア（種別 web）。
	if got := r.f.Leases.Revokers; len(got) != 1 || !got[0].SameSession(actor(id)) {
		t.Errorf("revokers = %+v, want this web viewer", got)
	}
}

func TestLeaseRevokeWhenIdleIsANoOp(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	res := r.post("/viewer/lease/revoke", nil, h...)
	status(t, res, http.StatusOK)
	contains(t, res, "空き")
}

// 自分の保持は剥奪ではなく解放を使う（ボタンは描画されないが、古い画面からの要求は 409 conflict）。
func TestLeaseRevokeOwnIsConflict(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	status(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusOK)
	failed(t, r.post("/viewer/lease/revoke", nil, h...), http.StatusConflict, "conflict")
	if _, ok := r.f.Leases.Holding(); !ok {
		t.Error("the viewer's own lease must stay")
	}
}

func TestLeaseRoutesNeedAValidViewerID(t *testing.T) {
	r := newRig(t)
	other, _ := r.viewer()
	tests := []struct {
		name   string
		header []string
	}{
		{"無い", nil},
		{"未知", []string{"X-Viewer-Id", "UNKNOWN"}},
		{"空", []string{"X-Viewer-Id", ""}},
		{"別のクライアントに束縛された viewer_id", nil},
	}
	r.f.Sessions.Rebind(other, "c-other")
	tests[3].header = []string{"X-Viewer-Id", other}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, req := range []resp{
				r.get("/viewer/lease", tt.header...),
				r.post("/viewer/lease/acquire", nil, tt.header...),
				r.post("/viewer/lease/release", nil, tt.header...),
				r.post("/viewer/lease/revoke", nil, tt.header...),
			} {
				failed(t, req, http.StatusBadRequest, "invalid_argument")
			}
		})
	}
	if r.f.Leases.Acquires != 0 || r.f.Leases.Releases != 0 || len(r.f.Leases.Revokers) != 0 {
		t.Error("an invalid viewer_id must not reach the lease manager")
	}
}

// 承認待ち（pending_approval。FR-L07）: 要求者本人には「ホストの承認を待っています」、他のビューアには要求者の表示。
// どちらも操作（取得・解放・強制剥奪）のボタンと #viewer-ops の中身は無い（操作権を持たない）。
func TestLeasePanelPendingApproval(t *testing.T) {
	r := newRig(t)
	id, h := r.viewer()
	r.f.Leases.SetPending(actor(id))

	res := r.get("/viewer/lease", h...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="lease-panel"`, `data-lease-state="pending_approval"`, "承認待ち", "alice-laptop", ">web<", "あなた", "ホストの承認を待っています",
		`id="viewer-ops"`, `hx-swap-oob="true"`)
	lacks(t, res, "/viewer/lease/acquire", "/viewer/lease/release", "/viewer/lease/revoke", `id="clipboard-panel"`, `id="key-menu"`, "<html")

	// 別のビューアから見ると、要求者の表示だけで「あなた」「待っています」は無い。
	_, other := r.viewer()
	r.f.Leases.SetPending(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", Label: "agent-x", SessionID: "s1"})
	res = r.get("/viewer/lease", other...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-lease-state="pending_approval"`, "承認待ち", "agent-x", ">mcp<")
	lacks(t, res, "あなた", "ホストの承認を待っています", "/viewer/lease/acquire", "/viewer/lease/release", "/viewer/lease/revoke", `id="clipboard-panel"`)
}

// 承認待ちの間の取得は、本人でも 409 lease_held_by_other（LeaseManager の規則）。
func TestLeaseAcquireDuringPendingApprovalIs409(t *testing.T) {
	r := newRig(t)
	id, h := r.viewer()
	r.f.Leases.SetPending(actor(id))
	failed(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusConflict, "lease_held_by_other")
}

// ホストの拒否は 403 approval_denied、タイムアウトは 408 approval_timeout のトースト。リースは取れず、パネルは空きのまま。
func TestLeaseAcquireApprovalFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		want int
	}{
		{"denied", domain.ErrApprovalDenied, "approval_denied", http.StatusForbidden},
		{"timeout", domain.ErrApprovalTimeout, "approval_timeout", http.StatusRequestTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.f.Leases.AcquireErr = tt.err
			_, h := r.viewer()
			res := r.post("/viewer/lease/acquire", nil, h...)
			failed(t, res, tt.want, tt.code)
			lacks(t, res, "id=\"lease-panel\"")
			if _, ok := r.f.Leases.Holding(); ok {
				t.Error("the lease must not be held after a failed approval")
			}
			// 失敗のあとのパネルは空き（取得できる）。
			contains(t, r.get("/viewer/lease", h...), `data-lease-state="idle"`, `hx-post="/viewer/lease/acquire"`)
		})
	}
}
