package remote_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

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

// 承認待ちの本人の取得（再試行）は、新しい承認を作らず 202 の承認待ちのパネルを返す。他のビューアの取得は 409 lease_held_by_other。
func TestLeaseAcquireDuringPendingApproval(t *testing.T) {
	r := newRig(t)
	id, h := r.viewer()
	r.f.Leases.SetPending(actor(id))
	res := r.post("/viewer/lease/acquire", nil, h...)
	status(t, res, http.StatusAccepted)
	contains(t, res, `data-lease-state="pending_approval"`, "あなた", "ホストの承認を待っています")
	if r.f.Leases.Requests != 0 {
		t.Errorf("approval requests = %d, want 0 (the viewer is already pending)", r.f.Leases.Requests)
	}

	_, other := r.viewer()
	failed(t, r.post("/viewer/lease/acquire", nil, other...), http.StatusConflict, "lease_held_by_other")
}

// 承認が要るクライアントの取得は、結果を待たずに 202 と承認待ちのパネル（本人）を返す。再送しても承認の要求は 1 件のまま。
func TestLeaseAcquireNeedingApprovalReturns202Immediately(t *testing.T) {
	r := newRig(t, func(f *remotetest.Fakes) { f.Leases.ApprovalRequired = true })
	id, h := r.viewer()
	res := r.post("/viewer/lease/acquire", nil, h...)
	status(t, res, http.StatusAccepted)
	contains(t, res, `id="lease-panel"`, `data-lease-state="pending_approval"`, "承認待ち", "alice-laptop", "あなた", "ホストの承認を待っています",
		`id="viewer-ops"`, `hx-swap-oob="true"`)
	lacks(t, res, "/viewer/lease/acquire", "/viewer/lease/release", `id="clipboard-panel"`, "<html")
	if _, ok := r.f.Leases.Holding(); ok {
		t.Error("the lease must not be held while waiting for the approval")
	}

	// 再試行（408 などのあとの再送）。承認は二重にならない。
	for range 2 {
		retry := r.post("/viewer/lease/acquire", nil, h...)
		status(t, retry, http.StatusAccepted)
		contains(t, retry, `data-lease-state="pending_approval"`, "ホストの承認を待っています")
	}
	if n := r.f.Leases.Requests; n != 1 {
		t.Errorf("approval requests = %d, want 1", n)
	}
	if p, ok := r.f.Leases.Pending(); !ok || !p.Actor.SameSession(actor(id)) {
		t.Errorf("pending = %+v, %v; want this viewer", p, ok)
	}
}

// 承認が要らなければ従来どおり 200 の保持中のパネル。
func TestLeaseAcquireWithoutApprovalIs200(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	status(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusOK)
}

// クールダウン中（拒否・タイムアウトの直後）の取得は、承認を求めずに 403 approval_denied。タイムアウトの失敗の表示も 408 approval_timeout。
// リースは取れず、パネルは空きのまま。
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

// FR-W08（M4）: 保持者が agent のとき、実行中のタスクの要約（状態・ステップ数 / 上限・経過と上限・開始時刻・依頼元の表示名）を出す。
// ビューアは依頼元とは別のクライアントなので、指示（prompt）と最終回答は出さない。強制剥奪の操作は他の保持者と同じ。
func TestLeasePanelShowsTheRunningAgentTask(t *testing.T) {
	started := remotetest.Now.Add(-95 * time.Second)
	agent := domain.Actor{Kind: domain.ActorAgent, ClientID: "c9", Label: "bot-9", SessionID: "task-1"}
	task := domain.Task{
		ID: "task-1", ClientID: "c9", ClientLabel: "requester-9", Prompt: "SECRET-PROMPT open the bank page", ResultText: "SECRET-RESULT",
		Status: domain.TaskRunning, StepCount: 3, MaxSteps: 50, MaxDurationSec: 600, StartedAt: &started, CreatedAt: started,
	}
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Leases.Hold(agent)
		f.Tasks.SetRunning(domain.Task{ID: "other-task", ClientLabel: "x", Status: domain.TaskRunning}, task)
	})
	_, h := r.viewer()
	res := r.get("/viewer/lease", h...) // r.get は CSP に適合した HTML かも検査する
	status(t, res, http.StatusOK)
	contains(t, res, `data-lease-state="other"`, "data-lease-task", "AI エージェントのタスク実行中", "実行中", "3 / 50", "1分35秒", "上限 10分",
		started.Local().Format("15:04:05"), "requester-9", `hx-post="/viewer/lease/revoke"`)
	lacks(t, res, "SECRET-PROMPT", "SECRET-RESULT", "task-1", "other-task")
}

