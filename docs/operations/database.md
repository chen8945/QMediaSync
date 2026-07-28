# 数据库运维

> 职责：说明数据库引擎、初始化、修复、清库、备份和恢复的运行维护方式。
>
> 权威范围：本文档维护运行操作；表、字段、时间策略和版本迁移见 [数据库 schema 与迁移](../reference/database-schema.md)。
>
> 修改时机：修改数据库引擎选择、初始化流程、修复 / 清库接口、备份恢复实现或操作限制时必须更新本文档。
>
> 相关代码：`backend/internal/db/`、`backend/internal/models/migrator.go`、`backend/internal/models/backup.go`、`backend/internal/backup/`、`backend/internal/controllers/backup.go`。

## 引擎与初始化

QMediaSync 支持 SQLite 和 PostgreSQL，默认 `postgres + embedded` 由程序启动内嵌 PostgreSQL。数据库配置通过 `config/config.yaml` 保存；首次配置、端口和外部 PostgreSQL 要求见 [配置、密钥与日志](configuration.md)。

首次启动时，如果 `migrator` 表不存在，`InitDB()` 创建所有表、写入当前版本、初始化默认设置、刮削设置和 Emby 配置。首次空库直接初始化到当前结构版本，不逐个回放历史迁移；首个管理员通过启动日志中的初始化码创建。

已有数据库启动时，`Migrate()` 按 `migrator.version_code` 顺序执行补丁并逐步推进版本。新增或修改表、字段和迁移时必须同时更新 [数据库 schema 与迁移](../reference/database-schema.md)。

## 修复与清库

`POST /api/database/repair` 调用 `RepairDB()`，对 `AllTables` 执行 `AutoMigrate` 并修复 PostgreSQL 主键序列：缺失表、字段和索引会补齐，不主动删除已有数据。

`POST /api/database/delete-all-table` 调用 `BatchDropTable()` 删除 `AllTables` 中的全部表，属于高风险清库操作。执行前必须确认备份可用，并在维护窗口内操作。

## 备份

备份配置存储在数据库的 `backup_config`，不在 `config/config.yaml`。默认自动备份关闭；默认 Cron 是 `0 3 * * *`，默认保留 7 天、最多保留 10 份。服务启动时会按“已启用且 Cron 非空”创建定时任务；每次保存备份配置都会停止并按最新配置重建 Cron，因此启用、禁用和 Cron 修改在当前进程立即生效。

备份和恢复由 `internal/backup` 的操作协调器互斥：同一进程最多一个实际操作，状态、一次性令牌散列和阶段日志写在 `config/state/backup/`，位于业务数据库和恢复可覆盖的白名单之外，因此恢复覆盖配置和数据后仍能读出准确终态。协调器只保留最近一次操作，下一次备份或恢复取得执行权时原子替换，不提供历史审计。

工件写入配置目录下的 `backups/`，命名为 `backup_<类型>_<时间>_<工件标识前 8 位>.zip`。工件是版本化的 v1 格式：外层 ZIP 只含 `header.json` 与 `payload.bin`，内层 ZIP 含 `manifest.json`、按稳定表 ID 命名的 `data/<表名>.jsonl`，以及白名单文件 `config/config.yaml`、`config/config.yml`、`config/encryption.key`、`config/server.crt`、`config/server.key` 和 `config/logs/` 内的常规文件。`config/.env` 不纳入备份。每条 JSONL 记录按 GORM 持久化列编码，因而 API 响应中隐藏的哈希、密文和会话列也会随受保护工件恢复；预检会拒绝缺列或未知列的记录。清单记录每个文件的字节数与 SHA-256、记录数、应用与 schema 版本、源引擎和表目录版本；导出使用的表目录由 `AllTables` 派生，排除仅保存本机工件索引的 `BackupRecord`。

手动备份的密码只来自本次请求，绝不复用定时备份密码；留空表示创建未加密工件，必须在同一请求带上显式确认。**未加密工件包含 `config/encryption.key`，启用内置 HTTPS 时还包含 TLS 私钥 `config/server.key`**，必须按密钥材料保管。定时备份密码以本机 AES-GCM 密文保存在 `backup_config`，只有 Cron 触发的备份会解密使用它，任何读取响应都不返回该密文。密码非空时长度至少 10 个字符，且同时包含大写英文字母、小写英文字母和数字，不允许任何空白字符。

导出在维护屏障和任务静止之后，从单一读事务的一致视图进行：PostgreSQL 使用 `REPEATABLE READ READ ONLY`，SQLite 使用 WAL 下的读事务。全部数据和白名单文件先写入 `config/tmp/backup-export/` 的受限暂存目录，通过完整性校验后才原子发布到 `backups/`，因此中途失败不会在备份目录留下半成品。

备份成功后才执行保留清理：只有应用生成的 `manual`、`auto` 以及升级迁移标记的 `legacy` 记录参与保留天数和最大数量计数，目录导入的 `imported` 和上传暂存的 `temporary_upload` 不会被自动删除。应定期把完成的工件复制到配置目录之外的独立存储，避免把唯一备份与运行数据放在同一磁盘。

备份和恢复期间，认证前的维护中间件对全部业务 API、登录和 Webhook 返回 HTTP `503`；只有静态资源和 `GET /api/backup/status` 例外。状态查询以 `operation_id` 查询参数和 `X-Backup-Operation-Token` 请求头读取外部状态文件，不触碰业务数据库，响应固定 `Cache-Control: no-store`。明文令牌只在受理响应中返回一次。

