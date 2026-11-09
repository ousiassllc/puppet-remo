package remote_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote/remotetest"
)

// ウィンドウ選択（#157）のハンドラのテスト。一覧は偽の Windows。

func pick(id string, pid uint32, mod func(*domain.WindowInfo), reason domain.GroupReason) domain.PickCandidate {
	w := domain.WindowInfo{ID: id, Title: "title-" + id, Process: "app" + id + ".exe", PID: pid, ProcessStart: 900 + uint64(pid), Class: "Main"}
	if mod != nil {
		mod(&w)
	}
	return domain.PickCandidate{Window: w, Ref: domain.RefOf(w), Reason: reason}
}

func windowsRig(t *testing.T) *rig {
	return newRig(t, func(f *remotetest.Fakes) {
		f.Windows.Set(
			pick("101", 10, func(w *domain.WindowInfo) { w.Foreground = true }, ""),
			pick("102", 11, func(w *domain.WindowInfo) { w.Elevated = true }, domain.ReasonElevatedWindow),
			pick("103", 12, func(w *domain.WindowInfo) { w.Minimized = true }, domain.ReasonTargetMinimized),
		)
	})
}

func form(id, pid, start string) url.Values {
	return url.Values{"window_id": {id}, "pid": {pid}, "process_start": {start}}
}

func TestWindowPickerListsSelectableAndGreyedWindows(t *testing.T) {
	r := windowsRig(t)
	_, h := r.viewer()
	res := r.get("/viewer/windows", h...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="window-picker"`, `hx-get="/viewer/windows"`, "every 3s", "remo:lease-changed", "一覧を更新", `data-window-refresh`,
		"title-101", "app101.exe", "前面", `data-window-id="101"`, `hx-post="/viewer/target"`, "&#34;window_id&#34;:&#34;101&#34;",
		// 昇格・最小化は灰色で選べない（理由つき・「選ぶ」ボタンなし）。
		`data-window-id="102"`, "管理者権限のため選べません", `data-window-id="103"`, "最小化されています",
		// 選べない行だけが aria-disabled="true"（選べる行に空の属性を付けない）。
		`aria-disabled="true"`)
	if n := strings.Count(res.Body, "aria-disabled"); n != 2 {
		t.Errorf("aria-disabled の行 = %d, want 2（昇格・最小化の行だけ）", n)
	}
	lacks(t, res, "&#34;window_id&#34;:&#34;102&#34;", "&#34;window_id&#34;:&#34;103&#34;", "<html")
}

func TestWindowPickerStateWhenTheLeaseIsNotFree(t *testing.T) {
	t.Run("他者が保持中は一覧を出さない", func(t *testing.T) {
		r := newRig(t, func(f *remotetest.Fakes) {
			f.Leases.Hold(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", Label: "agent-x", SessionID: "s1"})
			f.Windows.Set(pick("101", 10, nil, ""))
		})
		_, h := r.viewer()
		res := r.get("/viewer/windows", h...)
		contains(t, res, "ほかのセッションが操作権を保持しています。", "data-window-message")
		lacks(t, res, "title-101", "data-window-refresh")
	})
	t.Run("自分がスコープなしで保持中は解放が先", func(t *testing.T) {
		r := windowsRig(t)
		_, h := r.viewer()
		status(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusOK)
		contains(t, r.get("/viewer/windows", h...), "先に操作権を解放してください")
	})
	t.Run("スコープ付きで保持中は対象を示す", func(t *testing.T) {
		r := windowsRig(t)
		_, h := r.viewer()
		status(t, r.post("/viewer/target", form("101", "10", "910"), h...), http.StatusOK)
		res := r.get("/viewer/windows", h...)
		contains(t, res, "data-window-scoped", "app.exe", "ウィンドウ 1 枚と関連するダイアログ・メニューだけ")
		lacks(t, res, "data-window-list")
	})
	t.Run("一覧を読めないときは理由を出す", func(t *testing.T) {
		r := windowsRig(t)
		r.f.Windows.Err = domain.ErrKillSwitchActive
		_, h := r.viewer()
		contains(t, r.get("/viewer/windows", h...), "data-window-error", "キルスイッチが発動中です。")
	})
}

func TestSelectTargetBindsTheScopedLease(t *testing.T) {
	r := windowsRig(t)
	id, h := r.viewer()
	res := r.post("/viewer/target", form("101", "10", "910"), h...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-lease-state="mine"`, "対象: app.exe", `data-lease-scope`,
		// スコープ付きではクリップボードの読み取りの UI が無く、Alt+Tab・Win+D は無効、Alt+F4・Ctrl+W・Esc は使える。
		`id="clipboard-panel"`, "data-clipboard-write-only", `data-key-disabled`)
	lacks(t, res, "/viewer/clipboard/fetch", `"keys":"alt+tab"`, `"keys":"win+d"`)
	contains(t, res, `&#34;keys&#34;:&#34;alt+f4&#34;`, `&#34;keys&#34;:&#34;escape&#34;`)
	if len(r.f.Leases.Scoped) != 1 || r.f.Leases.Scoped[0].ID != "101" || r.f.Leases.Scoped[0].PID != 10 {
		t.Errorf("scoped targets = %+v, want window 101 (pid 10)", r.f.Leases.Scoped)
	}
	if h, ok := r.f.Leases.Holding(); !ok || !h.SameSession(actor(id)) {
		t.Errorf("holder = %+v, %v; want this viewer", h, ok)
	}
}

