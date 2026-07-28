# 审查并修复最新备份恢复提交：实施与验证计划

## 当前状态

**仅处于规划阶段。** 在用户批准本最终计划、任务状态变为 `in_progress` 前，不修改产品代码，也不执行破坏性 PostgreSQL 失败场景。

## 0. 实施前门禁

- [ ] 复核 `prd.md`、`design.md`、`docs/operations/database.md`、`docs/engineering/request-validation.md`、`docs/engineering/verification.md`、`docs/architecture/authentication-sessions.md` 和 `.trellis/spec/backend/database-guidelines.md`。
- [ ] 确认 `git status --short`、`git diff --check`、已批准的 `postgres:15-alpine` 镜像，以及不存在无关活动测试容器。
- [ ] 按用户决策落实未加密 v1 工件兼容策略：继续允许恢复，但明确其不能认证抗篡改完整性；本任务不实现新的外置签名格式。
- [ ] 对重叠的 backup/coordinator/taskgate 文件一次只安排一个实现代理；仅在源边界稳定后，并行运行互不写入的前端/文档测试和补丁复审。

## 1. 恢复与维护生命周期

### 产品修改

- [ ] 在修改顺序前，检索每一处 `OperationCoordinator.Transition`、`SetMaintenance`、`finishOperation`、`requestOrderlyExit`、`taskBarrier.Block` 和 `taskBarrier.Resume` 调用。
- [ ] 为业务维护中间件执行链加入请求准入/排空闸门，同时保留静态资源和精确状态路由例外。
- [ ] 调整恢复成功路径：维护必须持续到有序 HTTP 关闭开始，不能在终态持久化与 `QMSApp.Stop()` 间放行普通请求。
- [ ] 调整快照就绪前的恢复失败/panic 路径：先持久化终态/维护切换，再恢复任务子系统。
- [ ] 手动备份/恢复在受理阶段关闭任务屏障、判断冲突；存在任务时返回 `409`，只有成功受理后才启动后台 goroutine。保留定时备份的等待策略。
- [ ] 重写恢复确认的短临界区，使 `ErrOperationInProgress` 不会消耗预检记录；昂贵的工件验证仍在临界区外。

### 必需回归测试

- [ ] 已在维护前进入的业务处理器会阻塞维护切换直到其退出；后进入的业务处理器收到 `503`；状态路由仍可读取。
- [ ] 恢复成功终态路径在注入的有序关闭开始前不得放行普通业务请求。
- [ ] 快照前失败和 panic 在任务闸门重新开放、worker 恢复前发布终态/维护语义。
- [ ] 手动备份/恢复受理与任务准入竞争时，不得创建等待新任务的 `202` 操作；必须返回 `ErrTasksRunning`/HTTP `409`，且不遗留操作状态。
- [ ] 有效确认与竞争性协调器取得执行权并发时，被拒绝确认的预检 ID 仍可使用。

**预期文件：** `backend/internal/backup/{operation.go,operation_backup.go,restore_operation.go,tasks.go,runtime.go,preflight.go}`、`backend/internal/controllers/maintenance.go`、控制器/备份/操作测试、`backend/main.go`。

## 2. 任务静止与 worker 所有权

### 产品修改

- [ ] 在线程设置写数据库或变更队列前检查任务准入；以维护窗口一致的冲突响应拒绝关闭状态。
- [ ] 用队列交接替代 `InitDQ` 直接更换指针：旧队列完全停止并等待后才发布新的全局队列。
- [ ] 使 `UpdateThreads` 只执行一条完整队列变更路径，不再同时“重初始化 + 更新并发”。
- [ ] 手动 Emby 媒体信息解析参与任务准入和运行计数；维护等待可观察其存活直到完成。

### 必需回归测试

- [ ] 任务准入关闭时线程设置被拒绝，设置记录和 `GlobalDownloadQueue` 均不变化。
- [ ] 活动旧下载 worker 不能被线程更新遗弃；维护屏障必须等待其退出。
- [ ] 任务准入关闭时手动 Emby 解析在 `ProcessLibraries` 前被拒绝；已启动解析会被维护等待条件纳入。
- [ ] 已有传输重启、动态并发、同步、STRM、目录上传、Emby item 同步和 Cron 准入测试继续通过。

**预期文件：** `backend/internal/models/{settings.go,download.go,queue_admission_test.go}`、`backend/internal/controllers/settings.go`、`backend/internal/emby/emby.go`、`backend/internal/backup/tasks.go` 及相关控制器/Emby 测试。

## 3. PostgreSQL 恢复 schema 回滚

### 产品修改

