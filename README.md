# go-emby

Go 编写的 Emby 兼容媒体服务器，提供 Web 管理界面和常用媒体库管理能力。播放采用跳转方式，客户端直连媒体源，不提供视频转码。

支持 **Linux x64 / amd64** 和 **ARM64 / aarch64**。

## 功能介绍

- Emby 兼容接口，可连接常用 Emby 客户端
- Web 媒体库、海报墙、影视详情与播放管理
- 媒体库全量扫描、增量刷新与目录管理
- NFO、本地海报及媒体元数据读取
- 媒体信息提取与管理
- TMDB 元数据与刮削管理
- 外挂字幕增强
- 用户、播放权限与设备数量管理
- 文件管理
- 实时日志与后台管理
- Docker Compose 部署
- 多架构镜像：amd64 / arm64
- 播放使用重定向/直连媒体源，不进行视频转码

## 部署方式一：一键部署

在 Debian/Ubuntu 服务器上以 root 运行；脚本会自动安装 Docker 和 Compose、生成随机数据库及管理员密码，并创建 `/opt/go-emby` 下的全部数据目录。普通用户需要已配置免密码 sudo。

```bash
curl -fsSL https://raw.githubusercontent.com/sd87671067/go-emby/main/install.sh | bash
```

全程无需交互。脚本自动完成密码生成、目录创建、权限设置、数据库初始化、服务启动和健康检查，并显示管理员密码。默认使用 Bridge 网络，Web 端口为 8097。

安装完成后访问：

```text
http://服务器IP:8097
```

使用账号 `admin` 和安装脚本显示的密码登录即可。数据库密码不会显示在终端，保存在权限为 `600` 的 `/opt/go-emby/.env` 中。脚本已创建可读写的 `/opt/go-emby/media` 并挂载到容器的 `/media`；以后可在 Web 中添加 `/media` 作为媒体库。

默认目录结构：

```text
/opt/go-emby/
├── compose.yaml
├── compose.host.yaml
├── .env
├── app-data/
├── app-backups/
├── postgres-data/
├── media/
└── secrets/
```

重复运行安装命令会保留 `.env`、密码及所有数据目录，仅更新 Compose 模板和镜像。现有数据库会使用 `.env` 中的密码执行 `SELECT 1` 验证；验证失败立即停止，不会重建数据库。旧版若使用 `go-emby_postgres-data` 数据卷，脚本会先停止服务并迁移到 `./postgres-data`，旧卷仍保留。

如果应用容器创建或启动失败，脚本会显示 Docker 错误及已有应用容器的状态和日志，并保留 `.env`、`postgres-data` 等数据。修复端口占用或挂载目录等问题后，直接重新运行同一安装命令即可。

## 部署方式二：Docker Compose

### 1. 创建部署目录

```bash
mkdir -p ~/go-emby && cd ~/go-emby
```

### 2. 下载部署文件

```bash
curl -fLO https://raw.githubusercontent.com/sd87671067/go-emby/main/compose.yaml
curl -fLO https://raw.githubusercontent.com/sd87671067/go-emby/main/.env.example
cp .env.example .env
chmod 600 .env
mkdir -p app-data app-backups postgres-data media secrets
```

### 3. 修改配置

编辑 `.env`，只需填写以下三项（`MEDIA_PATH=./media` 已是默认值）：

```env
POSTGRES_PASSWORD=你的数据库密码
ADMIN_PASSWORD=你的管理员密码
MEDIA_PATH=./media
```

管理员初始密码须为 12 到 72 字节。一键脚本生成 48 个 ASCII 字符的随机密码。

`MEDIA_PATH` 是宿主机路径，也可设为 `/vol1/1000/Emby` 或 `/mnt/media`，该目录应已存在；不必修改 `compose.yaml`。容器内统一挂载为可读写的 `/media`，`MEDIA_ROOTS=/media` 无需随宿主机路径修改。可选配置有 `HTTP_PORT`、`NETWORK_MODE`、`PUID`、`PGID` 和 `POSTGRES_HOST_PORT`。go-emby 默认使用 `PUID=0`、`PGID=0`；不会递归修改外部媒体库的权限。

`APP_DATA_PATH=./app-data`、`APP_BACKUP_PATH=./app-backups` 和 `POSTGRES_DATA_PATH=./postgres-data` 默认都位于当前 Compose 项目目录。手动部署首次启动前，先从官方镜像查询 PostgreSQL UID/GID 并设置数据库目录权限：

```bash
docker pull postgres:17-bookworm
PG_UID=$(docker run --rm --entrypoint id postgres:17-bookworm -u postgres)
PG_GID=$(docker run --rm --entrypoint id postgres:17-bookworm -g postgres)
sudo chown "$PG_UID:$PG_GID" postgres-data
sudo chmod 700 postgres-data
```

