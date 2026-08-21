# AI 编码助手工作说明

> 职责：定义 QMediaSync 的完整 AI 协作规则、开发约定、验证入口和文档同步方式。
>
> 权威范围：`AGENTS.md` 和 `CLAUDE.md` 只作为完全相同的兼容入口；本文件是 AI 协作规则的唯一完整来源。具体业务事实以其所属的架构、运维或参考文档和源码为准。
>
> 修改时机：修改 AI 入口、开发约定、构建与验证命令、代码组织、密钥策略或文档同步规则时必须更新本文档。
>
> 相关代码：`backend/`、`frontend/`、`docs/`、`.github/workflows/`、`scripts/`。

## 协作与文档

- 优先遵循用户当前请求；请求未明确细节时，按现有代码和本文档做保守实现。
- 修改前先阅读相关权威文档、代码、调用方和测试；索引不能定位时检索仓库，理解真实行为后再修改。
- 保持改动聚焦在用户请求范围内，不做无关重构、格式化或依赖更新。
- 未经用户明确要求，不得把功能修改与重构混在一起。重构必须保持可观察行为、公开接口、错误响应和数据语义不变，并由相关验证证明。
- 不得擅自增加需求外的功能、配置项、接口、依赖或交互。发现值得处理的相邻问题时，在最终回复中说明，不直接纳入本次改动。
- 实现新行为时，识别与本次改动相关的无效输入、空值、零值、权限、状态冲突、并发、外部服务失败和兼容性边界；沿用所在模块既有的错误响应和日志模式，不为无关场景过度设计。
- 修改现有文件时优先做局部、可审查的改动。只有用户明确要求，或现有结构已无法安全维护且能通过验证保持行为时，才进行大范围替换或重写。
- 工作区可能有用户未提交修改；不得回滚、覆盖、整理或提交与当前任务无关的修改，也不得使用 `git reset --hard` 或未经明确授权的 `git checkout --`。
- 修改已有接口、模型、配置或前端交互时，优先保持所在模块的既有模式和公开兼容性。
- 中文文案、注释、日志和展示性描述遵循中英文排版习惯；品牌名、产品名和协议名使用官方名称、大小写和空格。
- 修改代码、配置、接口、命令或流程时必须同步更新权威文档；确认无需更新时，在最终回复中说明已检查且无需更新。
- 正式文档以 [文档治理](documentation-governance.md) 为准。

## 前端测试

- `frontend/test/` 是受版本管理的前端测试目录。测试文件优先验证用户可见行为、组件公开接口或稳定的源码契约，不依赖私有状态和偶然的内部结构。
- 测试目录分类、文件后缀、质量检查和运行命令以 [验证说明](verification.md) 为准；不得把构建产物检查伪装为 Vitest 测试。
- 前端测试、构建产物检查、测试命令或 CI 流程变更时，同步更新 [验证说明](verification.md) 与 [发布流程](../operations/release.md)。

## 项目快照

QMediaSync 是媒体同步和刮削系统，用于管理 115 网盘、百度网盘、OpenList 等云存储与 Emby 媒体服务器之间的文件同步、STRM 生成和媒体刮削。

- 语言：Go 1.25。
- 后端：Gin、GORM，模块名为 `qmediasync`，位于 `backend/`。
- 数据库：SQLite 或 PostgreSQL，支持内嵌和外部模式。
- 前端：Vue 3、Vite、TypeScript，位于 `frontend/`；本地生产构建输出 `frontend/dist`，发布流程将其复制为 `backend/web_statics`，运行目录使用 `web_statics`。
- 其他目录：`backend/emby302/` 是嵌入的 Emby 302 代理，`backend/openxpanapi/` 是自动生成的百度网盘 OpenAPI 客户端，`docker/` 存放容器脚本，`scripts/` 存放安装和发布辅助脚本。

完整目录职责见 [仓库结构](repository-structure.md)。

## 修改前的文档导航

先从 [文档索引](../README.md) 定位文档，再按下表补充阅读和同步更新。

