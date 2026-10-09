#!/usr/bin/env bash
# 占用类部署参数的静态守卫（w12deploy）：systemd 单元的 CPU 策略、compose 的开关与健康检查间隔。
# 不需要数据库和 root。
set -euo pipefail

DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'deploy-params: %s\n' "$*" >&2; exit 1; }
# 只看指令，不看注释
active() { grep -vE '^[[:space:]]*#' "$1"; }

# 门户网关：不设 CPU 硬配额，只设争抢时的权重；内存上限不变。
# 口令哈希闸门缺省并发 2（platform/config）以此为前提，加回 CPUQuota 要同时把闸门压回 1
public="$DIR/systemd/aegis-public.service"
active "$public" | grep -Eq '^CPUWeight=[0-9]+$' || fail 'aegis-public.service: CPUWeight missing'
if active "$public" | grep -Eq '^CPUQuota'; then fail 'aegis-public.service: CPUQuota is back (the hash gate default assumes none)'; fi
active "$public" | grep -Eq '^MemoryMax=256M$' || fail 'aegis-public.service: MemoryMax changed'
w="$(active "$public" | sed -n 's/^CPUWeight=//p')"
(( w >= 1 && w <= 10000 )) || fail "aegis-public.service: CPUWeight $w out of range"

# 其余两个网关这一波不动，仍是硬配额
for unit in aegis-admin aegis-node; do
  active "$DIR/systemd/$unit.service" | grep -Eq '^CPUQuota=[0-9]+%$' || fail "$unit.service lost its CPUQuota"
done

# compose：shared_buffers 缺省 128MB、可由 .env 的 PG_SHARED_BUFFERS 覆盖（降 64MB 要先同机 A/B）
compose="$DIR/docker-compose.yml"
grep -Fq 'shared_buffers=${PG_SHARED_BUFFERS:-128MB}' "$compose" || fail 'compose: shared_buffers is not switchable with a 128MB default'
grep -Eq '^PG_SHARED_BUFFERS=$' "$DIR/.env.example" || fail '.env.example: PG_SHARED_BUFFERS must exist and default to empty'

# healthcheck 每次是一次 docker exec：间隔不得短于 60 秒
intervals="$(grep -E '^[[:space:]]+interval:' "$compose" | grep -Eo '[0-9]+' || true)"
[[ "$(wc -l <<<"$intervals")" -eq 2 ]] || fail 'compose: want exactly two healthcheck intervals'
while read -r secs; do
  (( secs >= 60 )) || fail "compose: healthcheck interval ${secs}s is shorter than 60s"
done <<<"$intervals"

# plan_cache_mode 与口令哈希并发的环境变量在样例里都有，缺省留空
for key in AEGIS_PUBLIC_DB_PLAN_CACHE_MODE AEGIS_ADMIN_DB_PLAN_CACHE_MODE AEGIS_NODE_DB_PLAN_CACHE_MODE AEGIS_PASSWORD_HASH_CONCURRENCY; do
  grep -Eq "^${key}=\$" "$DIR/.env.example" || fail ".env.example: $key must exist and default to empty"
done

printf 'deploy-params tests passed\n'
