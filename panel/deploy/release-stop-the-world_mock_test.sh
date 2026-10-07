#!/usr/bin/env bash
# Linux/root dynamic safety gate for failures that must never touch systemd.
set -Eeuo pipefail
umask 077

[ "$(id -u)" -eq 0 ] || { echo "mock gate requires root" >&2; exit 1; }
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONTROLLER="$ROOT/deploy/release-stop-the-world.sh"
TMP="$(mktemp -d "${PANDORA_MOCK_TMPDIR:-${TMPDIR:-/tmp}}/pandora-release-mock.XXXXXX")"
HOLDER_PID=""
cleanup() {
  [ -z "$HOLDER_PID" ] || kill "$HOLDER_PID" >/dev/null 2>&1 || true
  rm -rf -- "$TMP"
}
trap cleanup EXIT

mkdir -p "$TMP/mock-bin" "$TMP/systemctl-state" "$TMP/app/bin" "$TMP/app/pdnd-dist" "$TMP/app/migrations" "$TMP/app/deploy" "$TMP/backup-output"
: >"$TMP/systemctl.log"
: >"$TMP/events.log"
export MOCK_SYSTEMCTL_LOG="$TMP/systemctl.log"
# 全部桩按发生顺序写进同一份事件日志，用来证明「预检在停服之前」。迁移脚本经 env -i
# 启动、拿不到环境变量，它们从 PATH 第一项（mock-bin）推出日志位置。
export MOCK_EVENT_LOG="$TMP/events.log"
export MOCK_SYSTEMCTL_STATE_DIR="$TMP/systemctl-state"
export MOCK_APP_DIR="$TMP/app"
for unit in nginx.service aegis-admin.service aegis-public.service aegis-node.service; do
  : >"$TMP/systemctl-state/active.$unit"
done

cat >"$TMP/mock-bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
unit="${!#:-}"
case "${1:-}" in
  cat) exit 0 ;;
  is-active) test -f "$MOCK_SYSTEMCTL_STATE_DIR/active.$unit" ;;
  show)
    if ! test -f "$MOCK_SYSTEMCTL_STATE_DIR/active.$unit"; then printf '0\n'; exit 0; fi
    case "$unit" in
      aegis-admin.service) printf '4194301\n' ;;
      aegis-public.service) printf '4194302\n' ;;
      aegis-node.service) printf '4194303\n' ;;
      *) printf '4194300\n' ;;
    esac
    ;;
  stop)
    printf '%s\n' "$*" >>"$MOCK_SYSTEMCTL_LOG"
    printf 'systemctl %s\n' "$*" >>"$MOCK_EVENT_LOG"
    shift
    for unit in "$@"; do rm -f "$MOCK_SYSTEMCTL_STATE_DIR/active.$unit"; done
    ;;
  start)
    printf '%s\n' "$*" >>"$MOCK_SYSTEMCTL_LOG"
    printf 'systemctl %s\n' "$*" >>"$MOCK_EVENT_LOG"
    shift
    for unit in "$@"; do : >"$MOCK_SYSTEMCTL_STATE_DIR/active.$unit"; done
    ;;
  *) exit 0 ;;
esac
MOCK
cat >"$TMP/mock-bin/readelf" <<'MOCK'
#!/usr/bin/env bash
case "${*: -1}" in
  *arm64*) machine='AArch64' ;;
  *) machine='Advanced Micro Devices X86-64' ;;
esac
printf '  Machine:                           %s\n' "$machine"
MOCK
cat >"$TMP/mock-bin/curl" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
cat >"$TMP/mock-bin/ss" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
cat >"$TMP/mock-bin/readlink" <<'MOCK'
#!/usr/bin/env bash
case "${2:-${1:-}}" in
  /proc/4194301/exe) printf '%s/bin/aegis-admin\n' "$MOCK_APP_DIR" ;;
  /proc/4194302/exe) printf '%s/bin/aegis-public\n' "$MOCK_APP_DIR" ;;
  /proc/4194303/exe) printf '%s/bin/aegis-node\n' "$MOCK_APP_DIR" ;;
  *) exec /usr/bin/readlink "$@" ;;
