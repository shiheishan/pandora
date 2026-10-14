#!/usr/bin/env bash
# install.sh 的常量与函数库（新栈）：路径与配置、PostgreSQL、Redis、发布物、服务与收尾。
# 只定义，不执行；由 install.sh 在校验发布目录后 source，桩测试也可直接 source。

#---- 1. 常量与小工具
SB="${PANDORA_INSTALL_SANDBOX:-}"
INSTALL_DIR="$SB/opt/pandora"
ETC_DIR="$SB/etc/pandora"
CONFIG_FILE="$ETC_DIR/config.yaml"
SECRETS_FILE="$ETC_DIR/secrets.env"
DEPLOY_ENV="$ETC_DIR/deploy.env"
# 备份加密用的 age 私钥：BACKUP_AGE_KEY 是本机读写的路径，BACKUP_AGE_KEY_PATH 是写进 deploy.env 的真实路径
BACKUP_AGE_KEY="$ETC_DIR/backup-age.key"
BACKUP_AGE_KEY_PATH=/etc/pandora/backup-age.key
LOG_DIR="$SB/var/log/pandora"
BACKUP_DIR="$SB/var/backups/pandora"
SYSTEMD_DIR="$SB/etc/systemd/system"
PG_ETC="$SB/etc/postgresql"
REDIS_CONF="$SB/etc/redis/redis.conf"
LOGROTATE_DIR="$SB/etc/logrotate.d"
CGROUP_ROOT="$SB/sys/fs/cgroup"
IF_INET6="$SB/proc/net/if_inet6"

PANDORA_USER=pandora
PG_MAJOR=18
DB_NAME=pandora
REDIS_UNIT=redis-server
SERVICES=(pandora-serve pandora-worker)
UNITS=(pandora-serve.service pandora-worker.service pandora-backup.service pandora-backup.timer
  pandora-health.service pandora-health.timer pandora-tls-renew.service pandora-tls-renew.timer)
HOST_SCRIPTS=(admin-url.sh bootstrap.sh render-nginx.sh backup-postgres.sh verify-backup.sh restore-postgres.sh
  healthcheck.sh edge-tls.sh update-cloudflare-realip.sh pg-revive.sh)
HOST_DATA=(configure-app-role.sql nginx-pandora.conf backup-webdav.example.json RUNBOOK.md)
DROPIN=pandora.conf

say() { printf '\033[1;32m%s\033[0m\n' "$*"; }
die() { printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "缺少 $1"; }

pandora_sandboxed() { [ -n "$SB" ]; }

pandora_own() {
  chown "$1" "${@:2}"
}

#---- 2. 读写 KEY=VALUE 文件（秘密文件，绝不 source / eval）
pandora_env_value() {
  [ -f "$1" ] || { printf '\n'; return 0; }
  awk -F= -v key="$2" '$1 == key { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$1"
}

pandora_export_env_file() {
  local f="$1" line k v
  [ -f "$f" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^[A-Z][A-Z0-9_]*= ]] || continue
    k="${line%%=*}"
    v="${line#*=}"
    export "$k=$v"
  done <"$f"
}

pandora_env_append_missing() {
  local env_file="$1" line tmp mode added=() orig_mode
  shift
  PANDORA_ENV_ADDED=""
  [ -f "$env_file" ] || die "找不到 $env_file"
  orig_mode="$(stat -c %a -- "$env_file")"
  for line in "$@"; do
    grep -q "^${line%%=*}=" "$env_file" || added+=("$line")
  done
  [ "${#added[@]}" -gt 0 ] || return 0
  tmp="$(mktemp "$(dirname -- "$env_file")/env.tmp.XXXXXX")"
  chmod 0600 "$tmp"
  {
    cat "$env_file"
    printf '\n# install.sh 升级时补上的新键（原有的行没动）\n'
    printf '%s\n' "${added[@]}"
  } >"$tmp"
  chmod "$orig_mode" "$tmp"
  mv -f -- "$tmp" "$env_file"
  for line in "${added[@]}"; do
    PANDORA_ENV_ADDED="${PANDORA_ENV_ADDED:+$PANDORA_ENV_ADDED }${line%%=*}"
  done
}

pandora_gen_password() { openssl rand -hex 24; }

