package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

var auditBase = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// appendAudit は ts（auditBase からの経過）を指定して 1 件追記し、id を返す。
func appendAudit(t *testing.T, a Audit, at time.Duration, mod func(*AuditEntry)) int64 {
	t.Helper()
	e := AuditEntry{
		TS: auditBase.Add(at), Kind: "tool", Event: "tool.left_click", ActorType: domain.ActorMCP,
		ClientLabel: "dev", Result: "ok",
	}
	if mod != nil {
		mod(&e)
	}
	id, err := a.Append(context.Background(), e)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return id
}

func ids(es []AuditEntry) []int64 {
	out := make([]int64, 0, len(es))
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func mustQuery(t *testing.T, a Audit, q AuditQuery) ([]AuditEntry, int64) {
	t.Helper()
	es, next, err := a.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("Query(%+v): %v", q, err)
	}
	return es, next
}

func TestAuditQueryOrderAndFilters(t *testing.T) {
	db, _ := openTestDB(t)
	a := db.Audit()
	mustExec(t, db, `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES ('c1', 'n1', 'l', 'dev1', 1, 1), ('c2', 'n2', 'l', 'dev2', 1, 1)`)

	oldest := appendAudit(t, a, 0, func(e *AuditEntry) { e.Kind = "auth"; e.Event = "auth.fail"; e.Result = "denied" })
	c1 := appendAudit(t, a, time.Minute, func(e *AuditEntry) { e.ClientID = "c1" })
	c2 := appendAudit(t, a, 2*time.Minute, func(e *AuditEntry) { e.ClientID = "c2"; e.Kind = "lease"; e.Event = "lease.acquire" })
	failed := appendAudit(t, a, 3*time.Minute, func(e *AuditEntry) { e.ClientID = "c1"; e.Result = "error"; e.Event = "tool.type" })

	tests := []struct {
		name string
		q    AuditQuery
		want []int64
	}{
		{"no filter: newest first", AuditQuery{}, []int64{failed, c2, c1, oldest}},
		{"from is inclusive", AuditQuery{From: auditBase.Add(time.Minute)}, []int64{failed, c2, c1}},
		{"to is inclusive", AuditQuery{To: auditBase.Add(time.Minute)}, []int64{c1, oldest}},
		{"from and to", AuditQuery{From: auditBase.Add(time.Minute), To: auditBase.Add(2 * time.Minute)}, []int64{c2, c1}},
		{"client", AuditQuery{ClientID: "c1"}, []int64{failed, c1}},
		{"kind", AuditQuery{Kind: "lease"}, []int64{c2}},
		{"result", AuditQuery{Result: "error"}, []int64{failed}},
		{"event substring", AuditQuery{Q: "lick"}, []int64{c1}},
		{"client and result", AuditQuery{ClientID: "c1", Result: "ok"}, []int64{c1}},
		{"kind and result and range", AuditQuery{Kind: "auth", Result: "denied", To: auditBase}, []int64{oldest}},
		{"all filters, no match", AuditQuery{ClientID: "c2", Kind: "tool", Result: "ok", Q: "tool"}, []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es, next := mustQuery(t, a, tt.q)
			if got := ids(es); !slices.Equal(got, tt.want) {
				t.Errorf("ids = %v, want %v", got, tt.want)
			}
			if next != 0 {
				t.Errorf("next = %d, want 0", next)
			}
		})
	}
}

func TestAuditQueryCursorPagesStableOverEqualTimestamps(t *testing.T) {
	db, _ := openTestDB(t)
	a := db.Audit()
	// 同じ ts の 4 行（id 降順に並ぶ）と、それより新しい 1 行、古い 1 行。
	var want []int64
	older := appendAudit(t, a, 0, nil)
	same := make([]int64, 4)
	for i := range same {
		same[i] = appendAudit(t, a, time.Minute, nil)
	}
	newest := appendAudit(t, a, 2*time.Minute, nil)
	want = append(want, newest)
	for i := len(same) - 1; i >= 0; i-- {
		want = append(want, same[i])
	}
	want = append(want, older)

	var got []int64
	var cursor int64
	for pages := 0; ; pages++ {
		if pages > len(want) {
			t.Fatal("paging does not terminate")
		}
		es, next := mustQuery(t, a, AuditQuery{Limit: 2, Cursor: cursor})
		got = append(got, ids(es)...)
		if next == 0 {
			break
		}
		if last := es[len(es)-1].ID; next != last {
			t.Fatalf("next = %d, want the last id of the page (%d)", next, last)
		}
		cursor = next
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged ids = %v, want %v", got, want)
	}

	// ちょうど limit 件で終わるときは next = 0。
	es, next := mustQuery(t, a, AuditQuery{Limit: len(want)})
	if len(es) != len(want) || next != 0 {
		t.Errorf("full page: len = %d, next = %d; want %d, 0", len(es), next, len(want))
	}
}