esac
MOCK
cat >"$TMP/mock-bin/sha256sum" <<'MOCK'
#!/usr/bin/env bash
case "${1:-}" in
  /proc/4194301/exe) exec /usr/bin/sha256sum "$MOCK_APP_DIR/bin/aegis-admin" ;;
  /proc/4194302/exe) exec /usr/bin/sha256sum "$MOCK_APP_DIR/bin/aegis-public" ;;
  /proc/4194303/exe) exec /usr/bin/sha256sum "$MOCK_APP_DIR/bin/aegis-node" ;;
  *) exec /usr/bin/sha256sum "$@" ;;
esac
MOCK
cat >"$TMP/mock-bin/rm" <<'MOCK'
#!/usr/bin/env bash
if [ "${MOCK_FAIL_STAGE_RM:-0}" = 1 ]; then
  for arg in "$@"; do
    case "$arg" in "$MOCK_APP_DIR"/.release-stage/*) exit 97 ;; esac
  done
fi
exec /usr/bin/rm "$@"
MOCK
cat >"$TMP/mock-bin/cp" <<'MOCK'
#!/usr/bin/env bash
/usr/bin/cp "$@"
if [ "${MOCK_TAMPER_STAGE:-0}" = 1 ]; then
  last="${@: -1}"
  case "$last" in
    */.release-stage/*/release/SHA256SUMS) printf 'tampered-stage-manifest\n' >>"$last" ;;
  esac
fi
MOCK
chmod 0755 "$TMP/mock-bin/systemctl" "$TMP/mock-bin/readelf" "$TMP/mock-bin/curl" \
  "$TMP/mock-bin/ss" "$TMP/mock-bin/readlink" "$TMP/mock-bin/sha256sum" "$TMP/mock-bin/rm" "$TMP/mock-bin/cp"

cat >"$TMP/app/deploy/.env" <<'ENV'
AEGIS_PUBLIC_ADDR=127.0.0.1:9000
AEGIS_ADMIN_ADDR=127.0.0.1:9001
AEGIS_NODE_ADDR=127.0.0.1:9003
PANDORA_NATIVE_RELEASE_VERSION=mock-v1
ENV
cat >"$TMP/app/deploy/backup-postgres.sh" <<'MOCK'
#!/usr/bin/env bash
printf 'backup\n' >>"${MOCK_EVENT_LOG:?}"
artifact="${MOCK_BACKUP_DIR:?}/database-backup.age"
printf 'encrypted test backup\n' >"$artifact"
/usr/bin/sha256sum "$artifact" >"$artifact.sha256"
printf 'backup complete: %s\n' "$artifact"
MOCK
chmod 0755 "$TMP/app/deploy/backup-postgres.sh"
export MOCK_BACKUP_DIR="$TMP/backup-output"

run_controller() {
  PATH="$TMP/mock-bin:$PATH" \
    PANDORA_APP_DIR="${MOCK_CONTROLLER_APP_DIR:-$TMP/app}" \
    PANDORA_RELEASE_MANIFEST_SHA256="${MOCK_MANIFEST_DIGEST:-}" \
    MOCK_TAMPER_STAGE="${MOCK_TAMPER_STAGE:-0}" \
    PANDORA_RELEASE_LOCK="$TMP/release.lock" \
    PANDORA_RELEASE_WAIT_SECONDS=5 \
    "$CONTROLLER" "$@"
}

run_expect_failure() {
  local expected="$1"; shift
  local stderr_file="$TMP/stderr.$RANDOM"
  if run_controller "$@" >/dev/null 2>"$stderr_file"; then
    echo "expected controller failure" >&2
    exit 1
  fi
  grep -Fq "$expected" "$stderr_file" || {
    echo "wrong preflight failure; expected: $expected" >&2
    sed -n '1,20p' "$stderr_file" >&2
    exit 1
  }
  [ ! -s "$TMP/systemctl.log" ] || {
    echo "preflight failure touched systemd: $(cat "$TMP/systemctl.log")" >&2
    exit 1
  }
}

# Missing argument and invalid manifest are both preflight failures.
run_expect_failure 'usage:'
mkdir -p "$TMP/bad-release/bin" "$TMP/bad-release/migrations" "$TMP/bad-release/deploy"
run_expect_failure 'release is missing pdnd-dist/' "$TMP/bad-release"

# Build the smallest checksum-valid release needed to reach lock acquisition.
mkdir -p "$TMP/good-release/bin" "$TMP/good-release/pdnd-dist" "$TMP/good-release/migrations" "$TMP/good-release/deploy"
for binary in aegis-admin aegis-public aegis-node; do
  printf '#!/bin/sh\nexit 0\n' >"$TMP/good-release/bin/$binary"
  chmod 0755 "$TMP/good-release/bin/$binary"
done
for arch in amd64 arm64; do
  printf '#!/bin/sh\nexit 0\n' >"$TMP/good-release/pdnd-dist/pandora-native-linux-$arch"
  chmod 0755 "$TMP/good-release/pdnd-dist/pandora-native-linux-$arch"
done
printf '%s\n' '-- +goose Up' 'SELECT 1;' '-- +goose Down' 'SELECT 1;' \
  >"$TMP/good-release/migrations/00001_test.sql"
for script in platform.sh release-stop-the-world.sh; do
  cp "$ROOT/deploy/$script" "$TMP/good-release/deploy/$script"
  chmod 0755 "$TMP/good-release/deploy/$script"
done
# 预检桩：完整预检写凭据，--verify-attestation 只记一笔；$TMP/fail_verify 存在时核对失败
cat >"$TMP/good-release/deploy/check-migrations.sh" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "${PATH%%:*}/.." && pwd)"
if [ "${1:-}" = --verify-attestation ]; then
  printf 'verify %s approved=%s\n' "$2" "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" >>"$root/events.log"
  [ ! -f "$root/fail_verify" ] || exit 78
  exit 0
fi
printf 'precheck rehearse=%s approved=%s dir=%s\n' "${PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER:-}" \
  "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" "${AEGIS_MIGRATIONS_DIR:-}" >>"$root/events.log"
[ -n "${PANDORA_PRECHECK_ATTESTATION_OUT:-}" ] || exit 64
printf 'format=mock\n' >"$PANDORA_PRECHECK_ATTESTATION_OUT"
MOCK
cat >"$TMP/good-release/deploy/migrate.sh" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "${PATH%%:*}/.." && pwd)"
printf 'migrate %s attestation=%s approved=%s\n' "$*" "${PANDORA_PRECHECK_ATTESTATION:-}" \
  "${PANDORA_STOPPED_WRITER_UPGRADE_APPROVED:-}" >>"$root/events.log"
[ -s "${PANDORA_PRECHECK_ATTESTATION:-/nonexistent}" ] || exit 65
MOCK
chmod 0755 "$TMP/good-release/deploy/check-migrations.sh" "$TMP/good-release/deploy/migrate.sh"
amd64_digest="$(sha256sum "$TMP/good-release/pdnd-dist/pandora-native-linux-amd64" | awk '{print $1}')"
arm64_digest="$(sha256sum "$TMP/good-release/pdnd-dist/pandora-native-linux-arm64" | awk '{print $1}')"
cat >"$TMP/good-release/deploy/release-artifact.env" <<ENV
PANDORA_NATIVE_RELEASE_VERSION=mock-v1
PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=$amd64_digest
PANDORA_NATIVE_ARTIFACT_ARM64_SHA256=$arm64_digest
ENV
cat >>"$TMP/app/deploy/.env" <<ENV
PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=$amd64_digest
PANDORA_NATIVE_ARTIFACT_ARM64_SHA256=$arm64_digest
ENV
(
  cd "$TMP/good-release"
  find bin pdnd-dist migrations deploy -type f -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS
)
GOOD_MANIFEST_DIGEST="$(sha256sum "$TMP/good-release/SHA256SUMS" | awk '{print $1}')"
MOCK_MANIFEST_DIGEST=""
run_expect_failure 'PANDORA_RELEASE_MANIFEST_SHA256 must be a 64-hex out-of-band digest' "$TMP/good-release"
MOCK_MANIFEST_DIGEST="$(printf '%064d' 0)"
run_expect_failure 'release manifest digest mismatch' "$TMP/good-release"
MOCK_MANIFEST_DIGEST="$GOOD_MANIFEST_DIGEST"

# A release with a stale live binding must be rejected before systemd or the
# release lock is touched.
cp -a "$TMP/good-release" "$TMP/bad-binding-release"
sed -i 's/^PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=.*/PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/' "$TMP/bad-binding-release/deploy/release-artifact.env"
(
  cd "$TMP/bad-binding-release"
  find bin pdnd-dist migrations deploy -type f -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS
)
MOCK_MANIFEST_DIGEST="$(sha256sum "$TMP/bad-binding-release/SHA256SUMS" | awk '{print $1}')"
run_expect_failure 'release-artifact.env does not match amd64 artifact' "$TMP/bad-binding-release"
MOCK_MANIFEST_DIGEST="$GOOD_MANIFEST_DIGEST"
MOCK_TAMPER_STAGE=1 run_expect_failure 'release manifest digest mismatch' "$TMP/good-release"

