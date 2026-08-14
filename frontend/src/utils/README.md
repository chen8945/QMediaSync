# 工具函数目录

`frontend/src/utils` 存放前端跨组件复用的小工具。当前目录按功能拆分为以下文件。

## clipboard.ts

文本复制辅助：

- `copyText(content)`：优先使用 Clipboard API；不可用、权限不足或非安全上下文失败时回退到 `document.execCommand('copy')`。空文本或两种方式都不可用时返回 `false`。

## csrf.ts

浏览器 Cookie 中 CSRF Token 的读取和请求方法判断：

- `shouldAttachCSRFToken(method)`：仅对 `POST`、`PUT`、`PATCH`、`DELETE` 返回 `true`。
- `getCSRFTokenFromCookie()`：读取并解码 `csrf_token` Cookie；Cookie 不存在时返回空字符串。

该文件由 `frontend/src/http/client.ts` 使用。Cookie、请求头和服务端校验的安全契约见 [认证与浏览器会话](../../../docs/architecture/authentication-sessions.md)。

## cloudAccountUtils.ts

云盘账号和 115 开放平台应用信息展示辅助：

- `CloudAccountAppInfo`
- `isCustomV115App(account)`
- `getV115AppInfoRows(account)`

## deviceUtils.ts

设备类型判断和窗口尺寸变化监听：

- `isMobile()`
- `onDeviceTypeChange(callback)`

`deviceUtils.ts` 是底层工具。Vue 组件需要响应屏幕尺寸变化时，应使用
`frontend/src/composables/useDeviceType.ts`，由 composable 统一管理监听和卸载清理；
不要在组件中直接调用 `onDeviceTypeChange()`。

```typescript
const { isMobile } = useDeviceType()
```

## directoryUploadRules.ts

目录监控上传规则的页面展示辅助：

- `groupDirectoryUploadRulesBySyncPath(rules)`
- `getEnabledDirectoryUploadRules(rules)`
- `formatDirectoryUploadStatus(rules, masterEnabled, loadFailed)`
- `formatDirectoryUploadPathSummary(rules)`

这些函数只聚合或展示已取得的规则，不校验规则合法性，也不写入后端。规则保存、幂等和错误契约见 [同步目录聚合 API](../../../docs/reference/sync-path-api.md)。

## fileIconUtils.ts

文件类型识别和 Element Plus 图标名称映射：

- `getFileType(filename)`
- `getFileIcon(type, isDirectory)`
- `getFileIconByName(filename, isDirectory)`

```typescript
const icon = getFileIconByName('movie.mp4')
```

支持的视频扩展名：`mp4`、`mkv`、`avi`、`mov`、`wmv`、`flv`、`m4v`、`webm`、`ts`、`rmvb`、`rm`、`3gp`、`mpg`、`mpeg`。

支持的图片扩展名：`jpg`、`jpeg`、`png`、`gif`、`bmp`、`webp`、`svg`、`ico`、`tiff`、`tga`。

## fileSizeUtils.ts

文件大小格式化：

- `formatFileSize(bytes)`

## logLevel.ts

日志等级选项和前端筛选：

- `LogLevelOption`
- `LOG_LEVEL_OPTIONS`
- `DEFAULT_VISIBLE_LOG_LEVELS`
- `isLogLevel(value)`
- `filterLogEntriesByLevels(entries, levels)`

筛选函数返回传入列表中等级被选中的条目；空等级列表返回空数组。

## notificationUtils.ts

通知渠道、事件类型和展示文本辅助：

- `ChannelType`
- `EventType`
- `NotificationChannel`
- `NotificationConfig`
- `NotificationRule`
- `getChannelTypeName(type)`
- `getChannelTypeColor(type)`
- `getEventTypeName(type)`
- `getEventTypeDescription(type)`
- `WebhookHeaderRow`
- `webhookHeaderRecordToRows(headers)`
- `webhookHeaderRowsToRecord(rows)`

Webhook header 转换会忽略空白键名；相同键名以后出现的行覆盖先前值。

## navigation.ts

应用内路由历史辅助：

- `hasAppBackHistory(historyState)`
- `navigateBackOrReplace(router, fallback)`

`navigateBackOrReplace` 用于详情页和表单页的返回 / 取消操作。它优先使用 Vue Router 写入的 `history.state.back` 判断是否存在应用内上一页；没有上一页时使用 `router.replace(fallback)` 回到兜底列表页，避免直接打开深链后回退到应用外页面。

## oauthCallback.ts

OAuth 回调参数收集：

- `collectOAuthCallbackParams(search, hash)`

该函数会合并普通 query string 和 hash 中的 query 参数。

## v115AuthorizationSession.ts

115 直接跳转 OAuth 时的当前标签页临时会话：

- `PendingV115Authorization`
- `savePendingV115Authorization(pending)`
- `loadPendingV115Authorization()`
- `clearPendingV115Authorization(authorizationId)`

