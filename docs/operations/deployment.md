# 部署与持久化

> 职责：说明 QMediaSync 的 Docker、发布二进制和飞牛应用部署方式，以及运行数据的持久化边界。
>
> 权威范围：本文档维护运行环境、镜像、挂载目录、端口暴露和部署身份；应用配置、密钥与日志见 [配置、密钥与日志](configuration.md)，数据库维护见 [数据库运维](database.md)，反向代理见 [反向代理与 SSE](reverse-proxy.md)。
>
> 修改时机：修改 Dockerfile、容器入口脚本、管理员恢复入口、二进制运行目录、飞牛安装流程、运行端口或持久化目录时必须更新本文档。
>
> 相关代码：`docker/`、`backend/main.go`、`backend/internal/app/admin_recovery.go`、`packaging/fnos/`、`scripts/recover-admin.sh`、`scripts/install/linux-init.sh`、`backend/internal/controllers/update.go`、`.github/workflows/release.yaml`。

## 部署选择与持久化边界

| 方式 | 适用场景 | 运行数据位置 |
| --- | --- | --- |
| Docker 镜像 | Linux 主机、NAS 或容器平台 | 容器内 `/app/config`，必须挂载到宿主机 |
| 发布二进制 | Windows 或直接管理 Linux 进程 | 可执行文件同级的 `config/` |
| 飞牛 FPK | 飞牛系统 | 平台的应用共享目录下 `config/`，由 QMS 网页初始化，路径由 `TRIM_*` 环境变量提供 |

