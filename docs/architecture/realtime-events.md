# 实时事件（SSE）

> 职责：定义全局事件、日志流和同步任务详情的 SSE 协议、快照和恢复边界。
>
> 权威范围：本文档维护 SSE 消息语义；代理配置见 [反向代理](../operations/reverse-proxy.md)，认证与 API Key 见 [认证会话](authentication-sessions.md)。
>
> 修改时机：修改 SSE 路由、事件 payload、快照、回放、心跳、断线恢复或浏览器降级行为时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/event_stream.go`、`backend/internal/controllers/log_stream.go`、`backend/internal/realtime/`、`frontend/src/`。

QMediaSync 使用 Server-Sent Events（SSE）提供只读实时更新。启动、暂停、重试、删除等操作仍通过既有受鉴权与 CSRF 保护的 HTTP API 执行；SSE 只负责服务端到浏览器的状态推送。

## 路由和同源要求

所有实时路由位于 `/api` JWT 鉴权组，浏览器使用当前站点的 Cookie 会话或现有 API Key 查询参数鉴权。

| 路由 | 用途 |
| --- | --- |
| `GET /api/events/stream` | 全局队列、刮削和同步记录事件 |
| `GET /api/logs/stream?path=...` | 指定日志文件的新增日志与重新加载提示 |
| `GET /api/sync/tasks/:id/stream` | 同步任务详情的 snapshot、状态 patch、日志和完成通知 |

生产环境由后端在同一 origin 托管前端。Vite 开发服务器把相对 `/api` 代理到 `http://localhost:12333`，因此 axios 与 `EventSource` 都使用 `/api/...`，不需要跨 origin Cookie、`withCredentials` 或额外 URL 构造工具。本期不提供旧协议兼容路由或跨 origin SSE 专项支持。

每条流响应为 `text/event-stream`，设置 `Cache-Control: no-cache` 与 `X-Accel-Buffering: no`。建立订阅后会立即写入注释帧，之后每 15 秒发送注释心跳；应用停机时会先取消实时流，再关闭 HTTP server。

## 全局业务事件

全局事件 payload 保留 `event_type`、`timestamp`、`data` 三个字段，事件类型包括队列状态、队列列表、刮削任务和同步任务事件。它的语义是“至多一次通知 + HTTP snapshot 最终收敛”：页面收到结构性事件或原生 EventSource 非首次 `open` 后，复用当前页的 HTTP 加载函数重新获取列表快照。

