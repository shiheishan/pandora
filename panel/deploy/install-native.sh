#!/usr/bin/env bash
# Pandora Panel — 普通直接安装版（无 Docker）
# 用法: sudo bash install-native.sh                 首装或升级（/opt/pandora）
#       sudo bash install-native.sh --from-docker   把 install.sh 装的 docker 布局（/opt/aegispanel）迁过来：
#         核对 → 停服 → 导出 → 恢复 → 逐项核对 → 迁移 → 切换 → 停 Docker 容器；失败自动回到 Docker，卷不删
#       无人值守: sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://你的域名 bash install-native.sh
# 对外地址与 HTTPS 和 install.sh 同一套（变量含义见 install.sh 头注释）：
#   PANDORA_PUBLIC_BASE_URL  https://域名 或 https://公网IPv4；首装不给时问，无人值守用本机公网 IPv4
#   PANDORA_ACME=0|1、PANDORA_ACME_EMAIL、PANDORA_ACME_SERVER、PANDORA_SKIP_NGINX=1
# 本机装了 nginx 时，首装把 HTTPS 边缘一并配好（edge-tls.sh setup：证书 → nginx → 续期 timer）；
# 没装 nginx 就只提示命令。
# 信条: 目录简单、文件简单、不臃肿
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
RELEASE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"

# 发布目录校验，与 install.sh / install-linux-binaries.sh 同样的口径：发布目录与它的每一级父目录都归 root、
# 不可被组或他人写，目录里没有别人能改的文件（防止有人往 /tmp 里塞一份假的），SHA256SUMS 逐个对得上，
# 节点端发布物绑定 release-artifact.env 在（生产模式下没有它节点接入全部被拒）。
# 定义在这里、在 source 发布包里的任何文件之前跑：校验函数不能放在被校验的文件里。
# install-native_backup_mock_test.sh 把它抽出来单测
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

FROM_DOCKER=0
for arg in "$@"; do
  case "$arg" in
    --from-docker) FROM_DOCKER=1 ;;
    -h|--help)
      printf '%s\n' "用法: sudo bash install-native.sh                首装或升级直装布局（/opt/pandora）" \
        "      sudo bash install-native.sh --from-docker  把 install.sh 装的 docker 布局（/opt/aegispanel）迁到直装" \
        "迁移会停服（导出、恢复、核对、迁移、切换期间面板不可用）；Docker 的卷不删，收尾打印删除命令。" \
        "迁移请在 tmux 里或用 systemd-run 跑（见 deploy/RUNBOOK.md 第 13 章），ssh 断了也不会半途停下。"
      exit 0 ;;
    *) echo "不认识的参数：$arg（只认 --from-docker、--help）" >&2; exit 1 ;;
  esac
done

# ── 前置 ──────────────────────────────────────────────
[[ $EUID -eq 0 ]] || { echo "请用 root 运行: sudo bash install-native.sh" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "缺少 sha256sum" >&2; exit 1; }
native_verify_release_tree "$RELEASE_ROOT" || exit 1

# 常量与函数在同目录的 install-native-lib.sh（只进发布包，不装到主机上）：上面验过发布目录之后才 source
# shellcheck source=install-native-lib.sh
. "$SCRIPT_DIR/install-native-lib.sh"
need openssl; need curl; need systemctl

ADMIN_PATH="ops_$(openssl rand -hex 12)"   # 高熵管理路径

