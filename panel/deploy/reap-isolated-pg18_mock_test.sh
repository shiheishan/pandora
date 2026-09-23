#!/usr/bin/env bash
set -euo pipefail

SELF="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
MOCK_CID='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
MOCK_NID='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
MOCK_BAD_ID='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
MOCK_RUN_ID='pandoraisolatedpg18ABC123-1234'
MOCK_NAME="pandora-pg18-preflight-${MOCK_RUN_ID}"

mock_date() {
  [[ "${1:-}" == '-u' && "${2:-}" == '+%s' ]] || return 92
  printf '%s\n' "${MOCK_NOW:-2000}"
}

mock_docker() {
  [[ "${1:-}" == '--host' && "${2:-}" == 'unix:///var/run/docker.sock' ]] || return 91
  shift 2
  printf '%s\n' "$*" >>"$MOCK_LOG"

  local container_name="$MOCK_NAME"
  local kind='isolated-pg18-v1'
  local container_expiry='1000'
  local network_expiry='1000'
  case "$MOCK_SCENARIO" in
    future)
      container_expiry='3000'
      network_expiry='3000'
      ;;
    fuzzy)
      container_name='attacker-object'
      kind='isolated-pg18-v1-evil'
      ;;
    ownership_mismatch)
      network_expiry='1001'
      ;;
  esac

  if [[ "${1:-}" == 'ps' && "${2:-}" == '-a' ]]; then
    [[ -e "$MOCK_STATE/container_alive" ]] && printf '%s\t%s\n' "$MOCK_CID" "$container_name"
    return 0
  fi
  if [[ "${1:-}" == 'ps' && "${2:-}" == '-aq' ]]; then
    [[ -e "$MOCK_STATE/container_alive" ]] && printf '%s\n' "$MOCK_CID"
    return 0
  fi
  if [[ "${1:-}" == 'network' && "${2:-}" == 'ls' && "${3:-}" == '--no-trunc' ]]; then
    [[ -e "$MOCK_STATE/network_alive" ]] && printf '%s\t%s\n' "$MOCK_NID" "$MOCK_NAME"
    return 0
  fi
  if [[ "${1:-}" == 'network' && "${2:-}" == 'ls' && "${3:-}" == '-q' ]]; then
    [[ -e "$MOCK_STATE/network_alive" ]] && printf '%s\n' "$MOCK_NID"
    return 0
  fi
  if [[ "${1:-}" == 'inspect' && "${2:-}" == '--type' && "${3:-}" == 'container' ]]; then
    [[ -e "$MOCK_STATE/container_alive" ]] || return 1
    if [[ "${4:-}" == '--format' ]]; then
      local inspected_id="$MOCK_CID"
      [[ "$MOCK_SCENARIO" == 'id_mismatch' ]] && inspected_id="$MOCK_BAD_ID"
      printf '%s|/%s|%s|%s|%s|true\n' \
        "$inspected_id" "$container_name" "$kind" "$MOCK_RUN_ID" "$container_expiry"
    fi
    return 0
  fi
  if [[ "${1:-}" == 'network' && "${2:-}" == 'inspect' ]]; then
    [[ -e "$MOCK_STATE/network_alive" ]] || return 1
    if [[ "${3:-}" == '--format' ]]; then
      printf '%s|%s|isolated-pg18-v1|%s|%s\n' \
        "$MOCK_NID" "$MOCK_NAME" "$MOCK_RUN_ID" "$network_expiry"
    fi
    return 0
  fi
  if [[ "${1:-}" == 'rm' && "${2:-}" == '-f' && "${3:-}" == "$MOCK_CID" ]]; then
    if [[ -e "$MOCK_STATE/fail_container_delete_once" ]]; then
      rm -f -- "$MOCK_STATE/fail_container_delete_once"
      printf 'rm_container_failed_once\n' >>"$MOCK_LOG"
      return 1
    fi
    rm -f -- "$MOCK_STATE/container_alive"
    printf 'rm_container\n' >>"$MOCK_LOG"
    return 0
  fi
  if [[ "${1:-}" == 'network' && "${2:-}" == 'rm' && "${3:-}" == "$MOCK_NID" ]]; then
    rm -f -- "$MOCK_STATE/network_alive"
    printf 'rm_network\n' >>"$MOCK_LOG"
    return 0
  fi

  printf 'unexpected_mock_docker_call=%s\n' "$*" >&2
  return 93
}

case "$(basename "$0")" in
  docker)
    mock_docker "$@"
    exit $?
    ;;
  date)
    mock_date "$@"
    exit $?
    ;;
esac