| 改动范围 | 必读和必更新的权威文档 |
| --- | --- |
| 请求 DTO、参数校验、控制器响应、API 兼容性 | [请求校验约定](request-validation.md)；同步目录聚合写入同时查看 [同步目录聚合 API](../reference/sync-path-api.md)，STRM Webhook 同时查看 [STRM Webhook](../reference/strm-webhook.md) |
| 配置字段、第三方密钥、部署参数 | [配置](../operations/configuration.md) 和 [部署与持久化](../operations/deployment.md)；认证或 Cookie 同时查看 [认证会话](../architecture/authentication-sessions.md) |
| 数据模型、迁移、时间字段、SQLite / PostgreSQL 差异 | [数据库 schema 与迁移](../reference/database-schema.md) 和 [数据库运维](../operations/database.md) |
| SSE、快照、事件回放、日志流或同步任务流 | [实时事件](../architecture/realtime-events.md) 和 [反向代理](../operations/reverse-proxy.md) |
| Vue 组件、HTTP 客户端注入、SSE 消费、路由历史、响应式布局或交互反馈 | [前端开发约定](frontend-development.md) 和 [验证说明](verification.md)；涉及接口字段时同时查看 [请求校验约定](request-validation.md) |
| 同步目录、全局或自定义 Cron、同步队列、`sync` 记录、取消或同步完成后的下游触发 | [STRM 同步调度与任务记录](../architecture/sync-orchestration.md)；修改保存接口、目录监控规则或幂等时同时查看 [同步目录聚合 API](../reference/sync-path-api.md) |
| 上传、目录监控、STRM 生成、源文件清理 | [上传与 STRM 处理](../architecture/upload-and-strm-processing.md) |
| Emby 刷新、全量 / 增量同步、Webhook 同步 | [Emby 媒体库同步](../architecture/emby-library-sync.md) |
| 任务来源、任务类型、展示映射或数据库机器值 | [任务来源](../reference/task-sources.md) |
| 刮削命名模板、模板变量、NFO 解析或媒体类型为其他的信息来源 | [刮削命名模板与其他类型 NFO](../reference/scrape-rename-templates.md) |
| 发布、CI、镜像标签或 FPK 打包 | [发布流程](../operations/release.md) |
| 单个客户端或前端工具目录 | 对应代码目录内的 `README.md` |

高风险跨模块改动还必须阅读文档中的“不变量”和“验证方式”；不存在对应契约时，按 [文档治理](documentation-governance.md) 补充。

## 验证

按改动范围选择最小必要验证，不为了无关验证运行全量构建。完整命令、前端测试约定和稳定验证规则见 [验证说明](verification.md)。

新增行为必须有相应验证。无法运行时，最终回复中说明未运行的命令、原因和剩余风险。纯文档改动至少运行 `git diff --check` 和相对链接检查，不强行构建后端或前端。

## Go 约定

### 导入、命名和注释

- Go 文件使用 `goimports` 维护 import，使用 `-local qmediasync` 识别本项目包；不要手工维护 import 分组。模块名不是域名式路径，分组以 `goimports -local qmediasync` 的实际输出为准。
- 导出类型和函数使用 `PascalCase`，未导出标识符使用 `camelCase`。常见 initialism 使用 `ID`、`URL`、`API`、`HTTP`、`JSON`、`SQL`、`OAuth` 等 Go 约定形式。
- 接口按行为或职责命名，例如 `Reader`、`Writer`、`EventHandler`、`Scraper`、`Driver`；不强制使用 `Impl` 后缀。
- 布尔字段优先使用自然状态名，例如 `IsRunning`、`HasRemoteSeasonPath`、`CronEnabled`。存量 `EnableXxx` 命名不做无关批量修改。
- 所有 Go 标识符使用英文，中文只出现在注释和字符串字面量中。注释默认使用中文；导出函数使用中文文档注释，Swagger 使用英文标签和中文描述。详细边界见 [注释规范](comment-guidelines.md)。

### 错误、响应和请求绑定

- `panic` 仅用于不可恢复的启动错误。常规数据库和业务错误按所在模块既有的返回和日志模式处理。
- 控制器修改必须保持所在控制器的响应风格。多数业务接口使用 `APIResponse[T]` 的 `Code` 表达业务状态；认证、参数、文件和部分历史接口可能直接返回 HTTP `400`、`401`、`404` 或 `409`。
- 查询参数使用 `c.ShouldBindQuery(&req)` 和 `form` 标签；JSON Body 使用 `c.ShouldBindJSON(&req)` 和 `json` 标签；路径参数使用 `c.Param("id")`。

