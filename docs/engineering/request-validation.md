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
| `requests/scrape_path.go` | 刮削路径保存 | 创建和更新分场景校验；更新时使用旧记录补齐不可编辑的来源类型、账号和媒体类型；刮削类型、整理方式、源路径、按场景要求的目标路径、扩展名、最小文件大小、线程上限、Cron。 |
| `requests/scrape_settings.go` | TMDB、AI、分类和 TMDB 搜索 | URL、语言代码、国家代码、AI 动作枚举、模型名长度、超时范围、分类名称、Genre ID、年份范围。 |
| `requests/accounts.go` | 账号、OpenList 账号、API Key | 账号来源类型、名称长度、115 授权来源组合、OpenList URL 规范化、用户名/密码或 Token、API Key 状态。 |
| `requests/connections.go` | HTTP 代理、OAuth、二维码、远程直链、反代、请求队列限制和统计 | 代理 URL、`preserve_proxy_credentials` 的显式凭据保留意图、账号 ID、OAuth 回调 URL、`data`/`payload` 条件必填、二维码 UID、PickCode、反代下载域名白名单、QPS/QPM/QPH、统计窗口和清理天数。 |
| `requests/emby.go` | Emby 配置 | Emby URL、同步 Cron、布尔开关枚举、媒体库 JSON 字符串。 |
| `requests/backup.go` | 备份创建、列表、记录 ID、恢复和配置 | 手动备份原因默认值、分页默认值、备份记录 ID、启用开关、Cron、保留天数、最大备份数、压缩开关。 |
| `requests/notification.go` | Telegram、MeoW、Bark、ServerChan、自定义 Webhook 渠道 | 渠道名称、必填凭据、URL、Webhook 方法、格式、认证方式和模板格式。 |
| `requests/users.go` | 登录、启用/关闭两步验证、当前用户用户名/密码修改 | 登录校验用户名和密码非空，用户名 20 个字符上限；创建和修改使用严格用户名 / 密码规则，用户名去除首尾空白后长度为 3 到 20 个字符且只能包含英文和数字，密码长度至少 6 个字符且不能是纯数字或纯字母；两步验证码必填。 |
| `requests/operations.go` | 分页、ID、路径浏览、网盘文件、目录操作、队列、同步/刮削关联、日志、临时图片、版本更新 | 分页默认值和范围、HTTP path 正 ID、ID 列表、CSV ID、来源类型、文件夹名、路径穿越防护、日志文件名限制、版本号格式、日期范围。 |

迁移临时服务是启动期流程，不纳入公共 `backend/internal/requests` 目录。它在 `backend/internal/migrate` 包内使用私有 DTO 校验 PostgreSQL 测试连接和保存配置请求。

## 重要兼容性规则

