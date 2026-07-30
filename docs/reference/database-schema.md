# 数据库 schema 与迁移

> 职责：说明数据库表、字段、索引、稳定存储值、时间策略和版本迁移语义。
>
> 权威范围：本文档维护 schema 和迁移兼容性；数据库引擎、修复、清库、备份和恢复操作见 [数据库运维](../operations/database.md)。
>
> 修改时机：修改模型、表、字段、索引、存储枚举、时间字段、迁移版本或备份依赖的 schema 时必须更新本文档。
>
> 相关代码：`backend/internal/models/`、`backend/internal/models/migrator.go`、`backend/internal/db/`、`frontend/src/utils/timeUtils.ts`。

## Schema 来源

数据库结构、默认数据和版本迁移由 `backend/internal/models/migrator.go` 统一处理：

- `AllTables` 定义了建表、修复、备份和恢复会遍历的表集合。
- `InitDB()` 负责首次启动时的全量初始化。
- `Migrate()` 负责已有数据库的版本升级。
- `BatchCreateTable()` 负责补齐缺失的表、字段和索引。
- `BatchRepairTableSeq()` 负责 PostgreSQL 的主键序列修复。

说明：

- 除 `internal/notification` 里的通知表外，项目大多数表都嵌入 `BaseModel`，主键和时间字段都是 `int64` Unix 秒。
- `internal/notification` 里的通知表使用 `time.Time` 作为 `CreatedAt` / `UpdatedAt`。
- 带 `gorm:"-"` 的字段不入库，只用于运行时组装、前端回传或关联对象。
- PostgreSQL 数据库名在首次配置、外部库自动创建和内嵌库初始化时统一校验并作为 identifier 引用；名称不能为空、不能超过 63 字节、不能包含空白或控制字符。

## 时间字段策略

业务时间字段统一遵循以下规则：

- 除 `internal/notification` 通知表自身的 GORM `CreatedAt` / `UpdatedAt` 的历史例外之外，数据库业务时间字段使用 Unix 秒 `int64`。
- API 业务时间字段返回 Unix 秒，例如 `created_at`、`updated_at`、`started_at`、`ended_at`、`expired_at`、`last_sync_time`。
- 前端展示统一通过 `frontend/src/utils/timeUtils.ts` 格式化，并按浏览器本地时区显示。
- 日志时间保持日志原始格式，不纳入业务时间字段改造。日志可以继续保留毫秒或微秒精度，例如 `2026/06/29 10:44:49.138474 INFO ...`。
- 新增毫秒时间或耗时字段必须使用 `_ms` 后缀，例如 `event_time_ms`、`duration_ms`。
- 115 远端文件的业务“修改时间”统一使用列表 `upt` 或详情 `utime`；上传时间 `uppt` / `ptime` 仅在官方修改时间缺失或为零时作为兼容回退。

兼容规则：

- 版本信息接口保留旧 `date` 字符串一段时间。
- 新代码优先读取 Unix 秒字段，例如 `build_time`、`published_at`。
- 旧无时区字符串只作为兼容回退，不作为新接口标准。

## 迁移与初始化

### 首次启动

当 `migrator` 表不存在时，`InitDB()` 会直接执行：

1. `BatchCreateTable()`：对 `AllTables` 逐表执行 `AutoMigrate`。
2. `InitMigrationTable(MaxVersionCode)`：写入当前版本号，当前值是 `61`。
3. `InitSettings()`：创建默认 `settings` 记录。
4. `InitScrapeSetting()`：创建默认刮削配置和默认分类。
5. `InitEmbyConfig()`：创建默认 `emby_config` 记录。

这意味着空库首次启动时，不会逐个版本回放历史迁移，而是直接初始化到当前结构版本。首次启动不会创建默认管理员；管理员由启动日志中的初始化码在 Web 登录页显式创建。

### 已有数据库

当 `migrator` 表已存在时，`Migrate()` 会读取 `version_code`，然后按顺序执行版本补丁。每一步更新后都会把版本号加一，因此一次启动可以连续跨过多个历史版本。

### 重点迁移历史（35+）

下表的“起始版本”对应迁移执行前的 `migrator.version_code`。迁移成功后，`UpdateVersionCode()` 会把版本推进到“目标版本”。版本 `1` 到 `34` 的历史补丁仍会对足够旧的数据库顺序执行，但其中多数只服务早期结构演进；需要排查这部分兼容性时，以 `backend/internal/models/migrator.go` 的对应分支为准。

| 起始版本 | 目标版本 | 变更 |
| --- | --- | --- |
| 35 | 36 | `emby_config` 补齐媒体库选择字段，并清理重复或缺失的 `scrape_settings` 记录。 |
| 36 | 37 | `settings` 新增 `file_list_page_size`。 |
| 37 | 38 | `emby_config` 新增播放剧情简介和播放进度开关。 |
| 38 | 39 | 为已有通知渠道补齐 `playback_*` 和 `scrape_error` 规则。 |
| 39 | 40 | `account` 新增 `app_id_name`。 |
| 40 | 41 | `account` 新增 `auth_source_type` 和 `auth_provider`。 |
| 41 | 42 | `users` 补齐两步验证字段，`db_download_tasks` 和 `db_upload_tasks` 补齐队列重试字段。 |
| 42 | 43 | 新增 `emby_library_refresh_tasks` 表。 |
| 43 | 44 | 下载 / 上传任务的 `source` 从展示文案迁移为稳定存储值。 |
| 44 | 45 | 新增 `user_sessions` 表，用于浏览器登录会话撤销、CSRF 校验和登录设备管理。 |
| 45 | 46 | 通知渠道类型索引从唯一索引改为普通索引，并为已有渠道补齐缺失通知规则。 |
| 46 | 47 | `users` 新增 `singleton_key`，用唯一约束保证系统只存在一个登录用户。 |
| 47 | 48 | `settings` 和 `sync_paths` 的 `add_path` 旧值 `2` 迁移为新值 `3`，为“只添加文件名”路径模式让出枚举值。 |
| 48 | 49 | `db_download_tasks` 新增 `sync_path_id`，用于 Emby 刷新任务直接判断对应同步目录是否还有未完成下载。 |
| 49 | 50 | `emby_config` 新增 Emby 条目同步状态字段，`emby_media_items` 新增全量同步批次标记字段。 |
| 50 | 51 | `emby_config` 新增每日首次全量同步开关和最近成功同步模式字段。 |
| 51 | 52 | 新增 `upload_sessions`、`directory_upload_rules`、`strm_generation_tasks` 表，并为上传任务和设置补齐上传增强字段。 |
| 52 | 53 | `emby_library_refresh_tasks` 新增 item 级定向刷新目标和媒体库回退字段。 |
| 53 | 54 | `db_upload_tasks` 新增 `local_mtime`，用于记录目录监控源文件秒级 mtime。 |
| 54 | 55 | `directory_upload_rules` 新增 `upload_metadata`，用于控制目录监控是否上传元数据扩展名文件。 |
| 55 | 56 | `strm_generation_tasks` 新增 Webhook 元数据下载、Emby 刷新开关和父任务刷新目标统计字段。 |
| 56 | 57 | 新增 `directory_upload_processed_files` 表，`db_upload_tasks` 新增目录监控源文件 fingerprint 关联字段。 |
| 57 | 58 | `sync_paths` 新增 `directory_upload_enabled`，作为目录监控上传同步目录总开关，并按已有启用规则回填。 |
| 58 | 59 | `settings` 新增 115 直链缓存有效性检查开关和总超时。 |
| 59 | 60 | 新增 `sync_path_idempotency_records`，用于同步目录创建的幂等重试；`emby_library_refresh_tasks` 新增 `task_key` 用于任务去重，item 定向刷新任务的 `library_id` 回填为真实媒体库 ID 或为空。 |
| 60 | 61 | 分离上传、下载队列的远端完整路径、文件 ID、PickCode 与哈希；迁移隐藏下载执行定位字段，并删除上传任务旧的 `completed_remote_file_id`、`completed_pick_code` 列。为活跃上传任务及可可靠定位的活跃下载任务补齐部分唯一索引；下载键以范围和定位值的 SHA-256 摘要存储，避免将签名直链写入索引。旧 115 下载任务的 `remote_file_id` 先回填为 `remote_pick_code`；关联 `SyncFile` 只有提供非空 PickCode 时才能覆盖该值，部分迁移重试优先保留已写入的 `remote_pick_code`。 |

当前数据库版本是 `61`。

## 不变量

