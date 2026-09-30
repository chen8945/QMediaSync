# 开发调试

> 职责：说明本地后端、前端启动和开发调试的入口。
>
> 权威范围：本文档维护本地运行方式；前端实现约定见 [前端开发约定](frontend-development.md)，验证命令见 [验证说明](verification.md)，仓库目录见 [仓库结构](repository-structure.md)。
>
> 修改时机：修改开发服务、Vite 代理、开发依赖版本或调试入口时必须更新本文档。
>
> 相关代码：`backend/main.go`、`frontend/vite.config.ts`、`frontend/package.json`。

## 后端启动

使用 Go 1.27.1 或更新的兼容工具链，最低版本以 `backend/go.mod` 为准。

```bash
cd backend
go run .
```

如果 `config/config.yaml` 和兼容的旧 `config/config.yml` 都不存在，后端会先启动配置向导。向导绑定 `:12333`，本机可访问 `http://127.0.0.1:12333`；配置保存后需要重新启动后端。

## 前端启动

前端依赖要求 Node `>=22.22.2`，pnpm 使用 11.x。

当前依赖基线为 Vue 3.5、Pinia 3、TypeScript 6、Vite 8 和 Vitest 4；`@types/node` 保持 22.x，与 CI 和 Docker 的 Node 22 环境对应。依赖声明见 `frontend/package.json`，实际安装版本以 `frontend/pnpm-lock.yaml` 为准。

```bash
cd frontend
corepack enable
corepack prepare pnpm@11 --activate
pnpm install
pnpm run dev
```

前端开发页面使用相对 `/api`，Vite 会把它代理到 `http://localhost:12333`；HTTP API 和 SSE 都经过该同源代理。

后端默认允许 Vite 开发来源 `http://localhost:5173`、`http://127.0.0.1:5173` 和 `http://[::1]:5173` 携带浏览器登录 Cookie。若修改 Vite 端口、使用自定义开发域名，或让前端跨源访问后端，需要把前端来源加入 `config/config.yaml`：

```yaml
trustedOrigins:
  - http://dev.example.local:5173
```

前端 HTTP 客户端、响应式组件、实时状态刷新、路由历史、通知渠道热刷新和 UI 反馈约定见 [前端开发约定](frontend-development.md)。SSE 协议与断线恢复见 [实时事件](../architecture/realtime-events.md)；上传和 STRM 的状态机见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。

## 常用验证

时间字段策略见 [数据库 schema 与迁移 - 时间字段策略](../reference/database-schema.md#时间字段策略)，Cron 边界见 [请求校验约定 - Cron 表达式边界](request-validation.md#cron-表达式边界)，Emby 同步架构见 [Emby 媒体库同步](../architecture/emby-library-sync.md)。完整验证范围见 [验证说明](verification.md)。

后端生产构建默认关闭 Gin 的 MsgPack 绑定和渲染支持，以缩小二进制体积：

```bash
(cd backend && CGO_ENABLED=0 go build -trimpath -tags=nomsgpack -ldflags="-s -w" -o QMediaSync .)
```

前端生产构建输出到 `frontend/dist`。发布、Docker 和离线包会把该目录作为 Web UI 静态资源输入，并在最终运行目录中放置为 `web_statics`。

## 退出

- Linux：按 `Ctrl+C` 退出。
- Windows：在系统托盘找到 QMediaSync 图标，右键退出。
