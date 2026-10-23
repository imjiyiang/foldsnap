# 0.17.0 UI and scan audit

2026-10-22, macOS Apple M3 Max. Installed release tested with the user's existing two plans and two snapshots; plan JSON and all run/snapshot IDs matched before and after upgrade. Test operations used an independent data directory on port 18787.

## Changes

- Plan scheduling is now “未开启（仅手动）”; last-run outcomes are independently displayed. Cancelled, interrupted and confirmation-required runs no longer fall through to “准备就绪”. Weekly and multiple-trigger schedules are accurately summarized.
- Shared run labels cover all current states; switches have specific accessible names. Source participation and automatic scheduling no longer use ambiguous “暂停”. Empty terminal logs no longer say a scan is running; cancellation no longer exposes a raw context error as failure.
- Removed the inert history-tab button, repeated setup links and duplicate empty-source/target add actions. Finished scan results can be dismissed without revealing older results for the same scope. Starting a run dismisses its scan result for this page session. Active scans stay visible.
- Cards, long names, status text and modal actions wrap. Short windows hide the decorative sidebar note and allow sidebar scrolling so Settings remains accessible.
- A reader-owned pooled hash buffer removes a 256 KiB allocation per file without weakening SHA-256 verification, identity checks, symlink protection or cancellation. Large preview operation filtering is memoized.

## Verification

Actual browser inspection: overview, plans, queue, resource monitor, history, storage and settings; plan editor's three tabs, backup preview, snapshot file browser and retention preview. Desktop widths 1265–1280 px had no document horizontal overflow. Native app launched and final installed UI captured in `.data/ui-0.17/installed.png`. Responsive CSS was revised; a full mobile breakpoint/device matrix was not performed.

- `node --test apps/web/tests/status.test.mjs`: three semantic cases passed.
- `go test -race ./internal/backup`: passed, 145.335 s; includes new concurrent-buffer and cancellation coverage.
- Targeted hash/Finder tests, `go vet ./...`, TypeScript/Vite, Prettier, app signature verification: passed.
- Prior release aggregate race run completed successfully before this audit; server/store were not changed by this release.

`BenchmarkHashSmallFile`, 4 KiB, three repetitions, same machine:

| Metric | Before | After |
| --- | --- | --- |
| ns/op | 26027 / 25774 / 26778 | 16455 / 15894 / 16160 |
| B/op | 263219 / 263224 / 263221 | 963 / 976 / 950 |

Approximately 38% less time for this microbenchmark, not a claim about full backup duration. Cold disk metadata operations, rsync preflight, index commits and full publication verification still contribute to end-to-end latency.

## Outstanding observation

The first installed 0.17 launch listened on 8787 but did not serve requests for roughly two minutes. It then became healthy without data changes; HTTP response measured 0.000510 s afterward. Sampling captured a blocked read but did not identify a reliable root cause. The startup reconciliation path is synchronous and is a candidate for further instrumentation, not a confirmed diagnosis. Do not describe this startup delay as fixed. Prior app and consistent database backup are in `.data/release-archive/0.16.0`.
