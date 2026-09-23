#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:?set PANDORA_TEST_POSTGRES_IMAGE to a locally present postgres@sha256 digest}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:?set PANDORA_TEST_GO_IMAGE to a locally present golang@sha256 digest}"
[[ "$POSTGRES_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || { echo 'order_release_pg18_refused_mutable_postgres_image' >&2; exit 2; }
[[ "$GO_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || { echo 'order_release_pg18_refused_mutable_go_image' >&2; exit 2; }
POSTGRES_CPUS="${PANDORA_TEST_POSTGRES_CPUS:-0.75}"
GO_CPUS="${PANDORA_TEST_GO_CPUS:-1.00}"
GOOSE_BIN="${PANDORA_TEST_GOOSE_BIN:-}"
GOOSE_VERSION_EXPECTED="${PANDORA_TEST_GOOSE_VERSION:-v3.27.3}"
RUN_ID="$(tr -d '-' </proc/sys/kernel/random/uuid)"
[[ "$RUN_ID" =~ ^[0-9a-f]{32}$ ]] || { echo 'order_release_pg18_random_identity_failed' >&2; exit 2; }
RUN_SAFE="$RUN_ID"
PG_CONTAINER="pandora-release40-pg-${RUN_ID}"
GO_CONTAINER="pandora-release40-go-${RUN_ID}"
NETWORK="pandora-release40-net-${RUN_ID}"
PG_VOLUME="pandora-release40-pgdata-${RUN_ID}"
MOD_VOLUME="pandora-release40-mod-${RUN_ID}"
LABEL_KEY="pandora.order-release-00040.run"
LABEL_TEST_KEY="pandora.test"
LABEL_TEST_VALUE="order-release-pg18"
DB_NAME="release40_${RUN_SAFE}"
POSTGRES_PASSWORD="release40-${RUN_ID}-postgres-test-only"
APP_PASSWORD="release40-${RUN_ID}-app-test-only"
APP_DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
ADMIN_DSN="postgres://postgres:${POSTGRES_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER_ID=""
GO_CONTAINER_ID=""
NETWORK_ID=""
PG_VOLUME_ID=""
MOD_VOLUME_ID=""
BUSINESS_OK=0

ARTIFACT_DIR="${PANDORA_TEST_ARTIFACT_DIR:-}"
if [[ -z "$ARTIFACT_DIR" ]]; then
  ARTIFACT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-release40-evidence.XXXXXX")"
else
  install -d -m 0700 "$ARTIFACT_DIR"
fi
AUDIT_LOG_DEST="${PANDORA_TEST_AUDIT_LOG:-$ARTIFACT_DIR/order-release-00040-${RUN_ID}.log}"
MANIFEST_DEST="${PANDORA_TEST_MANIFEST:-$ARTIFACT_DIR/order-release-00040-${RUN_ID}.manifest}"
[[ "$AUDIT_LOG_DEST" != "$MANIFEST_DEST" ]]

LOG="$(mktemp "${TMPDIR:-/tmp}/pandora-release40-log.XXXXXX")"
SOURCE_INVENTORY="$(mktemp "${TMPDIR:-/tmp}/pandora-release40-sources.XXXXXX")"
SOURCE_INVENTORY_AFTER="$(mktemp "${TMPDIR:-/tmp}/pandora-release40-sources-after.XXXXXX")"
VOLUMES_BEFORE="$(mktemp "${TMPDIR:-/tmp}/pandora-release40-volumes.XXXXXX")"
MANIFEST_TMP="$(mktemp "${TMPDIR:-/tmp}/pandora-release40-manifest.XXXXXX")"
chmod 0600 "$LOG" "$SOURCE_INVENTORY" "$SOURCE_INVENTORY_AFTER" \
  "$VOLUMES_BEFORE" "$MANIFEST_TMP"

FIRST_ERROR_LINE=""
BLOCKED_INTERFACE=""
BASELINE_QUERY_FAILED=0
DOCKER_QUERIES_READY=0
RESOURCES_CREATED=0
OWNED_MOUNT_SOURCES=()
PG_VERSION_NUM="unknown"
PG_SYSTEM_IDENTIFIER="unknown"
PG_DATABASE_OID="unknown"
GO_VERSION="unknown"
GOOSE_VERSION="unknown"
GOOSE_BIN_SHA256="unknown"
POSTGRES_IMAGE_ID="unknown"
GO_IMAGE_ID="unknown"
SOURCE_INVENTORY_SHA256="unknown"

required_behavior_markers=(
  order_release_pg18_admin_package_compile_ok
  order_release_pg18_target_identity_ok
  order_release_pg18_commission_forged_insert_rejected_ok
  order_release_pg18_commission_illegal_update_rejected_ok
  order_release_pg18_commission_unfunded_maturity_rejected_ok
  order_release_pg18_commission_refunded_maturity_rejected_ok
  order_release_pg18_extra_commission_settlement_rejected_ok
  order_release_pg18_withdrawal_acl_lifecycle_ok
  order_release_pg18_withdrawal_detached_ledger_rejected_ok
  order_release_pg18_fault_rollback_same_request_retry_ok
  order_release_pg18_cancel_idempotent_ok
  order_release_pg18_cancel_four_resource_conservation_ok
  order_release_pg18_expiry_idempotent_ok
  order_release_pg18_expiry_four_resource_conservation_ok
  order_release_pg18_expiry_two_workers_multibatch_exact_once_ok
  order_release_pg18_cancel_expiry_concurrent_single_winner_ok
  order_release_pg18_released_late_payment_quarantine_exactly_once_ok
  order_release_pg18_paid_distinct_capture_quarantine_exactly_once_ok
  order_release_pg18_fulfilled_distinct_capture_quarantine_exactly_once_ok
)

required_rollback_markers=(
  order_release_pg18_goose_validate_ok
  order_release_pg18_pre40_role_acl_ok
  order_release_pg18_down_unused_ok
  order_release_pg18_reapply_after_clean_down_ok
  order_release_pg18_used_watermark_ok
  order_release_pg18_down_used_refused_ok
  order_release_pg18_down_used_atomicity_ok
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
    "$ROOT/deploy/test-order-release-00040-pg18.sh"
    "$ROOT/deploy/configure-app-role.sql"
    "$ROOT/go.mod"
    "$ROOT/go.sum"
    "$ROOT/internal/api/public/order_cancel.go"
    "$ROOT/internal/api/public/router.go"
    "$ROOT/cmd/aegis-admin/main.go"
    "$ROOT/cmd/aegis-public/main.go"
  )
  for migration in "$ROOT"/migrations/000{01..40}_*.sql; do
    [[ -f "$migration" ]]
    files+=("$migration")
    count=$((count + 1))
  done
  [[ "$count" -eq 40 ]]
  while IFS= read -r -d '' file; do
    files+=("$file")
  done < <(find "$ROOT/internal/domain/billing" -maxdepth 1 -type f -name '*.go' -print0)
  while IFS= read -r -d '' file; do
    files+=("$file")
  done < <(find "$ROOT/internal/api/admin" -maxdepth 1 -type f -name '*.go' -print0)
  printf '%s\n' "${files[@]}" | LC_ALL=C sort -u | while IFS= read -r file; do
    [[ -f "$file" ]]
    printf '%s  %s\n' "$(sha256sum "$file" | awk '{print $1}')" "${file#"$ROOT"/}"
  done >"$destination"
}

resource_name_present() {
  local kind="$1" name="$2" names
  case "$kind" in
    container) names="$(docker ps -a --format '{{.Names}}')" || return 2 ;;
    network) names="$(docker network ls --format '{{.Name}}')" || return 2 ;;
    volume) names="$(docker volume ls --format '{{.Name}}')" || return 2 ;;
    *) return 2 ;;
  esac
  awk -v expected="$name" '$0==expected{found=1} END{exit found?0:1}' <<<"$names"
}

