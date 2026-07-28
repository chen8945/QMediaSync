# 认证与浏览器会话

> 职责：定义首次管理员、浏览器 Cookie 会话、CSRF、API Key、可信来源和本地下载代理的安全边界。
>
> 权威范围：本文档是认证、会话和 API Key 行为的唯一说明；运行配置和密钥来源见 [配置、密钥与日志](../operations/configuration.md)。
>
> 修改时机：修改登录、注销、会话撤销、两步验证、Cookie、CSRF、可信来源、API Key 或下载代理鉴权时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/users.go`、`backend/internal/controllers/auth_*.go`、`backend/internal/controllers/csrf.go`、`backend/internal/controllers/api_key.go`、`frontend/src/stores/auth.ts`。

## 首次管理员

`config.yaml` 不保存管理员用户名和密码。数据库初始化后如果 `users` 表为空，程序会生成一次性初始化码并写入启动日志。登录页使用初始化码、用户名和密码创建首个管理员；成功后初始化码立即失效。重启且仍未创建管理员时会生成新的初始化码。

用户名去除首尾空白后必须为 3 到 20 个英文或数字字符；密码至少 6 个字符，不能是纯数字或纯字母，修改时不得与当前密码相同。密码使用 bcrypt 成本 `12` 哈希保存，旧成本哈希在下次成功登录后升级。初始化必须在可信网络内完成。

登录失败（包括用户名、密码和 TOTP 不匹配）对客户端统一返回“登录失败”，避免枚举凭据状态。登录限流按“客户端 IP + 去除首尾空白并转小写后的用户名”统计：15 分钟窗口内累计 5 次失败后锁定 15 分钟，成功登录会清除该计数；被锁定时返回 HTTP `429` 和剩余等待秒数。限流状态只保存在当前进程内，多实例部署或进程重启不会共享该状态。

## TOTP 两步验证

两步验证采用基于时间的一次性密码（TOTP）。`POST /api/login` 始终接收 `totp_code`；只有用户同时满足 `two_factor_enabled=true` 且存在有效加密密钥时，密码校验成功后才要求该验证码。

- `POST /api/user/two-factor/setup` 为当前用户生成新的密钥和 `otpauth_url`，将密钥以本机 `config/encryption.key` 加密后保存为待确认值；明文只在这次响应中返回。
- `POST /api/user/two-factor/enable` 必须用待确认密钥生成的有效验证码确认，随后将待确认密钥转为生效密钥并清空待确认值。
- `POST /api/user/two-factor/disable` 必须同时提供当前密码和当前有效验证码，成功后清空生效与待确认密钥。
- 启用或关闭两步验证会撤销其他浏览器会话，保留执行操作的当前会话；项目当前不提供恢复码或绕过两步验证的备用登录方式。

两步验证密钥是实例本地敏感数据，不能写入日志、API Key、环境变量或数据库明文列。丢失 `config/encryption.key` 会使已加密的密钥无法解密，不能通过文档或代码假定为可恢复。

## 浏览器会话与 CSRF

- 登录使用 `auth_token` HttpOnly Cookie，不在 Web Storage 保存 JWT。Cookie 使用 `Path=/`、`SameSite=Lax` 和 Host-only 范围；HTTPS 使用 `Secure`。
- `csrf_token` Cookie 可由前端读取；`POST`、`PUT`、`PATCH`、`DELETE` 通过 `X-CSRF-Token` 发送，服务端同时校验请求来源和 session 中的 CSRF 哈希。
- `user_sessions` 控制会话有效性。退出、用户名或密码修改、两步验证变更和登录设备撤销都会更新该表；修改用户名或密码时在同一事务撤销全部浏览器会话并清除当前 Cookie；两步验证变更只撤销其他设备，保留当前会话。
- 单进程下登录会话创建与凭据修改串行处理，避免旧凭据在修改完成后创建会话。多实例共享数据库时需要跨实例锁或凭据版本机制。
- 已撤销会话保留审计，不显示在设备列表。当前设备排首位，其他设备按最后活跃时间倒序。
- 主动退出调用 `POST /api/logout` 撤销服务端会话；业务请求收到 `401` 时前端只清理状态、关闭实时连接并跳转登录，不再次调用 logout。CSRF 失败返回 `403`。

`GET /api/session` 是会话状态查询。无 Cookie、无效或过期 JWT、已撤销会话均返回 `200` 和 `data.authenticated=false`；有效会话返回用户、会话和 CSRF 数据，始终设置 `Cache-Control: no-store, private`。内部故障返回 `5xx`，不得伪装匿名状态。登录成功后前端先调用该接口确认 Cookie；只有明确返回未认证时才提示 Cookie 问题。

## 备份操作状态能力

`GET /api/backup/status` 是备份或恢复操作页的独立状态查询，注册在 JWT/API Key 认证之前，以便维护屏障关闭业务认证链后仍能读取终态。它不是浏览器会话或 API Key 的替代：请求必须同时带 `operation_id` 查询参数和仅在受理响应中出现一次的 `X-Backup-Operation-Token` 请求头，令牌不能写入 URL、Cookie、Web Storage、日志或普通 axios 鉴权链。该接口只读取 `config/state/backup/` 的受限状态文件，响应为 `Cache-Control: no-store`；自动回滚失败的只读诊断模式仍执行相同的双凭据校验。

## API Key、可信来源与下载代理

- API Key 接受 `X-API-Key` 或 `?api_key=`，不需要 CSRF。`/emby/webhook` 默认鉴权，优先 header，保留查询参数兼容只能配置 URL 的 Emby Webhook。
- 创建时生成 `qms_` 前缀加 24 位随机字符的完整密钥，只在创建响应中返回一次。数据库只保存 SHA256 `key_hash`、前 8 位 `key_prefix`、状态和时间字段，不保存明文。
- CORS 和 CSRF 共享可信来源判断。默认允许 Vite 的 `localhost:5173`、`127.0.0.1:5173` 和 `[::1]:5173`；跨源部署通过 `trustedOrigins` 配置精确的 `scheme://host[:port]`。
- 反向代理必须保留原始 `Host`，由可信代理传递 `X-Forwarded-Proto: https`；后端 HTTP 监听不得直接暴露，以防客户端伪造该 header。具体代理配置见 [反向代理](../operations/reverse-proxy.md)。
- `/proxy-115` 仅允许 115 CDN 和百度网盘下载域名；初始目标和每次重定向目标都执行同一白名单校验。

