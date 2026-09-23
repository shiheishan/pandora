#!/usr/bin/env bash
# Pandora Panel — 普通直接安装版（无 Docker）
# 用法: sudo bash install.sh [--yes]
# 信条: 目录简单、文件简单、不臃肿
set -euo pipefail

# ── 常量 ──────────────────────────────────────────────
# 潘多拉专属安装目录。systemd 单元里原路径 /opt/aegispanel 会替换为这里
INSTALL_DIR="/opt/pandora"
PG_PKG="postgresql"          # 系统自带版本(16+)即可, 迁移无 PG18 专属语法
VK_PKG="valkey-server"
SERVICES=(aegis-public aegis-admin aegis-node)   # aegis-agent 是节点侧代理，不在面板机安装
ADMIN_PATH="ops_$(openssl rand -hex 12)"   # 高熵管理路径

say(){ printf '\033[1;32m%s\033[0m\n' "$*"; }
die(){ printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }
need(){ command -v "$1" >/dev/null 2>&1 || die "缺少 $1"; }

# ── 前置 ──────────────────────────────────────────────
[[ $EUID -eq 0 ]] || die "请用 root 运行: sudo bash install.sh"
need openssl; need curl; need systemctl

# ── 0. 环境自愈（在安装开始前修复常见环境问题，避免装到一半炸）──────
say "[0/6] 环境预检与自愈"

# 0.1 /dev/null 权限必须是 666，否则任何降权进程（postgres/valkey/nginx）
#     写 /dev/null 都会 Permission denied。实测有服务器被改成 755。
if [[ "$(stat -c %a /dev/null 2>/dev/null)" != "666" ]]; then
  say "  修复 /dev/null 权限: $(stat -c %a /dev/null) -> 666"
  chmod 666 /dev/null || die "/dev/null 权限修复失败"
fi

# 0.2 locale：最小化系统可能没有 en_US.UTF-8，pg_createcluster / initdb 会失败。
#     直接生成常用 locale；失败则后续建集群用 C locale 兜底。
if ! locale -a 2>/dev/null | grep -qE "en_US\.utf-?8"; then
  say "  生成 en_US.UTF-8 locale"
  sed -i 's/^# *\(en_US.UTF-8.*\)/\1/' /etc/locale.gen 2>/dev/null || true
  locale-gen en_US.UTF-8 >/dev/null 2>&1 || true
  export LC_ALL=C   # 兜底：即使 locale-gen 失败，后续命令用 C locale
fi

# 0.3 端口占用：检测 Pandora 需要的端口是否已被其他服务占用（Docker 旧部署残留等）
for p in 5432 6379 9000 9001 9002 9003; do
  if ss -tlnp 2>/dev/null | grep -q ":$p "; then
    say "  端口 $p 已被占用，检查是否 Pandora 旧残留..."
  fi
done

# ── 1. 装系统依赖 ─────────────────────────────────────
say "[1/6] 安装 PostgreSQL 18 + Valkey"
# Pandora 迁移使用 uuidv7() 等 PG18 内建函数，系统自带 PG16/17 跑不了。
# 必须用 PGDG 官方源安装 PostgreSQL 18。
if ! ls /usr/lib/postgresql/ 2>/dev/null | grep -q "^18$"; then
  say "  检测到 PG 版本低于 18，配置 PGDG 源并安装 PostgreSQL 18"
  . /etc/os-release 2>/dev/null || true
  CODENAME="${VERSION_CODENAME:-$(lsb_release -cs 2>/dev/null || echo bookworm)}"
  # PGDG 源（官方 PostgreSQL 仓库）
  install -d /usr/share/postgresql-common/pgdg
  curl -fsSL "https://apt.postgresql.org/pub/repos/apt/$(. /etc/os-release; echo $VERSION_CODENAME)-pgdg/Release.key" -o /etc/apt/trusted.gpg.d/postgresql.asc 2>/dev/null \
    || curl -fsSL "https://www.postgresql.org/media/keys/ACCC4CF8.asc" -o /etc/apt/trusted.gpg.d/postgresql.asc
  echo "deb https://apt.postgresql.org/pub/repos/apt ${CODENAME}-pgdg main" > /etc/apt/sources.list.d/pgdg.list
  apt-get update -qq 2>/dev/null || true
  apt-get install -y -qq postgresql-18 2>&1 | tail -2 || die "PostgreSQL 18 安装失败（PGDG 源不可用？）"