- 新增持久化表必须加入 `AllTables`，并在需要时补充初始化、迁移、备份恢复和对应模型测试。
- 业务时间字段默认使用 Unix 秒 `int64`；新增毫秒字段使用 `_ms` 后缀，旧字符串时间只作为兼容回退。
- 写入数据库的枚举、来源和状态必须使用稳定机器值，展示文案由前端映射；变更历史值必须提供迁移兼容方案。
- 迁移版本只能顺序推进。空库直接初始化到当前结构版本，已有库才按历史补丁升级。

## 验证方式

- 修改模型或迁移后运行 `(cd backend && go test ./internal/models/)`；涉及配置或数据库连接时同时运行相应 `helpers`、`db` 或控制器包测试。
- 运行 `(cd backend && go vet ./...)` 检查后端改动。
- 修改时间字段时检查 `frontend/src/utils/timeUtils.ts` 的展示调用与相关 API 响应；涉及旧数据时补充迁移场景测试。

## 常用枚举

以下字段存的是稳定值，不是展示文案：

| 字段 | 存储值 |
| --- | --- |
| `source_type` | `115`、`local`、`123`、`openlist`、`baidupan`、`emby_media` |
| `media_type` | `movie`、`tvshow`、`other` |
| `scrape_type` | `only_scrape`、`scrape_and_rename`、`only_rename` |
| `rename_type` | `hard_symlink`、`soft_symlink`、`move`、`copy`、`same` |
| `enable_ai` | `off`、`assist`、`enforce` |
| `sync.status` | `0` 待处理、`1` 进行中、`2` 已完成、`3` 失败 |
| `sync.sub_status` | `0` 无、`1` 正在处理网盘文件、`2` 正在处理本地文件列表 |
| `scrape_media.status` | `unscanned`、`scanned`、`scraping`、`scraped`、`renaming`、`renamed`、`rename_failed`、`ignore`、`scrape_failed`、`rollbacking` |
| `download.status` | `0` 待下载、`1` 下载中、`2` 已完成、`3` 失败、`4` 已取消 |
| `download.source` | `strm_sync`、`local_file`、`emby_media` |
| `upload.status` | `0` 等待上传、`1` 正在上传、`2` 上传完成、`3` 上传失败、`4` 已取消、`5` 等待完成处理（远端已完成待本地收尾）、`6` 正在完成处理（正在本地收尾） |
| `upload.source` | `strm_sync`、`scrape_organize`、`directory_monitor` |
| `upload.result` | `unknown`、`rapid_upload`、`multipart_uploaded`、`remote_exists`、`skipped_after_rapid_wait` |
| `upload.resume_state` | `none`、`new_session`、`resumed_session`、`session_expired_restarted` |
| `upload.source_cleanup_status` | `none`、`pending`、`completed`、`failed` |
| `directory_upload_processed.result` | `queued`、`pending_replace`、`uploaded_pending_strm`、`remote_exists_pending_strm`、`strm_enqueue_failed`、`uploaded`、`remote_exists`、`skipped_existing`、`failed` |
| `strm_generation.source` | `upload_completed`、`webhook`、`remote_exists` |
| `strm_generation.task_type` | `file`、`directory_scan`、`batch_files` |
| `strm_generation.status` | `pending`、`running`、`finalizing`、`waiting_children`、`completed`、`failed`、`cancelled` |
| `emby_refresh.target_type` | `library`、`item` |
| `backup.status` | `pending`、`running`、`completed`、`failed`、`cancelled`、`timeout` |
| `backup.type` | `manual`、`auto` |
| `notification.type` | `sync_finish`、`sync_error`、`scrape_finish`、`scrape_error`、`system_alert`、`media_added`、`media_removed`、`playback_start`、`playback_pause`、`playback_stop` |
| `notification.priority` | `high`、`normal`、`low` |

### 关键约束与索引

下表只列跨模块依赖的唯一约束和查询索引；完整字段标签以模型定义为准。

| 表 / 字段 | 约束或索引 | 语义 |
| --- | --- | --- |
| `sync_paths.base_cid` | 唯一 | 同一同步源目录 ID 只能创建一个同步目录。 |
| `sync_path_scrape_paths(sync_path_id, scrape_path_id)` | 联合唯一 | 同一同步目录与刮削目录的关联不能重复。 |
| `scrape_strm_paths(scrape_path_id, strm_path_id)` | 联合唯一 | 同一刮削目录与同步目录的关联不能重复。 |
| `api_keys.key_hash` | 唯一；`user_id` 索引 | API Key 只保存哈希，且归属用户可查询。 |
| `user_sessions.session_id`、`token_id` | 分别唯一；`user_id`、`expires_at`、`last_seen_at`、`revoked_at` 索引 | 浏览器会话票据和 JWT `jti` 不可重复，并支持有效会话查询。 |
| `upload_sessions.upload_task_id` | 唯一；`account_id`、`status` 索引 | 每个上传任务最多保留一个恢复会话。 |
| `db_download_tasks(source, source_type, account_id, dedup_scope_hash, dedup_locator_hash)` | 部分唯一索引 `idx_db_download_tasks_active_target` | 两个摘要非空且状态为等待下载或下载中时，同一下载来源和定位范围只能有一个任务；终态记录不参与约束。 |
| `db_upload_tasks(source, source_type, account_id, remote_full_path)` | 部分唯一索引 `idx_db_upload_tasks_active_target` | `remote_full_path` 非空且状态为等待上传、上传中、等待完成处理或正在完成处理时，同一上传来源和存储范围只能有一个任务；终态记录不参与约束。 |
| `sync_path_idempotency_records.key_hash` | 唯一；`sync_path_id`、`status` 索引 | 同步目录创建请求的幂等键不能重复。 |
| `emby_library_sync_paths(library_id, sync_path_id)` | 联合唯一 | Emby 媒体库与同步目录的关联不能重复。 |
| 各 `*_channel_configs.channel_id` | 唯一 | 一条通知渠道基础记录至多关联一份同类型配置。 |

目录监控规则的重复范围由应用层校验，并没有对应的数据库联合唯一键；`notification_rules(channel_id, event_type)` 也没有联合唯一约束。

## 表结构

### `migrator`

数据库版本表，记录当前结构版本。

- `id`：固定为 `1`。
- `created_at` / `updated_at`：创建和更新时间。
- `version_code`：当前数据库版本号，当前值为 `61`。

### `users`

登录用户表。系统按单用户模式运行，通过固定的 `singleton_key` 唯一约束保证只能存在一个登录用户。

- `singleton_key`：固定为 `1` 的单用户约束键，唯一。
- `username`：登录名，唯一。
- `password`：bcrypt 哈希后的密码。
- `two_factor_enabled`：是否启用两步验证。
- `two_factor_secret`：加密后的 TOTP 密钥。
- `two_factor_pending_secret`：启用确认前的临时 TOTP 密钥。

### `account`

网盘 / OpenList / 115 授权账号表。

- `name`：账号备注，便于人工识别。
- `source_type`：账号来源类型，`115`、`local`、`123`、`openlist`、`baidupan` 或 `emby_media`。
- `app_id`：115 开放平台 APP ID。
- `app_id_name`：自定义应用显示名。
- `auth_source_type`：115 授权来源类型。
- `auth_provider`：115 授权 Provider。
- `token` / `refresh_token`：访问凭据。
- `token_expiries_time`：凭据过期时间戳。
- `user_id`：账号对应的用户 ID。
- `username`：网盘用户名或 OpenList 登录用户名。
- `password`：OpenList 登录密码。
- `base_url`：OpenList 访问地址。
- `token_failed_reason`：刷新 Token 失败原因。

补充：

- `app_id_name` 是后加字段，用于区分自定义 115 应用。
- `auth_source_type`、`auth_provider` 是后加字段，用于稳定描述授权来源。

### `api_keys`

API Key 认证表。

- `user_id`：所属用户 ID。
- `name`：Key 名称或用途说明。
- `key_hash`：API Key 的 SHA256 哈希，只存哈希，不存明文。
- `key_prefix`：前 8 位明文前缀，用于列表展示。
- `last_used_at`：最后使用时间戳。
- `is_active`：是否启用。

### `user_sessions`

浏览器登录会话表。JWT 只作为客户端 Cookie 中的会话票据，实际有效性以本表为准。

