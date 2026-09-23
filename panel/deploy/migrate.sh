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
    exit 78
    ;;
  up|up-to|up-by-one|status|version) ;;
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
expected_version=1
MAX_MIGRATION_VERSION=0
for migration in "${migration_files[@]}"; do
  name="${migration##*/}"
  if [[ ! "$name" =~ ^([0-9]{5})_[A-Za-z0-9._-]+\.sql$ ]]; then
    echo "migration: invalid migration filename" >&2
    exit 78
  fi
  version=$((10#${BASH_REMATCH[1]}))
  if [ "$version" -ne "$expected_version" ]; then
    echo "migration: migration sequence is incomplete" >&2
    exit 78
  fi
  MAX_MIGRATION_VERSION=$version
  expected_version=$((expected_version + 1))
done
# CA42 客户端认证子系统冻结后，它的迁移已移出主序列（见
# migrations/frozen-client-auth/README.md），原先「版本号 ≥42 就必须存在
# 00042_client_auth_expand.sql 且 SHA 匹配」的三处闸门随之失效。
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
  MIGRATION_PGOPTIONS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c app.order_release_writers_stopped=yes -c aegis.client_auth_00042_upgrade_approved=approved-v1 -c aegis.client_auth_writers_stopped=stopped-v1'
fi

GOOSE_BASE_ENV=(env -i PATH="$PATH" HOME="${HOME:-/root}"
  GOOSE_DRIVER="$GOOSE_DRIVER" GOOSE_DBSTRING="$GOOSE_DBSTRING"
  GOOSE_MIGRATION_DIR="$GOOSE_MIGRATION_DIR")
[ -z "$MIGRATION_PGPASSWORD" ] || GOOSE_BASE_ENV+=(PGPASSWORD="$MIGRATION_PGPASSWORD")

# 这里原本整块都是 CLIENT-AUTH-00042 的隔离认证闸门：版本号一旦到 42，
# up / up-to / up-by-one / redo 全部拒绝，必须先跑 check-migrations-isolated-pg18.sh
# 换一张 attestation。CA42 冻结、迁移移出主序列后，这套闸门只剩下
# 「任何 42 号以后的迁移都发不出去」这一个效果。
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
exec "${GOOSE_ENV[@]}" "$GOOSE" "$COMMAND" "$@"
