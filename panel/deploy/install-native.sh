#!/usr/bin/env bash
# Pandora Panel — 普通直接安装版（无 Docker）
# 用法: sudo bash install-native.sh
#       无人值守: sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://你的域名 bash install-native.sh
# 对外地址与 HTTPS 和 install.sh 同一套（变量含义见 install.sh 头注释）：
#   PANDORA_PUBLIC_BASE_URL  https://域名 或 https://公网IPv4；首装不给时问，无人值守用本机公网 IPv4
#   PANDORA_ACME=0|1、PANDORA_ACME_EMAIL、PANDORA_ACME_SERVER、PANDORA_SKIP_NGINX=1
# 本机装了 nginx 时，首装把 HTTPS 边缘一并配好（edge-tls.sh setup：证书 → nginx → 续期 timer）；
# 没装 nginx 就只提示命令。
# 信条: 目录简单、文件简单、不臃肿
set -euo pipefail

# ── 常量 ──────────────────────────────────────────────
# 潘多拉专属安装目录。systemd 单元里原路径 /opt/aegispanel 会替换为这里
INSTALL_DIR="/opt/pandora"
# Pandora 只用这一个大版本的 main 集群（迁移用到 PG18 的 uuidv7() 等内建函数）
PANDORA_PG_MAJOR=18
SERVICES=(aegis-public aegis-admin aegis-node)

say(){ printf '\033[1;32m%s\033[0m\n' "$*"; }
die(){ printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }
need(){ command -v "$1" >/dev/null 2>&1 || die "缺少 $1"; }

#------------------------------------------------------------------------------
# PostgreSQL 集群。install-native_pgcluster_mock_test.sh 以 PANDORA_INSTALL_LIB=1 source 本文件，
# 只取函数，不往下执行任何安装动作
#------------------------------------------------------------------------------

# 以 postgres 系统用户经本地 socket 跑 psql（peer 认证，不用口令）。SQL 经 -c 或标准输入给，
# 口令一律不拼进命令行。先 cd /：postgres 用户进不了调用方的当前目录（如 /root）时 psql 会告警
native_pg_peer() { (cd / && runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 "$@"); }

# <版本>/main 在线时打印它的端口（pg_lsclusters 为准，多版本共存时 PG18 不一定是 5432），否则返回 1
native_pg_cluster_port() {
  pg_lsclusters 2>/dev/null \
    | awk -v v="$1" '$1 == v && $2 == "main" && $4 ~ /^online/ { print $3; found = 1; exit } END { exit !found }'
}

# 确保 <版本>/main 存在并在线：没有就建（LC_ALL=C：最小化系统常缺 en_US.UTF-8，initdb 会失败），
# 有但没起就启动。只建、只启动——不停、不升级、不删任何集群
native_ensure_pg_cluster() {
  local ver="$1" _
  if ! pg_lsclusters 2>/dev/null | awk -v v="$ver" '$1 == v && $2 == "main" { found = 1 } END { exit !found }'; then
    say "  初始化 PostgreSQL ${ver}/main 集群"
    LC_ALL=C pg_createcluster "$ver" main 2>&1 | tail -3 || die "PostgreSQL ${ver}/main 集群初始化失败"
  fi
  systemctl reset-failed "postgresql@${ver}-main" 2>/dev/null || true
  systemctl start "postgresql@${ver}-main" 2>/dev/null || true
  for _ in $(seq 1 15); do
    native_pg_cluster_port "$ver" >/dev/null && return 0
    sleep 1
  done
  die "PostgreSQL ${ver}/main 起不来：journalctl -u postgresql@${ver}-main -n 50"
}

# 端口上的集群里有没有 aegis 库：打印 yes / no / unknown（查不了）
native_pg_has_aegis() {
  local out
  out="$(native_pg_peer -p "$1" -d postgres -Atc "SELECT 1 FROM pg_catalog.pg_database WHERE datname = 'aegis'" 2>/dev/null)" \
    || { echo unknown; return 0; }
  case "$out" in 1) echo yes ;; '') echo no ;; *) echo unknown ;; esac
}

