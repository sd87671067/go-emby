# Compose 配置

新安装仅需 `compose.yaml`，无需 `.env`。默认端口 8097、Bridge、Asia/Shanghai、root、GHCR latest；数据库和管理员密码分别随机生成并持久化。读取初始管理员密码：

```bash
docker compose exec go-emby cat /app/data/initial-admin-password
```

`.env` 为可选配置；已有 `.env` 继续生效，不能覆盖、清空或重新生成旧密码。bind mount 升级必须保留原 `APP_DATA_PATH`、`APP_BACKUP_PATH`、`POSTGRES_DATA_PATH` 和 `MEDIA_PATH`。若原 `.env` 未写路径，升级前补入原实际路径，否则新版默认 named volume 会指向其他存储。不要自动复制、搬迁或初始化替代数据库。详细核对步骤见 [README](README.md#已有用户升级)。

| 变量 | 默认值或用途 |
| --- | --- |
| `IMAGE_TAG` | `latest` |
| `HTTP_PORT` | `8097` |
| `TZ` | `Asia/Shanghai` |
| `PUID` / `PGID` | `0` / `0`；降权需自行配置媒体访问权限 |
| `POSTGRES_PASSWORD` | 新装留空随机生成；升级必须使用原数据库密码 |
| `ADMIN_PASSWORD` | 新装留空随机生成；仅空用户库首次创建管理员时生效 |
| `DATABASE_URL` | 默认连接 postgres:5432/emby，启动时对密码逐字节 URL 编码，兼容特殊字符与备份；可保留原自定义连接 |
| `APP_DATA_PATH` / `APP_BACKUP_PATH` | 默认 `app-data` / `app-backups` 卷；接受原 bind 路径 |
| `POSTGRES_DATA_PATH` | 默认 `postgres-data` 卷；接受原 bind 路径 |
| `MEDIA_PATH` | 默认 `media` 卷；支持宿主机目录 rw 挂载 |
| `MEDIA_ROOTS` | `/media`；多个容器内路径用冒号分隔，并保留对应挂载 |
| `POSTGRES_HOST_PORT` | `5433`，仅绑定宿主机回环供旧 Host override 使用 |
| `LICENSE_KEY` / `LICENSE_SERVER_URL` | 原授权配置继续使用；默认服务为 https://tl.macacaaca.top |
| `GO_EMBY_INSTANCE_ID` | 原值保留；为空时使用持久化实例标识 |
| `PUBLIC_URL` / `BOOTSTRAP_API_KEY` | 保留旧配置；默认空 |
| `XIAOYA_USERNAME` / `XIAOYA_PASSWORD` | 旧凭据兼容；新 UI 密码优先，用户名固定 dav，留空保留 |

旧自定义证书或额外挂载需保留原 override，精简模板不再声明 `LICENSE_CA_FILE` 和 secrets 目录。不需要 NanShare/Xiaoya URL 变量来解析播放源；本模板不会创建或修改任何第三方服务。

无 `.env` 安装的数据库密码在独立 bootstrap 卷中，管理员密码在 app-data 中；两个密码不同、文件权限 600。备份时保留应用、数据库、bootstrap 和备份卷，媒体卷或外部媒体目录也需按原策略备份。显式配置密码时通过 `.env` 保护并执行 `chmod 600 .env`，含 `$`、`#` 或空格使用单引号。
