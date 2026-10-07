# STRM Webhook：让外部程序触发 STRM 生成

外部程序把视频保存到 115 网盘后，可以调用 STRM Webhook，通知 QMediaSync 为这些文件生成本地 STRM。它不会替外部程序上传视频，也不需要为每个新文件重新扫描整个同步目录。

**此功能仍处于测试阶段，目前只支持 115 网盘。** 建议先用少量文件确认路径、生成结果和失败处理，再接入自动化流程。「STRM 同步 → STRM 设置」底部会显示测试阶段说明，目前没有单独的 Webhook 启用开关。

## 接入前准备

1. 按 [账号管理](账号管理.md) 添加并授权 115 账号。
2. 按 [生成 STRM](生成-STRM.md) 建立一个 115 同步目录，确认其来源覆盖准备处理的网盘路径，本地 STRM 存放目录和 STRM 直连地址也已配置正确。
3. 确认调用方能访问 QMediaSync 的管理服务地址，例如 `http://qms.example:12333`，这里不是 Emby 代理的 `8095` 端口。
4. 进入「系统设置 → API Key」，点击「生成 API Key」，填写便于辨认的名称，再点击「生成」。复制弹窗中的完整密钥，并确认列表中的状态为启用。

完整 API Key 只显示一次，关闭弹窗后不能再次查看。这里使用的是 **QMediaSync API Key**，不是 Emby API Key、TMDB API Key 或 115 的授权凭据。

本文中的 `qms.example`、`YOUR_QMS_API_KEY`、文件路径和任务 ID 都是示例，使用时替换成自己的值。

## 最小完整请求：处理一个视频

假设已有同步目录覆盖 115 网盘中的 `/电影`，而视频已存在于 `/电影/示例电影 (2024)/示例电影.mkv`。可以发送：

```bash
curl --request POST 'http://qms.example:12333/api/strm/webhook' \
  --header 'Content-Type: application/json' \
  --header 'X-API-Key: YOUR_QMS_API_KEY' \
  --data-raw '{
    "action": "file",
    "path": "/电影/示例电影 (2024)",
    "file_name": "示例电影.mkv"
  }'
```

这里的 `path` 是 **115 网盘中视频所在的目录**，`file_name` 是包含扩展名的文件名。不要填写宿主机、容器或 Emby 的本地路径。

请求会按远端路径寻找已有的 115 同步目录，本地输出位置由这个同步目录计算。接口不接受用 `local_path` 指定写入位置；想改变输出目录，应先修改对应同步目录配置。

鉴权推荐使用 `X-API-Key` 请求头。只有外部程序不能设置请求头时，才使用 URL 查询参数 `?api_key=YOUR_QMS_API_KEY`。这个创建接口不接受浏览器登录 Cookie，也不需要 CSRF Token。

## 收到成功响应后还要做什么

成功接收时可能返回：

```json
{
  "code": 200,
  "message": "STRM 生成任务已接收",
  "data": {
    "request_id": "strm_example_request",
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

这表示任务已经接收或复用了仍在处理的任务，**不表示 STRM 已经生成完成**。保存 `task_ids`，再按下文查询最终状态。`request_id` 用于核对日志，不能代替任务 ID。

Webhook 不会新增完整 STRM 同步记录，也没有独立的 Webhook 任务页面。结果以查询接口、实际生成文件、下载队列和日志为准。

## 请求类型和文件定位

建议明确填写 `action`，方便外部程序和日志对应。

| `action` | 适合什么情况 | 主要字段 |
| --- | --- | --- |
| `file` | 已知单个视频已经到达网盘 | `path` + `file_name` |
| `batch_files` | 已知一批视频已经到达网盘 | 非空的 `items` 数组，每项填写文件定位信息 |
| `directory_scan` | 需要处理一个目录及其子目录中的视频 | `directory_path` |

`file` 只用于实际视频文件；目录请使用 `directory_scan`，不要把目录 ID 填进 `file_id`。

### 可选的同步目录 ID

如果自动匹配不明确，在请求顶层添加 `sync_path_id`，值是「STRM 同步目录」卡片左上角 **`#` 后面的同步目录编号**，不是悬浮提示里的网盘目录 ID。它必须指向一个 115 同步目录，文件也必须确实位于该目录范围内。

自动匹配遵循以下规则：

