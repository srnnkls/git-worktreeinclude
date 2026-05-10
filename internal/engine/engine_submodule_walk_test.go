package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/satococoa/git-worktreeinclude/internal/exitcode"
)

// TestSubmoduleWalk_PartialTreeAnchorsLeaves — a submodule pattern marked
// `submodule-walk` recurses into the submodule's working tree. Per-leaf
// symlinks land INSIDE the existing target mountpoint dir; source-tracked
// submodule files generate no actions; an `expand/walked` rollup is emitted
// at the mountpoint and exit code is OK.
func TestSubmoduleWalk_PartialTreeAnchorsLeaves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	// `vendor/lib` already has `lib.txt` tracked in the SUBMODULE'S index
	// (committed in setupSubmoduleFixture's upstream repo). Add an untracked
	// sibling inside the submodule working tree so the walker has at least
	// one leaf to symlink. The submodule's own .gitignore makes it ignored.
	subWT := filepath.Join(fx.root, "vendor", "lib")
	writeFile(t, filepath.Join(subWT, ".gitignore"), "build.log\n")
	runGit(t, subWT, "add", ".gitignore")
	runGit(t, subWT, "commit", "-q", "-m", "ignore build.log inside submodule")
	writeFile(t, filepath.Join(subWT, "build.log"), "BUILD\n")

	// Re-create the target's mountpoint as an empty directory so the test
	// asserts that walked actions land INSIDE it.
	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink submodule-walk\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d (submodule-walk should walk, not conflict)", code, exitcode.OK)
	}

	// Mountpoint must remain a real directory (not a symlink, not deleted).
	mountInfo, err := os.Lstat(mountTgt)
	if err != nil {
		t.Fatalf("lstat target mountpoint after Apply: %v", err)
	}
	if mountInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("mountpoint must NOT be replaced with a symlink, mode=%v", mountInfo.Mode())
	}
	if !mountInfo.IsDir() {
		t.Fatalf("mountpoint must remain a directory, mode=%v", mountInfo.Mode())
	}

	// The walker emits a rollup at the mountpoint with at least one expansion.
	rollup := findActionOptional(res.Actions, "vendor/lib")
	if rollup.Op != "expand" || rollup.Status != StatusWalked {
		t.Fatalf("expected expand/%s rollup at vendor/lib, got %+v (all=%+v)", StatusWalked, rollup, res.Actions)
	}
	if rollup.Expanded < 1 {
		t.Fatalf("expand rollup should report Expanded>=1, got %+v", rollup)
	}

	// Untracked submodule leaf becomes a per-leaf symlink inside the mountpoint.
	if action := findAction(t, res.Actions, "vendor/lib/build.log"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for vendor/lib/build.log, got %+v", action)
	}
	leafInfo, err := os.Lstat(filepath.Join(mountTgt, "build.log"))
	if err != nil {
		t.Fatalf("lstat vendor/lib/build.log in target: %v", err)
	}
	if leafInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("vendor/lib/build.log should be a symlink, mode=%v", leafInfo.Mode())
	}

	// Source-tracked submodule file (lib.txt is tracked in the submodule's
	// index) must NOT generate an action — submodule's own tracked set is
	// authoritative for the recursion.
	if a := findActionOptional(res.Actions, "vendor/lib/lib.txt"); a.Op != "" {
		t.Fatalf("submodule-tracked leaf must produce no action, got %+v", a)
	}
}

// TestSubmoduleWalk_DotGitSkipped — the submodule's `.git` gitdir-pointer
// file at the submodule root is silently skipped by the walker. Never emit
// any action for `<mountpoint>/.git`.
func TestSubmoduleWalk_DotGitSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	// setupSubmoduleFixture adds the submodule via `git submodule add`, which
	// places a `.git` file (pointing at the gitdir) at the submodule root.
	subWT := filepath.Join(fx.root, "vendor", "lib")
	dotGit := filepath.Join(subWT, ".git")
	if _, err := os.Lstat(dotGit); err != nil {
		t.Fatalf("expected submodule .git pointer at %s: %v", dotGit, err)
	}

	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink submodule-walk\n")

	res, _, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// No action at all may reference the submodule's .git pointer — neither
	// a leaf, nor a skip, nor a conflict, nor an error.
	if a := findActionOptional(res.Actions, "vendor/lib/.git"); a.Op != "" {
		t.Fatalf("walker must not emit any action for the submodule .git pointer, got %+v", a)
	}
	// Also: the gitdir pointer itself must not have been touched in target.
	if _, err := os.Lstat(filepath.Join(mountTgt, ".git")); err == nil {
		t.Fatalf("walker leaked the submodule .git pointer into target mountpoint")
	}
}

