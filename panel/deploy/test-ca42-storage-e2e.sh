#!/usr/bin/env bash
set -euo pipefail

readonly E2E_MARKER='pandora-ca42-e2e-storage-fixture-v1'
readonly E2E_TEST='TestCA42E2EStorageDescriptorTwoPhaseNative'
readonly -a HOST_FIXED_PATHS=(
  /var/lib/pandora/ca42-storage-runtime-preflight
  /var/lib/pandora/ca42-storage-runtime-tainted
  /run/pandora/ca42/attempt-storage-preflight
  /run/pandora/ca42/attempt-storage-tainted
)
E2E_WORKSPACE=''
E2E_CHILD_PID=''
E2E_CHILD_LAUNCHING=0
E2E_PENDING_SIGNAL=''

die() { printf 'ca42_storage_e2e=DENY reason=%s\n' "$1" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "missing_command_$1"; }

assert_host_roots_absent() {
  local path
  for path in "${HOST_FIXED_PATHS[@]}"; do
    [[ ! -e "$path" && ! -L "$path" ]] || die 'host_fixed_root_present'
  done
}

assert_test_binary() {
  local binary=$1
  [[ -f "$binary" && ! -L "$binary" ]] || die 'test_binary_not_regular'
  [[ $(stat -c '%u:%g:%a:%h' -- "$binary") == '0:0:500:1' ]] || die 'test_binary_identity_invalid'
  grep -a -F -q "$E2E_MARKER" "$binary" || die 'test_binary_marker_missing'
}

stop_e2e_child() {
  local pid=${E2E_CHILD_PID:-}
  [[ $pid =~ ^[1-9][0-9]*$ ]] || return 0
  if kill -0 -- "-$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    local attempt
    for attempt in {1..50}; do
      kill -0 -- "-$pid" 2>/dev/null || break
      sleep 0.1
    done
    kill -0 -- "-$pid" 2>/dev/null && kill -KILL -- "-$pid" 2>/dev/null || true
  fi
  wait "$pid" 2>/dev/null || true
  E2E_CHILD_PID=''
}

cleanup_outer() {
  trap - EXIT INT TERM
  set +e
  stop_e2e_child
  if [[ -n $E2E_WORKSPACE && $E2E_WORKSPACE == /var/tmp/pandora-ca42-storage-e2e.* && -d $E2E_WORKSPACE ]]; then
    rm -rf -- "$E2E_WORKSPACE"
  fi
}

handle_outer_signal() {
  local signal=$1
  if ((E2E_CHILD_LAUNCHING == 1)) && [[ -z $E2E_CHILD_PID ]]; then
    E2E_PENDING_SIGNAL=$signal
    return 0
  fi
  trap - INT TERM
  stop_e2e_child
  assert_host_roots_absent
  [[ $signal == INT ]] && exit 130
  exit 143
}

inner_main() {
  [[ $# -eq 3 ]] || die 'inner_arguments_invalid'
  local source_binary=$1 workspace=$2 image=$3
  local filesystem_root="$workspace/ext4"
  local isolated_binary='/root/ca42storage-e2e.test'
  [[ $(id -u) -eq 0 ]] || die 'inner_euid_not_zero'
  [[ $(readlink /proc/1/ns/mnt) == "$(readlink /proc/thread-self/ns/mnt)" ]] || die 'inner_mount_namespace_mismatch'
  [[ $(readlink /proc/1/ns/pid) == "$(readlink /proc/thread-self/ns/pid)" ]] || die 'inner_pid_namespace_mismatch'
  [[ $(readlink /proc/self/fd/8) == pid:* ]] || die 'host_pid_namespace_fd_missing'
  [[ $(readlink /proc/self/fd/9) == mnt:* ]] || die 'host_mount_namespace_fd_missing'
  [[ $(stat -Lc '%d:%i' /proc/self/fd/8) != "$(stat -Lc '%d:%i' /proc/thread-self/ns/pid)" ]] || die 'pid_namespace_not_isolated_from_host'
  [[ $(stat -Lc '%d:%i' /proc/self/fd/9) != "$(stat -Lc '%d:%i' /proc/thread-self/ns/mnt)" ]] || die 'mount_namespace_not_isolated_from_host'

  mount --make-rprivate /
  mkdir -p "$filesystem_root"
  mount -o loop,nodev,nosuid -- "$image" "$filesystem_root"
  mount -t tmpfs -o mode=0755,nodev,nosuid tmpfs /etc
  mount -t tmpfs -o mode=0755,nodev,nosuid tmpfs /var/lib
  mount -t tmpfs -o mode=0755,nodev,nosuid tmpfs /run
  mount -t tmpfs -o mode=0700,nodev,nosuid tmpfs /root
  install -d -o 0 -g 0 -m 0700 "$filesystem_root/var-lib-pandora" "$filesystem_root/run-pandora"
  install -d -o 0 -g 0 -m 0700 /var/lib/pandora /run/pandora
  mount --bind "$filesystem_root/var-lib-pandora" /var/lib/pandora
  mount --bind "$filesystem_root/run-pandora" /run/pandora
  install -o 0 -g 0 -m 0500 -- "$source_binary" "$isolated_binary"
  assert_test_binary "$isolated_binary"
  "$isolated_binary" -test.list "^${E2E_TEST}$" | grep -Fqx "$E2E_TEST" || die 'required_test_missing'
  PANDORA_CA42_E2E_ISOLATED=1 "$isolated_binary" -test.run "^${E2E_TEST}$" -test.count=1 -test.v
}

outer_main() {
  [[ $# -eq 1 ]] || die 'usage_binary_required'
  [[ $(uname -s) == Linux ]] || die 'linux_required'
  [[ $(id -u) -eq 0 ]] || die 'euid_zero_required'
  [[ ${PANDORA_CA42_E2E_ACK:-} == isolated-disposable-root ]] || die 'explicit_ack_required'
  local command
  for command in bash grep unshare setsid mount mkfs.ext4 truncate stat install readlink sleep; do require_command "$command"; done
  local binary
  binary=$(realpath -e -- "$1")
  assert_test_binary "$binary"
  assert_host_roots_absent
  E2E_WORKSPACE=$(mktemp -d /var/tmp/pandora-ca42-storage-e2e.XXXXXX)
  [[ $E2E_WORKSPACE == /var/tmp/pandora-ca42-storage-e2e.* ]] || die 'workspace_identity_invalid'
  trap cleanup_outer EXIT
  trap 'handle_outer_signal INT' INT
  trap 'handle_outer_signal TERM' TERM
  chmod 0700 "$E2E_WORKSPACE"
  local image="$E2E_WORKSPACE/ca42-storage-e2e.ext4"
  truncate -s 1G "$image"
  mkfs.ext4 -F -q -O verity "$image"
  exec 8</proc/self/ns/pid
  exec 9</proc/self/ns/mnt
  local inner_status
  set +e
  E2E_CHILD_LAUNCHING=1
  setsid unshare --mount --pid --fork --kill-child=KILL --mount-proc \
    bash "$0" --inner "$binary" "$E2E_WORKSPACE" "$image" &
  E2E_CHILD_PID=$!
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
  ((inner_status == 0)) || return "$inner_status"
  printf 'ca42_storage_e2e=PASS isolation=mount_pid_ext4 two_phase=verified host_roots=unchanged\n'
}

if [[ ${1:-} == --inner ]]; then shift; inner_main "$@"; else outer_main "$@"; fi
