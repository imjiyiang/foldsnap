# Three backup modes (0.3)

All modes publish verified immutable generations under `LocalBackup/plans/<plan-id>/snapshots/<snapshot-id>`. Neither incremental nor mirror writes into a committed generation. Their visible current copy is `LocalBackup/plans/<plan-id>/current/<source-id>/…`, an atomically replaced, repository-relative symlink to a complete generation. Changing this pointer changes the current copy without destroying history. Source display names and target mappings remain in the manifest; stable IDs are storage namespaces.

| Mode | New generation | Current copy | Missing/excluded source paths |
| --- | --- | --- | --- |
| Snapshot (default) | Exactly the selected source set | Does not alter an existing sync pointer | Absent from the new snapshot; older restore points remain |
| Incremental | Selected files plus safely retained previous entries | Atomically advances after verification | Retained in the current view |
| Mirror | Mirrors enabled source/rule scope and preserves protected entries | Atomically advances after deletion approval when required | In-scope missing paths are removed from current; excluded/disabled/removed source scopes remain protected |

Incremental gives newly selected paths precedence over retained paths. A source file replacing an old directory, or vice versa, cannot retain conflicting descendants/ancestors at the same path. The old type/content remains recoverable from prior history. A source rename normally leaves both old and new paths in incremental current. Verified old content can still be reused for the new path without retransmission. In snapshot/mirror the old path can disappear from the new generation, while history remains intact.

The transfer manifest contains only selected live-source entries still requiring transfer. Unchanged and moved candidates are independently checked against source content and metadata, linked into private staging, and excluded from rsync so it cannot modify shared historical inode attributes. Since 0.21.12, their mandatory target content check is the final complete staging SHA-256 pass; sync modes also retain the initial full baseline audit. Retained entries are seeded separately from a validated prior generation and included in the final complete manifest. Files use immutable hardlinks where possible with a copy fallback; independent directories and symlinks receive their own metadata. Shared historical inode attributes are never adjusted. Basic-metadata entries from old openrsync generations remain marked basic when retained across a later rsync 3.x generation; missing historical attributes cannot be invented.

## Mirror approval protocol

`POST plans/{id}/preview` returns a ten-minute, single-use preview ID, complete searchable operation/removal lists, deletion path count and logical deletion bytes. Directory removals and file/directory type conflicts count as removals; moves also remove the old current path. The UI exposes all operations through pagination and separate deletion search; it does not silently truncate the review list.

The stored approval binds plan configuration, selected source entries, every target's current pointer, repository identity and complete baseline snapshot fingerprint. `POST plans/{id}/runs` can pass `{ "previewId": "...", "confirmDeletes": true }`. Starting consumes the grant transactionally with the durable run record. The executor rescans and compares the fingerprint before writing a generation. A changed plan/source/baseline, expired grant, reused grant or absent explicit deletion confirmation cannot authorize mirror removals. A preflight rejection publishes neither a generation nor a current view. A conflict discovered after a verified immutable generation commits can leave a recoverable orphaned history point, but does not advance current and does not mark the run successful.

Scheduled mirror backups can proceed without approval when they remove nothing. Otherwise the run terminates in `needs_confirmation`; scheduling holds that plan instead of repeatedly retrying deletion. The user opens its preview and confirms a fresh run. Historical snapshots remain available throughout. Switching the plan to a non-mirror mode releases the mirror hold.

Each destination has its own baseline/current generation. The approval covers all enabled destinations together. Source changes during transfer still fail verification or rsync rather than permitting an incomplete generation. If a target completes and a later independent target fails, the completed target remains valid while the plan run is marked failed.

## Repository concurrency and integrity

Writers acquire a nonblocking exclusive `.write.lock` advisory repository lock; restores acquire a shared lock. This coordinates separate service processes using the same repository. Old repositories without a lock file can still be read, including on read-only media; they receive a lock when next written by this version.

Preview validates repository/manifest identity and, for incremental and mirror modes, checks the current generation for untracked paths. It compares manifests without hashing historical file contents, so completing the first backup does not add a full historical-content read to every subsequent preview. A preview is not an integrity audit.