# 机器上 PG18 以外的集群（更早的 16/17、以后的 19）一律不碰：不停、不升级、不删。
# 以前这里对在线的旧集群跑 pg_upgrade，而且不管它成没成、跑没跑，最后都 pg_dropcluster——
# 旧库直接没了（新数据目录已初始化时 pg_upgrade 根本不跑）。现在只看旧集群里有没有 aegis 库，
# 有的话 PG18 是不是它的接班人：
#   - 首装（或从 Docker 迁）而 PG18 里还没有 aegis 库：数据多半就在旧集群里，停下；
#   - 升级而 .env 的 POSTGRES_PORT 指着旧集群：这台面板一直跑在旧集群上，停下；
#   - 其余（PG18 已有 aegis 库、.env 指着 PG18）：旧集群是早先的遗留，只提示。
# 查不了旧集群按「有 aegis 库」算。升级时 .env 的 POSTGRES_PORT 不是 PG18 的端口也停下。
# 停下时什么都还没改。
#   native_check_foreign_clusters <PG 大版本> <它的端口> <install|upgrade|from-docker> <.env 的 POSTGRES_PORT，仅升级>
native_check_foreign_clusters() {
  local want="$1" want_port="$2" mode="$3" env_port="${4:-}"
  local ver cluster port status _rest has blockers=() pg18_has=""
  if command -v pg_lsclusters >/dev/null 2>&1; then
    while read -r ver cluster port status _rest; do
      [[ "$ver" =~ ^[0-9]+$ ]] || continue
      [ "$ver" != "$want" ] || continue
      if [[ "$status" != online* ]]; then
        say "  ! 另有 PostgreSQL ${ver}/${cluster}（端口 ${port}，${status}）：不是 Pandora 用的集群，安装器不碰它"
        continue
      fi
      has="$(native_pg_has_aegis "$port")"
      if [ "$has" = no ]; then
        say "  另有 PostgreSQL ${ver}/${cluster}（端口 ${port}），里面没有 aegis 库，安装器不碰它"
        continue
      fi
      if [ "$mode" = upgrade ]; then
        if [ "$env_port" = "$port" ]; then
          blockers+=("PostgreSQL ${ver}/${cluster}（端口 ${port}）：.env 的 POSTGRES_PORT 指着它，这台面板的数据就在它里面")
          continue
        fi
      else
        [ -n "$pg18_has" ] || pg18_has="$(native_pg_has_aegis "$want_port")"
        if [ "$pg18_has" != yes ]; then
          blockers+=("PostgreSQL ${ver}/${cluster}（端口 ${port}）里$([ "$has" = yes ] && echo 有 || echo 查不清有没有) aegis 库，PG${want} 里还没有")
          continue
        fi
      fi
      say "  ! PostgreSQL ${ver}/${cluster}（端口 ${port}）里$([ "$has" = yes ] && echo 有 || echo 查不清有没有) aegis 库：面板用的是 PG${want}（端口 ${want_port}），它是早先的遗留，安装器不碰；确认没用后自己处理"
    done < <(pg_lsclusters 2>/dev/null)
  fi
  if [ "$mode" = upgrade ] && [ -n "$env_port" ] && [ "$env_port" != "$want_port" ] && [ "${#blockers[@]}" -eq 0 ]; then
    blockers+=(".env 的 POSTGRES_PORT=${env_port}，而 PostgreSQL ${want}/main 在端口 ${want_port}：不知道面板的数据在哪个集群")
  fi
  [ "${#blockers[@]}" -eq 0 ] && return 0
  printf '\033[1;31m%s\033[0m\n' "数据库不在安装器预期的地方，停下（什么都还没改，任何集群都没停、没删）：" >&2
  printf '  - %s\n' "${blockers[@]}" >&2
  die "旧集群里的 aegis 库要由人搬到 PG${want}（导出 → 恢复 → 核对，步骤见发布包 deploy/MIGRATION-RUNBOOK.md「旧版本集群里的 aegis 库」）；搬完、确认 .env 的 POSTGRES_PORT=${want_port} 后重跑本脚本"
}

#------------------------------------------------------------------------------
# .env 与加密备份。install-native_backup_mock_test.sh 测这几个函数
#------------------------------------------------------------------------------
# 直装的加密备份目录（升级前备份、HTTPS 边缘的备份也在这里）；备份单元的 ReadWritePaths 换成它
NATIVE_BACKUP_DIR=/var/backups/pandora

# 往 .env 末尾追加缺的键（参数是 KEY=VALUE），已有的行一个字节都不动——那里是随机生成的口令与密钥，
# 改错一个就连不上库、解不开信封加密的字段。先写同目录临时文件再改名，保持 0600。
# 追加了的键名放在 NATIVE_ENV_ADDED（空格分隔）
native_env_append_missing() {
  local env_file="$1" line tmp added=()
  shift
  NATIVE_ENV_ADDED=""
  for line in "$@"; do
    grep -q "^${line%%=*}=" "$env_file" || added+=("$line")
  done
  [ "${#added[@]}" -gt 0 ] || return 0
  tmp="$(mktemp "$env_file.tmp.XXXXXX")"
  chmod 0600 "$tmp"
  {
    cat "$env_file"
    printf '\n# install-native.sh 升级时补上的新键（原有的行没动）\n'
    printf '%s\n' "${added[@]}"
  } >"$tmp"
  mv -f -- "$tmp" "$env_file"
  for line in "${added[@]}"; do NATIVE_ENV_ADDED="${NATIVE_ENV_ADDED:+$NATIVE_ENV_ADDED }${line%%=*}"; done
}

