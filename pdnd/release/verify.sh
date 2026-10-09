#!/usr/bin/env bash
set -Eeuo pipefail

# Verify a release directory produced by build.sh. This checks the artifact
# bytes and manifest metadata; it never treats file existence as acceptance.

RELEASE_DIR="${1:?usage: verify.sh <release-dir> [expected-version]}"
EXPECTED_VERSION="${2:-}"
MANIFEST="${RELEASE_DIR}/manifest.json"

[[ -f "${MANIFEST}" ]] || { echo "missing manifest: ${MANIFEST}" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required" >&2; exit 1; }

version="$(sed -n 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${MANIFEST}")"
[[ -n "${version}" ]] || { echo "manifest version missing" >&2; exit 1; }
runtime="$(sed -n 's/^[[:space:]]*"runtime"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${MANIFEST}")"
[[ "${runtime}" == "pandora-native" ]] || { echo "manifest runtime must be pandora-native" >&2; exit 1; }
native_only="$(sed -n 's/^[[:space:]]*"native_only"[[:space:]]*:[[:space:]]*\(true\|false\).*/\1/p' "${MANIFEST}")"
[[ "${native_only}" == "true" ]] || { echo "manifest native_only must be true" >&2; exit 1; }
# 构建用的 Go 工具链：manifest 里的 go_toolchain 必须与 "go version" 记的一致，
# 并等于本仓库 pdnd/go.mod 的 go 指令（找得到 go.mod 时）。本脚本没有 go.mod 可对照时只做前一项。
go_toolchain="$(sed -n 's/^[[:space:]]*"go_toolchain"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${MANIFEST}")"
go_version="$(sed -n 's/^[[:space:]]*"go"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${MANIFEST}")"
[[ -n "${go_toolchain}" ]] || { echo "manifest go_toolchain missing" >&2; exit 1; }
[[ "${go_version}" == "go version ${go_toolchain} "* ]] || { echo "manifest go (${go_version}) does not match go_toolchain (${go_toolchain})" >&2; exit 1; }
GO_MOD="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/go.mod"
if [[ -f "${GO_MOD}" ]]; then
  want="go$(tr -d '\r' < "${GO_MOD}" | awk '/^go[[:space:]]+[0-9]/ { print $2; exit }')"
  [[ "${go_toolchain}" == "${want}" ]] || { echo "go toolchain mismatch: manifest=${go_toolchain} go.mod=${want}" >&2; exit 1; }
fi
if [[ -n "${EXPECTED_VERSION}" && "${version}" != "${EXPECTED_VERSION}" ]]; then
  echo "version mismatch: manifest=${version} expected=${EXPECTED_VERSION}" >&2
  exit 1
fi

for name in \
  pandora-native-linux-amd64 \
  pandora-native-linux-arm64 \
  pandora-h3-probe-linux-amd64 \
  pandora-h3-probe-linux-arm64; do
  path="${RELEASE_DIR}/${name}"
  if [[ "${OSTYPE:-}" == linux* ]]; then
    [[ -x "${path}" ]] || { echo "missing or non-executable artifact: ${path}" >&2; exit 1; }
  else
    # Windows filesystems do not preserve Unix executable bits; Linux CI
    # performs the executable-mode gate above.
    [[ -f "${path}" ]] || { echo "missing artifact: ${path}" >&2; exit 1; }
  fi
  expected="$(awk -v name="${name}" 'index($0, "\"name\":\"" name "\"") { split($0, parts, "\"sha256\":\""); split(parts[2], hash, "\""); print hash[1]; exit }' "${MANIFEST}")"
  [[ "${#expected}" -eq 64 ]] || { echo "manifest hash missing for ${name}" >&2; exit 1; }
  actual="$(sha256sum "${path}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "hash mismatch for ${name}" >&2; exit 1; }
  if command -v file >/dev/null 2>&1 && [[ "${name}" == pandora-native-linux-* ]]; then
    description="$(file -b "${path}")"
    case "${name}" in
      *-amd64) [[ "${description}" == *"x86-64"* ]] || { echo "architecture mismatch for ${name}: ${description}" >&2; exit 1; } ;;
      *-arm64) [[ "${description}" == *"aarch64"* || "${description}" == *"ARM64"* ]] || { echo "architecture mismatch for ${name}: ${description}" >&2; exit 1; } ;;
    esac
  fi
  echo "verified ${name} ${actual}"
done

echo "release verified: version=${version}"