- `session_id`：会话 ID，写入 JWT claims，用于查表。
- `token_id`：JWT `jti`，用于审计和排查。
- `user_id` / `username`：关联用户和登录用户名快照。
- `csrf_token_hash`：CSRF Token 的 SHA256 哈希，原始值只保存在前端可读 `csrf_token` Cookie 和内存状态中。
- `user_agent`：登录设备标识。
- `ip_address`：登录 IP。
- `expires_at`：会话过期时间戳。
- `last_seen_at`：最后活跃时间戳。
- `revoked_at` / `revoke_reason`：撤销时间和原因。

### `settings`

全局配置表，包含线程、STRM 和历史兼容字段。

- `download_threads`：下载队列并发数。
- `file_detail_threads`：115 文件详情请求并发数。
- `openlist_qps`：OpenList QPS。
- `openlist_retry`：OpenList 重试次数。
- `openlist_retry_delay`：OpenList 重试间隔，单位秒。
- `file_list_page_size`：115 文件列表分页大小。
- `url_validity_check_enabled`：是否启用 115 直链缓存有效性检查，默认 `1`。关闭后 115 缓存命中不会发起 HEAD 检查。
- `url_validity_check_timeout_seconds`：115 直链缓存有效性检查总超时，单位秒，默认 `3`，可配置范围为 `1` 到 `9`；旧配置中超过 `9` 的值会在运行时裁剪为 `9`。

STRM 相关字段：

- `local_proxy`：全局本地代理开关，只接受 `0`（关闭）或 `1`（开启）。同步目录自定义 STRM 配置才额外接受 `-1`，表示继承这里的全局值。
- `strm_base_url`：生成 STRM 的基础地址。
- `cron`：定时任务表达式。
- `min_video_size`：最小视频大小，单位字节。
- `video_ext`：视频扩展名 JSON 字符串。
- `meta_ext`：元数据扩展名 JSON 字符串。
- `exclude_name`：排除文件名 JSON 字符串。
- `upload_meta`：是否上传元数据。
- `download_meta`：是否下载元数据。
- `delete_dir`：是否删除目录。
- `add_path`：STRM 链接路径模式。全局配置使用 `1` 添加完整路径、`2` 只添加文件名、`3` 不添加；同步目录自定义配置额外使用 `-1` 表示继承全局 STRM 设置。
- `check_meta_mtime`：是否检查元数据修改时间。

115 上传秒传等待字段：

- `upload_rapid_wait_enabled`：是否启用秒传等待，默认 `0`。
- `upload_rapid_wait_timeout_seconds`：秒传等待最大时长，单位秒，默认 `0`；最后一次等待会按剩余超时时间裁剪。
- `upload_rapid_wait_interval_seconds`：秒传等待重试间隔，单位秒，默认 `60`，只控制重复 init 的频率。
- `upload_rapid_wait_min_size`：启用秒传等待的最小文件大小，单位字节，默认 `0`。
- `upload_rapid_wait_force_size`：强制等待到超时的文件大小阈值，单位字节，默认 `0`。
- `upload_rapid_wait_skip_upload`：等待超时后是否跳过真实上传，默认 `0`。

历史兼容字段：

- `use_telegram`、`telegram_bot_token`、`telegram_chat_id`、`meow_name`：旧通知配置，已迁移。
- `emby_url`、`emby_api_key`：旧 Emby 配置，已迁移。
- `http_proxy`：出站代理地址，列名保持历史命名，取值支持 `http`、`https`、`socks5` 和 `socks5h`。

### `sync_paths`

同步目录表，保存一个同步源和它对应的 STRM / 元数据生成规则。

`sync_paths` 复用了 `SettingStrm` 的字段，因此也包含这些列：

- `local_proxy`
- `strm_base_url`
- `cron`
- `min_video_size`
- `video_ext`
- `meta_ext`
- `exclude_name`
- `upload_meta`
- `download_meta`
- `delete_dir`
- `add_path`
- `check_meta_mtime`

额外字段：

- `custom_config`：是否使用自定义配置。
- `base_cid`：同步源目录 ID，115 / 123 需要。
- `local_path`：本地 STRM / 元数据输出目录。
- `remote_path`：同步源路径。
- `source_type`：来源类型，`115`、`local`、`123`、`openlist`、`baidupan` 或 `emby_media`。
- `account_id`：对应账号 ID。
- `enable_cron`：是否启用定时同步。
- `directory_upload_enabled`：目录监控上传同步目录总开关；运行时只有该字段为 `true` 且规则自身 `enabled=true` 的目录监控规则会生效。关闭总开关不会改写规则表的 `enabled`。
- `last_sync_at`：上次同步时间戳。
- `is_full_sync`：是否全量同步。

运行时字段：

- `account_name`：账号显示名，不入库。
- `is_running`：当前运行状态，不入库。

### `sync_path_scrape_paths`

同步目录与刮削目录的关联表。

- `sync_path_id`：同步目录 ID。
- `scrape_path_id`：刮削目录 ID。

### `sync_path_idempotency_records`

同步目录创建请求的幂等记录表。

- `key_hash`：`Idempotency-Key` 的 SHA-256 摘要，唯一；原始 key 不入库。
- `sync_path_id`：成功创建后的同步目录 ID。
- `status`：`pending` 表示创建事务正在处理，`completed` 表示可回放已创建聚合。

同一 key 已完成时会回放已创建聚合；尚未完成时返回 `IDEMPOTENCY_CONFLICT`。完整 HTTP 契约见 [同步目录聚合 API](sync-path-api.md)。

### `syncs`

同步任务记录表。

- `sync_path_id`：对应同步目录。
- `status`：任务主状态。
- `sub_status`：任务子状态。
- `file_offset`：文件偏移量，用于断点续跑。
- `total`：总文件数。
- `finish_at`：完成时间戳。
- `new_strm`：新增 STRM 数量。
- `new_meta`：新增元数据数量。
- `new_upload`：新增上传数量。
- `net_file_start_at` / `net_file_finish_at`：处理网盘文件开始 / 结束时间。
- `local_file_start_at` / `local_file_finish_at`：处理本地文件列表开始 / 结束时间。
- `local_path`：本地同步路径。
- `remote_path`：远程同步路径。
- `base_cid`：根目录 ID。
- `fail_reason`：失败原因。
- `is_full_sync`：是否全量同步。

删除规则：

- 同步记录作为历史审计数据保留；删除同步目录不会级联删除 `syncs` 历史记录。
- 删除同步目录会在同一事务内删除对应的 `directory_upload_rules`，并由接口层重载目录监控上传服务，停止对应 watcher。
- 用户手动删除同步记录时，仅允许删除已完成或失败的记录，同时删除对应同步日志文件。
- 定时任务每天 0 点清理创建时间早于 7 天的同步记录和对应同步日志。

运行时字段：

- `sync_path`：关联的同步路径对象。
- `logger`：日志句柄。

### `sync_files`

同步文件表，记录单个文件在同步链路中的状态。

- `source_type`：来源类型。
- `account_id`：所属账号。
- `sync_path_id`：所属同步目录。
- `file_id`：文件 ID。
- `parent_id`：父目录 ID。
- `file_name`：文件名。
- `file_size`：文件大小。
- `file_type`：115 文件类型。
- `pick_code`：115 PickCode。
- `sha1`：文件 SHA1。
- `mtime`：来源文件时间戳；115 同步链路保存上传时间 `Ptime`，用于同目标 STRM 的最新文件选择。
- `local_file_path`：本地完整文件路径。
- `path`：绝对路径，不含文件名。
- `is_video`：是否视频文件。
- `is_meta`：是否元数据文件。
- `openlist_sign`：OpenList 文件签名。
- `openlist_object_id`：OpenList API 返回的可选对象 ID；`file_id` 仍保存既有路径语义，创建下载任务时才复制该字段到远端文件 ID。
- `openlist_sha1` / `openlist_md5`：OpenList `hash_info` 中明确命名为 SHA1 / MD5 的值；`hashinfo` 私有字符串不解析。
- `uploaded`：是否已上传完成。
- `thumb_url`：缩略图地址。
- `processed`：是否已处理。

运行时字段：

- `sync_path`、`sync`、`account`：关联对象，不入库。

115 同一目录下的不同扩展名视频可能映射到相同的 `local_file_path`。`sync_files` 会保留各远端文件记录，运行时按上传时间 `Ptime` 选择唯一 STRM owner；时间相同时使用 FileID 固定排序。non-owner 不参与 STRM 比较、写入或 Emby 刷新，owner 删除后由剩余候选中的最新文件接管。

### `scrape_settings`

全局刮削设置表。

