#!/usr/bin/env bash
# Native Linux/root Phase-A gate.  No database, VPS, or network operation.
# Source-tree preflight only: it consumes local .ai-company contract evidence
# and is never an authorizing, packageable, or release gate.
set -Eeuo pipefail
umask 077

NOT_RUN='client_auth_00044_verifier_linux_root=NOT_RUN reason=native_linux_root_required db=NOT_CONNECTED network=NOT_USED'
kernel="$(uname -s 2>/dev/null || printf unknown)"
release="$(uname -r 2>/dev/null || printf unknown)"
if [[ "$kernel" != Linux || "$EUID" -ne 0 || "$release" == *[Mm]icrosoft* || "$release" == *WSL* ]]; then
  printf '%s\n' "$NOT_RUN"
  exit 77
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GENERATOR="$ROOT/deploy/client-auth-00044-verifier-gate.py"
CONTRACT="$ROOT/.ai-company/handoffs/client-auth-00044-classify-backfill-contract-20260731.md"
VERIFIER_SOURCE="$ROOT/cmd/pandora-client-auth-00044-artifact-verifier"
GOLDEN_DIR="$ROOT/cmd/pandora-device-key-classifier/testdata"
CONTRACT_SHA=78fd468074f8ad528c2e406ae9fc83c8ae1f2da8c36b81eb386e8eeee38595c4
SYNTHETIC_APPROVAL=NON_AUTHORIZING_SYNTHETIC_V1
BASE=/root/pandora-ca44-gates
RUN=''
IDENTITY_GATE=NOT_PASSED
PASS_COUNT=0

fail() {
  printf 'client_auth_00044_verifier_linux_root=FAIL reason=%s db=NOT_CONNECTED network=NOT_USED\n' "$1" >&2
  exit 1
}

sha_file() { sha256sum -- "$1" | awk '{print tolower($1)}'; }

cleanup() {
  [[ -n "$RUN" ]] || return 0
  case "$RUN" in
    /root/pandora-ca44-gates/phase-a.*)
      [[ -d "$RUN" && ! -L "$RUN" ]] || return 1
      rm -rf --one-file-system -- "$RUN" || return 1
      [[ ! -e "$RUN" && ! -L "$RUN" ]] || return 1
      ;;
    *) return 1 ;;
  esac
}
on_exit() {
  local rc=$?
  trap - EXIT HUP INT TERM
  if ! cleanup; then
    printf 'client_auth_00044_verifier_linux_root=FAIL reason=cleanup_failed db=NOT_CONNECTED network=NOT_USED\n' >&2
    [[ "$rc" -ne 0 ]] || rc=1
  fi
  exit "$rc"
}
trap on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

PYTHON_BIN="${PYTHON_BIN:-python3}"
for command_name in uname sha256sum awk stat install mktemp chmod chown ln truncate cmp grep find od setpriv rm mv cat tr dirname; do
  command -v "$command_name" >/dev/null 2>&1 || fail "required_command_missing:${command_name}"
done
command -v "$PYTHON_BIN" >/dev/null 2>&1 || fail required_command_missing:python3

[[ "$#" -eq 4 && "$1" == --verifier-elf && "$3" == --verifier-test-elf && -n "$2" && -n "$4" ]] \
  || fail invalid_arguments
VERIFIER_INPUT="$2"
TEST_INPUT="$4"
[[ "${PANDORA_CA44_PHASE_A_SYNTHETIC_APPROVED:-}" == "$SYNTHETIC_APPROVAL" ]] \
  || fail signed_release_identity_absent_synthetic_not_approved

case "$(uname -m)" in
  x86_64|amd64)
    ARCH=amd64
    EXPECTED_VERIFIER_SHA=f9c0a1fe23593d6917f87987a5d458648d7bb80db9862ffad7eefa693e822628
    EXPECTED_TEST_SHA=0d0d50a25353c3cb23a878d2788136c9eeada0016df677a9f55f9e99689c48f1
    EXPECTED_MACHINE=62
    ;;
  aarch64|arm64)
    ARCH=arm64
    EXPECTED_VERIFIER_SHA=253c332c2670f64c1c65dc4f590702a20238b3eef2014e953f805ee1b0e779d8
    EXPECTED_TEST_SHA=2eef8a960b024869b35dd93a339f91dbfe7a46aa90895d5d97188382f8083969
    EXPECTED_MACHINE=183
    ;;
  *) fail unsupported_native_architecture ;;
esac

