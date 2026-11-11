package gui_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

// seedDecided は処理済みの要求（許可・拒否）を入れる。
func seedDecided(f *guitest.Fakes, id, name, status string, firstSeen time.Time) {
	decided := firstSeen.Add(5 * time.Minute)
	f.Admin.Requests = append(f.Admin.Requests, service.ConnRequest{
		ID: id, StableNodeID: "node-" + id, LoginName: "carol@example.com", DisplayName: name, RemoteIP: "100.64.0.20",
		Status: status, RequestCount: 1, FirstSeenAt: firstSeen, LastSeenAt: firstSeen, ExpiresAt: firstSeen.Add(24 * time.Hour),
		DecidedAt: &decided,
	})
}

func TestRequestsHistoryPageShowsAllResults(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedDecided(f, "req-ok", "allowed-pc", service.ConnApproved, f.Clock.T.Add(-3*time.Hour))
		seedDecided(f, "req-ng", "denied-pc", service.ConnDenied, f.Clock.T.Add(-2*time.Hour))
		seedRequest(f, "req-wait", "waiting-pc")                         // 未処理（1 時間前）
		f.Admin.Requests = append(f.Admin.Requests, service.ConnRequest{ // 期限切れで Expire 前の pending
			ID: "req-old", StableNodeID: "node-old", LoginName: "d@example.com", DisplayName: "stale-pc", RemoteIP: "100.64.0.30",
			Status: service.ConnPending, FirstSeenAt: f.Clock.T.Add(-30 * time.Hour), ExpiresAt: f.Clock.T.Add(-time.Hour),
		})
	})
	res := r.get("/requests/history")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "<title>接続要求の履歴 - puppet-remo</title>",
		"届いた日時", "デバイス名", "tailnet IP", "ユーザー", "結果", "処理した日時",
		`data-history-request="req-ok" data-result="approved"`, "allowed-pc", "carol@example.com", "100.64.0.20",
		`data-history-request="req-ng" data-result="denied"`, "denied-pc",
		`data-history-request="req-wait" data-result="pending"`, "waiting-pc", "bob@example.com",
		`data-history-request="req-old" data-result="expired"`, "stale-pc",
		">許可<", ">拒否<", ">未処理<", ">失効<",
		// タブ: 履歴が現在、未処理への導線。
		`href="/requests"`, `aria-current="page"`, "監査ログ")
	// 履歴は読み取り専用。許可・拒否の操作は出さない。
	lacks(t, res, "/approve", "/deny", `name="rev_requests"`, "data-pager")
	// 新しい順: 未処理（1 時間前）→ 拒否（2 時間前）→ 許可（3 時間前）→ 期限切れ（30 時間前）。
	order := []string{"req-wait", "req-ng", "req-ok", "req-old"}
	last := -1
	for _, id := range order {
		i := strings.Index(res.Body, `data-history-request="`+id+`"`)
		if i < last {
			t.Fatalf("history is not newest first: %s appears before the previous one\n%s", id, res.Body)
		}
		last = i
	}
	// 処理した日時: 許可・拒否にだけ入る（FixedClock の 5 分後）。
	row := rowOf(t, res, "req-ok")
	if strings.Contains(row, "-</td>") {
		t.Errorf("approved row must have a decided time: %s", row)
	}
	if !strings.Contains(rowOf(t, res, "req-wait"), "-</td>") {
		t.Errorf("pending row must show '-' as the decided time: %s", rowOf(t, res, "req-wait"))
	}
}

func rowOf(t *testing.T, r resp, id string) string {
	t.Helper()
	start := strings.Index(r.Body, `data-history-request="`+id+`"`)
	if start < 0 {
		t.Fatalf("row %s missing\n%s", id, r.Body)
	}
	end := strings.Index(r.Body[start:], "</tr>")
	return r.Body[start : start+end]
}