只保存账号 ID 和授权会话 ID，不保存令牌。页面从第三方授权页返回后，调用方应只继续处理匹配的回调，其他情况取消并清理会话。
完整的账号授权会话和回滚边界见 [账号授权与更换](../../../docs/reference/account-authorization.md)。

## queueStatusUtils.ts

下载和上传队列状态快照的兼容和页面操作辅助：

- `QueueStatusSnapshot`
- `emptyQueueStatusSnapshot()`
- `normalizeQueueStatusSnapshot(value, fallbackRunning)`
- `canPauseQueue(snapshot)`
- `canResumeQueue(snapshot)`
- `hasActiveQueueTasks(snapshot)`
- `QueueRowWithStatus`
- `removePendingQueueRows(rows)`

`normalizeQueueStatusSnapshot` 同时兼容旧的布尔运行状态和对象快照；缺失或非数值计数归零。`removePendingQueueRows` 只移除 `status=0` 的本地行，不请求后端。

## queueTaskDetailUtils.ts

下载和上传队列的详情展示辅助：

- `QueueTaskDetailField`、`QueueTaskDetailGroup`
- `buildDownloadTaskDetailGroups(task)`
- `buildUploadTaskDetailGroups(task)`
- `getQueueRemotePathSummary(remoteFullPath)`、`getDownloadQueueLocationSummary(task)`、`getUploadQueueLocationSummary(task)`
- 下载 / 上传状态文案与标签类型映射
- `getUploadStageSummaryLabel(task)`、`getUploadStageResultTagType(task)`
- `getUploadTransportDetailRows(task)`、`getUploadTransportDetailSummary(task)`

这些函数只把接口已返回的队列字段组织为摘要或展开详情，且省略不适用或空值字段；不修改队列状态、请求、SSE payload 或本地列表。`getQueueRemotePathSummary` 仅接受远端完整路径，历史记录缺失时返回明确的未知路径文案，不会把文件 ID 回退显示为路径。下载与上传摘要函数都会排除不应作为远端身份展示的字段；本地复制上传以“复制到”明确展示本地目标。展开详情使用轻量的纵向标签布局，并按分组独立指定列数：基本信息紧凑排列，上传传输信息优先并列展示大小、阶段和结果，文件信息中的路径独占整行，执行时间包含开始、结束和可计算的耗时。上传信息优先并列展示断点续传、源文件清理和相对监控根目录。上传传输详情 Tooltip 会同时包含续传、分片、目录监控源文件清理信息和实际存在的完整失败原因。

详情将远端目录路径、远端完整路径、远端文件 ID 和 PickCode 分别展示。文件 ID、PickCode、SHA1 / MD5 和覆盖旧文件 ID 只在来源语义成立且值非空时显示；OpenList 可展示可用对象 ID 和明确的哈希，local 与 Emby 不展示远端身份，下载直链和其他隐藏执行字段绝不进入摘要或详情。

## sourceTypeUtils.ts

同步来源类型展示配置：

- `sourceTypeOptions`
- `sourceTypeTagMap`
- `sourceTypeMap`

当前启用的来源类型包括 `115`、`baidupan`、`openlist`、`local`。

`123` 的展示配置仍以注释形式保留，当前不在选项、标签或名称映射中启用。完整存储来源类型和任务队列的展示差异见 [任务来源](../../../docs/reference/task-sources.md)。

## syncRecordEvents.ts

同步记录全局 SSE 事件的本地列表 patch：

- `SyncRecordEventType`
- `SyncRecordRow`
- `SyncTaskRecordEventPayload`
- `ApplySyncRecordEventPatchOptions`
- `ApplySyncRecordEventPatchResult`
- `mapSyncTaskPayloadToRecord(payload, now)`
- `applySyncRecordEventPatch(options)`

更新和删除当前页已有记录时直接 patch；新建记录只在第一页插入。其他页收到新建事件时以 `refreshNeeded=true` 要求调用方重新获取 HTTP 快照。SSE 的至多一次通知和快照收敛语义见 [实时事件](../../../docs/architecture/realtime-events.md)。

## syncTaskEventSequence.ts

同步任务事件的本地 sequence 水位控制：

- `shouldApplySyncTaskEvent(sequences, syncID, sequence, deleted)`
- `resetSyncTaskEventSequences(sequences)`

删除事件总会被接受并清除该任务水位；HTTP 快照重新收敛前必须清空水位，避免服务端 sequence 重置后丢弃新事件。

## syncRefreshDecision.ts

同步任务完成后是否提交 Emby 媒体库刷新的展示决策：

- `SyncRefreshDecisionInput`
- `SyncRefreshDecisionTagType`
- `SyncRefreshDecision`
- `getEmbyRefreshDecision(input)`

该函数根据同步结果中的新增 STRM 数、下载元数据数和任务状态派生展示状态。前端只展示是否存在媒体库刷新相关变更，不声明后端已提交刷新任务；实际刷新还需要后端满足 Emby 已启用和同步目录已关联媒体库等条件。

## taskSourceUtils.ts

任务来源、同步队列任务类型和来源类型展示映射：

