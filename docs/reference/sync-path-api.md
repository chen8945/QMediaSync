# 同步目录聚合 API

> 职责：定义同步目录及其目录监控上传最终规则集合的创建、更新、幂等、错误和响应契约。
>
> 权威范围：本文档维护 `POST /api/sync/paths` 与 `PUT /api/sync/paths/:id` 的 HTTP 契约；同步、上传和 watcher 的运行时行为见 [STRM 同步调度与任务记录](../architecture/sync-orchestration.md) 和 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。
>
> 修改时机：修改聚合路由、请求字段、默认值、验证、幂等、响应、错误码或事务边界时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/sync_config.go`、`backend/internal/requests/sync.go`、`backend/internal/syncconfig/`、`backend/internal/models/syncpath.go`、`backend/internal/models/directory_upload.go`、`backend/internal/controllers/sync_directory_upload_test.go`。

同步目录基础配置、目录监控上传总开关和最终规则集合通过同一个聚合接口保存。旧的同步目录创建 / 更新和规则独立写接口不再提供。

## 入口与鉴权

| 动作 | 方法与路径 | 说明 |
| --- | --- | --- |
| 创建 | `POST /api/sync/paths` | 创建同步目录和最终规则集合。 |
| 更新 | `PUT /api/sync/paths/:id` | 更新既有同步目录和最终规则集合；`id` 必须是正整数。 |

接口位于受保护的 `/api` 路由组。浏览器 Cookie 会话需同时通过 CSRF 校验；API Key 调用可使用 `X-API-Key` 或 `api_key` 查询参数。认证边界见 [认证与浏览器会话](../architecture/authentication-sessions.md)。

创建可选携带 `Idempotency-Key` 请求头；更新不会使用该头。

## 请求体

请求体必须是 JSON 对象，包含 `sync_path`，可选包含 `directory_upload`：

```json
{
  "sync_path": {
    "source_type": "115",
    "account_id": 12,
    "base_cid": "0",
    "local_path": "/media/strm",
    "remote_path": "/Media",
    "enable_cron": true,
    "custom_config": false
  },
  "directory_upload": {
    "enabled": true,
    "rules": [
      {
        "client_id": "new-rule-1",
        "enabled": true,
        "monitor_path": "/media/incoming",
        "remote_root_path": "/Media/Incoming",
        "remote_root_id": "123456",
        "recursive": true,
        "watch_mode": "auto",
        "startup_scan_enabled": true,
        "processed_cache_ttl_seconds": 600,
        "overwrite_mode": "skip_same"
      }
    ]
  }
}
```

### `sync_path`

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `source_type` | 是 | 仅允许 `115`、`local`、`123`、`openlist`、`baidupan`。 |
| `account_id` | 非本地来源必填 | 必须指向同一 `source_type` 的已有账号；本地来源不需要。 |
| `base_cid` | 是 | 同步源根目录 ID，不能为空。 |
| `local_path` | 是 | STRM / 元数据输出根目录，不能为空。 |
| `remote_path` | 是 | 同步源路径，不能为空。非本地来源会统一分隔符、移除前导 `/` 并清理路径。 |
| `enable_cron` | 否 | 是否启用定时同步，省略为 `false`。 |
| `custom_config` | 否 | 是否使用同步目录自定义 STRM 配置，省略为 `false`。 |
| `setting` | `custom_config=true` 时使用 | 嵌套 STRM 配置；非零嵌套配置优先于兼容的顶层同名字段。 |
| `directory_upload_enabled` | 否，不应使用 | 聚合接口忽略该字段，最终总开关只由 `directory_upload.enabled` 决定。 |

更新时，路径参数 `id` 是唯一的记录标识；请求中的 `source_type` 和 `account_id` 必须与既有同步目录相同，不能借更新接口改写同步来源或账号。

`custom_config=true` 时，`setting`（或兼容的顶层 STRM 字段）使用以下约束：

| 字段 | 允许值或约束 |
| --- | --- |
| `local_proxy` | `-1` 继承全局、`0` 关闭、`1` 开启。 |
| `strm_base_url` | 空值或合法 `http` / `https` URL。 |
| `cron` | 空值或标准 5 段 Cron / 项目支持的描述符。 |
| `min_video_size` | 不小于 `-1`，单位字节。 |
| `video_ext_arr`、`meta_ext_arr` | 空数组或以 `.` 开头、不含空白字符的扩展名数组。 |
| `exclude_name_arr` | 完整匹配的排除名称数组，不区分大小写。 |
| `exclude_name_regex_arr` | 正则排除名称数组，每项必须是非空且能由 Go `regexp` 编译的原始表达式；不转小写或裁剪，错误包含字段和规则序号。 |
| `upload_meta` | `-1`、`0`、`1`、`2`。 |
| `download_meta`、`delete_dir`、`check_meta_mtime` | `-1`、`0`、`1`。 |
| `add_path` | `-1`、`1`、`2`、`3`。 |

`custom_config=false` 时，服务端存储继承全局配置的默认值；调用方不应依赖传入的自定义字段被保留。

两类排除列表分别继承：自定义列表非空时覆盖相应全局列表，为空时继承相应全局列表；精确名称与正则命中任意一项都会排除。名称、大小写与父目录语义见 [STRM 名称排除](../operations/configuration.md#strm-名称排除)。

### `directory_upload`

`directory_upload` 是 115 同步目录的最终规则集合。字段存在时，`enabled` 和 `rules` 都必须出现；`rules` 可以是空数组。每次保存以提交的数组为准：未提交的既有规则会被删除。

若 115 同步目录省略或传入 `null`，服务端会按 `enabled=false`、空规则集合保存，因此更新调用方若要保留既有目录监控配置，必须先读取并随请求回传完整规则集合。非 115 来源只能省略该对象，或提交 `enabled=false` 且空规则数组。

| 规则字段 | 必填 | 规则 / 默认值 |
| --- | --- | --- |
| `client_id` | 否 | 仅用于将错误定位到前端暂存规则，不会持久化。 |
| `id` | 否 | 大于 `0` 时必须属于当前同步目录；省略或 `0` 表示新建。 |
| `enabled` | 否 | 单条规则开关，默认 `true`。总开关开启时至少要有一条启用规则。 |
| `monitor_path` | 是 | 本地监控目录，不能为空。 |
| `remote_root_path` / `remote_root_id` | 是 | 远端根路径和目录 ID 都不能为空，且必须位于同步目录远端路径下。 |
| `recursive` | 否 | 是否递归监控，默认 `true`。 |
| `upload_metadata` | 否 | 是否上传当前同步目录识别的元数据扩展名，默认 `false`。 |
| `watch_mode` | 否 | `auto`、`fsnotify` 或 `polling`，默认 `auto`。 |
| `startup_scan_enabled` | 否 | 是否在启动时补偿扫描，默认 `true`。 |
| `processed_cache_ttl_seconds` | 否 | 内存去重 TTL，非正值时使用 `600` 秒。 |
| `delete_source_after_success` | 否 | 上传和 STRM 生成成功后是否删除源文件，默认 `false`。 |
| `ignore_patterns` | 否 | 忽略规则数组。 |
| `overwrite_mode` | 否 | `skip_same`、`fail_conflict` 或 `replace_conflict`，默认 `skip_same`。 |

规则中的 `sync_path_id` 与 `account_id` 即使传入也不会用于写入；服务端从 `sync_path` 派生归属。完全相同的 `monitor_path + remote_root_path + remote_root_id` 会被拒绝；两个启用规则的监控目录不能重复或在递归监控时互相嵌套。

## 原子性与幂等

基础配置、目录监控总开关和最终规则集合在一个数据库事务中校验和保存。任一规则校验、账号校验或数据库写入失败时，整个聚合回滚。

创建请求的 `Idempotency-Key` 会先去除首尾空白并保存 SHA-256 摘要到 `sync_path_idempotency_records`：

- 相同 key 已完成时，返回原先创建的聚合，不创建新同步目录。
- 相同 key 仍在处理时，返回 HTTP `409` 和 `IDEMPOTENCY_CONFLICT`。
- 原始 key 不入库，也不参与更新请求的幂等处理。

事务提交后，服务端会创建本地目录、重载同步 Cron 和目录监控服务。任何一项后续操作失败都不会回滚已保存配置，而会写入响应 `warnings`。

## 响应与错误

成功时返回 HTTP `200`：

```json
{
  "code": 200,
  "message": "添加同步目录成功",
  "data": {
    "sync_path": {},
    "directory_upload": {
      "enabled": true,
      "rules": []
    },
    "warnings": []
  }
}
```

更新成功时 `message` 为「保存同步目录成功」。`warnings` 说明事务已成功、但本地目录创建或运行态服务重载未完成，调用方应提示并结合日志排查。

错误响应同样使用 `APIResponse` 包络。当前 JSON `code` 固定为 `500`，必须以 HTTP 状态、`data.error_code` 和 `data.field_errors` 判断失败原因：

| HTTP 状态 | `error_code` | 说明 |
| --- | --- | --- |
| `400` | `INVALID_REQUEST` | 请求格式、基础字段、来源不可变或枚举校验失败。 |
| `400` | `ACCOUNT_SOURCE_INVALID` | 账号不存在，或账号类型与同步来源不一致。 |
| `400` | `DIRECTORY_UPLOAD_RULE_OWNERSHIP` | 提交的既有规则不属于当前同步目录。 |
| `400` | `DIRECTORY_UPLOAD_RULE_BOUNDARY` | 目录规则超出同步目录边界或缺少必要根目录信息。 |
| `400` | `DIRECTORY_UPLOAD_RULE_CONFLICT` | 规则重复、重叠或总开关启用但没有启用规则。 |
| `404` | `SYNC_PATH_NOT_FOUND` | 更新目标不存在。 |
| `409` | `IDEMPOTENCY_CONFLICT` | 相同创建幂等键仍在处理。 |
| `500` | `DATABASE_SAVE_FAILED` | 数据库或未分类保存错误。 |

`field_errors` 是数组，每项包含 `field`、`message`，规则错误可额外包含暂存用 `client_id`。基础字段错误没有 `client_id`。

## 不变量

- 创建和更新只能通过聚合接口保存；基础配置与目录监控规则不得分开写入。
- 更新不得改写同步来源或账号；规则归属只由当前同步目录派生。
- 提交的规则数组是最终集合，不得把未成功加载规则误传为空数组。
- 只有 115 同步目录可以启用目录监控上传；总开关和至少一条规则自身开关必须同时开启才会运行。
- 创建幂等只保存摘要并只保护 `POST /api/sync/paths`；调用方超时重试必须复用同一个 `Idempotency-Key`。

## 验证方式

- 修改接口或聚合保存逻辑后，运行 `(cd backend && go test ./internal/controllers/ -run 'Test(Create|Update)SyncPathAggregate')`。
- 修改事务、幂等或规则归属后，运行 `(cd backend && go test ./internal/syncconfig/)` 与 `(cd backend && go test ./internal/models/ -run TestMigrateVersion59AddsSyncPathIdempotencyTableAndEmbyTaskKey)`。
- 修改前端表单或字段定位后，运行 `(cd frontend && pnpm run type-check)`。
