# 请求校验约定

> 职责：定义 HTTP Request DTO、通用校验、路径参数与控制器校验边界。
>
> 权威范围：本文档维护输入校验和控制器边界；接口字段和 Webhook 行为以 [STRM Webhook](../reference/strm-webhook.md) 等专题参考为准。
>
> 修改时机：修改 Request DTO、`Validate()` 规则、绑定方式、路径参数约定或前后端校验边界时必须更新本文档。
>
> 相关代码：`backend/internal/requests/`、`backend/internal/validation/`、`backend/internal/controllers/`、`frontend/src/constants/validation.ts`。

本文档记录 QMediaSync 后端请求校验体系的当前实现和后续约定。已迁移的 HTTP 接口使用 `backend/internal/requests` 下的 Request DTO 绑定请求，复用 `backend/internal/validation` 下的通用规则；未迁移或特殊流程接口以实际代码为准。

## 适用边界

- DTO 面向 HTTP 边界：JSON Body、Query/Form 参数，以及需要从控制器传入统一校验结构的请求参数。
- 纯内部 Go 函数、Service 调用、模型方法和后台任务不为了形式统一额外创建 DTO。
- 路径参数仍允许在控制器中就地解析，例如 `c.Param("id")` 后转换为整数；需要复用时可迁移到通用 ID DTO 或独立辅助函数。
- 数据库存在性、账号归属、权限、任务状态、外部服务连通性等依赖运行时状态的校验保留在控制器或业务层。
- 控制器响应风格保持所在模块既有模式，不因为 DTO 迁移统一改 HTTP 状态码或响应结构。

## 响应与错误分类

`APIResponse[T]` 保留 `code`、`message`、`data`，并通过可选顶层 `error_code` 提供稳定的机器错误码；未设置时省略该字段，成功响应不携带错误码。三个状态来源各有职责：

- HTTP 状态表示请求层面的结果，沿用接口已有的 `200`、`400`、`401`、`403` 等状态。
- 数值 `code` 不代替 HTTP 认证状态；仅有响应体 `code=401` 不触发会话失效。它表示历史业务结果：常规接口 `200` 为成功，`500` 为业务失败，不能将响应体的 `500` 当作 HTTP `500`。通知管理接口保留 `code=0` 成功、`code=1` 失败的独立历史契约，前端仅在该领域 API 中识别。
- 字符串 `error_code` 提供可识别的原因；缺失或未知时，客户端根据 HTTP 状态和业务结果回退，不依赖任意文案猜测。