inspect_absent() {
  local kind="$1" name="$2"
  if docker "$kind" inspect "$name" >/dev/null 2>&1; then
    printf 'order_release_pg18_refused_preexisting_%s=%s\n' "$kind" "$name" >&2
    return 1
  fi
  docker info >/dev/null 2>&1 || {
    printf 'order_release_pg18_%s_inspect_infrastructure_error=%s\n' "$kind" "$name" >&2
    return 1
  }
  docker "$kind" inspect "$name" >/dev/null 2>&1 && {
    printf 'order_release_pg18_%s_inspect_transient_error=%s\n' "$kind" "$name" >&2
    return 1
  }
  if resource_name_present "$kind" "$name"; then
    printf 'order_release_pg18_%s_inspect_failed_but_listed=%s\n' "$kind" "$name" >&2
    return 1
  else
    local list_rc=$?
    if [[ "$list_rc" -eq 1 ]]; then
      return 0
    fi
  fi
  printf 'order_release_pg18_%s_list_infrastructure_error=%s\n' "$kind" "$name" >&2
  return 1
}

remove_owned_container() {
  local id="$1" expected="$2" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker container inspect -f '{{.Id}}|{{.Name}}|{{index .Config.Labels "pandora.test"}}|{{index .Config.Labels "pandora.order-release-00040.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || {
      printf 'order_release_pg18_container_inspect_infrastructure_error=%s\n' "$expected" >&2
      return 1
    }
    docker container inspect "$id" >/dev/null 2>&1 && {
      printf 'order_release_pg18_container_inspect_transient_error=%s\n' "$expected" >&2
      return 1
    }
    if resource_name_present container "$expected"; then
      printf 'order_release_pg18_container_inspect_failed_but_listed=%s\n' "$expected" >&2
      return 1
    elif [[ "$?" -eq 1 ]]; then
      return 0
    fi
    printf 'order_release_pg18_container_list_infrastructure_error=%s\n' "$expected" >&2
    return 1
  fi
  [[ "$actual" == "$id|/$expected|$LABEL_TEST_VALUE|$RUN_ID" ]] || {
    printf 'order_release_pg18_refused_unowned_container=%s actual=%s\n' "$expected" "$actual" >&2
    return 1
  }
  docker rm -fv "$id" >/dev/null
}

