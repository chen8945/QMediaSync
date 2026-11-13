# 媒体服务器客户端

`Client` 只持有一个 URL/API Key 连接，统一提供查询、刷新、详情、图片和媒体信息提取。现有调用保留 `embyclientrestgo` 包标识；固定业务入口使用 `NewClientForProduct` 传入预期产品。认证、路由、参数和 GUID 规范分别放在 `emby.go`、`jellyfin.go`，传输、分页完整性与业务入口共用。

`CheckConnection` 在尚无明确产品结果时读取 Ping。`ConnectionInfo.Product` 仅接受完整明确产品名；未知或探测失败沿用 Emby 协议，`Diagnostic` 保留诊断且失败不缓存。明确产品与预期不符时返回错误。需要真实实例身份时使用 `GetServerID`：先读 Public，复用其中的明确产品或已有产品结果，必要时才补 Ping，空 ID 或 Public 失败始终返回错误。两条探测路由各自只在 HTTP 404 时尝试 `/emby` 前缀；配置子路径保留。公开操作检查连接，内部分页和补查复用结果；失败探测在后续操作重试。结果缓存与在途合并只属于当前客户端，网络请求在锁外执行，等待可取消。

合并 Ping 或 Public 探测时，等待者只遵循自己的取消信号。探测发起者取消或超时后，仍有效的等待者重新探测；不会继承其他请求的取消错误，也不会跳过产品错配或真实实例身份校验。

普通详情保留动态来源 ID；只有静态索引和删除证据才把 Jellyfin 静态来源关联到规范 GUID。删除清单要求完整分页；按 ID 查询漏项时用合法库用户逐项核验隐藏版本，单项 HTTP 404 仅补证该项缺失，业务层仍承担冻结证据和删除授权核验。

全部媒体库可见用户的查询保留 `EnableAllFolders` 筛选，`EnableMediaPlayback` 由提取层额外筛选。媒体提取只请求 PlaybackInfo，不启动播放或转码；`ValidatePlaybackInfoResponse` 同时供旧 URL 任务使用，验证状态、完整 JSON、ErrorCode 和按媒体类型可用的主要流，不要求双流、正时长或可选字段。刷新成功只表示服务端接受请求，最终扫描或元数据变化由业务验证。

从 `backend/` 运行：

```sh
go test ./internal/medialibrary/client
go test -race ./internal/medialibrary/client
go vet ./internal/medialibrary/client
```
