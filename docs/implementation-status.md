# Original design completion ledger

The attached LocalBackup product/architecture plan remains the objective. The current implementation is not treated as a complete replacement for that scope. This ledger separates evidence from unfinished work and keeps the design's explicitly later features distinct from V1 work.

| Design requirement | Current evidence | Status / next work |
| --- | --- | --- |
| Go + React/TypeScript/Vite monorepo, SQLite WAL, migrations, OpenSpec, engineering constraints | cmd, internal, apps/web, migrations 001/002/003/004/005/006/007, AGENTS.md, openspec/specs; build and persistence tests | Implemented core; frontend uses authored CSS/Lucide rather than Tailwind/shadcn |
| Multi-plan/multi-source/multi-target CRUD and stable source storage IDs | Plan aggregate API/editor; multi-source integration tests; separate target snapshot IDs | Implemented through 0.9, including reviewed source-only/manual subset runs; richer per-target runtime status remains |
| Three modes: snapshot, nondeleting incremental, explicitly authorized mirror | Immutable generation/current-view composition; single-use bound preview grants; deletion counts/bytes/list and explicit checkbox; mode, authorization and retention tests | Implemented core in 0.3; all modes retain recoverable history |
| Path validation, overlap, recursion, symlinks, offline/identity safety | paths.go, cross-plan validation, symlink/volume/overlap tests | Core implemented; physical/case-alias overlap rejection and staging filesystem name-collision preflight are implemented; source identity binding/rebinding and durable offline waiting implemented in 0.10; automatic per-source splitting and configurable follow-symlinks remain |
| Rule hierarchy, glob/hidden/presets and test preview | shared rule compiler and NUL manifest, rule tests and UI acceptance | Implemented in 0.5: size bounds, relative modification window, extension-based file categories, plan/source policies, exact file exceptions and complete paginated test results |
| SQLite identity/index/fingerprints | success-only file_entries plus normalized source_paths/filesystem_objects, volume/file IDs, mtime/ctime/birthtime, generations and SHA-256 versions | Implemented core in 0.7: persistent APFS fingerprint reuse with metadata revalidation and full-audit bypass; authenticated dirty-subtree scans added in 0.13; O(N) projection processing and million-file performance acceptance remain |
| Native FSEvents, replay, durable cursors, loss recovery | adapter live tests, migration 002, watcher/scheduler durable-queue and same-metadata gap tests | Implemented native path; authenticated incremental subtree scans implemented in 0.13; scaling acceptance remains |
| Verified movement and immutable reuse | Compare + curated reference tree + verified within-source moved-file linking; old-history/metadata tests | File movement implemented; APFS cloning, cross-source and whole-tree optimization belong to later performance work |
| Complete staged snapshots, verification, atomic publication, crash recovery | executor, manifest reconciliation; fail/cancel/interruption/orphan tests | Core implemented; complete-file staging recovery implemented in 0.14; intra-file continuation and real unplug/power-loss fault-injection verification remain |
| Retention count/days, latest and permanent pins, preview | explicit retention preview/apply; integration tests preserve latest | Core implemented, including current-pointer protection; daily/weekly/monthly representative tiers implemented in 0.19 (UTC populated calendar periods, additive protection) |
| Interval/change/mount/wakeup scheduling and bounded retries | native events + vnode mount notifications, interval catchup, debounce, checkpoint/backoff tests | Implemented through 0.11: actual-view thresholds, min/max intervals, durable scoped retries and diagnostics; calendar and per-target policies remain; system-idle is later |
| Preview, audit, interrupt, pause/resume and task queue state | true dry-run, persisted stage/logs, cancellation and serial writers | Implemented through 0.6: persisted FIFO queue, queued holds, active pause/resume, process-group cancellation, offline waiting and restart classification; completed-file recovery across restart implemented in 0.14; intra-file continuation remains |
| History/file browsing and safe restore | source filtering/search and single file/directory/source restore; real UI and integration tests | Core implemented; richer history/event discovery and restore interruption controls remain |
| Full recovery from disk metadata | manifests re-import snapshots; interrupted runs identified | Implemented in 0.15 for snapshots carrying full plan configuration: paused plans/rules/schedule/target bindings, historical index, disk pins and fresh live rescan; legacy absent configuration, old jobs/settings/audit are not reconstructable |
| REST + WebSocket | authenticated REST and SSE; UI polls | WebSocket protocol and event-driven UI remain |
| Dashboard, plan editor, preview, history, storage, resource monitor, settings, first-run UX | rendered and browser-tested UI; accessible directory picker | Core pages and first-run guidance implemented; 0.18 consolidates task scans and disk resources with browser-persisted overview customization; fuller platform diagnostics remain |
| launchd + macOS app distribution | embedded binary, .app and zip, ad-hoc signature verified, explicit launchd installer | Build/install path implemented; no-terminal installer UX, upgrade, Developer ID/notarization and broader OS release testing remain |
| Safety matrix and performance targets | Automated Go tests through fingerprint/index .7 work plus real UI backup/restore/native-trigger acceptance | Partial; do not infer full hardware failure or million-file/500GB benchmarks from small isolated tests |
| SSH/NAS, bidirectional sync, advanced move optimization | Not exposed | Explicitly later design phases; not claimed as V1 capability |

