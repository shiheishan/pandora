#!/usr/bin/env bash
# check-migrations.sh 的直装布局（install-native.sh 的机器，没有 docker）：
#   - 布局取 .env 的 PANDORA_DB_LAYOUT，老 .env 凭 POSTGRES_SUPER_PASSWORD 认出直装；非法值拒绝；
#   - 完整预检与停服后核对都用本机 psql / pg_dump，经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户连，
#     goose 在克隆上演练用同一个端点与身份；docker 一次都不调用；
#   - 口令（POSTGRES_SUPER_PASSWORD）只经干净环境的 PGPASSWORD 给客户端，不进任何 argv，
#     客户端不继承导出的函数；缺了超级用户口令就拒绝。
# docker 布局的同类断言在 check-migrations_mock_test.sh。
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$ROOT/deploy/check-migrations.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-check-native.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT
fail() { echo "check-migrations native: $*" >&2; exit 1; }
mkdir -p "$TMP/bin" "$TMP/migrations" "$TMP/attest"
printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' 'SELECT 1;' >"$TMP/migrations/00001_a.sql"

# 老的直装 .env：没有 PANDORA_DB_LAYOUT，有 POSTGRES_SUPER_PASSWORD
cat >"$TMP/env" <<'ENV'
POSTGRES_USER=aegis
POSTGRES_PASSWORD=owner-secret-sentinel
POSTGRES_DB=aegis
POSTGRES_PORT=5432
POSTGRES_SUPER_PASSWORD=super-secret-sentinel
ENV

# 共用的客户端检查：口令只在环境里、是超级用户口令；不继承函数；端点与身份固定
cat >"$TMP/bin/client-guard" <<'MOCK'
#!/usr/bin/env bash
root="$1"; name="$2"; shift 2
printf '%s %s\n' "$name" "$*" >>"$root/client.argv"
[ "${PGPASSWORD:-}" = super-secret-sentinel ] || { echo "mock $name: wrong PGPASSWORD" >&2; exit 80; }
if /usr/bin/env | grep -q '^BASH_FUNC_'; then echo "mock $name: inherited function" >&2; exit 81; fi
args=" $* "
[[ "$args" == *' -h 127.0.0.1 -p 5432 -U postgres '* ]] || { echo "mock $name: wrong endpoint: $*" >&2; exit 82; }
MOCK
cat >"$TMP/bin/psql" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
"$root/bin/client-guard" "$root" psql "$@" || exit $?
args=" $* "
if [[ "$args" =~ CREATE[[:space:]]DATABASE[[:space:]](aegis_check_[0-9]+) ]]; then
  printf '%s\n' "${BASH_REMATCH[1]}" >"$root/create.id"
elif [[ "$args" =~ DROP[[:space:]]DATABASE[[:space:]]IF[[:space:]]EXISTS[[:space:]](aegis_check_[0-9]+) ]]; then
  printf '%s\n' "${BASH_REMATCH[1]}" >"$root/drop.id"
elif [[ "$args" == *' -d aegis_check_'*' -q '* ]]; then
  cat >"$root/restore.payload"
elif [[ "$args" == *"to_regclass('public.goose_db_version')"* ]]; then
  echo t
elif [[ "$args" == *"to_regclass('public.orders')"* ]]; then
  echo f
elif [[ "$args" == *'max(version_id)'* ]]; then
  echo 1
elif [[ "$args" == *' -tAc '* ]]; then
  echo 1
fi
exit 0
MOCK
cat >"$TMP/bin/pg_dump" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
"$root/bin/client-guard" "$root" pg_dump "$@" || exit $?
[[ " $* " == *' -d aegis '* ]] || exit 83
printf '%s\n' '-- native mock dump'
MOCK
cat >"$TMP/bin/goose" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'goose %s\n' "$*" >>"$root/client.argv"
[ "${PGPASSWORD:-}" = super-secret-sentinel ] || exit 85
printf 'GOOSE_DBSTRING=%s\n' "${GOOSE_DBSTRING:-}" >>"$root/goose.env"
[ "${1:-}" = up ]
MOCK
# 直装上不许碰 docker
cat >"$TMP/bin/docker" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'docker %s\n' "$*" >>"$root/docker.called"
exit 99
MOCK
chmod 0755 "$TMP/bin/"*

