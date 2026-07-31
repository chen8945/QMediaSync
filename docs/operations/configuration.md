# 配置、密钥与日志

> 职责：说明 QMediaSync 的运行配置、第三方密钥、日志和与配置相关的运行时限制。
>
> 权威范围：本文档维护配置文件、端口、密钥优先级、日志和运行参数；浏览器认证见 [认证会话](../architecture/authentication-sessions.md)，反向代理见 [反向代理](reverse-proxy.md)。
>
> 修改时机：修改配置字段、默认值、密钥来源、日志行为、Emby 302 TLS 选项或运行时监控指标时必须更新本文档和 `docs/examples/config.yaml`。
>
> 相关代码：`backend/internal/helpers/config.go`、`backend/internal/helpers/logger.go`、`backend/main.go`、`backend/emby302.yaml`、`docs/examples/config.yaml`。

## 配置文件与默认端口

- 主配置为 `config/config.yaml`，兼容旧 `config.yml`。首次启动缺少主配置时会启动配置向导，当前可选择 SQLite 或外部 PostgreSQL，保存后生成 `config/config.yaml`。
- Web 默认端口：HTTP `12333`、HTTPS `12332`；Emby 302 代理默认端口：HTTP `8095`、HTTPS `8094`。
- 完整字段示例见 [config.yaml](../examples/config.yaml)。示例仅说明字段，运行时以 `config/config.yaml` 为准。
- 代码默认数据库配置为 `postgres + embedded`。Docker 镜像安装 `postgresql15`；裸二进制和本地开发环境不携带 PostgreSQL 二进制，使用 PostgreSQL 时应安装 PostgreSQL 15 及以上、配置外部数据库，或自行保证内嵌模式依赖的命令可用。
- 数据库引擎、备份恢复和修复操作见 [数据库运维](database.md)；表、版本和迁移语义见 [数据库 schema 与迁移](../reference/database-schema.md)。

## 115 运行参数

- 首页「115 接口监控」的请求数、QPS、QPM、QPH、平均响应时间和限流次数来自 `request_stats` 表，重启后仍按时间窗口聚合展示。
- 当前是否限流、等待时间和剩余时间来自进程内 115 请求队列管理器。限流暂停时长为 1 分钟，重启后恢复为未限流。
- 秒传等待策略保存于 `settings`，由 `upload_rapid_wait_interval_seconds`、`upload_rapid_wait_timeout_seconds`、`upload_rapid_wait_min_size`、`upload_rapid_wait_force_size` 和 `upload_rapid_wait_skip_upload` 控制。间隔只控制重试频率，超时字段才是最大等待上限。
- 115 直链缓存有效性检查保存于 `settings`，默认开启，默认总超时为 3 秒、范围为 1 到 9 秒。它只影响缓存 URL 的 HEAD 检查；百度网盘和 OpenList 不使用这套机制。
- 上传协议、目录监控、断点续传、远端已存在和 STRM 后处理的状态边界见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。

## Emby 302 出站 HTTPS

Emby 302 代理访问 Emby、OpenList、m3u8 和下载资源时默认校验证书，并复用共享 HTTP client 的空闲连接。仅在受控内网自签名证书或临时排障场景下，才设置：

```yaml
emby302:
  insecure_skip_verify: true
```

启用 `emby302.insecure_skip_verify` 后，出站 HTTPS 请求会接受无法验证的证书，程序会写入风险提示日志。该模式存在中间人攻击风险，不适合公网或长期生产环境。

## 出站代理

出站代理地址保存在 `settings.http_proxy`，支持 `http`、`https`、`socks5` 和 `socks5h`，可带 `用户名:密码@` 凭据。它由 Web 设置的「出站代理」保存，不读取环境变量。

保存代理时必须维护以下不变量：

- 只有写库成功后才更新内存全局值 `models.SettingsGlobal.HttpProxy`。写库失败必须还原旧值：GORM 的 `Updates(map)` 在生成 SQL 阶段就会把 map 里的值回写进模型字段，因此仅调整赋值顺序不够，`models.Settings.UpdateHttpProxy` 显式保存并回滚旧值。若不回滚，内存持新地址而数据库、GitHub 管理器和通知管理器仍持旧地址，接口却已报告保存失败，重启后又静默回退。
- 保存成功后必须刷新所有直读生效值的下游客户端，由 `models.RefreshProxyConsumers` 统一完成：`helpers.HTTP_PROXY`（Fanart 客户端每次构造都直读）和 TMDB 全局单例 `tmdb.GlobalTmdbClient`。刮削设置里的「是否启用 TMDB 代理」开关同样是 `ScrapeSettings.GetProxyUrl` 的入参，改动后也要刷新。
- 清空代理或关闭代理开关时，TMDB 单例必须调用 resty 的 `RemoveProxy()` 显式清除；只在地址非空时 `SetProxy` 会让"清空"变成空操作，请求会继续带 API Key 走用户已撤销的隧道。

`GET /setting/http-proxy` 一律回传脱敏地址，不回传明文凭据，任何 JWT 或 API Key 持有者都读不到代理密码。响应额外带 `credentials_masked`（`"1"` 表示地址里的凭据已被遮蔽），前端据此提示输入框里的 `xxxxx` 是占位串。