# 首装还是升级：已有 .env 就是升级，已有的行一字不动（里面是随机生成的口令与密钥，
# 重写一次就连不上原来的数据库、解不开信封加密的字段），新版本新增的键缺了才追加到末尾。首装在动手之前先拿到
# 合规的对外地址——.env 定为 production，网关拿不到 https://域名 或 https://公网IPv4 会拒绝启动。
[[ -f "$SCRIPT_DIR/public-base-url.sh" ]] || die "发布目录缺少 deploy/public-base-url.sh"
. "$SCRIPT_DIR/public-base-url.sh"
# 升级迁移的停服顺序、首装交互式建管理员，与 install.sh 共用一份
[[ -f "$SCRIPT_DIR/install-lib.sh" ]] || die "发布目录缺少 deploy/install-lib.sh"
. "$SCRIPT_DIR/install-lib.sh"
ENV_FILE="$INSTALL_DIR/deploy/.env"
FD_STATE_FILE="$INSTALL_DIR/deploy/from-docker.state"
if [[ "$FROM_DOCKER" = 1 ]]; then
  MODE=from-docker
  FD_RETRY=0
  # 状态文件在迁移动手的第一步就写下：有它说明是上次没走完的迁移，能重来；没有它而直装的 .env 在，
  # 这台本来就是直装
  if [[ -f "$FD_STATE_FILE" ]]; then
    case "$(fd_state_get state)" in
      done)
        [[ -f "$ENV_FILE" ]] || die "$FD_STATE_FILE 记着已经迁完，可直装的 .env 不在：像是退回过 Docker。想再迁，先把 from-docker.state 改名为 from-docker.state.retired 再跑"
        die "已经从 Docker 迁完了（$FD_STATE_FILE）。升级直装直接跑 install-native.sh" ;;
      cutover)
        # 上次直装已接管，只差停 Docker 容器：只收尾
        say "上次已切换到直装、还没收尾：这次只停 Docker 容器"
        FD_CUTOVER=1
        FD_BACKUP_TIMER_WAS_ACTIVE="$(fd_state_get backup_timer)"
        fd_finalize
        say "收尾完成。删 Docker 的卷与守护进程由你决定：cd $DOCKER_DIR/deploy && docker compose down -v（不可恢复）"
        exit 0 ;;
      *)
        FD_RETRY=1
        say "上次从 Docker 迁移没完成（$(fd_state_get state)）：docker 那边的库没动过，这次重来" ;;
    esac
  elif [[ -f "$ENV_FILE" ]]; then
    die "这台已经是直装布局（$ENV_FILE 已存在）。--from-docker 只用于还在 docker 布局上的机器；升级直装直接跑 install-native.sh"
  elif [[ -f "$FD_STATE_FILE.retired" ]]; then
    # 迁过、又照 RUNBOOK 退回了 Docker：直装那份旧库还在 PG18 里，按重来处理（改名放一边）
    FD_RETRY=1
    say "这台迁过直装又退回了 Docker（$FD_STATE_FILE.retired）：直装那份旧库改名放一边，从头再迁"
  fi
  say "从 docker 布局（$DOCKER_DIR）迁到直装（$INSTALL_DIR）：先核对，什么都不改"
  fd_preflight
  install -d -m 0755 "$INSTALL_DIR" "$INSTALL_DIR/deploy"
  fd_state_set state=preparing
  PUBLIC_BASE_URL="$(pandora_env_file_value "$FD_DOCKER_ENV" AEGIS_PUBLIC_BASE_URL)"
  # 停了 docker 的写入者之后、直装接管之前，任何退出（含 Ctrl-C、kill、ssh 断开的 HUP）都把 docker 布局拉回来；
  # 迁移的每一步与回滚的结果另写一份日志（终端断了也看得到）
  FD_LOG="$INSTALL_DIR/deploy/from-docker.log"
  trap fd_abort EXIT
  trap 'FD_SIGNAL_RC=129; exit 129' HUP
  trap 'FD_SIGNAL_RC=130; exit 130' INT
  trap 'FD_SIGNAL_RC=143; exit 143' TERM
else
  # 普通模式：docker 布局还在服务就不动手（迁完的除外），见 native_plain_mode_guard
  native_plain_mode_guard "$DOCKER_DIR" "$INSTALL_DIR" "$(fd_state_get state)"
fi
if [[ "$FROM_DOCKER" = 1 ]]; then
  :
elif [[ -f "$ENV_FILE" ]]; then
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
# 升级：更早的直装可能把库建成了 SQL_ASCII，只告警给办法（返回 2），不改库
[[ "$MODE" != upgrade ]] || native_check_db_encoding "$PG_PORT" || true

# ── 2. 准备数据目录 / 启动服务 ─────────────────────────
say "[2/6] 启动数据服务"
VK_CONF="" VK_UNIT=""
if command -v valkey-server >/dev/null 2>&1; then
  VK_CONF="/etc/valkey/valkey.conf" VK_UNIT=valkey-server
elif command -v redis-server >/dev/null 2>&1; then
  VK_CONF="/etc/redis/redis.conf" VK_UNIT=redis-server
fi
if [[ -n "$VK_UNIT" ]]; then
  systemctl reset-failed "$VK_UNIT" 2>/dev/null || true   # 清掉历史失败限速
  systemctl start "$VK_UNIT" 2>/dev/null || true
fi

