package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/satococoa/git-worktreeinclude/internal/exitcode"
)

type engineFixture struct {
	root string
	wt   string
}

const testIncludeFile = ".test.worktreeinclude"

func TestEngineApplyCopiesIgnoredFiles(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Copied != 2 {
		t.Fatalf("expected 2 copied files, got %+v", res.Summary)
	}

	gotEnv, err := os.ReadFile(filepath.Join(fx.wt, ".env"))
	if err != nil {
		t.Fatalf("read copied .env: %v", err)
	}
	if string(gotEnv) != "SOURCE_ENV\n" {
		t.Fatalf("unexpected .env content: %q", gotEnv)
	}
}

func TestEngineApplyConflictAndForce(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	writeFile(t, filepath.Join(fx.wt, ".env.local"), "TARGET_LOCAL\n")
	_, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.Conflict)
	}

	gotConflict, err := os.ReadFile(filepath.Join(fx.wt, ".env.local"))
	if err != nil {
		t.Fatalf("read conflict target .env.local: %v", err)
	}
	if string(gotConflict) != "TARGET_LOCAL\n" {
		t.Fatalf("target should remain unchanged on conflict, got %q", gotConflict)
	}

	_, code, err = e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Apply --force returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply --force exit code = %d, want %d", code, exitcode.OK)
	}

	gotForced, err := os.ReadFile(filepath.Join(fx.wt, ".env.local"))
	if err != nil {
		t.Fatalf("read forced .env.local: %v", err)
	}
	if string(gotForced) != "SOURCE_LOCAL\n" {
		t.Fatalf("target should be overwritten with --force, got %q", gotForced)
	}
}

func TestEngineApplySkipsSameContent(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	writeFile(t, filepath.Join(fx.wt, ".env"), "SOURCE_ENV\n")

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.SkippedSame != 1 {
		t.Fatalf("expected one skipped-same file, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Status != "same" || action.Op != "skip" {
		t.Fatalf("expected .env to be skipped as same, got %+v", action)
	}
	if res.Summary.Copied != 1 {
		t.Fatalf("expected only remaining missing file to be copied, got %+v", res.Summary)
	}
}

func TestEngineApplyRecreatesRelativeSourceSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	e := NewEngine()

	if err := os.Remove(filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("remove source .env: %v", err)
	}
	if err := os.Symlink(".env.local", filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("create relative source symlink: %v", err)
	}

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Symlinked != 1 {
		t.Fatalf("expected 1 symlinked, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for .env, got %+v", action)
	}
	dst := filepath.Join(fx.wt, ".env")
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat recreated .env: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".env should be a symlink, got mode=%v", info.Mode())
	}
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != ".env.local" {
		t.Fatalf("expected verbatim relative target %q, got %q", ".env.local", got)
	}
}

func TestEngineApplyRewritesAbsoluteInSourceSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	e := NewEngine()

	if err := os.Remove(filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("remove source .env: %v", err)
	}
	absInside := filepath.Join(fx.root, ".env.local")
	if err := os.Symlink(absInside, filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("create absolute-inside-source symlink: %v", err)
	}

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Symlinked != 1 {
		t.Fatalf("expected 1 symlinked, got %+v", res.Summary)
	}
	dst := filepath.Join(fx.wt, ".env")
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected rewritten absolute target, got %q", got)
	}
	// Link must resolve into the target worktree's .env.local, not the
	// source worktree's, so the worktree stays self-contained.
	if !sameFile(t, dst, filepath.Join(fx.wt, ".env.local")) {
		t.Fatalf("recreated link should resolve to target's .env.local, readlink=%q", got)
	}
	if sameFileWeak(t, got, absInside) {
		t.Fatalf("recreated link should not point back into source, readlink=%q", got)
	}
}

// sameFileWeak compares two raw paths textually after Clean, without
// dereferencing them. Used when one of the paths may not exist.
func sameFileWeak(t *testing.T, a, b string) bool {
	t.Helper()
	return filepath.Clean(a) == filepath.Clean(b)
}