# 备份加密用的 age 密钥：没有就生成（0600，目录 0700），打印公钥（recipient）。已有的绝不覆盖：
# 旧备份只能用它解开。解密私钥与备份在同一台机器，机器整体丢失时要另存（见收尾提示）
native_ensure_age_key() {
  local key="$1"
  install -d -m 0700 "$(dirname "$key")"
  if [ ! -f "$key" ]; then
    (umask 077 && age-keygen -o "$key" 2>/dev/null) || die "生成备份加密密钥失败（age-keygen）"
  fi
  chmod 0600 "$key"
  age-keygen -y "$key" || die "读不出备份加密密钥的公钥：$key"
}

# 直装 .env 里与布局、加密备份有关的键（首装写进去，升级缺了才追加）。recipient 为空时不出那一行
#   native_layout_env_lines <安装目录> <age recipient>
native_layout_env_lines() {
  printf '%s\n' "PANDORA_DB_LAYOUT=native" "AEGIS_BACKUP_DIR=$NATIVE_BACKUP_DIR" "AEGIS_BACKUP_RETENTION_DAYS=14"
  [ -z "$2" ] || printf '%s\n' "AEGIS_BACKUP_AGE_RECIPIENT=$2"
  printf '%s\n' "AEGIS_BACKUP_AGE_IDENTITY=$1/secrets/backup-age.key" "AEGIS_BACKUP_WEBDAV_BIN=$1/bin/aegis-backup-webdav"
}

# 发布包里的单元（按 docker 布局写）改成直装：安装目录、备份目录，去掉对 docker 的依赖
#   native_render_unit <单元文件> <安装目录>
native_render_unit() {
  sed -e "s|/opt/aegispanel|$2|g" \
      -e "s|/var/backups/aegispanel|$NATIVE_BACKUP_DIR|g" \
      -e '/^Requires=docker\.service$/d' \
      -e 's|^After=docker\.service$|After=postgresql.service|' \
      "$1"
}

# 可单测的部分到此为止
if [ "${PANDORA_INSTALL_LIB:-}" = 1 ]; then
  return 0 2>/dev/null || exit 0
fi

ADMIN_PATH="ops_$(openssl rand -hex 12)"   # 高熵管理路径

# ── 前置 ──────────────────────────────────────────────
[[ $EUID -eq 0 ]] || die "请用 root 运行: sudo bash install.sh"
need openssl; need curl; need systemctl
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# 首装还是升级：已有 .env 就是升级，已有的行一字不动（里面是随机生成的口令与密钥，
# 重写一次就连不上原来的数据库、解不开信封加密的字段），新版本新增的键缺了才追加到末尾。首装在动手之前先拿到
# 合规的对外地址——.env 定为 production，网关拿不到 https://域名 或 https://公网IPv4 会拒绝启动。
[[ -f "$SCRIPT_DIR/public-base-url.sh" ]] || die "发布目录缺少 deploy/public-base-url.sh"
. "$SCRIPT_DIR/public-base-url.sh"
# 升级迁移的停服顺序、首装交互式建管理员，与 install.sh 共用一份
[[ -f "$SCRIPT_DIR/install-lib.sh" ]] || die "发布目录缺少 deploy/install-lib.sh"
. "$SCRIPT_DIR/install-lib.sh"
ENV_FILE="$INSTALL_DIR/deploy/.env"
if [[ -f "$ENV_FILE" ]]; then
  MODE=upgrade
  say "检测到 $ENV_FILE，按升级处理：保留现有配置与口令"
  current_env="$(pandora_env_file_value "$ENV_FILE" AEGIS_ENV)"
  [[ "${current_env,,}" = production ]] || say "  ! 现有 .env 的 AEGIS_ENV=${current_env:-（未设置，按 development）}，这次升级不改它"
else
  MODE=install
  PUBLIC_BASE_URL="$(pandora_resolve_public_base_url 'bash install-native.sh')" \
    || die "首装需要合规的面板对外地址（原因见上）"
  say "面板对外地址：$PUBLIC_BASE_URL（运行模式 production）"
fi

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
for p in 5432 6379 9000 9001 9003; do
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

if ! command -v psql >/dev/null 2>&1; then
  apt-get install -y -qq postgresql-client-18 2>/dev/null || true
fi
if ! command -v valkey-server >/dev/null 2>&1 && ! command -v redis-server >/dev/null 2>&1; then
  apt-get install -y -qq valkey-server 2>/dev/null || apt-get install -y -qq redis-server
fi
# 加密备份（backup-postgres.sh）要 age，与 install.sh 一样缺了就装，装不上就停
if ! command -v age >/dev/null 2>&1 || ! command -v age-keygen >/dev/null 2>&1; then
  apt-get install -y -qq age >/dev/null 2>&1 || die "缺少 age（备份加密）：apt-get install -y age 后重跑"
