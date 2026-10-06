#!/usr/bin/env bash
# [INPUT]: 依赖面板主机上安装链写下的 deploy/.env（只读解析，不 source），依赖 docker（install.sh 的 Docker 数据基座）或 runuser / systemctl（install-native.sh 的直装 PG18 与 Valkey）
# [OUTPUT]: 被 source 的函数：lt_init（定下 LT_ENV_PATH 与 LT_MODE）、lt_init_env（只定 LT_ENV_PATH）、lt_env、lt_psql、lt_pg_restart、lt_valkey、lt_die、lt_say、lt_require_root、lt_require_linux、lt_stamp
# [POS]: tools/loadtest/scripts 的公共段：找 .env、判定数据基座是 Docker 还是直装、给出以超级用户跑 psql 与带口令跑 valkey-cli 的统一入口；pgstat.sh、snapshot-mem.sh、grab-pprof.sh source 它
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
#
# 两种生产布局（与 deploy/ 的安装链一致）：
#   docker  install.sh：/opt/aegispanel，容器 aegis-postgres / aegis-valkey，POSTGRES_USER 是
#           容器里的超级用户，容器内本地连接也要口令（initdb 用了 --auth-local=scram-sha-256）
#   native  install-native.sh：/opt/pandora，Debian 的 postgresql@<版本>-main，系统用户 postgres
#           经 peer 认证即超级用户；Valkey（或 Redis）是系统服务，口令写在 .env 的 VALKEY_PASSWORD
# 口令只经环境变量传给子进程（PGPASSWORD、REDISCLI_AUTH），从不出现在命令行参数里。

lt_die() { printf 'loadtest: %s\n' "$*" >&2; exit 1; }
lt_say() { printf '==> %s\n' "$*" >&2; }

lt_require_linux() { [[ -r /proc/self/stat ]] || lt_die "只能在 Linux 面板主机上运行（需要 /proc）"; }
lt_require_root() { [[ "$(id -u)" -eq 0 ]] || lt_die "需要 root：.env 是 0600，且要读其他用户进程的 /proc 与重启数据库"; }

# lt_stamp 产物文件名用的 UTC 时间戳
lt_stamp() { date -u +%Y%m%dT%H%M%SZ; }

# lt_init 在调用方的主 shell 里定下两个全局量（不能放进 $(...)，否则 lt_die 只退出子 shell）：
#   LT_ENV_PATH  面板的 deploy/.env；LT_ENV_FILE 可覆盖，缺省按两种安装布局找
#   LT_MODE      数据基座 docker 或 native；LT_DATA_MODE 可覆盖
lt_init() {
  lt_init_env
  LT_MODE="${LT_DATA_MODE:-}"
  if [[ -z "$LT_MODE" ]]; then
    if command -v docker >/dev/null 2>&1 \
      && [[ "$(docker inspect -f '{{.State.Running}}' aegis-postgres 2>/dev/null)" == true ]]; then
      LT_MODE=docker
    elif command -v pg_lsclusters >/dev/null 2>&1; then
      LT_MODE=native
    else
      lt_die "既没有运行中的 aegis-postgres 容器，也没有 pg_lsclusters，判断不了数据基座（可用 LT_DATA_MODE 指定）"
    fi
  fi
  [[ "$LT_MODE" == docker || "$LT_MODE" == native ]] || lt_die "LT_DATA_MODE 只能是 docker 或 native"
  lt_say "数据基座：$LT_MODE，配置：$LT_ENV_PATH"
}

# lt_init_env 只定 LT_ENV_PATH，给不碰数据库的脚本（grab-pprof.sh）
lt_init_env() {
  local candidate
  LT_ENV_PATH="${LT_ENV_FILE:-}"
  if [[ -z "$LT_ENV_PATH" ]]; then
    for candidate in /opt/aegispanel/deploy/.env /opt/pandora/deploy/.env; do
      if [[ -r "$candidate" ]]; then
        LT_ENV_PATH="$candidate"
        break
      fi
    done
  fi
  [[ -n "$LT_ENV_PATH" ]] || lt_die "找不到面板的 deploy/.env（试过 /opt/aegispanel 与 /opt/pandora），可用 LT_ENV_FILE 指定"
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
  local db user
  db="$(lt_env POSTGRES_DB aegis)"
  case "$LT_MODE" in
    docker)
      user="$(lt_env POSTGRES_USER aegis)"
      PGPASSWORD="$(lt_env POSTGRES_PASSWORD)" docker exec -i -e PGPASSWORD aegis-postgres \
        psql -X -q -v ON_ERROR_STOP=1 -U "$user" -d "$db" "$@"
      ;;
    native)
      # peer 认证：postgres 系统用户即超级用户；端口取 .env（与 docker 并存过的主机可能不是 5432）
      runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -p "$(lt_env POSTGRES_PORT 5432)" -d "$db" "$@"
      ;;
  esac
}

# lt_pg_service 直装模式下 PG 的 systemd 单元名，判定与 install-native.sh 相同
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
  case "$LT_MODE" in
    docker) docker restart aegis-postgres >/dev/null ;;
    native)
      service="$(lt_pg_service)"
      systemctl restart "$service"
      ;;
  esac
  for _ in $(seq 1 60); do
    if echo 'SELECT 1' | lt_psql -tA >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  lt_die "PostgreSQL 重启后 60 秒仍连不上"
}

# lt_valkey ARGS...：带口令跑 valkey-cli（直装且只有 redis-cli 时用它）
lt_valkey() {
  local cli
  case "$LT_MODE" in
    docker)
      REDISCLI_AUTH="$(lt_env VALKEY_PASSWORD)" docker exec -i -e REDISCLI_AUTH aegis-valkey valkey-cli "$@"
      ;;
    native)
      cli=valkey-cli
      command -v "$cli" >/dev/null 2>&1 || cli=redis-cli
      command -v "$cli" >/dev/null 2>&1 || lt_die "没有 valkey-cli 或 redis-cli"
      REDISCLI_AUTH="$(lt_env VALKEY_PASSWORD)" "$cli" -h 127.0.0.1 -p "$(lt_env VALKEY_PORT 6379)" "$@"
      ;;
  esac
}