pandora_url_host() {
  local url="${1%/}"
  [[ "$url" =~ ^https?://([^/:]+) ]] || return 1
  printf '%s\n' "${BASH_REMATCH[1],,}"
}

# 首装生成三个配置文件，口令与密钥现生成，只经 printf（内建，不进参数表）写进 umask 077 的临时文件再改名。
# secrets.env 最后写：它在不在就是「首装 / 升级」的判据，前面任何一步失败重跑仍按首装处理。
pandora_write_new_config() {
  local public_base_url="$1" grpc_public_host="$2" pg_port="$3"
  local app_pw owner_pw redis_pw super_pw master jwt_pub jwt_admin admin_path
  local deploy_tmp config_tmp secrets_tmp grpc_host
  install -d -m 0750 "$ETC_DIR"
  pandora_own root:pandora "$ETC_DIR"
  app_pw="$(pandora_gen_password)"
  owner_pw="$(pandora_gen_password)"
  redis_pw="$(pandora_gen_password)"
  super_pw="$(pandora_gen_password)"
  master="$(openssl rand -base64 32)"
  jwt_pub="$(openssl rand -hex 32)"
  jwt_admin="$(openssl rand -hex 32)"
  admin_path="ops_$(openssl rand -hex 12)"
  grpc_host="$grpc_public_host"
  [ -n "$grpc_host" ] || grpc_host="$(pandora_url_host "$public_base_url")"
  (
    umask 077
    deploy_tmp="$(mktemp "$ETC_DIR/deploy.env.tmp.XXXXXX")"
    config_tmp="$(mktemp "$ETC_DIR/config.yaml.tmp.XXXXXX")"
    secrets_tmp="$(mktemp "$ETC_DIR/secrets.env.tmp.XXXXXX")"
    printf '%s\n' \
      "POSTGRES_PORT=$pg_port" \
      "POSTGRES_DB=pandora" \
      "POSTGRES_SUPER_PASSWORD=$super_pw" \
      "PANDORA_DATABASE_MIGRATION_URL=postgres://pandora_owner:${owner_pw}@127.0.0.1:${pg_port}/pandora?sslmode=disable" \
      "PANDORA_ADMIN_PATH=$admin_path" \
      "PANDORA_SYSTEMD_HARDENING=1" >"$deploy_tmp"
    pandora_backup_env_lines >>"$deploy_tmp"
    chmod 0600 "$deploy_tmp"
    pandora_own root:root "$deploy_tmp"
    mv -f -- "$deploy_tmp" "$DEPLOY_ENV"
    printf '%s\n' \
      '# pandora 配置（install.sh 生成）。秘密不在这里，在 /etc/pandora/secrets.env' \
      'env: production' \
      "public_base_url: $public_base_url" \
      'http:' \
      '  public:' \
      '    addr: 127.0.0.1:9000' \
      '  admin:' \
      '    addr: 127.0.0.1:9001' \
      'grpc:' \
      '  node:' \
      '    addr: ":9443"' \
      "    public_host: $grpc_host" \
      'pdnd:' \
      '  dist_dir: /opt/pandora/pdnd-dist' \
      'geoip:' \
      '  ipv4_db: /opt/pandora/geoip/ip2region_v4.xdb' \
      'backup:' \
      '  dir: /var/backups/pandora' \
      'log:' \
      '  level: info' >"$config_tmp"
    chmod 0640 "$config_tmp"
    pandora_own root:pandora "$config_tmp"
    mv -f -- "$config_tmp" "$CONFIG_FILE"
    printf '%s\n' \
      "PANDORA_DATABASE_URL=postgres://pandora_app:${app_pw}@127.0.0.1:${pg_port}/pandora?sslmode=disable" \
      "PANDORA_REDIS_URL=redis://:${redis_pw}@127.0.0.1:6379/0" \
      "PANDORA_SECRETS_MASTER_KEY=$master" \
      "PANDORA_AUTH_PUBLIC_JWT_SECRET=$jwt_pub" \
      "PANDORA_AUTH_ADMIN_JWT_SECRET=$jwt_admin" >"$secrets_tmp"
    chmod 0640 "$secrets_tmp"
    pandora_own root:pandora "$secrets_tmp"
    mv -f -- "$secrets_tmp" "$SECRETS_FILE"
  )
}

# 备份加密用的 age 密钥：没有就生成（root 0600），已有的绝不覆盖（旧备份只能用它解开）。
# 公钥（recipient）记进 BACKUP_AGE_RECIPIENT。私钥与备份在同一台机器，机器整体丢失时要另存（见收尾提示）
pandora_ensure_backup_key() {
  if [ ! -f "$BACKUP_AGE_KEY" ]; then
    (umask 077 && age-keygen -o "$BACKUP_AGE_KEY" 2>/dev/null) || die "生成备份加密密钥失败（age-keygen）"
  fi
  chmod 0600 "$BACKUP_AGE_KEY"
  pandora_own root:root "$BACKUP_AGE_KEY"
  BACKUP_AGE_RECIPIENT="$(age-keygen -y "$BACKUP_AGE_KEY")" || die "读不出备份加密密钥的公钥：$BACKUP_AGE_KEY"
  [[ "$BACKUP_AGE_RECIPIENT" == age1* ]] || die "备份加密密钥的公钥不像 age 收件人：$BACKUP_AGE_KEY"
}

# deploy.env 里备份脚本要的键（backup-postgres.sh 缺了就拒绝备份）。首装写进去，升级缺了才补，已有的值不动
pandora_backup_env_lines() {
  printf '%s\n' \
    "PANDORA_BACKUP_RETENTION_DAYS=14" \
    "PANDORA_BACKUP_AGE_RECIPIENT=$BACKUP_AGE_RECIPIENT" \
    "PANDORA_BACKUP_AGE_IDENTITY=$BACKUP_AGE_KEY_PATH"
}

pandora_ensure_backup_env() {
  local lines=()
  mapfile -t lines < <(pandora_backup_env_lines)
  pandora_env_append_missing "$DEPLOY_ENV" "${lines[@]}"
}

# 升级前核对三个配置文件是 install.sh 生成的（键齐全），并把属主与权限收回到约定值；内容一个字节不改。
pandora_require_config_files() {
  local k v missing=()
  [ -f "$DEPLOY_ENV" ] && [ -f "$SECRETS_FILE" ] && [ -f "$CONFIG_FILE" ] || die "不是 install.sh 生成的，拒绝升级（缺少配置文件）"
  for k in POSTGRES_PORT POSTGRES_SUPER_PASSWORD PANDORA_DATABASE_MIGRATION_URL PANDORA_ADMIN_PATH; do
    v="$(pandora_env_value "$DEPLOY_ENV" "$k")"
    [ -n "$v" ] || missing+=("deploy.env:$k")
  done
  for k in PANDORA_DATABASE_URL PANDORA_REDIS_URL PANDORA_SECRETS_MASTER_KEY PANDORA_AUTH_PUBLIC_JWT_SECRET PANDORA_AUTH_ADMIN_JWT_SECRET; do
    v="$(pandora_env_value "$SECRETS_FILE" "$k")"
    [ -n "$v" ] || missing+=("secrets.env:$k")
  done
  grep -q '^public_base_url:' "$CONFIG_FILE" || missing+=("config.yaml:public_base_url")
  if [ "${#missing[@]}" -gt 0 ]; then
    die "不是 install.sh 生成的，拒绝升级（缺：${missing[*]}）"
  fi
  chmod 0600 "$DEPLOY_ENV"
  pandora_own root:root "$DEPLOY_ENV"
  chmod 0640 "$CONFIG_FILE" "$SECRETS_FILE"
  pandora_own root:pandora "$CONFIG_FILE" "$SECRETS_FILE"
  chmod 0750 "$ETC_DIR"
  pandora_own root:pandora "$ETC_DIR"
}

pandora_config_public_base_url() {
  awk '/^[[:space:]]*public_base_url:[[:space:]]*/ {
    line = $0
    sub(/^[^:]*:[[:space:]]*/, "", line)
    gsub(/^["'\'']|["'\'']$/, "", line)
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
    print line
    exit
  }' "$CONFIG_FILE" 2>/dev/null
}

#---- 3. 系统包、用户、目录
pandora_install_packages() {
  local need_apt=0 codename gpg_dir list_file
  if [ ! -x "$SB/usr/lib/postgresql/18/bin/postgres" ] && [ ! -x /usr/lib/postgresql/18/bin/postgres ]; then
    need_apt=1
    gpg_dir="$SB/etc/apt/trusted.gpg.d"
    list_file="$SB/etc/apt/sources.list.d/pgdg.list"
    install -d -m 0755 "${gpg_dir%/*}" "${list_file%/*}"
    curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | install -m 0644 /dev/stdin "$gpg_dir/postgresql.asc" \
      || die "下载 PGDG 签名公钥失败"
    codename="$(. /etc/os-release && printf '%s' "${VERSION_CODENAME:-}")"
    [ -n "$codename" ] || die "读不出 VERSION_CODENAME"
    printf '%s\n' "deb http://apt.postgresql.org/pub/repos/apt ${codename}-pgdg main" >"$list_file"
  fi
  command -v redis-server >/dev/null 2>&1 || need_apt=1
  command -v age-keygen >/dev/null 2>&1 || need_apt=1
  command -v nginx >/dev/null 2>&1 || need_apt=1
  [ "$need_apt" = 1 ] || return 0
  DEBIAN_FRONTEND=noninteractive apt-get update -qq || die "apt-get update 失败"
  if [ ! -x "$SB/usr/lib/postgresql/18/bin/postgres" ] && [ ! -x /usr/lib/postgresql/18/bin/postgres ]; then
    LC_ALL=C.UTF-8 LANG=C.UTF-8 DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql-18 postgresql-client-18 \
      || die "安装 postgresql-18 失败"
  fi
  command -v redis-server >/dev/null 2>&1 || DEBIAN_FRONTEND=noninteractive apt-get install -y -qq redis-server \
    || die "安装 redis-server 失败"
  command -v nginx >/dev/null 2>&1 || DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx \
    || die "安装 nginx 失败"
  command -v age-keygen >/dev/null 2>&1 || DEBIAN_FRONTEND=noninteractive apt-get install -y -qq age \
    || die "安装 age 失败（备份加密要用）"
}

pandora_ensure_user() {
  getent passwd pandora >/dev/null 2>&1 && return 0
  useradd --system --user-group --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin pandora \
    || die "创建系统用户 pandora 失败"
}

pandora_ensure_dirs() {
  install -d -m 0755 "$INSTALL_DIR/bin" "$INSTALL_DIR/deploy" "$INSTALL_DIR/pdnd-dist" "$INSTALL_DIR/geoip"
  pandora_own root:root "$INSTALL_DIR/bin" "$INSTALL_DIR/deploy" "$INSTALL_DIR/pdnd-dist" "$INSTALL_DIR/geoip"
  install -d -m 0750 "$ETC_DIR"
  pandora_own root:pandora "$ETC_DIR"
  install -d -m 0750 "$LOG_DIR"
  pandora_own pandora:pandora "$LOG_DIR"
  install -d -m 0750 "$BACKUP_DIR"
  pandora_own root:pandora "$BACKUP_DIR"
  install -d -m 0755 "$SB/var/www/pandora-acme"
  pandora_own root:root "$SB/var/www/pandora-acme"
}

#---- 4. PostgreSQL
pandora_pg_peer() { (cd / && runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 "$@"); }

pandora_pg_cluster_port() {
  pg_lsclusters 2>/dev/null \
    | awk -v v="$1" '$1 == v && $2 == "main" && $4 ~ /^online/ { print $3; found = 1; exit } END { exit !found }'
}

pandora_ensure_pg_cluster() {
  local ver="$1" _
  if ! pg_lsclusters 2>/dev/null | awk -v v="$ver" '$1 == v && $2 == "main" { found = 1 } END { exit !found }'; then
    say "  初始化 PostgreSQL ${ver}/main 集群"
    LC_ALL=C pg_createcluster --locale=C.UTF-8 --encoding=UTF8 "$ver" main 2>&1 | tail -3 \
      || die "PostgreSQL ${ver}/main 集群初始化失败"
  fi
  systemctl reset-failed "postgresql@${ver}-main" 2>/dev/null || true
  systemctl start "postgresql@${ver}-main" 2>/dev/null || true
  pandora_wait_pg_online "$ver" || die "PostgreSQL ${ver}/main 起不来：journalctl -u postgresql@${ver}-main -n 50"
}

pandora_wait_pg_online() {
  local _
  for _ in $(seq 1 "${2:-15}"); do
    pandora_pg_cluster_port "$1" >/dev/null && return 0
    sleep 1
  done
  return 1
}

pandora_pg_conf_dir() { printf '%s/%s/main\n' "$PG_ETC" "$1"; }
pandora_pg_tuning_file() { printf '%s/conf.d/pandora.conf\n' "$(pandora_pg_conf_dir "$1")"; }

pandora_check_pg_conf_include() {
  local dir conf
  dir="$(pandora_pg_conf_dir "$1")"
  conf="$dir/postgresql.conf"
  [ -f "$conf" ] || die "找不到 $conf，停下（什么都还没改）"
  awk -v q="'" -v abs="$dir/conf.d" '
    /^[[:space:]]*include_dir([[:space:]=]|$)/ {
      line = $0
      sub(/^[[:space:]]*include_dir[[:space:]]*(=[[:space:]]*)?/, "", line)
      if (substr(line, 1, 1) != q) next
      line = substr(line, 2)
      end = index(line, q)
      if (end == 0) next
      v = substr(line, 1, end - 1); rest = substr(line, end + 1)
      sub(/\/$/, "", v)
      if ((v == "conf.d" || v == abs) && rest ~ /^[[:space:]]*(#.*)?$/) found = 1
    }
    END { exit !found }' "$conf" \
    || die "$conf 里没有生效的 include_dir = 'conf.d'，停下（什么都还没改）"
}

pandora_write_file() {
  install -d -m 0755 "${1%/*}"
  cat >"$1.next" && chmod 0644 "$1.next" && mv -f -- "$1.next" "$1"
}

pandora_dropin_file() { printf '%s\n' "$SYSTEMD_DIR/$1.d/$DROPIN"; }
pandora_write_dropin() {
  local file
  file="$(pandora_dropin_file "$1")"
  install -d -m 0755 "${file%/*}"
  printf '%s\n' "$2" >"$file.next" && chmod 0644 "$file.next" && mv -f -- "$file.next" "$file"
}
pandora_remove_dropin() { rm -f -- "$(pandora_dropin_file "$1")"; }
pandora_apply_dropin() {
  local file
  file="$(pandora_dropin_file "$1")"
  if [ -f "$file" ] && [ "$(cat "$file")" = "$2" ]; then return 1; fi
  pandora_write_dropin "$1" "$2"
}
pandora_restore_dropin() {
  if [ "$2" = 1 ]; then pandora_write_dropin "$1" "$3"; else pandora_remove_dropin "$1"; fi
}

pandora_pg_hardening_dropin() {
  cat <<'UNIT'
User=postgres
Group=postgres
ExecStartPre=+/usr/bin/install -d -m 2775 -o postgres -g postgres /run/postgresql
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
PrivateTmp=yes
PrivateDevices=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/postgresql -/var/log/postgresql /run/postgresql
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
IPAddressDeny=any
IPAddressAllow=localhost
SystemCallArchitectures=native
SystemCallErrorNumber=EPERM
SystemCallFilter=~@clock @cpu-emulation @debug @module @mount @obsolete @raw-io @reboot @swap
UNIT
}

pandora_pg_dropin() {
  printf '%s\n' '# pandora（install.sh 生成）：资源约束一直在，隔离随 PANDORA_SYSTEMD_HARDENING' '[Service]' \
    'MemoryMax=512M' 'MemorySwapMax=0' 'OOMPolicy=continue' 'ExecStopPost=+/opt/pandora/deploy/pg-revive.sh %i'
  [ "${PANDORA_HARDENING:-1}" != 1 ] || pandora_pg_hardening_dropin
}

pandora_unit_failure_hint() {
  if [ "$(systemctl show -p ExecMainStatus --value "$1" 2>/dev/null)" = 226 ] \
      || journalctl -u "$1" -n 80 --no-pager 2>/dev/null | grep -Eq '226/NAMESPACE|step NAMESPACE'; then
    printf '%s\n' "原因是 226/NAMESPACE：这台主机不支持 systemd 沙箱要的挂载命名空间。确认是这样，就用 PANDORA_SYSTEMD_HARDENING=0 重跑（记进 deploy.env，之后的升级沿用）"
  else
    printf '%s\n' "原因看 journalctl -u $1 -n 50。确实要关掉这层加固，用 PANDORA_SYSTEMD_HARDENING=0 重跑（记进 deploy.env）"
  fi
}

pandora_pg_online_budget() {
  local started
  started="$(date +%s)"
  pandora_pg_peer -p "$1" -d postgres -c CHECKPOINT >/dev/null 2>&1 || true
  echo $(( 30 + 3 * ($(date +%s) - started) ))
}

# 调参文件进 conf.d、资源约束与隔离进 drop-in；两样都没变就不重启 PG。改了起不来：两样都还原成这次之前的样子、
# 再起一次；还原后在线返回 1（install.sh 记进 HARDEN_FAILED，服务照常装完再报错），还原后仍起不来直接 die。
pandora_apply_pg_config() {
  local ver="$1" port="$2" src="$3" unit="postgresql@$1-main.service"
  local conf dropin had_conf=0 prev_conf="" had_dropin=0 prev_dropin="" conf_changed=0 dropin_changed=0
  local what budget hint
  conf="$(pandora_pg_tuning_file "$ver")"
  dropin="$(pandora_dropin_file "$unit")"
  [ -f "$src" ] || die "找不到 PostgreSQL 调参文件 $src"
  if [ -f "$conf" ]; then had_conf=1; prev_conf="$(cat "$conf")"; fi
  if [ -f "$dropin" ]; then had_dropin=1; prev_dropin="$(cat "$dropin")"; fi
  if [ "$had_conf" != 1 ] || ! cmp -s "$src" "$conf"; then
    pandora_write_file "$conf" <"$src" || die "写 $conf 失败"
    conf_changed=1
  fi
  if pandora_apply_dropin "$unit" "$(pandora_pg_dropin)"; then dropin_changed=1; fi
  [ "$conf_changed" = 1 ] || [ "$dropin_changed" = 1 ] || return 0
  what=""
  [ "$conf_changed" != 1 ] || what="调参 $conf"
  if [ "$dropin_changed" = 1 ]; then
    [ -z "$what" ] || what+="、"
    what+="drop-in $(pandora_dropin_file "$unit")"
  fi
  budget="$(pandora_pg_online_budget "$port")"
  systemctl daemon-reload
  systemctl restart "$unit" 2>/dev/null || true
  if pandora_wait_pg_online "$ver" "$budget"; then
    say "  PostgreSQL $ver/main：已应用$what，重启后在线"
    return 0
  fi
  if [ "$dropin_changed" = 1 ]; then hint="$(pandora_unit_failure_hint "$unit")"; else hint="原因看 journalctl -u $unit -n 50。"; fi
  [ "$conf_changed" != 1 ] || hint+="这次也换了 $conf：若是其中某个参数这台机器起不来，日志里的 FATAL 行会写出是哪个"
  if [ "$had_conf" = 1 ]; then
    printf '%s\n' "$prev_conf" | pandora_write_file "$conf" || die "PostgreSQL $ver/main 起不来，写回之前的 $conf 也失败"
  else
    rm -f -- "$conf"
  fi
  pandora_restore_dropin "$unit" "$had_dropin" "$prev_dropin"
  systemctl daemon-reload
  systemctl reset-failed "$unit" 2>/dev/null || true
  systemctl restart "$unit" 2>/dev/null || true
  if pandora_wait_pg_online "$ver" "$budget"; then
    say "  ! PostgreSQL $ver/main 应用$what后起不来；调参文件与 drop-in 已还原成这次之前的样子，核实已在线。$hint" >&2
    return 1
  fi
  die "PostgreSQL $ver/main 应用$what后起不来；调参文件与 drop-in 已还原成这次之前的样子，但 $budget 秒内仍没在线（journalctl -u $unit -n 50）。$hint"
}

pandora_swap_accounting_note() {
  if [ -f "$CGROUP_ROOT/cgroup.controllers" ] && [ -e "$CGROUP_ROOT/system.slice/memory.swap.max" ]; then
    say "  面板的服务不换出到硬盘：PostgreSQL、Redis 与 pandora-serve / pandora-worker 都带 MemorySwapMax=0（cgroup v2，swap 记账已开）"
    return 0
  fi
  say "  ! 本机 cgroup 不是 v2 或没开 swap 记账：MemorySwapMax=0 被 systemd 忽略，PostgreSQL、Redis 与面板服务仍可能被换出到交换区（不影响安装）"
}

pandora_load_hardening_switch() {
  local env_file="$DEPLOY_ENV" v="${PANDORA_SYSTEMD_HARDENING:-}" from_env=1 tmp
  if [ -z "$v" ]; then
    from_env=0
    v="$(pandora_env_value "$env_file" PANDORA_SYSTEMD_HARDENING)"
  fi
  case "${v:-1}" in
    1|on|yes|true) v=1 ;;
    0|off|no|false) v=0 ;;
    *) die "PANDORA_SYSTEMD_HARDENING 只认 1（开，缺省）或 0（关）：$v" ;;
  esac
  if [ "$from_env" = 1 ] && [ -f "$env_file" ] \
      && [ "$(pandora_env_value "$env_file" PANDORA_SYSTEMD_HARDENING)" != "$v" ]; then
    if grep -q '^PANDORA_SYSTEMD_HARDENING=' "$env_file"; then
      tmp="$(mktemp "$env_file.tmp.XXXXXX")"
      chmod 0600 "$tmp"
      PANDORA_SWITCH="$v" awk '/^PANDORA_SYSTEMD_HARDENING=/ { print "PANDORA_SYSTEMD_HARDENING=" ENVIRON["PANDORA_SWITCH"]; next } { print }' \
        "$env_file" >"$tmp" && mv -f -- "$tmp" "$env_file" || { rm -f -- "$tmp"; die "改 $env_file 的 PANDORA_SYSTEMD_HARDENING 失败"; }
    else
      pandora_env_append_missing "$env_file" "PANDORA_SYSTEMD_HARDENING=$v"
    fi
  fi
  PANDORA_HARDENING="$v"
  [ "$v" = 1 ] || say "  ! systemd 加固已按 PANDORA_SYSTEMD_HARDENING=0 关闭（记在 deploy.env）：PostgreSQL 与 Redis 没有挂载、进程、网络隔离（RUNBOOK）"
}