exact_sha() { [[ -f "$1" && ! -L "$1" && "$(sha_file "$1")" == "$2" ]] || fail "identity_mismatch:$3"; }
exact_sha "$CONTRACT" "$CONTRACT_SHA" contract
exact_sha "$VERIFIER_SOURCE/main.go" 05d3c32d50ef8b14355440d52a7f583d53e7c575d409310e5d027ede1ceed0b4 verifier_main
exact_sha "$VERIFIER_SOURCE/verifier.go" b53cf46faacab6917df092f16939b76191620b2e5953e9bd848d2da062ca035f verifier_business
exact_sha "$VERIFIER_SOURCE/fd_linux.go" b2d51b492732d5b626fcb1707e21800c2e5981b706df876d3be8c8b8a47a99d2 verifier_linux_fd
exact_sha "$VERIFIER_SOURCE/fd_other.go" e35ac09c5de40b454241391a51d2cd2cc60cab2bb8e50359a54b9029020c1b90 verifier_other_fd
exact_sha "$VERIFIER_SOURCE/verifier_test.go" 55dae658d4949fff6f9d1548f241e48c52041150e8bcf479086015a18ba57d94 verifier_test_source
exact_sha "$GOLDEN_DIR/golden20-source.ndjson" 9d7957e43f1494aea7dd39c4cc2f442cc6096857008d578c55455f4588b1c1d8 golden_source
exact_sha "$GOLDEN_DIR/golden20-artifact.json" 4563d10078318eec79c2d42947122b9e1af311990a87e44f928fad7471befc1c golden_artifact
exact_sha "$VERIFIER_INPUT" "$EXPECTED_VERIFIER_SHA" current_arch_verifier_elf
exact_sha "$TEST_INPUT" "$EXPECTED_TEST_SHA" current_arch_test_elf
[[ "$(od -An -t u1 -N4 -- "$VERIFIER_INPUT")" == ' 127  69  76  70' ]] || fail verifier_not_elf
machine="$(od -An -t u2 -j18 -N2 -- "$VERIFIER_INPUT" | tr -d '[:space:]')"
[[ "$machine" == "$EXPECTED_MACHINE" ]] || fail verifier_wrong_machine

install -d -m 0700 -o 0 -g 0 "$BASE"
[[ -d "$BASE" && ! -L "$BASE" && "$(stat -c '%u:%a' -- "$BASE")" == 0:700 ]] || fail base_metadata_invalid
RUN="$(mktemp -d "$BASE/phase-a.XXXXXXXX")"
[[ "$RUN" == /root/pandora-ca44-gates/phase-a.* && -d "$RUN" && ! -L "$RUN" ]] || fail run_directory_invalid
[[ "$(stat -c '%u:%a' -- "$RUN")" == 0:700 ]] || fail run_directory_metadata_invalid
install -d -m 0700 -o 0 -g 0 "$RUN/bin" "$RUN/input" "$RUN/cases" "$RUN/generated"
install -m 0500 -o 0 -g 0 "$VERIFIER_INPUT" "$RUN/bin/verifier"
install -m 0500 -o 0 -g 0 "$TEST_INPUT" "$RUN/bin/verifier.test"
exact_sha "$RUN/bin/verifier" "$EXPECTED_VERIFIER_SHA" copied_verifier
exact_sha "$RUN/bin/verifier.test" "$EXPECTED_TEST_SHA" copied_test
VERIFIER="$RUN/bin/verifier"
TEST_ELF="$RUN/bin/verifier.test"
IDENTITY_GATE=PASSED

install -m 0600 -o 0 -g 0 "$GOLDEN_DIR/golden20-source.ndjson" "$RUN/input/source"
install -m 0600 -o 0 -g 0 "$GOLDEN_DIR/golden20-artifact.json" "$RUN/input/artifact"
"$PYTHON_BIN" -c 'import os,sys; os.umask(0o077); open(sys.argv[1],"xb").write(bytes(range(32))); open(sys.argv[2],"xb").write(bytes([0xa5])*32)' \
  "$RUN/input/artifact.key" "$RUN/input/evidence.key"
chmod 0600 "$RUN/input/artifact.key" "$RUN/input/evidence.key"
"$PYTHON_BIN" "$GENERATOR" generate --source "$RUN/input/source" --artifact "$RUN/input/artifact" \
  --artifact-key "$RUN/input/artifact.key" --evidence-key "$RUN/input/evidence.key" \
  --out-dir "$RUN/generated" --contract-sha "$CONTRACT_SHA" --verifier-sha "$EXPECTED_VERIFIER_SHA" \
  >"$RUN/cases/generator.out" 2>"$RUN/cases/generator.err" || fail independent_generator_failed
[[ ! -s "$RUN/cases/generator.err" ]] || fail independent_generator_stderr

ARGS=(
  -source-format ndjson -source-fd 30 -artifact-fd 31
  -detached-manifest-fd 32 -release-expectations-fd 33
  -artifact-hmac-key-id artifact-2026-01 -artifact-hmac-key-fd 34
  -evidence-hmac-key-id evidence-2026-01 -evidence-hmac-key-fd 35
)