# A precheck failure now happens before any service is stopped: systemd is never
# touched and the live layout stays as it was.
cp -a "$TMP/app" "$TMP/rollback-app"
printf 'old-admin\n' >"$TMP/rollback-app/bin/aegis-admin"
printf 'old-public\n' >"$TMP/rollback-app/bin/aegis-public"
printf 'old-node\n' >"$TMP/rollback-app/bin/aegis-node"
printf 'old-native-amd64\n' >"$TMP/rollback-app/pdnd-dist/pandora-native-linux-amd64"
printf 'old-native-arm64\n' >"$TMP/rollback-app/pdnd-dist/pandora-native-linux-arm64"
printf 'old-migration\n' >"$TMP/rollback-app/migrations/00001_test.sql"
cp -a "$TMP/good-release" "$TMP/bad-migration-release"
printf '#!/usr/bin/env bash\nexit 17\n' >"$TMP/bad-migration-release/deploy/check-migrations.sh"
chmod 0755 "$TMP/bad-migration-release/deploy/check-migrations.sh"
(
  cd "$TMP/bad-migration-release"
  find bin pdnd-dist migrations deploy -type f -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS
)
MOCK_MANIFEST_DIGEST="$(sha256sum "$TMP/bad-migration-release/SHA256SUMS" | awk '{print $1}')"
ROLLBACK_STDERR="$TMP/rollback.stderr"
: >"$TMP/systemctl.log"
if MOCK_CONTROLLER_APP_DIR="$TMP/rollback-app" MOCK_APP_DIR="$TMP/rollback-app" run_controller "$TMP/bad-migration-release" >"$TMP/rollback.stdout" 2>"$ROLLBACK_STDERR"; then
  echo 'migration failure was incorrectly reported as success' >&2
  exit 1
