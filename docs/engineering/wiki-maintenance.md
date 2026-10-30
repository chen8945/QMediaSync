# Wiki 用户手册维护

> 职责：维护主仓库权威资料与 Wiki 页面的对应关系，以及用户手册的编写、核对和同步方式。
>
> 权威范围：本文档唯一维护 Wiki 的来源映射与同步规则；业务行为仍由对应的架构、运维、参考文档和实现定义。
>
> 修改时机：新增、改名或合并 Wiki 页面，在常见问题与专题页之间移动内容，或修改用户可见功能、操作入口、默认值、安装与恢复流程时，检查并更新本文档及对应页面。
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
| `Windows-安装使用.md` | [部署](../operations/deployment.md)、[发布](../operations/release.md)、[程序入口](../../backend/main.go)、[重启实现](../../backend/internal/helpers/restart.go) | 架构选择、解压运行、托盘、首次配置后自动启动与失败重试、初始化码 |
| `飞牛安装.md` | [部署](../operations/deployment.md)、[配置](../operations/configuration.md)、[发布](../operations/release.md)、[fnOS 模板](../../packaging/fnos/)、[程序入口](../../backend/main.go) | FPK 架构与附件名、网页数据库配置、自动启动与初始化码、目录授权、数据保留和手动重启边界 |
| `数据库的选择.md` | [数据库运维](../operations/database.md)、[数据库实现](../../backend/internal/db/) | SQLite / PostgreSQL 的适用条件与切换方式 |
| `配置文件.md` | [配置](../operations/configuration.md)、[示例](../examples/config.yaml)、[加载实现](../../backend/internal/helpers/config.go) | 文件与网页设置的分工、实际生效值、端口与密钥 |
| `备份与恢复.md` | [数据库运维](../operations/database.md)、[schema 与备份格式](../reference/database-schema.md)、[备份实现](../../backend/internal/backup/) | 备份范围、保留策略、跨引擎恢复、密钥、维护状态与重启 |
| `管理员与密码恢复.md` | [认证会话](../architecture/authentication-sessions.md)、[管理员恢复](../operations/deployment.md#管理员恢复)、[恢复脚本](../../scripts/recover-admin.sh) | 初始化、密码与两步验证、恢复命令及影响 |
| `更新与旧版迁移.md` | [部署](../operations/deployment.md)、[旧内嵌数据库](../operations/database.md#旧内嵌数据库)、[发布](../operations/release.md) | 各部署方式的更新、fnOS 应用中心与手动 FPK、校验文件用途、版本兼容、旧库迁出与回退准备 |
| `反向代理配置.md` | [反向代理](../operations/reverse-proxy.md)、[认证会话](../architecture/authentication-sessions.md) | Nginx / Caddy、HTTPS、可信来源、SSE、转发与验证 |
| `账号管理.md` | [账号授权与更换](../reference/account-authorization.md)、[认证会话](../architecture/authentication-sessions.md)、[账号页面](../../frontend/src/views/accounts/AppCloudAccounts.vue) | 添加账号、授权失效、重新授权与更换账号的区别 |
| `生成-STRM.md` | [同步调度](../architecture/sync-orchestration.md)、[同步目录 API](../reference/sync-path-api.md)、[配置](../operations/configuration.md)、[上传与 STRM](../architecture/upload-and-strm-processing.md) | 来源差异、路径、全量 / 增量、定时、过滤、关联刮削；预加载的全局开关、触发时机与支持范围 |
| `目录监控上传.md` | [上传与 STRM](../architecture/upload-and-strm-processing.md)、[同步目录 API](../reference/sync-path-api.md)、[目录监控实现](../../backend/internal/directoryupload/) | 支持来源、监控规则、上传后生成、同名处理与源文件清理 |
| `STRM-Webhook.md` | [STRM Webhook](../reference/strm-webhook.md)、[上传与 STRM](../architecture/upload-and-strm-processing.md)、[认证会话](../architecture/authentication-sessions.md) | 测试阶段入口、API Key、请求示例、路径、幂等、异步结果与错误处理 |
| `任务与通知.md` | [任务来源](../reference/task-sources.md)、[同步调度](../architecture/sync-orchestration.md)、[上传与 STRM](../architecture/upload-and-strm-processing.md)、[通知实现](../../backend/internal/notification/) | 任务状态、生成与后台处理、队列操作、通知渠道及规则 |
| `刮削指南.md` | [模板与 NFO](../reference/scrape-rename-templates.md)、[同步调度](../architecture/sync-orchestration.md)、[刮削实现](../../backend/internal/scrape/) | 模式、目录、定时、线程、重新识别与重新整理 |
| `整理文件（夹）模板可用变量.md` | [模板与 NFO](../reference/scrape-rename-templates.md)、[模板变量实现](../../backend/internal/models/scrapemedia.go)及相邻测试 | 可用变量、两种语法、空值、补零、转义和命名示例 |
| `Emby-媒体库同步.md` | [Emby 同步](../architecture/emby-library-sync.md)、[Emby 设置](../../frontend/src/views/settings/AppEmbySettings.vue) | 扫描媒体库、条目同步、选库、定时与进度的区别 |
| `Emby-媒体信息提取.md` | [Emby 同步](../architecture/emby-library-sync.md)、[Emby 实现](../../backend/internal/emby/) | 手动与通知触发、API Key、插件配合与结果检查 |
| `Emby-通知配置.md` | [Emby 同步](../architecture/emby-library-sync.md)、[认证会话](../architecture/authentication-sessions.md) | Webhook 地址、鉴权、事件选择和通知转发 |
| `Emby-联动删除网盘文件.md` | [Emby 同步](../architecture/emby-library-sync.md)、[路径映射配置](../operations/configuration.md#emby-外部-strm-路径映射)、[上传与 STRM](../architecture/upload-and-strm-processing.md)、[Emby 实现](../../backend/internal/emby/)及相邻测试 | 开启步骤、支持来源、STRM 存放与挂载要求、删除范围、结果查看与排障 |
| `Emby外网302.md`、`Emby-无法播放问题的排查步骤.md` | [上传与 STRM](../architecture/upload-and-strm-processing.md)、[配置](../operations/configuration.md)、[302 代理](../../backend/internal/emby302/)、[控制器](../../backend/internal/controllers/) | 三类地址、来源差异、代理与直链、预加载入口、客户端和网络排障 |
| `飞牛影视反代.md` | [飞牛影视反代运维](../operations/fnos-proxy.md)、[飞牛影视反代 API](../reference/fnos-proxy-api.md)、[飞牛配置页面](../../frontend/src/views/settings/AppFnosProxySettings.vue)、[反代实现](../../backend/internal/fnosproxy/) | 「飞牛影视」入口、默认关闭、保存立即应用、根目录对应、容器端口映射、恢复重启边界、共用 STRM 预加载开关及支持限制、客户端排障；不另写飞牛 HTTPS 教程 |
| `文件权限相关问题.md` | [部署](../operations/deployment.md)、[容器入口](../../docker/entrypoint.sh)、[安装脚本](../../scripts/install/)、[fnOS 模板](../../packaging/fnos/)、[目录选择器](../../frontend/src/components/files/DirectorySelector.vue) | 实际运行身份、目录读写、容器挂载、原生 fnOS 授权与 Docker 区别、密钥保护 |
| `常见问题.md` | 本表中对应专题的权威资料与 Wiki 操作页 | 按现象提供排查步骤和处理方法；专题页保留操作流程与页内检查表 |

## 常见问题页与专题页的分工

`wiki/常见问题.md` 按现象集中提供排查步骤，让用户能在同一条目内完成检查和处理。

- 新写“为什么……”“没有变化 / 没有进度 / 收不到”类排障说明时，写入常见问题，给出检查位置、操作和下一步；跨页链接用于需要重新完成的安装或配置流程，不把同一问题的步骤拆散到多个页面。
- 专题页保留与自身操作直接绑定的检查表和错误对照表，例如恢复失败表、Webhook 常见错误和目录监控检查清单。已有独立排障页（如 Emby 播放排查）保留完整流程，由常见问题提供入口。
- 同一段排障内容只维护一处。移动内容后，原位置链接到新的具体条目，避免两处重复展开。

## 编写与逐页核对

- 只写用户需要的内容：功能的作用、怎么用、怎么选、怎样确认成功和出错后怎么办。影响操作选择和数据安全的范围说明可以使用简短清单或表格；内部组成、执行顺序和实现原理留在 `docs/`。
- 第三方软件的通用安装和管理教程链接官方说明；Wiki 保留完成本项目配置所需的参数、示例和操作。
- 使用准确、简洁的书面语，必要时简短解释专业术语。内部机制改写为用户能观察或操作的行为；界面文字、命令、接口字段和错误码按原样保留。
- 删除无助于当前操作的防御性说明，不以更换句式的方式保留。必要提醒直接说明该做什么及实际后果，避免反复解释“什么不能证明什么”。
- 备份、重启等前置步骤按实际影响说明用途和时机；普通更新的备份建议与回退需求对应，不附加无关的数据备份或停机要求。
- 以常见操作为主，少见的兼容或迁移场景只保留必要的简短提示，不展开内部原因或逐项罗列异常分支；技术细节留在 `docs/`。
- 非必要不修改 Wiki。内部错误码分类、诊断日志字段、日志关联方法、重试实现和开发验证记录只维护在 `docs/`；不影响用户操作或选择的底层兼容修补也不写入 Wiki。仅当用户操作、配置选择、使用限制或恢复步骤变化时更新对应 Wiki，不因代码或维护者文档变更机械同步。
- 按“什么时候需要 → 准备什么 → 怎么操作 → 怎样确认成功 → 出错后怎么办”组织流程，短页面不必机械套用全部标题。
- 设置名、按钮和菜单以当前界面为准，行为说法与代码、测试或权威文档核对后落笔；界面提示与实际行为不一致时说明真实行为，代码问题另行记录。仅供维护者使用的历史字段和兼容规则留在 `docs/`。
- 说明操作对象及后果，尤其区分网盘源目录、QMediaSync 可见目录、宿主机目录和 Emby 可见目录。删除、覆盖、恢复和迁移前写清需要保留的内容。
- 示例应完整、可替换，标出域名、路径、容器名、账号和端口等需要修改的位置。中文与英文、数字之间使用空格，使用 QMediaSync、Emby、OpenList、Docker Compose、PostgreSQL 等一致官方品牌名；专有词、代码、命令、字段名和 URL 保持原样。
- 页面标题与链接显示文字使用易读名称；已有 Wiki 文件名优先保留，避免破坏收藏链接。源文件使用普通 Markdown 链接，如 `[快速开始](快速开始.md)`，便于仓库阅读和 Lychee 检查；发布时由 Action 去掉内部页面链接的 `.md`，文件名本身仍保留 `.md`。
- 不承诺“绝不会限流”“只换镜像即可迁移”等超出实现和环境的保证。旧版本步骤须核对对应版本，第三方限制须核对官方资料。

## 同步与发布检查

1. 先从文档索引找到相关契约、实现、调用方和测试，再用本表定位受影响页面。
2. 修改权威事实时先判断是否影响用户使用，仅在必要时同步相应 Wiki；新增或改名页面时同步本表、`Home.md`、`_Sidebar.md` 和所有交叉链接。
3. 在主仓库 `wiki/` 修改正文与导航，不直接编辑线上 Wiki；Action 会镜像同步源目录，包括页面删除。
4. 对照准备发布的版本检查说明。`dev` 不自动发布；`main` 的 `wiki/**` 或 Wiki 工作流变更触发发布，也可在 `main` 手动运行。工作流串行执行，并检出最新 `main`，避免排队或重跑旧任务时发布过时内容。发布脚本本身不操作 Wiki，其推送 `main` 可以触发工作流。
5. 按 [文档验证](verification.md#文档验证) 检查源文件差异、页面链接、锚点、图片和导航覆盖。再次核对高风险命令的路径、工作目录、适用部署方式与实际影响。
6. 发布前逐项自查：每段内容都服务于当前操作；没有重复解释和无用提醒；常见问题的条目能直接用于排查；操作、后果与实际行为一致。安装和恢复步骤中的必要提醒保留在操作处。

## 自动发布契约

- `wiki.yaml` 先用 Lychee 检查主仓库 Markdown 的内部文件、图片和标题锚点，失败时停止发布。检查范围和本地命令见 [文档验证](verification.md#文档验证)。
- 使用固定到 v5.0.6 提交的 `Andrew-Chen-Wang/github-wiki-action`，启用 `preprocess`、`strategy: clone` 和 `disable-empty-commits`；转换发生在临时 Wiki 检出中，不回写 `wiki/`。
- 发布 job 使用 `contents: write` 和自带 `GITHUB_TOKEN`，无需 PAT。Wiki 必须先在 GitHub 初始化，本项目已完成。提交身份使用 Action 自带的 `github-actions[bot]`。
- Wiki 工作流获取完整 Git 历史，提交标题取 `git log -1 --format=%s -- wiki/`，即实际检出版本历史中最近一次修改 `wiki/` 的提交标题。正文记录主仓库名称和实际发布版本的完整 SHA，可能与标题来源提交不同。一次推送包含多个 Wiki 提交时，将最终内容合并为一次同步，标题只取最近一次；无内容差异不创建提交。
- Action 克隆现有 Wiki 历史后同步内容，但该版本内部使用 `git push -f`；串行工作流只能避免工作流之间并发，不能保护同时发生的网页编辑。因此所有正文修改应先提交主仓库，线上临时编辑应先回收到 `wiki/` 再发布。
- 发布完成后查看 Actions 结果并抽查线上首页、侧栏及跨页跳转；本地链接检查不能替代 GitHub Wiki 的实际渲染验收。
