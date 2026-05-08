package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/satococoa/git-worktreeinclude/internal/exitcode"
	"github.com/satococoa/git-worktreeinclude/internal/gitexec"
)

type Action struct {
	Op     string `json:"op"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

type Summary struct {
	Matched              int `json:"matched"`
	Copied               int `json:"copied,omitempty"`
	CopyPlanned          int `json:"copy_planned,omitempty"`
	Symlinked            int `json:"symlinked,omitempty"`
	SymlinkPlanned       int `json:"symlink_planned,omitempty"`
	SkippedSame          int `json:"skipped_same"`
	SkippedMissingSrc    int `json:"skipped_missing_src"`
	SkippedSubmoduleCopy int `json:"skipped_submodule_copy,omitempty"`
	Conflicts            int `json:"conflicts"`
	Errors               int `json:"errors"`
}

type Result struct {
	DryRun      bool     `json:"dry_run"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	IncludeFile string   `json:"include_file"`
	Summary     Summary  `json:"summary"`
	Actions     []Action `json:"actions"`

	// Non-JSON metadata for human-readable CLI output.
	ResolvedIncludePath string `json:"-"`
	IncludeFound        bool   `json:"-"`
	IncludeOrigin       string `json:"-"`
	IncludeMissingHint  string `json:"-"`
	TargetIncludePath   string `json:"-"`
	PatternCount        int    `json:"-"`
}

type ApplyOptions struct {
	From    string
	Include string
	DryRun  bool
	Force   bool
}

type Engine struct {
	git *gitexec.Runner
}

type CLIError struct {
	Code int
	Msg  string
	Err  error
}

func (e *CLIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Msg
	}
	if e.Msg == "" {
		return e.Err.Error()
	}
	return e.Msg + ": " + e.Err.Error()
}

