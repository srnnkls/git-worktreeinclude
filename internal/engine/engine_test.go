package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseWorktreePorcelainZ(t *testing.T) {
	data := []byte("worktree /repo/main\x00HEAD deadbeef\x00branch refs/heads/main\x00\x00worktree /repo/wt\x00HEAD cafebabe\x00branch refs/heads/feature\x00\x00")

	entries, err := parseWorktreePorcelainZ(data)
	if err != nil {
		t.Fatalf("parseWorktreePorcelainZ failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Path != "/repo/main" {
		t.Fatalf("unexpected first path: %s", entries[0].Path)
	}
	if entries[0].Bare {
		t.Fatalf("first entry should not be bare")
	}
}

func TestNormalizeRepoPath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "normal", in: ".env", want: ".env"},
		{name: "clean", in: "./foo/../bar/.env", want: "bar/.env"},
		{name: "absolute", in: "/etc/passwd", wantErr: true},
		{name: "traversal", in: "../secret", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRepoPath(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestNormalizeRepoPathBackslashBehavior(t *testing.T) {
	got, err := normalizeRepoPath(`dir\file.txt`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runtime.GOOS == "windows" {
		if got != "dir/file.txt" {
			t.Fatalf("expected slash-normalized path on Windows, got %q", got)
		}
		return
	}

	if got != `dir\file.txt` {
		t.Fatalf("expected backslash to remain a literal character on non-Windows, got %q", got)
	}
}

func TestSecureJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()

	if _, err := secureJoin(root, "../oops"); err == nil {
		t.Fatalf("expected traversal error")
	}
	got, err := secureJoin(root, "a/b.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Dir(got) != filepath.Join(root, "a") {
		t.Fatalf("unexpected joined path: %s", got)
	}
}

func TestFilesSame(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	c := filepath.Join(dir, "c.txt")

	if err := os.WriteFile(a, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(b, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	if err := os.WriteFile(c, []byte("world"), 0o644); err != nil {
		t.Fatalf("write c: %v", err)
	}

	same, err := filesSame(a, b)
	if err != nil {
		t.Fatalf("filesSame(a,b): %v", err)
	}
	if !same {
		t.Fatalf("expected same files")
	}

	same, err = filesSame(a, c)
	if err != nil {
		t.Fatalf("filesSame(a,c): %v", err)
	}
	if same {
		t.Fatalf("expected different files")
	}
}

func TestCountPatternsSupportsLongLine(t *testing.T) {
	dir := t.TempDir()
	includePath := filepath.Join(dir, ".worktreeinclude")

	longLine := strings.Repeat("a", 70*1024)
	content := "# comment\n\n" + longLine + "\n"
	if err := os.WriteFile(includePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write include file: %v", err)
	}

	count, err := countPatterns(includePath)
	if err != nil {
		t.Fatalf("countPatterns returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
}

func TestEnsurePathWithinRootBoundaries(t *testing.T) {
	root := t.TempDir()

	inside := filepath.Join(root, "sub", ".worktreeinclude")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	if err := os.WriteFile(inside, []byte(".env\n"), 0o644); err != nil {
		t.Fatalf("write inside include: %v", err)
	}

	if err := ensurePathWithinRoot(root, inside); err != nil {
		t.Fatalf("inside path should be allowed, got error: %v", err)
	}

	// Guard against naive prefix checks such as /repo vs /repo2.
	outsideSibling := root + "2"
	if err := ensurePathWithinRoot(root, outsideSibling); err == nil {
		t.Fatalf("outside sibling path should be rejected")
	}
}

func TestSplitPatternAndAttrs(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantGlob  string
		wantAttrs []string
	}{
		{name: "plain glob", in: ".env", wantGlob: ".env"},
		{name: "glob plus copy", in: ".env  copy", wantGlob: ".env", wantAttrs: []string{"copy"}},
		{name: "glob plus symlink", in: "node_modules\tsymlink", wantGlob: "node_modules", wantAttrs: []string{"symlink"}},
		{name: "multiple attrs", in: "*.tmp  symlink binary key=value", wantGlob: "*.tmp", wantAttrs: []string{"symlink", "binary", "key=value"}},
		{name: "negation", in: "!secret.env", wantGlob: "!secret.env"},
		{name: "negation with attr", in: "!secret.env  symlink", wantGlob: "!secret.env", wantAttrs: []string{"symlink"}},
		{name: "extra spaces", in: "  .env     symlink   ", wantGlob: ".env", wantAttrs: []string{"symlink"}},
		{name: "tabs between", in: ".env\t\tsymlink", wantGlob: ".env", wantAttrs: []string{"symlink"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotGlob, gotAttrs := splitPatternAndAttrs(strings.TrimSpace(tt.in))
			if gotGlob != tt.wantGlob {
				t.Fatalf("glob: want %q, got %q", tt.wantGlob, gotGlob)
			}
			if len(gotAttrs) != len(tt.wantAttrs) {
				t.Fatalf("attrs len: want %d, got %d (%v)", len(tt.wantAttrs), len(gotAttrs), gotAttrs)
			}
			for i := range gotAttrs {
				if gotAttrs[i] != tt.wantAttrs[i] {
					t.Fatalf("attrs[%d]: want %q, got %q", i, tt.wantAttrs[i], gotAttrs[i])
				}
			}
		})
	}
}

func TestResolveMode(t *testing.T) {
	tests := []struct {
		name  string
		attrs []string
		want  *Mode
	}{
		{name: "none", attrs: nil, want: nil},
		{name: "only unknowns", attrs: []string{"binary", "export-ignore", "eol=lf"}, want: nil},
		{name: "explicit copy", attrs: []string{"copy"}, want: ptrMode(ModeCopy)},
		{name: "explicit symlink", attrs: []string{"symlink"}, want: ptrMode(ModeSymlink)},
		{name: "symlink wins over copy", attrs: []string{"copy", "symlink"}, want: ptrMode(ModeSymlink)},
		{name: "symlink with unknown trailing", attrs: []string{"symlink", "binary"}, want: ptrMode(ModeSymlink)},
		{name: "copy with unknown trailing", attrs: []string{"binary", "copy", "key=val"}, want: ptrMode(ModeCopy)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMode(tt.attrs)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("nil-ness mismatch: want %v, got %v", tt.want, got)
			}
			if got != nil && *got != *tt.want {
				t.Fatalf("want %v, got %v", *tt.want, *got)
			}
		})
	}
}

func TestParseIncludeFileBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	includePath := filepath.Join(dir, ".worktreeinclude")
	if err := os.WriteFile(includePath, []byte("# legacy file\n.env\n.env.local\n!.env.example\n"), 0o644); err != nil {
		t.Fatalf("write include: %v", err)
	}

	patterns, err := parseIncludeFile(includePath)
	if err != nil {
		t.Fatalf("parseIncludeFile: %v", err)
	}
	if len(patterns) != 3 {
		t.Fatalf("expected 3 patterns, got %d", len(patterns))
	}
	for _, p := range patterns {
		if p.Mode != nil {
			t.Fatalf("expected Mode=nil for attribute-free line %q, got %v", p.Glob, *p.Mode)
		}
	}
	if patterns[2].Glob != "!.env.example" || !patterns[2].Negation {
		t.Fatalf("expected leading-! negation pattern, got %+v", patterns[2])
	}
}

func TestParseIncludeFileWithAttributes(t *testing.T) {
	dir := t.TempDir()
	includePath := filepath.Join(dir, ".worktreeinclude")
	contents := strings.Join([]string{
		"# attributes example",
		".env                copy",
		"node_modules        symlink",
		"package-lock.json   binary",
		"tests/fixtures/**   export-ignore",
		"!secret.env         symlink",
		"",
	}, "\n")
	if err := os.WriteFile(includePath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write include: %v", err)
	}

	patterns, err := parseIncludeFile(includePath)
	if err != nil {
		t.Fatalf("parseIncludeFile: %v", err)
	}
	if len(patterns) != 5 {
		t.Fatalf("expected 5 patterns, got %d", len(patterns))
	}

	if patterns[0].Mode == nil || *patterns[0].Mode != ModeCopy {
		t.Fatalf("expected explicit copy on .env, got %v", patterns[0].Mode)
	}
	if patterns[1].Mode == nil || *patterns[1].Mode != ModeSymlink {
		t.Fatalf("expected symlink on node_modules, got %v", patterns[1].Mode)
	}
	if patterns[2].Mode != nil {
		t.Fatalf("unknown attr should leave Mode=nil, got %v", *patterns[2].Mode)
	}
	if patterns[3].Mode != nil {
		t.Fatalf("unknown attr should leave Mode=nil, got %v", *patterns[3].Mode)
	}
	if patterns[4].Mode == nil || *patterns[4].Mode != ModeSymlink || !patterns[4].Negation {
		t.Fatalf("expected !secret.env to be symlink-bucket negation, got %+v", patterns[4])
	}
}

func ptrMode(m Mode) *Mode { return &m }

func TestEnsurePathWithinRootRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()

	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	escaped := filepath.Join(link, ".worktreeinclude")
	if err := os.WriteFile(filepath.Join(outside, ".worktreeinclude"), []byte(".env\n"), 0o644); err != nil {
		t.Fatalf("write outside include: %v", err)
	}

	if err := ensurePathWithinRoot(root, escaped); err == nil {
		t.Fatalf("symlink escape path should be rejected")
	}
}
