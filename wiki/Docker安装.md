本文适用于已经安装 Docker Engine 和 Docker Compose 的 Linux 主机或 NAS。镜像支持 `amd64` 和 `arm64`；macOS 没有独立发行包，可通过 Docker 运行。

首次安装先选一种数据库方案：只想尽快开始，可用 SQLite；已有 PostgreSQL 或希望单独管理数据库，可用 PostgreSQL 15 及以上。区别见 [数据库的选择](数据库的选择.md)。QMediaSync 镜像本身不包含 PostgreSQL 服务。

## 1. 准备目录

先在终端确认 Docker Compose 可用：

```bash
docker compose version
```

建立固定的部署目录，以后启动、更新和查看日志都在这个目录执行：

```bash
mkdir -p qmediasync
cd qmediasync
mkdir -p config media
```

下面示例的 `./config` 保存配置和应用数据，`./media` 存放生成的 STRM 和元数据。已有媒体目录时，把 `./media:/media` 改为实际路径，例如 `/vol1/1000/网盘:/media`。冒号左边是宿主机路径，右边是 QMediaSync 看到的路径。

## 2. 选择一种 Compose 配置

### 方案 A：使用 SQLite

在部署目录新建 `compose.yaml`，填入：

```yaml
services:
  qmediasync:
    image: ghcr.io/chen8945/qmediasync:latest
    container_name: qmediasync
    restart: unless-stopped
    ports:
      - "12333:12333"
      - "8095:8095"
    volumes:
      - ./config:/app/config
      - ./media:/media
```

首次进入数据库向导时选择 SQLite 即可，不需要再安装数据库。

### 方案 B：同时部署 PostgreSQL

如果还没有 PostgreSQL，可用下面这份完整的 `compose.yaml`。先把 `POSTGRES_PASSWORD` 改为自己的密码，并记录下来；它是数据库密码，与稍后创建的 QMediaSync 管理员密码不同。

```yaml
services:
  postgres:
    image: postgres:18
    restart: unless-stopped
    environment:
      POSTGRES_USER: qms
      POSTGRES_PASSWORD: "replace-with-a-long-random-password"
      POSTGRES_DB: qmediasync
    volumes:
      - ./postgres/data:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -h 127.0.0.1 -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 12
      start_period: 10s

  qmediasync:
    image: ghcr.io/chen8945/qmediasync:latest
    container_name: qmediasync
    restart: unless-stopped
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "12333:12333"
      - "8095:8095"
    volumes:
      - ./config:/app/config
      - ./media:/media
```

