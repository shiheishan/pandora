#!/usr/bin/env bash
# PostgreSQL 的调参与加固、Valkey 的加固，不需要 root、systemd：
#   ① drop-in 内容：两份都整套写全，不依赖发行版单元（Debian 12 的 redis 单元把 ProtectSystem 改回 true）；
#      内存上限在这里钉死（PostgreSQL MemoryMax=512M、Valkey MemoryMax=160M）；被挡的系统调用返回 EPERM；
#      PostgreSQL 不许带 MemoryDenyWriteExecute（超级用户会话的 JIT 要可写可执行内存），Valkey 必须带；
#      Valkey 的系统调用允许清单先清空再写，放行写的目录随 valkey / redis 走；
#   ② Valkey 配置块：钉死 maxmemory 96mb、allkeys-lru、禁 FLUSHALL / FLUSHDB、不落盘，另加只听回环与保护模式；
#      bind 按版本与 IPv6 写（Redis 6.0 不认「-」前缀：Ubuntu 22.04、Debian 12、Debian 13 三种都覆盖，真起一遍见
#      valkey-hardening_versions_docker_test.sh）；重复跑一字不变；旧块（块头说明文字不同）被整块换掉；
#      不带口令，口令不进命令行参数；最低版本：Redis 6.0、Valkey 任何版本，太老或认不出就停下；
#   ③ PostgreSQL：调参文件 conf.d/pandora.conf 与 drop-in 一起应用、只重启一次；文件内容与发布包逐字相同；
#      都没变不重启；等在线的时长按重启前 CHECKPOINT 的实测耗时给（不写死）；起不来就把调参文件与 drop-in
#      都还原成这次之前的样子（之前有的写回、没有的删掉），再核实在线：在线返回 1、提示照实写，仍没在线就停下、
#      也照实写；226/NAMESPACE 提示原因与开关；postgresql.conf 没有生效的 include_dir = 'conf.d' 就停下；
#      Valkey 分口令、配置块、drop-in 三步，只撤回失败的那一步；
#   ④ 开关 PANDORA_SYSTEMD_HARDENING：缺省开；0 去掉两份 drop-in（调参照旧）；记进 .env 只改这一行；
#   ⑤ 静态：版本核对在装包之后、建集群之前；include 核对在建集群之后、任何数据改动之前；调参与加固在外来集群
#      检查、迁移、收窄角色之后、起网关之前；失败时把服务按新版本起来之后再停下。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
INST="$DEPLOY/install.sh"
LIB="$DEPLOY/install-lib.sh"
TUNING="$DEPLOY/postgresql-pandora.conf"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-hardening.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install hardening: %s\n' "$*" >&2; exit 1; }

