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
| `GET /api/logs/stream?path=...&cursor=...&file_id=...` | 指定日志文件的批次增量与恢复提示 |
| `GET /api/logs/catch-up?path=...&cursor=...&file_id=...` | 按完整行分页补读断线缺口 |
| `GET /api/logs/view?path=...` | 内联纯文本展示完整的当前日志文件 |
| `GET /api/logs/download?path=...` | 下载完整的当前日志文件 |
| `GET /api/sync/task?sync_id=...` | 不读取结果 JSON 的任务基本信息 |
| `GET /api/sync/tasks/:id/result` | 独立的扫描结果摘要 |
| `GET /api/sync/tasks/:id/failures?offset=...&limit=...` | 失败范围分页 |
| `GET /api/sync/tasks/:id/stream` | 同步任务详情的状态 snapshot、patch 和完成通知 |

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

通用日志与任务详情共用 `AppLogViewer`。先请求 `GET /api/logs/old?pos=-1&direction=forward`，快照从固定读取终点反向定位，返回按新到旧排列的条目、历史翻页起点 `pos`、读取终点 `start_pos`、最后完整换行的 `cursor` 和文件身份 `file_id`。静态历史保留无换行尾行；实时窗口等待尾行补齐后再显示，订阅从完整行 cursor 衔接，不丢半行前缀。

浏览器将快照的 `file_id` 随历史翻页、cursor 补读和日志订阅传回；它是文件身份条件，正常追加不会改变。身份与读取内容来自同一个已打开文件，身份不匹配返回 `409 / LOG_CURSOR_INVALID`。旧 tailer 已释放后，断线期间发生的同大小或更大文件替换也会使旧位置失效。无条件静态历史读取和未指定 cursor 的首次 EOF 订阅保留原接口语义；cursor 补读与显式 cursor／`Last-Event-ID` 订阅必须携带 `file_id`，缺失时同样返回 `409 / LOG_CURSOR_INVALID`。

`log_batch` 携带 `entries`（旧到新）、末条 `cursor` 和 `file_id`，SSE ID 同为 cursor；前端只合并当前文件身份的批次。服务端达到 100 条、64 KiB 正文或 100ms 即发送；超过字节预算的单行完整独占一批。显式 cursor 或原生重连的 `Last-Event-ID` 优先于当前 EOF；负数、非法值和超出文件大小的位置返回 `409 / LOG_CURSOR_INVALID`。日志路径继续沿用既有白名单和安全路径校验。

原生连接错误仅显示重连状态，不判断认证。CLOSED 后允许一次补读并重建流，以处理断线期间的文件变化；重建的流尚未成功 open 就再次 CLOSED 时停止自动恢复。成功 open、显式连接或切换路径／实时模式才开启下一轮，避免流路由持续拒绝而普通 HTTP 正常时反复建连。重连成功后关闭旧流，按客户端最后 cursor 调用 catch-up，每页最多 500 条且正文软预算 64 KiB；收到并缓冲一页后才推进 cursor，再从该位置订阅。一次恢复最多 20 页，持续落后则提示并恢复最新窗口，较早日志仍可通过历史分页查看。`resync_required` 的队列满和补读超限走 cursor 恢复；截断、轮转或明确的 `LOG_CURSOR_INVALID` 重取快照，同时取消旧历史请求、作废旧文件的分页位置并提示。普通重连保留正在阅读的历史窗口及位置。HTTP 错误沿用公共分类；恢复失败停止本轮连接，不建立无界重试。

前端每 100ms 合并发布缓冲，最多保留 2000 行及约 2 MiB 的估算正文预算；超长单行允许单独保留。日志按实际换行高度维护滚动窗口，最多渲染 200 行。离开最新位置后冻结正在阅读的内容，滚近底部自动读取更早日志，向上滚动可重读较新历史；历史每页最多 200 行，正文最多缓存两页，已访问页仅保留文件位置。等级筛选可跨过整页隐藏的内容并保留可见锚点；筛选后不足一屏时，继续滚动按用户方向推进两页窗口，释放的页仍可反向重读。磁盘重读的范围与行数只按文件日志确定，浏览器本地提示不参与；无换行尾行仍是实际文件行。可滚动窗口合并、释放正文和调整行高时保留可见日志及其偏移，实时输入不推走阅读位置。

历史请求与实时恢复独立，同一时间只读取一页。forward 历史响应显式保留无换行尾行的 `cursor: 0`，它只表示该行未完成，历史位置仍按 `pos` 继续；重叠长行去重为空时只推进读取位置，保留当前非空阅读页。慢请求期间仍可阅读和滚动已有内容；用户反向滚动后，迟到的响应不得移走正在阅读的页，包括不足一屏、scrollTop 未变化的反向操作。等级变化取消旧历史请求；跨隐藏页形成的非相邻缓存只保留第一页正文，随后按原文件位置重读中间页，不遗漏重新启用的等级。失败保留正文并在底部信息栏提供重试；“返回最新”仅在浏览历史时以信息栏文字按钮显示。离开页面、切换路径和显式停止取消旧请求、连接及待处理刷新。清空只清理浏览器窗口，任务结束后仍可使用。

