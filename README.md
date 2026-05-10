# git-worktreeinclude

`git-worktreeinclude` safely applies ignored files listed in `.worktreeinclude` from a source worktree to the current worktree.

## Quickstart

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

Each pattern is evaluated independently:

- **Literal paths** (no `*`, `?`, `[`) act on the named source path directly. The path may be untracked, gitignored, or even untracked-but-not-gitignored — if it exists in the source worktree, it is a candidate.
- **Globs** expand against the source worktree using `git ls-files`, so untracked-but-not-gitignored files matching the glob are still picked up.
- Negation lines (`!foo`) subtract from the matched set with the usual gitignore semantics.

For each candidate, `git-worktreeinclude` performs a per-leaf **tracked-check** as it walks the source subtree:

- A tracked source leaf is silently skipped — no action, no conflict, no error. Listing a directory whose subtree mixes tracked and untracked content (`.claude/` with committed shared rules plus untracked local agent files) is well-formed: tracked content stays untouched, untracked content is symlinked or copied.
- A tracked target leaf is a hard `conflict` with status `tracked`. `--force` does **not** override; refusing to clobber version-controlled content at the target is non-negotiable.
- For `symlink` patterns the walker anchors at the deepest fully-untracked subtree: `.claude/skills/loqui` becomes one directory symlink, `.claude/skills/effect` (tracked) is silently skipped. Where the subtree contains tracked content on either side, the walker descends and decides per-leaf.