func TestAuditQueryCursorEdgeCases(t *testing.T) {
	db, _ := openTestDB(t)
	a := db.Audit()
	id := appendAudit(t, a, 0, nil)
	appendAudit(t, a, time.Minute, nil)

	if _, _, err := a.Query(context.Background(), AuditQuery{Cursor: -1}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("negative cursor: err = %v, want ErrInvalidArgument", err)
	}
	// 保持期間の削除でカーソルの行が消えていたら、続きは無い。
	es, next := mustQuery(t, a, AuditQuery{Cursor: id + 100})
	if len(es) != 0 || next != 0 {
		t.Errorf("unknown cursor: %d entries, next %d; want none", len(es), next)
	}
}

func TestAuditQueryLimitDefaultsAndCap(t *testing.T) {
	db, _ := openTestDB(t)
	a := db.Audit()
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxAuditLimit + 10 {
		if _, err := tx.Exec(`INSERT INTO audit_logs (ts, kind, event, actor_type, client_label, result)
			VALUES (?, 'tool', 'tool.x', 'mcp', 'd', 'ok')`, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		limit, want int
		more        bool
	}{
		{0, DefaultAuditLimit, true},
		{-5, DefaultAuditLimit, true},
		{10, 10, true},
		{MaxAuditLimit, MaxAuditLimit, true},
		{MaxAuditLimit + 1, MaxAuditLimit, true},
		{100000, MaxAuditLimit, true},
	} {
		es, next := mustQuery(t, a, AuditQuery{Limit: tt.limit})
		if len(es) != tt.want || (next != 0) != tt.more {
			t.Errorf("limit %d: len = %d, next = %d; want len %d, more = %v", tt.limit, len(es), next, tt.want, tt.more)
		}
	}
}

func TestAuditQueryEscapesLikeWildcards(t *testing.T) {
	db, _ := openTestDB(t)
	a := db.Audit()
	events := []string{"tool.a_b", "tool.axb", "tool.100%", "tool.100x", `tool.a\b`, "tool.plain"}
	byEvent := map[string]int64{}
	for i, ev := range events {
		byEvent[ev] = appendAudit(t, a, time.Duration(i)*time.Second, func(e *AuditEntry) { e.Event = ev })
	}
	for _, tt := range []struct {
		q    string
		want []string
	}{
		{"a_b", []string{"tool.a_b"}},
		{"100%", []string{"tool.100%"}},
		{`a\b`, []string{`tool.a\b`}},
		{"%", []string{"tool.100%"}},
		{"_", []string{"tool.a_b"}},
		{"PLAIN", []string{"tool.plain"}}, // ASCII は大文字小文字を区別しない（SQLite の LIKE）
		{"tool.", events},
	} {
		es, _ := mustQuery(t, a, AuditQuery{Q: tt.q})
		got := map[int64]bool{}
		for _, e := range es {
			got[e.ID] = true
		}
		if len(got) != len(tt.want) {
			t.Errorf("q=%q: matched %d events, want %d", tt.q, len(got), len(tt.want))
		}
		for _, ev := range tt.want {
			if !got[byEvent[ev]] {
				t.Errorf("q=%q: event %q not matched", tt.q, ev)
			}
		}
	}
}

func TestAuditQueryDeletedClientHasEmptyClientID(t *testing.T) {
	db, _ := openTestDB(t)
	a := db.Audit()
	mustExec(t, db, `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES ('c1', 'n1', 'l', 'dev1', 1, 1)`)
	id := appendAudit(t, a, 0, func(e *AuditEntry) { e.ClientID = "c1"; e.ClientLabel = "dev1" })
	mustExec(t, db, `DELETE FROM clients WHERE id = 'c1'`)

	es, _ := mustQuery(t, a, AuditQuery{})
	if len(es) != 1 || es[0].ID != id || es[0].ClientID != "" || es[0].ClientLabel != "dev1" {
		t.Errorf("entries = %+v, want the row with empty ClientID and label dev1", es)
	}
	if es, _ := mustQuery(t, a, AuditQuery{ClientID: "c1"}); len(es) != 0 {
		t.Errorf("filtering by the deleted client matched %d rows", len(es))
	}
}

func TestAuditQueryAndGetReturnThumbAndDetail(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	a := db.Audit()
	step, dur := 3, int64(42)
	withThumb := appendAudit(t, a, 0, func(e *AuditEntry) {
		e.SessionID, e.LeaseID, e.TaskID = "s1", "l1", ""
		e.StepNo, e.DurationMS, e.Tool, e.ErrorCode = &step, &dur, "left_click", "x"
		e.DetailJSON = json.RawMessage(`{"x":1,"y":2}`)
	})
	plain := appendAudit(t, a, time.Minute, nil)
	meta := ThumbMeta{RelPath: "2026/10/30/a.jpg", SizeBytes: 1234, Width: 320, Height: 180, CreatedAt: clock.Now()}
	if err := a.AttachThumb(ctx, withThumb, meta); err != nil {
		t.Fatalf("AttachThumb: %v", err)
	}

	got, err := a.Get(ctx, withThumb)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != withThumb || got.Kind != "tool" || got.Event != "tool.left_click" || got.ActorType != domain.ActorMCP ||
		got.SessionID != "s1" || got.LeaseID != "l1" || got.Tool != "left_click" || got.ErrorCode != "x" || got.Result != "ok" ||
		got.StepNo == nil || *got.StepNo != 3 || got.DurationMS == nil || *got.DurationMS != 42 ||
		!got.TS.Equal(auditBase) || string(got.DetailJSON) != `{"x":1,"y":2}` {
		t.Errorf("Get = %+v", got)
	}
	if got.Thumb == nil || *got.Thumb != meta {
		t.Errorf("Get thumb = %+v, want %+v", got.Thumb, meta)
	}
	if g, err := a.Get(ctx, plain); err != nil || g.Thumb != nil || g.DetailJSON != nil {
		t.Errorf("Get(plain) = %+v, %v; want no thumb and no detail", g, err)
	}
	if _, err := a.Get(ctx, plain+100); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) err = %v, want ErrNotFound", err)
	}

	es, _ := mustQuery(t, a, AuditQuery{})
	if len(es) != 2 || es[1].Thumb == nil || *es[1].Thumb != meta || es[0].Thumb != nil {
		t.Errorf("Query thumbs = %+v", es)
	}
	if es[1].DetailJSON != nil {
		t.Errorf("Query must not read detail_json, got %s", es[1].DetailJSON)
	}
}

