package engine

import (
	"context"
	"strings"
	"sync"
)

// trackedSet holds the index entry list for one repoRoot, parsed once and
// queried O(1) thereafter. Bulk-load replaces per-call `git ls-files` shells
// previously made for every leaf during walk.
type trackedSet struct {
	paths map[string]struct{}
	// dirs holds every directory prefix of every tracked path. A subtree at
	// `rel` is tracked iff `rel` is in paths or in dirs.
	dirs map[string]struct{}
}

// trackedSetCache loads tracked-set tables lazily, keyed by repoRoot. One
// instance lives for the duration of a single Apply.
type trackedSetCache struct {
	git   gitRunner
	mu    sync.Mutex
	cache map[string]*trackedSet
}

func newTrackedSetCache(git gitRunner) *trackedSetCache {
	return &trackedSetCache{
		git:   git,
		cache: make(map[string]*trackedSet),
	}
}

func (c *trackedSetCache) get(ctx context.Context, repoRoot string) *trackedSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ts, ok := c.cache[repoRoot]; ok {
		return ts
	}

	ts := &trackedSet{paths: map[string]struct{}{}, dirs: map[string]struct{}{}}
	out, err := c.git.Run(ctx, repoRoot, "ls-files", "-z")
	if err != nil {
		// A failure here means the bulk-load is unavailable for this repo;
		// caching the empty set keeps callers consistent and avoids reentrant
		// fallbacks. Errors here are rare (corrupt repo, etc.).
		c.cache[repoRoot] = ts
		return ts
	}
	for _, raw := range splitNUL(out) {
		if raw == "" {
			continue
		}
		norm, err := normalizeRepoPath(raw)
		if err != nil {
			continue
		}
		ts.paths[norm] = struct{}{}
		// Record every parent directory for prefix lookups.
		for d := parentDir(norm); d != ""; d = parentDir(d) {
			ts.dirs[d] = struct{}{}
		}
	}
	c.cache[repoRoot] = ts
	return ts
}

// hasPath reports whether `rel` is itself in the index.
func (ts *trackedSet) hasPath(rel string) bool {
	_, ok := ts.paths[rel]
	return ok
}

// hasPathOrSubtree mirrors the legacy isPathTracked||isSubtreeTracked guard:
// `rel` is tracked, or `rel` is a directory containing tracked content.
func (ts *trackedSet) hasPathOrSubtree(rel string) bool {
	if _, ok := ts.paths[rel]; ok {
		return true
	}
	_, ok := ts.dirs[rel]
	return ok
}

func splitNUL(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	parts := strings.Split(string(b), "\x00")
	return parts
}

func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return ""
	}
	return p[:i]
}
