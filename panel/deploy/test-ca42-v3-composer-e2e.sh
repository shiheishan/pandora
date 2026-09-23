#!/usr/bin/env bash
set -euo pipefail

readonly E2E_MARKER='pandora-ca42-e2e-roots-v1'
readonly FROZEN_MIGRATION_SHA256='ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5'
readonly FROZEN_MIGRATION_SIZE='76030'
readonly ISOLATED_MIGRATION='/root/00042_client_auth_expand.sql'
readonly E2E_TEST_REGEX='^(TestOpenProductionV3VerificationSessionIsolatedProvisionedRoot|TestOpenProductionV3VerificationSessionJournalFailureRollsBackOwnership)$'
readonly -a E2E_TEST_NAMES=(
  TestOpenProductionV3VerificationSessionIsolatedProvisionedRoot
  TestOpenProductionV3VerificationSessionJournalFailureRollsBackOwnership
)
readonly -a HOST_FIXED_PATHS=(
  /etc/pandora/ca42
  /var/lib/pandora/ca42
  /var/lib/pandora/ca42-control-v3
  /var/lib/pandora/release-journal-v3
  /run/pandora/ca42
)
E2E_WORKSPACE=''
E2E_CHILD_PID=''
E2E_CHILD_STARTTIME=''
E2E_CHILD_LAUNCHING=0
E2E_PENDING_SIGNAL=''

