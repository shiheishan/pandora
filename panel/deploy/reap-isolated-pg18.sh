#!/usr/bin/env bash
set -euo pipefail

readonly EXIT_DENIED=78
readonly RESOURCE_PREFIX='pandora-pg18-preflight-'
readonly LABEL_KIND='pandora.preflight'
readonly LABEL_RUN_ID='pandora.preflight.run_id'
readonly LABEL_EXPIRES_AT='pandora.preflight.expires_at'
readonly EXPECTED_KIND='isolated-pg18-v1'
readonly LOCAL_DOCKER_HOST='unix:///var/run/docker.sock'

deny() {
  printf 'isolated_pg18_reaper=DENY reason=%s\n' "$1" >&2
  exit "$EXIT_DENIED"
}

for docker_env_name in DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH; do
  if [[ -n "${!docker_env_name:-}" ]]; then
    deny "remote_docker_environment:${docker_env_name}"
  fi
done

DOCKER_BIN="$(command -v docker 2>/dev/null)" || deny 'docker_not_found'
[[ -n "$DOCKER_BIN" ]] || deny 'docker_not_found'

docker_local() {
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    "$DOCKER_BIN" --host "$LOCAL_DOCKER_HOST" "$@"
}

NOW_EPOCH="$(date -u +%s 2>/dev/null)" || deny 'clock_unavailable'
[[ "$NOW_EPOCH" =~ ^[1-9][0-9]*$ ]] || deny 'clock_invalid'

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pg18-reaper.XXXXXX")" || deny 'temporary_directory_failed'
cleanup() {
  rm -rf -- "$WORK_DIR"
}
trap cleanup EXIT HUP INT TERM

declare -A CONTAINER_CANDIDATES=()
declare -A NETWORK_CANDIDATES=()
declare -A CONTAINER_FINGERPRINT=()
declare -A NETWORK_FINGERPRINT=()
declare -A CONTAINER_RUN=()
declare -A NETWORK_RUN=()
declare -A CONTAINER_EXPIRES=()
declare -A NETWORK_EXPIRES=()
declare -A CONTAINER_BY_RUN=()
declare -A NETWORK_BY_RUN=()
declare -a EXPIRED_CONTAINERS=()
declare -a EXPIRED_NETWORKS=()

docker_local ps -a --no-trunc --format '{{.ID}}\t{{.Names}}' >"$WORK_DIR/containers.all" \
  || deny 'container_discovery_failed'
docker_local ps -aq --no-trunc --filter "label=${LABEL_KIND}" >"$WORK_DIR/containers.labeled" \
  || deny 'container_label_discovery_failed'
docker_local network ls --no-trunc --format '{{.ID}}\t{{.Name}}' >"$WORK_DIR/networks.all" \
  || deny 'network_discovery_failed'
docker_local network ls -q --no-trunc --filter "label=${LABEL_KIND}" >"$WORK_DIR/networks.labeled" \
  || deny 'network_label_discovery_failed'

while IFS=$'\t' read -r resource_id resource_name; do
  [[ -n "$resource_id" ]] || continue
  if [[ "$resource_name" == "${RESOURCE_PREFIX}"* ]]; then
    CONTAINER_CANDIDATES["$resource_id"]=1
  fi
done <"$WORK_DIR/containers.all"
while IFS= read -r resource_id; do
  [[ -n "$resource_id" ]] && CONTAINER_CANDIDATES["$resource_id"]=1
done <"$WORK_DIR/containers.labeled"

while IFS=$'\t' read -r resource_id resource_name; do
  [[ -n "$resource_id" ]] || continue
  if [[ "$resource_name" == "${RESOURCE_PREFIX}"* ]]; then
    NETWORK_CANDIDATES["$resource_id"]=1
  fi
done <"$WORK_DIR/networks.all"
while IFS= read -r resource_id; do
  [[ -n "$resource_id" ]] && NETWORK_CANDIDATES["$resource_id"]=1
done <"$WORK_DIR/networks.labeled"

valid_full_id() {
  [[ "$1" =~ ^[0-9a-f]{64}$ ]]
}

valid_run_id() {
  [[ "$1" =~ ^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]*$ ]]
}

