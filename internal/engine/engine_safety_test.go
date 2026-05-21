package engine

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/satococoa/git-worktreeinclude/internal/exitcode"
)

// TestSafety_SubmoduleSymlinkAttrCreatesDirSymlink — a submodule listed with
// `symlink` becomes a directory-level symlink at the destination pointing at
// the source submodule working tree.
func TestSafety_SubmoduleSymlinkAttrCreatesDirSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink\n")

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

	dst := filepath.Join(fx.wt, "vendor", "lib")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat vendor/lib: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("vendor/lib should be a symlink (got mode=%v) — directory-level symlink for submodule", info.Mode())
	}
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink vendor/lib: %v", err)
	}
	wantAbs := filepath.Join(fx.root, "vendor", "lib")
	if !sameFileWeak(t, got, wantAbs) && !sameFile(t, dst, wantAbs) {
		t.Fatalf("vendor/lib symlink should point at source submodule tree (%q), got readlink=%q", wantAbs, got)
	}

	if action := findAction(t, res.Actions, "vendor/lib"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for vendor/lib, got %+v", action)
	}
}

// TestSafety_SubmoduleCopyAttrSkipsWithReason — a submodule with default
// (copy) attribute must be skipped with a clear reason; no directory created.
func TestSafety_SubmoduleCopyAttrSkipsWithReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib\n")

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

	if action := findAction(t, res.Actions, "vendor/lib"); action.Op != "skip" || action.Status != StatusSubmoduleCopyUnsupported {
		t.Fatalf("expected skip/%s for submodule copy, got %+v", StatusSubmoduleCopyUnsupported, action)
	}

	if res.Summary.SkippedSubmoduleCopy != 1 {
		t.Fatalf("expected SkippedSubmoduleCopy=1, got %+v", res.Summary)
	}
	if res.Summary.SkippedMissingSrc != 0 {
		t.Fatalf("submodule-copy skip must NOT increment SkippedMissingSrc, got %+v", res.Summary)
	}

	if _, err := os.Lstat(filepath.Join(fx.wt, "vendor", "lib")); !os.IsNotExist(err) {
		t.Fatalf("submodule copy should not create vendor/lib, lstat err=%v", err)
	}
}

// TestSafety_LiteralPatternMissingSrcSkipped — a literal-path pattern whose
// source does not exist must surface as skip/missing_src instead of being
// silently dropped from the candidate set.
func TestSafety_LiteralPatternMissingSrcSkipped(t *testing.T) {
	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "nonexistent.txt  symlink\n")

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

	if action := findAction(t, res.Actions, "nonexistent.txt"); action.Op != "skip" || action.Status != StatusMissingSrc {
		t.Fatalf("expected skip/%s for nonexistent.txt, got %+v", StatusMissingSrc, action)
	}
	if res.Summary.SkippedMissingSrc != 1 {
		t.Fatalf("expected SkippedMissingSrc=1, got %+v", res.Summary)
	}
}

// TestSafety_UntrackedNotIgnoredFileIsActioned — a regular untracked file
// that is NOT in `.gitignore` must still be actioned. The current engine
// intersects with `git ls-files -o -i --exclude-standard`, which excludes
// these files; the new engine must drop that filter.
func TestSafety_UntrackedNotIgnoredFileIsActioned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	// .codex is NOT mentioned in .gitignore in setupEngineFixture, so it is
	// "untracked, not ignored". Today the engine misses it.
	writeFile(t, filepath.Join(fx.root, ".codex"), "PERSONAL\n")
	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".codex  symlink\n")

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

	dst := filepath.Join(fx.wt, ".codex")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat .codex: %v (untracked-not-ignored file should be actioned)", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".codex should be a symlink, got mode=%v", info.Mode())
	}

	if action := findAction(t, res.Actions, ".codex"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for .codex, got %+v", action)
	}
}