func TestAuditQueryUsesIndexes(t *testing.T) {
	db, _ := openTestDB(t)
	for _, tt := range []struct {
		name, where, index string
	}{
		{"ts", "", "ix_audit_ts"},
		{"client", "l.client_id = 'c'", "ix_audit_client_ts"},
		{"kind", "l.kind = 'tool'", "ix_audit_kind_ts"},
		{"result", "l.result = 'ok'", "ix_audit_result_ts"},
	} {
		query := `EXPLAIN QUERY PLAN SELECT l.id` + auditFrom
		if tt.where != "" {
			query += ` WHERE ` + tt.where
		}
		query += ` ORDER BY l.ts DESC, l.id DESC LIMIT 51`
		rows, err := db.sql.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, tt.index) {
			t.Errorf("%s: plan does not use %s:\n%s", tt.name, tt.index, joined)
		}
		if strings.Contains(joined, "SCAN l\n") || strings.HasSuffix(joined, "SCAN l") {
			t.Errorf("%s: plan scans the whole table:\n%s", tt.name, joined)
		}
	}
}

func TestAttachThumb(t *testing.T) {
	ctx := context.Background()
	db, clock := openTestDB(t)
	a := db.Audit()
	id := appendAudit(t, a, 0, nil)
	meta := ThumbMeta{RelPath: "2026/10/30/a.jpg", SizeBytes: 10, Width: 320, Height: 180}

	if err := a.AttachThumb(ctx, id, meta); err != nil {
		t.Fatalf("AttachThumb: %v", err)
	}
	got, _ := a.Get(ctx, id)
	if got.Thumb == nil || !got.Thumb.CreatedAt.Equal(clock.Now()) {
		t.Errorf("zero CreatedAt must become the clock's now: %+v", got.Thumb)
	}
	if err := a.AttachThumb(ctx, id, ThumbMeta{RelPath: "b.jpg", SizeBytes: 1, Width: 1, Height: 1}); !errors.Is(err, ErrConflict) {
		t.Errorf("second thumbnail: err = %v, want ErrConflict", err)
	}
	if err := a.AttachThumb(ctx, id+100, meta); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown audit log: err = %v, want ErrNotFound", err)
	}
	other := appendAudit(t, a, time.Second, nil)
	for _, bad := range []ThumbMeta{
		{RelPath: "../x.jpg", SizeBytes: 1, Width: 1, Height: 1},
		{RelPath: "/abs/x.jpg", SizeBytes: 1, Width: 1, Height: 1},
		{RelPath: `C:\x.jpg`, SizeBytes: 1, Width: 1, Height: 1},
		{RelPath: "", SizeBytes: 1, Width: 1, Height: 1},
		{RelPath: "x.jpg", SizeBytes: -1, Width: 1, Height: 1},
		{RelPath: "x.jpg", SizeBytes: 1, Width: 0, Height: 1},
		{RelPath: "x.jpg", SizeBytes: 1, Width: 1, Height: 0},
	} {
		if err := a.AttachThumb(ctx, other, bad); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("AttachThumb(%+v): err = %v, want ErrInvalidArgument", bad, err)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_thumbnails`); n != 1 {
		t.Errorf("thumbnail rows = %d, want 1", n)
	}
}

func attachThumb(t *testing.T, db *DB, auditID int64, rel string, size int64, created time.Time) {
	t.Helper()
	err := db.Audit().AttachThumb(context.Background(), auditID,
		ThumbMeta{RelPath: rel, SizeBytes: size, Width: 320, Height: 180, CreatedAt: created})
	if err != nil {
		t.Fatalf("AttachThumb(%s): %v", rel, err)
	}
}

func TestPurgeBeforeDeletesRowsAndReturnsThumbPaths(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	a := db.Audit()
	old1 := appendAudit(t, a, 0, nil)
	old2 := appendAudit(t, a, time.Minute, nil)
	boundary := appendAudit(t, a, 2*time.Minute, nil)
	keep := appendAudit(t, a, 3*time.Minute, nil)
	attachThumb(t, db, old1, "old1.jpg", 10, auditBase)
	attachThumb(t, db, boundary, "boundary.jpg", 10, auditBase)
	attachThumb(t, db, keep, "keep.jpg", 10, auditBase)

	removed, err := a.PurgeBefore(ctx, auditBase.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("PurgeBefore: %v", err)
	}
	if !slices.Equal(removed, []string{"old1.jpg"}) {
		t.Errorf("removed = %v, want [old1.jpg]", removed)
	}
	es, _ := mustQuery(t, a, AuditQuery{})
	if got := ids(es); !slices.Equal(got, []int64{keep, boundary}) { // ts < t だけ削除する（境界は残る）
		t.Errorf("remaining = %v, want %v (old rows %d, %d deleted)", got, []int64{keep, boundary}, old1, old2)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_thumbnails`); n != 2 {
		t.Errorf("thumbnail rows = %d, want 2 (CASCADE removed one)", n)
	}
	// 何も無ければ空・エラー無し。
	if removed, err := a.PurgeBefore(ctx, auditBase); err != nil || len(removed) != 0 {
		t.Errorf("PurgeBefore(nothing) = %v, %v", removed, err)
	}
}

