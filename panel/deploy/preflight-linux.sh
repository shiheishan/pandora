#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=platform.sh
. "$ROOT/deploy/platform.sh"

pandora_detect_platform

missing=0
for command_name in bash curl nginx age docker sha256sum install; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "缺少: $command_name" >&2
    missing=$((missing + 1))
  fi
done

if command -v docker >/dev/null 2>&1 && ! docker compose version >/dev/null 2>&1; then
  echo "缺少: Docker Compose v2 插件（docker compose）" >&2
  missing=$((missing + 1))
fi

if command -v docker >/dev/null 2>&1 && ! docker info >/dev/null 2>&1; then
  echo "异常: Docker daemon 不可用或当前用户无权访问" >&2
  missing=$((missing + 1))
fi

if [ "$missing" -ne 0 ]; then
  pandora_die "预检失败，共 $missing 项"
fi

echo "Pandora Panel Linux 预检通过"
echo "  distribution: $PANDORA_DISTRO $PANDORA_VERSION"
echo "  architecture: linux/$PANDORA_ARCH"
echo "  init: systemd"
echo "  docker: $(docker --version)"
echo "  compose: $(docker compose version --short)"