刮削目录的媒体项完成事件可能密集到达，页面须合并其 HTTP 状态查询，并限制连续查询失败的提示次数；状态查询失败不改变 SSE 连接状态。请求合并、旧结果失效和提示规则见 [前端开发约定](../engineering/frontend-development.md#api-响应与请求错误)。

上传队列的 `upload_queue_changed` 中，`progress` 和 `source_cleanup_changed` 是当前页已有任务的局部 patch 例外。目录监控源文件清理更新会同时携带 `source_cleanup_status`、可为空的 `source_cleanup_error` 与正值 `source_deleted_at`；前端将这些字段合并到现有行。缺少目标行、其他变更原因或原生重连仍通过 HTTP snapshot 收敛。

同步记录列表将有效的普通状态事件直接合并到现有行；HTTP 列表请求在途时，同时保留期间收到的事件，在响应返回后按顺序合并到快照，不因每次进度更新额外补读。创建、删除等结构变化仍使在途快照失效，并在请求结束后合并补读当前页；连续事件不产生并发查询。已经因任务序号过期而丢弃的事件不触发补读。旧快照不得把已收到的生成终态或后台完成、失败状态退回处理中。

全局流的前端连接状态分为 `idle`、`connecting`、`connected`、`reconnecting` 和 `disconnected`。创建 `EventSource` 后先进入 `connecting`，首次 `open` 只更新为 `connected`，不显示断线提示；原生 `error` 才进入 `reconnecting` 并显示“实时更新暂时断开，正在重新连接…”。后台重连会等待页面重新可见后执行一次收敛，避免后台页无意义请求。同步记录和同步目录页面会在这次 HTTP snapshot 前清空本地 `sequence` 水位，避免后端进程重启后用旧水位丢弃重新计数的事件。若 `error` 时 `readyState` 已为 `CLOSED`（浏览器放弃自动重连），前端释放该 source 并进入 `disconnected`，提示“实时更新已断开，请刷新页面恢复”，监听器保留，下一次订阅会重新建立 source。最后一个全局监听器注销时关闭 source；登出或认证状态清理会关闭所有已登记的实时 source。

不支持 `EventSource` 的浏览器不创建 source、不引入全局轮询，应用壳层会提示“当前浏览器不支持自动刷新，请手动刷新页面查看最新状态”。连接暂时断开时显示“实时更新暂时断开，正在重新连接…”，由浏览器原生重连处理。

## 通用日志

日志查看器先成功请求 `GET /api/logs/old`，再创建 `/api/logs/stream`。日志路径切换或组件卸载会取消旧快照请求，并忽略已过期的响应，避免旧历史日志与新路径的实时增量混合。服务端 stream 自身会校验路径、普通文件类型和 EOF cursor；建立 tail 时只读取文件末尾位置，不扫描整个历史日志。

初始快照和向前翻页均通过日志领域 API 读取，保留 Cookie、位置、条数和取消信号。失败复用公共 HTTP 分类，日志区域只显示安全说明；路径切换后的旧失败也不能写入新路径的日志。原生 `EventSource.onerror` 只表示连接暂时断开，不据此判断认证失效或服务不可达，也不新增会话探测。

`log_append` 传递新增日志条目，`resync_required` 表示截断、轮转或 tailer 无法继续时应重新请求 HTTP 日志快照。日志查看器的连接状态为 `idle`、`connecting`、`connected`、`reconnecting` 或 `unsupported`：初始历史快照和 `EventSource` 建立期间显示“正在连接”，仅在原生连接错误后显示重连提示；初始快照请求失败时会记录错误并回退到 `idle`；不支持 `EventSource` 时进入 `unsupported`，仅显示不支持提示。普通连接错误不关闭 source，下一次 `open` 会重新加载 HTTP 历史快照；`error` 时 `readyState` 已为 `CLOSED` 则关闭 source 回到 `idle`（显示已断开），由用户手动重新连接。日志流不承诺严格投递、持久回放或客户端 cursor 补偿。

不支持 SSE 时，日志不轮询，界面提示“当前浏览器不支持实时日志，请手动刷新查看最新内容”。

## 同步任务详情

同步任务详情 stream 先订阅任务事件，再读取数据库与最近 1000 行日志快照，避免订阅前的状态变化丢失。消息类型为 `snapshot`、`task_patch`、`log_append`、`complete`、`resync_required` 和 `error`；`complete` 不回放。任务不存在时返回带 `deleted=true` 的 `complete`；数据库读取、日志快照读取或日志订阅失败在 SSE 响应开始前返回普通 HTTP 500，不会伪装为已删除任务。

运行中任务会在单个进程 epoch 内缓存最近 64 条 `task_patch`。浏览器携带格式为 `<stream_epoch>:<sequence>` 的 `Last-Event-ID` 时，仅在 epoch 一致且缓存连续时回放后续 patch；空值、非法值、epoch 不同、缓存缺口或服务重启时均返回完整 snapshot。snapshot 带注册时的 sequence 水位线，后续只发送更大的 patch，避免旧事件回退快照。日志不参与回放，未命中时由 snapshot 中的最近日志恢复。

任务详情前端同样区分 `idle`、`connecting`、`connected` 和 `reconnecting`：首次订阅期间不显示断线提示，原生连接错误后才显示重连提示。生成已终结（已完成、失败、部分完成、扫描不完整或已取消）且后台账本不再处于 `pending/running`，或任务已删除时，服务端才清理该任务的回放缓存和 sequence 状态。历史后台字段缺失仍沿用原有终态判断。已订阅客户端继续接收最多 2 秒的最终日志增量，再收到唯一 `complete`；客户端关闭 source，避免终态任务自动重连。后续新建连接通过终态 snapshot（或缺失记录的 `deleted complete`）收敛，不会在终态 snapshot 后重复发送 `complete`。原生 `error` 时 `readyState` 已为 `CLOSED` 表示浏览器不再重连，详情页关闭 source 回到 `idle` 并改用下述 HTTP 降级读取。浏览器不支持 SSE 时，详情页对生成未终结或后台账本仍为 `pending/running` 的任务每 5 秒请求 `GET /api/sync/task?sync_id=...`，两者均终结后停止；日志仍由用户手动刷新。

详情 snapshot、patch、全局同步记录事件和 HTTP 降级使用同一 `scan_result` 结构（字段见 [数据库 schema](../reference/database-schema.md#syncs)）。生成终态 `2–6` 清除目录的运行展示；详情流及轮询另外等待后台终态。目录页在推进事件时间水位前忽略后台专属更新（`running/completed/failed/interrupted`），防止旧任务后台事件把同目录新任务显示为空闲；删除历史任务后通过现有状态查询收敛，不直接清空目录运行态。历史缺少明细时显示“未记录”。失败范围通过详情的普通文本展示，不执行其中的路径或错误内容。

后台结果以同一持久化快照携带 `ledger_status`、`ledger_finished_at` 和 `ledger_error`；生成计数、结果和 `finish_at` 保持固定。生成终态且后台 pending/running 的 snapshot、patch 和重连回放仍继续订阅；后台结果成功保存后再发布终态，失败保存不能发送已经完成的假象。来源队列的生成完成事件、通知渠道的生成通知与详情 `complete` 是三个独立边界。

该 HTTP 降级读取必须校验业务成功和任务 ID，同一轮查询不得重叠；失败保留已有任务快照，并显示公共错误说明，后续成功清除错误。HTTP `401`、`403`、`404` 或明确的认证、来源、CSRF 拒绝会停止该轮查询，须由后续显式连接重新开始；这也适用于第一次降级读取，不能在失败后再次创建定时器。显式停止、任务切换或卸载会使在途结果失效，旧请求不得回写状态或重新创建轮询。SSE 支持与连接状态不因 HTTP 错误分类而改变。

## 日志 tailer 与事件字段

通用日志与同步任务日志共享 `logstream.GlobalManager`，按绝对路径复用 tailer。tailer 发现文件缺失、截断、半行过长或订阅者缓冲满时会通知 `resync_required` 或结束慢订阅；不会阻塞同步、上传、下载或刮削业务。

同步任务结构化 payload 由 `backend/internal/realtime/sync_task_events.go` 定义。`sync_id` 是真实同步记录 ID；`sequence` 仍按任务递增，以兼容全局事件消费者。生成与后台均终结时会清理该任务的 sequence 状态，因此后续 `deleted=true` 事件可能从新的 sequence 重新开始；全局列表不得因旧 sequence 忽略删除事件，并在处理后清除该任务的本地去重记录。`sync_path_id`、`status`、`sub_status`、统计字段、阶段时间、`log_path`、`event_time`、路径和失败原因保持既有含义。

运行中同步任务的进度、阶段切换和终态按同一发布顺序提交。普通进度保留约 1 秒节流，已有发布在途时跳过本次发布、继续累计计数；强制发布及阶段、终态迁移等待旧发布结束，并响应取消。计数（包括零值）成功落库后才广播对应快照，普通写入失败允许后续重试；最终计数或完成状态写入失败不能发布成功终态。生成终态之后不再发布旧的运行中计数，后续后台事件从数据库取得冻结的生成快照；SSE 的 sequence 不能代替这个并发边界。

同一进程内，同一同步记录的状态保存与事件发布、历史删除与删除事件发布共用短临界区。删除等待已经开始的状态发布完成；删除先完成时，后续状态写入不能再发布该记录的旧事件，也不能重建回放缓存。新建记录的事件同样在这一边界内确认当前记录后发布。不同记录独立处理，等待状态更新锁可响应取消；锁不覆盖扫描、文件入账或通知发送。定期清理与用户删除遵循同一顺序。

代理层的缓冲、超时和可信转发 header 要求见 [反向代理](../operations/reverse-proxy.md)。

## 不变量

- SSE 只从服务端向浏览器推送状态，写操作仍通过既有鉴权和 CSRF 保护的 HTTP API。
- 全局事件最多一次通知，页面必须通过 HTTP snapshot 最终收敛；不得把它当作可靠消息队列。
- 同步任务 patch 仅在同一 `stream_epoch` 且缓存连续时回放；其他情况必须回退到完整 snapshot，日志不参与回放。
- `complete` 不回放，终态客户端收到唯一完成事件后关闭 source；终态任务的新连接通过 snapshot 或 `deleted complete` 收敛。
- SSE 不支持跨 origin Cookie 通道，也不得因为浏览器不支持 EventSource 而引入全局轮询。

## 验证方式

- 运行 `(cd backend && go test ./internal/controllers/ -run 'Test.*(Event|Stream|Log)')` 和 `(cd backend && go test ./internal/realtime/)`。
- 前端改动按 [验证说明](../engineering/verification.md) 选择相应验证。
- 修改代理层时在测试环境订阅三个 SSE 路由，确认流式响应未被缓冲、断线恢复按 snapshot 收敛。
