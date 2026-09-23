#!/usr/bin/env bash
# Pandora Panel schema-changing release controller.
#
# This is deliberately a maintenance-window deployment.  It prevents old and
# new admin/public writers from overlapping while idempotency scopes or schema
# invariants change.  It is not a rolling/blue-green deployment tool.
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=platform.sh
. "$SCRIPT_DIR/platform.sh"

APP_DIR="${PANDORA_APP_DIR:-/opt/aegispanel}"
RELEASE_DIR="${1:-}"
SOURCE_RELEASE_DIR=""
INGRESS_UNIT="${PANDORA_INGRESS_UNIT:-nginx.service}"
LOCK_FILE="${PANDORA_RELEASE_LOCK:-/run/lock/pandora-panel-release.lock}"
WAIT_SECONDS="${PANDORA_RELEASE_WAIT_SECONDS:-45}"
ENV_FILE="$APP_DIR/deploy/.env"
BACKUP_ROOT="$APP_DIR/.release-backups"
STAGE_ROOT="$APP_DIR/.release-stage"

ADMIN_UNIT=aegis-admin.service
PUBLIC_UNIT=aegis-public.service
NODE_UNIT=aegis-node.service
WRITER_UNITS=("$ADMIN_UNIT" "$PUBLIC_UNIT" "$NODE_UNIT")

PHASE=preflight
RELEASE_ID=""
BACKUP_DIR=""
STAGE_DIR=""
INGRESS_WAS_ACTIVE=0
OLD_ADMIN_PID=0
OLD_PUBLIC_PID=0
OLD_NODE_PID=0
MIGRATION_PID=""
MIGRATION_PGID=""
SUCCESS=0
ROLLBACK_RUNNING=0

RELEASE_DEPLOY_FILES=(platform.sh check-migrations.sh migrate.sh release-stop-the-world.sh renewal-cutover.md)
RELEASE_DEPLOY_FILES+=(release-artifact.env)

die() { echo "pandora-release: $*" >&2; exit 1; }
evidence() { printf '%s=%s\n' "$1" "$2"; }

is_uint() { [[ "$1" =~ ^[0-9]+$ ]]; }

require_port() {
  is_uint "$1" && [ "$1" -ge 1 ] && [ "$1" -le 65535 ] \
    || die "invalid health-check port"
}

read_env_file_value() {
	local file="$1" name="$2" fallback="$3" value
	value="$(awk -v key="$name" '
    $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      sub("^[[:space:]]*" key "[[:space:]]*=", "")
      gsub("^[[:space:]]+|[[:space:]]+$", "")
      first=substr($0,1,1); last=substr($0,length($0),1); sq=sprintf("%c",39)
      if ((first == "\"" && last == "\"") || (first == sq && last == sq)) {
        $0=substr($0,2,length($0)-2)
      }
      print; exit
	}' "$file")"
	printf '%s\n' "${value:-$fallback}"
}

read_env_value() { read_env_file_value "$ENV_FILE" "$1" "$2"; }

loopback_port() {
  local label="$1" address="$2" port
  if [[ "$address" =~ ^127\.0\.0\.1:([0-9]+)$ ]]; then
    port="${BASH_REMATCH[1]}"
    require_port "$port"
    printf '%s\n' "$port"
    return 0
  fi
  die "$label must bind to 127.0.0.1:<port> for controlled ingress isolation"
}

unit_pid() {
  systemctl show --property MainPID --value "$1" 2>/dev/null || printf '0\n'
}

wait_unit_inactive() {
  local unit="$1" deadline=$((SECONDS + WAIT_SECONDS))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! systemctl is-active --quiet "$unit" && [ "$(unit_pid "$unit")" = 0 ]; then
      return 0
    fi
    sleep 1
  done
  return 1
}

wait_pid_gone() {
  local pid="$1" deadline=$((SECONDS + WAIT_SECONDS))
  [ "$pid" = 0 ] && return 0
  while [ "$SECONDS" -lt "$deadline" ]; do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 1
  done
  return 1
}

port_is_listening() {
  local port="$1"
  ss -H -ltn | awk -v wanted=":$port" '$4 ~ (wanted "$") { found=1 } END { exit !found }'
}

