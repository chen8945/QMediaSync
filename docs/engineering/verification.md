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
| 配置、密钥或数据库迁移 | `helpers`、`models` 或相关控制器包测试 | 配置、数据库 schema 与运维 |
| Vue 组件、组合式函数或 HTTP 客户端 | `pnpm run test`、`pnpm lint`、`pnpm format:check`、`pnpm run type-check` | AI 协作说明、请求校验 |
| 账号授权更换跨端流程 | `cd backend && go test ./internal/requests ./internal/v115auth ./internal/v115open ./internal/models ./internal/controllers`；`cd frontend && pnpm run test -- test/components/cloud-auth test/composables/useV115DeviceAuthorization.test.ts`、`pnpm run type-check`、`pnpm run build` | [账号授权与更换](../reference/account-authorization.md) |
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

## 构建和发布命令

```bash
# 构建前端静态文件
(cd frontend && corepack enable && corepack prepare pnpm@11 --activate && pnpm install --frozen-lockfile && pnpm run build)

# 本地后端构建
(cd backend && go build -o QMediaSync .)

# 注入版本信息的构建
(cd backend && CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v1.0.0 -X 'main.PublishDate=2026-01-01'" -o QMediaSync .)

# Docker 构建
docker build -f docker/source.Dockerfile -t qmediasync .
```

跨平台构建、GitHub Actions 和 FPK 打包以 [发布流程](../operations/release.md) 为准。

## CI 覆盖边界

当前 `.github/workflows/ci.yaml` 会在 `main`、`dev`、`feature/**` 推送和 Pull Request 上依次执行前端 `pnpm run test`、`pnpm run build`（其中包含类型检查）和 `pnpm run check:build`，以及后端 `go vet ./...`、`go test ./...` 和 `go build -trimpath -tags=nomsgpack`。它不会运行前端 ESLint 或 Prettier。

因此，涉及行为、校验或兼容性的改动不能只依赖 CI 构建通过；仍应按本文档的改动范围运行相关本地验证。`feature.yaml` 和 `beta.yaml` 负责分支镜像构建，不替代 CI 或测试。

## 稳定回归验证

- 长期回归风险优先由相关 Go 包内测试保护；新增或修改测试时遵循 table-driven 模式。
- 当前端行为或源码契约需要自动保护时，在 `frontend/test/` 下按 `components/`、`composables/`、`router/`、`unit/`、`utils/` 或 `regression/` 分类创建 `*.test.ts` / `*.test.mjs`，由 Vitest 统一运行；测试应断言公开行为或稳定契约，避免绑定组件内部实现细节。
- 仅依赖构建产物的检查使用 `*.check.mjs`，通过独立脚本在 `pnpm run build` 后执行，不能使用 Vitest 测试文件后缀。
- 当包内 Go 测试、前端测试、lint、类型检查和生产构建都无法覆盖明确的长期风险时，优先补充对应测试；无法自动覆盖时，在对应契约文档中写明人工检查步骤和剩余风险。

## 文档验证

文档改动完成后执行：

```bash
git diff --check
```

检查所有相对 Markdown 链接均指向存在的文件或锚点。新增或移动正式文档后，确认仓库中的旧路径和旧名称已更新；修改 AI 入口时，确认两个兼容入口内容完全一致。
