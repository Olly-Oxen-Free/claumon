package parser

import (
	"sync"
	"time"
)

// fileCache memoizes per-file parse results keyed by absolute path. An entry
// is valid while the file's mtime and size are unchanged (both are checked so
// a truncate+rewrite at the same instant still invalidates) and the pricing
// table version matches (parsed results embed USD costs, which must be
// recomputed when pricing changes).
//
// This is what keeps the watcher hot path cheap: a session write triggers a
// rescan of every session file, but only the file that actually changed is
// re-parsed - everything else is served from here.
type fileCache[T any] struct {
	mu      sync.RWMutex
	entries map[string]fileCacheEntry[T]
}

type fileCacheEntry[T any] struct {
	modTime        time.Time
	size           int64
	pricingVersion uint64
	val            T
}

func newFileCache[T any]() *fileCache[T] {
	return &fileCache[T]{entries: map[string]fileCacheEntry[T]{}}
}

// get returns the cached parse result for f, invoking parse only when the
// file changed since the entry was stored. Cached values are shared across
// callers; treat them as read-only (copy before mutating).
func (c *fileCache[T]) get(f sessionFile, parse func(path string) (T, error)) (T, error) {
	pv := pricingVersion()

	c.mu.RLock()
	e, ok := c.entries[f.path]
	c.mu.RUnlock()
	if ok && e.size == f.size && e.modTime.Equal(f.modTime) && e.pricingVersion == pv {
		return e.val, nil
	}

	val, err := parse(f.path)
	if err != nil {
		var zero T
		return zero, err
	}

	c.mu.Lock()
	c.entries[f.path] = fileCacheEntry[T]{modTime: f.modTime, size: f.size, pricingVersion: pv, val: val}
	c.mu.Unlock()
	return val, nil
}

// prune drops entries for files no longer present, so deleted sessions don't
// pin memory indefinitely. Callers pass the current enumeration result.
func (c *fileCache[T]) prune(files []sessionFile) {
	keep := make(map[string]bool, len(files))
	for _, f := range files {
		keep[f.path] = true
	}
	c.mu.Lock()
	for path := range c.entries {
		if !keep[path] {
			delete(c.entries, path)
		}
	}
	c.mu.Unlock()
}

// pricingVersion returns the current pricing table version, or 0 if no table
// has been set (tests, zero-cost fallback).
func pricingVersion() uint64 {
	if pricingTable == nil {
		return 0
	}
	return pricingTable.Version()
}
