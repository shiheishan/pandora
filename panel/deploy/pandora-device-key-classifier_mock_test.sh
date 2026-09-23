#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PACKAGE='./cmd/pandora-device-key-classifier'
SOURCE_DIR="$ROOT/cmd/pandora-device-key-classifier"

fail() {
  printf 'pandora_device_key_classifier_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

need_literal() {
  local file="$1" literal="$2" reason="$3"
  grep -Fq -- "$literal" "$file" || fail "$reason"
}

reject_literal() {
  local file="$1" literal="$2" reason="$3"
  if grep -Fq -- "$literal" "$file"; then
    fail "$reason"
  fi
}

need_literal "$SOURCE_DIR/classifier.go" 'x509.ParsePKIXPublicKey' 'strict_x509_parse_missing'
need_literal "$SOURCE_DIR/classifier.go" 'x509.MarshalPKIXPublicKey' 'canonical_remarshal_missing'
need_literal "$SOURCE_DIR/classifier.go" 'bytes.Equal(remarshaled, der)' 'byte_equality_missing'
need_literal "$SOURCE_DIR/classifier.go" 'decoder.DisallowUnknownFields()' 'unknown_field_rejection_missing'
need_literal "$SOURCE_DIR/classifier.go" 'duplicate_tenant_fingerprint' 'tenant_duplicate_downgrade_missing'
need_literal "$SOURCE_DIR/classifier.go" 'classCrossTenant' 'cross_tenant_classification_missing'

need_literal "$SOURCE_DIR/main.go" 'func runWithPanicBoundary' 'panic_boundary_missing'
need_literal "$SOURCE_DIR/main.go" 'classifier=DENY reason=internal_failure' 'panic_denial_wire_missing'
need_literal "$SOURCE_DIR/main.go" '"-artifact-dir-fd"' 'artifact_directory_fd_missing'
need_literal "$SOURCE_DIR/main.go" '"-artifact-name"' 'artifact_name_flag_missing'
need_literal "$SOURCE_DIR/main.go" '"-artifact-hmac-key-id"' 'artifact_key_id_flag_missing'
need_literal "$SOURCE_DIR/main.go" '"-artifact-hmac-key-fd"' 'artifact_key_fd_flag_missing'
need_literal "$SOURCE_DIR/main.go" 'if err := writeRootOwnedArtifact' 'artifact_publication_missing'
need_literal "$SOURCE_DIR/main.go" 'if err := writeExact(stdout, detached)' 'exact_detached_write_missing'
need_literal "$SOURCE_DIR/main.go" 'classifier=DENY reason=detached_write_failed' 'detached_write_denial_missing'

need_literal "$SOURCE_DIR/classifier.go" 'github.com/aegispanel/aegis/internal/platform/clientauth/evidencecodec' 'framing_codec_import_missing'
need_literal "$SOURCE_DIR/classifier.go" 'evidencecodec.ComputeArtifactDigest' 'framed_artifact_hmac_missing'
need_literal "$SOURCE_DIR/classifier.go" 'pandora-client-auth-00044-classifier-detached-v1' 'detached_manifest_format_missing'
need_literal "$SOURCE_DIR/classifier.go" 'func classifySource(source []byte, format string)' 'key_independent_classifier_signature_missing'
need_literal "$SOURCE_DIR/classifier.go" 'detached_count_summary_invalid' 'detached_count_guard_missing'
need_literal "$SOURCE_DIR/classifier.go" "return append(wire, '\\n'), nil" 'single_lf_detached_missing'

need_literal "$SOURCE_DIR/classifier_test.go" 'TestGolden20ExistingClassifierArtifact' 'golden20_test_missing'
need_literal "$SOURCE_DIR/classifier_test.go" '811790829cc18e9a5d3fe9b94489e590ec006de8cc7dc530789dbeb5e6eb0c8d' 'detached_golden_sha_missing'
need_literal "$SOURCE_DIR/classifier_test.go" '17607946658785fb135d5fc8019c702c2dd3c2b8833c2e42f06c19b3c16a8606' 'framed_hmac_golden_missing'
need_literal "$SOURCE_DIR/classifier_test.go" '8342372b3b1905b43676c26fffe9a95349b1ae58b8e723cf6fcd4709d169d73b' 'legacy_raw_hmac_rejection_vector_missing'
need_literal "$SOURCE_DIR/classifier_test.go" 'TestClassifierCLIExactGrammar' 'exact_cli_test_missing'
need_literal "$SOURCE_DIR/classifier_test.go" '"legacy flag"' 'legacy_cli_rejection_test_missing'
need_literal "$SOURCE_DIR/classifier_test.go" 'TestClassifierCLIDoesNotUseEnvironmentFallback' 'environment_fallback_test_missing'
need_literal "$SOURCE_DIR/classifier_test.go" 'TestRunWithPanicBoundary' 'panic_boundary_test_missing'
need_literal "$SOURCE_DIR/classifier_test.go" 'TestWriteExact' 'exact_write_test_missing'

need_literal "$SOURCE_DIR/artifact_linux.go" 'unix.RENAME_NOREPLACE' 'atomic_noreplace_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" '0o600' 'private_mode_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" 'readRootOwnedSecretFD' 'inherited_key_fd_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" 'stat.Size != 32' 'exact_key_length_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" 'unix.Pread(fd, value, 0)' 'offset_independent_key_read_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" 'openFlags&unix.O_ACCMODE != unix.O_RDONLY' 'read_only_key_fd_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" 'dirStat.Mode&0o077 != 0' 'private_parent_directory_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" 'dirFlags&unix.O_DIRECTORY == 0' 'syncable_directory_fd_check_missing'
need_literal "$SOURCE_DIR/artifact_linux.go" 'dirFlags&unix.O_PATH != 0' 'opath_directory_fd_rejection_missing'

reject_literal "$SOURCE_DIR/main.go" '"-hmac-key-fd"' 'legacy_key_fd_flag_present'
reject_literal "$SOURCE_DIR/classifier.go" 'ManifestHMACSHA256' 'legacy_manifest_field_present'
reject_literal "$SOURCE_DIR/classifier.go" 'manifest_hmac_sha256' 'legacy_manifest_wire_present'
reject_literal "$SOURCE_DIR/classifier.go" 'legacy_raw_artifact_hmac_sha256' 'legacy_raw_hmac_wire_present'
reject_literal "$SOURCE_DIR/classifier.go" '"crypto/hmac"' 'direct_raw_hmac_dependency_present'
reject_literal "$SOURCE_DIR/classifier.go" 'hmac.New(' 'direct_raw_hmac_call_present'
reject_literal "$SOURCE_DIR/main.go" 'os.Open(' 'untrusted_input_path_open_present'

if grep -Rq -- 'hmac-key-file' "$SOURCE_DIR"; then
  fail 'key_path_interface_present'
fi
if grep -Eq 'Openat2|AT_FDCWD|filepath\.Clean' "$SOURCE_DIR/artifact_linux.go"; then
  fail 'artifact_path_traversal_present'
fi
if grep -REq '"(database/sql|github.com/jackc/pgx|crypto/rsa)"' "$SOURCE_DIR"/*.go; then
  fail 'forbidden_dependency_detected'
fi

if ! command -v go >/dev/null 2>&1; then
  printf 'pandora_device_key_classifier_mock=PASS static=PASS go_test=NOT_RUN go_vet=NOT_RUN gofmt=NOT_RUN reason=go_unavailable linux_crossbuild=NOT_RUN runtime=NOT_RUN integration=NOT_RUN native_linux_root=NOT_RUN production=NOT_RUN\n'
  exit 0
fi

GO_BIN_DIR="$(dirname "$(command -v go)")"
GOFMT="$GO_BIN_DIR/gofmt"
[[ -x "$GOFMT" ]] || fail 'gofmt_unavailable'

(cd "$ROOT" && GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -buildvcs=false -count=1 "$PACKAGE") \
  || fail 'go_test_failed'
(cd "$ROOT" && GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go vet -buildvcs=false "$PACKAGE") \
  || fail 'go_vet_failed'

format_diff="$($GOFMT -d \
  "$SOURCE_DIR/main.go" \
  "$SOURCE_DIR/classifier.go" \
  "$SOURCE_DIR/classifier_test.go" \
  "$SOURCE_DIR/artifact_linux.go" \
  "$SOURCE_DIR/artifact_other.go")"
[[ -z "$format_diff" ]] || fail 'gofmt_diff_present'

printf 'pandora_device_key_classifier_mock=PASS static=PASS go_test=PASS go_vet=PASS gofmt=PASS cases=four_classes,canonical_spki,tenant_scoped_conflict,cross_tenant_allowed,uuid_grammar,key_independent_artifact,framed_hmac,detached_golden,legacy_refusal,exact_cli,environment_refusal,panic_boundary,count_invariants,exact_stdout_write,atomic_root_artifact linux_crossbuild=NOT_RUN runtime=NOT_RUN integration=NOT_RUN native_linux_root=NOT_RUN production=NOT_RUN\n'
