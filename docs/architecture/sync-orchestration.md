# STRM 同步调度与任务记录

> 职责：定义保存型同步目录的触发、Cron、按来源分队列、`sync` 任务记录、取消和下游协作边界。
>
> 权威范围：本文档维护完整 STRM 同步的运行链路；STRM 文件生成、目录监控上传和上传后处理见 [上传和 STRM 后处理](upload-and-strm-processing.md)，实时消息协议见 [实时事件](realtime-events.md)，任务机器值见 [任务来源](../reference/task-sources.md)。
>
> 修改时机：修改同步入口、Cron 注册、队列路由、去重、取消、`sync` 状态或同步完成后的下游触发时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/sync.go`、`backend/internal/syncconfig/`、`backend/internal/synccron/`、`backend/internal/syncstrm/`、`backend/internal/models/sync.go`、`backend/internal/models/syncpath.go`。

## 范围和对象

`sync_paths` 是可保存、可调度的同步目录：它绑定来源类型、账号、远端路径、STRM 本地目录和可继承的 STRM 配置。创建和更新通过 `POST /api/sync/paths`、`PUT /api/sync/paths/:id` 的聚合保存完成；保存成功后重载同步目录自定义 Cron，并在需要时重载目录监控服务。精确请求、幂等和错误契约见 [同步目录聚合 API](../reference/sync-path-api.md)。

`POST /api/sync/manual` 是临时同步入口。它使用请求中的账号、文件或目录和目标路径，不对应保存的 `sync_paths`，运行时使用临时 ID 创建记录；不会提交 Emby 刷新，也不会触发关联刮削。临时记录可以按临时记录规则删除，但不应被当作可再次调度的同步目录。

完整同步与上传后单文件 STRM 处理必须分开：前者创建 `sync` 记录、进入同步队列并处理目录差异；后者由 `strm_generation_tasks` worker 执行，不创建完整同步记录。

## 触发与 Cron

| 触发入口 | 适用范围 | 结果 |
| --- | --- | --- |
| `POST /api/sync/start` | `enable_cron=true` 且没有自定义 Cron 的保存型同步目录 | 将每个目录加入对应来源队列 |
| `POST /api/sync/path/start` | 指定保存型同步目录 | 将该目录加入对应来源队列 |
| `POST /api/sync/path/full-start` | 指定保存型同步目录 | 先写入 `is_full_sync=true`，再加入对应来源队列 |
| 全局 STRM Cron | 同 `POST /api/sync/start` | 调用 `StartSyncCron()`，不直接执行同步 |
| 同步目录自定义 Cron | `enable_cron=true` 且 `sync_paths.cron` 非空的保存型目录 | 由 `SyncCron` 独立注册并入队 |
| `POST /api/sync/manual` | 临时文件或目录同步 | 以临时任务入队 |

全局和自定义 Cron 互斥：全局 Cron 会跳过配置了自定义表达式的目录；自定义 Cron 只加载已开启定时同步且表达式非空的目录。修改全局 Cron、同步目录自定义 Cron 或 `enable_cron` 后，必须重载相应 scheduler，不能依赖旧的已注册任务自然失效。

## 按来源队列

同步调度器为每个 `source_type` 延迟创建一个内存队列；同一来源队列串行执行，来源不同的队列可以并发执行。每条队列任务使用 `sync_path_id-task_type` 作为保存型任务 key，`task_type` 使用稳定机器值 `strm_sync`；同一个目录处于等待或运行时，重复入队会被拒绝。临时任务以 `source_path_id-task_type` 去重。

单队列通道和等待集合的容量均为 `50`。队列暂停时新任务仍留在等待集合，恢复后重新投递；停止服务会取消队列 context、关闭通道并清空内存等待状态。因此队列不是持久化消息系统，进程重启后不会恢复尚未执行的队列项。

`POST /api/sync/path/stop` 的语义取决于任务状态：等待项直接从等待集合移除；运行中的 STRM 同步调用 `SyncStrm.Stop()` 取消 context。接口以成功响应表示已发起取消，最终结果以 `sync` 记录状态、日志和 SSE 事件为准。

```text
HTTP / Cron / Telegram
          │
          ▼
  按 source_type 的内存同步队列
          │  去重、容量检查、取消
          ▼
 SyncStrm 创建 sync 记录并执行
          │
          ├── 写入 STRM、下载和上传任务
          ├── 发布同步任务事件和日志
          └── 完成后按变化触发 Emby 刷新与关联刮削
