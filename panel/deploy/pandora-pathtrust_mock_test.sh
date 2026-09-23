#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LINUX_SOURCE="$ROOT/cmd/pandora-pathtrust/main_linux.go"
OTHER_SOURCE="$ROOT/cmd/pandora-pathtrust/main_unsupported.go"
README="$ROOT/cmd/pandora-pathtrust/README.md"

fail() {
  printf 'pandora_pathtrust_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}

[[ -f "$LINUX_SOURCE" && -f "$OTHER_SOURCE" && -f "$README" ]] ||
  fail source_missing

grep -Fq '//go:build linux && (amd64 || arm64)' "$LINUX_SOURCE" || fail linux_build_tag_missing
grep -Fq 'linuxSYSOpenat2       = 437' "$LINUX_SOURCE" || fail openat2_syscall_missing
grep -Fq 'resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks' "$LINUX_SOURCE" ||
  fail openat2_resolve_policy_missing
grep -Fq 'syscall.Openat(parentFD, name, flags, 0)' "$LINUX_SOURCE" ||
  fail component_openat_fallback_missing
grep -Fq 'linuxONoFollow' "$LINUX_SOURCE" || fail nofollow_missing
grep -Fq 'stat.Uid != 0' "$LINUX_SOURCE" || fail root_owner_policy_missing
[[ "$(grep -Fc 'stat.Mode&0022 != 0' "$LINUX_SOURCE")" -eq 2 ]] ||
  fail writable_policy_incomplete
grep -Fq 'stat.Mode&syscall.S_IFMT != syscall.S_IFREG' "$LINUX_SOURCE" ||
  fail regular_file_policy_missing
grep -Fq 'stat.Nlink != 1' "$LINUX_SOURCE" || fail hardlink_policy_missing
grep -Fq 'subtle.ConstantTimeCompare' "$LINUX_SOURCE" || fail digest_compare_missing
grep -Fq 'path_hex=%s' "$LINUX_SOURCE" || fail shell_safe_output_missing
grep -Fq 'trustedArtifactFDPath = "/proc/self/fd/3"' "$LINUX_SOURCE" ||
  fail fd_exec_missing
grep -Fq 'cmd.ExtraFiles = []*os.File{targetFile}' "$LINUX_SOURCE" ||
  fail trusted_fd_not_inherited
grep -Fq 'PANDORA_TRUSTED_CHAIN_SHA256=' "$LINUX_SOURCE" ||
  fail trusted_chain_not_exported
grep -Fq 'PANDORA_TRUSTED_DEVICE=' "$LINUX_SOURCE" ||
  fail trusted_device_not_exported
grep -Fq 'PANDORA_TRUSTED_MODE=' "$LINUX_SOURCE" ||
  fail trusted_mode_not_exported
grep -Fq 'trustedChildEnv(os.Environ())' "$LINUX_SOURCE" ||
  fail trusted_environment_not_sanitized
grep -Fq 'exec.Command("/bin/bash", commandArgs...)' "$LINUX_SOURCE" ||
  fail fixed_bash_missing
grep -Fq 'openat2_required_for_exec' "$LINUX_SOURCE" ||
  fail production_openat2_gate_missing
grep -Fq '*expectedMode != "0500"' "$LINUX_SOURCE" ||
  fail production_mode_gate_missing
grep -Fq 'device_mismatch' "$LINUX_SOURCE" ||
  fail device_gate_missing
grep -Fq 'ancestor_device_not_allowed' "$LINUX_SOURCE" ||
  fail ancestor_device_gate_missing
grep -Fq 'chain_sha256_mismatch' "$LINUX_SOURCE" ||
  fail ancestor_chain_gate_missing
grep -Fq 'target_changed_while_hashing' "$LINUX_SOURCE" ||
  fail post_hash_identity_gate_missing
grep -Fq 'const clientAuthPrefix = "PANDORA_CLIENT_AUTH_00043_"' "$LINUX_SOURCE" ||
  fail environment_allowlist_missing
if grep -Fq 'filtered := make([]string, 0, len(source))' "$LINUX_SOURCE"; then
  fail inherited_environment_present
fi
grep -Fq 'Linux `amd64` and `arm64`' "$README" || fail architecture_contract_missing

if ! command -v go >/dev/null 2>&1; then
  printf 'pandora_pathtrust_mock=PASS static=PASS build=NOT_RUN reason=go_unavailable runtime=NOT_RUN\n'
  exit 0
fi

export GOTOOLCHAIN=local
BUILD_TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pathtrust-build.XXXXXX")"
trap 'rm -rf -- "$BUILD_TMP"' EXIT HUP INT TERM
GOOS=linux GOARCH=amd64 go build -o "$BUILD_TMP/pandora-pathtrust-amd64" ./cmd/pandora-pathtrust ||
  fail linux_amd64_build
GOOS=linux GOARCH=arm64 go build -o "$BUILD_TMP/pandora-pathtrust-arm64" ./cmd/pandora-pathtrust ||
  fail linux_arm64_build

if [[ "$(uname -s)" != Linux || "$(id -u)" != 0 ]]; then
  printf 'pandora_pathtrust_mock=PASS static=PASS build=PASS architectures=amd64,arm64 runtime=NOT_RUN reason=linux_root_required\n'
  exit 0
fi

TMP="$(mktemp -d "/root/pandora-pathtrust.XXXXXX")"
trap 'rm -rf -- "$TMP" "$BUILD_TMP"' EXIT HUP INT TERM
chmod 0700 "$TMP"
mkdir "$TMP/trusted"
chmod 0700 "$TMP/trusted"
printf '#!/bin/bash\n[[ -z ${BASH_ENV:-} ]] || exit 91\n[[ "$PATH" == /usr/sbin:/usr/bin:/sbin:/bin ]] || exit 92\n[[ ${PANDORA_TRUSTED_CHAIN_SHA256:-} =~ ^[0-9a-f]{64}$ ]] || exit 93\n[[ ${PANDORA_TRUSTED_DEVICE:-} =~ ^[1-9][0-9]*$ ]] || exit 94\n[[ ${PANDORA_TRUSTED_MODE:-} == 0500 ]] || exit 95\nexit 23\n' >"$TMP/trusted/tool"
chmod 0500 "$TMP/trusted/tool"

go build -o "$TMP/pandora-pathtrust" ./cmd/pandora-pathtrust ||
  fail native_build
TOOL_SHA="$(sha256sum -- "$TMP/trusted/tool" | awk '{print $1}')"
TOOL_DEV="$(stat -c '%d' -- "$TMP/trusted/tool")"
ALLOWED_DEVS="$(
  printf '%s\n' \
    "$(stat -c '%d' -- /)" \
    "$(stat -c '%d' -- /root)" \
    "$(stat -c '%d' -- "$TMP")" \
    "$(stat -c '%d' -- "$TMP/trusted")" \
    "$TOOL_DEV" |
    sort -nu | paste -sd, -
)"
CHECK_OUTPUT="$("$TMP/pandora-pathtrust" check --path "$TMP/trusted/tool" \
  --expect-sha256 "$TOOL_SHA" --expect-mode 0500 --expect-device "$TOOL_DEV" \
  --allow-devices "$ALLOWED_DEVS")" ||
  fail trusted_file_denied
