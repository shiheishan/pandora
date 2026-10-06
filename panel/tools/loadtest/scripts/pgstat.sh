#!/usr/bin/env bash
# [INPUT]: 依赖同目录 lt-common.sh 的 lt_init / lt_psql / lt_pg_restart，依赖面板库的 PostgreSQL 超级用户
# [OUTPUT]: pg_stat_statements 的开启（ALTER SYSTEM + 重启 + CREATE EXTENSION）、清零、导出 top N 三份 CSV（总耗时、平均耗时、调用次数），以及压测后的撤销
# [POS]: tools/loadtest/scripts 的 SQL 画像采集，压测前 enable + reset、压测后 export、收尾 disable；在面板主机上以 root 运行
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
#
# 用法（面板主机，root）：
#   pgstat.sh enable --yes        写 shared_preload_libraries 并重启 PostgreSQL（网关会有几秒连库失败），建扩展
#   pgstat.sh reset               清零计数，压测开始前跑
#   pgstat.sh export DIR [N]      导出 top N（缺省 50）到 DIR/pgstat-<UTC 时间>-{total,mean,calls}.csv
#   pgstat.sh disable --yes       删扩展、还原 shared_preload_libraries 并重启
#
# 设计取舍：用 ALTER SYSTEM 而不是改 postgresql.conf——两种布局都适用（Docker 的配置在
# compose 的 -c 参数里、直装的在 /etc/postgresql/<版本>/main/），ALTER SYSTEM 写进数据目录的
# postgresql.auto.conf，compose 的 -c 参数里没有 shared_preload_libraries，不会被盖掉。
# 只在原值为空时才写入；原值非空说明主机另有安排，交给人处理，免得 disable 还原错。
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lt-common.sh
. "$SCRIPT_DIR/lt-common.sh"

usage() {
  sed -n '7,11p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

preload_value() {
  echo 'SHOW shared_preload_libraries;' | lt_psql -tA | tr -d '[:space:]'
}

cmd_enable() {
  [[ "${1:-}" == --yes ]] || lt_die "enable 会重启 PostgreSQL（网关几秒内连库失败），确认后加 --yes"
  local current
  current="$(preload_value)"
  case "$current" in
    pg_stat_statements) lt_say "shared_preload_libraries 已含 pg_stat_statements，不重启" ;;
    "")
      lt_say "写 shared_preload_libraries = 'pg_stat_statements' 并重启 PostgreSQL"
      echo "ALTER SYSTEM SET shared_preload_libraries = 'pg_stat_statements';" | lt_psql
      lt_pg_restart
      ;;
    *) lt_die "shared_preload_libraries 已是 '$current'，本脚本只处理空值，请手工追加 pg_stat_statements" ;;
  esac
  echo 'CREATE EXTENSION IF NOT EXISTS pg_stat_statements;' | lt_psql
  echo 'SELECT pg_stat_statements_reset();' | lt_psql -tA >/dev/null
  lt_say "pg_stat_statements 已开启并清零"
}

cmd_reset() {
  echo 'SELECT pg_stat_statements_reset();' | lt_psql -tA >/dev/null
  lt_say "pg_stat_statements 已清零（$(date -u +%FT%TZ)）"
}

# export_one ORDER_BY FILE N：只取本库、顶层语句；查询文本压成一行便于表格查看
export_one() {
  lt_psql <<SQL > "$2"
COPY (
  SELECT s.queryid,
         s.userid::regrole AS role,
         s.calls,
         round(s.total_exec_time::numeric, 2)  AS total_exec_ms,
         round(s.mean_exec_time::numeric, 3)   AS mean_exec_ms,
         round(s.max_exec_time::numeric, 3)    AS max_exec_ms,
         round(s.stddev_exec_time::numeric, 3) AS stddev_exec_ms,
         s.rows,
         s.shared_blks_hit,
         s.shared_blks_read,
         s.temp_blks_written,
         s.wal_bytes,
         regexp_replace(s.query, '\s+', ' ', 'g') AS query
    FROM pg_stat_statements s
   WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
     AND s.toplevel
   ORDER BY $1 DESC
   LIMIT $3
) TO STDOUT WITH (FORMAT csv, HEADER true);
SQL
}

cmd_export() {
  local dir="${1:-}" n="${2:-50}" stamp
  [[ -n "$dir" ]] || usage
  [[ "$n" =~ ^[0-9]+$ ]] && (( n >= 1 && n <= 10000 )) || lt_die "N 必须是 1–10000 的整数"
  mkdir -p -- "$dir"
  stamp="$(lt_stamp)"
  export_one s.total_exec_time "$dir/pgstat-$stamp-total.csv" "$n"
  export_one s.mean_exec_time "$dir/pgstat-$stamp-mean.csv" "$n"
  export_one s.calls "$dir/pgstat-$stamp-calls.csv" "$n"
  lt_say "已导出 $dir/pgstat-$stamp-{total,mean,calls}.csv（各 top $n）"
}

cmd_disable() {
  [[ "${1:-}" == --yes ]] || lt_die "disable 会重启 PostgreSQL（网关几秒内连库失败），确认后加 --yes"
  echo 'DROP EXTENSION IF EXISTS pg_stat_statements;' | lt_psql
  if [[ "$(preload_value)" == pg_stat_statements ]]; then
    lt_say "还原 shared_preload_libraries 并重启 PostgreSQL"
    echo 'ALTER SYSTEM RESET shared_preload_libraries;' | lt_psql
    lt_pg_restart
  fi
  lt_say "pg_stat_statements 已撤销"
}

main() {
  local cmd="${1:-}"
  [[ -n "$cmd" ]] || usage
  shift
  lt_require_root
  lt_init
  case "$cmd" in
    enable) cmd_enable "$@" ;;
    reset) cmd_reset ;;
    export) cmd_export "$@" ;;
    disable) cmd_disable "$@" ;;
    *) usage ;;
  esac
}

main "$@"
