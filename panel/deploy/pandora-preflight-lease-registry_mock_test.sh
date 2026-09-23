#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REGISTRY="$SCRIPT_DIR/pandora-preflight-lease-registry.sh"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-lease-registry-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT HUP INT TERM

RUN_ID='pandoraisolatedpg18ABC123-4242'
SOURCE_ID='sha256:1111111111111111111111111111111111111111111111111111111111111111'
CONTAINER_ID='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
NETWORK_ID='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
IMAGE_ID='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
OWNER_PID='4242'
OWNER_START='777'
OWNER_UID="$(id -u)"
NOW='2000'
BOOT_ID='11111111-2222-3333-4444-555555555555'

fail() {
  printf 'pandora_lease_registry_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

assert_contains() {
  grep -Fq -- "$2" <<<"$1" || fail "missing_output:$2"
}

setup_case() {
  CASE_DIR="$TEST_DIR/$1"
  PROC_DIR="$CASE_DIR/proc"
  ROOT_DIR="$CASE_DIR/registry"
  mkdir -p "$PROC_DIR/$OWNER_PID"
  mkdir -p "$PROC_DIR/sys/kernel/random"
  printf '%s\n' "$BOOT_ID" >"$PROC_DIR/sys/kernel/random/boot_id"
  printf '%s\n' "$OWNER_PID (pandora test owner) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 $OWNER_START 20" >"$PROC_DIR/$OWNER_PID/stat"
  printf 'Name:\tpandora-test\nUid:\t%s\t%s\t%s\t%s\n' "$OWNER_UID" "$OWNER_UID" "$OWNER_UID" "$OWNER_UID" >"$PROC_DIR/$OWNER_PID/status"
}

run_registry() {
  set +e
  OUTPUT="$(PANDORA_LEASE_REGISTRY_TEST_MODE=1 \
    PANDORA_LEASE_REGISTRY_TEST_ROOT="$ROOT_DIR" \
    PANDORA_LEASE_REGISTRY_TEST_PROC_ROOT="$PROC_DIR" \
    PANDORA_LEASE_REGISTRY_TEST_NOW="$NOW" \
    "$@" bash "$REGISTRY" 2>&1)"
  STATUS=$?
  set -e
}

create_lease() {
  run_registry env bash "$REGISTRY" create \
    --run-id "$RUN_ID" --source-identity "$SOURCE_ID" \
    --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 3000
}

# Direct helper avoids nesting a second bash when adding injected environment.
invoke() {
  local fail_atomic="${1:-0}"
  shift || true
  set +e
  OUTPUT="$(PANDORA_LEASE_REGISTRY_TEST_MODE=1 \
    PANDORA_LEASE_REGISTRY_TEST_ROOT="$ROOT_DIR" \
    PANDORA_LEASE_REGISTRY_TEST_PROC_ROOT="$PROC_DIR" \
    PANDORA_LEASE_REGISTRY_TEST_NOW="$NOW" \
    PANDORA_LEASE_REGISTRY_TEST_FAIL_ATOMIC="$fail_atomic" \
    bash "$REGISTRY" "$@" 2>&1)"
  STATUS=$?
  set -e
}

extract_capability() {
  sed -n 's/^lease_created=.*capability:\([0-9a-f]\{64\}\)$/\1/p' <<<"$1"
}

setup_case lifecycle
invoke 0 create --run-id "$RUN_ID" --source-identity "$SOURCE_ID" --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 3000
[[ "$STATUS" -eq 0 ]] || fail "create:$STATUS:$OUTPUT"
CAPABILITY="$(extract_capability "$OUTPUT")"
[[ "$CAPABILITY" =~ ^[0-9a-f]{64}$ ]] || fail 'capability_not_csprng_shape'
[[ "$(stat -c '%a:%u' "$ROOT_DIR")" == "700:${OWNER_UID}" ]] || fail 'registry_root_permissions'
[[ "$(stat -c '%a:%u:%h' "$ROOT_DIR/active/$RUN_ID.lease")" == "600:${OWNER_UID}:1" ]] || fail 'record_permissions'
invoke 0 commit --run-id "$RUN_ID" --capability "$CAPABILITY" --container-id "$CONTAINER_ID" --network-id "$NETWORK_ID" --image-id "$IMAGE_ID"
[[ "$STATUS" -eq 0 ]] || fail "commit:$STATUS:$OUTPUT"
invoke 0 heartbeat --run-id "$RUN_ID" --capability "$CAPABILITY" --expires-at 4000
[[ "$STATUS" -eq 0 ]] || fail "heartbeat:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'expires_at:4000'

# Heartbeat cannot resurrect an expired lease or extend beyond the bounded
# horizon, even with the correct live owner and capability.
NOW='4001'
invoke 0 heartbeat --run-id "$RUN_ID" --capability "$CAPABILITY" --expires-at 5000
[[ "$STATUS" -eq 78 ]] || fail "expired_heartbeat_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=heartbeat_after_expiry_forbidden'
NOW='2000'
invoke 0 heartbeat --run-id "$RUN_ID" --capability "$CAPABILITY" --expires-at 6001
[[ "$STATUS" -eq 78 ]] || fail "long_heartbeat_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=heartbeat_window_too_long'

# Wrong capability cannot mutate a committed lease.
invoke 0 heartbeat --run-id "$RUN_ID" --capability 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' --expires-at 5000
[[ "$STATUS" -eq 78 ]] || fail "wrong_capability_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=capability_mismatch'
grep -q '^expires_at=4000$' "$ROOT_DIR/active/$RUN_ID.lease" || fail 'wrong_capability_mutated_record'

# A forged Docker label has no authority when no registry record exists.
setup_case forged_label_no_registry
invoke 0 inspect --run-id "$RUN_ID"
[[ "$STATUS" -eq 78 ]] || fail "forged_label_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=lease_not_found'

# Future leases cannot be orphan-consumed, even when the owner is gone.
setup_case future_orphan
invoke 0 create --run-id "$RUN_ID" --source-identity "$SOURCE_ID" --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 3000
CAPABILITY="$(extract_capability "$OUTPUT")"
invoke 0 commit --run-id "$RUN_ID" --capability "$CAPABILITY" --container-id "$CONTAINER_ID" --network-id "$NETWORK_ID" --image-id "$IMAGE_ID"
rm -rf -- "$PROC_DIR/$OWNER_PID"
invoke 0 consume --run-id "$RUN_ID" --orphan
[[ "$STATUS" -eq 78 ]] || fail "future_orphan_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=orphan_lease_not_expired'

# Expiry alone is insufficient while the exact PID/start/UID owner remains live.
setup_case expired_live_owner
invoke 0 create --run-id "$RUN_ID" --source-identity "$SOURCE_ID" --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 3000
CAPABILITY="$(extract_capability "$OUTPUT")"
invoke 0 commit --run-id "$RUN_ID" --capability "$CAPABILITY" --container-id "$CONTAINER_ID" --network-id "$NETWORK_ID" --image-id "$IMAGE_ID"
NOW='4000'
invoke 0 consume --run-id "$RUN_ID" --orphan
[[ "$STATUS" -eq 78 ]] || fail "expired_live_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=orphan_owner_still_live'
rm -rf -- "$PROC_DIR/$OWNER_PID"
invoke 0 consume --run-id "$RUN_ID" --orphan
[[ "$STATUS" -eq 0 ]] || fail "expired_dead_consume:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'state:consumed'
invoke 0 consume --run-id "$RUN_ID" --orphan
[[ "$STATUS" -eq 78 ]] || fail "consume_replay_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=lease_not_committed_or_already_consumed'
invoke 0 delete --run-id "$RUN_ID" --orphan
[[ "$STATUS" -eq 0 ]] || fail "orphan_delete:$STATUS:$OUTPUT"
[[ ! -e "$ROOT_DIR/active/$RUN_ID.lease" && -f "$ROOT_DIR/tombstones/$RUN_ID.lease" ]] || fail 'atomic_delete_not_tombstoned'
invoke 0 create --run-id "$RUN_ID" --source-identity "$SOURCE_ID" --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 5000
[[ "$STATUS" -eq 78 ]] || fail "deleted_replay_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=replayed_deleted_lease'

# Atomic replacement failure leaves the previous committed record byte-identical.
NOW='2000'
setup_case atomic_failure
invoke 0 create --run-id "$RUN_ID" --source-identity "$SOURCE_ID" --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 3000
CAPABILITY="$(extract_capability "$OUTPUT")"
invoke 0 commit --run-id "$RUN_ID" --capability "$CAPABILITY" --container-id "$CONTAINER_ID" --network-id "$NETWORK_ID" --image-id "$IMAGE_ID"
BEFORE_HASH="$(sha256sum "$ROOT_DIR/active/$RUN_ID.lease" | awk '{print $1}')"
invoke 1 heartbeat --run-id "$RUN_ID" --capability "$CAPABILITY" --expires-at 4000
[[ "$STATUS" -eq 78 ]] || fail "atomic_failure_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'reason=atomic_write_injected_failure'
AFTER_HASH="$(sha256sum "$ROOT_DIR/active/$RUN_ID.lease" | awk '{print $1}')"
[[ "$BEFORE_HASH" == "$AFTER_HASH" ]] || fail 'atomic_failure_changed_record'

# Wrong mode, symlink and hardlink records all fail closed before parsing.
chmod 0644 "$ROOT_DIR/active/$RUN_ID.lease"
invoke 0 inspect --run-id "$RUN_ID"
[[ "$STATUS" -eq 78 ]] || fail "wrong_mode_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'registry_file_wrong_mode'
chmod 0600 "$ROOT_DIR/active/$RUN_ID.lease"
ln "$ROOT_DIR/active/$RUN_ID.lease" "$CASE_DIR/record-hardlink"
invoke 0 inspect --run-id "$RUN_ID"
[[ "$STATUS" -eq 78 ]] || fail "hardlink_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'registry_file_unexpected_links'
rm -f -- "$CASE_DIR/record-hardlink" "$ROOT_DIR/active/$RUN_ID.lease"
ln -s "$CASE_DIR/nonexistent" "$ROOT_DIR/active/$RUN_ID.lease"
invoke 0 inspect --run-id "$RUN_ID"
[[ "$STATUS" -eq 78 ]] || fail "symlink_status:$STATUS:$OUTPUT"
assert_contains "$OUTPUT" 'registry_file_invalid'

# Concurrent duplicate create: exactly one wins; the loser sees the committed name.
NOW='2000'
setup_case concurrent_create
OUT1="$CASE_DIR/out1"; OUT2="$CASE_DIR/out2"
set +e
PANDORA_LEASE_REGISTRY_TEST_MODE=1 PANDORA_LEASE_REGISTRY_TEST_ROOT="$ROOT_DIR" PANDORA_LEASE_REGISTRY_TEST_PROC_ROOT="$PROC_DIR" PANDORA_LEASE_REGISTRY_TEST_NOW="$NOW" \
  bash "$REGISTRY" create --run-id "$RUN_ID" --source-identity "$SOURCE_ID" --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 3000 >"$OUT1" 2>&1 & P1=$!
PANDORA_LEASE_REGISTRY_TEST_MODE=1 PANDORA_LEASE_REGISTRY_TEST_ROOT="$ROOT_DIR" PANDORA_LEASE_REGISTRY_TEST_PROC_ROOT="$PROC_DIR" PANDORA_LEASE_REGISTRY_TEST_NOW="$NOW" \
  bash "$REGISTRY" create --run-id "$RUN_ID" --source-identity "$SOURCE_ID" --owner-pid "$OWNER_PID" --owner-start-ticks "$OWNER_START" --expires-at 3000 >"$OUT2" 2>&1 & P2=$!
wait "$P1"; S1=$?
wait "$P2"; S2=$?
set -e
[[ "$S1:$S2" == '0:78' || "$S1:$S2" == '78:0' ]] || fail "concurrent_statuses:$S1:$S2"
[[ "$(find "$ROOT_DIR/active" -maxdepth 1 -type f -name '*.lease' | wc -l | tr -d ' ')" == '1' ]] || fail 'concurrent_record_count'

printf 'pandora_lease_registry_mock=PASS cases=8\n'
