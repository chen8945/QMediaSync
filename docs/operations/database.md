# 数据库运维

> 职责：说明数据库引擎、初始化、修复、清库、备份和恢复的运行维护方式。
>
> 权威范围：本文档维护运行操作；表、字段、时间策略和版本迁移见 [数据库 schema 与迁移](../reference/database-schema.md)。
>
> 修改时机：修改数据库引擎选择、初始化流程、修复 / 清库接口、备份恢复实现或操作限制时必须更新本文档。
>
> 相关代码：`backend/internal/db/`、`backend/internal/models/migrator.go`、`backend/internal/models/backup.go`、`backend/internal/backup/`、`backend/internal/controllers/backup.go`。

## 引擎与初始化

QMediaSync 支持 SQLite 和 PostgreSQL，默认使用 PostgreSQL。程序和 Docker 镜像均不提供 PostgreSQL 服务进程。数据库配置通过 `config/config.yaml` 保存；首次配置、端口和 PostgreSQL 要求见 [配置、密钥与日志](configuration.md)。

SQLite 连接使用 WAL 日志模式、`synchronous = NORMAL` 和 10 秒 `busy_timeout`，并且连接池固定为一个连接（`MaxOpenConns` 和 `MaxIdleConns` 均为 1，连接不设置存活和空闲上限）。SQLite 同一时间只允许一个写事务：多连接时，事务读取快照之后如果其他连接提交了写入，本事务的写升级会立即失败并返回 `SQLITE_BUSY`（快照冲突），`busy_timeout` 不会重试这种错误。单连接把所有语句串行化到进程内的连接锁上排队，写入不再返回 `database is locked`。`MaxOpenConns` 不适用于 SQLite 引擎，只作用于 PostgreSQL。

因此 SQLite 下的写事务体内不得再通过全局 `db.Db` 发起新语句，必须使用事务自身的 `tx`：单连接会让这种嵌套语句等待自己持有的连接而死锁。PostgreSQL 连接池仍按 `config/config.yaml` 的 `maxOpenConns` 和 `maxIdleConns` 配置。

首次启动时，如果 `migrator` 表不存在，`InitDB()` 创建所有表、写入当前版本、初始化默认设置、刮削设置和 Emby 配置。首次空库直接初始化到当前结构版本，不逐个回放历史迁移；首个管理员通过启动日志中的初始化码创建。

已有数据库启动时，`Migrate()` 按 `migrator.version_code` 顺序执行补丁并逐步推进版本。新增或修改表、字段和迁移时必须同时更新 [数据库 schema 与迁移](../reference/database-schema.md)。