pandora_db_has_schema() {
  local out port="$1"
  out="$(pandora_pg_peer -p "$port" -d postgres -Atc "SELECT 1 FROM pg_catalog.pg_database WHERE datname = 'pandora'" 2>/dev/null)" || return 1
  [ "$out" = 1 ] || return 1
  out="$(pandora_pg_peer -p "$port" -d pandora -Atc \
    "SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'goose_db_version' LIMIT 1" 2>/dev/null)" || return 1
  [ "$out" = 1 ]
}

# 升级前整库 pg_dump -Fc 到备份目录（0600 root），pg_restore --list 能读才算成功；库里还没有迁移表就跳过。
pandora_pre_upgrade_dump() {
  local port="$1" f ts
  PANDORA_PRE_UPGRADE_DUMP=""
  pandora_db_has_schema "$port" || return 0
  ts="$(date +%Y%m%d-%H%M%S)"
  f="$BACKUP_DIR/pre-upgrade-$ts.dump"
  install -d -m 0750 "$BACKUP_DIR"
  pandora_own root:pandora "$BACKUP_DIR"
  # 重定向放在 umask 077 的子 shell 里面：文件一出生就是 0600，备份目录对 pandora 组可读，不能有 0644 的窗口
  if ! ( umask 077; cd / && runuser -u postgres -- pg_dump -Fc -p "$port" -d pandora >"$f" ); then
    rm -f -- "$f"
    die "升级前备份失败，什么都还没改（服务已停）"
  fi
  if ! pg_restore --list <"$f" >/dev/null 2>&1; then
    rm -f -- "$f"
    die "升级前备份失败，什么都还没改（服务已停）"
  fi
  chmod 0600 "$f"
  pandora_own root:root "$f"
  PANDORA_PRE_UPGRADE_DUMP="$f"
}