invoke() {
  local name="$1" want_rc="$2" want_err="$3" mode="${4:-normal}" out="$RUN/cases/$name.out" err="$RUN/cases/$name.err" expected_err="$RUN/cases/$name.expected.err" rc child_rc
  [[ "$IDENTITY_GATE" == PASSED ]] || fail verifier_child_before_identity_gate
  : >"$out"; : >"$err"; : >"$expected_err"; chmod 0600 "$out" "$err" "$expected_err"
  if [[ -n "$want_err" ]]; then printf '%s\n' "$want_err" >"$expected_err"; fi
  set +e
  case "$mode" in
    normal) "$VERIFIER" "${ARGS[@]}" 30<"$RUN/input/source" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err" ;;
    alias) local -a a=("${ARGS[@]}"); a[5]=30; "$VERIFIER" "${a[@]}" 30<"$RUN/input/source" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err" ;;
    missing_cli) "$VERIFIER" "${ARGS[@]:0:16}" >"$out" 2>"$err" ;;
    missing_fd) local -a a=("${ARGS[@]}"); a[3]=99; "$VERIFIER" "${a[@]}" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err" ;;
    nonroot)
      install -m 0555 -o 0 -g 0 "$VERIFIER" "$RUN/bin/verifier.nonroot"
      chmod 0711 "$RUN" "$RUN/bin"
      setpriv --reuid=65534 --regid=65534 --clear-groups "$RUN/bin/verifier.nonroot" "${ARGS[@]}" 30<"$RUN/input/source" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err"
      child_rc=$?
      chmod 0700 "$RUN" "$RUN/bin" || child_rc=125
      rm -f -- "$RUN/bin/verifier.nonroot" || child_rc=125
      (exit "$child_rc")
      ;;
    type) "$VERIFIER" "${ARGS[@]}" 30</dev/null 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err" ;;
    access) "$VERIFIER" "${ARGS[@]}" 30<>"$RUN/input/source" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err" ;;
    offset) { IFS= read -r -N 1 <&30 || true; "$VERIFIER" "${ARGS[@]}" >"$out" 2>"$err"; } 30<"$RUN/input/source" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" ;;
    object_alias) "$VERIFIER" "${ARGS[@]}" 30<"$RUN/input/source" 31<&30 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err" ;;
    leak) CA44_TEST_LEAK=000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f "$VERIFIER" "${ARGS[@]}" 30<"$RUN/input/source" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >"$out" 2>"$err" ;;
    full) "$VERIFIER" "${ARGS[@]}" 30<"$RUN/input/source" 31<"$RUN/input/artifact" 32<"$RUN/generated/detached.json" 33<"$RUN/generated/expectations.json" 34<"$RUN/input/artifact.key" 35<"$RUN/input/evidence.key" >/dev/full 2>"$err" ;;
    *) fail "unknown_case_mode:$mode" ;;
  esac
  rc=$?
  set -e
  [[ "$rc" -eq "$want_rc" ]] || fail "case_rc:$name:$rc"
  cmp -s -- "$err" "$expected_err" || fail "case_stderr:$name"
  if [[ "$mode" != full ]]; then [[ ! -s "$out" || "$want_rc" -eq 0 ]] || fail "case_stdout_not_empty:$name"; fi
  PASS_COUNT=$((PASS_COUNT+1))
}

DENY_USAGE='verifier=DENY reason=invalid_arguments'
DENY_IO='verifier=DENY reason=io_failure'
DENY_VERIFY='verifier=DENY reason=verification_denied'
invoke cli_alias 64 "$DENY_USAGE" alias
invoke cli_missing 64 "$DENY_USAGE" missing_cli
invoke fd_missing 74 "$DENY_IO" missing_fd
invoke non_root 78 "$DENY_VERIFY" nonroot
invoke fd_type 78 "$DENY_VERIFY" type

make_case_file() { install -m 0600 -o 0 -g 0 "$1" "$2"; }
make_case_file "$RUN/input/source" "$RUN/input/uid"; chown 1:1 "$RUN/input/uid"
saved_source="$RUN/input/source"; mv "$saved_source" "$RUN/input/source.good"; mv "$RUN/input/uid" "$RUN/input/source"; invoke fd_uid 78 "$DENY_VERIFY"; mv "$RUN/input/source" "$RUN/input/uid"; mv "$RUN/input/source.good" "$RUN/input/source"
make_case_file "$RUN/input/source" "$RUN/input/mode"; chmod 0640 "$RUN/input/mode"; mv "$RUN/input/source" "$RUN/input/source.good"; mv "$RUN/input/mode" "$RUN/input/source"; invoke fd_mode 78 "$DENY_VERIFY"; mv "$RUN/input/source" "$RUN/input/mode"; mv "$RUN/input/source.good" "$RUN/input/source"
ln "$RUN/input/source" "$RUN/input/source.link"; invoke fd_nlink 78 "$DENY_VERIFY"; rm "$RUN/input/source.link"
invoke fd_access 78 "$DENY_VERIFY" access
invoke fd_offset 78 "$DENY_VERIFY" offset

