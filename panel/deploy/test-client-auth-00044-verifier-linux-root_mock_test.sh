#!/usr/bin/env bash
# Dependency-light static acceptance for the Phase-A Linux/root gate.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER="$ROOT/deploy/test-client-auth-00044-verifier-linux-root.sh"
MOCK="$ROOT/deploy/test-client-auth-00044-verifier-linux-root_mock_test.sh"
PY="$ROOT/deploy/client-auth-00044-verifier-gate.py"

fail() { printf 'client_auth_00044_verifier_linux_root_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }
PYTHON_BIN="${PYTHON_BIN:-python3}"
for command_name in bash grep awk uname cut; do command -v "$command_name" >/dev/null 2>&1 || fail "required_command_missing:$command_name"; done
command -v "$PYTHON_BIN" >/dev/null 2>&1 || fail required_command_missing:python3
[[ -f "$RUNNER" && -f "$MOCK" && -f "$PY" ]] || fail files_missing
bash -n "$RUNNER" || fail runner_bash_syntax
bash -n "$MOCK" || fail mock_bash_syntax
"$PYTHON_BIN" -c 'import pathlib,sys; compile(pathlib.Path(sys.argv[1]).read_bytes(), sys.argv[1], "exec")' "$PY" || fail python_compile

required_pins=(
  78fd468074f8ad528c2e406ae9fc83c8ae1f2da8c36b81eb386e8eeee38595c4
  05d3c32d50ef8b14355440d52a7f583d53e7c575d409310e5d027ede1ceed0b4
  b53cf46faacab6917df092f16939b76191620b2e5953e9bd848d2da062ca035f
  b2d51b492732d5b626fcb1707e21800c2e5981b706df876d3be8c8b8a47a99d2
  e35ac09c5de40b454241391a51d2cd2cc60cab2bb8e50359a54b9029020c1b90
  55dae658d4949fff6f9d1548f241e48c52041150e8bcf479086015a18ba57d94
  f9c0a1fe23593d6917f87987a5d458648d7bb80db9862ffad7eefa693e822628
  253c332c2670f64c1c65dc4f590702a20238b3eef2014e953f805ee1b0e779d8
  0d0d50a25353c3cb23a878d2788136c9eeada0016df677a9f55f9e99689c48f1
  2eef8a960b024869b35dd93a339f91dbfe7a46aa90895d5d97188382f8083969
  9d7957e43f1494aea7dd39c4cc2f442cc6096857008d578c55455f4588b1c1d8
  4563d10078318eec79c2d42947122b9e1af311990a87e44f928fad7471befc1c
)
for pin in "${required_pins[@]}"; do grep -Fqi "$pin" "$RUNNER" "$PY" || fail "pin_missing:$pin"; done

required_cases=(
  cli_alias cli_missing fd_missing non_root fd_type fd_uid fd_mode fd_nlink
  fd_access fd_offset source_size_cap artifact_size_cap detached_size_cap
  key_short key_long object_alias key_exposure golden_success dev_full_write
)
for case_name in "${required_cases[@]}"; do grep -Fq "$case_name" "$RUNNER" || fail "case_missing:$case_name"; done
for fd in 30 31 32 33 34 35; do grep -Eq "(^|[^0-9])${fd}(<|>)" "$RUNNER" || fail "deterministic_fd_missing:$fd"; done

grep -Fq 'IDENTITY_GATE=NOT_PASSED' "$RUNNER" || fail identity_gate_initial_state_missing
grep -Fq 'verifier_child_before_identity_gate' "$RUNNER" || fail identity_gate_runtime_guard_missing
first_invoke="$(grep -n '^invoke cli_alias' "$RUNNER" | cut -d: -f1)"
identity_pass="$(grep -n '^IDENTITY_GATE=PASSED' "$RUNNER" | cut -d: -f1)"
[[ "$identity_pass" -lt "$first_invoke" ]] || fail identity_gate_order_invalid
grep -Fq '/root/pandora-ca44-gates/phase-a.' "$RUNNER" || fail root_disposable_path_missing
grep -Fq 'install -m 0500' "$RUNNER" || fail elf_mode_missing
grep -Fq 'install -m 0600' "$RUNNER" || fail input_mode_missing
grep -Fq 'NON_AUTHORIZING_SYNTHETIC_V1' "$RUNNER" || fail synthetic_approval_missing
grep -Fq 'authorization=NONE' "$RUNNER" || fail non_authorizing_report_missing
grep -Fq 'never an authorizing, packageable, or release gate' "$RUNNER" || fail source_tree_only_boundary_missing
for limitation in ptrace_race syscall_injection true_partial_write; do
  grep -Fq "$limitation=NOT_IMPLEMENTED" "$RUNNER" || fail "limitation_missing:$limitation"
  ! grep -Eq "$limitation=(PASS|IMPLEMENTED)" "$RUNNER" || fail "limitation_false_pass:$limitation"
done

if grep -En '(^|[;&|[:space:]])(curl|wget|ssh|scp|psql|goose|nc|ncat|socat|docker|podman|kubectl)([[:space:]]|$)|/dev/tcp|https?://' "$RUNNER" "$PY"; then
  fail network_or_database_command_present
fi
if grep -En '^[[:space:]]*(import|from)[[:space:]]+(socket|subprocess|urllib|requests|psycopg|asyncpg)' "$PY"; then
  fail forbidden_python_dependency
fi
grep -Fq 'db=NOT_CONNECTED network=NOT_USED' "$RUNNER" || fail no_side_effect_report_missing
grep -Fq 'cmp -s -- "$RUN/cases/golden_success.out" "$RUN/generated/expected-receipt.json"' "$RUNNER" || fail byte_exact_receipt_compare_missing
grep -Fq 'cmp -s -- "$err" "$expected_err"' "$RUNNER" || fail byte_exact_stderr_compare_missing
grep -Fq "[[ ! -s \"\$out\" || \"\$want_rc\" -eq 0 ]]" "$RUNNER" || fail empty_stdout_denial_assertion_missing
grep -Fq '>"$RUN/cases/generator.out" 2>"$RUN/cases/generator.err"' "$RUNNER" || fail generator_capture_scan_scope_missing
grep -Fq 'scan-public --artifact-key' "$RUNNER" || fail complete_secret_scan_missing
grep -Fq 'setpriv --reuid=65534 --regid=65534 --clear-groups' "$RUNNER" || fail deterministic_nonroot_missing
grep -Fq '31<&30' "$RUNNER" || fail true_fd_object_alias_missing
! grep -Fq 'ln "$RUN/input/source" "$RUN/input/artifact"' "$RUNNER" || fail hardlink_alias_false_coverage_present
grep -Fq '(cd "$VERIFIER_SOURCE" && "$TEST_ELF"' "$RUNNER" || fail test_elf_cwd_missing
grep -Fq '"O_NOFOLLOW"' "$PY" || fail nofollow_input_open_missing
grep -Fq 'fd = os.open(path, flags)' "$PY" || fail same_fd_input_open_missing
grep -Fq 'os.fstat(fd)' "$PY" || fail same_fd_input_fstat_missing
grep -Fq 'def read_public_regular(path: Path)' "$PY" || fail same_fd_public_scan_missing
grep -Fq 'O_NONBLOCK' "$PY" || fail nonblocking_public_scan_missing
grep -Fq 'data[:] = b"\x00" * len(data)' "$PY" || fail public_scan_zeroization_missing
grep -Fq 'trap - EXIT HUP INT TERM' "$RUNNER" || fail cleanup_trap_fail_closed_missing
grep -Fq '[[ ! -e "$RUN" && ! -L "$RUN" ]]' "$RUNNER" || fail cleanup_residue_guard_missing
grep -Fq '[[ -d "$BASE" && ! -L "$BASE"' "$RUNNER" || fail base_symlink_guard_missing

not_run_probe=STATIC_ONLY_NATIVE_LINUX_ROOT
kernel="$(uname -s 2>/dev/null || printf unknown)"
release="$(uname -r 2>/dev/null || printf unknown)"
if [[ "$kernel" != Linux || "$EUID" -ne 0 || "$release" == *[Mm]icrosoft* || "$release" == *WSL* ]]; then
  set +e
  output="$(bash "$RUNNER" 2>&1)"
  rc=$?
  set -e
  [[ "$rc" -eq 77 ]] || fail "not_run_exit:$rc"
  [[ "$output" == 'client_auth_00044_verifier_linux_root=NOT_RUN reason=native_linux_root_required db=NOT_CONNECTED network=NOT_USED' ]] || fail not_run_output
  not_run_probe=PASS
fi

printf 'client_auth_00044_verifier_linux_root_mock=PASS syntax=PASS python_compile=PASS pins=%s cases=%s no_network_db=PASS not_run=%s dynamic_linux_root=NOT_RUN\n' \
  "${#required_pins[@]}" "${#required_cases[@]}" "$not_run_probe"