fi

# 只用 PG18 的 main 集群：版本钉死，不取「装着的最新版本」——机器上哪天多装了 19，
# 按最新版本取会另起一个空集群，把迁移跑到空库上，而数据还在 18 里
PG_VERSION="$PANDORA_PG_MAJOR"
[[ -x "/usr/lib/postgresql/${PG_VERSION}/bin/postgres" ]] || die "PostgreSQL ${PG_VERSION} 没装上（/usr/lib/postgresql/${PG_VERSION}）：Pandora 迁移需要 PG18 的 uuidv7() 等函数"
native_ensure_pg_cluster "$PG_VERSION"
PG_PORT="$(native_pg_cluster_port "$PG_VERSION")" || die "PostgreSQL ${PG_VERSION}/main 没有在线，读不出端口"
say "  PostgreSQL ${PG_VERSION}/main 在线，端口 ${PG_PORT}"
# 别的版本的集群一律不碰；其中有 aegis 库而 PG18 不是它的接班人时停下（见函数注释）
native_check_foreign_clusters "$PG_VERSION" "$PG_PORT" "$MODE" \
  "$([[ "$MODE" = upgrade ]] && pandora_env_file_value "$ENV_FILE" POSTGRES_PORT || true)"

# ── 2. 准备数据目录 / 启动服务 ─────────────────────────
say "[2/6] 启动数据服务"
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

# 凭据：首装一次性生成并写入 .env；升级从现有 .env 读回，后面的建角色、改口令、
# 迁移、收敛运行角色都拿同一套值，重跑是幂等的。
if [[ "$MODE" = upgrade ]]; then
  DB_PASS="$(pandora_env_file_value "$ENV_FILE" POSTGRES_PASSWORD)"
  APP_PASS="$(pandora_env_file_value "$ENV_FILE" AEGIS_DB_APP_PASSWORD)"
  VK_PASS="$(pandora_env_file_value "$ENV_FILE" VALKEY_PASSWORD)"
  PG_SUPER_PASS="$(pandora_env_file_value "$ENV_FILE" POSTGRES_SUPER_PASSWORD)"
  ADMIN_PATH="$(pandora_env_file_value "$ENV_FILE" AEGIS_ADMIN_PATH)"
  [[ -n "$DB_PASS" && -n "$APP_PASS" && -n "$VK_PASS" && -n "$PG_SUPER_PASS" ]] \
    || die "现有 $ENV_FILE 缺少 POSTGRES_PASSWORD / AEGIS_DB_APP_PASSWORD / VALKEY_PASSWORD / POSTGRES_SUPER_PASSWORD，不是 install-native.sh 生成的，拒绝升级"
else
  DB_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
  APP_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
  VK_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
  PG_SUPER_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
fi
# Valkey/Redis 系统包默认 6379；探测实际监听端口，不写死。
# 注意：不能用 grep|head 管道（head 提前退出会让 grep 收 SIGPIPE，
# set -euo pipefail 下整体返回 141 导致脚本静默退出——实测踩过）。
VK_PORT="${VALKEY_PORT:-$(awk -F' ' '/^port /{print $2; exit}' /etc/valkey/valkey.conf /etc/redis/redis.conf 2>/dev/null)}"
[[ "$VK_PORT" =~ ^[0-9]+$ ]] || VK_PORT=6379

