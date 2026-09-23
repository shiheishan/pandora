#!/usr/bin/env bash
set -Eeuo pipefail

# Reproducible, CGO-free Linux release builder for Pandora NativeCore.
# Run from any directory: ./release/build.sh [output-dir] [version].

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${1:-${ROOT_DIR}/release/dist}"
VERSION="${2:-dev}"
bash "${ROOT_DIR}/release/check_native_stdout.sh"
mkdir -p "${OUTPUT_DIR}"
# Resolve relative staging paths before the per-architecture build changes
# directory into ROOT_DIR; otherwise the output would be looked up under
# pdnd/ rather than at the caller's requested location.
OUTPUT_DIR="$(cd -- "${OUTPUT_DIR}" && pwd)"

if ! command -v go >/dev/null 2>&1; then
  echo "go is required" >&2
  exit 1
fi

# The default release must remain structurally NativeCore-only. This catches
# an accidental production import of the compatibility dispatcher even when
# the runtime configuration would usually reject it.
DEFAULT_DEPS="$(cd "${ROOT_DIR}" && go list -mod=readonly -deps .)"
if grep -Eq 'github.com/aegispanel/nodeagent/core/(multi|sing|xray)|github.com/sagernet/sing-box' <<<"${DEFAULT_DEPS}"; then
  echo "default release unexpectedly depends on compatibility core" >&2
  exit 1
fi
# Exercise the host build's registry self-check before producing either
# cross-compiled artifact. This catches capability/adapter drift even when the
# target binary cannot run on the current build host.
# 这里要 cd：脚本声称「从任何目录跑都行」，而这一行是唯一一个漏了 cd 的
# go 调用——同文件里 go list、go build 全都包在 (cd "${ROOT_DIR}" && ...)
# 里。从仓库根目录跑时它会报 "cannot find main module"，因为 go.mod 在
# pdnd/ 下面。本地一直是在 pdnd/ 里跑的，所以没露出来，直到进了 CI。
(cd "${ROOT_DIR}" && go run -mod=readonly . --self-check >/dev/null)

GO_VERSION="$(go version)"
COMMIT="unknown"
if command -v git >/dev/null 2>&1; then
  COMMIT="$(git -C "${ROOT_DIR}/.." rev-parse --verify HEAD 2>/dev/null || printf '%s' unknown)"
fi

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s' "${value}"
}

for arch in amd64 arm64; do
  target="${OUTPUT_DIR}/pandora-native-linux-${arch}"
  echo "building linux/${arch} -> ${target}"
  (cd "${ROOT_DIR}" && CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" \
    go build -mod=readonly -trimpath -ldflags "-s -w -X main.buildVersion=${VERSION}" \
    -o "${target}" .)
  chmod 0755 "${target}"
done

for arch in amd64 arm64; do
  target="${OUTPUT_DIR}/pandora-h3-probe-linux-${arch}"
  echo "building REALITY H3 probe linux/${arch} -> ${target}"
  (cd "${ROOT_DIR}" && CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" \
    go build -mod=readonly -trimpath -ldflags "-s -w" \
    -o "${target}" ./cmd/pandora-h3-probe)
  chmod 0755 "${target}"
done

{
  printf '{\n'
  printf '  "product": "pandora-native",\n'
  printf '  "runtime": "pandora-native",\n'
  printf '  "native_only": true,\n'
  printf '  "version": "'; json_escape "${VERSION}"; printf '",\n'
  printf '  "commit": "'; json_escape "${COMMIT}"; printf '",\n'
  printf '  "go": "'; json_escape "${GO_VERSION}"; printf '",\n'
  printf '  "cgo": false,\n'
  printf '  "artifacts": [\n'
  printf '    {"name":"pandora-native-linux-amd64","sha256":"%s"},\n' "$(sha256sum "${OUTPUT_DIR}/pandora-native-linux-amd64" | awk '{print $1}')"
  printf '    {"name":"pandora-native-linux-arm64","sha256":"%s"},\n' "$(sha256sum "${OUTPUT_DIR}/pandora-native-linux-arm64" | awk '{print $1}')"
  printf '    {"name":"pandora-h3-probe-linux-amd64","sha256":"%s"},\n' "$(sha256sum "${OUTPUT_DIR}/pandora-h3-probe-linux-amd64" | awk '{print $1}')"
  printf '    {"name":"pandora-h3-probe-linux-arm64","sha256":"%s"}\n' "$(sha256sum "${OUTPUT_DIR}/pandora-h3-probe-linux-arm64" | awk '{print $1}')"
  printf '  ]\n}\n'
} > "${OUTPUT_DIR}/manifest.json"

echo "release manifest: ${OUTPUT_DIR}/manifest.json"
