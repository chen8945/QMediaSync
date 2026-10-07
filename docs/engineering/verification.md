# 验证说明

> 职责：定义 QMediaSync 按改动范围选择验证的方式，以及稳定回归验证的边界。
>
> 权威范围：本文档是验证命令的权威来源；具体业务契约的验证方式以对应契约文档为准。
>
> 修改时机：修改测试命令、构建工具或新增高风险契约时必须更新本文档。
>
> 相关代码：`backend/**/*_test.go`、`frontend/package.json`。

## 选择原则

- 按改动范围运行最小必要验证，不为了无关改动运行全量构建。
- 新增行为必须有相应验证；优先扩展相应 Go 包内、可提交的 table-driven 测试。
- 业务改动的验证应覆盖成功路径、与改动相关的无效输入或状态冲突、失败处理和关键边界；不要求为无关场景机械增加测试。
- 纯文档改动至少运行 `git diff --check` 和相对 Markdown 链接检查；不强行构建后端或前端。
- 无法运行验证时，在最终回复中说明未运行的命令、原因和剩余风险。

## 改动范围与最小验证

| 改动范围 | 最小验证 | 相关文档 |
| --- | --- | --- |
| Go helper、模型或请求 DTO | 对应包的 `go test`；需要时指定 `-run` | 请求校验、数据库 schema |
| 控制器、认证或 API 响应 | 对应控制器包测试；必要时 `go vet ./...` | 请求校验、认证会话、STRM Webhook |
| 同步、队列、STRM、目录监控或 Emby | 对应 `synccron`、`syncstrm`、`directoryupload`、`emby` 或模型包测试 | 上传与 STRM、Emby 同步、实时事件 |
| Emby 物理索引、持久 Webhook 与联动删除 | `emby`、`embyclient-rest-go`、`models`、`controllers`、`requests`、`backup` 和应用包的普通测试／vet；相关并发加 `-race`，provider 变更另验证 `v115open`、`baidupan`、`openlist`。覆盖真实脱敏通知、失败 deep、早期观察、删除屏障、逐目标结果保存失败、停止／重启和备份停收；SQLite 与隔离 PostgreSQL 验证新库初始化、统一 65→66 迁移、建表或版本写入失败的完整回滚、重复启动和连接重开。迁移后保留旧媒体／关联，不凭空生成删除证据；旧数组、空字符串、损坏或未知版本的旁车 JSON 必须拒绝，包含纯视频冻结计划和成员证据复制路径。未设置 `QMS_TEST_POSTGRES_DSN` 导致 skip 不算 PostgreSQL 通过；`TestWebhookEpisodePart1DeletionAndRemainingPartReindex` 贯穿官方收件、后台删除 part1／专属旁车、part2 新 ID 同步及晚到旧事件保护 | [Emby 同步与删除](../architecture/emby-library-sync.md)、[数据库 schema](../reference/database-schema.md) |
| Emby 目录清理与共同批删 | `models`、`emby`、`embyclient-rest-go`、`v115open`、`baidupan`、`openlist`、`backup` 的普通测试／vet及相关 race；新持久化用例另以 `-tags=integration` 和隔离 PostgreSQL DSN 验证。`TestWebhookCleanup*` 覆盖共同批次、部分成功、attempt／结果写入失败、重启、旧／未知政策拒绝及三类目录；`TestEmbyCleanup*Persistence` 和 `TestBackupRestoreEmbyCleanupAuthorityAndAttempts` 验证政策、结果、大小限制及真实备份恢复。按真实来源的账本字段验证根证据，包含百度路径 ID 与 fsid 区分、同路径替换和同步根移动；正常目录成功确认原 ID 后免子项回查，初次或异常目录缺失仍逐已知身份恢复。分别计数 Emby 全扫描／分页／AdditionalParts、网盘列表／详情／写请求、重试和本地 SQL；接口替身计数不能冒充实际 HTTP。验证采集超过 30 秒可成功、完成后 30 秒复用、5 分钟采集预算、排队后过期拒绝、写后列表失效，以及未知文件不逐项查归属 | [目录清理](../architecture/emby-library-sync.md#已采纳的目录清理策略)、[身份与事件存储](../reference/database-schema.md#emby_index_statesemby_item_statesemby_item_evidences) |
| STRM 分页、账本批删与进度并发 | `cd backend && go test ./internal/syncstrm ./internal/models ./internal/baidupan ./internal/synccron ./internal/realtime`；相关并发回归加 `-race` | [同步调度与任务记录](../architecture/sync-orchestration.md)、[实时事件](../architecture/realtime-events.md) |
| STRM 扫描完整性与失败范围 | `cd backend && go test ./internal/syncstrm ./internal/models ./internal/baidupan ./internal/openlist ./internal/v115open ./internal/synccron ./internal/realtime`；详情流另跑控制器流测试，并对相关并发运行 `-race`；模型事务与迁移同时验证 SQLite / PostgreSQL | [同步编排](../architecture/sync-orchestration.md)、[数据库 schema](../reference/database-schema.md) |
| 百度保存目录路径兼容 | `syncstrm` 的 `TestBaiduSavedPathIncrementalScope` 和 `TestMovedUnreadableDirectoryKeepsOldSubtree`，运行普通/race；经真实目录保存与构造验证前导斜杠、空根、空格、越界拒绝、旧子树保留与独立兄弟清理 | [同步编排](../architecture/sync-orchestration.md) |
| STRM 后台结果、通知与生命周期 | `syncstrm`、`models`、`realtime`、`helpers` 对应测试及 race；控制器同步流、应用编译；SQLite/PostgreSQL 迁移与条件更新，前端详情/列表与 SSE/HTTP 降级 | [生成结果与后台账本](../architecture/sync-orchestration.md#生成结果与后台账本)、[实时事件](../architecture/realtime-events.md) |
| STRM 相关操作排队 | `syncscope`、`syncstrm`、`models`、`syncconfig`、`scrape` 相关普通测试、race 和 vet；目录管理控制器测试及应用编译；SQLite/PostgreSQL 验证范围读取、修改和删除，检查多页查询不重复叠加条件且末页旧位置仍受保护。路径规范化由 `TestStrmGenerationScopeValidatesRemotePaths`、`TestStrmGenerationScopeDirectoryScanPreservesSpaces` 和 `TestStrmRemotePathWithinRoot` 保护，覆盖真实生成、空根兼容、目录展开、名称空格保留与越界拒绝 | [相互影响的操作如何排队](../architecture/sync-orchestration.md#相互影响的操作如何排队) |
| STRM 账本批量保存 | `syncstrm` 相关普通测试、race 和 vet，并在 SQLite/PostgreSQL 验证字段、SQL 子批、整页回滚和重跑；产品链路验证后台失败后增量与全量恢复、进程中断和提交结果不明 | [`sync_files`](../reference/database-schema.md#sync_files)、[生成结果与后台账本](../architecture/sync-orchestration.md#生成结果与后台账本) |
| STRM 后处理配置生效时间 | `syncstrm` 的 `TestGenerationBatchConfigBoundary`、`TestGenerationConfigInheritanceAndIsolation`、`TestGenerationConfigFinalizingAndCancellation`，以及生成、目录展开和范围等待相关普通/race/vet；核对本批后项与下一批的配置、继承规则、数组隔离、取消及完成收尾 | [后处理配置何时生效](../architecture/upload-and-strm-processing.md#后处理配置何时生效) |
| STRM 同次生成复用目录列表 | `syncstrm` 的 `TestGenerationDirectoryCopiesAndRetries`、`TestGenerateReusesOwnerDirectoryForMetadata` 及既有 owner、115 完整分页、取消和范围回归，运行相关普通/race/vet；核对真实生成请求数、STRM 内容、元数据匹配和独立操作重新读取 | [上传与 STRM 后处理](../architecture/upload-and-strm-processing.md) |
| STRM 小批元数据目录复用 | `syncstrm` 的 `TestGenerationBatchDirectoryValidity`、`TestProcessPendingReusesDirectoryWithinBatch` 及原目录复用/owner/重试/跳过回归；`v115open` 的写入版本与取消/上传回归。运行普通/race/vet，核对实际请求数、下一批重读、五秒到期、事实不符、写入期间和取消后的失效；不以请求数减少推算整轮收益 | [上传与 STRM 后处理](../architecture/upload-and-strm-processing.md) |
| 元数据安全替换与恢复 | 运行 `helpers` 的 `TestMetadataSafeWrite`、`TestMetadataHTTPFailurePreservesTarget`，`models` 的 `TestMetadataDownloadRecovery`、`TestMetadataDownloadMigrationSQLite`、`TestMetadataDownloadSourceHashes`，`backup` 的 `TestBackupMetadataDownloadRoundTrip` 及 `syncstrm` 的元数据回归；相关普通/race/vet。来源摘要测试覆盖百度新建/历史任务、首次/替换及十六进制云端哈希不作内容 MD5 校验，同时保持大小和 115/OpenList 内容摘要错误保旧。设置隔离 `QMS_TEST_POSTGRES_DSN` 后以 `-tags=integration` 运行 `TestMetadataDownloadMigrationPostgres`，核对实际迁移和下载恢复，未设置时的 skip 不计通过 | [同步调度](../architecture/sync-orchestration.md)、[数据库 schema](../reference/database-schema.md)、[数据库运维](../operations/database.md) |
| 元数据源符号链接 | `helpers` 的 `TestMetadataCopySymlinkSource`、`models` 的本地下载恢复、`directoryupload` 的 `TestResolveMetadataSource*` 和 `syncstrm` 的 `TestGenerationMetadataSourceLinks`，运行普通/race/vet；验证合法源链接、链接父目录、越界/缺规则/复制期间变化失败保旧、目标链接仍被拒绝、清理只删除原链接 | [上传与 STRM](../architecture/upload-and-strm-processing.md) |
| STRM 目标锁与 Emby 后续处理 | `syncstrm` 的 `TestProcessStrmFileReleasesTargetBeforeEmby`、`TestGenerateReleasesTargetButKeepsScopeDuringFollowup`、`TestGenerateResolvesEmbyOnlyOnce`，以及 owner、取消、旧路径清理和 finalizing 回归；普通/race/vet。用真实入口暂停慢请求，核对目标锁释放、范围等待、错误返回及解析次数 | [上传与 STRM 后处理](../architecture/upload-and-strm-processing.md)、[Emby 刷新](../architecture/emby-library-sync.md) |
| STRM 满批续取 | `syncstrm` 的 `TestStrmGenerationWorkerBatchContinuation` 运行真实 worker 循环，覆盖满批、空闲/不足批、收尾/领取/SQL错误、终态失败、取消和批间配置；保留既有父子/finalizing/源清理回归，运行普通/race/vet | [上传与 STRM 后处理](../architecture/upload-and-strm-processing.md) |
| STRM 收尾公平重试 | `syncstrm` 的 `TestGenerationRetry*`、`TestGenerationOrphan*`、`TestGenerationMissingUpload` 与模型领取/重试回归，覆盖持续错误、分页、依赖、失效引用、父子计数、状态保存故障、取消和领取后位置变化；相关普通/race/vet。隔离 `QMS_TEST_POSTGRES_DSN` 下以 `-tags=integration` 运行 `TestGenerationRetryPostgres`、`TestGenerationOrphanPostgres`，验证实际任务处理、事务回滚及重载恢复，skip 不计通过 | [上传与 STRM 后处理](../architecture/upload-and-strm-processing.md)、[数据库 schema](../reference/database-schema.md) |
| 独立文件收尾失败 | `syncstrm` 的 `TestStandaloneFinalization*` 及既有 finalizing/失效引用/跳过回归，运行普通/race/vet；核对成功路径原有状态写入次数、普通错误保存 failed、源与成果保留、显式重新提交、取消及终态/删行不覆盖。隔离 `QMS_TEST_POSTGRES_DSN` 下以 `-tags=integration` 运行 `TestStandaloneFinalizationPostgres`，验证范围查询、完成写入及失败状态写入故障，skip 不计通过 | [后处理规则](../architecture/upload-and-strm-processing.md#后处理配置何时生效)、[数据库 schema](../reference/database-schema.md) |
| STRM 扫描失败后的元数据与旧目录保护 | `syncstrm/sync_115_upload_parent_test.go`、`sync_others_directory_failure_test.go` 和 `TestLocalComparisonAuthenticationFailureStopsRemainingFiles`；运行包内普通/race/vet，覆盖空目录与移动目录身份确认、失败范围全部后代和原本地目标、兄弟目录清理、请求复用、认证及 SQL 故障 | [同步调度与任务记录](../architecture/sync-orchestration.md#验证方式) |
| 剧集可选海报与必需 NFO | `scrape` 包的 `TestEpisodeScrape*` 及包内普通/race/vet，验证本地/网盘输出、海报缺失不阻断 NFO、必需文件与真实读写错误仍失败、兄弟文件隔离和来源保留 | [上传与 STRM 后处理](../architecture/upload-and-strm-processing.md#验证方式) |
| STRM 名称匹配优化 | `go test ./internal/syncstrm ./internal/validation`；对名称/正则、手动生成、通用扫描、百度增量与旧记录回填、115 预取/祖先目录/保留目录运行相关 race 和 vet。新增入口回归见 `TestStartOtherKeepsNameChecksForDirectoriesAndPathNames`、`TestBaiduIncrementalAndOldCacheKeepExclusionRules`；匹配短基准不代表整轮同步耗时 | [名称排除](../operations/configuration.md#strm-名称排除) |
| STRM 后处理跳过结果 | `syncstrm`、`models`、`directoryupload`、`controllers`、`requests` 相关测试/race/vet；覆盖各来源过滤、同名候选、全跳过/混合父子计数、收尾失败重载、历史成功与新跳过并存、启动/周期源清理。隔离 `QMS_TEST_POSTGRES_DSN` 下 `-tags=integration` 运行 `TestGenerationSkippedPostgres`、`TestCleanupSkippedPostgres`，skip 不计通过。前端结果弹窗与状态测试、类型和 lint 检查覆盖查询范围、原因、子项与历史未知数量 | [跳过结果](../architecture/upload-and-strm-processing.md#过滤与跳过结果)、[结果查询](../reference/strm-webhook.md#查询处理结果) |
| 115 本轮目录关系复用 | `syncstrm` 的 `Test115VerifiedRelationsReuseAndUnknown`、`Test115FullSyncOldDirectoryLocationMustBeConfirmed`、`Test115InFlightConflictPreservesUnresolvedFiles`、`Test115FailedAndCanceledDetailsDoNotTeachRelations`、`Test115PreloadedRootConflictAndRuleChange` 和 `Test115RelationRequestSamples`，以及现有 115 扫描/排除回归；运行相关普通/race/vet，核对真实文件页、请求数、冲突后不生成和清理保护 | [扫描完整性与失败保护](../architecture/sync-orchestration.md) |
| 同步文件查询索引 | `models`、`backup` 相关普通测试、race 和 vet；SQLite/PostgreSQL 验证新库、64→65、失败重试、修复与备份恢复，保留长路径和原记录；检查文件 ID 查询与同目录查询的结果、不同同步目录隔离，以及 Emby 候选的最新 50 条和排除自身 | [`sync_files`](../reference/database-schema.md#sync_files)、[刷新目标解析](../architecture/emby-library-sync.md#刷新目标解析) |
| 配置、密钥或数据库迁移 | `helpers`、`models`、`db` 或相关控制器包测试 | 配置、数据库 schema 与运维 |
| 数据库启动、首次配置或部署模板 | 本文“数据库启动与部署验证”的 Go、镜像和脚本检查 | 数据库运维、部署、发布流程 |
| 本地管理员恢复与 Compose 脚本 | 本文“管理员恢复验证”的 Go、脚本和 PostgreSQL 检查；涉及 Windows 展示时交叉构建并人工确认窗口 | 认证会话、部署 |
| Vue 组件、组合式函数或 HTTP 客户端 | `pnpm run test`、`pnpm lint`、`pnpm format:check`、`pnpm run type-check` | AI 协作说明、请求校验 |
| 账号授权更换跨端流程 | `cd backend && go test ./internal/requests ./internal/v115auth ./internal/v115open ./internal/models ./internal/controllers ./internal/db`；`cd frontend && pnpm run test -- test/components/cloud-auth test/composables/useV115DeviceAuthorization.test.ts`、`pnpm run type-check`、`pnpm run build` | [账号授权与更换](../reference/account-authorization.md) |
| 115 共享客户端凭据 | `cd backend && go test -race ./internal/v115open`；`cd backend && go test ./internal/models ./internal/controllers ./internal/synccron` | [账号授权与更换](../reference/account-authorization.md#访问凭证定时刷新与失效) |
| 下载代理 Cookie 隔离 | `cd backend && go test -race ./internal/controllers -run '^TestProxy115'`；覆盖 115/百度首跳与重定向、Range/Referer/UA 保留 | [认证与浏览器会话](../architecture/authentication-sessions.md#api-key可信来源与下载代理) |
| 115 STRM 直链解析与播放日志 | `cd backend && go test ./emby302/service/emby ./emby302/web/cache ./emby302/util/https ./internal/controllers ./internal/playback ./internal/helpers`；缓存联调运行 `go test -race ./emby302/service/emby ./emby302/web/cache`，覆盖 UA 分离、请求保真、签名安全期限、排队过期及失败回退不缓存 | [115 STRM 直链解析](../architecture/upload-and-strm-processing.md#115-strm-直链解析)、[日志行为与脱敏](../operations/configuration.md#日志行为与脱敏) |
| Emby 302 回源、共享路径和 Web 兼容性 | `cd backend && go test -race ./emby302/config ./emby302/service/emby ./emby302/util/https ./emby302/web/cache ./emby302/util/jsons ./internal/helpers`；覆盖 HTTP 取消及不完整响应不缓存、完整字幕仍可命中缓存、WebSocket 直连及消息收发、本地 / SMB 路径、原图默认裁剪与增强显式关闭、主配置读写；脚本执行验证需要 Node.js | [Emby 302 回源连接](../operations/configuration.md#emby-302-回源连接)、[图片与自定义脚本](../operations/configuration.md#emby-302-图片与自定义脚本) |
| Emby 302 随机列表缓存 | `cd backend && go test -race ./emby302/service/emby ./emby302/web/cache`；覆盖上游读取失败、下游写入失败、失败后重新回源、完整响应与随机重排缓存复用，以及关闭缓存的兼容性 | [Emby 302 缓存](../operations/configuration.md#emby-302-缓存) |
| 115 多端播放 | `cd backend && go test ./internal/playback ./internal/controllers ./internal/v115open ./internal/synccron ./internal/models ./internal/requests ./internal/helpers ./internal/syncstrm ./internal/scrape/scan`；相关播放、目录与回收站定时清理和过滤测试加 `-race`；涉及设置页交互时再运行前端检查 | [多端播放链路](../architecture/upload-and-strm-processing.md#115-多端播放链路) |
| 多端副本失败保护与有限重试 | `cd backend && go test -race ./internal/controllers ./internal/playback ./internal/v115open`；覆盖原文件零取链、旧槽位保留、当前预留释放、一次取链重试、确认缺失后一次重建、共享预算与独立清理 | [多端播放链路](../architecture/upload-and-strm-processing.md#115-多端播放链路) |
| 公共授权随机串 | `cd backend && go test -race ./internal/helpers ./internal/v115open`；`cd backend && go test ./internal/v115auth ./internal/controllers` | [账号授权与更换](../reference/account-authorization.md#授权流程传递) |
| 前端生产集成 | `pnpm run test`、`pnpm run build`、`pnpm run check:build` | 本地开发、发布流程 |
| 后端可执行文件或发布配置 | `go build` 或发布文档中的对应构建命令 | 发布流程 |
| 正式 Markdown 文档 | `git diff --check`、相对链接检查；改动 AI 入口时确认兼容入口内容一致 | 文档治理 |

文件管理回归覆盖桌面 / 移动布局切换后的选择与待提交弹窗清理、三类网盘的根目录移动 / 复制，以及 OpenList 同步完成、异步已提交和失败响应。后端控制器测试保护 115 成功标记的防御性检查和 OpenList 任务 ID 返回；移动端按钮换行后的左对齐和间距需在窄屏浏览器复核。

批量模式切换回归使用真实 Element Plus 表格，验证已有图标和操作菜单不被重新挂载、原生单选／全选和退出清空仍有效；控制定时器验证全选延迟未结束即退出时不会恢复隐藏选择。浏览器检查桌面／移动端非批量状态下的勾选框不可见、不可聚焦，展开详情和行内操作可用；性能比较使用同一环境、相同当前页条数，记录切换耗时、节点复用和列表请求数，不以固定毫秒阈值作为自动测试断言。

返回上级目录回归覆盖桌面／移动端、根目录隐藏、非根空目录可返回、三类网盘按面包屑 ID／路径只退一级、加载时禁用，以及导航行不计入全选和分页；浏览器复核原生按钮的键盘操作与窄屏显示。

文件操作安全回归运行 `(cd backend && go test ./internal/requests ./internal/controllers ./internal/openlist ./internal/baidupan)`，覆盖路径空白保真、跨目录整批拒绝且不访问上游、重命名同名保护与旧刮削接口兼容，以及写入成功或失败后的父目录 / 子树失效和旧请求不能回写。百度客户端测试覆盖文件管理顶层与逐项错误、凭据错误分类、旧响应兼容，以及其他接口的 `info` 语义隔离。前端批量操作测试覆盖删除 / 移动 / 复制失败后的强制刷新、取消不刷新、上下文切换保护、刷新再次失败及不自动重发写入；桌面 / 移动表格验证单项操作后保留有效选择、移除消失条目并在手动刷新时清空。移动 / 复制使用真实弹窗验证提交期间关闭按钮、Esc 和取消均被拦截，成功或失败后仍反馈结果、清理选择并刷新；提交前仍可正常关闭。OpenList 客户端同时验证响应丢失不重发、一次认证恢复，以及认证恢复后响应丢失不再重发。重命名使用真实输入弹窗验证名称冲突 / 网络失败后的输入保留与重试、重复确认和关闭保护，以及取消不提交；统一随 Vitest 执行。上述上游交互通过本地 HTTP 替身验证；真实 OpenList 存储驱动的并发重命名保护，以及异步任务完成后的手动刷新须在实际环境复核。

目录浏览排序运行 `(cd backend && go test . ./internal/requests ./internal/controllers ./internal/v115open ./internal/openlist ./internal/baidupan)`，覆盖能力与参数映射、跟随网盘兼容、置顶缓存身份、115 当前层分页和原始 offset、系统目录计数、百度目录分页、OpenList 原生目录刷新及取消、本地可选修改时间。OpenList 公共取消／共享认证和缓存并发变化额外运行相应包的定向 `-race` 测试。

共享浏览缓存回归同时覆盖 180 秒绝对过期且命中不续期、115 文件／目录双向命中且原始顺序与元数据保留、百度与 OpenList 不同列表语义隔离、刷新使所有排序／视图失效、115 祖先目录改名／移动后的路径失效、失败不缓存及失效代次阻止旧结果回填。并发测试覆盖同一批次合并、取消一个等待者不影响另一位、刷新后不加入旧请求；使用本地上游替身计数，不调用真实网盘。

前端随 Vitest 验证能力控件、账号／场景／用户隔离、偏好损坏与存储失败、跟随意图、排序失败回滚、旧请求失效、新建后按序刷新以及本地未知时间；文件图标测试必须通过真实动态组件渲染出 SVG。排序展示替代旧的整体禁用契约；生产构建后运行 `check:build`。浏览器检查覆盖桌面和窄屏控件换行、长文件名下图标不收缩，以及路径选择底部按钮可达。上游替身和离线响应验证不代替真实百度／OpenList 账号联调；115 同值跨页稳定性及并发目录变更仍由上游决定。

## 后端命令

Emby 删除修复回归包含：未知存活来源的跨目录引用、百度历史 fsid 缺失／替换及独立真实父目录、收件重试造成的观察顺序倒置（含同时间戳、无效状态候选及 PostgreSQL）、首次父目录缺失与临时列表／详情故障、移动或替换对象保护、初始缺失结果原子保存和重启恢复。`TestWebhookCleanupSourceVerificationCountsBaiduHTTP` 使用真实 provider／SDK 和本地 HTTP 替身，分别断言列表、详情、删除及 Emby 请求数；单页夹具的计数不代表线上固定成本。提取回归验证完整音视频流不重复入队，字幕不计入流数量。`TestAppWebhookStartup` 通过子进程验证恢复失败时非零退出、队列停止与资源释放，以及成功恢复；Windows 构建验证不能替代真实桌面的托盘验收。

来源反查另覆盖 115／百度真实客户端 HTTP：账本命中不反查、未知存活来源查明在目录外后整删、目录内来源和异常查询回退、缺失或冲突账号／URL、同来源跨核验阶段去重，以及陌生 TXT／图片不逐项查归属。115 使用独立账号客户端并在首次请求前设置测试传输；不得改运行中共享实例。缓存回归覆盖读开始时间、失败复用、取消、过期、账号／地址配置改变和长读取后拒绝。已移出成员的回归通过真实收件事务验证最新有效观察可排除旧 state 归属，排除项无 owner、目标及删除屏障；无效观察、未知归属、直接删除和其他真实成员保持原行为，并在 SQLite／隔离 PostgreSQL 验证。

一层目录保护覆盖账本旧位置在外、真实存活来源被移入直接子项的 115／百度场景，包括只有实时 Emby 来源而缺少本地关联的情况。分别计数列表页、详情与删除请求，验证同轮复用、多个待删目录、陌生元数据不逐项读取及普通子目录不递归；保留子目录外部移入风险这一明确边界。OpenList 单文件／共同批删夹具使用真实生成并脱敏的下载 URL 作为冻结 PickCode，验证视频及专属旁车可匹配，同时继续拒绝对象、哈希、大小、时间和位置变化。

发送上限恢复覆盖第四次已删除但未保存结果、已填写 FinishedAt 但远端结果仍不明、确认读取失败后有限重试，以及目录缺失而已知成员移出的情况；断言没有第五次删除、确认不增加发送次数、失效 claim 不能保存，目录缺失不直接完成成员。目录自身未耗尽但覆盖成员已耗尽时也不能发送；同 claim 的第四次 guard 可复用登记，成员确认缺失后其余目录操作可继续。

文件批次重试覆盖临时 Emby 核验失败后恢复：同批视频与专属旁车共同完成、通知正常结束、关联按原身份收尾。通过缩小模拟请求预算触发真实分组的回填，覆盖附件早于依赖视频进入批次的情况；后续视频成功后，暂缓附件应汇总分批处理，不能逐文件发送或遗留为终态。继续验证本轮身份或共享冲突、跨批保留、发送失败、发送次数耗尽及历史成功后原对象重现时的附件保护；同一 owner 的不同视频目标不得相互豁免。合批及分包预算由 provider 替身断言，不冒充真实网盘 HTTP 请求计数，也不据缩小预算的实验推断线上发生频率。

观察范围索引在 SQLite／隔离 PostgreSQL 验证新库、65→66 建表、修复与新旧备份恢复、故障回滚和重建失败。查询回归用无关已完成历史和 500／501 个相关候选验证：无关历史不增加逐条查询，相关读取按批次增长；保留重复证据引用的不同预期状态、无效新观察后的有效旧观察、移出排除及直接删除语义。只报告实际 SQL 数和数据规模，不把批量查询描述为任意规模下固定次数或固定耗时。

历史成员索引回归另外构造 0／10,000 条无关状态及其真实证据，通过完整删除收件验证候选定位和冻结读取范围；不能仅增加没有对应状态的观察历史。保留原 0／500 条观察规模测试和 500／501 的分批边界。覆盖当前媒体索引清空、成员移出、带屏障的历史分段关系、仅有父项关系的分段、投影随快照原子更新，以及迁移、修复和备份恢复后的重建。检查读取的证据行数及查询计划，避免少量 SQL 掩盖单次全库查询。

单条同步回归保留 10,000 条无关删除屏障，断言核验请求仅涉及目标及实际关联成员，无关历史不增加 HTTP 或逐条 SQL。全量／增量仍验证全范围恢复；单条另覆盖隐藏分段、完整版本组、缺失或未请求响应、暂时失败、准入后整组重新读取及并发版本变化。核验计数来自本地 Emby HTTP 替身，不代表真实服务耗时或网盘联调。

115 反查取消必须由实际 HTTP transport 观察请求 context 结束，不能仅断言上层方法提前返回；取链和文件详情均需覆盖。单 pickcode 返回多个文件时，普通客户端和播放客户端都必须拒绝。整目录删除的最终发送检查验证过期、缺失或仍在读取的缓存只导致拒绝，不能增加全库扫描／网盘反查请求或发出删除。URL 回归按真实 STRM 生成器覆盖基础地址含路径／查询参数时生成的根路由；带凭据 URL 的取消错误须归一为安全错误后再缓存。

Go 工具链或直接依赖升级须运行全部后端测试、`go vet ./...`、`go mod verify`，并按发布参数交叉构建 Linux / Windows 的 amd64、arm64。约 2 GiB 内存、4 个逻辑 CPU 的环境使用 `GOMAXPROCS=4 GOFLAGS=-p=1 GOMEMLIMIT=1GiB GOGC=100`，测试加 `-parallel=4`；允许单个进程使用 4 个 CPU，包级编译和独立验证命令仍串行执行。`GOMEMLIMIT` 是每进程的 Go 内存软限制，不是所有进程的内存总额上限；保留 `-p=1`，避免多个编译进程同时占用接近 1 GiB。`GOMAXPROCS` 不限制创建的 goroutine 总数，仍须观察 RSS；Linux 可额外用 `taskset` 限定 CPU。不要并行运行基准与其他编译任务。

YAML 配置变更运行 `go test ./internal/helpers ./emby302/config ./internal/controllers .`，覆盖统一 v3 后的布尔字段、八进制、锚点合并、主配置保存回读和非法合并键返回错误；顶层与嵌套重复键必须报错，原文件保持不变，不能因解析失败退回旧 `config.yml`。同时保留首次配置、管理员恢复、JWT 自动保存、日志设置和可信来源配置回归；格式与兼容边界见 [配置文件](../operations/configuration.md#配置文件与默认端口)。反射转换回归运行 `go test ./emby302/util/jsons ./emby302/service/emby`，覆盖导出字段、嵌套对象、空值、指针和 map 键类型边界。

性能比较使用同一工具链、CPU 和 GC 参数，至少重复 6 次并用 `benchstat` 比较：`go test ./emby302/util/jsons ./internal/helpers -run '^$' -bench 'Benchmark(FromObject|ConfigYAML)$' -benchmem -benchtime=200ms -count=6 -cpu=1`。以改动前工作区为对照，同时报告耗时与分配；无显著差异不得宣称提速，依赖维护收益与运行时性能收益分开记录。

备份压缩回归通过 `go test ./internal/helpers ./internal/backup` 验证 Deflate 输出、备份恢复往返和旧版 Store 包兼容。性能测量可运行 `go test ./internal/helpers ./internal/embyclient-rest-go -run '^$' -bench 'Benchmark(ZipDir|FetchMediaItemsPage)$' -benchmem`；使用合成 JSON 数据，ZIP 结果包括文件 I/O，不代表真实数据库导出总耗时。

网络升级回归覆盖非法百度 STRM 地址与转码 Host、WebSocket 原始请求语义，以及私有 HTTP transport 在成功、错误和重定向后的连接释放；运行 `go test -race ./internal/helpers ./internal/syncstrm ./emby302/service/emby`。密码与 NFO 的 Unicode 分类随工具链升级，分别由 `requests` 和 `helpers` 包测试保护；Windows 证书环境变量须按 [出站证书信任](../operations/configuration.md#emby-302-出站-https) 在目标机器验收。

Pongo2 升级运行 `go test ./internal/models -run 'Test(NewSyntax|OldSyntax|BackwardCompatibility|SyntaxDetection|GenerateNameByTemplateOrKeep)'`，并对模型包运行 `-race`。覆盖普通变量转义、显式 `safe`、父块字面量与变量的原生继承输出，以及 `removetags` 正则元字符、无 `else` 的 `ifchanged` 不发生 panic；文件名契约见 [模板渲染流程](../reference/scrape-rename-templates.md#模板渲染流程)。

GitHub 私有连接池回收运行 `go test -race ./internal/github -run '^TestManagerRetiresPrivateConnections$'`，覆盖更新配置、清缓存、过期重探测、有效缓存复用，以及共享默认连接池隔离和重探测失败后的旧客户端回退。

```bash
# 全部测试
(cd backend && go test ./...)

# 常用包
(cd backend && go test ./internal/helpers/)
(cd backend && go test ./internal/models/)
(cd backend && go test ./internal/synccron/)

# 指定测试
(cd backend && go test ./internal/helpers/ -run TestExtractFilename)
(cd backend && go test ./internal/models/ -run TestOldSyntax_BasicMovie)

# 覆盖率与静态检查
(cd backend && go test -cover ./...)
(cd backend && go vet ./...)

# 统一维护 import 分组
(cd backend && goimports -local qmediasync -w .)
```

项目没有配置 Go lint 工具。Go 文件的 import 以 `goimports -local qmediasync` 的实际输出为准；仅在用户请求或本次变更确实需要格式化时运行会写入文件的命令，并检查不会带入无关改动。

`TestProxyCustomJsWaitsForEmby` 通过 Node.js 执行处理器生成的脚本，验证等待初始化、单次执行及异常隔离。运行相关 Go 测试前确认 `node --version` 可用；没有 Node.js 时该项会明确跳过，不能据此声称脚本行为已验证。

## 数据库启动与部署验证

```bash
# 配置读写、首次配置、旧状态拒绝、数据库连接和 schema 升级
(cd backend && go test ./internal/helpers ./internal/db/... ./internal/models .)

# 安装入口语法
bash -n scripts/install/linux-init.sh
bash -n backend/FNOS/qmediasync-amd64/cmd/install_callback backend/FNOS/qmediasync-arm64/cmd/install_callback

# 本地源码镜像；正式发布镜像按发布流程准备独立构建上下文
docker build -f docker/source.local.Dockerfile -t qmediasync:verify .
```

回归须覆盖默认 PostgreSQL、两种引擎配置保存后回读、旧 SQLite 配置中无效的 `postgresType` 不影响连接，以及内嵌或未知模式在写配置、开库前被拒绝。正常启动和管理员恢复遇到 `backups/migrate.zip` 必须拒绝且保留文件；缺少主配置但存在 `config/postgres` 时不得启动空实例向导。

空库初始化改动须分别验证 SQLite 单连接和隔离 PostgreSQL：完整建表与默认数据、后段 DDL／必要索引／默认数据／最终版本写入失败、事务回滚、关闭重开连接后重试，以及重复启动保留已配置值。失败时不得留下本次创建的版本表、业务表和默认记录，也不得记录初始化成功；启动入口须返回初始化错误并停止后续步骤。保留无默认管理员、默认分类顺序和既有数据库修复行为；已有库的历史迁移保证按对应版本测试验收。

定向回归可运行 `cd backend && go test -tags=integration ./internal/models . -run 'TestMigrateFresh|TestStartDatabaseRejectsInitializationFailure'`；PostgreSQL 用例要求 `QMS_TEST_POSTGRES_DSN` 指向可丢弃测试库，缺少 DSN 的跳过不算通过。

镜像在临时配置目录和专用PostgreSQL 中验证启动；确认不含 PostgreSQL 服务端和旧 `DB_*` 默认环境变量，且 `GUID` / `GPID` 权限切换、`su-exec`、`inotifywait` 仍可用。飞牛两架构分别验证 SQLite 和 PostgreSQL 配置生成；Linux 脚本使用隔离替身检查参数传递和 systemd 内容，不在验证中安装或修改主机数据库。

systemd 在线更新中“进程退出后由 systemd 用新版本拉起”只能在真实 systemd 环境验证。步骤：

1. 用不同版本号构建两个二进制，用 `scripts/release/package-release-asset.sh` 打出新版本的 Linux 包，按发布流程用 `sha256sum` 生成 `checksums.txt`，一起由本机 HTTP 服务提供下载。
2. 按 `linux-init.sh` 的写法创建带 `Restart=always` 和 `Environment=QMS_SYSTEMD_UPDATE=1` 的临时服务，运行旧版本。
3. 从更新页或接口发起更新，确认 `QMediaSync` 与 `web_statics/` 已替换、`old/` 保留旧版本、`update/` 和下载包已删除、服务重启次数加一，且 `/api/version` 返回新版本。
4. 改用对安装目录无写权限的服务用户运行，确认下载前即被拒绝；去掉标记后确认返回不支持在线更新，再按部署文档添加 drop-in 确认可重新启用。

更新地址来自 GitHub Release，本地验证需要真实发布版本，或只在临时构建副本中改写下载地址和校验文件地址，不提交该改动。验证结束后删除临时服务、drop-in、测试目录和本机 HTTP 服务。

## 管理员恢复验证

```bash
# 命令参数、已有数据库连接、进程锁、认证事务与应用日志
(cd backend && go test ./internal/helpers ./internal/db ./internal/models . -run 'Test(AcquireInstanceLock|OpenExisting|RecoverAdmin|ParseAdminRecoveryOptions|PerformAdminRecovery|AdminRecoveryConfigDir|StartupConfigMigrationLock)')

# Docker 命令替身覆盖脚本边界，不操作真实部署；只依赖 Python 3 标准库
python3 scripts/tests/test_recover_admin.py

# 真实 PostgreSQL：使用可创建 schema 的专用测试库 URL
(cd backend && QMS_TEST_POSTGRES_DSN='postgres://用户:密码@127.0.0.1:端口/测试库?sslmode=disable' go test -tags=integration ./internal/models -run '^TestRecoverAdminPostgres$')

# Windows 无控制台发布方式的编译检查
(cd backend && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-H=windowsgui' -o /tmp/QMediaSync-recovery.exe .)
```

PostgreSQL 测试在测试库内为每个场景创建独立 schema，并在结束后删除；不要使用生产数据库。SQLite 与 PostgreSQL 共同覆盖已有数据库连接、重置、删除、事务中途失败回滚、损坏旧凭据、非固定管理员 ID、会话审计、API Key 和业务数据保留。命令入口还覆盖数据库连接错误不泄密，以及旧配置迁移前必须持有源、目标实例锁。

脚本检查覆盖当前目录发现、显式部署参数、候选歧义与自定义镜像的容器选择、管道执行时的终端交互、无效编号重试与取消、无容器或无终端、多容器、镜像和配置不一致、服务级环境文件、容器内更新、只读挂载、实际卷与网络复用、卷子目录拒绝、bind 选项保留、原运行状态保留、失败后启动原服务，以及启动失败或收尾被中断时仍交付新密码。修改 Compose 调用方式后，还应在隔离测试项目中确认真实 `config`/`run` 行为和临时容器清理。

Windows 的窗口可见性和 `Ctrl+C` 复制不能由交叉编译证明，需在真实交互式桌面人工验证：先退出托盘程序，执行重置，确认能复制并登录；再次启动后确认旧会话失效。无法执行时必须记录该限制。

## 前端命令

```bash
# Vitest 测试、代码检查、格式、类型和生产构建
(cd frontend && pnpm run test)
(cd frontend && pnpm run test:watch)
(cd frontend && pnpm lint)
(cd frontend && pnpm format:check)
(cd frontend && pnpm run type-check)
(cd frontend && pnpm run build)
(cd frontend && pnpm run check:build)
```

Vitest 5 通过 `vite.config.ts` 中的 `environments.client.resolve.noExternal: ['element-plus']` 使用 Vite 内联依赖处理，让真实表单校验获得与浏览器构建一致的 `async-validator` CommonJS 互操作；否则 Node 的嵌套默认导出可能使校验异常被表单聚合逻辑忽略。该设置替代已弃用的 `test.server.deps.inline`，仅在 `mode === 'test'` 时启用；Vitest 会将环境中的内联规则汇总为项目级配置。调整此配置须回归真实 Element Plus 表单的非法输入拦截与保存行为。

## 构建和发布命令

```bash
# 使用 Node 26 构建前端静态文件
(cd frontend && npm install --global pnpm@12 && pnpm install --frozen-lockfile && pnpm run build)

# 本地后端构建
(cd backend && go build -o QMediaSync .)

# 注入版本信息的构建
(cd backend && CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v1.0.0 -X 'main.PublishDate=2026-01-01'" -o QMediaSync .)

# 本地 Docker 构建
docker build -f docker/source.local.Dockerfile -t qmediasync:local .
```

发布脚本的交互、回滚、dry-run 和沙箱模拟验证（临时仓库 + git-cliff 替身，不触碰真实仓库与远端；修改 `scripts/release/` 时必须运行）：

```bash
python3 scripts/tests/test_release.py
```

跨平台构建、GitHub Actions 和 FPK 打包以 [发布流程](../operations/release.md) 为准。

## CI 覆盖边界

当前 `.github/workflows/ci.yaml` 会在 `main`、`dev`、`feature/**` 推送和 Pull Request 上依次执行前端 `pnpm run test`、`pnpm run build`（其中包含类型检查）和 `pnpm run check:build`，以及后端 `go vet ./...`、`go test ./...` 和 `go build -trimpath -tags=nomsgpack`。它不会运行前端 ESLint 或 Prettier。

因此，涉及行为、校验或兼容性的改动不能只依赖 CI 构建通过；仍应按本文档的改动范围运行相关本地验证。`feature.yaml` 和 `beta.yaml` 负责分支镜像构建，不替代 CI 或测试。

## 稳定回归验证

- 长期回归风险优先由相关 Go 包内测试保护；新增或修改测试时遵循 table-driven 模式。
- Emby 附件跨批删除与重试运行 `(cd backend && go test ./internal/emby -run '^TestWebhookCleanupDeferred' -count=1)`，并加 `-race` 检查。用例须实际形成附件先于依赖视频的分包，覆盖初始无结果、暂缓后的临时核验失败、待核验转发送失败后的连续失败与恢复、共享和身份冲突、发送次数耗尽，以及视频结果已保存但附件补处理前中断后的恢复；断言冻结计划不变、附件保护、成功视频不重发、发送范围与次数、通知与关联正确收尾。使用隔离 SQLite、本地 Emby HTTP 和内存网盘替身，不代表真实网盘或断电持久化验收。
- Emby 目录旁车问题隔离运行 `(cd backend && go test ./internal/models -run '^TestEmbyDeletionMatrix(SidecarPlanningIssues|FinalizePlanningIssue)' -count=1)`。覆盖旁车身份变化和历史身份未确认时只保留相关目录的媒体关联，其他目录、账号或来源已完成的成员独立收尾；未知问题仍保留，历史证据与 `SyncFile` 账本不变。
- 分类保存回归在 `models` 包中使用隔离 SQLite，确认电影／电视剧分类写入失败时 `Save` 返回数据库错误而非固定成功。运行 `(cd backend && go test ./internal/models -run TestCategorySaveReturnsDBError)`。
- 上传后的 STRM 收尾与 OpenList 上传队列回归须覆盖生产 SQLite 单连接配置；信息准备、事务回滚和幂等边界见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md#验证方式)。
- OpenList 凭据变更须验证内存与数据库两处的过时结果保护，并覆盖临时验证失败、条件保存冲突与正常刷新；认证重试变更还须验证一次独立认证恢复、完整 multipart 重发及普通网络重试次数不变。契约和回归范围见 [账号授权与更换](../reference/account-authorization.md#openlist-登录与-token-回写)。
- 当前端行为或源码契约需要自动保护时，在 `frontend/test/` 下按 `components/`、`composables/`、`router/`、`unit/`、`utils/` 或 `regression/` 分类创建 `*.test.ts` / `*.test.mjs`，由 Vitest 统一运行；测试应断言公开行为或稳定契约，避免绑定组件内部实现细节。
- 公共请求错误和认证 API 回归随 `pnpm run test` 执行，覆盖新旧错误码、HTTP `200` 业务失败、合法空值、取消／超时／无响应与普通异常的区别、诊断脱敏、默认业务消息与空值回退、字段文案改写、登录统一文案、匿名会话查询及并发 HTTP `401` 只处理一次。响应体数值 `code=401` 不单独使会话失效。后端 `controllers`、`helpers` 测试保护第三方错误中的已知密钥和常见凭据脱敏。业务页面迁入 API 模块时还须验证失败保留输入、不误报成功和不执行成功回调；契约见 [API 响应与请求错误](frontend-development.md#api-响应与请求错误)。
- 刮削请求迁移的回归覆盖 AI / TMDB 保存失败、TMDB 连接测试 `data=false`、来源／CSRF 拒绝、刮削搜索与重新识别参数和专用超时，以及记录删除、目录保存和启停失败不执行成功后续动作。组件错误提示测试应同时确认表单输入保留、提示条关闭后新错误仍可显示，以及取消／已处理认证错误不重复提示。刮削目录须实际触发已订阅的事件回调，验证突发事件的请求合并、连续失败只提示一次、成功后恢复提示，以及停用／卸载后的旧响应失效。
- 账号请求与授权回归覆盖无响应、来源／CSRF、业务失败的提示和输入保留，以及 QR / OAuth 会话 ID、取消、隐藏／卸载、过期请求和 APP ID 搜索竞态。账号状态查询还须覆盖在途时删除或替换列表、同一行再次请求及卸载，确保旧请求不会抛出数组越界异常、写回过时状态、清除新加载状态或弹出过期错误。旧请求不得覆盖新授权或新搜索状态，错误诊断不得包含授权载荷和凭据；复用 [账号授权与更换](../reference/account-authorization.md#验证方式) 的前端验证入口。
- 设置领域请求回归覆盖 Emby 轮询失败去重与成功后复位、配置和媒体库读取分离、Cron 字段错误及旧预览失效、代理凭据保留和脱敏回读、通知 `code=0` 的历史成功语义、STRM 安全校验提示、Emby 保存、提取与启动同步在途时互相禁用、通知规则切换渠道或关闭后重开同一渠道时旧规则与晚到结果失效，以及当前用户、两步验证和设备撤销失败不执行成功动作。保存失败保留输入、取消／已处理认证错误静默、回读失败不得被成功说明覆盖，错误与日志不含敏感载荷；均随 Vitest 执行。提取媒体信息同一时间只运行一轮由 `(cd backend && go test -race ./internal/emby)` 验证。
- 同步目录与队列请求回归覆盖聚合保存的字段定位、成功警告和幂等键复用、详情与关联读取失败保护，以及队列操作成功后刷新失败只提示一次、取消／已处理认证错误静默。请求参数、分页统计、刷新合并和生命周期保护仍由 API、composable 与组件测试共同验证；原始版本对象另有 API 契约测试。
- 剩余领域请求回归覆盖文件与目录操作、同步记录、API Key、分类、备份恢复、更新和后台统计的业务成功校验与输入保护。日志及任务 HTTP 快照测试同时验证错误状态保留、HTML 响应、JSON 解析异常与传输故障区分、取消静默和旧结果失效；原生 SSE 错误不额外探测 HTTP 错误原因；任务流进入 CLOSED 时允许读取任务快照降级，明确认证／来源／CSRF 拒绝或 HTTP `401`、`403`、`404` 会停止轮询，包含首次请求失败的场景。备份下载额外验证 JSON 错误不生成下载文件，更新取消失败继续保留进度状态，页面只在下载中显示取消，重启导致任务状态丢失时须核对目标版本后才提示成功，版本不匹配时仅提示一次“更新未生效”并结束轮询；后端 `controllers` 测试同时验证已结束的更新任务不能被取消改写、进入安装与取消互斥且安装后拒绝取消、不支持的运行方式拒绝在线更新，以及 systemd 在线更新的启用条件、安装目录不可写或运行中程序不在安装目录时下载前拒绝、更新包 SHA-256 校验、发布包替换与中途失败回滚。`go test . ./internal/controllers ./internal/helpers` 同时覆盖 Windows / Docker 更新包准备与交付失败、残留临时文件、正常发布包结构，以及 Linux 下 Docker 入口脚本的成功、损坏包和替换失败回滚；Windows 更新入口还运行 `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/qms-update-verification.exe .` 验证编译。

- 请求失败后的连续交互也必须验证：APP ID 新关键词失败后不能混用旧分页；同步与刮削目录的双向关联窗口在候选或已有绑定加载失败时不能保存，关闭／重开或切换目录后旧读取与保存结果失效；刮削编辑、AI、TMDB、代理、Emby、线程、日志及 STRM 首次读取失败时禁止默认配置写回，关闭提示仍不能保存，重试成功恢复保存；更新启动与取消 POST 在途时切换页面可见性，仍处理该次写入的真实结果。以上均纳入组件和 composable 的 Vitest 回归。
- 备份与恢复终态运行 `(cd backend && go test -race ./internal/backup)` 及 `(cd backend && go test ./internal/controllers ./internal/helpers)`，覆盖并发任务占用、快照隔离、文件／数据库／ZIP 收尾失败及残缺归档清理、恢复部分失败和旧包缺表兼容。前端备份 store 测试保护明确成功、失败、旧响应未知结果和安全文案，不能把停止运行直接判为成功；契约见 [备份和恢复状态](../operations/database.md#备份和恢复状态)。
- 下载、上传队列的统计由 `frontend/test/components/QueueTotals.test.ts` 覆盖全局“剩余 / 排队 / 处理中”、分页和筛选不改变统计口径、快照刷新与空队列归零；下载预取和上传完成处理均沿用后端 `processing` 口径。相关组件测试随 `pnpm run test` 执行。
- 上传并发由 `frontend/test/components/AppThreadSettings.upload-concurrency.test.ts` 覆盖默认值、保存回读、整数范围及保存失败提示；后端 `requests`、`controllers` 和 `models` 测试覆盖旧请求兼容、写库失败不生效、默认设置和迁移重试。队列测试使用受控在途任务验证增减并发、暂停后保存与恢复、重复领取及清空后的旧任务，并额外运行相关 `models` 测试的 `-race` 检查。百度网盘和 OpenList 还须通过真实队列与本地 HTTP 替身验证驱动共享状态的并发安全，覆盖范围与命令见[上传和 STRM 处理的验证方式](../architecture/upload-and-strm-processing.md#验证方式)。
- 局部加载遮罩与导航的层级由 `frontend/test/regression/sidebar-menu-motion.test.ts` 保护样式契约；真实绘制和点击命中需在浏览器复核：分别使用移动和桌面视口，延迟首页、更新页及队列接口，确认移动菜单及背景遮罩可点击、关闭菜单后加载区域仍阻止操作、响应结束后遮罩消失，并确认模态对话框仍覆盖侧栏。路由模块加载骨架和全屏加载不得被局部遮罩规则改变。
- STRM 正则预检由 `frontend/test/utils/strmRegex.test.ts` 与 Go `validation` 包共同读取 [兼容性样例](../../backend/internal/validation/testdata/strm_regex_cases.json)，保护合法 Go 表达式不被前端误拦截、明确不兼容项能提示，以及无法可靠预检的语法交由后端判断。新增样例需同时通过两端测试。
- STRM 原文输入和保存由 `frontend/test/components/StrmRegexInput.test.ts`、`frontend/test/components/StrmSettings.regex-save.test.ts` 保护，覆盖输入法组合、大小写、空白、分隔符、转义、桌面与移动表单保存回读、服务端错误展示和清空列表；随 `pnpm run test` 执行。后端 `controllers` 包以真实保存接口和 SQLite 覆盖原文落库、失败不改旧配置及清空；`models` 包覆盖旧库迁移和迁移重试，`syncstrm` 包覆盖手动文件 / 目录生成、全局继承与自定义覆盖、115 目录缓存 / 预取 / 路径补全中的祖先目录排除。
- 标签输入的折叠、展开、添加和输入保留由 `frontend/test/components/MetadataExtInput.test.ts` 与 `frontend/test/components/StrmRegexInput.test.ts` 保护，同时保留普通名称 / 扩展名的规范化和正则原文的区别。浏览器检查需覆盖多个控件同时展开时的焦点、添加后的焦点恢复、桌面输入尺寸，以及移动端长名称 / 正则换行和删除操作可达性。
- STRM 列表的清空确认、取消与草稿重置由上述输入组件测试保护；`StrmSettings.regex-save.test.ts` 同时覆盖桌面 / 移动表单四类列表的合并导入、去重、逐项清空、保存回读、导入失败保留原值及导入期间禁止保存。后端 `requests` 和 `controllers` 包覆盖全局扩展名空数组校验、默认值回退、空数组 / `null` / 字段省略时统一落库为空数组，以及保存失败不改内存；浏览器还需复核窄屏按钮换行与就地确认的可达性。
- 仅依赖构建产物的检查使用 `*.check.mjs`，通过独立脚本在 `pnpm run build` 后执行，不能使用 Vitest 测试文件后缀。
- 当包内 Go 测试、前端测试、lint、类型检查和生产构建都无法覆盖明确的长期风险时，优先补充对应测试；无法自动覆盖时，在对应契约文档中写明人工检查步骤和剩余风险。

## 文档验证

文档改动完成后执行：

```bash
git diff --check
```

检查所有相对 Markdown 链接均指向存在的文件或锚点。新增或移动正式文档后，确认仓库中的旧路径和旧名称已更新；修改 AI 入口时，确认两个兼容入口内容完全一致。

同步任务扫描结果前端回归使用 `cd frontend && pnpm exec vitest run test/composables/useSyncTaskStream.test.ts test/components/AppSyncTaskDetail.scan-result.test.ts test/components/AppSyncDirectories.errors.test.ts test/components/FileAndSyncPages.errors.test.ts test/utils/syncRefreshDecision.test.ts`，验证部分完成／扫描不完整／取消、失败范围、历史缺字段、HTTP 降级停止和目录重新可启动；同时运行类型检查及对应文件 lint。这些验证不替代真实网盘分页一致性或 NAS 权限验收。

STRM 后台验证必须分别断言生成结果和后台结果：慢／失败通知不阻塞账本与来源队列，后台失败／取消不改生成计数或水位，旧进度不覆盖终态，删除后的记录和日志不复活。启动中断恢复只说明未知退出，不可当作真实断电持久化验证。详情 snapshot／live complete／HTTP fallback 覆盖后台 pending、成功、失败、已知与未知中断、无需后台及历史缺字段；总耗时取实际较晚终点，不能累加重叠阶段。

后台前端回归沿用上述定向 Vitest 命令，额外覆盖旧任务后台事件／历史删除不清空同目录新任务运行态（包括只收到 HTTP 运行快照的情况）、冻结生成计数和双耗时。`FileAndSyncPages.errors.test.ts` 用真实同步记录组件和延迟 HTTP 响应验证旧快照不覆盖后台完成/失败事件、普通事件合并在途快照、结构事件合并必要补读、过期序号不刷新，以及失效请求错误不提示。迁移测试同时覆盖 64→65、迁移中断后的重试与新库初始化，重复执行保持已有结果和版本号。

STRM 缓存与队列优化验证使用同一数据和工具链对照旧版、改动前及改动后，分别计数 HTTP、账本查询、路径解析和等待，不以局部阶段耗时替代整轮生成或后台耗时。115 覆盖 600 个已有目录连续三轮、新增 8 个目录、全量重新确认、已知移动、规则改变及空目录详情复用；实际远端一致性仍需真实账号复核。

范围缓存运行 `models` 的 `TestSyncPositionCache*`，覆盖冷构建、热复用、内容变化不失效、位置变化、构建期间写入、取得许可后版本变化、跨轮符号链接及部分清库失败；结合 `syncstrm` 账本部分提交和 `backup` 恢复失败测试。`syncscope`、`scrape`、`syncstrm` 运行相关 race，检查独立刮削并行、同目标与配置修改等待、根链接转向拒绝写入。内存位置版本保守全局失效，位置变化后可能重建其他目录缓存；这不是所有轮次 O(变化量) 的保证。

生成队列运行 `TestStrmGenerationQueue*`，在 SQLite 和隔离的 `QMS_TEST_POSTGRES_DSN` 下核对真实 EXPLAIN：LIMIT 5 无额外排序，后续页能按排序键定位；同时覆盖依赖阻塞、重试公平性、NULL 历史时间、64→65、新库、修复和恢复失败。下载重复任务及元数据复制运行 helpers/syncstrm 的元数据测试及 race，验证去重发生在摘要之前、已有安排不增加计数、独立目标并行与别名目标互斥。