// TestSubmoduleWalk_TargetMountpointPreserved — the target's mountpoint dir
// must remain a real directory before AND after Apply when submodule-walk
// is active. No symlink replaces it.
func TestSubmoduleWalk_TargetMountpointPreserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}
	// Anchor a marker file inside the mountpoint to make sure the dir itself
	// is preserved (not deleted and recreated as a symlink).
	marker := filepath.Join(mountTgt, ".keep")
	if err := os.WriteFile(marker, []byte("KEEP\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	beforeInfo, err := os.Lstat(mountTgt)
	if err != nil {
		t.Fatalf("lstat mountpoint before Apply: %v", err)
	}
	if beforeInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("precondition: mountpoint should be a real dir, mode=%v", beforeInfo.Mode())
	}
	if !beforeInfo.IsDir() {
		t.Fatalf("precondition: mountpoint should be a directory, mode=%v", beforeInfo.Mode())
	}

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink submodule-walk\n")

	if _, _, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	afterInfo, err := os.Lstat(mountTgt)
	if err != nil {
		t.Fatalf("lstat mountpoint after Apply: %v", err)
	}
	if afterInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("mountpoint must NOT be replaced with a symlink after submodule-walk, mode=%v", afterInfo.Mode())
	}
	if !afterInfo.IsDir() {
		t.Fatalf("mountpoint must remain a real directory after submodule-walk, mode=%v", afterInfo.Mode())
	}

	// Marker file inside mountpoint must still be there.
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("marker file inside mountpoint disappeared: %v", err)
	}
}

// TestSubmoduleWalk_DefaultBehaviorUnchanged — the same submodule pattern
// without `submodule-walk` continues to emit a single applySymlink at the
// mountpoint, which collides with an existing target dir as
// `conflict/diff_link`. Regression guard for the existing v0.8.2 path.
func TestSubmoduleWalk_DefaultBehaviorUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	// Existing dir at mountpoint — the legacy code path tries to anchor a
	// directory-level symlink here and must conflict.
	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}

	// Plain `symlink` attribute — no `submodule-walk`.
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("default submodule symlink with existing target dir must conflict, got code=%d (all=%+v)", code, res.Actions)
	}
	if action := findAction(t, res.Actions, "vendor/lib"); action.Op != "conflict" || action.Status != StatusDiffLink {
		t.Fatalf("expected conflict/%s for vendor/lib, got %+v", StatusDiffLink, action)
	}
	// And no rollup, since legacy path does not recurse.
	for _, a := range res.Actions {
		if a.Op == "expand" && a.Path == "vendor/lib" {
			t.Fatalf("default submodule symlink path must NOT emit expand/walked rollup, got %+v", a)
		}
	}
}