// TestSafety_TrackedSourceFileSilentlySkipped — a tracked source leaf is
// silently skipped at every depth, including when it is the candidate root.
// No action, no conflict, no error. Target-tracked leaves remain a hard
// `conflict/tracked` (covered by walker tests); source-tracked is skip-silent.
func TestSafety_TrackedSourceFileSilentlySkipped(t *testing.T) {
	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "README.md\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d (tracked source must silently skip)", code, exitcode.OK)
	}
	if a := findActionOptional(res.Actions, "README.md"); a.Op != "" {
		t.Fatalf("tracked source leaf must produce no action, got %+v", a)
	}
	if res.Summary.Conflicts != 0 || res.Summary.Errors != 0 {
		t.Fatalf("tracked source leaf must not increment conflicts/errors, got %+v", res.Summary)
	}
}

// TestSafety_TrackedDestinationSubtreeWalksAroundTrackedLeaves — the walker
// recurses into a partial-tracked dir, anchors a symlink at every untracked
// leaf, and silently leaves tracked-target leaves alone (no per-leaf action,
// exit OK).
func TestSafety_TrackedDestinationSubtreeWalksAroundTrackedLeaves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	// Tracked target leaf: mixed/keep.txt committed on the worktree branch.
	if err := os.MkdirAll(filepath.Join(fx.wt, "mixed"), 0o755); err != nil {
		t.Fatalf("mkdir target mixed/: %v", err)
	}
	writeFile(t, filepath.Join(fx.wt, "mixed", "keep.txt"), "TARGET_KEEP\n")
	runGit(t, fx.wt, "add", "mixed/keep.txt")
	runGit(t, fx.wt, "commit", "-q", "-m", "tracked mixed/keep.txt in target")

	// Source has only an untracked file under mixed/ (.gitignore covers it).
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\nmixed/\n")
	runGit(t, fx.root, "add", ".gitignore")
	runGit(t, fx.root, "commit", "-q", "-m", "ignore mixed/")
	if err := os.MkdirAll(filepath.Join(fx.root, "mixed"), 0o755); err != nil {
		t.Fatalf("mkdir source mixed/: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "mixed", "build.log"), "untracked\n")

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "mixed/  symlink\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d (walker should anchor around tracked leaf)", code, exitcode.OK)
	}

	// Rollup at pattern root.
	rollup := findActionOptional(res.Actions, "mixed")
	if rollup.Op != "expand" || rollup.Status != StatusWalked {
		t.Fatalf("expected expand/%s rollup at mixed, got %+v (all=%+v)", StatusWalked, rollup, res.Actions)
	}
	if rollup.Expanded < 1 {
		t.Fatalf("expand rollup should report expanded>=1, got %+v", rollup)
	}

	// Untracked leaf gets a symlink.
	if action := findAction(t, res.Actions, "mixed/build.log"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for mixed/build.log, got %+v", action)
	}
	leaf := filepath.Join(fx.wt, "mixed", "build.log")
	info, err := os.Lstat(leaf)
	if err != nil {
		t.Fatalf("lstat mixed/build.log: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("mixed/build.log should be a symlink, got mode=%v", info.Mode())
	}

	// Tracked target leaf untouched and not actioned.
	if a := findActionOptional(res.Actions, "mixed/keep.txt"); a.Op != "" {
		t.Fatalf("tracked target leaf must produce no action, got %+v", a)
	}
	keep, err := os.ReadFile(filepath.Join(fx.wt, "mixed", "keep.txt"))
	if err != nil {
		t.Fatalf("read mixed/keep.txt: %v", err)
	}
	if string(keep) != "TARGET_KEEP\n" {
		t.Fatalf("tracked target leaf should remain unchanged, got %q", keep)
	}

	// `mixed` itself must remain a directory (not anchored as a symlink).
	dirInfo, err := os.Lstat(filepath.Join(fx.wt, "mixed"))
	if err != nil {
		t.Fatalf("lstat target mixed/: %v", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target mixed/ must NOT be replaced with a symlink, mode=%v", dirInfo.Mode())
	}
}

// TestSafety_DirSymlinkForIgnoredDir — gitignored source dir with `symlink`
// attribute produces ONE symlink at the destination pointing at source's
// absolute dir path. Per-file recursion must NOT happen.
func TestSafety_DirSymlinkForIgnoredDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\ncache/\n")
	runGit(t, fx.root, "add", ".gitignore")
	runGit(t, fx.root, "commit", "-q", "-m", "ignore cache/")
	if err := os.MkdirAll(filepath.Join(fx.root, "cache"), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "cache", "a.bin"), "AAA\n")
	writeFile(t, filepath.Join(fx.root, "cache", "b.bin"), "BBB\n")
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "cache/  symlink\n")

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

	dst := filepath.Join(fx.wt, "cache")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat target cache: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected ONE directory symlink at cache, got mode=%v", info.Mode())
	}

	// Per-file entries must not exist as separate symlinks or copies.
	for _, sub := range []string{"cache/a.bin", "cache/b.bin"} {
		if a := findActionOptional(res.Actions, sub); a.Op != "" {
			t.Fatalf("expected no per-file action for %s, got %+v", sub, a)
		}
	}
	// No rollup either: nothing recursed, the dir was anchored at the pattern root.
	if a := findActionOptional(res.Actions, "cache"); a.Op == "expand" {
		t.Fatalf("anchored dir-symlink must NOT emit an expand rollup, got %+v", a)
	}
}