工具栏以“查看日志”为默认操作，在新标签页打开完整纯文本；下拉菜单提供“下载日志”，沿用原下载接口。预览复用下载的鉴权、日志路径白名单及原生文件发送，成功返回 `text/plain; charset=utf-8`、`Content-Disposition: inline`、`X-Content-Type-Options: nosniff` 和 `Cache-Control: no-store`，保留 Range；读取前失败仍返回公共 JSON 错误。浏览器原生搜索和全选复制可覆盖已接收正文。完整范围是打开时已写入的当前文件，后续内容通过刷新查看，不合并轮转备份；传输整份大文件和浏览器搜索仍有相应成本。

不支持 SSE 时，日志不轮询，界面提示“当前浏览器不支持实时日志，请手动刷新查看最新内容”。

## 同步任务详情

同步任务状态流使用 version 2，仅包含 `snapshot`、`task_patch`、`complete`、`resync_required` 和 `error`。snapshot 为 `{task, log_path}`，不携带日志或完整扫描结果。建连先查任务是否存在，注册任务订阅后再读取轻量快照，避免订阅前状态变化丢失；两次查询均排除 `scan_result`，不读取日志文件。任务不存在时发送 `deleted=true` 的 complete，数据库错误在 SSE 响应开始前返回普通 HTTP 错误。

前端并行请求任务基本信息和状态流，不等待日志。较晚返回的旧 HTTP 响应不能覆盖新 snapshot／patch；同一流按 sequence 丢弃旧 patch。任务状态与日志分别连接、分别显示错误。浏览器不支持 SSE，或状态 source 进入 CLOSED 后，仅对生成未终结或后台账本仍为 pending/running 的任务每 5 秒进行一次不重叠的轻量 HTTP 查询。CLOSED 时取消仍在途的首屏读取并开启新的降级轮次，旧响应不得回写；首次查询遇到 `401`／`403`／`404` 或明确的认证、来源、CSRF 拒绝，以及显式停止后，都不得重建计时器。

任务回放保留进程 epoch、最近 64 条 patch 和 `<stream_epoch>:<sequence>` Last-Event-ID 语义。epoch 变化、非法 ID 或缓存缺口退回状态 snapshot；snapshot 携带订阅水位，后续仅发送更大序号的 patch。日志恢复由独立 cursor 协议完成。

生成与后台账本均终结时立即发送最终 task_patch，保留 2 秒日志落盘窗口后发送唯一 complete；客户端随后关闭状态流，日志查看器结束实时订阅并再读一次最终快照。终态任务新连接只通过 snapshot 收敛，不重复 complete；complete 不参与回放。历史缺少后台字段仍沿用既有终态判断。

扫描摘要包括成功、失败、跳过数量及清理状态、原因，不包含 failures。运行事件与全局事件从已有结果投影摘要；记录列表直接解析摘要，不构造失败数组。初次详情通过 result 请求独立加载，失败只影响结果区域。失败范围由 failures 请求返回 `{entries,total,recorded}`，默认 100 条、最大 200 条，offset 不小于 0；total 包含文件与目录失败，不能使用 failed_files 代替。翻页失败保留已显示明细，重试实际失败的页码。未记录结果时 recorded=false，明确的空结果为 recorded=true。两种查询均保留现有 JSON 存储，摘要仍扫描全文，分页仍完整解析后裁剪，不代表数据库已按页读取。

生成终态 `2–6` 清除目录运行展示；后台事件、历史删除和记录列表的 sequence 收敛规则保持不变。结果与失败范围均显示普通文本。结果读取使用可取消请求；HTTP 基础读取失败保留已有快照，明确的 401／403／404 停止本轮降级查询；显式停止、任务切换和卸载后旧请求不得回写或重新创建计时器。

后台结果以同一持久化快照携带 `ledger_status`、`ledger_finished_at` 和 `ledger_error`；生成计数、结果和 `finish_at` 保持固定。生成终态且后台 pending/running 的 snapshot、patch 和重连回放仍继续订阅；后台结果成功保存后再发布终态，失败保存不能发送已经完成的假象。来源队列的生成完成事件、通知渠道的生成通知与详情 `complete` 是三个独立边界。

该 HTTP 降级读取必须校验业务成功和任务 ID，同一轮查询不得重叠；失败保留已有任务快照，并显示公共错误说明，后续成功清除错误。HTTP `401`、`403`、`404` 或明确的认证、来源、CSRF 拒绝会停止该轮查询，须由后续显式连接重新开始；这也适用于第一次降级读取，不能在失败后再次创建定时器。显式停止、任务切换或卸载会使在途结果失效，旧请求不得回写状态或重新创建轮询。SSE 支持与连接状态不因 HTTP 错误分类而改变。

## 日志 tailer 与事件字段