- `tmdb_url`：TMDB API 地址。
- `tmdb_image_url`：TMDB 图片地址。
- `tmdb_api_key`：TMDB API Key。
- `tmdb_access_token`：TMDB Access Token。
- `tmdb_language`：TMDB 语言。
- `tmdb_image_language`：TMDB 图片语言。
- `tmdb_enable_proxy`：是否启用 TMDB 代理。
- `enable_ai`：AI 识别模式，`off` / `assist` / `enforce`。
- `ai_base_url`：AI 服务基础地址。
- `ai_api_key`：AI API Key。
- `ai_model_name`：AI 模型名。
- `ai_prompt`：AI 提示词。
- `ai_timeout`：AI 超时时间，单位秒。
- `fanart_api_key`：fanart.tv API Key。

说明：

- `tmdb_*` 和 `fanart_api_key` 会在运行时和 `helpers` 包里的默认值合并。
- `tmdb_enable_proxy` 打开时会使用 `settings.http_proxy` 作为通用代理。

### `scrape_paths`

刮削目录表，保存扫描源、命名规则、分类规则和定时任务信息。

基础字段：

- `account_id`：账号 ID。
- `source_type`：来源类型。
- `media_type`：媒体类型，`movie` / `tvshow` / `other`。
- `source_path`：源路径。
- `source_path_id`：源路径 ID。
- `dest_path`：目标路径。
- `dest_path_id`：目标路径 ID。
- `scrape_type`：刮削类型。
- `rename_type`：重命名类型。
- `folder_name_template`：文件夹模板。
- `file_name_template`：文件名模板。

规则字段：

- `deleted_keyword`：要删除的关键词 JSON 字符串。
- `enable_category`：是否启用分类。
- `video_ext`：视频扩展名 JSON 字符串。
- `min_video_file_size`：最小视频文件大小。
- `exclude_no_image_actor`：是否排除没有图片的演员。
- `enable_ai`：AI 识别模式。
- `ai_prompt`：AI 提示词。
- `force_delete_source_path`：是否强制删除源路径。
- `enable_fanart_tv`：是否启用 fanart.tv。
- `max_threads`：最大刮削线程数。

定时字段：

- `enable_cron`：是否启用定时任务。
- `cron_expression`：Cron 表达式。
- `cron_description`：Cron 描述。
- `last_cron_run`：上次执行时间。
- `next_cron_run`：下次执行时间。
- `cron_enabled`：定时任务启用状态。

状态字段：

- `is_scraping`：是否正在刮削。

运行时字段：

- `delete_keyword`、`video_ext_list`：数组视图，不入库。
- `v115_client`、`baidu_pan_client`、`openlist_client`：运行时客户端。
- `exists_files`、`scrape_root_path`、`category`、`category_map`、`tvshow_renamed_cache`、`episode_finish_channel`、`running`、`mutex`、`is_task_running`：运行时状态。

### `scrape_strm_paths`

刮削目录和同步目录的关联表。

- `scrape_path_id`：刮削目录 ID。
- `strm_path_id`：同步目录 ID。

### `movie_categories`

电影分类表。

- `name`：分类名称。
- `genre_ids`：TMDB genre ID 的 JSON 数组。
- `language`：语言过滤的 JSON 数组。

### `tv_show_categories`

剧集分类表。

- `name`：分类名称。
- `genre_ids`：TMDB genre ID 的 JSON 数组。
- `countries`：国家过滤的 JSON 数组。

### `scrape_path_categories`

刮削目录与分类的映射表。

- `scrape_path_id`：刮削目录 ID。
- `category_id`：分类 ID。
- `file_id`：分类对应的文件 ID。

说明：

- 115 场景下是文件 ID。
- 123 场景下是文件夹 ID。
- 本地和 OpenList 场景下是相对路径。

### `scrape_media_files`

待刮削媒体表，也是刮削流程中最复杂的一张表。

关联与来源：

- `scrape_path_id`：所属刮削目录。
- `media_type`、`source_type`、`scrape_type`、`rename_type`：当前文件的业务类型。
- `enable_category`：是否启用二级分类。
- `source_path`、`source_path_id`、`dest_path`、`dest_path_id`：来源和目标路径信息。
- `media_id`、`media_season_id`、`media_episode_id`：关联的媒体、季、集记录。

识别与文件信息：

- `name`、`year`、`tmdb_id`：基础媒体匹配信息。
- `season_number`、`episode_number`：季 / 集编号。
- `path`、`path_id`：媒体文件夹路径和路径 ID。
- `tvshow_path`、`tvshow_path_id`：剧集根路径和路径 ID。
- `video_filename`、`video_file_id`、`video_pick_code`：视频文件位置和识别码。
- `nfo_path`、`nfo_file_name`、`nfo_file_id`、`nfo_pick_code`：NFO 文件信息。
- `image_files_json`、`subtitle_file_json`：图片 / 字幕文件列表 JSON。
- `tvshow_files_json`、`season_files_json`：剧集和季文件列表 JSON。

媒体分析结果：

- `resolution`、`resolution_level`、`is_hdr`：分辨率和 HDR 信息。
- `video_codec_json`、`audio_codec_json`、`subtitle_codec_json`：ffprobe 结果。

整理结果：

- `status`：当前处理状态。
- `failed_reason`：失败原因。
- `scan_time`、`scrape_time`、`rename_time`、`re_scrape_time`：各阶段时间戳。
- `category_name`、`scrape_path_category_id`：分类结果。
- `new_path_name`、`new_season_path_name`、`new_path_id`、`new_season_path_id`：整理后的新路径。
- `new_video_base_name`：整理后的视频文件名（不含扩展名）。
- `video_ext`：视频扩展名。
- `is_re_scrape`：是否重新刮削。
- `batch_no`：同一次扫描的批次号。
- `tv_is_rename`、`season_is_rename`：剧集 / 季重命名状态。

运行时字段：

- `tvshow_files`、`season_files`、`video_codec`、`audio_codec`、`subtitle_codec`、`media`、`media_season`、`media_episode`、`scrape_root_path`：运行时对象，不入库。
- `v115_client`、`baidu_pan_client`、`openlist_client`、`exists_files`、`category`、`category_map`、`tvshow_renamed_cache`、`episode_finish_channel`、`running`、`mutex`、`is_task_running`：运行时状态，不入库。

### `media`

刮削后的媒体主表，电影和剧集都对应一条记录。

基础识别字段：

- `scrape_path_id`：来源刮削目录。
- `tmdb_id`、`imdb_id`、`name`、`year`、`original_name`：媒体基础标识。
- `media_type`：电影、剧集或其他。
- `release_date`：上映或首播时间。
- `original_language`：原始语言。
- `original_country_json`、`genres_json`：国家和流派的 JSON 字符串。
- `runtime`：时长，单位分钟。
- `last_air_date`、`number_of_episodes`、`number_of_seasons`：剧集信息。
- `num`：番号。
- `mpaa_rating`：分级信息。

画面资源：

- `poster_path`：竖版海报。
- `backdrop_path`：背景图。
- `logo_path`：Logo。
- `thumb_path`、`landscape_path`、`banner_path`：其他备用封面。

正文信息：

- `overview`：简介。
- `tagline`：标语。
- `vote_average`、`vote_count`：评分和投票数。

整理结果：

- `path`、`path_id`：最终整理后的路径。
- `video_file_name`、`video_file_id`：视频文件名和文件 ID。
- `video_pick_code`：115 / 百度网盘识别码。
- `video_open_list_sign`：OpenList 签名。
- `status`：媒体状态，存储值为 `scraped`、`scanned`、`renamed`。
- `subtitle_file_json`：字幕文件 JSON 列表。

运行时字段：

- `actors`、`director`、`origin_country`、`genres`、`subtitle_files`：运行时对象，不入库。

### `media_seasons`

剧集季表。

- `scrape_path_id`：来源刮削目录。
- `media_id`：所属媒体 ID。
- `season_number`：季号。
- `season_name`：季名称。
- `overview`：季简介。
- `number_of_episodes`：季内集数。
- `release_date`：发布日期。
- `path`、`path_id`：季目录路径和路径 ID。
- `poster_path`：季海报。
- `vote_average`：评分。
- `year`：年份。
- `status`：状态，和 `media.status` 共用同一套值。

### `media_episodes`

剧集集表。

