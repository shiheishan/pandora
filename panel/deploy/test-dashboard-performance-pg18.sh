#!/usr/bin/env bash
set -Eeuo pipefail

bootstrap_block_runner() {
  printf 'BLOCKED: %s\n' "$2" >&2
  exit 78
}
for command in dirname date tr mktemp install chmod; do
  command -v "$command" >/dev/null 2>&1 ||
    bootstrap_block_runner "command_${command}" "$command is required"
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:-golang:1.26}"
GOOSE_BIN="${PANDORA_TEST_GOOSE_BIN:-}"
GOOSE_VERSION_EXPECTED="${PANDORA_TEST_GOOSE_VERSION:-v3.27.3}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
RUN_SAFE="$(printf '%s' "$RUN_ID" | tr '[:upper:]-' '[:lower:]_')"
PG_CONTAINER="pandora-dashperf-pg-${RUN_ID}"
GO_CONTAINER="pandora-dashperf-go-${RUN_ID}"
NETWORK="pandora-dashperf-net-${RUN_ID}"
PG_VOLUME="pandora-dashperf-pgdata-${RUN_ID}"
MOD_VOLUME="pandora-dashperf-gomod-${RUN_ID}"
LABEL_KEY="pandora.dashboard-performance.run"
DB_NAME="dashperf_${RUN_SAFE}"
POSTGRES_PASSWORD="dashperf-${RUN_ID}-postgres-test-only"
APP_PASSWORD="dashperf-${RUN_ID}-app-test-only"
TENANT_ID="59000000-0000-7000-8000-000000000001"
APP_DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"

# The database is frozen at Linux x64, 2 vCPU and 4 GiB. The Docker server must
# have at least 8 GiB so the 2 GiB Go probe, kernel, Docker and page cache are
# not silently oversubscribed.
PG_CPUS="2.00"
PG_MEMORY_BYTES="4294967296"
GO_CPUS="2.00"
GO_MEMORY_BYTES="2147483648"
HOST_MEMORY_MIN_BYTES="8589934592"
TRAFFIC_P95_LIMIT_NS=3500000000
TRAFFIC_SINGLE_LIMIT_NS=5000000000
BACKLOG_P95_LIMIT_NS=750000000
BACKLOG_SINGLE_LIMIT_NS=1500000000

ARTIFACT_DIR="${PANDORA_TEST_ARTIFACT_DIR:-}"
if [[ -z "$ARTIFACT_DIR" ]]; then
  ARTIFACT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-dashperf-evidence.XXXXXX")"
else
  install -d -m 0700 "$ARTIFACT_DIR"
fi
ARTIFACT_DIR="$(cd "$ARTIFACT_DIR" && pwd)"
AUDIT_LOG_DEST="${PANDORA_TEST_AUDIT_LOG:-$ARTIFACT_DIR/dashboard-performance-${RUN_ID}.log}"
MANIFEST_DEST="${PANDORA_TEST_MANIFEST:-$ARTIFACT_DIR/dashboard-performance-${RUN_ID}.manifest}"
SAMPLES_DEST="$ARTIFACT_DIR/dashboard-performance-${RUN_ID}-samples.tsv"
EXPLAIN_NODES_DEST="$ARTIFACT_DIR/dashboard-performance-${RUN_ID}-explain-nodes.txt"
EXPLAIN_USERS_DEST="$ARTIFACT_DIR/dashboard-performance-${RUN_ID}-explain-users.txt"
EXPLAIN_BACKLOG_DEST="$ARTIFACT_DIR/dashboard-performance-${RUN_ID}-explain-backlog.txt"
RSS_DEST="$ARTIFACT_DIR/dashboard-performance-${RUN_ID}-postgres-rss.tsv"
TEMP_IO_DEST="$ARTIFACT_DIR/dashboard-performance-${RUN_ID}-temp-io.tsv"
[[ "$AUDIT_LOG_DEST" != "$MANIFEST_DEST" ]]

LOG="$(mktemp "${TMPDIR:-/tmp}/pandora-dashperf-log.XXXXXX")"
SOURCE_BEFORE="$(mktemp "${TMPDIR:-/tmp}/pandora-dashperf-source-before.XXXXXX")"
SOURCE_AFTER="$(mktemp "${TMPDIR:-/tmp}/pandora-dashperf-source-after.XXXXXX")"
VOLUMES_BEFORE="$(mktemp "${TMPDIR:-/tmp}/pandora-dashperf-volumes-before.XXXXXX")"
MANIFEST_TMP="$(mktemp "${TMPDIR:-/tmp}/pandora-dashperf-manifest.XXXXXX")"
METADATA="$(mktemp "${TMPDIR:-/tmp}/pandora-dashperf-metadata.XXXXXX")"
chmod 0600 "$LOG" "$SOURCE_BEFORE" "$SOURCE_AFTER" "$VOLUMES_BEFORE" "$MANIFEST_TMP" "$METADATA"

FIRST_ERROR_LINE=""
BLOCKED_INTERFACE=""
DOCKER_QUERIES_READY=0
RESOURCES_CREATED=0
BASELINE_QUERY_FAILED=0
SOURCE_IMMUTABLE="unknown"
SECRET_SCAN="unknown"
PG_VERSION_NUM="unknown"
POSTGRES_IMAGE_ID="unknown"
GO_IMAGE_ID="unknown"
GO_VERSION="unknown"
GOOSE_VERSION="unknown"
GOOSE_BIN_SHA256="unknown"
SNAPSHOT_AT="unknown"
SAMPLE_SUMMARY="unavailable"
PERF_BUDGET_FAILED=0
RSS_SAMPLE_COUNT="unknown"
RSS_MAX_KIB="unknown"
TEMP_FILES_DELTA="unknown"
TEMP_BYTES_DELTA="unknown"
PG_MEMORY_PEAK_BYTES="unknown"
GO_MEMORY_PEAK_BYTES="unknown"
OWNED_MOUNT_SOURCES=()

record_error() {
  local rc=$?
  local command_text="$2"
  command_text="${command_text//$POSTGRES_PASSWORD/[REDACTED]}"
  command_text="${command_text//$APP_PASSWORD/[REDACTED]}"
  if [[ -z "$FIRST_ERROR_LINE" ]]; then
    FIRST_ERROR_LINE="line=$1 rc=$rc command=$command_text"
    printf '%s\n' "$FIRST_ERROR_LINE" >>"$LOG"
  fi
  return "$rc"
}
trap 'record_error "$LINENO" "$BASH_COMMAND"' ERR

block_runner() {
  BLOCKED_INTERFACE="$1"
  echo "BLOCKED: $2" >&2
  exit 78
}

write_source_inventory() {
  local destination="$1"
  {
    printf '%s\n' "$ROOT/go.mod" "$ROOT/go.sum" \
      "$ROOT/deploy/configure-app-role.sql" \
      "$ROOT/deploy/test-dashboard-performance-pg18.sh"
    find "$ROOT/internal" -type f -name '*.go' -print
    find "$ROOT/migrations" -maxdepth 1 -type f -name '*.sql' -print
  } | LC_ALL=C sort -u | while IFS= read -r file; do
    [[ -f "$file" ]]
    printf '%s  %s\n' "$(sha256sum "$file" | awk '{print $1}')" "${file#"$ROOT"/}"
  done >"$destination"
}

artifact_block() {
  local label="$1" file="$2"
  printf '%s\n' "--- ${label} ---"
  if [[ -s "$file" ]]; then
    cat "$file"
  else
    printf 'unavailable\n'
  fi
}