func TestLeasePanelShowsAPendingAgentTask(t *testing.T) {
	agent := domain.Actor{Kind: domain.ActorAgent, ClientID: "c9", Label: "bot-9", SessionID: "task-1"}
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Leases.SetPending(agent)
		f.Tasks.SetRunning(domain.Task{ID: "task-1", ClientLabel: "requester-9", Prompt: "SECRET-PROMPT", Status: domain.TaskPendingApproval, MaxSteps: 50, MaxDurationSec: 600})
	})
	_, h := r.viewer()
	res := r.get("/viewer/lease", h...)
	contains(t, res, `data-lease-state="pending_approval"`, "data-lease-task", "承認待ち", "0 / 50", "requester-9")
	lacks(t, res, "SECRET-PROMPT", "data-task-started", "/viewer/lease/revoke", "/viewer/lease/acquire")
}

// agent 以外の保持者（mcp・web）では、タスクの欄を出さず、タスクの読み取りもしない。
func TestLeasePanelHasNoTaskSummaryForOtherHolders(t *testing.T) {
	for name, holder := range map[string]domain.Actor{
		"mcp": {Kind: domain.ActorMCP, ClientID: "c9", Label: "bot-9", SessionID: "s1"},
		"web": {Kind: domain.ActorWeb, ClientID: "c8", Label: "other-viewer", SessionID: "v1"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, func(f *remotetest.Fakes) {
				f.Leases.Hold(holder)
				f.Tasks.SetRunning(domain.Task{ID: "s1", ClientLabel: "requester", Prompt: "SECRET-PROMPT", Status: domain.TaskRunning})
			})
			_, h := r.viewer()
			res := r.get("/viewer/lease", h...)
			lacks(t, res, "data-lease-task", "AI エージェント", "SECRET-PROMPT", "requester")
			if r.f.Tasks.Calls != 0 {
				t.Errorf("tasks were read %d times for a non-agent holder", r.f.Tasks.Calls)
			}
		})
	}
}

// タスクが読めない・見つからないときも、パネルは保持者の表示と操作だけで描画する（失敗にしない）。
func TestLeasePanelWithoutTheTaskStillRenders(t *testing.T) {
	agent := domain.Actor{Kind: domain.ActorAgent, ClientID: "c9", Label: "bot-9", SessionID: "task-1"}
	for name, setup := range map[string]func(f *remotetest.Fakes){
		"read error": func(f *remotetest.Fakes) { f.Tasks.Err = errors.New("db is down") },
		"not found":  func(f *remotetest.Fakes) { f.Tasks.SetRunning() },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, func(f *remotetest.Fakes) { f.Leases.Hold(agent); setup(f) })
			_, h := r.viewer()
			res := r.get("/viewer/lease", h...)
			status(t, res, http.StatusOK)
			contains(t, res, "bot-9", `data-lease-state="other"`, `hx-post="/viewer/lease/revoke"`)
			lacks(t, res, "data-lease-task", "db is down")
		})
	}
}

// ビューアのページの初期表示（GET /viewer）にも、agent のタスクの要約が入る。
func TestViewerPageShowsTheRunningAgentTask(t *testing.T) {
	agent := domain.Actor{Kind: domain.ActorAgent, ClientID: "c9", Label: "bot-9", SessionID: "task-1"}
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Leases.Hold(agent)
		f.Tasks.SetRunning(domain.Task{ID: "task-1", ClientLabel: "requester-9", Prompt: "SECRET-PROMPT", Status: domain.TaskRunning, StepCount: 1, MaxSteps: 10, MaxDurationSec: 60})
	})
	res := r.get("/viewer")
	status(t, res, http.StatusOK)
	contains(t, res, "data-lease-task", "1 / 10", "requester-9")
	lacks(t, res, "SECRET-PROMPT")
}