- `scrape_path_id`：来源刮削目录。
- `media_id`：所属媒体 ID。
- `media_season_id`：所属季 ID。
- `episode_name`：集名称。
- `overview`：集简介。
- `poster_path`：集海报。
- `season_number`、`episode_number`：季号和集号。
- `release_date`：发布日期。
- `vote_average`、`vote_count`：评分和投票数。
- `year`：年份。
- `video_file_name`、`video_file_id`、`video_pick_code`、`video_open_list_sign`：视频定位信息。
- `status`：状态。
- `subtitle_file_json`：字幕文件 JSON。

运行时字段：

- `actors`：演员列表，不入库。

### `emby_config`

Emby 总配置表。

- `emby_url`：Emby 地址。
- `emby_api_key`：Emby API Key。
- `enable_delete_netdisk`：是否联动删除网盘文件。
- `enable_refresh_library`：是否刷新媒体库。
- `enable_media_notification`：是否发送媒体通知。
- `enable_extract_media_info`：是否提取媒体信息。
- `enable_auth`：是否启用 Emby Webhook 认证，默认启用。
- `sync_enabled`：是否启用同步。
- `sync_cron`：同步 Cron 表达式。
- `last_sync_time`：上次同步时间戳。
- `last_full_sync_at`：最近一次成功全量同步时间戳。
- `last_incremental_sync_at`：最近一次成功增量同步时间戳。
- `last_saved_cursor_at`：增量同步使用的 `DateLastSaved` 游标时间戳。
- `last_processed_count`：最近一次 Emby 条目同步处理数量。
- `last_success_sync_mode`：最近一次成功同步模式，取值包括 `full`、`incremental`、`webhook`。
- `last_error`：最近一次 Emby 条目同步失败原因。
- `is_running`：是否有 Emby 条目同步任务正在运行。
- `sync_mode`：当前或最近一次同步模式，取值包括 `idle`、`full`、`incremental`、`webhook`、`refresh_library`。
- `started_at`：当前同步任务开始时间戳。
- `selected_libraries`：选中的媒体库 ID 列表，JSON 字符串。
- `sync_all_libraries`：是否同步所有媒体库。
- `enable_daily_first_full_sync`：是否启用每日首次定时全量同步，默认启用。
- `enable_playback_overview`：播放通知是否显示剧情简介。
- `enable_playback_progress`：播放通知是否显示播放进度。

说明：

- `selected_libraries` 是 JSON 数组字符串，不是逗号分隔文本。
- `enable_playback_overview`、`enable_playback_progress` 是后加字段。

### `emby_media_items`

同步下来的 Emby 媒体项。

- `item_id`：Emby 媒体项 ID，唯一。
- `item_id_int`：数值化 ID，便于索引和排序。
- `server_id`：Emby Server ID。
- `name`：名称。
- `type`：类型，电影 / 集 / 文件夹等。
- `parent_id`：父节点 ID。
- `series_id`、`series_name`：剧集 ID 和名称。
- `season_id`、`season_name`：季 ID 和名称。
- `library_id`：所属媒体库 ID。
- `path`：媒体路径。
- `pick_code`：关联的 PickCode。
- `media_source_path`：媒体源路径。
- `index_number`、`parent_index_number`：集号 / 季号。
- `production_year`：年份。
- `premiere_date`：首播日期。
- `date_created`、`date_modified`：时间字符串。
- `date_created_time`、`date_modified_time`：对应时间戳。
- `is_folder`：是否文件夹。
- `last_seen_sync_run`：全量同步批次标记，用于按媒体库清理旧条目。
- `last_seen_at`：最近一次被全量、增量或 Webhook 同步看到的时间戳。

### `emby_media_sync_files`

Emby 媒体项与同步文件的关联表。

- `sync_path_id`：同步目录 ID。
- `emby_item_id`：Emby 媒体项 ID。
- `sync_file_id`：同步文件 ID。
- `pick_code`：PickCode。

### `emby_libraries`

Emby 媒体库基础表。

- `name`：媒体库名称。
- `library_id`：Emby 媒体库 ID。
- `sync_path_id`：关联的同步目录 ID，`0` 表示未关联。

### `emby_library_sync_paths`

Emby 媒体库与同步目录的关联表。

- `library_id`：媒体库 ID。
- `sync_path_id`：同步目录 ID。
- `library_name`：媒体库名称。

### `emby_library_refresh_tasks`

Emby 刷新任务表。旧媒体库刷新和 STRM 更新后的 item 定向刷新共用本表；去抖、等待和执行协调见 [Emby 媒体库同步](../architecture/emby-library-sync.md)。

- `task_key`：刷新目标的唯一去重 key。媒体库刷新使用 `library:<library_id>`；item 定向刷新使用 `item:<item_id>`。
- `library_id`：真实 Emby 媒体库 ID。item 定向刷新在无法可靠解析媒体库时为空，不再混入 item 标识。
- `library_name`：媒体库名称或 item 名称。
- `sync_path_ids_str`：关联的同步目录 ID 列表，JSON 字符串。
- `target_type`：刷新目标类型，`library` 表示媒体库刷新，`item` 表示 Emby item 定向刷新；旧数据为空时按 `library` 兼容。
- `item_ids_str`：item 定向刷新目标 ID 列表，JSON 字符串。
- `item_recursive`：item 定向刷新是否递归。Movie、Video、Episode 默认为非递归；Season、Series、Folder 默认为递归。
- `fallback_library_id`：item 定向刷新失败时可使用的已解析媒体库 ID；旧值仅作为 hint，本地 item、sibling Episode 和 Ancestors 证据优先。
- `fallback_library_name`：回退媒体库名称。
- `status`：任务状态，`pending`、`refreshing`、`completed`、`failed`、`cancelled`。
- `last_event_at`：最近一次事件时间戳。
- `refresh_after_at`：去抖后的刷新执行时间。
- `deadline_at`：最长等待时间。
- `last_checked_at`：最后检查时间。
- `last_refresh_at`：最后刷新时间。
- `error`：错误信息。

刷新目标解析、任务合并、等待和取消属于运行时协调行为，见 [Emby 媒体库同步](../architecture/emby-library-sync.md)；本表只保存其持久化状态。

### `request_stats`

请求统计表。

- `request_time`：请求时间戳。
- `url`：请求 URL。
- `method`：请求方法。
- `duration`：响应时长，单位毫秒。
- `is_throttled`：是否被限流。
- `account_id`：关联账号 ID，默认 `0`。

### `db_download_tasks`

数据库下载队列表。

- `account_id`：账号 ID。
- `sync_file_id`：对应同步文件 ID。
- `sync_path_id`：STRM 同步下载任务所属同步目录 ID，用于 Emby 刷新任务判断对应目录是否仍有未完成下载；旧数据可能为 `0` 或 `NULL`，系统会回退到 `sync_file_id` 关联 `sync_files` 判断。
- `source_type`：任务来源账号类型，`115`、`local`、`123`、`openlist`、`baidupan` 或 `emby_media`。
- `remote_file_id`：远端服务实际返回的稳定文件 ID。115 为文件 ID，OpenList 仅在 API 返回非空对象 ID 时填写，百度为 `fs_id`；不支持或不可得时为空。
- `file_name`：文件名。
- `remote_path`：远端目录路径，不含文件名。
- `remote_full_path`：创建任务时确定的远端完整文件路径，包含文件名；后续远端改名或移动不会回写历史任务。
- `remote_pick_code`：115 PickCode；仅 115 使用。
- `remote_sha1` / `remote_md5`：远端响应明确声明算法的 SHA1 / MD5，不使用本地计算值或算法未知的哈希兜底；当前仅 115 与 OpenList 可写入 SHA1，百度可写入 MD5，其他未确认支持的来源保持空。
- `remote_download_url`：OpenList 签名直链或 Emby 媒体提取地址，仅下载 worker 使用，序列化时排除，HTTP / SSE 和前端均不可见。
- `emby_item_id`：Emby 媒体提取运行定位，仅 worker 使用，序列化时排除。
- `local_source_path`：本地复制任务的源路径，仅 worker 使用，序列化时排除。
- `dedup_scope_hash`、`dedup_locator_hash`：活跃下载任务去重范围和来源定位值的 SHA-256 摘要，仅数据库约束、创建竞争处理和失败重试使用，序列化时排除；不重复索引 OpenList 或 Emby 的签名直链。
- `local_full_path`：本地落盘路径。
- `source`：下载来源，`strm_sync`、`local_file` 或 `emby_media`。
- `status`：下载状态。
- `size`：文件大小。
- `start_time`、`end_time`：开始和结束时间戳。
- `error`：错误信息。
- `mtime`：下载完成后要回写的文件修改时间。
- `retry_count`：重试次数。
- `last_retry_time`：最近重试时间。

说明：