- [ ] 扩展恢复快照元数据/使用方式，区分恢复前存在的目录表和 `ensureRestoreSchema` 新建的表。
- [ ] 在 `rollbackPostgres` 的单一事务中：仅清空/导入恢复前存在的表，反向目录顺序删除恢复中新建的表，并为剩余恢复表修复序列。
- [ ] 保持 SQLite 回滚行为不变。

### 确定性包内测试

- [ ] 空目标：创建快照、创建恢复 schema、回滚后断言所有目录表不存在；后续首次初始化能创建 migrator/默认记录。
- [ ] 部分目标：只建立部分表和数据，恢复期间补齐 schema，回滚后断言仅原有表和原有记录存在。
- [ ] 回滚错误仍按既有行为进入仅诊断模式。

**预期文件：** `backend/internal/backup/{restore_snapshot.go,restore_apply.go,restore_snapshot_test.go,restore_flow_test.go}`。

## 4. 迁移导入后失败与重试语义

### 产品修改

- [ ] 确认哪些导入后 schema 要求不由事务内 `migrateTargetSchema` 保证，首先覆盖活跃传输任务部分唯一索引。
- [ ] 提取并调用错误返回的导入后 schema 保证辅助函数，调用点位于删除 `migrate.zip` 之前。
- [ ] 失败时保留 `migrate.zip`、向 `main.go` 返回错误并阻止缓存、worker 和业务运行时初始化。
- [ ] 保持从完整工件重导的幂等性：重试在事务中重新清空、导入并修复序列。

### 必需测试

- [ ] 注入或触发必需导入后 schema 保证失败，断言工件保留、启动阻断、无假成功结果。
- [ ] 成功导入在工件删除前断言必需索引已经存在。
- [ ] 既有坏工件、插入失败、导入后回调失败测试持续保护验证/保留语义。

**预期文件：** `backend/internal/migrate/{artifact.go,artifact_test.go}`、`backend/internal/models/migrator.go`、`backend/main.go`、`docs/operations/database.md`。

## 5. 工件容量与未加密 v1 工件说明

### 产品修改

- [ ] 从验证数据导出内层归档已验证的物化大小，以检查过的整数运算传给 SQLite/PostgreSQL 容量检查。
- [ ] 计算快照、临时数据库和暂存配置/日志文件的最大同时空间；保持稳定的空间不足错误映射。
- [ ] 在恢复界面、接口文案和权威文档明确未加密 v1 工件的已选策略：可恢复、能检测偶然损坏、不能认证抗篡改完整性。
- [ ] 不增加基于嵌入 `config/encryption.key` 的伪 HMAC；外置签名密钥的新工件格式留给后续独立任务。

### 必需测试

- [ ] 使用压缩体积小但已验证内容体积大的投影，断言预检在创建快照前以空间不足拒绝。
- [ ] 现有加密工件往返和篡改拒绝测试保持不变。
- [ ] 未加密 v1 已选策略有后端、前端或文档回归保护。

**预期文件：** `backend/internal/backup/{artifact*.go,restore_preflight.go,restore_target.go,restore_target_test.go}`、备份 UI 组件、`docs/operations/database.md`、`docs/operations/configuration.md`。

## 6. CORS、multipart 与前端状态

### 产品修改

- [ ] 在可信来源 CORS 允许请求头中加入 `X-Backup-Operation-Token`。
- [ ] 在任何 multipart 绑定/解析前安装请求体大小上限，保留 `Content-Length` 提前拒绝和对 chunked 请求的流式限制。
- [ ] 手动备份进入终态后，活动且可见的备份记录页面刷新权威列表快照；保持目录清点轮询、隐藏页和卸载清理语义。
- [ ] 用 Go `unicode.IsSpace` 等价集合/谓词替换 JavaScript 空白判定，并保留长度、字符类别、不裁剪和不归一化规则。

### 必需测试

- [ ] 配置可信来源的 CORS OPTIONS 请求要求操作令牌头，响应 `Access-Control-Allow-Headers` 必须包含它。
- [ ] 超限 multipart 请求在完整表单解析/暂存前被拒绝；小型正常工件路径仍通过。
- [ ] 终态备份轮询恰好触发一次活动记录列表刷新，并显示最新终态/记录；隐藏或卸载页面不刷新。
- [ ] 密码测试覆盖 U+0085 被拒绝、U+FEFF 被允许，以及普通空格、全角空格、制表符和换行。

**预期文件：** `backend/internal/controllers/{base.go,backup.go,auth_security_test.go,backup_response_test.go}`、`frontend/src/{stores/backup.ts,components/AppBackupRecords.vue,utils/backupPassword.ts}`、对应前端测试、认证/请求校验文档。

