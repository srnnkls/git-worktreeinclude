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
// `submodule-walk` shares the submodule's WT (sans `.git`) with target.
// Per-leaf symlinks land INSIDE the existing target mountpoint dir for
// every entry the target lacks; an `expand/walked` rollup is emitted at the
// mountpoint and exit code is OK. Submodule-tracked files are NOT silently
// skipped — they live only in source's initialised submodule, so target
// needs them.
func TestSubmoduleWalk_PartialTreeAnchorsLeaves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	// `vendor/lib` already has `lib.txt` tracked in the SUBMODULE'S index
	// (committed in setupSubmoduleFixture's upstream repo). Add an untracked
	// sibling inside the submodule working tree alongside it. The submodule's
	// own .gitignore makes it ignored.
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

	// Submodule-tracked file `lib.txt` lives only in source's initialised
	// submodule WT — target needs it. Walker MUST emit a symlink/done action
	// for it (or anchor a parent dir-symlink covering it).
	libAction := findAction(t, res.Actions, "vendor/lib/lib.txt")
	if libAction.Op != "symlink" || libAction.Status != "done" {
		t.Fatalf("expected symlink/done for vendor/lib/lib.txt (submodule-tracked content must be shared), got %+v", libAction)
	}
	libPath := filepath.Join(mountTgt, "lib.txt")
	libInfo, err := os.Lstat(libPath)
	if err != nil {
		t.Fatalf("lstat vendor/lib/lib.txt in target: %v", err)
	}
	if libInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("vendor/lib/lib.txt in target should be a symlink, mode=%v", libInfo.Mode())
	}
	got, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatalf("read vendor/lib/lib.txt through target symlink: %v", err)
	}
	if string(got) != "LIB\n" {
		t.Fatalf("vendor/lib/lib.txt content via target symlink = %q, want %q", string(got), "LIB\n")
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
	// Pre-create target's `inner/` so the walker descends into it and
	// rewrites the absolute symlink at the per-leaf visit, rather than
	// covering it with a single dir-symlink at `inner/`.
	if err := os.MkdirAll(filepath.Join(mountTgt, "inner"), 0o755); err != nil {
		t.Fatalf("mkdir target inner/: %v", err)
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

// TestSubmoduleWalk_SubmoduleTrackedContentIsShared — files tracked inside
// the submodule's own index live ONLY in source's initialised submodule WT.
// Target's mountpoint is empty after `git checkout` (the submodule is
// uninitialised on target's side), so the walker MUST share submodule-tracked
// content with target. Silently skipping it leaves the target with almost
// nothing of the submodule.
//
// Concretely: a file tracked in the submodule's index gets a leaf-level
// action (or is reachable through a parent dir-symlink), and target's path
// resolves to the source's tracked file.
func TestSubmoduleWalk_SubmoduleTrackedContentIsShared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	subWT := filepath.Join(fx.root, "vendor", "lib")
	// Tracked-only-in-submodule file. The parent repo never sees it.
	innerDir := filepath.Join(subWT, "inner")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatalf("mkdir submodule inner/: %v", err)
	}
	writeFile(t, filepath.Join(innerDir, "tracked-only-in-sub.txt"), "SUB_TRACKED\n")
	runGit(t, subWT, "add", "inner/tracked-only-in-sub.txt")
	runGit(t, subWT, "commit", "-q", "-m", "track inner/tracked-only-in-sub.txt in submodule")

	// Target's mountpoint is empty — the submodule is uninitialised on
	// target's side. This is the realistic post-`git checkout` state.
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
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	// Target's `vendor/lib/inner/tracked-only-in-sub.txt` must resolve to a
	// readable file containing the tracked content. Whether the action is
	// emitted at the leaf or covered by a dir-symlink at `vendor/lib/inner`
	// is an implementation detail; the contract is that the file is shared.
	leafPath := filepath.Join(mountTgt, "inner", "tracked-only-in-sub.txt")
	got, err := os.ReadFile(leafPath)
	if err != nil {
		t.Fatalf("read shared submodule-tracked file at target: %v (actions=%+v)", err, res.Actions)
	}
	if string(got) != "SUB_TRACKED\n" {
		t.Fatalf("shared submodule-tracked content mismatch: got %q, want %q", string(got), "SUB_TRACKED\n")
	}

	// And the walker must NOT have silently skipped the submodule-tracked
	// content: at minimum, the parent dir or the leaf produced an action.
	leafAction := findActionOptional(res.Actions, "vendor/lib/inner/tracked-only-in-sub.txt")
	innerAction := findActionOptional(res.Actions, "vendor/lib/inner")
	if leafAction.Op == "" && innerAction.Op == "" {
		t.Fatalf("walker silently skipped submodule-tracked content: no action at vendor/lib/inner or vendor/lib/inner/tracked-only-in-sub.txt (all=%+v)", res.Actions)
	}
}