- `source` 的存储值在版本 `43` 迁移中从展示文案统一成稳定枚举值。
- `source_type` 仍然表示账号来源，不等于 `source`。
- 活跃下载任务的去重范围为 `source + source_type + account_id + sync_path_id +` 来源对应的远端定位值；`sync_path_id=0` 的临时任务改以本地目标路径隔离。不同账号、存储类型或同步目录的相同远端 ID 不能互相阻止入队。范围与定位值以摘要写入部分唯一索引，`pending`、`downloading` 两种活跃状态由数据库最终保证；应用层预检查仅用于返回明确状态。
- 定位值为 115 / 百度 / 一般来源的 `remote_file_id`，OpenList 优先对象 ID、缺失时使用隐藏直链，local 使用隐藏源路径，Emby 使用隐藏提取地址。定位为空的历史任务不参与约束；失败任务可保留为历史并创建替代任务，手动或自动重试时若已有活跃替代任务则保持失败状态和原重试信息。
- local 和 Emby 媒体任务不伪造远端路径、文件 ID、PickCode 或哈希；OpenList 缺少对象 ID 时文件 ID 保持空，下载直链只存入隐藏字段。
- 用户清空下载队列等待任务时，系统会取消受影响同步目录对应的待刷新媒体库任务；暂停队列和重试失败任务不会取消媒体库刷新任务。

### `db_upload_tasks`

数据库上传队列表。

- `source`：上传来源，`strm_sync`、`scrape_organize` 或 `directory_monitor`。
- `account_id`：账号 ID。
- `sync_file_id`：同步文件 ID。
- `scrape_media_file_id`：刮削文件 ID。
- `sync_path_id`：同步目录 ID，目录监控上传和后续 STRM 生成使用。
- `source_type`：任务来源账号类型，`115`、`local`、`123`、`openlist`、`baidupan` 或 `emby_media`。
- `local_full_path`：本地完整路径。
- `relative_path`：目录监控源文件相对监控根目录的路径。
- `source_fingerprint`：目录监控源文件 fingerprint，格式为 `v1:size:mtime_ns`，不包含 ctime、inode 或文件内容 hash。
- `remote_file_id`：本次上传完成后远端返回的稳定文件 ID；创建和传输阶段为空，不再复用为目标路径或被覆盖的旧文件 ID。
- `remote_full_path`：创建任务时确定的远端完整目标路径，包含文件名；上传去重和后续 STRM 路径推导使用该字段。
- `remote_path_id`：远端父目录 ID，仅来源实际提供时填写；115 为父目录 ID，其他不提供稳定 ID 的来源为空。
- `remote_pick_code`：115 上传完成后远端返回的 PickCode。
- `remote_sha1` / `remote_md5`：远端响应明确返回的 SHA1 / MD5。115 仅接受回调或文件详情中的 SHA1；百度上传成功时把 `xpan/file/create` 的 MD5 写入 `remote_md5`。
- `replaced_remote_file_id`：覆盖上传且旧远端文件删除成功后记录的旧文件 ID；115 使用文件 ID，百度使用 `fs_id`，OpenList 使用非空对象 ID；非覆盖、删除失败或来源未提供稳定 ID 时为空。
- `file_name`：上传文件名。
- `status`：上传状态。115 上传在远端已确认完成、但本地结果落库和 STRM 入队等收尾尚未完成时，会先处于 `5`，队列阶段字段为 `remote_completed_pending_finalize`，前端显示为“等待完成处理”；执行收尾的 worker 会原子抢占为 `6`，队列阶段字段为 `remote_completed_finalizing`，前端显示为“正在完成处理”。
- `file_size`：文件大小。
- `local_mtime`：上传前本地源文件秒级 mtime，保留用于历史兼容和排查；目录监控源文件执行前和删除前校验使用 `source_fingerprint`。
- `local_mtime_ns`：上传前本地源文件纳秒级 mtime，用于目录监控源文件 fingerprint 和后续幂等关联。
- `uploaded_bytes`：已上传字节数，用于上传队列展示和恢复进度快照。
- `upload_result`：上传结果，`unknown`、`rapid_upload`、`multipart_uploaded`、`remote_exists` 或 `skipped_after_rapid_wait`。
- `resume_state`：断点续传状态，`none`、`new_session`、`resumed_session` 或 `session_expired_restarted`。
- `rapid_wait_attempts` / `rapid_wait_until`：秒传等待尝试次数和截止时间。
- `error`：错误信息。
- `start_time`、`end_time`：开始和结束时间戳。
- `retry_count`：重试次数。
- `last_retry_time`：最近重试时间。
- `source_cleanup_status`：目录监控上传后的源文件清理状态，`none`、`pending`、`completed` 或 `failed`。
- `source_cleanup_error`：源文件清理失败原因。
- `source_deleted_at`：源文件删除时间戳。
- `is_season_or_tvshow_file`：是否为剧集或电视剧文件。

运行时字段：

- `upload_phase`：上传阶段，仅用于队列列表和 SSE 展示，不入库。
- `upload_speed_bytes`：最近一次进度事件计算出的上传速度，不入库。
- `progress_percent`：根据 `uploaded_bytes / file_size` 计算的进度百分比，不入库。
- `total_parts` / `uploaded_parts`：从 `upload_sessions` 补齐的分片进度展示字段，不入库。

说明：

- 115 上传任务完成后会保存 `upload_result`、`resume_state`、`uploaded_bytes`、远端文件 ID、PickCode 和远端确认的 SHA1。`upload_result=remote_exists` 表示远端同路径文件 SHA1 和大小均匹配，因此跳过真实上传；`upload_result=rapid_upload` 会在秒传成功后按 `file_id` 查询详情补齐远端 mtime 和可得 SHA1。115 官方秒传返回不包含 mtime；`strm_sync` 上传依赖详情查询同步本地元数据 mtime，目录监控上传在详情查询失败时可先用 `file_id` 兜底，但不得把本地 session SHA1 写入公开任务字段。目录监控上传任务还会保存上传前本地文件 mtime、纳秒级 mtime 和源文件 fingerprint，上传执行前和源文件清理前都会校验当前文件 fingerprint，防止同路径文件被替换后误传或误删。目录监控上传远端结果确认后，会先把上传任务置为 `remote_completed_pending_finalize`，收尾 worker 通过条件更新抢占到 `remote_completed_finalizing` 后会清除上一次收尾错误，再保存任务最终结果、创建 STRM 任务和推进 processed 账本；如果收尾失败，会退回 `remote_completed_pending_finalize` 供上传队列重试，进程重启时遗留的 `remote_completed_finalizing` 也会恢复为等待完成处理。目录监控上传完成并保存任务最终结果后，会按 `upload_result` 把对应 `directory_upload_processed_files.result` 标记为 `uploaded_pending_strm` 或 `remote_exists_pending_strm`；STRM 入队成功后才更新为 `uploaded` 或 `remote_exists` 终态；STRM 入队失败时更新为 `strm_enqueue_failed`，后续扫描会重试 STRM 入队而不是重新上传；`skipped_after_rapid_wait` 不会写入终态。
- 所有上传入口保留应用层预检查以返回明确状态，但部分唯一索引才是并发创建的最终保证。失败任务可以保留为历史记录并创建替代任务；手动或自动重试失败任务时，若同一范围已有活跃替代任务，旧任务保持失败且不递增重试次数。
- 115 上传任务成功后，`strm_sync` 和 `directory_monitor` 来源会按远端文件 ID / PickCode 创建 `strm_generation_tasks` 记录；刮削整理来源仍只执行原有后处理。历史 `strm_sync` 上传缺少完整路径时，只能在实际入队后处理时按其新完成文件 ID 查询一次详情，结果只用于该 STRM 任务，不能回写上传任务；查询失败必须显式失败，不能把文件 ID 当路径。

### `upload_sessions`

115 上传会话表，保存上传初始化、断点续传和 OSS multipart checkpoint。STS 临时凭证不写入本表。