fi

# ── 1b. 数据库合并升级通道 ──────────────────────────────
# 固定 PG18 作为标准版本。若机器上已有旧版本 PG（16/17）集群，自动升级：
# 用 pg_upgrade 把旧集群数据合并到 PG18（保留数据），而不是要求手工迁移。
# 这是为后续 PG18 → PG19+ 升级预留的同一通道：升级逻辑统一走这里。
UPGRADE_SOURCE=""
for old_ver in $(ls /usr/lib/postgresql/ 2>/dev/null | sort -V | grep -v "^18$" || true); do
  if pg_lsclusters 2>/dev/null | grep -qE "^${old_ver}\s+.*online"; then
    UPGRADE_SOURCE="$old_ver"
    say "  发现旧版 PostgreSQL ${old_ver} 集群，准备合并升级到 PG18"
    break
  fi
done
if [[ -n "$UPGRADE_SOURCE" ]] && [[ -z "${PANDORA_SKIP_PG_UPGRADE:-}" ]]; then
  say "  执行 pg_upgrade: ${UPGRADE_SOURCE} → 18（数据保留）"
  OLD_DATA="/var/lib/postgresql/${UPGRADE_SOURCE}/main"
  NEW_DATA="/var/lib/postgresql/18/main"
  NEW_BIN="/usr/lib/postgresql/18/bin"
  OLD_BIN="/usr/lib/postgresql/${UPGRADE_SOURCE}/bin"
  # 停旧集群，备份旧数据目录（升级可回滚）
  pg_ctlcluster "$UPGRADE_SOURCE" main stop 2>/dev/null || true
  if [[ -d "$OLD_DATA" ]] && [[ ! -f "$NEW_DATA/PG_VERSION" ]]; then
    cp -a "$NEW_DATA" "${NEW_DATA}.pristine-$(date +%Y%m%d)" 2>/dev/null || true
    # 用 pg_upgrade 合并（--link 硬链接加速，失败可回滚）
    su -s /bin/bash postgres -c "cd /tmp && '$NEW_BIN/pg_upgrade' \
      -b '$OLD_BIN' -B '$NEW_BIN' \
      -d '$OLD_DATA' -D '$NEW_DATA' \
      -p 5432 -P 5433 --link --no-sync" 2>&1 | tail -5 || {
        say "  pg_upgrade 失败，回滚到原样"
        rm -rf "$NEW_DATA"; mv "${NEW_DATA}.pristine-$(date +%Y%m%d)" "$NEW_DATA" 2>/dev/null || true
      }
  fi
  # 删除旧集群（数据已并入 PG18）
  pg_dropcluster "$UPGRADE_SOURCE" main 2>/dev/null || true
  say "  旧版 PostgreSQL ${UPGRADE_SOURCE} 已合并到 PG18"
fi
if ! command -v psql >/dev/null 2>&1; then
  apt-get install -y -qq postgresql-client-18 2>/dev/null || true
fi
if ! command -v valkey-server >/dev/null 2>&1 && ! command -v redis-server >/dev/null 2>&1; then
  apt-get install -y -qq valkey-server 2>/dev/null || apt-get install -y -qq redis-server
fi

# 解析实际数据库服务名/版本（优先 18，迁移依赖 PG18 的 uuidv7 等内建函数）
PG_VERSION="$(ls /usr/lib/postgresql/ 2>/dev/null | sort -V | tail -1)"
[[ -n "${PG_VERSION:-}" ]] || die "未找到 PostgreSQL 安装"
[[ "$PG_VERSION" -ge 18 ]] || die "PostgreSQL 版本过低（$PG_VERSION < 18）：Pandora 迁移需要 PG18 的 uuidv7() 等函数"
PG_SERVICE="postgresql@${PG_VERSION}-main"
[[ -f "/etc/postgresql/${PG_VERSION}/main/postgresql.conf" ]] || PG_SERVICE="postgresql"
# PG 实际监听端口：pg_lsclusters 为准（多版本共存时 PG18 可能不是 5432）
PG_PORT="$(pg_lsclusters 2>/dev/null | awk -v v="$PG_VERSION" '$1==v && /online/ {print $3; exit}')"
[[ "$PG_PORT" =~ ^[0-9]+$ ]] || PG_PORT=5432

