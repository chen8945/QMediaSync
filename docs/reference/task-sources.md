# 任务来源枚举

> 职责：定义下载、上传和同步队列任务来源的稳定机器值、展示映射和迁移边界。
>
> 权威范围：本文档维护下载、上传、同步队列和 STRM 生成任务的来源 / 类型机器值，以及其 API、事件和迁移边界；表字段见 [数据库 schema 与迁移](database-schema.md)，具体流程见对应架构文档。
>
> 修改时机：新增、删除或迁移任务来源、任务类型、前端映射、队列 API / 事件、队列 key 或持久化存储值时必须更新本文档。
>
> 相关代码：`backend/internal/models/dbdownload.go`、`backend/internal/models/dbupload.go`、`backend/internal/models/strm_generation_task.go`、`backend/internal/synccron/`、`backend/internal/realtime/`、`backend/internal/controllers/file.go`、`frontend/src/utils/taskSourceUtils.ts`、`frontend/src/utils/sourceTypeUtils.ts`。

下载和上传队列表都会持久化 `source` 与 `source_type`。`source` 表示创建任务的业务流程，用于来源级查询、去重和后处理分支；`source_type` 表示关联的账号 / 存储后端类型，完整稳定值见 [数据库 schema 与迁移](database-schema.md#常用枚举)。两者不能互相推导：例如 `strm_sync` 可以对应多种存储后端，而 Emby 媒体信息提取下载任务同时使用 `source=emby_media` 与 `source_type=emby_media`。上传任务只在同一 `source + source_type + account_id + remote_full_path` 范围内对活跃状态去重；`remote_full_path` 已是实际目标，因此不以 `sync_path_id` 额外切分。

下载和上传队列把远端位置与身份分开存储：`remote_full_path` 是创建任务时确定、包含文件名的远端完整路径，`remote_file_id` 只表示服务实际返回的稳定文件 ID，115 PickCode 只存 `remote_pick_code`。摘要始终按传输方向使用完整路径；历史任务无法可靠回填时显示“远端路径未知（历史记录未保存）”，不得把 ID 回退显示为路径。local 上传例外地将该字段作为受配置源根目录约束的本地复制目标路径保存；它不是远端身份，前端不会以远端路径展示。

下载任务保留 `remote_path` 作为不含文件名的远端目录路径。OpenList 只在 API 提供非空对象 ID 时写入文件 ID，带签名的下载直链只存入隐藏的 `remote_download_url`；Emby 提取地址、Emby 条目 ID 和本地复制源路径同样只使用隐藏执行字段。Emby 提取请求不解析响应内容，但必须排空并关闭响应体。local 与 Emby 不伪造远端身份字段。SHA1 和 MD5 仅在对应远端响应明确声明算法时写入和展示；115 使用 SHA1，百度使用 MD5。

上传覆盖时，只有旧远端文件已删除成功才会在 `replaced_remote_file_id` 记录和展示旧文件 ID：115 使用文件 ID，百度使用 `fs_id`，OpenList 使用非空对象 ID。新文件上传、删除失败的覆盖，以及没有稳定 ID 的来源都不会显示该字段。

同步队列的 `task_type` 不写入数据库，但参与队列路由、去重 key、取消和状态查询。STRM 生成任务的 `source` 和 `task_type` 会写入数据库。

后端业务标识使用稳定机器值，展示层负责把机器值映射为用户可读文案。

队列页面通过 `taskSourceUtils.ts` 展示 `source` 和 `source_type`。其中任务队列的 `source_type=local` 显示为「本地文件」，同步目录 / 账号映射中的同值显示为「本地目录」，两者不可混用。

`DownloadTaskStatusChangedPayload` 是内部下载状态事件，其 `source` 使用上述下载来源机器值。`sync_path_id` 的旧数据回退、下载状态触发的 Emby 刷新等待和取消语义见 [Emby 媒体库同步](../architecture/emby-library-sync.md)；字段存储兼容性见 [数据库 schema 与迁移](database-schema.md)。

## 下载任务

| 字段 | 存储值 | 展示文案 | 用途 |
| --- | --- | --- | --- |
| `db_download_tasks.source` | `strm_sync` | `STRM 同步` | STRM 同步产生的下载任务 |
| `db_download_tasks.source` | `local_file` | `本地文件` | 本地文件复制任务 |
| `db_download_tasks.source` | `emby_media` | `Emby 媒体信息提取` | Emby 媒体信息提取触发任务 |

## 上传任务

| 字段 | 存储值 | 展示文案 | 用途 |
| --- | --- | --- | --- |
| `db_upload_tasks.source` | `strm_sync` | `STRM 同步` | STRM 同步产生的上传任务 |
| `db_upload_tasks.source` | `scrape_organize` | `刮削整理` | 刮削整理产生的上传任务 |
| `db_upload_tasks.source` | `directory_monitor` | `目录监控上传` | 目录监控发现本地文件后产生的上传任务 |

## 同步队列任务类型

| 字段 | 存储值 | 展示文案 | 用途 |
| --- | --- | --- | --- |
| `SyncTaskTypeStrm` | `strm_sync` | `STRM 同步` | STRM 同步队列任务 |
| `SyncTaskTypeScrape` | `scrape_organize` | `刮削整理` | 刮削整理队列任务 |

## STRM 生成任务

`strm_generation_tasks.source` 和 `task_type` 会持久化到数据库；完整字段语义见 [数据库 schema 与迁移](database-schema.md#strm_generation_tasks)，状态流转见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。这些机器值没有对应的队列页面展示映射。

| 字段 | 存储值 | 用途 |
| --- | --- | --- |
| `StrmGenerationSourceUploadCompleted` | `upload_completed` | 上传完成后创建的文件级 STRM 生成任务 |
| `StrmGenerationSourceWebhook` | `webhook` | [STRM Webhook](strm-webhook.md) 创建的任务 |
| `StrmGenerationSourceRemoteExists` | `remote_exists` | 远端同名文件经大小和 SHA1 确认一致、跳过真实上传后创建的任务 |
| `StrmGenerationTaskTypeFile` | `file` | 单文件任务 |
| `StrmGenerationTaskTypeDirectoryScan` | `directory_scan` | 目录扫描父任务 |
| `StrmGenerationTaskTypeBatchFiles` | `batch_files` | 批量请求父任务 |

## API 与事件暴露

`GET /api/download/queue` 和 `GET /api/upload/queue` 的 `data.list[]` 返回原始机器值 `source` 和 `source_type`；列表查询当前只支持 `status`、`page`、`page_size`，不支持按来源筛选。

全局 SSE 的 `download_queue_changed` 和 `upload_queue_changed` 使用 `QueueChangedPayload.source` 传递同一机器值。事件只用于提示或局部 patch；页面展示仍应以 HTTP 列表快照和本文件规定的映射为准。

## 维护约束

- 不要把展示文案直接写入任务来源字段或同步队列任务类型。
- 新增任务来源或队列任务类型时，同步更新后端常量、前端展示映射和本文档。
- 版本 `60 → 61` 的队列身份迁移不补查远端服务：只能从已有任务和 `sync_files` 回填可靠字段，隐藏执行定位从旧字段迁入，两个旧上传完成字段会被删除。升级前备份数据库，且不得让依赖旧字段的二进制与新版本混合运行。
- 当前兼容迁移仅在数据库版本 `43 → 44` 执行：下载 `source` 的 `strm同步`、`本地文件`、`emby媒体信息提取` 分别迁移为 `strm_sync`、`local_file`、`emby_media`；下载 `source_type` 的 `emby媒体信息提取` 迁移为 `emby_media`；上传 `source` 的 `strm同步`、`刮削整理` 分别迁移为 `strm_sync`、`scrape_organize`。未知或自定义值保持不变，不得在无明确迁移设计时擅自归一化。
- 仅存在于内存队列的任务类型不需要数据库迁移，但需要测试队列 key 和状态输出是否使用机器值。
- `frontend/src/utils/sourceTypeUtils.ts` 面向同步目录和账号来源；任务队列来源使用 `frontend/src/utils/taskSourceUtils.ts`，不要混用两套 `local` 文案。
- `MQ的媒体库` 是 115 授权应用名，不属于任务来源枚举。

## 不变量

- 持久化来源和任务类型只使用稳定机器值，展示文案不得写回数据库或队列 key。
- 新增或变更已存储值必须包含迁移；仅内存队列值不迁移，但必须保持路由、去重和状态输出一致。
- 同步目录 / 账号来源映射与任务队列来源映射是不同前端模块，不能混用 `local` 等展示文案。
- STRM 生成来源与任务类型变更时，必须同步检查本文件、`strm_generation_tasks` 的迁移兼容性，以及对应上传 / Webhook 流程。

## 验证方式

- 运行涉及来源变更的 `models`、`synccron` 或队列包测试，覆盖存储值、去重 key 和状态输出。
- 前端映射改动按 [验证说明](../engineering/verification.md) 选择相应验证。
- 修改已持久化值时核对 [数据库 schema 与迁移](database-schema.md) 中的迁移版本和历史值说明。