// TestSafety_DirCopyRecursesFiles — a gitignored dir with default copy
// attribute keeps the per-file recursion behavior.
func TestSafety_DirCopyRecursesFiles(t *testing.T) {
	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\ncache/\n")
	runGit(t, fx.root, "add", ".gitignore")
	runGit(t, fx.root, "commit", "-q", "-m", "ignore cache/")
	if err := os.MkdirAll(filepath.Join(fx.root, "cache"), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "cache", "a.bin"), "AAA\n")
	writeFile(t, filepath.Join(fx.root, "cache", "b.bin"), "BBB\n")
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "cache/\n")

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

	for _, rel := range []string{"cache/a.bin", "cache/b.bin"} {
		if action := findAction(t, res.Actions, rel); action.Op != "copy" || action.Status != "done" {
			t.Fatalf("expected copy/done for %s, got %+v", rel, action)
		}
		if _, err := os.Stat(filepath.Join(fx.wt, rel)); err != nil {
			t.Fatalf("expected file %s in target: %v", rel, err)
		}
	}

	// Copy-mode dir always walks, so a rollup must be emitted at the pattern root.
	rollup := findActionOptional(res.Actions, "cache")
	if rollup.Op != "expand" || rollup.Status != StatusWalked {
		t.Fatalf("expected expand/%s rollup at cache, got %+v (all=%+v)", StatusWalked, rollup, res.Actions)
	}
	if rollup.Expanded < 2 {
		t.Fatalf("expand rollup should count both leaves, got %+v", rollup)
	}

	// The destination must NOT be a single symlink.
	info, err := os.Lstat(filepath.Join(fx.wt, "cache"))
	if err != nil {
		t.Fatalf("lstat target cache: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("copy mode should not produce a directory symlink, got mode=%v", info.Mode())
	}
}

// TestSafety_GlobPatternStillExpands — globs continue to expand to every
// matching untracked file; the `data/` sub-case pins the dropped
// `--exclude-standard` filter by matching files that are NOT in gitignore.
func TestSafety_GlobPatternStillExpands(t *testing.T) {
	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\nlogs/\n")
	runGit(t, fx.root, "add", ".gitignore")
	runGit(t, fx.root, "commit", "-q", "-m", "ignore logs/")
	if err := os.MkdirAll(filepath.Join(fx.root, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "logs", "one.log"), "1\n")
	writeFile(t, filepath.Join(fx.root, "logs", "two.log"), "2\n")
	writeFile(t, filepath.Join(fx.root, "logs", "ignore.txt"), "x\n")
	// data/ is NOT mentioned in .gitignore: data/*.log files are
	// "untracked, not ignored". The legacy `-i --exclude-standard` filter
	// would drop them; the new model must still match them.
	if err := os.MkdirAll(filepath.Join(fx.root, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, "data", "metrics.log"), "M\n")
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "logs/*.log\ndata/*.log\n")

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

	for _, rel := range []string{"logs/one.log", "logs/two.log", "data/metrics.log"} {
		if action := findAction(t, res.Actions, rel); action.Op != "copy" || action.Status != "done" {
			t.Fatalf("expected copy/done for %s, got %+v", rel, action)
		}
	}
	if a := findActionOptional(res.Actions, "logs/ignore.txt"); a.Op != "" {
		t.Fatalf("logs/ignore.txt should not match logs/*.log, got %+v", a)
	}
}