func TestEngineApplyPreservesAbsoluteOutOfSourceSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	e := NewEngine()

	external := filepath.Join(t.TempDir(), "elsewhere.env")
	writeFile(t, external, "EXTERNAL\n")
	if err := os.Remove(filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("remove source .env: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("create absolute-outside-source symlink: %v", err)
	}

	_, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	got, err := os.Readlink(filepath.Join(fx.wt, ".env"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != external {
		t.Fatalf("expected verbatim external target %q, got %q", external, got)
	}
}

func TestEngineApplyRecreatesSymlinkDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	if err := os.Remove(filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("remove source .env: %v", err)
	}
	if err := os.Symlink(".env.local", filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Apply dry-run: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply dry-run exit code = %d", code)
	}
	if res.Summary.SymlinkPlanned != 1 || res.Summary.Symlinked != 0 {
		t.Fatalf("expected SymlinkPlanned=1 and Symlinked=0, got %+v", res.Summary)
	}
	if _, err := os.Lstat(filepath.Join(fx.wt, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run should not create .env, err=%v", err)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "symlink" || action.Status != "planned" {
		t.Fatalf("expected symlink/planned for .env, got %+v", action)
	}
}

func TestEngineApplyRecreatesSymlinkSameSkip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	if err := os.Remove(filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("remove source .env: %v", err)
	}
	if err := os.Symlink(".env.local", filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}
	if err := os.Symlink(".env.local", filepath.Join(fx.wt, ".env")); err != nil {
		t.Fatalf("create matching dst symlink: %v", err)
	}

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d", code)
	}
	if res.Summary.SkippedSame != 1 {
		t.Fatalf("expected SkippedSame=1, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "skip" || action.Status != "same_link" {
		t.Fatalf("expected skip/same_link for .env, got %+v", action)
	}
}

func TestEngineApplyRecreatesSymlinkConflictAndForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	if err := os.Remove(filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("remove source .env: %v", err)
	}
	if err := os.Symlink(".env.local", filepath.Join(fx.root, ".env")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}
	writeFile(t, filepath.Join(fx.wt, ".env"), "PRE_EXISTING\n")

	_, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("expected conflict exit code, got %d", code)
	}

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply --force exit code = %d", code)
	}
	if res.Summary.Symlinked != 1 {
		t.Fatalf("expected force to replace with symlink, got %+v", res.Summary)
	}
	info, err := os.Lstat(filepath.Join(fx.wt, ".env"))
	if err != nil {
		t.Fatalf("lstat after force: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".env should be a symlink after --force, got mode=%v", info.Mode())
	}
	got, err := os.Readlink(filepath.Join(fx.wt, ".env"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != ".env.local" {
		t.Fatalf("expected verbatim relative target, got %q", got)
	}
}

func TestEngineApplyTreatsTargetDirectoryAsConflict(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	targetPath := filepath.Join(fx.wt, ".env")
	if err := os.Mkdir(targetPath, 0o755); err != nil {
		t.Fatalf("mkdir target path: %v", err)
	}

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.Conflict)
	}
	if res.Summary.Conflicts != 1 {
		t.Fatalf("expected one conflict for target directory, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Status != "diff" || action.Op != "conflict" {
		t.Fatalf("expected directory target to register conflict, got %+v", action)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat target directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("target directory should remain intact")
	}
}

func TestEngineApplyIncludeValidationAndNoop(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: ".missing-worktreeinclude",
	})
	if err != nil {
		t.Fatalf("Apply with missing include returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply with missing include exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Matched != 0 || res.Summary.Copied != 0 || len(res.Actions) != 0 {
		t.Fatalf("expected missing include no-op, got %+v", res.Summary)
	}

	outside := filepath.Join(filepath.Dir(fx.root), "outside.include")
	writeFile(t, outside, ".env\n")
	_, code, err = e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: outside,
	})
	if err == nil {
		t.Fatalf("expected error for include outside repository")
	}
	if code != exitcode.Env {
		t.Fatalf("Apply include outside exit code = %d, want %d", code, exitcode.Env)
	}
	if !strings.Contains(err.Error(), "include path must be inside source repository root") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEngineApplyUsesSourceIncludeWhenTargetIncludeMissing(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	if err := os.Remove(filepath.Join(fx.wt, testIncludeFile)); err != nil {
		t.Fatalf("remove target include: %v", err)
	}

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Copied != 2 {
		t.Fatalf("expected 2 copied files, got %+v", res.Summary)
	}
}

