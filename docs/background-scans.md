# 后台扫描、目录创建与运行控制

从 0.16 起，新计划保存后启动后台预览扫描，编辑器随即关闭。扫描只建立索引与准备预览，不会自动执行备份；关闭弹窗、切换页面或关闭浏览器都不会取消扫描。页面顶部显示扫描状态、已检查文件数、实际内容读取量，并提供取消或查看预览入口。手动预览窗口也可点击「放到后台」。首次内容校验仍需读取所选文件，此功能消除界面阻塞，并不承诺首次扫描不读取大文件。

扫描结果与状态保存在 SQLite 的 preview_jobs 表（migration 008）。同一计划、配置和目录范围的活动扫描合并；最多排队 20 个、同时执行 1 个。请求断开不影响后台任务；取消通过独立 context 传递给扫描与 rsync dry-run。服务重启后将未完成扫描标记为已中断，可重新扫描，绝不自动开始备份。变更配置的旧扫描不能签发可用结果。预览授权沿用 10 分钟、单次使用和运行时完整复核规则。

目录选择器提供「新建文件夹 → 创建并进入」。后端重新验证浏览时绑定的卷 UUID、目录 ID、创建时间，以 no-follow 目录描述符执行 mkdirat，拒绝已有名称、路径穿越、链接替代、服务数据、源目录及仓库内部位置；创建操作记录审计。目标选择还将尚未保存的源目录作为禁止重叠路径。

计划卡片区分「自动备份已暂停，仍可手动备份」与本次运行暂停。活动任务可直接暂停、继续、取消或打开详情；失败、中断或取消后可重新预览并重试。取消保留已有历史，不在历史快照原地重跑。

Finder 可能在用户浏览的暂存目录生成 .DS_Store。执行器只在本次新建、尚未提交的 staging 中清理未被清单选择的普通 .DS_Store 文件，然后继续严格的完整布局、内容和属性校验。选中的源 .DS_Store 保留；符号链接、目录或其他清单外文件仍会失败。历史快照的校验规则不放宽。

API：POST /plans/{id}/preview-jobs；GET /preview-jobs（不返回大型变更列表）；GET /preview-jobs/{id}；POST /preview-jobs/{id}/cancel；POST /filesystem/directories。原同步预览接口保留供兼容客户端使用。

本机验收只使用 .demo/ui-0.16、.demo-targets/ui-0.16 与 .data/ui-0.16，独立端口 18787。验证真实目录创建、后台扫描及取消、暂停/继续/取消、重试成功、模拟 Finder 缓存与源/目标 SHA-256 一致性。结果见 .data/ui-0.16/acceptance.json，截图 new-folder.png 与 paused-card.png。

## 0.21 preview latency investigation

Preview now reports identity resolution, index preparation, source scanning, target metadata, rsync preflight, and result composition separately. A slow-preview watchdog writes private local goroutine stacks after 10 seconds. Destination validation first checks the saved path/UUID and enumerates alternate mounts only if that fails. Rsync dry-run uses the allowlisted manifest with an empty disposable target instead of constructing a historical hardlink reference. This avoids historical-file checksum reads performed by rsync against link-dest during preview; execution still performs real reuse selection and final verification. A preview remains a proposed change list, not an integrity audit of old content.

Local investigation on 0.19/0.21 found a separate blocking issue: the photo source stopped before its first file, in os.ReadDir -> syscall.Open. A Python directory enumeration also blocked and ended with EINTR. Finder could enumerate the directory immediately. The LocalBackup full-disk-access switch was observed off; this is a diagnostic lead, not proof of causation. No permission was changed. Full production speed comparison is pending resolution of that access wait. No backup or retention operation was started during this investigation. Automated race tests including dry-run, volume binding, snapshot immutability and background previews passed (.data/test-race-0.21.0.log).

Follow-up 2026-10-22: the user authorized full disk access for LocalBackup and supplied a screenshot with its toggle enabled. After restarting the native app, service 0.21.0 became ready, but the photo preview still remained at zero paths/bytes in directory open. A second native restart reproduced the same symptom (sample: .data/permission-restart.sample). Full disk access alone has therefore not resolved this wait; the pending file-access prompt identity and disposition still need confirmation. No additional application permissions were granted.

## 0.21.2 目录枚举的有界等待