See docs/change-detection.md for the native event protocol and docs/backup-semantics.md for the current snapshot boundary. All items marked partial or remaining need further work; a passing test suite for the current subset does not prove the whole design complete.

## Resource monitoring (0.4)

Authenticated `system/monitor` samples macOS IOBlockStorageDriver physical-device read/write counters every second. Rates use elapsed monotonic time and counter deltas, with null during warm-up, counter resets, unavailable sampling and device reconnects. Disk identity uses IORegistry IDs; diskutil classifies internal/external. APFS volumes sharing a physical disk are reported once. These are system-wide device I/O rates, not backup-attributed bandwidth; cached file operations can exceed physical throughput. Devices without driver counters remain unavailable rather than synthetic zero.

The resource page retains two minutes in memory and plots the latest minute. It shows current plan, source/target, workflow stage, logical processing bytes/s, processed bytes, hash path and rsync per-file percentage. The overview and running-detail dialogs also show live activity. Scan/hash, reuse preparation, transfer and verification are distinct stages. The workflow percentage remains a stage indicator, not an estimated byte percentage or ETA. Samples are transient and reset on service restart. Resource monitoring does not add pause/resume or change publication safety.

Validation includes fragmented CR/LF rsync progress, bounded output, real rsync transfer plus SHA-256 verification telemetry, plist 64-bit counters, counter resets, stage isolation, endpoint authentication and native disk smoke sampling. External hardware hot-unplug and broader third-party driver coverage still require actual hardware validation.

Local acceptance: a generated 32 MiB file was backed up through the system rsync with a temporary 1 MiB/s test limit. The authenticated sampler captured 32 live transfer samples, a positive transfer rate and intermediate per-file percentages; the run finished successfully with a verified immutable snapshot. The original rsync setting was restored and the sample plan remains paused. Browser screenshots are `.data/resource-monitor-running.png` and `.data/resource-monitor-io.png`. Native internal-disk I/O was observed; no external disk was connected during this acceptance.

## 0.8.0 磁盘绑定补充

持久化卷 UUID + 卷内相对目录绑定现已在目标编辑和存储空间页展示，离线保留关联计划。调度按目标记录连接转换和待处理触发；同计划轮换硬盘可分别插入，无需同时在线。连接任务在持久化完整计划的同时记录 destinationIds，执行前检查完整计划未变化，然后仅执行选定绑定。调度开关支持离线保存。详见 [磁盘绑定](disk-binding.md)。真实外置硬盘拔插/休眠/断电、跨服务中断续传及其余原设计缺项仍未完成。

## 0.9.0 目录范围备份

手动任务和预览可选择部分启用源目录。新恢复点更新所选目录，沿用各目标上一恢复点中其他目录的内容；首次运行只包含实际选中的目录。队列按计划及目录/目标范围合并同范围请求，不同范围分别持久化排队。单目录成功不清除整计划变更事件，也不重置整计划间隔的成功基准。快照 manifest 记录 updatedSourceIds；执行前仍校验完整保存计划，镜像审批绑定目录范围。原设计其余未完成项继续保留。详见 [目录范围备份](source-scope.md)。

## 0.10.0 源目录身份

新增源根的卷 UUID/目录 ID/创建时间绑定、原目录显式重新绑定、外置卷挂载名称重新定位、源离线持久等待与身份替代拒绝。扫描前后、传输与发布前均核对绑定，运行时重新检查跨计划目标重叠。编辑页提供只读身份检查；旧计划必须明确保存一次建立首次绑定，避免静默绑定替代目录。等待运行/队列状态采用原子事务。详见 [源目录身份](source-binding.md)。自动按源拆分等待、真实硬件故障验证、增量子树和跨重启续传等原范围缺项仍未完成。

## 0.11.0 智能调度策略

变化数量/逻辑大小双阈值、最短变化间隔、最长强制完整检查、真实指数退避重试和次数上限均已实现，并显示最近检查与等待原因。阈值基于净目标视图而非事件数量，跨目标去重；最长检查无变化不生成恢复点。单目录/部分目标成功不延后整计划时钟。重试保留原目标范围，手动失败与确认状态不自动重试。详见 [智能调度](smart-schedule.md)。日历时刻/每目标策略、增量子树、跨重启 staging 续传、硬件与规模验收等缺项继续保留。

## 0.12.0 首次使用

增加只读环境/目录/目标权限检查和首次备份引导。新计划界面默认暂停，保存后自动打开真实预览；已有计划状态不变。历史无计划时仍可发现与浏览。检查不创建目标探针文件、不读取源文件内容，也不启动备份或修改绑定。详见 [首次使用](first-run.md)。无终端安装、升级/公证及原设计其他缺项仍未完成。