cleanup() {
  local business_rc="$1" cleanup_rc=0 final_rc query_failed="$BASELINE_QUERY_FAILED"
  local containers='' networks='' volumes='' labels_c='' labels_n='' labels_v=''
  local exact_c=0 exact_n=0 exact_v=0 label_remaining=0 mount_remaining=0 anonymous_new=-1
  local source new_all='' audit_sha='unavailable' manifest_sha='unavailable'
  trap - EXIT ERR INT TERM
  set +e

  if [[ "$RESOURCES_CREATED" -eq 1 ]]; then
    docker rm -fv "$GO_CONTAINER" >/dev/null 2>&1
    docker rm -fv "$PG_CONTAINER" >/dev/null 2>&1
    docker network rm "$NETWORK" >/dev/null 2>&1
    docker volume rm "$PG_VOLUME" >/dev/null 2>&1
    docker volume rm "$MOD_VOLUME" >/dev/null 2>&1
  fi

  if [[ "$DOCKER_QUERIES_READY" -eq 1 ]]; then
    containers="$(docker ps -a --format '{{.Names}}' 2>/dev/null)" || query_failed=1
    networks="$(docker network ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    volumes="$(docker volume ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    labels_c="$(docker ps -a --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Names}}' 2>/dev/null)" || query_failed=1
    labels_n="$(docker network ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    labels_v="$(docker volume ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    exact_c="$(awk -v a="$PG_CONTAINER" -v b="$GO_CONTAINER" '$0==a||$0==b{n++} END{print n+0}' <<<"$containers")"
    exact_n="$(awk -v x="$NETWORK" '$0==x{n++} END{print n+0}' <<<"$networks")"
    exact_v="$(awk -v a="$PG_VOLUME" -v b="$MOD_VOLUME" '$0==a||$0==b{n++} END{print n+0}' <<<"$volumes")"
    label_remaining=$((
      $(awk 'NF{n++} END{print n+0}' <<<"$labels_c") +
      $(awk 'NF{n++} END{print n+0}' <<<"$labels_n") +
      $(awk 'NF{n++} END{print n+0}' <<<"$labels_v")
    ))
    for source in "${OWNED_MOUNT_SOURCES[@]}"; do
      [[ ! -e "$source" ]] || mount_remaining=$((mount_remaining + 1))
    done
    printf '%s\n' "$volumes" | awk 'NF' | LC_ALL=C sort >"$LOG.all.after" || query_failed=1
    if [[ "$query_failed" -eq 0 ]]; then
      new_all="$(comm -13 "$VOLUMES_BEFORE" "$LOG.all.after")"
      anonymous_new="$(awk 'length($0)==64 && $0~/^[0-9a-f]+$/{n++} END{print n+0}' <<<"$new_all")"
    fi
  elif [[ "$RESOURCES_CREATED" -eq 1 ]]; then
    query_failed=1
  else
    anonymous_new=0
  fi

  write_source_inventory "$SOURCE_AFTER" || cleanup_rc=1
  if cmp -s "$SOURCE_BEFORE" "$SOURCE_AFTER"; then
    SOURCE_IMMUTABLE="ok"
  else
    SOURCE_IMMUTABLE="failed"
    cleanup_rc=1
  fi

  # Scan every retained evidence file and the private log before publishing the
  # manifest. The manifest is constructed only from redacted metadata below.
  if grep -Fq "$POSTGRES_PASSWORD" "$LOG" || grep -Fq "$APP_PASSWORD" "$LOG" ||
     grep -R -F -q --exclude='*.manifest' "$POSTGRES_PASSWORD" "$ARTIFACT_DIR" 2>/dev/null ||
     grep -R -F -q --exclude='*.manifest' "$APP_PASSWORD" "$ARTIFACT_DIR" 2>/dev/null; then
    SECRET_SCAN="failed"
    cleanup_rc=1
    sed -i "s/$POSTGRES_PASSWORD/[REDACTED]/g; s/$APP_PASSWORD/[REDACTED]/g" "$LOG" 2>/dev/null || true
    while IFS= read -r source; do
      sed -i "s/$POSTGRES_PASSWORD/[REDACTED]/g; s/$APP_PASSWORD/[REDACTED]/g" "$source" 2>/dev/null || true
    done < <(find "$ARTIFACT_DIR" -type f ! -name '*.manifest' -print 2>/dev/null)
  else
    SECRET_SCAN="ok"
  fi

  if [[ "$query_failed" -ne 0 || "$exact_c" -ne 0 || "$exact_n" -ne 0 ||
        "$exact_v" -ne 0 || "$label_remaining" -ne 0 || "$mount_remaining" -ne 0 ||
        "$anonymous_new" -ne 0 ]]; then
    cleanup_rc=1
  fi

  install -m 0600 "$LOG" "$AUDIT_LOG_DEST" || cleanup_rc=1
  [[ ! -f "$AUDIT_LOG_DEST" ]] || audit_sha="$(sha256sum "$AUDIT_LOG_DEST" | awk '{print $1}')"

  if [[ "$business_rc" -eq 0 && "$cleanup_rc" -eq 0 ]]; then
    final_rc=0
  elif [[ "$business_rc" -ne 0 ]]; then
    final_rc="$business_rc"
  else
    final_rc=1
  fi

  {
    printf 'schema=pandora-dashboard-performance-pg18-manifest-v1\n'
    printf 'run_id=%s\ncreated_at_utc=%s\n' "$RUN_ID" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'database_name=%s\ntenant_id=%s\nsnapshot_at=%s\n' "$DB_NAME" "$TENANT_ID" "$SNAPSHOT_AT"
    printf 'postgres_image=%s\npostgres_image_id=%s\npostgres_version_num=%s\n' \
      "$POSTGRES_IMAGE" "$POSTGRES_IMAGE_ID" "$PG_VERSION_NUM"
    printf 'postgres_cpu_limit=%s\npostgres_memory_limit_bytes=%s\npostgres_pids_limit=512\n' \
      "$PG_CPUS" "$PG_MEMORY_BYTES"
    printf 'go_image=%s\ngo_image_id=%s\ngo_version=%s\n' "$GO_IMAGE" "$GO_IMAGE_ID" "$GO_VERSION"
    printf 'go_cpu_limit=%s\ngo_memory_limit_bytes=%s\ngo_pids_limit=512\n' "$GO_CPUS" "$GO_MEMORY_BYTES"
    printf 'goose_version=%s\ngoose_bin_sha256=%s\n' "$GOOSE_VERSION" "$GOOSE_BIN_SHA256"
    printf 'source_inventory_sha256=%s\naudit_log_sha256=%s\n' \
      "$(sha256sum "$SOURCE_BEFORE" | awk '{print $1}')" "$audit_sha"
    printf 'traffic_p95_limit_ns=%s\ntraffic_single_limit_ns=%s\n' "$TRAFFIC_P95_LIMIT_NS" "$TRAFFIC_SINGLE_LIMIT_NS"
    printf 'backlog_p95_limit_ns=%s\nbacklog_single_limit_ns=%s\n' "$BACKLOG_P95_LIMIT_NS" "$BACKLOG_SINGLE_LIMIT_NS"
    printf 'nearest_rank_sample_count=20\nnearest_rank_p95_position=19\n'
    printf 'sample_summary=%s\n' "$SAMPLE_SUMMARY"
    printf 'postgres_rss_sample_count=%s\npostgres_rss_max_kib=%s\n' "$RSS_SAMPLE_COUNT" "$RSS_MAX_KIB"
    printf 'postgres_temp_files_delta=%s\npostgres_temp_bytes_delta=%s\n' "$TEMP_FILES_DELTA" "$TEMP_BYTES_DELTA"
    printf 'postgres_memory_peak_bytes=%s\ngo_memory_peak_bytes=%s\n' "$PG_MEMORY_PEAK_BYTES" "$GO_MEMORY_PEAK_BYTES"
    printf 'source_immutable=%s\nsecret_scan=%s\n' "$SOURCE_IMMUTABLE" "$SECRET_SCAN"
    printf 'business_exit=%s\ncleanup_exit=%s\nfinal_exit=%s\n' "$business_rc" "$cleanup_rc" "$final_rc"
    printf 'cleanup_query_failed=%s\ncleanup_exact_containers=%s\ncleanup_exact_network=%s\n' \
      "$query_failed" "$exact_c" "$exact_n"
    printf 'cleanup_exact_volumes=%s\ncleanup_label_residual=%s\n' "$exact_v" "$label_remaining"
    printf 'cleanup_mount_source_residual=%s\ncleanup_anonymous_volume_new=%s\n' \
      "$mount_remaining" "$anonymous_new"
    [[ -z "$BLOCKED_INTERFACE" ]] || printf 'blocked_interface=%s\n' "$BLOCKED_INTERFACE"
    [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error=%s\n' "$FIRST_ERROR_LINE"
    if [[ "$cleanup_rc" -eq 0 ]]; then
      printf 'cleanup_marker=dashboard_performance_cleanup_ok\n'
    else
      printf 'cleanup_marker=dashboard_performance_cleanup_failed\n'
    fi
    printf '%s\n' '--- artifact_sha256 ---'
    for source in "$SAMPLES_DEST" "$EXPLAIN_NODES_DEST" "$EXPLAIN_USERS_DEST" "$EXPLAIN_BACKLOG_DEST" "$RSS_DEST" "$TEMP_IO_DEST"; do
      [[ ! -s "$source" ]] || printf '%s  %s\n' "$(sha256sum "$source" | awk '{print $1}')" "$(basename "$source")"
    done
    artifact_block hardware_and_postgres "$METADATA"
    artifact_block samples "$SAMPLES_DEST"
    artifact_block explain_nodes "$EXPLAIN_NODES_DEST"
    artifact_block explain_users "$EXPLAIN_USERS_DEST"
    artifact_block explain_backlog "$EXPLAIN_BACKLOG_DEST"
    artifact_block postgres_rss "$RSS_DEST"
    artifact_block postgres_temp_io_phases "$TEMP_IO_DEST"
    printf '%s\n' '--- source_sha256 ---'
    cat "$SOURCE_BEFORE"
  } >"$MANIFEST_TMP" || cleanup_rc=1
  install -m 0600 "$MANIFEST_TMP" "$MANIFEST_DEST" || cleanup_rc=1

  rm -f "$LOG" "$LOG.all.after" "$SOURCE_BEFORE" "$SOURCE_AFTER" \
    "$VOLUMES_BEFORE" "$MANIFEST_TMP" "$METADATA" || cleanup_rc=1
  for source in "$LOG" "$LOG.all.after" "$SOURCE_BEFORE" "$SOURCE_AFTER" \
    "$VOLUMES_BEFORE" "$MANIFEST_TMP" "$METADATA"; do
    [[ ! -e "$source" ]] || cleanup_rc=1
  done
  if [[ "$cleanup_rc" -ne 0 ]]; then
    [[ "$final_rc" -ne 0 ]] || final_rc=1
    sed -i 's/^cleanup_exit=0$/cleanup_exit=1/; s/^final_exit=0$/final_exit=1/; s/^cleanup_marker=dashboard_performance_cleanup_ok$/cleanup_marker=dashboard_performance_cleanup_failed/' \
      "$MANIFEST_DEST" 2>/dev/null || true
  fi
  [[ ! -f "$MANIFEST_DEST" ]] || manifest_sha="$(sha256sum "$MANIFEST_DEST" | awk '{print $1}')"

  printf 'business_exit=%s cleanup_exit=%s final_exit=%s\n' "$business_rc" "$cleanup_rc" "$final_rc" >&3
  printf 'sample_summary=%s\n' "$SAMPLE_SUMMARY" >&3
  printf 'source_immutable=%s secret_scan=%s\n' "$SOURCE_IMMUTABLE" "$SECRET_SCAN" >&3
  if [[ "$cleanup_rc" -eq 0 ]]; then
    echo 'cleanup_marker=dashboard_performance_cleanup_ok' >&3
  else
    echo 'cleanup_marker=dashboard_performance_cleanup_failed' >&4
  fi
  [[ -z "$BLOCKED_INTERFACE" ]] || printf 'blocked_interface=%s\n' "$BLOCKED_INTERFACE" >&4
  [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error=%s\n' "$FIRST_ERROR_LINE" >&4
  printf 'audit_log=%s audit_log_sha256=%s\n' "$AUDIT_LOG_DEST" "$audit_sha" >&3
  printf 'manifest=%s manifest_sha256=%s\n' "$MANIFEST_DEST" "$manifest_sha" >&3
  exit "$final_rc"
}
trap 'cleanup $?' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

exec 3>&1 4>&2
exec >>"$LOG" 2>&1

for command in awk basename cat cmp comm find grep install sed seq sha256sum sort stat tr; do
  command -v "$command" >/dev/null 2>&1 || block_runner "command_${command}" "$command is required"
done
write_source_inventory "$SOURCE_BEFORE"
if [[ -z "$GOOSE_BIN" || ! -x "$GOOSE_BIN" ]]; then
  BLOCKED_INTERFACE='PANDORA_TEST_GOOSE_BIN'
  echo 'BLOCKED: a pinned Goose executable is required' >&2
  exit 78
fi
GOOSE_VERSION="$("$GOOSE_BIN" -version | awk '{print $NF}')" ||
  block_runner goose_version 'cannot query the pinned Goose version'
[[ "$GOOSE_VERSION" == "$GOOSE_VERSION_EXPECTED" ]] ||
  block_runner goose_version "Goose $GOOSE_VERSION_EXPECTED is required"
GOOSE_BIN_SHA256="$(sha256sum "$GOOSE_BIN" | awk '{print $1}')"
"$GOOSE_BIN" -dir "$ROOT/migrations" validate
echo 'marker=dashboard_performance_goose_validate_ok'

command -v docker >/dev/null 2>&1 || block_runner docker 'docker is required'
[[ "$(uname -s)" == 'Linux' ]] || block_runner host_os 'native Linux host is required'
[[ "$(uname -m)" == 'x86_64' ]] || block_runner host_arch 'native x86_64 host is required'
docker info >/dev/null || block_runner docker 'Docker Server is unavailable'
DOCKER_HOST_CPUS="$(docker info --format '{{.NCPU}}')"
DOCKER_HOST_MEMORY="$(docker info --format '{{.MemTotal}}')"
DOCKER_SERVER_OS="$(docker info --format '{{.OSType}}')"
DOCKER_SERVER_ARCH="$(docker info --format '{{.Architecture}}')"
[[ "$DOCKER_HOST_CPUS" =~ ^[0-9]+$ && "$DOCKER_HOST_CPUS" -ge 2 ]] || block_runner docker_cpus 'Docker Server must expose at least 2 CPUs'
[[ "$DOCKER_HOST_MEMORY" =~ ^[0-9]+$ && "$DOCKER_HOST_MEMORY" -ge "$HOST_MEMORY_MIN_BYTES" ]] || block_runner docker_memory 'Docker Server must expose at least 8 GiB'
[[ "$DOCKER_SERVER_OS" == 'linux' ]] || block_runner docker_os 'Docker Server must be Linux'
[[ "$DOCKER_SERVER_ARCH" == 'x86_64' || "$DOCKER_SERVER_ARCH" == 'amd64' ]] || block_runner docker_arch 'Docker Server must be native amd64'
docker image inspect "$POSTGRES_IMAGE" >/dev/null 2>&1 || {
  BLOCKED_INTERFACE='postgres_image'; echo "preloaded image is required: $POSTGRES_IMAGE" >&2; exit 78;
}
docker image inspect "$GO_IMAGE" >/dev/null 2>&1 || {
  BLOCKED_INTERFACE='go_image'; echo "preloaded image is required: $GO_IMAGE" >&2; exit 78;
}
[[ "$(docker image inspect -f '{{.Architecture}}' "$POSTGRES_IMAGE")" == 'amd64' ]] || block_runner postgres_image_arch 'PostgreSQL image must be amd64'
[[ "$(docker image inspect -f '{{.Architecture}}' "$GO_IMAGE")" == 'amd64' ]] || block_runner go_image_arch 'Go image must be amd64'
docker volume ls --format '{{.Name}}' 2>/dev/null | LC_ALL=C sort >"$VOLUMES_BEFORE" || BASELINE_QUERY_FAILED=1
[[ "$BASELINE_QUERY_FAILED" -eq 0 ]] || block_runner docker_inventory 'Docker volume inventory is unavailable'
DOCKER_QUERIES_READY=1

up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1;next} up' "$1"; }
psql_admin_db() {
  local database="$1"
  shift
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$database" "$@"
}
psql_admin() { psql_admin_db "$DB_NAME" "$@"; }
assert_cgroup_v2_limits() {
  local container="$1" memory_bytes="$2" expected_cpus="$3"
  local cpu_line quota period memory_max swap_max pids_max
  cpu_line="$(docker exec "$container" sh -ec 'cat /sys/fs/cgroup/cpu.max')" || block_runner cgroup_v2 'cpu.max is unavailable'
  read -r quota period <<<"$cpu_line"
  memory_max="$(docker exec "$container" sh -ec 'cat /sys/fs/cgroup/memory.max')" || block_runner cgroup_v2 'memory.max is unavailable'
  swap_max="$(docker exec "$container" sh -ec 'cat /sys/fs/cgroup/memory.swap.max')" || block_runner cgroup_v2 'memory.swap.max is unavailable'
  pids_max="$(docker exec "$container" sh -ec 'cat /sys/fs/cgroup/pids.max')" || block_runner cgroup_v2 'pids.max is unavailable'
  [[ "$quota" =~ ^[0-9]+$ && "$period" =~ ^[0-9]+$ && "$period" -gt 0 ]] || block_runner cgroup_cpu 'cpu.max must contain an exact numeric quota and period'
  [[ "$quota" -eq $((expected_cpus * period)) ]] || block_runner cgroup_cpu 'actual cgroup CPU quota does not match the frozen limit'
  [[ "$memory_max" == "$memory_bytes" ]] || block_runner cgroup_memory 'actual cgroup memory.max does not match the frozen limit'
  [[ "$swap_max" == '0' ]] || block_runner cgroup_swap 'cgroup swap must be disabled'
  [[ "$pids_max" == '512' ]] || block_runner cgroup_pids 'actual cgroup pids.max must be 512'
}
configure_role() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
    "$PG_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" \
    -f /src/deploy/configure-app-role.sql >/dev/null
}
apply_guarded() {
  local number="$1" name="$2"
  {
    echo 'BEGIN;'
    echo "SET LOCAL app.idempotency_writers_stopped='yes';"
    echo "SET LOCAL app.allow_idempotency_schema${number}_up='yes';"
    up_sql "$ROOT/migrations/$name"
    echo 'COMMIT;'
  } | psql_admin >/dev/null
}

docker network create --label "$LABEL_KEY=$RUN_ID" "$NETWORK" >/dev/null
RESOURCES_CREATED=1
docker volume create --label "$LABEL_KEY=$RUN_ID" "$PG_VOLUME" >/dev/null
docker volume create --label "$LABEL_KEY=$RUN_ID" "$MOD_VOLUME" >/dev/null
docker create --name "$PG_CONTAINER" --network "$NETWORK" --network-alias pg18 \
  --label "$LABEL_KEY=$RUN_ID" -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" -e POSTGRES_DB="$DB_NAME" \
  --cpus "$PG_CPUS" --memory "$PG_MEMORY_BYTES" --memory-swap "$PG_MEMORY_BYTES" \
  --pids-limit 512 --shm-size 512m \
  --mount "type=volume,src=$PG_VOLUME,dst=/var/lib/postgresql" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" "$POSTGRES_IMAGE" >/dev/null

[[ "$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$PG_CONTAINER")" == "$RUN_ID" ]] || block_runner postgres_container_identity 'PostgreSQL run label mismatch'
[[ "$(docker inspect -f '{{.HostConfig.NanoCpus}}' "$PG_CONTAINER")" == '2000000000' ]] || block_runner postgres_container_limits 'Docker did not honor the PostgreSQL CPU limit'
[[ "$(docker inspect -f '{{.HostConfig.Memory}}' "$PG_CONTAINER")" == "$PG_MEMORY_BYTES" ]] || block_runner postgres_container_limits 'Docker did not honor the PostgreSQL memory limit'
[[ "$(docker inspect -f '{{.HostConfig.MemorySwap}}' "$PG_CONTAINER")" == "$PG_MEMORY_BYTES" ]] || block_runner postgres_container_limits 'Docker did not disable PostgreSQL swap'
[[ "$(docker inspect -f '{{.HostConfig.PidsLimit}}' "$PG_CONTAINER")" == '512' ]] || block_runner postgres_container_limits 'Docker did not honor the PostgreSQL PID limit'
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER")" == 'null' ]] || block_runner postgres_container_network 'PostgreSQL must not publish host ports'
[[ "$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/src"}}{{.RW}}{{end}}{{end}}' "$PG_CONTAINER")" == 'false' ]] || block_runner postgres_source_mount 'PostgreSQL source mount must be read-only'
while IFS='|' read -r type name source destination rw; do
  [[ -n "$type" ]] || continue
  [[ "$type" != volume ]] || OWNED_MOUNT_SOURCES+=("$source")
done < <(docker inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' "$PG_CONTAINER")
POSTGRES_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")"

docker start "$PG_CONTAINER" >/dev/null
POSTGRES_READY=0
for _ in $(seq 1 120); do
  if [[ "$(docker exec "$PG_CONTAINER" sh -c 'cat /proc/1/comm' 2>/dev/null || true)" == postgres ]] &&
     docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null 2>&1; then
    POSTGRES_READY=1
    break
  fi
  sleep 1
done
[[ "$POSTGRES_READY" -eq 1 ]] || block_runner postgres_runtime 'final PostgreSQL server did not become ready'
docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null || block_runner postgres_runtime 'PostgreSQL readiness probe failed'
assert_cgroup_v2_limits "$PG_CONTAINER" "$PG_MEMORY_BYTES" 2
[[ "$(docker exec "$PG_CONTAINER" uname -s)" == 'Linux' ]] || block_runner postgres_runtime 'PostgreSQL container must be Linux'
[[ "$(docker exec "$PG_CONTAINER" uname -m)" == 'x86_64' ]] || block_runner postgres_runtime 'PostgreSQL container must be x86_64'
PG_VERSION_NUM="$(psql_admin -Atqc 'SHOW server_version_num')" || block_runner postgres_runtime 'cannot query the PostgreSQL version'
[[ "$PG_VERSION_NUM" =~ ^[0-9]+$ && "$PG_VERSION_NUM" -ge 180000 && "$PG_VERSION_NUM" -lt 190000 ]] || block_runner postgres_runtime 'PostgreSQL 18 is required'
echo "marker=dashboard_performance_postgres_version_ok version_num=$PG_VERSION_NUM"

count=0
for migration in "$ROOT"/migrations/000{01..09}_*.sql; do
  [[ -f "$migration" ]]
  { echo 'BEGIN;'; up_sql "$migration"; echo 'COMMIT;'; } | psql_admin >/dev/null
  count=$((count + 1))
done
[[ "$count" -eq 9 ]]
count=9
for migration in "$ROOT"/migrations/000{10..36}_*.sql; do
  [[ -f "$migration" ]]
  { echo 'BEGIN;'; up_sql "$migration"; echo 'COMMIT;'; } | psql_admin >/dev/null
  count=$((count + 1))
done
[[ "$count" -eq 36 ]]
configure_role
apply_guarded 37 00037_idempotency_runtime_hardening.sql
configure_role
apply_guarded 38 00038_idempotency_resource_binding.sql
configure_role
apply_guarded 39 00039_bound_idempotency_success.sql
configure_role
{ echo 'BEGIN;'; up_sql "$ROOT/migrations/00040_order_release_and_late_suspense.sql"; echo 'COMMIT;'; } | psql_admin >/dev/null
configure_role
{ echo 'BEGIN;'; up_sql "$ROOT/migrations/00041_dashboard_read_models.sql"; echo 'COMMIT;'; } | psql_admin >/dev/null
configure_role
echo 'marker=dashboard_performance_migrations_1_41_ok'

SNAPSHOT_AT="$(psql_admin -Atqc \
  "SELECT to_char((statement_timestamp()-interval '2 seconds') AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")"
[[ "$SNAPSHOT_AT" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z$ ]]

# Set-based fixture: 100,000 non-duplicates contain 90,000 x 20 and
# 10,000 x 21 raw entries (2,010,000 total); 10,000 more rows are duplicates.
psql_admin -v tenant_id="$TENANT_ID" -v snapshot_at="$SNAPSHOT_AT" >/dev/null <<'SQL'
BEGIN;
SET LOCAL synchronous_commit=off;
SET LOCAL statement_timeout='30min';

INSERT INTO tenants(id,slug,display_name,timezone,default_currency)
VALUES (:'tenant_id','dash-perf','Dashboard Performance','UTC','USD');
INSERT INTO products(id,tenant_id,code,name,status)
VALUES ('56000000-0000-7000-8000-000000000001',:'tenant_id','perf','Performance Product','active');
INSERT INTO plans(id,tenant_id,product_id,code,name,status)
VALUES ('57000000-0000-7000-8000-000000000001',:'tenant_id',
        '56000000-0000-7000-8000-000000000001','perf','Performance Plan','active');
INSERT INTO plan_versions(id,tenant_id,plan_id,version,frozen_at,status,created_at)
VALUES ('58000000-0000-7000-8000-000000000001',:'tenant_id',
        '57000000-0000-7000-8000-000000000001',1,:'snapshot_at'::timestamptz,
        'published',:'snapshot_at'::timestamptz-interval '60 days');
UPDATE plans SET current_version_id='58000000-0000-7000-8000-000000000001'
 WHERE id='57000000-0000-7000-8000-000000000001';

INSERT INTO users(id,tenant_id,email,status,created_at)
SELECT ('54000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       :'tenant_id',('perf-user-'||gs||'@example.invalid')::citext,'active',
       :'snapshot_at'::timestamptz-interval '60 days'
FROM generate_series(1,20) gs;
INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,
                          snapshot_currency,snapshot_amount,node_uid,created_at)
SELECT ('55000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       :'tenant_id',('54000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       '57000000-0000-7000-8000-000000000001',
       '58000000-0000-7000-8000-000000000001','active','USD',100,gs,
       :'snapshot_at'::timestamptz-interval '60 days'
FROM generate_series(1,20) gs;
INSERT INTO nodes(id,tenant_id,name,display_name,status,created_at)
SELECT ('53000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       :'tenant_id','perf-node-'||lpad(gs::text,2,'0'),'Performance Node '||gs,'active',
       :'snapshot_at'::timestamptz-interval '60 days'
FROM generate_series(1,40) gs;

WITH valid_payload AS MATERIALIZED (
  SELECT jsonb_object_agg(k::text,jsonb_build_array(k*10,k*20)) AS payload
  FROM generate_series(1,20) k
), mixed_payload AS MATERIALIZED (
  SELECT payload || jsonb_build_object('bad-key',jsonb_build_array(1)) AS payload
  FROM valid_payload
)
INSERT INTO node_traffic_reports(id,tenant_id,node_id,user_count,total_upload,total_download,
                                 raw_payload,content_hash,received_at)
SELECT ('51000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       :'tenant_id',
       ('53000000-0000-7000-8000-'||lpad((((gs-1)%40)+1)::text,12,'0'))::uuid,
       20,2100,4200,CASE WHEN gs%10=0 THEN m.payload ELSE v.payload END,
       decode(md5('report-'||gs),'hex'),
       :'snapshot_at'::timestamptz-((((gs-1)*25)%2591999)+1)*interval '1 second'
FROM generate_series(1,100000) gs CROSS JOIN valid_payload v CROSS JOIN mixed_payload m;

INSERT INTO node_traffic_reports(id,tenant_id,node_id,user_count,total_upload,total_download,
                                 raw_payload,content_hash,duplicate_of,received_at)
SELECT ('52000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       :'tenant_id',
       ('53000000-0000-7000-8000-'||lpad((((gs-1)%40)+1)::text,12,'0'))::uuid,
       0,0,0,'{}'::jsonb,decode(md5('duplicate-'||gs),'hex'),
       ('51000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       :'snapshot_at'::timestamptz-(((gs%2591999)+1)*interval '1 second')
FROM generate_series(1,10000) gs;

INSERT INTO notification_deliveries(id,tenant_id,template_code,channel,dedupe_key,status,
                                    attempts,next_retry_at,sent_at,created_at)
SELECT ('61000000-0000-7000-8000-'||lpad(gs::text,12,'0'))::uuid,
       :'tenant_id','perf','email','perf-'||gs,
       CASE WHEN gs<=20000 THEN 'queued'
            WHEN gs<=500000 THEN 'sent'
            WHEN gs<=700000 THEN 'failed'
            WHEN gs<=850000 THEN 'suppressed' ELSE 'bounced' END,
       (gs%4)::smallint,
       CASE WHEN gs<=10000 THEN :'snapshot_at'::timestamptz-interval '700 seconds'
            WHEN gs<=20000 THEN :'snapshot_at'::timestamptz+interval '7 days' END,
       CASE WHEN gs>20000 AND gs<=500000 THEN :'snapshot_at'::timestamptz-interval '1 minute' END,
       :'snapshot_at'::timestamptz-((gs%5184000)*interval '1 second')
FROM generate_series(1,1000000) gs;
COMMIT;
ANALYZE node_traffic_reports;
ANALYZE notification_deliveries;
ANALYZE subscriptions;
ANALYZE users;
ANALYZE nodes;
SQL

fixture_counts="$(psql_admin -Atqc "SELECT
 (SELECT count(*) FROM node_traffic_reports WHERE tenant_id='$TENANT_ID' AND duplicate_of IS NULL
    AND received_at>='$SNAPSHOT_AT'::timestamptz-interval '30 days' AND received_at<'$SNAPSHOT_AT'::timestamptz),
 (SELECT count(*) FROM node_traffic_reports WHERE tenant_id='$TENANT_ID' AND duplicate_of IS NOT NULL
    AND received_at>='$SNAPSHOT_AT'::timestamptz-interval '30 days' AND received_at<'$SNAPSHOT_AT'::timestamptz),
 (SELECT coalesce(sum(jsonb_object_length(raw_payload)),0) FROM node_traffic_reports WHERE tenant_id='$TENANT_ID' AND duplicate_of IS NULL
    AND received_at>='$SNAPSHOT_AT'::timestamptz-interval '30 days' AND received_at<'$SNAPSHOT_AT'::timestamptz),
 (SELECT count(*) FROM notification_deliveries WHERE tenant_id='$TENANT_ID'),
 (SELECT count(*) FROM notification_deliveries WHERE tenant_id='$TENANT_ID' AND status='queued')")"
IFS='|' read -r report_count duplicate_count raw_entry_count notification_count queued_count <<<"$fixture_counts"
[[ "$report_count" -ge 100000 && "$duplicate_count" -ge 10000 && "$raw_entry_count" -ge 2000000 ]]
[[ "$notification_count" -ge 1000000 && "$queued_count" -ge 10000 ]]
echo "marker=dashboard_performance_fixture_ok reports=$report_count duplicates=$duplicate_count raw_entries=$raw_entry_count notifications=$notification_count queued=$queued_count"

docker create --name "$GO_CONTAINER" --network "$NETWORK" \
  --label "$LABEL_KEY=$RUN_ID" --cpus "$GO_CPUS" --memory "$GO_MEMORY_BYTES" \
  --memory-swap "$GO_MEMORY_BYTES" --pids-limit 512 \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" \
  --mount "type=bind,src=$ARTIFACT_DIR,dst=/evidence" \
  --mount "type=volume,src=$MOD_VOLUME,dst=/go/pkg/mod" -w /work "$GO_IMAGE" \
  sh -ec 'sleep infinity' >/dev/null
[[ "$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$GO_CONTAINER")" == "$RUN_ID" ]] || block_runner go_container_identity 'Go run label mismatch'
[[ "$(docker inspect -f '{{.HostConfig.NanoCpus}}' "$GO_CONTAINER")" == '2000000000' ]] || block_runner go_container_limits 'Docker did not honor the Go CPU limit'
[[ "$(docker inspect -f '{{.HostConfig.Memory}}' "$GO_CONTAINER")" == "$GO_MEMORY_BYTES" ]] || block_runner go_container_limits 'Docker did not honor the Go memory limit'
[[ "$(docker inspect -f '{{.HostConfig.MemorySwap}}' "$GO_CONTAINER")" == "$GO_MEMORY_BYTES" ]] || block_runner go_container_limits 'Docker did not disable Go swap'
[[ "$(docker inspect -f '{{.HostConfig.PidsLimit}}' "$GO_CONTAINER")" == '512' ]] || block_runner go_container_limits 'Docker did not honor the Go PID limit'
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$GO_CONTAINER")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$GO_CONTAINER")" == 'null' ]] || block_runner go_container_network 'Go probe must not publish host ports'
[[ "$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/src"}}{{.RW}}{{end}}{{end}}' "$GO_CONTAINER")" == 'false' ]] || block_runner go_source_mount 'Go source mount must be read-only'
while IFS='|' read -r type name source destination rw; do
  [[ -n "$type" ]] || continue
  [[ "$type" != volume ]] || OWNED_MOUNT_SOURCES+=("$source")
done < <(docker inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' "$GO_CONTAINER")
GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")"
docker start "$GO_CONTAINER" >/dev/null
assert_cgroup_v2_limits "$GO_CONTAINER" "$GO_MEMORY_BYTES" 2
docker exec "$GO_CONTAINER" sh -ec 'cp -a /src/. /work/'

docker exec -i "$GO_CONTAINER" sh -ec 'cat > /work/internal/domain/adminops/dashboard_performance_generated_test.go' <<'GO_TEST'
package adminops

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

func TestDashboardPerformanceGate(t *testing.T) {
	dsn, tenant, snapshot, evidence := os.Getenv("PERF_DSN"), os.Getenv("PERF_TENANT"), os.Getenv("PERF_SNAPSHOT"), os.Getenv("PERF_EVIDENCE")
	tempName := os.Getenv("PERF_TEMP_IO_FILE")
	if dsn == "" || tenant == "" || snapshot == "" || evidence == "" || tempName == "" { t.Fatal("performance environment is incomplete") }
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	pool, err := platformdb.Open(ctx, dsn)
	if err != nil { t.Fatalf("open pool: %v", err) }
	defer pool.Close()
	service := NewService(pool)
	query := DashboardTrafficQuery{Range:"30d", Limit:20, SnapshotAt:snapshot}
	tempPath := filepath.Join(evidence, tempName)
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil { t.Fatal(err) }
	fmt.Fprintln(tempFile, "phase\ttemp_files\ttemp_bytes")
	writeTemp := func(phase string) {
		t.Helper()
		var files, bytes int64
		err := pool.InTx(ctx, platformdb.Scope{TenantID:tenant}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,"SELECT temp_files,temp_bytes FROM pg_stat_database WHERE datname=current_database()").Scan(&files,&bytes)
		})
		if err != nil { t.Fatalf("temp stats %s: %v",phase,err) }
		if _,err=fmt.Fprintf(tempFile,"%s\t%d\t%d\n",phase,files,bytes); err!=nil { t.Fatal(err) }
		if err=tempFile.Sync(); err!=nil { t.Fatal(err) }
	}
	writeTemp("before_warmup")

	// Exactly one warm-up for each frozen endpoint before any measured sample.
	warmNodes, err := service.DashboardNodeTraffic(ctx, tenant, query)
	if err != nil { t.Fatalf("node warm-up: %v", err) }
	warmUsers, err := service.DashboardUserTraffic(ctx, tenant, query)
	if err != nil { t.Fatalf("user warm-up: %v", err) }
	warmBacklog, err := service.DashboardNotificationBacklog(ctx, tenant)
	if err != nil { t.Fatalf("backlog warm-up: %v", err) }
	quality:=DashboardTrafficQuality{DuplicateReportCount:10000,InvalidReportCount:10000,InvalidEntryCount:10000}
	if len(warmNodes.Items)!=20 || warmNodes.Totals.ReportedBytes!="630000000" || warmNodes.Totals.AttributedBytes!="630000000" ||
		warmNodes.Totals.UnattributedBytes!="0" || warmNodes.Ranking.ReturnedBytes!="315000000" ||
		warmNodes.Ranking.OtherNodeBytes!="315000000" || warmNodes.Quality!=quality { t.Fatalf("node fixture mismatch: %#v",warmNodes) }
	if len(warmUsers.Items)!=20 || warmUsers.Totals!=warmNodes.Totals || warmUsers.Ranking.ReturnedBytes!="630000000" ||
		warmUsers.Ranking.OtherUserBytes!="0" || warmUsers.Quality!=quality || warmUsers.SnapshotAt!=warmNodes.SnapshotAt {
		t.Fatalf("user fixture mismatch: %#v",warmUsers)
	}
	wantBacklog:=DashboardNotificationCounts{Ready:10000,ReadyRetry:7500,Scheduled:10000,ScheduledRetry:7500,
		FailedTotal:200000,SuppressedTotal:150000,BouncedTotal:150000}
	if warmBacklog.Counts!=wantBacklog || warmBacklog.ProcessorState!="unobservable" { t.Fatalf("backlog fixture mismatch: %#v",warmBacklog) }
	writeTemp("after_warmup")

	samplesPath := filepath.Join(evidence, os.Getenv("PERF_SAMPLES_FILE"))
	samples, err := os.OpenFile(samplesPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil { t.Fatal(err) }
	w := bufio.NewWriter(samples)
	fmt.Fprintln(w, "endpoint\titeration\twall_ns\twall_ms")
	measure := func(endpoint string, iteration int, call func() error) {
		t.Helper()
		started := time.Now()
		if err := call(); err != nil { t.Fatalf("%s sample %d: %v", endpoint, iteration, err) }
		elapsed := time.Since(started)
		fmt.Fprintf(w, "%s\t%d\t%d\t%.3f\n", endpoint, iteration, elapsed.Nanoseconds(), float64(elapsed.Nanoseconds())/1e6)
	}
	for i:=1; i<=20; i++ { measure("nodes",i,func() error { _,e:=service.DashboardNodeTraffic(ctx,tenant,query); return e }) }
	for i:=1; i<=20; i++ { measure("users",i,func() error { _,e:=service.DashboardUserTraffic(ctx,tenant,query); return e }) }
	for i:=1; i<=20; i++ { measure("backlog",i,func() error { _,e:=service.DashboardNotificationBacklog(ctx,tenant); return e }) }
	if err:=w.Flush(); err!=nil { t.Fatal(err) }
	if err:=samples.Sync(); err!=nil { t.Fatal(err) }
	if err:=samples.Close(); err!=nil { t.Fatal(err) }
	writeTemp("after_samples")

	snapshotTime, err := time.Parse(time.RFC3339Nano,snapshot)
	if err != nil { t.Fatal(err) }
	from := snapshotTime.Add(-30*24*time.Hour)
	writeExplain := func(filename, queryText string, args ...any) {
		t.Helper()
		path:=filepath.Join(evidence,filename)
		file,e:=os.OpenFile(path,os.O_CREATE|os.O_WRONLY|os.O_TRUNC,0600); if e!=nil { t.Fatal(e) }
		defer file.Close()
		e=pool.InTx(ctx,platformdb.Scope{TenantID:tenant},func(tx pgx.Tx) error {
			rows,e:=tx.Query(ctx,"EXPLAIN (ANALYZE, BUFFERS, SETTINGS, WAL, SUMMARY, FORMAT TEXT) "+queryText,args...)
			if e!=nil { return e }; defer rows.Close()
			for rows.Next() { var line string; if e=rows.Scan(&line); e!=nil{return e}; if _,e=fmt.Fprintln(file,line); e!=nil{return e} }
			return rows.Err()
		})
		if e!=nil { t.Fatalf("explain %s: %v",filename,e) }
		if e=file.Sync(); e!=nil { t.Fatal(e) }
	}
	writeExplain(os.Getenv("PERF_EXPLAIN_NODES_FILE"),dashboardNodeTrafficSQL,tenant,from,snapshotTime,20)
	writeExplain(os.Getenv("PERF_EXPLAIN_USERS_FILE"),dashboardUserTrafficSQL,tenant,from,snapshotTime,20)
	writeExplain(os.Getenv("PERF_EXPLAIN_BACKLOG_FILE"),dashboardNotificationBacklogSQL,tenant)
	writeTemp("after_explains")
	if err=tempFile.Close(); err!=nil { t.Fatal(err) }
	t.Log("marker=dashboard_performance_go_samples_and_explains_ok")
}
GO_TEST

GO_VERSION="$(docker exec "$GO_CONTAINER" sh -ec 'go env GOVERSION')" || block_runner go_version 'cannot query the Go version'
case "$GO_VERSION" in go1.26.*) ;; *) block_runner go_version "Go 1.26 is required, got $GO_VERSION";; esac
docker exec -d "$PG_CONTAINER" sh -ec '
  rm -f /tmp/pandora-dashboard-rss.stop /tmp/pandora-dashboard-rss.done /tmp/pandora-dashboard-rss.pid
  printf "%s\n" "$$" >/tmp/pandora-dashboard-rss.pid
  printf "unix_seconds\trss_kib\n" >/tmp/pandora-dashboard-rss.tsv
  while [ ! -e /tmp/pandora-dashboard-rss.stop ]; do
    now="$(date -u +%s)"
    rss="$(awk "/^VmRSS:/{sum+=\$2} END{print sum+0}" /proc/[0-9]*/status 2>/dev/null)"
    printf "%s\t%s\n" "$now" "$rss" >>/tmp/pandora-dashboard-rss.tsv
    sleep 1
  done
  : >/tmp/pandora-dashboard-rss.done
'
RSS_SAMPLER_READY=0
for _ in $(seq 1 20); do
  if docker exec "$PG_CONTAINER" sh -ec '
    pid="$(cat /tmp/pandora-dashboard-rss.pid)"
    case "$pid" in ""|*[!0-9]*) exit 1;; esac
    kill -0 "$pid"
    awk "END{exit !(NR>=2)}" /tmp/pandora-dashboard-rss.tsv
  '; then
    RSS_SAMPLER_READY=1
    break
  fi
  sleep 1
done
[[ "$RSS_SAMPLER_READY" -eq 1 ]] || block_runner rss_sampler 'PostgreSQL RSS sampler did not become ready'
echo 'marker=dashboard_performance_rss_sampler_ready_ok'
docker exec \
  -e PERF_DSN="$APP_DSN" -e PERF_TENANT="$TENANT_ID" -e PERF_SNAPSHOT="$SNAPSHOT_AT" \
  -e PERF_EVIDENCE=/evidence \
  -e PERF_SAMPLES_FILE="$(basename "$SAMPLES_DEST")" \
  -e PERF_EXPLAIN_NODES_FILE="$(basename "$EXPLAIN_NODES_DEST")" \
  -e PERF_EXPLAIN_USERS_FILE="$(basename "$EXPLAIN_USERS_DEST")" \
  -e PERF_EXPLAIN_BACKLOG_FILE="$(basename "$EXPLAIN_BACKLOG_DEST")" \
  -e PERF_TEMP_IO_FILE="$(basename "$TEMP_IO_DEST")" \
  "$GO_CONTAINER" sh -ec \
  'go test -v -count=1 -timeout=30m -run "^TestDashboardPerformanceGate$" ./internal/domain/adminops'
grep -Fq 'marker=dashboard_performance_go_samples_and_explains_ok' "$LOG"
docker exec "$PG_CONTAINER" sh -ec ': >/tmp/pandora-dashboard-rss.stop'
for _ in $(seq 1 20); do
  docker exec "$PG_CONTAINER" test -e /tmp/pandora-dashboard-rss.done && break
  sleep 1
done
docker exec "$PG_CONTAINER" test -e /tmp/pandora-dashboard-rss.done
docker cp "$PG_CONTAINER:/tmp/pandora-dashboard-rss.tsv" "$RSS_DEST" >/dev/null
chmod 0600 "$RSS_DEST"
RSS_SAMPLE_COUNT="$(awk 'NR>1 && $1~/^[0-9]+$/ && $2~/^[0-9]+$/{n++} END{print n+0}' "$RSS_DEST")"
RSS_MAX_KIB="$(awk 'NR>1 && $2~/^[0-9]+$/ && $2>max{max=$2} END{print max+0}' "$RSS_DEST")"
[[ "$RSS_SAMPLE_COUNT" -ge 2 && "$RSS_MAX_KIB" -gt 0 ]]
[[ "$(awk 'NR>1{printf "%s%s",sep,$1;sep=","}' "$TEMP_IO_DEST")" == 'before_warmup,after_warmup,after_samples,after_explains' ]]
read -r TEMP_FILES_DELTA TEMP_BYTES_DELTA < <(awk 'NR==2{first_files=$2;first_bytes=$3} NR>1{last_files=$2;last_bytes=$3} END{print last_files-first_files,last_bytes-first_bytes}' "$TEMP_IO_DEST")
[[ "$TEMP_FILES_DELTA" =~ ^[0-9]+$ && "$TEMP_BYTES_DELTA" =~ ^[0-9]+$ ]]

assert_no_cgroup_oom() {
  local container="$1" events oom oom_kill
  events="$(docker exec "$container" sh -ec 'test -r /sys/fs/cgroup/memory.events; cat /sys/fs/cgroup/memory.events')"
  oom="$(printf '%s\n' "$events" | awk '$1=="oom"{print $2}')"
  oom_kill="$(printf '%s\n' "$events" | awk '$1=="oom_kill"{print $2}')"
  [[ "$oom" =~ ^[0-9]+$ && "$oom" -eq 0 ]]
  [[ "$oom_kill" =~ ^[0-9]+$ && "$oom_kill" -eq 0 ]]
}
assert_no_cgroup_oom "$PG_CONTAINER"
assert_no_cgroup_oom "$GO_CONTAINER"
[[ "$(docker inspect -f '{{.State.OOMKilled}}' "$PG_CONTAINER")" == 'false' ]]
[[ "$(docker inspect -f '{{.State.OOMKilled}}' "$GO_CONTAINER")" == 'false' ]]
PG_MEMORY_PEAK_BYTES="$(docker exec "$PG_CONTAINER" sh -ec 'cat /sys/fs/cgroup/memory.peak')"
GO_MEMORY_PEAK_BYTES="$(docker exec "$GO_CONTAINER" sh -ec 'cat /sys/fs/cgroup/memory.peak')"
[[ "$PG_MEMORY_PEAK_BYTES" =~ ^[0-9]+$ && "$PG_MEMORY_PEAK_BYTES" -gt 0 && "$PG_MEMORY_PEAK_BYTES" -le "$PG_MEMORY_BYTES" ]]
[[ "$GO_MEMORY_PEAK_BYTES" =~ ^[0-9]+$ && "$GO_MEMORY_PEAK_BYTES" -gt 0 && "$GO_MEMORY_PEAK_BYTES" -le "$GO_MEMORY_BYTES" ]]
echo 'marker=dashboard_performance_cgroup_oom_zero_ok'

summarize_endpoint() {
  local endpoint="$1" p95_limit="$2" single_limit="$3" result_var="$4"
  local sorted count p95 max result status='pass'
  sorted="$(awk -F '\t' -v e="$endpoint" '$1==e{print $3}' "$SAMPLES_DEST" | sort -n)"
  count="$(awk 'NF{n++} END{print n+0}' <<<"$sorted")"
  if [[ "$count" -ne 20 ]]; then
    echo "$endpoint sample count is $count, expected 20" >&2
    return 1
  fi
  p95="$(sed -n '19p' <<<"$sorted")"
  max="$(sed -n '20p' <<<"$sorted")"
  if [[ ! "$p95" =~ ^[0-9]+$ || ! "$max" =~ ^[0-9]+$ ]]; then
    echo "$endpoint samples are not integer nanoseconds" >&2
    return 1
  fi
  if [[ "$p95" -gt "$p95_limit" || "$max" -gt "$single_limit" ]]; then
    status='fail'
    PERF_BUDGET_FAILED=1
  fi
  printf '%s_count=%s %s_p95_ns=%s %s_max_ns=%s %s_budget=%s\n' \
    "$endpoint" "$count" "$endpoint" "$p95" "$endpoint" "$max" "$endpoint" "$status" >>"$METADATA"
  result="${endpoint}:${p95}/${max}/${status}"
  printf -v "$result_var" '%s' "$result"
}

nodes_summary=''
users_summary=''
backlog_summary=''
summarize_endpoint nodes "$TRAFFIC_P95_LIMIT_NS" "$TRAFFIC_SINGLE_LIMIT_NS" nodes_summary
summarize_endpoint users "$TRAFFIC_P95_LIMIT_NS" "$TRAFFIC_SINGLE_LIMIT_NS" users_summary
summarize_endpoint backlog "$BACKLOG_P95_LIMIT_NS" "$BACKLOG_SINGLE_LIMIT_NS" backlog_summary
SAMPLE_SUMMARY="${nodes_summary},${users_summary},${backlog_summary}"

{
  printf 'host_uname='; uname -a
  printf 'docker_server_cpus=%s\n' "$DOCKER_HOST_CPUS"
  printf 'docker_server_memory_bytes=%s\n' "$DOCKER_HOST_MEMORY"
  printf 'docker_server_os=%s\n' "$DOCKER_SERVER_OS"
  printf 'docker_server_arch=%s\n' "$DOCKER_SERVER_ARCH"
  printf 'postgres_container_uname='; docker exec "$PG_CONTAINER" uname -a
  printf 'postgres_container_nproc='; docker exec "$PG_CONTAINER" nproc
  printf 'postgres_cgroup_cpu_max='; docker exec "$PG_CONTAINER" sh -ec 'cat /sys/fs/cgroup/cpu.max 2>/dev/null || true'
  printf 'postgres_cgroup_memory_max='; docker exec "$PG_CONTAINER" sh -ec 'cat /sys/fs/cgroup/memory.max 2>/dev/null || true'
  printf 'postgres_cgroup_memory_peak='; docker exec "$PG_CONTAINER" sh -ec 'cat /sys/fs/cgroup/memory.peak 2>/dev/null || cat /sys/fs/cgroup/memory/memory.max_usage_in_bytes 2>/dev/null || true'
  printf 'postgres_cgroup_memory_events='; docker exec "$PG_CONTAINER" sh -ec 'tr "\n" "," </sys/fs/cgroup/memory.events 2>/dev/null || true'; printf '\n'
  printf 'go_cgroup_cpu_max='; docker exec "$GO_CONTAINER" sh -ec 'cat /sys/fs/cgroup/cpu.max 2>/dev/null || true'
  printf 'go_cgroup_memory_max='; docker exec "$GO_CONTAINER" sh -ec 'cat /sys/fs/cgroup/memory.max 2>/dev/null || true'
  printf 'go_cgroup_memory_peak='; docker exec "$GO_CONTAINER" sh -ec 'cat /sys/fs/cgroup/memory.peak 2>/dev/null || cat /sys/fs/cgroup/memory/memory.max_usage_in_bytes 2>/dev/null || true'
  printf 'go_cgroup_memory_events='; docker exec "$GO_CONTAINER" sh -ec 'tr "\n" "," </sys/fs/cgroup/memory.events 2>/dev/null || true'; printf '\n'
  printf 'postgres_settings='; psql_admin -Atqc "SELECT string_agg(name||'='||setting||coalesce(unit,''),',' ORDER BY name) FROM pg_settings WHERE name IN ('shared_buffers','work_mem','maintenance_work_mem','effective_cache_size','max_parallel_workers_per_gather','jit','random_page_cost','effective_io_concurrency')"
  printf 'postgres_temp_io='; psql_admin -Atqc "SELECT temp_files||','||temp_bytes FROM pg_stat_database WHERE datname=current_database()"
  printf 'fixture_counts=reports:%s,duplicates:%s,raw_entries:%s,notifications:%s,queued:%s\n' \
    "$report_count" "$duplicate_count" "$raw_entry_count" "$notification_count" "$queued_count"
  printf 'report_entry_distribution='; psql_admin -Atqc "SELECT string_agg(entry_count||':'||reports,',' ORDER BY entry_count) FROM (SELECT jsonb_object_length(raw_payload) entry_count,count(*) reports FROM node_traffic_reports WHERE tenant_id='$TENANT_ID' AND duplicate_of IS NULL GROUP BY 1) s"
  printf 'notification_status_distribution='; psql_admin -Atqc "SELECT string_agg(status||':'||n,',' ORDER BY status) FROM (SELECT status,count(*) n FROM notification_deliveries WHERE tenant_id='$TENANT_ID' GROUP BY status) s"
  printf 'relation_sizes='; psql_admin -Atqc "SELECT string_agg(rel||':'||bytes,',' ORDER BY rel) FROM (SELECT 'node_traffic_reports' rel,pg_total_relation_size('node_traffic_reports') bytes UNION ALL SELECT 'notification_deliveries',pg_total_relation_size('notification_deliveries')) s"
} >>"$METADATA"

for artifact in "$SAMPLES_DEST" "$EXPLAIN_NODES_DEST" "$EXPLAIN_USERS_DEST" "$EXPLAIN_BACKLOG_DEST" "$RSS_DEST" "$TEMP_IO_DEST"; do
  [[ -s "$artifact" ]]
  [[ "$(stat -c '%a' "$artifact")" == '600' ]]
done
if [[ "$PERF_BUDGET_FAILED" -ne 0 ]]; then
  echo "marker=dashboard_performance_budget_failed $SAMPLE_SUMMARY" >&2
  exit 1
fi
echo "marker=dashboard_performance_budget_ok $SAMPLE_SUMMARY"
write_source_inventory "$SOURCE_AFTER"
cmp -s "$SOURCE_BEFORE" "$SOURCE_AFTER"
SOURCE_IMMUTABLE="ok"
echo 'marker=dashboard_performance_source_immutable_ok'
echo 'dashboard_performance_pg18_suite=ok'
