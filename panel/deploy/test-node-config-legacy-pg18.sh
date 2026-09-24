#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:-golang:1.26}"
GOOSE_BIN="${PANDORA_TEST_GOOSE_BIN:-}"
GOOSE_VERSION_EXPECTED="${PANDORA_TEST_GOOSE_VERSION:-v3.27.3}"
GOOSE_SHA256_EXPECTED="${PANDORA_TEST_GOOSE_SHA256:-}"
GO_MODCACHE="${PANDORA_TEST_GO_MODCACHE:-}"
GO_MODCACHE_MANIFEST="${PANDORA_TEST_GO_MODCACHE_MANIFEST:-}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
RUN_SAFE="$(printf '%s' "$RUN_ID" | tr '[:upper:]-' '[:lower:]_')"
DB_NAME="pandora_nodecfg_${RUN_SAFE}"
PG_CONTAINER="pandora-nodecfg-pg-${RUN_ID}"
GO_CONTAINER="pandora-nodecfg-go-${RUN_ID}"
NETWORK="pandora-nodecfg-net-${RUN_ID}"
PG_VOLUME="pandora-nodecfg-pgdata-${RUN_ID}"
CACHE_VOLUME="pandora-nodecfg-cache-${RUN_ID}"
LABEL_KEY="pandora.node-config-pg18.run"
DSN_SECRET_ERE='postgres(ql)?://[^[:space:]]+:[^[:space:]@]+@'
PRIVATE_KEY_ERE='BEGIN (ENCRYPTED |RSA |DSA |EC |OPENSSH )?PRIVATE KEY'

ARTIFACT_DIR="${PANDORA_TEST_ARTIFACT_DIR:-}"
if [[ -z "$ARTIFACT_DIR" ]]; then
  ARTIFACT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-nodecfg-evidence.XXXXXX")"
else
  install -d -m 0700 "$ARTIFACT_DIR"
fi
AUDIT_LOG_DEST="${PANDORA_TEST_AUDIT_LOG:-$ARTIFACT_DIR/node-config-pg18-${RUN_ID}.log}"
MANIFEST_DEST="${PANDORA_TEST_MANIFEST:-$ARTIFACT_DIR/node-config-pg18-${RUN_ID}.manifest}"
[[ "$AUDIT_LOG_DEST" != "$MANIFEST_DEST" ]]
if [[ -e "$AUDIT_LOG_DEST" || -e "$MANIFEST_DEST" ]]; then
  echo 'refusing to overwrite existing PG18 evidence artifacts' >&2
  exit 73
fi

umask 077
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-nodecfg-run.XXXXXX")"
WORK_DIR_ID="$(stat -c '%d:%i' "$WORK_DIR")"
WORK_OWNER_TOKEN="pandora-nodecfg-owner:$RUN_ID"
WORK_OWNER_MARKER="$WORK_DIR/.owner"
printf '%s\n' "$WORK_OWNER_TOKEN" >"$WORK_OWNER_MARKER"
SECRET_DIR="$WORK_DIR/secrets"
mkdir -m 0700 "$SECRET_DIR"
POSTGRES_SECRET="$SECRET_DIR/postgres_password"
APP_SECRET="$SECRET_DIR/app_password"
LOG="$WORK_DIR/run.log"
SOURCE_BEFORE="$WORK_DIR/source.before"
SOURCE_AFTER="$WORK_DIR/source.after"
SOURCE_SNAPSHOT="$WORK_DIR/source-snapshot"
SOURCE_SNAPSHOT_BEFORE="$WORK_DIR/source-snapshot.before"
SOURCE_SNAPSHOT_AFTER="$WORK_DIR/source-snapshot.after"
MODCACHE_SNAPSHOT="$WORK_DIR/gomodcache-snapshot"
MODCACHE_HOST_INVENTORY="$WORK_DIR/gomodcache.host"
MODCACHE_EXPECTED_MANIFEST="$WORK_DIR/gomodcache.expected"
MODCACHE_SNAPSHOT_BEFORE="$WORK_DIR/gomodcache-snapshot.before"
MODCACHE_SNAPSHOT_AFTER="$WORK_DIR/gomodcache-snapshot.after"
VOLUMES_BEFORE="$WORK_DIR/volumes.before"
MANIFEST_TMP="$WORK_DIR/manifest.tmp"
DOCKER_INSPECT="$WORK_DIR/docker.inspect"
mkdir -m 0700 "$SOURCE_SNAPSHOT" "$MODCACHE_SNAPSHOT"
touch "$LOG" "$SOURCE_BEFORE" "$SOURCE_AFTER" "$SOURCE_SNAPSHOT_BEFORE" "$SOURCE_SNAPSHOT_AFTER" \
  "$MODCACHE_HOST_INVENTORY" "$MODCACHE_SNAPSHOT_BEFORE" "$MODCACHE_SNAPSHOT_AFTER" \
  "$MODCACHE_EXPECTED_MANIFEST" \
  "$VOLUMES_BEFORE" "$MANIFEST_TMP" "$DOCKER_INSPECT"
chmod 0600 "$LOG" "$SOURCE_BEFORE" "$SOURCE_AFTER" "$SOURCE_SNAPSHOT_BEFORE" "$SOURCE_SNAPSHOT_AFTER" \
  "$MODCACHE_HOST_INVENTORY" "$MODCACHE_SNAPSHOT_BEFORE" "$MODCACHE_SNAPSHOT_AFTER" \
  "$MODCACHE_EXPECTED_MANIFEST" \
  "$VOLUMES_BEFORE" "$MANIFEST_TMP" "$DOCKER_INSPECT"

