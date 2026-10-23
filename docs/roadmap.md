# Delivery scope

## Implemented through 0.16

- Go + React/TypeScript/Vite monorepo; Lucide UI; versioned embedded SQLite migration, WAL and durable plans/runs/indexes/restore audit.
- Three modes: immutable snapshots, nondeleting incremental current views, and explicit single-use-preview mirror removals; atomic current pointers and repository writer/read locks.
- Multi-plan, multi-source, multi-target configuration; per-target independent snapshots; stable source namespaces; directory picker; rule presets/preview; includes/excludes/hidden files plus size/date/type conditions, exact exceptions and source overrides.
- rsync capability detection, actual dry-run, transfers and cancellable runs; persistent logs and workflow progress, isolated process-group pause/resume/cancel, durable FIFO/held/offline jobs.
- Source volume/file IDs, mtime/ctime/birthtime, normalized path/object index, monotonic scan generations and authenticated APFS fingerprint reuse; explicit complete scans and cache diagnostics.
- Live file-byte progress, processing speed and physical-device read/write charts.
- SHA-256 verified immutable snapshot protocol; link-dest reuse and verified within-source rename reuse; startup interrupted-run recovery and committed-manifest reconciliation.
- History filtering, file search, single file/directory/source restore into a new location; explicit retention preview, latest/pinned protection.
- Native FSEvents with journal replay, durable/coalesced change paths and atomic cursor updates; full content recovery after event loss, startup/configuration reconciliation and hourly audits; polling fallback.
- Periodic/debounced scheduling, nonrecursive native mount-directory notifications, wakeup catchup and bounded retry; persisted checkpoints.
- Volume UUID and repository identity checks; mounted external-volume rediscovery; source/target overlap and cross-plan recursion protection; remaining-space preflight.
- Self-contained Go binary with embedded web assets; macOS app bundle, zip and explicit user-level launchd installer; double-click source launcher.

- Disk UUID binding UI, persistent offline associations and independent mount-triggered rotation targets.
- Reviewed manual source-only/subset runs with immutable preservation of unselected sources, scope-bound grants and scope-aware durable queue deduplication.

- Source-root UUID/directory ID/creation-time binding, safe original-directory rebinding, external remount resolution, and atomic durable waiting for unavailable sources.

- Net-view change thresholds, minimum change spacing, maximum forced content checks, durable bounded exponential retries and saved-policy diagnostics.

- Reopenable first-run guidance, read-only source/target permission preflight, paused-by-default UI plans and automatic first preview before user-confirmed backup.

- Authenticated native dirty-subtree scans on APFS: sealed whole source projections and event queues, generation conflict fallback, bounded filesystem traversal, full loss/config/hourly reconciliation and actual visited/projected path metrics.

- Cross-restart complete-file staging recovery: durable configuration-bound receipts, actual source/candidate SHA-256 proof, independent APFS clones/copies into new staging and full publication checks.

- Configuration/history recovery previews with full audits, paused full-plan restoration, durable external pin markers, historical file-index rebuilding and fresh live-index rescan; transactional import and audit.

- Durable cancellable background preview scans, live read counters, request-independent execution and explicit restart interruption; directory picker creation with bound parent identity and audit; direct card run controls/retry and strict staging-only Finder cache cleanup.

- Optional daily/weekly/monthly representative retention tiers with additive protection, deterministic UTC calendar grouping, independent destinations and apply-time policy revalidation (0.19).

## Still incomplete (not represented as completed)

- Million-file/500GB performance acceptance. Dirty-subtree scans now reduce healthy native filesystem enumeration, but whole projection authentication, materialization and SQLite updates remain O(N); complete target verification and loss-recovery audits still read full trees. There is no million-file or 500GB performance claim.
- General snapshot clonefile reuse (staging recovery already uses APFS clones), block-level fingerprint caches, cross-source/cross-volume move reuse and whole-directory move optimization. Current verified moves avoid retransmission but still read content for hashes.
- SSH/NAS targets, bidirectional sync and follow-symlink mode. Snapshot/incremental/mirror modes are implemented; historical generations remain immutable in every mode.
- Automatic source relocation search, per-target schedules and scheduled unattended cleanup.
- macOS ACL/ownership/Finder metadata completeness, native notifications, keychain token storage, event-driven Disk Arbitration, system-idle trigger.
- Intra-file/block continuation, explicit old-staging cleanup, and recovery progress/cancel UI. Configurations absent from legacy manifests and unbacked-up local changes cannot be reconstructed; settings, queued jobs and old run/audit history are not disk-recovered.
- Developer-ID signing, Apple notarization, updater, universal build and real unplug/power-loss/APFS case-sensitivity stress tests. The local app bundle is ad-hoc signed for this Mac and is not a publicly notarized release.

This is a progress ledger, not a reduction of the requested design. Items marked incomplete still need implementation or verification. Remote targets, bidirectional sync and idle-trigger policies belong to the design’s explicitly later phases.