// TestSubmoduleWalk_SourceAbsoluteSymlinkRewrittenToSourceAbsolute — an
// absolute symlink inside a source submodule WT must be rewritten so the
// recreated link in the target points at the SOURCE submodule's path, not
// at the per-leaf destination AND not at the target mountpoint. Concretely,
// `<subWT>/inner/foo -> <subWT>/data/x.txt` must become
// `<wt>/vendor/lib/inner/foo -> <subWT>/data/x.txt`, NOT
// `<wt>/vendor/lib/inner/foo/data/x.txt` and NOT
// `<wt>/vendor/lib/inner/foo -> <wt>/vendor/lib/data/x.txt`.
//
// The walk-mode contract is: target's tree only contains the visited
// untracked leaves, so any link target inside `subWT` resolves reliably
// only via the source path.
func TestSubmoduleWalk_SourceAbsoluteSymlinkRewrittenToSourceAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	subWT := filepath.Join(fx.root, "vendor", "lib")
	// Track sentinels inside `inner/` and `data/` so neither directory can be
	// anchored as a single dir-symlink by the submodule walker; the walker
	// must then recurse and visit the absolute symlink itself.
	innerDir := filepath.Join(subWT, "inner")
	dataDir := filepath.Join(subWT, "data")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatalf("mkdir submodule inner/: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir submodule data/: %v", err)
	}
	writeFile(t, filepath.Join(innerDir, "tracked.txt"), "T\n")
	writeFile(t, filepath.Join(dataDir, "tracked.txt"), "T\n")
	runGit(t, subWT, "add", "inner/tracked.txt", "data/tracked.txt")
	runGit(t, subWT, "commit", "-q", "-m", "track sentinels under inner/ and data/")

	writeFile(t, filepath.Join(dataDir, "x.txt"), "X\n")
	absLinkTarget := filepath.Join(subWT, "data", "x.txt")
	linkPath := filepath.Join(innerDir, "foo")
	if err := os.Symlink(absLinkTarget, linkPath); err != nil {
		t.Fatalf("create absolute symlink: %v", err)
	}

	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink submodule-walk\n")

	if _, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	} else if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	recreated := filepath.Join(mountTgt, "inner", "foo")
	got, err := os.Readlink(recreated)
	if err != nil {
		t.Fatalf("readlink %s: %v", recreated, err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected rewritten absolute target, got %q", got)
	}
	// The recreated link must point at the SOURCE submodule's
	// `<subWT>/data/x.txt` — NOT the buggy per-leaf path
	// `<wt>/vendor/lib/inner/foo/data/x.txt` and NOT a target-mountpoint-
	// rooted `<wt>/vendor/lib/data/x.txt` (which would dangle whenever
	// the submodule walker silently skipped the tracked sibling).
	// Compare via EvalSymlinks to absorb the macOS `/var → /private/var`
	// mismatch between git's `rev-parse --show-toplevel` output and
	// `os.MkdirTemp`'s prefix.
	gotCanon, err := filepath.EvalSymlinks(filepath.Dir(got))
	if err != nil {
		t.Fatalf("eval parent of recreated link target %q: %v", got, err)
	}
	gotCanon = filepath.Join(gotCanon, filepath.Base(got))
	wantCanon, err := filepath.EvalSymlinks(filepath.Join(subWT, "data"))
	if err != nil {
		t.Fatalf("eval expected target dir: %v", err)
	}
	wantCanon = filepath.Join(wantCanon, "x.txt")
	if filepath.Clean(gotCanon) != filepath.Clean(wantCanon) {
		t.Fatalf("recreated absolute symlink target mismatch:\n  got:  %s\n  want: %s", gotCanon, wantCanon)
	}
	// Buggy form would contain `inner/foo` in the link target's directory
	// portion. Guard explicitly so the failure mode is unambiguous.
	if strings.Contains(filepath.Clean(got), filepath.Join("inner", "foo")+string(os.PathSeparator)) {
		t.Fatalf("recreated link target must not pass through per-leaf inner/foo, got=%s", got)
	}
}

// TestSubmoduleWalk_SubmoduleTrackedSetIsIndependent — when the walker
// recurses into a submodule under `submodule-walk`, the tracked-set lookup
// must use the SUBMODULE's own index, not the parent repo's. A file that is
// tracked in the submodule but absent from the parent's index must NOT be
// turned into a symlink (the submodule's tracked set excludes it).
//
// This is the precise test that catches an implementation that accidentally
// re-uses prep.tracked (parent-rooted) inside the submodule walk.
func TestSubmoduleWalk_SubmoduleTrackedSetIsIndependent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	subWT := filepath.Join(fx.root, "vendor", "lib")
	// Create and commit a file that is tracked ONLY in the submodule's index.
	// The parent repo never sees it (the submodule shows up in the parent
	// index as a single gitlink commit).
	innerDir := filepath.Join(subWT, "inner")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatalf("mkdir submodule inner/: %v", err)
	}
	writeFile(t, filepath.Join(innerDir, "tracked-only-in-sub.txt"), "SUB_TRACKED\n")
	runGit(t, subWT, "add", "inner/tracked-only-in-sub.txt")
	runGit(t, subWT, "commit", "-q", "-m", "track inner/tracked-only-in-sub.txt in submodule")

	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink submodule-walk\n")

	res, _, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The submodule-tracked file must NOT have a symlink action. If the
	// implementation incorrectly checks the PARENT's tracked set, it would
	// see "untracked at parent" and emit a symlink/done.
	if action := findActionOptional(res.Actions, "vendor/lib/inner/tracked-only-in-sub.txt"); action.Op == "symlink" {
		t.Fatalf("walker used parent's tracked set instead of submodule's: emitted %+v for a submodule-tracked file", action)
	}
	// And no symlink should have been written on disk for it.
	if info, err := os.Lstat(filepath.Join(mountTgt, "inner", "tracked-only-in-sub.txt")); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("walker created a symlink for a submodule-tracked file (%v)", info.Mode())
		}
	}
}

