# STRM Webhook

> 职责：定义外部程序创建 STRM 生成任务的 API、字段、响应和幂等边界。
>
> 权威范围：本文档维护 Webhook API 契约；入队后的上传与 STRM worker 流程见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)，API Key 规则见 [认证会话](../architecture/authentication-sessions.md)。
>
> 修改时机：修改路由、鉴权、请求字段、响应、幂等哈希、父子任务或 Webhook 刷新行为时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/strm_webhook.go`、`backend/internal/syncstrm/`、`backend/internal/models/strm_generation_task.go`、`backend/internal/controllers/strm_webhook_test.go`。

本文说明外部程序通过 Webhook 请求 QMediaSync 创建 STRM 生成任务的接口边界。Webhook 请求只负责入队，实际 STRM 写入、`SyncFile` 更新、同名元数据下载和 Emby 刷新提交都由后台 STRM worker 处理。

## 入口和鉴权

接口地址：

```http
POST /api/strm/webhook
Content-Type: application/json
X-API-Key: qms_xxxxxxxxxxxxxxxxxxxxxxxx
```

接口不使用浏览器登录态，也不需要 CSRF。鉴权只接受 API Key：

- 推荐使用 `X-API-Key` 请求头。
- 仅在调用方无法设置请求头时，使用 `?api_key=` 查询参数。
- API Key 无效或缺失时返回 HTTP `401`。请求未携带 API Key（或只有空白）时，响应带顶层 `error_code=AUTHENTICATION_REQUIRED`；API Key 不存在或已停用时带 `error_code=AUTHENTICATION_INVALID`；API Key 查询出现内部故障时不带 `error_code`。错误码含义见 [认证会话](../architecture/authentication-sessions.md)。

API Key 在 Web 页面「系统设置 - API Key」中创建。完整密钥只会在创建响应中返回一次，后端只保存哈希。

## 处理边界

- 当前 STRM Webhook 仅支持 115 网盘来源。
- `sync_path_id` 可选；显式提供时必须指向 115 同步目录；未提供时，后端会按请求里的 115 远端路径自动匹配同步目录。
- 当前 Webhook 按 115 远端文件详情解析文件信息，`path` 和 `directory_path` 都表示 115 远端路径。
- 禁止通过 `local_path` 指定本地写入位置；顶层请求和批量项中的 `local_path` 都会被拒绝。
- 本地 STRM 输出路径只能由显式指定或自动匹配到的同步目录配置计算。
- 文件级请求入队前会按 `file_id` 或 `path + file_name` 查询远端详情，并以解析后的真实远端路径再次校验同步目录边界。
- `file` 动作应只传入实际文件；当前实现解析 115 详情后尚未额外校验对象类型，目录 ID 或目录路径可能被当作文件任务接收。调用方不得依赖接口拒绝目录，后续修改此处应补充文件类型校验和回归测试。
- 目录级请求同时提供 `directory_id` 和 `directory_path` 时，会按 `directory_id` 查询 115 目录详情，并要求返回对象是目录、目录 ID 和远端路径都与请求一致。
- 未提供 `sync_path_id` 时，`file` 和 `batch_files` 必须提供 `path + file_name`；仅提供 `file_id` 无法自动判断同步目录。
- 未提供 `sync_path_id` 时，`directory_scan` 必须提供 `directory_path`；仅提供 `directory_id` 无法自动判断同步目录。
- 批量请求自动匹配时，所有 `items[]` 必须匹配到同一个同步目录；跨同步目录的文件需要拆成多个请求，或显式提供 `sync_path_id`。
- 目录级请求只创建目录扫描父任务，HTTP 请求内不同步展开完整目录。
- 合法请求只创建 `strm_generation_tasks`，不会在 HTTP 请求内直接写 STRM，也不会直接请求 Emby。
- `download_meta` 和 `refresh_emby` 默认关闭；调用方必须显式开启需要的后处理。

## 请求动作

`action` 支持三类值：

| action | 说明 |
| --- | --- |
| `file` | 创建单文件 STRM 生成任务。 |
| `batch_files` | 至少有一个合法 `items[]` 项时，创建批量父任务，再为每个合法项创建单文件子任务。单项失败不会影响其他合法项入队，重试会复用父任务并补齐缺失子任务。 |
| `directory_scan` | 创建目录扫描父任务，后续由 STRM worker 异步递归枚举远端目录及其子目录，并为视频文件创建子任务。 |

`action` 为空时，后端按字段自动判断：

- `items` 为非空数组时视为 `batch_files`。
- 有 `directory_id` 或 `directory_path` 时视为 `directory_scan`。
- 其他情况视为 `file`。

## 请求字段

通用字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `sync_path_id` | 否 | 同步目录 ID。显式提供时必须是 115 同步目录；缺省时按 115 远端路径自动匹配最具体的同步目录。 |
| `action` | 否 | `file`、`batch_files` 或 `directory_scan`。 |
| `download_meta` | 否 | 是否下载本次视频强相关的同名元数据，默认 `false`。 |
| `refresh_emby` | 否 | 是否在 STRM 变更或新增元数据下载任务后提交 Emby 刷新目标，默认 `false`。 |

`download_meta` 和 `refresh_emby` 是请求级字段。`batch_files` 的 `items[]` 内禁止放这两个开关；如果任一 item 包含 `download_meta` 或 `refresh_emby`，整个请求返回 HTTP `400`。批量和目录扫描会按整个请求统一控制，所有子任务继承父任务开关。

单文件字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `path` | 条件必填 | 文件所在 115 远端目录。使用 `path + file_name` 定位时必填；未提供 `sync_path_id` 时也必填。 |
| `file_name` | 条件必填 | 文件名。使用 `path + file_name` 定位时必填；未提供 `sync_path_id` 时也必填。 |
| `file_id` | 条件必填 | 115 文件 ID。与 `path + file_name` 至少提供一组；提供后后端优先用它查询真实远端文件信息。未提供 `sync_path_id` 时只能作为补充字段，不能单独定位。 |
| `pick_code` | 否 | 只能作为辅助字段，不能单独定位文件。 |
| `parent_id` | 否 | 父目录 ID；远端详情解析成功后会以解析结果为准。 |
| `file_size` | 否 | 文件大小，单位字节；远端详情解析成功后会以解析结果为准。 |
| `sha1` | 否 | 文件 SHA1；远端详情解析成功后会以解析结果为准。 |
| `mtime` | 否 | 文件远端更新时间戳；远端详情解析成功后会以解析结果为准。 |

批量请求使用顶层 `items` 数组，数组项字段与单文件字段一致。`items` 不能为空。

目录扫描字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `directory_id` | 条件必填 | 115 目录 ID。与 `directory_path` 至少提供一个；未提供 `sync_path_id` 时不能单独使用；与 `directory_path` 同时提供时必须解析到同一个 115 目录。 |
| `directory_path` | 条件必填 | 115 远端目录路径。提供时必须位于同步目录的 `remote_path` 下；未提供 `sync_path_id` 时必填；与 `directory_id` 同时提供时必须和 115 目录详情一致。 |

## 同步目录自动匹配

未提供 `sync_path_id` 时，后端会用请求中的 115 远端路径匹配 `source_type=115` 的同步目录：

- 只有路径位于同步目录 `remote_path` 内时才算匹配。
- 如果多个同步目录都匹配，选择 `remote_path` 最长的同步目录，也就是最具体的目录。
- 如果最长匹配仍有多个同步目录，返回 HTTP `400`，调用方需要显式提供 `sync_path_id`。
- 如果没有任何同步目录匹配，返回 HTTP `400`。
- `batch_files` 中所有 item 必须匹配到同一个同步目录；否则返回 HTTP `400`。

## 元数据下载

`download_meta=true` 时只下载和本次生成视频强相关的元数据。对视频文件名去掉扩展名得到 `base`，只匹配：

- `base + 元数据扩展名`
- `base + "-thumb" + 元数据扩展名`

例如视频文件是：

```text
古诺希亚 - S01E18 - ANi.mp4
```

只会匹配类似：

```text
古诺希亚 - S01E18 - ANi.nfo
古诺希亚 - S01E18 - ANi.srt
古诺希亚 - S01E18 - ANi-thumb.jpg
古诺希亚 - S01E18 - ANi-thumb.png
```

不会下载同目录下其他海报、fanart、season 图、全局 NFO 等文件。Webhook 元数据下载也不会删除本地多余元数据，不会上传本地元数据。

Webhook 任务按实际处理批次使用配置，修改从下一小批生效；批量请求跨批执行时不固定整条请求的配置，见 [后处理配置何时生效](../architecture/upload-and-strm-processing.md#后处理配置何时生效)。

元数据匹配可复用当前小批内仍有效的完整父目录列表；同名文件冲突检查仍读取新列表，具体范围见 [STRM 后处理](../architecture/upload-and-strm-processing.md#strm-后处理和源文件删除)。

元数据扩展名沿用当前同步目录的 STRM 配置：同步目录自定义配置优先，未自定义时回退全局配置。只有本地缺失的匹配元数据会创建下载任务，下载仍走现有下载队列。后台 STRM worker 也会校验任务来源，只有 Webhook file 任务会响应该开关，上传完成、远端已存在等非 Webhook STRM 任务即使意外写入该字段也不会触发这套同名元数据下载规则。

## Emby 刷新

`refresh_emby=true` 时，文件任务只有在 STRM 发生变更或新增元数据下载任务后才解析并向 Emby 刷新协调器提交刷新目标。

- `file`：单文件任务完成后提交目标，但仍进入现有 Emby 刷新协调器防抖。
- `batch_files`：所有子任务完成或跳过且存在 STRM / 元数据变化后，由批量父任务统一提交一次已收集目标集合；任一子任务失败则父任务失败且不提交刷新。
- `directory_scan`：目录展开出的所有子任务完成或跳过且存在 STRM / 元数据变化后，由目录扫描父任务统一提交一次已收集目标集合；任一子任务失败则父任务失败且不提交刷新。

刷新目标优先保持 item 精度。每个发生变化的文件会先解析为 Movie、Episode、Season、Series 等 item；解析不到可靠 item 时回退同步目录关联媒体库。目标集合会去重，同一媒体库内如果出现 library fallback，该媒体库内其他 item 目标会被覆盖，不再单独提交；不同媒体库互不影响。

提交不在 STRM worker 内直接请求 Emby。只有 Emby 已配置、已启用且刷新目标能映射到关联媒体库时，协调器才会创建或合并 `emby_library_refresh_tasks`；其他情况会跳过刷新。防抖、同媒体库关联同步目录的等待范围、可重试失败下载和最长等待后取消等规则由 [Emby 媒体库同步](../architecture/emby-library-sync.md) 统一维护。

上传完成、远端已存在等非 Webhook STRM 任务保持原有行为：STRM 变更后可逐文件提交 Emby 刷新，不受 Webhook 默认开关影响。

## 响应

成功接收请求时返回 HTTP `200`，业务响应的 `data` 包含：

| 字段 | 说明 |
| --- | --- |
| `request_id` | 本次 Webhook 请求 ID，格式为 `strm_` 加随机字符串。 |
| `task_ids` | 本次接受的 STRM 生成任务 ID 列表。`batch_files` 返回合法文件子任务 ID，不返回批量父任务 ID。 |
| `accepted_count` | 成功入队或命中已有活动任务的数量。 |
| `failed_count` | 批量请求中失败项数量。 |
| `results` | 逐项结果，`index` 从 0 开始，包含 `accepted`、`task_id` 或 `error`。 |

单文件和目录扫描请求如果校验失败，会返回 HTTP `400`。批量请求中单项失败不会导致整个请求失败，只会在对应 `results` 项中写入错误；如果所有 item 都不合法，仍返回 HTTP `200`、`accepted_count=0`、`failed_count=items.length`，且不会创建父任务或子任务。`items[]` 内放入请求级开关属于整批请求错误，会直接返回 HTTP `400`。

所有 JSON 响应使用 `APIResponse` 包络。成功响应的 JSON `code` 为 `200`；当前错误响应虽然分别使用 HTTP `400` 或 `401`，但 JSON `code` 固定为 `500`，调用方必须以 HTTP 状态和 `message` 判断请求是否失败，不能把 JSON `code` 当作 HTTP 状态。

请求完成入队统计后，会向 `app.log` 写入 `[STRM Webhook] 接收到 STRM 任务` INFO 日志，包含 `request_id`、`action`、`sync_path_id`、`download_meta`、`refresh_emby`、接收 / 拒绝数量和任务 ID 列表。日志不会记录 API Key、请求头或鉴权密钥。

示例响应：

```json
{
  "code": 200,
  "message": "STRM 生成任务已接收",
  "data": {
    "request_id": "strm_abcdefghijklmnop",
    "task_ids": [123],
    "accepted_count": 1,
    "failed_count": 0,
    "results": [
      {
        "index": 0,
        "accepted": true,
        "task_id": 123
      }
    ]
  }
}
```

## 幂等和重试

Webhook 入队会为请求生成短格式 `request_hash`，形如 `webhook:file:v2:<sha256>`。请求动作、远端定位信息和请求级开关会参与摘要计算，但远端路径、文件名和目录路径不会明文拼进唯一键；完整值仍保存在 `strm_generation_tasks.path`、`file_name` 或 `directory_path` 等任务字段中。

- `pending`、`running`、`finalizing` 或 `waiting_children` 状态的相同请求会复用已有任务，不重复创建。
- 两个相同请求并发到达时，数据库唯一键冲突会被入队逻辑吸收并复查已有活跃任务；调用方应拿到同一个任务 ID，而不是偶发唯一键错误。
- 升级后再次提交相同请求时，会优先生成短格式哈希；如果数据库中仍有旧格式活跃任务，会复用旧任务，不重复创建。
- 历史任务如果已经 `failed`、`completed` 或 `cancelled`，再次提交相同请求会归档旧请求哈希并创建新的待处理任务。
- worker 自动领取 `pending` 和 `finalizing` 任务。文件生成阶段失败会标记为 `failed`、递增 `retry_count` 并写入 `last_error`；独立文件在保存完成状态前发生普通收尾错误，也会将仍在 `running` 的任务标记为 `failed`，需重新提交才会再执行。父任务刷新提交等 `finalizing` 阶段副作用失败会保留在 `finalizing`，由后续 worker 再次处理。取消、状态保存失败及目录失效的边界见[后处理配置何时生效](../architecture/upload-and-strm-processing.md#后处理配置何时生效)。
- `batch_files` 父任务不由 worker 执行；创建后状态为 `waiting_children`。所有子任务的生成阶段完成后，父任务转为 `completed` 或 `failed`；最后一个成功子任务可能短暂处于 `finalizing`，随后完成副作用并收敛到终态。相同批量请求重试时会按合法 item 的原始 `items[]` index 匹配子任务，已存在的子任务复用，缺失的子任务补建，非法项仍按原始 index 返回失败结果。
- `batch_files` 父任务和合法子任务在同一个数据库事务内创建；SQLite 遇到并发写入锁冲突时会回滚并有限重试整个父子任务事务，确保相同请求最终复用同一组任务；任一非幂等冲突类子任务写入失败时整个请求返回错误并回滚父任务，后续相同请求会重新创建完整父子任务集合。
- `directory_scan` 父任务展开完成后记录 `total_items`；子任务后续完成、失败或跳过时累计相应的 `accepted_items`、`failed_items`、`skipped_items`，变化另计入 `changed_items` 和 `new_meta_items`。

## 查询处理结果

```http
GET /api/strm/tasks?id=123
X-API-Key: qms_xxxxxxxxxxxxxxxxxxxxxxxx
```

此查询复用常规 API 鉴权，接受 API Key 或登录会话。Webhook 创建接口仍只接受 API Key。查询只读取后处理任务，不创建同步记录。

| 参数 | 说明 |
| --- | --- |
| `page` | 页码，默认 1，范围 1–1000000 |
| `page_size` | 每页数量，默认 20，范围 1–100 |
| `id` | 精确查询返回的任务 ID |
| `parent_task_id` | 查询某个父任务的子项 |
| `upload_task_id` | 限定上传任务 |
| `sync_path_id` | 限定同步目录 |

多个筛选条件同时提供时取交集。未提供任务 ID、父任务 ID 或上传任务 ID 时，只列顶层任务。结果按任务 ID 从新到旧排列，响应沿用 `APIResponse`：`data.items` 是当前页，`data.total` 是符合条件的总数；没有匹配项返回空数组。无效分页返回 HTTP 400，数据库读取失败返回 HTTP 500。

每项包含任务/父任务/上传/同步目录 ID、来源、类型、文件名或目录路径、`status`、`skip_reason`、`total_items`、`accepted_items`、`failed_items`、`skipped_items` 和 `updated_at`。不返回 PickCode、幂等哈希或原始错误详情。历史没有记录的数量可为 `null`，表示未记录，不能当作 0。

`status=skipped` 表示已跳过，`skip_reason` 说明命中的规则。父任务分别展示已完成、失败和跳过数量；全跳过为 `skipped`，完成与跳过混合为 `completed`，有真实失败为 `failed`。`accepted_items` 是已处理完成的子项数量，不能与创建接口的 `accepted_count` 入队数量混用。生成过滤和源文件保留规则见[过滤与跳过结果](../architecture/upload-and-strm-processing.md#过滤与跳过结果)。

上传详情和同步目录卡片中的 STRM 结果入口已隐藏；结果查询 API 继续可用，外部程序可按任务 ID 再次查询终态，或按父任务 ID 查询子项。

## 示例

单文件请求主示例使用 `path + file_name`：

```json
{
  "action": "file",
  "download_meta": true,
  "refresh_emby": true,
  "path": "/剧集/示例剧/S01",
  "file_name": "示例剧 - S01E01.mkv"
}
```

也可以可选提供 `file_id`，后端会优先用它查询真实远端文件信息：

```json
{
  "action": "file",
  "download_meta": true,
  "refresh_emby": true,
  "file_id": "115-file-id",
  "path": "/剧集/示例剧/S01",
  "file_name": "示例剧 - S01E01.mkv"
}
```

批量文件请求：

```json
{
  "action": "batch_files",
  "download_meta": false,
  "refresh_emby": false,
  "items": [
    {
      "path": "/剧集/示例剧/S01",
      "file_name": "示例剧 - S01E01.mkv",
      "file_id": "115-file-id-1"
    },
    {
      "path": "/剧集/示例剧/S01",
      "file_name": "示例剧 - S01E02.mkv",
      "file_id": "115-file-id-2"
    }
  ]
}
```

目录扫描请求：

```json
{
  "action": "directory_scan",
  "download_meta": true,
  "refresh_emby": true,
  "directory_path": "/剧集/示例剧/S01"
}
```

`curl` 示例：

```bash
curl -X POST 'http://127.0.0.1:12333/api/strm/webhook' \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: qms_xxxxxxxxxxxxxxxxxxxxxxxx' \
  -d '{
    "action": "file",
    "download_meta": true,
    "refresh_emby": true,
    "path": "/剧集/示例剧/S01",
    "file_name": "示例剧 - S01E01.mkv"
}'
```

## 不变量

- Webhook 只负责鉴权、校验和入队；STRM 写入、`SyncFile` 更新、元数据下载和 Emby 刷新由后台 worker 执行。
- 鉴权只接受 API Key，不使用浏览器 Cookie 会话或 CSRF。
- 相同活动请求必须复用任务；并发唯一键冲突对调用方表现为复用，而不是数据库错误。
- `batch_files` 与 `directory_scan` 的父任务只在所有子任务完成或跳过且存在变化时提交 Emby 刷新；任何子任务失败时父任务失败且不提交。
- 远端路径只用于 115 远端定位，本地 STRM 路径必须由同步目录计算，不能接受调用方的 `local_path`。

## 验证方式

- 运行 `(cd backend && go test ./internal/controllers/ -run TestStrmWebhook)` 和 `(cd backend && go test ./internal/syncstrm/)`。
- 修改鉴权、响应或幂等规则时覆盖 header / 查询参数 API Key、重复请求、并发入队、父子任务和远端路径边界。