源扫描的每次目录枚举最多等待 30 秒。若系统调用长期不返回，扫描报告目录读取超时；用户取消时立即返回取消，不再无限停在“正在取消”。读取失败不会当作空目录，也不会生成可执行预览。扫描仍按目录名顺序处理，不跟随目录符号链接，保留筛选 SkipDir 行为。

macOS 的已进入内核的 open 无法由 Go context 强制撤销，因此底层只读调用可能继续等待。全进程最多保留 4 个未返回的目录读取调用，避免反复取消/重试无限累积线程；迟到结果不能继续扫描或提交索引。该限制只覆盖源目录枚举，不代表所有 stat、文件内容读取、外置盘系统调用都已有超时保障，也没有修复系统调用阻塞的根因。

测试注入永不返回直到释放的枚举器，验证取消、超时、资源上限；与标准 WalkDir 对照验证顺序、文件/目录 SkipDir 和不跟随链接。全套备份安全测试继续执行。

本机 0.21.2 验收：同一照片源目录触发超时，30.098 秒转 failed 并返回目录路径；第二次进入目录读取后取消，0.002 秒转 cancelled。证据 .data/directory-wait-acceptance.json，用户原有标签页已刷新，任务中心显示“扫描已取消”和“重新扫描”，截图 .data/ui-0.21.2/cancelled.png。根因尚未解决；测试后重启服务清除未返回的底层只读调用。


### 目录读取重试保护（源码待发布）

源扫描与目录选择器共享最多四个尚未完成的目录系统读取。同一规范化路径的并发请求共享正在进行的读取；任何等待方取消或超时后，后续对同一路径的请求会明确提示“上一次系统读取仍未返回”，不再发起重复系统调用。系统调用完成后立即移除记录，下一次请求必须重新读取，不缓存已经取消请求的迟到目录清单。等待读取槽位超时会明确说明尚未开始读取该目录，与目录本身读取超时区分。

此保护避免反复重试同一个卡住的目录耗尽全部槽位；四个不同目录同时永久阻塞仍会占满上限。它不解除 macOS 授权等待，不应被表述为系统问题已经修复。取消前尚未进入遍历时，也先检查取消状态，避免启动新的根目录 lstat。

## 0.21.5 预检查成功结果复用与 AI 模型实测

仅手动/后台预览可复用进程内十分钟有效、最多 64 条的成功 rsync 预检查。每次仍完成源身份解析、索引扫描和目标检查；完整清单（含内容哈希、属性及物理版本）、规则/范围/排除项、目标及基线摘要、rsync 版本和可执行文件物理身份任一变化均不命中。失败或取消不缓存，重启不保留。批准记录仍逐次生成、限时、单次使用。实际执行强制重新运行完整 rsync 预检查，不读取或填充该缓存；已有目标基线完整性与暂存发布前验证保持不变。

2026-10-23 已完成原生应用更新至 0.21.5，保留旧应用及一致 SQLite 备份。正式 AI 模型计划实测（38,356 个普通文件指纹，预览含链接共 38,362 项）：更新前完整预览 21.451 秒，其中 rsync 预检查约 18.488 秒；更新后首次建立缓存 28.084 秒，随后两次命中缓存分别 3.068、2.994 秒，平均 3.031 秒，相比更新前该次测量约快 7.08 倍。命中时预检查阶段约 0.063–0.066 秒。均为零变更、内容读取 0 B；阶段耗时由轮询近似得到，完整耗时包含 API 请求和轮询。首次建立缓存、缓存过期/失效和应用重启后仍会执行全量预检查，不能承诺首次预览也达到约三秒。

界面另实际点击 AI 模型预览，核对完成且零变更。原计划配置与历史快照未改变，本轮未执行个人计划备份。`go test -race ./...` 与 `go vet ./...` 通过；回归覆盖源内容与程序替换失效、规则/身份/范围/属性及基线键、过期与容量、失败/运行中取消不缓存、每次独立批准，以及实际备份在缓存命中后仍能因新预检查失败而拒绝发布。

证据：`.data/preflight-cache-0.21.5-acceptance.json`、`.data/preflight-cache-0.21.5-ui.png`、`.data/preflight-cache-0.21.5-tests.log`；回退位置 `.data/preflight-cache-rollback-location.txt`。

## 0.21.6 预览只检查工具能力，不向 rsync 提交真实清单

