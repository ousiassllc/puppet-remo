package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

func ident(node string) Identity {
	return Identity{LoginName: testUser, StableNodeID: node, NodeName: "host-" + node}
}

func TestRecordConnectionRequestAggregates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := e.authz.RecordConnectionRequest(ctx, ident(testNodeA), "100.64.0.5"); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
	}
	if err := e.authz.RecordConnectionRequest(ctx, ident(testNodeB), "100.64.0.6"); err != nil {
		t.Fatal(err)
	}
	rows := e.reqs.snapshot()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (1 デバイス 1 件)", len(rows))
	}
	a := rows[0]
	if a.StableNodeID != testNodeA || a.RequestCount != 3 || a.Status != repository.ConnPending || a.RemoteIP != "100.64.0.5" {
		t.Fatalf("row A = %+v", a)
	}
	// 保留は最初の要求から 24 時間で失効する（集約しても延びない）。
	if want := e.clock.Now().Add(-3 * time.Minute).Add(24 * time.Hour); !a.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", a.ExpiresAt, want)
	}
	if a.DisplayName != "host-"+testNodeA || a.LoginName != testUser {
		t.Fatalf("row A = %+v", a)
	}
}

func TestRecordConnectionRequestSuppressedAfterDenial(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.authz.RecordConnectionRequest(ctx, ident(testNodeA), "100.64.0.5"); err != nil {
		t.Fatal(err)
	}
	rows := e.reqs.snapshot()
	if err := e.reqs.Decide(ctx, rows[0].ID, repository.ConnDenied, nil); err != nil {
		t.Fatal(err)
	}

	for _, wait := range []time.Duration{time.Minute, 23 * time.Hour} {
		e.clock.Advance(wait)
		if err := e.authz.RecordConnectionRequest(ctx, ident(testNodeA), "100.64.0.5"); err != nil {
			t.Fatal(err)
		}
		if n := len(e.reqs.snapshot()); n != 1 {
			t.Fatalf("a new request was created %v after denial (rows=%d)", wait, n)
		}
	}
	// 他デバイスは抑止されない。
	if err := e.authz.RecordConnectionRequest(ctx, ident(testNodeB), "100.64.0.6"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.reqs.snapshot()); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	// 24 時間を過ぎれば新しい要求を作れる。
	e.clock.Advance(time.Hour)
	if err := e.authz.RecordConnectionRequest(ctx, ident(testNodeA), "100.64.0.5"); err != nil {
		t.Fatal(err)
	}
	pending, _ := e.reqs.ListPending(ctx)
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2 (A と B)", len(pending))
	}
}

func TestRecordConnectionRequestRateLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	node := func(i int) string { return "node-" + string(rune('a'+i)) }

	for i := 0; i < connRequestLimit+5; i++ {
		if err := e.authz.RecordConnectionRequest(ctx, ident(node(i)), "100.64.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.reqs.snapshot()); n != connRequestLimit {
		t.Fatalf("rows = %d, want %d（超過分は GUI に出さない）", n, connRequestLimit)
	}
	// 超過は窓ごとに 1 回だけ記録する。
	if got := e.audit.events(); len(got) != 1 || got[0] != "auth.connreq_rate_limited" {
		t.Fatalf("audit = %v", got)
	}
	var detail map[string]any
	if err := json.Unmarshal(e.audit.entries[0].DetailJSON, &detail); err != nil || detail["stable_node_id"] != node(connRequestLimit) {
		t.Fatalf("detail = %v (%v)", detail, err)
	}

	// 既存の pending への集約は数えず、上限後も更新される。
	if err := e.authz.RecordConnectionRequest(ctx, ident(node(0)), "100.64.9.9"); err != nil {
		t.Fatal(err)
	}
	if first := e.reqs.snapshot()[0]; first.RequestCount != 2 || first.RemoteIP != "100.64.9.9" {
		t.Fatalf("row 0 = %+v", first)
	}

	// 窓が終われば再び作れる。
	e.clock.Advance(time.Minute)
	if err := e.authz.RecordConnectionRequest(ctx, ident(node(connRequestLimit)), "100.64.1.1"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.reqs.snapshot()); n != connRequestLimit+1 {
		t.Fatalf("rows = %d, want %d", n, connRequestLimit+1)
	}
}

func TestRecordConnectionRequestRepositoryError(t *testing.T) {
	e := newEnv(t)
	e.reqs.err = errors.New("db down")
	if err := e.authz.RecordConnectionRequest(context.Background(), ident(testNodeA), "100.64.0.5"); err == nil {
		t.Fatal("expected an error")
	}
}