// TestSubmoduleWalk_SourceRelativeSymlinkRewrittenToSourceAbsolute — a
// relative symlink inside a source submodule that crosses into a
// source-tracked sibling must be rewritten to an absolute path resolving
// inside the SOURCE submodule WT. The submodule walker silently skips
// submodule-tracked leaves, so the target tree does NOT mirror the
// submodule's tree; preserving the verbatim relative target would resolve
// through paths that target never materializes.
//
// Reproducer drawn from the user's `effect/` submodule report: an untracked
// fixture file links across into a tracked sibling subtree.
func TestSubmoduleWalk_SourceRelativeSymlinkRewrittenToSourceAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	subWT := filepath.Join(fx.root, "vendor", "lib")
	dataDir := filepath.Join(subWT, "data")
	innerDir := filepath.Join(subWT, "inner")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}

	// `data/x.txt` is tracked in the SUBMODULE'S index — the walker will
	// silently skip it, leaving the target without `vendor/lib/data/x.txt`.
	writeFile(t, filepath.Join(dataDir, "x.txt"), "X\n")
	// Sentinel inside `inner/` so the dir is partial-tracked; the walker
	// must recurse and visit the relative symlink rather than anchor.
	writeFile(t, filepath.Join(innerDir, "tracked.txt"), "T\n")
	runGit(t, subWT, "add", "data/x.txt", "inner/tracked.txt")
	runGit(t, subWT, "commit", "-q", "-m", "track data/x.txt and inner/tracked.txt")

	// Untracked relative symlink crossing the tracked/untracked boundary.
	if err := os.Symlink("../data/x.txt", filepath.Join(innerDir, "foo")); err != nil {
		t.Fatalf("create relative symlink: %v", err)
	}

	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink submodule-walk\n")

	if _, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	} else if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	// Target must NOT have the tracked sibling — the walker silently skipped
	// it, which is precisely why a verbatim relative link would be broken.
	if _, err := os.Lstat(filepath.Join(mountTgt, "data", "x.txt")); err == nil {
		t.Fatalf("precondition broken: target unexpectedly has data/x.txt; the walker was supposed to skip it")
	}

	recreated := filepath.Join(mountTgt, "inner", "foo")
	info, err := os.Lstat(recreated)
	if err != nil {
		t.Fatalf("lstat recreated link %s: %v", recreated, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("recreated path must be a symlink, got mode=%v", info.Mode())
	}

	got, err := os.Readlink(recreated)
	if err != nil {
		t.Fatalf("readlink %s: %v", recreated, err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("walk-mode must rewrite the relative target to an absolute source-rooted path, got %q", got)
	}

	// The recreated link must resolve to the SOURCE submodule's data/x.txt
	// because the source is the only side that actually has the file.
	gotResolved, err := filepath.EvalSymlinks(recreated)
	if err != nil {
		t.Fatalf("eval symlinks on recreated link: %v", err)
	}
	wantResolved, err := filepath.EvalSymlinks(filepath.Join(subWT, "data", "x.txt"))
	if err != nil {
		t.Fatalf("eval symlinks on source data/x.txt: %v", err)
	}
	if filepath.Clean(gotResolved) != filepath.Clean(wantResolved) {
		t.Fatalf("recreated symlink must resolve through the source submodule:\n  got:  %s\n  want: %s", gotResolved, wantResolved)
	}
}