# 凭据：首装一次性生成并写入 .env；升级从现有 .env 读回，后面的建角色、改口令、
# 迁移、收敛运行角色都拿同一套值，重跑是幂等的。
if [[ "$MODE" = from-docker ]]; then
  # 库属主、运行角色、Valkey 的口令沿用 docker 布局的；postgres 超级用户口令重试时沿用上次写下的
  DB_PASS="$(pandora_env_file_value "$FD_DOCKER_ENV" POSTGRES_PASSWORD)"
  APP_PASS="$(pandora_env_file_value "$FD_DOCKER_ENV" AEGIS_DB_APP_PASSWORD)"
  VK_PASS="$(pandora_env_file_value "$FD_DOCKER_ENV" VALKEY_PASSWORD)"
  ADMIN_PATH="$(pandora_env_file_value "$FD_DOCKER_ENV" AEGIS_ADMIN_PATH)"
  PG_SUPER_PASS=""
  [[ ! -f "$ENV_FILE" ]] || PG_SUPER_PASS="$(pandora_env_file_value "$ENV_FILE" POSTGRES_SUPER_PASSWORD)"
  [[ -n "$PG_SUPER_PASS" ]] || PG_SUPER_PASS="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
elif [[ "$MODE" = upgrade ]]; then
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
RELEASE_BIN="$RELEASE_ROOT/bin"
mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/migrations" "$INSTALL_DIR/deploy"
cp -f "$SCRIPT_DIR"/../migrations/*.sql "$INSTALL_DIR/migrations/"
cp -f "$SCRIPT_DIR"/systemd/*.service "$INSTALL_DIR/deploy/"
# 节点端发布物绑定（SHA-256 与版本），aegis-node.service 以 EnvironmentFile= 加载；
# 下面装单元时 /opt/aegispanel 会被替换成 $INSTALL_DIR
cp -f "$SCRIPT_DIR/release-artifact.env" "$INSTALL_DIR/deploy/"

# ── 4. 建库 + 迁移 ────────────────────────────────────
say "[4/6] 初始化数据库 + 执行迁移"
# postgres 超级用户口令（迁移要超级用户绕过 RLS；本地 peer 认证不用它，但迁移 DSN、预检、备份恢复
# 经 127.0.0.1 TCP 连要它）、库属主 aegis、库、运行角色 aegis_app（NOBYPASSRLS，口令由下面的
# bootstrap.sh 每次重设）。已有的不重建。口令经 psql 的标准输入给，不进任何进程的命令行参数
# （以前拼在 su -c 里，ps 看得见）；出错即停，不再 || true 吞掉。
# --from-docker 要一个空的 aegis 库来恢复：上次没迁完留下的改名放一边（不删）；第一次迁就已经有，不知道
# 是什么数据，停下
if [[ "$MODE" = from-docker ]] && [[ "$(native_pg_has_aegis "$PG_PORT")" != no ]]; then
  [[ "$FD_RETRY" = 1 ]] || die "PG${PG_VERSION} 里已经有 aegis 库（或查不清），不知道是什么数据，不往里恢复；确认没用后改名或删掉再重跑"
  stale="aegis_stale_$(date +%Y%m%d%H%M%S)"
  native_pg_peer -p "$PG_PORT" -d postgres -c "ALTER DATABASE aegis RENAME TO $stale" >/dev/null \
    || die "把上次没迁完的直装库 aegis 改名失败（还有连接？）"
  say "  上次没迁完留下的直装库 aegis 改名为 $stale（确认不要后自己删）"
fi
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
if [[ "$MODE" = install ]] || { [[ "$MODE" = upgrade ]] && ! grep -q '^AEGIS_BACKUP_AGE_RECIPIENT=' "$ENV_FILE"; }; then
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
elif [[ "$MODE" = from-docker ]]; then
  # 由 docker 布局的 .env 改写（应用密钥原样沿用）；备份解密私钥等 secrets/ 拷过来（已有的不覆盖），
  # docker 的 .env 里没有备份键的才生成新的 age 密钥
  if [[ -d "$DOCKER_DIR/secrets" ]]; then
    install -d -m 0700 "$INSTALL_DIR/secrets"
    cp -a -n "$DOCKER_DIR/secrets/." "$INSTALL_DIR/secrets/"
  fi
  install -d -m 0755 "$INSTALL_DIR/deploy"
  (umask 077 && fd_render_env "$FD_DOCKER_ENV" >"$ENV_FILE.next") || die "改写 .env 失败"
  mv -f -- "$ENV_FILE.next" "$ENV_FILE"
  grep -q '^AEGIS_BACKUP_AGE_RECIPIENT=' "$ENV_FILE" || AGE_RECIPIENT="$(native_ensure_age_key "$AGE_KEY")"
  mapfile -t layout_lines < <(native_layout_env_lines "$INSTALL_DIR" "$AGE_RECIPIENT")
  native_env_append_missing "$ENV_FILE" "${layout_lines[@]}"
  say "  直装的 .env 已由 docker 布局的改写：$ENV_FILE（应用密钥原样沿用）"
else
  # 升级：老 .env 没有布局与加密备份的键，缺了才追加
  mapfile -t layout_lines < <(native_layout_env_lines "$INSTALL_DIR" "$AGE_RECIPIENT")
  layout_lines+=("AEGIS_MIGRATION_DATABASE_URL=postgres://postgres:${PG_SUPER_PASS}@127.0.0.1:${PG_PORT}/aegis?sslmode=disable")
  native_env_append_missing "$ENV_FILE" "${layout_lines[@]}"
  [[ -z "$NATIVE_ENV_ADDED" ]] || say "  .env 补上了新键：$NATIVE_ENV_ADDED（原有的行没动）"
fi

# 迁移只走官方 migrate.sh（它带 PGOPTIONS 保护参数），预检走 check-migrations.sh；
# 备份、校验、恢复、psql、收窄运行角色与 docker 布局同一套脚本（按 .env 的布局各自连库）。
# 健康巡检经 psql.sh 查库
install -m 0755 "$SCRIPT_DIR/healthcheck.sh" "$INSTALL_DIR/deploy/healthcheck.sh"
cp -f "$SCRIPT_DIR/migrate.sh" "$SCRIPT_DIR/platform.sh" "$SCRIPT_DIR/configure-app-role.sql" "$SCRIPT_DIR/legacy-privilege-repair.sql" "$SCRIPT_DIR/check-migrations.sh" "$SCRIPT_DIR/render-nginx.sh" "$SCRIPT_DIR/edge-tls.sh" "$SCRIPT_DIR/update-cloudflare-realip.sh" "$SCRIPT_DIR/nginx-aegis.conf" "$SCRIPT_DIR/admin-url.sh" "$SCRIPT_DIR/MIGRATION-RUNBOOK.md" "$INSTALL_DIR/deploy/" 2>/dev/null || true
chmod 0755 "$INSTALL_DIR/deploy/migrate.sh" "$INSTALL_DIR/deploy/check-migrations.sh" "$INSTALL_DIR/deploy/render-nginx.sh" "$INSTALL_DIR/deploy/edge-tls.sh" "$INSTALL_DIR/deploy/update-cloudflare-realip.sh" "$INSTALL_DIR/deploy/admin-url.sh" 2>/dev/null || true
for f in backup-postgres.sh verify-backup.sh restore-postgres.sh psql.sh bootstrap.sh; do
  install -m 0755 "$SCRIPT_DIR/$f" "$INSTALL_DIR/deploy/$f" || die "发布目录缺少 deploy/$f"
done
[[ ! -f "$SCRIPT_DIR/backup-webdav.example.json" ]] \
  || install -m 0644 "$SCRIPT_DIR/backup-webdav.example.json" "$INSTALL_DIR/deploy/backup-webdav.example.json"
GOOSE_BIN="$RELEASE_BIN/goose"
# 迁移 DSN 用 postgres 超级用户（00010 等迁移需绕过 RLS）：老的 .env 里没有这一行时上面已经追加进 .env。
# 不再 export 给子进程——它带着超级用户口令，会经 install-lib.sh 的 env(1) 进命令行参数
# 早期版本把「写入者已停」写死在 .env 里，手工跑 migrate.sh 时也会被当成已停服。
# 不替人改 .env（那里是口令与密钥），只提醒；本脚本自己只在真的停服之后才递交这份声明。
if [[ "$MODE" = upgrade ]] && grep -q '^PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=' "$ENV_FILE"; then
  say "  ! $ENV_FILE 里有 PANDORA_STOPPED_WRITER_UPGRADE_APPROVED：它让每次手工跑 migrate.sh 都声称写入者已停，建议删掉这一行"
fi
# --from-docker：停 docker 的写入者 → 导出 → 建角色、恢复 → 跑迁移的超级用户改成 postgres → 两边指纹逐项核对。
# 停服从这里开始，到直装的网关通过健康检查为止；中途任何失败都由 fd_abort 把 docker 布局拉回来
if [[ "$MODE" = from-docker ]]; then
  fd_state_set state=prepared started="$(date +%s)"
  say "  停 docker 布局的写入者（从这里起面板暂停服务，直到直装接管）"
  fd_stop_docker_writers
  fd_state_set backup_timer="$FD_BACKUP_TIMER_WAS_ACTIVE"
  install -d -o root -g root -m 0700 "$NATIVE_BACKUP_DIR"
  FD_DUMP="$NATIVE_BACKUP_DIR/from-docker-$(date +%Y%m%d-%H%M%S).dump"
  (umask 077 && fd_docker_pg pg_dump -d "$FD_DOCKER_PG_DB" -Fc >"$FD_DUMP") \
    || die "从 docker 布局导出失败（pg_dump），没有切换"
  [[ -s "$FD_DUMP" ]] && pg_restore --list <"$FD_DUMP" >/dev/null \
    || die "导出的文件读不出目录（pg_restore --list），没有切换"
  fd_state_set dump="$FD_DUMP"
  say "  已导出 docker 布局的库：$FD_DUMP（$(du -h "$FD_DUMP" | cut -f1)，含属主与权限）"
  # 角色是集群级的：docker 那边的非系统角色先在直装集群里建好（属性照搬），跑迁移的那个用户也要有，
  # 恢复时 ALTER … OWNER TO 才不失败
  roles_sql="$(fd_roles_plan | fd_roles_sql)" || die "整理要搬的角色失败"
  if [[ "$FD_DOCKER_PG_USER" != postgres ]]; then
    roles_sql+=$'\n'"SELECT 'CREATE ROLE $FD_DOCKER_PG_USER NOLOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '$FD_DOCKER_PG_USER') \\gexec"
  fi
  printf '%s\n' "$roles_sql" | native_pg_peer -p "$PG_PORT" -d postgres >/dev/null \
    || die "在直装集群里建角色失败，没有切换"
  (cd / && runuser -u postgres -- pg_restore -p "$PG_PORT" -d aegis --exit-on-error --single-transaction) <"$FD_DUMP" \
    || die "恢复到直装的库失败（pg_restore，整体回滚了），没有切换"
  # docker 那边跑迁移的超级用户是 POSTGRES_USER，直装是 postgres：它名下的对象（SECURITY DEFINER 函数要
  # 以超级用户身份执行）转给 postgres；库本身还归 aegis，与全新直装一致
  if [[ "$FD_DOCKER_PG_USER" != postgres ]]; then
    native_reassign_in_db "$PG_PORT" aegis "$FD_DOCKER_PG_USER" postgres aegis || die "转移对象属主失败，没有切换"
  fi
  fd_fingerprint docker >"$FD_DUMP.docker.fingerprint" || die "读不出 docker 布局的库指纹，没有切换"
  fd_fingerprint native >"$FD_DUMP.native.fingerprint" || die "读不出直装的库指纹，没有切换"
  if ! diff -u "$FD_DUMP.docker.fingerprint" "$FD_DUMP.native.fingerprint" >"$FD_DUMP.fingerprint.diff"; then
    head -40 "$FD_DUMP.fingerprint.diff" >&2
    die "两边的库对不上（差异见上，全文 $FD_DUMP.fingerprint.diff），没有切换"
  fi
  say "  核对通过：迁移水位、$(grep -c '^rel ' "$FD_DUMP.native.fingerprint") 个表/视图/序列的行数与权限、函数、策略、触发器逐项一致"
  fd_state_set state=restored
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
# （--from-docker 刚导出的那份就是迁移前的备份）
if [[ "$fresh_db" = f && "$MODE" != from-docker ]]; then
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
  *) [[ "$MODE" != from-docker ]] || die "直装的库迁移失败（输出见上），没有切换"
     die "迁移失败, 见上" ;;
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

# PostgreSQL 与 Valkey 的加固（RUNBOOK 第 13 章「直装的加固」）：PG 单元 drop-in；Valkey 的口令、与 docker 布局同口径的
# 配置块（只听回环、禁 FLUSHALL / FLUSHDB、不落盘、内存上限）、单元 drop-in。放在这里：外来集群检查早已通过
# （它停下时什么都没改），升级时网关在迁移前就停了、首装还没起，重启 PG 与 Valkey 不打断在线请求。
# 都已经是这样就不动、不重启。某一项起不来会撤回这一项、核实服务照原样在跑，记下来：--from-docker 立即停下回到
# docker 布局；首装与升级把服务按新版本起来之后再停下，提示原因与开关 PANDORA_SYSTEMD_HARDENING
native_load_hardening_switch "$ENV_FILE"
HARDEN_FAILED=""
native_harden_pg_unit "$PG_VERSION" "$PG_PORT" || HARDEN_FAILED+=" PostgreSQL"
[[ -z "$VK_UNIT" ]] || native_harden_valkey "$VK_UNIT" "$VK_CONF" "$VK_PASS" || HARDEN_FAILED+=" $VK_UNIT"
if [[ -n "$HARDEN_FAILED" && "$MODE" = from-docker ]]; then
  die "加固没成功（${HARDEN_FAILED# }，原因与办法见上），回到 docker 布局"
fi

# ── 5. 程序与 systemd ─────────────────────────────────
say "[5/6] 安装程序与 systemd 服务"
if [[ "$MODE" = from-docker ]]; then
  # 直装与 docker 布局的单元同名：覆盖之前存一份，切换失败时原样放回
  FD_UNITS_BACKUP="$NATIVE_BACKUP_DIR/from-docker-units-$(date +%Y%m%d-%H%M%S)"
  fd_save_units
  fd_state_set units_backup="$FD_UNITS_BACKUP"
  FD_UNITS_SWAPPED=1
fi
cp -f "$RELEASE_BIN"/* "$INSTALL_DIR/bin/"
chmod 0755 "$INSTALL_DIR/bin/"*
# 节点端一键安装下发的二进制（aegis-node 从 .env 的 PANDORA_PDND_DIST_DIR 读）：与程序一起、迁移成功之后才换
install -d -m 0755 "$INSTALL_DIR/pdnd-dist"
cp -f "$RELEASE_ROOT"/pdnd-dist/* "$INSTALL_DIR/pdnd-dist/"
chmod 0755 "$INSTALL_DIR/pdnd-dist/"*
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
if [[ -n "$HARDEN_FAILED" ]]; then
  die "程序已装好，服务已按新版本起来，但 ${HARDEN_FAILED# } 的加固没生效（已撤回、核实在跑；原因与办法见上）。处理后重跑本脚本（按升级处理），或用 PANDORA_SYSTEMD_HARDENING=0 明确关掉"
fi
# --from-docker：直装的三个网关 /healthz 都 200 才算接管；接管之后停 Docker 容器
if [[ "$MODE" = from-docker ]]; then
  fd_gateways_healthy || die "直装的网关没通过健康检查（journalctl -u aegis-public -n 50；tail /var/log/aegis/*.log），回到 docker 布局"
  FD_CUTOVER=1
  fd_state_set state=cutover
  say "  直装已接管（三个网关 /healthz 200）"
  fd_finalize
fi
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
case "$MODE" in
  install) say " Pandora 安装完成" ;;
  upgrade) say " Pandora 升级完成" ;;
  from-docker) say " Pandora 已从 Docker 布局迁到直装" ;;
esac
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
if [[ "$MODE" = from-docker ]]; then
  say ""
  say " 从 Docker 迁过来："
  say "   迁移前的库：$FD_DUMP（未加密的 pg_dump，含属主与权限；用完自己删）"
  say "   Docker 的容器已停，卷与 $DOCKER_DIR 都还在；原来的 systemd 单元存在 $FD_UNITS_BACKUP"
  say "   跑稳之后（建议观察一天，做一份新的加密备份并用 verify-backup.sh 校验）再由你决定删 Docker："
  say "     cd $DOCKER_DIR/deploy && docker compose --env-file .env.migrated-to-native down -v   # 删容器与卷，不可恢复"
  say "     docker ps -a 里没有别的容器时：systemctl disable --now docker.service docker.socket containerd.service"
  say "   删之前想退回 Docker（切换之后在直装上写入的数据不会带回去）："
  say "     systemctl stop ${SERVICES[*]}"
  say "     mv $DOCKER_DIR/deploy/.env.migrated-to-native $DOCKER_DIR/deploy/.env"
  say "     cp -a $FD_UNITS_BACKUP/aegis-* /etc/systemd/system/ && systemctl daemon-reload"
  say "     (cd $DOCKER_DIR/deploy && docker compose start) && systemctl start ${SERVICES[*]}"
  say "     mv $INSTALL_DIR/deploy/.env $INSTALL_DIR/deploy/.env.retired   # 免得 install.sh 再把这台认成直装"
  say "     mv $FD_STATE_FILE $FD_STATE_FILE.retired   # 状态文件一起改名；以后想再迁，直接跑 --from-docker"
fi
say "═══════════════════════════════════════════"
[[ "$HEALTH_OK" == 1 ]] || die "部分服务未启动, 检查日志: journalctl -u aegis-public"
