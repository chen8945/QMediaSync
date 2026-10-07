# Emby 通知配置

这里配置的是 **Emby 把入库、删除和播放事件告诉 QMediaSync**。如果还想把消息推送到 Telegram 等渠道，需要再配置 QMediaSync 的通知渠道，步骤见下文。

## 先分清两个 API Key

| 密钥 | 在哪里创建 | 填在哪里 |
| --- | --- | --- |
| Emby API Key | Emby 控制台的“高级 → API 密钥” | QMediaSync“系统设置 → Emby”的“Emby API Key” |
| QMediaSync API Key | QMediaSync“系统设置 → API Key” | Emby Webhook 的请求头或通知地址参数，用于验证通知来源 |

这两个密钥不能互换。含有密钥的通知地址不要发到截图、公开日志或 Issue 中。

## 1. 配好 QMediaSync

1. 在 Emby 中创建 API 密钥。
2. 打开 QMediaSync 的“系统设置 → Emby”，填写 Emby 地址和刚创建的 Emby API Key，然后点击“保存设置”。例如，Emby 地址为 `http://192.168.1.20:8096`。
3. 如果同时使用 Emby 302 代理，首次配置或更换 Emby 地址后，还需要重启 QMediaSync。
4. 按 [Emby 媒体库同步](Emby-媒体库同步.md) 启用同步，并完成一次全量 Emby 条目同步。使用联动删除前，还要先确保 STRM 文件记录已经更新。
5. 保持“通知链接鉴权”开启，在“系统设置 → API Key”创建供 Emby 使用的 QMediaSync API Key。

## 2. 在 Emby 中添加 Webhook

进入 Emby 控制台的通知设置，新建通知并选择 `Webhooks`。不同 Emby 版本的菜单位置可能不同，配置时以以下字段为准。

| 设置 | 填写内容 |
| --- | --- |
| 名称 | `QMediaSync`，也可以自定义 |
| 网址 | QMediaSync“系统设置 → Emby”显示的“Emby 通知链接”，例如 `http://192.168.1.10:12333/emby/webhook` |
| 请求内容类型 | JSON，即 `application/json` |
| 分组 | 关闭“按剧集和专辑对通知进行分组”等分组选项 |
| 媒体库 | 选择希望发送通知的媒体库 |
| 媒体库事件 | 按需勾选“已添加新媒体”和“媒体已移除” |
| 播放事件 | 按需勾选“开始”“暂停”“停止” |

通知地址必须能从 **Emby 所在机器或容器** 访问。页面显示的地址来自你访问 QMediaSync 的入口；如果 Emby 无法访问它，就改成 Emby 能访问的地址，并保留 `/emby/webhook` 路径。

开启鉴权后，还需要选择一种方式传入 **QMediaSync API Key**：

- Emby 的 Webhook 配置支持自定义请求头时，设置 `X-API-Key`，值为 QMediaSync API Key。
- 没有请求头设置时，在通知地址末尾加上 `?api_key=你的_QMediaSync_API_Key`，例如 `http://192.168.1.10:12333/emby/webhook?api_key=你的_QMediaSync_API_Key`。

保存通知。QMediaSync 当前处理的播放事件是开始、暂停和停止，不把“取消暂停”当作独立支持的播放事件。

## 3. 验证通知能收到

先保持 QMediaSync 的“删除时联动删除网盘文件”关闭，用可丢弃的测试媒体完成一次入库或播放操作，再查看 QMediaSync 日志。删除通知也应先在这个开关关闭时测试。

没有收到时，依次检查：

1. Emby 能否访问通知地址，容器网络、防火墙和反向代理是否放行。
2. 使用的是否为 QMediaSync API Key，密钥是否仍有效；`401` 通常表示缺少密钥或密钥无效。
3. 内容类型是否为 `application/json`，是否已关闭分组，是否勾选了对应媒体库和事件。
4. 通知所属用户与实际操作用户是否符合 Emby 的通知设置。“通知属于管理员”不代表已经覆盖所有用户事件。

Emby 可能缓冲后再发送通知，QMediaSync 也会合并同一部剧的消息，因此不一定立即出现。

## 4. 把消息推送到 Telegram 等渠道

1. 在 QMediaSync 的“系统设置 → 通知管理”添加并启用所需渠道。
2. 使用渠道的“测试”按钮，确认该渠道能收到消息。
3. 打开渠道的“规则”，启用需要的媒体入库、媒体删除和播放事件。
4. 再做一次实际入库或播放操作，确认事件消息能送达。

详细说明见 [任务与通知](任务与通知.md)。只配 Emby Webhook，没有启用 QMediaSync 通知渠道或对应规则时，不会自动向 Telegram 等平台发送消息。

## 与媒体提取、联动删除的关系

- 自动提取还需要开启“入库后提取媒体信息”，只对收到入库通知的电影和剧集生效，见 [Emby 媒体信息提取](Emby-媒体信息提取.md)。消息推送与媒体信息提取分别处理，不需要等提取成功才发送消息。
- 删除网盘原文件还需要开启“删除时联动删除网盘文件”。先完整阅读 [Emby 联动删除网盘文件](Emby-联动删除网盘文件.md)，确认文件关联、删除范围和测试结果后再开启。
- Emby 官方通知可以独立使用，无需安装插件。如果已使用 [Emby 神医助手（Strm Assistant）](https://github.com/sjtuross/StrmAssistant/wiki) 或 [MediaInfoKeeper](https://github.com/honue/MediaInfoKeeper/wiki)，且当前插件版本提供“通知增强”，可仅启用这项功能，并在 Webhook 中加选“媒体深度删除”（`deep.delete`）；不要为此开启插件自己的物理深度删除功能。
- 如事件列表提供独立的移动、重命名、扫描失效或移除媒体库事件，不要把它们勾选为联动删除入口。普通“媒体已移除”也可能由文件变化或扫描触发，仍需遵守联动删除页的注意事项。
