#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLI="$ROOT/cmd/pandora-release-journal/main_linux.go"
SOURCE="$ROOT/internal/platform/releasejournal/store_linux.go"
SESSION="$ROOT/internal/platform/releasejournal/session_linux.go"
MODEL="$ROOT/internal/platform/releasejournal/model.go"
TEST="$ROOT/internal/platform/releasejournal/model_test.go"
LINUX_TEST="$ROOT/internal/platform/releasejournal/store_linux_test.go"
README="$ROOT/cmd/pandora-release-journal/README.md"

fail() { printf 'pandora_release_journal_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }
for path in "$CLI" "$SOURCE" "$SESSION" "$MODEL" "$TEST" "$LINUX_TEST" "$README"; do [[ -f "$path" ]] || fail source_missing; done

grep -Fq '//go:build linux && (amd64 || arm64)' "$SOURCE" || fail linux_build_tag_missing
grep -Fq 'releasejournal.RunCLI' "$CLI" || fail thin_cli_adapter_missing
grep -Fq 'resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks' "$SOURCE" || fail openat2_policy_missing
grep -Fq 'syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|linuxONoFollow|linuxOCloExec' "$SOURCE" || fail exclusive_create_missing
grep -Fq 'renameAt2NoReplace' "$SOURCE" || fail rename_noreplace_missing
grep -Fq 'syscall.Flock(journalFD, syscall.LOCK_EX)' "$SOURCE" || fail journal_lock_missing
grep -Fq 'recoverAdvance' "$SOURCE" || fail exact_retry_missing
grep -Fq 'samePreparedRequest' "$SOURCE" || fail prepare_recovery_missing
grep -Fq 'journalStageRE' "$SOURCE" || fail strict_stage_grammar_missing
grep -Fq 'journalTempRE' "$SOURCE" || fail strict_temp_grammar_missing
grep -Fq 'release_stage_limit_exceeded' "$SOURCE" || fail stage_limit_missing
grep -Fq 'stat.Uid != 0' "$SOURCE" || fail owner_check_missing
grep -Fq 'stat.Mode&07777 != 0600' "$SOURCE" || fail mode_check_missing
grep -Fq 'stat.Nlink != 1' "$SOURCE" || fail link_check_missing
grep -Fq 'func OpenV2Session' "$SESSION" || fail retained_v2_session_missing
grep -Fq 'func (s *Session) AdvanceAdmissionAttempted' "$SESSION" || fail fixed_admission_advance_missing
grep -Fq 'journal_retained_binding_changed' "$SESSION" || fail retained_binding_check_missing
grep -Fq 'CA42-JOURNAL-EVENT' "$SESSION" || fail v2_event_domain_missing
grep -Fq 'stateRecoveryRequired' "$MODEL" || fail recovery_state_missing
grep -Fq 'bytes_after_terminal_state' "$MODEL" || fail terminal_tail_rejection_missing
grep -Fq 'TestAdvanceCASExactRetryDivergenceAndPostRenameRecovery' "$LINUX_TEST" || fail linux_recovery_test_missing
grep -Fq 'TestStrictTemporaryResidueDoesNotBlockOtherAttempt' "$LINUX_TEST" || fail temp_residue_test_missing
if grep -Eq '"os/exec"|"net"|"database/sql"' "$SOURCE" "$SESSION"; then fail execution_or_network_capability_present; fi
if grep -Eq 'syscall\.(Unlink|Rmdir)|os\.(Remove|RemoveAll)' "$SOURCE" "$SESSION"; then fail delete_capability_present; fi

GO_BIN="${PANDORA_RELEASE_JOURNAL_GO:-}"
if [[ -z "$GO_BIN" ]] && command -v go >/dev/null 2>&1; then GO_BIN="$(command -v go)"; fi
if [[ -z "$GO_BIN" || ! -x "$GO_BIN" ]]; then
  printf 'pandora_release_journal_mock=PASS static=PASS build=NOT_RUN reason=go_unavailable runtime=NOT_RUN\n'
  exit 0
fi

export GOTOOLCHAIN=local
BUILD_TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-release-journal-build.XXXXXX")"
trap 'rm -rf -- "$BUILD_TMP"' EXIT HUP INT TERM
"$GO_BIN" test -buildvcs=false ./internal/platform/releasejournal ./cmd/pandora-release-journal || fail model_tests
GOOS=linux GOARCH=amd64 "$GO_BIN" test -buildvcs=false -c -o "$BUILD_TMP/test-amd64" ./internal/platform/releasejournal || fail amd64_test_compile
GOOS=linux GOARCH=arm64 "$GO_BIN" test -buildvcs=false -c -o "$BUILD_TMP/test-arm64" ./internal/platform/releasejournal || fail arm64_test_compile

printf 'pandora_release_journal_mock=PASS static=PASS model=PASS test_compile=PASS architectures=amd64,arm64 runtime=NOT_RUN reason=linux_root_fault_gate_pending integration=NOT_RUN\n'