## 0.13.0 变化子树扫描

可信 FSEvents 队列和 APFS 源投影均经过私有密钥 HMAC 校验；只枚举脏路径父目录及必要祖先，其他路径沿用已完成索引。完整投影包含性校验能发现路径行丢失/删改，事件队列校验防止丢失事件被静默认可。规则/源身份变化、根事件、挂卸载、事件丢失、无原生覆盖、日期筛选及每小时审计回退全扫。部分提交代次冲突会重做完整扫描，取消不替换已完成索引。预览与设置显示实际访问路径与沿用路径，不将沿用路径计作本次内容读取。SQLite 投影读取、认证与更新仍为 O(N)，目标仍完整校验；没有百万文件或 500GB 性能结论。详见 [变化子树扫描](subtree-scanning.md)。跨重启 staging 续传、完整元数据重建、真实拔盘/断电等原范围缺项继续保留。

## 0.14.0 中断暂存文件恢复

新运行可以从兼容中断暂存中恢复已完整传输的普通文件。持久化标记只定位候选，每个候选和当前源均重新进行内容核对；克隆/复制到独立新暂存，完整校验后发布。原运行保持中断审计状态，新运行记录恢复数量。真实 SIGKILL、原服务重启与新运行恢复流程已验收，修改旧暂存不会影响新恢复点。详见 [暂存恢复](staging-recovery.md)。单文件中途续传、旧暂存清理界面和真实硬件断电/拔盘仍未完成。

## 0.15.0 配置与历史恢复

新清单保存完整计划，部分目录/部分目标运行也保留完整配置。永久保留标记独立原子写入磁盘；新设置流程先完整校验预览，再一次性恢复暂停计划、历史文件索引、保留状态与审计。已有计划不覆盖，冲突/过期/内容变化拒绝提交，损坏或缺失保留标记保守保护。实时索引需对实际源完整重扫后重建，不信任历史物理缓存。详见 [配置恢复](configuration-recovery.md)。单文件中途续传、恢复进度取消界面、安装体验、硬件与规模验收等原范围缺项继续保留。

## 0.19 分层保留

计划新增 keepDaily / keepWeekly / keepMonthly，默认 0，不改变旧计划行为。每个目标独立选择最近 N 个有快照的 UTC 日、ISO 周（周一开始）、月，每期最新的一份受到额外保护；空缺周期不占名额，三个层级取并集。最新恢复点、当前视图和永久保留继续保护。原最近份数/最大天数条件保持原语义，超出任意条件且没有保护才可进入清理候选。预览和提交使用相同计算，提交重新读取最新策略，新增保护会使旧删除请求失败。分层数量限定 0–1000，非法策略拒绝清理。新增字段存于既有 JSON 计划及清单，无表结构变更；老清单缺省为 0，配置恢复已覆盖新字段。

验证：完整 `go test -race ./...`、`go vet ./...`、前端类型检查和生产构建通过。测试覆盖 UTC/ISO 跨年分组、相同时刻确定性、保护并集与原策略兼容、永久保留和当前视图、策略变更拒绝旧清理请求，以及从备份清单恢复分层配置。浏览器使用 18787 独立测试数据保存 7/4/12 设置并核对清理页；未清理用户快照。其余原设计未完成项继续保留。

## 0.19.1 恢复故障处理（候选版）

恢复前读取全部计划失败时立即拒绝，避免缺失跨计划源/仓库排除信息后继续写入。最终恢复审计保存失败不再返回成功，错误包含运行 ID 与输出路径；已完成输出保留。请求取消导致恢复失败时审计记录 cancelled。新增数据库损坏与终态审计写入失败注入测试，验证不产生未经路径校验的输出、不丢失已完成恢复文件，并释放服务忙状态。完整 race 测试和 go vet 通过（.data/test-race-0.19.1.log）。候选包位于 .data/package-0.19.1；本机因锁屏尚未执行界面启动验收，生产继续运行 0.19.0。持久异步恢复、恢复进度和显式取消界面仍未完成。

## 0.20 恢复中断审计与记录页（候选版）

恢复输出路径在 mkdir 前持久化；写入失败不创建输出。重启时仅将文件恢复的 running 审计标为 interrupted，保留输出路径，不删除任何部分文件，不改写其他类型审计或已完成记录。兼容缺少 type 的旧恢复记录。GET /api/v1/restores 返回最近 50 条文件恢复审计；任务中心新增恢复记录页，显示成功、取消、失败、中断以及输出位置。完整 race 测试、go vet、前端类型检查与构建通过。真实隔离测试使用 .data/ui-0.16 与 .demo-restores/ui-0.20：慢速恢复请求超时取消，随后系统 rsync 恢复成功；浏览器核对两种记录与布局，截图 .data/ui-0.20/restore-history.png。原生应用启动验收仍待解锁，生产保留 0.19.0，候选包 .data/package-0.20.0 包含 0.19.1 修复。异步恢复、阶段进度和显式取消按钮仍未完成。
