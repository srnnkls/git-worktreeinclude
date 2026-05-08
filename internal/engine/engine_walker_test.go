package engine

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/satococoa/git-worktreeinclude/internal/exitcode"
	"github.com/satococoa/git-worktreeinclude/internal/gitexec"
)

// TestWalker_SubmoduleMidWalk_EmitsDirSymlink — when the walker descends
// through a partial-tracked dir and reaches a submodule path, that submodule
// is anchored as a directory-level symlink at every depth, not refused.
func TestWalker_SubmoduleMidWalk_EmitsDirSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("submodule and symlink semantics vary on Windows")
	}

	fx := setupSubmoduleFixtureUnder(t, "lib", "vendor")

	// Tracked file inside `lib/` to force the walker to recurse rather than
	// anchor at the pattern root.
	if err := os.MkdirAll(filepath.Join(fx.root, "lib"), 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "lib", "tracked.md"), "TRACKED\n")
	runGit(t, fx.root, "add", "lib/tracked.md")
	runGit(t, fx.root, "commit", "-q", "-m", "track lib/tracked.md")
	// Untracked sibling should anchor as a leaf symlink.
	writeFile(t, filepath.Join(fx.root, "lib", "untracked.md"), "UNTRACKED\n")

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "lib  symlink\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	if rollup := findActionOptional(res.Actions, "lib"); rollup.Op != "expand" || rollup.Status != StatusWalked {
		t.Fatalf("expected expand/%s rollup at lib, got %+v (all=%+v)", StatusWalked, rollup, res.Actions)
	}

	if action := findAction(t, res.Actions, "lib/vendor"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for submodule lib/vendor, got %+v", action)
	}
	if action := findAction(t, res.Actions, "lib/untracked.md"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for lib/untracked.md, got %+v", action)
	}
	if a := findActionOptional(res.Actions, "lib/tracked.md"); a.Op != "" {
		t.Fatalf("tracked source leaf must produce no action, got %+v", a)
	}

	subInfo, err := os.Lstat(filepath.Join(fx.wt, "lib", "vendor"))
	if err != nil {
		t.Fatalf("lstat lib/vendor: %v", err)
	}
	if subInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("lib/vendor should be a directory-level symlink, got mode=%v", subInfo.Mode())
	}
}

// TestWalker_SourceSymlinkMidWalk_RecreatesAtDestination — a symlink leaf
// inside a partial-tracked dir is recreated as a symlink regardless of the
// pattern's mode. Reuses rewriteAbsoluteLinkTarget for absolute-inside-source.
func TestWalker_SourceSymlinkMidWalk_RecreatesAtDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	if err := os.MkdirAll(filepath.Join(fx.root, "mix"), 0o755); err != nil {
		t.Fatalf("mkdir mix: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "mix", "tracked.txt"), "TRACKED\n")
	runGit(t, fx.root, "add", "mix/tracked.txt")
	runGit(t, fx.root, "commit", "-q", "-m", "track mix/tracked.txt")

	// A relative source-symlink inside the partial dir.
	if err := os.Symlink("../README.md", filepath.Join(fx.root, "mix", "to-readme")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}

	// Pattern uses `copy` (default) — the source-symlink branch must still fire mid-walk.
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "mix\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	if action := findAction(t, res.Actions, "mix/to-readme"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for mix/to-readme, got %+v (all=%+v)", action, res.Actions)
	}
	got, err := os.Readlink(filepath.Join(fx.wt, "mix", "to-readme"))
	if err != nil {
		t.Fatalf("readlink mix/to-readme: %v", err)
	}
	if got != "../README.md" {
		t.Fatalf("expected verbatim relative target %q, got %q", "../README.md", got)
	}
	if a := findActionOptional(res.Actions, "mix/tracked.txt"); a.Op != "" {
		t.Fatalf("tracked source leaf must produce no action, got %+v", a)
	}
}

