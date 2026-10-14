#!/usr/bin/env bash
# 把 Go 的 protocol.Schemas()（经 node/handler 的响应 DTO，与 GET /v1/node-protocol-schemas 同形）导出成前端假后端的 node-schemas.ts。
# 用法：export-schemas.sh [--repo <仓库或 worktree 根>] [输出文件]
#   不给输出文件时写到 <仓库>/panel/frontend/dev/mock/admin/node-schemas.ts（覆盖，之后用 git diff 看变化）
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo="$(git -C "$here" rev-parse --show-toplevel)"
if [ "${1:-}" = "--repo" ]; then repo="$(cd "$2" && pwd)"; shift 2; fi
dest="${1:-$repo/panel/frontend/dev/mock/admin/node-schemas.ts}"
dest="$(cd "$(dirname "$dest")" && pwd)/$(basename "$dest")"
source "$here/scripts/toolchain.sh"
pin_go_toolchain "$repo"
ov="$(mktemp -d "${TMPDIR:-/tmp}/schemas-overlay.XXXXXX")"
trap 'rm -rf "$ov"' EXIT
printf '{"Replace":{"%s":"%s"}}\n' \
  "$repo/panel/internal/module/node/handler/zz_e2e_schemas_test.go" "$here/overlay/zz_e2e_schemas_test.go" > "$ov/overlay.json"
rc=0
(cd "$repo/panel" && SCHEMA_OUT="$dest" go test -overlay "$ov/overlay.json" -count=1 -run '^TestE2EExportSchemas$' ./internal/module/node/handler/ >"$ov/test.log" 2>&1) || rc=$?
[ "$rc" = 0 ] || { tail -30 "$ov/test.log" >&2; echo "导出失败（退出 $rc）" >&2; exit 1; }
echo "已写 $dest"
