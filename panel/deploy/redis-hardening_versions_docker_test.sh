#!/usr/bin/env bash
# install.sh 写进 Redis 配置的 pandora 块，在支持的几个版本上真起一遍（用官方镜像，要 Docker，只在 GitHub 上跑）：
#   Redis 6.0（Ubuntu 22.04，支持的最低版本，不认 bind 的「-」前缀）回环有 IPv6 与没有两种、
#   Redis 7.0（Debian 12、Ubuntu 24.04）、Redis 8.0（Debian 13）。每个镜像真实的 --version 先过 install-lib.sh 的
#   pandora_check_redis_version；bind 那一行由 pandora_redis_bind_addrs 按版本与 /proc/net/if_inet6 生成，
#   配置块由 pandora_set_redis_block、口令由 pandora_set_redis_password 写进一份 Debian 风格的配置；起来之后核：
#   口令生效、FLUSHALL / FLUSHDB 已禁、不做 RDB 快照、AOF 开且每秒刷盘、maxmemory 256mb 与 noeviction、bind 与生成的一致。
#   反证：Redis 6.0 硬写 `bind 127.0.0.1 -::1` 必须起不来；不是 Redis 的服务端（--version 不以 Redis 开头）被版本核对拒绝。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-redis-versions.XXXXXX")"
cids=()
cleanup() { [ "${#cids[@]}" -eq 0 ] || docker rm -f "${cids[@]}" >/dev/null 2>&1 || true; rm -rf -- "$T"; }
trap cleanup EXIT
fail() { printf 'redis versions: %s\n' "$*" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || fail 'needs docker'

# 沙箱前缀只为让 IF_INET6 指到临时目录里的 /proc/net/if_inet6
mkdir -p "$T/sb/proc/net" "$T/bin"
export PANDORA_INSTALL_SANDBOX="$T/sb"
# shellcheck source=install-lib.sh
. "$DEPLOY/install-lib.sh"
set -euo pipefail
PW=redis-versions-fixture

# 把镜像里 redis-server --version 的真实输出做成桩，让库函数按这个版本判断
stub_version() {
  printf '#!/usr/bin/env bash\nprintf "%%s\\n" %q\n' "$1" >"$T/bin/redis-server"
  chmod 0755 "$T/bin/redis-server"
}

# <镜像> <IPv6：on|off> [硬写的 bind]：起来返回 0 并核行为；起不来返回 1
run_case() {
  local image="$1" v6="$2" force_bind="${3:-}" ver bind conf cid i pong sysctl
  sysctl=(--sysctl net.ipv6.conf.all.disable_ipv6=1)
  [ "$v6" = off ] || sysctl=(--sysctl net.ipv6.conf.all.disable_ipv6=0 --sysctl net.ipv6.conf.lo.disable_ipv6=0)
  ver="$(docker run --rm "$image" redis-server --version)"
  docker run --rm --network none "${sysctl[@]}" "$image" cat /proc/net/if_inet6 >"$IF_INET6" 2>/dev/null || : >"$IF_INET6"
  if [ "$v6" = on ]; then
    grep -q '^00000000000000000000000000000001 .* lo$' "$IF_INET6" || fail "no IPv6 loopback in the $image container although IPv6 was enabled"
  fi
  stub_version "$ver"
  ( PATH="$T/bin:$PATH"; pandora_check_redis_version ) >/dev/null || fail "$image ($ver) is refused by the minimum-version check"
  bind="$(PATH="$T/bin:$PATH" pandora_redis_bind_addrs)"
  [ -z "$force_bind" ] || bind="$force_bind"
  conf="$T/redis-$v6.conf"
  # Debian 包配置里与块相冲的几行：块在末尾，应以块为准
  printf 'port 6379\nbind 127.0.0.1 ::1\nprotected-mode yes\nsave 3600 1 300 100 60 10000\nappendonly no\nrequirepass old-fixture\n' >"$conf"
  pandora_set_redis_password "$conf" "$PW" || fail "requirepass not replaced for $image"
  pandora_set_redis_block "$conf" "$bind" || fail "block not written for $image"
  # 再写一次不应有改动（幂等）
  if pandora_set_redis_password "$conf" "$PW"; then fail "requirepass rewritten although unchanged ($image)"; fi
  if pandora_set_redis_block "$conf" "$bind"; then fail "block rewritten although unchanged ($image)"; fi
  [ "$(grep -c '^requirepass ' "$conf")" = 1 ] || fail "more than one requirepass line ($image)"
  chmod 0644 "$conf"
  printf '  %s（%s，IPv6 %s）bind %s\n' "$image" "$(sed -n 's/.* v=\([^ ]*\).*/\1/p' <<<"$ver")" "$v6" "$bind"
  cid="$(docker run -d --network none "${sysctl[@]}" -v "$conf:/etc/pandora-test.conf:ro" "$image" redis-server /etc/pandora-test.conf)"
  cids+=("$cid")
  pong=""
  for i in $(seq 1 30); do
    [ "$(docker inspect -f '{{.State.Running}}' "$cid")" = true ] || break
    pong="$(docker exec -e REDISCLI_AUTH="$PW" "$cid" redis-cli ping 2>/dev/null || true)"
    [ "$pong" = PONG ] && break
    sleep 1
  done
  if [ "$pong" != PONG ]; then
    docker logs "$cid" 2>&1 | tail -5 | sed 's/^/    /' >&2
    return 1
  fi
  q() { docker exec -e REDISCLI_AUTH="$PW" "$cid" redis-cli "$@" 2>&1; }
  [ "$(docker exec "$cid" redis-cli ping 2>&1)" != PONG ] || fail "$image answers without the password"
  q flushall | grep -qi 'unknown command' || fail "$image still accepts FLUSHALL"
  q flushdb | grep -qi 'unknown command' || fail "$image still accepts FLUSHDB"
  [ "$(q config get save | sed -n 2p)" = '' ] || fail "$image still snapshots: $(q config get save)"
  [ "$(q config get appendonly | sed -n 2p)" = yes ] || fail "$image appendonly: $(q config get appendonly)"
  [ "$(q config get appendfsync | sed -n 2p)" = everysec ] || fail "$image appendfsync: $(q config get appendfsync)"
  [ "$(q config get maxmemory | sed -n 2p)" = 268435456 ] || fail "$image maxmemory: $(q config get maxmemory)"
  [ "$(q config get maxmemory-policy | sed -n 2p)" = noeviction ] || fail "$image maxmemory-policy: $(q config get maxmemory-policy)"
  [ "$(q config get bind | sed -n 2p)" = "$bind" ] || fail "$image bind: $(q config get bind), want $bind"
  docker rm -f "$cid" >/dev/null
  return 0
}

run_case redis:6.0.16 on || fail 'Redis 6.0 (Ubuntu 22.04) with IPv6 did not start'
run_case redis:6.0.16 off || fail 'Redis 6.0 (Ubuntu 22.04) without IPv6 did not start'
run_case redis:7.0.15 off || fail 'Redis 7.0 (Debian 12 / Ubuntu 24.04) did not start'
run_case redis:7.0.15 on || fail 'Redis 7.0 (Debian 12 / Ubuntu 24.04) with IPv6 did not start'
run_case redis:8.0.2 off || fail 'Redis 8.0 (Debian 13) did not start'
run_case redis:8.0.2 on || fail 'Redis 8.0 (Debian 13) with IPv6 did not start'
# 反证一：当初那一行在 Redis 6.0 上起不来
if run_case redis:6.0.16 on '127.0.0.1 -::1' 2>/dev/null; then
  fail 'Redis 6.0 started with "bind 127.0.0.1 -::1": this test cannot tell a bad bind line from a good one'
fi
# 反证二：--version 不以 Redis 开头的服务端（例如别的兼容实现冒名 redis-server）被拒绝
stub_version 'Other server v=8.1.1 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=0'
if ( PATH="$T/bin:$PATH"; pandora_check_redis_version ) >/dev/null 2>&1; then
  fail 'a non-Redis server passed the version check'
fi
stub_version 'Redis server v=5.0.14 sha=00000000:0 malloc=jemalloc-5.1.0 bits=64 build=0'
if ( PATH="$T/bin:$PATH"; pandora_check_redis_version ) >/dev/null 2>&1; then
  fail 'Redis 5.0 passed the minimum-version check'
fi

printf 'redis hardening versions (docker): PASS\n'
