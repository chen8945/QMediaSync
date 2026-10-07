# 发布流程

> 职责：说明持续集成、预发布镜像、版本发布、changelog 生成、GitHub Actions 和 FPK 打包流程。
>
> 权威范围：本文档维护 CI/CD、镜像标签、发布操作和产物；运行时镜像挂载与启动参数见 [部署与持久化](deployment.md)，日常开发构建与验证见 [验证说明](../engineering/verification.md)。
>
> 修改时机：修改发布脚本、tag 规则、CI 工作流、镜像标签、changelog 或 FPK 打包时必须更新本文档。
>
> 相关代码：`scripts/release/`、`.github/workflows/release.yaml`、`cliff.toml`、`backend/FNOS/`。

更新日志由 [git-cliff](https://git-cliff.org/) 从 git 提交记录自动生成，因此提交信息需遵循 [Conventional Commits](https://www.conventionalcommits.org/)。

当前 `cliff.toml` 把 `feat:`、`fix:`、`perf:`、`refactor:`、`style:` 和 `revert:` 写入 changelog；`docs:`、`chore:`、`ci:`、`test:` 等其余开发类提交不会进入发布说明，除非调整 `cliff.toml`。不规范的提交（如 `Merge`、自由文案）会被自动忽略。

## 持续集成与预发布镜像

后端工具链最低版本为 Go 1.27.1。CI 与正式发布通过 `backend/go.mod` 选择 Go 版本；两份源码 Dockerfile 使用 `golang:1.27-alpine`，跟随 1.27 系列补丁更新，升级 Go 系列时须一起更新。

前端 CI、正式发布与两份源码 Dockerfile 统一使用 Node 26 和 pnpm 12，pnpm 只指定主版本。CI 与正式发布通过 `pnpm/action-setup` 安装 pnpm，两份 Dockerfile 使用 `npm install --global pnpm@12`。升级 pnpm 主版本时同步这些安装入口及本地开发命令；版本约束和发布时间要求见 [本地开发](../engineering/local-development.md#前端启动)。

`ci.yaml` 在 pull request，以及 `dev`、`feature/**` 分支推送时执行。前端依次运行 `pnpm run test`、`pnpm run build`（包含类型检查）和 `pnpm run check:build`；后端依次运行 `go vet ./...`、`go test ./...` 和 `go build -trimpath -tags=nomsgpack`。独立的 `docs` job 使用 Lychee 最新正式版（`lycheeVersion: latest`） 离线检查 Git 文件列表中的 Markdown 本地链接和锚点，跳过网络链接与未跟踪的忽略文件。CI 不运行前端 ESLint 或 Prettier；完整验证范围见 [验证说明](../engineering/verification.md)。

前后端测试共用 STRM 正则兼容性样例，并覆盖标签输入交互、四类列表的合并导入和清空、真实表单保存回读、全局空扩展名默认值回退、原文落库、迁移重试与各同步入口的排除行为。Vitest 在测试模式下通过 Vite 环境配置内联处理 Element Plus，确保真实表单校验的 CommonJS 互操作与浏览器构建一致；配置方式见 [前端命令](../engineering/verification.md#前端命令)。这些回归沿用上述命令，不增加依赖或单独的校验服务；覆盖边界见 [稳定回归验证](../engineering/verification.md#稳定回归验证)。

局部加载遮罩的导航层级契约随 Vitest 执行；浏览器中的绘制、点击命中及模态层级按 [稳定回归验证](../engineering/verification.md#稳定回归验证) 复核。

下载、上传队列的全局剩余数、筛选分页、快照刷新，以及上传并发设置的保存和校验回归也随 Vitest 执行。后端同时覆盖上传并发调整、暂停恢复、任务领取和设置迁移，验证方式见 [稳定回归验证](../engineering/verification.md#稳定回归验证)。

公共 HTTP 错误分类、业务响应校验和认证请求的兼容性与安全回归沿用上述 Vitest 命令；刮削及账号授权请求迁移同时验证失败反馈、成功判定、第三方连接测试结果和授权生命周期，覆盖边界见 [稳定回归验证](../engineering/verification.md#稳定回归验证)。

设置页回归同时覆盖 Emby、代理、通知、STRM、线程、日志和用户安全设置的失败保护、初次加载保存门槛、Cron 时序、专用成功码与敏感数据处理，沿用上述 Vitest 和类型检查入口。

同步目录和队列领域请求的回归沿用同一入口，保护聚合字段错误、警告、幂等键，以及写入成功后刷新失败的反馈；队列统计、请求合并和生命周期测试继续执行。

STRM 生成结果与后台账本的独立状态、双耗时、详情 SSE／HTTP 延续、旧任务后台事件不覆盖新任务目录运行态，以及普通进度事件不增加在途 HTTP 补读、旧列表快照不覆盖较新的生成或后台终态，均随 Vitest 回归执行；后端同时验证通知、日志、退出与迁移，定向命令及真实环境边界见 [验证说明](../engineering/verification.md)。

文件管理、记录、备份更新、API Key 与分类等其余请求，以及日志和任务 HTTP 快照的错误兼容测试，也统一纳入 Vitest。文件管理的布局切换、根目录操作、删除 / 移动 / 复制失败后的刷新与选择保护、单项操作后保留有效勾选、重命名失败保留输入、移动 / 复制提交期间的弹窗关闭保护，以及 OpenList 异步提交回归沿用同一入口；OpenList 写入不重发与认证恢复、百度逐项错误、路径身份、同名保护和缓存失效的后端验证见 [验证说明](../engineering/verification.md)。原生 SSE 连接行为与构建产物检查继续沿用原有入口。业务错误默认消息与空值回退、同步任务降级遇到明确拒绝后停查、更新重启后的版本核对及版本未生效提示均随 Vitest 执行；Go 测试覆盖 Windows / Docker 更新准备失败和文件替换回滚，Linux 测试环境同时执行 Docker 入口脚本的成功与失败路径。

目录浏览的来源排序能力、偏好持久化、目录完整读取、共享缓存及动态图标回归沿用现有 Go／Vitest 入口；批量切换同时验证普通表格节点复用与选择行为，返回上级导航验证逐级返回及与文件数据隔离。生产构建和 `check:build` 覆盖共享控件与图标集成，不新增发布步骤。真实账号及浏览器人工检查边界见 [验证说明](../engineering/verification.md#改动范围与最小验证)。

发布前还须验证失败后的分页、双向关联窗口切换、设置初始化重试、账号列表变化后的旧请求失效、突发刮削事件的请求合并与错误提示去重，以及更新请求在途时的页面可见性切换。备份与恢复须覆盖原子回滚、会话清空后的回执查询、重启授权与重复请求、查询失败重试与页面可见性切换。须验证格式 2 的 `tables/` 布局与无清单旧包兼容路径；SQLite 导出期间的业务池读写、ZIP 权限和 Docker 纯重启入口须有回归。备份收尾还须覆盖成功后清理、原子发布不覆盖、归档与上传资源边界、禁用后撤销 Cron、TOTP 密钥预检，以及回滚／提交结果在前端准确展示。Windows 重启除交叉编译外还需在实际桌面核验。SQLite ↔ PostgreSQL ZIP 互转及驱动门禁需要额外的隔离 PostgreSQL 集成验证，普通 CI 的无 DSN 测试不能替代；命令与范围见 [稳定回归验证](../engineering/verification.md#稳定回归验证)。

推送 `dev` 还会触发 `beta.yaml`，发布多架构镜像 `ghcr.io/<owner>/qmediasync:beta`。推送 `feature/**` 还会触发 `feature.yaml`，发布 `ghcr.io/<owner>/qmediasync:<branch-tag>`：分支名会去掉 `feature/` 前缀、转为小写，斜杠和非法字符替换为连字符，最长 120 个字符。`dev` 的同一分支构建会取消仍在运行的旧 beta 构建。

这些镜像使用 `docker/source.Dockerfile` 从源码构建，目标为 `linux/amd64` 和 `linux/arm64`。它们是预发布交付物；运行时挂载、端口和权限参数见 [部署与持久化](deployment.md)。

源码镜像、正式发布镜像和本地镜像均只携带应用运行依赖，不安装 PostgreSQL 服务端、不预置旧 `DB_*` 数据库环境变量，也不创建内嵌数据库专用账号。`su-exec` 和 `inotify-tools` 仍分别用于容器运行身份和在线更新。修改运行依赖时必须同步三份 Dockerfile。

本地构建和验证使用 `docker/source.local.Dockerfile` 的镜像源配置，具体命令见 [验证说明](../engineering/verification.md#数据库启动与部署验证)。

## 本地发版

发版步骤推荐使用脚本：

1. 按 [Wiki 用户手册维护](../engineering/wiki-maintenance.md) 核对本次用户可见变化对应的操作页、导航和适用版本；用户手册在主仓库 `wiki/` 维护；以下脚本不直接同步 Wiki，但推送 `main` 中的 Wiki 变更会触发 `wiki.yaml` 自动发布。确认当前工作区干净，并确认 `dev` 是准备发布的内容。
2. 执行发布脚本（需先安装 git-cliff，见其 [安装文档](https://git-cliff.org/docs/installation/)）：

   ```bash
   scripts/release/release.sh v0.xx.xx
   ```

也可以用 `patch`、`minor`、`major` 根据当前最新版本自动推导下一个版本：

```bash
scripts/release/release.sh patch
scripts/release/release.sh minor
scripts/release/release.sh major
```

使用 `patch`、`minor`、`major` 时，脚本会先显示推导出的发布版本并进入确认菜单：直接回车使用推导版本，也可以输入 `v<major>.<minor>.<patch>` 覆盖，覆盖输入走同一套格式和递增关系校验。

首次使用或调整脚本后，可先只读预览或在沙箱中完整演练：

```bash
# 只执行检查并预览发布说明，不修改任何文件、分支或远端；显式 tag 时无需交互
scripts/release/release.sh --dry-run v0.xx.xx

# 在临时沙箱克隆中完整走一遍发布流程（含提交、打 tag 和推送到沙箱远端），真实仓库与远端不受影响
scripts/release/release.sh --simulate patch
```

`--simulate` 会把仓库镜像克隆到临时目录并在其中原样运行同一段流程，结束时询问是否删除沙箱：回车默认删除，输入 `k` 保留，便于检查沙箱内生成的 `CHANGELOG.md` 与 `.changes/`。模拟基于本地引用，不校验与真实远端的竞争状态，也不会触发 GitHub workflow。

## 发布脚本行为

`scripts/release/release.sh` 和 `scripts/release/gen-changelog.sh` 共享 `scripts/release/lib.sh` 中的 tag、版本号和重复发布校验逻辑。

交互遵循统一约定：所有确认步骤回车即为安全默认；无效输入（如格式错误的覆盖版本）提示后重试，不会终止脚本；`q` 随时中止；仅在最后执行提交、打 tag、推送等外部动作前需要输入完整 `yes`。major/minor 发布不再单独输入 `minor yes` / `major yes`，改为在版本确认和最终确认中展示醒目警示。

输出使用轻量配色：阶段标题与输入提示为粗体青色，成功结果为绿色，警示与回滚提示为黄色，错误为红色。在非交互终端（管道、CI）或设置了 `NO_COLOR` 时自动禁用，输出保持纯文本。

该脚本会：

- 前置执行全部零成本检查：命令存在性、工作区干净、本地与远端分支存在、本地 `dev` 包含 `origin/dev`、远端无同名 tag。
- 校验 tag 格式必须为 `v<major>.<minor>.<patch>`，例如 `v0.15.3`，且必须大于当前最新版本。
- 同步 `main`，并把本地 `dev` 快进合入 `main`。
- 读取上一个 `v*` 标签至今的提交，按类型分组生成 `.changes/v0.xx.xx.md`，作为 GitHub Release 正文；把本版本段落插入 `CHANGELOG.md` 顶部，保留历史内容。
- 拒绝重复版本：如果本地已存在同名 git tag、`.changes/<tag>.md`，或 `CHANGELOG.md` 已包含该版本段落，命令会直接失败。
- 展示 `CHANGELOG.md` 与 `.changes/<tag>.md` 的 diff 进入审查菜单：输入 `e` 编辑发布说明（优先 `VISUAL` / `EDITOR`，缺省回退 `GIT_EDITOR` / `core.editor`，最后探测 vi / nano / vim）并自动同步回 `CHANGELOG.md`；推导模式下输入 `v` 可返回重新选择版本；最终确认输入 `b` 可返回审查。
- 在 `main` 上提交 `chore: release <tag>`，创建 annotated tag：`git tag -a <tag> -m "Release <tag>"`，推送 `main` 和 tag 触发 release workflow，最后将 release commit 快进同步回 `dev` 并推送 `dev`。
- 中止发布（`q` 或输入流关闭）会还原 `CHANGELOG.md`、删除自动生成的发布说明并切回原分支；手动编辑过的发布说明保留在 `.changes/<tag>.md`。
- 提交之后的步骤失败时，打印当前分支、`main` / tag / `dev` 的推送状态和对应的恢复或回退命令。

`--dry-run` 执行上述全部检查，并临时检出 `dev` 让 git-cliff 预览发布说明（显示后切回），不写入 `.changes/`、不修改 `CHANGELOG.md`、不创建或推送任何分支、提交或 tag；`main` 无法快进合并 `dev` 时会直接报错。

## GitHub Actions 发布

推送 `v*` 标签会触发 GitHub Actions 的 release 流程，生成 Windows / Linux 发布包、可选的飞牛 FPK，并创建 GitHub Release。

后端发布二进制使用 `-trimpath -tags=nomsgpack -ldflags="-s -w"` 构建，默认关闭 Gin 的 MsgPack 绑定和渲染支持，以减少发布包体积。

发布 Actions 从 GitHub Secrets 读取 `FANART_API_KEY`、`OAUTH_RELAY_ENCRYPTION_KEY`、`SC_API_KEY`、`TMDB_ACCESS_TOKEN` 和 `TMDB_API_KEY`，分别注入发布二进制的 `ldflags` 或源码 Docker 构建参数。它们只是编译期默认值；运行时 `config/.env` / 环境变量仍按现有规则覆盖编译期值，数据库中的 UI 配置优先级也不变。

创建 GitHub Release 前，workflow 对 `release-assets/` 下全部文件执行 `sha256sum` 生成 `checksums.txt` 并一同上传；在线更新依赖它校验下载包（见 [部署与持久化](deployment.md)），修改资产名称或删除该步骤时须同步 `updater.ChecksumsAssetName` 和 `controllers/update.go`。

GitHub Release 的标题直接使用 `v<major>.<minor>.<patch>` tag，不额外添加 `Release` 前缀；正文取自上一步提交的 `.changes/v0.xx.xx.md`。release workflow 会拒绝重复 GitHub Release 和缺失 `.changes/<tag>.md` 的发布。

发布二进制包按平台保留不同的运行文件：Linux `.tar.gz` 包包含 `scripts/docker-entrypoint.sh` 和 `scripts/watch_update.sh`，供 Docker 在线更新复用；systemd 在线更新只从该包取 `QMediaSync` 和 `web_statics/` 替换安装目录，修改包内这两项的名称或位置时须同步更新 `controllers/update.go`。Windows `.zip` 包只包含 `QMediaSync.exe`、`web_statics/` 和 `icon.ico`（存在时），不包含 Docker 脚本。Windows 在线更新由 `QMediaSync.exe -update <目录>` 完成，不执行这些 shell 脚本。

发布包不携带内嵌 PostgreSQL 二进制或旧库迁移页面；正常首次数据库配置向导仍嵌入应用。升级旧内嵌实例前的数据处理要求见 [数据库运维](database.md#旧内嵌数据库)。

发布流程还会使用 `GITHUB_TOKEN` 推送 GHCR 镜像 `ghcr.io/<owner>/qmediasync:<tag>` 和 `ghcr.io/<owner>/qmediasync:latest`。

GHCR 镜像同时构建 `linux/amd64` 和 `linux/arm64`。Dockerfile 中的 `TARGETOS`、`TARGETARCH` 由 Buildx 自动注入，声明时不得设置平台默认值；否则 ARM64 镜像可能错误打包 AMD64 二进制。

也可以在 GitHub Actions 中手动触发 `release` workflow，并输入要发布的 Git tag（同样要求该 tag 对应的 `.changes/<tag>.md` 已提交）。

后端更新流程解压 `.zip`、`.tar.gz` / `.tgz` 发布包时，会拒绝绝对路径、`..` 路径逃逸、指向解压目录外的符号链接，以及经过已有符号链接组件写入文件。发布包内文件应使用相对路径，并保持符号链接目标位于包内目录。

## 飞牛 FPK

飞牛 FPK 打包依赖飞牛官方工具 `fnpack`（不公开分发）。release workflow 通过仓库 Secret `FNPACK_DOWNLOAD_URL`（指向可下载 `fnpack` 可执行文件的地址）下载安装，再用 `backend/FNOS/` 下的素材执行 `fnpack build` 生成 `*.fpk`。

未配置该 Secret 时，`fpk` job 和 `scripts/release/package-fnos.sh` 会自动跳过 FPK 打包，其余 Windows / Linux 发布包、Docker 镜像不受影响；若希望缺少工具时直接报错（而非静默跳过），可在脚本环境设置 `REQUIRE_FNPACK=1`。

调整 changelog 的分组、过滤规则可编辑仓库根目录的 `cliff.toml`。

## Wiki 发布

`.github/workflows/wiki.yaml` 在 `main` 的 `wiki/**` 或自身工作流变更时运行，支持在 `main` 手动触发；`dev` 和其他分支不发布。任务串行执行并检出最新 `main`，先通过与 CI 相同的 Lychee 检查，再用现成 Wiki Action 转换链接并镜像同步。使用自带 `GITHUB_TOKEN` 的 `contents: write` 权限，无需新增 Secret。

提交标题取实际检出版本历史中最近一次修改 `wiki/` 的提交标题，正文记录实际发布的主仓库 SHA；无差异不提交。源文件与线上编辑边界、Action 的强制推送行为及验收方式见 [Wiki 自动发布契约](../engineering/wiki-maintenance.md#自动发布契约)。