capture_e2e_child_identity() {
  local pid=$1 record tail
  [[ -r /proc/$pid/stat ]] || return 1
  IFS= read -r record <"/proc/$pid/stat" || return 1
  tail=${record##*) }
  local -a fields=()
  read -r -a fields <<<"$tail"
  ((${#fields[@]} > 19)) || return 1
  E2E_CHILD_STARTTIME=${fields[19]}
}

e2e_child_identity_matches() {
  local pid=$1 record tail
  [[ $E2E_CHILD_STARTTIME =~ ^[0-9]+$ && -r /proc/$pid/stat ]] || return 1
  IFS= read -r record <"/proc/$pid/stat" || return 1
  tail=${record##*) }
  local -a fields=()
  read -r -a fields <<<"$tail"
  ((${#fields[@]} > 19)) && [[ ${fields[19]} == "$E2E_CHILD_STARTTIME" ]]
}

stop_e2e_child() {
  local pid=${E2E_CHILD_PID:-}
  [[ $pid =~ ^[1-9][0-9]*$ ]] || return 0
  if kill -0 -- "-$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || true
  elif e2e_child_identity_matches "$pid"; then
    kill -TERM -- "$pid" 2>/dev/null || true
  fi
  if kill -0 -- "-$pid" 2>/dev/null || e2e_child_identity_matches "$pid"; then
    local attempt
    for attempt in {1..50}; do
      if ! kill -0 -- "-$pid" 2>/dev/null && ! e2e_child_identity_matches "$pid"; then
        break
      fi
      sleep 0.1
    done
    if kill -0 -- "-$pid" 2>/dev/null; then
      kill -KILL -- "-$pid" 2>/dev/null || true
    elif e2e_child_identity_matches "$pid"; then
      kill -KILL -- "$pid" 2>/dev/null || true
    fi
  fi
  wait "$pid" 2>/dev/null || true
  E2E_CHILD_PID=''
  E2E_CHILD_STARTTIME=''
}

cleanup_outer() {
  trap - EXIT INT TERM
  set +e
  stop_e2e_child
  if [[ -n $E2E_WORKSPACE && $E2E_WORKSPACE == /var/tmp/pandora-ca42-e2e.* && -d $E2E_WORKSPACE ]]; then
    rm -rf -- "$E2E_WORKSPACE"
  fi
}

handle_outer_signal() {
  local signal=$1
  if ((E2E_CHILD_LAUNCHING == 1)); then
    E2E_PENDING_SIGNAL=$signal
    return 0
  fi
  trap - INT TERM
  stop_e2e_child
  assert_host_roots_absent
  case "$signal" in
    INT) exit 130 ;;
    TERM) exit 143 ;;
    *) exit 1 ;;
  esac
}

die() {
  printf 'ca42_e2e=DENY reason=%s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "missing_command_$1"
}

assert_host_roots_absent() {
  local path
  for path in "${HOST_FIXED_PATHS[@]}"; do
    [[ ! -e "$path" && ! -L "$path" ]] || die "host_fixed_root_present"
  done
}

assert_test_binary() {
  local binary=$1
  [[ -f "$binary" && ! -L "$binary" ]] || die 'test_binary_not_regular'
  [[ $(stat -c '%u:%g:%a:%h' -- "$binary") == '0:0:500:1' ]] || die 'test_binary_identity_invalid'
  grep -a -F -q "$E2E_MARKER" "$binary" || die 'test_binary_e2e_marker_missing'
}

assert_frozen_migration() {
  local migration=$1
  [[ -f "$migration" && ! -L "$migration" ]] || die 'migration_not_regular'
  local identity
  identity=$(stat -c '%u:%g:%s:%h:%a' -- "$migration")
  [[ $identity == "0:0:${FROZEN_MIGRATION_SIZE}:1:"* ]] || die 'migration_identity_invalid'
  local mode=${identity##*:}
  (( (8#$mode & 8#022) == 0 )) || die 'migration_writable_by_group_or_other'
  local digest ignored
  read -r digest ignored < <(sha256sum -- "$migration")
  [[ $digest == "$FROZEN_MIGRATION_SHA256" ]] || die 'migration_sha256_invalid'
}

inner_main() {
  [[ $# -eq 3 ]] || die 'inner_arguments_invalid'
  local workspace=$1
  local image=$2
  local test_name=$3
  local filesystem_root="$workspace/ext4"
  local isolated_root="$workspace/rootfs"
  local isolated_binary='/root/ca42runner-e2e.test'

  case "$test_name" in
    TestOpenProductionV3VerificationSessionIsolatedProvisionedRoot|TestOpenProductionV3VerificationSessionJournalFailureRollsBackOwnership) ;;
    *) die 'test_name_not_allowlisted' ;;
  esac

  [[ $(id -u) -eq 0 ]] || die 'inner_euid_not_zero'
  [[ -r /proc/1/ns/mnt && -r /proc/1/ns/pid && -r /proc/1/ns/user ]] || die 'inner_proc_namespace_missing'
  [[ $(readlink /proc/1/ns/mnt) == "$(readlink /proc/thread-self/ns/mnt)" ]] || die 'inner_mount_namespace_mismatch'
  [[ $(readlink /proc/1/ns/pid) == "$(readlink /proc/thread-self/ns/pid)" ]] || die 'inner_pid_namespace_mismatch'
  [[ $(readlink /proc/1/ns/user) == "$(readlink /proc/thread-self/ns/user)" ]] || die 'inner_user_namespace_mismatch'
  [[ $(readlink /proc/self/fd/8) == pid:* ]] || die 'host_pid_namespace_fd_missing'
  [[ $(readlink /proc/self/fd/9) == mnt:* ]] || die 'host_mount_namespace_fd_missing'
  [[ $(stat -Lc '%d:%i' /proc/self/fd/8) != "$(stat -Lc '%d:%i' /proc/thread-self/ns/pid)" ]] || die 'pid_namespace_not_isolated_from_host'
  [[ $(stat -Lc '%d:%i' /proc/self/fd/9) != "$(stat -Lc '%d:%i' /proc/thread-self/ns/mnt)" ]] || die 'mount_namespace_not_isolated_from_host'

  mount --make-rprivate /
  mkdir -p "$filesystem_root"
  mount -o loop,nodev,nosuid -- "$image" "$filesystem_root"

  mkdir -p "$isolated_root"
  mount -t tmpfs -o mode=0755,nodev,nosuid tmpfs "$isolated_root"
  install -d -o 0 -g 0 -m 0555 "$isolated_root/proc"
  install -d -o 0 -g 0 -m 0755 \
    "$isolated_root/etc" "$isolated_root/var" "$isolated_root/var/lib" \
    "$isolated_root/run" "$isolated_root/usr" "$isolated_root/lib" "$isolated_root/lib64"
  install -d -o 0 -g 0 -m 0700 "$isolated_root/root"
  install -d -o 0 -g 0 -m 0700 \
    "$filesystem_root/etc-pandora" \
    "$filesystem_root/var-lib-pandora" \
    "$filesystem_root/run-pandora"
  install -d -o 0 -g 0 -m 0700 \
    "$isolated_root/etc/pandora" "$isolated_root/var/lib/pandora" "$isolated_root/run/pandora"
  mount --bind "$filesystem_root/etc-pandora" "$isolated_root/etc/pandora"
  mount --bind "$filesystem_root/var-lib-pandora" "$isolated_root/var/lib/pandora"
  mount --bind "$filesystem_root/run-pandora" "$isolated_root/run/pandora"
  mount --bind /proc "$isolated_root/proc"

  install -o 0 -g 0 -m 0500 -- /proc/self/fd/6 "$isolated_root$isolated_binary"
  install -o 0 -g 0 -m 0400 -- /proc/self/fd/7 "$isolated_root$ISOLATED_MIGRATION"
  assert_test_binary "$isolated_root$isolated_binary"
  assert_frozen_migration "$isolated_root$ISOLATED_MIGRATION"

  local listed_tests expected_test
  listed_tests=$(chroot "$isolated_root" "$isolated_binary" -test.list "$E2E_TEST_REGEX")
  for expected_test in "${E2E_TEST_NAMES[@]}"; do
    grep -Fqx "$expected_test" <<<"$listed_tests" || die 'required_e2e_test_missing'
  done

  PANDORA_CA42_E2E_ISOLATED=1 \
    chroot "$isolated_root" "$isolated_binary" -test.run "^${test_name}$" -test.count=1 -test.v
}

outer_main() {
  [[ $# -eq 2 ]] || die 'usage_binary_and_migration_required'
  [[ $(uname -s) == 'Linux' ]] || die 'linux_required'
  [[ $(id -u) -eq 0 ]] || die 'euid_zero_required'
  [[ ${PANDORA_CA42_E2E_ACK:-} == 'isolated-disposable-root' ]] || die 'explicit_ack_required'

  local command
  for command in bash chroot grep sha256sum unshare setsid timeout mount mkfs.ext4 truncate stat install readlink sleep; do
    require_command "$command"
  done

  local binary
  binary=$(realpath -e -- "$1")
  local migration
  migration=$(realpath -e -- "$2")
  assert_test_binary "$binary"
  assert_frozen_migration "$migration"
  assert_host_roots_absent

  E2E_WORKSPACE=$(mktemp -d /var/tmp/pandora-ca42-e2e.XXXXXX)
  [[ $E2E_WORKSPACE == /var/tmp/pandora-ca42-e2e.* ]] || die 'workspace_identity_invalid'
  trap cleanup_outer EXIT
  trap 'handle_outer_signal INT' INT
  trap 'handle_outer_signal TERM' TERM
  chmod 0700 "$E2E_WORKSPACE"
  exec 6<"$binary"
  exec 7<"$migration"
  exec 8</proc/self/ns/pid
  exec 9</proc/self/ns/mnt

  local index=0 test_name
  for test_name in "${E2E_TEST_NAMES[@]}"; do
    index=$((index + 1))
    local case_workspace="$E2E_WORKSPACE/case-$index"
    local image="$case_workspace/ca42-e2e.ext4"
    install -d -o 0 -g 0 -m 0700 "$case_workspace"
    truncate -s 3G "$image"
    mkfs.ext4 -F -q -O verity "$image"
    local inner_status
    set +e
    E2E_CHILD_LAUNCHING=1
    setsid timeout --kill-after=5s 90s unshare --mount --pid --fork --kill-child=KILL --mount-proc \
      bash "$0" --inner "$case_workspace" "$image" "$test_name" &
    E2E_CHILD_PID=$!
    capture_e2e_child_identity "$E2E_CHILD_PID" || E2E_CHILD_STARTTIME=''
    E2E_CHILD_LAUNCHING=0
    if [[ -n $E2E_PENDING_SIGNAL ]]; then
      local pending_signal=$E2E_PENDING_SIGNAL
      E2E_PENDING_SIGNAL=''
      handle_outer_signal "$pending_signal"
    fi
    wait "$E2E_CHILD_PID"
    inner_status=$?
    E2E_CHILD_PID=''
    set -e
    assert_host_roots_absent
    if ((inner_status != 0)); then
      return "$inner_status"
    fi
  done
  printf 'ca42_e2e=PASS isolation=mount_pid_ext4 host_roots=unchanged\n'
}

if [[ ${1:-} == '--inner' ]]; then
  shift
  inner_main "$@"
else
  outer_main "$@"
fi
