#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:-golang:1.26}"
POSTGRES_CPUS="${PANDORA_TEST_POSTGRES_CPUS:-0.75}"
POSTGRES_MEMORY="${PANDORA_TEST_POSTGRES_MEMORY:-1024m}"
GO_CPUS="${PANDORA_TEST_GO_CPUS:-1.00}"
GO_MEMORY="${PANDORA_TEST_GO_MEMORY:-1536m}"
GOOSE_BIN="${PANDORA_TEST_GOOSE_BIN:-}"
GOOSE_VERSION_EXPECTED="${PANDORA_TEST_GOOSE_VERSION:-v3.27.3}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
RUN_SAFE="$(printf '%s' "$RUN_ID" | tr '[:upper:]-' '[:lower:]_')"
PG_CONTAINER="pandora-dashboard41-pg-${RUN_ID}"
GO_CONTAINER="pandora-dashboard41-go-${RUN_ID}"
NETWORK="pandora-dashboard41-net-${RUN_ID}"
PG_VOLUME="pandora-dashboard41-pgdata-${RUN_ID}"
MOD_VOLUME="pandora-dashboard41-mod-${RUN_ID}"
LABEL_KEY="pandora.dashboard-read-models-00041.run"
DB_NAME="dashboard41_${RUN_SAFE}"
POSTGRES_PASSWORD="dashboard41-${RUN_ID}-postgres-test-only"
APP_PASSWORD="dashboard41-${RUN_ID}-app-test-only"
APP_DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"

ARTIFACT_DIR="${PANDORA_TEST_ARTIFACT_DIR:-}"
if [[ -z "$ARTIFACT_DIR" ]]; then
  ARTIFACT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-dashboard41-evidence.XXXXXX")"
else
  install -d -m 0700 "$ARTIFACT_DIR"
fi
AUDIT_LOG_DEST="${PANDORA_TEST_AUDIT_LOG:-$ARTIFACT_DIR/dashboard-read-models-00041-${RUN_ID}.log}"
MANIFEST_DEST="${PANDORA_TEST_MANIFEST:-$ARTIFACT_DIR/dashboard-read-models-00041-${RUN_ID}.manifest}"
[[ "$AUDIT_LOG_DEST" != "$MANIFEST_DEST" ]]

LOG="$(mktemp "${TMPDIR:-/tmp}/pandora-dashboard41-log.XXXXXX")"
SOURCE_INVENTORY="$(mktemp "${TMPDIR:-/tmp}/pandora-dashboard41-sources.XXXXXX")"
SOURCE_INVENTORY_AFTER="$(mktemp "${TMPDIR:-/tmp}/pandora-dashboard41-sources-after.XXXXXX")"
VOLUMES_BEFORE="$(mktemp "${TMPDIR:-/tmp}/pandora-dashboard41-volumes.XXXXXX")"
MANIFEST_TMP="$(mktemp "${TMPDIR:-/tmp}/pandora-dashboard41-manifest.XXXXXX")"
chmod 0600 "$LOG" "$SOURCE_INVENTORY" "$SOURCE_INVENTORY_AFTER" \
  "$VOLUMES_BEFORE" "$MANIFEST_TMP"

FIRST_ERROR_LINE=""
BLOCKED_INTERFACE=""
BASELINE_QUERY_FAILED=0
DOCKER_QUERIES_READY=0
RESOURCES_CREATED=0
OWNED_MOUNT_SOURCES=()
PG_VERSION_NUM="unknown"
GO_VERSION="unknown"
GOOSE_VERSION="unknown"
GOOSE_BIN_SHA256="unknown"
POSTGRES_IMAGE_ID="unknown"
GO_IMAGE_ID="unknown"
SOURCE_INVENTORY_SHA256="unknown"

required_markers=(
  dashboard41_goose_validate_ok
  dashboard41_postgres_version_ok
  dashboard41_permission_backfill_ok
  dashboard41_clean_down_ok
  dashboard41_reapply_ok
  dashboard41_fixture_ok
  dashboard41_arbitrary_json_quality_ok
  dashboard41_traffic_equations_ok
  dashboard41_empty_window_ok
  dashboard41_tenant_mask_decimal_snapshot_ok
  dashboard41_backlog_matrix_ok
  dashboard41_backlog_boundary_ok
  dashboard41_pg18_suite_ok
)

record_error() {
  local rc=$?
  if [[ -z "$FIRST_ERROR_LINE" ]]; then
    FIRST_ERROR_LINE="line=$1 rc=$rc command=$2"
    printf '%s\n' "$FIRST_ERROR_LINE" >>"$LOG"
  fi
  return "$rc"
}
trap 'record_error "$LINENO" "$BASH_COMMAND"' ERR

write_source_inventory() {
  local destination="$1" migration count=0 file
  local -a files
  files=(
    "$ROOT/deploy/test-dashboard-read-models-pg18.sh"
    "$ROOT/deploy/configure-app-role.sql"
    "$ROOT/go.mod"
    "$ROOT/go.sum"
    "$ROOT/internal/domain/adminops/dashboard.go"
    "$ROOT/internal/domain/adminops/service.go"
    "$ROOT/internal/platform/db/db.go"
    "$ROOT/migrations/00041_dashboard_read_models.sql"
  )
  for migration in "$ROOT"/migrations/000{01..41}_*.sql; do
    [[ -f "$migration" ]]
    files+=("$migration")
    count=$((count + 1))
  done
  [[ "$count" -eq 41 ]]
  printf '%s\n' "${files[@]}" | LC_ALL=C sort -u | while IFS= read -r file; do
    [[ -f "$file" ]]
    printf '%s  %s\n' "$(sha256sum "$file" | awk '{print $1}')" "${file#"$ROOT"/}"
  done >"$destination"
}