- `SyncPathRequest` 同时支持计划中的嵌套 `setting` 字段和旧前端使用的顶层 STRM 字段；存在非零嵌套配置时优先使用 `setting`。
- 同步路径自定义 STRM 配置使用 `-1` 表示继承全局值；全局 STRM 配置不接受 `-1`。
- `SaveScrapePathRequest` 新增请求要求提供来源类型、账号和媒体类型；更新请求沿用旧记录中的这些不可编辑字段，避免旧前端编辑请求被误拒。
- 刮削路径 `only_scrape` 模式不要求目标路径，整理方式只能为 `same`；需要整理或重命名时才要求目标路径。
- 刮削路径本地来源支持移动、复制、软链接和硬链接整理；115、百度网盘和 OpenList 支持移动和复制整理；其他远程来源只保守允许移动整理。
- `SaveRelScrapePathRequest` 同时支持旧字段 `id`、`scrape_path_id` 和新字段 `sync_path_id`、`scrape_path_ids`。
- 同步路径和刮削路径的关联保存允许空 ID 列表，用于清空关联；通用 `IDListRequest` 不允许空列表。
- `IDCSVRequest` 保留 `ids=1,2` 的 Query 格式，用于刮削记录批量操作。
- `ParsePositiveIDRequest` 用于解析 HTTP path 中的正整数 `id`，控制器仍按各自模块既有响应格式返回错误。
- `QueueListRequest.Status` 当前只绑定为 `int`，不做枚举限制，继续兼容现有前端和模型状态值。
- `AISettingsRequest.EnableAI` 允许空值，避免旧前端或局部保存请求被误拒。
- `HTTPProxyRequest.PreserveProxyCredentials` 是可空布尔值：当前前端保存或测试脱敏代理地址时必须显式提交。`true` 仅在提交地址与当前存储地址的协议和 `host:port` 一致时保留用户名和密码；端点变化时忽略该标志，使用 `http_proxy` 中的凭据，避免将已存凭据转发给其他代理。`false` 表示将 `http_proxy` 中的凭据作为新值；字段缺失仅为兼容未升级前端，继续沿用历史的脱敏字符串匹配行为。
- 账号添加页面会在提交前拦截空账号备注、OpenList 访问地址、用户名、密码或 Token 等轻量问题；后端 DTO 仍是最终校验来源，并在账号接口返回前把字段级校验错误转换为面向用户的提示。
- `CreateOpenListAccountRequest` 会自动补全缺失的 `http://` 协议，并去掉末尾 `/`。
- `LoginRequest` 校验用户名和密码非空，并在进入限流、数据库查询和失败日志前保留用户名 20 个字符上限；实际身份校验交给登录模型。首次管理员创建和当前用户凭据修改使用严格用户名 / 密码规则。控制器仍统一返回「登录失败」，不向客户端暴露用户名、密码或验证码的具体失败原因。
- `BackupCreateRequest` 在原因为空时默认使用「手动备份」，与旧控制器行为一致。
- `BackupListRequest` 保留旧分页兼容策略：页码小于 1 时回退为 1，每页数量小于 1 或大于 100 时回退为 20，类型为空时回退为 `all`。
- 备份配置中 `backup_retention` 为 0 时表示不更新或使用既有值；大于 0 时限制为 1 到 365。
- `internal/migrate` 的测试连接请求允许 `database` 为空，并继续固定连接 `dbname=postgres`；保存配置请求要求 `database` 非空。

## 安全敏感校验

- `/proxy-115` 使用 `Proxy115Request` 和 `DownloadProxyURL` 限制目标下载域名和端口范围，并在重定向时重新校验 Location，避免通过跳转绕过反代白名单。
- 日志读取相关请求接受根日志文件名或 `libs/<日志文件名>`；拒绝绝对路径、路径穿越、非白名单子目录和多级子目录。
- 同步任务详情实时流 `/api/sync/tasks/:id/stream` 不接受客户端传入日志路径，只使用 `ParsePositiveIDRequest` 校验路径 `id`，再由后端根据 `sync_id` 派生同步任务日志路径。
- 临时图片读取请求只接受相对路径，并拒绝绝对路径和路径穿越。
- 创建目录请求拒绝空名称、`.`、`..`、路径分隔符和控制字符。
- Webhook JSON 模板会先替换内置变量再做 JSON 解析；Form 模板必须符合 `key=value&key2=value2` 格式。
- Webhook 额外请求头使用 `headers` 对象传递，Header 名称会去除首尾空白，且必须是合法 HTTP token；空 Header 名会被拒绝。

## 上传和 STRM 入站校验边界

115 上传增强实现中，`preid` 计算窗口是官方协议参数，固定为文件前 `128 KiB` 的 SHA1，并通过单元测试约束；请求入口不允许外部传入自定义 `preid` 窗口。二次认证的 `sign_check` 必须解析为合法闭区间，结束位置不得小于起始位置，读取范围必须落在本地文件大小内。`/open/upload/resume` 只接受 `file_size`、`target`、`fileid`、`pick_code` 这组官方字段。

OSS multipart 的 part size 必须由后端计算，不接受外部传入：默认 `32 MiB`，超过 `9999` 个 part 时动态放大并按 `1 MiB` 对齐，超过 OSS 单 part 上限时直接失败。初始化 multipart 必须带 `sequential=1`，相关单元测试会校验该请求参数。115 调度返回的 `callback` 可为单个对象或对象数组，数组只使用第一个回调配置。传给 OSS complete 前必须校验 `callback` / `callback_var` 是 JSON 对象，并将 115 返回的 JSON 字符串原样 Base64 编码；不得提前展开 `callbackBody`，不得向 `callback_var` 增加非 `x:` 字段，也不得把本地 SHA1 作为 OSS multipart 或 callback 的替代输入。`CompleteMultipartUpload` 后必须校验 115 callback 业务结果；`state=false`、`message` 非空、缺少 `file_id` 或缺少 `pick_code` 都不能视为上传成功。

