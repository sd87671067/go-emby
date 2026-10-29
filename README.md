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

只需一个 `compose.yaml` 即可安装，无需编写 `.env`：

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

需要自行指定首次管理员密码时，可使用可选的 `.env` 设置 `ADMIN_PASSWORD`；该值仅在空用户库首次初始化时生效，不会重置已有账号密码。数据库密码保存在独立的 `bootstrap` 卷中，仅以只读方式提供给应用和 Schema 服务；应用数据与 PostgreSQL 使用独立持久化卷。不要删除这些卷。媒体默认使用可读写的 `media` 卷。要读取宿主机已有的影视文件，可直接将 `compose.yaml` 中的 `- ${MEDIA_PATH:-media}:/media:rw` 改成 `- /你的影视目录:/media:rw`；如有多个媒体目录，再添加挂载并配置 `MEDIA_ROOTS`。


## 配置说明

进阶用户可选用 `.env` 自定义配置；不使用 `.env` 也可以正常安装。完整环境变量说明：

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