两个服务在同一网络中，QMediaSync 可以直接连接 `postgres:5432`，因此无需把数据库端口发布到宿主机。健康检查用于等待 PostgreSQL 就绪，再启动 QMediaSync；普通的启动顺序不能保证数据库已经可用。见 [Docker Compose 启动顺序说明](https://docs.docker.com/compose/how-tos/startup-order/)。

首次数据库向导按下表填写：

| 项目 | 填写内容 |
| --- | --- |
| 数据库类型 | PostgreSQL |
| 主机 | `postgres` |
| 端口 | `5432` |
| 用户名 | `qms` |
| 密码 | 刚才设置的 `POSTGRES_PASSWORD` |
| 数据库名 | `qmediasync` |
| SSL | 本例未给 PostgreSQL 配置 TLS，保持关闭 |

PostgreSQL 官方镜像从 18 开始，默认数据目录放在版本子目录中，例如 `/var/lib/postgresql/18/docker`，应挂载父目录 `/var/lib/postgresql`。使用 17 或更早版本的默认配置时，挂载目标是 `/var/lib/postgresql/data`。这些路径属于 PostgreSQL 容器，与 QMediaSync 的 `/app/config` 无关。见 [PostgreSQL 官方镜像数据目录说明](https://github.com/docker-library/docs/blob/master/postgres/README.md#pgdata)。

已有数据库不能只改镜像的大版本号直接启动。升级前先备份，并按 PostgreSQL 的升级方式迁移数据。`POSTGRES_*` 变量只在空数据目录初始化时生效，修改 Compose 中的密码不会自动修改已有数据库用户的密码。

### 方案 C：连接已有 PostgreSQL

先使用方案 A 的 QMediaSync 配置，再根据数据库位置选择连接方式：

| 数据库位置 | 数据库向导中的主机和端口 |
| --- | --- |
| 同一 Compose 文件中的服务 | 服务名和容器端口，例如 `postgres`、`5432` |
| 同一宿主机上的数据库，且端口可从容器访问 | 宿主机的局域网 IP 和实际监听或映射端口，例如 `192.168.1.10`、`15432` |
| 另一台主机上的数据库 | 那台主机的可达 IP 或域名，以及数据库端口 |
| 另一个 Compose 项目中的数据库 | 先加入共同网络，再使用网络别名和容器端口，方法如下 |

在 QMediaSync 容器里，`127.0.0.1` 指容器自己。另一个 Compose 项目的 `postgres` 服务也不会自动出现在本项目网络中。

跨 Compose 项目共用数据库时，可以建立一个专用网络：

```bash
docker network create qms-db
```

如果这个网络已存在，直接复用。在**数据库原来的 Compose 文件**中，为 PostgreSQL 服务添加网络；保留原来的镜像、密码、端口和数据挂载，以及它已有的其他网络：

```yaml
services:
  postgres:
    # 原来的 image、environment、volumes 等配置保留在这里
    networks:
      default:
      database:
        aliases:
          - qms-postgres

networks:
  database:
    external: true
    name: qms-db
```

在 **QMediaSync 的 Compose 文件**中添加：

```yaml
services:
  qmediasync:
    # 方案 A 中的 image、ports、volumes 等配置保留在这里
    networks:
      - default
      - database

networks:
  database:
    external: true
    name: qms-db
```

这两段是合并到原文件的补充配置，不是完整的部署文件。分别在两个项目的目录执行 `docker compose up -d` 后，QMediaSync 中的数据库主机填 `qms-postgres`，端口填 `5432`。共同网络让两个项目通过别名互相访问，具体规则见 [Docker Compose 网络说明](https://docs.docker.com/compose/how-tos/networking/)。

给 QMediaSync 使用独立的数据库，不要填入其他应用正在使用的数据库名。数据库用户应能访问该数据库和向导测试使用的 `postgres` 数据库；需要让程序创建数据库时，还应有建库权限。

## 3. 启动并配置数据库

在保存 `compose.yaml` 的目录运行：

```bash
docker compose up -d
docker compose ps
docker compose logs --tail=100 -f qmediasync
```

按 `Ctrl+C` 只会退出日志查看，不会停止容器。

1. 在同一台主机访问 `http://127.0.0.1:12333`；从其他电脑访问时，把地址换成宿主机的局域网地址，例如 `http://192.168.1.10:12333`。首次配置请在可信的本机或局域网内完成。
2. 选择 SQLite，或按上面的表格填写 PostgreSQL。PostgreSQL 先点击“测试连接”，通过后再保存。
3. 如果提示已有数据库包含数据，保留旧数据应选择“取消”；选择“确定”会尝试删除并重建该数据库。全新安装使用独立的空数据库最容易核对。
4. 保存成功后，程序会退出。上面的重启策略会重新启动容器；稍等片刻再刷新页面，并查看新一轮日志。如果容器没有启动，再执行 `docker compose up -d`。

## 4. 创建管理员

1. 在日志中找到“检测到系统尚未创建管理员，请使用以下初始化码完成首次管理员创建”，复制后面的一次性初始化码。也可查看宿主机的 `config/logs/app.log`。
2. 刷新页面，在“创建管理员”表单中填写初始化码、用户名和密码。用户名为 3～20 个英文字母或数字；密码至少 6 个字符，不能是纯数字或纯字母。
3. 创建成功后，使用刚设置的用户名和密码登录。

初始化码只在本次启动期间有效，创建管理员后立即失效。创建前如果重启了程序，请从最新启动日志重新取码。日志已经出现初始化码时，无需为了取码再重启。已有管理员的实例不会再次生成初始化码。

登录后继续阅读 [快速开始](快速开始.md)。忘记密码或无法完成两步验证时，见 [管理员与密码恢复](管理员与密码恢复.md)。

## 路径和权限

| 位置 | 用途 | 是否需要保留 |
| --- | --- | --- |
| 宿主机 `./config` → 容器 `/app/config` | 配置、SQLite 数据库、日志、本机加密密钥和备份等 | 必须保留 |
| 宿主机 `./media` → 容器 `/media` | STRM、图片、NFO 等文件 | 按实际媒体目录保留 |
| 宿主机 `./postgres/data` → PostgreSQL 容器数据目录 | PostgreSQL 数据 | 使用 PostgreSQL 时必须保留 |

在 QMediaSync 页面选择本地目录时，填写容器内路径，例如 `/media/电影`。如果 Emby 也运行在容器中，还需给 Emby 挂载同一个宿主机目录，并在 Emby 中填写它自己能看到的路径。见 [文件权限相关问题](文件权限相关问题.md)。

需要以指定用户运行时，可在 `qmediasync` 服务中添加：

```yaml
environment:
  TZ: Asia/Shanghai
  GUID: "1000"
  GPID: "1000"
```

请使用实际的数字 UID 和 GID，可在宿主机终端执行 `id` 查看。`GUID` 和 `GPID` 可以分别设置；通常一起设置更方便核对身份和权限。未设置 `GUID` 时，主程序默认以 root 运行，单独设置 `GPID` 不会切换主程序用户。

入口脚本会修正 `/app/config` 的所有者，**不会替你修正 `/media` 的权限**。媒体目录应允许 QMediaSync 写入、允许 Emby 读取；不要求两个应用必须使用同一个用户，只要权限满足即可。不要使用 Compose 的 `user:` 绕过镜像入口所需的初始化权限。

## 端口和版本

| 容器端口 | 用途 |
| --- | --- |
| `12333` | QMediaSync 管理页面，HTTP |
| `12332` | QMediaSync 管理页面，HTTPS；同时存在 `config/server.crt` 和 `config/server.key` 时才监听 |
| `8095` | Emby 302 代理，HTTP；配置 Emby 后使用 |
| `8094` | Emby 302 代理，HTTPS；需要相应证书配置 |

示例只发布 `12333` 和 `8095`。如果不使用 Emby 代理，可以删去 `8095:8095`。使用其他端口时，修改冒号左侧的宿主机端口；使用内置 HTTPS 时还需发布相应端口。管理页面的域名和 HTTPS 配置见 [反向代理配置](反向代理配置.md)，Emby 播放入口见 [Emby 外网 302](Emby外网302.md)。

镜像标签 `latest` 对应最新正式发布，`v<版本号>` 用来固定发布版本，`beta` 为测试版本。升级和从旧项目迁移前先阅读 [更新与旧版迁移](更新与旧版迁移.md)，尤其不要直接用新镜像启动旧的内嵌 PostgreSQL 数据。

## 单独部署 PostgreSQL，供非容器部署连接

如果 QMediaSync 不是容器，而是 Linux 或 Windows 发布程序，可以把下面内容保存到**另一个目录**的 `compose.yaml`，单独运行 PostgreSQL：

```yaml
services:
  postgres:
    image: postgres:18
    restart: unless-stopped
    environment:
      POSTGRES_USER: qms
      POSTGRES_PASSWORD: "replace-with-a-long-random-password"
      POSTGRES_DB: qmediasync
    ports:
      - "15432:5432"
    volumes:
      - ./data:/var/lib/postgresql
```

修改密码后，在该目录执行 `docker compose up -d`，再用 `docker compose logs --tail=100 postgres` 确认数据库启动完成。QMediaSync 与 Docker 在同一主机时，主机填 `127.0.0.1`，端口填 `15432`；在其他主机时，填 Docker 宿主机的局域网 IP，端口仍为 `15432`。用户名、密码和数据库名与上例一致。

这个示例发布了数据库端口，应只允许需要使用它的主机连接，不要在路由器上把数据库端口转发到公网。数据库已有数据时，保留 `./data`，备份方式见 [备份与恢复](备份与恢复.md)。
