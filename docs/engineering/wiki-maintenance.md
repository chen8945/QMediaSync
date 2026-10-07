# Wiki 用户手册维护

> 职责：维护主仓库权威资料与 Wiki 页面的对应关系，以及用户手册的编写、核对和同步方式。
>
> 权威范围：本文档唯一维护 Wiki 的来源映射与同步规则；业务行为仍由对应的架构、运维、参考文档和实现定义。
>
> 修改时机：新增、改名或合并 Wiki 页面，或修改用户可见功能、操作入口、默认值、安装与恢复流程时，检查并更新本文档及对应页面。
>
> 相关入口：`docs/README.md`、`wiki/Home.md`、`wiki/_Sidebar.md` 和 `.github/workflows/wiki.yaml`。

## 两种文档的读者

主仓库 `docs/` 面向维护者和 AI，记录行为契约、实现边界与验证方式。Wiki 面向使用 QMediaSync 的用户，应让用户在 Wiki 内完成安装、配置、使用和排障。

Wiki 可以依据权威资料完整解释用户需要的事实，包括字段含义、默认值、操作步骤和后果。正文中的进一步说明链接到相应 Wiki 页面；下载发布包、获取安装脚本、访问第三方官方说明等操作资源可以使用外链。不要把必要步骤缩成“请看主仓库文档”，也不要将维护者契约全文复制给用户。

来源对应关系集中维护在下表，不在各 Wiki 页的隐藏注释中再维护一份。表中的 Wiki 文件名均相对于主仓库 `wiki/`，该目录是用户手册的唯一编辑来源；GitHub Wiki 仓库只承接发布结果。

## 页面与来源

同一来源可能影响多个页面。修改时同时检查概览、操作页、排障页和交叉链接。