remove_owned_network() {
  local id="$1" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker network inspect -f '{{.Id}}|{{.Name}}|{{index .Labels "pandora.test"}}|{{index .Labels "pandora.order-release-00040.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || {
      printf 'order_release_pg18_network_inspect_infrastructure_error=%s\n' "$NETWORK" >&2
      return 1
    }
    docker network inspect "$id" >/dev/null 2>&1 && {
      printf 'order_release_pg18_network_inspect_transient_error=%s\n' "$NETWORK" >&2
      return 1
    }
    if resource_name_present network "$NETWORK"; then
      printf 'order_release_pg18_network_inspect_failed_but_listed=%s\n' "$NETWORK" >&2
      return 1
    elif [[ "$?" -eq 1 ]]; then
      return 0
    fi
    printf 'order_release_pg18_network_list_infrastructure_error=%s\n' "$NETWORK" >&2
    return 1
  fi
  [[ "$actual" == "$id|$NETWORK|$LABEL_TEST_VALUE|$RUN_ID" ]] || {
    printf 'order_release_pg18_refused_unowned_network=%s actual=%s\n' "$NETWORK" "$actual" >&2
    return 1
  }
  docker network rm "$id" >/dev/null
}

verify_owned_volume() {
  local identity="$1" name="$2" actual labels
  actual="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$name" 2>/dev/null)" || return 1
  labels="$(docker volume inspect -f '{{index .Labels "pandora.test"}}|{{index .Labels "pandora.order-release-00040.run"}}' "$name")" || return 1
  [[ "$actual" == "$identity" && "$labels" == "$LABEL_TEST_VALUE|$RUN_ID" ]] || {
    printf 'order_release_pg18_refused_unowned_volume=%s actual=%s labels=%s\n' "$name" "$actual" "$labels" >&2
    return 1
  }
}

remove_owned_volume() {
  local identity="$1" name="$2"
  [[ -n "$identity" ]] || return 0
  if ! docker volume inspect "$name" >/dev/null 2>&1; then
    docker info >/dev/null 2>&1 || {
      printf 'order_release_pg18_volume_inspect_infrastructure_error=%s\n' "$name" >&2
      return 1
    }
    docker volume inspect "$name" >/dev/null 2>&1 && {
      printf 'order_release_pg18_volume_inspect_transient_error=%s\n' "$name" >&2
      return 1
    }
    if resource_name_present volume "$name"; then
      printf 'order_release_pg18_volume_inspect_failed_but_listed=%s\n' "$name" >&2
      return 1
    elif [[ "$?" -eq 1 ]]; then
      return 0
    fi
    printf 'order_release_pg18_volume_list_infrastructure_error=%s\n' "$name" >&2
    return 1
  fi
  verify_owned_volume "$identity" "$name" || return 1
  docker volume rm "$name" >/dev/null
}

