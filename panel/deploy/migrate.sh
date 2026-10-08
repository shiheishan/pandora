#!/usr/bin/env bash
# Privileged goose wrapper. Runtime services never receive the migration DSN.
set -Eeuo pipefail
umask 077

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${AEGIS_ENV_FILE:-$DEPLOY_DIR/.env}"
GOOSE="${GOOSE_BIN:-/root/go/bin/goose}"
# 迁移文件默认取与 deploy/ 并排的 migrations/：install.sh（/opt/aegispanel）、install-native.sh
# （/opt/pandora）和源码树（make check-migrations）都是这个布局。不写死安装路径——
# 曾写死 /opt/pandora，install.sh 装的机器上会找不到，或者读到另一套安装留下的旧迁移。
MIGRATIONS_DIR="${AEGIS_MIGRATIONS_DIR:-$(dirname -- "$DEPLOY_DIR")/migrations}"
COMMAND="${1:-up}"
if [ "$#" -gt 0 ]; then shift; fi

if [ "$COMMAND" = --skip-check ]; then
  echo "migration: --skip-check is not a public migration option" >&2
  exit 78
fi
for argument in "$@"; do
  if [ "$argument" = --skip-check ]; then
    echo "migration: --skip-check is not a public migration option" >&2
    exit 78
  fi
done

# Never pass an unrecognized first token to Goose. Goose accepts global flags
# before its command, so treating an arbitrary token as COMMAND would let
# callers spell e.g. `-v up` and bypass the wrapper's `up` precheck.
case "$COMMAND" in
  down|redo)
    echo "migration: destructive down/redo is not available through the public wrapper" >&2
    echo "migration: to return to an earlier version use the confirmed 'rollback-to <version>' (see panel/deploy/MIGRATION-RUNBOOK.md in the source tree)" >&2
    exit 78
    ;;
  up|up-to|up-by-one|status|version|rollback-to|check-indexes) ;;
  *)
    echo "migration: unsupported command" >&2
    exit 78
    ;;
esac

[ -r "$ENV_FILE" ] || { echo "migration: missing environment file" >&2; exit 1; }
[ -d "$MIGRATIONS_DIR" ] || { echo "migration: missing migrations directory" >&2; exit 1; }
[ -x "$GOOSE" ] || { echo "migration: goose executable is missing" >&2; exit 1; }
. "$ENV_FILE"

