#!/usr/bin/env bash
# 潘多拉面板一键安装 / 升级：系统 PostgreSQL 18 + Valkey（或 Redis 6.0+），程序装在 /opt/pandora。
#
#   首次安装：  sudo bash <发布目录>/deploy/install.sh
#   升级：      sudo bash <发布目录>/deploy/install.sh   （已有 /opt/pandora/deploy/.env 就按升级处理）
#   无人值守：  sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.com bash <发布目录>/deploy/install.sh
#
# 面板默认走 HTTPS（nginx 边缘 + 证书，交给同目录的 edge-tls.sh）：
#   有域名     → Let's Encrypt 域名证书（certbot webroot）
#   只有公网 IP → Let's Encrypt IP 证书（lego，shortlived 约 6 天，aegis-tls-renew.timer 自动续期）
#   申请失败   → 自签证书兜底（浏览器提示不安全），timer 每天两次重试，成功即无缝换上
# 申请证书即表示同意 Let's Encrypt 的订户协议。本机没装 nginx 时只提示命令。
#
# 环境变量（都可选）：
#   PANDORA_ASSUME_YES=1            不问任何问题
#   PANDORA_PUBLIC_BASE_URL=URL     面板对外地址：https://你的域名 或 https://公网IPv4。首装不给时，
#                                   终端里现场问（回车用本机公网 IPv4），无人值守直接用本机公网 IPv4；
#                                   本机没有公网 IPv4（NAT 后面）就必须给
#   PANDORA_ACME=0                  不联系任何 CA，只用自签证书（旧名 PANDORA_CERTBOT=0 同义）
#   PANDORA_ACME=1                  升级一台还没走 nginx 边缘的面板时，同意接管 nginx 并申请证书
#                                   （旧名 PANDORA_CERTBOT=1 同义）
#   PANDORA_ACME_EMAIL=邮箱          ACME 账号联系邮箱（旧名 PANDORA_CERTBOT_EMAIL）
#   PANDORA_ACME_SERVER=URL         ACME 目录地址，缺省 Let's Encrypt 正式环境（演练可用 staging）
#   PANDORA_SKIP_NGINX=1            不碰 nginx 与证书
#   PANDORA_SYSTEMD_HARDENING=0     关掉 PostgreSQL 与 Valkey 的 systemd 沙箱 drop-in（缺省开；记进 .env，
#                                   之后的升级沿用）。只给不支持挂载命名空间的容器化 VPS 用
#   PANDORA_NONINTERACTIVE=1        首装不现场问管理员邮箱与密码（收尾给出手工命令）
#
# 发布包装出来的就是生产：首装写 AEGIS_ENV=production。生产模式下网关启动时
# 要求 AEGIS_PUBLIC_BASE_URL 是 https://域名 或 https://公网IPv4（platform/config 的
# CanonicalPublicOrigin），节点接入要求发布物的 SHA-256 与版本（deploy/
# release-artifact.env，随发布包生成）。所以首装必须先拿到对外地址：
# PANDORA_PUBLIC_BASE_URL 给出、终端里现场问、或用本机公网 IPv4；都没有就在动手前停下。
#
# 设计上的三条约束：
#   1. 幂等。已有 .env 绝不覆盖——那里面是随机生成的密钥，重写一次就再也连不上原来的数据库了；
#      新版本新增的键缺了才追加到末尾。
#   2. 不替用户生成管理员密码：生成再打印出来，等于把它写进终端回滚日志和 CI 输出里。
#      首装且在交互终端里时，健康检查通过后现场问邮箱和密码（不回显、输两次），
#      密码只经标准输入交给 aegis-adminctl；无人值守时只提示该跑哪条命令。
#   3. 失败即停，并说清楚停在哪一步、怎么恢复。
# 信条: 目录简单、文件简单、不臃肿
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
RELEASE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"