// TestWalker_TargetTrackedLeafConflict_DoesNotBlockSiblings — a tracked
// target leaf produces conflict/tracked, but sibling untracked leaves still
// get their symlinks. Exit 3 because of the leaf conflict.
func TestWalker_TargetTrackedLeafConflict_DoesNotBlockSiblings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	// Source: two untracked siblings under partial/.
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\npartial/\n")
	runGit(t, fx.root, "add", ".gitignore")
	runGit(t, fx.root, "commit", "-q", "-m", "ignore partial/")
	if err := os.MkdirAll(filepath.Join(fx.root, "partial"), 0o755); err != nil {
		t.Fatalf("mkdir partial: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "partial", "a.toml"), "AAA\n")
	writeFile(t, filepath.Join(fx.root, "partial", "b.toml"), "BBB\n")

	// Target: one of them is tracked.
	if err := os.MkdirAll(filepath.Join(fx.wt, "partial"), 0o755); err != nil {
		t.Fatalf("mkdir target partial: %v", err)
	}
	writeFile(t, filepath.Join(fx.wt, "partial", "a.toml"), "TARGET_A\n")
	runGit(t, fx.wt, "add", "-f", "partial/a.toml")
	runGit(t, fx.wt, "commit", "-q", "-m", "track partial/a.toml in target")

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "partial  symlink\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("Apply exit code = %d, want %d (target-tracked leaf must conflict)", code, exitcode.Conflict)
	}

	if action := findAction(t, res.Actions, "partial/a.toml"); action.Op != "conflict" || action.Status != StatusTracked {
		t.Fatalf("expected conflict/%s for partial/a.toml, got %+v", StatusTracked, action)
	}
	if action := findAction(t, res.Actions, "partial/b.toml"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for partial/b.toml, got %+v", action)
	}

	// Sibling symlink IS created on disk.
	siblingInfo, err := os.Lstat(filepath.Join(fx.wt, "partial", "b.toml"))
	if err != nil {
		t.Fatalf("lstat partial/b.toml: %v", err)
	}
	if siblingInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("partial/b.toml should be a symlink despite sibling conflict, got mode=%v", siblingInfo.Mode())
	}

	// --force does NOT override target-tracked.
	res, code, err = NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("Apply --force exit code = %d, want %d (--force must NOT override tracked)", code, exitcode.Conflict)
	}
	if action := findAction(t, res.Actions, "partial/a.toml"); action.Op != "conflict" || action.Status != StatusTracked {
		t.Fatalf("expected conflict/%s under --force for partial/a.toml, got %+v", StatusTracked, action)
	}
}

// TestWalker_DeepNestedAnchorDepth — multi-level partial tree exercises
// "anchor at deepest fully-untracked subtree". `.claude/skills/loqui` is
// fully untracked → anchors as one dir-symlink. `.claude/skills/effect` has
// tracked content → walker descends, tracked source leaves silently skipped.
func TestWalker_DeepNestedAnchorDepth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)

	// .claude/skills/effect/rules.md — tracked.
	if err := os.MkdirAll(filepath.Join(fx.root, ".claude", "skills", "effect"), 0o755); err != nil {
		t.Fatalf("mkdir effect: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, ".claude", "skills", "effect", "rules.md"), "EFFECT\n")
	runGit(t, fx.root, "add", ".claude/skills/effect/rules.md")
	runGit(t, fx.root, "commit", "-q", "-m", "track effect rules")

	// .claude/skills/loqui/* — fully untracked.
	if err := os.MkdirAll(filepath.Join(fx.root, ".claude", "skills", "loqui"), 0o755); err != nil {
		t.Fatalf("mkdir loqui: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, ".claude", "skills", "loqui", "go.md"), "LOQUI\n")
	writeFile(t, filepath.Join(fx.root, ".claude", "skills", "loqui", "rust.md"), "LOQUI\n")

	// .claude/agents/* — fully untracked dir.
	if err := os.MkdirAll(filepath.Join(fx.root, ".claude", "agents"), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, ".claude", "agents", "alpha.md"), "ALPHA\n")

	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".claude  symlink\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d (deep nested anchor must succeed)", code, exitcode.OK)
	}

	// Pattern root rolls up because we recurse into .claude.
	if rollup := findActionOptional(res.Actions, ".claude"); rollup.Op != "expand" || rollup.Status != StatusWalked {
		t.Fatalf("expected expand/%s rollup at .claude, got %+v", StatusWalked, rollup)
	}

	// loqui anchors as a single dir-symlink (no per-file actions under it).
	loquiAction := findActionOptional(res.Actions, ".claude/skills/loqui")
	if loquiAction.Op != "symlink" || loquiAction.Status != "done" {
		t.Fatalf("expected .claude/skills/loqui to be a dir-symlink, got %+v (all=%+v)", loquiAction, res.Actions)
	}
	for _, sub := range []string{".claude/skills/loqui/go.md", ".claude/skills/loqui/rust.md"} {
		if a := findActionOptional(res.Actions, sub); a.Op != "" {
			t.Fatalf("loqui anchored as dir-symlink — no per-file action expected for %s, got %+v", sub, a)
		}
	}
	loquiInfo, err := os.Lstat(filepath.Join(fx.wt, ".claude", "skills", "loqui"))
	if err != nil {
		t.Fatalf("lstat .claude/skills/loqui: %v", err)
	}
	if loquiInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".claude/skills/loqui should be one symlink, got mode=%v", loquiInfo.Mode())
	}

	// agents/ same — fully untracked dir anchors.
	agentsAction := findActionOptional(res.Actions, ".claude/agents")
	if agentsAction.Op != "symlink" || agentsAction.Status != "done" {
		t.Fatalf("expected .claude/agents to be a dir-symlink, got %+v", agentsAction)
	}

	// effect/ tracked — silently skipped, no per-file actions.
	if a := findActionOptional(res.Actions, ".claude/skills/effect/rules.md"); a.Op != "" {
		t.Fatalf("tracked effect rules.md must produce no action, got %+v", a)
	}
	if a := findActionOptional(res.Actions, ".claude/skills/effect"); a.Op != "" {
		t.Fatalf("partially-tracked effect dir must not anchor; got %+v", a)
	}
}