RESOURCES_CREATED=0
DOCKER_QUERIES_READY=0
BASELINE_QUERY_FAILED=0
DATABASE_CREATED=0
DATABASE_DROPPED=0
FIRST_ERROR_LINE=""
BLOCKED_INTERFACE=""
PG_VERSION_NUM="unknown"
DATABASE_OID="unknown"
SYSTEM_IDENTIFIER="unknown"
POSTGRES_IMAGE_ID="unknown"
GO_IMAGE_ID="unknown"
GO_VERSION="unknown"
GOOSE_VERSION="unknown"
GOOSE_BIN_SHA256="unknown"
GO_MODCACHE_MANIFEST_SHA256="unknown"
SOURCE_INVENTORY_SHA256="unknown"
GO_MODCACHE_SNAPSHOT_SHA256="unknown"

record_error() {
  local rc=$?
  if [[ -z "$FIRST_ERROR_LINE" ]]; then
    FIRST_ERROR_LINE="line=$1 rc=$rc command=$2"
    printf '%s\n' "$FIRST_ERROR_LINE" >>"$LOG"
  fi
  return "$rc"
}
trap 'record_error "$LINENO" "$BASH_COMMAND"' ERR

remove_owned_work_dir() {
  local target="$1" temp_root resolved live_id live_owner mount_targets mount_target
  command -v findmnt >/dev/null 2>&1 || return 1
  [[ -d "$target" && ! -L "$target" ]] || return 1
  temp_root="$(cd "${TMPDIR:-/tmp}" && pwd -P)" || return 1
  resolved="$(cd "$target" && pwd -P)" || return 1
  case "$resolved" in
    "$temp_root"/pandora-nodecfg-run.*) ;;
    *) return 1 ;;
  esac
  live_id="$(stat -c '%d:%i' "$resolved")" || return 1
  [[ "$live_id" == "$WORK_DIR_ID" ]] || return 1
  [[ -f "$WORK_OWNER_MARKER" && ! -L "$WORK_OWNER_MARKER" ]] || return 1
  live_owner="$(cat "$WORK_OWNER_MARKER")" || return 1
  [[ "$live_owner" == "$WORK_OWNER_TOKEN" ]] || return 1
  mount_targets="$(findmnt -rn -o TARGET)" || return 1
  while IFS= read -r mount_target; do
    [[ "$mount_target" != "$resolved" && "$mount_target" != "$resolved/"* ]] || return 1
  done <<<"$mount_targets"
  [[ "$(stat -c '%d:%i' "$resolved")" == "$WORK_DIR_ID" ]] || return 1
  [[ "$(cat "$WORK_OWNER_MARKER")" == "$WORK_OWNER_TOKEN" ]] || return 1
  rm -rf --one-file-system -- "$resolved" || return 1
  [[ ! -e "$resolved" ]]
}

write_source_inventory() {
  local source_root="$1" destination="$2" migration count=0 file file_list="${destination}.files0"
  local -a files=(
    "$source_root/deploy/test-node-config-legacy-pg18.sh"
    "$source_root/deploy/configure-app-role.sql"
    "$source_root/go.mod" "$source_root/go.sum"
  )
  for migration in "$source_root"/migrations/000{01..41}_*.sql; do
    [[ -f "$migration" ]]
    files+=("$migration")
    count=$((count + 1))
  done
  [[ "$count" -eq 41 ]]
  find "$source_root/internal" "$source_root/web" -type f -name '*.go' -print0 >"$file_list"
  while IFS= read -r -d '' file; do files+=("$file"); done <"$file_list"
  rm -f -- "$file_list"
  printf '%s\n' "${files[@]}" | LC_ALL=C sort -u | while IFS= read -r file; do
    [[ -f "$file" ]]
    printf '%s  %s\n' "$(sha256sum "$file" | awk '{print $1}')" "${file#"$source_root"/}"
  done >"$destination"
}