cleanup() {
  local business_rc="$1" cleanup_rc=0 final_rc query_failed="$BASELINE_QUERY_FAILED"
  local containers='' networks='' volumes='' labels_c='' labels_n='' labels_v=''
  local exact_c=0 exact_n=0 exact_v=0 label_remaining=0 mount_remaining=0 anonymous_new=-1
  local source new_all='' audit_sha='unavailable' manifest_sha='unavailable' secret_scan='ok'
  local source_block=''
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

  if [[ "$query_failed" -ne 0 || "$exact_c" -ne 0 || "$exact_n" -ne 0 ||
        "$exact_v" -ne 0 || "$label_remaining" -ne 0 || "$mount_remaining" -ne 0 ||
        "$anonymous_new" -ne 0 ]]; then
    cleanup_rc=1
  fi
  if grep -Fq "$POSTGRES_PASSWORD" "$LOG" || grep -Fq "$APP_PASSWORD" "$LOG"; then
    secret_scan='failed'
    cleanup_rc=1
  fi

  source_block="$(cat "$SOURCE_INVENTORY")" || cleanup_rc=1
  install -m 0600 "$LOG" "$AUDIT_LOG_DEST" || cleanup_rc=1
  [[ ! -f "$AUDIT_LOG_DEST" ]] || audit_sha="$(sha256sum "$AUDIT_LOG_DEST" | awk '{print $1}')"
  rm -f "$LOG" "$LOG.all.after" "$SOURCE_INVENTORY" "$SOURCE_INVENTORY_AFTER" \
    "$VOLUMES_BEFORE" || cleanup_rc=1
  for source in "$LOG" "$LOG.all.after" "$SOURCE_INVENTORY" "$SOURCE_INVENTORY_AFTER" \
    "$VOLUMES_BEFORE"; do
    [[ ! -e "$source" ]] || cleanup_rc=1
  done

  if [[ "$business_rc" -eq 0 && "$cleanup_rc" -eq 0 ]]; then
    final_rc=0
  elif [[ "$business_rc" -ne 0 ]]; then
    final_rc="$business_rc"
  else
    final_rc=1
  fi

  {
    printf 'schema=pandora-dashboard-read-models-pg18-manifest-v1\n'
    printf 'run_id=%s\ncreated_at_utc=%s\n' "$RUN_ID" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'database_name=%s\n' "$DB_NAME"
    printf 'postgres_image=%s\npostgres_image_id=%s\npostgres_version_num=%s\n' \
      "$POSTGRES_IMAGE" "$POSTGRES_IMAGE_ID" "$PG_VERSION_NUM"
    printf 'go_image=%s\ngo_image_id=%s\ngo_version=%s\n' "$GO_IMAGE" "$GO_IMAGE_ID" "$GO_VERSION"
    printf 'goose_version=%s\ngoose_bin_sha256=%s\n' "$GOOSE_VERSION" "$GOOSE_BIN_SHA256"
    printf 'source_inventory_sha256=%s\naudit_log_sha256=%s\n' "$SOURCE_INVENTORY_SHA256" "$audit_sha"
    printf 'business_exit=%s\ncleanup_exit=%s\nfinal_exit=%s\n' "$business_rc" "$cleanup_rc" "$final_rc"
    printf 'cleanup_query_failed=%s\ncleanup_exact_containers=%s\ncleanup_exact_network=%s\n' \
      "$query_failed" "$exact_c" "$exact_n"
    printf 'cleanup_exact_volumes=%s\ncleanup_label_residual=%s\n' "$exact_v" "$label_remaining"
    printf 'cleanup_mount_source_residual=%s\ncleanup_anonymous_volume_new=%s\n' \
      "$mount_remaining" "$anonymous_new"
    printf 'secret_scan=%s\n' "$secret_scan"
    [[ -z "$BLOCKED_INTERFACE" ]] || printf 'blocked_interface=%s\n' "$BLOCKED_INTERFACE"
    [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error=%s\n' "$FIRST_ERROR_LINE"
    if [[ "$cleanup_rc" -eq 0 ]]; then
      printf 'cleanup_marker=dashboard41_cleanup_ok\n'
    else
      printf 'cleanup_marker=dashboard41_cleanup_failed\n'
    fi
    printf '%s\n%s\n' '--- source_sha256 ---' "$source_block"
  } >"$MANIFEST_TMP" || cleanup_rc=1
  install -m 0600 "$MANIFEST_TMP" "$MANIFEST_DEST" || cleanup_rc=1
  rm -f "$MANIFEST_TMP" || cleanup_rc=1
  [[ ! -e "$MANIFEST_TMP" ]] || cleanup_rc=1
  if [[ "$cleanup_rc" -ne 0 ]]; then
    [[ "$final_rc" -ne 0 ]] || final_rc=1
    sed -i 's/^cleanup_exit=0$/cleanup_exit=1/; s/^final_exit=0$/final_exit=1/; s/^cleanup_marker=dashboard41_cleanup_ok$/cleanup_marker=dashboard41_cleanup_failed/' \
      "$MANIFEST_DEST" 2>/dev/null || true
  fi
  [[ ! -f "$MANIFEST_DEST" ]] || manifest_sha="$(sha256sum "$MANIFEST_DEST" | awk '{print $1}')"

  printf 'business_exit=%s cleanup_exit=%s final_exit=%s\n' "$business_rc" "$cleanup_rc" "$final_rc" >&3
  printf 'cleanup_query_failed=%s exact_containers=%s exact_network=%s exact_volumes=%s exact_label_residual=%s mount_source_residual=%s anonymous_volume_new=%s\n' \
    "$query_failed" "$exact_c" "$exact_n" "$exact_v" "$label_remaining" \
    "$mount_remaining" "$anonymous_new" >&3
  printf 'secret_scan=%s\n' "$secret_scan" >&3
  if [[ "$cleanup_rc" -eq 0 ]]; then
    echo 'cleanup_marker=dashboard41_cleanup_ok' >&3
  else
    echo 'cleanup_marker=dashboard41_cleanup_failed' >&4
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

for command in awk cmp comm find grep install sed seq sha256sum sort tr; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 1; }
done
if [[ -z "$GOOSE_BIN" || ! -x "$GOOSE_BIN" ]]; then
  BLOCKED_INTERFACE='PANDORA_TEST_GOOSE_BIN'
  echo 'BLOCKED: a pinned Goose executable is required for migration validation' >&2
  exit 78
fi
GOOSE_VERSION="$("$GOOSE_BIN" -version | awk '{print $NF}')"
[[ "$GOOSE_VERSION" == "$GOOSE_VERSION_EXPECTED" ]]
GOOSE_BIN_SHA256="$(sha256sum "$GOOSE_BIN" | awk '{print $1}')"
"$GOOSE_BIN" -dir "$ROOT/migrations" validate
echo 'marker=dashboard41_goose_validate_ok'

write_source_inventory "$SOURCE_INVENTORY"
SOURCE_INVENTORY_SHA256="$(sha256sum "$SOURCE_INVENTORY" | awk '{print $1}')"
command -v docker >/dev/null 2>&1 || { BLOCKED_INTERFACE='docker'; echo 'docker is required' >&2; exit 78; }
docker info >/dev/null
docker volume ls --format '{{.Name}}' 2>/dev/null | LC_ALL=C sort >"$VOLUMES_BEFORE" || BASELINE_QUERY_FAILED=1
[[ "$BASELINE_QUERY_FAILED" -eq 0 ]]
DOCKER_QUERIES_READY=1

up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1;next} up' "$1"; }
down_sql() { awk '/^-- \+goose Down/{down=1;next} down' "$1"; }
psql_admin_db() {
  local database="$1"
  shift
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$database" "$@"
}
psql_admin() { psql_admin_db "$DB_NAME" "$@"; }
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
  --cpus "$POSTGRES_CPUS" --memory "$POSTGRES_MEMORY" --memory-swap "$POSTGRES_MEMORY" \
  --pids-limit 256 --shm-size 128m \
  --mount "type=volume,src=$PG_VOLUME,dst=/var/lib/postgresql" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" "$POSTGRES_IMAGE" >/dev/null

