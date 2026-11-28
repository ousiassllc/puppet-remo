package platformtest

import (
	"context"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

const largeLimit = 1_000_000

// RunUIAutomationContract は platform.UIAutomation の契約を検査する。
// 前面ウィンドウ（windowID が空）に UI Automation で読める要素があることを前提にする。読み取りだけで、実機でも副作用はない。
func RunUIAutomationContract(t *testing.T, newUIA func(t *testing.T) platform.UIAutomation) {
	t.Helper()
	ctx := context.Background()

	t.Run("unknown window", func(t *testing.T) {
		u := newUIA(t)
		_, err := u.Tree(ctx, "no-such-window", 5, 10)
		requireCode(t, err, domain.ErrWindowNotFound, "Tree")
		_, err = u.Snapshot(ctx, "no-such-window", 10)
		requireCode(t, err, domain.ErrWindowNotFound, "Snapshot")
	})

	t.Run("limits below 1", func(t *testing.T) {
		u := newUIA(t)
		for _, c := range []struct{ depth, nodes int }{{0, 10}, {5, 0}, {-1, -1}} {
			_, err := u.Tree(ctx, "", c.depth, c.nodes)
			requireCode(t, err, domain.ErrInvalidArgument, "Tree limits")
		}
		_, err := u.Snapshot(ctx, "", 0)
		requireCode(t, err, domain.ErrInvalidArgument, "Snapshot limit")
	})

	t.Run("Tree honors maxNodes and maxDepth and marks what it cut", func(t *testing.T) {
		u := newUIA(t)
		full, err := u.Tree(ctx, "", largeLimit, largeLimit)
		if err != nil {
			t.Fatalf("Tree: %v", err)
		}
		if full.Truncated {
			t.Error("the full tree is marked Truncated")
		}
		total := countNodes(full)
		if total < 3 {
			t.Fatalf("the foreground window has %d UI nodes; the contract needs at least 3", total)
		}

		limited, err := u.Tree(ctx, "", largeLimit, 2)
		if err != nil {
			t.Fatalf("Tree(maxNodes 2): %v", err)
		}
		if n := countNodes(limited); n > 2 {
			t.Errorf("Tree(maxNodes 2) returned %d nodes", n)
		}
		if !anyTruncated(limited) {
			t.Error("Tree(maxNodes 2) cut nodes but marked none Truncated")
		}

		shallow, err := u.Tree(ctx, "", 1, largeLimit)
		if err != nil {
			t.Fatalf("Tree(maxDepth 1): %v", err)
		}
		if d := depthOf(shallow); d > 1 {
			t.Errorf("Tree(maxDepth 1) has depth %d", d)
		}
		if depthOf(full) > 1 && !anyTruncated(shallow) {
			t.Error("Tree(maxDepth 1) cut nodes but marked none Truncated")
		}
	})

	t.Run("Snapshot is consistent, bounded and carries no node text", func(t *testing.T) {
		u := newUIA(t)
		first, err := u.Snapshot(ctx, "", largeLimit)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if first.Window.ID == "" || first.Hash == "" {
			t.Errorf("Snapshot window ID %q, hash %q; both must be set", first.Window.ID, first.Hash)
		}
		if first.Truncated {
			t.Error("the full snapshot is marked Truncated")
		}
		again, err := u.Snapshot(ctx, first.Window.ID, largeLimit)
		if err != nil {
			t.Fatalf("Snapshot(%q): %v", first.Window.ID, err)
		}
		if again.Hash != first.Hash {
			t.Errorf("hash changed between two snapshots of an unchanged window: %q, %q", first.Hash, again.Hash)
		}

		if len(first.Elements) < 2 {
			t.Fatalf("the foreground window has %d elements; the contract needs at least 2", len(first.Elements))
		}
		limited, err := u.Snapshot(ctx, "", 1)
		if err != nil {
			t.Fatalf("Snapshot(maxNodes 1): %v", err)
		}
		if len(limited.Elements) > 1 || !limited.Truncated {
			t.Errorf("Snapshot(maxNodes 1): %d elements, Truncated %v; want at most 1 and true", len(limited.Elements), limited.Truncated)
		}
	})

	t.Run("Find and Resolve", func(t *testing.T) {
		u := newUIA(t)
		matchAll := func(domain.UIElementInfo) bool { return true }
		_, err := u.Find(ctx, "no-such-window", matchAll, 5, 10)
		requireCode(t, err, domain.ErrWindowNotFound, "Find")
		for _, c := range []struct{ results, nodes int }{{0, 10}, {5, 0}, {-1, -1}} {
			_, err := u.Find(ctx, "", matchAll, c.results, c.nodes)
			requireCode(t, err, domain.ErrInvalidArgument, "Find limits")
		}

		all, err := u.Find(ctx, "", matchAll, largeLimit, largeLimit)
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if all.Window.ID == "" || len(all.Elements) < 3 || all.Truncated {
			t.Fatalf("Find(all) = window %q, %d elements, Truncated %v; want a window, at least 3 elements, not truncated", all.Window.ID, len(all.Elements), all.Truncated)
		}
		one, err := u.Find(ctx, all.Window.ID, matchAll, 1, largeLimit)
		if err != nil {
			t.Fatalf("Find(maxResults 1): %v", err)
		}
		if len(one.Elements) != 1 || !one.Truncated {
			t.Errorf("Find(maxResults 1): %d elements, Truncated %v; want 1 and true", len(one.Elements), one.Truncated)
		}
		capped, err := u.Find(ctx, all.Window.ID, matchAll, largeLimit, 2)
		if err != nil {
			t.Fatalf("Find(maxNodes 2): %v", err)
		}
		if len(capped.Elements) > 2 || !capped.Truncated {
			t.Errorf("Find(maxNodes 2): %d elements, Truncated %v; want at most 2 and true", len(capped.Elements), capped.Truncated)
		}
		none, err := u.Find(ctx, all.Window.ID, func(domain.UIElementInfo) bool { return false }, 5, largeLimit)
		if err != nil || len(none.Elements) != 0 || none.Truncated {
			t.Errorf("Find(match none) = %d elements, Truncated %v, %v; want none", len(none.Elements), none.Truncated, err)
		}

		resolved := 0
		for _, e := range all.Elements {
			if e.ID == "" {
				continue // RuntimeId を取れない要素は引き直せない
			}
			ref, err := domain.ParseElementRef(e.ID)
			if err != nil {
				t.Fatalf("Find returned the id %q, which does not parse: %v", e.ID, err)
			}
			if ref.WindowID() != all.Window.ID || ref.PID != all.Window.PID {
				t.Errorf("id %q is for window %s pid %d; want window %s pid %d", e.ID, ref.WindowID(), ref.PID, all.Window.ID, all.Window.PID)
			}
			got, err := u.Resolve(ctx, ref)
			if err != nil {
				t.Fatalf("Resolve(%s): %v", e.ID, err)
			}
			if got.ID != e.ID {
				t.Errorf("Resolve(%s) returned %q", e.ID, got.ID)
			}
			resolved++
			if resolved == 5 {
				break
			}
		}
		if resolved == 0 {
			t.Error("no element from Find has an id")
		}

		gone := domain.ElementRef{HWND: 1 << 40, PID: 1, RuntimeID: []int32{1, 2}}
		_, err = u.Resolve(ctx, gone)
		requireCode(t, err, domain.ErrStaleElement, "Resolve(unknown window)")
	})

	t.Run("canceled context", func(t *testing.T) {
		u := newUIA(t)
		_, err := u.Tree(canceledCtx(), "", 5, 10)
		requireCanceled(t, err, "Tree")
		_, err = u.Snapshot(canceledCtx(), "", 10)
		requireCanceled(t, err, "Snapshot")
		_, err = u.Find(canceledCtx(), "", func(domain.UIElementInfo) bool { return true }, 5, 10)
		requireCanceled(t, err, "Find")
		_, err = u.Resolve(canceledCtx(), domain.ElementRef{HWND: 1, PID: 1, RuntimeID: []int32{1}})
		requireCanceled(t, err, "Resolve")
	})
}

func countNodes(n domain.UINode) int {
	total := 1
	for _, c := range n.Children {
		total += countNodes(c)
	}
	return total
}

// depthOf は根を 0 とした最大の深さ。
func depthOf(n domain.UINode) int {
	deepest := 0
	for _, c := range n.Children {
		deepest = max(deepest, depthOf(c)+1)
	}
	return deepest
}

func anyTruncated(n domain.UINode) bool {
	if n.Truncated {
		return true
	}
	for _, c := range n.Children {
		if anyTruncated(c) {
			return true
		}
	}
	return false
}