fi
grep -Fq 'rollback=not_required' "$TMP/rollback.stdout" || {
  echo 'precheck failure did not stay in the preflight phase' >&2
  cat "$TMP/rollback.stdout" >&2
  cat "$ROLLBACK_STDERR" >&2
  exit 1
}
grep -Fq 'before any service was stopped' "$ROLLBACK_STDERR"
[ ! -s "$TMP/systemctl.log" ] || { echo "precheck failure touched systemd: $(cat "$TMP/systemctl.log")" >&2; exit 1; }
grep -Fxq 'old-admin' "$TMP/rollback-app/bin/aegis-admin"
grep -Fxq 'old-native-amd64' "$TMP/rollback-app/pdnd-dist/pandora-native-linux-amd64"
grep -Fxq 'old-native-arm64' "$TMP/rollback-app/pdnd-dist/pandora-native-linux-arm64"
grep -Fxq 'old-migration' "$TMP/rollback-app/migrations/00001_test.sql"
: >"$TMP/systemctl.log"
MOCK_MANIFEST_DIGEST="$GOOD_MANIFEST_DIGEST"

(
  exec 8>"$TMP/release.lock"
  flock -n 8
  : >"$TMP/lock-ready"
  sleep 20
) &
HOLDER_PID=$!
for _ in $(seq 1 50); do
  [ -f "$TMP/lock-ready" ] && break
  sleep 0.1
done
[ -f "$TMP/lock-ready" ] || { echo "failed to hold test lock" >&2; exit 1; }
run_expect_failure 'another Pandora Panel release is already running' "$TMP/good-release"

kill "$HOLDER_PID" >/dev/null 2>&1 || true
wait "$HOLDER_PID" 2>/dev/null || true
HOLDER_PID=""

assert_duration() {
  local output="$1" name="$2" count value
  count="$(grep -c "^${name}=" "$output")"
  value="$(sed -n "s/^${name}=//p" "$output")"
  [[ "$count" -eq 1 && "$value" =~ ^[1-9][0-9]*$ ]] || {
    echo "invalid duration: $name count=$count value=$value" >&2
    exit 1
  }
}

