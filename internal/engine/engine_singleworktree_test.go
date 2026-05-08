package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/satococoa/git-worktreeinclude/internal/exitcode"
)

// setupSingleWorktreeRepo builds a repo with exactly one (initial) worktree
// and a populated `.test.worktreeinclude` referencing ignored files. The
// returned path is the only worktree, which is also the target of `Apply`.
//
// This mirrors the in-test fixture from TestEngineApplyAutoRefusesTargetWorktree
// but exposes it as a helper so all single-worktree cases share one shape.
func setupSingleWorktreeRepo(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "branch", "-M", "main")

	writeFile(t, filepath.Join(repo, "README.md"), "tracked\n")
	writeFile(t, filepath.Join(repo, ".gitignore"), ".env\n.env.local\n")
	writeFile(t, filepath.Join(repo, testIncludeFile), ".env\n.env.local\n")
	runGit(t, repo, "add", "README.md", ".gitignore", testIncludeFile)
	runGit(t, repo, "commit", "-q", "-m", "init")

	// The presence of these untracked-but-ignored files would cause copies
	// in the multi-worktree scenario; the single-worktree no-op must not
	// touch them.
	writeFile(t, filepath.Join(repo, ".env"), "SOURCE_ENV\n")
	writeFile(t, filepath.Join(repo, ".env.local"), "SOURCE_LOCAL\n")

	return repo
}

// TestSingleWorktree_AutoIsNoopSuccess pins the new behavior: in a clone with
// only one worktree, `--from auto` (the default) must short-circuit to a
// successful no-op, not error out with exitcode.Env.
//
// Today's engine errors here ("no other non-bare worktree found for --from
// auto"). After the fix, downstream hooks (e.g. hk post-checkout) can call
// `git worktreeinclude apply` unconditionally without wrapping it in a shell
// guard like `[ $(git worktree list | wc -l) -gt 1 ]`.
func TestSingleWorktree_AutoIsNoopSuccess(t *testing.T) {
	repo := setupSingleWorktreeRepo(t)

	res, code, err := NewEngine().Apply(context.Background(), repo, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: unexpected error in single-worktree no-op: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d (single-worktree --from auto must be a no-op success)", code, exitcode.OK)
	}

	if got := res.Summary.Matched; got != 0 {
		t.Errorf("Summary.Matched = %d, want 0", got)
	}
	if got := res.Summary.Copied; got != 0 {
		t.Errorf("Summary.Copied = %d, want 0", got)
	}
	if got := res.Summary.Symlinked; got != 0 {
		t.Errorf("Summary.Symlinked = %d, want 0", got)
	}
	if got := res.Summary.Conflicts; got != 0 {
		t.Errorf("Summary.Conflicts = %d, want 0", got)
	}
	if got := res.Summary.Errors; got != 0 {
		t.Errorf("Summary.Errors = %d, want 0", got)
	}
	if n := len(res.Actions); n != 0 {
		t.Errorf("len(Actions) = %d, want 0 (no-op produces no actions)", n)
	}

	// The hint must be surfaced via the same Result field used for other
	// no-op short-circuits (IncludeMissingHint), parallel to
	// IncludeMissingHintSourceMissing. Naming the value "single_worktree"
	// matches the existing snake_case style.
	if res.IncludeMissingHint != IncludeMissingHintSingleWorktree {
		t.Errorf("IncludeMissingHint = %q, want %q (single-worktree no-op must surface a hint)", res.IncludeMissingHint, IncludeMissingHintSingleWorktree)
	}

	// The source files must NOT have been touched in the (only) worktree as
	// a side-effect of the short-circuit. They were created in setup; the
	// short-circuit must happen before any expansion.
	if _, err := os.Stat(filepath.Join(repo, ".env")); err != nil {
		t.Fatalf("source .env should remain present, stat err: %v", err)
	}
}

// TestSingleWorktree_ExplicitFromMissingPathStillErrors regression-pins that
// the new no-op short-circuit only triggers for `--from auto`. An explicit
// `--from <nonexistent>` must continue to error with exitcode.Env.
func TestSingleWorktree_ExplicitFromMissingPathStillErrors(t *testing.T) {
	repo := setupSingleWorktreeRepo(t)

	bogus := filepath.Join(t.TempDir(), "does", "not", "exist")

	_, code, err := NewEngine().Apply(context.Background(), repo, ApplyOptions{
		From:    bogus,
		Include: testIncludeFile,
	})
	if err == nil {
		t.Fatalf("expected error for explicit --from pointing at non-existent path, got nil (code=%d)", code)
	}
	if code != exitcode.Env {
		t.Fatalf("exit code = %d, want %d (explicit bogus --from must keep erroring)", code, exitcode.Env)
	}
}

// TestSingleWorktree_TwoWorktreesAutoStillWorks regression-pins that adding a
// second worktree restores normal `--from auto` behavior — i.e. the no-op
// short-circuit only fires when exactly one worktree exists.
func TestSingleWorktree_TwoWorktreesAutoStillWorks(t *testing.T) {
	fx := setupEngineFixture(t)

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
	if res.Summary.Copied == 0 {
		t.Fatalf("two-worktree happy path must still copy, got %+v", res.Summary)
	}
	if res.IncludeMissingHint == IncludeMissingHintSingleWorktree {
		t.Fatalf("IncludeMissingHint = %q must NOT fire in multi-worktree mode", res.IncludeMissingHint)
	}
}

// TestSingleWorktree_ExplicitFromOtherWorktreeStillWorks regression-pins that
// when two worktrees exist and an explicit `--from <other-worktree>` is
// passed, behavior is unchanged. (This guards against an over-broad
// short-circuit that ignores the From option.)
func TestSingleWorktree_ExplicitFromOtherWorktreeStillWorks(t *testing.T) {
	fx := setupEngineFixture(t)

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    fx.root,
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Copied == 0 {
		t.Fatalf("explicit --from other-worktree must still copy, got %+v", res.Summary)
	}
	if res.IncludeMissingHint == IncludeMissingHintSingleWorktree {
		t.Fatalf("IncludeMissingHint = %q must NOT fire when --from is explicit", res.IncludeMissingHint)
	}
}

// TestSingleWorktree_ExplicitAbsoluteIncludeUnderTargetNoops pins that the
// single-worktree no-op short-circuits BEFORE include resolution. Even when
// the caller passes an explicit absolute --include path under the target
// worktree (which would otherwise be valid), the engine returns a no-op
// success rather than erroring on missing source.
//
// This codifies the ordering: `--from auto` + single-worktree → no-op,
// regardless of any --include argument.
func TestSingleWorktree_ExplicitAbsoluteIncludeUnderTargetNoops(t *testing.T) {
	repo := setupSingleWorktreeRepo(t)

	includeAbs := filepath.Join(repo, testIncludeFile)

	res, code, err := NewEngine().Apply(context.Background(), repo, ApplyOptions{
		From:    "auto",
		Include: includeAbs,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d (explicit absolute --include must still no-op in single-worktree mode)", code, exitcode.OK)
	}
	if res.Summary.Matched != 0 || res.Summary.Copied != 0 || len(res.Actions) != 0 {
		t.Fatalf("expected single-worktree no-op even with explicit --include, got %+v", res.Summary)
	}
	if res.IncludeMissingHint != IncludeMissingHintSingleWorktree {
		t.Errorf("IncludeMissingHint = %q, want %q", res.IncludeMissingHint, IncludeMissingHintSingleWorktree)
	}
}
