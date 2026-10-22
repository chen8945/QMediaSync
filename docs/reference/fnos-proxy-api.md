# 飞牛影视反代 API

> 职责：定义飞牛影视反代的配置 API、运行状态和播放接管边界。
>
> 权威范围：本文档维护接口与跨模块不变量；配置步骤、证书和网络部署见 [飞牛影视反代运维](../operations/fnos-proxy.md)，持久化结构见 [数据库 schema](database-schema.md#fnos_proxy_config)。
>
> 修改时机：修改配置字段、状态语义、鉴权、接管路由、来源缓存或内部转交规则时必须同步更新。
>
> 相关代码：`backend/internal/controllers/fnos_proxy.go`、`backend/internal/requests/fnos_proxy.go`、`backend/internal/models/fnos_proxy_config.go`、`backend/internal/fnosproxy/`、`backend/main.go`、`frontend/src/api/fnosProxy.ts`。

## 管理入口和鉴权

主站提供以下接口，使用现有 QMediaSync 管理 API 认证与浏览器写入保护，不接受飞牛影视登录态代替 QMediaSync 身份：

```http
GET /api/setting/fnos-proxy
POST /api/setting/fnos-proxy
Content-Type: application/json
```

认证、Cookie、API Key、来源和 CSRF 的通用规则见 [认证会话](../architecture/authentication-sessions.md)。独立飞牛反代端口不挂载 QMediaSync 的管理 API。

GET 只读取配置和进程运行状态，不创建默认记录。POST 先原子保存单实例配置，再调用服务的 `Apply`，返回 `data.config` 与实际 `data.runtime`；普通保存无需重启 QMediaSync。读取和成功保存的响应带 `Cache-Control: no-store, private`。

## 保存字段

```json
{
  "enabled": true,
  "fnos_url": "http://fnos-video:8005",
  "path_mappings": [
    {"fnos_root": "/vol1/media/strm", "local_root": "/media/strm"}
  ]
}
```

| 字段 | 语义与校验 |
| --- | --- |
| `enabled` | 默认 `false`；保存后启用或关闭飞牛影视反代。关闭状态不启动飞牛监听器或专用后台任务 |
| `fnos_url` | 飞牛影视根地址；启用时必填 HTTP(S) 地址，不含账号密码、路径、查询参数或 fragment；保存时去掉首尾空白及末尾 `/` |
| `path_mappings` | 完整映射数组，最多 64 条；`[]` 清空。不包含 `sync_path_id`，不关联 Emby 删除权限 |
| `path_mappings[].fnos_root` | 飞牛影视实际看到的 STRM 绝对根目录，支持 POSIX、Windows 盘符和 UNC 路径；不能重复或重叠 |
| `path_mappings[].local_root` | QMediaSync 运行系统可访问的绝对根目录；Docker 中为容器内路径 |

保存 Body 限制为 128 KiB，不适用于视频流。根路径不得包含 `.`、`..` 或控制字符；路径按组件匹配，不使用普通字符串前缀换根。关闭整个功能仍校验保留的非空地址和映射，拒绝明显指向本反代或本机主站、Emby 的上游配置。监听固定使用 HTTP `9005`、HTTPS `9006`，不属于保存字段；启动与保存共用保留端口和本机上游回环校验；即使已存配置来自恢复或主站后来调整端口，飞牛也不得先占用主站或 Emby302 的配置端口。实际外部端口占用在应用配置时确认。

这是整份配置的保存接口；调用方应提交全部业务字段，不能把省略字段当作局部更新。布尔 `false` 和空映射必须实际入库；损坏的已存映射 JSON 返回错误，不按默认值覆盖。不增加自定义端口、HTTPS 开关或证书字段。

## 响应与运行状态

响应沿用 `APIResponse`，成功 `code=200`。以下省略配置记录的主键与时间元字段：

```json
{
  "code": 200,
  "message": "飞牛影视反代配置已保存并生效",
  "data": {
    "config": {
      "enabled": true,
      "fnos_url": "http://fnos-video:8005",
      "path_mappings": []
    },
    "runtime": {
      "running": true,
      "listen_port": 9005,
      "https_enabled": false,
      "https_port": 0,
      "restart_required": false,
      "last_error": ""
    }
  }
}
```

| 运行字段 | 含义 |
| --- | --- |
| `running` | 当前实例 HTTP 已运行；不是已保存的启用开关，HTTPS 单独失败时仍可为 `true` |
| `listen_port` | 当前实际 HTTP 端口；未运行为 `0` |
| `https_enabled` | 当前实际提供 HTTPS；不能由证书文件存在或管理页面协议推断 |
| `https_port` | 当前实际 HTTPS 端口；未运行或仅 HTTP 时为 `0` |
| `restart_required` | 遗留字段名，表示已保存配置与当前运行实例未一致；前端提示“未应用／再次保存”，不作为普通保存必须重启的依据 |
| `last_error` | 脱敏的实例启动或运行错误；不包含凭据、私钥和证书内容 |

未保存过时返回功能关闭、空地址和空映射，未运行的端口为 `0`。启用后 HTTP 固定监听 `9005`；主站证书和私钥两文件均存在时自动尝试附加 HTTPS `9006`，缺任一文件仅运行 HTTP。证书无效、不可读或 HTTPS 绑定失败时，保留 HTTP 并在 `last_error` 报告 HTTPS 问题；HTTP 绑定失败才使飞牛模块停止。主站和 Emby302 不受影响，页面依据实际运行字段显示状态标记和必要的错误／未应用提示，不生成连接地址示例；保存结果在保存按钮附近展示。

绑定或字段校验错误返回 HTTP `400`；读取或保存数据库失败沿用业务失败响应。数据库保存失败不改变运行服务；数据库已保存但 Apply 失败仍返回 `code=200`，携带已保存配置、实际运行状态和脱敏错误，页面显示“已保存但未完全应用”警告。不能把此响应描述为未保存，也不能只凭 `code=200` 宣称功能已运行。新实例校验和传输构造在停止旧实例前完成，此阶段失败时旧实例仍可能运行，但新配置未应用，应结合 `restart_required` 和 `last_error` 判断；进入切换后失败不自动回退旧配置。修正原因后再次保存重试。

同一健康配置重复保存不重建实例，也不打断播放。启停、上游或映射变化需要重建飞牛模块，会中断本模块在途播放及 WebSocket 并清空来源缓存，主站和 Emby302 不重启。HTTPS 失败等非健康状态可通过再次保存重试；健康同配置保存不重载证书，因此新增或替换证书后仍需重启 QMediaSync。Apply 受恢复维护门禁约束，不能用普通保存绕过恢复完成后必须重启的流程。

## 播放接管与回退

除下表中的播放响应外，普通页面、API、图片、字幕、非 STRM 视频和升级连接透传。接管覆盖 Emby 兼容 STRM 链路和已经核对的原生网页接口：

| 入口 | 行为 |
| --- | --- |
| `[/emby]/Items/{id}/PlaybackInfo` GET／POST | 只改写已经安全定位并解析成功的 STRM 媒体源，保留其他媒体源和未知字段 |
| `[/emby]/Videos/{id}/stream`、`original` 及兼容后缀 | 当前用户和准确媒体源核验后尝试接管 |
| `master.m3u8` | 仅接管已确认的目标 STRM，不劫持所有 HLS |
| `[/emby]/Items/{id}/Download`、`File` GET／HEAD | 使用相同的权限、媒体源和路径核验，并额外尊重用户的禁止下载设置 |
| `POST /v/api/v1/stream` | 原生请求先交给飞牛验签和授权；只在成功响应与本地 STRM 身份、内容一致时内部取链并改写直接播放 URL |

已知兼容路由规范化 `/emby` 前缀，包括电视剧的 `/Shows/{id}/Seasons` 和 `/Shows/{id}/Episodes`，保留查询参数；不给所有原生 API 和静态资源添加前缀。原生网页接管针对飞牛影视 0.9.8、服务版本 0.8.43 的已知响应，不代表所有播放接口、版本和客户端已覆盖。

飞牛影视 0.9.8 的兼容协议中，条目 `ItemID` 与媒体源 `MediaSourceId` 可能不同。GET PlaybackInfo 必须保留客户端请求的准确 `MediaSourceId`，不能用条目 ID 替换或省略；改写后的取流路径仍使用原条目 ID，查询参数使用已经核验的源 ID。条目查询返回 HTTP `200` 但对象为空也不视为授权成功。某些 POST PlaybackInfo 请求在上游落入 SPA 并返回 HTML，此时保留原响应，不改成 GET 重试，也不合成上游不支持的播放信息。本功能不代替飞牛补齐客户端协议能力，这些版本事实不代表所有设备链路已验收。

兼容协议中，当前用户策略明确禁用账号、禁止媒体播放或强制远程来源转码时，不改写直播放能力，也不接管取流，交回上游执行原策略。Download／File 还检查 `EnableContentDownloading=false`；播放权限不能替代下载许可。查询参数 `PlayMethod=Transcode` 或 `static=false`（名称和值均忽略大小写），以及重复或大小写冲突的这两类参数，在 PlaybackInfo 改写前和取流前均排除，保留上游响应与转码能力；不因先改写为 `static=true` 丢失客户端意图。其他歧义参数和未经核验的媒体源同样不切换为直连。原生请求沿用飞牛本次验签、授权和播放策略结果，见下文。

GET／POST PlaybackInfo 以及取流入口同时检查查询参数 `EnableDirectPlay` 和 `EnableDirectStream`（名称和值忽略大小写）：缺省或唯一的 `true` 允许继续核验，任一为 `false`、空值、其他值、重复或大小写冲突时保留上游响应与转码能力。

POST PlaybackInfo 还检查 JSON 顶层的这两个标记（字段名忽略大小写）：任一为 `false`、非布尔值或重复出现时，保留上游响应与转码能力。除空白请求体外，无法解析为唯一 JSON 对象时同样透传；缺省标记或两项均为 `true` 不因此阻止接管。检查复用有大小限制的原始请求体缓冲，不改写发往飞牛的请求字节或未知字段。

兼容 PlaybackInfo 成功改写后统一设置 `Cache-Control: no-store, private`，覆盖上游可能允许公共缓存的策略，避免改写地址中的当前用户凭据进入共享缓存；未改写的响应保留上游缓存策略。

显式映射优先；无映射时只读已知输出根中的相同绝对路径，或使用可靠来源身份唯一确认的有限候选。仅同名、仅唯一文件或路径相似不能证明对应关系。无法确认、无权限、文件不可读、不是 STRM 或格式不能改写时保留上游行为，不假装接管成功。受限目录读取阻止路径穿越和链接逃逸，文件上限 64 KiB，并核对读取前后身份和状态。

已确认属于当前实例的 URL 内部交给既有 `/115/url/...`、`/115/newurl`、`/baidupan/url/...`、`/openlist/url` 处理器，保留 UA、取链缓存、播放模式和多端播放决策。复用的 OpenList、百度网盘取链处理器将请求 context 传给实际文件详情请求；OpenList 重试和百度缓存锁等待同样响应取消，原生元数据超时、客户端取消和实例停止可中断当前出站请求与等待。百度请求取消后不采纳迟到的成功结果，不回填该结果的直链缓存或返回播放跳转；OpenList 认证刷新仍遵循既有共享刷新生命周期。播放器无需访问 STRM 中的 QMediaSync 内网主机。清除飞牛认证头和 Cookie 后转交；不把整个主站开放到飞牛端口。既有相对 `/proxy-115` 结果仅对这个确切路由继续复用原代理安全检查。

主站既有 `/proxy-115` 路由仍为 GET；应用入口为飞牛内部转交单独分发 GET／HEAD 并调用共享 `controllers.Proxy115`，不新增主站 HEAD 公共路由。这个不访问数据库的视频入口在转发前重新检查恢复维护准入，通过后立即释放许可，不再经过覆盖整个请求的主站维护中间件；维护开始后仍拒绝新转发，其他内部取链入口仍沿用主站门禁。共享处理器向网盘保留 HEAD、状态和响应头，不读取或发送视频 Body，原 GET 仍流式转发；两种方法都传播请求 context，客户端取消或实例停止会取消出站请求。Cookie 和重定向白名单规则不变，契约见 [下载代理](../architecture/authentication-sessions.md#api-key可信来源与下载代理)。

内部 115 取链的缓存有效性 HEAD 检查也继承请求 context，使客户端取消、元数据期限和实例切换能中断校验；取消不作为缓存链接失效，不因此删缓存、重新取链或继续播放跳转。共享校验边界见 [115 STRM 直链解析](../architecture/upload-and-strm-processing.md#115-strm-直链解析)。

其他 HTTP(S) URL 按完整原地址跳转，不任意代下载。播放器仍须能访问外部 STRM 地址或最终 CDN；本实例入口私有可由内部转交解决，任意外部私有地址不能据此自动变成可用代理。

### 原生网页播放

`POST /v/api/v1/stream` 的请求体、原生 `Authorization` 和 `authx` 签名原样透传，不生成签名、不跨接口重放，也不建立管理员代登录。回源、响应读取及内部取链共用从回源前开始的 30 秒元数据超时，普通页面和视频流不使用此超时。只有 HTTP 200 且业务 `code=0` 的成功响应可以进入识别；失败、HTML、未知压缩和不支持的格式保留原响应。

接管要求 `file_stream` 有准确文件 GUID、`.strm` 路径、`type=1`、`can_play=1` 且无 `play_error`；`video_stream.media_guid` 必须与该文件 GUID 相同且类型为视频；`cloud_storage_info` 明确为有效、未禁用的 STRM 来源（`cloud_storage_type=9001`）。只处理 `direct_link_qualities` 中明确非 M3U8、与受限读取的 STRM 内容对应的 URL，不从客户端提供的任意路径或 URL 取链。

来源定位和缓存复用现有受限路径流程，原生与兼容来源使用不同条目标识，当前会话的 `Authorization` 只以指纹隔离缓存。每次 POST 都重新经过飞牛授权；缓存不能代替本次响应的许可或文件状态核验。

确认 URL 属于当前 QMediaSync 实例后，按 GET 内部调用既有取链处理器，只读取其重定向，不跟随 CDN 或下载视频。优先使用飞牛成功响应中唯一有效的播放 `User-Agent`，缺省沿用入站 UA；歧义或非法 UA 不改写。仅采纳 HTTP(S) 跳转或通过既有参数校验的确切相对 `/proxy-115`，不创建新的通用视频代理路由。

只有成功取链后才替换本次 `direct_link_qualities[].url`；保留原生播放头、转码选项、字幕与未知字段，不长期保存最终签名 CDN URL。外部 STRM、取链失败、权限失败或文件不能核对时保留原响应。改写后的 JSON 移除失效的压缩与校验头，并禁止公共缓存；超出 2 MiB 的响应原样回放。停止或数据库恢复维护期间不能用来源缓存继续内部取链。

暂停、继续和正常返回播放详情页仍由飞牛网页处理，`POST /v/api/v1/play/record` 的播放位置及 `POST /v/api/v1/play/info` 的进度读取原样透传，不新增停止接口或补造进度记录。暂停期间仍可能有原生 `/stream` 查询与进度上报，不能仅凭请求发生在暂停后就认定是一次新的播放。实机已验证暂停后进度不变、继续播放及正常退出，飞牛能保存并返回进度；同一真实 STRM 的飞牛媒体时长为 `0`，网页在构造播放器前以 `min(ts, item.duration)` 限制进度，导致保存位置被归零。实机浏览器对照中保持 `ts=420`，原时长 `0` 时从头播放，仅临时将 `item.duration` 修正为视频实际时长约 1625 秒即可从 420 秒续播。该实验定位了飞牛网页对零时长媒体的处理，生产反代不改写进度或时长，也不注入网页脚本；此类媒体的自动续播仍需飞牛正确识别时长。上报成功和网页实际恢复播放位置须分别验证。

### 兼容列表查询

飞牛影视 0.9.8 的用户媒体库列表应使用 `/Users/{userID}/Items`，明确提供 `ParentId`、非空 `Fields` 和正数 `Limit`，按需带 `StartIndex`。实机中缺少 `Limit` 可返回非零 `TotalRecordCount` 但 `Items=[]`，省略或留空 `Fields` 则可返回 HTTP 400；反代保留该上游行为，不擅自补页大小或改写列表。

```text
/Users/{userID}/Items?ParentId={libraryID}&Fields=Path,MediaSources&Limit=20&StartIndex=0
/Shows/{seriesID}/Seasons?UserId={userID}&Fields=Path,MediaSources&Limit=20&StartIndex=0
/Shows/{seriesID}/Episodes?UserId={userID}&SeasonId={seasonID}&Fields=Path,MediaSources&Limit=20&StartIndex=0
```

电视剧、季和单集按上述对应接口逐层查询；通用 Items 接口以电视剧或季作为 `ParentId`、或者不指定媒体库而直接全局递归，在该版本可返回空列表。实机已核对电影分页及电视剧层级，原生与反代一致；不能据此合成缺失媒体、替换上游权限或添加媒体库扫描。

## 来源缓存

来源缓存由飞牛实例独立持有，最多 256 条，写入后固定 24 小时到期，命中不续期，只更新淘汰所需的最近使用时间。不保存到数据库或备份，也不共用 Emby302 响应缓存。

键隔离运行配置、允许根、当前飞牛认证凭据的单向指纹、准确条目和媒体源；不存明文凭据。只缓存已经核验的 STRM 路径、稳定入口 URL 和文件状态，不缓存权限结果、失败结果、整份播放信息、视频内容或最终签名 CDN 地址。

每次接管仍核验当前身份和准确媒体源，并重新受限打开文件比较身份、大小和纳秒 mtime。变化、缺失、读取失败或过期会失效并重读，不返回旧结果。稳定的本实例入口命中后仍内部转交现有取链层；临时签名或无法判断稳定性的外部 URL 不长期复用文本。

没有目录监控、预热或新增 SSE。原地修改内容又恢复相同元数据可能在固定到期前无法察觉，不承诺内容级实时一致。运行配置、允许根变化或实例停止使旧结果失效，并阻止停止后的在途操作回填。

## 不变量

- 默认关闭不改变既有 Emby302、同步、刮削和取链行为；不迁移 `backend/emby302/`。
- 设置使用 QMediaSync 身份，播放使用当前用户的飞牛身份；不保存管理员万能凭据，不用缓存绕过权限。
- QMediaSync 的认证与 CSRF Cookie 不送往飞牛，飞牛响应不能覆盖这些保留 Cookie；升级与普通响应遵循相同边界。
- 飞牛回源使用实例独有直连传输，默认验证上游 HTTPS 证书，不继承系统代理或 Emby 的跳过证书验证选项。
- 独立端口遵守数据库恢复维护门禁；只在短解析期间持许可，不持有覆盖整个视频流的数据库维护许可。恢复进入维护后旧实例不得继续使用来源缓存；完成恢复仍需重启。
- 关闭流程释放 HTTP／HTTPS、升级连接、缓存和本实例连接池；实例失败不终止主站或 Emby302。
- 日志复用现有脱敏入口，不输出密码、Token、Cookie、私钥或完整签名 URL；不新增专用日志页面。

## 验证方式

- 后端运行 `(cd backend && go test . ./internal/fnosproxy ./internal/requests ./internal/models ./internal/controllers ./internal/backup)`，并对新模块和受影响并发路径运行 `-race`。覆盖代理透传、前缀、不同条目／媒体源 ID、禁播与下载策略、POST SPA 回退、内部转交、HEAD／Range、取消、缓存隔离与失效、维护和停止。应用入口回归在独立测试进程中保持 115、百度视频流打开，执行临时 SQLite 的真实恢复并验证完成后仍拒绝新转发；百度回归经真实取链控制器和 SDK 观察期限、取消、迟到结果与锁等待，并验证原生播放停止能结束在途百度请求。
- PlaybackInfo 回归经实际 HTTP 反代核对 GET／POST 查询禁用、歧义标记与 POST JSON 保留转码响应，取流入口使用同一约束，原始 POST 字节透传且正常直播放仍接管；GET 请求头 Token 和 POST 改写结果均禁止公共缓存，回退响应保持上游缓存策略。电视剧 Seasons／Episodes 核对带 `/emby` 和不带前缀时的准确上游路径与完整查询串。
- 115 缓存 HEAD 回归使用真实控制器和受控慢响应，核对取消及期限、不删除有效缓存、不重新取链或跳转，并经原生内部转交验证单次 Apply 能取消旧请求并启动新实例；该替身验证不能当作真实 CDN 故障或全部客户端覆盖。
- 使用临时证书核对 HTTP／HTTPS、缺任一证书仅 HTTP、无效证书或 HTTPS 端口失败时 HTTP 保持、HTTP 失败仅停飞牛模块，以及 WS／WSS 与停止后的连接释放，不提交真实私钥。
- 保存和应用分开验证：数据库失败不触碰运行；已保存但 Apply 失败返回成功业务码及真实错误状态；同配置健康保存不中断，配置变化只重建飞牛并清缓存，错误可再次保存重试，恢复维护不能被 Apply 绕过。
- SQLite／隔离 PostgreSQL 核对新库、68→69 事务升级、修复、新版备份和已支持旧包升级，失败不能改变原版本或留下目标旧飞牛配置。
- 前端用 Vitest、类型检查和生产构建验证首次加载失败禁止默认值写回、保存失败保留输入、HTTP／HTTPS 实际状态、目录选择取消及过期回调；命令入口见 [验证说明](../engineering/verification.md)。
- 原生接管测试覆盖原始 POST 与认证头透传、失败／热缓存撤权、来源及 URL 不符、HLS／策略回退、UA、gzip 改写、未知编码／超限透传、内部取链失败、确切本地代理与停服取消。
- 实际客户端逐项验证登录、兼容协议及原生网页 STRM、非 STRM、拖动、字幕、外网地址及退出；本地 HTTP 替身不能证明所有飞牛客户端均已支持。