// TestSafety_PartiallyTrackedDirSymlinkWalksAndAnchorsLeaves — a partial dir
// listed with `symlink` walks: a rollup at the pattern root, a symlink at
// the untracked leaf, and silent skip for the tracked source leaf.
func TestSafety_PartiallyTrackedDirSymlinkWalksAndAnchorsLeaves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	if err := os.MkdirAll(filepath.Join(fx.root, ".cfg"), 0o755); err != nil {
		t.Fatalf("mkdir .cfg: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, ".cfg", "tracked.toml"), "TRACKED\n")
	runGit(t, fx.root, "add", ".cfg/tracked.toml")
	runGit(t, fx.root, "commit", "-q", "-m", "track .cfg/tracked.toml")
	writeFile(t, filepath.Join(fx.root, ".cfg", "untracked.toml"), "UNTRACKED\n")

	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".cfg  symlink\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d (walker should anchor untracked leaves)", code, exitcode.OK)
	}

	rollup := findActionOptional(res.Actions, ".cfg")
	if rollup.Op != "expand" || rollup.Status != StatusWalked {
		t.Fatalf("expected expand/%s rollup at .cfg, got %+v (all=%+v)", StatusWalked, rollup, res.Actions)
	}
	if rollup.Expanded < 1 {
		t.Fatalf("expand rollup should report expanded>=1, got %+v", rollup)
	}

	if action := findAction(t, res.Actions, ".cfg/untracked.toml"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for .cfg/untracked.toml, got %+v", action)
	}
	if a := findActionOptional(res.Actions, ".cfg/tracked.toml"); a.Op != "" {
		t.Fatalf("tracked source leaf must produce no action, got %+v", a)
	}

	leaf := filepath.Join(fx.wt, ".cfg", "untracked.toml")
	info, err := os.Lstat(leaf)
	if err != nil {
		t.Fatalf("lstat .cfg/untracked.toml: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".cfg/untracked.toml should be a symlink, got mode=%v", info.Mode())
	}

	dirInfo, err := os.Lstat(filepath.Join(fx.wt, ".cfg"))
	if err != nil {
		t.Fatalf("lstat target .cfg: %v", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("partial-tracked .cfg must NOT be replaced with a symlink")
	}
}

// TestSymlinkSource_PatternRootRelativeInsideSource_RewritesToSourceAbsolute —
// a source-side symlink whose relative target resolves inside the source root
// is recreated at target as a source-absolute path. Dotfile-alias use case:
// `.codex -> .claude` in source must produce target's `.codex` resolving
// through to source's `.claude`, not to target's separate `.claude`.
func TestSymlinkSource_PatternRootRelativeInsideSource_RewritesToSourceAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\ntarget.txt\nalias\n")
	writeFile(t, filepath.Join(fx.root, "target.txt"), "CANONICAL\n")
	if err := os.Symlink("target.txt", filepath.Join(fx.root, "alias")); err != nil {
		t.Fatalf("create relative source symlink alias -> target.txt: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "alias  symlink\n")

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
	if action := findAction(t, res.Actions, "alias"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for alias, got %+v", action)
	}

	dst := filepath.Join(fx.wt, "alias")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat alias: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("alias should be a symlink, got mode=%v", info.Mode())
	}
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink alias: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("recreated link target must be source-absolute, got relative %q", got)
	}
	wantSource := filepath.Join(fx.root, "target.txt")
	if !sameFileWeak(t, got, wantSource) && !sameFile(t, dst, wantSource) {
		t.Fatalf("recreated link should resolve to source's target.txt (%q), readlink=%q", wantSource, got)
	}
	if sameFileWeak(t, got, filepath.Join(fx.wt, "target.txt")) {
		t.Fatalf("recreated link must NOT be rewritten under target worktree, readlink=%q", got)
	}
}

