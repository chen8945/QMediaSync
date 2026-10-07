本文介绍直接运行 Linux 发布包，以及用 systemd 在后台运行的方法。使用容器请阅读 [Docker 安装](Docker安装.md)。

## 1. 下载并解压

在终端执行 `uname -m`，选择对应架构：

| 输出 | 下载文件 |
| --- | --- |
| `x86_64` | `QMediaSync_linux_x86_64.tar.gz` |
| `aarch64` 或 `arm64` | `QMediaSync_linux_arm64.tar.gz` |

从 [QMediaSync 发布页面](https://github.com/chen8945/QMediaSync/releases) 下载需要的版本。请下载发布附件中的压缩包，不是 `Source code` 源码包。压缩包文件名不带版本号，版本以发布页面为准。

下面以 `x86_64` 为例。在压缩包所在目录打开终端，以日常使用的普通用户运行：

```bash
mkdir -p "$HOME/apps"
tar -xzf QMediaSync_linux_x86_64.tar.gz -C "$HOME/apps"
cd "$HOME/apps/QMediaSync_linux_x86_64"
chmod +x QMediaSync
./QMediaSync
```

ARM64 用户把命令中的两处 `x86_64` 换成 `arm64`。解压后有一层 `QMediaSync_linux_x86_64` 目录，必须进入包含 `QMediaSync` 和 `web_statics/` 的目录再启动。

`config/` 会保存在可执行文件旁边，包含配置、SQLite 数据库、日志和本机加密密钥。请把程序放在有写入权限的固定目录，升级时保留 `config/`。

## 2. 首次配置数据库

程序启动后会尝试打开浏览器。如果没有自动打开：

- 在服务器本机访问 `http://127.0.0.1:12333`。
- 在其他电脑使用服务器的局域网 IP，例如访问 `http://192.168.1.10:12333`。

首次配置请在可信的本机或局域网内完成。打不开时，先检查终端是否报错，以及主机防火墙是否允许局域网访问 `12333`。

首次启动会显示数据库配置向导：

1. 选择 SQLite 或 PostgreSQL。SQLite 不需要安装额外服务，适合先开始使用；更多区别见 [数据库的选择](数据库的选择.md)。
2. 选择 PostgreSQL 时，先准备 PostgreSQL 15 及以上的独立服务。可使用 [Docker 安装](Docker安装.md) 页面末尾的“单独部署 PostgreSQL”示例；应用发布包不包含 PostgreSQL。
3. 填写数据库主机、端口、用户名、密码和独立的数据库名，点击“测试连接”，通过后保存。若采用上述本机 Docker 示例，主机填 `127.0.0.1`，端口填 `15432`，其他字段与 Compose 配置一致。
4. 如果向导提示已有数据库包含数据，保留数据应选择“取消”；选择“确定”会尝试删除并重建该数据库。
5. 保存成功后程序会退出。在原目录重新执行：

```bash
./QMediaSync
```

## 3. 创建管理员

1. 在这次启动的终端输出中找到“检测到系统尚未创建管理员，请使用以下初始化码完成首次管理员创建”，复制初始化码。也可以查看 `config/logs/app.log`。
2. 刷新浏览器，在“创建管理员”表单填写初始化码、用户名和密码。用户名为 3～20 个英文字母或数字；密码至少 6 个字符，不能是纯数字或纯字母。
3. 创建成功后，使用新账号登录。

初始化码只在本次启动期间有效。如果还没创建管理员就重启了程序，请使用最新日志中的初始化码。已有管理员时不会重复生成。

先按 [快速开始](快速开始.md) 完成基本设置。直接运行时，关闭终端或按 `Ctrl+C` 会结束程序；需要持续运行，可按下一节设置 systemd。

## 4. 使用 systemd 后台运行和开机自启

以下方法适用于使用 systemd 的 Linux 系统，沿用刚才安装程序的普通用户。先在前台运行程序的终端按 `Ctrl+C`，等程序退出；不要同时启动两份程序。

确认当前目录仍包含 `QMediaSync` 和 `web_statics/`，然后创建服务：

```bash
QMS_INSTALL_DIR="$(pwd)"
QMS_RUN_USER="$(id -un)"

sudo tee /etc/systemd/system/qmediasync.service > /dev/null <<EOF
[Unit]
Description=QMediaSync
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=$QMS_RUN_USER
WorkingDirectory=$QMS_INSTALL_DIR
ExecStart="$QMS_INSTALL_DIR/QMediaSync"
Restart=always
RestartSec=5
TimeoutStopSec=70

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now qmediasync
```

`User` 使用执行上述命令的用户，`sudo` 只用于写入服务文件和管理服务。请从普通用户终端执行，安装目录建议不含空格；如果原来用 root 运行过程序，先按 [文件权限相关问题](文件权限相关问题.md) 核对该用户对 `config/` 和媒体目录的读写权限。

查看状态和日志：

```bash
sudo systemctl status qmediasync --no-pager
sudo journalctl -u qmediasync -n 100 --no-pager
sudo journalctl -u qmediasync -f
```

状态为 `active (running)` 表示服务正在运行。日志查看中按 `Ctrl+C` 只会退出查看，服务继续运行。更详细的应用日志仍在安装目录的 `config/logs/`。

常用操作：

| 操作 | 命令 |
| --- | --- |
| 停止 | `sudo systemctl stop qmediasync` |
| 启动 | `sudo systemctl start qmediasync` |
| 重启 | `sudo systemctl restart qmediasync` |
| 取消开机自启并停止 | `sudo systemctl disable --now qmediasync` |

这份自建服务采用手动更新：停止服务，替换发布包中的 `QMediaSync` 和 `web_statics/`，保留 `config/` 后再启动。完整步骤见 [更新与旧版迁移](更新与旧版迁移.md)。页面提示不支持在线更新或重启时，使用上面的 systemd 命令即可。

仓库另有 `linux-init.sh` 辅助脚本，它会安装或初始化 PostgreSQL，并创建服务；Linux 发布包不包含该脚本。本页的 SQLite 或已有数据库安装方式无需运行它。

## 安装后检查

- 能登录管理页面，刷新后仍保持正常。
- `config/config.yaml` 已生成，重启程序后仍使用原来的账号和配置。
- QMediaSync 对保存 STRM 和元数据的目录有写入权限，Emby 对这些文件有读取权限。
- 若启用 systemd，服务状态为 `active (running)`，日志没有连续启动失败。

默认管理端口为 HTTP `12333`；域名、HTTPS 和代理配置见 [反向代理配置](反向代理配置.md)。备份见 [备份与恢复](备份与恢复.md)，忘记密码见 [管理员与密码恢复](管理员与密码恢复.md)。
