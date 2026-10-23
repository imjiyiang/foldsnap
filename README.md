<p align="center"><img src="assets/brand/app-icon.png" width="144" alt="Foldsnap icon" /></p>
<h1 align="center">Foldsnap · 留档</h1>
<p align="center">留一份过去，安心向前。<br/>Local-first, verified snapshot backups for macOS.</p>
<p align="center"><a href="LICENSE">MIT</a> · <a href="docs/backup-modes.md">备份模式</a> · <a href="docs/development.md">开发指南</a> · <a href="docs/brand.md">品牌与兼容性</a></p>

Foldsnap 是基于 Go、React、SQLite 和 rsync 的 macOS 本地备份工具。将选定目录备份到自己掌控的磁盘，保留可恢复的历史版本，无需账号或云服务。

## 为什么使用 Foldsnap

- **先预览，再备份**：查看新增、修改、移动和删除，任务可暂停、继续、取消。
- **三种模式**：快照、保留消失路径的增量同步、需要删除确认的镜像同步。
- **完整验证后发布**：SHA-256 内容校验、源与目标身份检查、私有暂存、持久化和原子提交。
- **复用未变化文件**：不可变历史、硬链接复用和经过完整性校验的指纹索引，减少复制与重复读取。
- **可恢复的版本**：浏览和搜索历史，恢复到新位置，预览后再清理历史。
- **本机自动化**：FSEvents、定时与磁盘连接触发、持久化队列、离线等待和失败退避。
- **清楚看到工作量**：文件进度、内容读取、磁盘读写及运行日志。

## 构建与运行

需要 macOS、Go **1.26.4+**、Node.js **22.12+ 或 24+**、npm 和 Xcode Command Line Tools。macOS 自带 openrsync 可用；Homebrew rsync 3.x 可提供扩展属性支持。已构建应用不依赖 Node 或 Go。

```sh
git clone https://github.com/jiyiiang/foldsnap.git
cd foldsnap
make run
```

`make run` 使用项目 `.data` 和本机端口 `18787`，与正式应用配置隔离。也可双击 **启动 Foldsnap.command**。默认正式配置不会自动导入开发环境。

```sh
make test       # Go race detector 与真实 rsync 集成回归
make check      # go vet、TypeScript 和前端构建
make package    # release/Foldsnap.app 与 Foldsnap-macOS.zip，当前 CPU 架构
```

打开 `.app` 后由原生菜单栏入口启动本机服务和浏览器界面。默认本机端口为 `8787`，占用时选择可用端口；服务仅监听 loopback，并使用访问令牌、Host 与 Origin 校验。

当前应用是 **ad hoc 签名，尚未完成 Apple 公证**。macOS 可能要求确认运行及目录访问权限；签名更新也可能重新请求权限。请保留系统保护，通过系统设置核对权限。

## 首次使用

新建计划 → 选择源和目标 → 检查规则 → 预览 → 运行首次备份 → 浏览历史并试恢复。新计划默认暂停，验证后再启用自动任务。

**备份不是文件系统冻结快照。** 正在运行的数据库、虚拟机等应先关闭或导出；卷在读取期间仍可能发生变化。工具会校验，但不能承诺应用级一致性或代替独立备份副本。尚无加密、远程云后端或单文件中途续传；大规模、拔盘和断电场景仍需持续验证。

## 从 LocalBackup 升级

Foldsnap 是本项目原 LocalBackup 的新名称。为兼容已有计划和历史，保留：

- `~/Library/Application Support/LocalBackup` 配置与 SQLite；
- 备份盘上的 `LocalBackup` 仓库目录及其数据格式；
- 已绑定的计划、卷、目录身份和应用标识。

**不要为了改名重命名备份目录或迁移真实数据。** 升级前先退出旧实例，避免同时启动两份应用。安全链接含本机访问令牌，请勿发布或分享。

## 文档

[备份语义与限制](docs/backup-semantics.md) · [备份模式](docs/backup-modes.md) · [首次引导](docs/first-run.md) · [系统授权与扫描阻塞](docs/scan-regression-investigation.md) · [磁盘绑定](docs/disk-binding.md) · [源身份](docs/source-binding.md) · [智能调度](docs/smart-schedule.md) · [架构](docs/architecture.md) · [API](docs/api.md) · [少量变化性能](docs/sparse-change-performance-0.21.12.md)

## 许可证

[MIT](LICENSE) © 2026 Jiyiang。第三方组件保留各自许可证。图标来源及视觉资产说明见 [assets/brand](assets/brand/README.md)。