Manual/background previews check rsync capabilities using only fixed generated data: one tiny UTF-8 file in a nested directory and one symbolic link, with the same transfer flags and a five-second command deadline. User sources, target paths and the real manifest are never passed to rsync during preview. A successful capability probe is cached by executable identity/version for ten minutes (at most 64 entries); expiry, replacement or service restart only repeats this small probe. Source/rule changes are handled by the fresh Go scan, not a repeated full rsync dry run. Every preview still resolves identities, scans sources, validates the target and issues an independent single-use approval. `preflightCacheHits` counts reused tool probes. Execution repeats the fresh Go scan and tool capability check. Since 0.21.12, a fresh rsync preflight covers the complete actual transfer allowlist after recovery/reuse decisions, including fallback copies, before any rsync transfer. First backups retain full preflight before repository initialization. A fully verified unchanged run needs no transfer preflight. Baseline and staging integrity checks remain in force.

Before incremental or mirror transfer/reuse, the executor holds the exclusive repository lock and fully audits the historical baseline, including content hashes, attributes and untracked paths. This check also detects corruption introduced after preview approval and aborts without publishing a new generation or advancing current. The executor checks its expected baseline and current-pointer identity before replacing current, and fully verifies staging before publication. Direct filesystem edits by the owning user are outside the immutable-application boundary; such edits are diagnosed, not silently deleted.

Retention always protects the current-pointer generation even when a newer snapshot-only backup exists. Cleanup rechecks current while holding the repository writer lock. Latest and explicitly pinned generations remain protected as well. A logical mirror removal does not imply physical bytes are released; history and shared inodes may still retain the data. Permanent cleanup remains a separate explicit operation.

Before transfer, the private staging tree probes target case sensitivity, Unicode normalization and trailing-dot/space equivalence. A conservative normalized-name check rejects colliding source namespaces/paths, including retained entries, before publication. Path validation also compares directory identities to reject parent/child overlap through case aliases. macOS F_GETPATH supplies actual path spelling without changing firmlink namespace. No source/history directory is written by the probes.


## 0.21.9 unchanged runs and direct reuse

When the complete resulting view, plan configuration, source bindings, mode and metadata capability match the existing recovery point, execution holds the repository writer lock and fully audits the baseline contents, attributes and layout. It reads selected source contents independently of SQLite hashes, verifies physical versions/attributes, then closes with a fresh full selection scan. Baseline physical versions/layout/manifest and repository/current identities are rechecked before success. A reusable in-memory hash map for the closing scan contains only source contents just fully read during this run; it cannot project FSEvents subtrees or write the persistent index.

This verified unchanged path creates no staging tree, receipt, reference links or manifest and never advances current. The run remains an auditable success with `noChanges`, `unchangedDestinationIds`, baseline ID in its logs and the message “内容未变化，完整校验通过，未新建恢复点”. In 0.21.9 this path still performed an uncached full-manifest rsync preflight; 0.21.10 defers that preflight until a new generation is necessary (see below). Each destination is decided independently; a new target still receives a complete snapshot. A changed configuration or metadata capability, missing sync pointer, changed directory metadata, scope or content still requires publication. Failures/cancellation cannot produce an unchanged success.

For new generations, verified unchanged/moved files are linked once directly to their final private staging paths. No temporary `.reference-*` tree is constructed. Rsync receives only the remaining allowed paths, with no historical link-dest, so its metadata updates cannot touch directly reused inodes. Unsupported hardlinks fall back to ordinary transfer. Full baseline auditing for synchronization modes, target name probes before new tree writes, immutable history, staging layout/content/attribute checks, fsync and atomic publication remain in place.

2026-10-23 本机验收：原生应用从 0.21.8 更新至 0.21.9。使用原 AI 模型计划（38,356 个普通文件、零变更），直接读取备份盘对应物理 disk6 的 IOBlockStorageDriver 累计计数；每次测量前记录 5 秒空闲计数，覆盖整个实际备份并在结束后等 10 秒收尾。旧版一次实测 136.503 秒，增加 281,006,080 B 写入、39,685 次写操作，并新增一份恢复点；该次空闲 5 秒有 159,744 B/29 次背景写入。新版连续两次实测分别 50.604、54.661 秒，两次均为 0 B/0 次写入，恢复点数量均保持 3 份，任务明确记录完整校验通过、沿用原恢复点。两次备份盘读取分别 1,417,318,400 B、1,443,246,080 B，完整校验读取仍在执行；本机 SQLite/任务审计仍会写入内置盘。

这些是整块物理盘计数，包含同期其他程序活动；不同测次的缓存和负载未强制一致，不能承诺所有环境下均为零物理写入或固定耗时。只针对本轮零变更路径报告写入实测收益；有变更时直接复用路径的安全性和清单行为由真实 rsync 集成测试验证，未为此修改个人模型源文件。