正常启动在取得配置目录实例锁后、后台任务和 HTTP 服务启动前，会清理备份功能明确命名的遗留临时文件；正式 ZIP、配置和无法确认归属的旧目录保留。清理范围和文件发布保证见[数据库运维](database.md#备份)。

配置文件、SQLite 数据库、备份、日志和本机加密密钥都依赖配置目录。升级、迁移或重建容器时必须保留该目录及现有数据库。更新替换程序文件，新版本启动时可能升级数据库结构；需要回退能力时，提前备份与旧版本配套的数据库和配置。旧实例遗留的内嵌 PostgreSQL 数据也应保留，升级前按 [数据库运维](database.md#旧内嵌数据库) 完成数据处理；当前版本不提供旧库自动迁移。

默认 HTTP 端口为 `12333`。Docker 和发布二进制部署中，主程序只有在运行目录 `config/server.crt` 和 `config/server.key` 都存在时才额外监听 HTTPS `12332`。Emby 302 服务使用 `8095`（HTTP）和 `8094`（HTTPS）；仅在已配置 Emby 时启动。端口、证书和代理层细节分别见 [配置、密钥与日志](configuration.md) 与 [反向代理与 SSE](reverse-proxy.md)。

Emby 海报、背景原图模式通过持久化主配置 `config/config.yaml` 的 `emby302.images_original` 开启，默认关闭，修改后重启 QMediaSync 即可，无需重建镜像。流量和图片标记变化见 [Emby 302 图片与自定义脚本](configuration.md#emby-302-图片与自定义脚本)。

飞牛影视反代默认关闭，网页保存后立即应用本模块，不重启主站或 Emby。内置固定 HTTP `9005`；主站证书和私钥均存在时自动尝试 HTTPS `9006`，缺任一文件仅 HTTP，HTTPS 失败不停止 HTTP。Docker 可发布为 `19005:9005` 等宿主机端口，并需挂载同一批 STRM；二进制部署需释放固定端口。新增／替换证书和数据库恢复仍需按原方式重启，fnOS 下须区分程序根目录与业务共享配置目录。完整操作见 [飞牛影视反代运维](fnos-proxy.md)。

## Docker

源码构建镜像的 Go 版本与更新方式见 [发布流程](release.md#持续集成与预发布交付)；Windows 二进制升级时同时检查 [出站证书信任](configuration.md#emby-302-出站-https)。

正式发布镜像为 `ghcr.io/chen8945/qmediasync:latest`，同时提供 `linux/amd64` 和 `linux/arm64`。固定版本使用 `ghcr.io/chen8945/qmediasync:<tag>`；`beta` 和功能分支镜像的生成规则见 [发布流程](release.md)。

以下示例把全部运行状态保存到宿主机的 `./config`，并按需给应用挂载媒体目录：

```bash
mkdir -p config media

docker run -d \
  --name qmediasync \
  --restart unless-stopped \
  -p 12333:12333 \
  -p 8095:8095 \
  -p 8094:8094 \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/media:/media" \
  ghcr.io/chen8945/qmediasync:latest
```

首次运行没有主配置且没有遗留数据库状态时，访问 HTTP `12333` 完成配置向导。默认数据库配置为 PostgreSQL；选择 PostgreSQL 时连接单独部署的数据库服务。使用内置 HTTPS 时还需显式映射 `-p 12332:12332`，并将证书文件放入已挂载的 `config/` 目录。

容器入口脚本以 root 完成初始目录检查；可选环境变量 `GUID`、`GPID` 为数值 UID/GID。设置后脚本会在容器内创建对应用户或组（如不存在），并在值变化时递归修正 `/app/config` 的所有者，再以 `GUID` 运行主进程。例如：

```bash
docker run -d \
  --name qmediasync \
  --restart unless-stopped \
  -e GUID="$(id -u)" \
  -e GPID="$(id -g)" \
  -p 12333:12333 \
  -v "$(pwd)/config:/app/config" \
  -v "/srv/media:/media" \
  ghcr.io/chen8945/qmediasync:latest
```

该所有权修正不覆盖 `/media`；宿主机媒体目录的读写权限仍由部署者自行保证。不要用 `--user` 替代上述入口逻辑，否则入口无法创建用户或修正持久化目录的权限。

`--guid` 参数仅为兼容旧启动脚本而保留，不在应用进程中切换用户；实际运行身份由容器入口或飞牛平台决定。

本地从当前源码构建和测试镜像使用：

```bash
docker build -f docker/source.local.Dockerfile -t qmediasync:local .
```

`docker/source.local.Dockerfile` 仅为本地网络环境替换构建镜像源，产物目标与 `source.Dockerfile` 相同，不用于正式发布。

## 发布二进制与 systemd

发布包解压后，从包含 `QMediaSync` 和 `web_statics/` 的目录启动程序。Linux 与 Windows 都把运行配置保存在可执行文件同级的 `config/`；因此替换程序和静态资源时不得覆盖该目录。

`scripts/install/linux-init.sh` 是 Linux 上的 PostgreSQL 与 systemd 辅助脚本：它会先安装或初始化 PostgreSQL、创建数据库和用户，使用 `-i` 时还会创建 `qmediasync.service`。发布压缩包不包含此脚本，须从主仓库另行取得；运行时的工作目录必须是发布二进制所在目录，并要求 root 与 systemd。仅为 SQLite 实例配置开机启动时应自行创建服务，不能把此脚本当作通用 systemd 安装入口。

```bash
# 在发布二进制所在目录运行；替换为已取得脚本的实际路径。
sudo bash /实际脚本路径/linux-init.sh -i
systemctl status qmediasync
```

脚本创建的服务从当前目录执行 `QMediaSync`，不依赖旧 `postgres.env`，也不向 shell 启动文件写入 `DB_*` 环境变量。脚本不会生成应用数据库配置；新实例应通过首次配置向导或 `config/config.yaml` 填写数据库连接信息，SSL 同样由 YAML 配置。

在线更新支持 Windows 发布版、Docker 镜像和 `linux-init.sh` 创建的 systemd 服务；Docker 需由镜像默认入口脚本启动，它负责监视更新包并重启应用。下载完成后用 release 附带的 `checksums.txt` 校验更新包的 SHA-256，不一致时放弃安装；Windows 和 Docker 遇到未发布校验文件的旧版本只记录警告后继续。经 GitHub 代理下载时，校验文件复用更新包已经确定的代理前缀。

Windows 在 `config/update/` 准备完整发布文件，启动独立更新进程后才退出原进程；更新进程替换程序和 `web_statics/`，将旧文件移到 `old/`，替换失败尝试回滚并重启旧版本，不替换 Docker 脚本。Docker 先在安装目录写完临时更新包，再改名为 `qms.update.tar.gz` 交给监视器；入口脚本检查解压和替换结果，中途失败尝试恢复旧文件。准备或启动更新器失败会报告失败，交付成功保持安装中，重启后由页面核对实际版本，不能仅凭进程重启提示更新完成。

脚本创建的服务带有 `Restart=always` 和 `Environment=QMS_SYSTEMD_UPDATE=1`。程序同时检测到该标记和 systemd 为服务进程设置的 `INVOCATION_ID` 时才启用 systemd 在线更新；`INVOCATION_ID` 需要 systemd 232 及以上，更早的版本按不支持在线更新处理。

- 发起更新时，下载前依次确认三项：安装目录对服务用户可写，否则返回“安装目录不可写，无法在线更新，请手动下载安装”；运行中的程序就是安装目录下的 `QMediaSync`，程序改名或经其他路径运行时返回“当前运行方式不支持在线更新，请手动下载安装”；该版本发布了 `checksums.txt`，否则返回“该版本未发布校验文件，无法在线更新，请手动下载安装”。
- 下载并通过校验后，把发布包解压到安装目录下的 `update/`，用其中的 `QMediaSync` 和 `web_statics/` 替换当前文件，旧文件移到 `old/`。`old/` 在开始替换时清空，只保留最近一次；替换中途失败时旧文件恢复原位并报告更新失败，但此前的备份不再保留。`config/` 和包内仅供 Docker 使用的 `scripts/` 不会被替换。
- 替换完成约 3 秒后程序自行优雅退出，由 systemd 在 `RestartSec` 之后用新版本启动；关闭流程超过 60 秒仍未结束时强制退出。新版本启动失败时 systemd 会持续重试，不会自动退回旧版本；需要回退时停止服务，把 `old/` 中的文件移回安装目录。

旧版本脚本创建的服务没有该标记。确认服务使用 `Restart=always` 后，可以添加 drop-in 启用，不必重跑会初始化 PostgreSQL 的脚本：

```bash
sudo mkdir -p /etc/systemd/system/qmediasync.service.d
printf '[Service]\nEnvironment=QMS_SYSTEMD_UPDATE=1\n' | sudo tee /etc/systemd/system/qmediasync.service.d/online-update.conf
sudo systemctl daemon-reload && sudo systemctl restart qmediasync
```

手动运行、自定义服务等其他 Linux 运行方式返回“当前运行方式不支持在线更新，请手动下载安装”。手动更新时停止服务，用新发布包的 `QMediaSync` 和 `web_statics/` 替换旧文件，保留 `config/` 后再启动。

## 恢复后重启应用

数据库恢复进入维护后，程序保持暂停直到重启。恢复结果页面保留成功或失败说明，由用户点击“重启服务”；预检失败尚未进入维护时无需重启。回执授权和结果语义见[备份和恢复状态](database.md#备份和恢复状态)。

| 运行方式 | 页面重启方式 |
| --- | --- |
| `linux-init.sh` 创建并带有上述标记的 systemd 服务 | 程序优雅退出，由 `Restart=always` 重新拉起 |
| 支持纯重启的新 Docker 默认入口 | 程序向入口交付本轮 PID 的重启信号，入口重新启动应用；不依赖容器的外层重启策略 |
| Windows 发布版 | 独立子进程等待原进程退出，使用原启动参数和工作目录启动程序，不替换文件 |
| 旧 Docker 入口、手动运行、自定义托管或飞牛 | 页面提示使用对应部署管理方式手动重启 |

首次数据库配置保存后，普通 Windows 发布版、新 fnOS 原生包和支持纯重启的 Docker 默认入口会自动进入正式服务。Windows 复用独立重启进程，等待旧进程退出并释放实例锁；fnOS 和 Docker 使用只匹配本轮配置服务 PID 的请求。fnOS 包装脚本通过私有运行目录中的 `QMS_INITIAL_CONFIG_RESTART_FILE` 声明首次配置交接能力；旧包和手动 Linux 二进制仍提示外部重启，不增加 systemd 首次配置能力。

保存端先落盘再准备启动；准备失败返回“已保存但未能自动启动”，保留配置服务和重试入口。保存成功后重复提交只重试启动，不重写配置或再次清库；准备成功只安排一次退出。PostgreSQL 清库失败会中止保存；清库成功但配置落盘失败时，再次保存沿用本轮数据库设置，不重复清库。等待服务启动后，查看本轮日志中的初始化码，再手动刷新当前页面创建管理员；配置页不会自动跳转。首次配置能力不代表 fnOS 支持恢复后的程序内重启或网页在线更新。

Docker 入口在主进程退出后检查更新包和重启请求，仅在检测到更新包时提示执行更新；两者均无有效请求时退出容器，后续是否自动重启由容器重启策略决定。

Docker 的 `QMS_DOCKER_RESTART=1` 由支持此机制的入口脚本声明，不应手工给旧入口补这个环境变量；只在线更新二进制不能让已经运行的旧入口获得新能力。重启准备成功后约 3 秒开始退出，关闭超过 60 秒则强制退出。准备失败时当前服务继续保持维护，页面可重试；重新上线后再登录并核验结果。

## 飞牛 FPK

fnOS FPK 由发布流程生成，源码位于 `packaging/fnos/`。平台安装向导只说明使用步骤；数据库选择、连接测试和配置保存统一使用 QMS 网页，不在安装钩子维护第二套 YAML。全新安装启动后访问 HTTP `12333`，选择 SQLite 或已有 PostgreSQL，保存后自动启动正式服务。已有主配置（含旧 `config.yml`）直接沿用。

平台注入 `TRIM_APPDEST`、`TRIM_PKGETC`、`TRIM_PKGVAR`、`TRIM_DATA_SHARE_PATHS` 等路径。共享目录下的 `config/` 是主配置、SQLite、日志和本机密钥的持久化目录。旧平台配置仅向空共享配置目录迁移；共享主配置已存在时继续使用它并保留旧目录，其他数据冲突时停止迁移并报告路径，不能覆盖或混合实例。迁移失败不能回退为空实例。Windows 旧目录迁移保持原行为。

迁移先取得源目录锁，再在目标配置目录写入 `.fnos-config-migration-pending`，持锁移动全部文件，成功后才删除标记。源目录被占用时不写标记，停止占用进程或等待恢复命令结束后即可重试启动。启动时优先检查未完成标记，存在即停止，不因 YAML 已存在或旧目录已空而继续启动，也不自动补建密钥或数据库。迁移报错或进程中断后，先停止应用并分别备份日志列出的两个目录，再核对同一实例的 YAML、原 `encryption.key`、数据库及其他配置文件是否已完整归入共享配置目录；SQLite 同时保留可能存在的 `-wal`、`-shm` 文件。确认完整后才能手工移除标记并启动，不应仅删除标记反复重试。

两架构共用启动脚本，运行身份仍由平台指定，不以 root 运行。首次配置服务也计为运行中；仅消费一次匹配请求，平台停止优先，正式服务崩溃不会循环重启。停止先 TERM，最多等待 90 秒才强制结束；诊断输出位于平台应用运行目录的 `info.log`。内部应用名、用户、组、共享目录和入口 ID 保持 `qmediasync`，显示名称为 QMediaSync。

包装脚本的 PID、控制锁、停止标记和诊断日志使用私有 `umask 077`；主程序每次启动（含首次配置交接）沿用平台传入的 umask。STRM 和新建目录的实际权限由程序请求的权限、平台 umask 与目录 ACL 共同决定，Emby 需要获得相应的目录访问和文件读取权限。更新包装脚本只影响后续创建的文件，已有输出的权限需单独核对。

安装、升级及保留数据重装不重建配置；卸载向导默认保留，明确删除时只删除已校验的本应用共享目录下 `config/`，包含其中的 SQLite、密钥和本地备份，不处理外部 PostgreSQL 或媒体目录。不要把 Docker 的 `/app/config`、`GUID`/`GPID` 或 systemd 配置套用到原生包。备份见 [数据库运维](database.md)。

本地目录仍使用平台环境变量和既有授权刷新回调；找不到目录或无法写入时，在应用设置检查授权，返回 QMS 刷新，必要时停止后启动应用。全部撤销授权时，旧目录刷新接口不接受空值，需重启使新环境生效。本次不接入 fnOS 账号、统一网关或新文件授权 SDK。

fnOS 的 QMS 网页仅提供更新说明，后端同样拒绝在线安装。应用中心来源的版本由平台提供更新；手动安装版本按发布说明下载对应 FPK，通过平台入口处理。是否有商店更新取决于应用来源，本项目构建 FPK 不代表已上架。GitHub `checksums.txt` 可供手工核对，平台生命周期脚本不读取它。

## 管理员恢复

忘记管理员密码或无法使用两步验证时，优先重置密码。需要重新创建管理员时才使用删除模式；两种模式的数据保留、会话、两步验证和 API Key 契约见 [认证会话](../architecture/authentication-sessions.md#本地管理员恢复)。恢复期间服务会中断，先安排好正在执行的同步、上传和下载。

### Docker Compose

使用本仓库的 [recover-admin.sh](../../scripts/recover-admin.sh)，要求 Bash、Docker Compose **2.24.4 或更新版本**、`tar`、`awk`，并有访问 Docker 的权限。脚本针对标准镜像布局 `/app/QMediaSync` 和可写持久化挂载 `/app/config`，目标服务必须已经正常部署过，且只有一个容器。配置卷使用 `volume.subpath` 时，须保留原子目录挂载并按下方非 Compose 部署方式执行，脚本会在停服前拒绝该情况。

在存放原 Compose 文件的目录打开终端，通过 `curl` 在线读取脚本并直接执行，无需保存到本地：

```bash
curl -fsSL https://raw.githubusercontent.com/chen8945/QMediaSync/main/scripts/recover-admin.sh | bash -s -- --action reset-password
```

不传 `-f` 时，只从执行脚本的当前目录依次查找 `compose.yaml`、`compose.yml`、`docker-compose.yaml`、`docker-compose.yml`，使用第一个存在的文件；同时按 `compose.override.yaml`、`compose.override.yml`、`docker-compose.override.yaml`、`docker-compose.override.yml` 顺序选择第一个覆盖文件。不会从脚本所在目录或父目录猜测部署。使用 `COMPOSE_FILE` 或其他文件组合时，通过 `-f` 明确指定全部文件。

复杂部署沿用原来的文件、项目名和环境文件，例如：

```bash
curl -fsSL https://raw.githubusercontent.com/chen8945/QMediaSync/main/scripts/recover-admin.sh | bash -s -- \
  -f compose.yaml -f compose.production.yaml \
  -p qmediasync --env-file .env.production \
  --service qmediasync --action reset-password
```

`-f` 和 `--env-file` 可重复，整个操作保持同一目录上下文；YAML、`.env`、变量替换和相对路径都由 Compose 解析。唯一候选服务可自动识别；未指定 `--service` 且无法唯一识别时，脚本列出当前 Compose 项目已部署的容器、服务名和状态，按编号选择 QMediaSync，按 `Ctrl+D` 可取消。通过管道执行时仍从终端读取选择；无可用终端时须通过 `--service` 明确指定服务名。

脚本先由 Compose 解析包含服务级 `env_file` 的完整配置快照，再核对服务配置哈希、实际镜像、持久化挂载和运行身份。服务配置与实际部署不一致、标签已指向其他镜像、容器内二进制已被在线更新、存在待更新包、没有持久化配置卷或存在多个目标容器时拒绝执行。先使服务配置与部署一致，再恢复；脚本不会自动拉取镜像、构建或重建原服务。

脚本只停止目标 QMediaSync 容器，PostgreSQL 继续运行；然后使用配置快照创建一次性容器，直接运行恢复二进制。恢复容器锁定原镜像 ID，复用原容器的实际配置卷、网络和 UID/GID，以及配置快照中的环境；bind 挂载保留传播模式和 SELinux 选项。项目级卷名或网络名即使改变，也不能把恢复指向另一份配置或另一数据库网络。临时容器关闭日志采集、不开启正常启动脚本，退出后删除。原容器原先运行则启动原容器，原先停止则保持停止。最后显示恢复结果和新密码；启动失败也会显示已提交的新密码，并返回非零退出码。启动容器后仍应确认应用能正常访问。

删除管理员使用：

```bash
curl -fsSL https://raw.githubusercontent.com/chen8945/QMediaSync/main/scripts/recover-admin.sh | bash -s -- --action delete-admin
```

脚本显示目标并要求输入 `DELETE`。非交互执行时须显式附加 `--yes`。删除后启动服务，从新一轮启动日志获取初始化码，重新创建管理员。

### 发布二进制、飞牛及非 Compose 部署

先停止服务，再以原运行身份执行一次恢复命令，完成后自行启动。不要把恢复参数写进长期运行的 systemd、容器或飞牛启动配置。

```bash
# Linux 发布二进制，默认使用可执行文件同级的 config/
./QMediaSync --reset-admin-password

# 显式指定已有配置目录，例如飞牛应用共享目录
./QMediaSync --reset-admin-password --config-dir /实际共享目录/config

# 删除管理员需要明确确认
./QMediaSync --delete-admin --yes --config-dir /实际配置目录
```

Windows 先退出托盘中的 QMediaSync，再从 PowerShell 执行：

```powershell
.\QMediaSync.exe --reset-admin-password
```

Windows 发布包继续使用无控制台模式，恢复结果由系统窗口显示，按 `Ctrl+C` 可复制窗口内容，关闭后命令退出。需要交互式桌面，不适用于无人值守的 Windows 服务会话。终端和窗口关闭后不能通过应用日志找回新密码；遗失时再次重置。

未使用 Compose 的 Docker 部署应先停止原容器，以同版本镜像、原挂载、网络和数值 UID/GID 执行一次性命令。示例中的值须替换为实际部署值：

```bash
docker run --rm --log-driver none --network 原数据库网络 \
  --user 原UID:原GID \
  --mount type=bind,src=/实际配置目录,dst=/app/config \
  --entrypoint /app/QMediaSync \
  ghcr.io/chen8945/qmediasync:实际版本 --reset-admin-password
```

不要在仍运行的原容器中用 `docker exec` 直接恢复。所有连接同一数据库的 QMediaSync 实例都必须停止；同配置目录的实例锁不能协调不同目录或不同主机。配置缺失、数据库不存在或存在 [旧内嵌数据库状态](database.md#旧内嵌数据库) 时，先处理对应部署状态；恢复命令不会自动建库或迁移。