func (e *CLIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewEngine() *Engine {
	return &Engine{git: gitexec.NewRunner()}
}

type prepared struct {
	targetRoot         string
	sourceRoot         string
	fromMode           string
	includeArg         string
	includePath        string
	includeFound       bool
	includeOrigin      string
	includeMissingHint string
	targetIncludePath  string
	patternCount       int
	candidates         []candidate
	submodulePaths     map[string]struct{}
}

// candidate is one resolved entry produced by `prepare`. Each candidate maps
// to exactly one user-visible action (copy, symlink, conflict, or skip).
type candidate struct {
	rel     string
	mode    Mode
	literal bool // came from a literal (non-glob) pattern; preserved for dir-expansion semantics
	missing bool // literal pattern whose source path did not exist at expansion time
}

const (
	IncludeOriginSource   = "source"
	IncludeOriginExplicit = "explicit"

	IncludeMissingHintSourceMissing             = "source_missing"
	IncludeMissingHintSourceMissingTargetExists = "source_missing_target_exists"
	// IncludeMissingHintSingleWorktree marks the no-op short-circuit taken
	// when `--from auto` runs in a clone with only one non-bare worktree.
	// Surfaced so post-checkout hooks can call `apply` unconditionally and
	// downstream tooling can distinguish this from a true include miss.
	IncludeMissingHintSingleWorktree = "single_worktree"
)

func (e *Engine) Apply(ctx context.Context, cwd string, opts ApplyOptions) (Result, int, error) {
	prep, err := e.prepare(ctx, cwd, opts.From, opts.Include)
	if err != nil {
		return Result{}, errorCode(err), err
	}

	result, code := e.executePrepared(ctx, prep, opts.DryRun, opts.Force)
	return result, code, nil
}

func (e *Engine) executePrepared(ctx context.Context, prep prepared, dryRun, force bool) (Result, int) {
	result := Result{
		DryRun:              dryRun,
		From:                prep.sourceRoot,
		To:                  prep.targetRoot,
		IncludeFile:         prep.includeArg,
		ResolvedIncludePath: prep.includePath,
		IncludeFound:        prep.includeFound,
		IncludeOrigin:       prep.includeOrigin,
		IncludeMissingHint:  prep.includeMissingHint,
		TargetIncludePath:   prep.targetIncludePath,
		PatternCount:        prep.patternCount,
		Summary:             Summary{},
		Actions:             make([]Action, 0, len(prep.candidates)),
	}

	if !prep.includeFound {
		return result, exitcode.OK
	}

	matched := 0
	trackedConflicts := 0
	for _, c := range prep.candidates {
		n, tc := e.executeCandidate(ctx, &result, prep, c, dryRun, force)
		matched += n
		trackedConflicts += tc
	}
	result.Summary.Matched = matched

	if result.Summary.Errors > 0 {
		return result, exitcode.Internal
	}
	// Tracked conflicts are not bypassable by `--force` (refusing to clobber
	// version-controlled content is the whole point).
	if trackedConflicts > 0 {
		return result, exitcode.Conflict
	}
	if result.Summary.Conflicts > 0 && !force {
		return result, exitcode.Conflict
	}
	return result, exitcode.OK
}

// executeCandidate processes a single resolved candidate, applying the
// per-action tracked-check before any copy/symlink work. Returns the number
// of "matched" units (a dir-copy expanded into N files contributes N) and
// the number of tracked-status conflicts emitted (which `--force` cannot
// override).
func (e *Engine) executeCandidate(ctx context.Context, result *Result, prep prepared, c candidate, dryRun, force bool) (int, int) {
	srcPath, err := secureJoin(prep.sourceRoot, c.rel)
	if err != nil {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusError})
		result.Summary.Errors++
		return 1, 0
	}
	dstPath, err := secureJoin(prep.targetRoot, c.rel)
	if err != nil {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusError})
		result.Summary.Errors++
		return 1, 0
	}

	if c.missing {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusMissingSrc})
		result.Summary.SkippedMissingSrc++
		return 1, 0
	}

	srcInfo, err := os.Lstat(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusMissingSrc})
			result.Summary.SkippedMissingSrc++
			return 1, 0
		}
		result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusError})
		result.Summary.Errors++
		return 1, 0
	}

	_, isSubmodule := prep.submodulePaths[c.rel]

	// Submodule with default-copy mode is unsupported; emit a clear skip and
	// move on without touching the destination.
	if isSubmodule && c.mode == ModeCopy {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusSubmoduleCopyUnsupported})
		result.Summary.SkippedSubmoduleCopy++
		return 1, 0
	}

	// Per-action tracked-check. Submodule gitlinks legitimately appear in the
	// index of both worktrees, so a known submodule path bypasses the check.
	// Everything else must be untracked on both sides; `--force` does NOT
	// override.
	if !isSubmodule {
		if e.isPathTracked(ctx, prep.sourceRoot, c.rel) || e.isSubtreeTracked(ctx, prep.sourceRoot, c.rel) {
			result.Actions = append(result.Actions, Action{Op: "conflict", Path: c.rel, Status: StatusTracked})
			result.Summary.Conflicts++
			return 1, 1
		}
		if e.isPathTracked(ctx, prep.targetRoot, c.rel) || e.isSubtreeTracked(ctx, prep.targetRoot, c.rel) {
			result.Actions = append(result.Actions, Action{Op: "conflict", Path: c.rel, Status: StatusTracked})
			result.Summary.Conflicts++
			return 1, 1
		}
	}

	// Source-side symlink: recreate a symlink at destination regardless of
	// the pattern's attribute (matches existing source-symlink semantics).
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		linkTarget, err := os.Readlink(srcPath)
		if err != nil {
			result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusError})
			result.Summary.Errors++
			return 1, 0
		}
		linkTarget = rewriteAbsoluteLinkTarget(linkTarget, prep.sourceRoot, prep.targetRoot)
		applySymlink(result, prep.targetRoot, c.rel, linkTarget, dstPath, dryRun, force)
		return 1, 0
	}

	// Submodule (gitlink) or directory with `symlink` attribute: emit ONE
	// directory-level symlink, no recursion.
	if (isSubmodule || srcInfo.IsDir()) && c.mode == ModeSymlink {
		applySymlink(result, prep.targetRoot, c.rel, srcPath, dstPath, dryRun, force)
		return 1, 0
	}

	if srcInfo.IsDir() {
		// copy-mode directory: walk the source dir and emit per-file copy
		// candidates. Negations within the dir are not supported in literal
		// mode — users wanting fine-grained exclusion should reach for globs.
		return e.expandDirCopy(ctx, result, prep, c.rel, srcPath, dryRun, force), 0
	}

	if !srcInfo.Mode().IsRegular() {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: c.rel, Status: StatusMissingSrc})
		result.Summary.SkippedMissingSrc++
		return 1, 0
	}

	if c.mode == ModeSymlink {
		applySymlink(result, prep.targetRoot, c.rel, srcPath, dstPath, dryRun, force)
	} else {
		applyCopy(result, prep.targetRoot, c.rel, srcPath, dstPath, srcInfo.Mode().Perm(), dryRun, force)
	}
	return 1, 0
}

