# Go-Emby

Go Emby是用go语言编写的 Emby 兼容媒体服务器，提供 Web 管理界面和常用媒体库管理能力。播放采用跳转方式，客户端直连媒体源，不提供视频转码。

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

## Docker Compose 部署

新安装只需一个 `compose.yaml`，不强制 `.env`：

```bash
mkdir -p ~/go-emby && cd ~/go-emby
curl -fLO https://raw.githubusercontent.com/sd87671067/go-emby/main/compose.yaml
docker compose up -d
```

默认 GHCR `latest`、Bridge 网络、8097 端口、Asia/Shanghai、应用 root（0:0）。启动顺序为 PostgreSQL Healthy → `schema-prepare --prepare-schema` 成功 → go-emby。Schema 失败时应用不会启动，没有 `data-init` 或自动数据搬迁。

访问 `http://服务器IP:8097`，用户名 `admin`。首次安装随机生成独立的数据库和管理员密码，没有公开固定密码；管理员密码通过以下命令读取，请妥善保管：

```bash
docker compose exec go-emby cat /app/data/initial-admin-password
```

已有 `ADMIN_PASSWORD` 仍优先使用，且仅在空用户库首次初始化时生效，不会重置旧账号密码。数据库密码保存在独立的 `bootstrap` 卷中，仅以只读方式提供给应用和 Schema 服务；应用数据与 PostgreSQL 使用独立持久化卷。不要删除这些卷。媒体默认使用可读写的 `media` 卷；挂载宿主机媒体可设置 `.env` 中的 `MEDIA_PATH=/你的媒体目录`。额外媒体路径需要保留原挂载并同步配置 `MEDIA_ROOTS`。

## 已有用户升级

保留原项目名称、部署目录、`.env`、Compose overrides、原密码、卷及所有数据。先用 `docker inspect` 核对当前容器的 Mounts，再用 `docker compose config` 对比新配置。**原来使用 bind mount 的用户必须保留原路径变量**，例如：

```dotenv
APP_DATA_PATH=./app-data
APP_BACKUP_PATH=./app-backups
POSTGRES_DATA_PATH=./postgres-data
MEDIA_PATH=./media
```

相对路径仍相对于原 Compose 目录；也可填写原目录的绝对路径。旧 `.env` 已有这些变量时直接继续使用。原来使用 named volume 的用户应保持相同项目名和卷名（默认 `go-emby_app-data`、`go-emby_postgres-data` 等）；自定义卷可在 override 中指明原 external volume。没有设置路径的新版默认使用 named volume，不能替换为一个空卷后启动旧安装。

保留原 `POSTGRES_PASSWORD`（包括特殊字符，启动时逐字节 URL 编码）及 `DATABASE_URL`、`LICENSE_KEY`、`LICENSE_SERVER_URL`、`GO_EMBY_INSTANCE_ID`。旧数据库若没有原密码也没有 bootstrap 密码文件会明确停止，不生成替代密码、不重置数据库。默认保留机器标识挂载以维持授权绑定；自定义授权证书不再内置，需要该配置的旧部署应保留自己的环境变量及只读证书挂载 override。

核对挂载后执行：

```bash
docker compose pull
docker compose up -d
docker compose ps -a
```

这只更新服务，不搬迁原数据库和文件；Schema 准备保留现有数据并幂等执行。旧 Host 网络继续使用原 `compose.host.yaml`，每次命令带上 `-f compose.yaml -f compose.host.yaml`。不要用旧安装脚本进行此次升级；该脚本存在历史数据搬迁逻辑，本次模板不调用它。

## 媒体写入与 Xiaoya

应用默认 root，并在 `cap_drop: ALL` 下仅补回 `DAC_OVERRIDE` 和 `FOWNER`，覆盖旧目录属主与权限造成的本地写入问题；不递归 chmod/chown，不修改其他容器。主动设置 `PUID/PGID` 的旧用户仍可降权，但需让该用户获得目标目录访问权限。NFO、海报、背景、字幕及 STRM 采用受限目录内原子发布；保留既有用户文件及来源所有权检查。

root 无法绕过容器/宿主机 `ro`、NFS `root_squash`、CIFS/FUSE 服务端权限、NAS ACL 或用户命名空间限制。失败提示保留 errno 并说明检查方向，不对这些限制自动 chmod。可用 `findmnt -T 媒体路径`、`namei -l 媒体路径`、`getfacl 媒体路径` 检查；同时检查容器 Mounts 的 RW、Capabilities 和宿主机映射。

新版应用的 Xiaoya 设置在“启用 STRM 同步”下提供密码框，新保存的密码固定使用 `dav`；密码加密持久化，不回显，留空保留。未保存新密码时继续兼容 `XIAOYA_USERNAME` / `XIAOYA_PASSWORD` 和源目录 `guestpass.txt`。同步的增量、缺失补回、来源隔离与暂停恢复行为保持不变。此文档和 Compose 更新不发布 GHCR；这些应用修复需使用包含相应私库提交的镜像。

## 配置说明

完整环境变量和配置说明：

[ENV_GUIDE.md](ENV_GUIDE.md)

常用配置：

| 配置 | 说明 | 默认值 |
| --- | --- | --- |
| `HTTP_PORT` | Web 访问端口 | `8097` |
| `IMAGE_TAG` | Docker 镜像标签 | `latest` |
| `PUID` / `PGID` | 容器运行 UID/GID | `0/0` |
| `MEDIA_PATH` | 宿主机媒体目录或卷 | `media` 卷 |
| `APP_DATA_PATH` / `APP_BACKUP_PATH` | 应用数据与备份目录或卷 | `app-data` / `app-backups` 卷 |
| `POSTGRES_DATA_PATH` | PostgreSQL 数据目录或卷 | `postgres-data` 卷 |
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