// TestSymlinkSource_PatternRootAbsoluteInsideSource_RewritesToSourceAbsolute —
// a source-side symlink whose absolute target points inside the source root
// is recreated at target as a source-absolute path (unchanged anchor, but
// the contract is the unified source-anchor rule, not a target-rewrite).
func TestSymlinkSource_PatternRootAbsoluteInsideSource_RewritesToSourceAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\ntarget.txt\nalias\n")
	writeFile(t, filepath.Join(fx.root, "target.txt"), "CANONICAL\n")
	absInside := filepath.Join(fx.root, "target.txt")
	if err := os.Symlink(absInside, filepath.Join(fx.root, "alias")); err != nil {
		t.Fatalf("create absolute-inside-source symlink alias -> %s: %v", absInside, err)
	}
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "alias  symlink\n")

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
	if action := findAction(t, res.Actions, "alias"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for alias, got %+v", action)
	}

	dst := filepath.Join(fx.wt, "alias")
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink alias: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("recreated link target must be source-absolute, got relative %q", got)
	}
	wantSource := filepath.Join(fx.root, "target.txt")
	if !sameFileWeak(t, got, wantSource) && !sameFile(t, dst, wantSource) {
		t.Fatalf("recreated link should resolve to source's target.txt (%q), readlink=%q", wantSource, got)
	}
	if sameFileWeak(t, got, filepath.Join(fx.wt, "target.txt")) {
		t.Fatalf("recreated link must NOT be rewritten under target worktree, readlink=%q", got)
	}
}

// TestSymlinkSource_PatternRootOutsideSource_PreservedVerbatim — an external
// absolute symlink target (resolving outside the source root) is preserved
// verbatim. Cross-boundary links are the user's responsibility.
func TestSymlinkSource_PatternRootOutsideSource_PreservedVerbatim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, ".gitignore"), ".env\n.env.local\nexternal\n")
	external := "/etc/hosts"
	if err := os.Symlink(external, filepath.Join(fx.root, "external")); err != nil {
		t.Fatalf("create external symlink: %v", err)
	}
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "external  symlink\n")

	_, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	dst := filepath.Join(fx.wt, "external")
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink external: %v", err)
	}
	if got != external {
		t.Fatalf("external symlink target should be preserved verbatim: want %q, got %q", external, got)
	}
}

// findActionOptional returns the matching action or a zero Action if absent.
// Unlike findAction it does not fail the test.
func findActionOptional(actions []Action, path string) Action {
	for _, a := range actions {
		if a.Path == path {
			return a
		}
	}
	return Action{}
}

// submoduleFixture extends engineFixture by adding one submodule at relPath
// inside the source worktree. The submodule has one initial commit. The
// target worktree (fx.wt) does NOT yet have the submodule checked out (it
// lives only in source's index until the user actions it).
func setupSubmoduleFixture(t *testing.T, relPath string) engineFixture {
	t.Helper()

	base := t.TempDir()

	// Create an "upstream" repo to add as a submodule. file:// URL keeps
	// `git submodule add` happy without network access.
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

	// Source repo.
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

	// Add submodule from local file:// URL.
	upstreamURL := (&url.URL{Scheme: "file", Path: upstream}).String()
	runGitTB(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstreamURL, relPath)
	runGitTB(t, repo, "commit", "-q", "-m", "add submodule "+relPath)

	subWT := filepath.Join(repo, relPath)
	runGitTB(t, subWT, "config", "user.name", "Test User")
	runGitTB(t, subWT, "config", "user.email", "test@example.com")

	// Target worktree.
	wt := filepath.Join(base, "wt")
	runGitTB(t, repo, "worktree", "add", "-q", wt, "-b", "feature")

	// `git worktree add` leaves an empty mountpoint dir for any uninitialized
	// submodule gitlink. Drop it so the fixture matches "submodule not yet
	// materialized in target" — the precondition the tests assume.
	pruneEmptyDirs(t, filepath.Join(wt, relPath))

	return engineFixture{root: repo, wt: wt}
}

// pruneEmptyDirs removes `leaf` and any now-empty parent dirs up to the
// caller-provided leaf path. Stops at the first non-empty dir.
func pruneEmptyDirs(t *testing.T, leaf string) {
	t.Helper()
	for cur := leaf; cur != "" && cur != "/"; cur = filepath.Dir(cur) {
		entries, err := os.ReadDir(cur)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(cur); err != nil {
			return
		}
	}
}

// runGitTB is a tb-aware variant of runGit so we can call it from helpers
// that take *testing.T but want clearer error context. It captures
// stderr+stdout for diagnostics.
func runGitTB(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/echo",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
