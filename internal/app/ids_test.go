package app

import (
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestULIDGenIsUniqueAndSortedWithinTheSameMillisecond(t *testing.T) {
	fixed := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	g := newULIDGen(rand.NewChaCha8([32]byte{1}), func() time.Time { return fixed })

	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = g.NewID()
		if len(ids[i]) != 26 {
			t.Fatalf("id %q has length %d, want 26", ids[i], len(ids[i]))
		}
	}
	if !slices.IsSorted(ids) {
		t.Error("IDs generated in the same millisecond are not monotonically increasing")
	}
	if len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Error("duplicate IDs")
	}
}

func TestULIDGenOrdersByTime(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	g := newULIDGen(rand.NewChaCha8([32]byte{1}), func() time.Time { return now })
	first := g.NewID()
	now = now.Add(time.Second)
	if second := g.NewID(); second <= first {
		t.Errorf("%q should sort after %q", second, first)
	}
}

func TestULIDGenIsSafeForConcurrentUse(t *testing.T) {
	g := NewULIDGen()
	const workers, each = 8, 200
	out := make(chan string, workers*each)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				out <- g.NewID()
			}
		}()
	}
	wg.Wait()
	close(out)
	seen := map[string]bool{}
	for id := range out {
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
	}
}