// expandDirCopy walks `srcDir` (a dir candidate in copy mode) and emits a
// per-file copy action for each regular file it contains. Tracked files
// inside the dir are skipped silently — the parent candidate already failed
// the subtree-tracked check earlier, so reaching this point implies nothing
// below is tracked, but we re-verify per file to be defensive.
func (e *Engine) expandDirCopy(ctx context.Context, result *Result, prep prepared, rel, srcDir string, dryRun, force bool) int {
	matched := 0
	walkErr := filepath.Walk(srcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relFile, relErr := filepath.Rel(prep.sourceRoot, p)
		if relErr != nil {
			return relErr
		}
		relFile = filepath.ToSlash(relFile)
		if e.isPathTracked(ctx, prep.sourceRoot, relFile) {
			return nil
		}
		dstPath, err := secureJoin(prep.targetRoot, relFile)
		if err != nil {
			result.Actions = append(result.Actions, Action{Op: "skip", Path: relFile, Status: StatusError})
			result.Summary.Errors++
			matched++
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, lerr := os.Readlink(p)
			if lerr != nil {
				result.Actions = append(result.Actions, Action{Op: "skip", Path: relFile, Status: StatusError})
				result.Summary.Errors++
				matched++
				return nil
			}
			linkTarget = rewriteAbsoluteLinkTarget(linkTarget, prep.sourceRoot, prep.targetRoot)
			applySymlink(result, prep.targetRoot, relFile, linkTarget, dstPath, dryRun, force)
			matched++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		applyCopy(result, prep.targetRoot, relFile, p, dstPath, info.Mode().Perm(), dryRun, force)
		matched++
		return nil
	})
	if walkErr != nil {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: rel, Status: StatusError})
		result.Summary.Errors++
		if matched == 0 {
			matched = 1
		}
	}
	return matched
}

func applyCopy(result *Result, targetRoot, rel, srcPath, dstPath string, perm os.FileMode, dryRun, force bool) {
	dstInfo, err := os.Lstat(dstPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: rel, Status: StatusError})
		result.Summary.Errors++
		return
	}

	if errors.Is(err, os.ErrNotExist) {
		recordCopy(result, targetRoot, rel, srcPath, dstPath, perm, dryRun)
		return
	}

	if dstInfo.IsDir() {
		result.Actions = append(result.Actions, Action{Op: "conflict", Path: rel, Status: StatusDiff})
		result.Summary.Conflicts++
		return
	}

	if dstInfo.Mode()&os.ModeSymlink != 0 {
		// Existing symlink at the destination is treated as a conflict in copy
		// mode so behavior matches symlink mode and so we don't silently
		// resolve through the link.
		if !force {
			result.Actions = append(result.Actions, Action{Op: "conflict", Path: rel, Status: StatusDiff})
			result.Summary.Conflicts++
			return
		}
		recordCopy(result, targetRoot, rel, srcPath, dstPath, perm, dryRun)
		return
	}

	same, err := filesSame(srcPath, dstPath)
	if err != nil {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: rel, Status: StatusError})
		result.Summary.Errors++
		return
	}
	if same {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: rel, Status: StatusSame})
		result.Summary.SkippedSame++
		return
	}

	if !force {
		result.Actions = append(result.Actions, Action{Op: "conflict", Path: rel, Status: StatusDiff})
		result.Summary.Conflicts++
		return
	}

	recordCopy(result, targetRoot, rel, srcPath, dstPath, perm, dryRun)
}

func recordCopy(result *Result, targetRoot, rel, srcPath, dstPath string, perm os.FileMode, dryRun bool) {
	status := StatusPlanned
	if !dryRun {
		if err := copyFileAtomic(targetRoot, srcPath, dstPath, perm); err != nil {
			result.Actions = append(result.Actions, Action{Op: "copy", Path: rel, Status: StatusError})
			result.Summary.Errors++
			return
		}
		status = StatusDone
	}
	result.Actions = append(result.Actions, Action{Op: "copy", Path: rel, Status: status})
	if dryRun {
		result.Summary.CopyPlanned++
	} else {
		result.Summary.Copied++
	}
}

