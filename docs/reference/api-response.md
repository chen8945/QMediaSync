# API 响应与请求错误

> 职责：定义 QMediaSync 自有普通 JSON API 的响应包络、HTTP 状态、请求错误与诊断关联。
>
> 权威范围：本文档是公共响应字段的唯一契约；领域数据和任务状态由各专题维护，输入规则见 [请求校验约定](../engineering/request-validation.md)。
>
> 修改时机：修改公共输出、状态分类、错误字段、前端解包或诊断关联时必须更新本文档。
>
> 相关代码：`backend/internal/httpapi/`、`backend/internal/controllers/`、`backend/internal/app/`、`backend/assets/db_config.html`、`frontend/src/api/response.ts`、`frontend/src/http/errors.ts`。

## 范围与包络

自有普通 JSON API 使用 `application/json` 和统一 `APIResponse[T]` 包络，包含管理接口、版本与公告、日志历史、首次数据库配置向导和自有 STRM Webhook。成功与请求失败共用外层结构；前后端及向导同构建发布，不保留历史数值 `code`、通知专用成功码、裸管理 JSON 或嵌套请求错误兼容。

SSE、HTML、静态资源、纯文本日志预览、文件下载、Range/HEAD、重定向、第三方媒体 Webhook 确认及兼容代理遵循各自协议。代理的上游 JSON 与后台任务结果不因含有 `code`、`status` 或 `error_code` 而转换为管理包络。本包络是项目自定义契约，不使用 `application/problem+json`。

| 字段 | 类型 | 成功 | 请求失败 |
| --- | --- | --- | --- |
| `status` | 整数 | 必须，与真实 HTTP `2xx` 相同 | 必须，与真实 HTTP `4xx/5xx` 相同 |
| `message` | 字符串 | 可选，安全的人类提示 | 必须，非空安全说明 |
| `data` | 领域类型 | 必须，完整保留结果 | 必须，通常为 `null`；明确的部分执行结果保留 |
| `error_code` | 字符串 | 省略 | 必须，稳定机器原因 |
| `instance` | URI 引用字符串 | 省略 | 必须，本次请求失败的诊断关联标识 |
| `field_errors` | 字段错误数组 | 省略 | 存在字段错误时返回 |
| `warnings` | 字符串数组 | 存在提交后应用警告时返回 | 省略 |

`field_errors` 每项包含 `field`、`message` 和可选 `client_id`。`field` 使用请求字段路径；规则 `client_id` 用于定位暂存规则，不改变领域标识。空 `field_errors`、空 `warnings` 省略，消费者按空列表处理。