func thumbRows(t *testing.T, db *DB) []string {
	t.Helper()
	paths, err := db.Audit().ThumbPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	return paths
}

func TestPurgeThumbsOverRemovesOldestAndKeepsLogRows(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	a := db.Audit()
	var logIDs []int64
	// created_at の順は ts の順と逆にして、created_at の古い順に消すことを確かめる。
	for i, spec := range []struct {
		rel     string
		created time.Duration
	}{{"c.jpg", 3 * time.Hour}, {"a.jpg", 1 * time.Hour}, {"b.jpg", 2 * time.Hour}, {"d.jpg", 4 * time.Hour}} {
		id := appendAudit(t, a, time.Duration(i)*time.Second, nil)
		logIDs = append(logIDs, id)
		attachThumb(t, db, id, spec.rel, 100, auditBase.Add(spec.created))
	}

	// 合計 400 バイト。ちょうど上限なら何も消さない。
	if removed, err := a.PurgeThumbsOver(ctx, 400); err != nil || len(removed) != 0 {
		t.Fatalf("at the cap: removed = %v, err = %v; want nothing", removed, err)
	}
	// 399 → 1 枚（最古の a）を消せば 300 で上限以下。
	removed, err := a.PurgeThumbsOver(ctx, 399)
	if err != nil || !slices.Equal(removed, []string{"a.jpg"}) {
		t.Fatalf("cap 399: removed = %v, err = %v; want [a.jpg]", removed, err)
	}
	// 150 → 残り 300 から b（200）、c（100）の順に消して、100 ≤ 150 で止まる。
	removed, err = a.PurgeThumbsOver(ctx, 150)
	if err != nil || !slices.Equal(removed, []string{"b.jpg", "c.jpg"}) {
		t.Fatalf("cap 150: removed = %v, err = %v; want [b.jpg c.jpg]", removed, err)
	}
	if got := thumbRows(t, db); !slices.Equal(got, []string{"d.jpg"}) {
		t.Errorf("remaining thumbnails = %v, want [d.jpg]", got)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs`); n != len(logIDs) {
		t.Errorf("audit_logs rows = %d, want all %d kept", n, len(logIDs))
	}
	got, err := a.Get(ctx, logIDs[0])
	if err != nil || got.Thumb != nil {
		t.Errorf("log row after its thumbnail was purged: %+v, %v; want the row without a thumbnail", got, err)
	}
}

func TestPurgeThumbsOverRejectsNonPositiveCap(t *testing.T) {
	db, _ := openTestDB(t)
	id := appendAudit(t, db.Audit(), 0, nil)
	attachThumb(t, db, id, "a.jpg", 10, auditBase)
	for _, maxBytes := range []int64{0, -1} {
		removed, err := db.Audit().PurgeThumbsOver(context.Background(), maxBytes)
		if !errors.Is(err, domain.ErrInvalidArgument) || len(removed) != 0 {
			t.Errorf("PurgeThumbsOver(%d) = %v, %v; want ErrInvalidArgument", maxBytes, removed, err)
		}
	}
	if got := thumbRows(t, db); len(got) != 1 {
		t.Errorf("thumbnails = %v, want unchanged", got)
	}
}

func TestPurgeMissingThumbsDeletesRowsWithoutFiles(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	a := db.Audit()
	var firstID int64
	for i, rel := range []string{"present.jpg", "gone1.jpg", "gone2.jpg"} {
		id := appendAudit(t, a, time.Duration(i)*time.Second, nil)
		if i == 1 {
			firstID = id
		}
		attachThumb(t, db, id, rel, 10, auditBase)
	}
	n, err := a.PurgeMissingThumbs(ctx, func(rel string) bool { return rel == "present.jpg" })
	if err != nil || n != 2 {
		t.Fatalf("PurgeMissingThumbs = %d, %v; want 2", n, err)
	}
	if got := thumbRows(t, db); !slices.Equal(got, []string{"present.jpg"}) {
		t.Errorf("thumbnail rows = %v", got)
	}
	if g, err := a.Get(ctx, firstID); err != nil || g.Thumb != nil {
		t.Errorf("the log row must remain without a thumbnail: %+v, %v", g, err)
	}
	if n, err := a.PurgeMissingThumbs(ctx, func(string) bool { return true }); err != nil || n != 0 {
		t.Errorf("nothing missing: %d, %v", n, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs`); n != 3 {
		t.Errorf("audit_logs rows = %d, want 3", n)
	}
}

func TestAuditThumbnailCascadesOnForeignKeys(t *testing.T) {
	// foreign_keys=ON が接続に効いていること（PurgeBefore の CASCADE の前提）。
	db, _ := openTestDB(t)
	id := appendAudit(t, db.Audit(), 0, nil)
	attachThumb(t, db, id, "a.jpg", 1, auditBase)
	mustExec(t, db, fmt.Sprintf(`DELETE FROM audit_logs WHERE id = %d`, id))
	if n := count(t, db, `SELECT COUNT(*) FROM audit_thumbnails`); n != 0 {
		t.Errorf("thumbnail rows = %d, want 0", n)
	}
}
