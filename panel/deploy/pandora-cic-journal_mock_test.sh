#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SOURCE="$ROOT/cmd/pandora-cic-journal/main_linux.go"
# Linux 实现按主题分在多个 *_linux.go 里（面板重构第 5 阶段 ②）：静态检查对整组源码做，
# 构建约束逐个文件要求，否定检查任一文件命中即失败。
SOURCES=("$ROOT"/cmd/pandora-cic-journal/*_linux.go)
TEST_SOURCE="$ROOT/cmd/pandora-cic-journal/main_linux_test.go"
AMD64="$ROOT/cmd/pandora-cic-journal/syscall_linux_amd64.go"
ARM64="$ROOT/cmd/pandora-cic-journal/syscall_linux_arm64.go"
UNSUPPORTED="$ROOT/cmd/pandora-cic-journal/main_unsupported.go"
README="$ROOT/cmd/pandora-cic-journal/README.md"

fail() {
  printf 'pandora_cic_journal_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

for path in "$SOURCE" "${SOURCES[@]}" "$TEST_SOURCE" "$AMD64" "$ARM64" "$UNSUPPORTED" "$README"; do
  [[ -f "$path" ]] || fail source_missing
done

for path in "${SOURCES[@]}"; do
  grep -Fq '//go:build linux && (amd64 || arm64)' "$path" || fail linux_build_tag_missing
done
grep -Fq 'linuxSYSOpenat2     = 437' "${SOURCES[@]}" || fail openat2_syscall_missing
grep -Fq 'resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks' "${SOURCES[@]}" ||
  fail openat2_resolution_policy_missing
grep -Fq 'syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|linuxONoFollow|linuxOCloExec' "${SOURCES[@]}" ||
  fail exclusive_stage_open_missing
grep -Fq 'rand.Reader' "${SOURCES[@]}" || fail csprng_missing
grep -Fq 'renameAt2NoReplace' "${SOURCES[@]}" || fail renameat2_missing
grep -Fq 'renameNoReplace' "${SOURCES[@]}" || fail rename_noreplace_missing
grep -Fq 'syscall.Fdatasync' "${SOURCES[@]}" || fail fdatasync_missing
grep -Fq 'journalFsync(journalFD)' "${SOURCES[@]}" ||
  fail directory_fsync_missing
[[ "$(cat "${SOURCES[@]}" | grep -Fc 'journalFsync(runFD)')" -ge 2 ]] ||
  fail source_directory_fsync_missing
if grep -Fq 'syscall.O_APPEND' "${SOURCES[@]}"; then
  fail mutable_append_present
fi
grep -Fq 'syscall.Flock(journalFD, syscall.LOCK_EX)' "${SOURCES[@]}" ||
  fail append_lock_missing
grep -Fq 'stat.Uid != 0' "${SOURCES[@]}" || fail root_owner_check_missing
grep -Fq 'stat.Mode&07777 != 0600' "${SOURCES[@]}" || fail exact_mode_check_missing
grep -Fq 'stat.Nlink != 1' "${SOURCES[@]}" || fail nlink_check_missing
grep -Fq 'sameFileIdentity' "${SOURCES[@]}" || fail dev_inode_recheck_missing
grep -Fq 'journal_root_device_mismatch' "${SOURCES[@]}" || fail root_device_binding_missing
grep -Fq 'expected_journal_sha256_mismatch' "${SOURCES[@]}" ||
  fail whole_file_sha_binding_missing
grep -Fq 'chainedDigest' "${SOURCES[@]}" || fail hash_chain_missing
grep -Fq 'manifestDigest' "${SOURCES[@]}" || fail immutable_manifest_missing
grep -Fq 'writeImmutableRecordAt' "${SOURCES[@]}" || fail immutable_record_publish_missing
grep -Fq 'recoverPublishedAppend' "${SOURCES[@]}" || fail post_rename_recovery_missing
grep -Fq 'journal_segment_publish_noreplace_failed' "${SOURCES[@]}" ||
  fail segment_noreplace_missing
grep -Fq 'journal_non_utf8' "${SOURCES[@]}" || fail utf8_rejection_missing
grep -Fq 'journal_cr_or_nul' "${SOURCES[@]}" || fail cr_nul_rejection_missing
grep -Fq 'journal_key_missing_duplicate_or_reordered' "${SOURCES[@]}" ||
  fail canonical_order_rejection_missing
grep -Fq 'journal_trailing_bytes_after_closed' "${SOURCES[@]}" ||
  fail terminal_trailing_bytes_rejection_missing
grep -Fq '"intent": {' "${SOURCES[@]}" || fail intent_schema_missing
grep -Fq '"catalog": {' "${SOURCES[@]}" || fail catalog_schema_missing
grep -Fq '"drop": {' "${SOURCES[@]}" || fail drop_schema_missing
grep -Fq '"close": {' "${SOURCES[@]}" || fail close_schema_missing
grep -Fq 'catalog_expected_hash_mismatch' "${SOURCES[@]}" ||
  fail catalog_intent_binding_missing
grep -Fq 'drop_transition_or_catalog_identity_invalid' "${SOURCES[@]}" ||
  fail drop_binding_missing
grep -Fq 'close_transition_invalid' "${SOURCES[@]}" || fail close_state_gate_missing
grep -Fq 'TestUnpublishedFaultsNeverCreateJournalSegment' "$TEST_SOURCE" ||
  fail fault_injection_test_missing
for vector in short-write enospc fdatasync file-fsync TestKillWindowsLeavePublishedRecordAbsentOrComplete TestDirectoryFsyncFailureHasIdempotentExactRecovery; do
  grep -Fq "$vector" "$TEST_SOURCE" || fail "fault_vector_${vector}_missing"
done
if grep -Fq '"os/exec"' "${SOURCES[@]}"; then
  fail exec_capability_present
fi
if grep -Eq '"(github\.com|golang\.org|gopkg\.in)/' "${SOURCES[@]}"; then
  fail non_standard_library_import_present
fi

GO_BIN="${PANDORA_CIC_JOURNAL_GO:-}"
if [[ -z "$GO_BIN" ]] && command -v go >/dev/null 2>&1; then
  GO_BIN="$(command -v go)"
fi
if [[ -z "$GO_BIN" || ! -x "$GO_BIN" ]]; then
  printf 'pandora_cic_journal_mock=PASS static=PASS build=NOT_RUN reason=go_unavailable runtime=NOT_RUN integration=NOT_RUN\n'
  exit 0
fi

export GOTOOLCHAIN=local
BUILD_TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-cic-journal-build.XXXXXX")"
trap 'rm -rf -- "$BUILD_TMP"' EXIT HUP INT TERM

"$GO_BIN" fmt -n ./cmd/pandora-cic-journal >"$BUILD_TMP/gofmt.commands"
if grep -Fq 'gofmt -w' "$BUILD_TMP/gofmt.commands"; then
  fail gofmt_required
fi
GOOS=linux GOARCH=amd64 "$GO_BIN" build -buildvcs=false -o "$BUILD_TMP/pandora-cic-journal-amd64" ./cmd/pandora-cic-journal ||
  fail linux_amd64_build
GOOS=linux GOARCH=amd64 "$GO_BIN" test -buildvcs=false -c -o "$BUILD_TMP/pandora-cic-journal-test-amd64" ./cmd/pandora-cic-journal ||
  fail linux_amd64_test_compile
GOOS=linux GOARCH=arm64 "$GO_BIN" build -buildvcs=false -o "$BUILD_TMP/pandora-cic-journal-arm64" ./cmd/pandora-cic-journal ||
  fail linux_arm64_build
GOOS=linux GOARCH=arm64 "$GO_BIN" test -buildvcs=false -c -o "$BUILD_TMP/pandora-cic-journal-test-arm64" ./cmd/pandora-cic-journal ||
  fail linux_arm64_test_compile

if [[ "$(uname -s)" != Linux || "$(id -u)" != 0 ]]; then
  printf 'pandora_cic_journal_mock=PASS static=PASS build=PASS test_compile=PASS architectures=amd64,arm64 runtime=NOT_RUN reason=linux_root_required integration=NOT_RUN\n'
  exit 0
fi

TMP="$(mktemp -d "/root/pandora-cic-journal.XXXXXX")"
trap 'rm -rf -- "$TMP" "$BUILD_TMP"' EXIT HUP INT TERM
chmod 0700 "$TMP"
mkdir "$TMP/run-00043"
chmod 0700 "$TMP/run-00043"
"$GO_BIN" build -buildvcs=false -o "$TMP/pandora-cic-journal" ./cmd/pandora-cic-journal ||
  fail native_build
"$GO_BIN" test -buildvcs=false ./cmd/pandora-cic-journal ||
  fail linux_fault_injection_tests

ROOT_DEV="$(stat -c '%d' -- "$TMP")"
ALLOWED_DEVS="$(
  printf '%s\n' \
    "$(stat -c '%d' -- /)" \
    "$(stat -c '%d' -- /root)" \
    "$ROOT_DEV" |
    sort -nu | paste -sd, -
)"
H1="$(printf '1%.0s' {1..64})"
H2="$(printf '2%.0s' {1..64})"
H3="$(printf '3%.0s' {1..64})"
H4="$(printf '4%.0s' {1..64})"
H5="$(printf '5%.0s' {1..64})"
H6="$(printf '6%.0s' {1..64})"
H7="$(printf '7%.0s' {1..64})"

CREATE_OUT="$("$TMP/pandora-cic-journal" create-intent \
  --journal-root "$TMP" --expected-root-device "$ROOT_DEV" --allow-devices "$ALLOWED_DEVS" \
  --run-id run-00043 --created-at-epoch 1 \
  --release-manifest-sha256 "$H1" --runner-sha256 "$H2" --migration-sha256 "$H3" \
  --source-system-identifier 100 --database-name aegis --database-oid 200 \
  --candidate U43-01 --table users --index-name uq_users_email \
  --expected-indexdef-sha256 "$H4" --expected-predicate-sha256 "$H5" \
  --expected-dependency-sha256 "$H6")" || fail create_intent
JOURNAL="$(awk '{for(i=1;i<=NF;i++) if($i ~ /^journal=/){sub(/^journal=/,"",$i); print $i}}' <<<"$CREATE_OUT")"
SHA="$(awk '{for(i=1;i<=NF;i++) if($i ~ /^journal_sha256=/){sub(/^journal_sha256=/,"",$i); print $i}}' <<<"$CREATE_OUT")"
[[ "$JOURNAL" =~ ^U43-01\.[0-9a-f]{64}\.journal$ && "$SHA" =~ ^[0-9a-f]{64}$ ]] ||
  fail create_output_invalid

CATALOG_OUT="$("$TMP/pandora-cic-journal" append-catalog \
  --journal-root "$TMP" --expected-root-device "$ROOT_DEV" --allow-devices "$ALLOWED_DEVS" \
  --run-id run-00043 --journal "$JOURNAL" --expect-journal-sha256 "$SHA" \
  --observed-at-epoch 2 --table-oid 300 --index-oid 400 --constraint-oid 0 \
  --catalog-sha256 "$H7" --indexdef-sha256 "$H4" --predicate-sha256 "$H5" \
  --dependency-sha256 "$H6" --classifier INVALID_EXACT)" || fail append_catalog
SHA="$(awk '{for(i=1;i<=NF;i++) if($i ~ /^journal_sha256=/){sub(/^journal_sha256=/,"",$i); print $i}}' <<<"$CATALOG_OUT")"

DROP_OUT="$("$TMP/pandora-cic-journal" append-drop \
  --journal-root "$TMP" --expected-root-device "$ROOT_DEV" --allow-devices "$ALLOWED_DEVS" \
  --run-id run-00043 --journal "$JOURNAL" --expect-journal-sha256 "$SHA" \
  --dropped-at-epoch 3 --index-oid 400 --catalog-sha256 "$H7" \
  --classifier REMOVED_EXACT)" || fail append_drop
SHA="$(awk '{for(i=1;i<=NF;i++) if($i ~ /^journal_sha256=/){sub(/^journal_sha256=/,"",$i); print $i}}' <<<"$DROP_OUT")"

CLOSE_OUT="$("$TMP/pandora-cic-journal" append-close \
  --journal-root "$TMP" --expected-root-device "$ROOT_DEV" --allow-devices "$ALLOWED_DEVS" \
  --run-id run-00043 --journal "$JOURNAL" --expect-journal-sha256 "$SHA" \
  --closed-at-epoch 4 --classifier REMOVED_EXACT)" || fail append_close
SHA="$(awk '{for(i=1;i<=NF;i++) if($i ~ /^journal_sha256=/){sub(/^journal_sha256=/,"",$i); print $i}}' <<<"$CLOSE_OUT")"

if "$TMP/pandora-cic-journal" append-close \
  --journal-root "$TMP" --expected-root-device "$ROOT_DEV" --allow-devices "$ALLOWED_DEVS" \
  --run-id run-00043 --journal "$JOURNAL" --expect-journal-sha256 "$SHA" \
  --closed-at-epoch 5 --classifier REMOVED_EXACT >/dev/null 2>&1; then
  fail duplicate_close_accepted
fi

printf 'pandora_cic_journal_mock=PASS static=PASS build=PASS test_compile=PASS architectures=amd64,arm64 runtime=PASS transitions=intent,catalog,drop,close faults=kill,enospc,short-write,fdatasync,fsync negative=duplicate_terminal integration=NOT_RUN\n'