mkdir -p "$T/bin"
printf '%s\n' "$TUNING" >"$T/tuning.src"
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
# pg_lsclusters：报 down 的条件——「起不来」开关开着且 drop-in 在；pg.badconf 开着且 conf.d 里是发布包那份调参；
# 或 pg.down 在。每次调用记一行
cat >"$T/bin/pg_lsclusters" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
echo x >>"$root/lscalls"
status=online
if [ -f "$root/fail.restart" ] && [ -f "$root/systemd/postgresql@18-main.service.d/pandora-hardening.conf" ]; then status=down; fi
if [ -f "$root/pg.badconf" ] && cmp -s "$(cat "$root/tuning.src")" "$root/etc/18/main/conf.d/pandora.conf"; then status=down; fi
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
NATIVE_PG_ETC="$T/etc"
# 重启前的 CHECKPOINT 记下来；date +%s 交替给 1000 / 1010：检查点「耗时」10 秒 → 等在线的预算 30 + 3×10 = 60 秒
native_pg_peer() { printf 'peer %s\n' "$*" >>"$T/peer.calls"; }
date() { if [ "${1:-}" = +%s ]; then n=$(( $(cat "$T/date.n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$T/date.n"; echo $(( n % 2 ? 1000 : 1010 )); else command date "$@"; fi; }
reset() {
  rm -rf "$T/systemd" "$T/etc" "$T/fail.restart" "$T/fail.conf" "$T/pg.down" "$T/pg.badconf" "$T/exec.status" "$T/journal"
  : >"$T/calls"; : >"$T/lscalls"; : >"$T/peer.calls"
}
PG_DROPIN="$T/systemd/postgresql@18-main.service.d/pandora-hardening.conf"
PG_TUNED="$T/etc/18/main/conf.d/pandora.conf"
VK_DROPIN="$T/systemd/valkey-server.service.d/pandora-hardening.conf"
pg_restarts() { grep -c 'systemctl restart postgresql@18-main.service' "$T/calls" || true; }
apply_pg() { ( native_apply_pg_config 18 5432 "$TUNING" ) >"$T/out" 2>&1; }

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
# 内存上限：PostgreSQL 512M（shared_buffers 128MB 加连接与维护内存的余量）、Valkey 160M（maxmemory 96mb 加开销）
[ "$(grep -c '^MemoryMax=' <<<"$pg")" -eq 1 ] && grep -qx 'MemoryMax=512M' <<<"$pg" || fail "PostgreSQL MemoryMax: $(grep '^MemoryMax' <<<"$pg")"
[ "$(grep -c '^MemoryMax=' <<<"$vk")" -eq 1 ] && grep -qx 'MemoryMax=160M' <<<"$vk" || fail "Valkey MemoryMax: $(grep '^MemoryMax' <<<"$vk")"

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

# 最低版本：Redis 6.0、Valkey 任何版本；太老、认不出都停下，并说出最低版本
version_case() { # <程序> <--version 输出> <ok|refuse>
  printf '%s\n' "$2" >"$T/version.$1"
  if ( native_check_valkey_version "$1" ) >"$T/out" 2>&1; then got=ok; else got=refuse; fi
  [ "$got" = "$3" ] || fail "version check for '$2': got $got, want $3: $(cat "$T/out")"
  [ "$3" = ok ] || grep -Fq 'Redis 6.0' "$T/out" || fail "refusal for '$2' does not name the minimum: $(cat "$T/out")"
}
version_case redis-server 'Redis server v=5.0.14 sha=00000000:0 malloc=jemalloc-5.1.0 bits=64 build=1' refuse
version_case redis-server 'Redis server v=6.0.16 sha=00000000:0 malloc=jemalloc-5.2.1 bits=64 build=1' ok
version_case redis-server 'Redis server v=7.0.15 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=1' ok
version_case valkey-server 'Valkey server v=8.1.1 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=1' ok
version_case valkey-server 'Valkey server v=7.2.5 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=1' ok
version_case redis-server 'garbage' refuse
version_case valkey-server 'Valkey server v=unknown' refuse
version_case redis-server '' refuse
rm -f "$T"/version.*

block="$(native_valkey_hardening_block '127.0.0.1 -::1')"
grep -qx 'maxmemory 96mb' <<<"$block" || fail 'the block does not cap memory at 96mb'
grep -qx 'maxmemory-policy allkeys-lru' <<<"$block" || fail 'the block does not evict with allkeys-lru'
grep -qx 'appendonly no' <<<"$block" && grep -qx 'save ""' <<<"$block" || fail 'the block persists data'
for cmd in FLUSHALL FLUSHDB; do
  grep -qx "rename-command $cmd \"\"" <<<"$block" || fail "the block does not disable $cmd"
done
grep -qx 'bind 127.0.0.1 -::1' <<<"$block" && grep -qx 'protected-mode yes' <<<"$block" || fail 'the block does not pin loopback and protected mode'
grep -qx 'bind 127.0.0.1' <<<"$(native_valkey_hardening_block 127.0.0.1)" || fail 'the bind line does not follow the given addresses'
if grep -qi 'requirepass' <<<"$block"; then fail 'the block carries the password'; fi
if grep -qi 'docker\|install-native' <<<"$block"; then fail "the block header still talks about another layout: $(head -1 <<<"$block")"; fi
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
[ "$(grep -c '^# >>> pandora' "$conf")" -eq 1 ] || fail 'the block was duplicated'
grep -qx 'maxmemory 96mb' "$conf" && ! grep -qx 'maxmemory 1gb' "$conf" || fail 'the old block was not replaced'
mode="$(stat -c %a "$conf" 2>/dev/null || stat -f %Lp "$conf")"; [ "$mode" = 640 ] || fail "valkey.conf mode became $mode"
# 块头说明文字不同的旧块（更早的安装器写的）也认得出：整块换掉，不会叠出第二份 rename-command（Redis 会拒绝启动）
printf 'port 6379\n\n# >>> pandora（an older header）\nbind 127.0.0.1\nrename-command FLUSHALL ""\nmaxmemory 64mb\n# <<< pandora\n' >"$conf"
native_set_valkey_hardening "$conf" '127.0.0.1 -::1' || fail 'an old block was reported as unchanged'
[ "$(grep -c '^# >>> pandora' "$conf")" -eq 1 ] && [ "$(grep -c '^rename-command FLUSHALL' "$conf")" -eq 1 ] \
  && ! grep -qx 'maxmemory 64mb' "$conf" || fail "an old block with a different header was not replaced: $(cat "$conf")"

# --- ③ PostgreSQL：include 核对 ----------------------------------------------------------------
pgconf="$T/etc/18/main/postgresql.conf"
include_case() { # <postgresql.conf 里 include 那一行，空表示没有这一行> <ok|stop>
  rm -rf "$T/etc"; mkdir -p "${pgconf%/*}"
  printf "max_connections = 100\nshared_buffers = 128MB\n%s\n# Add settings for extensions here\n" "$1" >"$pgconf"
  if ( native_check_pg_conf_include 18 ) >"$T/out" 2>&1; then got=ok; else got=stop; fi
  [ "$got" = "$2" ] || fail "include line '$1': got $got, want $2: $(cat "$T/out")"
  [ "$2" = ok ] || grep -Fq "include_dir = 'conf.d'" "$T/out" || fail "stop message does not say what to restore: $(cat "$T/out")"
}
include_case "include_dir = 'conf.d'			# include files ending in '.conf' from" ok   # Debian 的原样
include_case "include_dir 'conf.d'" ok
include_case "include_dir = '$T/etc/18/main/conf.d/'" ok
include_case "#include_dir = 'conf.d'			# include files ending in '.conf' from" stop
include_case "include_dir = 'other.d'" stop
include_case "include_dir = 'conf.d' trailing" stop
include_case '' stop
rm -rf "$T/etc"
if ( native_check_pg_conf_include 18 ) >"$T/out" 2>&1; then fail 'a missing postgresql.conf was accepted'; fi

# --- ③ PostgreSQL：调参与 drop-in 一起应用、没变不重启、等在线的预算、撤回 ------------------------------
native_apply_dropin demo.service 'x=1' || fail 'a new drop-in was reported as unchanged'
[ "$(cat "$T/systemd/demo.service.d/pandora-hardening.conf")" = 'x=1' ] || fail 'drop-in content'
if native_apply_dropin demo.service 'x=1'; then fail 'an unchanged drop-in asked for a restart'; fi
# 全新：调参文件与 drop-in 都写上，只重启一次，重启前做 CHECKPOINT
reset
apply_pg || fail "PostgreSQL config failed: $(cat "$T/out")"
[ "$(pg_restarts)" -eq 1 ] || fail "PostgreSQL must restart exactly once for the tuning file and the drop-in: $(cat "$T/calls")"
[ -f "$PG_DROPIN" ] || fail 'PostgreSQL drop-in not written'
cmp -s "$TUNING" "$PG_TUNED" || fail 'conf.d/pandora.conf differs from deploy/postgresql-pandora.conf'
mode="$(stat -c %a "$PG_TUNED" 2>/dev/null || stat -f %Lp "$PG_TUNED")"; [ "$mode" = 644 ] || fail "conf.d/pandora.conf mode is $mode"
grep -qx 'peer -p 5432 -d postgres -c CHECKPOINT' "$T/peer.calls" || fail "no CHECKPOINT before the restart: $(cat "$T/peer.calls")"
if ls "$T/etc/18/main/conf.d/" | grep -v '^pandora\.conf$' | grep -q .; then fail "stray files left in conf.d: $(ls "$T/etc/18/main/conf.d/")"; fi
# 都没变：不重启、不做检查点
: >"$T/calls"; : >"$T/peer.calls"
apply_pg || fail 'an unchanged PostgreSQL config failed'
if grep -q restart "$T/calls" || [ -s "$T/peer.calls" ]; then fail 'PostgreSQL restarted although nothing changed'; fi
# 只有调参文件变了（手改过、或新版本改了参数）：整份覆盖回发布包那份，重启一次
printf 'max_connections = 500\n' >"$PG_TUNED"; : >"$T/calls"
apply_pg || fail "rewriting a changed tuning file failed: $(cat "$T/out")"
cmp -s "$TUNING" "$PG_TUNED" && [ "$(pg_restarts)" -eq 1 ] || fail 'a hand-edited tuning file was not overwritten with one restart'
# 新 drop-in 起不来，撤回后在线：返回 1，提示照实写「已还原…核实已在线」，原因提示带开关；调参文件与 drop-in 都删掉
reset; touch "$T/fail.restart"
if apply_pg; then fail 'a PostgreSQL that would not start was accepted'; fi
grep -Fq '已还原成这次之前的样子，核实已在线' "$T/out" || fail "PostgreSQL revert message: $(cat "$T/out")"
grep -Fq 'PANDORA_SYSTEMD_HARDENING=0' "$T/out" || fail 'the revert message does not name the switch'
[ ! -f "$PG_DROPIN" ] || fail 'the failing PostgreSQL drop-in was left behind'
[ ! -f "$PG_TUNED" ] || fail 'the tuning file was left behind although there was none before'
[ "$(pg_restarts)" -eq 2 ] || fail "PostgreSQL not restarted once more after the revert: $(cat "$T/calls")"
# 等在线的预算按检查点实测：10 秒 → 60 次（不是写死的 15）
[ "$(grep -c . "$T/lscalls")" -eq 61 ] || fail "online wait budget is not derived from the checkpoint time: $(grep -c . "$T/lscalls") polls"
# 之前就有（内容不同的）调参文件与 drop-in，新调参起不来：两样都写回之前的内容，不是删掉；核实在线后返回 1
reset; mkdir -p "${PG_DROPIN%/*}" "${PG_TUNED%/*}"; printf 'old=1\n' >"$PG_DROPIN"; printf 'old_tuning = 1\n' >"$PG_TUNED"; touch "$T/pg.badconf"
if apply_pg; then fail 'a PostgreSQL that would not start with the new tuning was accepted'; fi
[ "$(cat "$PG_TUNED" 2>/dev/null)" = 'old_tuning = 1' ] || fail "the previous tuning file was not restored: $(cat "$PG_TUNED" 2>/dev/null)"
[ "$(cat "$PG_DROPIN" 2>/dev/null)" = 'old=1' ] || fail "the previous PostgreSQL drop-in was not restored: $(cat "$PG_DROPIN" 2>/dev/null)"
grep -Fq '核实已在线' "$T/out" && grep -Fq "$PG_TUNED" "$T/out" || fail "the revert message does not mention the tuning file: $(cat "$T/out")"
[ "$(pg_restarts)" -eq 2 ] || fail "expected one restart to apply and one after the revert: $(cat "$T/calls")"
# 撤回之后仍不在线：停下，提示照实写「仍没在线」，不说已恢复
reset; touch "$T/pg.down"
if apply_pg; then fail 'a PostgreSQL still down after the revert was accepted'; fi
grep -Fq '仍没在线' "$T/out" || fail "message after a failed revert: $(cat "$T/out")"
if grep -Fq '核实已在线' "$T/out"; then fail 'claimed PostgreSQL is online although it is not'; fi
[ "$(grep -c . "$T/lscalls")" -eq 120 ] || fail "the revert was not checked with the same budget: $(grep -c . "$T/lscalls") polls"
[ ! -f "$PG_TUNED" ] && [ ! -f "$PG_DROPIN" ] || fail 'the new tuning file or drop-in was left behind after a failed revert'
# 226/NAMESPACE：说出原因与开关
reset; touch "$T/fail.restart"; echo 226 >"$T/exec.status"
apply_pg || true
grep -Fq '226/NAMESPACE' "$T/out" && grep -Fq 'PANDORA_SYSTEMD_HARDENING=0' "$T/out" || fail "no 226/NAMESPACE hint: $(cat "$T/out")"
reset; touch "$T/fail.restart"; printf 'postgresql@18-main.service: Failed at step NAMESPACE spawning /usr/bin/pg_ctlcluster\n' >"$T/journal"
apply_pg || true
grep -Fq '226/NAMESPACE' "$T/out" || fail "no 226/NAMESPACE hint from the journal: $(cat "$T/out")"
# 开关关着：去掉已有的 drop-in 并重启（调参照旧装上）；两样都已是这样就什么都不做
reset; mkdir -p "${PG_DROPIN%/*}" "${PG_TUNED%/*}"; native_pg_hardening_dropin >"$PG_DROPIN"; cp "$TUNING" "$PG_TUNED"
( NATIVE_HARDENING=0; native_apply_pg_config 18 5432 "$TUNING" ) >"$T/out" 2>&1 || fail "switching PostgreSQL hardening off failed: $(cat "$T/out")"
[ ! -f "$PG_DROPIN" ] && [ "$(pg_restarts)" -eq 1 ] || fail 'the switch did not remove the PostgreSQL drop-in'
cmp -s "$TUNING" "$PG_TUNED" || fail 'the switch removed the tuning file'
: >"$T/calls"
( NATIVE_HARDENING=0; native_apply_pg_config 18 5432 "$TUNING" ) >/dev/null 2>&1
if grep -q restart "$T/calls"; then fail 'restarted PostgreSQL although hardening was already off and the tuning unchanged'; fi
# 开关关着、调参是新的：只写调参，不写 drop-in
reset
( NATIVE_HARDENING=0; native_apply_pg_config 18 5432 "$TUNING" ) >/dev/null 2>&1 || fail 'tuning with hardening off failed'
cmp -s "$TUNING" "$PG_TUNED" && [ ! -f "$PG_DROPIN" ] && [ "$(pg_restarts)" -eq 1 ] || fail 'hardening off: tuning not applied alone with one restart'

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
# 版本核对在装完包之后、建集群之前；include 核对在建集群之后、建库角色与写 .env 之前（两处停下时都还没改数据与配置）
awk '/apt-get install -y -qq valkey-server/ { pkg = NR } /^native_check_valkey_version "\$VK_UNIT"$/ { v = NR }
     /^native_ensure_pg_cluster "\$PG_VERSION"$/ { c = NR } /^native_check_pg_conf_include "\$PG_VERSION"$/ { i = NR }
     /^say "\[2\/6\]/ { two = NR } /^native_pg_peer -p "\$PG_PORT" -d postgres >\/dev\/null <<SQL/ { sql = NR }
     /^cat > "\$INSTALL_DIR\/deploy\/\.env" <<EOF$/ { env = NR }
     END { exit !(pkg && v && c && i && two && sql && env && pkg < v && v < c && c < i && i < two && two < sql && sql < env) }' "$INST" \
  || fail 'the Valkey version and the conf.d include are not checked between the package install and the first change'
awk '/^native_check_foreign_clusters / { f = NR } /^role_log="\$\(bash "\$INSTALL_DIR\/deploy\/bootstrap.sh"/ { b = NR }
     /pandora_run_migrations "\$MODE"/ { m = NR }
     /^native_load_hardening_switch "\$ENV_FILE"$/ { s = NR }
     /^native_apply_pg_config "\$PG_VERSION" "\$PG_PORT" "\$SCRIPT_DIR\/postgresql-pandora.conf" \|\| HARDEN_FAILED\+=" PostgreSQL"$/ { h = NR }
     /^native_harden_valkey "\$VK_UNIT" "\$VK_CONF" "\$VK_PASS" \|\| HARDEN_FAILED\+=" \$VK_UNIT"$/ { v = NR }
     /^say "\[5\/6\]/ { five = NR } /^  systemctl start "\$s"/ { st = NR }
     END { exit !(f && m && b && s && h && v && five && st && f < m && m < b && b < s && s < h && h < v && v < five && five < st) }' "$INST" \
  || fail 'tuning and hardening are not after the foreign-cluster check, migrations and role setup, before the services start'
if grep -n 'native_apply_pg_config\|native_harden_valkey' "$INST" | grep -v 'HARDEN_FAILED+=' | grep -q .; then
  fail 'tuning or hardening is called somewhere else too'
fi
awk '/^  systemctl start "\$s"/ { st = NR } /^if \[\[ -n "\$HARDEN_FAILED" \]\]; then$/ { d = NR }
     END { exit !(st && d && st < d) }' "$INST" || fail 'install/upgrade does not stop after starting the services when hardening failed'
if grep -Fq 'native_set_valkey_password "$VK_CONF"' "$INST"; then fail 'the installer still sets the Valkey password outside native_harden_valkey'; fi
# 调参走 conf.d，不走 ALTER SYSTEM（postgresql.auto.conf 留给运维）
if grep -v '^[[:space:]]*#' "$INST" "$LIB" | grep -qi 'ALTER SYSTEM'; then fail 'the installer tunes PostgreSQL with ALTER SYSTEM'; fi
# 调参文件进发布包（拷贝与 0644 数据文件两处），安装器动手之前就点名核对它在
grep -Fq 'cp "$ROOT/deploy/postgresql-pandora.conf" "$target/deploy/postgresql-pandora.conf"' "$DEPLOY/build-release.sh" \
  || fail 'build-release.sh does not copy postgresql-pandora.conf'
awk '/^  release_data=\(/{p=1} p{print} p&&/^  \)/{exit}' "$DEPLOY/build-release.sh" | grep -Fq '"$target_base/deploy/postgresql-pandora.conf"' \
  || fail 'build-release.sh does not archive postgresql-pandora.conf as a 0644 data file'
grep -Fq 'for f in postgresql-pandora.conf ' "$INST" || fail 'install.sh does not check for postgresql-pandora.conf before it starts'

printf 'install hardening mock: PASS\n'