func TestEngineApplyNoopWhenSourceIncludeMissingEvenIfTargetHasInclude(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	if err := os.Remove(filepath.Join(fx.root, testIncludeFile)); err != nil {
		t.Fatalf("remove source include: %v", err)
	}
	writeFile(t, filepath.Join(fx.wt, testIncludeFile), ".env\n")

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Matched != 0 || res.Summary.Copied != 0 || len(res.Actions) != 0 {
		t.Fatalf("expected source-missing include no-op, got %+v", res.Summary)
	}
	if res.IncludeFound {
		t.Fatalf("expected include to be missing")
	}
	if res.IncludeMissingHint != IncludeMissingHintSourceMissingTargetExists {
		t.Fatalf("unexpected include hint: %q", res.IncludeMissingHint)
	}
}

func TestEngineApplyReadsIncludeFileIgnoredByGlobalExcludes(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	globalIgnore := filepath.Join(t.TempDir(), "global_ignore")
	writeFile(t, globalIgnore, ".global.worktreeinclude\n")
	runGit(t, fx.root, "config", "core.excludesFile", globalIgnore)

	writeFile(t, filepath.Join(fx.root, ".global.worktreeinclude"), ".env\n")

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: ".global.worktreeinclude",
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.Summary.Copied == 0 {
		t.Fatalf("expected ignored include file to be read, got %+v", res.Summary)
	}
}

func TestEngineApplyHintsWhenTargetIncludeIsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	e := NewEngine()

	if err := os.Remove(filepath.Join(fx.root, testIncludeFile)); err != nil {
		t.Fatalf("remove source include: %v", err)
	}
	if err := os.Remove(filepath.Join(fx.wt, testIncludeFile)); err != nil {
		t.Fatalf("remove target include: %v", err)
	}

	brokenTarget := filepath.Join(filepath.Dir(fx.wt), "missing.include")
	if err := os.Symlink(brokenTarget, filepath.Join(fx.wt, testIncludeFile)); err != nil {
		t.Fatalf("create target symlink include: %v", err)
	}

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if res.IncludeMissingHint != IncludeMissingHintSourceMissingTargetExists {
		t.Fatalf("expected target-only include hint, got %q", res.IncludeMissingHint)
	}
}

func TestEngineApplyDryRunIncludesMetadata(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d, want %d", code, exitcode.OK)
	}
	if !res.IncludeFound {
		t.Fatalf("expected include file to be found")
	}
	if res.PatternCount != 2 {
		t.Fatalf("unexpected pattern count: got %d want 2", res.PatternCount)
	}
}

func TestEngineApplyDryRunCopyPlanned(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	res, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Apply dry-run returned error: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply dry-run exit code = %d, want %d", code, exitcode.OK)
	}
	if !res.DryRun {
		t.Fatalf("expected DryRun=true in result")
	}
	if res.Summary.CopyPlanned == 0 {
		t.Fatalf("expected CopyPlanned > 0 in dry-run summary, got %+v", res.Summary)
	}
	if res.Summary.Copied != 0 {
		t.Fatalf("expected Copied=0 in dry-run summary, got %+v", res.Summary)
	}

	for _, a := range res.Actions {
		if a.Op == "copy" && a.Status != "planned" {
			t.Fatalf("expected copy actions to have status=planned in dry-run, got %+v", a)
		}
	}
}

func TestEngineApplySymlinksMarkedPattern(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".env  symlink\n.env.local\n")

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
	if res.Summary.Symlinked != 1 || res.Summary.Copied != 1 {
		t.Fatalf("expected 1 symlink + 1 copy, got %+v", res.Summary)
	}

	envLink := filepath.Join(fx.wt, ".env")
	info, err := os.Lstat(envLink)
	if err != nil {
		t.Fatalf("lstat .env: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected .env to be a symlink, got mode=%v", info.Mode())
	}
	if !sameFile(t, envLink, filepath.Join(fx.root, ".env")) {
		got, _ := os.Readlink(envLink)
		t.Fatalf("symlink target %q does not resolve to source .env", got)
	}

	envLocal := filepath.Join(fx.wt, ".env.local")
	localInfo, err := os.Lstat(envLocal)
	if err != nil {
		t.Fatalf("lstat .env.local: %v", err)
	}
	if localInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf(".env.local should be a regular file copy, got mode=%v", localInfo.Mode())
	}

	if action := findAction(t, res.Actions, ".env"); action.Op != "symlink" || action.Status != "done" {
		t.Fatalf("expected symlink/done for .env, got %+v", action)
	}
	if action := findAction(t, res.Actions, ".env.local"); action.Op != "copy" || action.Status != "done" {
		t.Fatalf("expected copy/done for .env.local, got %+v", action)
	}
}

func TestEngineApplySymlinkDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".env  symlink\n.env.local\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Apply dry-run: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply dry-run exit code = %d", code)
	}
	if res.Summary.SymlinkPlanned != 1 || res.Summary.CopyPlanned != 1 {
		t.Fatalf("expected planned counts, got %+v", res.Summary)
	}
	if res.Summary.Symlinked != 0 {
		t.Fatalf("Symlinked should be zero in dry-run, got %+v", res.Summary)
	}
	if _, err := os.Lstat(filepath.Join(fx.wt, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run should not create .env, err=%v", err)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "symlink" || action.Status != "planned" {
		t.Fatalf("expected symlink/planned for .env, got %+v", action)
	}
}

func TestEngineApplySymlinkSameLinkSkip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".env  symlink\n.env.local\n")

	if err := os.Symlink(filepath.Join(fx.root, ".env"), filepath.Join(fx.wt, ".env")); err != nil {
		t.Fatalf("create existing symlink: %v", err)
	}

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d", code)
	}
	if res.Summary.SkippedSame != 1 {
		t.Fatalf("expected SkippedSame=1, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "skip" || action.Status != "same_link" {
		t.Fatalf("expected skip/same_link for .env, got %+v", action)
	}
}

func TestEngineApplySymlinkConflictWithRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".env  symlink\n")
	writeFile(t, filepath.Join(fx.wt, ".env"), "PRE_EXISTING\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("expected conflict exit code, got %d", code)
	}
	if res.Summary.Conflicts != 1 {
		t.Fatalf("expected one conflict, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "conflict" || action.Status != "diff_link" {
		t.Fatalf("expected conflict/diff_link for .env, got %+v", action)
	}

	res, code, err = NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply --force exit code = %d", code)
	}
	if res.Summary.Symlinked != 1 {
		t.Fatalf("expected force to replace with symlink, got %+v", res.Summary)
	}
	info, err := os.Lstat(filepath.Join(fx.wt, ".env"))
	if err != nil {
		t.Fatalf("lstat after force: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".env should be a symlink after --force, got mode=%v", info.Mode())
	}
}

func TestEngineApplySymlinkConflictWithDifferentTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".env  symlink\n")

	other := filepath.Join(t.TempDir(), "other.env")
	writeFile(t, other, "OTHER\n")
	if err := os.Symlink(other, filepath.Join(fx.wt, ".env")); err != nil {
		t.Fatalf("create wrong symlink: %v", err)
	}

	_, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("expected conflict exit code, got %d", code)
	}

	_, code, err = NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply --force exit code = %d", code)
	}
	if !sameFile(t, filepath.Join(fx.wt, ".env"), filepath.Join(fx.root, ".env")) {
		got, _ := os.Readlink(filepath.Join(fx.wt, ".env"))
		t.Fatalf("symlink target %q does not resolve to source .env", got)
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	infoA, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	infoB, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(infoA, infoB)
}

func TestEngineApplyUnknownAttributesIgnored(t *testing.T) {
	fx := setupEngineFixture(t)
	writeFile(t, filepath.Join(fx.root, testIncludeFile), ".env  binary export-ignore\n.env.local\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d", code)
	}
	if res.Summary.Copied != 2 || res.Summary.Symlinked != 0 {
		t.Fatalf("unknown attrs should not change copy semantics, got %+v", res.Summary)
	}
}

func TestEngineApplyNegationInsideSymlinkBucket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	// Both .env and .env.local match `*.env*`, but .env.local is negated
	// from the symlink bucket, so it must fall back to copy.
	writeFile(t, filepath.Join(fx.root, testIncludeFile), "*.env*  symlink\n!.env.local  symlink\n")

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("Apply exit code = %d", code)
	}
	if res.Summary.Symlinked != 1 || res.Summary.Copied != 1 {
		t.Fatalf("expected one symlink + one copy, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "symlink" {
		t.Fatalf("expected .env to be symlinked, got %+v", action)
	}
	if action := findAction(t, res.Actions, ".env.local"); action.Op != "copy" {
		t.Fatalf("expected .env.local to be copied (negated from symlink bucket), got %+v", action)
	}
}