func applySymlink(result *Result, targetRoot, rel, linkTarget, dstPath string, dryRun, force bool) {
	dstInfo, err := os.Lstat(dstPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		result.Actions = append(result.Actions, Action{Op: "skip", Path: rel, Status: StatusError})
		result.Summary.Errors++
		return
	}

	if errors.Is(err, os.ErrNotExist) {
		recordSymlink(result, targetRoot, rel, linkTarget, dstPath, dryRun)
		return
	}

	if dstInfo.IsDir() {
		result.Actions = append(result.Actions, Action{Op: "conflict", Path: rel, Status: StatusDiffLink})
		result.Summary.Conflicts++
		return
	}

	if dstInfo.Mode()&os.ModeSymlink != 0 {
		same, err := symlinkPointsTo(dstPath, linkTarget)
		if err != nil {
			result.Actions = append(result.Actions, Action{Op: "skip", Path: rel, Status: StatusError})
			result.Summary.Errors++
			return
		}
		if same {
			result.Actions = append(result.Actions, Action{Op: "skip", Path: rel, Status: StatusSameLink})
			result.Summary.SkippedSame++
			return
		}
		if !force {
			result.Actions = append(result.Actions, Action{Op: "conflict", Path: rel, Status: StatusDiffLink})
			result.Summary.Conflicts++
			return
		}
		recordSymlink(result, targetRoot, rel, linkTarget, dstPath, dryRun)
		return
	}

	if !force {
		result.Actions = append(result.Actions, Action{Op: "conflict", Path: rel, Status: StatusDiffLink})
		result.Summary.Conflicts++
		return
	}
	recordSymlink(result, targetRoot, rel, linkTarget, dstPath, dryRun)
}

func recordSymlink(result *Result, targetRoot, rel, linkTarget, dstPath string, dryRun bool) {
	status := StatusPlanned
	if !dryRun {
		if err := symlinkAtomic(targetRoot, linkTarget, dstPath); err != nil {
			result.Actions = append(result.Actions, Action{Op: "symlink", Path: rel, Status: StatusError})
			result.Summary.Errors++
			return
		}
		status = StatusDone
	}
	result.Actions = append(result.Actions, Action{Op: "symlink", Path: rel, Status: status})
	if dryRun {
		result.Summary.SymlinkPlanned++
	} else {
		result.Summary.Symlinked++
	}
}

func (e *Engine) prepare(ctx context.Context, cwd, fromOpt, includeOpt string) (prepared, error) {
	targetRoot, err := e.repoRoot(ctx, cwd)
	if err != nil {
		return prepared{}, err
	}

	includeArg := includeOpt
	if includeArg == "" {
		includeArg = ".worktreeinclude"
	}

	fromMode := fromOpt
	if fromMode == "" {
		fromMode = "auto"
	}

	sourceRoot, sourceFound, err := e.resolveSourceRoot(ctx, targetRoot, cwd, fromMode)
	if err != nil {
		return prepared{}, err
	}

	// `--from auto` in a clone with only one non-bare worktree has no source
	// to copy from. Short-circuit to a no-op success so post-checkout hooks
	// can run `apply` unconditionally without a worktree-count guard.
	if !sourceFound {
		return prepared{
			targetRoot:         targetRoot,
			fromMode:           fromMode,
			includeArg:         includeArg,
			includeMissingHint: IncludeMissingHintSingleWorktree,
		}, nil
	}

	if err := e.assertSameRepository(ctx, targetRoot, sourceRoot); err != nil {
		return prepared{}, err
	}

	prep := prepared{
		targetRoot: targetRoot,
		sourceRoot: sourceRoot,
		fromMode:   fromMode,
		includeArg: includeArg,
	}

	includePath := includeArg
	if !filepath.IsAbs(includePath) {
		includePath = filepath.Join(sourceRoot, includePath)
		prep.includeOrigin = IncludeOriginSource
		prep.targetIncludePath = filepath.Clean(filepath.Join(targetRoot, includeArg))
	} else {
		prep.includeOrigin = IncludeOriginExplicit
	}

	includePath = filepath.Clean(includePath)
	if err := ensurePathWithinRoot(sourceRoot, includePath); err != nil {
		return prepared{}, &CLIError{Code: exitcode.Env, Msg: "include path must be inside source repository root", Err: err}
	}
	prep.includePath = includePath

	info, err := os.Stat(includePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			prep.includeMissingHint = IncludeMissingHintSourceMissing
			if prep.includeOrigin == IncludeOriginSource && prep.targetIncludePath != "" {
				if targetInfo, targetErr := os.Lstat(prep.targetIncludePath); targetErr == nil && !targetInfo.IsDir() {
					prep.includeMissingHint = IncludeMissingHintSourceMissingTargetExists
				}
			}
			return prep, nil
		}
		return prepared{}, &CLIError{Code: exitcode.Env, Msg: "failed to read include file", Err: err}
	}
	if info.IsDir() {
		return prepared{}, &CLIError{Code: exitcode.Env, Msg: "include path is a directory", Err: nil}
	}

	patterns, err := parseIncludeFile(includePath)
	if err != nil {
		return prepared{}, &CLIError{Code: exitcode.Env, Msg: "failed to parse include file", Err: err}
	}
	prep.patternCount = len(patterns)
	prep.includeFound = true

	submodules, err := e.discoverSubmodules(ctx, sourceRoot)
	if err != nil {
		return prepared{}, err
	}
	prep.submodulePaths = submodules

	candidates, err := e.expandCandidates(ctx, sourceRoot, patterns)
	if err != nil {
		return prepared{}, err
	}
	prep.candidates = candidates
	return prep, nil
}