# ── 3. 安装文件 ───────────────────────────────────────
# 程序（bin/）放到第 5 步、迁移成功之后再装：升级时迁移失败要把服务拉回来，拉回来的
# 必须还是原来的程序；迁移用发布包自带的 goose（RELEASE_BIN）。
say "[3/6] 安装到 $INSTALL_DIR"
RELEASE_BIN="$(cd "$SCRIPT_DIR/../bin" && pwd)"
mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/migrations" "$INSTALL_DIR/deploy"
cp -f "$SCRIPT_DIR"/../migrations/*.sql "$INSTALL_DIR/migrations/"
cp -f "$SCRIPT_DIR"/systemd/*.service "$INSTALL_DIR/deploy/"
# 节点端发布物绑定（SHA-256 与版本），aegis-node.service 以 EnvironmentFile= 加载；
# 下面装单元时 /opt/aegispanel 会被替换成 $INSTALL_DIR
[[ ! -f "$SCRIPT_DIR/release-artifact.env" ]] || cp -f "$SCRIPT_DIR/release-artifact.env" "$INSTALL_DIR/deploy/"

# ── 4. 建库 + 迁移 ────────────────────────────────────
say "[4/6] 初始化数据库 + 执行迁移"
# postgres 超级用户口令（迁移要超级用户绕过 RLS；本地 peer 认证不用它，但迁移 DSN、预检、备份恢复
# 经 127.0.0.1 TCP 连要它）、库属主 aegis、库、运行角色 aegis_app（NOBYPASSRLS，口令由下面的
# bootstrap.sh 每次重设）。已有的不重建。口令经 psql 的标准输入给，不进任何进程的命令行参数
# （以前拼在 su -c 里，ps 看得见）；出错即停，不再 || true 吞掉。
native_pg_peer -p "$PG_PORT" -d postgres >/dev/null <<SQL || die "建数据库角色或库失败（输出见上）"
ALTER ROLE postgres PASSWORD '${PG_SUPER_PASS}';
SELECT pg_catalog.format('CREATE ROLE aegis LOGIN PASSWORD %L', '${DB_PASS}')
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'aegis') \gexec
SELECT 'CREATE DATABASE aegis OWNER aegis'
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datname = 'aegis') \gexec
SELECT pg_catalog.format('CREATE ROLE aegis_app LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD %L', '${APP_PASS}')
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'aegis_app') \gexec
GRANT CONNECT ON DATABASE aegis TO aegis_app;
SQL

# 加密备份的 age 密钥（首装生成；升级时 .env 里还没有备份键才生成，已有的绝不覆盖）
AGE_KEY="$INSTALL_DIR/secrets/backup-age.key"
AGE_RECIPIENT=""
if [[ "$MODE" = install ]] || ! grep -q '^AEGIS_BACKUP_AGE_RECIPIENT=' "$ENV_FILE"; then
  AGE_RECIPIENT="$(native_ensure_age_key "$AGE_KEY")"
fi

# 生成应用密钥并写 .env：只在首装
if [[ "$MODE" = install ]]; then
MASTER_KEY="$(openssl rand -base64 32)"
JWT_PUBLIC_SECRET="$(openssl rand -base64 32)"
JWT_ADMIN_SECRET="$(openssl rand -base64 32)"
CONFIG_SIGNING_SEED="$(openssl rand -base64 32)"

# 写入 .env（umask 077：写进去之前就是 0600）
install -d -m 0755 "$INSTALL_DIR/deploy"
(umask 077 && : > "$INSTALL_DIR/deploy/.env")
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
AEGIS_NODE_ADDR=127.0.0.1:9003
AEGIS_ADMIN_PATH=${ADMIN_PATH}
AEGIS_MASTER_KEY=${MASTER_KEY}
AEGIS_JWT_PUBLIC_SECRET=${JWT_PUBLIC_SECRET}
AEGIS_JWT_ADMIN_SECRET=${JWT_ADMIN_SECRET}
AEGIS_CONFIG_SIGNING_SEED=${CONFIG_SIGNING_SEED}
AEGIS_PUBLIC_BASE_URL=${PUBLIC_BASE_URL}
# 数据库布局与加密备份（deploy/backup-postgres.sh、aegis-backup.timer；timer 装好不启用，见收尾提示）
$(native_layout_env_lines "$INSTALL_DIR" "$AGE_RECIPIENT")
EOF
chmod 0600 "$INSTALL_DIR/deploy/.env"
else
  # 升级：老 .env 没有布局与加密备份的键，缺了才追加
  mapfile -t layout_lines < <(native_layout_env_lines "$INSTALL_DIR" "$AGE_RECIPIENT")
  native_env_append_missing "$ENV_FILE" "${layout_lines[@]}"
  [[ -z "$NATIVE_ENV_ADDED" ]] || say "  .env 补上了新键：$NATIVE_ENV_ADDED（原有的行没动）"
fi

# 给系统 Valkey/Redis 配置密码（普通安装版没有 Docker 隔离，密码落在系统配置里）
# 口令已经是这个就不动、不重启（升级时网关还在跑，重启 Valkey 会断开它们的连接）
if [[ -n "${VK_CONF:-}" ]] && [[ -f "$VK_CONF" ]] && ! grep -qxF "requirepass ${VK_PASS}" "$VK_CONF"; then
  if grep -q '^requirepass' "$VK_CONF"; then
    sed -i "s|^requirepass.*|requirepass ${VK_PASS}|" "$VK_CONF"
  else
    printf '\nrequirepass %s\n' "$VK_PASS" >> "$VK_CONF"
  fi
  systemctl restart valkey-server 2>/dev/null || systemctl restart redis-server 2>/dev/null || true
  sleep 1
fi

# 迁移只走官方 migrate.sh（它带 PGOPTIONS 保护参数），预检走 check-migrations.sh；
# 备份、校验、恢复、psql、收窄运行角色与 docker 布局同一套脚本（按 .env 的布局各自连库）。
# 健康巡检经 psql.sh 查库
install -m 0755 "$SCRIPT_DIR/healthcheck.sh" "$INSTALL_DIR/deploy/healthcheck.sh"
cp -f "$SCRIPT_DIR/migrate.sh" "$SCRIPT_DIR/platform.sh" "$SCRIPT_DIR/configure-app-role.sql" "$SCRIPT_DIR/check-migrations.sh" "$SCRIPT_DIR/render-nginx.sh" "$SCRIPT_DIR/edge-tls.sh" "$SCRIPT_DIR/update-cloudflare-realip.sh" "$SCRIPT_DIR/nginx-aegis.conf" "$SCRIPT_DIR/admin-url.sh" "$SCRIPT_DIR/MIGRATION-RUNBOOK.md" "$INSTALL_DIR/deploy/" 2>/dev/null || true
chmod 0755 "$INSTALL_DIR/deploy/migrate.sh" "$INSTALL_DIR/deploy/check-migrations.sh" "$INSTALL_DIR/deploy/render-nginx.sh" "$INSTALL_DIR/deploy/edge-tls.sh" "$INSTALL_DIR/deploy/update-cloudflare-realip.sh" "$INSTALL_DIR/deploy/admin-url.sh" 2>/dev/null || true
for f in backup-postgres.sh verify-backup.sh restore-postgres.sh psql.sh bootstrap.sh; do
  install -m 0755 "$SCRIPT_DIR/$f" "$INSTALL_DIR/deploy/$f" || die "发布目录缺少 deploy/$f"
done
[[ ! -f "$SCRIPT_DIR/backup-webdav.example.json" ]] \
  || install -m 0644 "$SCRIPT_DIR/backup-webdav.example.json" "$INSTALL_DIR/deploy/backup-webdav.example.json"
GOOSE_BIN="$RELEASE_BIN/goose"
# 迁移 DSN 用 postgres 超级用户（00010 等迁移需绕过 RLS）；老的 .env 里可能没有这一行
export AEGIS_MIGRATION_DATABASE_URL="postgres://postgres:${PG_SUPER_PASS}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable"
# 早期版本把「写入者已停」写死在 .env 里，手工跑 migrate.sh 时也会被当成已停服。
# 不替人改 .env（那里是口令与密钥），只提醒；本脚本自己只在真的停服之后才递交这份声明。
if [[ "$MODE" = upgrade ]] && grep -q '^PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=' "$ENV_FILE"; then
  say "  ! $ENV_FILE 里有 PANDORA_STOPPED_WRITER_UPGRADE_APPROVED：它让每次手工跑 migrate.sh 都声称写入者已停，建议删掉这一行"
fi
# 只有全新库才跳过一次性数据库预检（migrate.sh 的约定：goose_db_version 不存在即全新库）。
# 以前这里无条件跳过，升级时已有数据的库也不演练就直接迁移
fresh_db="$(native_pg_peer -p "$PG_PORT" -d aegis -tAc "SELECT pg_catalog.to_regclass('public.goose_db_version') IS NULL" 2>/dev/null | tr -d '[:space:]')"
case "$fresh_db" in
  t) FRESH_DB=yes ;;
  f) FRESH_DB=no ;;
  *) die "查不到库 aegis 的迁移状态，不知道是不是全新库，停下（psql -d aegis 能连上吗？）" ;;
esac

# 升级前必备份：库里已有迁移记录就先 pg_dump，导出失败或读不出目录就停在迁移之前
if [[ "$fresh_db" = f ]]; then
  BACKUP_DIR="$NATIVE_BACKUP_DIR"
  install -d -o root -g root -m 0700 "$BACKUP_DIR"
  BK="$BACKUP_DIR/pre-upgrade-$(date +%Y%m%d-%H%M%S).dump"
  (umask 077 && cd / && runuser -u postgres -- pg_dump -Fc -d aegis -p "$PG_PORT") >"$BK" \
    || { rm -f -- "$BK"; die "升级前备份失败（pg_dump），迁移没有执行"; }
  [[ -s "$BK" ]] && pg_restore --list <"$BK" >/dev/null \
    || { rm -f -- "$BK"; die "升级前备份读不出目录（pg_restore --list），迁移没有执行"; }
  chmod 0600 "$BK"
  say "  升级前备份：$BK（$(du -h "$BK" | cut -f1)）"
fi
# 升级：停服前完整预检（写凭据）→ 停服 → 迁移只核凭据；首装直接迁移。见 install-lib.sh
migrate_rc=0
PANDORA_SERVICES="${SERVICES[*]}" pandora_run_migrations "$MODE" "$FRESH_DB" "$INSTALL_DIR/deploy" \
  "$ENV_FILE" "$INSTALL_DIR/migrations" "$GOOSE_BIN" || migrate_rc=$?
case "$migrate_rc" in
  0) ;;
  10) die "停服前的迁移预检没通过（输出见上）：服务没停，数据库没动" ;;
  11) die "迁移失败（输出见上），服务已拉回原来的版本；处置见 $INSTALL_DIR/deploy/MIGRATION-RUNBOOK.md" ;;
  *) die "迁移失败, 见上" ;;
esac

# 应用角色权限（迁移后）：必须跑官方 configure-app-role.sql 做收敛
# （REVOKE TEMPORARY、固定 search_path、列级权限白名单）。只做粗粒度 GRANT
# 会让 aegis_app 残留 database TEMPORARY 权限，应用启动校验直接拒绝：
#   db: unsafe runtime role "aegis_app" has database TEMPORARY privilege
# 与 docker 布局同一个入口 bootstrap.sh（以超级用户执行，口令只经环境变量给 psql）。
# 收敛失败即停。以前这里兜底 GRANT 全表的增删改查：那会绕过 configure-app-role.sql 的列级
# 白名单与追加写表的限制，让运行角色拿到比设计大得多的权限，而安装还显示「完成」
role_log="$(bash "$INSTALL_DIR/deploy/bootstrap.sh" 2>&1)" || {
  printf '%s\n' "$role_log" | tail -20 >&2
  die "configure-app-role.sql 收敛运行角色失败（输出见上），没有启动服务（升级时服务已在迁移前停下）。修好后重跑本脚本（按升级处理，幂等）"
}
say "  configure-app-role.sql 角色收敛完成"

# ── 5. 程序与 systemd ─────────────────────────────────
say "[5/6] 安装程序与 systemd 服务"
cp -f "$RELEASE_BIN"/* "$INSTALL_DIR/bin/"
chmod 0755 "$INSTALL_DIR/bin/"*
# 单元把日志 append 到 /var/log/aegis，目录不存在时 systemd 以 209/STDOUT 失败
install -d -m 0750 /var/log/aegis
[[ ! -d /etc/logrotate.d || ! -f "$SCRIPT_DIR/logrotate-aegis" ]] \
  || install -m 0644 "$SCRIPT_DIR/logrotate-aegis" /etc/logrotate.d/aegis
for s in "${SERVICES[@]}"; do
  sed "s|/opt/aegispanel|${INSTALL_DIR}|g" "$SCRIPT_DIR/systemd/${s}.service" > "/etc/systemd/system/${s}.service"
done
# HTTPS 证书续期（edge-tls.sh renew）：只装单元，启用由 edge-tls.sh setup 做
for u in aegis-tls-renew.service aegis-tls-renew.timer; do
  [[ ! -f "$SCRIPT_DIR/systemd/$u" ]] || sed "s|/opt/aegispanel|${INSTALL_DIR}|g" "$SCRIPT_DIR/systemd/$u" > "/etc/systemd/system/$u"
done
# 健康巡检（healthcheck.sh）：脚本自己取安装根目录，单元里的路径换成安装目录；timer 在服务起来后启用
for u in aegis-health.service aegis-health.timer; do
  [[ ! -f "$SCRIPT_DIR/systemd/$u" ]] || sed "s|/opt/aegispanel|${INSTALL_DIR}|g" "$SCRIPT_DIR/systemd/$u" > "/etc/systemd/system/$u"
done
# 加密备份（backup-postgres.sh）：与 docker 布局同一份单元，换掉安装目录与备份目录、去掉对 docker 的依赖。
# 只装不启用（与 install.sh 一致：先把解密私钥另存、想清楚要不要异地备份，再 enable --now）。
# 单元的 ReadWritePaths / ReadOnlyPaths 里的目录必须存在，否则 systemd 以 226/NAMESPACE 失败
install -d -o root -g root -m 0700 "$NATIVE_BACKUP_DIR"
[[ -d /var/lib/aegispanel/backup-webdav ]] || install -d -o root -g root -m 0700 /var/lib/aegispanel/backup-webdav
[[ -d /etc/aegispanel ]] || install -d -o root -g root -m 0755 /etc/aegispanel
for u in aegis-backup.service aegis-backup.timer; do
  [[ ! -f "$SCRIPT_DIR/systemd/$u" ]] || native_render_unit "$SCRIPT_DIR/systemd/$u" "$INSTALL_DIR" > "/etc/systemd/system/$u"
done
systemctl daemon-reload
for s in "${SERVICES[@]}"; do
  systemctl enable "$s" >/dev/null 2>&1 || true
  systemctl start "$s" 2>/dev/null || true
done
# 巡检 timer 首装与升级都启用（首跑在启用后 10 分钟；已启用的 enable --now 不重置计时）
if [[ -f /etc/systemd/system/aegis-health.timer && -f "$INSTALL_DIR/deploy/healthcheck.sh" ]]; then
  systemctl enable --now aegis-health.timer >/dev/null 2>&1 \
    || echo "没能启用健康巡检：systemctl enable --now aegis-health.timer" >&2
fi

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
  REDISCLI_AUTH="$VK_PASS" valkey-cli -p "$VK_PORT" ping 2>/dev/null | grep -q PONG && echo "  valkey: PONG" || echo "  valkey: 检查失败"
fi

# 首装且在交互终端里：现场建第一个管理员（aegis-adminctl 经 platform/config 读 .env）
PANDORA_ADMIN_STATE=manual
if [[ "$HEALTH_OK" == 1 ]] && pandora_admin_prompt_wanted "$MODE"; then
  say "创建管理员"
  set -a; . "$ENV_FILE"; set +a
  pandora_bootstrap_admin "$INSTALL_DIR/bin/aegis-adminctl"
fi

# HTTPS 边缘：与 install.sh 同一条 edge-tls.sh setup（首装默认配；升级只在已走 nginx 边缘、
# 或显式 PANDORA_ACME=1 时接管）。失败不算安装失败，收尾说清楚补救命令
EDGE_NOTE=""
EDGE_URL="$(pandora_env_file_value "$ENV_FILE" AEGIS_PUBLIC_BASE_URL)"
if [[ "${PANDORA_SKIP_NGINX:-}" = 1 ]]; then
  EDGE_NOTE="PANDORA_SKIP_NGINX=1，没碰 nginx；要 HTTPS 时：sudo ${INSTALL_DIR}/deploy/edge-tls.sh setup"
elif ! command -v nginx >/dev/null 2>&1; then
  EDGE_NOTE="本机没有 nginx：apt-get install -y nginx 后执行 sudo ${INSTALL_DIR}/deploy/edge-tls.sh setup"
elif ! pandora_valid_public_base_url "$EDGE_URL"; then
  EDGE_NOTE="AEGIS_PUBLIC_BASE_URL 不是 https://域名 或 https://公网IPv4，没配 HTTPS；改好后 sudo ${INSTALL_DIR}/deploy/edge-tls.sh setup"
elif ! pandora_edge_wanted "$MODE" /etc/nginx/conf.d/aegis.conf; then
  EDGE_NOTE="还没走 nginx 边缘，升级不替你接管 80/443；切到 HTTPS：sudo ${INSTALL_DIR}/deploy/edge-tls.sh setup"
else
  say "配置 HTTPS（nginx 边缘与证书）"
  edge_rc=0
  PANDORA_BACKUP_DIR=/var/backups/pandora bash "$INSTALL_DIR/deploy/edge-tls.sh" setup "$ENV_FILE" || edge_rc=$?
  case "$edge_rc" in
    0) EDGE_NOTE="Let's Encrypt 证书在用（查看：sudo ${INSTALL_DIR}/deploy/edge-tls.sh status）" ;;
    3) EDGE_NOTE="在用自签证书（浏览器提示不安全）；80/tcp 公网可达后 sudo ${INSTALL_DIR}/deploy/edge-tls.sh issue，续期 timer 也会每天两次自动重试" ;;
    *) EDGE_NOTE="HTTPS 边缘没配好（原因见上，nginx 保持原配置）；修好后 sudo ${INSTALL_DIR}/deploy/edge-tls.sh setup" ;;
  esac
fi

say ""
say "═══════════════════════════════════════════"
say " Pandora $([[ "$MODE" = install ]] && echo 安装 || echo 升级)完成"
say " 管理后台:    $(bash "$INSTALL_DIR/deploy/admin-url.sh" "$ENV_FILE" 2>/dev/null || echo "/${ADMIN_PATH}/")"
say " 重看后台地址: sudo ${INSTALL_DIR}/deploy/admin-url.sh"
if [[ "$MODE" = install && "$PANDORA_ADMIN_STATE" = manual ]]; then
  say " 创建管理员:  cd ${INSTALL_DIR} && set -a && . deploy/.env && set +a && read -rsp '密码：' p && echo && printf '%s\n' \"\$p\" | ./bin/aegis-adminctl create --email <你的邮箱> --password-stdin; unset p"
fi
say " 配置文件:    ${INSTALL_DIR}/deploy/.env"
say " 数据库:      sudo ${INSTALL_DIR}/deploy/psql.sh"
say " 加密备份:    立刻做一份 sudo ${INSTALL_DIR}/deploy/backup-postgres.sh；每日 systemctl enable --now aegis-backup.timer"
say "              解密私钥 ${INSTALL_DIR}/secrets/backup-age.key 与备份在同一台机器，另存一份到别处（机器整体丢失时备份才解得开）"
say " 对外地址:    $(pandora_env_file_value "$ENV_FILE" AEGIS_PUBLIC_BASE_URL)"
say " HTTPS:       ${EDGE_NOTE}"
say " Cloudflare:  站点在 Cloudflare 后面时再跑 ${INSTALL_DIR}/deploy/update-cloudflare-realip.sh（默认不信任任何代理）"
say "═══════════════════════════════════════════"
[[ "$HEALTH_OK" == 1 ]] || die "部分服务未启动, 检查日志: journalctl -u aegis-public"