func TestEngineApplyCopyModeConflictsWithExistingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior and permissions vary on Windows")
	}

	fx := setupEngineFixture(t)
	other := filepath.Join(t.TempDir(), "elsewhere.env")
	writeFile(t, other, "ELSEWHERE\n")
	if err := os.Symlink(other, filepath.Join(fx.wt, ".env")); err != nil {
		t.Fatalf("create existing symlink: %v", err)
	}

	res, code, err := NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.Conflict {
		t.Fatalf("copy-mode against existing symlink should conflict, got code=%d summary=%+v", code, res.Summary)
	}
	if res.Summary.Conflicts != 1 {
		t.Fatalf("expected one conflict, got %+v", res.Summary)
	}
	if action := findAction(t, res.Actions, ".env"); action.Op != "conflict" {
		t.Fatalf("expected conflict on .env, got %+v", action)
	}

	_, code, err = NewEngine().Apply(context.Background(), fx.wt, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("force should resolve, got code=%d", code)
	}
	info, err := os.Lstat(filepath.Join(fx.wt, ".env"))
	if err != nil {
		t.Fatalf("lstat after force: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected .env to be a regular file copy after --force, got mode=%v", info.Mode())
	}
}

// TestEngineApplyAutoIsNoopInSingleWorktreeRepo pins the no-op short-circuit
// for `--from auto` in a clone whose only non-bare worktree is the target.
// See TestSingleWorktree_* in engine_singleworktree_test.go for the full
// behavior contract.
func TestEngineApplyAutoIsNoopInSingleWorktreeRepo(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "branch", "-M", "main")
	writeFile(t, filepath.Join(repo, ".gitignore"), ".env\n")
	writeFile(t, filepath.Join(repo, testIncludeFile), ".env\n")
	runGit(t, repo, "add", ".gitignore", testIncludeFile)
	runGit(t, repo, "commit", "-q", "-m", "init")
	writeFile(t, filepath.Join(repo, ".env"), "X\n")

	res, code, err := NewEngine().Apply(context.Background(), repo, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
	}
	if res.IncludeMissingHint != IncludeMissingHintSingleWorktree {
		t.Fatalf("IncludeMissingHint = %q, want %q", res.IncludeMissingHint, IncludeMissingHintSingleWorktree)
	}
}

func TestEngineApplyAutoSkipsTargetWhenSecondaryIsTarget(t *testing.T) {
	fx := setupEngineFixture(t)

	_, code, err := NewEngine().Apply(context.Background(), fx.root, ApplyOptions{
		From:    "auto",
		Include: testIncludeFile,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d (auto should pick fx.wt over fx.root)", code, exitcode.OK)
	}
}

func TestEngineApplyExplicitFromEqualsTarget(t *testing.T) {
	fx := setupEngineFixture(t)
	e := NewEngine()

	_, code, err := e.Apply(context.Background(), fx.wt, ApplyOptions{
		From:    fx.wt,
		Include: testIncludeFile,
	})
	if err == nil {
		t.Fatalf("expected error when --from points at target worktree, got none (code=%d)", code)
	}
	if code != exitcode.Env {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Env)
	}
}

func TestErrorCodeFromCLIError(t *testing.T) {
	err := &CLIError{Code: exitcode.Env, Msg: "x"}
	if got := errorCode(err); got != exitcode.Env {
		t.Fatalf("errorCode(CLIError) = %d, want %d", got, exitcode.Env)
	}
	if got := errorCode(errors.New("plain")); got != exitcode.Internal {
		t.Fatalf("errorCode(plain) = %d, want %d", got, exitcode.Internal)
	}
}

func setupEngineFixture(t *testing.T) engineFixture {
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
	// .worktreeinclude lists only ignored paths so the regression suite can
	// assert copy/symlink behaviors in isolation. Tracked-file refusal lives
	// in TestSafety_TrackedSourceFileRefused.
	writeFile(t, filepath.Join(repo, testIncludeFile), ".env\n.env.local\n")
	runGit(t, repo, "add", "README.md", ".gitignore", testIncludeFile)
	runGit(t, repo, "commit", "-q", "-m", "init")

	writeFile(t, filepath.Join(repo, ".env"), "SOURCE_ENV\n")
	writeFile(t, filepath.Join(repo, ".env.local"), "SOURCE_LOCAL\n")

	wt := filepath.Join(base, "wt")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "feature")

	return engineFixture{root: repo, wt: wt}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func findAction(t *testing.T, actions []Action, path string) Action {
	t.Helper()
	for _, action := range actions {
		if action.Path == path {
			return action
		}
	}
	t.Fatalf("action for %s not found", path)
	return Action{}
}