// expandCandidates resolves parsed patterns into a list of candidates.
//
// Literal-path patterns lstat directly so tracked paths are visible (and can
// be flagged as conflicts later). Glob expansion uses the legacy two-pass
// model — full match set + symlink subset — so bucket-specific negations
// (e.g. `!foo symlink`) keep their bucket-narrowing semantics: a path
// excluded only from the symlink bucket falls back to copy.
func (e *Engine) expandCandidates(ctx context.Context, sourceRoot string, patterns []Pattern) ([]candidate, error) {
	tmpDir, err := os.MkdirTemp("", "git-worktreeinclude-include-")
	if err != nil {
		return nil, &CLIError{Code: exitcode.Env, Msg: "failed to create include scratch dir", Err: err}
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	// Per-rel state: first-seen mode wins; later positive symlink patterns
	// upgrade to symlink, matching legacy "any symlink wins" semantics.
	type acc struct {
		mode    Mode
		literal bool
		missing bool
	}
	resolved := make(map[string]*acc)
	order := make([]string, 0)

	add := func(rel string, mode Mode, literal, missing bool) {
		if existing, ok := resolved[rel]; ok {
			if mode == ModeSymlink {
				existing.mode = ModeSymlink
			}
			if literal {
				existing.literal = true
			}
			// A later non-missing match clears the missing flag — the path
			// does exist via at least one pattern.
			if !missing {
				existing.missing = false
			}
			return
		}
		resolved[rel] = &acc{mode: mode, literal: literal, missing: missing}
		order = append(order, rel)
	}
	remove := func(rel string) {
		if _, ok := resolved[rel]; ok {
			delete(resolved, rel)
			for i, p := range order {
				if p == rel {
					order = append(order[:i], order[i+1:]...)
					break
				}
			}
		}
	}

	// Pass 1: literal-path positives. lstat directly so tracked paths surface
	// for the per-action tracked-check.
	for _, p := range patterns {
		if p.Negation || !isLiteralPattern(p.Glob) {
			continue
		}
		mode := ModeCopy
		if p.Mode != nil {
			mode = *p.Mode
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(p.Glob, "/"), "/")
		abs, err := secureJoin(sourceRoot, rel)
		if err != nil {
			continue
		}
		if _, statErr := os.Lstat(abs); statErr != nil {
			// Surface a non-existent literal path so executeCandidate can
			// emit skip/missing_src; silently dropping it would hide the
			// configuration error.
			if errors.Is(statErr, os.ErrNotExist) {
				add(rel, mode, true, true)
			}
			continue
		}
		add(rel, mode, true, false)
	}

	// Pass 2: glob expansion (legacy two-pass).
	hasGlob := false
	for _, p := range patterns {
		if !p.Negation && !isLiteralPattern(p.Glob) {
			hasGlob = true
			break
		}
	}
	if hasGlob {
		matchPath, err := writeSanitizedInclude(tmpDir, patterns)
		if err != nil {
			return nil, &CLIError{Code: exitcode.Env, Msg: "failed to write sanitized include file", Err: err}
		}
		matches, err := e.listOthersWithExclude(ctx, sourceRoot, matchPath)
		if err != nil {
			return nil, err
		}

		symlinkSet := make(map[string]struct{})
		symlinkPath, err := writeSymlinkSubsetInclude(tmpDir, patterns)
		if err != nil {
			return nil, &CLIError{Code: exitcode.Env, Msg: "failed to write symlink-subset include file", Err: err}
		}
		if symlinkPath != "" {
			symMatches, err := e.listOthersWithExclude(ctx, sourceRoot, symlinkPath)
			if err != nil {
				return nil, err
			}
			for _, p := range symMatches {
				symlinkSet[p] = struct{}{}
			}
		}

		for _, rel := range matches {
			mode := ModeCopy
			if _, ok := symlinkSet[rel]; ok {
				mode = ModeSymlink
			}
			add(rel, mode, false, false)
		}
	}

	// Apply literal negations to literal candidates. Glob candidates already
	// had negations folded in via the legacy two-pass include files.
	for _, p := range patterns {
		if !p.Negation {
			continue
		}
		raw := strings.TrimPrefix(p.Glob, "!")
		if !isLiteralPattern(raw) {
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(raw, "/"), "/")
		if a, ok := resolved[rel]; ok && a.literal {
			remove(rel)
		}
	}

	out := make([]candidate, 0, len(order))
	for _, rel := range order {
		a := resolved[rel]
		out = append(out, candidate{rel: rel, mode: a.mode, literal: a.literal, missing: a.missing})
	}
	return out, nil
}

// listOthersWithExclude runs `git ls-files -o -i -X <subset>` to expand a
// glob pattern. `-i -X subset` makes git treat the subset file as the only
// gitignore rules in play and emit files matching them. Critically, we do
// NOT pass `--exclude-standard` — this is what allows untracked-not-ignored
// paths to surface when their pattern matches.
func (e *Engine) listOthersWithExclude(ctx context.Context, repoRoot, includePath string) ([]string, error) {
	args := []string{"ls-files", "-o", "-i", "-z"}
	if includePath != "" {
		args = append(args, "-X", includePath)
	}
	out, err := e.git.Run(ctx, repoRoot, args...)
	if err != nil {
		return nil, &CLIError{Code: exitcode.Env, Msg: "failed to apply include patterns", Err: err}
	}
	paths, err := parseNULPaths(out)
	if err != nil {
		return nil, &CLIError{Code: exitcode.Env, Msg: "failed to parse ls-files output", Err: err}
	}
	return paths, nil
}

// discoverSubmodules returns the set of submodule paths recorded in
// `.gitmodules`, normalized to slash-separated repo-root-relative form. An
// absent or unreadable `.gitmodules` is treated as "no submodules".
func (e *Engine) discoverSubmodules(ctx context.Context, repoRoot string) (map[string]struct{}, error) {
	gitmodules := filepath.Join(repoRoot, ".gitmodules")
	if _, err := os.Stat(gitmodules); err != nil {
		return map[string]struct{}{}, nil
	}
	out, err := e.git.RunText(ctx, repoRoot, "config", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		// `git config --get-regexp` exits 1 when nothing matches — not an error.
		return map[string]struct{}{}, nil
	}
	paths := make(map[string]struct{})
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "submodule.<name>.path<TAB-or-SPACE><value>"
		idx := strings.IndexAny(line, " \t")
		if idx < 0 {
			continue
		}
		raw := strings.TrimSpace(line[idx+1:])
		norm, normErr := normalizeRepoPath(raw)
		if normErr != nil {
			continue
		}
		// Verify the index entry mode is 160000 (gitlink) so we don't honour
		// stale .gitmodules entries that no longer exist in the index.
		stOut, stErr := e.git.RunText(ctx, repoRoot, "ls-files", "-s", "--", norm)
		if stErr != nil || !strings.HasPrefix(stOut, "160000 ") {
			continue
		}
		paths[norm] = struct{}{}
	}
	return paths, nil
}