- `upload_task_id`：关联 `db_upload_tasks.id`，唯一。
- `account_id`：115 账号 ID。
- `local_full_path`、`file_name`、`file_size`、`local_mtime`、`local_signature`：本地文件恢复校验信息。
- `file_sha1`、`preid`：115 秒传、续传和本地 session 复用校验所需文件签名；`file_sha1` 不写入 OSS callback，`CompleteMultipartUpload` 中的 `${sha1}` 由 OSS 基于最终对象回填。
- `parent_file_id`、`target`、`file_id`、`pick_code`：115 调度和远端定位字段。
- `sign_key`、`sign_range_start`、`sign_range_end`、`sign_val_sha1`：二次认证相关字段。
- `last_init_at`、`last_resume_at`：最近一次 init / resume 调度时间。
- `callback`、`callback_var`：OSS complete 时需要带回 115 的回调参数。
- `bucket`、`object`、`endpoint`、`region`、`upload_id`：OSS multipart 调度信息。
- `part_size`、`total_parts`、`uploaded_bytes`、`uploaded_parts`、`last_part_number`、`last_part_etag`：分片进度快照。
- `status`：上传会话状态，`init`、`rapid_waiting`、`multipart`、`completing`、`completed`、`aborted`、`failed`。
- `resume_state`：恢复状态。
- `rapid_wait_until`、`rapid_wait_attempts`：秒传等待状态。
- `retry_count`、`last_error`、`last_progress_at`：重试和排错信息。
- `upload_started_at`、`completed_at`：上传开始和完成时间。
- `complete_callback_state`、`complete_callback_error`：OSS complete 后 115 callback 业务状态。
- `completed_file_id`、`completed_pick_code`、`completed_parent_id`、`completed_sha1`、`completed_size`、`completed_mtime`：最终远端文件信息。

说明：

- `upload_task_id` 仍保持单任务一个当前 session。进程重启只恢复上传任务状态，不删除本表记录。
- 有效 session 重试时会先走 115 `/open/upload/resume`，再用 OSS `upload_id` 查询已上传 part 并跳过已完成分片。
- 本地文件大小、mtime、SHA1 或快速签名发生变化时，不复用旧 checkpoint；复用当前 session 记录写入新本地签名，清空 115 调度、OSS multipart、分片进度和完成态字段，将状态设为 `init`、恢复状态设为 `session_expired_restarted`，并在 `last_error` 留下废弃原因。上传执行会立即重新 init，不新增 session 历史记录，也不先标记上传任务失败。
- 本地签名仍匹配但 OSS 返回 `NoSuchUpload`、`InvalidUploadId` 等 checkpoint 已失效错误时，会将当前 session 标记为 `session_expired_restarted`，清空 `upload_id`、part size、已上传字节数和分片进度，并在同一次任务中复用当前 115 调度结果创建新的 OSS multipart。

### 版本 61 迁移与回退

版本 `60 → 61` 先增加新列，再仅使用已有任务和关联 `sync_files` 回填可靠数据：115 下载可回填文件 ID、PickCode、SHA1 和完整路径；百度的 `sync_files.pick_code` 为可证明的 `fs_id`，回填到 `remote_file_id`，其历史 `sync_files.sha1` 语义为 MD5，只回填到 `remote_md5`；OpenList、Emby 和 local 的旧执行定位分别迁入隐藏的直链、Emby 条目 ID 和本地源路径。无法证明的路径、对象 ID、PickCode 或 SHA1 保持空，不发起远端补查。

上传任务由旧完成字段回填新完成身份；旧 `remote_file_id` 只有可证明为完整目标路径时才迁入 `remote_full_path`，只有可证明为 STRM 覆盖且新旧完成 ID 不同时才迁入 `replaced_remote_file_id`。迁移会删除 `completed_remote_file_id` 和 `completed_pick_code`，但不删除 `upload_sessions` 内部 checkpoint。若迁移在任一字段回填或两列删除之间中断，重试会跳过已清空 / 删除的旧列，并保留已经迁入的 PickCode、隐藏执行定位及完成身份字段。

迁移会为 `remote_full_path` 非空的活跃上传任务创建 `idx_db_upload_tasks_active_target`。若旧数据在相同 `source + source_type + account_id + remote_full_path` 范围内已有多个活跃任务，先保留进度最高的一个（正在完成处理、等待完成处理、上传中、等待上传依次优先；相同状态保留 ID 最小者），其余任务标记为已取消并写入迁移原因，再创建索引。已处于版本 `61` 但缺少该索引的数据库会在启动时补齐相同约束。

升级前必须备份数据库。版本 61 删除了旧列，回退到依赖它们的旧二进制不安全；升级期间不得让新旧二进制混合连接同一个数据库。

### `directory_upload_processed_files`

目录监控源文件处理账本，用于在服务重启、内存 TTL 过期或保留源文件不删除时保持幂等。

- `rule_id`、`sync_path_id`、`account_id`：目录监控规则、同步目录和账号 ID。
- `scope_hash`：规则处理范围哈希，包含规则、同步目录、账号、本地监控目录和远端上传根目录。
- `source_key`：`scope_hash + relative_path` 的稳定哈希，唯一。
- `relative_path`、`local_full_path`：源文件相对监控根目录路径和本地完整路径。
- `source_fingerprint`：源文件 fingerprint，格式为 `v1:size:mtime_ns`。
- `file_size`、`local_mtime_ns`：生成 fingerprint 时使用的文件大小和纳秒级 mtime。
- `result`：处理结果，`queued`、`pending_replace`、`uploaded_pending_strm`、`remote_exists_pending_strm`、`strm_enqueue_failed`、`uploaded`、`remote_exists`、`skipped_existing` 或 `failed`。
- `upload_task_id`：关联 `db_upload_tasks.id`；`skipped_existing` 和 `failed` 可为空。
- `processed_at`、`last_seen_at`：处理时间和最近一次扫描看到同一源文件的时间。

说明：

- `uploaded`、`remote_exists` 和 `skipped_existing` 是终态幂等结果；同一 `source_key` 和 `source_fingerprint` 再次出现时会更新 `last_seen_at` 并跳过入队。
- `uploaded_pending_strm`、`remote_exists_pending_strm` 和 `strm_enqueue_failed` 表示远端上传结果已确认但 STRM 尚未成功入队；同一文件只重试 STRM 入队，不重新上传。`failed` 不是终态，后续扫描允许重试。
- 删除目录监控规则或同步目录时会清理关联记录。运行时的活跃任务判断、收尾、重扫和清理策略见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。

### `directory_upload_rules`

目录监控上传规则表，绑定同步目录和 115 上传目标。一个同步目录可以关联多条目录监控规则，每条规则对应一个本地监控目录。

- `sync_path_id`：关联同步目录 ID。
- `account_id`：115 账号 ID。
- `enabled`：规则自身是否启用；还需要所属同步目录的 `directory_upload_enabled=true` 才会在运行时生效。
- `monitor_path`：本地监控目录。
- `remote_root_path` / `remote_root_id`：目标目录路径和目录 ID，二者都不能为空，且必须位于同步目录远端路径下。
- `recursive`：是否递归监控子目录，默认 `true`。
- `upload_metadata`：是否上传当前同步目录识别为元数据扩展名的文件，默认 `false`。
- `watch_mode`：监控模式，`auto`、`fsnotify` 或 `polling`。
- `stability_seconds`、`stability_check_interval_seconds`、`stability_required_count`：历史稳定性字段，新建规则写入内置默认值，运行时不读取 DB 值。
- `rescan_interval_seconds`：历史补偿扫描间隔字段，新建规则写入内置默认值 `30`，运行时不读取 DB 值。
- `startup_scan_enabled`、`processed_cache_ttl_seconds`：启动查漏和目录上传内存 TTL 去重参数。
- `delete_source_after_success`：上传成功且 STRM 生成成功后是否删除源文件，默认 `false`。
- `ignore_patterns_str`：忽略规则 JSON 字符串。
- `overwrite_mode`：同名文件处理方式，`skip_same`、`fail_conflict` 或 `replace_conflict`，默认 `skip_same`。

说明：

- 同一同步目录下允许多条规则，但完全相同的 `monitor_path + remote_root_path + remote_root_id` 组合会被拒绝。同步目录与规则通过聚合写接口在同一事务保存；完整请求、幂等和错误返回见 [同步目录聚合 API](sync-path-api.md)。
- 目录监控的文件发现、稳定性检查、内存去重、watch mode 自动降级、补偿扫描、同名文件处理和源文件清理属于运行时流程，见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。本表只保存该流程需要跨重启保持的规则配置。

### `strm_generation_tasks`

STRM 生成任务表，上传完成、远端已存在跳过和 [STRM Webhook](strm-webhook.md) 会共用该任务模型。