func TestSelectTargetApprovalPendingIs202(t *testing.T) {
	r := windowsRig(t)
	r.f.Leases.ApprovalRequired = true
	_, h := r.viewer()
	res := r.post("/viewer/target", form("101", "10", "910"), h...)
	status(t, res, http.StatusAccepted)
	contains(t, res, `data-lease-state="pending_approval"`, "ホストの承認を待っています。")
}

func TestSelectTargetRejections(t *testing.T) {
	tests := []struct {
		name   string
		form   url.Values
		status int
		code   string
	}{
		{"昇格は選べない", form("102", "11", "911"), http.StatusConflict, "input_blocked"},
		{"最小化は選べない", form("103", "12", "912"), http.StatusConflict, "input_blocked"},
		{"一覧にない", form("999", "1", "1"), http.StatusNotFound, "window_not_found"},
		{"PID が違う（置き換わった）", form("101", "77", "910"), http.StatusNotFound, "window_not_found"},
		{"開始時刻が違う", form("101", "10", "1"), http.StatusNotFound, "window_not_found"},
		{"値の欠け", url.Values{"window_id": {"101"}}, http.StatusUnprocessableEntity, "invalid_argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := windowsRig(t)
			_, h := r.viewer()
			res := r.post("/viewer/target", tt.form, h...)
			if tt.code == "invalid_argument" {
				contains(t, res, `data-error-code="invalid_argument"`)
			} else {
				failed(t, res, tt.status, tt.code)
			}
			if len(r.f.Leases.Scoped) != 0 {
				t.Errorf("a rejected selection must not bind a lease: %+v", r.f.Leases.Scoped)
			}
		})
	}
}

func TestSelectTargetConflictsWithHolders(t *testing.T) {
	t.Run("他者が保持中は 409 lease_held_by_other", func(t *testing.T) {
		r := windowsRig(t)
		r.f.Leases.Hold(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", Label: "agent-x", SessionID: "s1"})
		_, h := r.viewer()
		failed(t, r.post("/viewer/target", form("101", "10", "910"), h...), http.StatusConflict, "lease_held_by_other")
	})
	t.Run("自分が保持中は 409 conflict（解放が先）", func(t *testing.T) {
		r := windowsRig(t)
		_, h := r.viewer()
		status(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusOK)
		failed(t, r.post("/viewer/target", form("101", "10", "910"), h...), http.StatusConflict, "conflict")
	})
}

func TestSelectTargetNeedsAViewerSession(t *testing.T) {
	r := windowsRig(t)
	status(t, r.post("/viewer/target", form("101", "10", "910")), http.StatusBadRequest)
	status(t, r.get("/viewer/windows"), http.StatusBadRequest)
}

func TestReleaseTargetReleasesTheLeaseAndShowsThePickerAgain(t *testing.T) {
	r := windowsRig(t)
	_, h := r.viewer()
	status(t, r.post("/viewer/target", form("101", "10", "910"), h...), http.StatusOK)
	res := r.post("/viewer/target/release", nil, h...)
	status(t, res, http.StatusOK)
	contains(t, res, `data-lease-state="idle"`)
	if _, ok := r.f.Leases.Holding(); ok {
		t.Error("the lease must be released")
	}
	contains(t, r.get("/viewer/windows", h...), "data-window-list", "title-101")
}

func TestViewerPageEmbedsThePickerAndTheReselectTrigger(t *testing.T) {
	r := windowsRig(t)
	res := r.get("/viewer")
	contains(t, res, `id="window-picker"`, "title-101", `hx-post="/viewer/target/release"`, "remo:reselect from:#viewer-root")
}
