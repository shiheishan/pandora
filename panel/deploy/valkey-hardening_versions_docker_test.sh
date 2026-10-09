#!/usr/bin/env bash
# 直装写进 Valkey / Redis 配置的 pandora 块，在平台声称支持的几个版本上真起一遍（要 Docker，只在 GitHub 上跑）：
#   Redis 6.0（Ubuntu 22.04 的版本，不认 bind 的「-」前缀）回环有 IPv6 与没有两种、Redis 7.0（Debian 12）、
#   Valkey 8.1（Debian 13）。bind 那一行由 install-native-lib.sh 的 native_valkey_bind_addrs 按容器里的版本与
#   /proc/net/if_inet6 生成，配置块由 native_set_valkey_hardening 写进一份 Debian 风格的配置；起来之后核：
#   口令生效、FLUSHALL / FLUSHDB 已禁、不落盘、maxmemory 96mb 与 allkeys-lru。
#   反证：Redis 6.0 硬写 `bind 127.0.0.1 -::1` 必须起不来（证明这个测试抓得到当初的问题）。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-valkey-versions.XXXXXX")"
cids=()
cleanup() { [ "${#cids[@]}" -eq 0 ] || docker rm -f "${cids[@]}" >/dev/null 2>&1 || true; rm -rf -- "$T"; }
trap cleanup EXIT
fail() { printf 'valkey versions: %s\n' "$*" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || fail 'needs docker'

. "$DEPLOY/install-native-lib.sh"
set -euo pipefail
PW=vk-versions-fixture

# <镜像> <服务端> <命令行客户端> <IPv6：on|off> [硬写的 bind]：起来返回 0 并核行为；起不来返回 1
run_case() {
  local image="$1" server="$2" cli="$3" v6="$4" force_bind="${5:-}" ver bind conf cid i pong sysctl
  sysctl=(--sysctl net.ipv6.conf.all.disable_ipv6=1)
  [ "$v6" = off ] || sysctl=(--sysctl net.ipv6.conf.all.disable_ipv6=0 --sysctl net.ipv6.conf.lo.disable_ipv6=0)
  ver="$(docker run --rm "$image" "$server" --version)"
  docker run --rm --network none "${sysctl[@]}" "$image" cat /proc/net/if_inet6 >"$T/if_inet6" 2>/dev/null || : >"$T/if_inet6"
  if [ "$v6" = on ]; then
    grep -q '^00000000000000000000000000000001 .* lo$' "$T/if_inet6" || fail "no IPv6 loopback in the $image container although IPv6 was enabled"
  fi
  mkdir -p "$T/bin"
  printf '#!/usr/bin/env bash\nprintf "%%s\\n" %q\n' "$ver" >"$T/bin/$server"; chmod 0755 "$T/bin/$server"
  bind="$(PATH="$T/bin:$PATH" NATIVE_IF_INET6="$T/if_inet6" native_valkey_bind_addrs "$server")"
  [ -z "$force_bind" ] || bind="$force_bind"
  conf="$T/$server-$v6.conf"
  # Debian 包配置里与块相冲的几行：块在末尾，应以块为准
  printf 'port 6379\nbind 127.0.0.1 ::1\nprotected-mode yes\nsave 3600 1 300 100 60 10000\nappendonly no\nrequirepass %s\n' "$PW" >"$conf"
  native_set_valkey_hardening "$conf" "$bind" || fail "block not written for $image"
  chmod 0644 "$conf"
  printf '  %s（%s，IPv6 %s）bind %s\n' "$image" "$(sed -n 's/.* v=\([^ ]*\).*/\1/p' <<<"$ver")" "$v6" "$bind"
  cid="$(docker run -d --network none "${sysctl[@]}" -v "$conf:/etc/pandora-test.conf:ro" "$image" "$server" /etc/pandora-test.conf)"
  cids+=("$cid")
  pong=""
  for i in $(seq 1 30); do
    [ "$(docker inspect -f '{{.State.Running}}' "$cid")" = true ] || break
    pong="$(docker exec "$cid" "$cli" -a "$PW" --no-auth-warning ping 2>/dev/null || true)"
    [ "$pong" = PONG ] && break
    sleep 1
  done
  if [ "$pong" != PONG ]; then
    docker logs "$cid" 2>&1 | tail -5 | sed 's/^/    /' >&2
    return 1
  fi
  q() { docker exec "$cid" "$cli" -a "$PW" --no-auth-warning "$@" 2>&1; }
  [ "$(docker exec "$cid" "$cli" ping 2>&1)" != PONG ] || fail "$image answers without the password"
  q flushall | grep -qi 'unknown command' || fail "$image still accepts FLUSHALL"
  q flushdb | grep -qi 'unknown command' || fail "$image still accepts FLUSHDB"
  [ "$(q config get save | sed -n 2p)" = '' ] || fail "$image still saves: $(q config get save)"
  [ "$(q config get maxmemory | sed -n 2p)" = 100663296 ] || fail "$image maxmemory: $(q config get maxmemory)"
  [ "$(q config get maxmemory-policy | sed -n 2p)" = allkeys-lru ] || fail "$image maxmemory-policy: $(q config get maxmemory-policy)"
  [ "$(q config get bind | sed -n 2p)" = "$bind" ] || fail "$image bind: $(q config get bind), want $bind"
  docker rm -f "$cid" >/dev/null
  return 0
}

run_case redis:6.0.16 redis-server redis-cli on || fail 'Redis 6.0 (Ubuntu 22.04) with IPv6 did not start'
run_case redis:6.0.16 redis-server redis-cli off || fail 'Redis 6.0 (Ubuntu 22.04) without IPv6 did not start'
run_case redis:7.0.15 redis-server redis-cli off || fail 'Redis 7.0 (Debian 12) did not start'
run_case redis:7.0.15 redis-server redis-cli on || fail 'Redis 7.0 (Debian 12) with IPv6 did not start'
run_case valkey/valkey:8.1.1 valkey-server valkey-cli off || fail 'Valkey 8.1 (Debian 13) did not start'
run_case valkey/valkey:8.1.1 valkey-server valkey-cli on || fail 'Valkey 8.1 (Debian 13) with IPv6 did not start'
# 反证：当初那一行在 Redis 6.0 上起不来
if run_case redis:6.0.16 redis-server redis-cli on '127.0.0.1 -::1' 2>/dev/null; then
  fail 'Redis 6.0 started with "bind 127.0.0.1 -::1": this test cannot tell a bad bind line from a good one'
fi

printf 'valkey hardening versions (docker): PASS\n'