#---- 5. Redis
PANDORA_REDIS_BLOCK_BEGIN='# >>> pandora（install.sh 维护；手改会被下次安装覆盖）'
PANDORA_REDIS_BLOCK_END='# <<< pandora'

pandora_redis_version() {
  local out major minor
  out="$(redis-server --version 2>/dev/null | head -1)" || return 1
  case "$out" in Redis*) ;; *) return 1 ;; esac
  major="$(sed -n 's/.* v=\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1/p' <<<"$out")"
  minor="$(sed -n 's/.* v=\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\2/p' <<<"$out")"
  [ -n "$major" ] && [ -n "$minor" ] || return 1
  printf '%s %s\n' "$major" "$minor"
}

pandora_check_redis_version() {
  local major minor
  read -r major minor <<<"$(pandora_redis_version || true)"
  if [ -z "$major" ]; then
    die "认不出 redis-server 的版本。Pandora 要 Redis 6.0 及以上，什么都还没改"
  fi
  if [ "$major" -lt 6 ]; then
    die "redis-server 是 Redis $major.$minor，太老：Pandora 要 Redis 6.0 及以上，什么都还没改"
  fi
  say "  redis-server：Redis $major.$minor"
}

pandora_redis_bind_addrs() {
  local major minor
  read -r major minor <<<"$(pandora_redis_version || true)"
  if [ -n "$major" ] && { [ "$major" -gt 6 ] || { [ "$major" -eq 6 ] && [ "${minor:-0}" -ge 2 ]; }; }; then
    echo '127.0.0.1 -::1'
    return 0
  fi
  if [ -r "$IF_INET6" ] && awk '$1 == "00000000000000000000000000000001" && $6 == "lo" { f = 1 } END { exit !f }' "$IF_INET6"; then
    echo '127.0.0.1 ::1'
  else
    echo '127.0.0.1'
  fi
}

