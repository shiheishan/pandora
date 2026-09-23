#!/usr/bin/env bash
set -Eeuo pipefail

# Safe release acceptance in an isolated prefix. This script never writes to
# /usr/local, /etc/systemd or /var; it is deliberately separate from the
# production installation commands in README.md.
# Usage: staging-acceptance.sh <release-dir> <stage-dir> [expected-version]

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASE_DIR="${1:?usage: staging-acceptance.sh <release-dir> <stage-dir> [expected-version]}"
STAGE_DIR="${2:?usage: staging-acceptance.sh <release-dir> <stage-dir> [expected-version]}"
EXPECTED_VERSION="${3:-}"

RELEASE_DIR="$(cd -- "${RELEASE_DIR}" && pwd)"
mkdir -p "${STAGE_DIR}"
STAGE_DIR="$(cd -- "${STAGE_DIR}" && pwd)"

bash "${ROOT_DIR}/release/verify.sh" "${RELEASE_DIR}" "${EXPECTED_VERSION}"

mkdir -p "${STAGE_DIR}/bin" "${STAGE_DIR}/etc/pandora-native" \
  "${STAGE_DIR}/var/lib/pandora-native" "${STAGE_DIR}/var/log/pandora-native"

arch="$(uname -m)"
case "${arch}" in
  x86_64|amd64) artifact="pandora-native-linux-amd64" ;;
  aarch64|arm64) artifact="pandora-native-linux-arm64" ;;
  *) echo "unsupported staging architecture: ${arch}" >&2; exit 1 ;;
esac

install -m 0755 "${RELEASE_DIR}/${artifact}" "${STAGE_DIR}/bin/pandora-native"
install -m 0644 "${ROOT_DIR}/release/pandora-native.service" \
  "${STAGE_DIR}/pandora-native.service"

"${STAGE_DIR}/bin/pandora-native" --self-check > "${STAGE_DIR}/self-check.json"
"${STAGE_DIR}/bin/pandora-native" --capabilities > "${STAGE_DIR}/capabilities.json"
grep -q '"status": "ok"' "${STAGE_DIR}/self-check.json" || {
  echo "staging self-check did not report status=ok" >&2
  exit 1
}
grep -q '"kernel": "pandora-native"' "${STAGE_DIR}/capabilities.json" || {
  echo "staging capability report is not Pandora NativeCore" >&2
  exit 1
}
grep -q '"native_only": true' "${STAGE_DIR}/capabilities.json" || {
  echo "staging capability report is not native_only=true" >&2
  exit 1
}

# Validate the exact unit that would be installed, but point every writable
# path at the isolated prefix. systemd-analyze is validation only; no unit is
# installed, enabled or started.
unit="${STAGE_DIR}/pandora-native-staging.service"
sed -e "s#^ExecStart=.*#ExecStart=${STAGE_DIR}/bin/pandora-native -c ${STAGE_DIR}/etc/pandora-native/config.json#" \
    -e "s#^User=.*#User=root#" \
    -e "s#^Group=.*#Group=root#" \
    -e "s#/var/lib/pandora-native#${STAGE_DIR}/var/lib/pandora-native#g" \
    -e "s#/var/log/pandora-native#${STAGE_DIR}/var/log/pandora-native#g" \
    "${STAGE_DIR}/pandora-native.service" > "${unit}"
if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze verify "${unit}"
fi

printf '{"status":"ok","artifact":"%s","stage":"%s","self_check":"%s","capabilities":"%s"}\n' \
  "${artifact}" "${STAGE_DIR}" "${STAGE_DIR}/self-check.json" "${STAGE_DIR}/capabilities.json"
