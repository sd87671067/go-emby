# go-emby

Go 编写的 Emby 服务端，包含 Web 管理界面、文件浏览、媒体库扫描、NFO/海报读取和 Emby 兼容接口。播放采用跳转方式，客户端直连媒体源，不提供视频转码。

支持 **Linux x64 / amd64** 和 **ARM64 / aarch64**。

## Docker Compose 部署

服务器需要提前安装 **Docker Engine** 和 **Docker Compose v2**。

### 1. 创建部署目录

```bash
mkdir -p ~/go-emby && cd ~/go-emby
```

### 2. 下载配置文件

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

建议使用强密码。管理员初始密码至少 12 个字符。

然后根据服务器实际媒体目录修改 `compose.yaml` 中的媒体挂载，例如：

```yaml
- /vol1/1000/strm:/media:rw
```

左侧是服务器真实媒体目录，右侧是容器内目录。

如果增加多个容器内媒体目录，请同步修改 `.env` 中的 `MEDIA_ROOTS`。

### 4. 启动

```bash
docker compose pull && docker compose up -d
```

启动完成后访问：

```text
http://服务器IP:8097
```

查看容器状态：

```bash
docker compose ps
```

查看日志：

```bash
docker compose logs --tail=100 go-emby
```

> 不要执行 `docker compose down -v`，否则可能删除 PostgreSQL 数据。

## 更新

进入部署目录：

```bash
cd ~/go-emby
```

如仓库中的部署配置有更新，可重新下载模板并按需合并自己的配置；**不要直接覆盖已经填写好的 `.env`**。

更新程序镜像并重新创建容器：

```bash
docker compose pull && docker compose up -d
```

已有 `.env`、媒体目录、`app-data` 和 PostgreSQL 数据会继续保留。

更新后检查：

```bash
docker compose ps
docker compose logs --tail=100 go-emby
```

## 配置说明

完整环境变量说明：

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

当前提供免费模式，`LICENSE_KEY` 可按实际授权方式配置。主机机器标识会用于授权设备绑定，迁移服务器后可能需要重新授权。

## 交流群

TG 反馈交流群：

https://t.me/+mocElSRiXPM3NWQ1

## 赞助

项目目前为爱发电维护。赞助可以帮助加快更新和维护速度。

支付宝口令红包等赞助方式可通过 TG 私聊联系：

https://t.me/macaembychannel?direct
