#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET_DIR="${ROOT_DIR}/internal/reality"

# Native REALITY may return structured errors to its caller, but it must never
# emit per-handshake diagnostics or derived authentication material directly to
# stdout. Keep this fail-closed so a future debug trace cannot silently become
# a production log flood.
if grep -R -nE '(^|[^[:alnum:]_])(fmt\.(Print|Printf|Println)|log\.(Print|Printf|Println))[[:space:]]*\(' \
  --include='*.go' --exclude='generate_cert.go' "${TARGET_DIR}"; then
  echo "native REALITY stdout print detected" >&2
  exit 1
fi

echo "NATIVE_REALITY_STDOUT_SILENT_OK"
