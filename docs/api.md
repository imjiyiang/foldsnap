# API v1

Base: http://127.0.0.1:8787/api/v1. Every request needs `Authorization: Bearer <access.token>`. Browser calls must be same-origin. Failures return `{ "error": "..." }` with a non-2xx status. Bodies are limited to 1 MiB.

| Endpoint | Method | Purpose |
| --- | --- | --- |
| system/monitor | GET | Latest 120 one-second resource samples; physical-device rates and live logical backup processing metrics |
| system/status | GET | Engine capability, DB, active file task, home |
| settings | GET/PATCH | rsync path and polling period |
| volumes | GET | Mounted disks, UUID, total/free capacity |
| filesystem/browse?path=... | GET | Existing directory and direct subdirectories |
| plans | GET/POST | List or create a complete plan aggregate |
| plans/{id} | GET/PATCH/DELETE | Read/replace aggregate or delete config only |
| plans/{id}/sources, destinations | GET | Read embedded plan collections |
| plans/{id}/rules/preview | POST | Draft complete plan → selected entries and ignored reasons |
| plans/{id}/preview | POST | Scan + actual rsync dry-run → operation/removal summary and single-use expiring previewId |
| plans/{id}/runs | POST | Start async backup; optional {previewId,confirmDeletes}; 202 with durable run |
| runs, runs/{id} | GET | List or inspect run status/logs |
| runs/{id}/cancel | POST | Cancel context/process; never publish partial tree |
| runs/{id}/operations | GET | Saved run preview |
| snapshots?planId=... | GET | Complete snapshots without large entry manifests |
| snapshots/{id}/files?sourceId=...&search=... | GET | At most 2,000 matching manifest entries and total |
| snapshots/{id} | PATCH | `{ "pinned": true }` |
| restores | POST | `{snapshotId,sourceId,path,destination}`; empty path = entire source; synchronous result is a persisted restore job |
| retention/preview | POST | `{planId}` → cleanup candidates |
| retention/apply | POST | `{planId,ids,confirm:true}`; validate candidates again |
| repositories/import | POST | `{path}` parent of LocalBackup repository; verify and re-index complete snapshots |
| events | GET | Authenticated SSE stream of recent runs |

Collections are arrays, including empty collections. Plan PATCH currently replaces the full aggregate; it is not JSON Merge Patch. Sources and targets are edited as part of the plan to keep validation atomic at that boundary. A single process serializes writes across all targets. Runs can be polled through REST; the React UI polls every three seconds.

Mirror deletion approvals and nondeleting incremental semantics are specified in [backup-modes.md](backup-modes.md). Preview grants live in migration 003; terminal runs may need_confirmation rather than failed/success.

Resource monitoring polls once per second. Disk readBps/writeBps are nullable bytes/second, system-wide physical-device metrics. Work bytes and bytesPerSecond describe the current processing stage, with optional per-file transfer percentage; they are separate from physical I/O. Samples are in-memory, and reset on restart. See implementation-status.md for scope and availability.

File selection policies are documented in [file-filters.md](file-filters.md). `Plan.fileFilters` and nullable `Source.fileFilters` support `minBytes`, `maxBytes`, `modifiedWithinDays`, `types` and exact-path `exceptions`. `rules/preview` additionally returns `selectedReasons`, source metadata and `evaluatedAt`, alongside the complete selected entries and ignored records. Excluded directories are reported once for the skipped subtree. Preview approvals also bind the computed view, and execution rejects a changed dynamic scope.

Queue/control: `GET system/queue` lists pending jobs in FIFO order; `POST runs/{id}/pause`, `/resume`, `/cancel` return the current durable run. A start request returns an existing pending run for the same plan or creates a new durable job, with status `queued`, `running`, `waiting_for_volume`, `pausing`, or `paused` before terminal completion. `queuedAt` records enqueue time, while `startedAt` is updated on each execution attempt. See [task-queue.md](task-queue.md) for revalidation, process-group control and restart semantics.

Index diagnostics: `GET system/index` returns normalized path/fingerprint counts and completed source generations. `POST system/index/scan` takes `{planId}` and performs an uncached complete source scan in the maintenance slot. Preview responses and rule tests include optional `scanStats` (`filesHashed`, `cacheHits`, `bytesHashed`, `bytesReused`, `forced`). These counts concern source fingerprint work, not per-target transfer bytes. See [file-index.md](file-index.md) for trust, generations and audit bypass.

### 0.8.0 磁盘绑定

`GET /api/v1/volumes` 同时返回实时已连接卷和计划持久化的离线卷。`connected=false` 时容量未知（0）；`bindings` 含 planId/planName/destinationId/relativePath/enabled/automatic。`automatic` 表示计划与目标均启用且 OnMount 开启。运行响应可含后端生成的 `destinationIds`（磁盘连接任务范围）；创建运行请求不接受此字段。绑定源为已验证计划目标中的 volumeId/mountPath/volumeRelativePath，不额外产生第二份磁盘注册表。