// isPathTracked reports whether `rel` is itself tracked in the index.
func (e *Engine) isPathTracked(ctx context.Context, repoRoot, rel string) bool {
	_, err := e.git.Run(ctx, repoRoot, "ls-files", "--error-unmatch", "-z", "--", rel)
	return err == nil
}

// isSubtreeTracked reports whether any tracked path lives at-or-below `rel`.
func (e *Engine) isSubtreeTracked(ctx context.Context, repoRoot, rel string) bool {
	out, err := e.git.Run(ctx, repoRoot, "ls-files", "-z", "--", rel)
	if err == nil && len(bytes.TrimRight(out, "\x00")) > 0 {
		return true
	}
	out, err = e.git.Run(ctx, repoRoot, "ls-files", "-z", "--", rel+"/")
	if err == nil && len(bytes.TrimRight(out, "\x00")) > 0 {
		return true
	}
	return false
}

// isLiteralPattern returns true when `glob` contains no glob metacharacters,
// so the user almost certainly means a single concrete path.
func isLiteralPattern(glob string) bool {
	g := strings.TrimPrefix(glob, "!")
	return !strings.ContainsAny(g, "*?[")
}

func (e *Engine) repoRoot(ctx context.Context, cwd string) (string, error) {
	root, err := e.git.RunText(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", &CLIError{Code: exitcode.Env, Msg: "not inside a git repository", Err: err}
	}
	return root, nil
}