inspect_container() {
  docker_local inspect --type container \
    --format '{{.Id}}|{{.Name}}|{{index .Config.Labels "pandora.preflight"}}|{{index .Config.Labels "pandora.preflight.run_id"}}|{{index .Config.Labels "pandora.preflight.expires_at"}}|{{.State.Running}}' \
    "$1"
}

inspect_network() {
  docker_local network inspect \
    --format '{{.Id}}|{{.Name}}|{{index .Labels "pandora.preflight"}}|{{index .Labels "pandora.preflight.run_id"}}|{{index .Labels "pandora.preflight.expires_at"}}' \
    "$1"
}

# Phase one validates every candidate before a single destructive Docker call.
for requested_id in "${!CONTAINER_CANDIDATES[@]}"; do
  valid_full_id "$requested_id" || deny "container_list_id_invalid:${requested_id}"
  fingerprint="$(inspect_container "$requested_id" 2>/dev/null)" \
    || deny "container_inspect_failed:${requested_id}"
  IFS='|' read -r actual_id resource_name kind run_id expires_at running extra <<<"$fingerprint"
  [[ -z "${extra:-}" ]] || deny "container_fingerprint_invalid:${requested_id}"
  [[ "$actual_id" == "$requested_id" ]] || deny "container_id_mismatch:${requested_id}"
  valid_full_id "$actual_id" || deny "container_inspected_id_invalid:${requested_id}"
  resource_name="${resource_name#/}"
  [[ "$kind" == "$EXPECTED_KIND" ]] || deny "container_kind_label_invalid:${requested_id}"
  valid_run_id "$run_id" || deny "container_run_id_invalid:${requested_id}"
  [[ "$resource_name" == "${RESOURCE_PREFIX}${run_id}" ]] \
    || deny "container_name_ownership_invalid:${requested_id}"
  [[ "$expires_at" =~ ^[1-9][0-9]*$ ]] || deny "container_lease_invalid:${requested_id}"
  [[ "$running" == 'true' || "$running" == 'false' ]] \
    || deny "container_running_state_invalid:${requested_id}"
  [[ -z "${CONTAINER_BY_RUN[$run_id]:-}" ]] || deny "container_duplicate_run_id:${run_id}"

  normalized="${actual_id}|${resource_name}|${kind}|${run_id}|${expires_at}|${running}"
  CONTAINER_FINGERPRINT["$requested_id"]="$normalized"
  CONTAINER_RUN["$requested_id"]="$run_id"
  CONTAINER_EXPIRES["$requested_id"]="$expires_at"
  CONTAINER_BY_RUN["$run_id"]="$requested_id"
done

for requested_id in "${!NETWORK_CANDIDATES[@]}"; do
  valid_full_id "$requested_id" || deny "network_list_id_invalid:${requested_id}"
  fingerprint="$(inspect_network "$requested_id" 2>/dev/null)" \
    || deny "network_inspect_failed:${requested_id}"
  IFS='|' read -r actual_id resource_name kind run_id expires_at extra <<<"$fingerprint"
  [[ -z "${extra:-}" ]] || deny "network_fingerprint_invalid:${requested_id}"
  [[ "$actual_id" == "$requested_id" ]] || deny "network_id_mismatch:${requested_id}"
  valid_full_id "$actual_id" || deny "network_inspected_id_invalid:${requested_id}"
  [[ "$kind" == "$EXPECTED_KIND" ]] || deny "network_kind_label_invalid:${requested_id}"
  valid_run_id "$run_id" || deny "network_run_id_invalid:${requested_id}"
  [[ "$resource_name" == "${RESOURCE_PREFIX}${run_id}" ]] \
    || deny "network_name_ownership_invalid:${requested_id}"
  [[ "$expires_at" =~ ^[1-9][0-9]*$ ]] || deny "network_lease_invalid:${requested_id}"
  [[ -z "${NETWORK_BY_RUN[$run_id]:-}" ]] || deny "network_duplicate_run_id:${run_id}"

  normalized="${actual_id}|${resource_name}|${kind}|${run_id}|${expires_at}"
  NETWORK_FINGERPRINT["$requested_id"]="$normalized"
  NETWORK_RUN["$requested_id"]="$run_id"
  NETWORK_EXPIRES["$requested_id"]="$expires_at"
  NETWORK_BY_RUN["$run_id"]="$requested_id"
done