Negation lines (`!foo`) are evaluated at candidate-resolution time and do **not** propagate into a walked subtree. Use a more specific pattern (or `copy`/`symlink` distinction) when fine-grained exclusion inside a directory matters.

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
- `submodule-walk`: only valid alongside `symlink`, and only on a literal (non-glob) pattern. Recurses into the source submodule's working tree and emits per-leaf actions inside the target's existing mountpoint instead of replacing the mountpoint with one directory-level symlink. Combining `submodule-walk` with `copy` (default or explicit) is rejected at parse time with the error `submodule-walk requires symlink mode`; pairing it with a glob is rejected with `submodule-walk requires a literal pattern`. See [Submodules](#submodules) for the full contract.

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

### Directory-level symlinks

When a `symlink`-attributed pattern resolves to a fully-untracked directory in the source worktree, `git-worktreeinclude` creates **one** symlink at the destination pointing at the source directory's absolute path. Use this to share large untracked trees (caches, build outputs, vendored deps) across worktrees without duplicating bytes.

When the same pattern resolves to a **partial-tracked** directory — tracked content on either side, untracked content beside it — the walker descends and anchors at the deepest fully-untracked subtree it can reach. Tracked source leaves are silently skipped; tracked target leaves emit `conflict/tracked`. Submodule paths (gitlinks) and source-side symlinks are handled at every depth, not only at the pattern root.

When the same pattern resolves to a directory under the default `copy` mode, the directory is always walked and each contained file is copied individually (no dir-anchor shortcut).

### Submodules

Submodules listed in `.gitmodules` (gitlink entries in the index) bypass the tracked-check, so they can be referenced from `.worktreeinclude`:

- With `symlink`: a directory-level symlink is created at the destination pointing at the source submodule's working tree. This is the default behavior and the only mode that materializes the submodule with a single action.
- With `copy` (the default when no attribute is given): the action is skipped with status `submodule_copy_unsupported`. Submodule content is not byte-copied; symlink the submodule path instead.
- With `symlink submodule-walk`: the walker shares the source submodule's entire working tree (sans `.git`) with target, emitting actions inside the target's existing mountpoint rather than anchoring a single dir-level symlink at the mountpoint. Use this when target already needs a real directory at the mountpoint (e.g. it carries local-only files alongside the submodule contents). Specifics:
  - Requires `symlink` mode. `copy submodule-walk` and bare `submodule-walk` (default-copy) are parser errors (`submodule-walk requires symlink mode`).
  - Requires a literal (non-glob) pattern. Globs like `vendor/*  symlink submodule-walk` are rejected at parse time (`submodule-walk requires a literal pattern`) because glob expansion uses `git ls-files`, which does not see content inside submodules.
  - Top-level entries inside the submodule WT anchor as single dir-symlinks where target's path is absent; recursion only happens when target already has real content at the same path. Submodule-tracked content is shared the same way as untracked content — both live only in source's initialised submodule WT, so target needs access to both.
  - Target-side tracked content at the same leaf path produces `conflict/tracked` (rare in practice — the mountpoint is typically empty after `git checkout`). Target-side tracked-checks use the parent repo's index because the mountpoint sits in the parent worktree.
  - The submodule's `.git` gitdir-pointer file at the submodule root is silently skipped — no leaf, no skip, no conflict, no error. As a consequence, `git submodule status` in the target reports the submodule as **uninitialised**, and `git submodule update --init` in the target is not expected to do anything meaningful when the mountpoint is already populated by `submodule-walk`. This is the explicit tradeoff: the target gets the submodule's content without becoming a second submodule checkout.
  - The mountpoint itself stays a real directory; it is never replaced with a symlink. Per-leaf and per-subdir symlinks land inside it.
  - Like the regular walker, `.worktreeinclude` negation lines (`!foo`) are evaluated at candidate-resolution time and do not propagate into the submodule walk.

### Source symlinks

If a matched source path is itself a symlink, it is recreated at the destination as a symlink with the same link semantics, regardless of any `copy`/`symlink` attribute on the pattern:

- Relative link targets are preserved verbatim (`.codex -> .claude` in source becomes `.codex -> .claude` in target).
- Absolute link targets pointing inside the source worktree are rewritten to the equivalent path under the target worktree, so the recreated link does not reach back into the source.
- Absolute link targets pointing outside the source worktree are preserved verbatim.

Conflict, same-link, dry-run, and `--force` semantics for recreated source-symlinks match symlink mode above.

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

- `--from`: `auto` (default) chooses the first non-bare worktree from `git worktree list --porcelain -z` other than the current (target) worktree (typically the main worktree). When the target is the only non-bare worktree, `apply` is a no-op success (exit code `0`); pass `--from <path>` explicitly to force a specific source.
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
- Single-worktree clone with `--from auto` is a no-op success (exit code `0`)

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
    "skipped_submodule_copy": 0,
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
    "skipped_submodule_copy": 0,
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
- `op` is one of `copy`, `symlink`, `skip`, `conflict`, `expand`
- `status` for `skip` includes `same`, `same_link`, `missing_src`, `submodule_copy_unsupported` (increments `skipped_submodule_copy`), `error`
- `status` for `conflict` is `diff` (copy mode), `diff_link` (symlink mode), or `tracked` (target has tracked content; `--force` does not override)
- `op:expand` with `status:walked` is a per-pattern rollup emitted only when a directory candidate triggered recursion. The action carries an `expanded` integer counting every per-leaf action emitted under that walk (including `same_link`, `done`, `conflict`, `skip`). The rollup itself does NOT increment `summary.matched` — `summary.symlinked`, `summary.copied`, and the rest count leaves only.
- `path` is repo-root relative and slash-separated
- File contents and secrets are never output

A walked partial-tracked directory looks like this in `actions`:

```json
[
  {"op": "symlink", "path": ".claude/agents", "status": "done"},
  {"op": "symlink", "path": ".claude/skills/loqui", "status": "done"},
  {"op": "expand", "path": ".claude", "status": "walked", "expanded": 2}
]
```

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
- `source and target are the same worktree`: `--from` resolved to the current worktree; pass `--from <path>` to a different worktree
- single-worktree clone: `--from auto` is a no-op success; create another worktree or pass `--from <path>` if you want an actual copy
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