pandora_redis_block() {
  printf '%s\n' "$PANDORA_REDIS_BLOCK_BEGIN" \
    "bind $1" \
    'protected-mode yes' \
    'port 6379' \
    'rename-command FLUSHALL ""' \
    'rename-command FLUSHDB ""' \
    'save ""' \
    'appendonly yes' \
    'appendfsync everysec' \
    'maxmemory 256mb' \
    'maxmemory-policy noeviction' \
    "$PANDORA_REDIS_BLOCK_END"
}

pandora_set_redis_block() {
  local conf="$1" tmp
  tmp="$(mktemp "$conf.XXXXXX")"
  PANDORA_RK_END="$PANDORA_REDIS_BLOCK_END" PANDORA_RK_BLOCK="$(pandora_redis_block "$2")" awk '
    index($0, "# >>> pandora") == 1 { skip = 1; next }
    skip && $0 == ENVIRON["PANDORA_RK_END"] { skip = 0; next }
    skip { next }
    { lines[++n] = $0 }
    END {
      while (n > 0 && lines[n] == "") n--
      for (i = 1; i <= n; i++) print lines[i]
      print ""
      print ENVIRON["PANDORA_RK_BLOCK"]
    }
  ' "$conf" >"$tmp" || { rm -f -- "$tmp"; die "改 $conf 的 pandora 配置块失败"; }
  if cmp -s "$tmp" "$conf"; then rm -f -- "$tmp"; return 1; fi
  cat "$tmp" >"$conf" || { rm -f -- "$tmp"; die "写回 $conf 失败"; }
  rm -f -- "$tmp"
  return 0
}