- `downloadSourceNameMap`
- `uploadSourceNameMap`
- `syncTaskTypeNameMap`
- `getDownloadSourceName(source)`
- `getUploadSourceName(source)`
- `getSyncTaskTypeName(taskType)`
- `getTaskSourceTypeName(type)`
- `getTaskSourceTypeTagType(type)`

稳定机器值、队列 API / SSE 暴露和迁移边界见 [任务来源枚举](../../../docs/reference/task-sources.md)。

`syncTaskTypeNameMap` 保留 `directory_monitor` 的展示条目，但当前后端同步调度任务类型只有 `strm_sync` 和 `scrape_organize`；不要据此把 `directory_monitor` 当作同步调度器可入队的任务类型。

## uploadQueueDisplayUtils.ts

上传队列展示辅助：

- `UploadQueueDisplayTask`
- `UploadQueuePatch`
- `UploadTaskDetailRow`
- `getUploadPhaseLabel(task)`
- `getUploadResultLabel(task)`
- `getUploadStageOrResultLabel(task)`
- `getUploadProgressPercent(task)`
- `formatByteRate(bytesPerSecond)`
- `getUploadedSizeLabel(task)`
- `getResumeStateLabel(resumeState)`
- `getSourceCleanupStatusLabel(cleanupStatus)`
- `getUploadTaskDetailRows(task)`
- `applyUploadQueuePatch(rows, patch)`：合并上传进度、分片、断点续传和目录监控源文件清理状态 patch（包括 `source_deleted_at`）。`source_cleanup_error` 可能是空字符串，用于清空前端已有的失败原因，不要用 truthy 判断跳过。

后端返回的 `upload_phase`、`upload_result` 和 `source_cleanup_status` 保持机器值，前端在这里统一映射为用户可读文案；例如上传状态 `5` 对应的 `remote_completed_pending_finalize` 显示为“等待完成处理”，状态 `6` 对应的 `remote_completed_finalizing` 显示为“正在完成处理”。不要把展示文案回写到接口字段或数据库字段。

完成态中 `uploaded_bytes=0` 可能表示后端未跟踪字节数，后端快照中的 `progress_percent=0` 也可能只是同一未知值的序列化结果；前端因此保持完整文件大小和 100% 进度的兼容回退。`skipped_after_rapid_wait` 是明确的例外：它表示秒传等待超时后按配置跳过真实上传，进度和已上传大小固定显示为零，不能按完成态回退为完整上传。

## timeUtils.ts

时间、存储空间和状态样式辅助。业务时间统一使用后端返回的 Unix 秒，并在前端按浏览器本地环境格式化；日志字符串保持原始日志格式，不强制转换。新接口如需毫秒时间或耗时，字段名必须使用 `_ms` 后缀，例如 `duration_ms`、`event_time_ms`。

完整时间字段策略见 [`docs/reference/database-schema.md`](../../../docs/reference/database-schema.md#时间字段策略)，Emby 同步状态字段说明见 [`docs/architecture/emby-library-sync.md`](../../../docs/architecture/emby-library-sync.md)。

- `MaybeUnixDateTime`
- `MaybeTimeValue`
- `normalizeLegacyDateTime(value)`
- `formatUnixDateTime(timestamp)`
- `formatMaybeUnixDateTime(value)`
- `formatUnixDate(timestamp)`
- `formatRelativeTime(value)`
- `formatTimestamp(timestamp)`
- `formatDateTime(timestamp)`
- `formatTime(timestamp)`
- `formatStorage(bytes)`
- `getStoragePercent(used, total)`
- `getProgressColor(used, total)`
- `getMemberClass(level)`
- `formatExpireTime(expireTime)`
- `getExpireClass(expireTime)`
- `formatDuration(seconds)`

兼容规则：

- Unix 秒数字按秒处理，展示为本地日期时间。
- RFC3339 UTC 字符串按浏览器本地时区展示。
- 旧无时区 `date` 字符串只作为兼容回退；新接口不要继续新增这种字段。
- 空值、`0` 和非法值返回 `-`，业务组件可按场景转换为“未同步”等文案。

## userCredentials.ts

用户名、密码和 Element Plus 表单规则辅助：

- `userCredentialLimits`
- `CredentialTextRule`
- `getTextLength(value)`
- `createUsernameRule(label)`
- `createPasswordRule(label)`
- `createLoginUsernameRule(label)`
- `createLoginPasswordRule(label)`
- `validateUsername(username, label)`
- `validatePassword(password, label)`
- `createElementCredentialRule(rule)`

创建 / 修改用户名要求 3–20 个字符且只含英文和数字；密码至少 6 个字符，不能是纯数字或纯字母。登录表单只执行非空和用户名最大长度检查，不能用它替代创建或修改时的规则。服务端绑定和校验边界见 [请求校验约定](../../../docs/engineering/request-validation.md)。

## mockAPI.ts

当前为空文件，保留为本地开发或后续模拟接口辅助的占位文件。
