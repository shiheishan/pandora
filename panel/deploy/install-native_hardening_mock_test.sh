#!/usr/bin/env bash
# 直装布局的 PostgreSQL 与 Valkey 加固，不需要 root、systemd：
#   ① drop-in 内容：两份都整套写全，不依赖发行版单元（Debian 12 的 redis 单元把 ProtectSystem 改回 true）；
#      内存上限与 docker-compose.yml 里的容器上限逐字一致（docker 布局改了，这里跟着红）；被挡的系统调用返回 EPERM；
#      PostgreSQL 不许带 MemoryDenyWriteExecute（超级用户会话的 JIT 要可写可执行内存），Valkey 必须带；
#      Valkey 的系统调用允许清单先清空再写，放行写的目录随 valkey / redis 走；
#   ② Valkey 配置块：与 docker-compose.yml 里 valkey 的启动参数同口径（禁 FLUSHALL / FLUSHDB、不落盘、内存上限与淘汰），
#      另加只听回环与保护模式；bind 按版本与 IPv6 写（Redis 6.0 不认「-」前缀：Ubuntu 22.04、Debian 12、Debian 13
#      三种都覆盖，真起一遍见 valkey-hardening_versions_docker_test.sh）；重复跑一字不变；不带口令，口令不进命令行参数；
#   ③ 应用与撤回：没变不重启；PostgreSQL 重启后等在线的时长按重启前 CHECKPOINT 的实测耗时给（不写死）；
#      起不来就把 drop-in / 配置还原成这次之前的样子（之前有的写回、没有的删掉），再核实在跑：在跑返回 1、提示照实写，
#      仍没起来就停下、也照实写；226/NAMESPACE 提示原因与开关；Valkey 分口令、配置块、drop-in 三步，只撤回失败的那一步；
#   ④ 开关 PANDORA_SYSTEMD_HARDENING：缺省开；0 去掉两份 drop-in；记进 .env 只改这一行；
#   ⑤ 静态：安装器在外来集群检查、迁移、收窄角色之后、起网关之前加固；失败时 --from-docker 立即停下，
#      首装与升级把服务按新版本起来之后再停下。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
NATIVE="$DEPLOY/install-native.sh"
LIB="$DEPLOY/install-native-lib.sh"
COMPOSE="$DEPLOY/docker-compose.yml"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-hardening.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-native hardening: %s\n' "$*" >&2; exit 1; }

mkdir -p "$T/bin"
# systemctl：单元「起不来」的条件——fail.restart 开着且单元带 pandora drop-in，或 fail.conf 里的模式出现在
# conf.path 指的 Valkey 配置里。postgresql@ 的 restart 一律返回 0（Debian 单元的 ExecStart 带「-」前缀），只能看 pg_lsclusters
cat >"$T/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'systemctl %s\n' "$*" >>"$root/calls"
broken() {
  { [ -f "$root/fail.restart" ] && [ -f "$root/systemd/$1.d/pandora-hardening.conf" ]; } && return 0
  [ -f "$root/fail.conf" ] && [ -f "$root/conf.path" ] && grep -qE -f "$root/fail.conf" "$(cat "$root/conf.path")"
}
case "$1" in
  restart) case "$2" in postgresql@*) exit 0 ;; esac; if broken "$2"; then exit 1; fi ;;
  is-active) if broken "$3"; then exit 3; fi ;;
  show) cat "$root/exec.status" 2>/dev/null || echo 0 ;;