进一步取消手动/后台预览中的全量用户文件预演。每次仍由 Go 重新检查源身份、文件版本/属性及规则、指纹索引、目标仓库和基线，再生成预览与独立批准记录。rsync 只接收临时生成的固定小样本：一个子目录、一个很小的 UTF-8 文件和一个相对符号链接，以实际传输参数验证工具能力，命令等待限制五秒。工具能力结果按程序物理身份/版本缓存十分钟；首次、过期、程序替换和重启仅重做小样本，源/规则变化不再触发全量 rsync 预演。实际备份的全清单 rsync 预检查、目标基线完整审计和暂存发布前验证保留。

已原生更新本机到 0.21.6 并保留旧应用及一致 SQLite 备份。AI 模型计划实测：首次工具检查未命中缓存，完整预览 10.469 秒，其中 Go 源扫描约 9.323 秒；随后两次 2.960、2.952 秒。首次也不再存在此前约 18–19 秒的 rsync 全量预演。各次为零变更、复用 38,356 个普通文件指纹、Go 内容读取 0 B，原计划配置和历史快照未变。本轮未执行个人计划备份。耗时是 API 调用和轮询观察值，不同测次文件系统与数据库缓存状态未强制一致，不能保证每次首次扫描都为相同耗时。

`go test -race ./...`、`go vet ./...` 通过；补充针对性竞态测试验证用户源/目标路径不传给预览 rsync、文件和规则变化仍正确扫描、程序替换/能力缓存过期重做小样本、失败及运行中取消不缓存，实际备份不能复用预览能力检查来跳过全清单预检查。

证据：`.data/lightweight-preflight-0.21.6-acceptance.json`、`.data/lightweight-preflight-0.21.6-tests.log`、`.data/lightweight-preflight-0.21.6-focused-tests.log`；回退位置 `.data/lightweight-preflight-rollback-location.txt`。

## 0.21.7 Go 扫描分析与索引提交优化

增加源扫描细分计时：身份解析、索引读取、遍历、属性读取、首次/最终 stat、索引提交和排序。属性及 stat 是遍历的子项，不能与遍历重复相加。历史单次约 9.323 秒未能在本轮稳定复现，不将其全部归因为某一函数，也不据此宣称数倍提速。

优化缓存命中分支，删除紧邻最终版本检查的重复 Lstat，仍保留观察前的物理版本和属性/指纹处理后的最终 Lstat 比较。SQLite 提交按最多 256 条读取已有版本并批量登记已见路径，未变化且认证有效的对象/路径不再逐条执行重复 UPSERT。仍在同一事务内验证代次、认证指纹、重新计算投影认证、清理消失路径和提交；强制内容复查仍刷新验证时间。没有采用实验性的增大 SQLite 缓存或 mmap 设置，没有跳过扩展属性。实际执行前完整预检查、基线审计和 staging 发布前验证不变。

AI 模型源、隔离数据库副本、相同扫描范围的诊断对比：优化前 2.344 秒，其中索引读取 0.137 秒、提交 0.667 秒；优化后 2.033 秒，其中读取 0.133 秒、提交 0.461 秒。整体约减少 13.3%，提交约减少 30.9%；两次均访问 42,305 个路径、复用 38,356 个普通文件指纹、内容读取 0 B。这是单次顺序测量，文件系统缓存和运行负载可能影响结果。

本机原生应用已更新到 0.21.7。更新后的第一次正式预览因桌面目录读取等待而在 30 秒超时，尚未读到文件；TCC 日志明确记录旧代码要求匹配失败并重新请求桌面权限，随后出现授权记录更新事件。未修改系统权限或重启安全服务。再次验收连续成功：完整预览 4.868、2.601、2.546 秒，后两次均值 2.574 秒；源扫描细分总计约 2.0 秒，其中遍历 1.316–1.330 秒、索引读取 0.112–0.129 秒、索引提交 0.441–0.473 秒、排序 0.076–0.078 秒。首次目标元数据及工具检查较慢，不能与源扫描混算。原计划配置和历史快照一致，本轮未执行个人计划备份。

`go test -race ./...`、`go vet ./...` 通过。新增回归覆盖缓存命中后、最终版本检查前发生的等长内容替换并恢复 mtime、符号链接替代、权限/扩展属性变化和删除，均拒绝提交；600 条索引在第二批故障时，前批与代次全部回滚。原有防篡改、取消、全量重扫和备份发布安全测试继续通过。

