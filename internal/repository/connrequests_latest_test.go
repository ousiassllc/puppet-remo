package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

// LatestByNode は状態を問わず、そのデバイスの最新の要求を返す（待機ページの出し分け。FR-W02）。
func TestConnRequestsLatestByNode(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	rs := db.ConnRequests()

	if _, err := rs.LatestByNode(ctx, "node1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LatestByNode with no rows: err = %v, want ErrNotFound", err)
	}

	upsert := func(id, node string) {
		t.Helper()
		r := ConnRequest{
			ID: id, StableNodeID: node, LoginName: "alice@example.com", DisplayName: "dev-" + node,
			RemoteIP: "100.64.0.1", ExpiresAt: clock.Now().Add(24 * time.Hour),
		}
		if _, _, err := rs.UpsertPending(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	upsert("r1", "node1")
	upsert("r2", "node2")
	if err := rs.Decide(ctx, "r1", ConnDenied, nil); err != nil {
		t.Fatal(err)
	}
	got, err := rs.LatestByNode(ctx, "node1")
	if err != nil || got.ID != "r1" || got.Status != ConnDenied {
		t.Errorf("LatestByNode(node1) = %+v, %v; want the denied r1", got, err)
	}

	// 同じデバイスの新しい要求（拒否の失効後に作られた pending）が最新になる。
	clock.Advance(time.Hour)
	upsert("r3", "node1")
	got, err = rs.LatestByNode(ctx, "node1")
	if err != nil || got.ID != "r3" || got.Status != ConnPending {
		t.Errorf("LatestByNode(node1) after a new request = %+v, %v; want the pending r3", got, err)
	}
	// 他のデバイスの行は混ざらない。
	got, err = rs.LatestByNode(ctx, "node2")
	if err != nil || got.ID != "r2" {
		t.Errorf("LatestByNode(node2) = %+v, %v; want r2", got, err)
	}
}