// countingRunner wraps a *gitexec.Runner and counts ls-files invocations so
// we can pin the cache invariant: bulk-load ls-files at most once per
// repoRoot per Apply.
type countingRunner struct {
	inner   *gitexec.Runner
	lsFiles atomic.Int64
}

func (c *countingRunner) Run(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "ls-files" {
		c.lsFiles.Add(1)
	}
	return c.inner.Run(ctx, cwd, args...)
}

func (c *countingRunner) RunText(ctx context.Context, cwd string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "ls-files" {
		c.lsFiles.Add(1)
	}
	return c.inner.RunText(ctx, cwd, args...)
}

// TestWalker_TrackedSetCache_OneGitInvocationPerRepo — a sizable partial dir
// must not regress to per-leaf `git ls-files` calls. The bulk-load happens
// at most once per repoRoot during Apply.
func TestWalker_TrackedSetCache_OneGitInvocationPerRepo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("filesystem and git semantics vary on Windows")
	}

	fx := setupEngineFixture(t)

	if err := os.MkdirAll(filepath.Join(fx.root, "bulk"), 0o755); err != nil {
		t.Fatalf("mkdir bulk: %v", err)
	}

	// Tracked anchor file forces walker recursion (so the dir is "partial").
	writeFile(t, filepath.Join(fx.root, "bulk", "tracked.md"), "TRACKED\n")
	runGit(t, fx.root, "add", "bulk/tracked.md")
	runGit(t, fx.root, "commit", "-q", "-m", "track bulk anchor")

	// Many untracked sibling leaves.
	const n = 200
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(fx.root, "bulk", "leaf"+itoa(i)+".md"), "X\n")
	}
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "bulk  symlink\n")

	e := NewEngine()
	counter := &countingRunner{inner: e.git.(*gitexec.Runner)}
	e.git = counter

	_, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	// Two repoRoots (source + target) → at most 2 ls-files invocations from
	// the tracked-set load. Allow a small headroom for unrelated ls-files
	// calls (submodule discovery, etc.) but reject per-leaf scaling.
	if got := counter.lsFiles.Load(); got > 8 {
		t.Fatalf("ls-files invocations = %d; expected bulk-load (<=8 total), regression to per-leaf scaling for %d leaves", got, n)
	}
}

// setupSubmoduleFixtureUnder is a variant of setupSubmoduleFixture that adds
// the submodule at parent/child instead of vendor/lib, so tests can exercise
// a partial-tracked outer dir containing a submodule path.
func setupSubmoduleFixtureUnder(t *testing.T, parent, child string) engineFixture {
	t.Helper()

	base := t.TempDir()

	upstream := filepath.Join(base, "upstream")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatalf("mkdir upstream: %v", err)
	}
	runGitTB(t, upstream, "init", "-q")
	runGitTB(t, upstream, "config", "user.name", "Up Stream")
	runGitTB(t, upstream, "config", "user.email", "up@example.com")
	runGitTB(t, upstream, "branch", "-M", "main")
	writeFile(t, filepath.Join(upstream, "lib.txt"), "LIB\n")
	runGitTB(t, upstream, "add", "lib.txt")
	runGitTB(t, upstream, "commit", "-q", "-m", "init upstream")

	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGitTB(t, repo, "init", "-q")
	runGitTB(t, repo, "config", "user.name", "Test User")
	runGitTB(t, repo, "config", "user.email", "test@example.com")
	runGitTB(t, repo, "branch", "-M", "main")
	runGitTB(t, repo, "config", "protocol.file.allow", "always")
	writeFile(t, filepath.Join(repo, "README.md"), "tracked\n")
	writeFile(t, filepath.Join(repo, ".gitignore"), ".env\n")
	writeFile(t, filepath.Join(repo, testIncludeFile), "# placeholder\n")
	runGitTB(t, repo, "add", "README.md", ".gitignore", testIncludeFile)
	runGitTB(t, repo, "commit", "-q", "-m", "init")

	upstreamURL := (&url.URL{Scheme: "file", Path: upstream}).String()
	rel := parent + "/" + child
	runGitTB(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstreamURL, rel)
	runGitTB(t, repo, "commit", "-q", "-m", "add submodule "+rel)

	wt := filepath.Join(base, "wt")
	runGitTB(t, repo, "worktree", "add", "-q", wt, "-b", "feature")

	pruneEmptyDirs(t, filepath.Join(wt, rel))
	return engineFixture{root: repo, wt: wt}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return strings.Clone(string(buf[pos:]))
}