### 0.9.0 手动目录范围

`POST plans/:id/preview` 接受可选 JSON `{ "sourceIds": ["src-id"] }`；省略或空数组表示整个计划；所有配置源均启用时，选择全部目录也等价于整个计划。计划含停用源时，显式选择所有启用源仍保留停用源的旧版本。响应的 sourceIds 返回规范化后的范围。`POST plans/:id/runs` 的 sourceIds 必须和 previewId 所绑定的范围一致。只接受该计划的已启用目录，重复/未知/停用 ID 会拒绝。运行持久化 sourceIds/sourceNames，快照 manifest 持久化 updatedSourceIds。未选目录沿用各目标最新备份，未曾备份的目录不虚构内容。旧无请求体的预览保持兼容。

### 0.10.0 源身份

`POST system/source/inspect` 接受 Source JSON，返回只读 `{state: bound|unbound|unavailable|changed, message, source?}`。受全局 bearer/Host/Origin 校验；不保存绑定。计划保存时绑定新源，并从已保存计划保留旧源的身份，不接受清空/伪造。源字段 volumeId/directoryId/directoryBirthTime/mountPath/volumeRelativePath 均可返回；ID 与创建时间为字符串。源离线运行返回 waiting_for_source，由已有任务控制接口暂停、继续、取消。旧未绑定计划预览/执行要求编辑并保存以建立首次绑定。

### 0.11.0 智能调度

Schedule 可选字段：minChangedFiles/minChangedBytes（0 关闭），minIntervalMinutes（省略默认 1，显式 0 关闭），maxIntervalMinutes（0 关闭），maxAttempts（1—10，省略默认 3）。所有间隔上限 525600 分钟，数量至多 1000000，大小至多 2^50 字节。`GET plans/:id/schedule` 返回最近 decision/evaluatedAt/checkedAt/lastMaxCheck/changes、failures/maxAttempts/retryPending/nextRetryAt；未知检查与统计为 null。只读接口不会启动扫描。运行记录可含 automaticAttempt/retryOf，只有后端可设置这两个字段。

### 0.12.0 首次设置检查

`GET system/setup` 返回 checkedAt/platform/databaseReady/rsync、全部恢复点记录数量 snapshots、计划列表 plans。各计划含 ready/snapshots/enabled 及 sources/destinations 状态；禁用的源和目标不阻挡本次完整运行。检查为只读元数据与目录权限预检，不创建文件，不更改绑定、索引、运行与调度。ready 不等价于已实际备份或实时完整性审计。

### 0.13.0 扫描范围统计

预览/规则测试/完整重扫返回的 `scanStats` 新增 `fullSources`、`subtreeSources`、`pathsVisited`、`projectedPaths`。`pathsVisited` 是实际扫描的路径（不含源根），`projectedPaths` 是沿用的完整性保护索引项，不代表本次读过该文件。`system/index` 的 sources 新增 `scanMode` (`full`/`subtree`)、`walkedPaths`、`lastFullScanNs`。运行时脏路径不能通过 JSON 计划指定。

### 0.15.0 配置恢复

`POST repositories/recovery/preview {path}` 完整校验仓库后返回 `previewId`、`expiresAt`、可恢复的暂停 `plans`、`snapshots`、`pinned` 与 `warnings`，不修改数据库。`POST repositories/recovery/apply {previewId,restorePlans}` 重新校验，原子恢复历史及可选的缺失计划，返回 `imported`、`restoredPlans`、`pinned`、`warnings` 和 `auditId`。原预览十分钟有效，单次使用。旧 `repositories/import` 继续只重建历史，缺失保留状态按永久保护处理。


## Background preview scans (0.16)

POST `/plans/{id}/preview-jobs` with `{sourceIds?: string[]}` returns 202 and a durable scan ID immediately. GET `/preview-jobs` lists compact progress without operation payloads; GET `/preview-jobs/{id}` returns the completed preview and single-use approval. POST `/preview-jobs/{id}/cancel` cancels active/queued work. States: queued, running, cancelling, ready, cancelled, failed. Restart interrupts incomplete scans; plan mutation invalidates old results. Scans never start a backup automatically.

POST `/filesystem/directories` accepts `{parent: <identity from filesystem/browse>, name: <single component>, protectedSources?: string[]}` and returns 201 `{path,auditId}`. Parent UUID/file ID/birthtime, no-follow identity and protected path checks precede mkdirat. Existing paths are not overwritten. GET `/filesystem/browse` now also returns `identity`.