pandora_set_redis_password() {
  local conf="$1" tmp
  printf 'requirepass %s\n' "$2" | grep -qxF -f - "$conf" && return 1
  tmp="$(mktemp "$conf.XXXXXX")"
  PANDORA_RK_PASS="$2" awk '
    /^requirepass[[:space:]]/ { if (!done) print "requirepass " ENVIRON["PANDORA_RK_PASS"]; done = 1; next }
    { print }
    END { if (!done) { print ""; print "requirepass " ENVIRON["PANDORA_RK_PASS"] } }
  ' "$conf" >"$tmp" || { rm -f -- "$tmp"; die "改 $conf 的 requirepass 失败"; }
  cat "$tmp" >"$conf" || { rm -f -- "$tmp"; die "写回 $conf 失败"; }
  rm -f -- "$tmp"
  return 0
}

pandora_redis_hardening_dropin() {
  cat <<'UNIT'
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
PrivateTmp=yes
PrivateDevices=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=-/var/lib/redis -/var/log/redis -/run/redis
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
IPAddressDeny=any
IPAddressAllow=localhost
SystemCallArchitectures=native
SystemCallErrorNumber=EPERM
SystemCallFilter=
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
UNIT
}

pandora_redis_dropin() {
  printf '%s\n' '# pandora（install.sh 生成）：资源约束一直在，隔离随 PANDORA_SYSTEMD_HARDENING' '[Service]' \
    'MemoryMax=384M' 'MemorySwapMax=0'
  [ "${PANDORA_HARDENING:-1}" != 1 ] || pandora_redis_hardening_dropin
}

pandora_redis_restart_ok() { systemctl restart "$REDIS_UNIT.service" 2>/dev/null && sleep 1 && systemctl is-active --quiet "$REDIS_UNIT.service"; }

pandora_redis_after_revert() {
  systemctl daemon-reload
  systemctl reset-failed "$REDIS_UNIT.service" 2>/dev/null || true
  if pandora_redis_restart_ok; then
    say "  ! $REDIS_UNIT 改了$2之后起不来；这一步已还原，核实 $REDIS_UNIT 照原样在跑。$3" >&2
    return 1
  fi
  die "$REDIS_UNIT 改了$2之后起不来；这一步已还原，但 $REDIS_UNIT 仍没起来（journalctl -u $REDIS_UNIT.service -n 50）。$3"
}

pandora_redis_conf_step() {
  local conf="$2" what="$3" snap hint
  shift 3
  snap="$(mktemp "$conf.pandora.XXXXXX")"
  cat "$conf" >"$snap" || { rm -f -- "$snap"; die "存 $conf 的副本失败"; }
  if ! "$@"; then rm -f -- "$snap"; return 0; fi
  if pandora_redis_restart_ok; then rm -f -- "$snap"; return 0; fi
  hint="$(pandora_unit_failure_hint "$REDIS_UNIT.service")"
  cat "$snap" >"$conf" || die "$REDIS_UNIT 改了$what之后起不来，写回 $conf 也失败（副本在 $snap）"
  rm -f -- "$snap"
  pandora_redis_after_revert "$REDIS_UNIT" "$what（$conf）" "$hint"
}

# 依次改 requirepass、pandora 配置块、drop-in；每改一样就重启 redis-server 核一次，起不来就还原这一样并返回 1。
pandora_harden_redis() {
  local conf="$1" pass="$2" unit="$REDIS_UNIT" file had=0 prev="" what hint bind
  bind="$(pandora_redis_bind_addrs)"
  if [ -f "$conf" ]; then
    pandora_redis_conf_step "$unit" "$conf" 口令 pandora_set_redis_password "$conf" "$pass" || return 1
    pandora_redis_conf_step "$unit" "$conf" "pandora 配置块" pandora_set_redis_block "$conf" "$bind" || return 1
  fi
  file="$(pandora_dropin_file "$unit.service")"
  if [ -f "$file" ]; then had=1; prev="$(cat "$file")"; fi
  pandora_apply_dropin "$unit.service" "$(pandora_redis_dropin)" || return 0
  what="drop-in $file"
  systemctl daemon-reload
  pandora_redis_restart_ok && return 0
  hint="$(pandora_unit_failure_hint "$unit.service")"
  pandora_restore_dropin "$unit.service" "$had" "$prev"
  pandora_redis_after_revert "$unit" "$what" "$hint"
}

pandora_redis_ping() {
  REDISCLI_AUTH="$1" redis-cli -h 127.0.0.1 -p 6379 ping 2>/dev/null | grep -q PONG
}

