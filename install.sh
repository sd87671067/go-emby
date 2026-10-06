#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

REPO=sd87671067/go-emby
BRANCH=main
INSTALL_DIR=/opt/go-emby
BASE_URL="https://raw.githubusercontent.com/${REPO}/${BRANCH}"
POSTGRES_IMAGE=postgres:17-bookworm

progress_step=0
progress_total=11
progress() {
    progress_step=$((progress_step + 1))
    printf '\n[%02d/%02d] %s\n' "$progress_step" "$progress_total" "$1"
}
progress_note() {
    printf '  -> %s\n' "$1"
}

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
failure_stage='安装准备'
rollback() {
    local exit_code=$?
    trap - EXIT
    if (( exit_code != 0 )); then
        echo "ERROR: ${failure_stage} failed (exit code ${exit_code})." >&2
        if [[ "$deployment_started" == true ]]; then
            local app_container
            app_container=$(docker compose --env-file .env "${compose_files[@]}" ps -a -q go-emby </dev/null 2>/dev/null || true)
            if [[ -n "$app_container" ]]; then
                docker inspect -f 'go-emby state={{.State.Status}} exit={{.State.ExitCode}} restarts={{.RestartCount}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$app_container" >&2 || true
                docker compose --env-file .env "${compose_files[@]}" logs --tail=100 go-emby </dev/null >&2 || true
            fi
        fi
    fi
    if [[ "$migration_pending" == true ]]; then
        echo '迁移或启动失败，正在恢复原部署；旧 named volume 保持不变。' >&2
        docker compose --env-file .env "${compose_files[@]}" stop go-emby postgres >/dev/null 2>&1 || true
        local postgres_container
        postgres_container=$(docker compose --env-file .env "${compose_files[@]}" ps -q postgres 2>/dev/null || true)
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
        # 保留复制出的文件供检查；安装脚本绝不删除数据库目录或其中的数据。
        echo 'postgres-data 中已复制的文件仍保留；排查后再重试迁移。' >&2
        if [[ "$old_compose" == true ]]; then
            docker compose --env-file .env "${compose_files[@]}" up -d >/dev/null 2>&1 || \
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
run_quiet() {
    if "$@" </dev/null >"$tmp_dir/command.log" 2>&1; then
        return 0
    fi
    cat "$tmp_dir/command.log" >&2
    return 1
}

progress '准备安装目录和部署文件'
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
progress_note '下载最新 compose.yaml / compose.host.yaml'
for filename in compose.yaml compose.host.yaml; do
    curl -fL --retry 3 --connect-timeout 10 --progress-bar "${BASE_URL}/${filename}" -o "$tmp_dir/${filename}"
done
cp "$tmp_dir/compose.yaml" compose.yaml
cp "$tmp_dir/compose.host.yaml" compose.host.yaml

if [[ ! -f .env ]]; then
    progress_note '首次安装：下载 .env.example 并生成随机密码'
    curl -fL --retry 3 --connect-timeout 10 --progress-bar "${BASE_URL}/.env.example" -o .env
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
get_env() {
    sed -n "s/^${1}=//p" .env | tail -n 1 | sed -e "s/^'//" -e "s/'$//" -e 's/^"//' -e 's/"$//'
}
if [[ "$new_install" == true ]]; then
    set_env POSTGRES_PASSWORD "$(openssl rand -hex 24)"
    set_env ADMIN_PASSWORD "$(openssl rand -hex 24)"
fi
# 已有 .env 原样保留；缺省目录及端口由 Compose 模板提供。
chmod 600 .env
POSTGRES_PASSWORD=$(get_env POSTGRES_PASSWORD)
ADMIN_PASSWORD=$(get_env ADMIN_PASSWORD)
HTTP_PORT=$(get_env HTTP_PORT)
HTTP_PORT=${HTTP_PORT:-8097}
NETWORK_MODE=$(get_env NETWORK_MODE)
POSTGRES_DATA_PATH=$(get_env POSTGRES_DATA_PATH)
progress '检查环境变量和媒体目录'
admin_password_bytes=$(printf '%s' "$ADMIN_PASSWORD" | wc -c)
if [[ -z "$POSTGRES_PASSWORD" || $admin_password_bytes -lt 12 ]]; then
    echo 'POSTGRES_PASSWORD 不能为空，ADMIN_PASSWORD 至少 12 字节。' >&2
    exit 1
fi
if (( admin_password_bytes > 72 )); then
    echo 'ADMIN_PASSWORD 不能超过 72 字节。' >&2
    exit 1
fi
if [[ -n "$POSTGRES_DATA_PATH" && "$POSTGRES_DATA_PATH" != ./postgres-data ]]; then
    echo '一键部署只能管理 ./postgres-data；现有 POSTGRES_DATA_PATH 不同，安装停止以保护数据库。' >&2
    exit 1
fi
compose_files=(-f compose.yaml)
if [[ "$NETWORK_MODE" == host ]]; then
    compose_files+=(-f compose.host.yaml)
    HTTP_PORT=8097
elif [[ -n "$NETWORK_MODE" && "$NETWORK_MODE" != bridge ]]; then
    echo 'NETWORK_MODE 必须为 bridge 或 host。' >&2
    exit 1
fi
mkdir -p app-data app-backups postgres-data media secrets
MEDIA_PATH=$(get_env MEDIA_PATH)
if [[ -n "$MEDIA_PATH" && ! -d "$MEDIA_PATH" ]]; then
    echo "ERROR: media bind mount invalid: ${MEDIA_PATH} 目录不存在。" >&2
    exit 1
fi

progress '校验 Docker Compose 配置'
failure_stage='Compose 配置解析'
docker compose --env-file .env "${compose_files[@]}" config --quiet
progress_note 'Compose 配置有效'

progress '拉取 PostgreSQL 镜像'
failure_stage='PostgreSQL 镜像拉取'
docker pull "$POSTGRES_IMAGE"
progress '检查 PostgreSQL 数据目录和权限'
failure_stage='PostgreSQL UID/GID 查询'
postgres_uid=$(docker run --rm --entrypoint id "$POSTGRES_IMAGE" -u postgres </dev/null)
postgres_gid=$(docker run --rm --entrypoint id "$POSTGRES_IMAGE" -g postgres </dev/null)
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
    migration_pending=true
    if [[ "$old_compose" == true ]]; then
        old_compose_files=(-f "$tmp_dir/compose.yaml.old")
        if [[ "$NETWORK_MODE" == host && "$old_host_compose" == true ]]; then
            old_compose_files+=(-f "$tmp_dir/compose.host.yaml.old")
        fi
        docker compose --project-directory "$INSTALL_DIR" --env-file .env \
            "${old_compose_files[@]}" stop go-emby postgres
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
failure_stage='postgres-data 目录权限设置'
chown "${postgres_uid}:${postgres_gid}" "$db_dir"
chmod 700 "$db_dir"
if [[ -f "$db_dir/PG_VERSION" ]]; then
    failure_stage='postgres-data 文件权限检查'
    if ! docker run --rm --user "${postgres_uid}:${postgres_gid}" \
        --entrypoint sh -v "$db_dir:/data:ro" "$POSTGRES_IMAGE" \
        -c 'test -r /data/PG_VERSION && test -x /data/base && test -x /data/global' </dev/null; then
        echo 'ERROR: postgres-data 权限错误：PostgreSQL 用户无法读取 PG_VERSION 或访问 base/global；请检查数据库文件属主和权限。' >&2
        exit 1
    fi
fi

progress '拉取 go-emby 最新镜像'
failure_stage='go-emby image pull'
docker compose --env-file .env "${compose_files[@]}" pull go-emby

progress '启动 PostgreSQL'
deployment_started=true
failure_stage='PostgreSQL container create'
docker compose --env-file .env "${compose_files[@]}" up -d postgres
failure_stage='PostgreSQL health check'
postgres_id=$(docker compose --env-file .env "${compose_files[@]}" ps -a -q postgres </dev/null)
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
    if (( i % 5 == 0 )); then
        progress_note "等待 PostgreSQL healthy... $((i * 2))s"
    fi
    sleep 2
done
if [[ "$healthy" != true ]]; then
    echo 'PostgreSQL 健康检查失败；请检查 postgres-data 权限和以下容器日志。' >&2
    docker compose --env-file .env "${compose_files[@]}" logs --tail=50 postgres </dev/null >&2 || true
    exit 1
fi
progress_note 'PostgreSQL healthy'
failure_stage='PostgreSQL SELECT 1'
if [[ $(docker compose --env-file .env "${compose_files[@]}" exec -T \
    -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres \
    psql -h 127.0.0.1 -U emby -d emby -Atqc 'SELECT 1' </dev/null) != 1 ]]; then
    echo 'PostgreSQL SELECT 1 验证失败，请检查现有数据库密码。' >&2
    exit 1
fi
progress '准备/升级数据库结构'
failure_stage='go-emby schema prepare'
docker compose --env-file .env "${compose_files[@]}" run --rm --no-deps schema-prepare
progress_note '数据库结构准备完成'

progress '启动 go-emby'
failure_stage='go-emby container create'
docker compose --env-file .env "${compose_files[@]}" up -d --no-deps --force-recreate --remove-orphans go-emby
app_id=$(docker compose --env-file .env "${compose_files[@]}" ps -a -q go-emby </dev/null)
if [[ -z "$app_id" ]]; then
    echo 'go-emby 容器未创建。' >&2
    exit 1
fi
container_owns_listen_port() {
    local pid port_hex inode fd
    pid=$(docker inspect -f '{{.State.Pid}}' "$app_id" 2>/dev/null || true)
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
    port_hex=$(printf '%04X' "$HTTP_PORT")
    while read -r inode; do
        for fd in "/proc/$pid/fd/"*; do
            if [[ $(readlink "$fd" 2>/dev/null || true) == "socket:[$inode]" ]]; then
                return 0
            fi
        done
    done < <(awk -v port="$port_hex" '$4 == "0A" && toupper($2) ~ ":" port "$" {print $10}' \
        "/proc/$pid/net/tcp" "/proc/$pid/net/tcp6" 2>/dev/null)
    return 1
}
failure_stage='go-emby health check'
progress '等待 go-emby 健康检查'
ready=false
for ((i=0; i<60; i++)); do
    state_before=$(docker inspect -f '{{.State.Running}} {{.RestartCount}}' "$app_id" 2>/dev/null || true)
    if [[ "$state_before" == true\ * ]] && \
        [[ $(docker inspect -f '{{.State.Health.Status}}' "$app_id" 2>/dev/null || true) == healthy ]] && \
        curl -fsS --max-time 2 "http://127.0.0.1:${HTTP_PORT}/health" >/dev/null 2>&1 && \
        { [[ "$NETWORK_MODE" != host ]] || container_owns_listen_port; }; then
        sleep 2
        state_after=$(docker inspect -f '{{.State.Running}} {{.RestartCount}}' "$app_id" 2>/dev/null || true)
        if [[ "$state_after" == "$state_before" ]]; then
            ready=true
            break
        fi
    fi
    if (( i % 5 == 0 )); then
        progress_note "等待 go-emby healthy... $((i * 2))s"
    fi
    sleep 2
done
if [[ "$ready" != true ]]; then
    app_state=$(docker inspect -f '{{.State.Status}}' "$app_id" 2>/dev/null || true)
    if [[ "$app_state" == exited || "$app_state" == restarting ]]; then
        echo "ERROR: go-emby exited or is restarting (${app_state}); container state and logs follow." >&2
    else
        echo 'ERROR: go-emby health check failed; container state and logs follow.' >&2
    fi
    exit 1
fi
progress_note 'go-emby 已健康运行'
progress '完成部署'
migration_pending=false
public_ip=$(curl -4 -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)
if [[ -z "$public_ip" ]]; then
    public_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
fi
printf '安装完成\n\n访问地址：\nhttp://%s:%s\n\n管理员账号：admin\n管理员密码：%s\n\n安装目录：%s\n' \
    "${public_ip:-服务器IP}" "$HTTP_PORT" "$ADMIN_PASSWORD" "$INSTALL_DIR"
