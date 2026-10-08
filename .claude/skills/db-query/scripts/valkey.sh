#!/usr/bin/env bash
# 在面板机上带口令跑 valkey-cli（要 root：deploy/.env 是 0600）。口令只经环境变量 REDISCLI_AUTH，
# 不进命令行参数、不打印。从本机经 ssh 送过去跑：
#   ssh <别名> 'bash -s -- cli GET some:key' < valkey.sh
#   ssh <别名> 'bash -s -- rl'               < valkey.sh      # 列出全部限流键
#   ssh <别名> 'bash -s -- rl sub_rotate'    < valkey.sh      # 只列名字以 sub_rotate 开头的限流键
# 子命令：
#   cli ARGS...   原样传给 valkey-cli（只做读：GET、PTTL、TTL、--scan、INFO、DBSIZE；不要 DEL/FLUSH）
#   rl [前缀]     用 SCAN（不用 KEYS）列 rl:<前缀>*，每键给计数、PTTL 毫秒、「计数键过期前约剩几秒」；至多 LIMIT（缺省 200）个
set -euo pipefail
d=""
for x in /opt/aegispanel /opt/pandora; do [ -r "$x/deploy/.env" ] && { d=$x; break; }; done
[ -n "$d" ] || { echo "找不到 deploy/.env（试过 /opt/aegispanel 与 /opt/pandora）" >&2; exit 1; }
envv() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); sub(/\r$/,""); v=$0} END{print v}' "$d/deploy/.env"; }
REDISCLI_AUTH="$(envv VALKEY_PASSWORD)"; export REDISCLI_AUTH

# 脚本本身从标准输入来（bash -s）：每个子进程都显式 </dev/null，免得吃掉后面的脚本
vk() {
  if command -v docker >/dev/null 2>&1 && [ "$(docker inspect -f '{{.State.Running}}' aegis-valkey 2>/dev/null)" = true ]; then
    docker exec -e REDISCLI_AUTH aegis-valkey valkey-cli "$@" </dev/null
  else
    local cli=valkey-cli port
    command -v "$cli" >/dev/null 2>&1 || cli=redis-cli
    port="$(envv VALKEY_PORT)"
    "$cli" -h 127.0.0.1 -p "${port:-6379}" "$@" </dev/null
  fi
}

sub="${1:-}"; shift || true
case "$sub" in
  cli) vk "$@" ;;
  rl)
    # 键里可能含空格或冒号：逐行读，不做词分割
    vk --scan --pattern "rl:${1:-}*" | head -n "${LIMIT:-200}" | while IFS= read -r k; do
      [ -z "$k" ] && continue
      n="$(vk GET "$k")"; ttl="$(vk PTTL "$k")"
      printf '%s\tcount=%s\tpttl_ms=%s\tretry_after_s≈%s\n' "$k" "$n" "$ttl" "$(( ttl > 1000 ? (ttl - 1000) / 1000 : 0 ))"
    done || true ;;
  *) echo "用法：bash -s -- cli <valkey-cli 参数...> | rl [键名前缀]" >&2; exit 2 ;;
esac
