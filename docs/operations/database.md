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

SQLite 连接使用 WAL 日志模式、`synchronous = NORMAL` 和 10 秒 `busy_timeout`，并且连接池固定为一个连接（`MaxOpenConns` 和 `MaxIdleConns` 均为 1，连接不设置存活和空闲上限）。SQLite 同一时间只允许一个写事务：多连接时，事务读取快照之后如果其他连接提交了写入，本事务的写升级会立即失败并返回 `SQLITE_BUSY`（快照冲突），`busy_timeout` 不会重试这种错误。单连接把所有语句串行化到进程内的连接锁上排队，写入不再返回 `database is locked`。`MaxOpenConns` 不适用于 SQLite 引擎，只作用于 PostgreSQL。

因此 SQLite 下的写事务体内不得再通过全局 `db.Db` 发起新语句，必须使用事务自身的 `tx`：单连接会让这种嵌套语句等待自己持有的连接而死锁。PostgreSQL 连接池仍按 `config/config.yaml` 的 `maxOpenConns` 和 `maxIdleConns` 配置。

首次启动时，如果 `migrator` 表不存在，`InitDB()` 创建所有表、写入当前版本、初始化默认设置、刮削设置和 Emby 配置。首次空库直接初始化到当前结构版本，不逐个回放历史迁移；首个管理员通过启动日志中的初始化码创建。

已有数据库启动时，`Migrate()` 按 `migrator.version_code` 顺序执行补丁并逐步推进版本。新增或修改表、字段和迁移时必须同时更新 [数据库 schema 与迁移](../reference/database-schema.md)。

## 修复与清库

`POST /api/database/repair` 调用 `RepairDB()`，对 `AllTables` 执行 `AutoMigrate` 并修复 PostgreSQL 主键序列：缺失表、字段和索引会补齐，不主动删除已有数据。

`POST /api/database/delete-all-table` 调用 `BatchDropTable()` 删除 `AllTables` 中的全部表，属于高风险清库操作。执行前必须确认备份可用，并在维护窗口内操作。

## 备份

备份配置存储在数据库的 `backup_config`，不在 `config/config.yaml`。默认自动备份关闭；默认 Cron 是 `0 3 * * *`，默认保留 7 天、最多保留 10 份。服务启动时会按“已启用且 Cron 非空”创建定时任务；保存为启用状态时也会重建 Cron。当前保存为禁用状态不会停止已在当前进程注册的旧 Cron，禁用后应重启服务以确保任务移除。手动备份和定时备份不会并行执行。

备份文件始终写入配置目录下的 `backups/`，命名为 `backup_<类型>_<时间>.zip`。压缩包内按 `AllTables` 的每个模型写入一个 JSON Lines 文件。当前 `backup_path` 和 `backup_compress` 虽可保存到配置记录，但备份实现尚未使用它们：输出路径仍是 `backups/`，格式始终为 ZIP。

每次新备份开始前，程序只清理状态为 `completed` 的历史记录；保留天数和最大数量独立生效，任一条件命中都会删除文件及其记录。应定期把完成的 ZIP 包复制到配置目录之外的独立存储，避免把唯一备份与运行数据放在同一磁盘。

备份开始时会暂停同步队列、上传下载队列和各类 Cron，完成后自动恢复。它无法阻止浏览器或外部客户端继续写入 API，因此应在维护窗口内操作并停止外部写入。

## 恢复与风险边界

恢复只接受 ZIP 文件：可以恢复已有备份记录，或上传 ZIP 后恢复。程序解压到 `backups/` 的临时目录，逐表删除旧表、重建结构并导入 JSON Lines，然后尝试修复主键序列。它是全量表级方案，不是增量备份或时间点恢复。

恢复过程中，单个表不存在或导入失败会写入日志；逐表恢复遇到错误会继续处理后续表，因而“恢复任务结束”不等于每张表都已成功恢复。解压阶段失败会使后台恢复直接返回，当前控制器不会把这个返回错误回传给已收到“任务已开始”的请求。操作后必须查看应用日志并核验关键数据，再恢复外部写入。

恢复完成后程序会重新启动内部队列和 Cron，但不会自动重启服务。若迁移、配置或外部连接状态仍异常，可在确认备份保留后手动重启服务。新增或修改模型字段会影响备份恢复行为，必须同时更新 [数据库 schema 与迁移](../reference/database-schema.md)。