[[ "$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$PG_CONTAINER")" == "$RUN_ID" ]]
[[ "$(docker network inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$NETWORK")" == "$RUN_ID" ]]
[[ "$(docker volume inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$PG_VOLUME")" == "$RUN_ID" ]]
[[ "$(docker volume inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$MOD_VOLUME")" == "$RUN_ID" ]]
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER")" == 'null' ]]
while IFS='|' read -r type name source destination rw; do
  [[ -n "$type" ]] || continue
  [[ "$type" != volume ]] || OWNED_MOUNT_SOURCES+=("$source")
done < <(docker inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' "$PG_CONTAINER")
POSTGRES_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")"

docker start "$PG_CONTAINER" >/dev/null
POSTGRES_READY=0
for _ in $(seq 1 90); do
  # The official image briefly starts a temporary postmaster during initdb.
  # Requiring PID 1 to be the final postgres process prevents a false-ready
  # result during the temporary-server shutdown/restart window.
  if [[ "$(docker exec "$PG_CONTAINER" sh -c 'cat /proc/1/comm' 2>/dev/null || true)" == postgres ]] &&
     docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null 2>&1; then
    POSTGRES_READY=1
    break
  fi
  sleep 1
done
[[ "$POSTGRES_READY" -eq 1 ]]
docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null
PG_VERSION_NUM="$(psql_admin -Atqc 'SHOW server_version_num')"
[[ "$PG_VERSION_NUM" =~ ^[0-9]+$ && "$PG_VERSION_NUM" -ge 180000 && "$PG_VERSION_NUM" -lt 190000 ]]
echo "marker=dashboard41_postgres_version_ok version_num=$PG_VERSION_NUM"

count=0
for migration in "$ROOT"/migrations/000{01..09}_*.sql; do
  [[ -f "$migration" ]]
  { echo 'BEGIN;'; up_sql "$migration"; echo 'COMMIT;'; } | psql_admin >/dev/null
  count=$((count + 1))
done
[[ "$count" -eq 9 ]]

# These tenants predate schema41. Their built-in roles are provisioned below
# immediately before schema41, reproducing the existing-tenant upgrade surface.
psql_admin >/dev/null <<'SQL'
INSERT INTO tenants(id,slug,display_name,timezone,default_currency) VALUES
 ('41000000-0000-7000-8000-000000000001','dash-a','Dashboard A','UTC','USD'),
 ('42000000-0000-7000-8000-000000000001','dash-b','Dashboard B','UTC','USD'),
 ('43000000-0000-7000-8000-000000000001','dash-empty','Dashboard Empty','UTC','USD'),
 ('44000000-0000-7000-8000-000000000001','dash-boundary-clear','Dashboard Boundary Clear','UTC','USD'),
 ('45000000-0000-7000-8000-000000000001','dash-boundary-backlogged','Dashboard Boundary Backlogged','UTC','USD'),
 ('46000000-0000-7000-8000-000000000001','dash-history','Dashboard History','UTC','USD');
SQL

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
# Model six already-provisioned tenants with the two system roles targeted by
# schema41. The default tenant's roles remain present as an additional control.
psql_admin >/dev/null <<'SQL'
INSERT INTO roles(tenant_id,code,name,is_system)
SELECT t.id,r.code,r.name,true
FROM tenants t
CROSS JOIN (VALUES('tenant_admin','租户管理员'),('platform_admin','平台管理员')) r(code,name)
WHERE t.id IN (
 '41000000-0000-7000-8000-000000000001','42000000-0000-7000-8000-000000000001',
 '43000000-0000-7000-8000-000000000001','44000000-0000-7000-8000-000000000001',
 '45000000-0000-7000-8000-000000000001','46000000-0000-7000-8000-000000000001')
ON CONFLICT (tenant_id,code) DO NOTHING;
SQL
{ echo 'BEGIN;'; up_sql "$ROOT/migrations/00041_dashboard_read_models.sql"; echo 'COMMIT;'; } | psql_admin >/dev/null
configure_role

permission_surface="$(psql_admin -Atqc "SELECT
  (SELECT count(*) FROM permissions WHERE code='ops.notification.read'),
  (SELECT count(*) FROM roles WHERE is_system AND code IN ('tenant_admin','platform_admin')),
  (SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id=rp.role_id
    WHERE rp.permission_code='ops.notification.read' AND r.is_system
      AND r.code IN ('tenant_admin','platform_admin')),
  (to_regclass('idx_node_traffic_reports_dashboard_window') IS NOT NULL)::int,
  (to_regclass('idx_node_traffic_reports_dashboard_duplicate_window') IS NOT NULL)::int,
  (to_regclass('idx_notification_deliveries_ready_expr') IS NOT NULL)::int,
  (to_regclass('idx_notification_deliveries_health_status') IS NOT NULL)::int")"
IFS='|' read -r permission_count target_roles grants idx1 idx2 idx3 idx4 <<<"$permission_surface"
[[ "$permission_count" -eq 1 && "$target_roles" -eq 14 && "$grants" -eq "$target_roles" ]]
[[ "$idx1$idx2$idx3$idx4" == '1111' ]]
echo 'marker=dashboard41_permission_backfill_ok'

{ echo 'BEGIN;'; down_sql "$ROOT/migrations/00041_dashboard_read_models.sql"; echo 'COMMIT;'; } | psql_admin >/dev/null
down_surface="$(psql_admin -Atqc "SELECT
  (SELECT count(*) FROM permissions WHERE code='ops.notification.read'),
  (SELECT count(*) FROM role_permissions WHERE permission_code='ops.notification.read'),
  (to_regclass('idx_node_traffic_reports_dashboard_window') IS NULL)::int,
  (to_regclass('idx_node_traffic_reports_dashboard_duplicate_window') IS NULL)::int,
  (to_regclass('idx_notification_deliveries_ready_expr') IS NULL)::int,
  (to_regclass('idx_notification_deliveries_health_status') IS NULL)::int")"
[[ "$down_surface" == '0|0|1|1|1|1' ]]
echo 'marker=dashboard41_clean_down_ok'
{ echo 'BEGIN;'; up_sql "$ROOT/migrations/00041_dashboard_read_models.sql"; echo 'COMMIT;'; } | psql_admin >/dev/null
configure_role
reapply_surface="$(psql_admin -Atqc "SELECT
  (SELECT count(*) FROM permissions WHERE code='ops.notification.read'),
  (SELECT count(*) FROM role_permissions WHERE permission_code='ops.notification.read'),
  (to_regclass('idx_node_traffic_reports_dashboard_window') IS NOT NULL)::int,
  (to_regclass('idx_node_traffic_reports_dashboard_duplicate_window') IS NOT NULL)::int,
  (to_regclass('idx_notification_deliveries_ready_expr') IS NOT NULL)::int,
  (to_regclass('idx_notification_deliveries_health_status') IS NOT NULL)::int")"
IFS='|' read -r permission_count grants idx1 idx2 idx3 idx4 <<<"$reapply_surface"
[[ "$permission_count" -eq 1 && "$grants" -eq "$target_roles" && "$idx1$idx2$idx3$idx4" == '1111' ]]
echo 'marker=dashboard41_reapply_ok'

SNAPSHOT_AT="$(psql_admin -Atqc \
  "SELECT to_char((statement_timestamp()-interval '2 seconds') AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")"
[[ "$SNAPSHOT_AT" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z$ ]]

psql_admin -v snapshot_at="$SNAPSHOT_AT" >/dev/null <<'SQL'
INSERT INTO users(id,tenant_id,email) VALUES
 ('41000000-0000-7000-8000-000000000011','41000000-0000-7000-8000-000000000001','Alice.Secret@Example.COM'),
 ('41000000-0000-7000-8000-000000000012','41000000-0000-7000-8000-000000000001','Bob.Private@Example.COM'),
 ('41000000-0000-7000-8000-000000000013','41000000-0000-7000-8000-000000000001','low-three@example.com'),
 ('41000000-0000-7000-8000-000000000014','41000000-0000-7000-8000-000000000001','low-four@example.com'),
 ('41000000-0000-7000-8000-000000000015','41000000-0000-7000-8000-000000000001','five@example.com'),
 ('41000000-0000-7000-8000-000000000016','41000000-0000-7000-8000-000000000001','猫咪@Example.COM'),
 ('41000000-0000-7000-8000-000000000017','41000000-0000-7000-8000-000000000001','broken-address'),
 ('42000000-0000-7000-8000-000000000011','42000000-0000-7000-8000-000000000001','tenant-b@example.com');

INSERT INTO products(id,tenant_id,code,name,status) VALUES
 ('41000000-0000-7000-8000-000000000021','41000000-0000-7000-8000-000000000001','dash-a-product','Dash A Product','active'),
 ('42000000-0000-7000-8000-000000000021','42000000-0000-7000-8000-000000000001','dash-b-product','Dash B Product','active');
INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES
 ('41000000-0000-7000-8000-000000000031','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000021','dash-a-plan','Dash A Plan','draft'),
 ('42000000-0000-7000-8000-000000000031','42000000-0000-7000-8000-000000000001','42000000-0000-7000-8000-000000000021','dash-b-plan','Dash B Plan','draft');
INSERT INTO plan_versions(id,tenant_id,plan_id,version,frozen_at) VALUES
 ('41000000-0000-7000-8000-000000000041','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000031',1,NULL),
 ('42000000-0000-7000-8000-000000000041','42000000-0000-7000-8000-000000000001','42000000-0000-7000-8000-000000000031',1,NULL);

INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,node_uid)
SELECT ('41000000-0000-7000-8000-'||lpad((50+n)::text,12,'0'))::uuid,
       '41000000-0000-7000-8000-000000000001'::uuid,
       ('41000000-0000-7000-8000-'||lpad((10+n)::text,12,'0'))::uuid,
       '41000000-0000-7000-8000-000000000031'::uuid,
       '41000000-0000-7000-8000-000000000041'::uuid,
       'active','USD',0,n
  FROM generate_series(1,7) n;
INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,node_uid)
VALUES('42000000-0000-7000-8000-000000000051','42000000-0000-7000-8000-000000000001',
 '42000000-0000-7000-8000-000000000011','42000000-0000-7000-8000-000000000031',
 '42000000-0000-7000-8000-000000000041','active','USD',0,100);

INSERT INTO nodes(id,tenant_id,name,display_name) SELECT
 ('41000000-0000-7000-8000-'||lpad((100+n)::text,12,'0'))::uuid,
 '41000000-0000-7000-8000-000000000001'::uuid,'node-'||n,'Edge '||n
 FROM generate_series(1,6) n;
INSERT INTO nodes(id,tenant_id,name,display_name) VALUES
 ('42000000-0000-7000-8000-000000000101','42000000-0000-7000-8000-000000000001','tenant-b-node','Tenant B Node');

INSERT INTO node_traffic_reports(id,tenant_id,node_id,raw_payload,content_hash,received_at) VALUES
 ('41000000-0000-7000-8000-000000000201','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000101',
  '{"1":[10,20],"001":[1,2],"+1":[3,4],"999":[5,6],"-0":[7,8],"-1":[9,10]}'::jsonb,decode(md5('dash-r1'),'hex'),:'snapshot_at'::timestamptz-interval '20 minutes'),
 ('41000000-0000-7000-8000-000000000202','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000102',
  '{"2":[100,200]}'::jsonb,decode(md5('dash-r2'),'hex'),:'snapshot_at'::timestamptz-interval '19 minutes'),
 ('41000000-0000-7000-8000-000000000204','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000101',
  '[]'::jsonb,decode(md5('dash-root-array'),'hex'),:'snapshot_at'::timestamptz-interval '17 minutes'),
 ('41000000-0000-7000-8000-000000000205','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000101',
  'null'::jsonb,decode(md5('dash-root-null'),'hex'),:'snapshot_at'::timestamptz-interval '16 minutes'),
 ('41000000-0000-7000-8000-000000000206','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000101',
  '"root-string"'::jsonb,decode(md5('dash-root-string'),'hex'),:'snapshot_at'::timestamptz-interval '15 minutes'),
 ('41000000-0000-7000-8000-000000000208','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000103',
  '{"1":[20,0],"3":[1,0],"4":[2,0],"5":[3,0],"6":[4,0],"7":[5,0]}'::jsonb,decode(md5('dash-users'),'hex'),:'snapshot_at'::timestamptz-interval '13 minutes'),
 ('41000000-0000-7000-8000-000000000209','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000104',
  '{"1":[30,0]}'::jsonb,decode(md5('dash-node4'),'hex'),:'snapshot_at'::timestamptz-interval '12 minutes'),
 ('41000000-0000-7000-8000-000000000210','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000105',
  '{"1":[40,0]}'::jsonb,decode(md5('dash-node5'),'hex'),:'snapshot_at'::timestamptz-interval '11 minutes'),
 ('41000000-0000-7000-8000-000000000211','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000106',
  '{"1":[50,0]}'::jsonb,decode(md5('dash-node6'),'hex'),:'snapshot_at'::timestamptz-interval '10 minutes'),
 ('41000000-0000-7000-8000-000000000212','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000101',
  '{"1":[9223372036854775807,9223372036854775807]}'::jsonb,decode(md5('dash-big'),'hex'),:'snapshot_at'::timestamptz-interval '9 minutes'),
 ('41000000-0000-7000-8000-000000000213','41000000-0000-7000-8000-000000000001','41000000-0000-7000-8000-000000000101',
  '{"1":[0,0]}'::jsonb,decode(md5('dash-zero'),'hex'),:'snapshot_at'::timestamptz-interval '8 minutes'),
 ('42000000-0000-7000-8000-000000000201','42000000-0000-7000-8000-000000000001','42000000-0000-7000-8000-000000000101',
  '{"100":[777,0]}'::jsonb,decode(md5('dash-tenant-b'),'hex'),:'snapshot_at'::timestamptz-interval '7 minutes');

INSERT INTO node_traffic_reports(id,tenant_id,node_id,raw_payload,content_hash,duplicate_of,received_at)
VALUES('41000000-0000-7000-8000-000000000207','41000000-0000-7000-8000-000000000001',
 '41000000-0000-7000-8000-000000000101','"duplicate-malformed-ignored"'::jsonb,
 decode(md5('dash-duplicate'),'hex'),'41000000-0000-7000-8000-000000000201',
 :'snapshot_at'::timestamptz-interval '14 minutes');

INSERT INTO node_traffic_reports(id,tenant_id,node_id,raw_payload,content_hash,received_at)
SELECT '41000000-0000-7000-8000-000000000203','41000000-0000-7000-8000-000000000001',
 '41000000-0000-7000-8000-000000000101',
 jsonb_build_object('1',jsonb_build_array(5,5),'bad',jsonb_build_array(1,2),
  '9223372036854775808',jsonb_build_array(1,2),repeat('9',10000),jsonb_build_array(1,2),
  '2',jsonb_build_array(1),'3',jsonb_build_array(1,2,3),'4','null'::jsonb,
  '5',to_jsonb('string'::text),'6',jsonb_build_array(-1,2),
  '7',jsonb_build_array(9223372036854775808::numeric,0),
  '8',jsonb_build_array(1.0,2),'9',jsonb_build_array(1.5,2)),
 decode(md5('dash-mixed'),'hex'),:'snapshot_at'::timestamptz-interval '18 minutes';

INSERT INTO notification_deliveries(tenant_id,template_code,channel,dedupe_key,status,attempts,next_retry_at,sent_at,created_at) VALUES
 ('41000000-0000-7000-8000-000000000001','dash','email','a-ready','queued',0,NULL,NULL,:'snapshot_at'::timestamptz-interval '700 seconds'),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-ready-retry','queued',2,:'snapshot_at'::timestamptz-interval '601 seconds',NULL,:'snapshot_at'::timestamptz-interval '800 seconds'),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-scheduled','queued',0,:'snapshot_at'::timestamptz+interval '1 hour',NULL,:'snapshot_at'::timestamptz),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-scheduled-retry','queued',1,:'snapshot_at'::timestamptz+interval '2 hours',NULL,:'snapshot_at'::timestamptz),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-sending','sending',1,NULL,NULL,:'snapshot_at'::timestamptz-interval '1 hour'),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-failed','failed',5,NULL,NULL,:'snapshot_at'::timestamptz-interval '2 days'),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-suppressed','suppressed',0,NULL,NULL,:'snapshot_at'::timestamptz-interval '2 days'),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-bounced','bounced',1,NULL,NULL,:'snapshot_at'::timestamptz-interval '2 days'),
 ('41000000-0000-7000-8000-000000000001','dash','email','a-sent','sent',1,NULL,:'snapshot_at'::timestamptz-interval '1 minute',:'snapshot_at'::timestamptz-interval '2 minutes'),
 ('42000000-0000-7000-8000-000000000001','dash','email','b-ready','queued',0,NULL,NULL,:'snapshot_at'::timestamptz-interval '3 hours'),
 ('44000000-0000-7000-8000-000000000001','dash','email','boundary-clear','queued',0,NULL,NULL,'2026-01-01T00:00:00.000000Z'),
 ('45000000-0000-7000-8000-000000000001','dash','email','boundary-backlogged','queued',0,NULL,NULL,'2025-12-31T23:59:59.999999Z'),
 ('46000000-0000-7000-8000-000000000001','dash','email','history-failed','failed',5,NULL,NULL,'2026-01-01T00:00:00.000000Z');