# When both halves of a lease are present they must attest to the same expiry.
for run_id in "${!CONTAINER_BY_RUN[@]}"; do
  container_id="${CONTAINER_BY_RUN[$run_id]}"
  network_id="${NETWORK_BY_RUN[$run_id]:-}"
  if [[ -n "$network_id" && "${CONTAINER_EXPIRES[$container_id]}" != "${NETWORK_EXPIRES[$network_id]}" ]]; then
    deny "lease_ownership_mismatch:${run_id}"
  fi
done

preserved=0
for requested_id in "${!CONTAINER_CANDIDATES[@]}"; do
  expires_at="${CONTAINER_EXPIRES[$requested_id]}"
  run_id="${CONTAINER_RUN[$requested_id]}"
  if (( expires_at <= NOW_EPOCH )); then
    EXPIRED_CONTAINERS+=("$requested_id")
  else
    printf 'isolated_pg18_reaper_preserved=container:%s:%s:%s\n' "$requested_id" "$run_id" "$expires_at"
    ((preserved += 1))
  fi
done
for requested_id in "${!NETWORK_CANDIDATES[@]}"; do
  expires_at="${NETWORK_EXPIRES[$requested_id]}"
  run_id="${NETWORK_RUN[$requested_id]}"
  if (( expires_at <= NOW_EPOCH )); then
    EXPIRED_NETWORKS+=("$requested_id")
  else
    printf 'isolated_pg18_reaper_preserved=network:%s:%s:%s\n' "$requested_id" "$run_id" "$expires_at"
    ((preserved += 1))
  fi
done

reaped=0
for requested_id in "${EXPIRED_CONTAINERS[@]}"; do
  current_now="$(date -u +%s 2>/dev/null)" || deny 'clock_recheck_unavailable'
  [[ "$current_now" =~ ^[1-9][0-9]*$ ]] || deny 'clock_recheck_invalid'
  (( CONTAINER_EXPIRES[$requested_id] <= current_now )) \
    || deny "container_lease_no_longer_expired:${requested_id}"
  current="$(inspect_container "$requested_id" 2>/dev/null)" \
    || deny "container_reinspect_failed:${requested_id}"
  IFS='|' read -r actual_id resource_name kind run_id expires_at running extra <<<"$current"
  resource_name="${resource_name#/}"
  current="${actual_id}|${resource_name}|${kind}|${run_id}|${expires_at}|${running}"
  [[ -z "${extra:-}" && "$current" == "${CONTAINER_FINGERPRINT[$requested_id]}" ]] \
    || deny "container_changed_before_delete:${requested_id}"
  docker_local rm -f "$requested_id" >/dev/null \
    || deny "container_delete_failed:${requested_id}"
  if docker_local inspect --type container "$requested_id" >/dev/null 2>&1; then
    deny "container_delete_not_confirmed:${requested_id}"
  fi
  printf 'isolated_pg18_reaper_reaped=container:%s:%s:%s\n' \
    "$requested_id" "${CONTAINER_RUN[$requested_id]}" "${CONTAINER_EXPIRES[$requested_id]}"
  ((reaped += 1))
done

for requested_id in "${EXPIRED_NETWORKS[@]}"; do
  current_now="$(date -u +%s 2>/dev/null)" || deny 'clock_recheck_unavailable'
  [[ "$current_now" =~ ^[1-9][0-9]*$ ]] || deny 'clock_recheck_invalid'
  (( NETWORK_EXPIRES[$requested_id] <= current_now )) \
    || deny "network_lease_no_longer_expired:${requested_id}"
  current="$(inspect_network "$requested_id" 2>/dev/null)" \
    || deny "network_reinspect_failed:${requested_id}"
  [[ "$current" == "${NETWORK_FINGERPRINT[$requested_id]}" ]] \
    || deny "network_changed_before_delete:${requested_id}"
  docker_local network rm "$requested_id" >/dev/null \
    || deny "network_delete_failed:${requested_id}"
  if docker_local network inspect "$requested_id" >/dev/null 2>&1; then
    deny "network_delete_not_confirmed:${requested_id}"
  fi
  printf 'isolated_pg18_reaper_reaped=network:%s:%s:%s\n' \
    "$requested_id" "${NETWORK_RUN[$requested_id]}" "${NETWORK_EXPIRES[$requested_id]}"
  ((reaped += 1))
done

printf 'isolated_pg18_reaper=PASS reaped=%d preserved=%d\n' "$reaped" "$preserved"