这带来一个必须成对维护的回环：前端把回传值直接载入输入框，并在尚未编辑脱敏值时自动提交 `preserve_proxy_credentials=true`；一旦用户编辑输入框则提交 `false`。保存和测试接口只有在提交地址与当前已存地址的端点一致时，才以该标志保留当前用户名和密码。端点由协议和 `host:port`（不区分大小写）组成，User 信息、路径和查询参数不参与比较；协议、主机或端口改变时忽略保留标志，绝不把已存凭据转发给新端点。因此 `xxxxx` 始终可以作为真实凭据保存，不再从最终字符串猜测用户意图。为兼容未升级的前端，缺少该字段时仍按旧的脱敏字符串回传规则处理；新的 API 调用方必须显式提交该字段。

代理地址写日志或回传接口前的脱敏要求见下一节。

## 日志行为与脱敏

日志路径由 `config/config.yaml` 的 `log` 配置决定，默认相对于配置目录：

| 配置项 | 默认值 | 用途 |
| --- | --- | --- |
| `log.level` | `info` | 可选 `debug`、`info`、`warn`、`error` |
| `log.maxSizeMB` | `10` | 单个轮转日志最大大小，单位 MB，范围 1 到 1024 |
| `log.maxBackups` | `3` | 每个日志的轮转备份数，范围 1 到 100 |
| `log.maxAgeDays` | `7` | 轮转备份最长保留天数，范围 1 到 365 |
| `log.app` | `logs/app.log` | 主程序日志 |
| `log.v115` | `logs/115.log` | 115 请求和队列日志 |
| `log.openList` | `logs/openList.log` | OpenList 日志 |
| `log.tmdb` | `logs/tmdb.log` | TMDB 日志 |
| `log.baiduPan` | `logs/baidupan.log` | 百度网盘日志 |
| `log.web` | `logs/web.log` | 预留 Web 日志配置 |
| `log.syncLogDir` | `logs/sync` | 同步任务独立日志目录 |

历史 `log.file` 仍可读取；当 `log.app` 为空且 `log.file` 有值时使用旧路径，新保存统一写 `log.app`。全局日志按写入触发轮转并压缩旧文件；同步任务日志不轮转，随同步记录清理删除。`QLogger` 在写入前脱敏 `api_key`、Token、Cookie、密码、STS 密钥等常见敏感字段，脱敏值统一显示为 `******`。

`QLogger` 的脱敏基于键值对匹配，不识别 URL 里的 `用户名:密码@` 段。凡是可能带凭据的 URL（例如出站代理地址），必须在调用点用 `validation.RedactProxyURL`（入参为原始字符串）或 `validation.RedactParsedProxyURL`（入参为已解析的 `*url.URL`）处理后再写日志或回传接口，不能依赖 `QLogger` 兜底。

不要使用标准库的 `url.URL.Redacted()`：它只替换密码，用户名仍是明文（企业代理常带域账号，形如 `http://DOMAIN\jsmith:pw@proxy.corp:8080`）；并且它对缺少 `//` 的 opaque 地址（形如 `socks5:user:secret@host:1080`）完全不生效，会把密码原样输出。上面两个函数同时遮蔽用户名和密码，覆盖 opaque 地址，并在 `url.Parse` 失败时返回占位符而不是原串。

`url.Parse` 失败时也不能直接外抛 `url.Error`，它的 `Error()` 会渲染成 `parse "<整个原始地址>"`，等于把凭据原样写进日志或接口响应，统一用 `validation.ProxyParseError` 剥掉原串。

这三个函数放在 `internal/validation`（纯 stdlib 叶子包）而不是 `helpers`：`helpers/net.go` 已导入 `internal/github`，`github` 无法反向引用 `helpers`。需要脱敏代理地址的包一律引用 `validation`，不得再复制一份实现。

`GET /setting/notification/channels/telegram/{id}` 回传的 `config.proxy_url` 同样脱敏：该字段由历史迁移从 `settings.http_proxy` 复制而来，可能带凭据。凡是把含 `proxy_url` 的配置结构体整体写进响应的接口都要先脱敏。

`QMS_UNSAFE_SENSITIVE_LOG=1` 只在本地调试时临时启用 `SensitiveDebug` 日志；它可能写出 API Key、Token、Cookie 或密码，不能在生产环境长期使用或分享相关日志。`backend/emby302.yaml` 默认关闭 ANSI 颜色，避免控制字符进入日志。

## 第三方密钥与本机敏感数据

- 115 开放平台 APP ID、TMDB API Key / Access Token、OpenAI 兼容 API Key 和 fanart.tv API Key 可以在 Web 设置中配置。
- 默认密钥也可由 `backend/main.go` 的变量、ldflags 或环境变量 / `config/.env` 注入。`FANART_API_KEY`、`TMDB_API_KEY`、`TMDB_ACCESS_TOKEN` 和 `SC_API_KEY` 的优先级是 Web UI > 环境变量 / `config/.env` > ldflags；`config/.env` 覆盖真实环境变量。
- 两步验证等本机敏感数据使用首次启动自动生成的 `config/encryption.key`。`jwtSecret` 为空或仍为公开默认值时会生成 32 字节随机密钥并写回配置；修改它会使现有登录 Cookie 失效。
- OAuth 中转使用 `OAUTH_RELAY_ENCRYPTION_KEY`，可由 `main.OAuthRelayEncryptionKey` ldflags 或环境变量 / `config/.env` 注入，环境变量优先。

浏览器 Cookie、CSRF、初始化管理员、API Key 和可信来源等安全契约见 [认证会话](../architecture/authentication-sessions.md)。