进入维护前，协调器会先关闭进程内的共享任务准入，再停止同步任务、上传队列、下载队列、目录上传、STRM 生成、Emby 刷新和各类 Cron，并等待它们**实际静止**——停止请求本身不作为静止证明。该准入屏障覆盖传输队列的 HTTP 重启和下载并发调整、同步入队、新来源队列、目录扫描、目录监控上传任务持久化、STRM 入队、Emby 条目同步与媒体库刷新任务的提交/领取，以及 Cron 重建；已处于 `refreshing` 的 Emby 媒体库任务也会计入等待，避免等待期间由事件或重启接口重新启动 worker。传输队列重启入口在此窗口返回 HTTP `409`。人工备份遇到运行中的任务时以 HTTP `409` 返回任务摘要，既不进入维护也不等待；定时备份取得执行权后先清空 `config/tmp/backup-restore/` 的上传暂存并使其预检记录失效，再最多等待半小时，超时以 `cancelled` 结束且不恢复已清理的暂存。若协调器已被占用，定时备份直接跳过本次任务且不做任何清理。实际导出没有总执行时限，避免大数据量或慢磁盘在安全切换中被强制中断。

## 恢复与风险边界

恢复只接受 v1 ZIP 工件：已有备份和上传暂存均复用既有 `POST /api/backup/restore` 或 `POST /api/backup/upload-restore`，不增加路由。两者都分为两阶段：`phase=preflight` 完成密码、格式、完整性、实例绑定和目标数据库可恢复性验证，不进入维护也不写业务数据；随后前端展示目标并提交 `phase=confirm`、一次性 `preflight_id` 和完整覆盖确认。确认成功交给协调器后才返回 HTTP `202 Accepted` 与一次性状态令牌，表示任务已受理而非已经完成。旧格式 ZIP 仍可下载，但常规恢复固定拒绝。

确认后的恢复先创建不可下载的预恢复快照，再写入备份配置指定的同引擎目标：SQLite 在同文件系统临时数据库验证后原子切换，PostgreSQL 在单一事务中清空、导入、失效浏览器会话并修复序列。工件的白名单配置、证书/私钥和日志按清单精确镜像；任何快照完成后的失败都会自动回滚数据库和文件。恢复成功、已自动回滚或回滚失败后，进程会有序退出；新进程由部署平台或操作者决定何时启动，应用不会请求或配置平台重启。

恢复期间由协调器启用维护屏障，业务 API 返回 HTTP `503`；只有带有 operation ID 和状态令牌的状态查询可用。新进程启动时会从终态和阶段日志收敛结果。新增或修改模型字段会影响备份恢复行为，必须同时更新 [数据库 schema 与迁移](../reference/database-schema.md)。

## SQLite→PostgreSQL 迁移

从内嵌 PostgreSQL 切换到外部 PostgreSQL 时使用独立的迁移协议，不调用常规备份恢复入口，也不接受普通 v1 备份工件作为跨引擎迁移工具。迁移包固定为 `backups/migrate.zip`，以 `migration` 清单记录共享主表目录中的全部应用表、记录数和 SHA-256；导出先在受限暂存目录完成验证，再原子发布该包。迁移包含 `BackupRecord`，而常规备份恢复不包含它。

外部 PostgreSQL 启动时只会导入通过迁移包预检的精确表集合；缺表、表名/清单漂移、JSON Lines 损坏或任一插入错误都会终止迁移。目标库的导入、schema 升级和主键序列修复全部成功后才删除 `migrate.zip` 并继续正常启动。任一步失败都会保留该包、阻止业务服务启动；修复目标库或包问题后，下次启动从同一包重新迁移，绝不把部分导入的数据库当作可用服务。

## 启动期状态收敛

每次启动会在连接数据库之前读取 `config/state/backup/` 的最近一次操作，并按其状态收敛：

- 非终态的备份直接记为失败，它不改动业务数据。
- 非终态的恢复若尚未记录 `snapshot_ready` 阶段，说明数据库和白名单文件都没有被改写，同样只记为失败。
- 非终态的恢复已经记录 `snapshot_ready` 时，程序用 `config/state/backup/rollback/<操作标识>/` 的预恢复快照执行幂等自动回滚：先还原备份配置指定的目标数据库，再精确还原 `config.yaml`、`config.yml`、`encryption.key`、`server.crt`、`server.key` 和 `config/logs/`，包括删除恢复过程中新增的同类文件。回滚成功后终态记为“恢复失败、已自动回滚”，服务正常启动；配置被还原时会重新加载配置再连接数据库。
- 自动回滚失败或快照缺失时，本次启动**只提供备份状态诊断**：不连接数据库、不启动任何业务运行时，全部业务 API 由维护屏障返回 HTTP `503`，只有携带 operation ID 与令牌的 `GET /api/backup/status` 可用。下一次启动会继续尝试安全回滚。

预恢复快照只由新进程在终态可靠读出后删除，任何情况下都不要手动清理 `config/state/backup/`。启动收敛完成后，程序还会清空 `config/tmp/backup-restore/` 的上传暂存与 `config/tmp/backup-verify/`、`config/state/backup/work/` 的中间文件，并在后台触发一次备份目录清点，不阻塞页面打开。