## 不变量

- 浏览器认证只使用 HttpOnly Cookie，会话状态以服务端 `user_sessions` 为准；前端状态不能替代鉴权。
- 登录失败不得向客户端区分用户名、密码或 TOTP 错误；进程内限流键必须同时包含客户端 IP 和规范化用户名。
- 凭据变更必须撤销所有浏览器会话，API Key 不受该撤销规则影响。
- 待确认的 TOTP 密钥不得用于登录；启用和关闭两步验证时都必须重新验证当前用户的敏感凭据，并且不得泄露 TOTP 密钥。
- 未认证的 `/api/session` 是正常匿名状态，必须返回 `200` 与 `authenticated=false`，不能返回伪造的认证错误。
- API Key 明文只能在创建响应出现一次，日志和数据库不得保存完整值。
- 备份操作令牌不是登录凭据：明文只能在受理响应出现一次，持久化时只保存其 SHA-256，状态查询必须同时校验操作 ID 和请求头令牌。
- 跨源部署必须显式配置可信来源，SSE 不作为跨源 Cookie 通道。

## 验证方式

- 运行 `(cd backend && go test ./internal/controllers/ -run 'Test.*(Session|Auth|CSRF|APIKey|Credential|TwoFactor|RateLimiter)')`、`(cd backend && go test ./internal/helpers/ -run TestTOTP)` 覆盖认证、会话、CSRF、API Key、两步验证、限流和凭据变更场景。
- 运行 `(cd frontend && pnpm lint)`、`(cd frontend && pnpm run type-check)` 检查前端认证调用改动。
- 代理或 Cookie 部署改动在 HTTPS 测试环境检查 `Set-Cookie`、`X-Forwarded-Proto` 和可信来源行为；真实域名与证书配置无法在单元测试中覆盖时，在变更说明中记录。