已由 DTO 或领域纯校验覆盖的必填条件，应在绑定后通过该校验保留字段定位，避免重复绑定校验提前返回而丢失 `field_errors`。未知绑定错误只返回安全说明；具体输入边界见 [请求校验约定](../engineering/request-validation.md#控制器流程)。

STRM、日志、Cron、限流和请求统计、API Key、同步路径及 TMDB 搜索的已有必填校验同样保留对应字段定位。同步路径聚合沿用现有字段名，nullable 布尔值的显式 `false` 仍是合法输入；省略与格式错误的边界由原 DTO 和绑定层分别负责。

`data=null/false/0/""/[]` 均为合法结果，不能用真假判断请求是否成功。`message` 供展示，机器判断使用真实 HTTP 状态、已知 `error_code` 和领域结构。

HTTP `200` 成功示例：

```json
{
  "status": 200,
  "message": "设置已保存",
  "data": { "enabled": true },
  "warnings": ["设置已保存，但本地应用未完成"]
}
```

HTTP `400` 字段错误示例：

```json
{
  "status": 400,
  "message": "同步源目录不能为空",
  "data": null,
  "error_code": "INVALID_REQUEST",
  "instance": "/request-errors/7A7wstj6PbJMv1NyF0hMxb4r",
  "field_errors": [
    { "field": "sync_path.base_cid", "message": "不能为空" }
  ]
}
```

## HTTP 状态与错误分类

HTTP 状态表达本次请求结果，正文 `status` 用同一个值写出。公共分类遵循下表，精确领域原因由所属模块判断：

| 状态 | 使用条件 |
| --- | --- |
| `200` | 成功查询、同步完成、保存成功或正常空闲；删除可返回 `data=null` |
| `201` | 创建可识别资源的成功结果；有资源地址时返回 `Location`，幂等创建按领域契约重放原响应 |
| `202` | 已受理异步任务，返回任务标识或状态；实际完成由任务系统确认 |
| `400` | 绑定、格式、参数或已知输入校验失败 |
| `401` | QMediaSync 会话、API Key 或专用恢复回执确实无效；认证拥有者设置对应 `WWW-Authenticate` |
| `403` | 权限不足、来源或 CSRF 拒绝 |
| `404` | 请求的本地资源或自有接口不存在 |
| `405` | 自有 API 路径存在但方法不支持，同时返回 `Allow` |
| `409` | 运行状态冲突或幂等请求仍在处理 |
| `413` | 超过已有请求或上传大小限制 |
| `429` | 现有限流策略拒绝，保留其等待信息 |
| `500` | 数据库或内部故障，未知内部错误使用安全通用消息 |
| `502/504` | 确实充当网关或代理时，上游响应无效或等待超时 |
| `503` | 临时不可用、恢复维护或服务准备失败 |

状态不能根据控制器名称、历史数值码或任意错误文案推断。公共层识别已有校验、sentinel 和可识别领域错误，使用 Go 错误链匹配；领域原因留在原拥有者。数据库查询失败与资源不存在分别处理，上游网盘凭据失效不表示 QMediaSync 会话失效。认证及恢复回执规则见 [认证会话](../architecture/authentication-sessions.md#请求错误码)。

日志补读与带 cursor 的订阅缺少 `file_id`，或 cursor／文件身份与当前文件不符时，返回 `409 / LOG_CURSOR_INVALID`；历史读取传入 `file_id` 时也按该身份条件校验。浏览器按 [实时事件](../architecture/realtime-events.md#通用日志) 重新加载文件快照，不能把文件变化归为认证失败。

在线更新的发布查询成功后，目标版本不存在或没有当前平台安装包，返回 `400 / UPDATE_TARGET_UNAVAILABLE`，不创建更新任务。发布列表查询故障仍返回 `500 / UPDATE_RELEASE_LOOKUP_FAILED`；下载和安装边界见 [部署说明](../operations/deployment.md#发布二进制与-systemd)。

AI 识别连接测试通过错误链识别上游等待超时，返回 `504 / UPSTREAM_TIMEOUT`；其他测试失败沿用 `500 / AI_TEST_FAILED`。客户端保留供应商标准嵌套 `error.message` 的实际原因，无法取得有效原因时使用包含 HTTP 状态的非空诊断。公共响应仍使用安全说明，关联诊断通过本次草稿的 API Key 脱敏，不打印完整上游正文。

文件浏览读取账号后产生的来源排序校验仍为 `400 / INVALID_REQUEST`，保留原 `field_errors`；不能因该入口随后访问网盘而归为上游失败。支持的组合见 [浏览排序](../engineering/request-validation.md#文件与目录浏览排序)。

115 直链缓存未命中时，取链编排保留实际 SDK 错误，由已有网盘分类器区分凭据失效（`400 / UPSTREAM_CREDENTIALS_INVALID`）、超时（`504 / UPSTREAM_TIMEOUT`）和其他上游失败（`502 / UPSTREAM_ERROR`），不能用空链接或通常为空的上下文错误代替 SDK 原因。重新刮削保留 TMDB 错误来源，网络或非成功响应返回 `502 / UPSTREAM_ERROR`，超时返回 `504 / UPSTREAM_TIMEOUT`；本地配置、数据库故障仍为安全的 `500 / INTERNAL_ERROR`。诊断保留原始错误链，并使用本次调用已取得的凭据脱敏。

共享下载代理在取得上游响应前，通过错误链区分等待超时（`504 / UPSTREAM_TIMEOUT`）与其他请求失败（`502 / DOWNLOAD_PROXY_FAILED`）；请求取消保留后者，不作为超时。已取得的上游状态、响应头、Range/HEAD 和正文仍按原生协议透传。

多端播放副本取链先读取本地原文件索引。索引不存在返回 `404 / RESOURCE_NOT_FOUND`，数据库读取故障返回 `500 / INTERNAL_ERROR`，两者不访问网盘；已识别的本地查询故障不进入上游分类，后续 SDK 故障沿用原上游分类。取消和复制预算耗尽保留原分类，失败仍不回退到原文件，详见 [多端播放链路](../architecture/upload-and-strm-processing.md#115-多端播放链路)。

普通同步、全量同步、手动同步和刮削扫描的重复入队请求返回 `409 / TASK_ALREADY_EXISTS`，明确说明任务已排队或正在运行。该结果保留原队列中的任务，不作为内部故障或本系统认证失效；非重复的未知入队错误仍返回安全的 `500 / INTERNAL_ERROR`。

删除仍有关联同步目录的账号返回 `409 / ACCOUNT_HAS_SYNC_PATHS`，提示先删除关联目录，保留账号及目录记录。关联查询或数据库删除失败返回安全的 `500 / INTERNAL_ERROR`，即使查询已经返回部分关联行，也不能误报成功或业务冲突。关联语义见 [账号授权与更换](account-authorization.md#账号关联语义)。

通知规则保存引用不存在或已删除的渠道时，返回 `404 / NOTIFICATION_CHANNEL_NOT_FOUND`，不写入或应用规则；渠道查询故障返回安全的 `500 / INTERNAL_ERROR`。规则保存与渠道删除的并发边界见 [通知规则](database-schema.md#notification_rules)。

## 请求、任务与应用结果

成功读取一个失败任务仍是成功查询，失败原因保留在任务数据中，例如 `data.status`、`data.error_code`；它们不提升为本次请求的错误。备份恢复结果见 [数据库运维](../operations/database.md#备份和恢复状态)。

数据库已提交、后续本地应用失败时，返回成功、已保存的数据和顶层 `warnings`。警告不回滚已提交结果，也不触发自动重新提交；用户修正原因后可按原操作重试应用。

批量操作已经部分受理或删除后失败时，错误包络保留明确的 `data.accepted/items` 或 `data.deleted_ids/failures`，同时返回真实请求错误。页面展示已发生的结果与剩余失败，不虚报全部成功、不自动重发。领域层只返回可公开的结果并脱敏逐项说明；公共输出不把任意内部对象作为失败数据回显。

文件操作的 `data.status=completed/submitted` 与上游 `task_ids` 保留原业务含义。STRM 的 `data.request_id/task_ids/results` 属于受理批次和逐项结果，不被 `instance` 替代；见 [STRM Webhook](strm-webhook.md#响应)。

## 输出与诊断

`internal/httpapi` 集中成功与失败写出。控制器绑定与校验输入、调用既有业务、选择真实状态并投影领域结果；应用服务不依赖 Gin、HTTP DTO 或响应包络。领域事务、幂等、并发和生命周期仍由原模块拥有。

普通 JSON 在提交响应头前完成序列化；编码失败且尚可写出时返回安全统一 `500`。响应已开始或网络写出失败时不追加第二个 JSON，也不声称业务已回滚。响应写出后发生的失败，包括恢复器捕获的 panic，仍使用公共出口记录安全关联诊断；日志状态表示该失败的分类，不改变已发送的状态、正文或响应头。重复成功写入不产生失败诊断。流与下载不进入 JSON 缓冲。CORS OPTIONS 在握手完成后终止原处理链。

自有请求经过公共出口处理 HEAD 时，只提交对应状态与响应头，不写 JSON 正文；失败仍生成并记录诊断关联。

失败响应生成 `/request-errors/<24 位随机标识>`，同一请求的关联诊断使用同一个标识。该 URI 不提供错误查询服务，不接收客户端自定义实例；上下文包含方法、安全路径、状态、机器原因和已脱敏的内部原因。公共 HTTP 诊断不转储完整 Query、Request/Response、Cookie、Token 或敏感配置；SDK 请求详情使用所属 logger 的规则，见 [日志行为与脱敏](../operations/configuration.md#日志行为与脱敏)。

自有普通 JSON API 的恢复器将 panic 类型、可读说明及当前请求 goroutine 的完整调用栈放入内部 Cause，经公共诊断记录一次 `ERROR`，沿用同一 instance；error panic 保留原错误链，字符串 panic 保留说明，复杂对象不整体展开配置。调用栈不进入公开响应，也不套用短原因截断。响应已写出后的 panic 仍只补诊断，不修改原响应；HEAD、原生流／下载和非自有 JSON 路由保持向外抛出的既有行为。

115 QR/OAuth 的用户信息校验使用本次新凭据，失败诊断同时用账号旧凭据和本次新 access token、refresh token 脱敏，不能只传尚未更新的账号字段。公共正文保持安全说明，原始错误链只供关联诊断使用。

OpenList 创建／编辑的临时验证失败通过 `OpenListVerificationError` 携带原始原因与诊断用 `Secrets`。秘密值包括候选密码／Token、用户信息请求开始时的 Token，以及请求内自动刷新后的 Token；普通用户名保留为诊断信息。控制器将这些值追加到草稿和旧账号凭据后交给公共诊断出口，不为日志提前保存验证结果。SDK 错误链携带本次实际使用的日志凭据，公共诊断复用它们，不在请求结束后仅靠最新缓存猜测旧凭据。

诊断复用 QLogger 和已有脱敏能力：预期请求拒绝记 `Info`，数据库等内部故障记 `Error`，`Debug` 只补充过程。默认 `info` 等级下可按实例查到失败关联记录；用户调高等级后遵循现有过滤规则，被过滤的记录不保证仍可检索，不强制绕过等级。首次数据库向导早于 AppLogger 初始化时沿用安全的启动期标准日志。

## 前端消费

前端保持 `http → api → composable/store → 页面`：公共层验证包络字段和正文／HTTP 状态一致性，完整保留成功的 `data/message/warnings`。领域查询提取业务数据，需反馈的操作保留消息与警告，不把 Axios 原始响应推到页面。

失败只解析新顶层字段，保留原始错误响应供领域读取部分结果。合法自有包络的安全 `message` 可展示，包括 `5xx`；HTML、非包络网关错误、传输失败和程序异常使用安全回退。取消、超时、会话批次和恢复回执处理遵循 [前端约定](../engineering/frontend-development.md#api-响应与请求错误)，不自动重发写入。

## 不变量

- 正文 `status` 与真实 HTTP 状态一致；成功数据和失败的明确部分结果不丢失。
- 请求错误、任务结果和提交后应用警告分别表达，文案不承担机器判断。
- 登录说明不泄露凭据匹配细节；内部或上游故障不误清本系统会话。
- 公共输出和诊断只保留一套；原生协议不被全局改写。

## 验证方式

公共 writer 的状态、合法空值、消息警告、字段定位、默认等级实例关联、脱敏和编码回退由 `internal/httpapi` 及主要控制器用例保护；真实 Gin 路由覆盖 CORS、未找到、方法拒绝和原生协议。前端公共解析与领域用例验证安全反馈及部分结果，首次向导验证独立 fetch 消费。

具体命令与同构建基本成品联调见 [验证说明](../engineering/verification.md#api-响应验证)。共享格式由公共用例验证，领域用例保留长期行为和安全覆盖，不逐路由复制相同包络断言。
