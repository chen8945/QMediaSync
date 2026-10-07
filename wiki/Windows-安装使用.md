## 1. 下载和启动

1. 从 [QMediaSync 发布页面](https://github.com/chen8945/QMediaSync/releases) 下载 Windows 压缩包。一般 Intel 或 AMD 处理器的台式机、笔记本选 `QMediaSync_windows_x86_64.zip`；Windows on ARM 设备选 `QMediaSync_windows_arm64.zip`。
2. 将压缩包完整解压到固定目录，例如 `D:\QMediaSync`。不要直接在压缩包里运行，也不要只复制 `QMediaSync.exe`；旁边的 `web_statics` 文件夹同样需要保留。
3. 双击 `QMediaSync.exe`。首次启动会尝试在浏览器打开数据库配置页面；完成配置并再次启动后，程序会出现在任务栏通知区域，也就是系统托盘。
4. 如果浏览器没有自动打开，访问 `http://127.0.0.1:12333`。从其他电脑访问时，使用这台 Windows 电脑的局域网 IP，例如 `http://192.168.1.10:12333`。

程序会在同级目录创建 `config`，用于保存配置、SQLite 数据库、日志和本机加密密钥。请使用自己有写入权限的目录；程序会对 C 盘或系统目录提示权限风险，建议使用可写的非系统盘目录。

## 2. 首次配置数据库

首次打开会进入数据库配置向导，请在可信的本机或局域网内完成：

1. 选择 SQLite 或 PostgreSQL。SQLite 无需额外安装；选择依据见 [数据库的选择](数据库的选择.md)。
2. 如果选择 PostgreSQL，先按本页下方步骤安装 PostgreSQL，或准备已有的 PostgreSQL 15 及以上服务。
3. PostgreSQL 需要填写主机、端口、用户名、密码和数据库名，先点击“测试连接”，成功后保存。SQLite 按页面提示确认即可。
4. 如果提示已有数据库包含数据，要保留数据就选择“取消”；选择“确定”会尝试删除并重建数据库。
5. 保存成功后，QMediaSync 会退出。重新双击 `QMediaSync.exe` 启动。

## 3. 创建管理员

1. 用记事本打开程序目录中的 `config\logs\app.log`，找到最新一次启动记录中的“检测到系统尚未创建管理员，请使用以下初始化码完成首次管理员创建”，复制后面的初始化码。
2. 刷新 `http://127.0.0.1:12333`，在“创建管理员”表单填写初始化码、用户名和密码。用户名为 3～20 个英文字母或数字；密码至少 6 个字符，不能是纯数字或纯字母。
3. 创建成功后，使用新账号登录，再按 [快速开始](快速开始.md) 完成账号和同步目录设置。

初始化码只在本次启动期间有效。创建管理员前如果退出并重新启动了程序，请重新查看最新日志；已有管理员的实例不会再生成初始化码。

## 退出和再次启动

关闭浏览器页面不会退出 QMediaSync。需要退出时，在系统托盘找到 QMediaSync 图标，右键选择“退出程序”；如果图标被折叠，先展开通知区域。

停止后再次使用，双击原目录中的 `QMediaSync.exe` 即可。它会继续使用原来的 `config`，无需重新配置数据库或创建管理员。更新前也应先退出托盘程序，具体方法见 [更新与旧版迁移](更新与旧版迁移.md)。

忘记密码或无法完成两步验证时，见 [管理员与密码恢复](管理员与密码恢复.md)。请保留整个 `config`，不要通过删配置尝试重置密码。

## 安装 PostgreSQL（可选）

只在选择 PostgreSQL 且还没有可用数据库时执行；SQLite 用户可以跳过。

1. 从 [PostgreSQL 官方 Windows 下载入口](https://www.postgresql.org/download/windows/) 前往 EDB 安装包页面，选择受支持的 PostgreSQL 15 及以上正式版本。普通 Intel 或 AMD 电脑选择 Windows x86-64 安装包。
2. 运行安装程序，选择安装目录和数据目录。组件中保留 PostgreSQL Server；需要图形化管理数据库时，同时勾选 pgAdmin 4。Stack Builder 用于安装额外工具，QMediaSync 不依赖它。

<img width="708" height="549" alt="PostgreSQL 安装组件选择" src="https://github.com/user-attachments/assets/1b5df9ae-ec60-40fd-86dc-c0b6ac7c9154" />

3. 为数据库管理员 `postgres` 设置自己的密码并记下来，不要沿用教程里的简单示例密码。这不是 QMediaSync 的登录密码。
4. 端口通常保持 `5432`；已有其他数据库占用时换一个可用端口，并记下实际值。
5. 区域设置可按自己的使用需要选择，然后继续完成安装。截图仅用于识别步骤，实际安装器版本和目录可能不同。安装器包含 PostgreSQL 服务和 pgAdmin，见 [PostgreSQL 官方安装说明](https://www.postgresql.org/download/windows/)。

<img width="708" height="549" alt="PostgreSQL 数据库区域设置" src="https://github.com/user-attachments/assets/508c6ddb-6424-496a-85fc-cfe88d0a5dfb" />

6. 打开 QMediaSync 的数据库配置向导，按下表填写：

| 项目 | 本机安装 PostgreSQL 时的示例 |
| --- | --- |
| 数据库类型 | PostgreSQL |
| 主机 | `127.0.0.1` |
| 端口 | 安装时的端口，通常为 `5432` |
| 用户名 | `postgres` |
| 密码 | 安装 PostgreSQL 时设置的密码 |
| 数据库名 | `qmediasync`，使用独立数据库，不要与其他应用共用 |
| SSL | 按数据库实际配置填写；本机未配置 TLS 时保持关闭 |

7. 点击“测试连接”。如果失败，打开 Windows 的“服务”，确认对应的 PostgreSQL 服务正在运行，再核对端口和密码。
8. 测试通过后保存，重新启动 QMediaSync，按本页“创建管理员”完成初始化。程序会使用你填写的数据库名创建数据库，所填用户需要有相应权限。

如果 PostgreSQL 在另一台设备上，主机填写那台设备的局域网 IP 或域名，端口使用实际对外提供的端口。

## 安装后检查

- 能正常登录，退出后重新启动仍保留原账号和配置。
- 保存 STRM 的目录可写，Emby 能读取生成的文件。
- 局域网访问失败时，先确认程序正在运行，再检查 Windows 防火墙是否允许你的局域网访问端口 `12333`。

备份方法见 [备份与恢复](备份与恢复.md)，域名和 HTTPS 访问见 [反向代理配置](反向代理配置.md)。