#---- 6. 发布物安装
pandora_install_host_files() {
  local root="$1" name
  for name in "${HOST_SCRIPTS[@]}"; do
    [ -f "$root/deploy/$name" ] || die "发布目录缺少 deploy/$name"
    install -m 0755 "$root/deploy/$name" "$INSTALL_DIR/deploy/$name"
  done
  for name in "${HOST_DATA[@]}"; do
    [ -f "$root/deploy/$name" ] || die "发布目录缺少 deploy/$name"
    install -m 0644 "$root/deploy/$name" "$INSTALL_DIR/deploy/$name"
  done
  if [ -f "$root/deploy/logrotate-pandora" ]; then
    [ -d "$LOGROTATE_DIR" ] || install -d -m 0755 "$LOGROTATE_DIR"
    install -m 0644 "$root/deploy/logrotate-pandora" "$LOGROTATE_DIR/pandora"
  fi
  pandora_own root:root "$INSTALL_DIR/deploy"/*
}

pandora_install_program() {
  local root="$1" mode="$2" bin="$INSTALL_DIR/bin/pandora"
  [ -f "$root/bin/pandora" ] || die "发布目录缺少 bin/pandora"
  install -m 0755 "$root/bin/pandora" "$bin.new"
  if [ "$mode" = upgrade ] && [ -f "$bin" ]; then cp -p "$bin" "$bin.prev"; fi
  mv -f -- "$bin.new" "$bin"
  pandora_own root:root "$bin"
  install -d -m 0755 "$INSTALL_DIR/pdnd-dist"
  for f in "$root"/pdnd-dist/*; do
    [ -e "$f" ] || continue
    install -m 0755 "$f" "$INSTALL_DIR/pdnd-dist/"
  done
  [ -f "$root/geoip/ip2region_v4.xdb" ] || die "发布目录缺少 geoip/ip2region_v4.xdb"
  install -m 0644 "$root/geoip/ip2region_v4.xdb" "$INSTALL_DIR/geoip/"
  [ -f "$root/deploy/release-artifact.env" ] || die "发布目录缺少 deploy/release-artifact.env"
  install -m 0644 "$root/deploy/release-artifact.env" "$INSTALL_DIR/release.env"
  pandora_own root:root "$INSTALL_DIR/pdnd-dist" "$INSTALL_DIR/geoip" "$INSTALL_DIR/release.env"
}

pandora_install_units() {
  local root="$1" u
  for u in "${UNITS[@]}"; do
    [ -f "$root/deploy/systemd/$u" ] || die "发布目录缺少 deploy/systemd/$u"
    install -m 0644 "$root/deploy/systemd/$u" "$SYSTEMD_DIR/$u"
  done
  systemctl daemon-reload
}

pandora_release_version() {
  pandora_env_value "$1" PANDORA_RELEASE_VERSION
}

#---- 7. 调 pandora 与数据库角色
# 以 pandora 用户跑 pandora 子命令：先清空继承来的环境（安装时 sudo 带进来的变量不进应用），只给 PATH、HOME、LANG
# 与 secrets.env 的键；第一个参数 yes 时另给迁移 DSN（只有 migrate 需要 pandora_owner）。秘密只在环境里，不进参数表。
pandora_cli() {
  local mig="$1"
  shift
  (
    local name kv p="$PATH" vars=()
    while IFS= read -r name; do unset "$name" 2>/dev/null || true; done < <(compgen -e)
    while read -r _ _ name; do unset -f "$name" 2>/dev/null || true; done < <(declare -Fx)
    export PATH="$p" HOME=/nonexistent LANG=C.UTF-8
    pandora_export_env_file "$SECRETS_FILE"
    if [ "$mig" = yes ]; then
      kv="$(pandora_env_value "$DEPLOY_ENV" PANDORA_DATABASE_MIGRATION_URL)"
      export PANDORA_DATABASE_MIGRATION_URL="$kv"
    fi
    exec runuser -u pandora -- "$INSTALL_DIR/bin/pandora" --config "$CONFIG_FILE" "$@"
  )
}

# 失败码要在命令那一行取：写成 if …; fi; rc=$? 时 $? 是 if 语句自己的 0，失败会被当成成功
pandora_migrate() {
  local log rc=0
  log="$(mktemp)"
  pandora_cli yes migrate up >"$log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    sed 's/^/    /' "$log"
  else
    sed 's/^/    /' "$log" >&2
  fi
  rm -f -- "$log"
  return "$rc"
}

pandora_bootstrap_db() {
  local stage="$1" log rc=0
  log="$(mktemp)"
  PANDORA_ETC_DIR="$ETC_DIR" bash "$INSTALL_DIR/deploy/bootstrap.sh" "$stage" >"$log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    sed 's/^/    /' "$log"
  else
    sed 's/^/    /' "$log" >&2
  fi
  rm -f -- "$log"
  return "$rc"
}

#---- 8. 服务、健康、防火墙
pandora_stop_services() {
  systemctl stop "${SERVICES[@]}" 2>/dev/null || true
}

pandora_start_services() {
  systemctl enable "${SERVICES[@]}" || die "systemctl enable 失败"
  if ! systemctl restart "${SERVICES[@]}"; then
    die "启动 pandora 服务失败：journalctl -u pandora-serve -n 50 --no-pager"
  fi
}

# 两个 readyz（门户 9000、后台 9001）都 200 且 worker 在跑才算健康；最多约一分钟。
pandora_services_healthy() {
  local _ code
  for _ in $(seq 1 30); do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:9000/readyz 2>/dev/null)"
    [ "$code" = 200 ] || { sleep 2; continue; }
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:9001/readyz 2>/dev/null)"
    [ "$code" = 200 ] || { sleep 2; continue; }
    systemctl is-active --quiet pandora-worker && return 0
    sleep 2
  done
  return 1
}

pandora_open_firewall() {
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
    ufw allow 80/tcp >/dev/null 2>&1 || true
    ufw allow 443/tcp >/dev/null 2>&1 || true
    ufw allow 9443/tcp >/dev/null 2>&1 || true
    return 0
  fi
  if command -v firewall-cmd >/dev/null 2>&1 && [ "$(firewall-cmd --state 2>/dev/null)" = running ]; then
    firewall-cmd --permanent --add-port=80/tcp >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-port=443/tcp >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-port=9443/tcp >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    return 0
  fi
  say "  没检测到 ufw / firewalld：云厂商安全组要放行 80、443、9443/tcp"
}

#---- 9. 管理员与收尾
# 没有可登录的管理员时建第一个：无人值守用 PANDORA_PRESET_ADMIN_EMAIL / _PW（install.sh 从标准输入读的一行），
# 交互终端现场问（不回显），都没有就留给收尾提示里的手工命令。口令只经管道给 --password-stdin，用完清空。
pandora_admin_bootstrap() {
  local email="" pw="" pw2="" attempt has_rc=0 create_rc out
  PANDORA_ADMIN_STATE=manual
  PANDORA_ADMIN_EMAIL=""
  pandora_cli no admin has-admin >/dev/null 2>&1 || has_rc=$?
  case "$has_rc" in
    0) PANDORA_ADMIN_STATE=existing; printf '    %s\n' "库里已有可登录的管理员，不再新建"; return 0 ;;
    3) ;;
    *) printf '    %s\n' "查不到现有管理员（pandora admin has-admin 退出码 $has_rc），管理员留给你手工建" >&2; return 0 ;;
  esac
  if [ -n "${PANDORA_PRESET_ADMIN_EMAIL:-}" ] && [ -n "${PANDORA_PRESET_ADMIN_PW:-}" ]; then
    create_rc=0
    out="$(printf '%s\n' "$PANDORA_PRESET_ADMIN_PW" | pandora_cli no admin create --email "$PANDORA_PRESET_ADMIN_EMAIL" --password-stdin 2>&1)" || create_rc=$?
    PANDORA_PRESET_ADMIN_PW=""
    printf '%s\n' "$out" | sed 's/^/    /'
    if [ "$create_rc" -eq 0 ]; then
      PANDORA_ADMIN_STATE=created
      PANDORA_ADMIN_EMAIL="$PANDORA_PRESET_ADMIN_EMAIL"
    fi
    return 0
  fi
  if [ "${PANDORA_ASSUME_YES:-}" = 1 ] || [ ! -t 0 ] || [ ! -t 1 ]; then
    printf '    %s\n' "未交互建管理员，收尾提示里给出手工命令"
    return 0
  fi
  printf '    %s\n' "现在创建第一个管理员（直接回车跳过，之后再手工建）。"
  for attempt in 1 2 3; do
    printf '    管理员邮箱：'
    IFS= read -r email || email=""
    email="${email//[[:space:]]/}"
    [ -n "$email" ] || { printf '    %s\n' "跳过，之后手工建"; return 0; }
    if [[ ! "$email" =~ ^[^@]+@[^@]+\.[^@]+$ ]]; then
      printf '    %s\n' "邮箱格式不对：$email" >&2
      continue
    fi
    printf '    密码（不回显）：'
    IFS= read -r -s pw || pw=""
    printf '\n    再输一次：'
    IFS= read -r -s pw2 || pw2=""
    printf '\n'
    if [ -z "$pw" ] || [ "$pw" != "$pw2" ]; then
      pw=""; pw2=""
      printf '    %s\n' "两次密码不一致或为空，重来" >&2
      continue
    fi
    pw2=""
    create_rc=0
    out="$(printf '%s\n' "$pw" | pandora_cli no admin create --email "$email" --password-stdin 2>&1)" || create_rc=$?
    pw=""
    printf '%s\n' "$out" | sed 's/^/    /'
    if [ "$create_rc" -eq 0 ]; then
      PANDORA_ADMIN_STATE=created
      PANDORA_ADMIN_EMAIL="$email"
      return 0
    fi
    printf '    %s\n' "没建成（原因见上），重来" >&2
  done
  printf '    %s\n' "三次都没建成，管理员留给你手工建" >&2
  return 0
}

pandora_setup_edge() {
  EDGE_STATE=failed
  if [ "${PANDORA_SKIP_NGINX:-}" = 1 ]; then
    EDGE_STATE=skipped
    return 0
  fi
  if ! command -v nginx >/dev/null 2>&1; then
    EDGE_STATE=no-nginx
    return 0
  fi
  local rc=0
  bash "$INSTALL_DIR/deploy/edge-tls.sh" setup || rc=$?
  case "$rc" in
    0) EDGE_STATE=trusted ;;
    3) EDGE_STATE=selfsigned ;;
    *) EDGE_STATE=failed ;;
  esac
  return 0
}

pandora_print_summary() {
  local d="$INSTALL_DIR/deploy" admin_path base url todo=() item create_cmd show_url edge
  admin_path="$(pandora_env_value "$DEPLOY_ENV" PANDORA_ADMIN_PATH)"
  base="$(pandora_config_public_base_url)"
  base="${base%/}"
  url="$base/$admin_path/"
  show_url="sudo $d/admin-url.sh"
  edge="sudo $d/edge-tls.sh"
  create_cmd="read -rsp '密码：' p && echo && printf '%s\n' \"\$p\" | sudo systemd-run --pipe --quiet --uid=pandora -p EnvironmentFile=/etc/pandora/secrets.env /opt/pandora/bin/pandora admin create --email <你的邮箱> --password-stdin; unset p"

  case "${EDGE_STATE:-skipped}" in
    trusted|selfsigned) ;;
    no-nginx) todo+=("装 nginx 才能从公网打开面板：apt-get install -y nginx 后 $edge setup") ;;
    failed) todo+=("HTTPS 边缘没配好（原因见上，nginx 保持原配置）：修好后 $edge setup") ;;
    skipped) todo+=("这次按 PANDORA_SKIP_NGINX=1 没碰 nginx；要从公网用 HTTPS 打开面板：$edge setup") ;;
    *) todo+=("切到 HTTPS：$edge setup") ;;
  esac
  if [ "${EDGE_STATE:-}" = selfsigned ]; then
    todo+=("换上正规证书（现在是自签）：确认 80/tcp 从公网可达后 $edge issue")
  fi
  if [ "${PANDORA_ADMIN_STATE:-manual}" = manual ]; then
    todo+=("创建管理员（下面这条整行复制）：" "    $create_cmd")
  fi
  if [ "${BACKUP_TIMER:-}" != enabled ]; then
    todo+=("开每日备份：sudo $d/backup-postgres.sh，再 systemctl enable --now pandora-backup.timer")
  fi
  todo+=("把备份解密私钥 $BACKUP_AGE_KEY_PATH 另存到别处：机器整体丢失时，没有它解不开任何备份")

  say ""
  if [ "${MODE:-install}" = install ]; then
    say "Pandora 安装完成"
    printf '%s\n' "  现在可以做什么："
    case "${EDGE_STATE:-}" in
      trusted|selfsigned)
        printf '    %s\n' "打开管理后台：$url" "  （这个地址就是后台入口，别外传）"
        ;;
      *)
        printf '    %s\n' "面板已在本机跑起来；HTTPS 边缘配好后从这里打开管理后台：" "  $url"
        ;;
    esac
    case "${PANDORA_ADMIN_STATE:-manual}" in
      created) printf '    %s\n' "用刚建的管理员 ${PANDORA_ADMIN_EMAIL:-} 登录，然后尽快绑定两步验证" ;;
      existing) printf '    %s\n' "用库里已有的管理员登录" ;;
    esac
  else
    say "Pandora 升级完成"
    printf '%s\n' "  现在可以做什么："
    printf '    %s\n' "面板已升级（${OLD_VERSION:-?} → ${NEW_VERSION:-?}），服务已重启并通过健康检查" \
      "管理后台地址没变（重看：$show_url）"
    [ -z "${PANDORA_PRE_UPGRADE_DUMP:-}" ] || printf '    %s\n' "升级前的数据库备份：$PANDORA_PRE_UPGRADE_DUMP（确认升级没问题后自己删）"
  fi

  printf '\n%s\n' "  还差什么："
  [ "${#todo[@]}" -gt 0 ] || printf '    %s\n' "没有了"
  for item in ${todo[@]+"${todo[@]}"}; do
    case "$item" in
      "    "*) printf '    %s\n' "$item" ;;
      *) printf '    · %s\n' "$item" ;;
    esac
  done

  printf '\n%s\n' "  常用操作："
  printf '    %s\n' \
    "重看后台地址  $show_url" \
    "查看状态      systemctl status pandora-serve pandora-worker" \
    "查看日志      journalctl -u pandora-serve -n 100" \
    "              tail -f /var/log/pandora/serve.log" \
    "重启          systemctl restart pandora-serve pandora-worker" \
    "配置文件      /etc/pandora/config.yaml（秘密在 secrets.env）" \
    "立刻备份      sudo $d/backup-postgres.sh" \
    "HTTPS 证书    $edge status" \
    "升级          新发布包放到 root 独占目录后：sudo <发布目录>/deploy/install.sh"
}
