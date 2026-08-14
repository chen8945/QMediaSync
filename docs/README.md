# 文档索引

本目录存放面向维护者和 AI 的正式文档。先按改动职责定位权威文档，再阅读相关实现、调用方和测试。

正式文档优先记录稳定概念、边界、流程、表格和路径；具体实现以源码和对应链接为准。文档职责、命名、契约和迁移规则见 [文档治理](engineering/documentation-governance.md)。

## 架构契约

- [认证与浏览器会话](architecture/authentication-sessions.md)：首次管理员、Cookie、CSRF、API Key、可信来源和下载代理安全边界。
- [实时事件（SSE）](architecture/realtime-events.md)：全局事件、日志流、任务详情快照和回放边界。
- [上传与 STRM 处理](architecture/upload-and-strm-processing.md)：115 上传、目录监控、STRM 后处理、源文件清理和上传后刷新。
- [STRM 同步调度与任务记录](architecture/sync-orchestration.md)：同步目录、Cron、按来源队列、`sync` 记录、取消和完成后的下游协作。
- [Emby 媒体库同步](architecture/emby-library-sync.md)：Emby 刷新、条目同步、Webhook 同步和协调器边界。

## 工程维护

- [AI 编码助手工作说明](engineering/ai-assistant.md)：完整 AI 协作规则、开发约定、验证入口和文档同步映射。
- [文档治理](engineering/documentation-governance.md)：正式文档职责、命名、唯一权威来源和迁移规则。
- [本地开发](engineering/local-development.md)：本地后端、前端启动和调试入口。
- [前端开发约定](engineering/frontend-development.md)：HTTP 客户端、状态刷新、路由、响应式布局和交互反馈。
- [仓库结构](engineering/repository-structure.md)：顶层目录和构建产物职责。
- [请求校验约定](engineering/request-validation.md)：Request DTO、`Validate()`、绑定和前后端校验边界。
- [注释规范](engineering/comment-guidelines.md)：Go、Vue、Swagger 和验证代码的注释边界。
- [验证说明](engineering/verification.md)：改动类型到最小验证的映射与稳定验证规则。

## 运行维护

- [部署与持久化](operations/deployment.md)：Docker、发布二进制和飞牛运行方式，以及端口、挂载目录和数据保留边界。
- [配置、密钥与日志](operations/configuration.md)：配置文件、端口、第三方密钥、日志和运行参数。
- [反向代理与 SSE](operations/reverse-proxy.md)：同源部署、可信代理和 SSE 缓冲 / 超时配置。
- [数据库运维](operations/database.md)：数据库初始化、修复、清库、备份和恢复。
- [发布流程](operations/release.md)：版本发布、GitHub Actions、changelog 和 FPK 打包。

## 参考资料

- [数据库 schema 与迁移](reference/database-schema.md)：表、字段、索引、时间策略、稳定存储值和迁移版本。
- [账号授权与更换](reference/account-authorization.md)：账号关联、同来源校验、授权会话、QR/OAuth 传递和原子落库契约。
- [同步目录聚合 API](reference/sync-path-api.md)：同步目录和目录监控上传规则的原子写入、幂等与错误契约。
- [STRM Webhook](reference/strm-webhook.md)：外部程序创建 STRM 任务的 API、字段、响应和幂等边界。
- [任务来源](reference/task-sources.md)：下载、上传和同步队列的机器值与前端映射。
- [完整配置示例](examples/config.yaml)：`config/config.yaml` 的主配置项和注释。
- [变更日志](../CHANGELOG.md)：历史版本的功能、修复和性能变更。

## 代码内文档

- [123 云盘客户端接口](../backend/internal/open123/README.md)：`open123` 包当前导出的客户端能力和限制。
- [前端工具函数目录](../frontend/src/utils/README.md)：`frontend/src/utils` 下跨组件复用工具的说明。