SQL
echo 'marker=dashboard41_fixture_ok'

docker create --name "$GO_CONTAINER" --network "$NETWORK" \
  --label "$LABEL_KEY=$RUN_ID" --cpus "$GO_CPUS" --memory "$GO_MEMORY" --memory-swap "$GO_MEMORY" \
  --pids-limit 384 --mount "type=bind,src=$ROOT,dst=/src,readonly" \
  --mount "type=volume,src=$MOD_VOLUME,dst=/go/pkg/mod" -w /work "$GO_IMAGE" \
  sh -ec 'sleep infinity' >/dev/null
[[ "$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$GO_CONTAINER")" == "$RUN_ID" ]]
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$GO_CONTAINER")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$GO_CONTAINER")" == 'null' ]]
while IFS='|' read -r type name source destination rw; do
  [[ -n "$type" ]] || continue
  [[ "$type" != volume ]] || OWNED_MOUNT_SOURCES+=("$source")
done < <(docker inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' "$GO_CONTAINER")
GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")"
docker start "$GO_CONTAINER" >/dev/null
docker exec "$GO_CONTAINER" sh -ec 'cp -a /src/. /work/'

docker exec -i "$GO_CONTAINER" sh -ec 'cat > /work/internal/domain/adminops/dashboard_pg18_generated_test.go' <<'GO_TEST'
package adminops

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

const (
	dashTenantA = "41000000-0000-7000-8000-000000000001"
	dashTenantB = "42000000-0000-7000-8000-000000000001"
	dashTenantEmpty = "43000000-0000-7000-8000-000000000001"
	dashBoundaryClear = "44000000-0000-7000-8000-000000000001"
	dashBoundaryBacklogged = "45000000-0000-7000-8000-000000000001"
	dashHistory = "46000000-0000-7000-8000-000000000001"
)

var dashDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
var dashTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)

