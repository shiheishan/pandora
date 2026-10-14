#!/usr/bin/env bash
# Vultr API 最底层调用：vultr.sh <METHOD> <路径，如 /instances> [JSON 文件]，响应原样打到 stdout。
# 现场值（1Password 引用、地域、os、SSH key、VPC 的 id）在主目录 ops-local/vultr/env，不进仓库。
# 密钥：从 1P Environment 挂载文件或 op read 读进变量，只经 builtin printf 走 stdin 给 curl -H @-，不进任何进程的命令行参数、不打印、不落盘。
set -euo pipefail
m="${1:?METHOD}"; p="${2:?路径}"; body="${3:-}"
here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")"
env="$root/ops-local/vultr/env"
[ -r "$env" ] || { echo "缺 $env（VULTR_OP_REF 等现场值）" >&2; exit 2; }
# shellcheck disable=SC1090
. "$env"
# 优先读 1Password Environment「pandora-ops」挂载的 .env（用户 10-09 定改用 1P MCP；挂载点只在本机、是管道不落盘）；
# 没挂上或还是占位值（「待填」开头）时退回 op read。
k=""; mnt="$root/ops-local/1p/pandora-ops.env"
if [ -r "$mnt" ]; then
  k="$(sed -n 's/^VULTR_API_KEY=//p' "$mnt" | head -n1)"; k="${k%\"}"; k="${k#\"}"
  case "$k" in 待填*) k="" ;; esac
fi
if [ -z "$k" ]; then
  k="$(op read "$VULTR_OP_REF")" || { echo "取不到 Vultr 密钥：1Password Environment 挂载（$mnt）没有值，op read 也失败（app 要解锁并开 CLI 集成；RequestDelegatedSession 多半是代理）" >&2; exit 2; }
fi
args=(-sS -X "$m" -H @- -H 'Content-Type: application/json' "https://api.vultr.com/v2$p")
[ -n "$body" ] && args+=(--data @"$body")
printf 'Authorization: Bearer %s\n' "$k" | curl "${args[@]}"
