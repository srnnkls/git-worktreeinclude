# git-worktreeinclude

`git-worktreeinclude` safely applies ignored files listed in `.worktreeinclude` from a source worktree to the current worktree.

## Quickstart

### Install (Homebrew)

```sh
brew tap satococoa/tap
brew install satococoa/tap/git-worktreeinclude
```

### Build

```sh
go build -o git-worktreeinclude ./cmd/git-worktreeinclude
```

To use the Git extension form (`git worktreeinclude ...`), place `git-worktreeinclude` on your `PATH`.

Subcommands are explicit. Use `git-worktreeinclude apply ...`.

### Apply

```sh
git-worktreeinclude apply
```

Or via Git extension:

```sh
git worktreeinclude apply
```

## `.worktreeinclude` semantics

- Place `.worktreeinclude` at the source worktree root (by default, this is typically the main worktree selected by `--from auto`).
- Format is gitignore-compatible (`#` comments, blank lines, `!` negation, `/` anchors, `**`, etc.).
- `.worktreeinclude` may be tracked, untracked, or ignored; if the file exists in the source worktree, it is used.
- Actual sync target is the intersection of:
  - Paths matching `.worktreeinclude`
  - Paths Git classifies as ignored

Tracked files are never copied, even if listed in `.worktreeinclude`.

Example for a typical app repo with local env, editor settings, and tool-specific cache:

```gitignore
.env
.env.local
!.env.example

.vscode/settings.json
.mise.local.toml
turbo/.cache/
```

### Per-pattern attributes

Each line may carry trailing whitespace-separated attributes after the glob:

```
<glob>   [attr ...]
```

Recognized attributes:

- `copy` (default): the matched path is byte-copied into the target worktree.
- `symlink`: the target gets a symlink pointing at the absolute path of the source file in the source worktree.

If both `copy` and `symlink` appear on the same line, `symlink` wins. Unknown attributes (e.g. `binary`, `eol=lf`, `export-ignore`, `key=value`) are accepted and ignored, so this file format stays forward-compatible and can sit next to (or share content with) `.gitattributes`-style metadata.

Example:

```gitignore
# Default: copy
.env
.env.local

# Share large directories instead of duplicating per worktree
node_modules        symlink
.cache/             symlink

# Forward-compat tokens (no effect today)
package-lock.json   binary
tests/fixtures/**   export-ignore
```

Symlink mode notes:

- The link target is an absolute path to the source file. If the source worktree is later moved or removed, the link will dangle.
- Pre-existing destinations:
  - already a symlink to the same source: skipped as `same link`
  - already a symlink to a different target: conflict; `--force` replaces
  - already a regular file: conflict; `--force` replaces
  - already a directory: conflict
- A pattern resolved as `copy` will also conflict if the destination already exists as a symlink (matches symlink-mode strictness; `--force` replaces with a regular file).
- Globs cannot contain whitespace (no quoting): the first whitespace run separates pattern from attributes.
- "Any matching `symlink` pattern wins": if a path matches both a copy-mode pattern and a symlink-mode pattern, it is symlinked. Negation lines (`!foo`) are honored within the symlink set the same way they are in a regular gitignore file.

## Commands

### `git-worktreeinclude --version`

Print the installed version.

```sh
git-worktreeinclude --version
git-worktreeinclude -v
```

### `git-worktreeinclude apply`

Uses the current worktree as target and copies from source worktree.

```sh
git-worktreeinclude apply [--from auto|<path>] [--include <path>] [--dry-run] [--force] [--json] [--quiet] [--verbose]
```

- `--from`: `auto` (default) chooses the first non-bare worktree from `git worktree list --porcelain -z` (typically the main worktree)
- `--include`: include file path (default: `.worktreeinclude`)
  - relative path: resolved from source worktree root only
  - absolute path: must be inside source worktree root