cleanup() {
  local business_rc="$1" cleanup_rc=0 final_rc query_failed="$BASELINE_QUERY_FAILED"
  local containers='' networks='' volumes='' labels_c='' labels_n='' labels_v=''
  local exact_c=0 exact_n=0 exact_v=0 label_remaining=0 mount_remaining=0 anonymous_new=-1
  local source new_all='' audit_sha='unavailable' manifest_sha='unavailable' secret_scan='ok'
  local source_block=''
  trap - EXIT ERR INT TERM
  set +e

  remove_owned_container "$GO_CONTAINER_ID" "$GO_CONTAINER" || cleanup_rc=1
  remove_owned_container "$PG_CONTAINER_ID" "$PG_CONTAINER" || cleanup_rc=1
  remove_owned_network "$NETWORK_ID" || cleanup_rc=1
  remove_owned_volume "$PG_VOLUME_ID" "$PG_VOLUME" || cleanup_rc=1
  remove_owned_volume "$MOD_VOLUME_ID" "$MOD_VOLUME" || cleanup_rc=1

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
  if [[ -f "$AUDIT_LOG_DEST" ]]; then
    audit_sha="$(sha256sum "$AUDIT_LOG_DEST" | awk '{print $1}')"
  fi
  rm -f "$LOG" "$LOG.all.after" "$LOG.down-used" "$SOURCE_INVENTORY" \
    "$SOURCE_INVENTORY_AFTER" "$VOLUMES_BEFORE" || cleanup_rc=1
  for source in "$LOG" "$LOG.all.after" "$LOG.down-used" "$SOURCE_INVENTORY" \
    "$SOURCE_INVENTORY_AFTER" "$VOLUMES_BEFORE"; do
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
    printf 'schema=pandora-order-release-pg18-manifest-v1\n'
    printf 'run_id=%s\n' "$RUN_ID"
    printf 'created_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'database_name=%s\n' "$DB_NAME"
    printf 'postgres_image=%s\npostgres_image_id=%s\npostgres_version_num=%s\n' \
      "$POSTGRES_IMAGE" "$POSTGRES_IMAGE_ID" "$PG_VERSION_NUM"
    printf 'postgres_system_identifier=%s\npostgres_database_oid=%s\n' \
      "$PG_SYSTEM_IDENTIFIER" "$PG_DATABASE_OID"
    printf 'go_image=%s\ngo_image_id=%s\ngo_version=%s\n' \
      "$GO_IMAGE" "$GO_IMAGE_ID" "$GO_VERSION"
    printf 'goose_version=%s\ngoose_bin_sha256=%s\n' "$GOOSE_VERSION" "$GOOSE_BIN_SHA256"
    printf 'source_inventory_sha256=%s\n' "$SOURCE_INVENTORY_SHA256"
    printf 'audit_log_sha256=%s\n' "$audit_sha"
    printf 'business_exit=%s\ncleanup_exit=%s\nfinal_exit=%s\n' \
      "$business_rc" "$cleanup_rc" "$final_rc"
    printf 'cleanup_query_failed=%s\ncleanup_exact_containers=%s\ncleanup_exact_network=%s\n' \
      "$query_failed" "$exact_c" "$exact_n"
    printf 'cleanup_exact_volumes=%s\ncleanup_label_residual=%s\n' \
      "$exact_v" "$label_remaining"
    printf 'cleanup_mount_source_residual=%s\ncleanup_anonymous_volume_new=%s\n' \
      "$mount_remaining" "$anonymous_new"
    printf 'secret_scan=%s\n' "$secret_scan"
    [[ -z "$BLOCKED_INTERFACE" ]] || printf 'blocked_interface=%s\n' "$BLOCKED_INTERFACE"
    [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error=%s\n' "$FIRST_ERROR_LINE"
    if [[ "$cleanup_rc" -eq 0 ]]; then
      printf 'cleanup_marker=order_release_pg18_cleanup_ok\n'
    else
      printf 'cleanup_marker=order_release_pg18_cleanup_failed\n'
    fi
    printf '%s\n' '--- source_sha256 ---'
    printf '%s\n' "$source_block"
  } >"$MANIFEST_TMP" || cleanup_rc=1
  install -m 0600 "$MANIFEST_TMP" "$MANIFEST_DEST" || cleanup_rc=1
  rm -f "$MANIFEST_TMP" || cleanup_rc=1
  [[ ! -e "$MANIFEST_TMP" ]] || cleanup_rc=1

  # A manifest write failure is itself a cleanup/evidence failure. Rewrite the
  # exit fields once when the destination exists so it cannot claim success.
  if [[ "$cleanup_rc" -ne 0 ]]; then
    if [[ "$final_rc" -eq 0 ]]; then final_rc=1; fi
    sed -i 's/^cleanup_exit=0$/cleanup_exit=1/; s/^final_exit=0$/final_exit=1/; s/^cleanup_marker=order_release_pg18_cleanup_ok$/cleanup_marker=order_release_pg18_cleanup_failed/' \
      "$MANIFEST_DEST" 2>/dev/null || true
  fi
  if [[ -f "$MANIFEST_DEST" ]]; then
    manifest_sha="$(sha256sum "$MANIFEST_DEST" | awk '{print $1}')"
  fi

  printf 'business_exit=%s cleanup_exit=%s final_exit=%s\n' "$business_rc" "$cleanup_rc" "$final_rc" >&3
  printf 'cleanup_query_failed=%s exact_containers=%s exact_network=%s exact_volumes=%s exact_label_residual=%s mount_source_residual=%s anonymous_volume_new=%s\n' \
    "$query_failed" "$exact_c" "$exact_n" "$exact_v" "$label_remaining" \
    "$mount_remaining" "$anonymous_new" >&3
  printf 'secret_scan=%s\n' "$secret_scan" >&3
  if [[ "$cleanup_rc" -eq 0 ]]; then
    echo 'cleanup_marker=order_release_pg18_cleanup_ok' >&3
  else
    echo 'cleanup_marker=order_release_pg18_cleanup_failed' >&4
  fi
  [[ -z "$BLOCKED_INTERFACE" ]] || printf 'blocked_interface=%s\n' "$BLOCKED_INTERFACE" >&4
  [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error=%s\n' "$FIRST_ERROR_LINE" >&4
  printf 'audit_log=%s audit_log_sha256=%s\n' "$AUDIT_LOG_DEST" "$audit_sha" >&3
  printf 'manifest=%s manifest_sha256=%s\n' "$MANIFEST_DEST" "$manifest_sha" >&3
  if [[ "$final_rc" -eq 0 && "$cleanup_rc" -eq 0 && "$BUSINESS_OK" -eq 1 ]]; then
    echo 'order_release_pg18_full_suite=ok cleanup=ok business=ok schema=40' >&3
  fi

  exit "$final_rc"
}
trap 'cleanup $?' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

exec 3>&1 4>&2
exec >>"$LOG" 2>&1

for command in awk cmp comm find grep install sed sha256sum sort tr; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "${command} is required" >&2
    exit 1
  }
done

if [[ -z "$GOOSE_BIN" || ! -x "$GOOSE_BIN" ]]; then
  BLOCKED_INTERFACE='PANDORA_TEST_GOOSE_BIN'
  echo 'BLOCKED: a pinned Goose executable is required for migration parser validation' >&2
  exit 78
fi
GOOSE_VERSION="$("$GOOSE_BIN" -version | awk '{print $NF}')"
[[ "$GOOSE_VERSION" == "$GOOSE_VERSION_EXPECTED" ]]
GOOSE_BIN_SHA256="$(sha256sum "$GOOSE_BIN" | awk '{print $1}')"
"$GOOSE_BIN" -dir "$ROOT/migrations" validate
echo 'marker=order_release_pg18_goose_validate_ok'

write_source_inventory "$SOURCE_INVENTORY"
SOURCE_INVENTORY_SHA256="$(sha256sum "$SOURCE_INVENTORY" | awk '{print $1}')"

if ! grep -Rqs '^func TestOrderReleasePG18' "$ROOT/internal/domain/billing"; then
  BLOCKED_INTERFACE='TestOrderReleasePG18'
  echo 'BLOCKED: TestOrderReleasePG18 is not present; refusing to fabricate gate success' >&2
  exit 78
fi

command -v docker >/dev/null 2>&1 || { echo 'docker is required' >&2; exit 1; }