run_check() {
  PATH="$TMP/bin:$PATH" GOOSE_BIN="$TMP/bin/goose" \
    AEGIS_ENV_FILE="${ENV_FILE:-$TMP/env}" AEGIS_MIGRATIONS_DIR="$TMP/migrations" \
    "$CHECK" "$@"
}

# ① 完整预检（推断出直装）：克隆、演练、写凭据，全程不碰 docker
ATT="$TMP/attest/precheck.attestation"
PANDORA_PRECHECK_ATTESTATION_OUT="$ATT" PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER=yes \
  run_check >"$TMP/full.out" 2>&1 || { cat "$TMP/full.out" >&2; fail 'native full precheck failed'; }
grep -Fq 'migration precheck complete' "$TMP/full.out" || fail 'full precheck did not complete'
grep -Fq 'migration precheck attestation written' "$TMP/full.out" || fail 'no attestation'
[ ! -e "$TMP/docker.called" ] || fail "docker was called: $(cat "$TMP/docker.called")"
grep -Fxq -- '-- native mock dump' "$TMP/restore.payload" || fail 'the clone did not receive the dump'
cmp -s "$TMP/create.id" "$TMP/drop.id" || fail 'the clone was not dropped'
clone="$(cat "$TMP/create.id")"
grep -Fxq "GOOSE_DBSTRING=host=127.0.0.1 port=5432 user=postgres dbname=$clone sslmode=disable" "$TMP/goose.env" \
  || fail "goose did not rehearse on the clone as postgres: $(cat "$TMP/goose.env")"
grep -Eq '^pg_dump .* -d aegis$' "$TMP/client.argv" || fail 'pg_dump did not read the source database'
if grep -Eq 'super-secret-sentinel|owner-secret-sentinel' "$TMP/client.argv" "$ATT"; then
  fail 'a password leaked into argv or the attestation'
fi

# ② 停服后只读核对：不建克隆、不跑 goose、不碰 docker
rm -f "$TMP/create.id" "$TMP/goose.env"
PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes run_check --verify-attestation "$ATT" >"$TMP/verify.out" 2>&1 \
  || { cat "$TMP/verify.out" >&2; fail 'native attestation verify failed'; }
grep -Fq 'migration precheck attestation verified: source=1 release=1' "$TMP/verify.out" || fail 'verify output'
[ ! -e "$TMP/create.id" ] && [ ! -e "$TMP/goose.env" ] || fail 'verify cloned or migrated'
[ ! -e "$TMP/docker.called" ] || fail 'verify called docker'

# ③ 显式 PANDORA_DB_LAYOUT=native 缺超级用户口令：拒绝，不碰数据库
sed -e '/^POSTGRES_SUPER_PASSWORD=/d' "$TMP/env" >"$TMP/env-nosuper"
printf 'PANDORA_DB_LAYOUT=native\n' >>"$TMP/env-nosuper"
: >"$TMP/client.argv"
if ENV_FILE="$TMP/env-nosuper" run_check >"$TMP/nosuper.out" 2>&1; then fail 'native layout without a superuser password was accepted'; fi
grep -Fq 'POSTGRES_SUPER_PASSWORD is required for the native database layout' "$TMP/nosuper.out" || fail "nosuper message: $(cat "$TMP/nosuper.out")"
[ ! -s "$TMP/client.argv" ] || fail 'a client ran without a superuser password'

# ④ 非法布局值：拒绝
cp "$TMP/env" "$TMP/env-bogus"; printf 'PANDORA_DB_LAYOUT=k8s\n' >>"$TMP/env-bogus"
if ENV_FILE="$TMP/env-bogus" run_check >"$TMP/bogus.out" 2>&1; then fail 'unknown layout accepted'; fi
grep -Fq 'PANDORA_DB_LAYOUT must be native or docker' "$TMP/bogus.out" || fail 'bogus layout message'

# ⑤ 显式 docker 布局压过推断：即使有超级用户口令也走容器（这里 docker 桩会失败）
cp "$TMP/env" "$TMP/env-docker"; printf 'PANDORA_DB_LAYOUT=docker\n' >>"$TMP/env-docker"
rm -f "$TMP/docker.called"
if ENV_FILE="$TMP/env-docker" run_check >"$TMP/docker.out" 2>&1; then fail 'docker layout succeeded without a container'; fi
[ -s "$TMP/docker.called" ] || fail 'explicit docker layout did not use docker'

echo "check-migrations native gate: PASS"