// resolveSourceRoot returns the source worktree root, a "found" flag, and
// any hard error. The flag is false (with a nil error) only for `--from auto`
// in a clone with no other non-bare worktree — that case is a no-op success
// at the caller, not an environment error.
func (e *Engine) resolveSourceRoot(ctx context.Context, targetRoot, cwd, from string) (string, bool, error) {
	if from == "auto" {
		out, err := e.git.Run(ctx, targetRoot, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return "", false, &CLIError{Code: exitcode.Env, Msg: "failed to list worktrees", Err: err}
		}
		worktrees, err := parseWorktreePorcelainZ(out)
		if err != nil {
			return "", false, &CLIError{Code: exitcode.Env, Msg: "failed to parse worktree list", Err: err}
		}
		targetCanon := canonicalPath(targetRoot)
		for _, wt := range worktrees {
			if wt.Bare || wt.Path == "" {
				continue
			}
			candidate := filepath.Clean(wt.Path)
			if canonicalPath(candidate) == targetCanon {
				continue
			}
			return candidate, true, nil
		}
		return "", false, nil
	}

	sourcePath := from
	if !filepath.IsAbs(sourcePath) {
		sourcePath = filepath.Join(cwd, sourcePath)
	}
	sourcePath = filepath.Clean(sourcePath)

	sourceRoot, err := e.repoRoot(ctx, sourcePath)
	if err != nil {
		return "", false, &CLIError{Code: exitcode.Env, Msg: "invalid --from path", Err: err}
	}
	return sourceRoot, true, nil
}

func (e *Engine) assertSameRepository(ctx context.Context, targetRoot, sourceRoot string) error {
	targetCommon, err := e.git.RunText(ctx, targetRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return &CLIError{Code: exitcode.Env, Msg: "failed to resolve target git common dir", Err: err}
	}
	sourceCommon, err := e.git.RunText(ctx, sourceRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return &CLIError{Code: exitcode.Env, Msg: "failed to resolve source git common dir", Err: err}
	}

	if filepath.Clean(targetCommon) != filepath.Clean(sourceCommon) {
		return &CLIError{Code: exitcode.Env, Msg: "source and target are not from the same repository", Err: nil}
	}
	if canonicalPath(targetRoot) == canonicalPath(sourceRoot) {
		return &CLIError{Code: exitcode.Env, Msg: "source and target are the same worktree; pass --from <path> to a different worktree", Err: nil}
	}
	return nil
}

func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func countPatterns(includePath string) (int, error) {
	patterns, err := parseIncludeFile(includePath)
	if err != nil {
		return 0, err
	}
	return len(patterns), nil
}

type worktreeEntry struct {
	Path string
	Bare bool
}

func parseWorktreePorcelainZ(out []byte) ([]worktreeEntry, error) {
	parts := bytes.Split(out, []byte{0})
	entries := make([]worktreeEntry, 0)
	current := worktreeEntry{}
	hasCurrent := false

	for _, raw := range parts {
		if len(raw) == 0 {
			if hasCurrent {
				entries = append(entries, current)
				current = worktreeEntry{}
				hasCurrent = false
			}
			continue
		}

		hasCurrent = true
		line := string(raw)
		switch {
		case strings.HasPrefix(line, "worktree "):
			current.Path = strings.TrimPrefix(line, "worktree ")
		case line == "bare":
			current.Bare = true
		}
	}

	if hasCurrent {
		entries = append(entries, current)
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("worktree list is empty")
	}
	return entries, nil
}

func parseNULPaths(out []byte) ([]string, error) {
	parts := bytes.Split(out, []byte{0})
	seen := make(map[string]struct{}, len(parts))
	paths := make([]string, 0, len(parts))
	for _, raw := range parts {
		if len(raw) == 0 {
			continue
		}
		norm, err := normalizeRepoPath(string(raw))
		if err != nil {
			return nil, err
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		paths = append(paths, norm)
	}
	return paths, nil
}

func normalizeRepoPath(raw string) (string, error) {
	if strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("path contains NUL")
	}
	rel := raw
	if os.PathSeparator == '\\' {
		rel = strings.ReplaceAll(rel, "\\", "/")
	}
	rel = path.Clean(rel)
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("path is empty")
	}
	if strings.HasPrefix(rel, "/") || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("unsafe relative path: %s", raw)
	}
	return rel, nil
}