func TestRequestsHistoryEmptyState(t *testing.T) {
	r := newRig(t)
	res := r.get("/requests/history")
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="request-history"`, "接続要求の履歴はありません")
	lacks(t, res, "<tbody", "data-pager")
}

func TestRequestsHistoryPaging(t *testing.T) {
	size := service.RequestHistoryPageSize
	total := size*2 + 3
	r := newRig(t, func(f *guitest.Fakes) {
		for i := range total {
			seedDecided(f, fmt.Sprintf("req-%03d", i), fmt.Sprintf("pc-%03d", i), service.ConnApproved, f.Clock.T.Add(-time.Duration(total-i)*time.Minute))
		}
	})
	page1 := r.get("/requests/history")
	status(t, page1, http.StatusOK)
	if n := strings.Count(page1.Body, "data-history-request="); n != size {
		t.Errorf("page 1 rows = %d, want %d", n, size)
	}
	contains(t, page1, fmt.Sprintf(`data-history-request="req-%03d"`, total-1), `href="/requests/history?page=2"`, "data-pager-next", "1 ページ目")
	contains(t, page1, "disabled data-pager-prev")

	page3 := r.get("/requests/history?page=3")
	status(t, page3, http.StatusOK)
	if n := strings.Count(page3.Body, "data-history-request="); n != 3 {
		t.Errorf("page 3 rows = %d, want 3", n)
	}
	contains(t, page3, `data-history-request="req-000"`, `href="/requests/history?page=2"`, "data-pager-prev", "3 ページ目")
	contains(t, page3, "disabled data-pager-next")

	// 不正・範囲外のページ番号。
	for _, q := range []string{"?page=0", "?page=-2", "?page=abc", "?page="} {
		res := r.get("/requests/history" + q)
		status(t, res, http.StatusOK)
		contains(t, res, `data-history-request="req-`+fmt.Sprintf("%03d", total-1)+`"`, "1 ページ目")
	}
	beyond := r.get("/requests/history?page=99")
	status(t, beyond, http.StatusOK)
	contains(t, beyond, `data-empty="request-history"`, "data-pager-prev")
}

func TestRequestsHistoryFragmentVariants(t *testing.T) {
	size := service.RequestHistoryPageSize
	r := newRig(t, func(f *guitest.Fakes) {
		for i := range size + 1 {
			seedDecided(f, fmt.Sprintf("req-%03d", i), fmt.Sprintf("pc-%03d", i), service.ConnDenied, f.Clock.T.Add(-time.Duration(size+1-i)*time.Minute))
		}
	})
	frag := r.get("/requests/history?page=2", hx...)
	status(t, frag, http.StatusOK)
	contains(t, frag, `id="request-history"`, `data-history-request="req-000"`, `hx-get="/requests/history?page=1"`,
		`hx-target="#request-history"`, `hx-push-url="true"`)
	lacks(t, frag, "<html", "<body", "<h1")

	boosted := r.get("/requests/history", "HX-Request", "true", "HX-Boosted", "true")
	status(t, boosted, http.StatusOK)
	contains(t, boosted, "<title>接続要求の履歴 - puppet-remo</title>", `id="request-history"`, `id="sidebar-nav"`)
	lacks(t, boosted, "<html")

	// 履歴の復元（戻る）はフルページ。
	restore := r.get("/requests/history", "HX-Request", "true", "HX-History-Restore-Request", "true")
	contains(t, restore, "<html", `id="request-history"`)
}

func TestRequestsPageLinksToHistoryAndFR_A06ListIsUnchanged(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		seedRequest(f, "req-1", "bob-laptop")
		seedDecided(f, "req-old", "allowed-pc", service.ConnApproved, f.Clock.T.Add(-5*time.Hour))
	})
	res := r.get("/requests")
	status(t, res, http.StatusOK)
	contains(t, res, `href="/requests/history"`, "data-requests-tabs", "bob-laptop", `data-pending-count="1"`)
	// 処理済みの要求は「未処理」の一覧には出ない（履歴だけ）。
	lacks(t, res, "allowed-pc")
}

func TestRequestApprovedAppearsInHistory(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedRequest(f, "req-1", "bob-laptop"); seedRequest(f, "req-2", "carol-phone") })
	status(t, r.post("/requests/req-1/approve", url.Values{"mcp_enabled": {"on"}}, hx...), http.StatusOK)
	status(t, r.post("/requests/req-2/deny", nil, hx...), http.StatusOK)
	res := r.get("/requests/history")
	contains(t, res, `data-history-request="req-1" data-result="approved"`, `data-history-request="req-2" data-result="denied"`)
	if row := rowOf(t, res, "req-1"); strings.Contains(row, "-</td>") {
		t.Errorf("approved row must have a decided time: %s", row)
	}
}

func TestRequestsHistoryErrorRendersErrorPage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Admin.HistoryErr = errCleanup })
	res := r.get("/requests/history")
	status(t, res, http.StatusInternalServerError)
	contains(t, res, "<html", `data-error-code="internal"`)
	lacks(t, res, "cleanup boom")
}
