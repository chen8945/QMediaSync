# 上传和 STRM 后处理流程

> 职责：定义 115 上传、目录监控、STRM 生成、源文件清理、上传后 Emby 刷新和多端播放副本的状态流转。
>
> 权威范围：本文档维护上传与 STRM 后处理的行为契约；运行参数见 [配置、密钥与日志](../operations/configuration.md)，外部 Webhook 字段见 [STRM Webhook](../reference/strm-webhook.md)。
>
> 修改时机：修改上传队列并发、上传协议、目录监控规则、上传任务状态、STRM 生成、源文件清理、幂等策略、上传后刷新或 115 多端播放链路时必须更新本文档。
>
> 相关代码：`backend/internal/directoryupload/`、`backend/internal/syncstrm/`、`backend/internal/playback/`、`backend/internal/v115open/`、`backend/internal/openlist/`、`backend/internal/models/upload.go`、`backend/internal/models/dbupload.go`、`backend/internal/models/strm_generation_task.go`、`backend/internal/controllers/directory_upload.go`、`backend/internal/controllers/open115_playback.go`。

## 上传队列执行

上传队列是进程内的全局实例，各同步批次共用它。一个 worker 同步处理一个任务的文件准备、秒传等待、上传和完成处理；并发限制约束完整任务，而非单个接口请求。配置入口与保存生效规则见 [上传队列并发](../operations/configuration.md#上传队列并发)。

队列保留小容量缓冲，缓冲中的普通任务仍属于等待上传。同一任务在缓冲或执行期间不得被重复领取；执行前以数据库条件更新抢占等待任务，被清空、取消或已结束的任务不能由旧缓冲记录重新上传。远端完成任务继续使用 `5 → 6` 的原子收尾抢占，收尾失败回到等待完成处理，不能重新传输文件。

并发调整和暂停、恢复共用在途任务计数；减少并发后，现有 worker 不能在实际并发仍达到或超过新上限时补入任务。暂停只停止领取新任务，在途任务继续完成；快速恢复也不能让新旧 worker 重复领取或突破上限。上传请求继续使用现有网盘限流器。

百度网盘上传与浏览、同步入口共用按账号缓存的客户端；请求级 Token 快照和原子更新遵循[百度网盘刷新](../reference/account-authorization.md#百度网盘刷新)约定，同账号上传无需串行化整个文件传输。

OpenList 上传与目录浏览、同步等入口共用按账号缓存的客户端。缓存访问和客户端地址、登录凭据、Token 的读写必须同步；每次 HTTP 请求使用一致的配置快照，网络传输和 Token 保存事件在状态锁外执行，以保留同账号与不同账号的上传并发。并发 `401` 触发的 Token 刷新按客户端、OpenList 地址和登录凭据去重，同配置的在途刷新只执行一次登录，其余请求复用结果后重试。登录接口自身返回业务码 `401` 时直接返回凭据失效错误，不再触发刷新。

OpenList 上传的普通重试预算保持为 `0`，网络错误或超时不自动重传。远端明确返回业务码 `401` 后允许一次认证恢复重发；重发必须重新读取完整文件，并保留 multipart 文件名、目标路径和原请求选项。认证恢复、登录结果的条件回写与账号编辑验证边界见 [账号授权与更换](../reference/account-authorization.md#openlist-登录与-token-回写)。

## 115 上传增强

115 上传任务先执行 `/open/upload/init`。所有上传入口在创建任务时写入不可变的 `remote_full_path`（包含文件名）；`remote_file_id` 在完成前保持空，完成后才记录新远端文件 ID。local 上传的 `remote_full_path` 记录受配置源根目录约束的本地目标完整路径，供复制 worker 使用；它不是远端身份，队列详情不会以远端路径展示它。115 官方秒传返回只保证包含新增 `file_id`，不返回 mtime；因此秒传成功后系统会按 `file_id` 查询远端文件详情，补齐 PickCode、SHA1、大小和官方修改时间，并记录 `upload_result=rapid_upload`。任务公开 `remote_sha1` 只接受远端详情或完成 callback 返回的 SHA1，不能用本地 SHA1 或 `upload_sessions` checkpoint 兜底。115 列表的 `upt` 和详情的 `utime` 是修改时间；只有字段缺失或零值时才回退列表 `uppt` 或详情 `ptime`。`strm_sync` 上传需要用最终查询到的远端官方修改时间同步本地元数据文件，详情查询失败会让任务失败；目录监控上传可用 init 返回的 `file_id` 兜底完成，后续 STRM worker 仍可按 `file_id` 补齐文件详情。如果未命中秒传且启用了秒传等待策略，任务会按 `upload_rapid_wait_interval_seconds` 重复尝试 init，直到命中秒传或达到 `upload_rapid_wait_timeout_seconds`。`upload_rapid_wait_interval_seconds` 是两次 init 之间的重试间隔，`upload_rapid_wait_timeout_seconds` 是最大等待时长；最后一次等待会按剩余超时时间裁剪，不会因为间隔更长而超过最大等待时长。`upload_rapid_wait_min_size` 控制进入等待策略的最小文件大小，`upload_rapid_wait_force_size` 控制必须等待到超时的大文件阈值；等待超时后是否跳过真实上传由 `upload_rapid_wait_skip_upload` 控制。

非秒传上传使用 OSS multipart。初始化 OSS multipart 时会带 `sequential=1`；真实非秒传任务验证显示，该参数配合 115 callback 原样透传后，OSS 会在完成对象上返回可供 115 校验的最终 SHA1。默认 part size 为 `32 MiB`；当文件按该大小切分会超过 `9999` 个 part 时，part size 会按文件大小动态放大并向上取整到 `1 MiB`。首次创建 `upload_sessions` 后会持久化 part size、OSS `upload_id`、本地文件签名、115 调度字段和已上传进度，后续重试或进程重启恢复时必须复用这些 checkpoint。

断点续传同时依赖 115 调度层和 OSS 数据层。恢复时先调用 115 `/open/upload/resume`，再用 OSS `ListParts` 查询已有分片并跳过已完成 part；如果本地文件大小、mtime、SHA1 或快速签名变化，系统会在同一 `upload_sessions` 记录中保存当前本地签名，清空 115 调度、OSS multipart 和进度 checkpoint，写入废弃原因并将恢复状态标记为 `session_expired_restarted`，随后在同一次上传执行中重新走 `/open/upload/init`。不得复用旧 checkpoint，也不先将任务标记失败。如果 OSS 返回 `NoSuchUpload`、`InvalidUploadId` 等明确 checkpoint 失效错误，任务会清空旧 `upload_id`、已上传字节和分片进度，将恢复状态标记为 `session_expired_restarted`，并在同一次任务中复用当前 115 调度结果创建新的 OSS multipart。

OSS `CompleteMultipartUpload` 完成后，必须带回 115 init 返回的 `callback` / `callback_var`。官方 `callback` 可能是单个对象或对象数组；对象数组会取第一个回调配置。传给 OSS 时只把 115 返回的 callback JSON 原样 Base64 编码为 `x-oss-callback` / `x-oss-callback-var`，由 OSS 处理 `${bucket}`、`${object}`、`${size}`、`${sha1}` 和 `${x:...}` 占位符；后端不要提前展开 `callbackBody`，也不要向 `callback_var` 增加非 `x:` 字段。本地 SHA1 不传给 OSS multipart，`callbackBody` 中的 `${sha1}` 以 OSS 完成对象后返回的最终 SHA1 为准。如果 115 callback 响应 `state=false`、缺少远端文件 ID、缺少 PickCode 或响应无法解析，任务不会视为上传成功，也不会创建后续 STRM 生成任务；错误会写入上传任务和 `upload_sessions.complete_callback_error` 供排查。

`preid` 按 115 官方「文件上传」文档使用文件前 `128 KiB` 的 SHA1。该窗口应封装为可测试实现，不能在上传流程中散落协议常量。115 直链缓存有效性检查只对命中的缓存 URL 发起 HEAD 请求；关闭检查后直接使用缓存链接，百度网盘和 OpenList 不使用这套机制。

## 115 多端播放链路

配置入口及本地代理置灰规则见 [115 多端播放](../operations/configuration.md#115-多端播放)。播放继续经过现有 `/115/url` 或 `/115/newurl`，STRM 和内置 Emby 302 的缓存设计不变，不引入 `PlaySessionId`。本地代理和多端播放开关从同一配置快照读取，保存、重载与播放请求并发时不混用新旧值。

URL 缓存仍按原始 PickCode、实际 mode 和 effectiveUA 保存，副本 URL 也写回原始键。缓存期限取 50 分钟与签名 URL 的 `t` 提前 5 分钟两者中较早者；`t` 缺失或无法解析时保留 50 分钟，安全期限已过的链接不能返回或写入缓存。缓存与槽位使用同一个 Unix 秒到期时间。有效命中直接复用；启用 HEAD 检查时，校验失效进入同一个 miss 分支。槽位元数据单独保存在进程内 map，按账号 ID、115 UID、原始 PickCode 分组，避免 freecache 单独淘汰索引造成漏判。命中不续期，删除副本不删除槽位，过期记录在后续访问中回收。

已有缓存键锁继续合并相同 UA、相同模式的请求。同一原文件的 miss 在短锁内同时检查活跃槽位和在途预留，再登记当前预留；网络操作在锁外执行，避免首次并发都领取原文件或互相等待取链。成功发布缓存后将预留转为活跃槽位，失败或取消时撤销；副本失败后的普通取链仍保留预留。开关开启、实际为 direct 且存在其他活跃或在途槽位时才复制；实际 proxy、没有其他槽位或关闭功能时普通取链。代理槽位也参与登记，后来的直链请求可识别它。原文件 ID、SHA1 和大小优先来自同账号的 115 `SyncFile`；有 ID 时可补齐详情，缺少可靠 ID 时降级，不能把 PickCode 当作 FileID。

临时根目录的目标路径固定为 `/多端播放`，目录缓存按账号 ID 和实际 115 UID 隔离。先按路径查询；未知业务错误不表示目录不存在，必要时完整分页列出根目录确认缺失后再创建。只有根目录初始化按账号合并，每次复制单独创建 `qms-` 加 32 位小写十六进制随机串的子目录，不在共享根目录做复制前快照和复制后差集。缓存命中不额外发起写前位置查询；复用复制后列表和清理前详情发现根目录移动或改名，只使仍匹配旧 ID 的缓存失效。复制后列表发现位置变化时本次降级，下一次需要副本时重新定位固定路径：已有目录则复用，确认缺失才创建。旧根目录不删除或移回。创建操作目录明确失败时，只有确认根身份变化才允许重新定位后重试；网络结果不明时不能盲目重发写操作。

复制显式使用 `nodupli=0`。成功响应 `data=[]` 是合法形态，不能据此判断复制失败。随后列出本次操作目录，只接受唯一文件，并核验完整目录路径、父目录 ID、文件类型、独立 FileID / PickCode、SHA1 和大小；响应中存在可解析候选时还必须一致。空列表可短暂等待后重查一次，多条、缺页、位置变化或身份不符时停止取链并清理本次目录，不能取“最新文件”。公共列表接口显式指定排序时同时发送 `custom_order=2`，副本识别仍不依赖排序。

Copy 因传输中断、响应损坏等导致结果不明时，仅在请求及副本总预算仍有效的情况下进入同一列表核验流程；找到唯一且身份匹配的副本即可继续取链，空列表等待 100 ms 后最多重查一次。明确的业务失败、授权或限流错误、队列确认请求未发送、调用方取消及预算耗尽直接退出，不增加补救列表请求。队列等待无法满足剩余预算时保留未发送标记，不伪装成平台限流；响应头已明确的 HTTP 4xx 拒绝（408 除外），即使响应体读取中断也保留状态。空列表不能证明前次复制未执行，因此任何分支都不重发 Copy；补救未成功时沿用普通取链降级和操作目录清理。

副本总预算为 10 秒，从决定尝试复制起计算，包含索引查询、根目录初始化等待、API 排队、HTTP、列表重查和取链退避。首次取链立即执行，暂时性失败最多再按 0.5、1、2 秒重试。`70004`、`31004` 和成功但缺少 URL 的响应归为未就绪；`fta` 仅辅助观测，不能因 `fta=1` 否决未就绪重试。取消、授权、限流和预算耗尽及时退出，SDK 不叠加内层重试。目录、复制和删除复用既有请求队列，下载地址沿用播放快速通路。播放器请求仍有效时，副本失败降级普通取链；普通取链不计入这 10 秒，失败沿用现有接口错误。

每次操作绑定不可变凭据快照，复用长期 HTTP 连接池，Authorization 和 UA 放在各自请求上。账号授权替换不改变旧操作清理所用身份。日志沿用现有请求统计，记录账号、操作标识、阶段、队列等待、HTTP 耗时及失败原因，不记录完整直链、Token 或远端可能回显凭据的消息。

确认创建操作目录后立即登记清理责任，无论复制、识别或取链是否成功，都在尝试结束 5 秒后按目录清理，预算独立为 10 秒且不继承播放器取消。删除前按 ID 核验目录类型、随机名称和原父目录 ID。持有本进程创建记录的操作目录允许随祖先根目录移动或改名后继续清理；操作目录自身改名、换父或身份无法核验时仍拒绝删除。`231011` 按已经删除处理。删除成功后释放记录，失败则保留精确身份供定时维护重试。已取得的 URL 在清理后继续使用；关闭开关不撤销清理。服务退出时停止播放编排并等待清理退出，再释放共享客户端资源。

每小时维护使用同一播放编排器，先按账号 ID 和实际 115 UID 重试已结束操作的清理记录，再回收固定根路径下严格匹配 QMS 命名、时间可信且超过 1 小时的非活跃目录。待重试记录不依赖固定根目录存在，沿用本次维护的对应账号凭据快照；延迟清理和维护共用操作抢占，不能重复领取或清理仍在取链、等待延迟删除的目录。固定路径扫描发现的目录没有本进程的创建证明，删除前仍须核验固定路径和时间，不能仅凭名称扩大清理范围。先完成分页候选收集再删除，避免边删边翻页漏项；每账号维护总预算 1 分钟，单次删除至多 10 秒，失败或未轮到的记录留待后续维护。维护不创建缺失根目录，不因关闭多端开关而停止。

操作记录只保存在进程内，重启后通过固定路径扫描恢复残留回收；若根目录已移出固定路径且尚未清理就退出，重启后无法自动定位这些目录，需人工核对。没有持久化租约、槽位上限或启动清空操作。

槽位代表缓存，不等同于设备或实际在播状态；同 UA 继续共享 URL，错峰过期后不保证始终存在一个原文件槽位。代理与直链混用或副本失败降级时，普通原文件取链仍可能影响其他原文件链接。定时补偿受账号授权和 API 可用性影响，失败后留待后续维护。

`/多端播放` 整个子树作为 115 临时工作目录，从普通同步、路径补全、缓存路径、手动 STRM 目录生成及刮削扫描中排除，对应本地镜像也不参与差异删除或元数据重传。单文件 STRM 后处理对该子树返回明确错误，不能标记完成后触发上传源文件清理。判断使用规范化完整路径，`/媒体/多端播放` 等其他位置和其他存储来源不受此保留路径影响。根目录被外部移动或改名到发现位置变化之间，副本可能短暂创建在固定路径之外；补偿清理不保证这段时间内不会被普通同步或刮削扫描发现。

## 目录监控上传

目录监控上传规则绑定一个同步目录，只支持 115 Open API 上传目标。一个同步目录可以配置多条目录监控上传规则，每条规则对应一个本地监控目录和一个远端上传根目录。目录监控上传有两层开关：`sync_paths.directory_upload_enabled` 是同步目录总开关，`directory_upload_rules.enabled` 是单条规则开关。运行时只加载总开关开启且规则自身启用的规则；关闭总开关不会修改各规则的 `enabled`，下次重新打开时会保留上一次的规则启停状态。

规则接口：

- `GET /api/directory-upload/rules`：查询规则列表，可用 `sync_path_id` 过滤。
- `POST /api/sync/paths`、`PUT /api/sync/paths/:id`：原子保存同步目录基础配置和目录监控上传最终规则集合。旧同步目录创建、更新和规则独立写接口不再提供；精确请求字段、幂等、错误与响应见 [同步目录聚合 API](../reference/sync-path-api.md)。
- `POST /api/directory-upload/sync-paths/:sync_path_id/scan`：手动触发一个同步目录下所有已启用规则扫描，返回汇总候选数和每条规则结果；页面“目录监控扫描”按钮会调用该接口，文案不区分单条或多条规则。扫描使用当前 HTTP 请求 context 派生的 10 分钟超时 context，请求取消或超时会终止扫描并在响应中返回错误。
- `GET /api/directory-upload/runtime-status`：查询当前目录监控运行状态，返回每条运行中规则的配置模式、实际模式、auto 降级原因、最近扫描时间、耗时、候选数、跳过数、最近错误和待稳定文件数。

同步目录页面只有一个“保存设置”动作。前端必须先成功加载目录监控规则，才能把规则最终集合提交给后端；若规则加载失败，会阻止整页保存，避免把加载失败误当成空规则集合。关闭目录监控总开关时，如果仍有未补完整的规则，页面会保持总开关开启，并在缺少信息的目录输入项上显示字段级错误，提示用户补完整或删除规则后再关闭。通过同步目录基础接口修改 `directory_upload_enabled` 成功后，后端会重载目录监控服务，使运行中的 watcher 与总开关状态保持一致。

同一同步目录下会做规则防呆：`monitor_path`、`remote_root_path` 和 `remote_root_id` 都不能为空；完全相同的 `monitor_path + remote_root_path + remote_root_id` 组合会被拒绝；两个启用规则的监控目录不能重复。若一个启用规则递归监控父目录，另一个启用规则不能监控其子目录，避免同一源文件被重复发现和重复上传。

监控模式：

- `auto`：自动（推荐），根据运行环境选择 fsnotify 或 polling。Linux 下会先通过 mount info 识别 `nfs`、`cifs`、`smb`、`fuse` 等网络文件系统或 FUSE 挂载，命中时直接使用 polling；再读取 inotify `max_user_watches` / `max_user_instances`，通过 `/proc/self/fdinfo` 统计当前进程已有 inotify watch / instance 使用量，并按规则递归语义统计本规则待 watch 目录数。`当前 watch 使用量 + 本规则目录数` 达到 `max_user_watches` 的 80%，或 `当前 instance 使用量 + 1` 达到 `max_user_instances` 的 80% 时使用 polling。检测失败不阻塞启动，会记录日志并继续尝试 fsnotify；非 Linux 不读取 `/proc`，只在 fsnotify 启动失败时自动回退到 polling 查漏。
- `fsnotify`：性能模式，强制使用 fsnotify，初始化失败则规则启动失败。
- `polling`：兼容模式，按内置 30 秒周期递归扫描。

启动查漏由 `startup_scan_enabled` 控制。查漏扫描默认只把候选视频文件加入稳定性队列；规则 `upload_metadata=true` 时，也会纳入当前同步目录配置中的元数据扩展名文件。视频扩展名和元数据扩展名都按同步目录自定义配置优先、为空回退全局 STRM 设置的规则解析。`recursive=false` 时，查漏扫描和 fsnotify 事件都只处理监控根目录下的文件，新建子目录不会被加入 watcher。扫描不直接创建上传任务。补偿扫描间隔为代码内置 30 秒，不提供页面或接口配置。启动查漏、polling 查漏和 fsnotify 新目录补偿扫描共用内置扫描执行器；执行器按 `rule_id + clean(root)` 合并重复目录任务，已取消的同 key 扫描会允许后续请求重新提交，默认并发为 2，不新增前端配置。polling 模式会在运行时维护 `relative_path -> source_fingerprint` 快照，每轮扫描只把新增文件或 fingerprint 变化的文件加入稳定性队列；`startup_scan_enabled=true` 时，启动查漏会处理已有文件并初始化快照，避免第一轮 polling 重复提交同一 fingerprint；`startup_scan_enabled=false` 时，启动时会先建立 baseline 快照，不处理已有文件，之后只有新增或变化的文件会进入队列；baseline 建立遇到非取消类扫描错误时会记录日志并由后续 polling 重试，已成功扫描到的部分仍作为 baseline。polling 定期扫描遇到非取消类中途错误时，不会用本轮 partial snapshot 替换完整快照，避免误删已知 fingerprint；但本轮已成功扫描部分中新增或变化的文件仍会加入稳定性队列，下一轮继续重试。启动查漏提交到执行器前会同步校验同步目录、监控路径和扫描根目录，基础校验失败会阻止规则启动；实际扫描期间发生的错误会写入应用日志，不阻塞已经启动的规则。

运行状态接口只返回当前进程中正在运行的规则。`configured_mode` 来自规则配置，`actual_mode` 是实际运行模式；`auto` 最终使用 polling 时会在 `fallback_reason` 返回原因，显式 `fsnotify` 或 `polling` 不会伪造降级原因。`last_scan_candidates` 表示最近一次扫描中符合规则的候选文件数，`last_scan_skipped` 表示已被 polling baseline 或 snapshot diff 跳过、未加入稳定性队列的候选数；`pending_count` 来自当前稳定性队列。

## 稳定性和去重

目录监控发现文件后，会先进入稳定性队列。允许监控目录内的 symlink 文件，但扫描、稳定性检查和创建上传任务前都会通过 `EvalSymlinks` 复验真实目标；真实目标不在监控目录内时，该候选会被跳过或从稳定性队列移除，不会创建上传任务。稳定性签名为版本化源文件 fingerprint，格式为 `v1:size:mtime_ns`；签名只包含文件大小和纳秒级 mtime，不包含 ctime、inode 或文件内容 hash。签名变化会重置稳定计数。稳定性检查间隔为内置 2 秒，文件需要在内置 15 秒稳定窗口内保持签名不变，并连续 3 次检查不变后，才会创建上传任务。这些稳定性参数不提供页面或接口配置。

文件通过过滤并进入稳定性队列时，会向 `app.log` 写入 `[目录上传] 监控到候选文件` INFO 日志，包含规则 ID、来源（`fsnotify` / `scan` / `polling`）、本地路径、相对路径、文件大小和 source fingerprint。已被忽略、去重或未通过扩展名规则的路径不会输出候选日志。

fsnotify 文件事件候选在通过递归、忽略规则和扩展名过滤后，会先按 `rule_id + relative_path + source_fingerprint` 做 recently queued 内存 TTL 去重，再进入稳定性队列。这个缓存复用 `processed_cache_ttl_seconds`，只减少同一 fsnotify 事件风暴造成的重复入队，不写入 `directory_upload_processed_files`，也不影响启动查漏、手动扫描或 polling 补偿扫描；TTL 过期或同一路径 `source_fingerprint` 变化后，fsnotify 候选可以再次进入稳定性队列。

同一规则下，已确认终态的源文件会按 `processed_cache_ttl_seconds` 做另一层内存 TTL 减噪，key 使用规则 ID、相对路径和 `source_fingerprint`，避免 create / write 多事件重复查询终态账本。内存层只用于减少相邻事件噪声，不作为正确性来源；强制重扫会绕过内存终态缓存和持久化终态记录。未命中终态缓存时会查询 `directory_upload_processed_files` 持久化账本：持久化 `source_key` 包含监控目录和远端上传根目录等范围信息；`uploaded`、`remote_exists`、`skipped_existing` 视为终态，源文件 fingerprint 未变化时直接跳过；`uploaded_pending_strm`、`remote_exists_pending_strm` 和 `strm_enqueue_failed` 会先按关联上传任务重试 STRM 入队，若该上传任务已被上传队列清理，则删除这条 stale 账本并让当前扫描重新处理源文件；`queued` 会结合关联上传任务是否仍为 `pending`、`uploading`、`remote_completed_pending_finalize` 或 `remote_completed_finalizing` 判断，不通过内存缓存绕过 DB 活跃状态检查；`pending_replace` 表示 `replace_conflict` 已准备覆盖远端冲突文件但尚未绑定上传任务，服务重启后会重新校验远端状态并继续创建任务；`failed` 不作为终态，后续扫描允许重试。创建上传任务前还会按 `source=directory_monitor + local_full_path + active status` 查询数据库，active status 包含等待上传、正在上传、等待完成处理和正在完成处理；已有未完成任务时会跳过重复入队，覆盖服务重启、轮询重复发现、强制重扫和大文件长时间上传场景。TTL 过期后，如果同一路径文件 fingerprint 变化，会更新账本并重新创建上传任务。

所有上传入口还由数据库部分唯一索引保护：`source + source_type + account_id + remote_full_path` 在路径非空且任务为等待上传、上传中、等待完成处理或正在完成处理时只能存在一条记录。应用层预检查只用于返回明确状态，不能替代该约束；并发创建时由索引拒绝后到达的插入。失败任务不占用该范围，允许创建替代任务；重试失败任务前会重新检查该范围，已有活跃替代任务时保留旧失败任务及其错误和重试次数。

目录上传服务启动时会立即清理一次 processed 账本，运行期间默认每 24 小时清理一次；同一清理周期会先筛选关联 completed STRM 的 pending 源文件清理任务并执行补偿，再删除过期 processed 账本、终态内存缓存和 recently queued 内存缓存。补偿统计只计入实际完成源文件删除或文件不存在的幂等收敛，不把因规则关闭或安全校验跳过的任务计为已清理。`queued` 记录在关联上传任务不存在或已结束时可清理；等待 STRM 的记录在关联上传任务不存在时可清理；`failed` 和成功终态记录需要超过默认 30 天阈值，其中成功终态还必须确认本地源文件已不存在。

默认忽略隐藏路径、带 `.part`、`.tmp`、`.download`、`.aria2`、`.torrent` 临时后缀的文件或目录，以及 `@Recycle`、`#recycle`、`.Trash`、`.Trashes` 回收站目录和规则中的 `ignore_patterns`；不会按 `.nfo`、`.jpg`、`.png` 等元数据扩展名做默认忽略，`upload_metadata=true` 时仍可按同步目录配置上传元数据文件。规则列表接口会把持久化的忽略规则解析为 `ignore_patterns` 数组返回，避免页面保存其他目录监控配置时丢失已有忽略规则。

## 上传任务和远端已存在

目录监控上传只创建 `db_upload_tasks.source = directory_monitor` 的上传任务，真实上传仍由全局上传队列执行。任务会写入 `sync_path_id`、`relative_path`、`source_fingerprint`、`local_mtime_ns`、创建时确定的 `remote_full_path` 和 115 父目录 ID `remote_path_id`，其中 `source_fingerprint` 使用 `v1:size:mtime_ns`。远端同名且内容一致时会直接写入该远端文件的 ID、PickCode 和 SHA1；`replace_conflict` 只有在旧远端文件删除成功后才写入 `replaced_remote_file_id`，删除失败不伪造覆盖记录。与此同时会写入 `directory_upload_processed_files`：待上传任务记录为 `queued`；远端上传结果确认后，任务会先进入 `remote_completed_pending_finalize`，收尾 worker 通过数据库条件更新抢占为 `remote_completed_finalizing`，并清除上一次收尾错误后才推进本地账本和 STRM 入队；上传完成并保存上传任务最终结果后按 `upload_result` 先更新为 `uploaded_pending_strm` 或 `remote_exists_pending_strm`；STRM 入队成功后才更新为终态 `uploaded` 或 `remote_exists`；STRM 入队失败时记录为 `strm_enqueue_failed`，后续扫描同一 fingerprint 时只重试 STRM 入队，不重新上传源文件。收尾失败会退回 `remote_completed_pending_finalize` 供队列重试，进程重启时遗留的 `remote_completed_finalizing` 也会恢复为等待完成处理。`replace_conflict` 删除远端冲突文件前会先写入 `pending_replace`，创建上传任务后再推进为 `queued`。`skipped_after_rapid_wait` 不写入终态。远端同名且内容一致时创建完成态上传任务并进入同一 STRM 入队流程，按 `skip_same` 跳过远端冲突时记录为 `skipped_existing`。因此任务会出现在上传队列页面。

目录监控成功创建待上传任务后，会向 `app.log` 写入 `[目录上传] 已创建上传任务` INFO 日志，包含规则 ID、上传任务 ID、本地路径、远端目标路径、远端父目录 ID、文件大小和源文件清理状态。远端已存在同内容文件时不会记录为真实上传，而是在 STRM 后处理任务创建成功后写入 `[目录上传] 远端已存在同内容文件，已创建 STRM 后处理任务`，包含上传任务 ID、STRM 任务 ID 和远端文件 ID。

创建任务前会检查远端同目录同名文件。只有远端文件大小和 SHA1 都与本地文件一致时，才把上传任务直接标记为 `completed`，`upload_result = remote_exists`，并创建后续 STRM 生成任务。该行为是远端已存在跳过，不是断点续传。

同名文件大小或 SHA1 不一致时，按 `overwrite_mode` 处理：

- `skip_same`：跳过本地文件，不创建上传任务，不删除远端文件。
- `fail_conflict`：停止处理并记录错误，不创建上传任务。
- `replace_conflict`：先写入 `pending_replace` 账本，再删除远端同名文件，最后创建新的上传任务并把账本推进为 `queued`。

普通上传成功、秒传成功或断点续传完成后，由上传任务统一创建 STRM 生成任务。`upload_result = skipped_after_rapid_wait` 不会创建 STRM 生成任务，也不会触发源文件删除。目录监控上传任务执行前会重新计算当前源文件 fingerprint；如果同一路径文件已被替换或任务缺少 fingerprint，任务会取消并记录错误，避免过期任务上传新文件。

## STRM 后处理和源文件删除

STRM 生成 worker 会读取 `strm_generation_tasks`，复用同步目录配置写入或确认 STRM。该后处理只创建或更新 `SyncFile` 和 STRM 文件，不创建 `syncs` 同步记录，也不向同步目录队列添加“等待中”任务；完整同步记录只由手动同步、定时同步等 STRM 同步入口创建。文件级任务会先比较已有 STRM 内容；确认需要更新后直接写入新 STRM，不再重复比较，因此同一次后处理只输出一次 PickCode、路径或用户 ID 差异日志。文件级任务在文件名、路径、父目录 ID、PickCode、mtime、大小或 115 SHA1 等远端元数据缺失时，会补齐文件详情后再保存 `SyncFile` 和 STRM 文件：115 使用 `file_id`，OpenList 和百度网盘的回退详情则使用创建任务时保存的完整远端路径（任务父路径加文件名）。新完成的百度上传是例外：若 `xpan/file/create` 响应提供非空 `fs_id` 和正数 mtime，且任务已有完整路径、文件名和正数大小，入队时直接将 `fs_id` 作为内部播放定位值并使用完整路径的父目录，不再执行路径详情查询；缺失或零值 mtime 都不会回写本地文件时间，也不会走该快速路径。`remote_pick_code` 仍严格只属于 115；百度 `fs_id` 仅写入内部 STRM 任务的兼容定位字段。无论详情补齐或使用该快速路径，百度 `SyncFile.file_id` 都使用完整远端路径，与普通扫描的协调键保持一致；`fs_id` 保留在 `SyncFile.pick_code` 兼容字段中，不能替换该路径标识。缺少任一条件、收尾重试或进程重启后，百度仍按完整路径补详情；OpenList 没有对象 ID、但完整路径存在时也仍会执行后处理。Webhook 和目录扫描子任务的 `request_hash` 使用短格式摘要，远端路径、文件名和目录路径仍保存在任务字段中，不依赖唯一键明文。上传完成、远端已存在等非 Webhook 文件任务在 STRM 新增或更新后会优先提交 Emby item 级定向刷新，定位不到可靠 item 时回退同步目录关联媒体库刷新。Webhook 文件任务只有 `refresh_emby=true` 且 STRM 变更或新增元数据下载任务时才提交刷新；批量和目录扫描只有在所有子任务成功完成且存在 STRM / 元数据变化时才统一提交目标集合，任一子任务失败则父任务失败且不提交刷新。

115 同目录视频去掉扩展名后映射到同一个 STRM 时，只由官方修改时间最新的文件生成；时间相同使用 FileID 固定排序。目录扫描复用已经取得的父目录列表选择 owner，不增加 115 请求。普通文件级任务先查询本地 `SyncFile`，只有发现同目标路径候选时才额外列出一次远端父目录；non-owner 任务仍保存远端文件记录，但不比较、不写入 STRM，也不触发 Emby 刷新。

外部程序触发 STRM 生成的接口见 [STRM Webhook](../reference/strm-webhook.md)。本文件只说明上传完成、远端已存在和 Webhook 入队后共用的 worker 后处理边界。

上传后的 STRM 入队从上传任务的完成文件 ID、PickCode、远端确认 SHA1 和 `remote_full_path` 的父目录读取信息。新完成的百度上传在本进程中额外保留创建文件响应的 mtime：当完整元数据齐全时，其 `fs_id` 同时作为内部 STRM 播放定位值，直接创建后处理任务，不新增数据库列；若收尾重试或进程重启，该临时 mtime 不存在，系统按完整路径补详情。历史 `strm_sync` 上传没有保存完整路径时，先使用关联 `SyncFile` 路径；仍不可得时，只在实际创建该 STRM 后处理任务时按新完成文件 ID 查询一次 115 文件详情。查询结果仅用于 STRM 任务，不回写上传任务；失败时 STRM 入队显式失败并可重试，绝不把文件 ID 作为远端路径。

上传后的 STRM 收尾先在事务外读取上传会话、关联 `SyncFile`、账号及必要的远端详情，准备入队数据；随后在同一事务中完成 STRM 任务入队与目录监控处理账本更新，任一写入失败均回滚。事务中的数据库操作统一使用 `tx`，不通过全局连接查询，也不等待远端请求或 Token 保存回调。该边界适用于正常收尾与失败重试，并兼容 [SQLite 单连接配置](../operations/database.md#引擎与初始化)。

目录监控规则 `upload_metadata=true` 时，元数据文件上传完成后也会进入同一 STRM 生成队列。worker 不会为元数据生成 `.strm`，而是把目录监控源文件复制到同步目录的 STRM 本地路径，文件名和扩展名保持不变，并保存对应 `SyncFile`。复制前会确认上传任务来源是 `directory_monitor`，并校验当前源文件 fingerprint 仍与上传任务记录一致；源文件不存在、已被替换或写入 STRM 本地路径失败时，STRM 任务会失败，后续源文件清理不会触发。复制发生在源文件清理之前，因此开启 `delete_source_after_success` 时不会因为先删除源文件导致元数据丢失。

“同步目录生效 STRM 配置”INFO 只在完整 STRM 同步启动时输出。上传完成后的单文件后处理仍沿用当前同步目录配置，但不会为每个后处理任务重复输出配置日志。

如果同一远端文件发生移动或重命名，服务会用 `file_id` / `pick_code` 查找旧 `SyncFile`。新 STRM 写入成功并保存新的 `SyncFile` 后，只 best-effort 精确删除旧记录里的 `local_file_path`，不会按文件名模糊删除其他 `latest` 或同名文件。旧 STRM 清理失败只记录应用日志，不会让 STRM 任务失败，也不会让数据库回到指向旧文件的状态。

目录监控规则的 `delete_source_after_success` 默认关闭。开启后，也必须同时满足以下条件才会删除本地源文件：

- 上传任务来源为 `directory_monitor`。
- 上传任务在创建时已因规则开启删除源文件而标记为 `source_cleanup_status=pending`。
- 上传任务状态为 `completed`。
- 上传结果为 `rapid_upload`、`multipart_uploaded` 或已确认签名的 `remote_exists`。
- 关联 `StrmGenerationTask` 状态为 `completed`；清理链路通过 `strm_generation_tasks.upload_task_id` 判断依赖状态。
- 源文件路径仍在规则 `monitor_path` 内。
- 当前路径上的文件 fingerprint 仍与上传任务记录的 `source_fingerprint` 一致；如果缺少 fingerprint，或同一路径已被新文件替换，则跳过删除并记录清理失败。

STRM 入队成功后，目录上传账本会更新为上传终态；后续清理依赖以 `strm_generation_tasks.upload_task_id` 为准。worker 完成时会反查全部依赖上传任务。服务启动和 processed 周期维护前还会分页补偿 `cleanup pending + STRM completed` 的任务，因此即使进程在 STRM 完成后、清理执行前退出，也能在恢复后补做清理。

删除源文件成功后，程序会从源文件所在目录向上删除空目录，但不会删除 `monitor_path` 根目录。清理失败只记录到上传任务的 `source_cleanup_status` 和 `source_cleanup_error`，不会回滚远端文件或已生成的 STRM。

源文件实际删除成功时，会向 `app.log` 写入 `[目录上传] 已删除源文件` INFO 日志，包含上传任务 ID、规则 ID、本地路径、上传结果和远端文件 ID。每成功删除一级空父目录，都会写入 `[目录上传] 已删除空目录` INFO 日志；如果源文件在清理前已经不存在，则不会伪造源文件删除成功日志。

## 不变量

- 多端播放只能在原始缓存键下发布已核验副本的直链；同一原文件的判定与预留必须原子完成，网络请求不占用状态锁，缓存命中不刷新槽位期限。
- 副本清理仅针对已核验的 QMS 操作目录，并使用对应账号身份；只有持有本进程创建记录且目录自身身份和原父 ID 未变，才允许随根目录移动后清理。扫描发现的目录仍受固定路径和时间约束。未知响应、目录最新项和不完整列表不得成为猜测文件归属的依据。定时维护必须避开活跃目录和无关目录。

- 上传任务领取必须幂等；减少并发、暂停或恢复不能中断在途上传，也不能重复执行缓冲中的任务。
- 断点续传必须同时恢复 115 调度和 OSS 分片 checkpoint；仅重新 init、普通 multipart 或远端已存在跳过都不是断点续传。
- 115 callback / `callback_var` 必须原样透传给 OSS；不得本地展开占位符或记录 STS 凭证。
- 115 上传成功后的本地 mtime 必须以远端详情的官方修改时间为准；只有详情查询成功且本地 `Chtimes` 成功，才视为本轮两端时间已收敛。
- 远端同名文件只有大小和 SHA1 都一致时才是 `remote_exists`；该结果仍进入 STRM 后处理，但不代表续传。
- 上传和下载任务的远端完整路径、文件 ID、PickCode、SHA1 / MD5 与执行直链语义互不复用；来源未提供可靠值时保持空，不以路径或下载链接代替。
- 文件级 STRM 回退详情定位按驱动能力选择：115 使用文件 ID；百度网盘和 OpenList 使用完整远端路径。只有当前进程内、创建文件响应已确认完整元数据的百度新上传可跳过该回退查询；其余路径型任务不能以稳定 ID 替代详情查询路径。
- 目录监控上传不能在 fsnotify / 扫描 goroutine 直接上传；稳定性、持久化账本和活跃队列共同保证幂等。
- 上传后的 STRM 信息准备在事务外执行；STRM 入队和目录监控账本终态更新必须在同一个事务内完成。
- 源文件只在目录监控任务、上传和 STRM 都成功、路径仍在监控根且 fingerprint 一致时删除；清理失败不得回滚远端文件或已生成 STRM。

## 验证方式

- 多端播放运行 `go test ./internal/playback ./internal/controllers ./internal/v115open ./internal/synccron ./internal/models ./internal/requests ./internal/helpers ./internal/syncstrm ./internal/scrape/scan`；对副本隔离、在途预留、10 秒预算、取消清理、定时补偿、TTL、排序及临时目录过滤相关测试运行 `-race`。修改设置交互时另验证本地代理置灰、保留值、恢复编辑与保存失败。
- Copy 列表补救须覆盖复制已成功但响应丢失或损坏、短暂空列表后恢复、持续空列表、歧义或身份不符拒绝，以及明确失败、取消和预算耗尽时停止补救；队列测试须覆盖剩余预算不足以排队但 context 尚有效，HTTP 替身须覆盖明确拒绝状态伴随响应体截断。同时确认不重复复制、正常成功路径不增加请求，且补救失败仍清理操作目录。
- 根目录移动回归须覆盖缓存命中不增加写前查询、列表发现移动后降级、下一次按固定路径复用或重建，以及清理详情发现移动后失效旧缓存。已知操作随根目录移动仍可删除，自身改名或换父仍拒删；扫描发现的目录不能绕过路径和时间校验。失败记录须覆盖固定根缺失时仍可重试、账号 UID 隔离、并发抢占、取消与预算耗尽后保留，以及成功后不再重复清理。
- 真机验收使用测试账号和不同 UA 的两台设备同时直链播放同一文件，确认后到槽位使用副本、直接删除整个操作目录后链接仍可播放，并验证实际队列压力、失败残留回收、实际代理跳过及 `force=1` 保留值。真实网盘复制、删除只在取得对应测试账号授权后执行，mock 测试不代替该验收。

- 上传并发测试须覆盖在途任务下增减并发、暂停后修改并恢复、快速暂停恢复、重复领取和已清空任务；运行相关 `models` 测试及 `-race` 检查，配置保存与迁移同时验证失败不生效和旧配置兼容。
- 百度网盘的 `models/upload_baidupan_test.go` 使用生产 SQLite 单连接、真实上传队列和本地 HTTP 替身，覆盖单 worker 对照、同账号和不同账号并发、冷缓存及后续缓存命中。验证请求实际重叠、凭据隔离、分片内容完整、每个上传阶段只执行一次及任务正确完成；运行 `(cd backend && go test -race ./internal/models -run '^TestUploadQueueBaiduPanConcurrency$')`。
- OpenList 客户端测试覆盖并发创建、缓存命中、配置更新、同配置 Token 刷新去重、跨地址刷新隔离及登录 `401` 及时返回；认证恢复回归须覆盖零普通重试预算下的完整 multipart 重发、普通网络重试次数不变及持续 `401` 及时失败。`models/upload_openlist_test.go` 使用真实上传队列和本地 HTTP 替身覆盖单 worker 对照、同账号与不同账号并发，验证冷缓存和后续缓存命中、请求实际重叠、凭据隔离、有效凭据下每个文件只上传一次及任务正确完成。运行 `(cd backend && go test -race ./internal/openlist)` 和 `(cd backend && go test -race ./internal/models -run 'Test(UploadQueue|UpdateOpenList)')`。
- 运行 `(cd backend && go test ./internal/directoryupload/)`、`(cd backend && go test ./internal/syncstrm/)` 和相关 `models` 测试。
- STRM 收尾回归使用生产 SQLite 单连接配置，覆盖信息补齐、重复收尾去重、准备失败以及账本更新失败时的事务回滚；OpenList 真实上传队列测试也使用该配置，覆盖上传到本地收尾的完整链路。
- 修改外部上传协议时使用 mock 覆盖 callback、part size、checkpoint 和幂等行为；真实 115 / OSS 上传仅在获得沙箱账号和远端写入授权后执行。
- 修改前端目录监控配置时按 [验证说明](../engineering/verification.md) 选择相应验证。
