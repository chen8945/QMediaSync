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

上传队列的 `upload_queue_changed` 中，`progress` 和 `source_cleanup_changed` 是当前页已有任务的局部 patch 例外。目录监控源文件清理更新会同时携带 `source_cleanup_status`、可为空的 `source_cleanup_error` 与正值 `source_deleted_at`；前端将这些字段合并到现有行。缺少目标行、其他变更原因或原生重连仍通过 HTTP snapshot 收敛。

全局流的前端连接状态分为 `idle`、`connecting`、`connected` 和 `reconnecting`。创建 `EventSource` 后先进入 `connecting`，首次 `open` 只更新为 `connected`，不显示断线提示；原生 `error` 才进入 `reconnecting` 并显示“实时更新暂时断开，正在重新连接…”。后台重连会等待页面重新可见后执行一次收敛，避免后台页无意义请求。同步记录和同步目录页面会在这次 HTTP snapshot 前清空本地 `sequence` 水位，避免后端进程重启后用旧水位丢弃重新计数的事件。最后一个全局监听器注销时关闭 source；登出或认证状态清理会关闭所有已登记的实时 source。

不支持 `EventSource` 的浏览器不创建 source、不引入全局轮询，应用壳层会提示“当前浏览器不支持自动刷新，请手动刷新页面查看最新状态”。连接暂时断开时显示“实时更新暂时断开，正在重新连接…”，由浏览器原生重连处理。

## 通用日志

日志查看器先成功请求 `GET /api/logs/old`，再创建 `/api/logs/stream`。日志路径切换或组件卸载会取消旧快照请求，并忽略已过期的响应，避免旧历史日志与新路径的实时增量混合。服务端 stream 自身会校验路径、普通文件类型和 EOF cursor；建立 tail 时只读取文件末尾位置，不扫描整个历史日志。

`log_append` 传递新增日志条目，`resync_required` 表示截断、轮转或 tailer 无法继续时应重新请求 HTTP 日志快照。日志查看器的连接状态为 `idle`、`connecting`、`connected`、`reconnecting` 或 `unsupported`：初始历史快照和 `EventSource` 建立期间显示“正在连接”，仅在原生连接错误后显示重连提示；初始快照请求失败时会记录错误并回退到 `idle`；不支持 `EventSource` 时进入 `unsupported`，仅显示不支持提示。普通连接错误不关闭 source，下一次 `open` 会重新加载 HTTP 历史快照。日志流不承诺严格投递、持久回放或客户端 cursor 补偿。

不支持 SSE 时，日志不轮询，界面提示“当前浏览器不支持实时日志，请手动刷新查看最新内容”。

## 同步任务详情

同步任务详情 stream 先订阅任务事件，再读取数据库与最近 1000 行日志快照，避免订阅前的状态变化丢失。消息类型为 `snapshot`、`task_patch`、`log_append`、`complete`、`resync_required` 和 `error`；`complete` 不回放。任务不存在时返回带 `deleted=true` 的 `complete`；数据库读取、日志快照读取或日志订阅失败在 SSE 响应开始前返回普通 HTTP 500，不会伪装为已删除任务。

运行中任务会在单个进程 epoch 内缓存最近 64 条 `task_patch`。浏览器携带格式为 `<stream_epoch>:<sequence>` 的 `Last-Event-ID` 时，仅在 epoch 一致且缓存连续时回放后续 patch；空值、非法值、epoch 不同、缓存缺口或服务重启时均返回完整 snapshot。snapshot 带注册时的 sequence 水位线，后续只发送更大的 patch，避免旧事件回退快照。日志不参与回放，未命中时由 snapshot 中的最近日志恢复。

任务详情前端同样区分 `idle`、`connecting`、`connected` 和 `reconnecting`：首次订阅期间不显示断线提示，原生连接错误后才显示重连提示。任务完成、失败或删除后，服务端清理该任务的回放缓存和 sequence 状态。已订阅客户端继续接收最多 2 秒的最终日志增量，再收到唯一 `complete`；客户端关闭 source，避免终态任务自动重连。后续新建连接通过终态 snapshot（或缺失记录的 `deleted complete`）收敛，不会在终态 snapshot 后重复发送 `complete`。浏览器不支持 SSE 时，详情页仅对运行中任务每 5 秒请求 `GET /api/sync/task?sync_id=...`，终态后停止；日志仍由用户手动刷新。

## 日志 tailer 与事件字段

通用日志与同步任务日志共享 `logstream.GlobalManager`，按绝对路径复用 tailer。tailer 发现文件缺失、截断、半行过长或订阅者缓冲满时会通知 `resync_required` 或结束慢订阅；不会阻塞同步、上传、下载或刮削业务。

同步任务结构化 payload 由 `backend/internal/realtime/sync_task_events.go` 定义。`sync_id` 是真实同步记录 ID；`sequence` 仍按任务递增，以兼容全局事件消费者。任务终态会清理该任务的 sequence 状态，因此后续 `deleted=true` 事件可能从新的 sequence 重新开始；全局列表不得因旧 sequence 忽略删除事件，并在处理后清除该任务的本地去重记录。`sync_path_id`、`status`、`sub_status`、统计字段、阶段时间、`log_path`、`event_time`、路径和失败原因保持既有含义。

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