func secureJoin(root, rel string) (string, error) {
	norm, err := normalizeRepoPath(rel)
	if err != nil {
		return "", err
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, filepath.FromSlash(norm))
	joined, err = filepath.Abs(joined)
	if err != nil {
		return "", err
	}

	if joined != absRoot && !strings.HasPrefix(joined, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes repository root: %s", rel)
	}
	return joined, nil
}

func filesSame(srcPath, dstPath string) (bool, error) {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return false, err
	}
	dstInfo, err := os.Stat(dstPath)
	if err != nil {
		return false, err
	}

	if !srcInfo.Mode().IsRegular() || !dstInfo.Mode().IsRegular() {
		return false, nil
	}
	if srcInfo.Size() != dstInfo.Size() {
		return false, nil
	}

	srcHash, err := hashFile(srcPath)
	if err != nil {
		return false, err
	}
	dstHash, err := hashFile(dstPath)
	if err != nil {
		return false, err
	}

	return bytes.Equal(srcHash[:], dstHash[:]), nil
}

func hashFile(filePath string) ([32]byte, error) {
	var zero [32]byte
	f, err := os.Open(filePath)
	if err != nil {
		return zero, err
	}
	defer func() {
		_ = f.Close()
	}()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return zero, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func copyFileAtomic(targetRoot, srcPath, dstPath string, perm os.FileMode) error {
	parent := filepath.Dir(dstPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}

	if err := ensurePathWithinRoot(targetRoot, parent); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(parent, ".git-worktreeinclude-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	src, err := os.Open(srcPath)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}

	if _, err := io.Copy(tmp, src); err != nil {
		_ = src.Close()
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := src.Close(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}

	if err := os.Rename(tmpName, dstPath); err != nil {
		cleanup()
		return err
	}

	return nil
}

func symlinkAtomic(targetRoot, linkTarget, dstPath string) error {
	parent := filepath.Dir(dstPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if err := ensurePathWithinRoot(targetRoot, parent); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(parent, ".git-worktreeinclude-link-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// CreateTemp leaves a file at tmpName; remove it so Symlink can create
	// a link in its place.
	if err := os.Remove(tmpName); err != nil {
		return err
	}
	if err := os.Symlink(linkTarget, tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func symlinkPointsTo(dstPath, wantLinkTarget string) (bool, error) {
	raw, err := os.Readlink(dstPath)
	if err != nil {
		return false, err
	}
	if filepath.Clean(raw) == filepath.Clean(wantLinkTarget) {
		return true, nil
	}

	wantAbs := wantLinkTarget
	if !filepath.IsAbs(wantAbs) {
		wantAbs = filepath.Join(filepath.Dir(dstPath), wantAbs)
	}
	gotResolved, gotErr := filepath.EvalSymlinks(dstPath)
	wantResolved, wantErr := filepath.EvalSymlinks(wantAbs)
	if gotErr == nil && wantErr == nil {
		return filepath.Clean(gotResolved) == filepath.Clean(wantResolved), nil
	}
	return false, nil
}

// rewriteAbsoluteLinkTarget rewrites absolute link targets that lie inside
// the source root to the equivalent path under the target root, so a
// recreated link does not reach back into the source worktree. Relative
// targets and absolute targets pointing outside the source root are
// returned unchanged. Both sides are canonicalized (e.g. /var vs
// /private/var on macOS) before comparison.
func rewriteAbsoluteLinkTarget(linkTarget, sourceRoot, targetRoot string) string {
	if !filepath.IsAbs(linkTarget) {
		return linkTarget
	}
	canonSource := canonicalPath(sourceRoot)
	canonTarget := canonicalPath(linkTarget)
	rel, err := filepath.Rel(canonSource, canonTarget)
	if err != nil {
		return linkTarget
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return linkTarget
	}
	return filepath.Join(targetRoot, rel)
}

func ensurePathWithinRoot(root, candidate string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candAbs, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}

	rootCanonical := rootAbs
	if realRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootCanonical = realRoot
	}
	candCanonical := candAbs
	if realCand, err := filepath.EvalSymlinks(candAbs); err == nil {
		candCanonical = realCand
	}

	rel, err := filepath.Rel(rootCanonical, candCanonical)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path escapes repository root")
	}

	if rel == "" || rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, "."+string(os.PathSeparator)) {
		return nil
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes repository root")
	}
	return nil
}

func errorCode(err error) int {
	var coded *CLIError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return exitcode.Internal
}