- `--dry-run`: plan only, make no changes
  - use `--dry-run --verbose` when you want diagnostics about source/target selection, include file resolution, and planned actions
  - in dry-run mode, the human-readable summary uses `copy_planned=` / `symlink_planned=` instead of `copied=` / `symlinked=`, and the JSON summary uses `"copy_planned"` / `"symlink_planned"` instead of `"copied"` / `"symlinked"`
- `--force`: overwrite differing target files
- `--json`: emit a single JSON object to stdout
- `--quiet`: suppress human-readable output
- `--verbose`: print additional details

Safe defaults:

- Never touches tracked files
- Never deletes files
- Never overwrites by default (differences become conflicts, exit code `3`)
- Missing source `.worktreeinclude` is a no-op success (exit code `0`)

## JSON output

`apply --json` emits a single JSON object to stdout.

Normal execution (`apply --json`):

```json
{
  "dry_run": false,
  "from": "/abs/path/source",
  "to": "/abs/path/target",
  "include_file": ".worktreeinclude",
  "summary": {
    "matched": 12,
    "copied": 8,
    "symlinked": 1,
    "skipped_same": 3,
    "skipped_missing_src": 1,
    "conflicts": 0,
    "errors": 0
  },
  "actions": [
    {"op": "copy", "path": ".env", "status": "done"},
    {"op": "symlink", "path": "node_modules", "status": "done"},
    {"op": "skip", "path": ".mise.local.toml", "status": "same"},
    {"op": "conflict", "path": ".vscode/settings.json", "status": "diff"}
  ]
}
```

Dry-run mode (`apply --dry-run --json`):

```json
{
  "dry_run": true,
  "from": "/abs/path/source",
  "to": "/abs/path/target",
  "include_file": ".worktreeinclude",
  "summary": {
    "matched": 12,
    "copy_planned": 8,
    "symlink_planned": 1,
    "skipped_same": 3,
    "skipped_missing_src": 1,
    "conflicts": 0,
    "errors": 0
  },
  "actions": [
    {"op": "copy", "path": ".env", "status": "planned"},
    {"op": "symlink", "path": "node_modules", "status": "planned"},
    {"op": "skip", "path": ".mise.local.toml", "status": "same"},
    {"op": "conflict", "path": ".vscode/settings.json", "status": "diff"}
  ]
}
```

- `"dry_run": true` indicates no files were written
- In dry-run mode `"copy_planned"` / `"symlink_planned"` are used instead of `"copied"` / `"symlinked"` in the summary (they are mutually exclusive)
- `op` is one of `copy`, `symlink`, `skip`, `conflict`
- `status` for `skip` includes `same`, `same_link`, `missing_src`, `symlink` (source is a symlink), `error`
- `status` for `conflict` is `diff` (copy mode) or `diff_link` (symlink mode)
- `path` is repo-root relative and slash-separated
- File contents and secrets are never output

## External tool integration

Run this immediately after creating a worktree:

```sh
git worktree add <path> -b <branch>
git -C <path> worktreeinclude apply --json
```

- Evaluate success by exit code
- Use JSON `summary` and `actions` for details

## Exit codes

- `0`: success
- `1`: internal error
- `2`: argument/usage error
- `3`: conflict (`apply`) or unknown help topic
- `4`: environment/prerequisite error

## Troubleshooting

- `not inside a git repository`: run from a Git repository
- `source and target are not from the same repository`: verify `--from` points to the same repo worktree
- conflict exit: use `--force` or resolve target differences first
- no-op due to missing include: verify `.worktreeinclude` exists in the source worktree selected by `--from`
- if include exists only in target: copy that file to source worktree (or run with a different `--from`)

## Development

```sh
make fmt
make check-fmt
make vet
make lint
make test
make test-race
make ci
```

CI runs on pull requests and pushes to `main` via GitHub Actions.
`golangci-lint` is used with its default configuration (no `.golangci.yml`).
`make lint` installs a pinned `golangci-lint` binary into `.cache/bin` on first run, so the first run needs network access to fetch the tool.

## License

MIT. See [LICENSE](LICENSE).
