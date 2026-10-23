# Architecture

LocalBackup 0.15 is a local-first modular monolith: React/TypeScript/Vite UI → loopback HTTP API → Go planner/executor → rsync and SQLite. Production assets and numbered SQL migrations are embedded in the Go binary. There are no remote services or telemetry.

`internal/backup` contains domain types, canonical-path and volume checks, shared filtering, SHA-256 scanning, operation planning, rsync argument construction, immutable snapshot publication and restore. `internal/store` owns SQLite WAL persistence and migrations. `internal/server` coordinates requests, audit records, a serial write queue, scheduling, retention and manifest recovery. All file writes stay behind Go validation. CLI arguments are passed directly to `exec.CommandContext`, never a shell.

On macOS, statfs determines the owning mount point; diskutil supplies its Volume UUID. External destinations save a mount-relative path and are rediscovered by UUID if their `/Volumes` mount name changes. A repository UUID prevents silent replacement with another repository on the same disk. Sources persist volume UUID, directory ID and creation time. Original renamed directories can be explicitly rebound; external mount-name changes resolve by identity. Missing sources wait, substituted roots require confirmation, and all publication still fails closed. See source-binding.md.

The scanner and transfer use one selection manifest: source rules precede plan rules, first match wins, excluded directory subtrees are pruned. A NUL-delimited files-from list contains only selected regular files and symlinks; Go creates directories and restores their metadata separately. A final tree-layout audit rejects any unlisted path, preventing directory transfer semantics from bypassing filters. Source IDs are real storage namespaces; targetPath is a display mapping in this version. Symlinks are preserved, never followed.

All writes (backup, restore, recovery, retention) are serialized. An advisory process lock prevents two service instances sharing a database. Completed snapshots remain immutable from the application's perspective. OS-level immutability flags are not applied; users with direct filesystem access can still modify backup files.

Progress uses persistent stage updates and REST polling every three seconds, with one-second live resource samples. `/events` offers authenticated Server-Sent Events for integrations. This version uses SSE rather than WebSocket. Workflow percentages and rsync per-file percentages are distinct; logical processing rates and physical-device read/write rates are shown separately.

Native FSEvents batches, cursors and dirty paths are durably coordinated by the scheduler; a full content audit handles gaps. See change-detection.md. The source scanner can reuse authenticated APFS physical-version fingerprints; journal-gap/startup/hourly audits and final target verification still read actual content. Durable generation leases and path/object indexes are described in file-index.md.

See backup-semantics.md for publication and recovery details, and roadmap.md for deliberately deferred features.

The first-run setup endpoint performs read-only directory/identity/permission preflight. UI-created plans default to paused automatic triggers; saving a paused new plan opens a real preview. Existing plan enablement is unchanged. See first-run.md for permission and setup boundaries.

Interrupted work recovery is documented in [staging-recovery.md](staging-recovery.md). Receipts locate candidates only; the current source and candidate file must both match current entries, and recovered files get independent cloned/copied inodes before final publication verification.

Configuration/history reconstruction and separate durable pin markers are documented in [configuration-recovery.md](configuration-recovery.md). Recovery imports paused plans and historical views only; current physical fingerprints require fresh scans.