## 7. PostgreSQL 15 短生命周期验证

仅在源代码修改和包内测试通过后执行。容器不使用生产数据、不提交 compose 文件、不安装主机依赖、不固定共享端口。

### 生命周期

- [ ] 在调用 shell 中生成临时容器名、用户、密码、数据库和临时 Docker volume/network 标识；不得输出凭据。
- [ ] 用 `postgres:15-alpine` 启动仅绑定 loopback 的动态端口（或使用隔离网络）；在容器内轮询 `pg_isready`。
- [ ] 仅通过测试进程环境变量传递短生命周期 DSN；不得写入配置、任务产物、源码或日志。
- [ ] 使用 `trap`/`defer` 清理：成功、断言失败或中断时均强制删除容器及 volume/network。

### PostgreSQL 15 实际断言

- [ ] **空目标恢复回滚：** 创建空目标快照，允许恢复创建 schema，模拟 schema 创建后的失败/回滚；断言目录表已消失，并确认正常首次初始化能创建 migrator/默认记录。
- [ ] **部分目标恢复回滚：** 预建部分表/数据，恢复中创建其余 schema，回滚后断言原 schema/记录存在且新建表已删除。
- [ ] **迁移导入回滚：** 强制导入/约束失败，断言导入前目标数据保留、`migrate.zip` 保留，启动级调用拒绝正常运行时。
- [ ] **迁移成功与序列：** 导入含明确 ID 的工件，断言预期记录和必需部分索引存在；只有全部成功后 `migrate.zip` 消失；新插入记录的生成 ID 高于导入最大值。
- [ ] **导入后失败：** 强制错误返回的导入后 schema 保证路径失败，断言工件保留和启动阻断；修复条件后用同一工件成功重试。

### 测试形式与命令

- [ ] 常规确定性表驱动/单元测试继续在普通 `go test` 下运行。
- [ ] 若真实 DSN 必需，新增带 `//go:build integration` 的 PostgreSQL 测试；缺少 `QMS_TEST_POSTGRES_DSN` 时必须清晰跳过。
- [ ] 通过临时 DSN 执行范围命令，例如：
  ```bash
  (cd backend && QMS_TEST_POSTGRES_DSN="$DSN" go test -tags=integration ./internal/backup ./internal/migrate)
  ```
- [ ] 单独运行非集成包。跳过的集成测试绝不能被报告为真实 PostgreSQL 成功。

## 8. 验证矩阵

### 后端

- [ ] `(cd backend && go test ./internal/backup ./internal/migrate ./internal/models ./internal/taskgate ./internal/controllers ./internal/emby)`
- [ ] `(cd backend && go test -race ./internal/backup ./internal/taskgate ./internal/models ./internal/synccron ./internal/syncstrm ./internal/directoryupload ./internal/emby)`
- [ ] `(cd backend && go test -race ./internal/controllers -run 'TestMaintenanceMiddlewareBlocksBusinessApisAndKeepsStatusReadable|TestBackupOperationEndpointsConflictBeforeMaintenance|TestBackupStatusRequiresOperationIDAndToken|<新增维护测试>')`
- [ ] `(cd backend && go vet ./...)`
- [ ] 执行本计划第 7 节的 PostgreSQL 15 集成命令。
- [ ] 聚焦验证通过后执行 `(cd backend && go test ./...)`。

### 前端

- [ ] `(cd frontend && pnpm run test)`
- [ ] `(cd frontend && pnpm lint)`
- [ ] `(cd frontend && pnpm run type-check)`
- [ ] `(cd frontend && pnpm run build)`
- [ ] `(cd frontend && pnpm run check:build)`
- [ ] `(cd frontend && pnpm format:check)`；在做任何纯格式化修改前，先核实或独立报告既有 `test/stores/backup.test.ts` 格式基线问题。

### 文档与复审

- [ ] `git diff --check`
- [ ] 验证每个变更的相对 Markdown 链接。
- [ ] 验证文档与已选未加密 v1 策略、PostgreSQL 实测结果完全一致。
- [ ] 对修复结果进行独立复审，覆盖生命周期、迁移、API/前端和测试质量；只处理新确认的问题，再运行受影响验证。

## 回滚点

- 每个问题簇均可独立验证：生命周期/闸门 → 队列/Emby → PostgreSQL 回滚/迁移 → 工件/API/前端。
- 任一问题簇未通过聚焦测试时，只回退该簇的局部源代码变更；不得使用 `git reset --hard`、覆盖无关工作或压制测试。
- PostgreSQL 容器无法启动或测试无法连接时，仍执行清理，并记录准确失败命令及未验证断言。