docker info >/dev/null
docker volume ls --format '{{.Name}}' 2>/dev/null | LC_ALL=C sort >"$VOLUMES_BEFORE" || BASELINE_QUERY_FAILED=1
[[ "$BASELINE_QUERY_FAILED" -eq 0 ]]
DOCKER_QUERIES_READY=1
for name in "$PG_CONTAINER" "$GO_CONTAINER"; do inspect_absent container "$name"; done
inspect_absent network "$NETWORK"
for name in "$PG_VOLUME" "$MOD_VOLUME"; do inspect_absent volume "$name"; done
POSTGRES_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")" || {
  echo 'order_release_pg18_postgres_image_not_local' >&2
  exit 2
}
GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")" || {
  echo 'order_release_pg18_go_image_not_local' >&2
  exit 2
}
[[ "$POSTGRES_IMAGE_ID" == sha256:* && "$GO_IMAGE_ID" == sha256:* ]]

up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1;next} up' "$1"; }
down_sql() { awk '/^-- \+goose Down/{down=1;next} down' "$1"; }
psql_admin_db() {
  local database="$1"
  shift
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER_ID" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$database" "$@"
}
psql_admin() {
  psql_admin_db "$DB_NAME" "$@"
}
configure_role_db() {
  local database="$1"
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
    "$PG_CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 -U postgres -d "$database" \
    -f /src/deploy/configure-app-role.sql >/dev/null
}
configure_role() {
  configure_role_db "$DB_NAME"
}
apply_guarded_db() {
  local database="$1" number="$2" name="$3"
  {
    echo 'BEGIN;'
    echo "SET LOCAL app.idempotency_writers_stopped='yes';"
    echo "SET LOCAL app.allow_idempotency_schema${number}_up='yes';"
    up_sql "$ROOT/migrations/$name"
    echo 'COMMIT;'
  } | psql_admin_db "$database" >/dev/null
}
apply_guarded() {
  apply_guarded_db "$DB_NAME" "$@"
}

NETWORK_ID="$(docker network create --label "$LABEL_TEST_KEY=$LABEL_TEST_VALUE" \
  --label "$LABEL_KEY=$RUN_ID" "$NETWORK")"
RESOURCES_CREATED=1
docker volume create --label "$LABEL_TEST_KEY=$LABEL_TEST_VALUE" --label "$LABEL_KEY=$RUN_ID" "$PG_VOLUME" >/dev/null
PG_VOLUME_ID="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$PG_VOLUME")"
verify_owned_volume "$PG_VOLUME_ID" "$PG_VOLUME"
docker volume create --label "$LABEL_TEST_KEY=$LABEL_TEST_VALUE" --label "$LABEL_KEY=$RUN_ID" "$MOD_VOLUME" >/dev/null
MOD_VOLUME_ID="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$MOD_VOLUME")"
verify_owned_volume "$MOD_VOLUME_ID" "$MOD_VOLUME"
PG_CONTAINER_ID="$(docker create --name "$PG_CONTAINER" --network "$NETWORK_ID" --network-alias pg18 \
  --label "$LABEL_TEST_KEY=$LABEL_TEST_VALUE" --label "$LABEL_KEY=$RUN_ID" \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" -e POSTGRES_DB="$DB_NAME" \
  --cpus "$POSTGRES_CPUS" \
  --mount "type=volume,src=$PG_VOLUME,dst=/var/lib/postgresql" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" "$POSTGRES_IMAGE_ID")"
GO_CONTAINER_ID="$(docker create --name "$GO_CONTAINER" --network "$NETWORK_ID" \
  --label "$LABEL_TEST_KEY=$LABEL_TEST_VALUE" --label "$LABEL_KEY=$RUN_ID" \
  --cpus "$GO_CPUS" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" \
  --mount "type=volume,src=$MOD_VOLUME,dst=/go/pkg/mod" -w /src \
  -e "AEGIS_ORDER_RELEASE_PG18_DSN=$APP_DSN" \
  -e "AEGIS_ORDER_RELEASE_PG18_ADMIN_DSN=$ADMIN_DSN" \
  "$GO_IMAGE_ID" sh -ec '
    case "$(go env GOVERSION)" in go1.26.*) ;; *) echo "Go 1.26 is required" >&2; exit 1;; esac
    go version
    go test -count=1 ./internal/api/admin
    go test -run "^$" -count=1 ./cmd/aegis-admin
    echo marker=order_release_pg18_admin_package_compile_ok
    go test -v -count=1 -timeout=180s -run "^TestOrderReleasePG18$" ./internal/domain/billing
  ')"