# Debian 上 apt 装 postgresql 可能未初始化主集群（比如之前用过 Docker 模式）。
# 没有集群就初始化一个，否则后续所有 psql 都会连不上。
# 注意：最小化系统可能没有 en_US.UTF-8 locale，pg_createcluster 会失败，
# 所以强制 LC_ALL=C 用 C locale 建集群（PostgreSQL 完全支持，避免 locale 依赖）。
if ! pg_lsclusters 2>/dev/null | grep -q "${PG_VERSION}.*online"; then
  say "初始化 PostgreSQL ${PG_VERSION} 主集群"
  if command -v pg_createcluster >/dev/null 2>&1; then
    LC_ALL=C pg_createcluster "$PG_VERSION" main --start 2>&1 | tail -3 || true
  fi
  # 建集群后确认真的在线，否则后续全部白搭
  pg_lsclusters 2>/dev/null | grep -q "${PG_VERSION}.*online" || die "PostgreSQL ${PG_VERSION} 集群初始化失败"
fi
# PG 实际监听端口：pg_lsclusters 为准（多版本共存时 PG18 可能不是 5432）
PG_PORT="$(pg_lsclusters 2>/dev/null | awk -v v="$PG_VERSION" '$1==v && /online/ {print $3; exit}')"
[[ "$PG_PORT" =~ ^[0-9]+$ ]] || PG_PORT=5432

# ── 2. 准备数据目录 / 启动服务 ─────────────────────────
say "[2/6] 启动数据服务"
systemctl reset-failed "$PG_SERVICE" 2>/dev/null || true
systemctl start "$PG_SERVICE" 2>/dev/null || true
VK_CONF=""
if command -v valkey-server >/dev/null 2>&1; then
  VK_CONF="/etc/valkey/valkey.conf"
  systemctl reset-failed valkey-server 2>/dev/null || true   # 清掉历史失败限速
  systemctl start valkey-server 2>/dev/null || true
elif command -v redis-server >/dev/null 2>&1; then
  VK_CONF="/etc/redis/redis.conf"
  systemctl reset-failed redis-server 2>/dev/null || true
  systemctl start redis-server 2>/dev/null || true
fi

# 生成凭据（一次性，写入 .env）
DB_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
APP_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
VK_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
# Valkey/Redis 系统包默认 6379；探测实际监听端口，不写死。
# 注意：不能用 grep|head 管道（head 提前退出会让 grep 收 SIGPIPE，
# set -euo pipefail 下整体返回 141 导致脚本静默退出——实测踩过）。
VK_PORT="${VALKEY_PORT:-$(awk -F' ' '/^port /{print $2; exit}' /etc/valkey/valkey.conf /etc/redis/redis.conf 2>/dev/null)}"
[[ "$VK_PORT" =~ ^[0-9]+$ ]] || VK_PORT=6379