认证与来源校验错误码由 [认证与浏览器会话](../architecture/authentication-sessions.md#请求错误码) 定义。错误码只表达已知原因，不暴露凭据匹配细节，也不为了分类改变既有认证策略。

普通业务失败的非空 `message` 会直接展示给用户。拼接第三方错误时复用 `helpers.RedactSensitiveLog` 清理凭据；对 URL 路径或无字段名的密钥回显同时传入已知密钥。客户端只改写需要本地化的字段文案，空消息或程序异常使用操作级回退说明。登录凭据失败仍统一返回“登录失败”，诊断日志不输出原始响应或请求载荷。

同步目录聚合接口暂时保留 `data.error_code`、`data.field_errors` 和成功响应中的 `warnings`，精确语义见 [同步目录聚合 API](../reference/sync-path-api.md)。这属于存量响应兼容，不是另一套永久的错误码层级；客户端解析时顶层错误码优先，已知旧接口可回退到嵌套字段。不得将其所有错误都当作字段校验失败。

异步备份和恢复的状态查询成功不等于任务成功，任务结果由 `data.status` 判定；任务内的 `data.error_code` 表示异步失败原因，`data.restore_outcome` 表示已确认的数据库结果，不能与查询接口的顶层错误码混淆。字段和旧响应兼容见 [备份和恢复状态](../operations/database.md#备份和恢复状态)。

网盘文件操作同样需要区分调用成功与操作完成。`POST /api/path/move` 和 `POST /api/path/copy` 的成功响应返回 `data.status`（`completed` 或 `submitted`）和 `data.task_ids`（字符串数组）：同步完成时为 `completed` 和空数组；OpenList 返回后台任务时为 `submitted`，保留上游任务 ID。`submitted` 只表示已提交，最终成功或失败在 OpenList 任务列表查看，不能依据本次响应或紧随其后的文件列表刷新宣称完成。前端兼容旧版本的 `data=null`，沿用旧版同步成功语义。

文件管理的批量删除、移动、复制和单项重命名在返回上游错误前统一脱敏，同时清理账号已知的 Token、Refresh Token 和密码回显，覆盖连接失败或超时错误中的请求 URL；保留原有 HTTP 状态、业务码和操作失败前缀。

115 文件管理的删除、移动、复制和重命名控制器同时检查客户端返回的错误与成功标记；错误为空但成功标记为 `false` 时仍返回业务失败。这是控制器边界的防御性校验，不改变 115 客户端原有的 `(bool, error)` 返回契约。

百度文件管理响应同时检查顶层 `errno` 和 `info[].errno`：顶层错误优先；顶层成功但任一条目失败时仍返回业务失败，并沿用既有错误码文案和凭据错误分类。该规则覆盖删除、移动、复制和重命名的单项及批量客户端入口，不改变公开方法签名；没有逐项结果的旧响应保留顶层成功语义，其他百度接口的 `info` 不按文件管理结果解析。

文件管理的路径型 ID（百度网盘、OpenList）保留文件名中的首尾空白，源目录、目标目录和缓存键遵循相同规则；批量列表只对原文相同的 ID 去重。115 数字 ID 单独裁剪空白。OpenList 删除、移动、复制和重命名要求所有完整文件路径属于同一父目录；提供 `parent_id` 时必须与实际父目录一致，省略时从首项推导。不一致的请求在访问上游前整批拒绝，不按首项目录重组其他文件路径。

OpenList 文件管理重命名使用 `/api/fs/rename` 并显式传 `overwrite=false`；接口不支持时返回失败，不回退到批量重命名。刮削继续使用原有批量接口。上游同名保护采用先检查后重命名，不能保证跨客户端并发下原子地不覆盖；该保证需要上游存储驱动提供。

OpenList 移动和复制关闭普通错误重试：网络错误、超时或响应丢失时直接返回失败，避免已受理的写入被重复执行；明确的凭据过期响应仍允许一次认证恢复并重新提交。文件管理与刮削使用的移动 / 复制入口共用该策略。

批量删除、移动、复制和单个重命名一旦实际调用上游，即使返回错误也失效受影响缓存，因为操作可能部分生效或响应丢失。路径型来源根据完整 ID 清理实际父目录列表和受影响子树：删除清理源路径，移动和重命名清理源、目标路径，复制清理目标路径；115 移动和重命名保留稳定数字 ID 对应的子目录缓存，仅刷新相关父目录。缓存代次阻止失效前的在途读取重新写回。OpenList 异步任务提交时的失效不代表任务终态，任务完成后仍须强制刷新所查看的目录。

电影和电视剧分类的创建、更新必须向控制器返回实际数据库写入结果。写入失败时沿用 HTTP `200`、业务 `code=500` 和 `data=null`，不能返回“已保存”；只有写入成功才返回业务 `code=200`。模型保存方法不得忽略 GORM 的 `Error` 后固定返回成功。

## 控制器流程

JSON Body 和 Query/Form 参数优先使用 DTO 绑定：

```go
var req requests.UpdateStrmConfigRequest
if err := c.ShouldBind(&req); err != nil {
    // 按所在控制器的既有响应风格返回参数错误
    return
}
if err := req.Validate(); err != nil {
    // 按所在控制器的既有响应风格返回校验错误
    return
}
input := req.ToModel()
```

DTO 负责：

- 字段必填、范围、枚举、格式和简单条件规则。
- 将外部字段转换为模型输入，例如 `ToModel()`、`StrmSettingModel()`、`NormalizedIDs()`。
- 与请求兼容性相关的规范化，例如 OpenList URL 自动补全协议、分页默认值、旧字段映射。

当校验规则内部做了规范化（例如 `TrimSpace`）时，DTO 必须暴露对应的 `NormalizedXxx()` 方法，控制器落库和调用外部依赖只使用该方法的返回值。直接使用原始字段会让「校验通过但实际取值非法」成为可能，例如 `NormalizedHTTPProxy()` 之于带首尾空白的代理地址。

控制器仍负责：

- 调用 `ShouldBind`、`ShouldBindJSON` 或 `ShouldBindQuery`。
- 调用 DTO 的 `Validate()` 或场景化方法，例如 `ValidateSave()`、`ValidateTest()`、`ValidateCreate()`、`ValidateUpdate()`。
- 数据库查询、账号类型匹配、权限检查、任务状态检查和外部请求。
- 保持模块既有错误响应。当前代码中同时存在 `APIResponse`、`gin.H`、HTTP 200 业务错误、HTTP 400/401/403/404/409 等模式。

模型层仍负责：

- 持久化。
- 派生字段写入。
- 与数据库结构强相关的转换。

## 通用规则

`backend/internal/validation` 只放跨模块复用规则，不访问数据库，不依赖 `gin.Context`，不处理具体业务流程。

| 函数 | 规则 |
| --- | --- |
| `NonBlank` | 去除首尾空白后不能为空。 |
| `Length` | 按 rune 计数字符串长度，并拒绝控制字符。 |
| `PositiveID` | `uint` ID 必须大于 0。 |
| `RangeInt` / `RangeInt64` | 整数必须落在闭区间内。 |
| `OneOfInt` / `OneOfString` | 值必须属于显式枚举。 |
| `HTTPURL` | 可配置是否允许空值；非空时必须是 `http` 或 `https` URL，并包含 Host，显式端口须落在 1-65535。 |
| `ProxyURL` | 出站代理 URL 校验，允许 `http`、`https`、`socks5` 或 `socks5h`；必须包含 Host，显式端口须落在 1-65535。协议白名单由 `ProxySchemeSupported` 和 `ProxySchemeHint` 对外暴露，`helpers` 的传输层复用同一份，不得各自维护。 |
| `DownloadProxyURL` | 网盘下载反代 URL 校验，只允许 `115cdn.net`、其子域名、`d.pcs.baidu.com`、`baidupcs.com` 及其子域名，显式端口须落在 1-65535。 |
| `PortInRange` | 校验已解析 URL 的显式端口落在 1-65535，供上面三个 URL 规则共用。 |
| `Cron` | 使用 `robfig/cron/v3` 的标准 5 段 Cron 解析。 |
| `ExtList` | 扩展名数组可配置是否允许空；非空项必须以 `.` 开头，且不能包含空白字符。 |
| `RegexList` | 数组允许为空；每项不能为空且必须能由 Go `regexp` 编译，保留原文，错误字段带数组下标。 |

通用错误使用 `validation.Error`，错误文本格式为 `字段：原因`。新增规则时应同时补充 `backend/internal/validation` 的 table-driven 测试。

### URL 校验边界

三个 URL 规则共用同一套判断顺序：先看 `url.Parse` 是否成功且 Host 段非空，再看协议白名单，最后用 `PortInRange` 校验显式端口。

- 空主机名合法。形如 `http://:1080`、`http://:8096` 的地址 `Hostname()` 为空但 `Host` 非空，Go 会按本机处理，实测经这类代理出站可以拿到 200 响应；这是本地代理和本机服务的常用简写，前后端都必须放行，不得改回要求主机名非空。
- Host 段为空才是非法。`socks5://`、`http:///path`、`socks5://user:pass@` 没有拨号目标；漏写 `//` 的 `localhost:1080`、`proxy.example.com:8080` 会被 `url.Parse` 当作协议或 opaque，Host 同样为空，一并按格式错误拒绝。
- 端口范围必须显式校验。`url.Parse` 只保证端口是数字，`:0` 和 `:99999` 都能解析通过，因此 `HTTPURL`、`ProxyURL`、`DownloadProxyURL` 都调用 `PortInRange`；不写端口时按协议默认端口处理，不做检查。
- 端口越界返回端口专属提示而不是通用格式错误，避免用户照着「格式无效」去改协议。

`ProxyURL` 的协议白名单是唯一来源，`helpers` 传输层通过 `ProxySchemeSupported` 和 `ProxySchemeHint` 复用；`TestProxySchemeSupported` 和 `TestProxySchemeHintCoversWhitelist` 钉住白名单与提示文案的同步。

`helpers.createProxyTransport` 不止复用白名单，而是整体调用 `validation.ProxyURL`，一次拿到协议、Host 和端口范围三层校验与统一中文文案。构造出站传输是唯一入口，`TestHttpProxyWithContext` 和 `TestHttpProxyAdvancedWithContext` 也走它，因此「测试代理」按钮与真实出站用同一套校验和同一份传输配置。传输层不得自行拼装代理地址校验，否则 `socks5://h:99999` 这类地址会被请求层拒绝、被传输层放过，直到拨号阶段才报底层错误。

### Cron 表达式边界

项目使用 `github.com/robfig/cron/v3`，后端通过 `cron.ParseStandard` 校验表达式。

当前支持：

- 标准 5 位 Cron：`分 时 日 月 周`。
- 常用示例：`0 * * * *`、`0 2 * * *`、`*/10 * * * *`。
- robfig 描述符：`@hourly`、`@daily`、`@midnight`、`@weekly`、`@monthly`、`@yearly`、`@annually`、`@every 1h30m`。

当前不支持：

- 6 位秒级 Cron，例如 `0 0 2 * * *`。
- Quartz 表达式，例如 `?`、`L`、`W`、`#`。

Emby 条目同步默认 Cron 为 `0 * * * *`，含义是每小时整点执行一次。调整默认值时必须同时检查后端默认配置、前端 `CRON_DEFAULTS`、表单文案和校验测试。

## 当前 DTO 覆盖范围

| 文件 | 覆盖接口类型 | 主要校验 |
| --- | --- | --- |
| `requests/settings.go` | 线程配置、全局 STRM 配置 | 线程范围、页面大小范围、115 URL 有效性检查开关和 1 到 9 秒总超时、STRM Base URL、Cron、扩展名、STRM 开关枚举。 |
| `requests/sync.go` | 同步路径创建和更新、自定义 STRM 配置 | 来源类型、非本地来源账号 ID、路径必填、自定义配置、继承值 `-1`、远程路径规范化。 |
| `requests/scrape_path.go` | 刮削路径保存 | 创建和更新分场景校验；更新时使用旧记录补齐不可编辑的来源类型、账号和媒体类型；刮削类型、整理方式、源路径、按场景要求的目标路径、扩展名、最小文件大小、线程上限、Cron；文件夹和文件命名模板拒绝绝对路径、反斜杠和 `..` 片段。 |
| `requests/scrape_settings.go` | TMDB、AI、分类和 TMDB 搜索 | URL、语言代码、国家代码、AI 动作枚举、模型名长度、超时范围、分类名称必须是单层目录名、Genre ID、年份范围。 |
| `requests/accounts.go` | 账号、账号授权更换/取消、OpenList 账号、API Key | 账号来源类型、名称长度、115 授权来源组合、更换授权的确认标志和来源字段、授权会话绑定、OpenList URL 规范化、用户名/密码或 Token、API Key 状态。 |
| `requests/connections.go` | HTTP 代理、OAuth、二维码、远程直链、反代、请求队列限制和统计 | 代理 URL、`preserve_proxy_credentials` 的显式凭据保留意图、账号 ID、OAuth 回调 URL、`authorization_id` 长度、`data`/`payload` 条件必填、二维码 UID、PickCode、反代下载域名白名单、QPS/QPM/QPH、统计窗口和清理天数。 |
| `requests/emby.go` | Emby 配置 | Emby URL、同步 Cron、布尔开关枚举、媒体库 JSON 字符串。 |
| `requests/backup.go` | 备份创建、列表、记录 ID、恢复和配置 | 手动备份原因默认值、分页默认值、备份记录 ID、启用开关、Cron、保留天数、最大备份数、压缩开关。 |
| `requests/notification.go` | Telegram、MeoW、Bark、ServerChan、自定义 Webhook 渠道 | 渠道名称、必填凭据、URL、Webhook 方法、格式、认证方式和模板格式。 |
| `requests/users.go` | 登录、启用/关闭两步验证、当前用户用户名/密码修改 | 登录校验用户名和密码非空，用户名 20 个字符上限；创建和修改使用严格用户名 / 密码规则，用户名去除首尾空白后长度为 3 到 20 个字符且只能包含英文和数字，密码长度至少 6 个字符且不能是纯数字或纯字母；两步验证码必填。 |
| `requests/operations.go` | 分页、ID、路径浏览、网盘文件、目录操作、网盘文件批量删除 / 移动 / 复制 / 重命名、队列、同步/刮削关联、日志、临时图片、版本更新 | 分页默认值和范围、HTTP path 正 ID、ID 列表、CSV ID、来源类型、文件夹名、文件条目名、路径穿越防护、日志文件名限制、版本号格式、日期范围；批量文件操作要求 `file_ids` 非空、去重、不含无效 ID 且单次最多 500 项，移动 / 复制另要求 `target_parent_id` 必填，重命名要求 `file_id` 非空且新名称为合法文件条目名。 |

首次数据库配置服务是启动期流程，不纳入公共 `backend/internal/requests` 目录；它只提供 SQLite 和PostgreSQL 配置，旧库迁移服务已移除。

## 重要兼容性规则

- `SyncPathRequest` 同时支持计划中的嵌套 `setting` 字段和旧前端使用的顶层 STRM 字段；存在非零嵌套配置时优先使用 `setting`。
- 同步路径自定义 STRM 配置使用 `-1` 表示继承全局值；全局 STRM 配置不接受 `-1`。
- 全局 `/api/setting/strm-config` 的 `multi_playback_enabled` 只接受整数 `0/1`，省略时按现有整表单保存语义取 `0`。它直接保存到 `Settings`，不属于同步目录可继承字段；与其他 STRM 字段同次落库，失败不替换运行时配置。本地代理开启时前端仅禁用编辑，仍提交保留值；实际播放请求的智能跳过见 [115 多端播放](../operations/configuration.md#115-多端播放)。
- 全局 STRM 和目录自定义配置的 `video_ext_arr`、`meta_ext_arr` 均允许空数组；非空项仍须以 `.` 开头且不含空白。全局接口中字段省略或为 `null` 同样按空列表处理，统一存储为 `[]`。全局空列表回退配置默认扩展名，目录空列表继承全局，详见 [STRM 列表与继承](../operations/configuration.md#strm-列表与继承)。
- 全局 STRM 和同步目录自定义配置的 `exclude_name_regex_arr` 在现有保存接口中逐项用 Go `regexp` 校验；空规则、非法语法及 Go 不支持的表达式会被拒绝，并指出字段和规则序号。空数组合法。正则原文直接传入模型，不套用原名称列表的转小写，也不裁剪首尾空格；仅包含正则字段的嵌套 `setting` 同样优先于顶层兼容字段。
- `SaveScrapePathRequest` 新增请求要求提供来源类型、账号和媒体类型；更新请求沿用旧记录中的这些不可编辑字段，避免旧前端编辑请求被误拒。
- 刮削路径 `only_scrape` 模式不要求目标路径，整理方式只能为 `same`；需要整理或重命名时才要求目标路径。
- 刮削路径本地来源支持移动、复制、软链接和硬链接整理；115、百度网盘和 OpenList 支持移动和复制整理；其他远程来源只保守允许移动整理。
- `SaveRelScrapePathRequest` 同时支持旧字段 `id`、`scrape_path_id` 和新字段 `sync_path_id`、`scrape_path_ids`。
- 同步路径和刮削路径的关联保存允许空 ID 列表，用于清空关联；通用 `IDListRequest` 不允许空列表。
- `IDCSVRequest` 保留 `ids=1,2` 的 Query 格式，用于刮削记录批量操作。
- `ParsePositiveIDRequest` 用于解析 HTTP path 中的正整数 `id`，控制器仍按各自模块既有响应格式返回错误。
- `QueueListRequest.Status` 当前只绑定为 `int`，不做枚举限制，继续兼容现有前端和模型状态值。
- `UpdateThreadsRequest.upload_threads` 为可选整数，显式数值必须在 `1` 到 `10` 之间；旧调用方省略该字段或传入 `null` 时保留当前上传并发数，首次初始化默认 `1`。设置写库失败时，内存中的配置和上传队列并发数都必须保持原值。
- `AISettingsRequest.EnableAI` 允许空值，避免旧前端或局部保存请求被误拒。
- `HTTPProxyRequest.PreserveProxyCredentials` 是可空布尔值：当前前端保存或测试脱敏代理地址时必须显式提交。`true` 仅在提交地址与当前存储地址的协议和 `host:port` 一致时保留用户名和密码；端点变化时忽略该标志，使用 `http_proxy` 中的凭据，避免将已存凭据转发给其他代理。`false` 表示将 `http_proxy` 中的凭据作为新值；字段缺失仅为兼容未升级前端，继续沿用历史的脱敏字符串匹配行为。
- 账号添加页面会在提交前拦截空账号备注、OpenList 访问地址、用户名、密码或 Token 等轻量问题；后端 DTO 仍是最终校验来源，并在账号接口返回前把字段级校验错误转换为面向用户的提示。
- `CreateOpenListAccountRequest` 会自动补全缺失的 `http://` 协议，并去掉末尾 `/`；新建 OpenList 账号必须提供 Token 或完整的用户名 / 密码。更新请求带有效 `id` 时，同认证方式且未提交新凭据可复用数据库中的已有凭据；切换为 Token 必须提交新 Token 并清空已保存的密码，切换为用户名密码必须提交用户名和密码并重新获取 Token，复用密码认证凭据校验时若触发自动刷新也会持久化最新 Token，实际凭据验证由模型层完成。
- `LoginRequest` 校验用户名和密码非空，并在进入限流、数据库查询和失败日志前保留用户名 20 个字符上限；实际身份校验交给登录模型。首次管理员创建和当前用户凭据修改使用严格用户名 / 密码规则。控制器仍统一返回「登录失败」，不向客户端暴露用户名、密码或验证码的具体失败原因。
- `BackupCreateRequest` 在原因为空时默认使用「手动备份」，与旧控制器行为一致。
- 115 内置应用和内置中转来源的 `deprecated` 标记只阻止新格式创建请求和带 `authorization_id` 的更换目标；历史账号解析以及旧格式普通授权/重新授权入口仍保留旧来源字符串兼容，避免影响已有 MQ 账号恢复授权。
- `PrepareAccountAuthorizationRequest` 要求正的 `account_id`、允许的目标 `source_type` 和 `confirmed=true`；115 目标继续复用 `SourceFromCreateRequest` 校验，并由控制器比较目标与原账号的 `source_type`，因此跨来源更换不能只靠前端拦截。
- `CancelAccountAuthorizationRequest` 要求正的 `account_id` 和非空 `authorization_id`；取消接口按账号绑定会话执行，并且设计为可重复调用。
- 账号 `name` 与非空 `user_id` 的重复检查属于模型/数据库业务约束，不在 DTO 中猜测数据库状态；冲突时控制器返回面向用户的错误，授权替换事务保持旧字段不变。
- `BackupListRequest` 保留旧分页兼容策略：页码小于 1 时回退为 1，每页数量小于 1 或大于 100 时回退为 20，类型为空时回退为 `all`。
- 备份配置中 `backup_retention` 为 0 时表示不更新或使用既有值；大于 0 时限制为 1 到 365。
- 首次数据库配置保存只接受 `sqlite`、`postgres` 引擎；PostgreSQL 配置中遗留的 `postgresType: embedded` 或未知模式必须被拒绝，不得静默改成外部连接。旧 SQLite 配置中的该字段不参与连接。

### 文件与目录浏览排序

`GET /api/path/sort-options?source_type=...&scope=files|directories` 返回当前来源、场景的 `fields`、`folders_first` 能力及 `default` 选择，不访问网盘。`requests/browse_sort.go` 是能力、字段校验及上游映射的共同来源；前端只维护显示文案。

| 来源 | 文件排序字段 | 目录排序字段 | 上游规则 |
| --- | --- | --- | --- |
| 115 | `name/time/size/type/default` | `name/time/default` | `o=file_name/user_utime/file_size/file_type`；升序 `asc=1`、降序 `asc=0` |
| 百度 | `name/time/size` | `name/time` | `order=name/time/size`；升序 `desc=0`、降序 `desc=1` |
| OpenList | `default` | `default` | 保留服务端顺序，不发送自定义排序参数 |
| 本地 | 无文件管理入口 | `name/time` | 返回当前层目录及可用的 `modified_time`；前端排序 |

`/api/path/files` 接受可选的 `folders_first`，仅 115 显式排序支持：开启映射 `custom_order=1`，关闭映射 `2`；省略时保留原置顶行为。`default` 映射 `custom_order=0`，不发送 `o/asc`。为兼容旧文件列表请求，`default + sort_order=asc` 允许作为占位，但不会传到上游；显式降序或置顶组合被拒绝。不支持的来源／场景／字段组合返回既有业务参数错误，不能静默忽略。

`/api/path/list` 接受可选的 `sort_by/sort_order` 和 `refresh=0|1`，维持目录数组响应。目录场景拒绝显式 `folders_first`；115 显式名称／时间排序内部使用置顶模式。未携带排序的旧目录请求及无选项 SDK 调用保留原行为。用户选择与上游 `order/is_asc` 回显分开，不能用一次名称回显覆盖 `default` 意图；回显的降序零值必须与缺失区分。

本地新建目录的 `/api/path/create` 响应 `id` 使用 `/` 作为路径分隔符，与 `/api/path/list` 中该目录的 `id` 一致；Windows 同样遵循此约定，确保创建后刷新仍能选中新目录。

115 目录列表按原始条目推进 offset，并过滤目录类型；`stdir=1` 不是仅目录筛选。显式置顶时可以在读到文件后结束目录前缀读取，跟随网盘时须继续读取；`count` 已包含系统目录，不再累加 `sys_count`。根目录系统文件夹可先于普通目录，不能据全局时间不单调重排或自动更换字段。类型排序采用上游规则，不以扩展名或 QMS 分类重建。

百度目录使用 `folder=1` 和普通 `list` 分页，不使用递归 `listall` 替代当前层读取。OpenList 目录复用 `/api/fs/dirs`；显式刷新先通过 `/api/fs/list` 的 `refresh=true` 刷新路径，再读目录，刷新失败返回错误。显式刷新传递调用方上下文，取消后不继续普通重试或等待重试间隔；取消一个共享认证等待者不取消其他请求的认证恢复。普通共享缓存读取的取消规则见下文。

目录分页、刷新或解析失败不能作为完整目录成功返回。远程列表只保序、过滤及分页，不在 QMS 中重排；缓存和请求合并身份区分字段、方向、置顶和跟随网盘，各种排序共用现有父目录／子树失效规则。交互和偏好规则见 [前端开发约定](frontend-development.md#目录浏览排序与偏好)。

### 文件与目录共享浏览缓存

`/api/path/files` 与远程 `/api/path/list` 复用进程内 `netFileCache`，最多保留 200 个批次，有效期为写入后 **180 秒**，命中不续期，进程重启即清空。本地目录不加入缓存。键包含来源、账号、规范化路径、排序字段／方向、文件夹置顶、筛选语义及批次范围；切换排序仍请求 QMS，只有对应缓存未命中、过期或显式刷新时才读取网盘。

- 115：相同参数、相同范围的原始列表批次共用一份缓存，保留条目类型、计数、路径及祖先链。文件视图投影文件项，目录视图按原始顺序过滤目录；当前层无类型筛选的两入口统一省略 `stdir`。目录显式名称／时间排序与相同排序且置顶的文件视图可互相命中；未指定排序的旧目录调用保留独立身份，不误用名称升序缓存。
- 百度：文件 `folder=0` 与目录 `folder=1` 共用缓存机制，但以不同筛选身份隔离数据，不能互相替代。
- OpenList：文件 `/api/fs/list` 与目录 `/api/fs/dirs` 分开保存；目录仍使用原生目录接口，不从文件首批推导完整目录。

任一入口显式刷新都会使该路径下所有排序和文件／目录视图失效，再读取当前视图；其他视图下次访问时按需重读。文件写操作复用父目录／子树失效规则。115 重命名、移动和删除还依据缓存响应的祖先 ID 清理受影响子树，避免数字 ID 不变时继续使用旧路径；同时推进失效代次，阻止尚未返回祖先链的旧读取回填缓存。

普通缓存未命中的相同请求按缓存身份和失效代次合并，并在执行前再次检查缓存。每位调用方可独立取消等待；共享上游批次使用最长 60 秒的独立上下文，取消一个等待者不影响其他等待者，全部离开后也不会开始后续分页。115 队列等待、普通重试和限流等待均响应上下文取消，不延长该批次期限。显式刷新沿用调用方上下文，不加入普通请求合并。上游读取失败或其上下文被取消时不写入缓存；仅等待者取消而共享读取成功时仍可缓存。较早失效代次的结果，以及同代次较慢的普通读取，都不能覆盖已经写入的有效刷新缓存；不同排序同时回源不提供网盘快照一致性保证。

## 安全敏感校验

- `/proxy-115` 使用 `Proxy115Request` 和 `DownloadProxyURL` 限制目标下载域名和端口范围，并在重定向时重新校验 Location，避免通过跳转绕过反代白名单。
- 日志读取相关请求接受根日志文件名或 `libs/<日志文件名>`；拒绝绝对路径、路径穿越、非白名单子目录和多级子目录。
- 同步任务详情实时流 `/api/sync/tasks/:id/stream` 不接受客户端传入日志路径，只使用 `ParsePositiveIDRequest` 校验路径 `id`，再由后端根据 `sync_id` 派生同步任务日志路径。
- 临时图片读取请求只接受相对路径，并拒绝绝对路径和路径穿越。
- 创建目录请求拒绝空名称、`.`、`..`、路径分隔符和控制字符。
- 刮削路径的文件夹和文件命名模板拒绝绝对路径、反斜杠和 `..` 片段；二级分类名必须是单层目录名，拒绝路径分隔符、`.`、`..` 和纯空白。模板渲染结果和分类名在参与目标路径拼接前仍会统一清理，本地落盘前还会校验目标路径仍在根目录内，完整契约见 [刮削命名模板与其他类型 NFO](../reference/scrape-rename-templates.md)。
- Webhook JSON 模板会先替换内置变量再做 JSON 解析；Form 模板必须符合 `key=value&key2=value2` 格式。
- Webhook 额外请求头使用 `headers` 对象传递，Header 名称会去除首尾空白，且必须是合法 HTTP token；空 Header 名会被拒绝。

## 上传和 STRM 入站校验边界

115 上传增强实现中，`preid` 计算窗口是官方协议参数，固定为文件前 `128 KiB` 的 SHA1，并通过单元测试约束；请求入口不允许外部传入自定义 `preid` 窗口。二次认证的 `sign_check` 必须解析为合法闭区间，结束位置不得小于起始位置，读取范围必须落在本地文件大小内。`/open/upload/resume` 只接受 `file_size`、`target`、`fileid`、`pick_code` 这组官方字段。

OSS multipart 的 part size 必须由后端计算，不接受外部传入：默认 `32 MiB`，超过 `9999` 个 part 时动态放大并按 `1 MiB` 对齐，超过 OSS 单 part 上限时直接失败。初始化 multipart 必须带 `sequential=1`，相关单元测试会校验该请求参数。115 调度返回的 `callback` 可为单个对象或对象数组，数组只使用第一个回调配置。传给 OSS complete 前必须校验 `callback` / `callback_var` 是 JSON 对象，并将 115 返回的 JSON 字符串原样 Base64 编码；不得提前展开 `callbackBody`，不得向 `callback_var` 增加非 `x:` 字段，也不得把本地 SHA1 作为 OSS multipart 或 callback 的替代输入。`CompleteMultipartUpload` 后必须校验 115 callback 业务结果；`state=false`、`message` 非空、缺少 `file_id` 或缺少 `pick_code` 都不能视为上传成功。

同步目录聚合写入和目录监控规则的 DTO 仍遵循本文的绑定与校验边界；精确请求字段、幂等、结构化错误和最终集合语义由 [同步目录聚合 API](../reference/sync-path-api.md) 维护。`/api/directory-upload/sync-paths/:sync_path_id/scan` 只扫描总开关和规则自身都启用的规则。

STRM Webhook 的外部字段、鉴权、路径边界、批量规则和响应由 [STRM Webhook](../reference/strm-webhook.md) 维护；本文只约束其控制器使用的输入校验边界。

## Emby Webhook 入站边界

`POST /emby/webhook` 沿用 `EnableAuth` 和现有 API Key 鉴权，优先 `X-API-Key` 请求头，兼容 `api_key` 查询参数。鉴权后读取最多 `4 MiB` 的 JSON，使用 `requests.ParseEmbyWebhook` 进行严格解析；该入口需要检查重复键，不能直接用会静默覆盖同名键的普通绑定代替。关键字段名沿用 Emby 原始大小写；重复键、关键字段类型错误、非法条目 ID、多份 JSON、超过 64 层嵌套或无效编码都被拒绝。日期支持 RFC3339Nano，仅用于诊断。

官方 `library.new`、兼容的 `library.modified`、`library.deleted` 和受支持的 `deep.delete` 保存为有限、脱敏的持久输入。缺少服务器身份、服务器字段冲突或删除通知缺少原始路径时，记录阻断原因，不把它们升级为可执行删除。`Item.Path` 保留文件名内真实换行和空格；增强通知正文的目录路径不能覆盖 JSON 文件路径。只从已识别的单行完整 Mount Paths 提取未核验候选，多行或不受支持正文仅保存原因，不盲拆文件或保留可能含凭据的自由文本。不记录原始 body，URL 用户信息、认证查询参数和片段不进入新收件记录；pickcode 作为独立候选字段保留。

持久保存成功后维持 HTTP `200`、`{"message":"webhook"}` 响应，表示已保存，后续同步和删除由应用生命周期内的 worker 处理。JSON 格式错误返回 `400`，超过大小限制返回 `413`；配置不完整、读取／保存失败或备份恢复期间暂停收件返回 `503`，不确认已接收。未配置连接时，无关事件沿用忽略并返回 `200` 的旧行为；启用的鉴权仍先于请求体解析。原有新增通知、媒体信息提取和播放通知路径保留。服务器身份、接收时删除授权、历史文件代际和执行前核验属于模型／服务层，不由解析器裁定；完整行为见 [Emby 媒体库同步](../architecture/emby-library-sync.md)。

## 当前例外

以下接口或参数仍是特殊实现，不应作为新增接口的默认写法：

- 用户会话撤销使用 `session_id` 路径参数，当前直接从 `c.Param("session_id")` 读取。
- 同步记录、同步任务详情 HTTP 查询、同步路径列表查询仍在 `controllers/sync.go` 使用控制器内局部 Request 结构；同步任务详情实时流在 `controllers/event_stream.go` 使用 `ParsePositiveIDRequest` 解析路径 `id`，不新增 DTO。
- 备份上传恢复在解析 multipart 前限制请求体，并单独限制实际文件大小；超限返回 HTTP `413`、顶层 `error_code=BACKUP_ARCHIVE_LIMIT`，清理 multipart 和本轮上传临时文件。文件读取、扩展名和暂存处理保留在控制器中，具体限额见[数据库运维](../operations/database.md#恢复与风险边界)。
- 首次数据库配置服务 `backend/main.go` 使用独立的启动期接口和私有请求结构，不纳入常规 API DTO 目录。
- 部分只读或触发型接口没有外部参数，或只做运行状态检查，不需要 DTO。

新增或改造接口时，不应继续扩大这些例外；如果改动触及上述接口，可以顺手迁移到 `backend/internal/requests`，但要保持外部响应兼容。

## 前端规则

前端校验用于即时反馈和减少误操作，不能替代后端校验，也不作为安全边界。与后端一致的范围和枚举常量放在 `frontend/src/constants/validation.ts`：

- `THREAD_LIMITS`：下载线程、同时上传任务数、文件详情线程、OpenList QPS、重试次数、重试延迟、文件列表分页大小。
- `SCRAPE_THREAD_LIMITS`：本地刮削最大线程 20、远程刮削最大线程 5、最小线程 1。
- `STRM_GLOBAL_OPTIONS` 和 `STRM_CUSTOM_OPTIONS`：全局配置与自定义配置的 STRM 开关枚举；`add_path` 全局值为 `1` 添加完整路径、`2` 只添加文件名、`3` 不添加，同步目录自定义配置额外支持 `-1` 继承全局 STRM 设置。
- `HTTP_URL_PATTERN`：前端 URL 输入提示使用，后端仍以 `validation.HTTPURL` 为准。
- `CRON_DEFAULTS`：前端默认 Cron 值。
- `PROXY_SCHEMES`、`PROXY_PORT_RANGE`：与后端 `proxySchemes` 和 `PortInRange` 对齐的代理协议白名单和端口范围。`PROXY_SCHEME_HINT`、`PROXY_URL_HELP` 和 `PROXY_URL_MESSAGES` 由白名单派生，协议名不得在组件里重复手写；后端新增协议时只改这一处。`PROXY_URL_PLACEHOLDER` 里的示例端口是各协议的社区惯例，与白名单无关，直接写字面量。

`AppProxySettings` 的 `validateProxyUrl` 与 `validation.ProxyURL` 的判断顺序一致：先拒绝空白和控制字符，再判断格式，然后是协议白名单，最后是端口范围。这里不能直接依赖 `new URL()`：它对 `proxy.example.com:8080` 不抛错（会把主机名当协议），会把 `\t`、`\n` 静默删掉，对越界端口直接抛错而拿不到端口专属提示，因此协议、Host 段和端口都要在交给 `URL` 之前自行切分判断。`scheme://:port` 在前端同样放行。

备份定时策略选择器将当前生效的 Cron 和自定义 Cron 草稿拆成两个前端状态；提交配置时仍只保存 `backup_cron`，避免预设策略覆盖尚未提交的自定义表达式。

调整后端范围或枚举时，必须同步检查该文件和相关表单组件，避免前后端提示不一致。

### STRM 正则预检

`strmRegex.ts` 使用原生 `RegExp` 预检临时副本，适配 `i/m/s/U` 标志及其组合、开关和局部作用域（如 `(?im-s)`、`(?i:abc)`），以及 `\A`、`\z`、Go 命名分组和 `\Q...\E`。这些转换只用于语法检查，不模拟 Go 匹配结果，也不得写回表单或提交数据。

扫描时区分转义、字符组和引用区域，提示明确不支持的前后向断言、反向引用、原子组、占有量词和超过 1000 的单个重复次数。合法八进制转义和包含类似符号的字面量不能误报。Unicode 属性、POSIX 字符组等无法可靠预检的 Go 语法允许继续添加并提示后端校验；浏览器预检通过也不代表 Go 一定接受。

全局设置和目录自定义设置共用正则输入与预检。输入每次整条添加，大小写、转义、逗号、分号和首尾空格保持不变；空字符串不允许作为规则，空列表合法。提交前再次预检列表，服务器仍通过原有保存接口做最终校验。全局设置只从已知校验结构提取规则序号和固定原因生成提示，不透传底层解析细节；目录表单将 `exclude_name_regex_arr[n]` 映射到正则字段并显示第 `n+1` 条错误。匹配和继承语义见 [STRM 名称排除](../operations/configuration.md#strm-名称排除)。

## 测试要求

- 通用规则测试放在 `backend/internal/validation`。
- Request DTO 测试放在 `backend/internal/requests`，按模块拆分。
- DTO 测试至少覆盖合法请求、必填缺失、枚举错误、范围错误、格式错误和关键条件规则。
- 涉及兼容性逻辑时必须补充回归测试，例如旧字段映射、空关联列表、默认分页值和 URL 规范化。
- 修改后端校验时运行 `(cd backend && go test ./...)`。
- 修改前端校验常量或表单时至少运行 `(cd frontend && pnpm run type-check)`；影响构建链路时运行 `(cd frontend && pnpm run build)`。

## 新增或迁移接口清单

1. 在 `backend/internal/requests` 新增或复用 Request DTO。
2. 使用 `form`、`json` 标签匹配现有外部字段名，避免破坏前端兼容性。
3. 在 `Validate()` 中优先复用 `backend/internal/validation`；只有业务条件规则留在 DTO 内。
4. 需要进入模型层时提供 `ToModel()` 或语义明确的转换方法。
5. 控制器绑定请求后立即调用 `Validate()`，再执行数据库或外部服务校验。
6. 保持原控制器响应风格，除非明确要做 API 行为变更。
7. 补充 DTO 和通用规则测试。
8. 如果改动字段范围、枚举或默认值，同步更新 `frontend/src/constants/validation.ts` 和本文档。

## 不变量

- 前端校验只用于即时反馈，不能代替后端 DTO、控制器或业务层校验。
- DTO 负责可在 HTTP 边界判断的格式、范围、枚举和条件规则；数据库存在性、权限、任务状态和外部服务状态留在控制器或业务层。
- 修改接口不得仅因迁移 DTO 改变既有控制器响应风格、HTTP 状态码或公开字段。
- 外部字段名由 `form` 和 `json` 标签定义；新增 DTO 不得擅自改写存量字段名。

## 验证方式

- 修改通用规则或 DTO 后运行对应包的 `go test`；涉及控制器时运行相应控制器包测试。
- 修改前端范围、枚举或表单时运行 `(cd frontend && pnpm run type-check)`；影响构建链路时运行 `(cd frontend && pnpm run build)`。
- 修改 API 兼容边界时补充合法、必填缺失、格式 / 枚举错误和旧字段兼容场景的测试。
