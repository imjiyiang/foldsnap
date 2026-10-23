# LocalBackup 0.1 acceptance specification

- A plan SHALL support multiple stable-ID sources and independent volume-bound destinations.
- Creation SHALL reject non-absolute/missing paths, overlapping sources/mappings, cross-plan source/target cycles and changed volume identities.
- Filtering SHALL be identical between scanning and transfer. An excluded subtree SHALL never be recursively copied.
- Preview SHALL enumerate logical additions, changes, verified move candidates and removals; removals SHALL preserve history.
- The executor SHALL write only to new staging trees, verify every selected content hash, durably write a manifest and atomically publish only a complete source set.
- No application operation SHALL mutate committed historical file contents or shared inode metadata.
- A cancelled or failed run SHALL not become successful; restart SHALL expose unfinished runs as recoverable.
- Restore SHALL use a newly created output directory outside sources/repositories and verify file contents.
- Retention SHALL require explicit candidate confirmation, reject stale/noneligible IDs and preserve latest/pinned snapshots.
- All API endpoints SHALL require a random local bearer token and validate Host/Origin. The server SHALL listen only on a loopback address.
- Scheduling SHALL debounce polling changes and re-evaluate missed intervals when service execution resumes. No wall-clock guarantee SHALL be made for sleeping/offline machines.
- Features deferred in docs/roadmap.md SHALL be disclosed rather than simulated.

## File selection and dynamic scope
- Plan-level file conditions support inclusive byte ranges, relative modification windows and extension-based file categories. Each source can inherit or replace that complete policy.
- Exact single-file include/exclude exceptions override path, hidden-file and file conditions, while preserving service-directory exclusions and no-follow safety.
- An included file under an excluded parent requires explicit ancestor directory entries and must not copy excluded siblings.
- Scan, dry-run, execution, metadata fallback and native event scope use compatible selection semantics; native events cannot be discarded solely because a file currently fails a dynamic condition.
- A file filtered out by size/time/type must not become a mirror deletion. Retained children require retained ancestor directories.
- Preview approval binds computed operations and the expected view; scope drift cannot add an unreviewed removal after approval.
- Rule-test results expose all selected/ignored records with reasons, search/pagination and an evaluation timestamp, and invalidate on draft edits.

## Persistent queue and interruption control
- Run creation, preview consumption and durable FIFO enqueue share one transaction. Duplicate triggers for a pending plan coalesce.
- At most one backup/restore/retention/recovery file writer executes at a time; paused active runs retain that slot.
- Queued holds and offline waiting must not prevent other ready backups from running.
- Pause requests are distinguishable from acknowledged pauses. Cooperative work gates and isolated rsync process groups must support pause, resume and cancellation without publishing partial data.
- Queued/held jobs survive restart; interrupted active writers become recoverable and never auto-publish.
- Expired previews and queued plan changes require renewed review before source scanning or target writes.

## Physical file index and fingerprint reuse
- Capture source volume/file identity, nanosecond mtime/ctime/birthtime, permissions and attribute digest; ctime is never treated as creation time.
- Persist physical content versions separately from source path observations. Cache keys must distinguish volume changes and file-ID reuse.
- Use persistent hash reuse only on trusted filesystem identities with sufficient metadata precision; otherwise perform full reads.
- Read actual regular files with descriptor/version validation; reject symlinks, FIFOs and changing file versions as hash inputs.
- Complete generations replace path observations atomically. Cancellation and older overlapping scans cannot overwrite a newer completed generation, even after wall-clock rollback.
- Unsaved rule tests cannot replace the authoritative saved-source observation policy.
- Journal-gap/startup/hourly audits and explicit complete scans bypass the cache. Target/restore verification cannot use source-cache hashes as proof without reading actual content.
- Index writes must allow WAL readers, and waiting to persist a pause must not prevent cancelling the active job.

- Persisted source fingerprint values require integrity tags; invalid/unsigned records must be re-read. Cache-key loss must not make historical backups unreadable.
- Moved-file reuse must validate actual current source content before linking a previous generation, independently of cached values.

## Manual source-scoped runs (0.9)

Authenticated manual preview/run requests may select enabled source IDs within the saved plan. Unknown/disabled/duplicate IDs are rejected. Selecting every configured source normalizes to full scope only when all configured sources are enabled; an explicit scope preserves previous data from disabled sources. Scope is included in approval grants, run metadata and manifest update metadata. The queue persists and validates the complete saved configuration before applying selection to an execution copy. Distinct scopes must remain distinct FIFO jobs; repeated matching scopes may coalesce. A scoped generation updates selected sources and retains previously backed-up data from unselected sources for each target. Every retained object is verified before publication. The first generation cannot claim to contain unselected, never-backed-up data. Mirror scope never authorizes unreviewed removals. Source-scoped success must not acknowledge whole-plan change events or advance the whole-plan interval baseline. Later full-plan runs must restore ordinary selection.

## Source identity and rebinding (0.10)

A saved source binds its volume UUID, directory ID and creation time. Existing source IDs cannot silently acquire a different root through clearing/forging fields. Source-root symlinks and replacement roots are rejected. A renamed original directory may be explicitly rebound while preserving its ID/history. External source mount-name changes may resolve by UUID plus the unchanged mount-relative location and directory identity. Missing sources wait durably for the original identity; run/queue waiting state commits atomically. Legacy unbound sources require explicit plan save before preview/execution, never automatic first-time binding. Resolve-time source/source and source/target overlap checks include all saved plan targets. Source binding is rechecked before/after scanning and before transfer/publication. Rebinding invalidates old queue configuration/preview as usual.

## Smart scheduling (0.11)

Change-trigger decisions use net intended-view differences, coalesced by source/path across enabled destinations. Counts must not use event counts. Enabled count and logical-size thresholds both apply after debounce and minimum time since full-plan success. Below-threshold changes and dirty revisions persist; unchanged observations do not repeatedly full-scan. A maximum interval performs a full content read, generates no recovery point when unchanged, and bypasses change thresholds when changed. Partial source/target success cannot reset full-plan interval clocks. Failed automatic tasks retry with persisted original destination scope, 1/2/4… minute exponential backoff capped at one hour and 1—10 total attempts. Waiting, manual failures, cancellation and confirmation are not failure retries. Confirmation gates must prevent auto loops in every mode. Settings changes must validate both ordinary and offline-save paths. A read-only status endpoint reports real/unknown observations and decisions without initiating work.

## First-run guidance (0.12)

Authenticated read-only setup checks report actual engine/platform, source identity/enumeration access and bound-target write-access preflight. They must not start runs, read source file contents, create target test files or mutate bindings/indexes. New UI plans start with automatic triggering paused; saving a paused new plan opens a real preview and requires the user to start backup. Existing enablement is preserved. Guides remain reopenable and imported recovery history must be discoverable without a plan. Permissions are preflight only; actual transfer/verification remains authoritative.
