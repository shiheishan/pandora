#!/usr/bin/env bash
set -euo pipefail

not_run() {
  printf 'client_auth_00044_root_runner_linux=NOT_RUN reason=%s pg18=NOT_RUN vps=NOT_RUN production=NOT_RUN\n' "$1" >&2
  exit 77
}

[[ "$(uname -s)" == "Linux" ]] || not_run linux_required
[[ "$(id -u)" -eq 0 ]] || not_run root_required

required=(
  PANDORA_CA44_RUNNER_BIN
  PANDORA_CA44_TRUST_ROOT
  PANDORA_CA44_MANIFEST_PATH
  PANDORA_CA44_MANIFEST_SHA256
  PANDORA_CA44_SIGNER_KEY_PATH
  PANDORA_CA44_SIGNER_KEY_SHA256
  PANDORA_CA44_CONTRACT_PATH
  PANDORA_CA44_CLASSIFIER_PATH
  PANDORA_CA44_VERIFIER_PATH
  PANDORA_CA44_SOURCE_PATH
  PANDORA_CA44_ARTIFACT_KEY_PATH
  PANDORA_CA44_ARTIFACT_KEY_SHA256
  PANDORA_CA44_EVIDENCE_KEY_PATH
  PANDORA_CA44_EVIDENCE_KEY_SHA256
  PANDORA_CA44_STAGING_ROOT
  PANDORA_CA44_PUBLISH_ROOT
)
for name in "${required[@]}"; do
  [[ -n "${!name:-}" ]] || not_run "missing_${name}"
done

case "$(uname -m)" in
  x86_64|aarch64) ;;
  *) not_run unsupported_architecture ;;
esac

[[ -f "$PANDORA_CA44_RUNNER_BIN" && ! -L "$PANDORA_CA44_RUNNER_BIN" ]] || not_run runner_missing
[[ "$(stat -c '%u:%a:%h' -- "$PANDORA_CA44_RUNNER_BIN")" == "0:500:1" ]] || not_run runner_identity

stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
cleanup() {
  rm -f -- "$stdout_file" "$stderr_file"
}
trap cleanup EXIT

set +e
env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C LC_ALL=C TZ=UTC \
  "$PANDORA_CA44_RUNNER_BIN" \
  -trust-root "$PANDORA_CA44_TRUST_ROOT" \
  -manifest-path "$PANDORA_CA44_MANIFEST_PATH" \
  -expected-manifest-sha256 "$PANDORA_CA44_MANIFEST_SHA256" \
  -signer-public-key-path "$PANDORA_CA44_SIGNER_KEY_PATH" \
  -approved-signer-key-sha256 "$PANDORA_CA44_SIGNER_KEY_SHA256" \
  -contract-path "$PANDORA_CA44_CONTRACT_PATH" \
  -classifier-path "$PANDORA_CA44_CLASSIFIER_PATH" \
  -verifier-path "$PANDORA_CA44_VERIFIER_PATH" \
  -source-path "$PANDORA_CA44_SOURCE_PATH" \
  -artifact-key-path "$PANDORA_CA44_ARTIFACT_KEY_PATH" \
  -artifact-key-sha256 "$PANDORA_CA44_ARTIFACT_KEY_SHA256" \
  -evidence-key-path "$PANDORA_CA44_EVIDENCE_KEY_PATH" \
  -evidence-key-sha256 "$PANDORA_CA44_EVIDENCE_KEY_SHA256" \
  -staging-root "$PANDORA_CA44_STAGING_ROOT" \
  -publish-root "$PANDORA_CA44_PUBLISH_ROOT" \
  -child-timeout "${PANDORA_CA44_CHILD_TIMEOUT:-30s}" \
  -wait-delay "${PANDORA_CA44_WAIT_DELAY:-1s}" \
  >"$stdout_file" 2>"$stderr_file"
code=$?
set -e

if [[ "$code" -ne 0 ]]; then
  tr -d '\000-\011\013-\037\177' <"$stderr_file" >&2 || true
  exit "$code"
fi
if [[ -s "$stderr_file" ]]; then
  printf 'client_auth_00044_root_runner_linux=DENY reason=unexpected_stderr\n' >&2
  exit 1
fi
receipt="$(tr -d '\r\n' <"$stdout_file")"
case "$receipt" in
  'root_runner=OK status=published authorization=NONE'|'root_runner=OK status=already_published authorization=NONE') ;;
  *) printf 'client_auth_00044_root_runner_linux=DENY reason=success_receipt_mismatch\n' >&2; exit 1 ;;
esac

printf 'client_auth_00044_root_runner_linux=PASS architecture=%s authorization=NONE pg18=NOT_RUN vps=NOT_RUN production=NOT_RUN\n' "$(uname -m)"