CHAIN_SHA="$(awk '{for(i=1;i<=NF;i++) if($i ~ /^chain_sha256=/){sub(/^chain_sha256=/,"",$i); print $i}}' <<<"$CHECK_OUTPUT")"
[[ "$CHAIN_SHA" =~ ^[0-9a-f]{64}$ ]] || fail chain_sha_missing

mkdir "$TMP/malicious-path"
printf '#!/bin/sh\nexit 96\n' >"$TMP/malicious-path/bash"
chmod 0700 "$TMP/malicious-path/bash"
printf 'exit 97\n' >"$TMP/malicious-bash-env"
chmod 0600 "$TMP/malicious-bash-env"

set +e
BASH_ENV="$TMP/malicious-bash-env" PATH="$TMP/malicious-path" \
  "$TMP/pandora-pathtrust" exec --path "$TMP/trusted/tool" \
  --expect-sha256 "$TOOL_SHA" --expect-mode 0500 --expect-device "$TOOL_DEV" \
  --allow-devices "$ALLOWED_DEVS" --expect-chain-sha256 "$CHAIN_SHA" -- >/dev/null 2>&1
child_status=$?
set -e
[[ "$child_status" -eq 23 ]] || fail child_exit_not_propagated

set +e
missing_digest_output="$("$TMP/pandora-pathtrust" exec --path "$TMP/trusted/tool" \
  --expect-mode 0500 --expect-device "$TOOL_DEV" --allow-devices "$ALLOWED_DEVS" \
  --expect-chain-sha256 "$CHAIN_SHA" -- 2>&1)"
missing_digest_status=$?
set -e
[[ "$missing_digest_status" -eq 64 &&
   "$missing_digest_output" == *"exec requires --expect-sha256"* ]] ||
  fail exec_without_digest_not_usage_denied
if "$TMP/pandora-pathtrust" check --path "$TMP/trusted/tool" \
  --expect-sha256 "$(printf '0%.0s' {1..64})" --expect-mode 0500 \
  --expect-device "$TOOL_DEV" --allow-devices "$ALLOWED_DEVS" >/dev/null 2>&1; then
  fail digest_mismatch_accepted
fi
if "$TMP/pandora-pathtrust" check --path "$TMP/trusted/tool" \
  --expect-sha256 "$TOOL_SHA" --expect-mode 0500 --expect-device "$TOOL_DEV" \
  --allow-devices "$ALLOWED_DEVS" \
  --expect-chain-sha256 "$(printf '0%.0s' {1..64})" >/dev/null 2>&1; then
  fail chain_mismatch_accepted
fi

ln -s "$TMP/trusted/tool" "$TMP/trusted/tool-link"
if "$TMP/pandora-pathtrust" check --path "$TMP/trusted/tool-link" >/dev/null 2>&1; then
  fail target_symlink_accepted
fi

chmod 0770 "$TMP/trusted"
if "$TMP/pandora-pathtrust" check --path "$TMP/trusted/tool" >/dev/null 2>&1; then
  fail writable_ancestor_accepted
fi
chmod 0700 "$TMP/trusted"

chmod 0700 "$TMP/trusted/tool"
if "$TMP/pandora-pathtrust" check --path "$TMP/trusted/tool" \
  --expect-sha256 "$TOOL_SHA" --expect-mode 0500 --expect-device "$TOOL_DEV" \
  --allow-devices "$ALLOWED_DEVS" >/dev/null 2>&1; then
  fail wrong_mode_accepted
fi
chmod 0500 "$TMP/trusted/tool"

ln "$TMP/trusted/tool" "$TMP/trusted/tool-hardlink"
if "$TMP/pandora-pathtrust" check --path "$TMP/trusted/tool" >/dev/null 2>&1; then
  fail hardlink_accepted
fi

printf 'pandora_pathtrust_mock=PASS static=PASS build=PASS architectures=amd64,arm64 runtime=PASS negative=env_injection,digest,chain,mode,symlink,writable_ancestor,hardlink\n'