- `source`：任务来源，`upload_completed`、`webhook` 或 `remote_exists`。
- `task_type`：任务类型，`file`、`directory_scan` 或 `batch_files`。
- `parent_task_id`：目录扫描或批量父任务 ID。
- `upload_task_id`：关联上传任务 ID。
- `sync_path_id`、`account_id`：同步目录和账号 ID。
- `download_meta`、`refresh_emby`：Webhook STRM 生成选项，默认关闭；`download_meta` 只对 `source=webhook` 且 `task_type=file` 的任务生效。
- `file_id`、`parent_id`、`pick_code`、`path`、`file_name`、`file_size`、`sha1`、`mtime`：文件级 STRM 生成所需远端信息。
- `directory_id`、`directory_path`、`total_items`、`accepted_items`、`failed_items`：目录级扫描或批量父任务信息和统计。
- `changed_items`、`new_meta_items`：父任务统计中发生 STRM 变更和新增元数据下载任务的子任务数量。
- `refresh_targets_str`、`refresh_submitted`：父任务收集到的 Emby 刷新目标和是否已提交刷新。
- `status`：任务状态。
- `request_hash`：幂等请求哈希，非空时唯一；Webhook 和目录扫描子任务使用 `*:v2:<sha256>` 短摘要格式，不保存明文长路径。并发创建相同哈希时，入队逻辑会使用数据库唯一键冲突忽略并复查已有任务，调用方应看到复用已有活跃任务而不是唯一键错误。
- `retry_count`、`last_retry_time`、`last_error`：重试和失败信息。

说明：

- 程序启动时会把上次异常退出遗留的 `running` 任务恢复为 `pending`，然后由后台 worker 按 ID 顺序领取待处理任务。
- `batch_files` 父任务不由 worker 执行；创建后处于 `waiting_children`，表示子任务尚未全部进入终态。子任务累计达到 `total_items` 后，父任务无失败时转为 `completed`，存在失败时转为 `failed`。
- 同一个 `batch_files` 请求在父任务仍为活跃状态时会复用父任务，并按每个合法 item 的原始 `items[]` index 匹配或补建缺失子任务；同批次重复文件项不会互相复用子任务。首次创建父任务与合法子任务在同一事务内完成，任一子任务创建失败会回滚父任务，重试时重新创建完整集合。
- `directory_scan` 父任务由 worker 异步展开远端目录，只为视频文件创建 `file` 子任务；115 目录枚举会按 `file_list_page_size` 分页累加所有文件列表结果，不会只处理最后一页。展开完成后 `total_items` 表示子任务总数，子任务后续完成或失败时累计 `accepted_items` / `failed_items`，不会把 `total_items` 降为当前已处理数。
- `request_hash` 只对 `pending` / `running` / `finalizing` / `waiting_children` 任务做幂等去重；如果历史任务已经 `failed`、`completed` 或 `cancelled`，再次提交同一请求会归档旧哈希并创建新任务。并发归档或创建时会在小范围内重试；批量 Webhook 还会在 SQLite 锁冲突时回滚并重试整个父子任务事务，避免锁错误泄漏给调用方。升级后新请求会写入短格式哈希；旧格式活跃任务仍可被相同请求复用。
- worker 自动领取 `pending` 和 `finalizing` 任务；执行失败会把任务标记为 `failed`，递增 `retry_count` 并写入 `last_error`，不会删除已成功写出的新 STRM。成功生成的批量 / 目录扫描子任务会先累计父任务进度并进入 `finalizing`，待父任务刷新提交成功或确认无需提交后才转为 `completed`，避免父刷新失败后子任务不可重试或重复累计父进度。
- 文件级任务会通过 `sync_path_id` 加载同步目录和账号。任务只提供 `file_id` 且缺少路径、文件名或 PickCode 时，会先补查远端详情再生成 STRM。
- 115 文件级任务生成前会检查同一 `sync_path_id + local_file_path` 是否已有其他视频记录；没有冲突时不新增远端请求，存在候选时列出一次父目录并按 `Ptime`、FileID 顺序确认 owner。目录扫描已经持有父目录列表，会在创建子任务前直接过滤 non-owner。
- Webhook 文件任务只有 `refresh_emby=true` 且 STRM 变更或新增元数据下载任务时才解析 Emby 目标；批量和目录扫描子任务会把目标累计到父任务，只有父任务全部子任务成功完成且存在 STRM / 元数据变化时才统一提交一次，任一子任务失败则父任务失败且不提交刷新。
- 上传完成、远端已存在等非 Webhook 文件任务保持原有行为：STRM 新增或更新后，优先提交 Emby item 级定向刷新，定位不到可靠 item 时回退同步目录关联媒体库刷新。
- 同一远端文件移动目录或重命名后，系统会以 `file_id` / `pick_code` 查找旧 `SyncFile`，新 STRM 写入成功后只删除旧记录里的 `local_file_path`，不做文件名模糊匹配。
- 目录监控上传任务关联的 STRM 生成任务完成后，会按 `strm_generation_tasks.upload_task_id` 反查对应上传任务并尝试清理本地源文件。清理结果回写到 `db_upload_tasks.source_cleanup_status`、`source_cleanup_error` 和 `source_deleted_at`。

### `backup_config`

自动备份配置表。

- `backup_enabled`：是否启用自动备份。
- `backup_cron`：备份 Cron 表达式。
- `backup_path`：备份存储路径。
- `backup_retention`：保留天数。
- `backup_max_count`：最多保留数量。
- `backup_compress`：是否压缩备份。

### `backup_record`

备份历史记录表。

- `task_id`：关联的任务 ID。
- `status`：状态，`pending`、`running`、`completed`、`failed`、`cancelled` 或 `timeout`。
- `file_path`：备份文件路径。
- `file_size`：备份文件大小，单位字节。
- `database_size`：数据库大小，单位字节。
- `table_count`：备份表数量。
- `backup_duration`：备份耗时，单位秒。
- `backup_type`：备份类型，`manual` 或 `auto`。
- `created_reason`：创建原因。
- `failure_reason`：失败原因。
- `compression_ratio`：压缩比。
- `is_compressed`：是否已压缩。
- `completed_at`：完成时间戳。

### `notification_channels`

通知渠道基础表。

- `channel_type`：渠道类型，当前内置实现包括 `telegram`、`meow`、`bark`、`serverchan`、`webhook`；允许创建多个相同类型渠道。
- `channel_name`：渠道名称。
- `description`：渠道说明。
- `is_enabled`：是否启用。
- `created_at` / `updated_at`：创建和更新时间，使用 `time.Time`。

### `telegram_channel_configs`

Telegram 渠道配置表。

- `channel_id`：关联的通知渠道 ID。
- `bot_token`：Bot Token。
- `chat_id`：Chat ID。
- `proxy_url`：代理地址。

### `meo_w_channel_configs`

MeoW 渠道配置表。

- `channel_id`：关联的通知渠道 ID。
- `nickname`：昵称。
- `endpoint`：请求地址，默认 `http://api.chuckfang.com`。

### `bark_channel_configs`

Bark 渠道配置表。

- `channel_id`：关联的通知渠道 ID。
- `device_key`：设备 Key。
- `server_url`：服务地址，默认 `https://api.day.app`。
- `sound`：提示音，默认 `alert`。
- `icon`：图标。

### `server_chan_channel_configs`

Server酱 渠道配置表。

- `channel_id`：关联的通知渠道 ID。
- `sc_key`：Server酱 Key。
- `endpoint`：服务地址，默认 `https://sc.ftqq.com`。

### `custom_webhook_channel_configs`

自定义 Webhook 渠道配置表。

- `channel_id`：关联的通知渠道 ID。
- `endpoint`：请求地址。
- `method`：请求方法，`GET` 或 `POST`。
- `template`：模板字符串，支持 `{{title}}`、`{{content}}`、`{{timestamp}}`、`{{image}}`。
- `format`：POST 格式，`json`、`form` 或 `text`。
- `auth_type`：鉴权方式，`none`、`bearer`、`basic`、`header`、`query`。
- `auth_token`：鉴权令牌。
- `auth_user` / `auth_pass`：Basic Auth 用户名和密码。
- `auth_header_key`：Header 模式下的头名。
- `auth_query_key`：Query 模式下的参数名。
- `headers`：额外请求头，JSON 对象字符串，可保存多个自定义请求头。
- `query_param`：GET 模式下承载模板的参数名，默认 `q`。

### `notification_rules`

通知规则表。

- `channel_id`：关联的通知渠道 ID。
- `event_type`：事件类型，`sync_finish`、`sync_error`、`scrape_finish`、`scrape_error`、`system_alert`、`media_added`、`media_removed`、`playback_start`、`playback_pause` 或 `playback_stop`。
- `is_enabled`：是否启用。