func TestDashboardGeneratedPG18(t *testing.T) {
	dsn := os.Getenv("AEGIS_DASHBOARD_PG18_DSN")
	snapshot := os.Getenv("AEGIS_DASHBOARD_SNAPSHOT_AT")
	if dsn == "" || snapshot == "" { t.Skip("dashboard PG18 environment is not set") }
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := platformdb.Open(ctx, dsn)
	if err != nil { t.Fatalf("open dashboard PG18 pool: %v", err) }
	defer pool.Close()
	service := NewService(pool)

	t.Run("arbitrary JSON quality and traffic equations", func(t *testing.T) {
		query := DashboardTrafficQuery{Range:"7d",Limit:5,SnapshotAt:snapshot}
		nodes, err := service.DashboardNodeTraffic(ctx,dashTenantA,query)
		if err != nil { t.Fatalf("node traffic: %v",err) }
		users, err := service.DashboardUserTraffic(ctx,dashTenantA,query)
		if err != nil { t.Fatalf("user traffic: %v",err) }
		if nodes.SnapshotAt!=snapshot || users.SnapshotAt!=snapshot || nodes.SnapshotAt!=users.SnapshotAt ||
			nodes.From!=users.From || nodes.To!=users.To { t.Fatalf("shared snapshot mismatch nodes=%#v users=%#v",nodes,users) }
		for _, value := range []string{nodes.SnapshotAt,nodes.From,nodes.To,users.SnapshotAt,users.From,users.To} {
			if !dashTimestamp.MatchString(value) { t.Fatalf("timestamp is not fixed UTC microseconds: %q",value) }
		}
		if nodes.Quality.DuplicateReportCount!=1 || nodes.Quality.InvalidReportCount!=4 || nodes.Quality.InvalidEntryCount!=10 ||
			users.Quality!=nodes.Quality { t.Fatalf("quality mismatch nodes=%+v users=%+v",nodes.Quality,users.Quality) }
		if len(nodes.Items)!=5 || len(users.Items)!=5 { t.Fatalf("top-5 lengths nodes=%d users=%d",len(nodes.Items),len(users.Items)) }
		assertTrafficBytes(t,nodes,users)
		if nodes.Totals.ReportedBytes!="18446744073709552167" || nodes.Totals.AttributedBytes!="18446744073709552119" ||
			nodes.Totals.UnattributedBytes!="48" || nodes.Ranking.OtherNodeBytes!="30" || users.Ranking.OtherUserBytes!="3" {
			t.Fatalf("known partition mismatch node=%+v/%+v user=%+v",nodes.Totals,nodes.Ranking,users.Ranking)
		}
		t.Log("marker=dashboard41_arbitrary_json_quality_ok")
		t.Log("marker=dashboard41_traffic_equations_ok")
	})

	t.Run("empty window", func(t *testing.T) {
		query:=DashboardTrafficQuery{Range:"24h",Limit:5,SnapshotAt:snapshot}
		n,err:=service.DashboardNodeTraffic(ctx,dashTenantEmpty,query); if err!=nil { t.Fatal(err) }
		u,err:=service.DashboardUserTraffic(ctx,dashTenantEmpty,query); if err!=nil { t.Fatal(err) }
		if len(n.Items)!=0 || len(u.Items)!=0 || n.Totals.ReportedBytes!="0" || u.Totals.ReportedBytes!="0" ||
			n.Quality!=(DashboardTrafficQuality{}) || u.Quality!=(DashboardTrafficQuality{}) { t.Fatalf("empty mismatch n=%#v u=%#v",n,u) }
		t.Log("marker=dashboard41_empty_window_ok")
	})

	t.Run("tenant isolation masked email decimal and common snapshot", func(t *testing.T) {
		query:=DashboardTrafficQuery{Range:"7d",Limit:5,SnapshotAt:snapshot}
		a,err:=service.DashboardUserTraffic(ctx,dashTenantA,query); if err!=nil { t.Fatal(err) }
		b,err:=service.DashboardUserTraffic(ctx,dashTenantB,query); if err!=nil { t.Fatal(err) }
		if b.Totals.ReportedBytes!="777" || b.Totals.AttributedBytes!="777" || len(b.Items)!=1 || b.Items[0].EmailMasked!="t***@example.com" {
			t.Fatalf("tenant B mismatch: %#v",b)
		}
		raw,_:=json.Marshal(a)
		text:=string(raw)
		for _, secret:=range []string{"Alice.Secret@Example.COM","Bob.Private@Example.COM","猫咪@Example.COM","broken-address"} {
			if strings.Contains(text,secret) { t.Fatalf("full email leaked: %s in %s",secret,text) }
		}
		for _, masked:=range []string{"A***@example.com","猫***@example.com","\"email_masked\":\"***\""} {
			if !strings.Contains(text,masked) { t.Fatalf("masked form missing %q in %s",masked,text) }
		}
		if a.SnapshotAt!=b.SnapshotAt || a.SnapshotAt!=snapshot { t.Fatalf("explicit common snapshot mismatch %s %s",a.SnapshotAt,b.SnapshotAt) }
		t.Log("marker=dashboard41_tenant_mask_decimal_snapshot_ok")
	})

	t.Run("backlog empty ready scheduled retry and history", func(t *testing.T) {
		empty,err:=service.DashboardNotificationBacklog(ctx,dashTenantEmpty); if err!=nil { t.Fatal(err) }
		if empty.BacklogState!="clear" || empty.ProcessorState!="unobservable" || empty.Counts!=(DashboardNotificationCounts{}) ||
			empty.OldestReadyAt!=nil || empty.LastSentAt!=nil || empty.MaxReadyLagSeconds!=0 || empty.Assessment.Reason!="no_due_backlog" {
			t.Fatalf("empty backlog mismatch: %#v",empty)
		}
		a,err:=service.DashboardNotificationBacklog(ctx,dashTenantA); if err!=nil { t.Fatal(err) }
		want:=DashboardNotificationCounts{Ready:2,ReadyRetry:1,Scheduled:2,ScheduledRetry:1,SendingUnobservable:1,FailedTotal:1,SuppressedTotal:1,BouncedTotal:1}
		if a.BacklogState!="backlogged" || a.ProcessorState!="unobservable" || a.Counts!=want || a.MaxReadyLagSeconds<=600 ||
			a.OldestReadyAt==nil || a.LastSentAt==nil || a.Assessment.Reason!="lag_exceeded" { t.Fatalf("backlog A mismatch: %#v",a) }
		history,err:=service.DashboardNotificationBacklog(ctx,dashHistory); if err!=nil { t.Fatal(err) }
		if history.BacklogState!="clear" || history.Counts.FailedTotal!=1 || history.Counts.Ready!=0 || history.Assessment.Reason!="no_due_backlog" {
			t.Fatalf("historical failure poisoned backlog: %#v",history)
		}
		t.Log("marker=dashboard41_backlog_matrix_ok")
	})

	t.Run("exact 600 microsecond boundary", func(t *testing.T) {
		asOf:=time.Date(2026,1,1,0,10,0,0,time.UTC)
		clear:=fixedBacklog(t,ctx,pool,dashBoundaryClear,asOf)
		late:=fixedBacklog(t,ctx,pool,dashBoundaryBacklogged,asOf)
		if clear.state!="clear" || clear.lag!=600 || clear.reason!="within_threshold" { t.Fatalf("600.000000 mismatch: %+v",clear) }
		if late.state!="backlogged" || late.lag!=601 || late.reason!="lag_exceeded" { t.Fatalf("600.000001 mismatch: %+v",late) }
		t.Log("marker=dashboard41_backlog_boundary_ok")
	})
	t.Log("marker=dashboard41_pg18_suite_ok")
}