for container in "$PG_CONTAINER_ID" "$GO_CONTAINER_ID"; do
  [[ "$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$container")" == "$RUN_ID" ]]
  while IFS='|' read -r type name source destination rw; do
    [[ -n "$type" ]] || continue
    [[ "$type" != volume ]] || OWNED_MOUNT_SOURCES+=("$source")
  done < <(docker inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' "$container")
done
[[ "$(docker network inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$NETWORK_ID")" == "$RUN_ID" ]]
[[ "$(docker volume inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$PG_VOLUME")" == "$RUN_ID" ]]
[[ "$(docker volume inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$MOD_VOLUME")" == "$RUN_ID" ]]
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER_ID")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER_ID")" == 'null' ]]

docker start "$PG_CONTAINER_ID" >/dev/null
for _ in $(seq 1 90); do
  docker exec "$PG_CONTAINER_ID" pg_isready -U postgres -d "$DB_NAME" >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$PG_CONTAINER_ID" pg_isready -U postgres -d "$DB_NAME" >/dev/null
PG_VERSION_NUM="$(psql_admin -Atqc 'SHOW server_version_num')"
[[ "$PG_VERSION_NUM" =~ ^[0-9]+$ && "$PG_VERSION_NUM" -ge 180000 && "$PG_VERSION_NUM" -lt 190000 ]]
echo "marker=order_release_pg18_postgres_version_ok version_num=$PG_VERSION_NUM"
IFS='|' read -r PG_SYSTEM_IDENTIFIER PG_DATABASE_OID target_database < <(
  psql_admin -Atqc "SELECT system_identifier::text,
    (SELECT oid::text FROM pg_database WHERE datname=current_database()),current_database()
    FROM pg_control_system()"
)
[[ "$PG_SYSTEM_IDENTIFIER" =~ ^[0-9]+$ && "$PG_DATABASE_OID" =~ ^[0-9]+$ && "$target_database" == "$DB_NAME" ]]
echo "marker=order_release_pg18_runner_target_ok run_id=$RUN_ID system_identifier=$PG_SYSTEM_IDENTIFIER database=$target_database oid=$PG_DATABASE_OID"

count=0
for migration in "$ROOT"/migrations/000{01..36}_*.sql; do
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
guard_pre40="$(psql_admin -Atqc \
  "SELECT md5(pg_get_functiondef('app.guard_payment_intent()'::regprocedure))")"
[[ "$guard_pre40" =~ ^[0-9a-f]{32}$ ]]
pre40_role_surface="$(psql_admin -Atqc "SELECT
  (to_regclass('app.order_release_00040_meta') IS NULL)::int,
  (NOT has_column_privilege('aegis_app','public.orders','cancelled_at','UPDATE'))::int,
  has_table_privilege('aegis_app','public.commission_entries','INSERT')::int,
  has_table_privilege('aegis_app','public.commission_entries','UPDATE')::int,
  has_table_privilege('aegis_app','public.commission_entries','DELETE')::int,
  has_table_privilege('aegis_app','public.withdrawals','INSERT')::int,
  has_table_privilege('aegis_app','public.withdrawals','UPDATE')::int,
  has_table_privilege('aegis_app','public.withdrawals','DELETE')::int")"
[[ "$pre40_role_surface" == '1|1|1|1|1|1|1|1' ]]
echo 'marker=order_release_pg18_pre40_role_acl_ok'
{ echo 'BEGIN;'; up_sql "$ROOT/migrations/00040_order_release_and_late_suspense.sql"; echo 'COMMIT;'; } | psql_admin >/dev/null
configure_role

surface="$(psql_admin -Atqc "SELECT
  (to_regclass('app.order_release_00040_meta') IS NOT NULL)::int,
  (to_regprocedure('app.assert_late_payment_suspense(uuid,uuid,uuid)') IS NOT NULL)::int,
  (to_regprocedure('app.assert_commission_entry(uuid,uuid)') IS NOT NULL)::int,
  (to_regprocedure('app.assert_commission_ledger_trigger()') IS NOT NULL)::int,
  (to_regprocedure('app.guard_withdrawal()') IS NOT NULL)::int,
  (to_regprocedure('app.assert_withdrawal(uuid,uuid)') IS NOT NULL)::int,
  (to_regprocedure('app.mark_order_release_00040_used()') IS NOT NULL)::int,
  (SELECT count(*) FROM pg_trigger WHERE tgname IN
    ('trg_late_payment_case_suspense_commit','trg_late_payment_transaction_commit',
     'trg_late_payment_entries_commit','trg_commission_entry_guard','trg_commission_entry_commit',
     'trg_commission_transaction_commit','trg_commission_ledger_entries_commit',
     'trg_withdrawal_guard','trg_withdrawal_commit','trg_withdrawals_no_delete')),
  has_function_privilege('aegis_app','app.assert_late_payment_suspense(uuid,uuid,uuid)','EXECUTE')::int,
  has_function_privilege('aegis_app','app.assert_commission_entry(uuid,uuid)','EXECUTE')::int,
  has_function_privilege('aegis_app','app.assert_commission_ledger_trigger()','EXECUTE')::int,
  has_function_privilege('aegis_app','app.guard_withdrawal()','EXECUTE')::int,
  has_function_privilege('aegis_app','app.assert_withdrawal(uuid,uuid)','EXECUTE')::int,
  has_function_privilege('aegis_app','app.mark_order_release_00040_used()','EXECUTE')::int,
  has_table_privilege('aegis_app','public.withdrawals','INSERT')::int,
  has_table_privilege('aegis_app','public.withdrawals','UPDATE')::int,
  has_table_privilege('aegis_app','public.withdrawals','DELETE')::int,
  has_table_privilege('aegis_app','public.withdrawals','TRUNCATE')::int,
  has_column_privilege('aegis_app','public.withdrawals','tenant_id','INSERT')::int,
  has_column_privilege('aegis_app','public.withdrawals','status','UPDATE')::int,
  has_column_privilege('aegis_app','public.withdrawals','amount','UPDATE')::int,
  has_column_privilege('aegis_app','public.orders','cancelled_at','UPDATE')::int")"
[[ "$surface" == '1|1|1|1|1|1|1|10|0|0|0|0|0|0|0|0|0|0|1|1|0|1' ]]
echo 'marker=order_release_pg18_real_migrations_1_40_configured_ok'

# Prove clean Down and reapply on this same isolated database before any
# Schema40 business write. Roles are cluster-global, so a second database in
# the same PostgreSQL cluster is not an independent role/ownership fixture.
{ echo 'BEGIN;'; echo "SET LOCAL app.order_release_writers_stopped='yes';"; \
  echo "SET LOCAL app.allow_order_release_schema40_down='yes';"; \
  down_sql "$ROOT/migrations/00040_order_release_and_late_suspense.sql"; echo 'COMMIT;'; } | \
  psql_admin >/dev/null
configure_role
rollback_surface="$(psql_admin -Atqc "SELECT
  (to_regclass('app.order_release_00040_meta') IS NULL)::int,
  (NOT EXISTS (SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='late_payment_cases' AND column_name='case_kind'))::int,
  (SELECT count(*)=0 FROM pg_trigger WHERE tgname IN
    ('trg_late_payment_case_suspense_commit','trg_late_payment_transaction_commit',
     'trg_late_payment_entries_commit','trg_late_payment_cases_no_delete',
     'trg_commission_entry_guard','trg_commission_entry_commit',
     'trg_commission_transaction_commit','trg_commission_ledger_entries_commit',
     'trg_commission_entries_no_delete','trg_withdrawal_guard','trg_withdrawal_commit',
     'trg_withdrawals_no_delete','trg_order_release_00040_late_used',
     'trg_order_release_00040_commission_used','trg_order_release_00040_event_used',
     'trg_order_release_00040_intent_used','trg_order_release_00040_withdrawal_used'))::int,
  (NOT has_column_privilege('aegis_app','public.orders','cancelled_at','UPDATE'))::int,
  has_table_privilege('aegis_app','public.commission_entries','INSERT')::int,
  has_table_privilege('aegis_app','public.commission_entries','UPDATE')::int,
  has_table_privilege('aegis_app','public.commission_entries','DELETE')::int,
  has_table_privilege('aegis_app','public.withdrawals','INSERT')::int,
  has_table_privilege('aegis_app','public.withdrawals','UPDATE')::int,
  has_table_privilege('aegis_app','public.withdrawals','DELETE')::int,
  (md5(pg_get_functiondef('app.guard_payment_intent()'::regprocedure))='$guard_pre40')::int")"
echo "rollback_surface=$rollback_surface"
[[ "$rollback_surface" == '1|1|1|1|1|1|1|1|1|1|1' ]]
echo 'marker=order_release_pg18_down_unused_ok'
{ echo 'BEGIN;'; up_sql "$ROOT/migrations/00040_order_release_and_late_suspense.sql"; echo 'COMMIT;'; } | \
  psql_admin >/dev/null
configure_role
reapply_surface="$(psql_admin -Atqc "SELECT
  (SELECT NOT used FROM app.order_release_00040_meta WHERE singleton)::int,
  (to_regprocedure('app.assert_late_payment_suspense(uuid,uuid,uuid)') IS NOT NULL)::int,
  (to_regprocedure('app.assert_commission_entry(uuid,uuid)') IS NOT NULL)::int,
  (SELECT count(*) FROM pg_trigger WHERE tgname IN
    ('trg_late_payment_case_suspense_commit','trg_late_payment_transaction_commit',
     'trg_late_payment_entries_commit','trg_late_payment_cases_no_delete',
     'trg_commission_entry_guard','trg_commission_entry_commit',
     'trg_commission_transaction_commit','trg_commission_ledger_entries_commit',
     'trg_commission_entries_no_delete','trg_withdrawal_guard','trg_withdrawal_commit',
     'trg_withdrawals_no_delete','trg_order_release_00040_late_used',
     'trg_order_release_00040_commission_used','trg_order_release_00040_event_used',
     'trg_order_release_00040_intent_used','trg_order_release_00040_withdrawal_used')),
  has_function_privilege('aegis_app','app.assert_withdrawal(uuid,uuid)','EXECUTE')::int,
  has_table_privilege('aegis_app','public.withdrawals','INSERT')::int,
  has_table_privilege('aegis_app','public.withdrawals','UPDATE')::int,
  has_table_privilege('aegis_app','public.withdrawals','DELETE')::int,
  has_column_privilege('aegis_app','public.withdrawals','tenant_id','INSERT')::int,
  has_column_privilege('aegis_app','public.withdrawals','status','UPDATE')::int,
  has_column_privilege('aegis_app','public.withdrawals','amount','UPDATE')::int,
  has_table_privilege('aegis_app','public.commission_entries','INSERT')::int,
  has_table_privilege('aegis_app','public.commission_entries','UPDATE')::int,
  has_table_privilege('aegis_app','public.commission_entries','DELETE')::int,
  has_column_privilege('aegis_app','public.commission_entries','commission_amount','INSERT')::int,
  has_column_privilege('aegis_app','public.commission_entries','status','UPDATE')::int,
  has_column_privilege('aegis_app','public.orders','cancelled_at','UPDATE')::int")"
[[ "$reapply_surface" == '1|1|1|17|0|0|0|0|1|1|0|0|0|0|1|1|1' ]]
echo 'marker=order_release_pg18_reapply_after_clean_down_ok'

docker start -a "$GO_CONTAINER_ID"
GO_VERSION="$(grep -Em1 '^go version go1\.26\.' "$LOG" | awk '{print $3}')"
[[ "$GO_VERSION" == go1.26.* ]]
grep -Eq '^=== RUN[[:space:]]+TestOrderReleasePG18$' "$LOG"
grep -Eq '^--- PASS: TestOrderReleasePG18 ' "$LOG"
for marker in "${required_behavior_markers[@]}"; do
  marker_count="$(grep -Ec "(^|[[:space:]])marker=${marker}([[:space:]]|$)" "$LOG" || true)"
  [[ "$marker_count" -eq 1 ]]
done

# The business suite necessarily commits at least one Schema40-controlled
# mutation. Down must now fail before destructive DDL and leave the surface
# intact.
used_state_before="$(psql_admin -Atqc "SELECT
  (SELECT used FROM app.order_release_00040_meta WHERE singleton)::int,
  (SELECT count(*) FROM late_payment_cases),
  (SELECT count(*) FROM commission_entries),
  (SELECT count(*) FROM withdrawals),
  (SELECT count(*) FROM order_reservation_events),
  (SELECT count(*) FROM pg_trigger WHERE tgname IN
    ('trg_late_payment_case_suspense_commit','trg_late_payment_transaction_commit',
     'trg_late_payment_entries_commit','trg_late_payment_cases_no_delete',
     'trg_commission_entry_guard','trg_commission_entry_commit',
     'trg_commission_transaction_commit','trg_commission_ledger_entries_commit',
     'trg_commission_entries_no_delete','trg_withdrawal_guard','trg_withdrawal_commit',
     'trg_withdrawals_no_delete','trg_order_release_00040_late_used',
     'trg_order_release_00040_commission_used','trg_order_release_00040_event_used',
     'trg_order_release_00040_intent_used','trg_order_release_00040_withdrawal_used'))")"
[[ "$used_state_before" == 1\|*\|17 ]]
echo 'marker=order_release_pg18_used_watermark_ok'
if { echo 'BEGIN;'; echo "SET LOCAL app.order_release_writers_stopped='yes';"; \
  echo "SET LOCAL app.allow_order_release_schema40_down='yes';"; \
  down_sql "$ROOT/migrations/00040_order_release_and_late_suspense.sql"; echo 'COMMIT;'; } | \
  psql_admin >/dev/null 2>"$LOG.down-used"; then
  echo 'used Schema40 rollback unexpectedly succeeded' >&2
  exit 1
fi
grep -Fq 'cannot rollback 00040: release, commission or withdrawal evidence exists' "$LOG.down-used"
cat "$LOG.down-used"
used_surface="$(psql_admin -Atqc "SELECT
  (SELECT used FROM app.order_release_00040_meta WHERE singleton)::int,
  (to_regprocedure('app.assert_late_payment_suspense(uuid,uuid,uuid)') IS NOT NULL)::int,
  (to_regprocedure('app.assert_commission_entry(uuid,uuid)') IS NOT NULL)::int,
  (SELECT count(*) FROM pg_trigger WHERE tgname IN
    ('trg_late_payment_case_suspense_commit','trg_late_payment_transaction_commit',
     'trg_late_payment_entries_commit','trg_commission_entry_guard','trg_commission_entry_commit',
     'trg_commission_transaction_commit','trg_commission_ledger_entries_commit'))")"
[[ "$used_surface" == '1|1|1|7' ]]
echo 'marker=order_release_pg18_down_used_refused_ok'
used_state_after="$(psql_admin -Atqc "SELECT
  (SELECT used FROM app.order_release_00040_meta WHERE singleton)::int,
  (SELECT count(*) FROM late_payment_cases),
  (SELECT count(*) FROM commission_entries),
  (SELECT count(*) FROM withdrawals),
  (SELECT count(*) FROM order_reservation_events),
  (SELECT count(*) FROM pg_trigger WHERE tgname IN
    ('trg_late_payment_case_suspense_commit','trg_late_payment_transaction_commit',
     'trg_late_payment_entries_commit','trg_late_payment_cases_no_delete',
     'trg_commission_entry_guard','trg_commission_entry_commit',
     'trg_commission_transaction_commit','trg_commission_ledger_entries_commit',
     'trg_commission_entries_no_delete','trg_withdrawal_guard','trg_withdrawal_commit',
     'trg_withdrawals_no_delete','trg_order_release_00040_late_used',
     'trg_order_release_00040_commission_used','trg_order_release_00040_event_used',
     'trg_order_release_00040_intent_used','trg_order_release_00040_withdrawal_used'))")"
[[ "$used_state_after" == "$used_state_before" ]]
echo 'marker=order_release_pg18_down_used_atomicity_ok'
for marker in "${required_rollback_markers[@]}"; do
  marker_count="$(grep -Ec "(^|[[:space:]])marker=${marker}([[:space:]]|$)" "$LOG" || true)"
  [[ "$marker_count" -eq 1 ]]
done

write_source_inventory "$SOURCE_INVENTORY_AFTER"
cmp -s "$SOURCE_INVENTORY" "$SOURCE_INVENTORY_AFTER"
echo 'order_release_pg18_business=ok schema=40 cleanup=pending'
BUSINESS_OK=1