| Wiki 文件 | 权威资料与实现入口 | 需要同步的内容 |
| --- | --- | --- |
| `Home.md`、`快速开始.md`、`_Sidebar.md` | [部署](../operations/deployment.md)、[同步调度](../architecture/sync-orchestration.md)、[Emby 同步](../architecture/emby-library-sync.md)、[前端路由](../../frontend/src/router/index.ts) | 支持范围、首次使用顺序、页面入口与导航 |
| `Docker安装.md` | [部署](../operations/deployment.md)、[配置](../operations/configuration.md)、[容器脚本](../../docker/) | 镜像、挂载、数据库网络、运行身份、初始化 |
| `Linux-安装使用.md` | [部署](../operations/deployment.md)、[发布](../operations/release.md)、[安装脚本](../../scripts/install/) | 发布包目录、启动、systemd、自启、日志 |
| `Windows-安装使用.md` | [部署](../operations/deployment.md)、[发布](../operations/release.md)、[程序入口](../../backend/main.go) | 架构选择、解压运行、托盘、初始化 |
| `数据库的选择.md` | [数据库运维](../operations/database.md)、[数据库实现](../../backend/internal/db/) | SQLite / PostgreSQL 的适用条件与切换方式 |
| `配置文件.md` | [配置](../operations/configuration.md)、[示例](../examples/config.yaml)、[加载实现](../../backend/internal/helpers/config.go) | 文件与网页设置的分工、实际生效值、端口与密钥 |
| `备份与恢复.md` | [数据库运维](../operations/database.md)、[schema 与备份格式](../reference/database-schema.md)、[备份实现](../../backend/internal/backup/) | 备份范围、保留策略、跨引擎恢复、密钥、维护状态与重启 |
| `管理员与密码恢复.md` | [认证会话](../architecture/authentication-sessions.md)、[管理员恢复](../operations/deployment.md#管理员恢复)、[恢复脚本](../../scripts/recover-admin.sh) | 初始化、密码与两步验证、恢复命令及影响 |
| `更新与旧版迁移.md` | [部署](../operations/deployment.md)、[旧内嵌数据库](../operations/database.md#旧内嵌数据库)、[发布](../operations/release.md) | 各部署方式的更新、版本兼容、旧库迁出与回退准备 |
| `反向代理配置.md` | [反向代理](../operations/reverse-proxy.md)、[认证会话](../architecture/authentication-sessions.md) | Nginx / Caddy、HTTPS、可信来源、SSE、转发与验证 |
| `账号管理.md` | [账号授权与更换](../reference/account-authorization.md)、[认证会话](../architecture/authentication-sessions.md)、[账号页面](../../frontend/src/components/) | 添加账号、授权失效、重新授权与更换账号的区别 |
| `生成-STRM.md` | [同步调度](../architecture/sync-orchestration.md)、[同步目录 API](../reference/sync-path-api.md)、[配置](../operations/configuration.md) | 来源差异、路径、全量 / 增量、定时、过滤与关联刮削 |
| `目录监控上传.md` | [上传与 STRM](../architecture/upload-and-strm-processing.md)、[同步目录 API](../reference/sync-path-api.md)、[目录监控实现](../../backend/internal/directoryupload/) | 支持来源、监控规则、上传后生成、同名处理与源文件清理 |
| `STRM-Webhook.md` | [STRM Webhook](../reference/strm-webhook.md)、[上传与 STRM](../architecture/upload-and-strm-processing.md)、[认证会话](../architecture/authentication-sessions.md) | 测试阶段入口、API Key、请求示例、路径、幂等、异步结果与错误处理 |
| `任务与通知.md` | [任务来源](../reference/task-sources.md)、[同步调度](../architecture/sync-orchestration.md)、[上传与 STRM](../architecture/upload-and-strm-processing.md)、[通知实现](../../backend/internal/notification/) | 任务状态、生成与后台处理、队列操作、通知渠道及规则 |
| `刮削指南.md` | [模板与 NFO](../reference/scrape-rename-templates.md)、[同步调度](../architecture/sync-orchestration.md)、[刮削实现](../../backend/internal/scrape/) | 模式、目录、定时、线程、重新识别与重新整理 |
| `整理文件（夹）模板可用变量.md` | [模板与 NFO](../reference/scrape-rename-templates.md)、[模板变量实现](../../backend/internal/models/scrapemedia.go)及相邻测试 | 可用变量、两种语法、空值、补零、转义和命名示例 |
| `Emby-媒体库同步.md` | [Emby 同步](../architecture/emby-library-sync.md)、[Emby 设置](../../frontend/src/components/AppEmbySettings.vue) | 扫描媒体库、条目同步、选库、定时与进度的区别 |
| `Emby-媒体信息提取.md` | [Emby 同步](../architecture/emby-library-sync.md)、[Emby 实现](../../backend/internal/emby/) | 手动与通知触发、API Key、插件配合与结果检查 |
| `Emby-通知配置.md` | [Emby 同步](../architecture/emby-library-sync.md)、[认证会话](../architecture/authentication-sessions.md) | Webhook 地址、鉴权、事件选择和通知转发 |
| `Emby-联动删除网盘文件.md` | [Emby 同步](../architecture/emby-library-sync.md)、[上传与 STRM](../architecture/upload-and-strm-processing.md)、[Emby 实现](../../backend/internal/emby/)及相邻测试 | 删除前提、来源与路径限制、附件保护、跳过与失败 |
| `Emby外网302.md`、`Emby-无法播放问题的排查步骤.md` | [上传与 STRM](../architecture/upload-and-strm-processing.md)、[配置](../operations/configuration.md)、[302 代理](../../backend/emby302/)、[控制器](../../backend/internal/controllers/) | 三类地址、来源差异、代理与直链、客户端和网络排障 |
| `文件权限相关问题.md` | [部署](../operations/deployment.md)、[容器入口](../../docker/entrypoint.sh)、[安装脚本](../../scripts/install/) | 实际运行身份、目录读写、容器挂载与密钥保护 |
| `常见问题.md` | 本表中对应专题的权威资料与 Wiki 操作页 | 问题入口、排查顺序和跨页链接；详细流程留在专题页 |

## 编写与逐页核对

- 只写用户需要的内容：功能的作用、怎么用、怎么选、怎样确认成功和出错后怎么办。实现原理、运行逻辑和内部机制不进入 Wiki；用户无法据此做出不同操作的内容一律不写。
- 非必要不修改 Wiki。内部错误码分类、诊断日志字段、日志关联方法、重试实现和开发验证记录只维护在 `docs/`；仅当用户操作、配置选择、使用限制或恢复步骤变化时更新对应 Wiki，不因代码或维护者文档变更机械同步。
- 按“什么时候需要 → 准备什么 → 怎么操作 → 怎样确认成功 → 出错后怎么办”组织流程，短页面不必机械套用全部标题。
- 设置名、按钮和菜单以当前界面为准，并核对后端实际执行与测试。发现界面提示和实现不一致时，明确真实行为，不照抄旧提示；代码问题另行记录处理。
- 说明操作对象及后果，尤其区分网盘源目录、QMediaSync 可见目录、宿主机目录和 Emby 可见目录。删除、覆盖、恢复和迁移前写清需要保留的内容。
- 示例应完整、可替换，标出域名、路径、容器名、账号和端口等需要修改的位置。中文与英文、数字之间使用空格，使用 QMediaSync、Emby、OpenList、Docker Compose、PostgreSQL 等一致官方品牌名名；专有词、代码、命令、字段名和 URL 保持原样。
- 页面标题与链接显示文字使用易读名称；已有 Wiki 文件名优先保留，避免破坏收藏链接。源文件使用普通 Markdown 链接，如 `[快速开始](快速开始.md)`，便于仓库阅读和 Lychee 检查；发布时由 Action 去掉内部页面链接的 `.md`，文件名本身仍保留 `.md`。
- 不承诺“绝不会限流”“只换镜像即可迁移”等超出实现和环境的保证。旧版本步骤须核对对应版本，第三方限制须核对官方资料。

## 同步与发布检查

1. 先从文档索引找到相关契约、实现、调用方和测试，再用本表定位受影响页面。
2. 修改权威事实时先判断是否影响用户使用，仅在必要时同步相应 Wiki；新增或改名页面时同步本表、`Home.md`、`_Sidebar.md` 和所有交叉链接。
3. 在主仓库 `wiki/` 修改正文与导航，不直接编辑线上 Wiki；Action 会镜像同步源目录，包括页面删除。
4. 对照准备发布的版本检查说明。`dev` 不自动发布；`main` 的 `wiki/**` 或 Wiki 工作流变更触发发布，也可在 `main` 手动运行。工作流串行执行，并检出最新 `main`，避免排队或重跑旧任务时发布过时内容。发布脚本本身不操作 Wiki，其推送 `main` 可以触发工作流。
5. 按 [文档验证](verification.md#文档验证) 检查源文件差异、页面链接、锚点、图片和导航覆盖。再次核对高风险命令的路径、工作目录、适用部署方式与实际影响。

## 自动发布契约

- `wiki.yaml` 先用 Lychee 检查主仓库 Markdown 的内部文件、图片和标题锚点，失败时停止发布。检查范围和本地命令见 [文档验证](verification.md#文档验证)。
- 使用固定到 v5.0.6 提交的 `Andrew-Chen-Wang/github-wiki-action`，启用 `preprocess`、`strategy: clone` 和 `disable-empty-commits`；转换发生在临时 Wiki 检出中，不回写 `wiki/`。
- 发布 job 使用 `contents: write` 和自带 `GITHUB_TOKEN`，无需 PAT。Wiki 必须先在 GitHub 初始化，本项目已完成。提交身份使用 Action 自带的 `github-actions[bot]`。
- Wiki 工作流获取完整 Git 历史，提交标题取 `git log -1 --format=%s -- wiki/`，即实际检出版本历史中最近一次修改 `wiki/` 的提交标题。正文记录主仓库名称和实际发布版本的完整 SHA，可能与标题来源提交不同。一次推送包含多个 Wiki 提交时，将最终内容合并为一次同步，标题只取最近一次；无内容差异不创建提交。
- Action 克隆现有 Wiki 历史后同步内容，但该版本内部使用 `git push -f`；串行工作流只能避免工作流之间并发，不能保护同时发生的网页编辑。因此所有正文修改应先提交主仓库，线上临时编辑应先回收到 `wiki/` 再发布。
- 发布完成后查看 Actions 结果并抽查线上首页、侧栏及跨页跳转；本地链接检查不能替代 GitHub Wiki 的实际渲染验收。
