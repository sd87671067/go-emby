#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

REPO=sd87671067/go-emby
BRANCH=main
INSTALL_DIR=/opt/go-emby
BASE_URL="https://raw.githubusercontent.com/${REPO}/${BRANCH}"
POSTGRES_IMAGE=postgres:17-bookworm

if (( EUID != 0 )); then
    if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
        curl -fsSL "${BASE_URL}/install.sh" | sudo -n bash
        exit $?
    fi
    echo '需要 root 权限；请用 root 运行，或配置免密码 sudo 后重试。' >&2
    exit 1
fi

export DEBIAN_FRONTEND=noninteractive
for command_name in curl openssl; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        apt-get update
        apt-get install -y curl ca-certificates openssl
        break
    fi
done
if ! command -v docker >/dev/null 2>&1; then
    curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker >/dev/null 2>&1 || true
if ! docker compose version >/dev/null 2>&1; then
    apt-get update
    apt-get install -y docker-compose-plugin
fi

tmp_dir=$(mktemp -d)
migration_pending=false
old_compose=false
old_host_compose=false
old_env=false
deployment_started=false
db_dir_owner=
db_dir_mode=
rollback() {
    local exit_code=$?
    trap - EXIT
    if [[ "$migration_pending" == true ]]; then
        echo '迁移或启动失败，正在恢复原部署；旧 named volume 保持不变。' >&2
        docker compose --env-file .env -f compose.yaml stop go-emby postgres >/dev/null 2>&1 || true
        local postgres_container
        postgres_container=$(docker compose --env-file .env -f compose.yaml ps -q postgres 2>/dev/null || true)
        if [[ -n "$postgres_container" && $(docker inspect -f '{{.State.Running}}' "$postgres_container" 2>/dev/null || true) == true ]]; then
            echo 'PostgreSQL 仍在运行，未清理复制目录；请先手动停止容器。' >&2
            rm -rf "$tmp_dir"
            exit "$exit_code"
        fi
        if [[ "$old_compose" == true ]]; then
            cp -a "$tmp_dir/compose.yaml.old" compose.yaml
        fi
        if [[ "$old_host_compose" == true ]]; then
            cp -a "$tmp_dir/compose.host.yaml.old" compose.host.yaml
        fi
        if [[ "$old_env" == true ]]; then
            cp -a "$tmp_dir/.env.old" .env
        fi
        # 迁移前已确认该目录为空，故这里只清理本次复制的内容。
        find "$INSTALL_DIR/postgres-data" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
        if [[ -n "$db_dir_owner" && -n "$db_dir_mode" ]]; then
            chown "$db_dir_owner" "$INSTALL_DIR/postgres-data"
            chmod "$db_dir_mode" "$INSTALL_DIR/postgres-data"
        fi
        if [[ "$old_compose" == true ]]; then
            docker compose --env-file .env -f compose.yaml up -d >/dev/null 2>&1 || \
                echo '旧配置已恢复，但旧服务未能自动启动；请检查 docker compose logs。' >&2
        fi
        echo '安装已停止。原数据库仍在 go-emby_postgres-data。' >&2
    elif (( exit_code != 0 )) && [[ "$deployment_started" == false ]]; then
        if [[ "$old_compose" == true ]]; then
            cp -a "$tmp_dir/compose.yaml.old" compose.yaml
        fi
        if [[ "$old_host_compose" == true ]]; then
            cp -a "$tmp_dir/compose.host.yaml.old" compose.host.yaml
        fi
        if [[ "$old_env" == true ]]; then
            cp -a "$tmp_dir/.env.old" .env
        fi
    fi
    rm -rf "$tmp_dir"
    exit "$exit_code"
}
trap rollback EXIT

mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"
if [[ -f compose.yaml ]]; then
    cp -a compose.yaml "$tmp_dir/compose.yaml.old"
    old_compose=true
fi
if [[ -f compose.host.yaml ]]; then
    cp -a compose.host.yaml "$tmp_dir/compose.host.yaml.old"
    old_host_compose=true
fi
if [[ -f .env ]]; then
    cp -a .env "$tmp_dir/.env.old"
    old_env=true
fi
for filename in compose.yaml compose.host.yaml; do
    curl -fsSL --retry 3 "${BASE_URL}/${filename}" -o "$tmp_dir/${filename}"
done
cp "$tmp_dir/compose.yaml" compose.yaml
cp "$tmp_dir/compose.host.yaml" compose.host.yaml

if [[ ! -f .env ]]; then
    curl -fsSL --retry 3 "${BASE_URL}/.env.example" -o .env
    new_install=true
else
    new_install=false
fi
chmod 600 .env
set_env() {
    local key=$1 value=$2
    if grep -q "^${key}=" .env; then
        sed -i "s|^${key}=.*|${key}=${value}|" .env
    else
        printf '%s=%s\n' "$key" "$value" >> .env
    fi
}
ensure_env() {
    if ! grep -q "^${1}=" .env; then
        set_env "$1" "$2"
    fi
}
get_env() {
    sed -n "s/^${1}=//p" .env | tail -n 1 | sed -e "s/^'//" -e "s/'$//" -e 's/^"//' -e 's/"$//'
}
if [[ "$new_install" == true ]]; then
    set_env POSTGRES_PASSWORD "$(openssl rand -hex 24)"
    set_env ADMIN_PASSWORD "$(openssl rand -hex 24)"