通用日志与同步任务日志共享 `logstream.GlobalManager`，按绝对路径复用 tailer。tailer 发现文件缺失、截断、轮转或订阅者缓冲满时会通知 `resync_required` 或结束慢订阅；不会阻塞同步、上传、下载或刮削业务。

历史日志与 cursor 补读按完整行读取，正常长行不受 64 KiB／1 MiB 的固定上限影响。历史 `direction=forward` 保持从指定位置向文件头取行、按新到旧返回，`backward` 保持向 EOF 读取；分页位置按真实字节落在完整行边界。静态历史接受无换行的 EOF 尾行；内部尾读返回固定读取终点，HTTP 实时 cursor 指向最后完整换行。历史页正文软预算约 2 MiB，跨越预算的单行保持完整。

实时 tailer 保留跨多次写入的未完成行，直到换行补齐后投递一次；cursor 补读同样只推进完整换行记录，未完成尾行不算落后或超限，不因长度触发重同步。翻页和补读按条数与字节软预算分页，保持顺序，不丢长行头尾。完整长行增加内存和传输量，磁盘保留与单次轮转写入边界见 [日志配置](../operations/configuration.md#日志行为与脱敏)。`GET /api/logs/download` 仅下发已保存文件，不能找回被 writer 拒绝的正文。

同步任务结构化 payload 由 `backend/internal/realtime/sync_task_events.go` 定义。`sync_id` 是真实同步记录 ID；`sequence` 仍按任务递增，以兼容全局事件消费者。生成与后台均终结时会清理该任务的 sequence 状态，因此后续 `deleted=true` 事件可能从新的 sequence 重新开始；全局列表不得因旧 sequence 忽略删除事件，并在处理后清除该任务的本地去重记录。`sync_path_id`、`status`、`sub_status`、统计字段、阶段时间、`log_path`、`event_time`、路径和失败原因保持既有含义。

运行中同步任务的进度、阶段切换和终态按同一发布顺序提交。普通进度保留约 1 秒节流，已有发布在途时跳过本次发布、继续累计计数；强制发布及阶段、终态迁移等待旧发布结束，并响应取消。计数（包括零值）成功落库后才广播对应快照，普通写入失败允许后续重试；最终计数或完成状态写入失败不能发布成功终态。生成终态之后不再发布旧的运行中计数，后续后台事件从数据库取得冻结的生成快照；SSE 的 sequence 不能代替这个并发边界。

同一进程内，同一同步记录的状态保存与事件发布、历史删除与删除事件发布共用短临界区。删除等待已经开始的状态发布完成；删除先完成时，后续状态写入不能再发布该记录的旧事件，也不能重建回放缓存。新建记录的事件同样在这一边界内确认当前记录后发布。不同记录独立处理，等待状态更新锁可响应取消；锁不覆盖扫描、文件入账或通知发送。定期清理与用户删除遵循同一顺序。

代理层的缓冲、超时和可信转发 header 要求见 [反向代理](../operations/reverse-proxy.md)。

HTTP 快照、详情流和全局事件使用相同的可空耗时字段：`net_file_duration_ms`、`local_file_duration_ms`、`generation_duration_ms`、`total_duration_ms`。NULL 表示未记录，0 是有效的不足 1 毫秒。生成终态冻结阶段及生成耗时，后续后台事件仍可更新总耗时；重连与 HTTP 降级保留同一显示精度。字段语义见 [数据库 schema](../reference/database-schema.md#syncs)。

## 不变量

- SSE 只从服务端向浏览器推送状态，写操作仍通过既有鉴权和 CSRF 保护的 HTTP API。
- 全局事件最多一次通知，页面必须通过 HTTP snapshot 最终收敛；不得把它当作可靠消息队列。
- 同步任务 patch 仅在同一 `stream_epoch` 且缓存连续时回放；其他情况必须回退到完整 snapshot，日志不参与任务 patch 回放。
- `complete` 不回放，终态客户端收到唯一完成事件后关闭状态 source；终态任务的新连接通过 snapshot 或 `deleted complete` 收敛。
- SSE 不支持跨 origin Cookie 通道，也不得因为浏览器不支持 EventSource 而引入全局轮询。
- 正常长行保持完整内容和真实行边界；实时半行补齐后只投递一次，真实轮转、截断与队列异常仍触发原重同步。

## 验证方式

- 运行 `(cd backend && go test ./internal/controllers/ -run 'Test.*(Event|Stream|Log)')` 和 `(cd backend && go test ./internal/realtime/)`。
- `helpers`／`logstream` 及现有日志、任务流回归覆盖长行头尾、中文、双向分页、cursor 补读和跨次半行；tailer 状态变化加测 `logstream` race，具体入口见 [验证说明](../engineering/verification.md#sdk-日志与完整行验证)。
- 前端改动按 [验证说明](../engineering/verification.md) 选择相应验证。
- 修改代理层时在测试环境订阅三个 SSE 路由，确认流式响应未被缓冲、断线恢复按 snapshot 收敛。
