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

服务器需要提前安装 Docker Engine 和 Docker Compose v2。

```bash
curl -fsSL https://raw.githubusercontent.com/sd87671067/go-emby/main/install.sh | bash
```

安装脚本会引导完成部署配置。

安装完成后访问：

```text
http://服务器IP:8097
```

请妥善保存管理员密码、数据库密码和授权信息。

## 部署方式二：Docker Compose

### 1. 创建部署目录

```bash
mkdir -p ~/go-emby && cd ~/go-emby
```

### 2. 下载 compose.yaml 和 .env.example

```bash
curl -fLO https://raw.githubusercontent.com/sd87671067/go-emby/main/compose.yaml
curl -fLO https://raw.githubusercontent.com/sd87671067/go-emby/main/.env.example
cp .env.example .env
```

### 3. 修改配置

编辑 `.env`，至少填写：

```env
POSTGRES_PASSWORD=你的数据库密码
ADMIN_PASSWORD=你的管理员密码
```

管理员初始密码至少 12 个字符。

根据服务器实际媒体目录修改 `compose.yaml` 中的媒体挂载，例如：

```yaml
- /vol1/1000/strm:/media:rw
```

左侧是服务器真实媒体目录，右侧是容器内目录。

如果增加多个容器内媒体目录，请同步修改 `.env` 中的 `MEDIA_ROOTS`。

### 4. 拉取镜像并启动

```bash
docker compose pull && docker compose up -d
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

> 不要执行 `docker compose down -v`，否则可能删除 PostgreSQL 数据卷。

## 更新

进入部署目录后执行：

```bash
docker compose pull && docker compose up -d
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
