#!/usr/bin/env bash
# 在开发对照机上建一份 5k 库副本，只应用给定迁移文件的 Up 段（各自一个事务），再 ANALYZE。
# 用法（在本机）：prep-copy.sh aegis_cmp_<名字> [迁移文件.sql ...]（同名副本会被删掉重建）
# 环境变量：BENCH_HOST（默认 vultr-sgp-pt-bench）、BENCH_CONTAINER（默认 bench-pg）、BENCH_DUMP（容器内路径，默认 /tmp/d.pgdump）
set -euo pipefail
db="${1:?用法: prep-copy.sh <副本库名> [迁移.sql ...]}"; shift
# 只许动 aegis_cmp_ 开头的副本：aegis 是训练集基线，aegis_train*/aegis_holdout* 归评测集，都不能被这里重建
[[ "$db" =~ ^aegis_cmp_[a-z0-9_]+$ ]] || { echo "副本库名须形如 aegis_cmp_<名字>（小写）" >&2; exit 2; }
host="${BENCH_HOST:-vultr-sgp-pt-bench}"; ctr="${BENCH_CONTAINER:-bench-pg}"; dump="${BENCH_DUMP:-/tmp/d.pgdump}"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
i=0
for m in "$@"; do
  i=$((i+1)); out="$tmp/$(printf '%02d' $i)-$(basename "$m")"
  # 只取 -- +goose Up 与 -- +goose Down 之间；StatementBegin/End 是注释，psql 照常执行
  awk '/^-- \+goose Up/{f=1;next} /^-- \+goose Down/{f=0} f' "$m" > "$out"
  [ -s "$out" ] || { echo "没取到 Up 段: $m" >&2; exit 1; }
done
ssh "$host" "rm -rf /root/bench/tmp/up && mkdir -p /root/bench/tmp/up"
[ "$i" -gt 0 ] && scp -q "$tmp"/*.sql "$host:/root/bench/tmp/up/"
ssh "$host" bash -s -- "$db" "$ctr" "$dump" <<'REMOTE'
set -euo pipefail
db="$1"; ctr="$2"; dump="$3"
docker exec "$ctr" psql -U postgres -qc "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db OWNER aegis"
start=$(date +%s); docker exec "$ctr" pg_restore -U postgres -d "$db" --exit-on-error "$dump"; echo "restore $(( $(date +%s)-start ))s"
for f in /root/bench/tmp/up/*.sql; do
  [ -e "$f" ] || break
  start=$(date +%s%N)
  (echo "BEGIN;"; cat "$f"; echo "COMMIT;") | docker exec -i "$ctr" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -q >/dev/null
  echo "applied $(basename "$f") in $(( ($(date +%s%N)-start)/1000000 ))ms"
done
docker exec "$ctr" psql -U postgres -d "$db" -qc ANALYZE && echo "analyzed $db"
REMOTE
