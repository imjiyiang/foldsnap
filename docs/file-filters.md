# File selection policies

`Plan.fileFilters` is the default file policy. An absent/null `Source.fileFilters` inherits it; a present object, including `{}`, replaces the complete policy for that source. Ordered source path rules still precede ordered plan path rules. Existing plans without these fields keep their existing behavior. Policies persist inside the existing JSON plan aggregate; no SQL schema changes are needed.

Fields:

| Field | Semantics |
| --- | --- |
| minBytes / maxBytes | Optional nonnegative byte boundaries, inclusive; minimum cannot exceed maximum |
| modifiedWithinDays | Optional integer 1–36500, inclusive lower bound relative to one UTC evaluation instant; future timestamps pass this lower bound |
| types | Empty/absent = all; otherwise OR of image, video, audio, document, archive, code, other |
| exceptions | Exact normalized source-relative paths with action include/exclude; duplicates, traversal and directory targets are rejected |

Size, age and type conditions combine with AND for regular files. Types are inferred from case-insensitive filename extensions, not content/MIME inspection. `other` includes unrecognized/no extensions. Directories preserve the selected structure; symlinks obey path rules/exceptions and never follow their targets. An exact single-file exception overrides hidden-file exclusion, path rules and file conditions. Including a file below an excluded parent opens only the necessary ancestor directories, not the surrounding subtree. Service metadata exclusions and the no-follow boundary always take precedence.

The Go selector is shared by full scans, metadata signatures, native event scope and mirror protection. Events use path scope rather than current file conditions so changes entering/leaving a size or date condition are not lost. Conditions are re-evaluated for each preview/run; native idle change detection also re-evaluates relative time windows in its existing hourly content audit. Time alone does not generate an FSEvent. A manual/interval backup evaluates at its actual start.

Rsync receives the exact selected regular-file/symlink NUL manifest. Directory structure is created explicitly, so an included ancestor cannot cause recursive copying of excluded siblings. Full SHA-256 verification and exact output-tree verification remain mandatory before publishing.

## Mirror safety

A file excluded by a new size/date/type/path policy is protected in mirror mode. Existing live metadata is used to distinguish a file that grew out of scope from an actual deletion; for a missing file, the old manifest metadata determines its policy scope. Old parent directories required by retained children are retained too. Nondeleting incremental mode retains missing paths as before; snapshot mode includes the current selected set and leaves previous history recoverable.

Preview approval fingerprints cover the selected entries, plan, baseline and computed operations/removals. The executor additionally checks a digest of the entire expected view before transfer. A file crossing a dynamic condition after scanning must not introduce an unreviewed removal, even when another removal was explicitly authorized. A changed view aborts and requires a new preview. The evaluation instant is frozen across each run's scan, dry-run, view construction and execution.

## Rule test UI

The editor exposes plan conditions, source override/inheritance, file categories and exact exceptions. The photo preset selects the image category. Test results show selected and ignored entries with reasons, source names, logical capacity and evaluation time; search and 100-row pages expose all returned results. Excluded directories are represented by their skipped parent, without enumerating a subtree that was deliberately not read. Individual ignored entries are no longer silently capped at 2,000. Editing a plan clears stale results; an in-flight result is discarded if its draft changed.

Tests cover inclusive boundaries, aged files, policy overrides, malformed policies, forced inclusion through excluded/hidden parents, exclusion of adjacent files, no-follow/runtime protection, live-metadata mirror retention, retained directory closure, unreviewed view drift, actual immutable publication, API persistence/preview and change-detection consistency. Hardware failure/performance acceptance remains tracked separately in implementation-status.md.

Local browser/API acceptance used generated data only: 109 files were selected, 5 excluded for separate reasons, source override inherited/replaced the correct policy, and the final immutable manifest exactly matched selection. Both pages of 112 selected file/directory records were reachable. Editing the maximum size cleared the old result; cancelling left the saved 4 KiB condition unchanged. The sample plan is paused. Screenshots: `.data/file-filters-editor.png`, `.data/file-filters-results.png`.
