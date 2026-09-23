#!/usr/bin/env bash
# Preflight-only path trust mock. Production requires fd/openat2 traversal.
set -euo pipefail
umask 077

fail() { printf 'client_auth_00043_path_trust_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }

path_trust_preflight() {
  local target="$1" expected_uid="$2" expected_mode="$3"
  local component current mode canonical
  [[ "$target" == /* && "$target" != *'//'* && "$target" != */./* &&
     "$target" != */../* && "$target" != */. && "$target" != */.. ]] || return 10
  [[ -e "$target" && ! -L "$target" ]] || return 11

  current=''
  IFS='/' read -r -a components <<<"${target#/}"
  for component in "${components[@]}"; do
    [[ -n "$component" && "$component" != . && "$component" != .. ]] || return 12
    current="$current/$component"
    [[ ! -L "$current" ]] || return 13
    if [[ "$current" != "$target" ]]; then
      [[ -d "$current" && "$(stat -c '%u' "$current")" == "$expected_uid" ]] || return 14
      mode="$(stat -c '%a' "$current")"
      (( (8#$mode & 8#022) == 0 )) || return 15
    fi
  done

  canonical="$(readlink -e -- "$target")" || return 16
  [[ "$canonical" == "$target" ]] || return 17
  [[ -f "$target" && "$(stat -c '%u' "$target")" == "$expected_uid" ]] || return 18
  [[ "$(stat -c '%a' "$target")" == "$expected_mode" ]] || return 19
  [[ "$(stat -c '%h' "$target")" == 1 ]] || return 20
}

expect_pass() {
  local label="$1" path="$2" uid="$3" mode="$4"
  path_trust_preflight "$path" "$uid" "$mode" || fail "$label"
  PASS_COUNT=$((PASS_COUNT+1))
}

expect_deny() {
  local label="$1" path="$2" uid="$3" mode="$4"
  if path_trust_preflight "$path" "$uid" "$mode"; then fail "$label"; fi
  DENY_COUNT=$((DENY_COUNT+1))
}

TMP="$(mktemp -d "${TMPDIR:-/tmp}/client-auth-00043-path-trust.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT HUP INT TERM
UID_NOW="$(id -u)"
PASS_COUNT=0
DENY_COUNT=0
SKIP_COUNT=0

# All ancestors in the positive fixture are private and owned by the invoking
# test user. Production calls the same policy with expected_uid=0.
chmod 0700 "$TMP"
mkdir "$TMP/trusted"
chmod 0700 "$TMP/trusted"
printf 'trusted\n' >"$TMP/trusted/artifact"
chmod 0600 "$TMP/trusted/artifact"
expect_pass trusted_regular "$TMP/trusted/artifact" "$UID_NOW" 600

ln -s "$TMP/trusted/artifact" "$TMP/target-link"
if [[ -L "$TMP/target-link" ]]; then
  expect_deny target_symlink "$TMP/target-link" "$UID_NOW" 600
else
  SKIP_COUNT=$((SKIP_COUNT+1))
  printf 'client_auth_00043_path_trust_mock=SKIP vector=target_symlink reason=platform_symlink_emulation\n'
fi

ln -s "$TMP/trusted" "$TMP/ancestor-link"
if [[ -L "$TMP/ancestor-link" ]]; then
  expect_deny ancestor_symlink "$TMP/ancestor-link/artifact" "$UID_NOW" 600
else
  SKIP_COUNT=$((SKIP_COUNT+1))
  printf 'client_auth_00043_path_trust_mock=SKIP vector=ancestor_symlink reason=platform_symlink_emulation\n'
fi

mkdir "$TMP/world-writable"
chmod 1777 "$TMP/world-writable"
printf 'x\n' >"$TMP/world-writable/artifact"
chmod 0600 "$TMP/world-writable/artifact"
WORLD_MODE="$(stat -c '%a' "$TMP/world-writable")"
if (( (8#$WORLD_MODE & 8#022) != 0 )); then
  expect_deny sticky_world_writable_ancestor "$TMP/world-writable/artifact" "$UID_NOW" 600
else
  SKIP_COUNT=$((SKIP_COUNT+1))
  printf 'client_auth_00043_path_trust_mock=SKIP vector=sticky_world_writable_ancestor reason=platform_chmod_emulation\n'
fi

mkdir "$TMP/group-writable"
chmod 0770 "$TMP/group-writable"
printf 'x\n' >"$TMP/group-writable/artifact"
chmod 0600 "$TMP/group-writable/artifact"
GROUP_MODE="$(stat -c '%a' "$TMP/group-writable")"
if (( (8#$GROUP_MODE & 8#022) != 0 )); then
  expect_deny group_writable_ancestor "$TMP/group-writable/artifact" "$UID_NOW" 600
else
  SKIP_COUNT=$((SKIP_COUNT+1))
  printf 'client_auth_00043_path_trust_mock=SKIP vector=group_writable_ancestor reason=platform_chmod_emulation\n'
fi

printf 'x\n' >"$TMP/trusted/writable-artifact"
chmod 0660 "$TMP/trusted/writable-artifact"
if [[ "$(stat -c '%a' "$TMP/trusted/writable-artifact")" != 600 ]]; then
  expect_deny writable_target "$TMP/trusted/writable-artifact" "$UID_NOW" 600
else
  SKIP_COUNT=$((SKIP_COUNT+1))
  printf 'client_auth_00043_path_trust_mock=SKIP vector=writable_target reason=platform_chmod_emulation\n'
fi

printf 'x\n' >"$TMP/trusted/hardlinked-artifact"
chmod 0600 "$TMP/trusted/hardlinked-artifact"
if ln "$TMP/trusted/hardlinked-artifact" "$TMP/trusted/hardlink-peer" 2>/dev/null; then
  expect_deny hardlink_target "$TMP/trusted/hardlinked-artifact" "$UID_NOW" 600
else
  SKIP_COUNT=$((SKIP_COUNT+1))
  printf 'client_auth_00043_path_trust_mock=SKIP vector=hardlink reason=filesystem_unsupported\n'
fi

expect_deny relative_path "relative/artifact" "$UID_NOW" 600
expect_deny dotdot_path "$TMP/trusted/../trusted/artifact" "$UID_NOW" 600
expect_deny duplicate_separator "${TMP}//trusted/artifact" "$UID_NOW" 600

if [[ "$UID_NOW" == 0 ]] && chown 1 "$TMP/trusted/artifact" 2>/dev/null; then
  expect_deny unexpected_owner "$TMP/trusted/artifact" 0 600
  chown 0 "$TMP/trusted/artifact"
else
  SKIP_COUNT=$((SKIP_COUNT+1))
  printf 'client_auth_00043_path_trust_mock=SKIP vector=unexpected_owner reason=chown_unavailable\n'
fi

# A portable local test does not assume a writable second device. Production
# enforces equal st_dev before renameat2(RENAME_NOREPLACE).
SKIP_COUNT=$((SKIP_COUNT+1))
printf 'client_auth_00043_path_trust_mock=SKIP vector=cross_device reason=no_portable_second_device_fixture\n'

grep -Fq 'openat2()' \
  "$(cd "$(dirname "$0")/.." && pwd)/.ai-company/handoffs/client-auth-00043-pre-cic-cleanup-provenance-20260731.md" \
  || fail design_missing_openat2
grep -Fq '不宣称抵抗恶意或' \
  "$(cd "$(dirname "$0")/.." && pwd)/.ai-company/handoffs/client-auth-00043-pre-cic-cleanup-provenance-20260731.md" \
  || fail threat_boundary_missing

printf 'client_auth_00043_path_trust_mock=PASS positive=%s negative=%s skipped=%s preflight_only=true\n' \
  "$PASS_COUNT" "$DENY_COUNT" "$SKIP_COUNT"