`go test -race ./...`、`go vet ./...`、前端/Go 构建及应用签名检查通过。回归覆盖三种模式不建重复恢复点且仓库目录/历史 inode 元数据不变、独立完整源读取、等长篡改、清单/布局篡改、校验中新增或修改更早文件、历史内容被修改、取消、直接复用路径不进入 rsync 清单、权限变化使用新 inode、元数据能力升级、单次批准和一个目标不变而另一个首次备份。保留策略测试改用真实不同版本生成恢复点，原保留/最新保护断言仍执行。

更新时已保存一致 SQLite 和旧应用。更新前已有的两份真实计划和五份恢复点记录逐条保持原值；新版两次计量期间的计划配置也完全一致，未引入测试计划。旧版写入基线测试新增的一份恢复点仍保留，没有清理任何真实历史。

证据：`.data/write-optimization-0.21.9-acceptance.json`、`.data/write-optimization-before-0.21.8.json`、`.data/write-optimization-after-0.21.9-first.json`、`.data/write-optimization-after-0.21.9-second.json`、`.data/write-optimization-0.21.9-preservation.json`、`.data/write-optimization-0.21.9-tests.log`、`.data/write-optimization-0.21.9-vet.log`；回退位置 `.data/write-optimization-rollback-location.txt`。


## 0.21.10 unchanged-run preflight and read accounting

Zero-change runs keep a fresh Go scan/tool capability check and all independent baseline/source content verification, physical-version guards, attributes, manifest/layout/identity checks and closing full selection scan. They no longer submit the real source manifest to rsync when no transfer or generation is required. Each new generation still requires a fresh full-manifest rsync dry run before staging or direct reuse; first backups run it before repository initialization. An unchanged corruption check cannot be replaced by SQLite fingerprints or a cross-run hash cache.

The run reports separate baseline, source and closing verification stages. Byte counters measure content delivered to the hasher, excluding manifests, attributes and filesystem metadata. Two source files with 7 B total are verified exactly once per copy in integration tests; a production-like stable-volume closing scan reuses only hashes fully read in the same run and reads 0 additional content bytes. A server test makes rsync fail after the first generation: unchanged verification still succeeds without another tool call, while a changed source requires fresh preflight and fails without publication. Full race tests, vet, builds and signing checks passed.

2026-10-23 原生 0.21.10 应用、原 AI 模型计划连续两轮实测：32.484 秒和 27.009 秒（0.21.9 两轮为 50.604 和 54.661 秒）。每轮已有恢复点和源文件分别完整读取 38,356 个普通文件、1,326,773,331 B；结束复核遍历 42,305 个路径，额外内容读取 0 B。基线内容校验分别 22.396/17.718 秒，源内容校验 4.401/4.457 秒，结束复核 0.769/0.771 秒。备份盘物理读取分别 1,195,790,336/826,662,912 B，均 0 B/0 次写入，AI 恢复点保持 3 份。物理读取受系统缓存影响，不能据此声称逻辑校验读取减少；本次主要消除零变更时无传输用途的 rsync 全量预演。

两份真实计划和五份恢复点的数据库记录与更新前逐条相同，无新增测试计划。未修改真实源文件或历史备份。证据见 `.data/read-optimization-0.21.10-acceptance.json`、两份 `.data/read-optimization-after-0.21.10-*.json`、`.data/read-optimization-0.21.10-preservation.json`、测试/构建日志及 `.data/read-optimization-rollback-location.txt`。


## 0.21.11 进一步减少重复元数据工作

成功任务的 `file_entries` 目录记录现在按事务内差异同步，无变化行不再删除/插回。零变更完整审计在同一阶段共用一次清单读取/解码，结束时仍独立读取并核对；元数据文件使用已验证的文件大小一次分配缓冲区，保留防链接、限长、短读/增长和版本检查。完整文件内容读取、结束复核、写入前身份校验及新一代完整预演/暂存验证/持久化发布均保留。整体检查、测试和本机计量参见 [0.21.11 性能复查](performance-audit-0.21.11.md)。


## 0.21.12 稀疏变化备份

未变化历史候选的内容检查合并到最终暂存完整 SHA-256 检查；独立源校验、同步模式初始完整基线审计、属性/身份检查与发布持久化保留。后续 rsync 新鲜预演和实际传输共用排除已复用/恢复项后的完整清单，包括回退复制项。详细独立负载、失败/篡改/取消及恢复验证见 [少量变化性能检查](sparse-change-performance-0.21.12.md)。