- 优先选择覆盖该路径、且来源路径最具体的同步目录。例如 `/电影/新片` 比 `/电影` 更具体。
- 没有匹配目录，或同样具体的目录有多个时，请求会失败，需要补建目录或明确指定 `sync_path_id`。
- 不同 115 账号可能存在相同路径；使用多个账号时，建议明确指定目录 ID，确认处理的是预期账号中的文件。
- 批量自动匹配时，全部项目必须归属同一个同步目录；跨目录的文件应拆开请求。

### 已有 115 文件 ID 时

| 字段 | 怎么用 |
| --- | --- |
| `file_id` | 115 文件 ID。指定 `sync_path_id` 后，可以只用它定位文件；没有 `sync_path_id` 时仍须提供 `path` + `file_name` |
| `pick_code` | 辅助信息，不能单独用来定位文件 |
| `parent_id` | 远端父目录 ID，可省略 |
| `file_size` | 文件大小，单位为字节，可省略 |
| `sha1` | 文件 SHA-1，可省略 |
| `mtime` | 远端修改时间的 Unix 时间戳，单位为秒，可省略 |

QMediaSync 会查询真实远端详情，并以查询结果为准。通常只需提供可靠的路径和文件名，或同步目录 ID 加文件 ID，不必自己拼出全部字段。

目录扫描也可使用 `directory_id`：只有同时提供 `sync_path_id` 时，才能仅凭目录 ID 定位；没有 `sync_path_id` 时必须填写 `directory_path`。同时填写目录 ID 和路径时，两者必须指向同一目录。

## 批量文件和目录扫描示例

沿用前面相同的请求地址、请求头，把 JSON 请求体替换成下面的内容即可。

批量处理两个视频：

```json
{
  "action": "batch_files",
  "download_meta": true,
  "refresh_emby": true,
  "items": [
    {
      "path": "/电视剧/示例剧/Season 1",
      "file_name": "示例剧 - S01E01.mkv"
    },
    {
      "path": "/电视剧/示例剧/Season 1",
      "file_name": "示例剧 - S01E02.mkv"
    }
  ]
}
```

递归扫描整个目录：

```json
{
  "action": "directory_scan",
  "directory_path": "/电视剧/示例剧/Season 1",
  "download_meta": true,
  "refresh_emby": true
}
```

这两个例子需要已有同步目录覆盖 `/电视剧` 或更具体的对应路径。目录扫描请求会先接收一个任务，之后才在后台展开目录，不能把 HTTP 请求返回的速度当成扫描速度。

批量请求中，`download_meta` 和 `refresh_emby` **只能放在最外层**，统一作用于整批文件。放进任意一个 `items[]` 项都会使整批请求返回 HTTP `400`。

批量响应的 `results` 按原始顺序给出逐项结果，`index` 从 0 开始。HTTP `200` 仍可能伴随 `failed_count > 0`，甚至 `accepted_count=0`；必须检查接收数量和每项的 `accepted`、`error`，不能仅凭 HTTP `200` 判定全部成功。`task_ids` 返回的是已接收的文件任务 ID，不包含批量父任务 ID。

## 两个后续处理开关

`download_meta` 和 `refresh_emby` 都是 JSON 中的布尔值，**不填写时默认为 `false`**。前面的最小请求只生成 STRM；需要后续操作时显式加上对应的 `true`。

### `download_meta`：补充同名元数据

开启后，程序只为本次视频补充本地缺少的同名元数据。例如视频为 `示例电影.mkv` 时，会按当前元数据扩展名配置匹配：

```text
示例电影.nfo
示例电影.srt
示例电影-thumb.jpg
```

匹配的是“视频基本名 + 元数据扩展名”或“视频基本名 + `-thumb` + 元数据扩展名”。不会顺便下载目录里其他名称的 `poster.jpg`、`fanart.jpg`、季图片或全局 NFO，也不会删除本地多余元数据或上传本地元数据。

元数据扩展名沿用对应同步目录设置，下载任务进入「上传下载 → 下载队列」。下载完成可能晚于 STRM 任务完成，队列说明见 [任务与通知](任务与通知.md)。

### `refresh_emby`：提交 Emby 刷新

开启后，只有 STRM 实际变化或新增元数据下载任务时，才安排 Emby 刷新。还需要先配置并启用 Emby 联动，让该同步目录关联到媒体库，具体见 [Emby 媒体库同步](Emby-媒体库同步.md)。

刷新请求会合并和等待，不是 HTTP 请求返回后立即要求 Emby 全库扫描。批量文件或目录扫描要等所有文件完成或跳过后统一提交；有文件任务失败时，不提交这次批量刷新。