wait_port_closed() {
  local port="$1" deadline=$((SECONDS + WAIT_SECONDS))
  while [ "$SECONDS" -lt "$deadline" ]; do
    port_is_listening "$port" || return 0
    sleep 1
  done
  return 1
}

wait_http_ready() {
  local url="$1" deadline=$((SECONDS + WAIT_SECONDS))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if curl --fail --silent --show-error --max-time 3 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

prove_running_binary() {
  local unit="$1" binary="$2" pid expected actual expected_hash actual_hash
  systemctl is-active --quiet "$unit" || return 1
  pid="$(unit_pid "$unit")"
  [ "$pid" != 0 ] || return 1
  expected="$(realpath "$APP_DIR/bin/$binary")" || return 1
  actual="$(readlink -f "/proc/$pid/exe")" || return 1
  [ "$actual" = "$expected" ] || return 1
  expected_hash="$(sha256sum "$expected" | awk '{print $1}')"
  actual_hash="$(sha256sum "/proc/$pid/exe" | awk '{print $1}')"
  [ "$actual_hash" = "$expected_hash" ]
}

wait_running_binary() {
  local unit="$1" binary="$2" deadline=$((SECONDS + WAIT_SECONDS))
  while [ "$SECONDS" -lt "$deadline" ]; do
    prove_running_binary "$unit" "$binary" && return 0
    sleep 1
  done
  return 1
}

prove_all_ready() {
  wait_running_binary "$PUBLIC_UNIT" aegis-public || return 1
  wait_running_binary "$ADMIN_UNIT" aegis-admin || return 1
  wait_running_binary "$NODE_UNIT" aegis-node || return 1
  wait_http_ready "http://127.0.0.1:$PUBLIC_PORT/healthz" || return 1
  wait_http_ready "http://127.0.0.1:$PUBLIC_PORT/readyz" || return 1
  wait_http_ready "http://127.0.0.1:$ADMIN_PORT/healthz" || return 1
  wait_http_ready "http://127.0.0.1:$ADMIN_PORT/readyz" || return 1
  wait_http_ready "http://127.0.0.1:$NODE_PORT/healthz" || return 1
}

stop_unit_and_prove() {
  local unit="$1"
  systemctl stop "$unit"
  wait_unit_inactive "$unit" || die "$unit did not become inactive with MainPID=0"
}

verify_release_manifest() {
  local dir="$1" actual expected
  [ -d "$dir/bin" ] || die "release is missing bin/"
  [ -d "$dir/pdnd-dist" ] || die "release is missing pdnd-dist/"
  [ -d "$dir/migrations" ] || die "release is missing migrations/"
  [ -d "$dir/deploy" ] || die "release is missing deploy/"
  [ -f "$dir/SHA256SUMS" ] || die "release is missing SHA256SUMS"
  [ -f "$dir/deploy/release-artifact.env" ] || die "release is missing deploy/release-artifact.env"
  local expected_manifest actual_manifest secure_path path_owner path_mode
  expected_manifest="${PANDORA_RELEASE_MANIFEST_SHA256:-}"
  [[ "$expected_manifest" =~ ^[A-Fa-f0-9]{64}$ ]] || die "PANDORA_RELEASE_MANIFEST_SHA256 must be a 64-hex out-of-band digest"
  actual_manifest="$(sha256sum "$dir/SHA256SUMS" | awk '{print $1}')"
  [ "${actual_manifest,,}" = "${expected_manifest,,}" ] || die "release manifest digest mismatch"
  secure_path="$dir"
  while :; do
    path_owner="$(stat -c %u -- "$secure_path")"
    path_mode="$(stat -c %a -- "$secure_path")"
    path_mode="${path_mode: -3}"
    [ "$path_owner" = 0 ] || die "release path is not root-owned: $secure_path"
    (( (8#$path_mode & 8#022) == 0 )) || die "release path is group/world-writable: $secure_path"
    [ "$secure_path" = / ] && break
    secure_path="$(dirname "$secure_path")"
  done
  if find "$dir" -xdev \( ! -user root -o -perm /022 \) -print -quit | grep -q .; then
    die "release tree must be root-owned and not group/world-writable"
  fi
  [ -x "$dir/deploy/migrate.sh" ] || die "release migrate.sh is not executable"
  [ -x "$dir/deploy/check-migrations.sh" ] || die "release check-migrations.sh is not executable"
  [ -x "$dir/deploy/platform.sh" ] || die "release platform.sh is not executable"
  [ -r "$dir/deploy/renewal-cutover.md" ] || die "release renewal cutover procedure is missing"
  [ -x "$dir/bin/aegis-admin" ] || die "release is missing aegis-admin"
  [ -x "$dir/bin/aegis-public" ] || die "release is missing aegis-public"
  [ -x "$dir/bin/aegis-node" ] || die "release is missing aegis-node"
  if find "$dir/bin" "$dir/pdnd-dist" "$dir/migrations" "$dir/deploy" -type l -print -quit | grep -q .; then
    die "release must not contain symlinks"
  fi
  (cd "$dir" && sha256sum -c SHA256SUMS >/dev/null)
  actual="$(cd "$dir" && find bin pdnd-dist migrations deploy -type f -print | sort)"
  expected="$(awk '{print $2}' "$dir/SHA256SUMS" | sed 's#^\*\?##' | sort)"
  [ "$actual" = "$expected" ] || die "SHA256SUMS must cover exactly bin/, pdnd-dist/, migrations/ and deploy/"

  local release_version live_value expected_value arch artifact node_machine expected_machine
  release_version="$(read_env_file_value "$dir/deploy/release-artifact.env" PANDORA_NATIVE_RELEASE_VERSION "")"
  [ -n "$release_version" ] || die "release-artifact.env is missing PANDORA_NATIVE_RELEASE_VERSION"
  [ "$release_version" = "$(read_env_file_value "$dir/deploy/release-artifact.env" PANDORA_NATIVE_RELEASE_VERSION "")" ] || die "release-artifact.env version is invalid"
  for arch in amd64 arm64; do
    artifact="$dir/pdnd-dist/pandora-native-linux-$arch"
    [ -x "$artifact" ] || die "release is missing executable pdnd-dist/pandora-native-linux-$arch"
    if [ "$arch" = amd64 ]; then expected_machine='Advanced Micro Devices X86-64'; else expected_machine='AArch64'; fi
    node_machine="$(readelf -h "$artifact" 2>/dev/null | awk -F: '/Machine:/{sub(/^[[:space:]]+/,"",$2); print $2; exit}')"
    [ "$node_machine" = "$expected_machine" ] || die "pdnd-dist architecture mismatch for $arch"
    expected_value="$(read_env_file_value "$dir/deploy/release-artifact.env" "PANDORA_NATIVE_ARTIFACT_$(printf '%s' "$arch" | tr '[:lower:]' '[:upper:]')_SHA256" "")"
    [[ "$expected_value" =~ ^[0-9a-f]{64}$ ]] || die "release-artifact.env has invalid $arch SHA-256"
    [ "$(sha256sum "$artifact" | awk '{print $1}')" = "$expected_value" ] || die "release-artifact.env does not match $arch artifact"
    live_value="$(read_env_value "PANDORA_NATIVE_ARTIFACT_$(printf '%s' "$arch" | tr '[:lower:]' '[:upper:]')_SHA256" "")"
    [ "$live_value" = "$expected_value" ] || die "live .env $arch artifact binding does not match release"
  done
  live_value="$(read_env_value PANDORA_NATIVE_RELEASE_VERSION "")"
  [ "$live_value" = "$release_version" ] || die "live .env release version does not match release"

  local expected_machine machine binary
  case "$PANDORA_ARCH" in
    amd64) expected_machine='Advanced Micro Devices X86-64' ;;
    arm64) expected_machine='AArch64' ;;
    *) die "unsupported release architecture" ;;
  esac
  for binary in "$dir"/bin/*; do
    machine="$(readelf -h "$binary" 2>/dev/null | awk -F: '/Machine:/{sub(/^[[:space:]]+/,"",$2); print $2; exit}')"
    [ "$machine" = "$expected_machine" ] || die "release binary architecture does not match host: $(basename "$binary")"
  done
}

restore_old_layout() {
  [ -n "$BACKUP_DIR" ] || return 1
  rm -rf "$APP_DIR/bin.rollback-discard" "$APP_DIR/migrations.rollback-discard"
  # Each directory is recovered independently so a failure between any two
  # rename operations cannot strand the live application without bin/.
  if [ -d "$BACKUP_DIR/bin" ]; then
    [ ! -e "$APP_DIR/bin" ] || mv "$APP_DIR/bin" "$APP_DIR/bin.rollback-discard"
    mv "$BACKUP_DIR/bin" "$APP_DIR/bin"
  fi
  if [ -d "$BACKUP_DIR/migrations" ]; then
    [ ! -e "$APP_DIR/migrations" ] || mv "$APP_DIR/migrations" "$APP_DIR/migrations.rollback-discard"
    mv "$BACKUP_DIR/migrations" "$APP_DIR/migrations"
  fi
  if [ -d "$BACKUP_DIR/pdnd-dist" ]; then
    [ ! -e "$APP_DIR/pdnd-dist" ] || mv "$APP_DIR/pdnd-dist" "$APP_DIR/pdnd-dist.rollback-discard"
    mv "$BACKUP_DIR/pdnd-dist" "$APP_DIR/pdnd-dist"
  elif [ -f "$BACKUP_DIR/pdnd-dist-absent" ]; then
    rm -rf "$APP_DIR/pdnd-dist"
  fi
  [ -d "$APP_DIR/bin" ] || return 1
  [ -d "$APP_DIR/pdnd-dist" ] || return 1
  [ -d "$APP_DIR/migrations" ] || return 1
  rm -rf "$APP_DIR/bin.rollback-discard" "$APP_DIR/migrations.rollback-discard"
  rm -rf "$APP_DIR/pdnd-dist.rollback-discard"

  local file mode
  for file in "${RELEASE_DEPLOY_FILES[@]}"; do
    mode=0755
    [ "${file##*.}" != md ] || mode=0644
    if [ -f "$BACKUP_DIR/deploy/$file" ]; then
      install -m "$mode" "$BACKUP_DIR/deploy/$file" "$APP_DIR/deploy/$file"
    elif [ -f "$BACKUP_DIR/deploy-absent/$file" ]; then
      rm -f "$APP_DIR/deploy/$file"
    fi
  done
}

start_old_services_and_ingress() {
  systemctl start "${WRITER_UNITS[@]}" || return 1
  prove_all_ready || return 1
  if [ "$INGRESS_WAS_ACTIVE" -eq 1 ]; then
    systemctl start "$INGRESS_UNIT" || return 1
  fi
}

attempt_safe_rollback() {
  local reason="$1"
  [ "$ROLLBACK_RUNNING" -eq 0 ] || return 1
  ROLLBACK_RUNNING=1
  echo "pandora-release: failure during phase=$PHASE ($reason)" >&2

  case "$PHASE" in
    preflight)
      evidence rollback not_required
      return 0
      ;;
  esac

  # Never leave a migration child running while rollback decisions are made.
  if [ -n "$MIGRATION_PID" ] && kill -0 "$MIGRATION_PID" 2>/dev/null; then
    kill -TERM -- "-${MIGRATION_PGID:-$MIGRATION_PID}" 2>/dev/null || true
    wait "$MIGRATION_PID" 2>/dev/null || true
  fi

  case "$PHASE" in
    isolating|isolated)
      start_old_services_and_ingress || return 1
      evidence rollback writers_and_ingress_restored
      return 0
      ;;
    layout_switching|layout_switched)
      restore_old_layout || return 1
      start_old_services_and_ingress || return 1
      evidence rollback restored_before_migration
      return 0
      ;;
    migration_attempted|migrated|new_writers_started)
      systemctl stop "${WRITER_UNITS[@]}" >/dev/null 2>&1 || true
      evidence rollback manual_required
      echo "pandora-release: FAIL-CLOSED after migration attempt; use the verified encrypted backup and reviewed recovery procedure" >&2
      return 1
      ;;
    exposure_attempted|committed)
      evidence rollback prohibited_after_exposure
      echo "pandora-release: release may have received traffic; automatic database rollback is prohibited" >&2
      return 1
      ;;
  esac
  return 1
}

on_exit() {
  local rc=$?
  trap - EXIT INT TERM
  if [ "$SUCCESS" -eq 1 ]; then
    exit "$rc"
  fi
  [ "$rc" -ne 0 ] || rc=1
  attempt_safe_rollback "exit=$rc" || true
  exit "$rc"
}

on_signal() {
  local signal="$1"
  echo "pandora-release: received $signal" >&2
  exit 130
}

trap on_exit EXIT
trap 'on_signal INT' INT
trap 'on_signal TERM' TERM

pandora_detect_platform
pandora_require_root
for command in awk basename chown cp curl date dirname find flock grep install kill mv readelf readlink realpath rm sed setsid sha256sum sleep sort ss stat systemctl tail xargs; do
  pandora_require_command "$command"
done
NANOSECONDS_PROBE="$(date +%s%N)"
is_uint "$NANOSECONDS_PROBE" || die "GNU date nanosecond timestamps are required"
if ! is_uint "$WAIT_SECONDS" || [ "$WAIT_SECONDS" -lt 5 ] || [ "$WAIT_SECONDS" -gt 300 ]; then
  die "PANDORA_RELEASE_WAIT_SECONDS must be an integer from 5 to 300"
fi
[ -n "$RELEASE_DIR" ] || die "usage: $0 /absolute/path/to/extracted-release"
[ "${APP_DIR#/}" != "$APP_DIR" ] || die "PANDORA_APP_DIR must be absolute"
APP_DIR="$(realpath "$APP_DIR")"
[ "$APP_DIR" != / ] || die "PANDORA_APP_DIR cannot be /"
ENV_FILE="$APP_DIR/deploy/.env"
BACKUP_ROOT="$APP_DIR/.release-backups"
STAGE_ROOT="$APP_DIR/.release-stage"
RELEASE_DIR="$(realpath "$RELEASE_DIR")"
SOURCE_RELEASE_DIR="$RELEASE_DIR"
case "$SOURCE_RELEASE_DIR" in
  "$APP_DIR"|"$APP_DIR"/*) die "release directory must be outside the live application directory" ;;
esac
[ "${LOCK_FILE#/}" != "$LOCK_FILE" ] || die "release lock path must be absolute"
[ "$LOCK_FILE" != / ] || die "release lock path cannot be /"
LOCK_DIR="$(dirname "$LOCK_FILE")"
[ -d "$LOCK_DIR" ] || die "release lock directory is missing"
[ "$(stat -c %u "$LOCK_DIR")" = 0 ] || die "release lock directory must be root-owned"
[ "$(stat -c %g "$LOCK_DIR")" = 0 ] || die "release lock directory group must be root"
[ ! -L "$LOCK_FILE" ] || die "release lock file must not be a symlink"
[ -r "$ENV_FILE" ] || die "live environment file is missing"
PUBLIC_PORT="$(loopback_port AEGIS_PUBLIC_ADDR "$(read_env_value AEGIS_PUBLIC_ADDR 127.0.0.1:9000)")"
ADMIN_PORT="$(loopback_port AEGIS_ADMIN_ADDR "$(read_env_value AEGIS_ADMIN_ADDR 127.0.0.1:9001)")"
NODE_PORT="$(loopback_port AEGIS_NODE_ADDR "$(read_env_value AEGIS_NODE_ADDR 127.0.0.1:9003)")"
[ -d "$APP_DIR/bin" ] || die "live bin/ is missing"
[ -d "$APP_DIR/migrations" ] || die "live migrations/ is missing"
[ -x "$APP_DIR/deploy/backup-postgres.sh" ] || die "verified encrypted backup command is missing"
for unit in "$INGRESS_UNIT" "${WRITER_UNITS[@]}"; do
  systemctl cat "$unit" >/dev/null 2>&1 || die "required systemd unit is missing: $unit"
done
for unit in "${WRITER_UNITS[@]}"; do
  systemctl is-active --quiet "$unit" || die "writer must be active before release: $unit"
done
verify_release_manifest "$SOURCE_RELEASE_DIR"

exec 9>>"$LOCK_FILE"
flock -n 9 || die "another Pandora Panel release is already running"
evidence release_lock ok

RELEASE_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
BACKUP_DIR="$BACKUP_ROOT/$RELEASE_ID"
STAGE_DIR="$STAGE_ROOT/$RELEASE_ID"
install -d -m 0700 "$BACKUP_DIR" "$STAGE_DIR"
install -d -m 0700 "$STAGE_DIR/release"
cp -a "$SOURCE_RELEASE_DIR/bin" "$STAGE_DIR/release/bin"
cp -a "$SOURCE_RELEASE_DIR/pdnd-dist" "$STAGE_DIR/release/pdnd-dist"
cp -a "$SOURCE_RELEASE_DIR/migrations" "$STAGE_DIR/release/migrations"
cp -a "$SOURCE_RELEASE_DIR/deploy" "$STAGE_DIR/release/deploy"
cp -a "$SOURCE_RELEASE_DIR/SHA256SUMS" "$STAGE_DIR/release/SHA256SUMS"
chown -R root:root "$STAGE_DIR/release"
RELEASE_DIR="$STAGE_DIR/release"
# All later execution and installation uses this root-owned, inaccessible stage.
verify_release_manifest "$RELEASE_DIR"
evidence release_preflight ok

OLD_ADMIN_PID="$(unit_pid "$ADMIN_UNIT")"
OLD_PUBLIC_PID="$(unit_pid "$PUBLIC_UNIT")"
OLD_NODE_PID="$(unit_pid "$NODE_UNIT")"
systemctl is-active --quiet "$INGRESS_UNIT" && INGRESS_WAS_ACTIVE=1 || INGRESS_WAS_ACTIVE=0

# Isolate the only ingress before touching a writer or the schema.
MAINTENANCE_STARTED_NS="$(date +%s%N)"
PHASE=isolating
stop_unit_and_prove "$INGRESS_UNIT"
stop_unit_and_prove "$ADMIN_UNIT"
stop_unit_and_prove "$PUBLIC_UNIT"
stop_unit_and_prove "$NODE_UNIT"
PHASE=isolated
evidence ingress_isolated ok
evidence old_writers_stopped ok

wait_pid_gone "$OLD_ADMIN_PID" || die "old admin PID is still alive"
wait_pid_gone "$OLD_PUBLIC_PID" || die "old public PID is still alive"
wait_pid_gone "$OLD_NODE_PID" || die "old node PID is still alive"
wait_port_closed "$ADMIN_PORT" || die "admin port is still listening"
wait_port_closed "$PUBLIC_PORT" || die "public port is still listening"
wait_port_closed "$NODE_PORT" || die "node port is still listening"
evidence old_pids_gone ok
evidence old_ports_closed ok

BACKUP_OUTPUT="$($APP_DIR/deploy/backup-postgres.sh)" \
  || die "encrypted database backup failed"
BACKUP_ARTIFACT="$(printf '%s\n' "$BACKUP_OUTPUT" | sed -n 's/^backup complete: //p' | tail -n1)"
[ -n "$BACKUP_ARTIFACT" ] && [ -s "$BACKUP_ARTIFACT" ] && [ -s "$BACKUP_ARTIFACT.sha256" ] \
  || die "backup command did not publish a verified encrypted artifact"
evidence database_backup ok

PHASE=layout_switching
install -d -m 0700 "$BACKUP_DIR/deploy" "$BACKUP_DIR/deploy-absent"
for file in "${RELEASE_DEPLOY_FILES[@]}"; do
  mode=0755
  [ "${file##*.}" != md ] || mode=0644
  if [ -f "$APP_DIR/deploy/$file" ]; then
    cp -a "$APP_DIR/deploy/$file" "$BACKUP_DIR/deploy/$file"
  else
    : >"$BACKUP_DIR/deploy-absent/$file"
  fi
  install -m "$mode" "$RELEASE_DIR/deploy/$file" "$APP_DIR/deploy/$file.next"
  mv "$APP_DIR/deploy/$file.next" "$APP_DIR/deploy/$file"
done
mv "$APP_DIR/bin" "$BACKUP_DIR/bin"
if [ -e "$APP_DIR/pdnd-dist" ]; then mv "$APP_DIR/pdnd-dist" "$BACKUP_DIR/pdnd-dist"; else : >"$BACKUP_DIR/pdnd-dist-absent"; fi
mv "$APP_DIR/migrations" "$BACKUP_DIR/migrations"
mv "$RELEASE_DIR/bin" "$APP_DIR/bin"
mv "$RELEASE_DIR/pdnd-dist" "$APP_DIR/pdnd-dist"
mv "$RELEASE_DIR/migrations" "$APP_DIR/migrations"
PHASE=layout_switched
evidence release_layout_switched ok

# Run the disposable-database precheck while automatic old-layout recovery is
# still safe.  Only the production goose command crosses the fail-closed line.
MIGRATION_PRECHECK_STARTED_NS="$(date +%s%N)"
setsid env -i PATH="$PATH" HOME="${HOME:-/root}" \
  AEGIS_ENV_FILE="$ENV_FILE" AEGIS_MIGRATIONS_DIR="$APP_DIR/migrations" \
  PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes \
  "$RELEASE_DIR/deploy/check-migrations.sh" &
MIGRATION_PID=$!
MIGRATION_PGID=$MIGRATION_PID
if ! wait "$MIGRATION_PID"; then
  MIGRATION_PID=""
  MIGRATION_PGID=""
  die "disposable-database migration precheck failed"
fi
MIGRATION_PID=""
MIGRATION_PGID=""
evidence migration_precheck ok
MIGRATION_PRECHECK_FINISHED_NS="$(date +%s%N)"
evidence migration_precheck_duration_ns "$((MIGRATION_PRECHECK_FINISHED_NS - MIGRATION_PRECHECK_STARTED_NS))"

# Set this phase immediately before spawning production goose: goose may commit
# earlier migrations and then fail later in the same `up` command.
# This controller deliberately chooses the stop-write transactional index-build
# branch: ingress and all three writers were already proven stopped above.
PHASE=migration_attempted
MIGRATION_STARTED_NS="$(date +%s%N)"
setsid env -i PATH="$PATH" HOME="${HOME:-/root}" \
  AEGIS_ENV_FILE="$ENV_FILE" AEGIS_MIGRATIONS_DIR="$APP_DIR/migrations" \
  PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes \
  PANDORA_LOCAL_MIGRATION_APPROVED=yes \
  "$RELEASE_DIR/deploy/migrate.sh" up &
MIGRATION_PID=$!
MIGRATION_PGID=$MIGRATION_PID
if ! wait "$MIGRATION_PID"; then
  MIGRATION_PID=""
  die "database migration failed"
fi
MIGRATION_PID=""
MIGRATION_PGID=""
PHASE=migrated
evidence migration ok
MIGRATION_FINISHED_NS="$(date +%s%N)"
evidence migration_duration_ns "$((MIGRATION_FINISHED_NS - MIGRATION_STARTED_NS))"

systemctl start "${WRITER_UNITS[@]}"
PHASE=new_writers_started
prove_all_ready || die "new writer binary identity or readiness check failed"
evidence new_writers_started ok
evidence health ok
evidence readiness ok

if [ "$INGRESS_WAS_ACTIVE" -eq 1 ]; then
  # From this point traffic may reach the new schema.  Automatic Down is no
  # longer safe even if systemctl start itself is interrupted or fails.
  PHASE=exposure_attempted
  systemctl start "$INGRESS_UNIT"
  systemctl is-active --quiet "$INGRESS_UNIT" || die "ingress failed to restart"
fi
MAINTENANCE_FINISHED_NS="$(date +%s%N)"
PHASE=committed
evidence ingress_restored ok
evidence maintenance_window_duration_ns "$((MAINTENANCE_FINISHED_NS - MAINTENANCE_STARTED_NS))"
evidence release_committed ok
rm -rf "$STAGE_DIR"
SUCCESS=1
exit 0