# ── 3. 安装文件 ───────────────────────────────────────
say "[3/6] 安装到 $INSTALL_DIR"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/migrations" "$INSTALL_DIR/deploy"
cp -f "$SCRIPT_DIR"/../bin/* "$INSTALL_DIR/bin/"
cp -f "$SCRIPT_DIR"/../migrations/*.sql "$INSTALL_DIR/migrations/"
cp -f "$SCRIPT_DIR"/systemd/*.service "$INSTALL_DIR/deploy/"
chmod 0755 "$INSTALL_DIR/bin/"*

# ── 4. 建库 + 迁移 ────────────────────────────────────
say "[4/6] 初始化数据库 + 执行迁移"
# postgres 超级用户密码（迁移需要 superuser 绕过 RLS；本地 peer 认证不用它，
# 但 TCP 迁移 DSN 需要。生成随机密码写入 .env 供后续迁移/维护用）
PG_SUPER_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -c \"ALTER USER postgres PASSWORD '${PG_SUPER_PASS}'\"" 2>/dev/null || true
su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -c \"CREATE ROLE aegis LOGIN PASSWORD '${DB_PASS}'\"" 2>/dev/null || true
su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -c \"CREATE DATABASE aegis OWNER aegis\"" 2>/dev/null || true

# 应用角色（NOBYPASSRLS 等）
su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -d aegis -c \"CREATE ROLE aegis_app LOGIN PASSWORD '${APP_PASS}' NOSUPERUSER NOBYPASSRLS\"" 2>/dev/null || true
su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -d aegis -c 'GRANT CONNECT ON DATABASE aegis TO aegis_app'" 2>/dev/null || true

# 生成应用密钥（base64 编码，32 字节）
MASTER_KEY="$(openssl rand -base64 32)"
JWT_PUBLIC_SECRET="$(openssl rand -base64 32)"
JWT_ADMIN_SECRET="$(openssl rand -base64 32)"
JWT_CLIENT_SECRET="$(openssl rand -base64 32)"
CONFIG_SIGNING_SEED="$(openssl rand -base64 32)"

# 写入 .env
cat > "$INSTALL_DIR/deploy/.env" <<EOF
# Pandora Panel 配置 (install-native.sh 自动生成)
POSTGRES_USER=aegis
POSTGRES_PASSWORD=${DB_PASS}
POSTGRES_DB=aegis
POSTGRES_PORT=${PG_PORT}
POSTGRES_SUPER_PASSWORD=${PG_SUPER_PASS}
# 迁移 DSN 用 postgres 超级用户（00010 等迁移需绕过 RLS；运行时服务绝不用这个）
AEGIS_MIGRATION_DATABASE_URL=postgres://postgres:${PG_SUPER_PASS}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable
AEGIS_DB_APP_PASSWORD=${APP_PASS}
VALKEY_PASSWORD=${VK_PASS}
VALKEY_PORT=${VK_PORT}
AEGIS_ENV=production
AEGIS_DATABASE_URL=postgres://aegis_app:${APP_PASS}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable
AEGIS_REDIS_URL=redis://:${VK_PASS}@127.0.0.1:${VK_PORT}/0
AEGIS_ACCESS_TOKEN_TTL=720h
AEGIS_REFRESH_TOKEN_TTL=720h
AEGIS_PUBLIC_ADDR=127.0.0.1:9000
AEGIS_ADMIN_ADDR=127.0.0.1:9001
AEGIS_CLIENT_ADDR=127.0.0.1:9002
AEGIS_NODE_ADDR=127.0.0.1:9003
AEGIS_ADMIN_PATH=${ADMIN_PATH}
AEGIS_MASTER_KEY=${MASTER_KEY}
AEGIS_JWT_PUBLIC_SECRET=${JWT_PUBLIC_SECRET}
AEGIS_JWT_ADMIN_SECRET=${JWT_ADMIN_SECRET}
AEGIS_JWT_CLIENT_SECRET=${JWT_CLIENT_SECRET}
AEGIS_CONFIG_SIGNING_SEED=${CONFIG_SIGNING_SEED}
AEGIS_PUBLIC_BASE_URL=https://CHANGE_ME_TO_YOUR_PANEL_DOMAIN
PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes
EOF
chmod 0600 "$INSTALL_DIR/deploy/.env"

# 给系统 Valkey/Redis 配置密码（普通安装版没有 Docker 隔离，密码落在系统配置里）
if [[ -n "${VK_CONF:-}" ]] && [[ -f "$VK_CONF" ]]; then
  if grep -q '^requirepass' "$VK_CONF"; then
    sed -i "s|^requirepass.*|requirepass ${VK_PASS}|" "$VK_CONF"
  else
    printf '\nrequirepass %s\n' "$VK_PASS" >> "$VK_CONF"
  fi
  systemctl restart valkey-server 2>/dev/null || systemctl restart redis-server 2>/dev/null || true
  sleep 1
fi

# 迁移（用官方 migrate.sh, 它带 PGOPTIONS 保护参数；migrate.sh 在包内 deploy/ 下）
cp -f "$SCRIPT_DIR/migrate.sh" "$SCRIPT_DIR/platform.sh" "$SCRIPT_DIR/configure-app-role.sql" "$SCRIPT_DIR/check-migrations.sh" "$SCRIPT_DIR/render-nginx.sh" "$SCRIPT_DIR/nginx-aegis.conf" "$INSTALL_DIR/deploy/" 2>/dev/null || true
chmod 0755 "$INSTALL_DIR/deploy/migrate.sh" "$INSTALL_DIR/deploy/check-migrations.sh" 2>/dev/null || true
export AEGIS_ENV_FILE="$INSTALL_DIR/deploy/.env"
export AEGIS_MIGRATIONS_DIR="$INSTALL_DIR/migrations"
export AEGIS_MIGRATION_DATABASE_URL="postgres://postgres:${PG_SUPER_PASS}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable"
export GOOSE_BIN="$INSTALL_DIR/bin/goose"
# 全新库跳过一次性数据库预检（migrate.sh 官方机制：goose_db_version 不存在即全新库）
export PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database
"$INSTALL_DIR/deploy/migrate.sh" up || die "迁移失败, 见上"

# 应用角色权限（迁移后）：必须跑官方 configure-app-role.sql 做收敛
# （REVOKE TEMPORARY、固定 search_path、列级权限白名单）。只做粗粒度 GRANT
# 会让 aegis_app 残留 database TEMPORARY 权限，应用启动校验直接拒绝：
#   db: unsafe runtime role "aegis_app" has database TEMPORARY privilege
# configure-app-role.sql 用 \getenv AEGIS_DB_APP_PASSWORD 读密码，所以要以
# postgres 超级用户 + 显式环境变量执行（迁移 DSN 同理必须是超级用户）。
cp -f "$SCRIPT_DIR/configure-app-role.sql" /tmp/configure-app-role.sql 2>/dev/null || true
chmod 0644 /tmp/configure-app-role.sql 2>/dev/null || true
if ! su -s /bin/bash postgres -c "AEGIS_DB_APP_PASSWORD='${APP_PASS}' psql -d aegis -v ON_ERROR_STOP=1 -f /tmp/configure-app-role.sql" >/dev/null 2>&1; then
  # 兜底：即使收敛失败也保留基础权限，方便人工排查
  su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -d aegis -c 'GRANT USAGE ON SCHEMA public TO aegis_app'" 2>/dev/null || true
  su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -d aegis -c 'GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO aegis_app'" 2>/dev/null || true
  su -s /bin/bash postgres -c "psql -v ON_ERROR_STOP=1 -d aegis -c 'GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO aegis_app'" 2>/dev/null || true
  say "  ! configure-app-role.sql 收敛失败，已保留基础 GRANT（需人工检查角色权限）"
else
  say "  configure-app-role.sql 角色收敛完成"
fi
rm -f /tmp/configure-app-role.sql

# ── 5. systemd ───────────────────────────────────────
say "[5/6] 安装 systemd 服务"
for s in "${SERVICES[@]}"; do
  sed "s|/opt/aegispanel|${INSTALL_DIR}|g" "$SCRIPT_DIR/systemd/${s}.service" > "/etc/systemd/system/${s}.service"
done
systemctl daemon-reload
for s in "${SERVICES[@]}"; do
  systemctl enable "$s" >/dev/null 2>&1 || true
  systemctl start "$s" 2>/dev/null || true
done

# ── 6. 验证 ──────────────────────────────────────────
say "[6/6] 验证"
sleep 2
HEALTH_OK=1
for s in "${SERVICES[@]}"; do
  st="$(systemctl is-active "$s" 2>/dev/null || echo inactive)"
  echo "  $s: $st"
  [[ "$st" == "active" ]] || HEALTH_OK=0
done
if command -v valkey-server >/dev/null 2>&1; then
  valkey-cli -a "$VK_PASS" --no-auth-warning ping 2>/dev/null | grep -q PONG && echo "  valkey: PONG" || echo "  valkey: 检查失败"
fi

say ""
say "═══════════════════════════════════════════"
say " Pandora 安装完成"
say " 管理后台路径: /${ADMIN_PATH}"
say " 配置文件:    ${INSTALL_DIR}/deploy/.env"
say " 请修改 AEGIS_PUBLIC_BASE_URL 为你的域名"
say "═══════════════════════════════════════════"
[[ "$HEALTH_OK" == 1 ]] || die "部分服务未启动, 检查日志: journalctl -u aegis-public"
