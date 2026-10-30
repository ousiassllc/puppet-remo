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

	t.Run("canceled context", func(t *testing.T) {
		u := newUIA(t)
		_, err := u.Tree(canceledCtx(), "", 5, 10)
		requireCanceled(t, err, "Tree")
		_, err = u.Snapshot(canceledCtx(), "", 10)
		requireCanceled(t, err, "Snapshot")
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
