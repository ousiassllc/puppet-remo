package service

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func historyIDs(p RequestHistoryPage) []string {
	var out []string
	for _, r := range p.Items {
		out = append(out, r.ID)
	}
	return out
}

// 許可・拒否した要求は処理後も履歴に残る（結果と処理日時つき）。同じデバイスの再要求は 1 件の pending に集約されたまま
// （FR-A06）で、履歴にも 1 行だけ出る。
func TestRequestHistoryKeepsDecidedRequestsAndFR_A06AggregationIsUnchanged(t *testing.T) {
	e := newAdminEnv(t)
	idA := Identity{StableNodeID: "nodeA", LoginName: "a@example.com", NodeName: "dev-a"}
	idB := Identity{StableNodeID: "nodeB", LoginName: "b@example.com", NodeName: "dev-b"}
	record := func(id Identity) {
		t.Helper()
		if err := e.authz.RecordConnectionRequest(e.ctx, id, "100.64.0.9"); err != nil {
			t.Fatal(err)
		}
	}

	// nodeA が 3 回要求しても pending は 1 件・履歴は 1 行。
	for range 3 {
		record(idA)
		e.clock.Advance(connRequestTouchInterval + time.Second)
	}
	pending, err := e.admin.PendingRequests(e.ctx)
	if err != nil || len(pending) != 1 || pending[0].RequestCount != 3 {
		t.Fatalf("pending = %+v, %v; want 1 request aggregated 3 times", pending, err)
	}
	if h, err := e.admin.RequestHistory(e.ctx, 1); err != nil || len(h.Items) != 1 || h.Items[0].Status != ConnPending {
		t.Fatalf("history of the aggregated request = %+v, %v", h, err)
	}

	// nodeB を拒否、nodeA を許可。どちらも履歴に残り、pending の一覧からは消える。
	record(idB)
	aID := pending[0].ID
	pendingB, _ := e.admin.PendingRequests(e.ctx)
	var bID string
	for _, r := range pendingB {
		if r.StableNodeID == "nodeB" {
			bID = r.ID
		}
	}
	e.clock.Advance(time.Minute)
	if err := e.admin.DenyRequest(e.ctx, bID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.ApproveRequest(e.ctx, aID, ApproveInput{WebEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := e.admin.PendingRequests(e.ctx); len(pending) != 0 {
		t.Fatalf("pending after decisions = %+v", pending)
	}
	h, err := e.admin.RequestHistory(e.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{bID, aID}; !slices.Equal(historyIDs(h), want) {
		t.Fatalf("history = %v, want %v (newest first)", historyIDs(h), want)
	}
	if r := h.Items[0]; r.Status != ConnDenied || r.DecidedAt == nil || !r.DecidedAt.Equal(e.clock.Now()) {
		t.Errorf("denied entry = %+v", r)
	}
	if r := h.Items[1]; r.Status != ConnApproved || r.DecidedAt == nil || r.ClientID == nil {
		t.Errorf("approved entry = %+v", r)
	}

	// 拒否から 24 時間は、同じデバイスの再要求は新しい行を作らない（履歴は増えない）。
	record(idB)
	if h, _ := e.admin.RequestHistory(e.ctx, 1); len(h.Items) != 2 {
		t.Errorf("history grew during the denial window: %v", historyIDs(h))
	}
	// 許可済みのデバイスがログイン名を変えて再認証されると、新しい要求が別の行になる。古い許可は履歴に残る。
	record(Identity{StableNodeID: "nodeA", LoginName: "other@example.com", NodeName: "dev-a"})
	h, _ = e.admin.RequestHistory(e.ctx, 1)
	if len(h.Items) != 3 || h.Items[0].Status != ConnPending || h.Items[0].StableNodeID != "nodeA" || h.Items[2].ID != aID {
		t.Errorf("history after re-request = %+v", h.Items)
	}
	if pending, _ := e.admin.PendingRequests(e.ctx); len(pending) != 1 {
		t.Errorf("pending after re-request = %+v, want exactly 1", pending)
	}
}

func TestRequestHistoryPaging(t *testing.T) {
	e := newAdminEnv(t)
	total := RequestHistoryPageSize + 5
	for i := range total {
		_, _, err := e.db.ConnRequests().UpsertPending(e.ctx, repository.ConnRequest{
			ID: fmt.Sprintf("req-%03d", i), StableNodeID: fmt.Sprintf("node%03d", i), LoginName: "l", DisplayName: "d", RemoteIP: "ip",
			FirstSeenAt: e.clock.Now().Add(time.Duration(i) * time.Second), ExpiresAt: e.clock.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	p1, err := e.admin.RequestHistory(e.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != RequestHistoryPageSize || p1.Page != 1 || p1.HasPrev || !p1.HasNext {
		t.Fatalf("page 1: n=%d page=%d prev=%v next=%v", len(p1.Items), p1.Page, p1.HasPrev, p1.HasNext)
	}
	if p1.Items[0].ID != fmt.Sprintf("req-%03d", total-1) {
		t.Errorf("page 1 starts with %s, want the newest", p1.Items[0].ID)
	}
	p2, err := e.admin.RequestHistory(e.ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 5 || p2.Page != 2 || !p2.HasPrev || p2.HasNext || p2.Items[4].ID != "req-000" {
		t.Errorf("page 2: n=%d page=%d prev=%v next=%v", len(p2.Items), p2.Page, p2.HasPrev, p2.HasNext)
	}
	// 範囲外・不正なページ番号は、1 ページ目か空のページ（エラーにしない）。
	if p, err := e.admin.RequestHistory(e.ctx, 0); err != nil || p.Page != 1 || len(p.Items) != RequestHistoryPageSize {
		t.Errorf("page 0 = %+v, %v", p, err)
	}
	if p, err := e.admin.RequestHistory(e.ctx, -7); err != nil || p.Page != 1 {
		t.Errorf("page -7 = %+v, %v", p, err)
	}
	if p, err := e.admin.RequestHistory(e.ctx, 3); err != nil || len(p.Items) != 0 || p.HasNext || !p.HasPrev {
		t.Errorf("page 3 = %+v, %v", p, err)
	}
	if p, err := e.admin.RequestHistory(e.ctx, int(^uint(0)>>1)); err != nil || len(p.Items) != 0 || p.Page != maxRequestHistoryPage {
		t.Errorf("huge page = %+v, %v", p, err)
	}
}
