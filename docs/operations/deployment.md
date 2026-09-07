# 部署与持久化

> 职责：说明 QMediaSync 的 Docker、发布二进制和飞牛应用部署方式，以及运行数据的持久化边界。
>
> 权威范围：本文档维护运行环境、镜像、挂载目录、端口暴露和部署身份；应用配置、密钥与日志见 [配置、密钥与日志](configuration.md)，数据库维护见 [数据库运维](database.md)，反向代理见 [反向代理与 SSE](reverse-proxy.md)。
>
> 修改时机：修改 Dockerfile、容器入口脚本、管理员恢复入口、二进制运行目录、飞牛安装流程、运行端口或持久化目录时必须更新本文档。
>
> 相关代码：`docker/`、`backend/main.go`、`backend/admin_recovery.go`、`backend/FNOS/`、`scripts/recover-admin.sh`、`scripts/install/linux-init.sh`、`.github/workflows/release.yaml`。

## 部署选择与持久化边界

| 方式 | 适用场景 | 运行数据位置 |
| --- | --- | --- |
| Docker 镜像 | Linux 主机、NAS 或容器平台 | 容器内 `/app/config`，必须挂载到宿主机 |
| 发布二进制 | Windows 或直接管理 Linux 进程 | 可执行文件同级的 `config/` |
| 飞牛 FPK | 飞牛系统 | 平台的应用共享目录下 `config/`，由安装向导和 `TRIM_*` 环境变量管理 |

配置文件、SQLite 数据库、备份、日志、本机加密密钥和用户设置都依赖配置目录。升级、迁移或重建容器前必须备份并保留该目录；不能只保留可执行文件或镜像层。旧实例遗留的内嵌 PostgreSQL 数据也应保留，升级前按 [数据库运维](database.md#旧内嵌数据库) 完成数据处理；当前版本不提供自动迁移。

默认 HTTP 端口为 `12333`。Docker 和发布二进制部署中，主程序只有在运行目录 `config/server.crt` 和 `config/server.key` 都存在时才额外监听 HTTPS `12332`。Emby 302 服务使用 `8095`（HTTP）和 `8094`（HTTPS）；仅在已配置 Emby 时启动。端口、证书和代理层细节分别见 [配置、密钥与日志](configuration.md) 与 [反向代理与 SSE](reverse-proxy.md)。

## Docker

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

`scripts/install/linux-init.sh` 是 Linux 上的PostgreSQL 与 systemd 辅助脚本：它可安装或初始化 PostgreSQL、创建数据库和用户，并用 `-i` 创建 `qmediasync.service`。脚本要求在发布二进制所在目录运行，并要求 root 与 systemd；它不是 Docker 或飞牛的安装入口。

```bash
sudo scripts/install/linux-init.sh -i
systemctl status qmediasync
```

脚本创建的服务从当前目录执行 `QMediaSync`，不依赖旧 `postgres.env`，也不向 shell 启动文件写入 `DB_*` 环境变量。脚本不会生成应用数据库配置；新实例应通过首次配置向导或 `config/config.yaml` 填写数据库连接信息，SSL 同样由 YAML 配置。

## 飞牛 FPK

飞牛 FPK 由发布流程生成；应用安装向导负责选择 SQLite 或PostgreSQL 并写入配置。飞牛运行时由平台注入 `TRIM_APPDEST`、`TRIM_PKGETC`、`TRIM_DATA_SHARE_PATHS` 等路径变量，程序会将实际配置目录迁移或定位到共享数据目录下的 `config/`。

不要把 Docker 的 `/app/config` 路径、`GUID`/`GPID` 约定或裸机 systemd 服务直接套用到飞牛安装；在飞牛文件管理器中保留应用共享目录下的 `config/`，再按 [数据库运维](database.md) 执行备份和恢复。

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
