package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

var auditEventName = regexp.MustCompile(`^[a-z]+\.[a-z_]+(\.[a-z_]+)?$`)

// emittedAuditEvents は service パッケージの（テスト以外の）ソースから、監査エントリのイベント名として使う文字列を集める:
// Event: "…"・x.Event = "…"・audit / recordAudit / bearerAudit の引数・名前が AuditEvent / AuditKey で終わる定数。
func emittedAuditEvents(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	add := func(e ast.Expr) {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil && auditEventName.MatchString(s) {
				found[s] = true
			}
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && id.Name == "Event" {
					add(x.Value)
				}
			case *ast.AssignStmt:
				for i, l := range x.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Event" && i < len(x.Rhs) {
						add(x.Rhs[i])
					}
				}
			case *ast.CallExpr:
				name := ""
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name == "audit" || name == "recordAudit" || name == "bearerAudit" {
					for _, a := range x.Args {
						add(a)
					}
				}
			case *ast.ValueSpec:
				for i, id := range x.Names {
					if (strings.HasSuffix(id.Name, "AuditEvent") || strings.HasSuffix(id.Name, "AuditKey")) && i < len(x.Values) {
						add(x.Values[i])
					}
				}
			}
			return true
		})
	}
	return found
}

// ソースが追記するイベント名と AuditCatalog が一致する。新しいイベントを足したら、カタログ（と er.md の表）も足す。
func TestAuditCatalogMatchesEmittedEvents(t *testing.T) {
	catalog := auditCatalogEvents()
	emitted := emittedAuditEvents(t)
	var missing, stale []string
	for ev := range emitted {
		// tool.* は族として扱う（ツール名は別のテストで検査する）。
		if _, ok := catalog[ev]; !ok && !strings.HasPrefix(ev, AuditToolEventPrefix) {
			missing = append(missing, ev)
		}
	}
	for ev := range catalog {
		if !emitted[ev] {
			stale = append(stale, ev)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("events emitted but not in AuditCatalog (add them, and the table in docs/architecture/er.md): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("events in AuditCatalog that no source emits: %v", stale)
	}
}

// FR-AU01 の各項目（agent 以外）には、イベントがあるか、実装する Issue が書いてある。
func TestAuditCatalogCoversEveryItem(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range AuditCatalog {
		if r.Item == "" || seen[r.Item] {
			t.Errorf("empty or duplicated item %q", r.Item)
		}
		seen[r.Item] = true
		if (len(r.Events) == 0) == (r.Pending == "") {
			t.Errorf("item %q must have events or a pending issue (not both, not neither): %+v", r.Item, r)
		}
		for _, ev := range r.Events {
			if !auditEventName.MatchString(ev) && ev != AuditToolEventPrefix+"*" {
				t.Errorf("item %q: %q is not a valid event name", r.Item, ev)
			}
		}
	}
	for _, want := range []string{"接続要求の作成", "接続要求の許可", "接続要求の拒否", "認証失敗", "トークン発行・失効", "キルスイッチ発動・解除", "設定変更"} {
		if !seen[want] {
			t.Errorf("FR-AU01 item %q is missing from AuditCatalog", want)
		}
	}
}

// Control が監査するどの操作も、tool.<名前>（Web は kind = input）のイベントになる（"unknown" にならない）。
func TestAuditCatalogToolEventsForEveryOp(t *testing.T) {
	ops := []domain.Op{
		domain.MouseMove{}, domain.Click{Button: domain.ButtonLeft, Count: 1}, domain.Click{Button: domain.ButtonLeft, Count: 2},
		domain.Click{Button: domain.ButtonLeft, Count: 3}, domain.Click{Button: domain.ButtonRight, Count: 1},
		domain.Click{Button: domain.ButtonMiddle, Count: 1}, domain.Drag{}, domain.Scroll{}, domain.KeyPress{}, domain.TypeText{},
		domain.FocusWindow{}, domain.ClipboardRead{}, domain.ClipboardWrite{}, domain.GetUITree{}, domain.Wait{},
		domain.MouseButton{Action: "down"}, domain.MouseButton{Action: "up"}, domain.Wheel{},
		domain.KeyEvent{Action: "down"}, domain.KeyEvent{Action: "up"},
	}
	for _, op := range ops {
		tool, _ := describeOp(op, Result{})
		if tool == "unknown" || tool == "" {
			t.Errorf("%T has no audit tool name", op)
			continue
		}
		for who, kind := range map[domain.Actor]string{ctlAlice: "tool", ctlViewer: "input"} {
			e := ToolAuditEntry(who, tool, nil, "", nil, time.Millisecond)
			if e.Event != AuditToolEventPrefix+tool || e.Kind != kind {
				t.Errorf("%T by %s: entry = {kind %s, event %s}", op, who.Kind, e.Kind, e.Event)
			}
		}
	}
}
