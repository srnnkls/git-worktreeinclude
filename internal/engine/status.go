package engine

// Action.Status string constants. These are part of the JSON contract emitted
// to stdout when `apply --json` is used, so they are exported for callers and
// tests that need to assert against them.
const (
	// StatusDone marks a copy/symlink action that completed.
	StatusDone = "done"
	// StatusPlanned marks a copy/symlink action recorded under --dry-run.
	StatusPlanned = "planned"
	// StatusError marks a skipped action whose underlying I/O failed.
	StatusError = "error"

	// StatusSame is a copy-mode skip: destination already byte-equal to source.
	StatusSame = "same"
	// StatusSameLink is a symlink-mode skip: destination already points at source.
	StatusSameLink = "same_link"
	// StatusMissingSrc is a skip: source path does not exist.
	StatusMissingSrc = "missing_src"

	// StatusDiff is a copy-mode conflict: destination differs from source.
	StatusDiff = "diff"
	// StatusDiffLink is a symlink-mode conflict: existing destination is not the
	// expected symlink.
	StatusDiffLink = "diff_link"

	// StatusTracked is a conflict raised when source or destination has any
	// tracked content under the candidate path. `--force` does NOT override.
	StatusTracked = "tracked"
	// StatusSubmoduleCopyUnsupported is a skip raised when a `.gitmodules`
	// gitlink would be copied; submodules can only be symlinked.
	StatusSubmoduleCopyUnsupported = "submodule_copy_unsupported"
)
