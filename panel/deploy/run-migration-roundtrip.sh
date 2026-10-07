#!/usr/bin/env bash
# 迁移往返门禁：每个迁移的 Down 都要能把结构原样退回去，再 Up 回来也要原样。
#
# 用法：
#   run-migration-roundtrip.sh <panel 源码目录>
#
# 在一次性 postgres:18-alpine 容器里，从空库开始逐个迁移走一遍：
#   1. up-by-one 到 v，dump 记作 S(v)；
#   2. down 一步，dump 必须等于上一步的 S(v-1)：Down 真把这个迁移撤干净了；
#   3. 再 up-by-one，dump 必须等于 S(v)：撤完能原样升回来，没有残留挡路；
#   4. 文件头标了 `-- irreversible:` 的，down 必须失败、版本不动、结构不变。
# 结构 = pg_dump --schema-only（表、列序、约束、索引、视图、函数体、触发器、RLS 策略、
# 授权、默认权限、注释）加 pg_dumpall --roles-only（角色属性），按 pg_dump 的对象条目
# 切开、排序后比较：只忽略对象的输出先后，不忽略任何对象的内容。
#
# 结果每个迁移一行 `--- PASS: roundtrip/<文件>` 或 `--- FAIL: roundtrip/<文件>`，
# 失败带 diff。历史迁移已知的问题登记在 KNOWN 里（只许删不许加），登记了却已经
# 通过的条目同样判失败，要求删掉。
#
# 需要 docker、python3 和 goose（GOOSE_BIN，版本跟 build-release.sh 走）。
# 全程只碰自己建的容器。
set -Eeuo pipefail

PANEL_DIR="${1:-}"
if [[ -z "$PANEL_DIR" || ! -d "$PANEL_DIR/migrations" ]]; then
  echo "用法: $0 <panel 源码目录>" >&2
  exit 2
fi
PANEL_DIR="$(cd "$PANEL_DIR" && pwd)"
MIGRATIONS="$PANEL_DIR/migrations"
CONTAINER="${PANDORA_ROUNDTRIP_CONTAINER:-pandora-migration-roundtrip-$$}"
PG_IMAGE="${PANDORA_ROUNDTRIP_IMAGE:-postgres:18-alpine}"
GOOSE="${GOOSE_BIN:-/root/go/bin/goose}"
PGUSER_MIGRATE=aegis
PGPW=roundtrip-gate-password
DB=pandora_roundtrip

# 历史迁移已知的往返问题：「文件名|原因」。2026-10-07 建门禁时（主线最大 00133）在
# GitHub 上实测出来的，修它们要改那些迁移的 Down 段，不在本门禁的授权里。
# 只许删不许加：新迁移必须一次往返通过（编号大于 KNOWN_CEILING 的条目直接判失败）；
# 某条修好之后这里会因「已登记却通过」变红，提醒删掉。
KNOWN_CEILING=133
KNOWN=(
  "00001_foundation.sql|Down 不删 Up 建的扩展 btree_gist、citext、pgcrypto"
  "00010_seed_rbac.sql|Down 对追加写表 subscription_events 执行 DELETE，语句级追加写触发器空表也拒绝"
  "00012_audit_node_actor.sql|Down 对追加写表 audit_events 执行 DELETE，被追加写触发器拒绝"
  "00035_catalog_authoring.sql|Down 重建的 app.guard_frozen_plan_version() 函数体文本与 Up 之前不同（只差 END 的写法）"
  "00036_order_reservations.sql|Down 留下 Up 之前不存在的列级 INSERT/UPDATE 授权（coupon_redemptions、ledger_accounts、ledger_entries、ledger_transactions 等）"
  "00037_idempotency_runtime_hardening.sql|Down 重建的 app.assert_refund_idempotency_key 等函数体文本与 Up 之前不同（排版）"
  "00038_idempotency_resource_binding.sql|Down 后集群级角色 aegis_idempotency_owner 残留"
  "00042_seed_registration_mode.sql|Down 对追加写表 system_setting_revisions 执行 DELETE，被追加写触发器拒绝"
  "00050_telegram.sql|Down 对追加写表 system_setting_revisions 执行 DELETE，被追加写触发器拒绝"
  "00067_drop_orphan_tables.sql|Down 无条件 RAISE（不恢复孤儿表），实为 irreversible，但文件头缺 irreversible 标记"
  "00102_subscription_period_resync.sql|修数据，Down 无条件 RAISE，实为 irreversible，但文件头缺 irreversible 标记"
  "00131_append_only_retention.sql|Down 恢复的 node_traffic_reports_duplicate_of_fkey 带 NOT VALID，Up 之前是已校验的外键"
)

