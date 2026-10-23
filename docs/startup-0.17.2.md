# Startup repair — 0.17.2

## Findings from the installed macOS application

The old constructor synchronously reconciled external repositories before HTTP serving and the launcher readiness message. An observed reconciliation took 90.868 seconds. A locally captured Go stack located the wait in a read-open from canonicalPlatform; after isolating that call, another capture showed repository lock-file opening could also wait. Separately, sampling a GUI-launched diskutil showed it opening a raw disk in DiskManagement's wholeDiskSupportsLowLevelFormat path. OS-level disk access can wait; it must not gate application startup.

The dashboard awaited volumes, status, plans, snapshots and runs together. Multiple tabs independently polled diskutil every three seconds. The readiness endpoint also executed rsync on every request. The launcher silently returned from Open when it had no URL. In-place replacement of signed executable inodes produced a LaunchServices spawn failure despite a valid on-disk signature; replacing the entire bundle restored launch.

## Changes

- HTTP serving starts independently of external repository I/O. Clean, indexed plans do not reread removable disks at launch. Missing indexes and recoverable runs still initiate background repository import; queue/watch operation does not wait on unrelated disks. Restore, retention and backup retain fresh identity/path validation.
- macOS canonical paths and volume UUIDs use getattrlist metadata queries. No read-open or diskutil raw-device probe is required for these checks. UUID identity was compared with diskutil for the system volume, APFS Data and the connected external disk. Metadata path lookup also works on a FIFO without waiting for a writer.
- Status uses cached rsync diagnostics. Display-only volume discovery has one shared in-flight query and a ten-second cache; write validation does not use that cache.
- Dashboard disk discovery is independent of core data loading. Refresh requests do not overlap; reads have a 15-second timeout.
- The native launcher shows a startup window immediately, including elapsed-time feedback after 15 seconds, and handles Open while starting. Phase and successful browser-open timings are persisted locally in ~/Library/Logs/LocalBackup/startup.log. Slow repository diagnostics are written privately beside the database.
- Packaging builds and signs a fresh bundle, verifies it, then replaces the complete app instead of overwriting signed Mach-O files in place.

## Installed acceptance results

Three consecutive stop/reopen cycles of release/LocalBackup.app on the user's Mac (2026-10-22, Asia/Shanghai):

| Cycle | HTTP ready | Browser open success callback | Repository startup processing |
| --- | --- | --- | --- |
| 1 | 0.201 s | 0.307 s | 0.265 s |
| 2 | 0.197 s | 0.307 s | 0.272 s |
| 3 | 0.179 s | 0.288 s | 0.268 s |

Browser-open callback times confirm macOS accepted the open action; they are not page paint measurements. The installed UI was also opened and visually checked. A separate UI harness delayed disk discovery by 20 seconds while the dashboard and plans remained usable. Production plans, run IDs and snapshot IDs were compared before/after and unchanged (2 plans, 3 runs, 2 snapshots). No production backup/restore was executed.

Tests cover deferred recovery, readiness without subprocess execution, concurrent-page volume discovery, metadata-only canonical paths, UUID compatibility, port fallback and authenticated service reuse. Full go test -race ./... and the TypeScript/Vite build pass. Logs and snapshots of verification are in .data/.

External media can still stall during actual file access; startup no longer relies on that access. Disk checks fail closed rather than treating inaccessible volumes as safe backup destinations.