## 查询最终处理结果

把 `id` 替换为创建响应中返回的任务 ID：

```bash
curl 'http://qms.example:12333/api/strm/tasks?id=123' \
  --header 'X-API-Key: YOUR_QMS_API_KEY'
```

响应中的 `data.items` 是结果列表，`data.total` 是符合条件的总数。主要查看 `status`：

| `status` | 含义 |
| --- | --- |
| `pending` | 等待处理 |
| `running` | 正在生成或扫描 |
| `waiting_children` | 等待批量或目录中的文件处理完 |
| `finalizing` | 正在完成后续收尾，尚未结束 |
| `completed` | 本任务处理完成；元数据下载或 Emby 刷新仍可能在各自队列中继续 |
| `skipped` | 因过滤等规则跳过，查看 `skip_reason` |
| `failed` | 处理失败，结合任务 ID 和应用日志排查 |
| `cancelled` | 已取消 |

查询返回的 `parent_task_id` 可以用来继续检查整批结果：

- 查询父任务：`GET /api/strm/tasks?id=父任务ID`。
- 查询父任务下的文件：`GET /api/strm/tasks?parent_task_id=父任务ID`。
- 按同步目录查看顶层任务：`GET /api/strm/tasks?sync_path_id=同步目录ID`。

列表可加 `page` 和 `page_size`，每页最多 100 项；多个筛选条件同时填写时需要全部满足。

父任务的 `accepted_items` 是已处理完成的子项数量，创建接口的 `accepted_count` 是已接收数量，两者含义不同。`failed_items`、`skipped_items` 分别记录失败和跳过；旧记录中的 `null` 表示未记录，不是 0。全跳过的父任务显示 `skipped`，完成与跳过混合时显示 `completed`，存在真实失败时显示 `failed`。

查询不会返回原始错误详情；失败时请根据任务 ID 查看应用日志，不要只反复查询等待它自行成功。

## 重复通知和失败重试

相同请求仍在等待、执行、等待子项或收尾时，再次发送会复用已有活动任务，避免重复入队。

已完成、失败或取消的任务，再次提交相同请求会创建新任务。因此外部程序收到成功响应后，应保存任务 ID 并查询结果，不要持续重复发送作为查询方式。

任务进入 `failed` 后，先修正文件路径、授权、过滤或本地权限等具体问题，再重新提交。处于 `finalizing` 的收尾问题由后台继续尝试，应先观察日志。请求中的后续处理开关也影响请求是否相同，修改开关后不能假定还会拿到原任务 ID。

## 常见错误

| 现象 | 检查方法 |
| --- | --- |
| HTTP `401` | 检查是否传入 QMediaSync API Key、密钥是否正确且已启用。只登录浏览器不能替代此接口的 API Key |
| HTTP `400` | 查看响应中的 `message`：常见原因是字段不完整、目录匹配歧义、文件超出同步范围、使用了 `local_path`，或把后续处理开关放进批量项 |
| HTTP `200` 但有项目未接收 | 检查 `accepted_count`、`failed_count` 和逐项 `results`，只修正失败项 |
| 已接收但没有生成文件 | 查询任务状态，确认最小视频大小、扩展名和名称排除规则；再检查网盘授权、本地目录权限和日志 |
| 已生成 STRM，但没有字幕或图片 | 确认 `download_meta=true`、文件名符合匹配规则、扩展名已配置，并查看下载队列 |
| STRM 已变化，但 Emby 没更新 | 确认 `refresh_emby=true`，检查 Emby 联动、媒体库关联和批量中是否有失败文件 |

判断创建请求是否失败时，以 **HTTP 状态和 `message`** 为准。当前错误响应的 JSON `code` 为 `500`，而 HTTP 状态是 `400` 或 `401`，不要把 JSON `code` 当作 HTTP 状态。

## 与其他 Webhook 的区别

| 功能 | 消息方向 | 用途 |
| --- | --- | --- |
| 本页的 STRM Webhook | 外部程序 → QMediaSync | 通知指定的 115 文件或目录需要生成 STRM |
| [Emby 通知配置](Emby-通知配置.md) | Emby → QMediaSync | 接收媒体添加、移除、播放等事件 |
| [通知管理](任务与通知.md)中的自定义 Webhook | QMediaSync → 接收服务 | 向用户或其他服务发送通知 |

这三类配置的地址、请求字段和用途不同，不能互相替代。