# 发布目录校验：发布目录与它的每一级父目录都归 root、不可被组或他人写，目录里没有别人能改的文件
# （防止有人往 /tmp 里塞一份假的），SHA256SUMS 逐个对得上，节点端发布物绑定 release-artifact.env 在
# （生产模式下没有它节点接入全部被拒）。
# 定义在这里、在 source 发布包里的任何文件之前跑：校验函数不能放在被校验的文件里。
# install-backup_mock_test.sh 把它抽出来单测
native_verify_release_tree() {
  local root="$1" probe owner perm
  [ -d "$root/bin" ] || { echo "这不像一个发布目录：找不到 $root/bin" >&2; return 1; }
  [ -f "$root/SHA256SUMS" ] || { echo "发布目录缺少 SHA256SUMS" >&2; return 1; }
  [ -f "$root/deploy/release-artifact.env" ] \
    || { echo "发布目录缺少 deploy/release-artifact.env（build-release.sh 生成，记着节点端二进制的 SHA-256 与版本）" >&2; return 1; }
  probe="$root"
  while :; do
    owner="$(stat -c %u -- "$probe")" && perm="$(stat -c %a -- "$probe")" \
      || { echo "读不出 $probe 的属主与权限" >&2; return 1; }
    if [ "$owner" != 0 ] || (( (8#$perm & 8#022) != 0 )); then
      printf '%s\n' "发布目录或它的父路径不归 root、或可被他人写入：$probe（属主 $owner，权限 $perm）。把发布包放到 root 独占的目录再装，例如：" \
        "  install -d -o root -g root -m 0755 /opt/pandora-release" \
        "  cp -r <发布目录> /opt/pandora-release/rel && chown -R root:root /opt/pandora-release" >&2
      return 1
    fi
    [ "$probe" = / ] && break
    probe="$(dirname -- "$probe")"
  done
  if find "$root" -xdev \( ! -user root -o -perm /022 \) -print -quit | grep -q .; then
    echo "发布目录里有不归 root 或可被他人写入的文件：chown -R root:root $root && chmod -R go-w $root" >&2
    return 1
  fi
  (cd "$root" && sha256sum --quiet --strict -c SHA256SUMS) \
    || { echo "发布包校验不过（SHA256SUMS 对不上），文件被改过或不完整，换一份完整的发布包" >&2; return 1; }
}

for arg in "$@"; do
  case "$arg" in
    -h|--help)
      printf '%s\n' "用法: sudo bash install.sh    首装或升级（/opt/pandora）；环境变量见本脚本头注释"
      exit 0 ;;
    *) echo "不认识的参数：$arg（只认 --help；选项都用环境变量给，见本脚本头注释）" >&2; exit 1 ;;
  esac
done

# ── 前置 ──────────────────────────────────────────────
[[ $EUID -eq 0 ]] || { echo "请用 root 运行: sudo bash install.sh" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "缺少 sha256sum" >&2; exit 1; }
native_verify_release_tree "$RELEASE_ROOT" || exit 1

# 常量与函数在同目录的 install-lib.sh（只进发布包，不装到主机上）：上面验过发布目录之后才 source
# shellcheck source=install-lib.sh
. "$SCRIPT_DIR/install-lib.sh"
need openssl; need curl; need systemctl
# 对外地址的校验与取值、.env 单键读取
[[ -f "$SCRIPT_DIR/public-base-url.sh" ]] || die "发布目录缺少 deploy/public-base-url.sh"
. "$SCRIPT_DIR/public-base-url.sh"
# 后面几步才用到的发布包文件在这里先点名：缺了在动手之前停下，不在停服之后才发现
for f in postgresql-pandora.conf logrotate-aegis "${UNITS[@]/#/systemd/}"; do
  [[ -f "$SCRIPT_DIR/$f" ]] || die "发布目录缺少 deploy/$f"
done

ADMIN_PATH="ops_$(openssl rand -hex 12)"   # 高熵管理路径

# 首装还是升级：已有 .env 就是升级，已有的行一字不动（里面是随机生成的口令与密钥，
# 重写一次就连不上原来的数据库、解不开信封加密的字段），新版本新增的键缺了才追加到末尾；
# 不再用的键（如更早版本写进去的数据库布局键）留着也不读。首装在动手之前先拿到
# 合规的对外地址——.env 定为 production，网关拿不到 https://域名 或 https://公网IPv4 会拒绝启动。
ENV_FILE="$INSTALL_DIR/deploy/.env"
if [[ -f "$ENV_FILE" ]]; then
  MODE=upgrade
  say "检测到 $ENV_FILE，按升级处理：保留现有配置与口令"
  current_env="$(pandora_env_file_value "$ENV_FILE" AEGIS_ENV)"
  [[ "${current_env,,}" = production ]] || say "  ! 现有 .env 的 AEGIS_ENV=${current_env:-（未设置，按 development）}，这次升级不改它"
else
  MODE=install
  PUBLIC_BASE_URL="$(pandora_resolve_public_base_url 'bash install.sh')" \
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
#     先试着生成；还没有就用 C.UTF-8（glibc 2.35 起内置）兜底。不能兜底成 LC_ALL=C：apt 装 postgresql-18
#     时 postgresql-common 按当前 locale 建 18/main，C locale 建出来的集群编码是 SQL_ASCII
if ! locale -a 2>/dev/null | grep -qE "en_US\.utf-?8"; then
  say "  生成 en_US.UTF-8 locale"
  sed -i 's/^# *\(en_US.UTF-8.*\)/\1/' /etc/locale.gen 2>/dev/null || true
  locale-gen en_US.UTF-8 >/dev/null 2>&1 || true
  if ! locale -a 2>/dev/null | grep -qE "en_US\.utf-?8"; then
    export LC_ALL=C.UTF-8 LANG=C.UTF-8
  fi
fi

# 0.3 端口占用：Pandora 要用的端口已被别的程序占着时提示（升级时是自己的服务，属正常）
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
  # 装包时 postgresql-common 顺手建 18/main：显式 UTF-8 locale，不随外层环境落成 C / SQL_ASCII
  LC_ALL="${LC_ALL:-C.UTF-8}" LANG="${LANG:-C.UTF-8}" apt-get install -y -qq postgresql-18 2>&1 | tail -2 \
    || die "PostgreSQL 18 安装失败（PGDG 源不可用？）"
fi

if ! command -v psql >/dev/null 2>&1; then
  apt-get install -y -qq postgresql-client-18 2>/dev/null || true
fi
if ! command -v valkey-server >/dev/null 2>&1 && ! command -v redis-server >/dev/null 2>&1; then
  apt-get install -y -qq valkey-server 2>/dev/null || apt-get install -y -qq redis-server
fi
# 加密备份（backup-postgres.sh）要 age：缺了就装，装不上就停
if ! command -v age >/dev/null 2>&1 || ! command -v age-keygen >/dev/null 2>&1; then
  apt-get install -y -qq age >/dev/null 2>&1 || die "缺少 age（备份加密）：apt-get install -y age 后重跑"
fi

# Valkey / Redis：认得出版本、且不低于 Pandora 支持的最低版本（Redis 6.0；Valkey 任何版本），
# 在任何数据与配置改动之前核对
VK_CONF="" VK_UNIT=""
if command -v valkey-server >/dev/null 2>&1; then
  VK_CONF="/etc/valkey/valkey.conf" VK_UNIT=valkey-server
elif command -v redis-server >/dev/null 2>&1; then
  VK_CONF="/etc/redis/redis.conf" VK_UNIT=redis-server
fi
[[ -n "$VK_UNIT" ]] || die "没装上 Valkey / Redis（apt-get install -y valkey-server 后重跑）"
native_check_valkey_version "$VK_UNIT"

# 只用 PG18 的 main 集群：版本钉死，不取「装着的最新版本」——机器上哪天多装了 19，
# 按最新版本取会另起一个空集群，把迁移跑到空库上，而数据还在 18 里
PG_VERSION="$PANDORA_PG_MAJOR"
[[ -x "/usr/lib/postgresql/${PG_VERSION}/bin/postgres" ]] || die "PostgreSQL ${PG_VERSION} 没装上（/usr/lib/postgresql/${PG_VERSION}）：Pandora 迁移需要 PG18 的 uuidv7() 等函数"
native_ensure_pg_cluster "$PG_VERSION"
PG_PORT="$(native_pg_cluster_port "$PG_VERSION")" || die "PostgreSQL ${PG_VERSION}/main 没有在线，读不出端口"
say "  PostgreSQL ${PG_VERSION}/main 在线，端口 ${PG_PORT}"
# Pandora 的 PostgreSQL 参数放在集群的 conf.d 里（第 4 步写入），先核 postgresql.conf 真的会读 conf.d
native_check_pg_conf_include "$PG_VERSION"
# 别的版本的集群一律不碰；其中有 aegis 库而 PG18 不是它的接班人时停下（见函数注释）
native_check_foreign_clusters "$PG_VERSION" "$PG_PORT" "$MODE" \
  "$([[ "$MODE" = upgrade ]] && pandora_env_file_value "$ENV_FILE" POSTGRES_PORT || true)"
# 升级：更早装的库可能是 SQL_ASCII，只告警给办法（返回 2），不改库
[[ "$MODE" != upgrade ]] || native_check_db_encoding "$PG_PORT" || true

# ── 2. 启动数据服务 ────────────────────────────────────
say "[2/6] 启动数据服务"
systemctl reset-failed "$VK_UNIT" 2>/dev/null || true   # 清掉历史失败限速
systemctl start "$VK_UNIT" 2>/dev/null || true

# 凭据：首装一次性生成并写入 .env；升级从现有 .env 读回，后面的建角色、改口令、
# 迁移、收敛运行角色都拿同一套值，重跑是幂等的。
if [[ "$MODE" = upgrade ]]; then
  DB_PASS="$(pandora_env_file_value "$ENV_FILE" POSTGRES_PASSWORD)"
  APP_PASS="$(pandora_env_file_value "$ENV_FILE" AEGIS_DB_APP_PASSWORD)"
  VK_PASS="$(pandora_env_file_value "$ENV_FILE" VALKEY_PASSWORD)"
  PG_SUPER_PASS="$(pandora_env_file_value "$ENV_FILE" POSTGRES_SUPER_PASSWORD)"
  ADMIN_PATH="$(pandora_env_file_value "$ENV_FILE" AEGIS_ADMIN_PATH)"
  [[ -n "$DB_PASS" && -n "$APP_PASS" && -n "$VK_PASS" && -n "$PG_SUPER_PASS" ]] \
    || die "现有 $ENV_FILE 缺少 POSTGRES_PASSWORD / AEGIS_DB_APP_PASSWORD / VALKEY_PASSWORD / POSTGRES_SUPER_PASSWORD，不是 install.sh 生成的，拒绝升级"
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
RELEASE_BIN="$RELEASE_ROOT/bin"
mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/migrations" "$INSTALL_DIR/deploy"
cp -f "$SCRIPT_DIR"/../migrations/*.sql "$INSTALL_DIR/migrations/"
# 节点端发布物绑定（SHA-256 与版本），aegis-node.service 以 EnvironmentFile= 加载
cp -f "$SCRIPT_DIR/release-artifact.env" "$INSTALL_DIR/deploy/"

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
SELECT 'CREATE DATABASE aegis OWNER aegis TEMPLATE template0 ENCODING ''UTF8'''
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
# Pandora Panel 配置 (install.sh 自动生成)
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
# 加密备份（deploy/backup-postgres.sh、aegis-backup.timer；timer 装好不启用，见收尾提示）与节点端分发目录
$(native_default_env_lines "$INSTALL_DIR" "$AGE_RECIPIENT")
EOF
chmod 0600 "$INSTALL_DIR/deploy/.env"
else
  # 升级：更早的 .env 没有加密备份、节点端分发目录与迁移 DSN 的键，缺了才追加
  mapfile -t default_lines < <(native_default_env_lines "$INSTALL_DIR" "$AGE_RECIPIENT")
  default_lines+=("AEGIS_MIGRATION_DATABASE_URL=postgres://postgres:${PG_SUPER_PASS}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable")
  native_env_append_missing "$ENV_FILE" "${default_lines[@]}"
  [[ -z "$NATIVE_ENV_ADDED" ]] || say "  .env 补上了新键：$NATIVE_ENV_ADDED（原有的行没动）"
fi

# 迁移只走官方 migrate.sh（它带 PGOPTIONS 保护参数），预检走 check-migrations.sh；
# 备份、校验、恢复、psql、收窄运行角色各有一个脚本，都按 .env 经 127.0.0.1 连库。
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
# 迁移 DSN 用 postgres 超级用户（00010 等迁移需绕过 RLS）：老的 .env 里没有这一行时上面已经追加进 .env。
# 不 export 给子进程——它带着超级用户口令，经 env(1) 传就进了命令行参数；migrate.sh 自己从 .env 读
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
# 迁移版本：收尾提示里写「从哪到哪」
schema_version() {
  native_pg_peer -p "$PG_PORT" -d aegis -tAc \
    "SELECT coalesce(max(version_id) FILTER (WHERE is_applied), 0) FROM public.goose_db_version" 2>/dev/null | tr -d '[:space:]'
}
MIGRATION_BEFORE=0
[[ "$FRESH_DB" = yes ]] || MIGRATION_BEFORE="$(schema_version || true)"

# 升级前必备份：库里已有迁移记录就先 pg_dump，导出失败或读不出目录就停在迁移之前
BK=""
if [[ "$fresh_db" = f ]]; then
  install -d -o root -g root -m 0700 "$NATIVE_BACKUP_DIR"
  BK="$NATIVE_BACKUP_DIR/pre-upgrade-$(date +%Y%m%d-%H%M%S).dump"
  (umask 077 && cd / && runuser -u postgres -- pg_dump -Fc -d aegis -p "$PG_PORT") >"$BK" \
    || { rm -f -- "$BK"; die "升级前备份失败（pg_dump），迁移没有执行"; }
  [[ -s "$BK" ]] && pg_restore --list <"$BK" >/dev/null \
    || { rm -f -- "$BK"; die "升级前备份读不出目录（pg_restore --list），迁移没有执行"; }
  chmod 0600 "$BK"
  say "  升级前备份：$BK（$(du -h "$BK" | cut -f1)）"
fi
# 升级：停服前完整预检（写凭据）→ 停服 → 迁移只核凭据；首装直接迁移。见 install-lib.sh
migrate_rc=0
pandora_run_migrations "$MODE" "$FRESH_DB" "$INSTALL_DIR/deploy" \
  "$ENV_FILE" "$INSTALL_DIR/migrations" "$GOOSE_BIN" || migrate_rc=$?
case "$migrate_rc" in
  0) ;;
  10) die "停服前的迁移预检没通过（输出见上）：服务没停，数据库没动" ;;
  11) die "迁移失败（输出见上），服务已拉回原来的版本；处置见 $INSTALL_DIR/deploy/MIGRATION-RUNBOOK.md" ;;
  *) die "迁移失败, 见上" ;;
esac
MIGRATION_AFTER="$(schema_version || true)"
say "  迁移版本 ${MIGRATION_BEFORE:-?} → ${MIGRATION_AFTER:-?}"

# 应用角色权限（迁移后）：必须跑官方 configure-app-role.sql 做收敛
# （REVOKE TEMPORARY、固定 search_path、列级权限白名单）。只做粗粒度 GRANT
# 会让 aegis_app 残留 database TEMPORARY 权限，应用启动校验直接拒绝：
#   db: unsafe runtime role "aegis_app" has database TEMPORARY privilege
# 入口是 bootstrap.sh（以超级用户执行，口令只经环境变量给 psql）。
# 收敛失败即停。以前这里兜底 GRANT 全表的增删改查：那会绕过 configure-app-role.sql 的列级
# 白名单与追加写表的限制，让运行角色拿到比设计大得多的权限，而安装还显示「完成」
role_log="$(bash "$INSTALL_DIR/deploy/bootstrap.sh" 2>&1)" || {
  printf '%s\n' "$role_log" | tail -20 >&2
  die "configure-app-role.sql 收敛运行角色失败（输出见上），没有启动服务（升级时服务已在迁移前停下）。修好后重跑本脚本（按升级处理，幂等）"
}
say "  configure-app-role.sql 角色收敛完成"

# PostgreSQL 的调参与加固、Valkey 的加固（RUNBOOK「PostgreSQL 与 Valkey 的加固」）：
#   PostgreSQL：conf.d/pandora.conf（发布包 postgresql-pandora.conf）与单元 drop-in，一起写、只重启一次；
#   Valkey：口令、配置块（只听回环、禁 FLUSHALL / FLUSHDB、不落盘、内存上限）、单元 drop-in。
# 放在这里：外来集群检查早已通过（它停下时什么都没改），升级时网关在迁移前就停了、首装还没起，重启 PostgreSQL
# 与 Valkey 不打断在线请求。都已经是这样就不动、不重启。某一项起不来会撤回这一项、核实服务照原样在跑，
# 记下来：把服务按新版本起来之后再停下，提示原因与开关 PANDORA_SYSTEMD_HARDENING
native_load_hardening_switch "$ENV_FILE"
HARDEN_FAILED=""
native_apply_pg_config "$PG_VERSION" "$PG_PORT" "$SCRIPT_DIR/postgresql-pandora.conf" || HARDEN_FAILED+=" PostgreSQL"
native_harden_valkey "$VK_UNIT" "$VK_CONF" "$VK_PASS" || HARDEN_FAILED+=" $VK_UNIT"

# ── 5. 程序与 systemd ─────────────────────────────────
say "[5/6] 安装程序与 systemd 服务"
cp -f "$RELEASE_BIN"/* "$INSTALL_DIR/bin/"
chmod 0755 "$INSTALL_DIR/bin/"*
# 节点端一键安装下发的二进制（aegis-node 从 .env 的 PANDORA_PDND_DIST_DIR 读）：与程序一起、迁移成功之后才换
install -d -m 0755 "$INSTALL_DIR/pdnd-dist"
cp -f "$RELEASE_ROOT"/pdnd-dist/* "$INSTALL_DIR/pdnd-dist/"
chmod 0755 "$INSTALL_DIR/pdnd-dist/"*
# 单元把日志 append 到 /var/log/aegis，目录不存在时 systemd 以 209/STDOUT 失败
install -d -m 0750 /var/log/aegis
[[ ! -d /etc/logrotate.d ]] || install -m 0644 "$SCRIPT_DIR/logrotate-aegis" /etc/logrotate.d/aegis
# 单元里的 ReadWritePaths / ReadOnlyPaths 目录必须存在，否则 systemd 以 226/NAMESPACE 失败（备份单元用到这几个）
install -d -o root -g root -m 0700 "$NATIVE_BACKUP_DIR"
[[ -d /var/lib/aegispanel/backup-webdav ]] || install -d -o root -g root -m 0700 /var/lib/aegispanel/backup-webdav
[[ -d /etc/aegispanel ]] || install -d -o root -g root -m 0755 /etc/aegispanel
# WebDAV 异地备份的检查点复制钩子放在这里（backup-webdav.example.json 的 checkpoint_replication_hook，
# aegis-backup-webdav 的缺省目录）：只给 root，钩子由运维自己放
install -d -o root -g root -m 0700 "$INSTALL_DIR/checkpoint-sink"
# 全部单元原样装上（路径就是 $INSTALL_DIR，不改写）：三个网关；证书续期（启用由 edge-tls.sh setup 做）；
# 健康巡检（timer 在服务起来后启用）；加密备份（只装不启用：先把解密私钥另存、想清楚要不要异地备份，
# 再 enable --now，见收尾提示）
for u in "${UNITS[@]}"; do
  install -m 0644 "$SCRIPT_DIR/systemd/$u" "/etc/systemd/system/$u"
done
systemctl daemon-reload
native_swap_accounting_note
for s in "${SERVICES[@]}"; do
  systemctl enable "$s" >/dev/null 2>&1 || true
  systemctl start "$s" 2>/dev/null || true
done
if [[ -n "$HARDEN_FAILED" ]]; then
  die "程序已装好，服务已按新版本起来，但 ${HARDEN_FAILED# } 的加固或调参没生效（已撤回、核实在跑；原因与办法见上）。处理后重跑本脚本（按升级处理），或用 PANDORA_SYSTEMD_HARDENING=0 明确关掉加固"
fi
# 巡检 timer 首装与升级都启用（首跑在启用后 10 分钟；已启用的 enable --now 不重置计时）
systemctl enable --now aegis-health.timer >/dev/null 2>&1 \
  || echo "没能启用健康巡检：systemctl enable --now aegis-health.timer" >&2

# ── 6. 验证 ──────────────────────────────────────────
say "[6/6] 验证"
HEALTH_OK=1
native_gateways_healthy || HEALTH_OK=0
for s in "${SERVICES[@]}"; do
  echo "  $s: $(systemctl is-active "$s" 2>/dev/null || echo inactive)"
done
for p in 9000:公开 9001:管理 9003:节点; do
  echo "  ${p#*:}接口 :${p%%:*} /healthz $(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:${p%%:*}/healthz" 2>/dev/null || echo 000)"
done
VK_CLI="${VK_UNIT%-server}-cli"
if command -v "$VK_CLI" >/dev/null 2>&1; then
  REDISCLI_AUTH="$VK_PASS" "$VK_CLI" -p "$VK_PORT" ping 2>/dev/null | grep -q PONG && echo "  ${VK_UNIT%-server}: PONG" || echo "  ${VK_UNIT%-server}: 检查失败"
fi

# 首装且在交互终端里：现场建第一个管理员（aegis-adminctl 经 platform/config 读 .env）。
# 放在 HTTPS 边缘之前：那一步出问题时管理员已经建好，重跑会走升级、不再问
PANDORA_ADMIN_STATE=manual
if [[ "$HEALTH_OK" == 1 ]] && pandora_admin_prompt_wanted "$MODE"; then
  say "创建管理员"
  set -a; . "$ENV_FILE"; set +a
  pandora_bootstrap_admin "$INSTALL_DIR/bin/aegis-adminctl"
fi

# HTTPS 边缘：edge-tls.sh setup（首装默认配；升级只在已走 nginx 边缘、或显式 PANDORA_ACME=1 时接管）。
# 证书申请失败不算安装失败：自签兜底；setup 本身失败时 nginx 保持原配置，收尾说清楚补救命令。
# 健康检查没过也照样配：首装停在这里的话，重跑按升级处理、不会再替人接管 80/443
EDGE_STATE=skipped
EDGE_URL="$(pandora_env_file_value "$ENV_FILE" AEGIS_PUBLIC_BASE_URL)"
if [[ "${PANDORA_SKIP_NGINX:-}" = 1 ]]; then
  say "  PANDORA_SKIP_NGINX=1：不碰 nginx 与证书"
elif ! command -v nginx >/dev/null 2>&1; then
  EDGE_STATE=no-nginx
elif ! pandora_valid_public_base_url "$EDGE_URL"; then
  EDGE_STATE=bad-url
elif ! pandora_edge_wanted "$MODE" /etc/nginx/conf.d/aegis.conf; then
  EDGE_STATE=not-enabled
else
  say "配置 HTTPS（nginx 边缘与证书）"
  edge_rc=0
  bash "$INSTALL_DIR/deploy/edge-tls.sh" setup "$ENV_FILE" || edge_rc=$?
  case "$edge_rc" in
    0) EDGE_STATE=trusted ;;
    3) EDGE_STATE=selfsigned ;;
    *) EDGE_STATE=failed ;;
  esac
fi

[[ "$HEALTH_OK" == 1 ]] || die "服务起来了但健康检查没通过（三个网关的 /healthz 一分钟内没有都回 200）。看日志：journalctl -u aegis-public -n 50 --no-pager；tail -50 /var/log/aegis/public.log。修好后重跑本脚本（按升级处理，幂等）"

# 收尾提示：首装与升级分开说，见 install-lib.sh 的 print_install_summary
BACKUP_TIMER=disabled
systemctl is-enabled --quiet aegis-backup.timer 2>/dev/null && BACKUP_TIMER=enabled
print_install_summary
