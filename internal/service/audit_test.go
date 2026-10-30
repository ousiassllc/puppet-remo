package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// ctlAuditStore は repository.Audit の偽物。block を設定すると Append がそのチャネルを待つ。
type ctlAuditStore struct {
	mu      sync.Mutex
	entries []repository.AuditEntry
	err     error
	block   chan struct{}
	entered chan struct{} // block 中の最初の Append で閉じられる
	once    sync.Once
}

func (s *ctlAuditStore) Append(_ context.Context, e repository.AuditEntry) (int64, error) {
	if s.block != nil {
		s.once.Do(func() { close(s.entered) })
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	s.entries = append(s.entries, e)
	return int64(len(s.entries)), nil
}

func (s *ctlAuditStore) Entries() []repository.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]repository.AuditEntry(nil), s.entries...)
}

func newTestAudit(t *testing.T, store repository.Audit, size int, logs *bytes.Buffer) *Audit {
	t.Helper()
	cfg := AuditConfig{Store: store, Clock: newCtlClock(), QueueSize: size}
	if logs != nil {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	a, err := NewAudit(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	return a
}

func TestAuditWritesInOrderAndFillsTimestamp(t *testing.T) {
	store := &ctlAuditStore{}
	a := newTestAudit(t, store, 0, nil)
	ctx := context.Background()
	ts := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	a.Record(ctx, repository.AuditEntry{Kind: "auth", Event: "auth.bearer_failed", Result: "denied", TS: ts})
	a.Record(ctx, repository.AuditEntry{Kind: "tool", Event: "tool.type", Result: "ok"})
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := store.Entries()
	if len(got) != 2 || got[0].Event != "auth.bearer_failed" || got[1].Event != "tool.type" {
		t.Fatalf("entries = %+v", got)
	}
	if !got[0].TS.Equal(ts) {
		t.Fatalf("explicit TS overwritten: %s", got[0].TS)
	}
	if want := newCtlClock().Now(); !got[1].TS.Equal(want) {
		t.Fatalf("zero TS = %s, want clock %s", got[1].TS, want)
	}
}

func TestAuditDoesNotBlockWhenQueueIsFullAndCountsDrops(t *testing.T) {
	store := &ctlAuditStore{block: make(chan struct{}), entered: make(chan struct{})}
	var logs bytes.Buffer
	a := newTestAudit(t, store, 1, &logs)
	ctx := context.Background()

	a.Record(ctx, repository.AuditEntry{Event: "e1", Result: "ok"})
	<-store.entered                                                 // goroutine は e1 の書き込みで止まっている
	a.Record(ctx, repository.AuditEntry{Event: "e2", Result: "ok"}) // キューに入る（容量 1）
	done := make(chan struct{})
	go func() {
		a.Record(ctx, repository.AuditEntry{Event: "e3", Result: "ok"}) // 満杯 → 捨てる（待たない）
		a.Record(ctx, repository.AuditEntry{Event: "e4", Result: "ok"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a full queue")
	}
	if a.Dropped() != 2 {
		t.Fatalf("Dropped = %d, want 2", a.Dropped())
	}
	if !strings.Contains(logs.String(), "audit entry dropped") {
		t.Fatalf("no drop warning in logs: %s", logs.String())
	}
	close(store.block)
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := store.Entries(); len(got) != 2 || got[0].Event != "e1" || got[1].Event != "e2" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestAuditStoreFailureDoesNotStopTheWriter(t *testing.T) {
	store := &ctlAuditStore{err: errors.New("db locked")}
	var logs bytes.Buffer
	a := newTestAudit(t, store, 0, &logs)
	ctx := context.Background()
	a.Record(ctx, repository.AuditEntry{Event: "lost", Result: "ok"})
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.err = nil
	store.mu.Unlock()
	a.Record(ctx, repository.AuditEntry{Event: "kept", Result: "ok"})
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := store.Entries(); len(got) != 1 || got[0].Event != "kept" {
		t.Fatalf("entries = %+v", got)
	}
	if !strings.Contains(logs.String(), "audit append failed") {
		t.Fatalf("append failure not logged: %s", logs.String())
	}
}

func TestAuditCloseDrainsQueueAndRefusesLaterRecords(t *testing.T) {
	store := &ctlAuditStore{}
	a := newTestAudit(t, store, 0, nil)
	ctx := context.Background()
	for _, ev := range []string{"a", "b", "c"} {
		a.Record(ctx, repository.AuditEntry{Event: ev, Result: "ok"})
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := store.Entries(); len(got) != 3 {
		t.Fatalf("entries after close = %+v", got)
	}
	a.Record(ctx, repository.AuditEntry{Event: "late", Result: "ok"})
	if a.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", a.Dropped())
	}
	if err := a.Flush(ctx); !errors.Is(err, ErrAuditClosed) {
		t.Fatalf("Flush after close = %v", err)
	}
	if err := a.Close(ctx); err != nil { // 2 回目の Close は無害
		t.Fatal(err)
	}
}

func TestAuditConcurrentRecord(t *testing.T) {
	store := &ctlAuditStore{}
	a := newTestAudit(t, store, 1024, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				a.Record(context.Background(), repository.AuditEntry{Event: "x", Result: "ok"})
			}
		}()
	}
	wg.Wait()
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(store.Entries()) + int(a.Dropped()); n != 400 {
		t.Fatalf("written+dropped = %d, want 400", n)
	}
}

func TestAuditWritesToRepository(t *testing.T) {
	ctx := context.Background()
	clock := newCtlClock()
	db, err := repository.Open(ctx, t.TempDir()+"/puppet-remo.db", repository.WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var logs bytes.Buffer
	a, err := NewAudit(AuditConfig{Store: db.Audit(), Clock: clock, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(ctx) }()

	// clients 表への外部キーがあるため、未登録のクライアント ID は付けない（登録は S7 の領分）。
	anon := domain.Actor{Kind: domain.ActorMCP, Label: "anon", SessionID: "s"}
	a.Record(ctx, ToolAuditEntry(anon, "type", map[string]any{"text_len": 5}, "lease-1", nil, 12*time.Millisecond))
	a.Record(ctx, ToolAuditEntry(anon, "left_click", nil, "", domain.ErrInputBlocked, time.Millisecond))
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Dropped() != 0 || strings.Contains(logs.String(), "append failed") {
		t.Fatalf("dropped = %d, logs = %s", a.Dropped(), logs.String())
	}
}

func TestToolAuditEntryResultMapping(t *testing.T) {
	web := domain.Actor{Kind: domain.ActorWeb, SessionID: "v"}
	tests := []struct {
		name       string
		who        domain.Actor
		err        error
		wantKind   string
		wantResult string
		wantCode   string
	}{
		{name: "成功", who: ctlAlice, wantKind: "tool", wantResult: "ok"},
		{name: "InputGuard の拒否は denied", who: ctlAlice, err: domain.ErrInputBlocked, wantKind: "tool", wantResult: "denied", wantCode: "input_blocked"},
		{name: "キルスイッチは denied", who: ctlAlice, err: domain.ErrKillSwitchActive, wantKind: "tool", wantResult: "denied", wantCode: "kill_switch_active"},
		{name: "リースは denied", who: ctlAlice, err: domain.ErrLeaseHeldByOther, wantKind: "tool", wantResult: "denied", wantCode: "lease_held_by_other"},
		{name: "引数不正は error", who: ctlAlice, err: domain.ErrInvalidArgument, wantKind: "tool", wantResult: "error", wantCode: "invalid_argument"},
		{name: "素のエラーは internal", who: ctlAlice, err: errors.New("x"), wantKind: "tool", wantResult: "error", wantCode: "internal"},
		{name: "web は kind=input", who: web, wantKind: "input", wantResult: "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := ToolAuditEntry(tt.who, "key", nil, "lease-9", tt.err, 1500*time.Microsecond)
			if e.Kind != tt.wantKind || e.Result != tt.wantResult || e.ErrorCode != tt.wantCode || e.Event != "tool.key" ||
				e.Tool != "key" || e.LeaseID != "lease-9" || e.DurationMS == nil || *e.DurationMS != 1 {
				t.Fatalf("entry = %+v", e)
			}
		})
	}
}

func TestNewAuditRequiresDependencies(t *testing.T) {
	if _, err := NewAudit(AuditConfig{}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("got %v", err)
	}
}

// Audit は S7 が使う AuditSink（Record(ctx, entry)）を満たす。
var _ AuditRecorder = (*Audit)(nil)
