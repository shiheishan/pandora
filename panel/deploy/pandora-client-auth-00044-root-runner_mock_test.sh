#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
PACKAGE_DIR="$ROOT_DIR/internal/platform/clientauth/ca44runner"
COMMAND_DIR="$ROOT_DIR/cmd/pandora-client-auth-00044-root-runner"
GO_BIN="${PANDORA_GO_BIN:-go}"
TMP_BASE="${PANDORA_CA44_MOCK_TMP:-${TMPDIR:-/tmp}/pandora-ca44-root-runner-mock.$$}"

cleanup() {
  rm -f -- "$TMP_BASE.windows.exe" "$TMP_BASE.linux-amd64" "$TMP_BASE.linux-arm64" \
    "$TMP_BASE.stdout" "$TMP_BASE.stderr"
}
trap cleanup EXIT

need_literal() {
  local file="$1" literal="$2" marker="$3"
  if ! grep -Fq -- "$literal" "$file"; then
    printf 'root_runner_mock=DENY reason=%s\n' "$marker" >&2
    exit 1
  fi
}

need_literal "$PACKAGE_DIR/fd_linux.go" 'unix.Openat2' 'openat2_missing'
need_literal "$PACKAGE_DIR/fd_linux.go" 'unix.RESOLVE_BENEATH' 'resolve_beneath_missing'
need_literal "$PACKAGE_DIR/fd_linux.go" 'unix.RESOLVE_NO_SYMLINKS' 'resolve_no_symlinks_missing'
need_literal "$PACKAGE_DIR/fd_linux.go" 'io.NewSectionReader' 'same_fd_hash_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'Setpgid: true' 'process_group_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'Pdeathsig: syscall.SIGKILL' 'parent_death_signal_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'unix.Kill(-cmd.Process.Pid, unix.SIGKILL)' 'group_kill_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'PidFD: &pidFD' 'pidfd_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'reapProcessGroup' 'process_group_reap_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'if err := sealInheritedDescriptors(); err != nil {' 'inherited_fd_seal_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'unix.CloseRange(3, ^uint(0), linuxCloseRangeCloexec)' 'close_range_cloexec_missing'
need_literal "$PACKAGE_DIR/child_linux.go" 'sealInheritedDescriptorsByProcScan()' 'inherited_fd_proc_scan_missing'
need_literal "$PACKAGE_DIR/runner_linux.go" 'unix.PR_SET_CHILD_SUBREAPER' 'subreaper_missing'
need_literal "$PACKAGE_DIR/runner_linux.go" 'duplicateCloseOnExec(stage.candidateFD)' 'candidate_cloexec_missing'
need_literal "$PACKAGE_DIR/runner_linux.go" 'verifierExtra := make([]*os.File, 34)' 'verifier_padding_missing'
need_literal "$PACKAGE_DIR/runner_linux.go" 'verifierExtra[33] = verifier' 'verifier_exec_fd_missing'
need_literal "$PACKAGE_DIR/runner_linux.go" 'stdout.MatchExact' 'exact_capture_missing'
need_literal "$PACKAGE_DIR/staging_linux.go" 'unix.RENAME_NOREPLACE' 'atomic_noreplace_missing'
need_literal "$PACKAGE_DIR/staging_linux.go" 'discardVerifiedDuplicate' 'idempotent_closure_missing'
need_literal "$PACKAGE_DIR/staging_linux.go" 'releaseManifestName' 'signed_manifest_bundle_missing'
need_literal "$PACKAGE_DIR/staging_linux.go" 'syncVerifiedExistingBundle(bundleFD, s.publishFD, fsyncDirectories)' 'idempotent_durability_retry_missing'
need_literal "$PACKAGE_DIR/staging_linux.go" 'scavengeActive' 'startup_scavenger_missing'
need_literal "$PACKAGE_DIR/staging_linux.go" 'unix.Fsync' 'durability_missing'
need_literal "$PACKAGE_DIR/runner_linux.go" 'mergeRecoveryFailure(failure, quarantineErr)' 'quarantine_failure_reporting_missing'

export GOTOOLCHAIN=local
export GOPROXY=off
export GOSUMDB=off
export GOCACHE="${PANDORA_GOCACHE:-$TMP_BASE.gocache}"
mkdir -p -- "$GOCACHE"

cd "$ROOT_DIR"
"$GO_BIN" test -buildvcs=false -count=1 ./internal/platform/clientauth/ca44runner
"$GO_BIN" vet -buildvcs=false ./internal/platform/clientauth/ca44runner

GOOS=windows GOARCH=amd64 "$GO_BIN" build -buildvcs=false -o "$TMP_BASE.windows.exe" \
  ./cmd/pandora-client-auth-00044-root-runner
set +e
"$TMP_BASE.windows.exe" >"$TMP_BASE.stdout" 2>"$TMP_BASE.stderr"
windows_code=$?
set -e
if [[ "$windows_code" -ne 77 ]] || [[ -s "$TMP_BASE.stdout" ]]; then
  printf 'root_runner_mock=DENY reason=nonlinux_exit_contract\n' >&2
  exit 1
fi
expected='client_auth_00044_root_runner=NOT_RUN reason=linux_required db=NOT_CONNECTED network=NOT_USED'
if [[ "$(tr -d '\r\n' <"$TMP_BASE.stderr")" != "$expected" ]]; then
  printf 'root_runner_mock=DENY reason=nonlinux_receipt_contract\n' >&2
  exit 1
fi

GOOS=linux GOARCH=amd64 "$GO_BIN" build -buildvcs=false -o "$TMP_BASE.linux-amd64" \
  ./cmd/pandora-client-auth-00044-root-runner
GOOS=linux GOARCH=arm64 "$GO_BIN" build -buildvcs=false -o "$TMP_BASE.linux-arm64" \
  ./cmd/pandora-client-auth-00044-root-runner

python_cmd="${PANDORA_PYTHON_BIN:-python}"
"$python_cmd" - "$TMP_BASE.linux-amd64" "$TMP_BASE.linux-arm64" <<'PY'
import struct
import sys

for path, expected in zip(sys.argv[1:], (0x3E, 0xB7)):
    with open(path, "rb") as stream:
        header = stream.read(20)
    if header[:4] != b"\x7fELF" or struct.unpack("<H", header[18:20])[0] != expected:
        raise SystemExit(f"ELF machine mismatch: {path}")
PY

printf 'root_runner_mock=PASS policy=PASS windows_notrun=PASS linux_amd64_crossbuild=PASS linux_arm64_crossbuild=PASS native_linux_root=NOT_RUN pg18=NOT_RUN vps=NOT_RUN production=NOT_RUN\n'
