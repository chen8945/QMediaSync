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

`ci.yaml` 在 pull request，以及 `main`、`dev`、`feature/**` 分支推送时执行。前端依次运行 `pnpm run test`、`pnpm run build`（包含类型检查）和 `pnpm run check:build`；后端依次运行 `go vet ./...`、`go test ./...` 和 `go build -trimpath -tags=nomsgpack`。CI 不运行前端 ESLint 或 Prettier；完整验证范围见 [验证说明](../engineering/verification.md)。

推送 `dev` 还会触发 `beta.yaml`，发布多架构镜像 `ghcr.io/<owner>/qmediasync:beta`。推送 `feature/**` 还会触发 `feature.yaml`，发布 `ghcr.io/<owner>/qmediasync:<branch-tag>`：分支名会去掉 `feature/` 前缀、转为小写，斜杠和非法字符替换为连字符，最长 120 个字符。`dev` 的同一分支构建会取消仍在运行的旧 beta 构建。

这些镜像使用 `docker/source.Dockerfile` 从源码构建，目标为 `linux/amd64` 和 `linux/arm64`。它们是预发布交付物；运行时挂载、端口和权限参数见 [部署与持久化](deployment.md)。

## 本地发版

发版步骤推荐使用脚本：

1. 确认当前工作区干净，并确认 `dev` 是准备发布的内容。
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

使用 `patch`、`minor`、`major` 时，脚本会先显示推导出的发布版本，并提供确认输入框。直接回车会使用推导版本；也可以输入 `v<major>.<minor>.<patch>` 手动覆盖，手动输入的版本会走同一套格式和递增关系校验。

## 发布脚本行为

`scripts/release/release.sh` 和 `scripts/release/gen-changelog.sh` 共享 `scripts/release/lib.sh` 中的 tag、版本号和重复发布校验逻辑。

该脚本会：

- 校验 tag 格式必须为 `v<major>.<minor>.<patch>`，例如 `v0.15.3`。
- 使用 `patch`、`minor`、`major` 推导版本时，提示确认推导版本；直接回车继续，也可输入合法 tag 覆盖。
- 校验 tag 必须大于当前最新版本；如果 minor 或 major 版本增加，还会分别要求输入 `minor yes` 或 `major yes` 额外确认。
- 同步 `main`，并把本地 `dev` 快进合入 `main`。
- 读取上一个 `v*` 标签至今的提交，按类型分组生成 `.changes/v0.xx.xx.md`，作为 GitHub Release 正文。
- 把本版本段落插入 `CHANGELOG.md` 顶部，保留历史内容。
- 拒绝重复版本：如果本地已存在同名 git tag、`.changes/<tag>.md`，或 `CHANGELOG.md` 已包含该版本段落，命令会直接失败。
- 展示 `CHANGELOG.md` 和 `.changes/<tag>.md` 的 diff，等待输入 `yes` 确认。
- 在 `main` 上提交 `chore: release <tag>`。
- 创建 annotated tag：`git tag -a <tag> -m "Release <tag>"`。
- 推送 `main` 和 tag 触发 release workflow。
- 将 release commit 快进同步回 `dev` 并推送 `dev`。

## GitHub Actions 发布

推送 `v*` 标签会触发 GitHub Actions 的 release 流程，生成 Windows / Linux 发布包、可选的飞牛 FPK，并创建 GitHub Release。

后端发布二进制使用 `-trimpath -tags=nomsgpack -ldflags="-s -w"` 构建，默认关闭 Gin 的 MsgPack 绑定和渲染支持，以减少发布包体积。

发布 Actions 从 GitHub Secrets 读取 `FANART_API_KEY`、`OAUTH_RELAY_ENCRYPTION_KEY`、`SC_API_KEY`、`TMDB_ACCESS_TOKEN` 和 `TMDB_API_KEY`，分别注入发布二进制的 `ldflags` 或源码 Docker 构建参数。它们只是编译期默认值；运行时 `config/.env` / 环境变量仍按现有规则覆盖编译期值，数据库中的 UI 配置优先级也不变。

GitHub Release 的标题直接使用 `v<major>.<minor>.<patch>` tag，不额外添加 `Release` 前缀；正文取自上一步提交的 `.changes/v0.xx.xx.md`。release workflow 会拒绝重复 GitHub Release 和缺失 `.changes/<tag>.md` 的发布。

发布二进制包按平台保留不同的运行文件：Linux `.tar.gz` 包包含 `scripts/docker-entrypoint.sh` 和 `scripts/watch_update.sh`，供 Docker 在线更新复用；Windows `.zip` 包只包含 `QMediaSync.exe`、`web_statics/` 和 `icon.ico`（存在时），不包含 Docker 脚本。Windows 在线更新由 `QMediaSync.exe -update <目录>` 完成，不执行这些 shell 脚本。

发布流程还会使用 `GITHUB_TOKEN` 推送 GHCR 镜像 `ghcr.io/<owner>/qmediasync:<tag>` 和 `ghcr.io/<owner>/qmediasync:latest`。

GHCR 镜像同时构建 `linux/amd64` 和 `linux/arm64`。Dockerfile 中的 `TARGETOS`、`TARGETARCH` 由 Buildx 自动注入，声明时不得设置平台默认值；否则 ARM64 镜像可能错误打包 AMD64 二进制。

也可以在 GitHub Actions 中手动触发 `release` workflow，并输入要发布的 Git tag（同样要求该 tag 对应的 `.changes/<tag>.md` 已提交）。

后端更新流程解压 `.zip`、`.tar.gz` / `.tgz` 发布包时，会拒绝绝对路径、`..` 路径逃逸、指向解压目录外的符号链接，以及经过已有符号链接组件写入文件。发布包内文件应使用相对路径，并保持符号链接目标位于包内目录。

## 飞牛 FPK

飞牛 FPK 打包依赖飞牛官方工具 `fnpack`（不公开分发）。release workflow 通过仓库 Secret `FNPACK_DOWNLOAD_URL`（指向可下载 `fnpack` 可执行文件的地址）下载安装，再用 `backend/FNOS/` 下的素材执行 `fnpack build` 生成 `*.fpk`。

未配置该 Secret 时，`fpk` job 和 `scripts/release/package-fnos.sh` 会自动跳过 FPK 打包，其余 Windows / Linux 发布包、Docker 镜像不受影响；若希望缺少工具时直接报错（而非静默跳过），可在脚本环境设置 `REQUIRE_FNPACK=1`。

调整 changelog 的分组、过滤规则可编辑仓库根目录的 `cliff.toml`。
