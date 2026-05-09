package engine

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Mode int

const (
	ModeCopy Mode = iota
	ModeSymlink
)

const (
	attrCopy          = "copy"
	attrSymlink       = "symlink"
	attrSubmoduleWalk = "submodule-walk"
)

// Pattern is one parsed line of a `.worktreeinclude` file. Glob retains any
// leading "!" so it can be written back to a gitignore-syntax temp file
// without losing negation. Attrs holds the raw trailing tokens (recognized
// or not). Mode is nil when no recognized mode attribute was present, which
// is distinct from an explicit `copy` attribute.
type Pattern struct {
	Raw           string
	Glob          string
	Negation      bool
	Mode          *Mode
	Attrs         []string
	SubmoduleWalk bool
}

func parseIncludeFile(path string) ([]Pattern, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var patterns []Pattern
	for s.Scan() {
		line := s.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		glob, attrs := splitPatternAndAttrs(trimmed)
		if glob == "" {
			continue
		}

		mode := resolveMode(attrs)
		submoduleWalk := hasSubmoduleWalk(attrs)
		if submoduleWalk && (mode == nil || *mode != ModeSymlink) {
			return nil, fmt.Errorf("submodule-walk requires symlink mode (line: %q)", line)
		}
		if submoduleWalk && !isLiteralPattern(glob) {
			return nil, fmt.Errorf("submodule-walk requires a literal pattern (line: %q)", line)
		}

		patterns = append(patterns, Pattern{
			Raw:           line,
			Glob:          glob,
			Negation:      strings.HasPrefix(glob, "!"),
			Mode:          mode,
			Attrs:         attrs,
			SubmoduleWalk: submoduleWalk,
		})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return patterns, nil
}

// splitPatternAndAttrs splits a trimmed line into a glob and trailing
// whitespace-separated attribute tokens. Quoting is not supported, so globs
// containing whitespace cannot be expressed.
func splitPatternAndAttrs(line string) (glob string, attrs []string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

// hasSubmoduleWalk reports whether the per-pattern attributes list contains
// the literal `submodule-walk` token.
func hasSubmoduleWalk(attrs []string) bool {
	for _, a := range attrs {
		if a == attrSubmoduleWalk {
			return true
		}
	}
	return false
}

// resolveMode picks the copy/symlink mode for a pattern. Unknown attribute
// tokens are ignored; if no recognized mode is present, the result is nil
// (caller treats nil as "default copy"). If both `copy` and `symlink` are
// present, `symlink` wins so users can layer `symlink` on top of a
// default-copy convention.
func resolveMode(attrs []string) *Mode {
	var out *Mode
	for _, a := range attrs {
		switch a {
		case attrSymlink:
			m := ModeSymlink
			return &m
		case attrCopy:
			m := ModeCopy
			out = &m
		}
	}
	return out
}

// writeSanitizedInclude writes a temp file containing the patterns that
// shape the overall match set: every positive (regardless of mode) and any
// negation that has no mode attribute attached. Negations carrying a mode
// attribute (e.g. `!foo symlink`) are bucket-specific and live only in
// their bucket's subset file, so they must not narrow the overall set.
// Suitable for `git ls-files -X`. Caller is responsible for removing the
// returned path.
func writeSanitizedInclude(dir string, patterns []Pattern) (string, error) {
	return writeIncludeTemp(dir, "include-all-*", patterns, func(p Pattern) bool {
		if p.Negation && p.Mode != nil {
			return false
		}
		return true
	})
}

// writeSymlinkSubsetInclude writes a temp file containing only the patterns
// that should produce symlinks. Negation lines (`!foo`) are preserved across
// the bucket so they can exclude paths from the symlink set the same way
// they would in a normal gitignore file. Returns ("", nil) if no symlink
// patterns exist.
func writeSymlinkSubsetInclude(dir string, patterns []Pattern) (string, error) {
	hasSymlink := false
	for _, p := range patterns {
		if p.Mode != nil && *p.Mode == ModeSymlink {
			hasSymlink = true
			break
		}
	}
	if !hasSymlink {
		return "", nil
	}

	return writeIncludeTemp(dir, "include-symlink-*", patterns, func(p Pattern) bool {
		return (p.Mode != nil && *p.Mode == ModeSymlink) || p.Negation
	})
}

func writeIncludeTemp(dir, pattern string, patterns []Pattern, keep func(Pattern) bool) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	w := bufio.NewWriter(f)
	for _, p := range patterns {
		if !keep(p) {
			continue
		}
		if _, err := w.WriteString(p.Glob); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return "", err
		}
		if err := w.WriteByte('\n'); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