# 迁移里的 fail-closed 闸门（00037–00040 的 Up 与 Down）要显式批准；一次性库里全部
# 原样满足，绕过闸门跑出来的结果证明不了线上能回滚。
MIGRATE_OPTS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c app.order_release_writers_stopped=yes'
MIGRATE_OPTS+=' -c app.idempotency_probe_callers_stopped=yes -c app.allow_idempotency_schema37_down=yes'
MIGRATE_OPTS+=' -c app.idempotency_binder_callers_stopped=yes -c app.allow_idempotency_schema38_down=yes'
MIGRATE_OPTS+=' -c app.idempotency_completer_callers_stopped=yes -c app.allow_idempotency_schema39_down=yes'
MIGRATE_OPTS+=' -c app.allow_order_release_schema40_down=yes'

[ -x "$GOOSE" ] || { echo "goose 不可执行：$GOOSE" >&2; exit 2; }
command -v docker >/dev/null 2>&1 || { echo "需要 docker" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "需要 python3" >&2; exit 2; }

WORK="$(mktemp -d)"
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

# pg_dump 输出按对象条目（以 "-- Name: …; Type: …" 注释开头）切开，去掉会话设置与
# 每次随机的 \restrict 行，条目内容原样保留，再按内容排序。
cat >"$WORK/normalize.py" <<'PY'
import re, sys
entries, cur = [], []
for line in sys.stdin.read().splitlines():
    if line.startswith('-- Name: ') or line.startswith('-- Role:'):
        if cur:
            entries.append('\n'.join(cur))
        cur = [re.sub(r'; Owner: .*$', '', line)]
        continue
    if line.startswith('--') or not line.strip():
        continue
    if line.startswith(('\\restrict', '\\unrestrict', 'SET ', "SELECT pg_catalog.set_config('search_path'")):
        continue
    cur.append(line)
if cur:
    entries.append('\n'.join(cur))
print('\n\n'.join(sorted(entries)))
PY

echo "==> 起 PostgreSQL 18"
docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD="$PGPW" -e POSTGRES_USER="$PGUSER_MIGRATE" -e POSTGRES_DB=postgres \
  -p 127.0.0.1::5432 "$PG_IMAGE" >/dev/null
READY=""
for _ in $(seq 1 60); do
  # 走 TCP 探测：镜像初始化时的临时实例只听 unix socket（同 run-pg18-gates.sh）
  if docker exec "$CONTAINER" pg_isready -h 127.0.0.1 -U "$PGUSER_MIGRATE" -q 2>/dev/null; then
    READY=1
    break
  fi
  sleep 1
done
[[ -n "$READY" ]] || { docker logs "$CONTAINER" 2>&1 | tail -20 >&2; echo "PostgreSQL 18 60 秒内没有就绪" >&2; exit 1; }
PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$CONTAINER")"
DSN="postgres://${PGUSER_MIGRATE}:${PGPW}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
psql_root() { docker exec "$CONTAINER" psql -X -U "$PGUSER_MIGRATE" -v ON_ERROR_STOP=1 "$@"; }
# 与 run-pg18-gates.sh 一致：补一个不可登录的 postgres 角色，集群形状与那边相同
psql_root -d postgres -qc "CREATE ROLE postgres NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS" >/dev/null

goose_run() { PGOPTIONS="$MIGRATE_OPTS" "$GOOSE" -dir "$MIGRATIONS" postgres "$DSN" "$@"; }
goose_version() {
  goose_run version 2>&1 | sed -n 's/.*goose: version \([0-9][0-9]*\).*/\1/p' | tail -1
}
schema_dump() {
  local out="$1"
  {
    docker exec "$CONTAINER" pg_dump -U "$PGUSER_MIGRATE" -d "$DB" --schema-only
    docker exec "$CONTAINER" pg_dumpall -U "$PGUSER_MIGRATE" --roles-only --no-role-passwords
  } | python3 "$WORK/normalize.py" >"$out"
}
fresh_db_at() {
  local target="$1"
  psql_root -d postgres -qc "DROP DATABASE IF EXISTS $DB WITH (FORCE)" >/dev/null
  # 迁移建的集群级角色在库删掉后还在，按 Down 的口径一并清掉，回到真正的空集群
  psql_root -d postgres -qc "DROP ROLE IF EXISTS aegis_app" >/dev/null 2>&1 || true
  psql_root -d postgres -qc "CREATE DATABASE $DB" >/dev/null
  if [ "$target" -gt 0 ]; then
    goose_run up-to "$target" >"$WORK/rebuild.log" 2>&1 \
      || { tail -20 "$WORK/rebuild.log" >&2; echo "重建到 $target 失败" >&2; exit 1; }
  fi
}

known_reason() {
  local name="$1" entry
  for entry in "${KNOWN[@]}"; do
    [ "${entry%%|*}" = "$name" ] && { printf '%s\n' "${entry#*|}"; return 0; }
  done
  return 1
}

for entry in "${KNOWN[@]}"; do
  if [ "$((10#${entry:0:5}))" -gt "$KNOWN_CEILING" ]; then
    echo "KNOWN 只登记 $(printf '%05d' "$KNOWN_CEILING") 及以前的历史迁移，新迁移必须一次往返通过：${entry%%|*}" >&2
    exit 1
  fi
done