func mustBig(t *testing.T,value string) *big.Int {
	t.Helper(); if !dashDecimal.MatchString(value) { t.Fatalf("non-decimal byte string %q",value) }
	n,ok:=new(big.Int).SetString(value,10); if !ok { t.Fatalf("invalid decimal %q",value) }; return n
}

func assertTrafficBytes(t *testing.T,n *DashboardNodeTraffic,u *DashboardUserTraffic) {
	t.Helper()
	for _, value:=range []string{n.Totals.ReportedBytes,n.Totals.AttributedBytes,n.Totals.UnattributedBytes,n.Ranking.ReturnedBytes,n.Ranking.OtherNodeBytes,
		u.Totals.ReportedBytes,u.Totals.AttributedBytes,u.Totals.UnattributedBytes,u.Ranking.ReturnedBytes,u.Ranking.OtherUserBytes} { mustBig(t,value) }
	if n.Totals!=u.Totals { t.Fatalf("shared totals mismatch n=%+v u=%+v",n.Totals,u.Totals) }
	if new(big.Int).Add(mustBig(t,n.Totals.AttributedBytes),mustBig(t,n.Totals.UnattributedBytes)).Cmp(mustBig(t,n.Totals.ReportedBytes))!=0 {
		t.Fatal("reported != attributed + unattributed")
	}
	if new(big.Int).Add(mustBig(t,n.Ranking.ReturnedBytes),mustBig(t,n.Ranking.OtherNodeBytes)).Cmp(mustBig(t,n.Totals.ReportedBytes))!=0 {
		t.Fatal("node returned + other != reported")
	}
	if new(big.Int).Add(mustBig(t,u.Ranking.ReturnedBytes),mustBig(t,u.Ranking.OtherUserBytes)).Cmp(mustBig(t,u.Totals.AttributedBytes))!=0 {
		t.Fatal("user returned + other != attributed")
	}
	nodeSum:=new(big.Int); for _,item:=range n.Items { nodeSum.Add(nodeSum,mustBig(t,item.TotalBytes)); mustBig(t,item.UploadBytes); mustBig(t,item.DownloadBytes) }
	userSum:=new(big.Int); for _,item:=range u.Items { userSum.Add(userSum,mustBig(t,item.TotalBytes)); mustBig(t,item.UploadBytes); mustBig(t,item.DownloadBytes) }
	if nodeSum.Cmp(mustBig(t,n.Ranking.ReturnedBytes))!=0 || userSum.Cmp(mustBig(t,u.Ranking.ReturnedBytes))!=0 { t.Fatal("item sums != returned bytes") }
	if mustBig(t,n.Totals.ReportedBytes).BitLen()<=63 { t.Fatal("fixture did not exercise aggregate beyond int64") }
}

