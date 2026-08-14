# 账号授权与更换

> 职责：定义云盘账号授权、更换授权的接口、来源边界、短时会话和落库语义。
>
> 权威范围：本文档维护 115 账号更换授权的跨后端、前端和数据库契约；账号字段和迁移以 [数据库 schema 与迁移](database-schema.md) 为准，通用请求校验以 [请求校验约定](../engineering/request-validation.md) 为准。
>
> 修改时机：修改账号授权入口、`authorization_id` 传递、授权来源兼容性、确认提示、原子更新或失败回滚边界时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/account.go`、`backend/internal/controllers/open115.go`、`backend/internal/requests/accounts.go`、`backend/internal/requests/connections.go`、`backend/internal/v115auth/`、`backend/internal/models/account.go`、`frontend/src/components/AppCloudAccounts.vue`、`frontend/src/components/cloud-auth/`、`frontend/src/composables/useV115DeviceAuthorization.ts`。

## 账号关联语义

账号表的 `id` 是本地关联的稳定主键。同步目录、刮削目录、任务历史和同步文件等数据只关联这个 ID；成功更换授权不会创建新账号，也不会迁移或复制这些记录。

更换授权只允许在原 `source_type` 内进行。当前完整 UI 覆盖 115：115 账号可以选择有效的 115 APP ID、内置中转或受支持的第三方服务，但不能切换为百度网盘、OpenList 或其他来源。已废弃来源仍可解析和展示已有账号，但不能作为新建或更换目标；已有账号仍保留不带 `authorization_id` 的普通授权/重新授权入口，以兼容历史账号。

新授权的 `user_id` 不需要等于旧值，但非空 `user_id` 在本地账号表中必须唯一；`name` 也必须唯一。临时账号尚未完成授权时允许使用空值，不会因为空 `name` 或空 `user_id` 互相冲突。已有账号按 `account_id` 复用，不会按 `user_id` 自动合并或迁移。

## 准备更换授权

受保护接口：`POST /api/account/authorization/prepare`。

请求体：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `account_id` | 是 | 已存在的原账号 ID。 |
| `source_type` | 是 | 目标网盘来源，必须与原账号一致；当前更换流程实际支持 `115`。 |
| `auth_source_type` | 115 必填 | `built_in_appid`、`built_in_relay`、`third_party_service` 或 `custom_appid`。 |
| `auth_provider` | 115 必填 | 与目标来源匹配的 provider，例如 `official_pkce`、`qmediasync`、`moviepilot`、`clouddrive`。 |
| `app_id` | 视来源 | 目标 APP ID；内置中转可以使用其默认值。 |
| `app_id_name` | 视来源 | 目标应用显示名。 |
| `custom_app_name` | 自定义 APP ID 时 | 自定义应用显示名。 |
| `confirmed` | 是 | 必须为 `true`，表示用户已完成风险确认。 |

成功响应的 `data` 只包含短时会话信息，不包含访问凭据：

```json
{
  "authorization_id": "随机字符串",
  "expires_in": 600
}
```

服务端会把账号 ID、已校验的目标来源、创建时间和过期时间保存在内存中。每个账号同时只允许一个未完成的更换会话；重复准备请求会被拒绝，前端开始新的 QR/OAuth 流程前必须先停止并取消旧流程。准备会话成功后，同时清理该账号不带 `authorization_id` 的旧二维码和 OAuth 状态；即使旧请求已经在远端飞行中，最终提交前也会再次检查状态和账号会话，不能在更换流程之后回写。会话默认 600 秒有效，成功原子提交后立即消费；过期、服务重启或用户取消只会使本次流程失效，不会修改原账号。`POST /api/account/authorization/cancel` 可按账号 ID 和会话 ID 取消流程；取消会同时清理对应的 QR/OAuth 临时状态，包含已进入用户信息查询阶段但尚未提交的会话。最终写库前会 claim 会话，取消与提交在同一进程锁上排队，取消先获得锁时不会执行写库。

## 授权流程传递

准备会话成功后，前端把 `authorization_id` 原样传给对应授权流程。后端不信任后续请求中自行修改的应用字段，而是从会话读取目标来源。

### QR 授权

- `POST /api/auth/115-qrcode-open` 接收 `account_id` 和可选 `authorization_id`。
- `POST /api/auth/115-qrcode-status` 接收 `account_id`、`uid` 和可选 `authorization_id`。
- 二维码状态绑定账号、二维码 UID 和会话 ID，有效期为 300 秒；轮询请求的会话 ID 必须与生成二维码时一致。
- 二维码状态过期或取消后会从内存清理；用户关闭二维码弹窗时前端会使飞行中的请求失效并通知后端取消会话。准备新的更换会话会清理旧的普通二维码状态，旧状态的最终提交和会话失效操作互斥。
- 更换流程使用目标 APP ID 创建临时客户端，不提前覆盖旧账号的缓存客户端或数据库字段。页面刷新或关闭时即使卸载通知未送达，重新进入账号页也会读取当前标签页的暂存会话并回收服务端状态。

### OAuth 授权

- `GET /api/115/oauth-url` 接收 `account_id`、`redirect_url` 和可选 `authorization_id`。
- `GET /api/115/oauth-status` 接收 `account_id`、`state` 和可选 `authorization_id`；服务端同时校验 OAuth 状态的账号、provider 和目标会话。
- OAuth 轮询状态一次只允许一个请求 claim；未完成或外部请求失败会释放 claim，拿到令牌后要等原子保存成功才消费 state，避免并发请求重复写入。
- 准备更换授权时会删除同账号旧的无会话 OAuth state；旧轮询即使已拿到令牌，也必须在保存前确认 state 仍存在，并通过无会话提交锁检查账号没有活动的更换会话。
- `POST /api/115/oauth-confirm` 接收 `account_id`、`data` 或 `payload`，并可接收 `authorization_id`。回调中的会话 ID 若存在，必须与请求中的值一致。
- OAuth provider 会把会话 ID 放进受保护的状态或回调参数；前端只回传状态和回调字段，不构造目标来源或令牌。
- Relay/CloudDrive 这类直接跳转 OAuth 在离开页面前会把待处理的账号 ID 和会话 ID 暂存在当前标签页的 `sessionStorage`；返回时只有带回相同会话 ID 的回调才继续确认。无回调、会话 ID 不匹配或确认失败时取消并清理该会话，成功确认后清理暂存值。

不带 `authorization_id` 的请求继续执行新建账号和原有重新授权路径，保持兼容。

## 原子落库

授权控制器先使用目标来源和新令牌请求 115 用户信息。只有用户信息请求成功后，`Account.ReplaceV115Authorization` 才在一个数据库事务中更新以下字段：

- `app_id`、`app_id_name`；
- `auth_source_type`、`auth_provider`；
- `token`、`refresh_token`、`token_expiries_time`；
- `user_id`、`username`、`token_failed_reason`。

事务不更新 `id`、`name` 或任何同步、刮削、任务关联表。事务提交成功后才刷新按账号 ID 缓存的 115 客户端，并消费 `authorization_id`。目标令牌无效、用户信息请求失败、事务失败、取消或超时都会保留旧授权的来源、应用、令牌和用户信息。

共享 115 客户端命中已有账号 ID 时必须同时更新 `AppId` 和令牌；待授权校验使用不进入共享缓存的临时客户端。

百度网盘 OAuth 也遵循相同的失败保护：先使用新 access token 的临时客户端获取用户信息，再在一个事务中同时写入新凭据和 `user_id`/`username`。如果用户 ID 唯一性冲突或事务失败，旧凭据和旧用户信息保持不变。

## 前端确认

所有 115 账号卡片都提供“授权/重新授权”和“更换授权”入口，未授权或授权失效的账号也可以直接选择新的有效授权来源。目标选择复用新建账号的应用选择器，因此已废弃 APP ID 不进入新建或更换目标列表；历史账号的普通授权入口仍保留，用于兼容旧来源。提交准备接口前，弹窗必须要求用户勾选确认，并明确说明：

- 原账号 ID、STRM 同步目录、刮削目录和任务历史会继续保留；
- 如果新授权属于其他 115 用户，旧路径或文件 ID 可能不再对应，后续同步和刮削可能失败。

取消、失败和超时只关闭或结束当前授权流程，不把临时目标来源写入账号展示，也不创建第二个账号。QR/OAuth 轮询在页面隐藏时暂停，恢复可见时继续；普通页面卸载、失败或超时会停止后续轮询并取消替换会话。直接跳转 OAuth 为了让正常回调继续，会在重定向期间保留暂存会话，回到页面后再按回调结果取消或消费；QR/OAuth 轮询更换流程在刷新或关闭后重新进入页面时也会按暂存会话回收。启动任一新的授权入口前，前端会先停止并取消已有流程，避免同一账号的旧结果覆盖新授权。

## 不变量

- `account_id` 是更换授权前后相同的本地关联主键。
- 更换目标的 `source_type` 必须与原账号一致；已废弃来源不能作为带 `authorization_id` 的更换目标。
- 后端必须校验 `confirmed=true`；前端复选框不能替代后端校验。
- 新 `user_id` 可以不同，但非空值必须唯一；重复用户 ID 会拒绝本次提交且保留旧授权，不会查询、合并或修改其他账号。
- `name` 和非空 `user_id` 由数据库部分唯一索引和模型层预检查共同约束；空值只用于未完成授权的临时账号。
- 同一 OAuth state 和同一授权会话只能成功消费一次。
- 同一账号同时只能有一个未完成的更换会话；新授权入口必须先取消旧 QR/OAuth 流程。
- 更换会话创建成功后，旧的普通二维码状态不能在新授权提交后再次写入账号。
- 更换会话创建成功后，旧的无会话 OAuth state 不能在新授权提交后再次写入账号；无会话旧授权提交必须通过同一账号会话锁。
- 直接跳转 OAuth 的待处理会话在页面返回时必须与回调中的会话 ID 匹配；无回调或失败回调不能留下活动会话。
- 授权结果必须在新令牌和用户信息都验证成功后原子写入；失败不能产生部分授权更新。
- 不带 `authorization_id` 的既有授权请求保持原有行为。
- 历史废弃来源仅禁止新建和带会话的更换目标，不阻断已有账号的无会话普通授权/重新授权入口。

## 验证方式

- 后端：`cd backend && go test ./internal/requests ./internal/v115auth ./internal/v115open ./internal/models ./internal/controllers`。
- 前端：`cd frontend && pnpm run test`、`pnpm run type-check`、`pnpm run build`、`pnpm run check:build`。
- 契约测试位置：`backend/internal/v115auth/authorization_state_test.go`、`backend/internal/controllers/account_test.go`、`backend/internal/controllers/open115_auth_state_test.go`、`backend/internal/models/account_test.go`、`backend/internal/v115open/client_test.go`、`frontend/test/components/cloud-auth/V115AuthorizationChangeDialog.test.ts`、`frontend/test/composables/useV115DeviceAuthorization.test.ts`、`frontend/test/utils/v115AuthorizationSession.test.ts`。
- 前端授权生命周期回归：`frontend/test/regression/account-dialog-responsive.test.ts` 覆盖新入口取消旧流程和 OAuth 隐藏页面暂停约定。
- 真实 115 二维码、第三方 OAuth 和跨用户远端路径有效性需要人工使用可撤销测试账号验证；自动化测试不访问真实云盘，也不能证明新用户下旧远端 ID 仍然存在。