```

## `sync` 记录与执行状态

保存型和临时完整同步开始时都创建 `sync` 记录。状态使用稳定整数：`0` 待处理、`1` 进行中、`2` 已完成、`3` 失败；子状态为无、处理网盘文件和处理本地文件列表。进度字段、时间字段、日志路径和失败原因通过数据库记录与同步任务 SSE 共同暴露，SSE 的 snapshot 与断线恢复规则见 [实时事件](realtime-events.md)。

执行器启动时会暂停全局下载和上传队列，完成或返回后再启动它们；同步过程中产生的元数据下载和上传工作由各自的持久化队列处理。不要把内存同步队列的等待状态与下载、上传队列的持久化状态混为同一种任务状态。

元数据下载和修改时间检查以远端官方修改时间为准。115 列表使用 `upt`、详情使用 `utime`，仅在该字段缺失或为零时回退上传时间 `uppt` / `ptime`；这些远端时间字段及两端比较、对齐精度均为 Unix 秒，并会写入 `SyncFile` 和下载任务。启用“下载元数据”和“检查元数据修改时间”时，远端时间严格较新才会删除本地并创建下载任务，本地严格较新且允许上传才会替换远端并创建上传任务。mtime 相等时不计算 SHA1；mtime 不同但大小相同且远端提供 SHA1 时，系统流式计算本地 SHA1，并在哈希前后只复核本地文件的大小与 `ModTime().UnixNano()`。该纳秒值仅用于尽可能发现哈希期间的本地变化，实际精度受本地文件系统限制，不表示 115 提供纳秒级时间。SHA1 忽略大小写比较，相同则不传输、仅将本地 mtime 对齐为远端官方修改时间；文件在哈希期间变化时跳过本轮，SHA1 缺失或计算失败时才回退既有时间方向。下载实际写入成功后也会使用任务携带的远端官方修改时间设置本地 mtime。

创建下载任务前会分页读取当前下载范围中处于 `pending` 或 `downloading` 的任务；范围由 `source`、`source_type`、`account_id` 和非零 `sync_path_id` 组成。每一页都必须先参与远端文件 ID 去重，再判断是否已读取最后一页，避免元数据任务重复入队。临时同步没有 `sync_path_id` 时不能使用该预过滤，最终创建任务会以相同来源、账号和本地目标路径作为范围兜底，避免不同本地目标互相阻塞。该预过滤只用于减少重复查询；创建时由下载队列表的活跃任务部分唯一索引作为并发最终约束，失败任务重试也会避让同一范围内的活跃替代任务。

正常完成时，保存型同步会更新 `last_sync_at`；全量同步成功结束后会清除 `is_full_sync`。全量任务在运行中失败或被取消时会保留该标记，使下一次同目录同步仍按全量执行。进程启动时，遗留在待处理或进行中的 `sync` 记录会标记为失败，并把受影响保存型目录的 `is_full_sync` 清回 `false`，避免过期运行态永久阻塞后续操作。

仅已完成或失败的普通记录允许通过用户删除接口删除；定期清理由全局 Cron 在每天 `00:00` 执行，保留期为 7 天，并同时删除关联日志。清理任务可以删除过期的未终态历史记录，因此它不是用户主动删除的替代路径。

## 完成后的协作边界

- 完整保存型同步只有在新增 STRM 或新增元数据下载任务时才请求 Emby 刷新；刷新由协调器延迟执行，具体规则见 [Emby 媒体库同步](emby-library-sync.md)。
- 只有新增 STRM 时才发布关联刮削路径的异步触发事件；没有新增 STRM 时不触发关联刮削。
- 临时同步不参与以上两类下游触发。
- 目录监控、外部 STRM Webhook 和上传完成后的文件级处理使用 `strm_generation_tasks`，不能伪造完整 `sync` 记录或插入同步目录队列。

## 不变量

- Cron 和 HTTP 入口只能入队，不能绕过按来源队列直接执行完整同步。
- 同一来源、同一保存型同步目录、同一任务类型在等待或运行期间只能有一个内存任务；跨来源队列不共享该去重范围。
- `sync` 记录是完整同步的审计与实时状态来源；上传后或 Webhook 的单文件 STRM 后处理不得创建伪造的完整同步记录。
- `is_full_sync` 只能由全量入口置为 `true`；成功完成后清除，运行中失败或取消时保留给下一次重试，启动恢复处理遗留运行态时清除。
- 停止同步是取消信号，不是伪造“已完成”状态；最终状态必须由执行器写入记录。
- 115 元数据的比较、下载任务和本地时间回写都使用同一官方修改时间；上传时间只可作为缺失修改时间的兼容回退。
- 内容相同的元数据不因秒级 mtime 差异传输；哈希期间本地文件发生变化时不得上传、下载或覆盖该文件。

## 验证方式

- 运行 `(cd backend && go test ./internal/synccron/)`，覆盖按来源队列、去重、暂停、恢复、取消和状态输出。
- 运行 `(cd backend && go test ./internal/models/ -run 'Test.*(Sync|Delete)')`、`(cd backend && go test ./internal/syncstrm/)`，覆盖记录状态、启动恢复、执行和下游触发边界。
- 修改 Cron 注册或同步目录聚合保存时，运行 `(cd backend && go test ./internal/syncconfig/)` 与相关控制器测试；在测试环境检查修改 `enable_cron` 或 `cron` 后旧计划已停止、新计划仅注册一次。