copy_source_snapshot() {
  local inventory="$1" destination="$2" line relative
  while IFS= read -r line; do
    relative="${line#*  }"
    [[ -n "$relative" && "$relative" != "$line" && "$relative" != /* && "$relative" != ../* && "$relative" != */../* ]]
    install -D -m 0644 "$ROOT/$relative" "$destination/$relative"
  done <"$inventory"
}

write_modcache_inventory() {
  local source_root="$1" destination="$2"
  if (cd "$source_root" && find . ! -type d ! -type f -print -quit | grep -q .); then
    echo 'Go module cache accepts only regular files and directories' >&2
    return 1
  fi
  (
    cd "$source_root"
    find . -type f -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum
  ) >"$destination"
}

cleanup() {
  local business_rc="$1" cleanup_rc=0 final_rc query_failed="$BASELINE_QUERY_FAILED"
  local containers='' networks='' volumes='' labelled_c='' labelled_n='' labelled_v=''
  local exact_c=0 exact_n=0 exact_v=0 labels_left=0 anonymous_new=-1 secret_scan=ok
  local source_block='' audit_sha=unavailable manifest_sha=unavailable
  local pg_label='' live_system='' live_database='' postgres_value='' app_value=''
  local audit_stage="${AUDIT_LOG_DEST}.tmp.${RUN_ID}" manifest_stage="${MANIFEST_DEST}.tmp.${RUN_ID}"
  trap - EXIT ERR INT TERM
  set +e

  if [[ "$DATABASE_CREATED" -eq 1 ]] && docker inspect -f '{{.State.Running}}' "$PG_CONTAINER" 2>/dev/null | grep -qx true; then
    pg_label="$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$PG_CONTAINER" 2>/dev/null)"
    live_system="$(pg_admin_db postgres -Atqc "SELECT (pg_control_system()).system_identifier" 2>/dev/null)"
    live_database="$(pg_admin_db postgres -Atqc "SELECT oid::text||'|'||coalesce(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname='$DB_NAME'" 2>/dev/null)"
    if [[ "$pg_label" == "$RUN_ID" && "$live_system" == "$SYSTEM_IDENTIFIER" &&
          "$live_database" == "$DATABASE_OID|pandora-nodecfg-disposable:$RUN_ID" ]]; then
      if pg_admin_db postgres -c "DROP DATABASE \"$DB_NAME\" WITH (FORCE)" >/dev/null 2>&1; then
        DATABASE_DROPPED=1
      fi
    else
      cleanup_rc=1
    fi
  fi
  if [[ "$RESOURCES_CREATED" -eq 1 ]]; then
    for container in "$GO_CONTAINER" "$PG_CONTAINER"; do
      if docker container inspect "$container" >/dev/null 2>&1; then
        if [[ "$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$container" 2>/dev/null)" == "$RUN_ID" ]]; then
          docker inspect "$container" >>"$DOCKER_INSPECT" 2>/dev/null || cleanup_rc=1
          docker rm -fv "$container" >/dev/null 2>&1 || cleanup_rc=1
        else
          cleanup_rc=1
        fi
      fi
    done
    if docker network inspect "$NETWORK" >/dev/null 2>&1; then
      if [[ "$(docker network inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$NETWORK" 2>/dev/null)" == "$RUN_ID" ]]; then
        docker network rm "$NETWORK" >/dev/null 2>&1 || cleanup_rc=1
      else
        cleanup_rc=1
      fi
    fi
    for volume in "$PG_VOLUME" "$CACHE_VOLUME"; do
      if docker volume inspect "$volume" >/dev/null 2>&1; then
        if [[ "$(docker volume inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$volume" 2>/dev/null)" == "$RUN_ID" ]]; then
          docker volume rm "$volume" >/dev/null 2>&1 || cleanup_rc=1
        else
          cleanup_rc=1
        fi
      fi
    done
  fi

  if [[ "$DOCKER_QUERIES_READY" -eq 1 ]]; then
    containers="$(docker ps -a --format '{{.Names}}' 2>/dev/null)" || query_failed=1
    networks="$(docker network ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    volumes="$(docker volume ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    labelled_c="$(docker ps -a --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Names}}' 2>/dev/null)" || query_failed=1
    labelled_n="$(docker network ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    labelled_v="$(docker volume ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
    exact_c="$(awk -v a="$PG_CONTAINER" -v b="$GO_CONTAINER" '$0==a||$0==b{n++} END{print n+0}' <<<"$containers")"
    exact_n="$(awk -v x="$NETWORK" '$0==x{n++} END{print n+0}' <<<"$networks")"
    exact_v="$(awk -v a="$PG_VOLUME" -v b="$CACHE_VOLUME" '$0==a||$0==b{n++} END{print n+0}' <<<"$volumes")"
    labels_left=$((
      $(awk 'NF{n++} END{print n+0}' <<<"$labelled_c") +
      $(awk 'NF{n++} END{print n+0}' <<<"$labelled_n") +
      $(awk 'NF{n++} END{print n+0}' <<<"$labelled_v")
    ))
    printf '%s\n' "$volumes" | awk 'NF' | LC_ALL=C sort >"$WORK_DIR/volumes.after" || query_failed=1
    if [[ "$query_failed" -eq 0 ]]; then
      anonymous_new="$(comm -13 "$VOLUMES_BEFORE" "$WORK_DIR/volumes.after" |
        awk 'length($0)==64 && $0~/^[0-9a-f]+$/{n++} END{print n+0}')"
    fi
  elif [[ "$RESOURCES_CREATED" -eq 1 ]]; then
    query_failed=1
  else
    anonymous_new=0
  fi

  [[ ! -f "$POSTGRES_SECRET" ]] || postgres_value="$(cat "$POSTGRES_SECRET")"
  [[ ! -f "$APP_SECRET" ]] || app_value="$(cat "$APP_SECRET")"
  if [[ -n "$postgres_value" ]] && grep -Fqs -- "$postgres_value" "$LOG" "$MANIFEST_TMP" "$DOCKER_INSPECT"; then
    secret_scan=failed
  fi
  if [[ -n "$app_value" ]] && grep -Fqs -- "$app_value" "$LOG" "$MANIFEST_TMP" "$DOCKER_INSPECT"; then
    secret_scan=failed
  fi
  [[ -z "$postgres_value" ]] || sed -i "s/${postgres_value}/[REDACTED_POSTGRES_PASSWORD]/g" "$LOG" "$MANIFEST_TMP" "$DOCKER_INSPECT" 2>/dev/null || cleanup_rc=1
  [[ -z "$app_value" ]] || sed -i "s/${app_value}/[REDACTED_APP_PASSWORD]/g" "$LOG" "$MANIFEST_TMP" "$DOCKER_INSPECT" 2>/dev/null || cleanup_rc=1
  if grep -RqsE "$DSN_SECRET_ERE" "$LOG" "$MANIFEST_TMP" "$DOCKER_INSPECT" 2>/dev/null; then
    secret_scan=failed
    sed -E -i 's#(postgres(ql)?://[^[:space:]:/@]+:)[^[:space:]@]+@#\1[REDACTED_PASSWORD]@#g' \
      "$LOG" "$MANIFEST_TMP" "$DOCKER_INSPECT" 2>/dev/null || cleanup_rc=1
  fi
  for artifact in "$LOG" "$MANIFEST_TMP" "$DOCKER_INSPECT"; do
    if grep -qsE "$PRIVATE_KEY_ERE" "$artifact" 2>/dev/null; then
      secret_scan=failed
      printf '%s\n' '[artifact content suppressed after private-key signature detection]' >"$artifact" || cleanup_rc=1
    fi
  done
  if [[ "$query_failed" -ne 0 || "$exact_c" -ne 0 || "$exact_n" -ne 0 ||
        "$exact_v" -ne 0 || "$labels_left" -ne 0 || "$anonymous_new" -ne 0 ||
        "$secret_scan" != ok || ( "$DATABASE_CREATED" -eq 1 && "$DATABASE_DROPPED" -ne 1 ) ]]; then
    cleanup_rc=1
  fi
  if { [[ -n "$postgres_value" && "$FIRST_ERROR_LINE" == *"$postgres_value"* ]] ||
       [[ -n "$app_value" && "$FIRST_ERROR_LINE" == *"$app_value"* ]] ||
       [[ "$FIRST_ERROR_LINE" =~ $DSN_SECRET_ERE ]] ||
       [[ "$FIRST_ERROR_LINE" =~ $PRIVATE_KEY_ERE ]]; }; then
    secret_scan=failed
    cleanup_rc=1
    FIRST_ERROR_LINE='error command suppressed after secret signature detection'
  fi

  source_block="$(cat "$SOURCE_BEFORE")" || cleanup_rc=1
  write_manifest_file() {
    local destination="$1"
    {
      printf 'schema=pandora-node-config-pg18-manifest-v1\nrun_id=%s\n' "$RUN_ID"
      printf 'database_name=%s\ndatabase_oid=%s\nsystem_identifier=%s\n' "$DB_NAME" "$DATABASE_OID" "$SYSTEM_IDENTIFIER"
      printf 'postgres_image=%s\npostgres_image_id=%s\npostgres_version_num=%s\n' "$POSTGRES_IMAGE" "$POSTGRES_IMAGE_ID" "$PG_VERSION_NUM"
      printf 'go_image=%s\ngo_image_id=%s\ngo_version=%s\n' "$GO_IMAGE" "$GO_IMAGE_ID" "$GO_VERSION"
      printf 'goose_version=%s\ngoose_bin_sha256=%s\n' "$GOOSE_VERSION" "$GOOSE_BIN_SHA256"
      printf 'go_modcache_manifest_sha256=%s\n' "$GO_MODCACHE_MANIFEST_SHA256"
      printf 'go_modcache_snapshot_sha256=%s\n' "$GO_MODCACHE_SNAPSHOT_SHA256"
      printf 'source_inventory_sha256=%s\naudit_log_sha256=%s\n' "$SOURCE_INVENTORY_SHA256" "$audit_sha"
      printf 'business_exit=%s\ncleanup_exit=%s\nfinal_exit=%s\n' "$business_rc" "$cleanup_rc" "$final_rc"
      printf 'database_created=%s\ndatabase_dropped=%s\n' "$DATABASE_CREATED" "$DATABASE_DROPPED"
      printf 'cleanup_query_failed=%s\nexact_containers=%s\nexact_networks=%s\nexact_volumes=%s\n' "$query_failed" "$exact_c" "$exact_n" "$exact_v"
      printf 'label_residual=%s\nanonymous_volume_new=%s\nsecret_scan=%s\n' "$labels_left" "$anonymous_new" "$secret_scan"
      [[ -z "$BLOCKED_INTERFACE" ]] || printf 'blocked_interface=%s\n' "$BLOCKED_INTERFACE"
      [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error=%s\n' "$FIRST_ERROR_LINE"
      printf '%s\n' '--- source_sha256 ---' "$source_block"
    } >"$destination"
    chmod 0600 "$destination"
  }
  publish_manifest() {
    rm -f -- "$manifest_stage" 2>/dev/null
    write_manifest_file "$manifest_stage" || { rm -f -- "$manifest_stage"; return 1; }
    if [[ -f "$MANIFEST_DEST" ]]; then
      mv -f -- "$manifest_stage" "$MANIFEST_DEST" || { rm -f -- "$manifest_stage"; return 1; }
    else
      mv -n -- "$manifest_stage" "$MANIFEST_DEST" || { rm -f -- "$manifest_stage"; return 1; }
      [[ ! -e "$manifest_stage" ]] || { rm -f -- "$manifest_stage"; return 1; }
    fi
  }

  install -m 0600 "$LOG" "$audit_stage" || cleanup_rc=1
  if [[ -f "$audit_stage" ]]; then
    mv -n -- "$audit_stage" "$AUDIT_LOG_DEST" || cleanup_rc=1
    [[ ! -e "$audit_stage" ]] || { rm -f -- "$audit_stage"; cleanup_rc=1; }
  fi
  if [[ -f "$AUDIT_LOG_DEST" ]]; then
    if [[ -n "$postgres_value" ]] && grep -Fqs -- "$postgres_value" "$AUDIT_LOG_DEST"; then
      secret_scan=failed
      cleanup_rc=1
      sed -i "s/${postgres_value}/[REDACTED_POSTGRES_PASSWORD]/g" "$AUDIT_LOG_DEST" || cleanup_rc=1
    fi
    if [[ -n "$app_value" ]] && grep -Fqs -- "$app_value" "$AUDIT_LOG_DEST"; then
      secret_scan=failed
      cleanup_rc=1
      sed -i "s/${app_value}/[REDACTED_APP_PASSWORD]/g" "$AUDIT_LOG_DEST" || cleanup_rc=1
    fi
    if grep -qsE "$DSN_SECRET_ERE" "$AUDIT_LOG_DEST"; then
      secret_scan=failed
      cleanup_rc=1
      sed -E -i 's#(postgres(ql)?://[^[:space:]:/@]+:)[^[:space:]@]+@#\1[REDACTED_PASSWORD]@#g' "$AUDIT_LOG_DEST" || cleanup_rc=1
    fi
    if grep -qsE "$PRIVATE_KEY_ERE" "$AUDIT_LOG_DEST"; then
      secret_scan=failed
      cleanup_rc=1
      printf '%s\n' '[audit content suppressed after private-key signature detection]' >"$AUDIT_LOG_DEST" || cleanup_rc=1
    fi
    audit_sha="$(sha256sum "$AUDIT_LOG_DEST" | awk '{print $1}')" || { audit_sha=unavailable; cleanup_rc=1; }
  fi

  remove_owned_work_dir "$WORK_DIR" || cleanup_rc=1
  if [[ "$business_rc" -ne 0 ]]; then
    final_rc="$business_rc"
  else
    # First publish is deliberately pessimistic. A failed final replacement
    # therefore cannot leave a success-looking manifest behind.
    final_rc=1
  fi
  publish_manifest || cleanup_rc=1

  if { [[ -n "$postgres_value" ]] && grep -Fqs -- "$postgres_value" "$AUDIT_LOG_DEST" "$MANIFEST_DEST" 2>/dev/null; } ||
     { [[ -n "$app_value" ]] && grep -Fqs -- "$app_value" "$AUDIT_LOG_DEST" "$MANIFEST_DEST" 2>/dev/null; } ||
     grep -RqsE "$DSN_SECRET_ERE|$PRIVATE_KEY_ERE" "$AUDIT_LOG_DEST" "$MANIFEST_DEST" 2>/dev/null; then
    secret_scan=failed
    cleanup_rc=1
    final_rc=1
    FIRST_ERROR_LINE='evidence artifact secret scan failed; sensitive content suppressed'
    printf '%s\n' '[audit content suppressed after final secret scan failure]' >"$AUDIT_LOG_DEST" || cleanup_rc=1
    audit_sha="$(sha256sum "$AUDIT_LOG_DEST" | awk '{print $1}')" || { audit_sha=unavailable; cleanup_rc=1; }
    source_block="$(sed -E 's#(postgres(ql)?://[^[:space:]:/@]+:)[^[:space:]@]+@#\1[REDACTED_PASSWORD]@#g' <<<"$source_block")"
  fi
  if [[ ! -f "$AUDIT_LOG_DEST" || ! -f "$MANIFEST_DEST" ]]; then
    cleanup_rc=1
  fi
  if [[ "$business_rc" -eq 0 && "$cleanup_rc" -eq 0 ]]; then
    final_rc=0
  elif [[ "$business_rc" -ne 0 ]]; then
    final_rc="$business_rc"
  else
    final_rc=1
  fi
  if ! publish_manifest; then
    cleanup_rc=1
    [[ "$business_rc" -ne 0 ]] && final_rc="$business_rc" || final_rc=1
    # Keep the already-published pessimistic manifest; if it is missing there
    # is no success artifact to misread.
  fi
  if [[ -f "$MANIFEST_DEST" ]] &&
     { ! grep -qx "business_exit=$business_rc" "$MANIFEST_DEST" 2>/dev/null ||
       ! grep -qx "cleanup_exit=$cleanup_rc" "$MANIFEST_DEST" 2>/dev/null ||
       ! grep -qx "final_exit=$final_rc" "$MANIFEST_DEST" 2>/dev/null ||
       ! grep -qx "secret_scan=$secret_scan" "$MANIFEST_DEST" 2>/dev/null; }; then
    cleanup_rc=1
    [[ "$business_rc" -ne 0 ]] && final_rc="$business_rc" || final_rc=1
    publish_manifest || rm -f -- "$MANIFEST_DEST"
  fi
  if { [[ -n "$postgres_value" ]] && grep -Fqs -- "$postgres_value" "$AUDIT_LOG_DEST" "$MANIFEST_DEST" 2>/dev/null; } ||
     { [[ -n "$app_value" ]] && grep -Fqs -- "$app_value" "$AUDIT_LOG_DEST" "$MANIFEST_DEST" 2>/dev/null; } ||
     grep -RqsE "$DSN_SECRET_ERE|$PRIVATE_KEY_ERE" "$AUDIT_LOG_DEST" "$MANIFEST_DEST" 2>/dev/null; then
    secret_scan=failed
    cleanup_rc=1
    final_rc=1
    FIRST_ERROR_LINE='final evidence verification failed; artifacts removed'
    rm -f -- "$AUDIT_LOG_DEST" "$MANIFEST_DEST" "$audit_stage" "$manifest_stage"
    audit_sha=unavailable
  fi
  unset postgres_value app_value
  if [[ -f "$MANIFEST_DEST" ]]; then
    manifest_sha="$(sha256sum "$MANIFEST_DEST" | awk '{print $1}')" || {
      manifest_sha=unavailable
      cleanup_rc=1
      [[ "$business_rc" -ne 0 ]] && final_rc="$business_rc" || final_rc=1
      rm -f -- "$MANIFEST_DEST"
    }
  fi

  printf 'business_exit=%s cleanup_exit=%s final_exit=%s\n' "$business_rc" "$cleanup_rc" "$final_rc" >&3
  printf 'database_dropped=%s cleanup_query_failed=%s exact_containers=%s exact_networks=%s exact_volumes=%s anonymous_volume_new=%s secret_scan=%s\n' \
    "$DATABASE_DROPPED" "$query_failed" "$exact_c" "$exact_n" "$exact_v" "$anonymous_new" "$secret_scan" >&3
  printf 'audit_log=%s audit_sha256=%s\nmanifest=%s manifest_sha256=%s\n' \
    "$AUDIT_LOG_DEST" "$audit_sha" "$MANIFEST_DEST" "$manifest_sha" >&3
  if [[ "$final_rc" -eq 0 ]]; then
    printf 'node_config_legacy_pg18_full_suite=ok\n' >&3
  fi
  exit "$final_rc"
}
trap 'cleanup $?' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

exec 3>&1 4>&2
exec >>"$LOG" 2>&1

for command in awk bash cat chmod cmp comm cp date dirname docker find findmnt grep install mkdir mktemp mv od pwd realpath rm sed seq sha256sum sleep sort stat touch tr xargs; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 1; }
done
if [[ -z "$GOOSE_BIN" || ! -f "$GOOSE_BIN" || ! -x "$GOOSE_BIN" || -L "$GOOSE_BIN" ||
      ! "$GOOSE_SHA256_EXPECTED" =~ ^[0-9A-Fa-f]{64}$ ]]; then
  BLOCKED_INTERFACE=PANDORA_TEST_GOOSE_BIN
  echo 'BLOCKED: regular non-symlink Goose executable and expected SHA-256 are required' >&2
  exit 78
fi
GOOSE_BIN="$(realpath -e "$GOOSE_BIN")"
GOOSE_SHA256_EXPECTED="$(printf '%s' "$GOOSE_SHA256_EXPECTED" | tr '[:upper:]' '[:lower:]')"
GOOSE_BIN_SHA256="$(sha256sum "$GOOSE_BIN" | awk '{print $1}')"
[[ "$GOOSE_BIN_SHA256" == "$GOOSE_SHA256_EXPECTED" ]]
if [[ -z "$GO_MODCACHE" || ! -d "$GO_MODCACHE" || -z "$GO_MODCACHE_MANIFEST" || ! -f "$GO_MODCACHE_MANIFEST" ]]; then
  BLOCKED_INTERFACE=PANDORA_TEST_GO_MODCACHE
  echo 'BLOCKED: read-only prewarmed Go module cache and manifest are required' >&2
  exit 78
fi
[[ ! -L "$GO_MODCACHE_MANIFEST" ]] || { echo 'Go module cache manifest symlinks are not accepted' >&2; exit 78; }
case "$GO_MODCACHE" in /*) ;; *) echo 'Go module cache path must be absolute' >&2; exit 78;; esac
case "$GO_MODCACHE_MANIFEST" in /*) ;; *) echo 'Go module cache manifest path must be absolute' >&2; exit 78;; esac
GO_MODCACHE_REAL="$(realpath -e "$GO_MODCACHE")"
GO_MODCACHE_MANIFEST_REAL="$(realpath -e "$GO_MODCACHE_MANIFEST")"
case "$GO_MODCACHE_MANIFEST_REAL" in
  "$GO_MODCACHE_REAL"/*) echo 'Go module cache manifest must be outside the cache tree' >&2; exit 78;;
esac
write_modcache_inventory "$GO_MODCACHE_REAL" "$MODCACHE_HOST_INVENTORY"
install -m 0600 "$GO_MODCACHE_MANIFEST_REAL" "$MODCACHE_EXPECTED_MANIFEST"
cmp -s "$MODCACHE_EXPECTED_MANIFEST" "$MODCACHE_HOST_INVENTORY"
GO_MODCACHE_MANIFEST_SHA256="$(sha256sum "$MODCACHE_EXPECTED_MANIFEST" | awk '{print $1}')"
cp -a -- "$GO_MODCACHE_REAL/." "$MODCACHE_SNAPSHOT/"
write_modcache_inventory "$MODCACHE_SNAPSHOT" "$MODCACHE_SNAPSHOT_BEFORE"
cmp -s "$MODCACHE_HOST_INVENTORY" "$MODCACHE_SNAPSHOT_BEFORE"
GO_MODCACHE_SNAPSHOT_SHA256="$(sha256sum "$MODCACHE_SNAPSHOT_BEFORE" | awk '{print $1}')"

write_source_inventory "$ROOT" "$SOURCE_BEFORE"
copy_source_snapshot "$SOURCE_BEFORE" "$SOURCE_SNAPSHOT"
write_source_inventory "$SOURCE_SNAPSHOT" "$SOURCE_SNAPSHOT_BEFORE"
cmp -s "$SOURCE_BEFORE" "$SOURCE_SNAPSHOT_BEFORE"
SOURCE_INVENTORY_SHA256="$(sha256sum "$SOURCE_SNAPSHOT_BEFORE" | awk '{print $1}')"
EXEC_ROOT="$SOURCE_SNAPSHOT"

GOOSE_VERSION="$("$GOOSE_BIN" -version | awk '{print $NF}')"
[[ "$GOOSE_VERSION" == "$GOOSE_VERSION_EXPECTED" ]]
"$GOOSE_BIN" -dir "$EXEC_ROOT/migrations" validate
grep -q '^func TestNodeConfigLegacyPG18' "$EXEC_ROOT/internal/api/admin/node_config_legacy_pg18_test.go"

printf 'Pg-%s-Aa1' "$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" >"$POSTGRES_SECRET"
printf 'App-%s-Aa1' "$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" >"$APP_SECRET"
chmod 0600 "$POSTGRES_SECRET" "$APP_SECRET"

docker info >/dev/null
docker volume ls --format '{{.Name}}' 2>/dev/null | LC_ALL=C sort >"$VOLUMES_BEFORE" || BASELINE_QUERY_FAILED=1
[[ "$BASELINE_QUERY_FAILED" -eq 0 ]]
DOCKER_QUERIES_READY=1

up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1;next} up' "$1"; }
pg_admin_db() {
  local database="$1"
  shift
  docker exec -i "$PG_CONTAINER" sh -ec '
    export PGPASSWORD="$(cat /run/pandora-secrets/postgres_password)"
    database="$1"
    shift
    exec psql -X -v ON_ERROR_STOP=1 -U postgres -d "$database" "$@"
  ' sh "$database" "$@"
}
pg_admin() { pg_admin_db "$DB_NAME" "$@"; }
configure_role() {
  docker exec "$PG_CONTAINER" sh -ec '
    export PGPASSWORD="$(cat /run/pandora-secrets/postgres_password)"
    export AEGIS_DB_APP_PASSWORD="$(cat /run/pandora-secrets/app_password)"
    exec psql -X -v ON_ERROR_STOP=1 -U postgres -d "$1" -f /src/deploy/configure-app-role.sql
  ' sh "$DB_NAME" >/dev/null
}
apply_guarded() {
  local number="$1" name="$2"
  {
    echo 'BEGIN;'
    echo "SET LOCAL app.idempotency_writers_stopped='yes';"
    echo "SET LOCAL app.allow_idempotency_schema${number}_up='yes';"
    up_sql "$EXEC_ROOT/migrations/$name"
    echo 'COMMIT;'
  } | pg_admin >/dev/null
}

docker network create --internal --label "$LABEL_KEY=$RUN_ID" "$NETWORK" >/dev/null
RESOURCES_CREATED=1
docker volume create --label "$LABEL_KEY=$RUN_ID" "$PG_VOLUME" >/dev/null
docker volume create --label "$LABEL_KEY=$RUN_ID" "$CACHE_VOLUME" >/dev/null
POSTGRES_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")"
docker create --pull=never --name "$PG_CONTAINER" --network "$NETWORK" --network-alias pg18 \
  --label "$LABEL_KEY=$RUN_ID" --cpus 0.75 \
  -e POSTGRES_PASSWORD_FILE=/run/pandora-secrets/postgres_password \
  -e POSTGRES_DB="$DB_NAME" \
  --mount "type=volume,src=$PG_VOLUME,dst=/var/lib/postgresql" \
  --mount "type=bind,src=$EXEC_ROOT,dst=/src,readonly" \
  --mount "type=bind,src=$SECRET_DIR,dst=/run/pandora-secrets,readonly" \
  "$POSTGRES_IMAGE_ID" >/dev/null
[[ "$(docker inspect -f '{{.Image}}' "$PG_CONTAINER")" == "$POSTGRES_IMAGE_ID" ]]
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER")" == 'null' ]]
docker start "$PG_CONTAINER" >/dev/null
for _ in $(seq 1 90); do
  docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null
DATABASE_CREATED=1
PG_VERSION_NUM="$(pg_admin -Atqc 'SHOW server_version_num')"
[[ "$PG_VERSION_NUM" =~ ^18[0-9]{4}$ ]]

count=0
for migration in "$EXEC_ROOT"/migrations/000{01..36}_*.sql; do
  { echo 'BEGIN;'; up_sql "$migration"; echo 'COMMIT;'; } | pg_admin >/dev/null
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
for migration in "$EXEC_ROOT"/migrations/000{40..41}_*.sql; do
  { echo 'BEGIN;'; up_sql "$migration"; echo 'COMMIT;'; } | pg_admin >/dev/null
  configure_role
done

pg_admin -c "COMMENT ON DATABASE \"$DB_NAME\" IS 'pandora-nodecfg-disposable:$RUN_ID'" >/dev/null
DATABASE_OID="$(pg_admin -Atqc "SELECT oid FROM pg_database WHERE datname=current_database()")"
SYSTEM_IDENTIFIER="$(pg_admin -Atqc "SELECT (pg_control_system()).system_identifier")"
[[ "$DATABASE_OID" =~ ^[0-9]+$ && "$SYSTEM_IDENTIFIER" =~ ^[0-9]+$ ]]
[[ "$(pg_admin -Atqc 'SELECT count(*) FROM tenants')" == 0 ]]

GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")"
docker create --pull=never --name "$GO_CONTAINER" --network "$NETWORK" \
  --label "$LABEL_KEY=$RUN_ID" --cpus 1.00 -w /src \
  --mount "type=bind,src=$EXEC_ROOT,dst=/src,readonly" \
  --mount "type=bind,src=$SECRET_DIR,dst=/run/pandora-secrets,readonly" \
  --mount "type=bind,src=$MODCACHE_SNAPSHOT,dst=/gomodcache,readonly" \
  --mount "type=volume,src=$CACHE_VOLUME,dst=/root/.cache/go-build" \
  -e GOMODCACHE=/gomodcache -e GOPROXY=off -e GOSUMDB=off \
  -e AEGIS_NODE_CONFIG_PG18_FIXTURE=disposable-v1 \
  -e AEGIS_NODE_CONFIG_PG18_DATABASE="$DB_NAME" \
  -e AEGIS_NODE_CONFIG_PG18_DATABASE_OID="$DATABASE_OID" \
  -e AEGIS_NODE_CONFIG_PG18_SYSTEM_ID="$SYSTEM_IDENTIFIER" \
  -e AEGIS_NODE_CONFIG_PG18_RUN_ID="$RUN_ID" \
  "$GO_IMAGE_ID" sh -ec '
    app_password="$(cat /run/pandora-secrets/app_password)"
    admin_password="$(cat /run/pandora-secrets/postgres_password)"
    export AEGIS_NODE_CONFIG_PG18_DSN="postgres://aegis_app:${app_password}@pg18:5432/${AEGIS_NODE_CONFIG_PG18_DATABASE}?sslmode=disable"
    export AEGIS_NODE_CONFIG_PG18_ADMIN_DSN="postgres://postgres:${admin_password}@pg18:5432/${AEGIS_NODE_CONFIG_PG18_DATABASE}?sslmode=disable"
    unset app_password admin_password
    case "$(go env GOVERSION)" in go1.26.*) ;; *) exit 78;; esac
    go version
    go test -mod=readonly -buildvcs=false -run "^$" ./internal/api/admin
    go test -mod=readonly -buildvcs=false -v -count=1 -timeout=1800s -run "^TestNodeConfig(PG18LockSchedule|LegacyPG18)$" ./internal/api/admin
  ' >/dev/null
[[ "$(docker inspect -f '{{.Image}}' "$GO_CONTAINER")" == "$GO_IMAGE_ID" ]]
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$GO_CONTAINER")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$GO_CONTAINER")" == 'null' ]]
docker start -a "$GO_CONTAINER"

GO_VERSION="$(grep -Em1 '^go version go1\.26\.' "$LOG" | awk '{print $3}')"
[[ "$GO_VERSION" == go1.26.* ]]
for marker in \
  node_config_pg18_runtime_role_rls_ok \
  node_config_pg18_new_node_materialization_ok \
  node_config_pg18_report_unique_ok \
  node_config_pg18_report_fail_closed_ok \
  node_config_pg18_pool_delete_config_refusal_ok \
  node_config_pg18_publish_concurrency_ok \
  node_config_pg18_projection_consistency_ok \
  node_config_pg18_tenant_publish_isolation_ok \
  node_config_pg18_rls_cross_tenant_ok \
  node_config_pg18_rls_connection_reuse_ok \
  node_config_pg18_report_wrong_scope_ok \
  node_config_pg18_report_superseded_ok \
  node_config_pg18_report_pool_move_ok \
  node_config_pg18_report_legacy_repeat_append_only_ok \
  node_config_pg18_expired_layer_refusal_ok \
  node_config_pg18_expiry_lock_wait_refusal_ok \
  node_config_pg18_duplicate_layer_refusal_ok \
  node_config_pg18_malformed_global_refusal_ok \
  node_config_pg18_publish_advisory_cancel_rollback_ok \
  node_config_pg18_publish_desired_wait_cancel_rollback_ok \
  node_config_pg18_report_wait_cancel_rollback_ok \
  node_config_pg18_life_publish_then_retire_ok \
  node_config_pg18_life_retire_then_publish_ok \
  node_config_pg18_bootstrap_token_name_binding_ok \
  node_config_pg18_bootstrap_legacy_token_refusal_ok \
  node_config_pg18_bootstrap_name_canonical_ok \
  node_config_pg18_bootstrap_disabled_pool_issue_refusal_ok \
  node_config_pg18_bootstrap_disabled_pool_use_refusal_ok \
  node_config_pg18_bootstrap_existing_pool_binding_ok \
  node_config_pg18_bootstrap_terminal_refusal_ok \
  node_config_pg18_terminal_identity_refusal_ok \
  node_config_pg18_life_pool_disable_commit_refusal_ok \
  node_config_pg18_life_pool_disable_rollback_publish_ok \
  node_config_pg18_life_global_publish_then_retire_ok \
  node_config_pg18_life_global_retire_then_publish_ok \
  node_config_pg18_life_pool_publish_then_retire_ok \
  node_config_pg18_life_pool_retire_then_publish_ok \
  node_config_pg18_del01_empty_delete_publish_refusal_ok \
  node_config_pg18_del03_delete_then_publish_ok \
  node_config_pg18_del03_publish_then_delete_ok \
  node_config_pg18_del04_create_delete_serialization_ok \
  node_config_pg18_del04_clone_delete_serialization_ok \
  node_config_pg18_new02_create_publish_serialization_ok \
  node_config_pg18_new03_clone_publish_serialization_ok \
  node_config_pg18_new04_new_bootstrap_publish_serialization_ok \
  node_config_pg18_new04_existing_bootstrap_publish_serialization_ok \
  node_config_pg18_new04_new_bootstrap_cancel_rollback_ok \
  node_config_pg18_new04_existing_bootstrap_cancel_rollback_ok \
  node_config_pg18_lock01_stress_504_ok \
  node_config_pg18_version_limit_ok; do
  [[ "$(grep -Ec "(^|[[:space:]])marker=${marker}([[:space:]]|$)" "$LOG" || true)" -eq 1 ]]
done
grep -Eq '^--- PASS: TestNodeConfigLegacyPG18 ' "$LOG"
grep -Fq 'node_config_legacy_pg18_first_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_second_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_third_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_fourth_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_fifth_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_sixth_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_seventh_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_eighth_batch=ok' "$LOG"
grep -Fq 'node_config_legacy_pg18_ninth_batch=ok' "$LOG"
write_source_inventory "$ROOT" "$SOURCE_AFTER"
cmp -s "$SOURCE_BEFORE" "$SOURCE_AFTER"
write_source_inventory "$SOURCE_SNAPSHOT" "$SOURCE_SNAPSHOT_AFTER"
cmp -s "$SOURCE_SNAPSHOT_BEFORE" "$SOURCE_SNAPSHOT_AFTER"
write_modcache_inventory "$MODCACHE_SNAPSHOT" "$MODCACHE_SNAPSHOT_AFTER"
cmp -s "$MODCACHE_SNAPSHOT_BEFORE" "$MODCACHE_SNAPSHOT_AFTER"
echo 'node_config_legacy_pg18_business=ok'