// TestSubmoduleWalk_SourceRelativeSymlinkRewrittenToSourceAbsolute — a
// relative symlink inside a source submodule that crosses into a sibling
// subtree must be rewritten to an absolute path resolving inside the SOURCE
// submodule WT. The walker rewrites verbatim-relative targets at every
// per-leaf visit because the relative path is computed against the source's
// layout — the recreated link in target may sit beside dir-symlinks rather
// than real subdirs, and the absolute form is the form that survives any
// such reshuffling.
//
// Reproducer drawn from the user's `effect/` submodule report: an untracked
// fixture file links across into a sibling subtree.
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

	writeFile(t, filepath.Join(dataDir, "x.txt"), "X\n")
	writeFile(t, filepath.Join(innerDir, "tracked.txt"), "T\n")
	runGit(t, subWT, "add", "data/x.txt", "inner/tracked.txt")
	runGit(t, subWT, "commit", "-q", "-m", "track data/x.txt and inner/tracked.txt")

	// Untracked relative symlink crossing into a sibling subtree.
	if err := os.Symlink("../data/x.txt", filepath.Join(innerDir, "foo")); err != nil {
		t.Fatalf("create relative symlink: %v", err)
	}

	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}
	// Pre-create target's `inner/` so the walker must descend into it (and
	// visit the relative symlink at the leaf) rather than anchor it as a
	// single dir-symlink.
	if err := os.MkdirAll(filepath.Join(mountTgt, "inner"), 0o755); err != nil {
		t.Fatalf("mkdir target inner/: %v", err)
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

// TestSubmoduleWalk_AnchorsTopLevelSubdirsAsSingleSymlinks — when target's
// mountpoint is empty (the realistic post-`git checkout` state) and source
// has populated subdirectories under the submodule WT, each top-level subdir
// where target's path is absent anchors as a SINGLE dir-symlink rather than
// fanning out to per-file leaves. This pins the "anchor at fully-shareable
// subtree" semantics: when target has nothing at the path, sharing the whole
// subtree via one symlink is correct (and dramatically reduces action count
// on real-world submodules with thousands of files).
func TestSubmoduleWalk_AnchorsTopLevelSubdirsAsSingleSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	subWT := filepath.Join(fx.root, "vendor", "lib")

	// Populate two top-level subdirs each with multiple tracked files.
	pkgFoo := filepath.Join(subWT, "packages", "foo")
	pkgQux := filepath.Join(subWT, "packages", "qux")
	if err := os.MkdirAll(pkgFoo, 0o755); err != nil {
		t.Fatalf("mkdir packages/foo: %v", err)
	}
	if err := os.MkdirAll(pkgQux, 0o755); err != nil {
		t.Fatalf("mkdir packages/qux: %v", err)
	}
	writeFile(t, filepath.Join(pkgFoo, "bar.ts"), "BAR\n")
	writeFile(t, filepath.Join(pkgFoo, "baz.ts"), "BAZ\n")
	writeFile(t, filepath.Join(pkgQux, "main.ts"), "MAIN\n")
	runGit(t, subWT, "add",
		"packages/foo/bar.ts",
		"packages/foo/baz.ts",
		"packages/qux/main.ts",
	)
	runGit(t, subWT, "commit", "-q", "-m", "track packages/")

	// Target's mountpoint exists but is empty (uninitialised submodule).
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
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}

	// `vendor/lib/packages` must be ONE symlink, not 3 individual file leaves.
	pkgPath := filepath.Join(mountTgt, "packages")
	pkgInfo, err := os.Lstat(pkgPath)
	if err != nil {
		t.Fatalf("lstat target packages dir: %v (actions=%+v)", err, res.Actions)
	}
	if pkgInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target's vendor/lib/packages must be a single dir-symlink, got mode=%v", pkgInfo.Mode())
	}

	// And the action set: one symlink/done at vendor/lib/packages, NO
	// separate file-leaf actions under it.
	pkgAction := findAction(t, res.Actions, "vendor/lib/packages")
	if pkgAction.Op != "symlink" || pkgAction.Status != "done" {
		t.Fatalf("expected symlink/done at vendor/lib/packages, got %+v", pkgAction)
	}
	for _, a := range res.Actions {
		if strings.HasPrefix(a.Path, "vendor/lib/packages/") {
			t.Fatalf("anchor must not fan out to per-file leaves under packages/, got %+v", a)
		}
	}

	// And the recreated symlink must point at the SOURCE submodule's
	// `<subWT>/packages` — verify content reaches through.
	got, err := os.ReadFile(filepath.Join(pkgPath, "foo", "bar.ts"))
	if err != nil {
		t.Fatalf("read packages/foo/bar.ts through dir-symlink: %v", err)
	}
	if string(got) != "BAR\n" {
		t.Fatalf("packages/foo/bar.ts content via dir-symlink = %q, want %q", string(got), "BAR\n")
	}
}