同步目录聚合写入和目录监控规则的 DTO 仍遵循本文的绑定与校验边界；精确请求字段、幂等、结构化错误和最终集合语义由 [同步目录聚合 API](../reference/sync-path-api.md) 维护。`/api/directory-upload/sync-paths/:sync_path_id/scan` 只扫描总开关和规则自身都启用的规则。

STRM Webhook 的外部字段、鉴权、路径边界、批量规则和响应由 [STRM Webhook](../reference/strm-webhook.md) 维护；本文只约束其控制器使用的输入校验边界。

## 当前例外

以下接口或参数仍是特殊实现，不应作为新增接口的默认写法：

- 用户会话撤销使用 `session_id` 路径参数，当前直接从 `c.Param("session_id")` 读取。
- 同步记录、同步任务详情 HTTP 查询、同步路径列表查询仍在 `controllers/sync.go` 使用控制器内局部 Request 结构；同步任务详情实时流在 `controllers/event_stream.go` 使用 `ParsePositiveIDRequest` 解析路径 `id`，不新增 DTO。
- 备份上传恢复使用 multipart 文件流，文件读取、扩展名和临时文件处理仍保留在控制器中。
- 迁移临时服务 `internal/migrate/server.go` 使用独立的启动期接口和包内私有 DTO，不纳入常规 API DTO 目录。
- 部分只读或触发型接口没有外部参数，或只做运行状态检查，不需要 DTO。

新增或改造接口时，不应继续扩大这些例外；如果改动触及上述接口，可以顺手迁移到 `backend/internal/requests`，但要保持外部响应兼容。

## 前端规则

前端校验用于即时反馈和减少误操作，不能替代后端校验，也不作为安全边界。与后端一致的范围和枚举常量放在 `frontend/src/constants/validation.ts`：

- `THREAD_LIMITS`：下载线程、文件详情线程、OpenList QPS、重试次数、重试延迟、文件列表分页大小。
- `SCRAPE_THREAD_LIMITS`：本地刮削最大线程 20、远程刮削最大线程 5、最小线程 1。
- `STRM_GLOBAL_OPTIONS` 和 `STRM_CUSTOM_OPTIONS`：全局配置与自定义配置的 STRM 开关枚举；`add_path` 全局值为 `1` 添加完整路径、`2` 只添加文件名、`3` 不添加，同步目录自定义配置额外支持 `-1` 继承全局 STRM 设置。
- `HTTP_URL_PATTERN`：前端 URL 输入提示使用，后端仍以 `validation.HTTPURL` 为准。
- `CRON_DEFAULTS`：前端默认 Cron 值。
- `PROXY_SCHEMES`、`PROXY_PORT_RANGE`：与后端 `proxySchemes` 和 `PortInRange` 对齐的代理协议白名单和端口范围。`PROXY_SCHEME_HINT`、`PROXY_URL_HELP` 和 `PROXY_URL_MESSAGES` 由白名单派生，协议名不得在组件里重复手写；后端新增协议时只改这一处。`PROXY_URL_PLACEHOLDER` 里的示例端口是各协议的社区惯例，与白名单无关，直接写字面量。

`AppProxySettings` 的 `validateProxyUrl` 与 `validation.ProxyURL` 的判断顺序一致：先拒绝空白和控制字符，再判断格式，然后是协议白名单，最后是端口范围。这里不能直接依赖 `new URL()`：它对 `proxy.example.com:8080` 不抛错（会把主机名当协议），会把 `\t`、`\n` 静默删掉，对越界端口直接抛错而拿不到端口专属提示，因此协议、Host 段和端口都要在交给 `URL` 之前自行切分判断。`scheme://:port` 在前端同样放行。

备份定时策略选择器将当前生效的 Cron 和自定义 Cron 草稿拆成两个前端状态；提交配置时仍只保存 `backup_cron`，避免预设策略覆盖尚未提交的自定义表达式。

调整后端范围或枚举时，必须同步检查该文件和相关表单组件，避免前后端提示不一致。

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
