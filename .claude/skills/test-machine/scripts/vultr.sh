#!/usr/bin/env bash
# Vultr API 最底层调用：vultr.sh <METHOD> <路径，如 /instances> [JSON 文件]，响应原样打到 stdout。
# 现场值（1Password 引用、地域、os、SSH key、VPC 的 id）在主目录 ops-local/vultr/env，不进仓库。
# 密钥：op read 读进变量，只经 builtin printf 走 stdin 给 curl -H @-，不进任何进程的命令行参数、不打印、不落盘。
set -euo pipefail
m="${1:?METHOD}"; p="${2:?路径}"; body="${3:-}"
here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")"
env="$root/ops-local/vultr/env"
[ -r "$env" ] || { echo "缺 $env（VULTR_OP_REF 等现场值）" >&2; exit 2; }
# shellcheck disable=SC1090
. "$env"
k="$(op read "$VULTR_OP_REF")" || { echo "op read 失败：1Password app 要解锁并开 CLI 集成；报 RequestDelegatedSession 多半是 app 连不上自家服务器（代理）" >&2; exit 2; }
args=(-sS -X "$m" -H @- -H 'Content-Type: application/json' "https://api.vultr.com/v2$p")
[ -n "$body" ] && args+=(--data @"$body")
printf 'Authorization: Bearer %s\n' "$k" | curl "${args[@]}"