esac
exit 0
MOCK
# pg_lsclusters：「起不来」开关开着且 drop-in 在、或 pg.down 在时报 down；每次调用记一行
cat >"$T/bin/pg_lsclusters" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
echo x >>"$root/lscalls"
status=online
if [ -f "$root/fail.restart" ] && [ -f "$root/systemd/postgresql@18-main.service.d/pandora-hardening.conf" ]; then status=down; fi
[ ! -f "$root/pg.down" ] || status=down
printf 'Ver Cluster Port Status Owner Data directory Log file\n18 main 5432 %s postgres /var/lib/postgresql/18/main /dev/null\n' "$status"
MOCK
cat >"$T/bin/journalctl" <<'MOCK'
#!/usr/bin/env bash
cat "$(cd "$(dirname "$0")/.." && pwd)/journal" 2>/dev/null || true
MOCK
# 服务端程序的 --version：内容取 version.<程序名>
for prog in valkey-server redis-server; do
  printf '#!/usr/bin/env bash\ncat "%s/version.%s" 2>/dev/null\n' "$T" "$prog" >"$T/bin/$prog"
done
printf '#!/usr/bin/env bash\nexit 0\n' >"$T/bin/sleep"
chmod 0755 "$T/bin/"*
export PATH="$T/bin:$PATH"
. "$DEPLOY/public-base-url.sh"
. "$LIB"
set -euo pipefail
NATIVE_SYSTEMD_DIR="$T/systemd"
# 重启前的 CHECKPOINT 记下来；date +%s 交替给 1000 / 1010：检查点「耗时」10 秒 → 等在线的预算 30 + 3×10 = 60 秒
native_pg_peer() { printf 'peer %s\n' "$*" >>"$T/peer.calls"; }
date() { if [ "${1:-}" = +%s ]; then n=$(( $(cat "$T/date.n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$T/date.n"; echo $(( n % 2 ? 1000 : 1010 )); else command date "$@"; fi; }
reset() { rm -rf "$T/systemd" "$T/fail.restart" "$T/fail.conf" "$T/pg.down" "$T/exec.status" "$T/journal"; : >"$T/calls"; : >"$T/lscalls"; : >"$T/peer.calls"; }
PG_DROPIN="$T/systemd/postgresql@18-main.service.d/pandora-hardening.conf"
VK_DROPIN="$T/systemd/valkey-server.service.d/pandora-hardening.conf"

# --- ① drop-in 内容 -------------------------------------------------------------------
pg="$(native_pg_hardening_dropin)"
common=('NoNewPrivileges=yes' 'CapabilityBoundingSet=' 'AmbientCapabilities=' \
  'PrivateTmp=yes' 'PrivateDevices=yes' 'ProtectHome=yes' 'ProtectSystem=strict' \
  'ProtectKernelTunables=yes' 'ProtectKernelModules=yes' 'ProtectKernelLogs=yes' 'ProtectControlGroups=yes' \
  'ProtectClock=yes' 'ProtectHostname=yes' 'ProtectProc=invisible' 'RestrictNamespaces=yes' 'RestrictRealtime=yes' \
  'RestrictSUIDSGID=yes' 'LockPersonality=yes' 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6' \
  'IPAddressDeny=any' 'IPAddressAllow=localhost' 'SystemCallArchitectures=native' 'SystemCallErrorNumber=EPERM')
for want in "${common[@]}" 'User=postgres' 'Group=postgres' \
    'ExecStartPre=+/usr/bin/install -d -m 2775 -o postgres -g postgres /run/postgresql' \
    'ReadWritePaths=/var/lib/postgresql -/var/log/postgresql /run/postgresql'; do
  grep -qxF "$want" <<<"$pg" || fail "PostgreSQL drop-in lacks: $want"
done
grep -q '^SystemCallFilter=~.*@mount.*@reboot' <<<"$pg" || fail 'PostgreSQL drop-in has no syscall deny list'
if grep -q '^MemoryDenyWriteExecute' <<<"$pg"; then fail 'PostgreSQL drop-in blocks W^X memory (breaks JIT for superuser sessions)'; fi
for flavor in valkey redis; do
  vk="$(native_valkey_hardening_dropin "$flavor")"
  for want in "${common[@]}" 'MemoryDenyWriteExecute=yes' "ReadWritePaths=-/var/lib/$flavor -/var/log/$flavor -/run/$flavor"; do
    grep -qxF "$want" <<<"$vk" || fail "$flavor drop-in lacks: $want"
  done
  # 允许清单：先清空（发行版单元里的不叠进来），再给 @system-service，最后挡 @privileged @resources
  [ "$(grep '^SystemCallFilter=' <<<"$vk")" = $'SystemCallFilter=\nSystemCallFilter=@system-service\nSystemCallFilter=~@privileged @resources' ] \
    || fail "$flavor drop-in syscall filter: $(grep '^SystemCallFilter=' <<<"$vk")"
done
vk="$(native_valkey_hardening_dropin valkey)"
# 内存上限与 docker 布局的容器上限一致
compose_limit() { awk -v svc="  $1:" '$0 == svc { p = 1; next } p && /^  [a-z]/ { p = 0 } p && /memory:/ { print $2; exit }' "$COMPOSE"; }
pg_limit="$(compose_limit postgres)"; vk_limit="$(compose_limit valkey)"
[ -n "$pg_limit" ] && [ -n "$vk_limit" ] || fail "cannot read the container memory limits from docker-compose.yml"
grep -qx "MemoryMax=$pg_limit" <<<"$pg" || fail "PostgreSQL MemoryMax differs from the container limit $pg_limit"
grep -qx "MemoryMax=$vk_limit" <<<"$vk" || fail "Valkey MemoryMax differs from the container limit $vk_limit"

# --- ② Valkey 配置块 ---------------------------------------------------------------------
# bind：按版本与 IPv6。IPv6 回环在 /proc/net/if_inet6 里是 lo 上的 ::1
printf '00000000000000000000000000000001 01 80 10 80       lo\n' >"$T/inet6.yes"
printf 'fe800000000000000000000000000001 02 40 20 80     eth0\n' >"$T/inet6.no"
bind_case() { # <程序> <--version 输出> <if_inet6> <应得>
  printf '%s\n' "$2" >"$T/version.$1"
  got="$(NATIVE_IF_INET6="$3" native_valkey_bind_addrs "$1")"
  [ "$got" = "$4" ] || fail "bind for $1 '$2' (inet6 $(basename "$3")): got '$got', want '$4'"
}
bind_case valkey-server 'Valkey server v=8.1.1 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=1' "$T/inet6.no" '127.0.0.1 -::1'   # Debian 13
bind_case redis-server 'Redis server v=7.0.15 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=1' "$T/inet6.no" '127.0.0.1 -::1'   # Debian 12
bind_case redis-server 'Redis server v=6.2.0 sha=00000000:0 malloc=jemalloc-5.1.0 bits=64 build=1' "$T/inet6.no" '127.0.0.1 -::1'
bind_case redis-server 'Redis server v=6.0.16 sha=00000000:0 malloc=jemalloc-5.2.1 bits=64 build=1' "$T/inet6.yes" '127.0.0.1 ::1'  # Ubuntu 22.04
bind_case redis-server 'Redis server v=6.0.16 sha=00000000:0 malloc=jemalloc-5.2.1 bits=64 build=1' "$T/inet6.no" '127.0.0.1'
bind_case redis-server 'Redis server v=6.0.16 sha=00000000:0 malloc=jemalloc-5.2.1 bits=64 build=1' "$T/missing" '127.0.0.1'
bind_case redis-server 'something unexpected' "$T/inet6.yes" '127.0.0.1 ::1'
rm -f "$T"/version.*

block="$(native_valkey_hardening_block '127.0.0.1 -::1')"
compose_vk="$(awk '/^  valkey:/ { p = 1; next } p && /^  [a-z]/ { p = 0 } p' "$COMPOSE")"
vk_arg() { awk -v k="- --$1" '$0 ~ "^ +" k "$" { getline; sub(/^ +- */, ""); print; exit }' <<<"$compose_vk"; }
[ "$(vk_arg maxmemory)" = 96mb ] && grep -qx 'maxmemory 96mb' <<<"$block" || fail "maxmemory differs from docker-compose.yml ($(vk_arg maxmemory))"
grep -qx "maxmemory-policy $(vk_arg maxmemory-policy)" <<<"$block" || fail 'maxmemory-policy differs from docker-compose.yml'
grep -qx 'appendonly no' <<<"$block" && grep -qx 'save ""' <<<"$block" || fail 'the block persists data while the container does not'
for cmd in FLUSHALL FLUSHDB; do
  grep -A1 -- '- --rename-command' <<<"$compose_vk" | grep -q -- "- $cmd" || fail "docker-compose.yml no longer renames $cmd (update this test and the block together)"
  grep -qx "rename-command $cmd \"\"" <<<"$block" || fail "the block does not disable $cmd"
done
grep -qx 'bind 127.0.0.1 -::1' <<<"$block" && grep -qx 'protected-mode yes' <<<"$block" || fail 'the block does not pin loopback and protected mode'
grep -qx 'bind 127.0.0.1' <<<"$(native_valkey_hardening_block 127.0.0.1)" || fail 'the bind line does not follow the given addresses'
if grep -qi 'requirepass' <<<"$block"; then fail 'the block carries the password'; fi
# 写进配置：其余行不动、块在末尾、重复跑一字不变、旧块被替换、口令不进命令行参数
conf="$T/valkey.conf"; printf '%s\n' "$conf" >"$T/conf.path"
printf 'port 6379\n# save 3600 1\nrequirepass vk-fixture-pw-aaaaaaaa\n\n' >"$conf"; chmod 0640 "$conf"
for tool in awk cat cmp mktemp; do
  real="$(command -v "$tool")"
  printf '#!/usr/bin/env bash\nprintf "%s %%s\\n" "$*" >>"%s/tools.argv"\nexec "%s" "$@"\n' "$tool" "$T" "$real" >"$T/bin/$tool"
  chmod 0755 "$T/bin/$tool"
done
hash -r
native_set_valkey_hardening "$conf" '127.0.0.1 -::1' || fail 'a new block was reported as unchanged'
for tool in awk cat cmp mktemp; do rm -f "$T/bin/$tool"; done; hash -r
grep -qx 'port 6379' "$conf" && grep -qx 'requirepass vk-fixture-pw-aaaaaaaa' "$conf" || fail 'other lines were changed'
[ "$(tail -n "$(grep -c . <<<"$block")" "$conf")" = "$block" ] || fail 'the block is not at the end of the file'
if grep -q 'vk-fixture-pw' "$T/tools.argv"; then fail 'the Valkey password reached a command line'; fi
cp "$conf" "$T/conf.once"
if native_set_valkey_hardening "$conf" '127.0.0.1 -::1'; then fail 'an unchanged block asked for a restart'; fi
cmp -s "$conf" "$T/conf.once" || fail 'a second run changed the file'
sed -i.bak 's/^maxmemory 96mb$/maxmemory 1gb/' "$conf"
native_set_valkey_hardening "$conf" '127.0.0.1 -::1' || fail 'a hand-edited block was not restored'
[ "$(grep -c "^$(printf '%s' "$NATIVE_VALKEY_BLOCK_BEGIN" | sed 's/[][\.*^$/]/\\&/g')$" "$conf")" -eq 1 ] || fail 'the block was duplicated'
grep -qx 'maxmemory 96mb' "$conf" && ! grep -qx 'maxmemory 1gb' "$conf" || fail 'the old block was not replaced'
mode="$(stat -c %a "$conf" 2>/dev/null || stat -f %Lp "$conf")"; [ "$mode" = 640 ] || fail "valkey.conf mode became $mode"

# --- ③ PostgreSQL：应用、没变不重启、等在线的预算、撤回 ------------------------------------------
native_apply_dropin demo.service 'x=1' || fail 'a new drop-in was reported as unchanged'
[ "$(cat "$T/systemd/demo.service.d/pandora-hardening.conf")" = 'x=1' ] || fail 'drop-in content'
if native_apply_dropin demo.service 'x=1'; then fail 'an unchanged drop-in asked for a restart'; fi
reset
( native_harden_pg_unit 18 5432 ) >"$T/out" 2>&1 || fail "PostgreSQL hardening failed: $(cat "$T/out")"
grep -qx 'systemctl restart postgresql@18-main.service' "$T/calls" || fail "PostgreSQL not restarted after hardening: $(cat "$T/calls")"
[ -f "$PG_DROPIN" ] || fail 'PostgreSQL drop-in not written'
grep -qx 'peer -p 5432 -d postgres -c CHECKPOINT' "$T/peer.calls" || fail "no CHECKPOINT before the restart: $(cat "$T/peer.calls")"
: >"$T/calls"; : >"$T/peer.calls"
( native_harden_pg_unit 18 5432 ) >/dev/null 2>&1 || fail 'an unchanged PostgreSQL drop-in failed'
if grep -q restart "$T/calls" || [ -s "$T/peer.calls" ]; then fail 'PostgreSQL restarted although the drop-in did not change'; fi
# 新 drop-in 起不来，撤回（删掉）后在线：返回 1，提示照实写「已还原…核实已在线」，原因提示带开关
reset; touch "$T/fail.restart"
if ( native_harden_pg_unit 18 5432 ) >"$T/out" 2>&1; then fail 'a PostgreSQL that would not start was accepted'; fi
grep -Fq '已还原成这次之前的样子，核实已在线' "$T/out" || fail "PostgreSQL revert message: $(cat "$T/out")"
grep -Fq 'PANDORA_SYSTEMD_HARDENING=0' "$T/out" || fail 'the revert message does not name the switch'
[ ! -f "$PG_DROPIN" ] || fail 'the failing PostgreSQL drop-in was left behind'
[ "$(grep -c 'systemctl restart postgresql@18-main.service' "$T/calls")" -eq 2 ] || fail "PostgreSQL not restarted without the drop-in: $(cat "$T/calls")"
# 等在线的预算按检查点实测：10 秒 → 60 次（不是写死的 15）
[ "$(grep -c . "$T/lscalls")" -eq 61 ] || fail "online wait budget is not derived from the checkpoint time: $(grep -c . "$T/lscalls") polls"
# 之前就有（内容不同的）drop-in：起不来时写回之前的内容，不是删掉
reset; mkdir -p "${PG_DROPIN%/*}"; printf 'old=1\n' >"$PG_DROPIN"; touch "$T/fail.restart"
if ( native_harden_pg_unit 18 5432 ) >"$T/out" 2>&1; then :; fi
[ "$(cat "$PG_DROPIN" 2>/dev/null)" = 'old=1' ] || fail "the previous PostgreSQL drop-in was not restored: $(cat "$PG_DROPIN" 2>/dev/null)"
# 撤回之后仍不在线：停下，提示照实写「仍没在线」，不说已恢复
reset; touch "$T/pg.down"
if ( native_harden_pg_unit 18 5432 ) >"$T/out" 2>&1; then fail 'a PostgreSQL still down after the revert was accepted'; fi
grep -Fq '仍没在线' "$T/out" || fail "message after a failed revert: $(cat "$T/out")"
if grep -Fq '核实已在线' "$T/out"; then fail 'claimed PostgreSQL is online although it is not'; fi
[ "$(grep -c . "$T/lscalls")" -eq 120 ] || fail "the revert was not checked with the same budget: $(grep -c . "$T/lscalls") polls"
# 226/NAMESPACE：说出原因与开关
reset; touch "$T/fail.restart"; echo 226 >"$T/exec.status"
( native_harden_pg_unit 18 5432 ) >"$T/out" 2>&1 || true
grep -Fq '226/NAMESPACE' "$T/out" && grep -Fq 'PANDORA_SYSTEMD_HARDENING=0' "$T/out" || fail "no 226/NAMESPACE hint: $(cat "$T/out")"
reset; touch "$T/fail.restart"; printf 'postgresql@18-main.service: Failed at step NAMESPACE spawning /usr/bin/pg_ctlcluster\n' >"$T/journal"
( native_harden_pg_unit 18 5432 ) >"$T/out" 2>&1 || true
grep -Fq '226/NAMESPACE' "$T/out" || fail "no 226/NAMESPACE hint from the journal: $(cat "$T/out")"
# 开关关着：去掉已有的 drop-in 并重启；本来就没有就什么都不做
reset; mkdir -p "${PG_DROPIN%/*}"; native_pg_hardening_dropin >"$PG_DROPIN"
( NATIVE_HARDENING=0; native_harden_pg_unit 18 5432 ) >"$T/out" 2>&1 || fail "switching PostgreSQL hardening off failed: $(cat "$T/out")"
[ ! -f "$PG_DROPIN" ] && grep -qx 'systemctl restart postgresql@18-main.service' "$T/calls" || fail 'the switch did not remove the PostgreSQL drop-in'
: >"$T/calls"
( NATIVE_HARDENING=0; native_harden_pg_unit 18 5432 ) >/dev/null 2>&1
if grep -q restart "$T/calls"; then fail 'restarted PostgreSQL although hardening was already off'; fi

# --- ③ Valkey：三步各自重启核实、只撤回失败的那一步 -------------------------------------------
printf 'Valkey server v=8.1.1 sha=0 malloc=jemalloc bits=64 build=1\n' >"$T/version.valkey-server"
reset; printf 'port 6379\nrequirepass old-pw-fixture\n' >"$conf"
( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >"$T/out" 2>&1 || fail "Valkey hardening failed: $(cat "$T/out")"
[ "$(grep -c 'systemctl restart valkey-server.service' "$T/calls")" -eq 3 ] || fail "Valkey not restarted once per changed step: $(cat "$T/calls")"
grep -qx 'requirepass new-pw-fixture' "$conf" && grep -qx 'bind 127.0.0.1 -::1' "$conf" && [ -f "$VK_DROPIN" ] || fail 'Valkey password, block or drop-in not applied'
[ "$(cat "$VK_DROPIN")" = "$(native_valkey_hardening_dropin valkey)" ] || fail 'valkey-server got the wrong drop-in'
printf 'Redis server v=6.0.16 sha=0 malloc=jemalloc bits=64 build=1\n' >"$T/version.redis-server"
( NATIVE_IF_INET6="$T/inet6.no"; native_harden_valkey redis-server "$conf" new-pw-fixture ) >/dev/null 2>&1 || fail 'redis-server hardening failed'
[ "$(cat "$T/systemd/redis-server.service.d/pandora-hardening.conf")" = "$(native_valkey_hardening_dropin redis)" ] || fail 'redis-server got the wrong drop-in'
grep -qx 'bind 127.0.0.1' "$conf" || fail 'Redis 6.0 without IPv6 did not get a plain IPv4 bind'
( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >/dev/null 2>&1 || true
: >"$T/calls"
( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >/dev/null 2>&1
if grep -q restart "$T/calls"; then fail 'Valkey restarted although nothing changed'; fi
if ls "$conf".pandora.* >/dev/null 2>&1; then fail 'a config snapshot (with the password) was left behind'; fi
# 配置块那一步起不来（如老 Redis 不认 bind 的「-」）：配置写回这一步之前的样子（口令那一步保留），核实在跑，返回 1
reset; printf 'port 6379\nrequirepass old-pw-fixture\n' >"$conf"
printf '^bind 127\\.0\\.0\\.1 -::1$\n' >"$T/fail.conf"
if ( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >"$T/out" 2>&1; then fail 'a Valkey that would not start with the new block was accepted'; fi
[ "$(cat "$conf")" = $'port 6379\nrequirepass new-pw-fixture' ] || fail "the config was not put back as it was before the block step: $(cat "$conf")"
grep -Fq '这一步已还原，核实 valkey-server 照原样在跑' "$T/out" || fail "Valkey revert message: $(cat "$T/out")"
[ ! -f "$VK_DROPIN" ] || fail 'went on to the drop-in after a failed step'
if ls "$conf".pandora.* >/dev/null 2>&1; then fail 'a config snapshot was left behind after a revert'; fi
# 写回之后仍起不来：停下，提示照实写
reset; printf 'port 6379\nrequirepass old-pw-fixture\n' >"$conf"
printf 'port 6379\n' >"$T/fail.conf"
if ( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >"$T/out" 2>&1; then fail 'a Valkey still down after the revert was accepted'; fi
grep -Fq '仍没起来' "$T/out" || fail "message after a failed Valkey revert: $(cat "$T/out")"
if grep -Fq '照原样在跑' "$T/out"; then fail 'claimed Valkey is running although it is not'; fi
[ "$(cat "$conf")" = $'port 6379\nrequirepass old-pw-fixture' ] || fail 'the config was not put back after the password step failed'
# drop-in 那一步起不来：之前的 drop-in 写回原内容
reset; printf 'port 6379\nrequirepass old-pw-fixture\n' >"$conf"
mkdir -p "${VK_DROPIN%/*}"; printf 'old=1\n' >"$VK_DROPIN"; touch "$T/fail.restart"
# 前两步时 drop-in 是旧的也算「起不来」：先让配置两步在没有 drop-in 的情况下过
mv "$VK_DROPIN" "$T/vk.old"
native_harden_valkey valkey-server "$conf" new-pw-fixture >/dev/null 2>&1 || true
reset; mkdir -p "${VK_DROPIN%/*}"; mv "$T/vk.old" "$VK_DROPIN"; touch "$T/fail.restart"
if ( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >"$T/out" 2>&1; then fail 'a Valkey that would not start with the new drop-in was accepted'; fi
[ "$(cat "$VK_DROPIN" 2>/dev/null)" = 'old=1' ] || fail "the previous Valkey drop-in was not restored: $(cat "$VK_DROPIN" 2>/dev/null)"
# 开关关着：去掉 drop-in（配置块照旧）
reset; mkdir -p "${VK_DROPIN%/*}"; native_valkey_hardening_dropin valkey >"$VK_DROPIN"
( NATIVE_HARDENING=0; native_harden_valkey valkey-server "$conf" new-pw-fixture ) >/dev/null 2>&1 || fail 'switching Valkey hardening off failed'
[ ! -f "$VK_DROPIN" ] && grep -qx 'bind 127.0.0.1 -::1' "$conf" || fail 'the switch did not remove only the Valkey drop-in'
rm -f "$T"/version.*

# --- ④ 开关：记进 .env，只改这一行 ----------------------------------------------------------
envf="$T/env"; printf 'AEGIS_ENV=production\nVALKEY_PASSWORD=x-fixture\n' >"$envf"; chmod 0600 "$envf"
( unset PANDORA_SYSTEMD_HARDENING; native_load_hardening_switch "$envf"; [ "$NATIVE_HARDENING" = 1 ] ) || fail 'hardening is not on by default'
[ "$(cat "$envf")" = $'AEGIS_ENV=production\nVALKEY_PASSWORD=x-fixture' ] || fail 'the default wrote to .env'
( PANDORA_SYSTEMD_HARDENING=0; native_load_hardening_switch "$envf" >/dev/null; [ "$NATIVE_HARDENING" = 0 ] ) || fail 'PANDORA_SYSTEMD_HARDENING=0 was ignored'
grep -qx 'PANDORA_SYSTEMD_HARDENING=0' "$envf" && head -2 "$envf" | cmp -s - <(printf 'AEGIS_ENV=production\nVALKEY_PASSWORD=x-fixture\n') || fail ".env not updated (or other lines changed): $(cat "$envf")"
( unset PANDORA_SYSTEMD_HARDENING; native_load_hardening_switch "$envf" >/dev/null; [ "$NATIVE_HARDENING" = 0 ] ) || fail 'the switch in .env was not honoured on the next run'
before="$(grep -v '^PANDORA_SYSTEMD_HARDENING=' "$envf")"
( PANDORA_SYSTEMD_HARDENING=on; native_load_hardening_switch "$envf"; [ "$NATIVE_HARDENING" = 1 ] ) || fail 'PANDORA_SYSTEMD_HARDENING=on was ignored'
[ "$(grep -c '^PANDORA_SYSTEMD_HARDENING=' "$envf")" -eq 1 ] && grep -qx 'PANDORA_SYSTEMD_HARDENING=1' "$envf" || fail "switch line not replaced: $(cat "$envf")"
[ "$(grep -v '^PANDORA_SYSTEMD_HARDENING=' "$envf")" = "$before" ] || fail 'switching back changed other .env lines'
mode="$(stat -c %a "$envf" 2>/dev/null || stat -f %Lp "$envf")"; [ "$mode" = 600 ] || fail ".env mode became $mode"
if ( PANDORA_SYSTEMD_HARDENING=maybe; native_load_hardening_switch "$envf" ) >/dev/null 2>&1; then fail 'accepted an unknown switch value'; fi

# --- ⑤ 静态：位置与失败处理 ------------------------------------------------------------------
awk '/^native_check_foreign_clusters / { f = NR } /^role_log="\$\(bash "\$INSTALL_DIR\/deploy\/bootstrap.sh"/ { b = NR }
     /pandora_run_migrations "\$MODE"/ { m = NR }
     /^native_load_hardening_switch "\$ENV_FILE"$/ { s = NR }
     /^native_harden_pg_unit "\$PG_VERSION" "\$PG_PORT" \|\| HARDEN_FAILED\+=" PostgreSQL"$/ { h = NR }
     /^\[\[ -z "\$VK_UNIT" \]\] \|\| native_harden_valkey "\$VK_UNIT" "\$VK_CONF" "\$VK_PASS" \|\| HARDEN_FAILED\+=" \$VK_UNIT"$/ { v = NR }
     /^say "\[5\/6\]/ { five = NR } /^  systemctl start "\$s"/ { st = NR }
     END { exit !(f && m && b && s && h && v && five && st && f < m && m < b && b < s && s < h && h < v && v < five && five < st) }' "$NATIVE" \
  || fail 'hardening is not after the foreign-cluster check, migrations and role setup, before the services start'
if grep -n 'native_harden_pg_unit\|native_harden_valkey' "$NATIVE" | grep -v 'HARDEN_FAILED+=' | grep -q .; then
  fail 'hardening is called somewhere else too'
fi
awk '/^if \[\[ -n "\$HARDEN_FAILED" && "\$MODE" = from-docker \]\]; then$/ { d = NR } /^say "\[5\/6\]/ { five = NR }
     END { exit !(d && five && d < five) }' "$NATIVE" || fail '--from-docker does not stop before switching units when hardening failed'
awk '/^  systemctl start "\$s"/ { st = NR } /^if \[\[ -n "\$HARDEN_FAILED" \]\]; then$/ { d = NR }
     END { exit !(st && d && st < d) }' "$NATIVE" || fail 'install/upgrade does not stop after starting the services when hardening failed'
if grep -Fq 'native_set_valkey_password "$VK_CONF"' "$NATIVE"; then fail 'the installer still sets the Valkey password outside native_harden_valkey'; fi

printf 'install-native hardening mock: PASS\n'
