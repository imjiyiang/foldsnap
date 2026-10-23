# Data model

Schema version is recorded in schema_migrations. `internal/store/migrations/*.sql` are authoritative; the root migrations copy is reference-only.

- backup_plans: stable plan ID with a versioned JSON domain aggregate (sources, destinations, rule order, schedule and retention). Source/destination IDs survive display-name changes. Each destination records volume UUID, original mount and mount-relative path.
- backup_runs: durable status, stage, estimated progress, trigger, preview operations and bounded execution logs. Terminal states have finishedAt and optional error.
- snapshots: complete independent target snapshots, source mapping and entry manifests. IDs, paths and repository identity are revalidated before restore/cleanup. Pin state lives in SQLite.
- file_entries: source ID + relative path key, metadata, physical device/inode hint, SHA-256 content hash and extended-attribute digest. Updated only after all targets in a run commit.
- scan_checkpoints: plan-level polling signature, change debounce timestamp, online state, previous run ID, attempt timestamp and retry count.
- restore_jobs: durable restore requests, status, destination path, completion timestamp and error.
- settings: selected rsync path and scan interval.

Migration 002 adds a watch_cursors aggregate and normalized change_events with per-plan dirty-path uniqueness and monotonic revisions. Cursor and event updates share a transaction; successful runs acknowledge only the captured baseline.

Migration 003 adds backup_previews with expiring single-use plan/source/target fingerprints and consumed-run audit linkage. Snapshots record their mode and optional current-view path. Entries may be marked basicMetadata when full extended attributes were unavailable.

The initial schema deliberately uses validated JSON aggregates rather than placeholder tables for advanced optimization modules. Further normalized indexes and entity splits should arrive in numbered migrations when their access patterns are implemented.

On disk: repository.json → plans/<plan-id>/snapshots/<snapshot-id>/manifest.json + <source-id>/...; staging siblings hold unpublished trees. Manifest files include the source labels/mapping and every selected entry so recovery does not depend solely on SQLite.

Migration 004 adds run_jobs with durable FIFO sequence, queued plan/approval snapshots and held/running/ready states. Run attempt IDs prevent late control callbacks from overwriting a later attempt's status.

Migration 005 adds filesystem_objects keyed by physical metadata version; source_paths with selected/excluded state, reason, normalized metadata and last-changed generation; source_scans with the latest completed observation; and scan_generations with monotonic scan leases. A scan that finishes after a newer generation cannot replace it, regardless of wall-clock adjustments. Complete path replacement/pruning is transactional. Unchanged path rows are not rewritten merely to advance a scan generation. file_entries remains the last successfully backed-up projection; observed index state never means a snapshot has committed.

The small SQLite connection pool uses WAL, per-connection foreign keys/busy timeout and immediate writer transactions. Readers can inspect committed state during index writes. Queue/grant mutation stays atomic without holding the active-run control mutex while waiting for the database writer. See file-index.md for cache trust and audit semantics.

Migration 006 adds authenticated fingerprint tags. A private local cache-integrity key binds the physical metadata-version key to the SHA-256 value; unsigned or damaged records are misses and get rebuilt from actual file reads. This key is unrelated to snapshot encryption or authentication tokens. Cache metadata is never the sole proof for moved-file reuse or target publication.

In 0.9, optional run sourceIds/sourceNames retain a canonical manual source selection; snapshot updatedSourceIds records the sources updated in that generation. Missing fields retain legacy full-plan semantics. ExecutionSourceIDs is runtime-only and is never accepted as saved-plan JSON. run_jobs always stores the full plan before applying source/destination scopes. The queue deduplication key is plan ID + canonical source scope + canonical destination scope. Single-source success updates only its selected source projection and does not acknowledge whole-plan events. These are backward-compatible fields in existing JSON aggregates, not a new SQL schema.

In 0.10, sources persist optional volumeId, directoryId, directoryBirthTime, mountPath and volumeRelativePath. Directory IDs and nanosecond creation times are strings to survive JavaScript roundtrips exactly. Missing fields denote legacy unbound sources; explicit plan save binds them. Existing source identities cannot be erased/replaced by request data. `waiting_for_source` is a durable queued state; run and run_jobs retry metadata are updated atomically. Source binding/rebinding resets stale failure backoff and requires fresh source audit. Fields remain within existing JSON aggregates; SQL schema remains at migration 006.

0.11 Schedule fields add net-change thresholds, optional explicit-zero minimum interval, maximum full-check interval and bounded automatic attempts. scan_checkpoints now retains change summary/revision/signature/baseline-run identity, decision time, maximum-check state and retry scope. backup_runs records automaticAttempt and retryOf. JSON aggregate changes remain compatible with SQL migration 006. Full-plan interval clocks exclude source- and destination-scoped runs; partial success can still invalidate the cached change summary because it changes a target baseline.