SUCCESS_STDOUT="$TMP/success.stdout"
SUCCESS_STDERR="$TMP/success.stderr"
run_controller "$TMP/good-release" >"$SUCCESS_STDOUT" 2>"$SUCCESS_STDERR" || {
  echo 'full success-path mock failed' >&2
  sed -n '1,120p' "$SUCCESS_STDERR" >&2
  exit 1
}
assert_duration "$SUCCESS_STDOUT" migration_precheck_duration_ns
assert_duration "$SUCCESS_STDOUT" migration_duration_ns
assert_duration "$SUCCESS_STDOUT" maintenance_window_duration_ns
grep -Fxq 'release_committed=ok' "$SUCCESS_STDOUT"
grep -Fxq 'migration_precheck_before_isolation=ok' "$SUCCESS_STDOUT"
grep -Fxq 'migration_precheck_attestation=ok' "$SUCCESS_STDOUT"

# 顺序证明：完整预检在第一次停服之前；停服之后只有只读核对、备份、迁移。
event_line() { grep -n -m1 -- "$1" "$TMP/events.log" | cut -d: -f1; }
assert_before() {
  local a b
  a="$(event_line "$1")"; b="$(event_line "$2")"
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] || {
    echo "event order violated: '$1' (line ${a:-missing}) must precede '$2' (line ${b:-missing})" >&2
    cat "$TMP/events.log" >&2
    exit 1
  }
}
assert_before '^precheck ' '^systemctl stop nginx.service'
assert_before '^systemctl stop nginx.service' '^systemctl stop aegis-node.service'
assert_before '^systemctl stop aegis-node.service' '^verify '
assert_before '^verify ' '^backup'
assert_before '^backup' '^migrate up '
assert_before '^migrate up ' '^systemctl start aegis-admin.service'
[ "$(grep -c '^precheck ' "$TMP/events.log")" -eq 1 ] || { echo 'full precheck must run exactly once' >&2; cat "$TMP/events.log" >&2; exit 1; }
grep -Eq '^precheck rehearse=yes approved= dir=.*/\.release-stage/[^/]+/release/migrations$' "$TMP/events.log" || {
  echo 'pre-stop precheck must rehearse on the staged release without claiming writers are stopped' >&2
  cat "$TMP/events.log" >&2
  exit 1
}
grep -Eq '^verify .*/\.release-stage/[^/]+/precheck\.attestation approved=yes$' "$TMP/events.log"
grep -Eq '^migrate up attestation=.*/\.release-stage/[^/]+/precheck\.attestation approved=yes$' "$TMP/events.log"

# 停服后核对不过：库没被碰过，自动拉回旧写入者与入口，迁移不执行。
: >"$TMP/events.log"
touch "$TMP/fail_verify"
VERIFY_STDOUT="$TMP/verify-fail.stdout"
if run_controller "$TMP/good-release" >"$VERIFY_STDOUT" 2>"$TMP/verify-fail.stderr"; then
  echo 'attestation mismatch after stop was reported as success' >&2
  exit 1
fi
rm -f "$TMP/fail_verify"
grep -Fxq 'rollback=writers_and_ingress_restored' "$VERIFY_STDOUT" || {
  echo 'attestation mismatch did not restore writers' >&2
  cat "$VERIFY_STDOUT" "$TMP/verify-fail.stderr" >&2
  exit 1
}
if grep -q '^migrate ' "$TMP/events.log"; then echo 'migration ran after a failed attestation check' >&2; exit 1; fi
if grep -q '^backup' "$TMP/events.log"; then echo 'backup ran after a failed attestation check' >&2; exit 1; fi
for unit in nginx.service aegis-admin.service aegis-public.service aegis-node.service; do
  [ -f "$TMP/systemctl-state/active.$unit" ] || { echo "$unit was not restarted" >&2; exit 1; }
done

# A failure after release_committed evidence must remain non-zero.  This
# directly guards against setting SUCCESS before the fallible stage cleanup.
POSTCOMMIT_STDOUT="$TMP/postcommit.stdout"
POSTCOMMIT_STDERR="$TMP/postcommit.stderr"
if MOCK_FAIL_STAGE_RM=1 run_controller "$TMP/good-release" >"$POSTCOMMIT_STDOUT" 2>"$POSTCOMMIT_STDERR"; then
  echo 'post-commit stage cleanup failure was incorrectly reported as success' >&2
  exit 1
fi
grep -Fxq 'release_committed=ok' "$POSTCOMMIT_STDOUT"
grep -Fq 'automatic database rollback is prohibited' "$POSTCOMMIT_STDERR"

echo "release-stop-the-world preflight, precheck-before-stop order, success, duration and post-commit failure gates: PASS"