shopt -s nullglob
FILES=("$MIGRATIONS"/*.sql)
[ "${#FILES[@]}" -gt 0 ] || { echo "没有迁移文件" >&2; exit 1; }

psql_root -d postgres -qc "CREATE DATABASE $DB" >/dev/null
goose_run version >/dev/null 2>&1   # 建出 goose_db_version，让 S(0) 与之后的库同形
schema_dump "$WORK/prev.sql"
PREV_VERSION=0
PASSED=0; FAILED=(); KNOWN_HIT=(); STALE=()
STARTED=$SECONDS

for file in "${FILES[@]}"; do
  name="${file##*/}"
  version=$((10#${name%%_*}))
  step_failed=""
  diag="$WORK/diag.txt"; : >"$diag"

  if ! goose_run up-by-one >"$WORK/up.log" 2>&1; then
    echo "--- FAIL: roundtrip/$name"
    sed 's/^/    up: /' "$WORK/up.log" | tail -20
    FAILED+=("$name")
    echo "Up 失败，后面的迁移无从验证，停止" >&2
    break
  fi
  schema_dump "$WORK/cur.sql"

  # 文件头（Up 标记之前）里的 irreversible 标记
  irreversible=0
  if awk '/^-- \+goose Up/{exit} /^-- irreversible:[[:space:]]*[^[:space:]]/{found=1} END{exit !found}' "$file"; then
    irreversible=1
  fi

  if [ "$irreversible" -eq 1 ]; then
    if goose_run down >"$WORK/down.log" 2>&1; then
      step_failed="标了 irreversible，down 却成功了"
    else
      after="$(goose_version)"
      schema_dump "$WORK/after.sql"
      if [ "$after" != "$version" ]; then
        step_failed="irreversible 的 down 失败后版本变成了 $after（应停在 $version）"
      elif ! diff -u "$WORK/cur.sql" "$WORK/after.sql" >"$diag"; then
        step_failed="irreversible 的 down 失败后结构变了"
      fi
    fi
  else
    if ! goose_run down >"$WORK/down.log" 2>&1; then
      step_failed="down 执行失败"
      sed 's/^/    down: /' "$WORK/down.log" | tail -15 >"$diag"
    else
      after="$(goose_version)"
      schema_dump "$WORK/down.sql"
      if [ "$after" != "$PREV_VERSION" ]; then
        step_failed="down 后版本是 $after，应为 $PREV_VERSION"
      elif ! diff -u "$WORK/prev.sql" "$WORK/down.sql" >"$diag"; then
        step_failed="down 后的结构与上一版不一致（- 上一版，+ down 之后）"
      elif ! goose_run up-by-one >"$WORK/reup.log" 2>&1; then
        step_failed="down 之后重新 up 失败"
        sed 's/^/    re-up: /' "$WORK/reup.log" | tail -15 >"$diag"
      else
        schema_dump "$WORK/reup.sql"
        if ! diff -u "$WORK/cur.sql" "$WORK/reup.sql" >"$diag"; then
          step_failed="重新 up 后的结构与第一次 up 不一致（- 第一次，+ 重新 up）"
        fi
      fi
    fi
  fi

  if reason="$(known_reason "$name")"; then
    if [ -n "$step_failed" ]; then
      echo "KNOWN roundtrip/$name: $step_failed（登记原因：$reason）"
      KNOWN_HIT+=("$name")
    else
      echo "--- FAIL: roundtrip/$name（KNOWN 里登记了，但已经通过，请删掉这一条）"
      STALE+=("$name")
    fi
  elif [ -n "$step_failed" ]; then
    echo "--- FAIL: roundtrip/$name: $step_failed"
    head -80 "$diag" | sed 's/^/    /'
    FAILED+=("$name")
  else
    if [ "$irreversible" -eq 1 ]; then
      echo "--- PASS: roundtrip/$name（irreversible：down 被拒绝，版本与结构不变）"
    else
      echo "--- PASS: roundtrip/$name"
    fi
    PASSED=$((PASSED + 1))
  fi

  # 出过任何问题都从空库重建到 v，下一个迁移在干净的状态上验
  if [ -n "$step_failed" ]; then
    fresh_db_at "$version"
  fi
  mv "$WORK/cur.sql" "$WORK/prev.sql"
  PREV_VERSION=$version
done

echo
echo "================ 迁移往返结果 ================"
echo "迁移数 ${#FILES[@]}，通过 $PASSED，失败 ${#FAILED[@]}，已登记问题 ${#KNOWN_HIT[@]}，过期登记 ${#STALE[@]}，耗时 $((SECONDS - STARTED)) 秒"
[[ ${#FAILED[@]} -gt 0 ]] && printf '失败: %s\n' "${FAILED[*]}"
[[ ${#KNOWN_HIT[@]} -gt 0 ]] && printf '已登记: %s\n' "${KNOWN_HIT[*]}"
[[ ${#STALE[@]} -gt 0 ]] && printf '过期登记: %s\n' "${STALE[*]}"
if [[ ${#FAILED[@]} -gt 0 || ${#STALE[@]} -gt 0 ]]; then
  exit 1
fi
echo "迁移往返全部通过"