shopt -s nullglob
migration_files=("$MIGRATIONS_DIR"/*.sql)
[ "${#migration_files[@]}" -gt 0 ] || { echo "migration: no SQL migrations found" >&2; exit 78; }
# 编号只要求严格递增、不重复，允许空号：主序列历史上合并压号留下了 00073、
# 00091、00092 三个空号，已装的库 goose_db_version 里记着其后的版本号，不能重编；
# goose 本身按版本号排序、容忍空号。同号两个文件 goose 会报错，这里提前拒绝。
MAX_MIGRATION_VERSION=0
for migration in "${migration_files[@]}"; do
  name="${migration##*/}"
  if [[ ! "$name" =~ ^([0-9]{5})_[A-Za-z0-9._-]+\.sql$ ]]; then
    echo "migration: invalid migration filename" >&2
    exit 78
  fi
  version=$((10#${BASH_REMATCH[1]}))
  if [ "$version" -eq "$MAX_MIGRATION_VERSION" ] && [ "$version" -ne 0 ]; then
    echo "migration: duplicate migration version: $name" >&2
    exit 78
  fi
  if [ "$version" -le "$MAX_MIGRATION_VERSION" ] || [ "$version" -eq 0 ]; then
    echo "migration: migration versions must be strictly increasing from 00001: $name" >&2
    exit 78
  fi
  MAX_MIGRATION_VERSION=$version
done
# CA42 客户端认证子系统冻结后，它的两个迁移已移出主线，连同代码与发布门禁脚本
# 存档在 git 标签 archive/client-auth（panel/migrations/frozen-client-auth/）。原先「版本号 ≥42 就必须存在 00042_client_auth_expand.sql
# 且 SHA 匹配」的三处闸门随之删除。
#
# 这些闸门守的是 client-auth 迁移本身，却以版本号为触发条件，于是 42 号槽位
# 一旦换人就会把所有后续发布全部拦死。恢复 CA42 时请以「该文件是否在序列中」
# 为条件重建，不要再用版本号。

MIGRATION_PGPASSWORD=""
if [ -z "${AEGIS_MIGRATION_DATABASE_URL:-}" ]; then
  if [ "${PANDORA_LOCAL_MIGRATION_APPROVED:-}" != yes ]; then
    echo "migration: AEGIS_MIGRATION_DATABASE_URL is required; local PostgreSQL fallback needs explicit approval" >&2
    exit 1
  fi
  : "${POSTGRES_USER:?POSTGRES_USER is required for approved local migration}"
  : "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required for approved local migration}"
  : "${POSTGRES_DB:?POSTGRES_DB is required for approved local migration}"
  : "${POSTGRES_PORT:?POSTGRES_PORT is required for approved local migration}"
  [[ "$POSTGRES_USER" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] \
    || { echo "migration: unsafe local PostgreSQL user" >&2; exit 1; }
  [[ "$POSTGRES_DB" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] \
    || { echo "migration: unsafe local PostgreSQL database" >&2; exit 1; }
  [[ "$POSTGRES_PORT" =~ ^[0-9]+$ ]] && [ "$POSTGRES_PORT" -ge 1 ] && [ "$POSTGRES_PORT" -le 65535 ] \
    || { echo "migration: unsafe local PostgreSQL port" >&2; exit 1; }
  # Keep the password out of the DSN/argv.  It is passed only in the scrubbed
  # child environment and the fallback is pinned to loopback.
  AEGIS_MIGRATION_DATABASE_URL="host=127.0.0.1 port=$POSTGRES_PORT user=$POSTGRES_USER dbname=$POSTGRES_DB sslmode=disable"
  MIGRATION_PGPASSWORD="$POSTGRES_PASSWORD"
fi

# Goose reads the privileged DSN from its environment, keeping it out of argv.
export GOOSE_DRIVER=postgres
export GOOSE_DBSTRING="$AEGIS_MIGRATION_DATABASE_URL"
export GOOSE_MIGRATION_DIR="$MIGRATIONS_DIR"
UPGRADE_APPROVED="${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}"
MIGRATION_PGOPTIONS=""
if [ "$UPGRADE_APPROVED" = yes ]; then
  # The values are fixed here so a caller cannot inject arbitrary libpq
  # options through the privileged migration wrapper.
  # 00040 的订单释放迁移同样是 fail-closed。生产早就过了那一版所以一直没
  # 暴露，但全新库从 0 装起会被它拦下——一键安装正是这种场景。
  # 冻结迁移要的两个 aegis.client_auth_* 开关随它移出主序列，不再下发。
  MIGRATION_PGOPTIONS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c app.order_release_writers_stopped=yes'
fi

GOOSE_BASE_ENV=(env -i PATH="$PATH" HOME="${HOME:-/root}"
  GOOSE_DRIVER="$GOOSE_DRIVER" GOOSE_DBSTRING="$GOOSE_DBSTRING"
  GOOSE_MIGRATION_DIR="$GOOSE_MIGRATION_DIR")
[ -z "$MIGRATION_PGPASSWORD" ] || GOOSE_BASE_ENV+=(PGPASSWORD="$MIGRATION_PGPASSWORD")

# ---------------------------------------------------------------------------
# 无效索引护栏（w8walk 第 5 节第 1 条，证据见 MIGRATION-RUNBOOK 第 4.3 节）。
# `-- +goose NO TRANSACTION` 的 CREATE INDEX CONCURRENTLY（00136、00139）中途失败会留下
# indisvalid=false 的索引；修好数据不清理就重跑，Up 里的 IF NOT EXISTS 把它当成已存在，goose
# 照样记版本——唯一索引从此不生效且不报错。所以：
#   - up / up-to / up-by-one 执行前查一次：有无效索引就拒绝，什么都不执行（这时重跑会被骗）；
#   - 执行后再查一次：有就失败（迁移本身留下了半成品）；
#   - check-indexes 只做这次查询（安装器停服之前先调它，失败不停服）。
# 查不了（没有 psql、也没有 aegis-postgres 容器）同样算失败：这是安全检查，不能悄悄跳过。
# 查询客户端：PANDORA_PSQL_BIN（桩测试用）> PATH 上的 psql（install-native.sh 的机器，按迁移 DSN 连）
# > docker 容器 aegis-postgres 里的 psql（install.sh 的机器；与 check-migrations.sh 同样先核对
# 容器发布的端口就是 POSTGRES_PORT，免得查到别的库）。
# ---------------------------------------------------------------------------
INVALID_INDEX_SQL="SELECT format('%I.%I', n.nspname, c.relname) || ' on ' || format('%I.%I', tn.nspname, t.relname)
  FROM pg_catalog.pg_index i
  JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_catalog.pg_class t ON t.oid = i.indrelid
  JOIN pg_catalog.pg_namespace tn ON tn.oid = t.relnamespace
 WHERE NOT i.indisvalid AND n.nspname NOT IN ('pg_catalog', 'pg_toast')
 ORDER BY 1;"

# 打印无效索引（一行一个，「索引 on 表」）；查不了返回 2
invalid_indexes() {
  local psql_bin="${PANDORA_PSQL_BIN:-}" endpoint
  if [ -n "$psql_bin" ]; then
    [ -x "$psql_bin" ] || { echo "migration: PANDORA_PSQL_BIN is not executable" >&2; return 2; }
  else
    psql_bin="$(command -v psql 2>/dev/null || true)"
  fi
  if [ -n "$psql_bin" ]; then
    local psql_env=(env -i PATH="$PATH" HOME="${HOME:-/root}")
    [ -z "$MIGRATION_PGPASSWORD" ] || psql_env+=(PGPASSWORD="$MIGRATION_PGPASSWORD")
    "${psql_env[@]}" "$psql_bin" -X -w -q -At -v ON_ERROR_STOP=1 -d "$AEGIS_MIGRATION_DATABASE_URL" -c "$INVALID_INDEX_SQL" \
      || { echo "migration: cannot query pg_index for invalid indexes" >&2; return 2; }
    return 0
  fi
  if command -v docker >/dev/null 2>&1 && [ -n "${POSTGRES_USER:-}" ] && [ -n "${POSTGRES_DB:-}" ]; then
    endpoint="$(docker port aegis-postgres 5432/tcp 2>/dev/null | head -n1 || true)"
    if [ -n "$endpoint" ] && [ "$endpoint" = "127.0.0.1:${POSTGRES_PORT:-}" ]; then
      # 口令只按名字经 -e 透传（值来自本进程环境），不进命令行参数
      env -i PATH="$PATH" HOME="${HOME:-/root}" PGPASSWORD="${POSTGRES_PASSWORD:-}" \
        docker exec -i -e PGPASSWORD aegis-postgres \
        psql -X -w -q -At -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$INVALID_INDEX_SQL" \
        || { echo "migration: cannot query pg_index for invalid indexes" >&2; return 2; }
      return 0
    fi
  fi
  echo "migration: cannot check for invalid indexes: no psql on PATH and no aegis-postgres container published on 127.0.0.1:${POSTGRES_PORT:-?}" >&2
  return 2
}

# 查一次并按时机给出修复命令。$1 = before（执行迁移前）| after（执行迁移后）| failed（迁移失败后）| check
# 返回 0 没有无效索引；1 有；2 查不了
check_invalid_indexes() {
  local when="$1" found rc=0
  found="$(invalid_indexes)" || rc=$?
  [ "$rc" -eq 0 ] || return 2
  [ -n "$found" ] || return 0
  echo "migration: INVALID indexes found (left by an interrupted CREATE INDEX CONCURRENTLY):" >&2
  printf '  %s\n' "$found" >&2
  case "$when" in
    before|check)
      echo "migration: re-running 'up' now would skip them (IF NOT EXISTS) and record the migration as done; nothing was executed" >&2 ;;
    failed)
      echo "migration: the failed migration left them behind; clean them up before running 'up' again, or the re-run will skip them (IF NOT EXISTS)" >&2 ;;
    after)
      echo "migration: the migrations ran, but these indexes are not in effect (a unique index that is INVALID enforces nothing)" >&2 ;;
  esac
  echo "migration: fix the cause first (e.g. the duplicate rows a unique index rejected), then for each index:" >&2
  printf '%s\n' "$found" | while IFS= read -r line; do
    printf '  DROP INDEX CONCURRENTLY IF EXISTS %s;\n' "${line%% on *}" >&2
  done
  echo "migration: run each DROP on its own (not inside a transaction), then 'migrate.sh version':" >&2
  echo "migration:   - the migration that creates it is NOT recorded yet: run 'migrate.sh up' again, it recreates the index;" >&2
  echo "migration:   - it IS already recorded: run that migration's CREATE ... CONCURRENTLY statement by hand (MIGRATION-RUNBOOK.md section 4.3, step 3)." >&2
  echo "migration: confirm with 'migrate.sh check-indexes' (exit 0 = no invalid index)." >&2
  return 1
}

if [ "$COMMAND" = check-indexes ]; then
  [ "$#" -eq 0 ] || { echo "migration: check-indexes does not accept arguments" >&2; exit 78; }
  rc=0; check_invalid_indexes check || rc=$?
  [ "$rc" -eq 0 ] && echo "migration: no invalid indexes"
  exit "$rc"
fi

# 这里原本整块都是 CLIENT-AUTH-00042 的隔离认证闸门：版本号一旦到 42，
# up / up-to / up-by-one / redo 全部拒绝，必须先跑一次隔离 PG18 预检换一张
# attestation（那个预检脚本随 CLIENT-AUTH 归档在标签 archive/client-auth）。
# CA42 冻结、迁移移出主序列后，这套闸门只剩下「任何 42 号以后的迁移都发不出去」
# 这一个效果，所以删掉了。
#
# 参数校验本身是有价值的，所以留下并改成无条件执行 —— 原先只在 ≥42 时才校验，
# 反而是版本号越小越宽松。
case "$COMMAND" in
  up-to)
    if [ "$#" -ne 1 ] || [[ "$1" != 0 && "$1" =~ ^0 ]] || [[ ! "$1" =~ ^[0-9]{1,10}$ ]]; then
      echo "migration: up-to requires one canonical non-negative version" >&2
      exit 78
    fi
    TARGET_VERSION=$((10#$1))
    if [ "$TARGET_VERSION" -gt 2147483647 ]; then
      echo "migration: up-to version is outside the supported range" >&2
      exit 78
    fi
    ;;
  up-by-one)
    if [ "$#" -ne 0 ]; then
      echo "migration: $COMMAND does not accept arguments through this wrapper" >&2
      exit 78
    fi
    ;;
  rollback-to)
    if [ "$#" -ne 1 ] || [[ "$1" != 0 && "$1" =~ ^0 ]] || [[ ! "$1" =~ ^[0-9]{1,10}$ ]]; then
      echo "migration: rollback-to requires one canonical non-negative version" >&2
      exit 78
    fi
    TARGET_VERSION=$((10#$1))
    ;;
esac

# rollback-to <版本>：带确认地逐个执行 Down，回到指定版本。公开入口仍然拒绝 down/redo，
# 回滚只走这里。顺序固定为：
#   1. 写入者已停（显式声明，有 systemd 时再核实三个单元都不在运行）；
#   2. 指向一份刚做的备份（回滚会删列删表，备份是退路）；
#   3. 读当前版本，列出 (目标, 当前] 之间要撤的迁移；
#   4. 预扫描：其中有文件头标了 irreversible 的，一个都不执行，直接拒绝并说明最多能回到哪；
#   5. 确认短语与当前、目标版本绑定（PANDORA_ROLLBACK_CONFIRM 或终端输入）；
#   6. 从高到低逐个 goose down，每步核对版本；某个 Down 失败或拒绝（goose 单迁移事务回滚，
#      库停在它之前的版本）就停下，不再往下，提示走备份恢复。
# 不下发任何 PGOPTIONS：00037–00040 的 Down 要逐版本批准，这里不替人批准，到那里会被拒绝。
# 流程与三种情形见 MIGRATION-RUNBOOK.md。
if [ "$COMMAND" = rollback-to ]; then
  rollback_refuse() { echo "migration rollback: $*" >&2; exit 78; }
  goose_current_version() {
    local out
    out="$("${GOOSE_BASE_ENV[@]}" "$GOOSE" version 2>&1)" || { printf '%s\n' "$out" | tail -5 >&2; return 1; }
    printf '%s\n' "$out" | sed -n 's/.*goose: version \([0-9][0-9]*\)[[:space:]]*$/\1/p' | tail -n1
  }

  [ "${PANDORA_ROLLBACK_WRITERS_STOPPED:-}" = yes ] \
    || rollback_refuse "stop aegis-admin, aegis-public and aegis-node first, then set PANDORA_ROLLBACK_WRITERS_STOPPED=yes"
  if command -v systemctl >/dev/null 2>&1; then
    for unit in aegis-admin.service aegis-public.service aegis-node.service; do
      if systemctl is-active --quiet "$unit" 2>/dev/null; then
        rollback_refuse "$unit is still active; stop every writer before rolling back"
      fi
    done
  fi
  ROLLBACK_BACKUP="${PANDORA_ROLLBACK_BACKUP:-}"
  [ -n "$ROLLBACK_BACKUP" ] && [ -f "$ROLLBACK_BACKUP" ] && [ ! -L "$ROLLBACK_BACKUP" ] && [ -s "$ROLLBACK_BACKUP" ] \
    || rollback_refuse "PANDORA_ROLLBACK_BACKUP must name the non-empty backup file taken right before this rollback"

  CURRENT_VERSION="$(goose_current_version)" || { echo "migration rollback: cannot read the current version" >&2; exit 1; }
  [[ "$CURRENT_VERSION" =~ ^[0-9]+$ ]] || { echo "migration rollback: cannot parse the current version" >&2; exit 1; }
  [ "$TARGET_VERSION" -lt "$CURRENT_VERSION" ] \
    || rollback_refuse "target $TARGET_VERSION must be below the current version $CURRENT_VERSION"

  ROLLBACK_PLAN=()        # 要撤的文件，从高到低
  ROLLBACK_EXPECT=()      # 撤完每一个之后应处的版本
  TARGET_FOUND=0; CURRENT_FOUND=0
  for (( index=${#migration_files[@]} - 1; index >= 0; index-- )); do
    name="${migration_files[$index]##*/}"
    version=$((10#${name%%_*}))
    [ "$version" -eq "$TARGET_VERSION" ] && TARGET_FOUND=1
    [ "$version" -eq "$CURRENT_VERSION" ] && CURRENT_FOUND=1
    if [ "$version" -gt "$TARGET_VERSION" ] && [ "$version" -le "$CURRENT_VERSION" ]; then
      ROLLBACK_PLAN+=("${migration_files[$index]}")
    fi
  done
  [ "$CURRENT_FOUND" -eq 1 ] \
    || rollback_refuse "the database is at $CURRENT_VERSION but this release has no migration file for it"
  [ "$TARGET_VERSION" -eq 0 ] || [ "$TARGET_FOUND" -eq 1 ] \
    || rollback_refuse "target $TARGET_VERSION is not a migration version in this release"
  for (( index=1; index < ${#ROLLBACK_PLAN[@]}; index++ )); do
    name="${ROLLBACK_PLAN[$index]##*/}"
    ROLLBACK_EXPECT+=("$((10#${name%%_*}))")
  done
  ROLLBACK_EXPECT+=("$TARGET_VERSION")

  BLOCKERS=()
  for migration in "${ROLLBACK_PLAN[@]}"; do
    if awk '/^-- \+goose Up/ { exit } /^-- irreversible:[[:space:]]*[^[:space:]]/ { found=1 } END { exit !found }' "$migration"; then
      BLOCKERS+=("${migration##*/}")
    fi
  done
  if [ "${#BLOCKERS[@]}" -gt 0 ]; then
    # 计划从高到低排，第一个阻塞者版本最高：最多只能回到它自己
    furthest=$((10#${BLOCKERS[0]%%_*}))
    echo "migration rollback: irreversible migrations in range: ${BLOCKERS[*]}" >&2
    if [ "$furthest" -lt "$CURRENT_VERSION" ]; then
      echo "migration rollback: the furthest this tool can go is version $furthest; anything earlier needs the pre-upgrade backup (panel/deploy/MIGRATION-RUNBOOK.md, section 3)" >&2
    else
      echo "migration rollback: the current migration itself is irreversible; restore the pre-upgrade backup (panel/deploy/MIGRATION-RUNBOOK.md, section 3)" >&2
    fi
    echo "migration rollback: nothing was executed" >&2
    exit 78
  fi

  CONFIRM_PHRASE="rollback $CURRENT_VERSION to $TARGET_VERSION"
  echo "migration rollback plan: $CURRENT_VERSION -> $TARGET_VERSION (${#ROLLBACK_PLAN[@]} migration(s), highest first; backup: $ROLLBACK_BACKUP)"
  for migration in "${ROLLBACK_PLAN[@]}"; do echo "  down ${migration##*/}"; done
  if [ -n "${PANDORA_ROLLBACK_CONFIRM+x}" ]; then
    [ "$PANDORA_ROLLBACK_CONFIRM" = "$CONFIRM_PHRASE" ] \
      || rollback_refuse "confirmation does not match; expected exactly: $CONFIRM_PHRASE"
  elif [ -t 0 ]; then
    printf 'Type "%s" to proceed: ' "$CONFIRM_PHRASE" >&2
    IFS= read -r answer || answer=""
    [ "$answer" = "$CONFIRM_PHRASE" ] || rollback_refuse "not confirmed; nothing was executed"
  else
    rollback_refuse "confirmation required: set PANDORA_ROLLBACK_CONFIRM='$CONFIRM_PHRASE'"
  fi

  for index in "${!ROLLBACK_PLAN[@]}"; do
    name="${ROLLBACK_PLAN[$index]##*/}"
    expected="${ROLLBACK_EXPECT[$index]}"
    if ! "${GOOSE_BASE_ENV[@]}" "$GOOSE" down; then
      now="$(goose_current_version || true)"
      echo "migration rollback: STOPPED: the Down of $name failed or refused (see above); the database is at version ${now:-unknown}" >&2
      echo "migration rollback: nothing below it was attempted; to go further back restore $ROLLBACK_BACKUP (panel/deploy/MIGRATION-RUNBOOK.md, section 3)" >&2
      exit 1
    fi
    now="$(goose_current_version)" || { echo "migration rollback: cannot read the version after $name" >&2; exit 1; }
    if [ "$now" != "$expected" ]; then
      echo "migration rollback: STOPPED: after $name the database is at version $now, expected $expected" >&2
      exit 1
    fi
    echo "migration rollback: undid $name; now at version $now"
  done
  echo "migration rollback complete: $CURRENT_VERSION -> $TARGET_VERSION"
  exit 0
fi

# 执行前先查无效索引：放在克隆预检之前，有就不必白跑一遍预检（见上面「无效索引护栏」）
case "$COMMAND" in
  up|up-to|up-by-one)
    rc=0; check_invalid_indexes before || rc=$?
    [ "$rc" -eq 0 ] || exit 1
    ;;
esac

# 全新库可以跳过预检——它要保护的数据还不存在。
#
# 预检会克隆源库再把待应用的迁移跑一遍。库里连 goose 记录都没有时，
# 克隆的是个空库，等于把全部迁移白跑一遍；单核机器上这让安装时间翻倍。
#
# 只认精确值，且跳过时明确打印：这是安全机制，不能悄悄失效。
# 调用方负责确认「确实是全新库」——install.sh 用 goose_db_version 是否
# 存在来判断，比「表数为 0」严谨（后者会把手工建过表的库误判成全新库）。
PRECHECK_ENABLED=1
if [ "${PANDORA_SKIP_PRECHECK_FRESH_DB:-}" = "yes-empty-database" ]; then
  PRECHECK_ENABLED=0
  echo "migration: 调用方声明这是全新库，跳过一次性数据库预检"
fi

# 停服前已做过完整预检的调用方（发布控制器）递交那次预检的凭据：这里只做停服后的
# 只读核对（迁移目录摘要、源库水位、续费闸门、停写演练一致、六小时内），不再克隆演练。
# 核对不过同样不碰正式库。没有凭据就照旧完整预检——up 永远不会在两者都没有时执行。
PRECHECK_ATTESTATION="${PANDORA_PRECHECK_ATTESTATION:-}"
if [ "$COMMAND" = up ] && [ "$PRECHECK_ENABLED" -eq 1 ] && [ -n "$PRECHECK_ATTESTATION" ]; then
  PRECHECK_LOG="$(mktemp "${TMPDIR:-/tmp}/pandora-precheck.XXXXXX")"
  cleanup_precheck() { rm -f -- "$PRECHECK_LOG"; }
  trap cleanup_precheck EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  if env -i PATH="$PATH" HOME="${HOME:-/root}" \
      AEGIS_ENV_FILE="$ENV_FILE" AEGIS_MIGRATIONS_DIR="$MIGRATIONS_DIR" \
      PANDORA_STOPPED_WRITER_UPGRADE_APPROVED="$UPGRADE_APPROVED" \
      ${GOOSE_BIN:+GOOSE_BIN="$GOOSE_BIN"} \
      "$DEPLOY_DIR/check-migrations.sh" --verify-attestation "$PRECHECK_ATTESTATION" >"$PRECHECK_LOG" 2>&1; then
    PRECHECK_STATUS=0
  else
    PRECHECK_STATUS=$?
    echo "migration: precheck attestation was not accepted; production is unchanged" >&2
    tail -20 "$PRECHECK_LOG" >&2
    if [ "$PRECHECK_STATUS" -eq 78 ]; then exit 78; fi
    exit 1
  fi
  cleanup_precheck
  trap - EXIT INT TERM
  PRECHECK_ENABLED=0
  echo "migration precheck=attested"
fi

if [ "$COMMAND" = up ] && [ "$PRECHECK_ENABLED" -eq 1 ]; then
  PRECHECK_LOG="$(mktemp "${TMPDIR:-/tmp}/pandora-precheck.XXXXXX")"
  cleanup_precheck() { rm -f -- "$PRECHECK_LOG"; }
  trap cleanup_precheck EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  # GOOSE_BIN 要一起透传：env -i 清空环境是为了不让凭据漏进子进程，
  # 但它同时清掉了这个路径，check-migrations.sh 于是回退到写死的
  # /root/go/bin/goose——发布包自带的那个它看不见，报「goose executable
  # is missing」。预检和正式迁移必须用同一个 goose，否则「预检通过」
  # 证明不了正式迁移也会通过。
  if env -i PATH="$PATH" HOME="${HOME:-/root}" \
      AEGIS_ENV_FILE="$ENV_FILE" AEGIS_MIGRATIONS_DIR="$MIGRATIONS_DIR" \
      PANDORA_STOPPED_WRITER_UPGRADE_APPROVED="$UPGRADE_APPROVED" \
      ${GOOSE_BIN:+GOOSE_BIN="$GOOSE_BIN"} \
      "$DEPLOY_DIR/check-migrations.sh" >"$PRECHECK_LOG" 2>&1; then
    PRECHECK_STATUS=0
  else
    PRECHECK_STATUS=$?
    echo "migration: disposable-database precheck failed; production is unchanged" >&2
    tail -20 "$PRECHECK_LOG" >&2
    if [ "$PRECHECK_STATUS" -eq 78 ]; then exit 78; fi
    exit 1
  fi
  cleanup_precheck
  trap - EXIT INT TERM
  echo "migration precheck=ok"
fi

GOOSE_ENV=(env -i PATH="$PATH" HOME="${HOME:-/root}"
  GOOSE_DRIVER="$GOOSE_DRIVER" GOOSE_DBSTRING="$GOOSE_DBSTRING"
  GOOSE_MIGRATION_DIR="$GOOSE_MIGRATION_DIR")
[ -z "$MIGRATION_PGPASSWORD" ] || GOOSE_ENV+=(PGPASSWORD="$MIGRATION_PGPASSWORD")
[ -z "$MIGRATION_PGOPTIONS" ] || GOOSE_ENV+=(PGOPTIONS="$MIGRATION_PGOPTIONS")
case "$COMMAND" in
  up|up-to|up-by-one) ;;
  *) exec "${GOOSE_ENV[@]}" "$GOOSE" "$COMMAND" "$@" ;;
esac

# 执行后再查一次无效索引（执行前那次在预检之前，见上面「无效索引护栏」）
goose_rc=0
"${GOOSE_ENV[@]}" "$GOOSE" "$COMMAND" "$@" || goose_rc=$?
if [ "$goose_rc" -ne 0 ]; then
  # 迁移失败：CONCURRENTLY 的半成品这时最常见，顺手把清理命令打出来（查不了就算了，失败本身已经报了）
  check_invalid_indexes failed 2>&1 | grep -v '^migration: cannot ' >&2 || true
  exit "$goose_rc"
fi
rc=0; check_invalid_indexes after || rc=$?
[ "$rc" -eq 0 ] || exit 1
