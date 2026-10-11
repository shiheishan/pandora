#!/usr/bin/env bash
# 面板机的布局只有一种（deploy/install.sh 直装）：/opt/pandora，Debian 的 postgresql@<版本>-main，
# 系统用户 postgres 经 peer 认证即超级用户；Valkey（Debian 12 上是 Redis）是系统服务，口令写在 .env 的
# VALKEY_PASSWORD。口令只经环境变量传给子进程（REDISCLI_AUTH），从不出现在命令行参数里。

lt_die() { printf 'loadtest: %s\n' "$*" >&2; exit 1; }
lt_say() { printf '==> %s\n' "$*" >&2; }

lt_require_linux() { [[ -r /proc/self/stat ]] || lt_die "只能在 Linux 面板主机上运行（需要 /proc）"; }
lt_require_root() { [[ "$(id -u)" -eq 0 ]] || lt_die "需要 root：.env 是 0600，且要读其他用户进程的 /proc 与重启数据库"; }

# lt_stamp 产物文件名用的 UTC 时间戳
lt_stamp() { date -u +%Y%m%dT%H%M%SZ; }

# lt_init 在调用方的主 shell 里定下 LT_ENV_PATH 并核对本机有直装的 PostgreSQL
# （不能放进 $(...)，否则 lt_die 只退出子 shell）
lt_init() {
  lt_init_env
  command -v pg_lsclusters >/dev/null 2>&1 || lt_die "没有 pg_lsclusters：这台不是 deploy/install.sh 直装的面板机"
  lt_say "配置：$LT_ENV_PATH"
}

# lt_init_env 只定 LT_ENV_PATH（面板的 deploy/.env，LT_ENV_FILE 可覆盖），给不碰数据库的脚本（grab-pprof.sh）
lt_init_env() {
  LT_ENV_PATH="${LT_ENV_FILE:-/opt/pandora/deploy/.env}"
  [[ -e "$LT_ENV_PATH" ]] || lt_die "找不到面板的 $LT_ENV_PATH（可用 LT_ENV_FILE 指定）"
  [[ -r "$LT_ENV_PATH" ]] || lt_die "$LT_ENV_PATH 不可读（.env 是 0600，用 root 跑）"
}

# lt_env KEY [DEFAULT] 不 source 地读 .env 的单个键（与 deploy/public-base-url.sh 的
# pandora_env_file_value 同一写法：最后一次出现为准，去掉行尾 CR）；先调 lt_init
lt_env() {
  local value
  value="$(awk -F= -v key="$1" '$1 == key { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$LT_ENV_PATH")"
  printf '%s\n' "${value:-${2:-}}"
}

# lt_psql [PSQL 参数...]：以超级用户连面板库，SQL 从标准输入读，结果写标准输出
# （-X 不读 psqlrc，出错即停）
lt_psql() {
  local db
  db="$(lt_env POSTGRES_DB aegis)"
  # peer 认证：postgres 系统用户即超级用户；端口取 .env（集群端口由 install.sh 写入，不一定是 5432）
  runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -p "$(lt_env POSTGRES_PORT 5432)" -d "$db" "$@"
}

# lt_pg_service PG 的 systemd 单元名，判定与 install.sh 相同
lt_pg_service() {
  local version
  version="$(find /usr/lib/postgresql -mindepth 1 -maxdepth 1 -type d -printf '%f\n' 2>/dev/null | sort -V | tail -1)"
  [[ -n "$version" ]] || lt_die "找不到 /usr/lib/postgresql/<版本>"
  if [[ -f "/etc/postgresql/${version}/main/postgresql.conf" ]]; then
    printf 'postgresql@%s-main\n' "$version"
  else
    printf 'postgresql\n'
  fi
}

# lt_pg_restart 重启 PostgreSQL 并等它重新接受连接（最多 60 秒）
lt_pg_restart() {
  local service _
  service="$(lt_pg_service)"
  systemctl restart "$service"
  for _ in $(seq 1 60); do
    if echo 'SELECT 1' | lt_psql -tA >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  lt_die "PostgreSQL 重启后 60 秒仍连不上"
}

# lt_valkey ARGS...：带口令跑 valkey-cli（只有 redis-cli 时用它）
lt_valkey() {
  local cli=valkey-cli
  command -v "$cli" >/dev/null 2>&1 || cli=redis-cli
  command -v "$cli" >/dev/null 2>&1 || lt_die "没有 valkey-cli 或 redis-cli"
  REDISCLI_AUTH="$(lt_env VALKEY_PASSWORD)" "$cli" -h 127.0.0.1 -p "$(lt_env VALKEY_PORT 6379)" "$@"
}