REAPER="$(cd "$(dirname "$0")" && pwd)/reap-isolated-pg18.sh"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pg18-reaper-test.XXXXXX")"
cleanup() {
  rm -rf -- "$TEST_DIR"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$TEST_DIR/bin"
ln -s "$SELF" "$TEST_DIR/bin/docker"
ln -s "$SELF" "$TEST_DIR/bin/date"

fail() {
  printf 'reap_isolated_pg18_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  grep -Fq -- "$needle" <<<"$haystack" || fail "missing_output:${needle}"
}

assert_no_delete() {
  local log_file="$1"
  if grep -Eq '^rm_(container|network)$' "$log_file"; then
    fail "unexpected_delete:$(basename "$(dirname "$log_file")")"
  fi
}

prepare_case() {
  local case_name="$1"
  CASE_DIR="$TEST_DIR/$case_name"
  mkdir -p "$CASE_DIR/state"
  : >"$CASE_DIR/state/container_alive"
  : >"$CASE_DIR/state/network_alive"
  : >"$CASE_DIR/docker.log"
}

run_reaper() {
  local scenario="$1"
  shift
  set +e
  CASE_OUTPUT="$(
    env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
      PATH="$TEST_DIR/bin:$PATH" \
      MOCK_STATE="$CASE_DIR/state" MOCK_LOG="$CASE_DIR/docker.log" \
      MOCK_SCENARIO="$scenario" MOCK_NOW=2000 \
      "$@" bash "$REAPER" 2>&1
  )"
  CASE_STATUS=$?
  set -e
}

prepare_case sigkill_residue
run_reaper happy
[[ "$CASE_STATUS" -eq 0 ]] || fail "sigkill_status:${CASE_STATUS}:${CASE_OUTPUT}"
[[ ! -e "$CASE_DIR/state/container_alive" && ! -e "$CASE_DIR/state/network_alive" ]] \
  || fail 'sigkill_resources_remain'
assert_contains "$CASE_OUTPUT" "isolated_pg18_reaper_reaped=container:${MOCK_CID}:${MOCK_RUN_ID}:1000"
assert_contains "$CASE_OUTPUT" "isolated_pg18_reaper_reaped=network:${MOCK_NID}:${MOCK_RUN_ID}:1000"
assert_contains "$CASE_OUTPUT" 'isolated_pg18_reaper=PASS reaped=2 preserved=0'

prepare_case future_lease
run_reaper future
[[ "$CASE_STATUS" -eq 0 ]] || fail "future_status:${CASE_STATUS}:${CASE_OUTPUT}"
[[ -e "$CASE_DIR/state/container_alive" && -e "$CASE_DIR/state/network_alive" ]] \
  || fail 'future_resource_deleted'
assert_no_delete "$CASE_DIR/docker.log"
assert_contains "$CASE_OUTPUT" 'isolated_pg18_reaper=PASS reaped=0 preserved=2'

prepare_case fuzzy_label
run_reaper fuzzy
[[ "$CASE_STATUS" -eq 78 ]] || fail "fuzzy_status:${CASE_STATUS}:${CASE_OUTPUT}"
assert_no_delete "$CASE_DIR/docker.log"
assert_contains "$CASE_OUTPUT" "isolated_pg18_reaper=DENY reason=container_kind_label_invalid:${MOCK_CID}"

prepare_case same_name_different_id
run_reaper id_mismatch
[[ "$CASE_STATUS" -eq 78 ]] || fail "id_mismatch_status:${CASE_STATUS}:${CASE_OUTPUT}"
assert_no_delete "$CASE_DIR/docker.log"
assert_contains "$CASE_OUTPUT" "isolated_pg18_reaper=DENY reason=container_id_mismatch:${MOCK_CID}"

prepare_case ownership_mismatch
run_reaper ownership_mismatch
[[ "$CASE_STATUS" -eq 78 ]] || fail "ownership_status:${CASE_STATUS}:${CASE_OUTPUT}"
assert_no_delete "$CASE_DIR/docker.log"
assert_contains "$CASE_OUTPUT" "isolated_pg18_reaper=DENY reason=lease_ownership_mismatch:${MOCK_RUN_ID}"

prepare_case delete_retry
: >"$CASE_DIR/state/fail_container_delete_once"
run_reaper retry
[[ "$CASE_STATUS" -eq 78 ]] || fail "retry_first_status:${CASE_STATUS}:${CASE_OUTPUT}"
[[ -e "$CASE_DIR/state/container_alive" && -e "$CASE_DIR/state/network_alive" ]] \
  || fail 'retry_first_run_changed_resources'
assert_contains "$CASE_OUTPUT" "isolated_pg18_reaper=DENY reason=container_delete_failed:${MOCK_CID}"
run_reaper retry
[[ "$CASE_STATUS" -eq 0 ]] || fail "retry_second_status:${CASE_STATUS}:${CASE_OUTPUT}"
[[ ! -e "$CASE_DIR/state/container_alive" && ! -e "$CASE_DIR/state/network_alive" ]] \
  || fail 'retry_second_run_resources_remain'
assert_contains "$CASE_OUTPUT" 'isolated_pg18_reaper=PASS reaped=2 preserved=0'

prepare_case remote_environment
run_reaper happy env DOCKER_HOST=tcp://attacker.invalid:2375
[[ "$CASE_STATUS" -eq 78 ]] || fail "remote_env_status:${CASE_STATUS}:${CASE_OUTPUT}"
[[ ! -s "$CASE_DIR/docker.log" ]] || fail 'remote_env_contacted_docker'
assert_contains "$CASE_OUTPUT" 'isolated_pg18_reaper=DENY reason=remote_docker_environment:DOCKER_HOST'

printf 'reap_isolated_pg18_mock=PASS cases=7\n'