### 数据模型

- 新增业务表默认嵌入 `BaseModel`，时间使用 Unix 秒 `int64`。`internal/notification` 的通知渠道、渠道配置和规则表是历史例外，直接使用 GORM 的 `time.Time` 创建 / 更新时间；除非同时设计并验证迁移，不得为统一风格单独改写这些表的时间语义。
- JSON 标签使用 `snake_case`；GORM 约束通过明确标签表达，例如 `primaryKey`、`unique`、`index` 和字段类型。
- 数组字段沿用 JSON 切片和 JSON 字符串双存储模式；枚举使用 `string` 类型和常量；非标准表名使用 `TableName()`。
- 修改模型、迁移或存储值时，必须同步阅读和更新 [数据库 schema 与迁移](../reference/database-schema.md)。

## 配置、密钥和日志

- 主配置为 `config/config.yaml`，兼容旧 `config.yml`；敏感变量文件为 `config/.env`，加载后会覆盖真实环境变量。
- 默认 API 密钥为 `FANART_API_KEY`、`TMDB_API_KEY`、`TMDB_ACCESS_TOKEN` 和 `SC_API_KEY`，可由 ldflags 或运行环境设置。取值优先级为 UI 配置（DB）> 环境变量 / `config/.env` > ldflags。
- 本机敏感数据密钥由 `helpers.InitEncryptionKey()` 每实例生成并保存到 `config/encryption.key`，不使用 `ENCRYPTION_KEY` 环境变量或 ldflags。
- OAuth 中转共享密钥为 `OAUTH_RELAY_ENCRYPTION_KEY`；环境变量或 `config/.env` 优先于 `main.OAuthRelayEncryptionKey` ldflags。
- 使用 `helpers.GlobalConfig` 读取全局配置。涉及优先级、密钥或浏览器安全时，同时更新配置与认证会话契约。
- 使用 `QLogger`：`AppLogger` 为主日志，115、OpenList、百度网盘和 TMDB 分别使用其子系统 logger。可用级别为 `Infof`、`Warnf`、`Errorf`、`Debugf`、`Fatalf`；日志不得泄露密钥、Token、Cookie 或完整敏感配置。

## 前端约定

- Vue 代码使用 Vue 3 Composition API 与 `<script setup lang="ts">`。
- 组合式函数接收可能响应式的普通值时，使用 `MaybeRefOrGetter` 和 `toValue()`；例如 `pageSize`。
- axios 实例、回调、SDK client、handler 等值本身可能可调用的参数，不得使用 `MaybeRefOrGetter` 和 `toValue()`，应使用 `MaybeRef` 和 `unref()`，避免把可调用对象误当 getter 执行。

## 文档与发布

- 根 `README.md` 只保留项目介绍、快速入口和 [文档索引](../README.md) 入口；完整索引维护在 `docs/README.md`。
- 正式使用、开发、配置、运行和发布说明进入 `docs/` 的对应职责目录；代码局部约束进入相邻 `README.md`。
- 发布、CI、镜像标签、FPK 和 changelog 的唯一流程说明见 [发布流程](../operations/release.md)。

## 专题入口

- [文档治理](documentation-governance.md)：文档职责、命名、契约和迁移规则。
- [前端开发约定](frontend-development.md)：HTTP 客户端、状态刷新、路由、响应式布局和交互反馈。
- [注释规范](comment-guidelines.md)：注释、Swagger 和验证代码注释边界。
- [验证说明](verification.md)：改动类型到最小验证的映射。
- [请求校验约定](request-validation.md)：Request DTO、校验和控制器边界。
- [实时事件](../architecture/realtime-events.md)：SSE、快照和回放边界。
- [STRM 同步调度与任务记录](../architecture/sync-orchestration.md)：同步目录、Cron、队列、记录与下游协作边界。
- [配置](../operations/configuration.md) 与 [认证会话](../architecture/authentication-sessions.md)：配置、密钥、Cookie、CSRF 和 API Key。
