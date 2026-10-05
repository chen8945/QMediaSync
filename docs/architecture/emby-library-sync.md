# Emby 媒体库同步

> 职责：定义 Emby 刷新、条目同步、Webhook 持久处理、联动删除和运行状态的边界。
>
> 权威范围：本文档维护 Emby 两条同步链路和刷新协调器；时间策略见 [数据库 schema 与迁移](../reference/database-schema.md#时间字段策略)，Cron 校验见 [请求校验约定](../engineering/request-validation.md#cron-表达式边界)。
>
> 修改时机：修改 Emby 刷新入口、条目同步、刷新任务状态、目标解析、去抖或 Webhook 行为时必须更新本文档。
>
> 相关代码：`backend/internal/emby/`、`backend/internal/models/emby_*.go`、`backend/internal/synccron/`、`backend/internal/controllers/emby*.go`、`frontend/src/components/AppEmbySettings.vue`。

实现细节以 `backend/internal/emby`、`backend/internal/models`、`backend/internal/synccron` 和 `frontend/src/components/AppEmbySettings.vue` 为准。

## 两条链路

Emby 相关任务分为两条独立链路，不能混用。

### 链路 A：通知 Emby 刷新

含义：QMediaSync 通知 Emby 刷新已有 item 或重新扫描它自己的媒体库。

刷新功能需要满足以下前置条件：

- 已配置 Emby URL 和 API Key，并启用 `enable_refresh_library`。
- 刷新来源必须属于已保存的同步目录，临时同步目录不提交刷新。
- 建议先完成一次链路 B 的 Emby 条目同步，用于建立 `emby_media_items`、`emby_media_sync_files` 和 `emby_library_sync_paths` 关联。没有 item 关联时会尝试回退媒体库刷新；同步目录也没有媒体库关联时会跳过提交。

#### 当前有效入口

| 入口 | 上游触发方式 | 提交条件 | 提交函数 |
| --- | --- | --- | --- |
| 常规 STRM 同步 | 全局同步、单同步目录同步、115 全量同步、全局或目录自定义 Cron、Telegram `/strm_inc` 和 `/strm_sync` | 非临时同步目录成功完成，且新增 STRM 或新增元数据下载任务 | `models.RequestEmbyRefreshTargets(...)` |
| 上传完成后的 STRM 生成 | 目录监控上传完成、STRM 上传完成或远端文件已存在 | 非 Webhook 文件任务的 STRM 实际新增或更新 | `models.RequestEmbyRefreshBySyncFile(...)` |
| STRM Webhook | `POST /api/strm/webhook` 的单文件、批量文件或目录扫描请求 | 显式设置 `refresh_emby=true`，并且 STRM 发生变化或新增元数据下载任务 | `models.RequestEmbyRefreshTargets(...)` |

常规 STRM 同步包括以下管理入口：

- `POST /api/sync/start`：把符合条件的同步目录加入队列。
- `POST /api/sync/path/start`：启动单个同步目录。
- `POST /api/sync/path/full-start`：启动单个 115 同步目录的全量同步。
- 全局同步 Cron 和同步目录自定义 Cron。
- Telegram `/strm_inc`、`/strm_sync`。

`new_meta` 表示已创建元数据下载任务，不表示元数据已经下载完成。常规同步完成后先提交刷新任务，刷新协调器再等待这些下载任务结束。

`POST /api/sync/manual` 使用临时同步目录，不提交 Emby 刷新任务。

上传完成、远端已存在等非 Webhook STRM 任务由 `strm_generation_tasks` worker 处理。只有 STRM 内容实际变化时才提交刷新；STRM 内容不变时只确认或更新 `sync_files`，不创建新的刷新任务。

完整同步在 STRM 写入成功并释放目标锁后收集刷新目标，解析失败仍记录告警并回退媒体库目标。单文件后处理由服务按任务来源处理刷新，不在底层写文件时重复解析；解析或提交失败仍返回任务重试。两条入口都等待自己负责的刷新处理结束，提交成功不代表 Emby 已扫库完成。

STRM Webhook 的具体规则：

- `refresh_emby` 默认关闭，调用方必须显式设置为 `true`。
- 单文件任务只有在 STRM 变化或新增元数据下载任务时提交刷新。
- 批量文件和目录扫描先由子任务累计刷新目标。
- 只有父任务全部子任务成功、计数满足且存在 STRM 或元数据变化时，才统一提交一次刷新。
- 任一子任务失败时父任务失败，不提交刷新。

#### 当前不是刷新入口的功能

- `POST /api/emby/sync/start`、Emby 条目同步 Cron 和 `POST /emby/webhook` 属于链路 B，只维护 QMediaSync 本地 Emby 索引，不调用刷新接口。
- 前端当前只有“同步后刷新媒体库”开关，没有“立即刷新媒体库”按钮，也没有独立的刷新 API。
- `models.RefreshEmbyLibraryBySyncPathId(...)` 是直接请求 Emby 的旧函数，当前没有生产调用方；当前刷新统一经过任务协调器。
- Telegram `strm_scrape` 对应的同步、刮削后刷新代码仍存在，但命令注册已注释，不属于当前有效入口。
- `refresh_library` 状态值已经在后端常量和前端文案中定义，但刷新协调器当前不会设置 `emby_config.sync_mode`，因此 `/api/emby/sync/status` 不展示刷新任务的实际运行状态。

#### 统一运行流程

```text
常规 STRM 同步 / 上传后处理 / STRM Webhook
                    │
                    ▼
            解析 Emby 刷新目标
            item 优先，媒体库兜底
                    │
                    ▼
    创建或合并 emby_library_refresh_tasks
            task_key 去重 + 10 秒防抖
                    │
                    ▼
      等待相关 STRM 同步和下载任务结束
                    │
                    ▼
       按 item 或媒体库调用 Emby Refresh API
```

完整流程如下：

1. 上游任务确认存在 STRM 或元数据变化。
2. 调用 `ResolveEmbyRefreshTarget(...)` 解析 item 或媒体库目标。
3. 调用 `RequestEmbyRefreshTargets(...)`、`RequestEmbyRefreshBySyncFile(...)` 或内部回退函数 `RequestEmbyLibraryRefreshBySyncPathId(...)`。
4. 创建或合并 `EmbyLibraryRefreshTask`，将任务状态设置为 `pending`，刷新时间设置为最后事件时间加 10 秒；提交后协调器会重新检查所有 pending 任务。
5. 全局 timer 指向最早的 `pending.refresh_after_at`，到期后唤醒协调器；60 秒 ticker 作为兜底扫描。
6. 协调器先按真实媒体库归并 pending item，再逐条判断同步目录和下载队列是否稳定。
7. 满足条件后原子地把任务从 `pending` 改为 `refreshing`，防止同一任务并发执行。
8. 调用 Emby item 或媒体库刷新接口，并将任务标记为 `completed`；请求失败时标记为 `failed`。

#### 刷新目标解析

刷新目标按以下顺序解析：

1. 根据 `sync_file_id` 或 PickCode 查询本地 `emby_media_sync_files` 和 `emby_media_items` 关联。
2. 已有关联的 Movie、Video、Episode 刷新对应 item。
3. 同季新增剧集没有自身 Episode 关联时，按同步目录和路径查找同目录 sibling Episode，只检查最新的 50 条记录并排除自身。优先刷新 Season，缺少 Season 时刷新 Series。Season、Series 和 Folder 使用递归刷新。
4. 本地索引无法定位 item 时，按 STRM 本地路径请求 Emby `/Items?Path=...` 做一次兜底查询。
5. 路径查询命中后，优先采用本地 item 或 sibling Episode 的媒体库证据；必要时通过 Emby Ancestors 消歧，最后才使用同步路径唯一关联的媒体库。
6. 仍无法定位可靠 item 时，回退同步目录关联媒体库刷新。

远端 Ancestors 解析按 Emby 服务、凭据和 item ID 缓存 30 秒。同一 item 的并发解析会合并为一次请求，远端失败结果不缓存。

item 级刷新任务使用 `task_key=item:<item_id>` 去重，媒体库刷新任务使用 `task_key=library:<library_id>` 去重。`library_id` 只保存真实媒体库 ID 或为空；`fallback_library_id` 只保存当前解析出的有效媒体库或旧任务 hint。解析优先级为本地 item、Season/Series sibling Episode、Emby Ancestors、同步路径唯一关联媒体库和仍属于候选集合的旧 hint。多库且没有可靠证据时保持 unresolved，不会任选第一个媒体库。某组即将被已有 pending library 吸收或达到阈值转换时，协调器会在同一个聚合事务中使用当前本地索引重新校验该组 hint；如果本地 item、sibling Episode 或唯一同步目录给出更强证据，会先修复持久化归属，并把归属变化的 item 留到重新分组后再处理。该校验不会调用 Emby HTTP API，本地没有更强证据时保留可能来自 Ancestors 的旧 hint。未达到吸收或转换条件的普通 pending 组不会执行逐 item 复核，避免连续提交时产生重复全量查询。

批量目标会在提交前去重。通用 library fallback 会先展开为同步目录关联的精确 `library:<library_id>` 目标，再覆盖同库 item；不同媒体库的目标互不影响。

目标解析结果需要区分两类 unresolved：

- 完全无法定位 Emby item 时返回通用 library target。该目标会展开为同步目录关联的全部媒体库，library fallback 优先于 item 数量阈值。
- 已找到 item ID，但无法确定所属媒体库时，保留 `item` target 且 `fallback_library_id` 为空。这类任务不参与任何媒体库阈值统计，也不会根据同批次其他 item 猜测媒体库。

#### 媒体库级 pending 聚合

聚合阈值固定为 `10`，统计同一个真实 `library_id` 在滚动防抖窗口内的唯一 pending item 任务。滚动窗口不是固定时间桶，组级结束时间为：

```text
group_refresh_after_at = max(组内所有 pending item 的 refresh_after_at)
```

因此该规则同时覆盖单次批量提交、不同入口和不同批次产生的任务。新 item 或相关下载事件会延长整个媒体库组的稳定窗口。

协调器按以下优先级处理：

1. 已有 `pending` 的精确 library 任务时，无论 item 数量是否达到 10，都吸收同库 pending item。
2. 没有 library 任务且防抖窗口尚未结束时，保持 item 任务，并将组内 `refresh_after_at` 同步为最大值。
3. 防抖窗口结束后，少于 10 个 item 保持 item 刷新。
4. 达到 10 个或更多唯一 item 时，创建或更新精确 `library:<library_id>` 任务，并将被覆盖的 item 标记为 `cancelled`，保留取消原因用于审计。

聚合、字段合并和 item 取消在同一个数据库事务中完成。事务开始时先取消已经过期的 pending 任务，再查询有效 pending item；过期任务不会参与阈值，也不会被当作有效 pending library 重新使用。`last_event_at` 和 `refresh_after_at` 取最大值，`deadline_at` 取最早有效值，`sync_path_ids` 取并集；不会因为合并重新获得完整的 6 小时等待时间。已有 pending library 会保留当前周期的 `item_ids` 并继续合并；completed、failed、cancelled 或已过期任务进入新周期时会清空历史 `item_ids`，避免任务负载随媒体库生命周期持续增长。

聚合查询只读取有效 pending item 和这些 item 对应的 library 任务，不加载 completed、failed 或 cancelled 的全部历史记录。活动周期内新增同步目录或新事件不会重置原有 deadline；只有新任务或 terminal 任务重新激活时才创建新的最长等待时间。

并发控制按数据库类型采用混合策略：PostgreSQL 在事务内按固定顺序锁定 library 和 item 行；SQLite 使用进程级 Emby 刷新任务写锁配合短事务。两种模式都保留 `status` 条件更新和唯一 `task_key` 约束，状态领取前会重新确认任务仍已到达防抖时间，避免旧扫描提前刷新新事件。并发创建同一个 `library:<library_id>` 发生唯一键冲突时，当前事务会重新锁定已有任务、重新合并字段并保存；只有保存成功后才取消被吸收的 item，保存失败则整体回滚。

扫描器不会把读取到的旧任务整行写回。readiness 检查出错或任务暂不可执行时，只在任务仍为 `pending`，且 `last_event_at`、`refresh_after_at`、`deadline_at` 与扫描快照一致时更新 `last_checked_at`；检查出错时同时更新 `error`。如果期间有新事件或状态变化，条件更新不命中，旧检查结果直接丢弃。deadline 到期取消同样要求数据库中的 `deadline_at` 与旧快照一致且仍已过期；新事件已续期时，旧扫描不能取消新周期任务。SQLite 进程锁只覆盖对应的单条条件更新，PostgreSQL 依靠原子 `UPDATE ... WHERE ...` 保证该语义。

SQLite 刷新任务写锁假设同一个 SQLite 数据库文件由单个 QMediaSync 进程写入；如果未来需要多进程共享写入，应切换到 PostgreSQL 或单独设计跨进程事务协调。

正在 `refreshing` 的 library 任务不会吸收新 item，新 item 留到下一轮。刷新完成或失败时使用 `status=refreshing` 条件更新，避免旧请求覆盖并发事件重新排队形成的 pending 任务。

典型边界结果：

| 输入 | 结果 |
| --- | --- |
| 8 或 9 个同库 item | 保持 item 刷新 |
| 10 或 11 个同库 item | 合并为一个 library 刷新 |
| 9 个 lib-a item + lib-a fallback | 只保留 `library:lib-a` |
| 9 个 lib-a item + lib-b fallback | `library:lib-b` 加 9 个 lib-a item |
| 9 个 lib-a item + unresolved item（无库归属） | 9 个 lib-a item 加 1 个 unresolved item，不触发阈值 |
| 同步目录同时关联 lib-a、lib-b 的通用 fallback | 分别创建两个 library 任务，不按多数归属选择一个 |

此前“通用 library fallback 与同库 item 同时存在”的重复刷新问题由两层逻辑解决：提交批次先展开并归一化精确 library target，协调器再对数据库内所有 pending 任务做跨批次聚合。

#### 协调器等待和调度

每个任务独立维护以下时间：

- `last_event_at`：最近一次相关变化时间。
- `refresh_after_at`：允许再次检查的时间，默认是 `last_event_at + 10 秒`。
- `deadline_at`：最长等待时间，默认是首次事件后 6 小时。
- `last_checked_at`：协调器最近一次检查时间。
- `last_refresh_at`：最近一次成功请求 Emby 的时间。

协调器判断任务 ready 时会检查：

- 任务必须为 `pending`，并且已经超过 10 秒防抖窗口。
- 等待范围始终包含任务自身 `sync_path_ids`。
- 根据可靠媒体库证据解析到媒体库后，还会扩展到该媒体库关联的其他同步目录。
- 等待范围内不能有处于等待或运行状态的 STRM 同步任务。
- 等待范围内不能有 `pending`、`downloading`，或失败但仍可自动重试的下载任务。

下载任务状态变化会先按同步目录或 `sync_file_id` 收集，每 5 秒批量更新相关刷新任务的 `last_event_at` 和 `refresh_after_at`，避免大量小文件频繁触发数据库查询。下载队列确认没有待下载和下载中任务后也会主动唤醒协调器。

用户清空等待下载任务时，系统会同时取消受影响同步目录的 `pending` 刷新任务；暂停下载队列和重试失败任务不会取消刷新任务。

如果等待超过 6 小时，任务转为 `cancelled`，不会强制刷新仍有同步或下载任务的媒体库。服务重启时，遗留的 `refreshing` 任务会恢复为 `pending`，重新进入 10 秒防抖窗口。

刷新任务创建、更新或下载事件批量落库后，会把全局最近到期 timer 调度到最早的 `pending.refresh_after_at`。并发调度时，较旧的数据库查询结果不能取消或覆盖已经存在的更早 timer。全局 timer 只负责唤醒检查，不直接决定是否刷新；每条任务仍独立判断是否 ready。

#### 执行和失败回退

任务状态流转如下：

```text
pending → refreshing → completed
                    └→ failed
pending ─────────────→ cancelled
```

item 刷新失败后，系统会重新根据本地 item、sibling Episode 和同步路径关联解析真实媒体库，并按需请求 Ancestors。只有成功解析出唯一媒体库时才回退媒体库刷新；仍为 unresolved 时保留原始失败结果，不会刷新不确定的媒体库。

- item 定向刷新接口：

```http
POST /emby/Items/{itemId}/Refresh?Recursive=<true|false>
```

- 媒体库刷新接口：

```http
POST /emby/Items/{libraryId}/Refresh
```

职责边界：

- 只调用 Emby 刷新接口。
- 不拉取 Emby item。
- 不写入 `emby_media_items`。
- 不建立 PickCode 和 `sync_files` 关联。
- 不和链路 B 串成一个长任务。

### 链路 B：同步 Emby 条目到本地

含义：QMediaSync 从 Emby 拉取媒体条目，写入本地数据库，用于建立 Emby item、PickCode 和同步文件之间的关联。

触发场景：

- 手动全量同步。
- Cron 定时增量同步。
- Webhook 新增或修改事件触发单条同步。
- 低频全量校验或重建本地索引。

职责边界：

- 只维护 QMediaSync 本地索引。
- 不调用 Emby 媒体库刷新接口。
- 不等待 STRM 生成流程。
- 不把“通知 Emby 扫库”和“同步 Emby 条目”串成一个长任务。

前端状态文案：

| 状态值 | 展示文案 |
| --- | --- |
| `idle` | 空闲 |
| `full` | 全量同步 Emby 条目 |
| `incremental` | 增量同步 Emby 条目 |
| `webhook` | Webhook 单条同步 Emby 条目 |
| `refresh_library` | 刷新 Emby 媒体库（预留状态，当前协调器不写入） |

## 同步架构

推荐运行模式是“Webhook 实时单条处理 + 定时增量兜底 + 低频全量校验”。

### 分页流式处理

全量同步和增量同步都复用分页拉取逻辑：

- 每页 `Limit` 默认取 `100`。
- 每取到一页就逐条处理 item，不把所有媒体项聚合成一个大切片。
- 每个物理 item 保存 Item.Path 与媒体源播放路径，保留来源 Id／ItemId；成功快照与当前关联替换在同一事务完成。所有来源都参与核验，不能确定物理归属的多来源响应返回错误。
- 当前逐条处理并向调用方传播依赖请求、关联查询和数据库保存错误；不在数据库事务中请求远端。
- 不再对每个 item 固定 sleep；如需限速，应限制分页请求频率或使用可配置限速。

### 物理快照与身份版本

同步显式请求 Path、MediaSources、PartCount 及季／剧上下文字段。Movie 和 Episode 的 PartCount>1 时请求 `/Videos/{id}/AdditionalParts`，保留隐藏 Video 与主项关系；已有完整字段直接保存，主项、版本和分段缺 Path／MediaSources 时才按 ID 补详情；补查后仍缺字段则保留旧快照。分段读取失败时主项和分段均不替换，当前关系保留。

物理 `/Items` 枚举不会按名称或 TMDB ID 合并版本。额外来源明确指向其他 ItemId 时查询其物理详情，再独立建立关联；无 ItemId 的多个不同来源不会盲绑到外层条目。没有云端来源的完整响应仍保存媒体项并清除旧关联。快照更新允许空字符串、0 和 false，保留 CreatedAt；增量与单条同步不清全量批次标记。

每轮远端条目读取前核验 `/System/Info/Public` 返回的实例 ID，获取持久版本。四个索引入口（全量、增量、单条及旧单条兼容函数）都使用同一事务提交，并核对配置范围和版本。数据库删除屏障登记、提交和成功游标／时间水位使用同一 SQL 锁；空扫描也核对版本。旧查询在屏障释放后仍会被拒绝，普通 UpdatedAt 不能替代代际。独立配置快照不改写旧全局缓存；设置页读取最新配置，保存只更新配置字段，不回写同步期间变化的游标／运行状态。服务地址、授权或有效选库范围改变时清除旧成功水位，重新建立范围内索引；仅通知选项变化不清游标。实际服务 ID 在原地址更换时也执行同样保护。

索引全量清旧只移除当前媒体项／关联，保留不可变物理证据。冻结证据包括已有 SyncFile 的来源、账号主体、同步根、file_id 与独立哈希／mtime，以及当时已知的旁车身份；缺字段和路径冲突保留原因，不能升级为删除授权。旧 ServerId 为空的历史行需要成功同步补全，不能自动认定属于当前服务器。主项完整响应解除旧成员关系时沿用旧文件证据，不重新绑定当前账本的同路径新文件；仍带删除屏障的消失成员只解除当前索引中的成员关系，不修改屏障、冻结证据和未完成删除工作。同步范围摘要仅用于索引提交，删除必须核对独立服务连接身份；选库变化不构成删除白名单。结构与保留约束见 [数据库 schema](../reference/database-schema.md#emby_index_statesemby_item_statesemby_item_evidences)。

### 手动全量同步

手动同步用于低频校验或重建本地索引。

流程：

1. 生成本轮 `sync_run_id`。
2. 按媒体库分页拉取 Emby item。
3. 每个 item upsert 时写入 `last_seen_sync_run` 和 `last_seen_at`。
4. 某个媒体库完整同步成功后，只清理该媒体库内 `last_seen_sync_run != 当前批次` 的旧条目。
5. 某个媒体库分页、分段补查、关联或保存失败，该库不清旧；成功库可独立完成，但本轮不会推进全量成功时间。非末页空响应、变化的总数、重复 ID 或条目数矛盾均算失败。

全量同步不再维护巨大的有效 item ID 列表，也不再对每个 item 固定 sleep。

### 定时同步

Cron 默认表达式为 `0 * * * *`，含义是每小时整点执行一次。

当 `enable_daily_first_full_sync=1` 时，每天第一次定时同步会执行全量同步；判断依据是 `last_full_sync_at` 是否属于服务器本地时区的当天。全量同步成功后会更新 `last_full_sync_at`，当天后续定时同步执行增量同步。如果当天首次全量失败，`last_full_sync_at` 不会推进，下一次定时同步仍会继续尝试全量。

当 `enable_daily_first_full_sync=0` 时，Cron 每次都执行增量同步。

保存 Emby 配置时，如果兼容客户端没有提交 `enable_daily_first_full_sync` 字段，后端会保留当前已有配置；只有显式提交 `0` 或 `1` 时才会修改该开关。

### 定时增量同步

请求使用 Emby `/Items` 的 `MinDateLastSaved` 参数。OpenAPI 中该参数类型为 `string`、格式为 `date-time`，分页参数为 `StartIndex` 和 `Limit`：

```http
GET /emby/Items
?ParentId=<libraryId>
&Recursive=true
&IncludeItemTypes=Movie,Video,Episode
&Fields=DateCreated,DateModified,ParentId,PremiereDate,Path,MediaSources,PartCount,SeriesId,SeasonId,SeriesName,SeasonName,IndexNumber,ParentIndexNumber
&MinDateLastSaved=<cursor_minus_overlap_rfc3339>
&SortBy=DateLastSaved
&SortOrder=Descending
&StartIndex=0
&Limit=100
```

响应示例：

```json
{
  "TotalRecordCount": 1,
  "Items": [
    {
      "Id": "10001",
      "Name": "示例影片",
      "Type": "Movie",
      "DateCreated": "2026-06-01T12:00:00.0000000Z",
      "DateModified": "2026-06-20T08:30:00.0000000Z",
      "HasPath": true,
      "MediaSources": [
        {
          "Id": "mediasource_10001",
          "HasPath": true,
          "PathPrefix": "http://qmediasync:12333/"
        }
      ]
    }
  ]
}
```

游标规则：

- 本轮开始时记录 `scan_started_at = now.UTC().Unix()`。
- 查询时使用 `last_saved_cursor_at - 600 秒`，初始或小于 overlap 时使用 Unix 0。
- 本轮内用 item ID 去重。
- 全部媒体库成功后更新 `last_incremental_sync_at`、`last_saved_cursor_at`、`last_processed_count` 和 `last_sync_time`。
- 任一媒体库失败时不推进 `last_saved_cursor_at`。
- 不依赖响应中的 `DateLastSaved` 推进游标，因为实际响应不返回该字段。
- 增量同步不做全局删除。

过滤边界：

- `MinDateLastSaved=2999-01-01T00:00:00Z` 这类未来时间应返回空集合。
- 空集合只表示本轮没有变更，不代表本地已有数据应被删除。
- 因响应不返回 `DateLastSaved`，不能用最后一条 item 的时间推进游标。

### Webhook 单条同步

Webhook 新增或修改事件只按 item ID 查询单条并 upsert：

```http
GET /emby/Items?Ids=<itemId>&Fields=DateCreated,DateModified,Path,MediaSources,PartCount,ParentId,SeriesId,SeasonId,SeriesName,SeasonName,IndexNumber,ParentIndexNumber
```

响应示例：

```json
{
  "TotalRecordCount": 1,
  "Items": [
    {
      "Id": "10001",
      "Name": "示例影片",
      "Type": "Movie",
      "DateCreated": "2026-06-01T12:00:00.0000000Z",
      "DateModified": "2026-06-20T08:30:00.0000000Z",
      "HasPath": true,
      "MediaSourceCount": 1
    }
  ]
}
```

如果需要用户态字段，可以改用：

```http
GET /emby/Users/{UserId}/Items/{itemId}?Fields=DateCreated,DateModified,Path,MediaSources
```

兼容注意：

- `/Items/{itemId}` 在部分 Emby 服务上可能返回 `404`，不能作为唯一单条详情路径。
- `/Items?Ids=<itemId>` 更适合无用户上下文的单条查询。
- `/Users/{UserId}/Items/{itemId}` 适合需要用户态字段时使用。

规则：

- 返回单条 Movie、Video 或 Episode 时，连同已确认的版本／分段物理快照写入本地 `emby_media_items`；其他类型不会补物理详情或入库。
- 写入前会通过 `/Items/{itemId}/Ancestors` 和 `/Library/VirtualFolders` 解析真实所属媒体库，不能把 Episode 的 `ParentId` 当作媒体库 ID。
- 当 `sync_all_libraries=0` 时，解析出的媒体库 ID 不在 `selected_libraries` 中会跳过本次单条同步。
- 解析到 PickCode 时建立 `emby_media_sync_files` 关联。
- 空结果或不支持的类型只记录日志，不触发全量同步。
- Webhook 单条同步不推进 `last_saved_cursor_at`。
- 同步忙时返回可识别的 `ErrEmbySyncBusy`；该结果与远端不存在不同，不声称条目已处理。

### Webhook 通知配置

在 QMS 的 Emby 设置页保存服务器地址和 Emby API Key，启用条目同步，完成一次全量 Emby 条目同步，再配置通知。升级后也应先成功完成一次全量同步，补齐版本、分段和文件身份；它只能补全仍可查询的条目，不能恢复已消失且从未保存的历史身份。

1. 在 Emby 通知设置中新建 Webhook，填写 QMS 设置页显示的“Emby 通知链接”。启用“通知链接鉴权”时，使用 QMS API Key，通过 `X-API-Key` 请求头或链接的 `api_key` 查询参数传入；这里使用的不是 Emby API Key。
2. 内容类型选择 **JSON（`application/json`）**，关闭分组（`GroupItems`）。不使用把 JSON 放在 `data` 字段中的 `multipart/form-data` 格式。关闭分组仍可能有 Emby 自身的通知缓冲，不保证即时投递。
3. 按需要选择发送通知的媒体库，勾选“已添加新媒体”（`library.new`）和“媒体已移除”（`library.deleted`）。界面按库名选择；直接配置 Emby 通知 API 时，`LibraryIds` 使用库 Guid，不能填 QMS 索引中的数字 item ID。QMS 的同步选库只约束条目同步，不是独立的删除白名单。
4. 如事件列表提供独立的移动、重命名、扫描失效或移除媒体库事件，不勾选它们用于联动删除。普通 `library.deleted` 也可能由重命名或扫描发现文件消失触发，不能仅靠取消独立事件排除误触发；QMS 仍会核验文件身份、原条目／STRM 及存活使用者。
5. 确认通知所属用户和实际操作用户。通知所属用户与“用户事件过滤”是不同设置，管理员名下的通知不代表已覆盖其他用户的增强事件。用可丢弃的测试媒体验证实际操作能向 QMS 投递所选事件；验证投递时保持联动删除关闭，避免涉及网盘原片。

官方通知可独立使用，无需安装插件。使用神医助手或 MediaInfoKeeper 时，推荐只开启插件的**通知增强**，并在 Emby Webhook 中额外勾选“媒体深度删除”（`deep.delete`）；无需开启插件的物理深度删除。QMS 按受支持的事件和字段结构内置识别，不要求选择插件品牌或增加插件开关，也不会修改 Emby／插件配置。其他增强事件和分组描述不用于推断待删成员。增强通知只补充证据，即使收到 `deep.delete`，也不能认定原 STRM 已删除成功。

需要联动删除网盘文件时，再在 QMS 中开启“删除时联动删除网盘文件”。官方与受支持增强通知共用此开关：接收成功只表示事件已保存，后台核验后才处理确定文件。条目或原 STRM 仍存在、来源共享、身份未知或查询失败时保留文件，未完成原因见服务日志。关闭期间收到的通知，以后启用也不追溯补删；执行和重试前仍检查开关及目标身份。

具体范围见下文[精确文件联动删除](#精确文件联动删除)：Movie 仅处理确定主项／分段并保护其他版本，Episode 仅处理 part1 和明确专属旁车，Video 仅自身，Season／Series 按真实成员保护其他季。只处理归属明确的专属 NFO、字幕和图片，通用或共享的 `poster.jpg`、`tvshow.nfo` 等保留；不递归删除目录，允许留下空目录。网盘删除后的本地 STRM 和 SyncFile 记录由后续符合条件的完整 STRM 对账收敛，不自动触发即时清理；完整对账资格见 [STRM 同步调度](sync-orchestration.md)。

### Webhook 持久接收与后台处理

`POST /emby/webhook` 保留现有鉴权、`X-API-Key` 与旧 `api_key` 查询参数兼容。官方教程使用 `application/json`。请求先校验体积、JSON 类型与关键字段，不记录原始正文或带凭据的播放 URL；关键字段冲突不生成删除工作。保存失败返回非成功响应，成功仍返回兼容的响应形状，仅表示事件已保存。

内置处理 `library.new`、兼容的 `library.modified`、`library.deleted` 及受支持的 `deep.delete` 结构，不需要选择插件品牌。服务器版本、Title 或是否带 User 都不能可靠识别插件。其他通知保留原有功能；分组通知的描述文本不被猜测为子条目列表。

接收事务保存必要字段、服务器连接身份、接收时的删除授权及冻结输入，并原子登记删除屏障。每次通知独立保存，不按 ItemId 或事件 Date 永久去重。官方与增强通知通过同一物理文件代际的结果避免重复删除；后到通知仍可处理已由历史证据确认的范围。同步选库只约束索引同步，不作为删除白名单。

应用启动后运行单一后台 worker，工作不绑定 Webhook HTTP 请求的取消。应用最终关闭后禁止备份恢复流程再次启动 worker；备份期间的暂停与应用最终关闭分别处理。新增／修改事件先尝试读取详情并保存早期不可变证据，即使全量同步正忙也不绕过锁写当前索引；后续仍通过普通同步入口更新索引。读取前同时记录已有证据水位，读取期间同条目的新快照会使旧观察失效；不同条目的更新不会互相废弃已保存证据。删除接收只在该条目的原状态未变化时采用这份证据。若官方缓冲通知到达时条目早已删除、此前也没有可靠身份，仅凭当前 Path 唯一命中不能补造旧文件身份，保留未完成原因。

同步忙时保持待办，不消耗失败次数。网络处理有界重试，失败上限为 4 次；每个删除目标保存自己的结果，已成功目标不重复发送删除。保存结果暂时失败时先重试持久化，不重发刚完成的网盘请求；应用取消时保留领取状态，启动恢复后先核验原身份。未知、冲突或超出重试范围的工作保留原因，不自动换成当前路径的新文件。工作状态保存在数据库并通过现有日志报告，不计入 EmbySyncMode，也没有新增任务页面。

接收时未获删除授权的事件，以后开启开关也不会自动补删；执行和重试仍检查现有 `SyncEnabled`、`EnableDeleteNetdisk`、服务连接及账号／文件身份。此类事件只允许在核验服务连接、原条目 ID 已消失后，按冻结快照与代际清理本地索引，不要求存在可删除的网盘来源。已确认新 ID 仍使用同一来源的重命名场景，也只清理消失的旧关联，保留共享网盘文件与新关联；其他未知或执行失败的范围继续保留。备份／恢复前停止接收并等待已开始的收件事务和 worker 退出，期间 Webhook 返回暂时不可用；恢复过程见 [数据库运维](../operations/database.md)。

`deep.delete` 只提供额外证据，不代表原删除成功。JSON `Item.Path` 保持完整文件路径，包括合法文件名中的换行；Description 中的 Item Path 可能只是目录，不授权递归删除。单一、完整的 Mount Paths 候选只能与已有历史来源核对，多行文本无法可靠区分多文件和文件名换行，不按换行或逗号拆成删除目标。没有独立历史映射的候选保留原因，明确身份冲突停止执行。

Webhook 删除事件不触发全量同步，也不调用刷新媒体库接口。删除前重新核验真实服务器、原条目 ID、原 STRM 路径及可访问的同步根；再枚举不受同步选库限制的完整物理清单、来源和 AdditionalParts，排除重命名、新 ID 和共享源。条目／STRM 仍存在、清单不完整或查询失败均不授权网盘删除；失败的深度删除通知不会清掉仍存活的正确索引。确认原条目存活时仅释放匹配的临时屏障，旧查询依然无权提交。若先前删除因查询故障结束，后续全量、增量、单条同步和新的 Webhook 都能重新核验仍存活的主项及分段，按完整确认组解除屏障，再丢弃旧响应重新读取。普通同步先按每页 100 条核验已有屏障；隐藏分段可用历史主项关系作为查询入口，但仅实际响应和 AdditionalParts 确认的成员才能恢复，不在旧分页中途换版本；增量本轮需要恢复时从 Unix 0 查询现存条目，仍不进行全量清旧。此过程只恢复索引，不重启旧删除或提升旧事件授权。

### 精确文件联动删除

`models/emby_delete.go` 提供接收事务冻结、构建计划、逐文件执行和条件收尾四个步骤。接收事务读取 S1 的不可变条目证据，保留当时的关联、来源／账号主体、同步根和物理文件代际；当前索引已被完整扫描清理时，仍可使用保留下来的 state→evidence。同目录视频账本在接收时冻结；旁车必须已有正常同步或早期观察保存的独立历史身份。接收时的当前 SyncFile 不能替代这份历史证明，旧证据缺少旁车身份时保留元数据；重试不会选择同路径后来出现的文件。

| 删除类型 | 文件范围 |
| --- | --- |
| Movie | 主项及通过 AdditionalParts 确认属于它的分段；不扩展到保留的独立版本。 |
| Episode | 主项 part1 及其明确专属旁车；保留其他 part，允许后续重新入库。 |
| Video | 该物理条目本身。 |
| Season／Series | 按保存的真实季／剧成员 ID 收集文件和确认分段，覆盖所有关联目录和账号；不根据目录名称推断范围。 |

所有网盘请求只删除明确文件，不递归删除目录。同步根、其祖先、其他条目共用的源文件受到保护。旁车使用全部完整视频 stem 和明确 NFO／字幕／图片后缀判定归属，同时考虑收到事件时的视频证据和执行前完整新鲜目录；互为前缀、点分隔 stem、同 stem 不同容器或未知使用者产生歧义时保留。通用 `poster.jpg`、`folder.jpg`、`tvshow.nfo`、季封面等共享文件直接保留。所属视频未确认消失时不先删旁车；空目录和不明确元数据允许残留。

执行每个目标前只取得一次现有 `syncscope` 全局保护，不在 SQL 事务中等待或执行 HTTP，也不嵌套调用旧删除入口。执行层要求调用方提供独立服务器、原条目／STRM 和存活使用者核验；缺少核验不调用网盘。文件身份与授权在远端只读核验后、单次破坏性请求前再次检查。115 使用准确的文件 ID 和真实父目录 ID，百度使用完整文件路径，OpenList 使用目录和 basename；新增执行路径不会在客户端内部盲目重发删除。

网盘同步根遵循[同步目录路径约定](../reference/sync-path-api.md#sync_path)，保存时移除前导 `/`；115 的文件账本通常也不带 `/`，百度和 OpenList 的文件路径则可能带 `/`。保存删除身份和执行删除时，三个来源均将合法的根相对目录与对应绝对表示按同一网盘目录比较，范围和同步根保护使用相同规则；本地 STRM 路径仍须为绝对路径。路径含向上跳转、非法分隔符或无法确认归属时拒绝删除。路径表示相等不能替代账号、文件 ID、父目录 ID、大小、哈希及时间等身份核验，也不会改写已保存的历史证据、清除历史失败原因或重新执行终态 `unresolved` 事件。

每个目标分别返回 `deleted`、`already_absent`、`unresolved` 或 `failed`。错误、false、取消、未知结果、不支持的来源和身份冲突均保留未完成证据；不能将查询失败视作不存在。收尾仅删除全部目标已完成、且仍为原快照代际的 owner 关联，不按 `sync_file_id` 清除其他使用者。某目录旁车清单核验失败时保留该目录 owner，其他目录已完成 owner 可以独立收尾。不可变证据保留；`SyncFile` 和本地派生文件仍由后续符合条件的完整 STRM 对账收敛。

官方／增强事件由持久 worker 接入上述计划 API。标量 `DeleteNetdisk*By*Id` 旧入口无法提供独立核验，继续返回待核验错误并保留本地索引。`syncscope` 只协调本进程内遵守该协议的操作，不能消除外部系统在最后核验与删除之间的写入窗口。

## 同步运行状态

状态接口返回的核心字段：

| 字段 | 含义 |
| --- | --- |
| `is_running` | 是否有 Emby 条目同步任务运行 |
| `sync_mode` | 当前或最近一次模式 |
| `started_at` | 当前任务开始时间 |
| `last_sync_time` | 最近一次成功同步时间 |
| `last_success_sync_mode` | 最近一次成功同步模式 |
| `last_full_sync_at` | 最近一次成功全量同步时间 |
| `last_incremental_sync_at` | 最近一次成功增量同步时间 |
| `last_saved_cursor_at` | 增量同步游标 |
| `last_processed_count` | 最近一次处理数量 |
| `last_error` | 最近一次失败原因 |
| `total_items` | 本地已同步 Emby item 数 |

如果已有任务运行，定时任务直接跳过，不排队堆积。手动全量同步遇到运行中的任务时，前端应提示稍后再试。

Web 设置页的配置、媒体库、提取和同步请求由 `api/emby.ts` 封装并判断业务结果，轮询仍保留页面可见性及过期请求保护。请求的来源、CSRF 和传输错误使用 [公共错误分类](../engineering/frontend-development.md#api-响应与请求错误)，配置保存失败保留表单输入；状态快照中的动态 `last_error` 不直接展示底层错误内容，页面提示查看服务日志。

“提取媒体信息”在后台扫描全部媒体库并把缺少媒体信息的条目加入下载队列。同一时间只运行一轮，运行标记在后台扫描结束后才释放；期间再次触发返回“Emby 媒体信息解析任务正在运行，请稍后再试”。Emby Webhook 触发单条媒体信息提取时，仅忽略 `ErrActiveDownloadTaskExists` 表示的重复任务，其他入队错误必须记录日志。

程序启动并完成数据库迁移后，会清理上次进程异常退出遗留的 `is_running=true` 状态，避免全量、增量和 Webhook 单条同步长期被旧运行标记阻塞。该清理只复位 `is_running`、`sync_mode` 和 `started_at`，并写入 `last_error` 说明原因；不会推进或清空 `last_sync_time`、`last_full_sync_at`、`last_incremental_sync_at` 和 `last_saved_cursor_at`。

## 升级和迁移说明

新增字段通过 GORM AutoMigrate 补齐，旧数据默认值保持兼容。

新增或扩展的字段：

- `emby_config.last_full_sync_at`
- `emby_config.last_incremental_sync_at`
- `emby_config.last_saved_cursor_at`
- `emby_config.last_processed_count`
- `emby_config.last_success_sync_mode`
- `emby_config.last_error`
- `emby_config.is_running`
- `emby_config.sync_mode`
- `emby_config.started_at`
- `emby_config.enable_daily_first_full_sync`
- `emby_media_items.last_seen_sync_run`
- `emby_media_items.last_seen_at`

版本 65→66 在一次事务中新增物理快照、成员、身份状态、旁车历史、持久收件和逐目标结果，保留旧媒体及关联记录；完整迁移与失败回滚约定见 [数据库 schema 与迁移](../reference/database-schema.md#迁移与初始化)。旧索引不从当前同名文件推断过去的删除身份。升级后建议先手动执行一次全量同步，用于建立本地索引和全量批次标记。默认启用每日首次定时全量同步，之后 Cron 会在每天首次成功全量后按增量同步兜底变更。

## 验证命令

后端：

```bash
(cd backend && go test . ./internal/emby ./internal/embyclient-rest-go ./internal/models ./internal/controllers ./internal/requests ./internal/backup ./internal/v115open ./internal/openlist ./internal/baidupan)
(cd backend && go test -race ./internal/emby ./internal/models ./internal/controllers ./internal/backup -run 'Emby|Webhook')
(cd backend && go vet . ./internal/emby ./internal/embyclient-rest-go ./internal/models ./internal/controllers ./internal/requests ./internal/backup)
```

前端：

```bash
(cd frontend && pnpm exec vitest run test/components/AppEmbySettings.sync-status.test.ts)
(cd frontend && pnpm exec vitest run test/utils/timeUtils.test.ts)
(cd frontend && pnpm run type-check)
(cd frontend && pnpm run build)
```

## 不变量

- 通知 Emby 刷新与同步 Emby 本地索引是两条独立链路，入口和状态不得混用。
- 刷新统一写入 `emby_library_refresh_tasks` 并由协调器执行，生产链路不得直接调用旧的立即刷新函数。
- 非 Webhook 任务仅在 STRM 实际变化后请求刷新；Webhook 默认不刷新，只有显式 `refresh_emby=true` 且满足变化条件时才提交。
- 批量和目录扫描父任务只有在全部子任务成功并存在 STRM 或元数据变化时提交一次刷新；任一子任务失败时不得提交。
- item 目标优先，可靠证据不足时才回退到关联媒体库；多库且无可靠证据时保持 unresolved，不任选媒体库。

## 验证方式

- 运行本节列出的后端、前端命令；改动刷新协调器时至少覆盖 item 目标、媒体库回退、去抖、下载等待和父子任务失败场景。
- 修改迁移字段时运行 `(cd backend && go test ./internal/models/)`，并核对 [数据库 schema 与迁移](../reference/database-schema.md) 的版本和字段说明。
