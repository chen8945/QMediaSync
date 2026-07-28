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

`QMS_UNSAFE_SENSITIVE_LOG=1` 只在本地调试时临时启用 `SensitiveDebug` 日志；它可能写出 API Key、Token、Cookie 或密码，不能在生产环境长期使用或分享相关日志。`backend/emby302.yaml` 默认关闭 ANSI 颜色，避免控制字符进入日志。

## 第三方密钥与本机敏感数据

- 115 开放平台 APP ID、TMDB API Key / Access Token、OpenAI 兼容 API Key 和 fanart.tv API Key 可以在 Web 设置中配置。
- 默认密钥也可由 `backend/main.go` 的变量、ldflags 或环境变量 / `config/.env` 注入。`FANART_API_KEY`、`TMDB_API_KEY`、`TMDB_ACCESS_TOKEN` 和 `SC_API_KEY` 的优先级是 Web UI > 环境变量 / `config/.env` > ldflags；`config/.env` 覆盖真实环境变量。
- 两步验证等本机敏感数据使用首次启动自动生成的 `config/encryption.key`。`jwtSecret` 为空或仍为公开默认值时会生成 32 字节随机密钥并写回配置；修改它会使现有登录 Cookie 失效。
- v1 备份以同一 `config/encryption.key` 绑定实例：工件内该密钥、工件头部指纹和当前实例必须匹配才可继续验证或恢复。定时备份密码密文也依赖这把本机密钥；只复制数据库或单独替换此文件都会使相关密文和工件不可用。`config/encryption.key`、未加密备份中的该文件及其中可能包含的 TLS 私钥都必须按凭据材料保护，不能写入日志或交给不受信任方。
- OAuth 中转使用 `OAUTH_RELAY_ENCRYPTION_KEY`，可由 `main.OAuthRelayEncryptionKey` ldflags 或环境变量 / `config/.env` 注入，环境变量优先。

浏览器 Cookie、CSRF、初始化管理员、API Key 和可信来源等安全契约见 [认证会话](../architecture/authentication-sessions.md)。
