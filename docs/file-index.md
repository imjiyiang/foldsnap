# Physical file index and fingerprint cache (0.7)

A source scan still enumerates the complete selected directory tree. The new index reduces repeated content reads; it does not yet implement dirty-subtree enumeration. Incremental subtree application, million-file memory/time benchmarks and broader real-device acceptance remain in implementation-status.md.

## Identity and reuse

Each observed entry records its volume UUID, file ID, device/inode hint, size, mtime, ctime and filesystem birthtime. ctime means metadata-change time, not creation time. These source identity fields are not assertions about the inode or ctime of the copied/restored target.

A SHA-256 content version key combines an algorithm/version tag, volume UUID, file ID, size, nanosecond mtime/ctime/birthtime, permissions and the extended-attribute digest. Source paths are separate from physical objects. New configurations reject source-ID aliases across plans so path indexes have independent namespaces. A different volume or reused inode with different birth/change times cannot inherit a previous version's fingerprint.

Persistent hash reuse is enabled only for verified APFS volume identities. Unknown, coarse-timestamp or unsupported filesystems continue full reads. Nested mounts are classified by their actual device/volume rather than inheriting the parent volume namespace. Even a cache hit rechecks the current pathname's metadata version. Same-size edits with restored mtimes are invalidated by ctime.

Hashes are calculated from a regular-file descriptor opened with no-follow/nonblocking flags, with physical-version checks before and after reading. A last-component symlink, FIFO or changed file cannot masquerade as a stable regular file. Source directory roots that disappear or become files fail the scan rather than turning into an empty backup.

## Durable observations

Migration 005 separates physical fingerprint versions (`filesystem_objects`) from source-relative paths (`source_paths`). The path record includes selected/excluded state and a reason. A skipped directory represents its excluded subtree without reading that subtree. `file_entries` continues to represent the last successful backup, not merely the last observation.

A monotonic scan lease is allocated before enumeration. Only a completed scan transaction replaces/prunes that source's path projection and advances `source_scans`. Failed/cancelled scans keep the previous completed projection; a late older scan cannot replace a newer one. Ordering does not depend on UTC clock monotonicity. A path's `scan_generation` is its last changed generation; unchanged rows are not rewritten just to advance the completed source generation. Unreferenced fingerprint versions older than 30 days are eligible for cleanup during subsequent scans of their volume.

Rule tests use a read-only index adapter: they may reuse known physical hashes but do not replace a saved source's policy/index with unsaved draft observations. Fingerprint versions themselves contain no source file contents. The index stays in the private local application database and is exposed only through the authenticated loopback API.

Migration 006 adds keyed fingerprint integrity tags. Legacy unsigned records are cache misses until actual source reads replace them.

## Verification boundary

Cache records are an optimization, never publication authority. Rsync still receives the exact NUL file manifest, and complete target-tree SHA-256 verification remains uncached before atomic snapshot publication. Snapshot audits, restore verification and reuse validation also read actual content. Cached values carry a keyed integrity tag bound to the physical-version key and hash. A damaged value/tag is ignored and re-read. A private disposable key authenticates cache records only; it never encrypts snapshots. Missing/truncated keys invalidate old tags safely, while unavailable credentials disable reuse. Moved-file reuse additionally re-reads the actual source before linking a previous file, so that branch cannot silently trust even an artificially signed bad cached value. Tests separately inject a signed bad value to exercise target/source verification independently of tag validation.

Startup, journal gaps, dropped events and hourly audits bypass the cache. Normal healthy native events can reuse unchanged fingerprints, while polling signatures now include physical identity/ctime/birthtime. Polling also performs its own hourly full audit; lightweight polls do not reset that audit's clock. Explicit complete scans repair cached versions from actual source content.

## Diagnostics

`GET system/index` returns path/fingerprint-version counts, selected/excluded counts and the latest completed source observations. `POST system/index/scan` with `{planId}` performs a forced source read in the maintenance slot and returns scan statistics. It updates metadata only and never deletes or publishes a snapshot. Backup jobs can queue while maintenance occupies the slot. Preview and rule-test results report files/bytes actually hashed versus fingerprints/bytes reused. Running monitoring reports cache-hit counts separately from bytes read.

SQLite has a small WAL connection pool, per-connection foreign keys/timeout and immediate writer transactions. Readers remain available during an index write. Queue insertion and active control persistence avoid holding the control mutex while waiting on that writer. Active execution attempts have distinct IDs so a delayed callback cannot modify a later attempt.

Tests cover persistent warm hits, restored-mtime mutation, physical-key changes, no-follow and FIFO handling, concurrent file mutation, cancelled/stale generations, clock rollback, damaged-tag repair, signed-bad-cache publication rejection (including moved-file reuse), read-only draft previews, global source namespaces, gap/polling audit bypass, authenticated diagnostics, WAL read availability and cancellation while pause persistence waits for a writer.

Local acceptance used an existing generated 32 MiB fixture only. A cold preview hashed 32 MiB; a warm preview reused one fingerprint with 0 source-hash bytes read. Rewriting one byte while restoring the original mtime caused a full rehash and a modified operation. A subsequent backup published a fully verified manifest containing the new hash and volume/file/ctime/birthtime metadata. Explicit API and browser rescans both hashed the full file with zero cache hits. Diagnostics are in `.data/index-acceptance.json`; the sample plan remains paused. Screenshots: `.data/file-index-diagnostics.png` and `.data/file-index-warm-preview.png`. Source-scan byte savings do not imply that rsync or target verification did no I/O.
