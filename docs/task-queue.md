# Durable backup queue and run control (0.6)

Migration 004 stores each pending job's FIFO sequence, plan snapshot, consumed preview approval, ready/held/running state and next-attempt time in `run_jobs`. Run creation, preview-grant consumption and job creation share one SQLite transaction. At most one nonterminal job exists per plan through the serialized coordinator; repeated manual or scheduled requests return that pending run. The queue is bounded to 100 pending/active jobs.

One global file writer serializes backup, restore, recovery and retention. Eligible queued backups execute in FIFO order. A paused active backup retains its writer slot and repository lock. A queued hold and a job waiting for its target disk allow other ready jobs to proceed. Missing/wrong-identity targets are checked again after 30 seconds, with the existing fresh UUID/repository safeguards before writes. No empty mount directory is created to simulate a disconnected disk.

`GET system/queue` returns nonterminal jobs in sequence order with large logs/previews omitted. Full status and audit logs remain available through `runs/{id}`. `POST runs/{id}/pause`, `/resume` and `/cancel` control both queued and active jobs. The UI includes a queue page and controls in run details. Starting a reviewed plan while busy adds it to the queue rather than rejecting it.

## Active run control

`pausing` means a pause was requested but is not yet acknowledged. Hashing, scanning, reuse/copy loops, verification and publication boundaries use context checkpoints. At the next checkpoint the task persists `paused` and waits without busy polling. Rsync uses an isolated Unix process group; pause/resume signal the complete group, including its local sender/receiver children. Cancellation kills the process group even while stopped and wakes cooperative gates through context cancellation. A paused transfer reports zero logical processing speed; physical disks can still show I/O from other apps.

Atomic metadata writes and filesystem flushes complete their current noninterruptible operation before reaching a checkpoint. A pause arriving after the final commit may race with successful completion; terminal state wins. A run never publishes an unchecked partial tree to implement pause/cancel. Resume continues the same live process/staging tree; source changes remain subject to final full verification.

## Queue revalidation and restart

Before the first source scan, the worker checks that the current saved plan matches the queued plan and that a consumed preview has not expired. The actual scan, baseline, current view and deletion approval are then revalidated using the existing fingerprint protocol. Queueing does not extend the 10-minute preview lifetime or authorize new mirror removals.

Queued and queued-held jobs survive service restart. An interrupted active job, including an actively paused one, becomes `recoverable`; it is not silently resumed or committed. Terminal runs with leftover queue rows are cleaned up rather than run again. Existing orphan-generation reconciliation still verifies immutable manifests independently. Resuming incomplete staging after process/service restart remains separate unfinished work in the original design.

Tests exercise actual stopped/resumed child-group writes, cancellation of a stopped group, cooperative gate acknowledgement, real rsync pause/resume, paused cancellation without publication, FIFO serialization and deduplication, queued cancellation/holds, restart preservation, interrupted paused recovery, offline queue bypass/recheck, expired queued approval and changed plan rejection. Simulated offline paths do not substitute for physical unplug/power-loss acceptance.

Browser acceptance used two generated-data plans: a real 32 MiB rsync transfer was paused with a stable processed-byte count, while the second plan queued. The browser held the second plan, resumed the first to success, then resumed the held plan. Both produced verified snapshots and the queue drained. The original rsync setting was restored and automatic scheduling remains disabled for both sample plans. Screenshot: `.data/task-queue-paused.png`.

## 0.9.0 目录范围

同计划、相同目录范围与目标范围的未完成任务继续合并。不同目录范围是不同任务，顺序执行，完整计划也作为独立范围排队。每个任务保留完整计划配置，sourceIds 仅在比较配置未变化后应用；目标新基线在执行时重新读取，已有审批若因前一个任务发布而失效则需要重新预览，不能静默绕过审批。暂停/取消仅控制对应任务。

0.14 起，重新预览并创建新运行时，可以校验并复用兼容暂存中的完整文件。原运行保留中断记录；不复用过期审批。详见 [中断暂存恢复](staging-recovery.md)。