// TestSubmoduleWalk_IdempotentReapply — applying the same submodule-walk
// pattern twice (with or without --force) must be a no-op on the second
// run: the dir-symlinks anchored on the first apply must satisfy the
// applySymlink same_link check, the walker must NOT descend through them
// into the source tree, and no leaf actions or errors must surface for
// paths inside an already-anchored subtree.
func TestSubmoduleWalk_IdempotentReapply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and submodule semantics vary on Windows")
	}

	fx := setupSubmoduleFixture(t, "vendor/lib")

	subWT := filepath.Join(fx.root, "vendor", "lib")
	pkgDir := filepath.Join(subWT, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	writeFile(t, filepath.Join(pkgDir, "a.txt"), "A\n")
	writeFile(t, filepath.Join(pkgDir, "b.txt"), "B\n")
	runGit(t, subWT, "add", "pkg/a.txt", "pkg/b.txt")
	runGit(t, subWT, "commit", "-q", "-m", "track pkg/")

	mountTgt := filepath.Join(fx.wt, "vendor", "lib")
	if err := os.MkdirAll(mountTgt, 0o755); err != nil {
		t.Fatalf("mkdir target mountpoint: %v", err)
	}

	writeFile(t, filepath.Join(fx.root, testIncludeFile), "vendor/lib  symlink submodule-walk\n")

	res1, code1, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if code1 != exitcode.OK {
		t.Fatalf("first Apply exit code = %d, want %d (actions=%+v)", code1, exitcode.OK, res1.Actions)
	}

	pkgTgt := filepath.Join(mountTgt, "pkg")
	pkgInfo, err := os.Lstat(pkgTgt)
	if err != nil {
		t.Fatalf("lstat target pkg/ after first Apply: %v", err)
	}
	if pkgInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("first Apply must anchor vendor/lib/pkg as a single dir-symlink, got mode=%v", pkgInfo.Mode())
	}
	gotLink, err := os.Readlink(pkgTgt)
	if err != nil {
		t.Fatalf("readlink pkg target: %v", err)
	}
	gotLinkCanon, err := filepath.EvalSymlinks(gotLink)
	if err != nil {
		t.Fatalf("eval first-apply link target %q: %v", gotLink, err)
	}
	wantLinkCanon, err := filepath.EvalSymlinks(pkgDir)
	if err != nil {
		t.Fatalf("eval source pkg dir: %v", err)
	}
	if filepath.Clean(gotLinkCanon) != filepath.Clean(wantLinkCanon) {
		t.Fatalf("first Apply: pkg dir-symlink target mismatch: got %q want %q", gotLinkCanon, wantLinkCanon)
	}

	for _, force := range []bool{false, true} {
		t.Run("force="+map[bool]string{false: "false", true: "true"}[force], func(t *testing.T) {
			res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
				From:    "auto",
				Include: testIncludeFile,
				Force:   force,
			})
			if err != nil {
				t.Fatalf("re-Apply: %v", err)
			}
			if code != exitcode.OK {
				t.Fatalf("re-Apply exit code = %d, want %d (actions=%+v)", code, exitcode.OK, res.Actions)
			}
			if res.Summary.Errors != 0 {
				t.Fatalf("re-Apply Summary.Errors = %d, want 0 (actions=%+v)", res.Summary.Errors, res.Actions)
			}

			for _, a := range res.Actions {
				if a.Status == StatusError {
					t.Fatalf("re-Apply produced an error action: %+v (all=%+v)", a, res.Actions)
				}
			}

			for _, a := range res.Actions {
				if a.Path == "vendor/lib/pkg/a.txt" || a.Path == "vendor/lib/pkg/b.txt" {
					t.Fatalf("re-Apply must not descend into already-anchored vendor/lib/pkg/, got %+v (all=%+v)", a, res.Actions)
				}
				if strings.HasPrefix(a.Path, "vendor/lib/pkg/") {
					t.Fatalf("re-Apply must not emit any action under already-anchored vendor/lib/pkg/, got %+v", a)
				}
			}

			pkgAction := findAction(t, res.Actions, "vendor/lib/pkg")
			if pkgAction.Op != "skip" || pkgAction.Status != StatusSameLink {
				t.Fatalf("expected skip/%s at vendor/lib/pkg on re-Apply, got %+v (all=%+v)", StatusSameLink, pkgAction, res.Actions)
			}

			afterInfo, err := os.Lstat(pkgTgt)
			if err != nil {
				t.Fatalf("lstat target pkg/ after re-Apply: %v", err)
			}
			if afterInfo.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("re-Apply replaced dir-symlink with non-symlink, mode=%v", afterInfo.Mode())
			}
			gotAfter, err := os.Readlink(pkgTgt)
			if err != nil {
				t.Fatalf("readlink pkg after re-Apply: %v", err)
			}
			gotAfterCanon, err := filepath.EvalSymlinks(gotAfter)
			if err != nil {
				t.Fatalf("eval re-apply link target %q: %v", gotAfter, err)
			}
			wantAfterCanon, err := filepath.EvalSymlinks(pkgDir)
			if err != nil {
				t.Fatalf("eval pkg dir after re-Apply: %v", err)
			}
			if filepath.Clean(gotAfterCanon) != filepath.Clean(wantAfterCanon) {
				t.Fatalf("re-Apply mutated pkg dir-symlink target: got %q want %q", gotAfterCanon, wantAfterCanon)
			}

			for _, leaf := range []string{"a.txt", "b.txt"} {
				p := filepath.Join(mountTgt, "pkg", leaf)
				inner := filepath.Join(pkgTgt, leaf)
				resolved, err := filepath.EvalSymlinks(p)
				if err != nil {
					t.Fatalf("eval %s: %v", p, err)
				}
				wantResolved, err := filepath.EvalSymlinks(filepath.Join(pkgDir, leaf))
				if err != nil {
					t.Fatalf("eval source %s: %v", leaf, err)
				}
				if filepath.Clean(resolved) != filepath.Clean(wantResolved) {
					t.Fatalf("leaf %s resolved to %q, want %q", leaf, resolved, wantResolved)
				}
				innerInfo, err := os.Lstat(inner)
				if err != nil {
					t.Fatalf("lstat inner %s: %v", inner, err)
				}
				if innerInfo.Mode()&os.ModeSymlink != 0 {
					t.Fatalf("re-Apply created a per-leaf symlink at %s through the dir-symlink, mode=%v", inner, innerInfo.Mode())
				}
			}
		})
	}
}