type fixedBacklogResult struct { state string; lag int64; reason string }

func fixedBacklog(t *testing.T,ctx context.Context,pool *platformdb.Pool,tenant string,asOf time.Time) fixedBacklogResult {
	t.Helper()
	query:=strings.Replace(dashboardNotificationBacklogSQL,
		"SELECT statement_timestamp()::timestamptz(6) AS as_of",
		"SELECT $2::timestamptz(6) AS as_of",1)
	var out fixedBacklogResult
	err:=pool.InTx(ctx,platformdb.Scope{TenantID:tenant},func(tx pgx.Tx) error {
		var asOfText string; var ready,readyRetry,scheduled,scheduledRetry,sending,failed,suppressed,bounced int64
		var oldest,last *string
		return tx.QueryRow(ctx,query,tenant,asOf).Scan(&asOfText,&out.state,&ready,&readyRetry,&scheduled,&scheduledRetry,
			&sending,&failed,&suppressed,&bounced,&oldest,&out.lag,&last,&out.reason)
	})
	if err!=nil { t.Fatalf("fixed backlog query: %v",err) }
	return out
}
GO_TEST

GO_VERSION="$(docker exec "$GO_CONTAINER" sh -ec 'go env GOVERSION')"
case "$GO_VERSION" in go1.26.*) ;; *) echo "Go 1.26 is required, got $GO_VERSION" >&2; exit 1;; esac
docker exec -e AEGIS_DASHBOARD_PG18_DSN="$APP_DSN" -e AEGIS_DASHBOARD_SNAPSHOT_AT="$SNAPSHOT_AT" \
  "$GO_CONTAINER" sh -ec \
  'go test -v -count=1 -timeout=120s -run "^TestDashboardGeneratedPG18$" ./internal/domain/adminops'

for marker in "${required_markers[@]}"; do
  grep -Fq "marker=$marker" "$LOG" || { echo "missing marker=$marker" >&2; exit 1; }
done
write_source_inventory "$SOURCE_INVENTORY_AFTER"
cmp -s "$SOURCE_INVENTORY" "$SOURCE_INVENTORY_AFTER"
echo 'marker=dashboard41_source_immutable_ok'