本地管理员恢复使用独立的已有数据库连接：SQLite 以 `mode=rw` 打开已有普通文件，PostgreSQL 只连接指定数据库；两者均不建库、不迁移、不启动后台保活，连接池限制为一个连接。认证变更以单个事务提交，失败回滚。它与备份恢复、数据库修复是不同操作，具体契约见 [本地管理员恢复](../architecture/authentication-sessions.md#本地管理员恢复)，使用方法见 [部署说明](deployment.md#管理员恢复)。

## 旧内嵌数据库

当前版本不再启动内嵌 PostgreSQL，也不提供旧库迁移网页、迁移导出或 `backups/migrate.zip` 自动导入。普通备份恢复和数据库 schema 升级仍按本文及 schema 文档执行。

如需从内置 PostgreSQL 迁移，可先使用原作者的 `v0.14.23` 或本项目的 `v0.15.17` 版本完成迁移。迁移后核对数据，再使用已准备好的 SQLite 或 PostgreSQL 配置启动当前版本。当前版本遇到以下状态会拒绝继续：

- PostgreSQL 配置显式指定 `postgresType: embedded`，或使用未知引擎、未知 PostgreSQL 模式。
- 配置目录下存在 `backups/migrate.zip`：正常启动和管理员恢复均拒绝操作，不导入或删除该文件。
- 主配置缺失但存在 `config/postgres`：拒绝进入首次配置向导，避免把旧实例当作空实例重新初始化。

旧 SQLite 配置中未使用的 `postgresType: embedded` 不影响 SQLite 连接。已经切换到受支持数据库的实例可以保留旧数据目录；程序不会自动删除 `config/postgres`、`config/postgres-backup` 或旧 PostgreSQL 二进制目录。确认数据完整后，由维护者自行归档或清理，不能靠删除未完成迁移包来代替数据核对。

## 修复与清库

`POST /api/database/repair` 调用 `RepairDB()`，对 `AllTables` 执行 `AutoMigrate`，补齐 `sync_files` 的查询索引及 STRM 生成队列的排序索引，并修复 PostgreSQL 主键序列：缺失表、字段和索引会补齐，不主动删除已有数据。

前端只有在 HTTP 与业务响应都成功时才提示修复成功；失败使用安全错误说明，不展示数据库内部异常。

`POST /api/database/delete-all-table` 调用 `BatchDropTable()` 删除 `AllTables` 中的全部表，属于高风险清库操作。执行前必须确认备份可用，并在维护窗口内操作。

## 备份

备份配置存储在数据库的 `backup_config`，不在 `config/config.yaml`。默认自动备份关闭；默认 Cron 是 `0 3 * * *`，默认保留 7 天、最多保留 10 份。服务启动时会按“已启用且 Cron 非空”创建定时任务；保存为启用状态时也会重建 Cron。当前保存为禁用状态不会停止已在当前进程注册的旧 Cron，禁用后应重启服务以确保任务移除。手动备份和定时备份不会并行执行。

备份文件始终写入配置目录下的 `backups/`，命名为 `backup_<类型>_<时间>.zip`。压缩包内按 `AllTables` 的每个模型写入一个 JSON Lines 文件，普通文件使用 ZIP Deflate 压缩，目录条目使用 Store。当前 `backup_path` 和 `backup_compress` 虽可保存到配置记录，但备份实现尚未使用它们：输出路径仍是 `backups/`，格式始终为 ZIP。

每次新备份开始前，程序只清理状态为 `completed` 的历史记录；保留天数和最大数量独立生效，任一条件命中都会删除文件及其记录。应定期把完成的 ZIP 包复制到配置目录之外的独立存储，避免把唯一备份与运行数据放在同一磁盘。

下载任务备份包含替换旧文件和恢复下载所需的内部信息，包括执行地址、本地源路径和内容校验记录；这些信息不在队列 API 中展示。备份文件应按包含账号配置和下载凭据的文件保管。

备份和恢复在操作业务表之前，会暂停 Emby Webhook 收件并等待其后台观察和执行任务退出，然后暂停同步队列、上传下载队列和各类 Cron。Webhook 停止失败时，本轮不导出业务表、不删除或重建恢复表；备份历史记录仍会记录失败。已成功停止后的任务无论完成还是失败，都会尝试恢复这些后台任务；恢复失败会使整轮状态显示为 `failed`，具体原因见服务日志。

Emby Webhook 暂停期间返回暂时不可用，不确认新事件已经保存。若等待 Webhook 退出超过期限，或后台恢复失败，收件保持暂停，须在核验日志后重启服务恢复处理。备份和恢复不能阻止其他浏览器或外部客户端继续写入 API，因此仍应在维护窗口内操作并停止外部写入；不要在窗口内继续执行依赖 Webhook 的媒体删除。

前端备份请求失败时保留配置和输入，不进入成功后的进度流程；写入已成功但列表刷新失败时单独提示加载问题。下载接口的 JSON 错误即使以 HTTP `200` 返回，也不能作为备份文件保存。错误反馈约定见 [前端开发约定](../engineering/frontend-development.md#api-响应与请求错误)。

## 备份和恢复状态

`GET /api/backup/status` 的 HTTP `200`、业务 `code=200` 表示成功读取任务快照，不代表备份或恢复成功。`data.status` 明确区分 `idle`（尚无任务）、`running`、`completed` 和 `failed`，原有 `is_running`、`count`、`total`、`error_msg` 等字段保留。启动接口在接受请求前占用运行状态并清除上一轮快照，避免并发启动或将上一轮终态当作新任务结果；状态读取返回独立快照。

备份在文件写入、ZIP 收尾和完成记录保存全部成功后才进入 `completed`；目录创建、数据库操作、文件读写、压缩或异常退出都进入 `failed`。压缩失败时删除本轮的残缺 ZIP，避免备份目录留下没有记录路径、无法随历史清理的文件。失败说明不包含内部路径或数据库细节，具体原因记录在服务日志中。备份和恢复的失败均不能仅靠 `is_running=false` 或 `count=total` 判为成功。

前端仅对明确的 `completed` 提示完成。连接旧后端时，仍在运行的任务继续查询；停止后若有 `error_msg` 则提示失败，否则提示结果尚未确认，停止查询并请用户核验记录和日志，不补猜成功。

## 恢复与风险边界

恢复只接受 ZIP 文件，兼容旧版 Store 和新版 Deflate 条目：可以恢复已有备份记录，或上传 ZIP 后恢复。程序解压到 `backups/` 的临时目录，逐表删除旧表、重建结构并导入 JSON Lines，然后尝试修复主键序列。重建 `sync_files` 和 `strm_generation_tasks` 时也会建立对应查询或排序索引；索引创建失败会记录为恢复失败，并继续导入可用记录。恢复及清库同时失效进程内的同步逻辑位置缓存，部分恢复或清库失败也不能沿用旧位置。它是全量表级方案，不是增量备份或时间点恢复。

恢复兼容旧备份缺少后来新增模型文件的情况：缺失文件对应的表保持不变，继续恢复包内已有模型。整个 ZIP 没有任何可识别模型文件时判为失败。已有文件的 JSON 解析、读取、建表、导入或序列修复出错后继续处理其余表，并将整轮任务标为 `failed`；解压等前置步骤失败也通过状态快照报告。

恢复不是全库事务，失败时部分表可能已经恢复，不会自动回滚这些数据。启动接口返回“任务已开始”只表示接受请求，最终结果以任务状态为准。操作后必须查看应用日志并核验关键数据，再恢复外部写入。

恢复完成后程序会重新启动内部队列、Cron 和 Emby Webhook 后台处理，但不会自动重启服务。Webhook 从恢复后的持久记录重新领取任务，不继续使用恢复前的内存领取状态；旧备份缺少新增模型时仍保留现有对应表，联动删除执行前必须通过当前身份和历史证据核验。若迁移、配置或外部连接状态仍异常，可在确认备份保留后手动重启服务。新增或修改模型字段会影响备份恢复行为，必须同时更新 [数据库 schema 与迁移](../reference/database-schema.md)。
