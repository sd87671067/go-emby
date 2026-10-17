# 发布验证

## 2026-10-17 Compose 精简验证

- 单文件 Compose 为 114 行，服务声明顺序为 go-emby、postgres、schema-prepare，无 data-init。
- 当前环境隔离项目验证无 `.env` 新装、独立随机密码与文件 600、Schema 失败阻止应用启动、Schema 成功后 Healthy。
- 隔离 PostgreSQL 重建容器后 system_identifier、管理员及 settings 保留；应用与 PG 独立卷。
- 原 Compose + 原 `.env` + bind mount 安装升级到新模板后，用户 ID/密码哈希、settings、数据库标识、挂载源及机器授权标识不变；没有迁移或重置。
- 原数据库缺少原密码且无 bootstrap 密码时明确停止，不生成替代凭据。
- 私库应用定向验证旧目录属主/模式、只读挂载、NFO/海报/背景/字幕/STRM 原子写入及继承 ACL；Xiaoya 密码加密持久化、不回显、旧环境变量及 guestpass、增量/补回/来源切换/取消恢复。
- 使用本地 ARM64 运行环境；未执行全量/race、生产全库扫描，未发布 GHCR。公共仓库仅更新部署配置与文档，未上传或更新应用源码。

## 2026-09-13 多架构更新

- Linux amd64 和 arm64 原生 Docker 多阶段构建成功，退出码 0。
- 独立 PostgreSQL 17 数据库测试：86 项通过，0 项失败，1 项跳过，退出码 0（不计子测试）。
- 大规模压力测试 `TestPostgresScale400K` 默认跳过，需要设置 `SCALE_TEST=1`。
- 前端静态资源重新编译并嵌入 Go 二进制。
- Compose 配置解析通过；默认自动选择主机架构。
- 提交文件检查与生产凭据、地址交叉检查通过。
- 两种架构二进制从最终运行镜像导出，随 Release 提供 SHA256 校验。

授权依赖外部 HTTPS 服务。此验证不表示所有第三方客户端及媒体源均已逐一测试。