make_case_file "$RUN/input/source" "$RUN/input/source.good"; truncate -s $((16*1024*1024+1)) "$RUN/input/source"; invoke source_size_cap 78 "$DENY_VERIFY"; mv "$RUN/input/source.good" "$RUN/input/source"
make_case_file "$RUN/input/artifact" "$RUN/input/artifact.good"; truncate -s $((64*1024*1024+1)) "$RUN/input/artifact"; invoke artifact_size_cap 78 "$DENY_VERIFY"; mv "$RUN/input/artifact.good" "$RUN/input/artifact"
make_case_file "$RUN/generated/detached.json" "$RUN/generated/detached.good"; truncate -s $((64*1024+1)) "$RUN/generated/detached.json"; invoke detached_size_cap 78 "$DENY_VERIFY"; mv "$RUN/generated/detached.good" "$RUN/generated/detached.json"

make_case_file "$RUN/input/artifact.key" "$RUN/input/artifact.key.good"; truncate -s 31 "$RUN/input/artifact.key"; invoke key_short 78 "$DENY_VERIFY"; mv "$RUN/input/artifact.key.good" "$RUN/input/artifact.key"
make_case_file "$RUN/input/artifact.key" "$RUN/input/artifact.key.good"; truncate -s 33 "$RUN/input/artifact.key"; invoke key_long 78 "$DENY_VERIFY"; mv "$RUN/input/artifact.key.good" "$RUN/input/artifact.key"
invoke object_alias 78 "$DENY_VERIFY" object_alias
invoke key_exposure 78 "$DENY_VERIFY" leak
invoke golden_success 0 '' normal
cmp -s -- "$RUN/cases/golden_success.out" "$RUN/generated/expected-receipt.json" || fail golden_receipt_mismatch
invoke dev_full_write 74 "$DENY_IO" full

[[ "$IDENTITY_GATE" == PASSED ]] || fail test_child_before_identity_gate
exact_sha "$CONTRACT" "$CONTRACT_SHA" contract_final
exact_sha "$RUN/bin/verifier" "$EXPECTED_VERIFIER_SHA" copied_verifier_final
exact_sha "$RUN/bin/verifier.test" "$EXPECTED_TEST_SHA" copied_test_final
exact_sha "$RUN/input/source" 9d7957e43f1494aea7dd39c4cc2f442cc6096857008d578c55455f4588b1c1d8 golden_source_final
exact_sha "$RUN/input/artifact" 4563d10078318eec79c2d42947122b9e1af311990a87e44f928fad7471befc1c golden_artifact_final
set +e
(cd "$VERIFIER_SOURCE" && "$TEST_ELF" -test.run '^Test' -test.count=1) >"$RUN/cases/unit.out" 2>"$RUN/cases/unit.err"
unit_rc=$?
set -e
[[ "$unit_rc" -eq 0 ]] || fail verifier_test_elf_failed
PASS_COUNT=$((PASS_COUNT+1))

# Search all raw/hex/base64/base64url forms in public captures. Key files are excluded.
"$PYTHON_BIN" "$GENERATOR" scan-public --artifact-key "$RUN/input/artifact.key" \
  --evidence-key "$RUN/input/evidence.key" --path "$RUN/cases" --path "$RUN/generated" \
  >"$RUN/cases/secret-scan.out" 2>"$RUN/cases/secret-scan.err" || fail secret_material_in_public_capture
[[ ! -s "$RUN/cases/secret-scan.err" ]] || fail secret_scan_stderr
[[ -z "$(find "$RUN" -xdev -type f ! -perm 0600 ! -perm 0500 -print -quit)" ]] || fail file_mode_inventory_invalid

RUN_SAVED="$RUN"
cleanup || fail cleanup_failed
RUN=''
[[ ! -e "$RUN_SAVED" ]] || fail cleanup_residue
trap - EXIT HUP INT TERM
printf 'client_auth_00044_verifier_linux_root=PASS phase=A authorization=NONE synthetic_identity=1 arch=%s cases=%s db=NOT_CONNECTED network=NOT_USED ptrace_race=NOT_IMPLEMENTED syscall_injection=NOT_IMPLEMENTED true_partial_write=NOT_IMPLEMENTED\n' "$ARCH" "$PASS_COUNT"