证据：`.data/scan-profile/before.json`、`.data/scan-profile/optimized.json`、`.data/scan-optimization-0.21.7-acceptance.json`、`.data/scan-optimization-0.21.7-tests.log`、`.data/scan-optimization-access-current.log`；回退位置 `.data/scan-optimization-rollback-location.txt`。

## 0.21.8 复用父目录规则判断，避免重复匹配

继续拆分遍历耗时后，同一 AI 模型源一次测量中，1.346 秒遍历包含约 0.824 秒筛选规则匹配，目录枚举约 0.140 秒，初始 stat 0.093 秒，扩展属性 0.058 秒，最终 stat 0.076 秒。过去将全部时间称为目录遍历，并不代表主要成本是目录读取。上述字段均为遍历子项，统计与运行负载存在开销和波动。

试验了同目录文件元数据的有界并发预读：保留串行选择与最终版本检查，只并行初始 stat/属性；两个线程、最多 256 项预读，在关闭 CPU/trace 采集后的三轮交替对比中，完整 Go 扫描为 2.412、2.387、2.433 秒，串行为 2.019、1.997、1.923 秒。最初四线程试验也没有收益。因此未采用并发实现，实验源码和结果仅保留在忽略目录 `.data/scan-profile`，没有运行时开关或后台线程留在产品中。

最终在单次、单源串行扫描内复用父目录的纯路径规则判断，最多保留 4,096 条，达到上限时清空并重新计算。规则不可变且每次扫描重新构造匹配器，不跨源/计划/扫描复用。排除父目录的阻止语义、规则先后顺序、单文件例外及其所需祖先、隐藏文件、中文路径、大小/类型/时间筛选和原因文本保持一致。每个文件自身仍匹配规则和筛选，所有文件系统状态、扩展属性、指纹认证、最终物理版本检查、索引事务以及实际执行安全验证保持原样。

同一源与隔离数据库、关闭 CPU/trace 采集的四轮交替测量：原扫描平均 1.950 秒，优化后 1.314 秒，约减少 32.6%；遍历平均 1.298 秒降至 0.650 秒，约减少 50.0%。八次完整选中/忽略清单 SHA-256 完全相同，均访问 42,305 个路径、复用 38,356 个普通文件指纹、内容读取 0 B。规则微基准从约 18,347 ns/文件、8 次分配降至 3,492 ns/文件、稳定复用阶段零分配；微基准不是完整预览耗时。

`go test -race ./...`、`go vet ./...`、前端与原生打包签名检查通过。新增规则等价测试覆盖乱序访问、各种文件类型/大小/时间、例外、排除祖先、规则优先级及缓存上限/清理/新策略。原缓存命中后变更内容并恢复 mtime、链接替代、权限/扩展属性及删除回归扩展至已有同目录兄弟文件的嵌套路径，确认父规则复用不会复用文件元数据，失败仍不提交索引。

本机已更新到 0.21.8 并保留旧应用与一致数据库备份。更新后 macOS 再次请求桌面/可移动卷访问，首次验收在目录访问等待中超时，用户确认已允许后重新验证。首次等待证据 `.data/traversal-optimization-0.21.8-initial-access-wait.json`；正式验收结果另存 `.data/traversal-optimization-0.21.8-acceptance.json`。

证据：`.data/scan-profile/compare-prefetch.json`、`.data/scan-profile/detail2-timing.json`、`.data/scan-profile/compare-rules.json`、`.data/scan-profile/rule-cache-benchmark.log`、`.data/traversal-optimization-0.21.8-tests.log`、`.data/traversal-optimization-0.21.8-mutation-tests.log`；回退位置 `.data/traversal-optimization-rollback-location.txt`。

用户允许系统访问后，正式 AI 模型计划连续三次验收成功：完整预览 2.019、1.877、1.875 秒；遍历 0.691、0.695、0.660 秒。后两次完整预览平均 1.876 秒，前版后两次平均为 2.574 秒（不同测次的文件系统缓存/负载未强制一致）。三次均为零变更、38,356 个普通文件指纹命中、Go 内容读取 0 B；原计划配置与历史快照一致，未执行个人计划备份。
