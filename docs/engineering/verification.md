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
| 配置、密钥或数据库迁移 | `helpers`、`models`、`db` 或相关控制器包测试 | 配置、数据库 schema 与运维 |
| 数据库启动、首次配置或部署模板 | 本文“数据库启动与部署验证”的 Go、镜像和脚本检查 | 数据库运维、部署、发布流程 |
| 本地管理员恢复与 Compose 脚本 | 本文“管理员恢复验证”的 Go、脚本和 PostgreSQL 检查；涉及 Windows 展示时交叉构建并人工确认窗口 | 认证会话、部署 |
| Vue 组件、组合式函数或 HTTP 客户端 | `pnpm run test`、`pnpm lint`、`pnpm format:check`、`pnpm run type-check` | AI 协作说明、请求校验 |
| 账号授权更换跨端流程 | `cd backend && go test ./internal/requests ./internal/v115auth ./internal/v115open ./internal/models ./internal/controllers ./internal/db`；`cd frontend && pnpm run test -- test/components/cloud-auth test/composables/useV115DeviceAuthorization.test.ts`、`pnpm run type-check`、`pnpm run build` | [账号授权与更换](../reference/account-authorization.md) |
| 前端生产集成 | `pnpm run test`、`pnpm run build`、`pnpm run check:build` | 本地开发、发布流程 |
| 后端可执行文件或发布配置 | `go build` 或发布文档中的对应构建命令 | 发布流程 |
| 正式 Markdown 文档 | `git diff --check`、相对链接检查；改动 AI 入口时确认兼容入口内容一致 | 文档治理 |

## 后端命令

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

镜像在临时配置目录和专用PostgreSQL 中验证启动；确认不含 PostgreSQL 服务端和旧 `DB_*` 默认环境变量，且 `GUID` / `GPID` 权限切换、`su-exec`、`inotifywait` 仍可用。飞牛两架构分别验证 SQLite 和 PostgreSQL 配置生成；Linux 脚本使用隔离替身检查参数传递和 systemd 内容，不在验证中安装或修改主机数据库。

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

Vitest 对 `element-plus` 使用 Vite 内联依赖处理，让真实表单校验获得与浏览器构建一致的 `async-validator` CommonJS 互操作；否则 Node 的嵌套默认导出可能使校验异常被表单聚合逻辑忽略。该设置仅位于 `vite.config.ts` 的 `test` 配置中。

## 构建和发布命令

```bash
# 构建前端静态文件
(cd frontend && corepack enable && corepack prepare pnpm@11 --activate && pnpm install --frozen-lockfile && pnpm run build)

# 本地后端构建
(cd backend && go build -o QMediaSync .)

# 注入版本信息的构建
(cd backend && CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v1.0.0 -X 'main.PublishDate=2026-01-01'" -o QMediaSync .)

# 本地 Docker 构建
docker build -f docker/source.local.Dockerfile -t qmediasync:local .
```

跨平台构建、GitHub Actions 和 FPK 打包以 [发布流程](../operations/release.md) 为准。

## CI 覆盖边界

当前 `.github/workflows/ci.yaml` 会在 `main`、`dev`、`feature/**` 推送和 Pull Request 上依次执行前端 `pnpm run test`、`pnpm run build`（其中包含类型检查）和 `pnpm run check:build`，以及后端 `go vet ./...`、`go test ./...` 和 `go build -trimpath -tags=nomsgpack`。它不会运行前端 ESLint 或 Prettier。

因此，涉及行为、校验或兼容性的改动不能只依赖 CI 构建通过；仍应按本文档的改动范围运行相关本地验证。`feature.yaml` 和 `beta.yaml` 负责分支镜像构建，不替代 CI 或测试。

## 稳定回归验证

- 长期回归风险优先由相关 Go 包内测试保护；新增或修改测试时遵循 table-driven 模式。
- 上传后的 STRM 收尾与 OpenList 上传队列回归须覆盖生产 SQLite 单连接配置；信息准备、事务回滚和幂等边界见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md#验证方式)。
- OpenList 凭据变更须验证内存与数据库两处的过时结果保护，并覆盖临时验证失败、条件保存冲突与正常刷新；认证重试变更还须验证一次独立认证恢复、完整 multipart 重发及普通网络重试次数不变。契约和回归范围见 [账号授权与更换](../reference/account-authorization.md#openlist-登录与-token-回写)。
- 当前端行为或源码契约需要自动保护时，在 `frontend/test/` 下按 `components/`、`composables/`、`router/`、`unit/`、`utils/` 或 `regression/` 分类创建 `*.test.ts` / `*.test.mjs`，由 Vitest 统一运行；测试应断言公开行为或稳定契约，避免绑定组件内部实现细节。
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
