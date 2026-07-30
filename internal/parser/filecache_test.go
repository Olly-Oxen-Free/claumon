package parser

import (
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/pricing"
)

func TestFileCacheHitAndInvalidation(t *testing.T) {
	c := newFileCache[int]()
	mt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	f := sessionFile{path: "/a.jsonl", modTime: mt, size: 100}

	calls := 0
	parse := func(string) (int, error) {
		calls++
		return calls, nil
	}

	// First call parses, second is served from cache.
	if v, _ := c.get(f, parse); v != 1 {
		t.Fatalf("first get = %d, want 1", v)
	}
	if v, _ := c.get(f, parse); v != 1 {
		t.Fatalf("cached get = %d, want 1", v)
	}
	if calls != 1 {
		t.Fatalf("parse called %d times, want 1", calls)
	}

	// Size change invalidates.
	f.size = 200
	if v, _ := c.get(f, parse); v != 2 {
		t.Fatalf("get after size change = %d, want 2", v)
	}

	// mtime change invalidates.
	f.modTime = mt.Add(time.Second)
	if v, _ := c.get(f, parse); v != 3 {
		t.Fatalf("get after mtime change = %d, want 3", v)
	}

	if calls != 3 {
		t.Fatalf("parse called %d times, want 3", calls)
	}
}

func TestFileCachePrune(t *testing.T) {
	c := newFileCache[int]()
	mt := time.Now()
	kept := sessionFile{path: "/kept.jsonl", modTime: mt, size: 1}
	gone := sessionFile{path: "/gone.jsonl", modTime: mt, size: 1}

	calls := 0
	parse := func(string) (int, error) {
		calls++
		return calls, nil
	}
	c.get(kept, parse)
	c.get(gone, parse)

	c.prune([]sessionFile{kept})

	// kept is still cached, gone must be re-parsed.
	c.get(kept, parse)
	c.get(gone, parse)
	if calls != 3 {
		t.Fatalf("parse called %d times, want 3 (gone re-parsed after prune)", calls)
	}
}

func TestFileCachePricingVersionInvalidates(t *testing.T) {
	// Cached entries embed USD costs, so a pricing change must invalidate
	// them. Table versions are globally unique, so swapping in a fresh table
	// (same prices, new version) is enough to exercise the check.
	c := newFileCache[int]()
	f := sessionFile{path: "/a.jsonl", modTime: time.Now(), size: 1}

	calls := 0
	parse := func(string) (int, error) {
		calls++
		return calls, nil
	}
	c.get(f, parse)
	c.get(f, parse)
	if calls != 1 {
		t.Fatalf("parse called %d times before pricing change, want 1", calls)
	}

	SetPricingTable(pricing.Load(nil))
	c.get(f, parse)
	if calls != 2 {
		t.Fatalf("parse called %d times after pricing change, want 2", calls)
	}
}