fi
ensure_env APP_DATA_PATH ./app-data
ensure_env APP_BACKUP_PATH ./app-backups
set_env POSTGRES_DATA_PATH ./postgres-data
ensure_env MEDIA_PATH ./media
set_env NETWORK_MODE bridge
ensure_env HTTP_PORT 8097
chmod 600 .env
POSTGRES_PASSWORD=$(get_env POSTGRES_PASSWORD)
ADMIN_PASSWORD=$(get_env ADMIN_PASSWORD)
HTTP_PORT=$(get_env HTTP_PORT)
if [[ -z "$POSTGRES_PASSWORD" || ${#ADMIN_PASSWORD} -lt 12 ]]; then
    echo 'POSTGRES_PASSWORD 不能为空，ADMIN_PASSWORD 至少 12 个字符。' >&2
    exit 1
fi
mkdir -p app-data app-backups postgres-data media secrets
chmod 700 secrets

docker compose --env-file .env -f compose.yaml config --quiet
docker pull "$POSTGRES_IMAGE"
postgres_uid=$(docker run --rm --entrypoint id "$POSTGRES_IMAGE" -u postgres)
postgres_gid=$(docker run --rm --entrypoint id "$POSTGRES_IMAGE" -g postgres)
if [[ ! "$postgres_uid" =~ ^[0-9]+$ || ! "$postgres_gid" =~ ^[0-9]+$ ]]; then
    echo '无法从 postgres:17-bookworm 查询 postgres 用户的 UID/GID。' >&2
    exit 1
fi

db_dir="$INSTALL_DIR/postgres-data"
if [[ -f "$db_dir/PG_VERSION" ]]; then
    echo '检测到现有 ./postgres-data，继续使用。'
elif [[ -n "$(find "$db_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo 'postgres-data 非空，但缺少 PG_VERSION；为避免覆盖数据，安装停止。' >&2
    exit 1
elif docker volume inspect go-emby_postgres-data >/dev/null 2>&1; then
    if ! docker run --rm --entrypoint sh -v go-emby_postgres-data:/source:ro \
        "$POSTGRES_IMAGE" -c 'test -f /source/PG_VERSION && test -d /source/base && test -d /source/global'; then
        echo '旧 named volume 存在，但数据库结构不完整；安装停止，避免初始化空数据库。' >&2
        exit 1
    fi
    echo '检测到旧 named volume 数据，开始迁移。'
    db_dir_owner=$(stat -c '%u:%g' "$db_dir")
    db_dir_mode=$(stat -c '%a' "$db_dir")
    migration_pending=true
    if [[ "$old_compose" == true ]]; then
        docker compose --project-directory "$INSTALL_DIR" --env-file .env \
            -f "$tmp_dir/compose.yaml.old" stop go-emby postgres
    else
        echo '存在旧数据库卷，但找不到原 compose.yaml；无法确认数据库已停止。' >&2
        exit 1
    fi
    # 再次确认目标为空；迁移期间任何失败都会回滚。
    if [[ -n "$(find "$db_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
        echo '目标目录已非空，迁移停止。' >&2
        exit 1
    fi
    docker run --rm --entrypoint sh \
        -v go-emby_postgres-data:/source:ro \
        -v "$db_dir:/target" "$POSTGRES_IMAGE" \
        -c 'cp -a /source/. /target/'
    if [[ ! -f "$db_dir/PG_VERSION" ]]; then
        echo '复制完成后未找到 PG_VERSION。' >&2
        exit 1
    fi
    echo '旧数据库已复制到 ./postgres-data；旧卷保留作为回滚备份。'
fi
# 只调整 PostgreSQL 目录本身；数据库文件保持原来的 owner/permission。
chown "${postgres_uid}:${postgres_gid}" "$db_dir"
chmod 700 "$db_dir"

docker compose --env-file .env -f compose.yaml pull
deployment_started=true
docker compose --env-file .env -f compose.yaml up -d postgres
postgres_id=$(docker compose --env-file .env -f compose.yaml ps -q postgres)
if [[ -z "$postgres_id" ]]; then
    echo 'PostgreSQL 容器未创建。' >&2
    exit 1
fi
healthy=false
for ((i=0; i<60; i++)); do
    if [[ $(docker inspect -f '{{.State.Health.Status}}' "$postgres_id" 2>/dev/null || true) == healthy ]]; then
        healthy=true
        break
    fi
    sleep 2
done
if [[ "$healthy" != true ]]; then
    echo 'PostgreSQL 健康检查失败。' >&2
    exit 1
fi
if [[ $(docker compose --env-file .env -f compose.yaml exec -T \
    -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres \
    psql -h 127.0.0.1 -U emby -d emby -Atqc 'SELECT 1') != 1 ]]; then
    echo 'PostgreSQL SELECT 1 验证失败，请检查现有数据库密码。' >&2
    exit 1
fi
docker compose --env-file .env -f compose.yaml up -d --remove-orphans go-emby
ready=false
for ((i=0; i<60; i++)); do
    if curl -fsS --max-time 2 "http://127.0.0.1:${HTTP_PORT}/health" >/dev/null 2>&1; then
        ready=true
        break
    fi
    sleep 2
done
if [[ "$ready" != true ]]; then
    echo 'go-emby 健康检查失败。' >&2
    docker compose --env-file .env -f compose.yaml logs --tail=50 go-emby >&2 || true
    exit 1
fi
migration_pending=false
public_ip=$(curl -4 -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)
if [[ -z "$public_ip" ]]; then
    public_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
fi
printf '\n安装完成\n访问地址：http://%s:%s\n管理员账号：admin\n管理员密码：%s\n安装目录：%s\n' \
    "${public_ip:-服务器IP}" "$HTTP_PORT" "$ADMIN_PASSWORD" "$INSTALL_DIR"