若曾使用 `go-emby_postgres-data` named volume，请先运行一键脚本完成安全迁移，再按需切回手动维护；不要直接启动空的 `./postgres-data`。

如果增加多个容器内媒体目录，请同步修改 `.env` 中的 `MEDIA_ROOTS`。

PostgreSQL 目录权限以上述官方镜像实际 UID/GID 为准；已有数据库文件也必须可由该 UID/GID 读取。不要使用 `chmod 777`。应用数据、备份和 `secrets` 目录使用当前用户可写的权限即可。

### 4. 拉取镜像并启动

```bash
docker compose pull
docker compose up -d
```

启动完成后访问：

```text
http://服务器IP:8097
```

查看状态：

```bash
docker compose ps
```

查看日志：

```bash
docker compose logs --tail=100 go-emby
```

默认 Bridge 网络（推荐）。如需 Host 网络，先下载覆盖文件，将 `.env` 中的 `NETWORK_MODE` 设为 `host`，然后执行：

```bash
curl -fLO https://raw.githubusercontent.com/sd87671067/go-emby/main/compose.host.yaml
docker compose -f compose.yaml -f compose.host.yaml config
docker compose -f compose.yaml -f compose.host.yaml pull
docker compose -f compose.yaml -f compose.host.yaml up -d
```

`NETWORK_MODE=host` 仅记录所选模式；Host 模式必须在每次 Compose 命令中带上 `-f compose.host.yaml`。Host 覆盖文件使用 `!reset` 清除应用端口映射，需要 Docker Compose 2.24.4 或更新版本。PostgreSQL 仍在 Bridge 网络中，供应用使用的数据库端口仅绑定 `127.0.0.1`。

默认目录结构如下。所有数据都在 `go-emby/` 内，方便整体备份和迁移：

```text
go-emby/
├── compose.yaml
├── compose.host.yaml  # 仅 Host 模式下载
├── .env
├── app-data/
├── app-backups/
├── postgres-data/
├── media/
└── secrets/
```

备份前建议先执行 `docker compose down`（Host 模式使用相同的两个 `-f` 参数），再备份整个 `go-emby/` 目录。恢复时还原整个目录，再执行 `docker compose up -d`（Host 模式仍使用两个 `-f` 参数）。不要使用 `docker compose down -v`。

## 更新

进入部署目录后，Bridge 模式执行：

```bash
docker compose pull
docker compose up -d
```

Host 模式执行：

```bash
docker compose -f compose.yaml -f compose.host.yaml pull
docker compose -f compose.yaml -f compose.host.yaml up -d
```

更新只拉取新镜像并重新创建容器，已有 `.env`、媒体目录、`app-data` 和 PostgreSQL 数据会继续保留。

如果仓库中的 `compose.yaml` 或 `.env.example` 有更新，可以重新下载模板后手动合并配置。**不要直接用 `.env.example` 覆盖已经配置好的 `.env`。**

## 配置说明

完整环境变量和配置说明：

[ENV_GUIDE.md](ENV_GUIDE.md)

常用配置：

| 配置 | 说明 | 默认值 |
| --- | --- | --- |
| `HTTP_PORT` | Web 访问端口 | `8097` |
| `IMAGE_TAG` | Docker 镜像标签 | `latest` |
| `PUID` / `PGID` | 容器运行 UID/GID | `0/0` |
| `MEDIA_PATH` | 宿主机媒体目录，必须事先存在 | `./media` |
| `APP_DATA_PATH` / `APP_BACKUP_PATH` | 宿主机应用数据与备份目录 | `./app-data` / `./app-backups` |
| `POSTGRES_DATA_PATH` | PostgreSQL 数据目录 | `./postgres-data` |
| `MEDIA_ROOTS` | 容器内允许访问的媒体根目录 | `/media` |
| `LICENSE_KEY` | 授权码，免费模式可留空 | 空 |

## 授权模式

本项目保留授权校验。

默认授权服务地址：

```text
https://tl.macacaaca.top
```

当前提供免费模式，`LICENSE_KEY` 可按实际授权方式配置；授权相关配置请参考 [ENV_GUIDE.md](ENV_GUIDE.md)。

主机机器标识会用于授权设备绑定，迁移服务器后可能需要重新授权。

## 交流群

TG 反馈交流群：

https://t.me/+mocElSRiXPM3NWQ1

## 赞助

项目目前为爱发电维护。赞助可以帮助加快更新和维护速度。

支付宝口令红包等赞助方式可通过 TG 私聊联系：

https://t.me/macaembychannel?direct
